"""Check Japanese-based commit subjects and explanatory bodies, not PR text."""

import argparse
import json
from pathlib import Path
import re
import subprocess
import sys
import unicodedata


PREFIX = re.compile(r'^(?:feat|fix|docs|test|refactor|perf|build|ci|style|revert|chore)(?:\([A-Za-z0-9_./-]+\))?!?:\s*')
MERGE_PREFIX = re.compile(r'^Merge pull request #[0-9]+:\s*')
TECHNICAL_NAMES = (
    'Windows', 'Linux', 'macOS', 'Python', 'PyInstaller', 'Go', 'Rust', 'Tauri',
    'React', 'TypeScript', 'JavaScript', 'Tk', 'Tkinter', 'WebView2', 'Git',
    'GitHub', 'Actions', 'CI', 'UI', 'API', 'IPC', 'SSH', 'HTTP', 'HTTPS', 'TCP',
    'JSON', 'YAML', 'HTML', 'CSS', 'EXE', 'SHA', 'SHA256', 'UTF8', 'Unicode',
)
TECHNICAL = re.compile(r'(?<![A-Za-z0-9_])(?:' + '|'.join(TECHNICAL_NAMES) + r')(?![A-Za-z0-9_])')
JAPANESE_NAMES = ('CJK UNIFIED IDEOGRAPH-', 'CJK COMPATIBILITY IDEOGRAPH-',
                  'HIRAGANA LETTER', 'KATAKANA LETTER', 'KATAKANA LIGATURE')
URL = re.compile(r'https?://[A-Za-z0-9._~:/?#\[\]@!$&\x27()*+,;=%-]+')
SHA = re.compile(r'(?<![A-Za-z0-9_])[0-9a-fA-F]{7,64}(?![A-Za-z0-9_])')
PATH = re.compile(r'(?<![A-Za-z0-9_])(?:[A-Za-z]:)?[A-Za-z0-9_./\\@+-]*[/\\][A-Za-z0-9_./\\@+*-]+')
FILE = re.compile(r'(?<![A-Za-z0-9_])[A-Za-z0-9_.-]+\.(?:py|go|ts|tsx|js|jsx|mjs|cjs|json|ya?ml|md|txt|toml|ps1|rs|exe|zip|whl|html|css)(?![A-Za-z0-9_])')
PERSON_TRAILER = re.compile(r'(?:Signed-off-by|Co-authored-by|Reviewed-by|Acked-by|Tested-by):[ \t]+[^<>\r\n]*[^<>\s][ \t]+<[^<>\s@]+@[^<>\s@]+>', re.IGNORECASE)
ISSUE_TRAILER = re.compile(r'(?:Refs|Closes|Fixes|Related-to):[ \t]+#[1-9][0-9]*(?:[ \t]*,[ \t]*#[1-9][0-9]*)*', re.IGNORECASE)


def strip_trailers(body):
    lines = body.splitlines()
    while lines and (not lines[-1].strip() or PERSON_TRAILER.fullmatch(lines[-1])
                     or ISSUE_TRAILER.fullmatch(lines[-1])):
        lines.pop()
    return '\n'.join(lines).strip()


def explanatory_text(text):
    lines = []
    fence = None
    for line in text.splitlines():
        marker = re.match(r'^ {0,3}(`{3,}|~{3,})(.*)$', line)
        if fence:
            if marker and marker[1][0] == fence[0] and len(marker[1]) >= len(fence) and not marker[2].strip():
                fence = None
        elif marker:
            fence = marker[1]
        else:
            lines.append(line)
    if fence:
        raise ValueError('Unclosed code fence')
    text = '\n'.join(lines)
    # Only one ASCII token is code. Backticks cannot hide an English sentence.
    text = re.sub(r'`([^`\n]+)`', lambda match: '' if match[1].isascii() and not any(
        char.isspace() for char in match[1]) else match[1], text)
    for pattern in (URL, PATH, FILE, SHA, TECHNICAL):
        text = pattern.sub('', text)
    return text


def check_text(text, field, label):
    text = unicodedata.normalize('NFKC', text)
    if field == 'subject':
        text = PREFIX.sub('', MERGE_PREFIX.sub('', text), count=1)
    text = explanatory_text(text)
    letters = [char for char in text if unicodedata.category(char).startswith('L')]
    japanese = sum(unicodedata.name(char, '').startswith(JAPANESE_NAMES) for char in letters)
    if japanese < 4 or japanese * 3 < len(letters):
        raise ValueError(f'{label} {field}: japanese={japanese} letters={len(letters)}; need >=4 and >=1/3')
    print(f'JAPANESE_OK {label} {field}: japanese={japanese} letters={len(letters)}')


def check_message(message, label):
    lines = message.splitlines()
    if not lines or not lines[0].strip():
        raise ValueError(label + ' subject: missing')
    check_text(lines[0], 'subject', label)
    body = '\n'.join(lines[1:]).strip()
    if body:
        explanation = strip_trailers(body)
        if explanation:
            check_text(explanation, 'body', label)
        else:
            print(f'JAPANESE_OK {label} body: metadata-only')


def git(repo, *arguments):
    return subprocess.run(['git', '--no-pager', '-c', 'log.showSignature=false',
                           '-c', 'i18n.logOutputEncoding=UTF-8', '-C', str(repo), *arguments],
                          check=True, capture_output=True, text=True, encoding='utf-8', timeout=30).stdout


def check_range(repo, base, head, linear=False):
    if any(not isinstance(sha, str) or not re.fullmatch(r'[0-9a-f]{40}', sha) for sha in (base, head)):
        raise ValueError('Base/head must be explicit full commit SHAs')
    for sha in (base, head):
        git(repo, 'rev-parse', '--verify', sha + '^{commit}')
    if base == '0' * 40 or head == '0' * 40:
        raise ValueError('Null before/after is not a bounded update')
    if linear:
        git(repo, 'merge-base', '--is-ancestor', base, head)
    else:
        git(repo, 'merge-base', base, head)
    commits = git(repo, 'rev-list', '--reverse', '--topo-order', '--max-count=1001', base + '..' + head).splitlines()
    if not commits or len(commits) > 1000:
        raise ValueError('New commit range must contain 1–1000 commits')
    failures = []
    for sha in commits:
        message = git(repo, 'show', '-s', '--no-show-signature', '--format=%B', sha)
        try:
            check_message(message, sha[:12])
        except ValueError as error:
            failures.append(str(error))
    if failures:
        raise ValueError(' | '.join(failures))
    print(f'COMMIT_RANGE checked={len(commits)} base={base[:12]} head={head[:12]}')
    return len(commits)


def event_range(path):
    event = json.loads(path.read_text(encoding='utf-8'))
    if 'pull_request' in event and event['pull_request']['base']['ref'] == 'main':
        return event['pull_request']['base']['sha'], event['pull_request']['head']['sha'], False
    if event.get('ref') == 'refs/heads/main' and not event.get('deleted', False):
        return event['before'], event['after'], True
    raise ValueError('Expected a main pull_request or bounded main push event')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--message-file', type=Path)
    mode.add_argument('--base')
    mode.add_argument('--event', type=Path)
    parser.add_argument('--head')
    parser.add_argument('--repo', type=Path, default=Path.cwd())
    args = parser.parse_args()
    try:
        if args.message_file:
            if args.head:
                raise ValueError('--head requires --base')
            check_message(args.message_file.read_text(encoding='utf-8-sig'), 'candidate')
            checked = 1
        elif args.event:
            if args.head:
                raise ValueError('--head requires --base')
            checked = check_range(args.repo, *event_range(args.event))
        else:
            checked = check_range(args.repo, args.base, args.head)
        print(f'COMMIT_MESSAGE_PASS checked={checked}')
        return 0
    except subprocess.SubprocessError:
        print('COMMIT_MESSAGE_FAIL: Git comparison failed; verify complete trusted base/head history', file=sys.stderr)
        return 1
    except (OSError, UnicodeError):
        print('COMMIT_MESSAGE_FAIL: Cannot read UTF-8 input or local Git data', file=sys.stderr)
        return 1
    except (KeyError, TypeError, AttributeError):
        print('COMMIT_MESSAGE_FAIL: Invalid event structure', file=sys.stderr)
        return 1
    except ValueError as error:
        print('COMMIT_MESSAGE_FAIL:', error, file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
