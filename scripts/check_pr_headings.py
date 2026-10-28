"""Check Japanese-based H1 headings in a main pull request's body."""

import argparse
import importlib.util
from html import escape
from html.parser import HTMLParser
import json
from pathlib import Path
import re
import sys
import string
import unicodedata


# -I deliberately excludes the script directory from sys.path. Load only the
# trusted sibling checker, never a module selected by the PR body or working dir.
SPEC = importlib.util.spec_from_file_location(
    'commit_message_policy', Path(__file__).resolve().with_name('check_commit_messages.py'))
POLICY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(POLICY)

HTML_BLOCK_TAGS = (
    'address article aside base basefont blockquote body caption center col colgroup dd details '
    'dialog dir div dl dt fieldset figcaption figure footer form frame frameset h1 h2 h3 h4 h5 h6 '
    'head header hr html iframe legend li link main menu menuitem nav noframes ol optgroup option '
    'p param search section summary table tbody td tfoot th thead title tr track ul'
).split()
HTML_BLOCK = re.compile(r'^ {0,3}</?(?:' + '|'.join(HTML_BLOCK_TAGS) + r')(?=[ \t>]|/>|$)', re.IGNORECASE)
ATTRIBUTE = r'''[A-Za-z_:][A-Za-z0-9_.:-]*(?:[ \t]*=[ \t]*(?:[^ \t"'=<>`]+|'[^']*'|"[^"]*"))?'''
COMPLETE_HTML_TAG = re.compile(
    r' {0,3}(?:<[A-Za-z][A-Za-z0-9-]*(?:[ \t]+' + ATTRIBUTE +
    r')*[ \t]*/?>|</[A-Za-z][A-Za-z0-9-]*[ \t]*>)[ \t]*')
NON_PARAGRAPH = re.compile(
    r'^ {0,3}(?:>|[-+*](?:[ \t]|$)|[0-9]{1,9}[.)](?:[ \t]|$)|\[[^\]]+\]:)')
THEMATIC_BREAK = re.compile(r'^ {0,3}(?:(?:\*[ \t]*){3,}|(?:_[ \t]*){3,}|(?:-[ \t]*){3,})$')


def html_block_end(line, allow_generic=False):
    for start, end in ((r'<(?:pre|script|style|textarea)(?=[ \t>]|$)',
                        r'</(?:pre|script|style|textarea)>'),
                       (r'<!--', r'-->'), (r'<\?', r'\?>'),
                       (r'<![A-Za-z]', r'>'), (r'<!\[CDATA\[', r'\]\]>')):
        if re.match(r'^ {0,3}' + start, line, re.IGNORECASE):
            return re.compile(end, re.IGNORECASE)
    if HTML_BLOCK.match(line) or (allow_generic and COMPLETE_HTML_TAG.fullmatch(line)):
        return re.compile(r'^[ \t]*$')
    return None


def inline_html_source(text, keep_code=False):
    """Protect Markdown escapes/code before scanning for actual HTML tags."""
    offset = 0
    output = []
    while offset < len(text):
        if text[offset] == '\\' and offset + 1 < len(text) and text[offset + 1] in string.punctuation:
            output.append(escape(text[offset + 1]))
            offset += 2
        elif text[offset] == '`':
            opening = re.match(r'`+', text[offset:])[0]
            end = offset + len(opening)
            closing = re.search(r'(?<!`)`{' + str(len(opening)) + r'}(?!`)', text[end:])
            if closing is None:
                output.append(opening)
                offset = end
                continue
            stop = end + closing.end()
            if keep_code:
                output.append(opening + escape(text[end:end + closing.start()]) + opening)
            else:
                output.append(re.sub(r'[^\n]', ' ', text[offset:stop]))
            offset = stop
        else:
            output.append(text[offset])
            offset += 1
    return ''.join(output)


class HTMLHeadings(HTMLParser):
    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.headings = []
        self.current = None
        self.start_line = None
        self.hidden = None
        self.text = []

    def finish_heading(self):
        if self.current is not None:
            self.headings.append((self.start_line, ''.join(self.current)))
            self.current = None

    def handle_starttag(self, tag, attrs):
        if self.hidden:
            return
        if tag in ('script', 'style', 'textarea', 'title', 'xmp', 'iframe', 'noembed', 'noframes'):
            self.hidden = tag
        elif tag == 'h1':
            self.finish_heading()
            self.current = []
            self.start_line = self.getpos()[0]

    def handle_endtag(self, tag):
        if self.hidden:
            if tag == self.hidden:
                self.hidden = None
        elif tag == 'h1':
            self.finish_heading()

    def handle_data(self, data):
        if not self.hidden:
            self.text.append(data)
            if self.current is not None:
                self.current.append(data)


def markdown_heading_text(text):
    parser = HTMLHeadings()
    parser.feed(inline_html_source(text, keep_code=True))
    parser.close()
    return ''.join(parser.text)


class MarkdownBlock:
    """Leaf-block state belongs to one quote/list container, including fences."""

    def __init__(self):
        self.fence = None
        self.html_end = None
        self.paragraph = []
        self.raw = []

    def html_headings(self):
        source = []
        chunk = []
        markdown = None
        for _, line, mode in self.raw:
            if markdown is not None and mode != markdown:
                source.append(inline_html_source('\n'.join(chunk)) if markdown else '\n'.join(chunk))
                chunk = []
            markdown = mode
            chunk.append(line)
        source.append(inline_html_source('\n'.join(chunk)) if markdown else '\n'.join(chunk))
        parser = HTMLHeadings()
        parser.feed('\n'.join(source))
        parser.close()
        parser.finish_heading()
        return [(self.raw[line - 1][0], text) for line, text in parser.headings]

    def feed(self, line, number):
        if self.html_end:
            self.raw.append((number, line, False))
            if self.html_end.search(line):
                self.html_end = None
            return []
        marker = re.match(r'^ {0,3}(`{3,}|~{3,})(.*)$', line)
        if self.fence:
            if marker and marker[1][0] == self.fence[0] and len(marker[1]) >= len(self.fence) and not marker[2].strip(' \t'):
                self.fence = None
            return []
        if marker and (marker[1][0] != '`' or '`' not in marker[2]):
            self.fence = marker[1]
            self.paragraph = []
            return []
        if not self.paragraph and line.startswith('    '):
            return []
        self.html_end = html_block_end(line, allow_generic=not self.paragraph)
        self.raw.append((number, line, self.html_end is None))
        if self.html_end:
            self.paragraph = []
            if self.html_end.search(line):
                self.html_end = None
            return []
        heading = re.match(r'^ {0,3}(#{1,6})(?:[ \t]+(.*)|$)', line)
        if heading:
            self.paragraph = []
            return [(number, markdown_heading_text(heading[2] or ''))] if len(heading[1]) == 1 else []
        if self.paragraph and re.fullmatch(r' {0,3}=+[ \t]*', line):
            heading = (self.paragraph[0][0], markdown_heading_text('\n'.join(text for _, text in self.paragraph)))
            self.paragraph = []
            return [heading]
        if not line.strip(' \t') or re.fullmatch(r' {0,3}-+[ \t]*', line) or THEMATIC_BREAK.fullmatch(line):
            self.paragraph = []
        elif not self.paragraph and NON_PARAGRAPH.match(line):
            self.paragraph = []
        elif self.paragraph or not line.startswith('    '):
            self.paragraph.append((number, line.strip(' \t')))
        return []


def interrupts_paragraph(line):
    return (not line.strip(' \t') or NON_PARAGRAPH.match(line) or THEMATIC_BREAK.fullmatch(line)
            or re.match(r'^ {0,3}(?:#{1,6}(?:[ \t]|$)|`{3,}|~{3,})', line)
            or html_block_end(line))


def headings_in(body):
    # A list entry records its content indentation, rather than stripping an
    # arbitrary number of spaces that could turn indented code into a heading.
    stack = [('root', 0, MarkdownBlock())]
    blocks = [stack[0][2]]
    headings = []
    for number, source in enumerate(body.replace('\r\n', '\n').replace('\r', '\n').split('\n')):
        line = source.expandtabs(4)
        matched = 1
        for kind, width, _ in stack[1:]:
            quote = re.match(r'^ {0,3}> ?', line) if kind == 'quote' else None
            if quote:
                line = line[quote.end():]
            elif kind == 'list' and (not line.strip(' \t') or line.startswith(' ' * width)):
                line = line[width:]
            else:
                break
            matched += 1
        if matched < len(stack):
            # Lazy lines remain part of the paragraph if a later explicitly
            # marked Setext underline turns that paragraph into a heading.
            # They cannot themselves introduce a new block.
            if stack[-1][2].paragraph and not interrupts_paragraph(line):
                stack[-1][2].paragraph.append((number, line.strip(' \t')))
                stack[-1][2].raw.append((number, line, True))
                continue
            del stack[matched:]
        block = stack[-1][2]
        while not block.fence and not block.html_end:
            quote = re.match(r'^ {0,3}> ?', line)
            item = re.match(r'^( {0,3})([-+*]|[0-9]{1,9}[.)])( +|$)', line)
            if item and block.paragraph and (not line[item.end():].strip(' \t')
                                            or (item[2][0].isdigit() and item[2][:-1] != '1')):
                item = None
            if quote:
                kind, width, line = 'quote', 0, line[quote.end():]
            elif item and not THEMATIC_BREAK.fullmatch(line):
                spacing = len(item[3])
                width = len(item[1]) + len(item[2]) + (spacing if 1 <= spacing <= 4 else 1)
                kind, line = 'list', line[min(width, len(line)):]
            else:
                break
            block.paragraph = []
            block = MarkdownBlock()
            blocks.append(block)
            stack.append((kind, width, block))
        headings.extend(block.feed(line, number))
    for block in blocks:
        headings.extend(block.html_headings())
    return [text for _, text in sorted(headings, key=lambda item: item[0])]


def check_body(body):
    headings = list(headings_in(body))
    failures = []
    for index, heading in enumerate(headings, 1):
        text = unicodedata.normalize('NFKC', heading)
        # Reuse the commit policy's limited technical exclusions, without its
        # block-fence handling: these strings are already heading content.
        text = re.sub(r'`([^`\n]+)`', lambda match: '' if match[1].isascii() and not any(
            char.isspace() for char in match[1]) else match[1], text)
        for pattern in (POLICY.URL, POLICY.PATH, POLICY.FILE, POLICY.SHA, POLICY.TECHNICAL):
            text = pattern.sub('', text)
        letters = [char for char in text if unicodedata.category(char).startswith('L')]
        japanese = sum(unicodedata.name(char, '').startswith(POLICY.JAPANESE_NAMES) for char in letters)
        if japanese < 1 or japanese * 3 < len(letters):
            failures.append(f'h1={index} japanese={japanese} letters={len(letters)}; need >=1 and >=1/3')
    if failures:
        print('PR_HEADING_FAIL ' + ' | '.join(failures), file=sys.stderr)
        return 1
    print(f'PR_HEADING_PASS checked={len(headings)}')
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--event', type=Path, required=True)
    args = parser.parse_args()
    try:
        event = json.loads(args.event.read_text(encoding='utf-8'))
        pr = event['pull_request']
        if pr['base']['ref'] != 'main':
            raise ValueError
        body = pr['body']
        if body is None:
            body = ''
        if not isinstance(body, str):
            raise ValueError
        return check_body(body)
    except AssertionError:
        # HTMLParser can include raw input in errors for malformed declarations.
        print('PR_HEADING_FAIL: Unsupported HTML input', file=sys.stderr)
        return 1
    except (OSError, UnicodeError, ValueError, KeyError, TypeError, AttributeError):
        print('PR_HEADING_FAIL: Invalid UTF-8 main pull_request event', file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
