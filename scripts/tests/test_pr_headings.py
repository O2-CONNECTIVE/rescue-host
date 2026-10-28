"""Exercise the PR H1 policy through JSON events and its isolated CLI."""

import json
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import unittest


CHECKER = Path(__file__).resolve().parents[1] / 'check_pr_headings.py'


class PullRequestHeadingTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='pr-heading-fixture-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)

    def check_event(self, body, action='opened'):
        path = self.root / 'event.json'
        path.write_text(json.dumps({'action': action, 'pull_request': {
            'base': {'ref': 'main'}, 'title': 'English title outside the gate',
            'body': body}}), encoding='utf-8')
        return subprocess.run([sys.executable, '-I', str(CHECKER), '--event', str(path)],
                              cwd=self.root, capture_output=True, text=True, timeout=20)

    def test_english_h1_fails_and_japanese_h1_passes(self):
        english = self.check_event('# Summary\n')
        self.assertEqual(english.returncode, 1, english.stdout + english.stderr)
        self.assertIn('PR_HEADING_FAIL h1=1', english.stderr)
        japanese = self.check_event('# 変更の概要\n')
        self.assertEqual(japanese.returncode, 0, japanese.stdout + japanese.stderr)
        self.assertIn('PR_HEADING_PASS checked=1', japanese.stdout)

    def test_fenced_examples_are_not_headings(self):
        for body in ('```markdown\n# English example\n```\n# 概要\n',
                     '~~~markdown\n# English example\n~~~\n# 概要\n',
                     '   ````markdown\n# English example\n```\n# Still code\n`````\n# 概要\n',
                     '```markdown\n# English example\n'):
            with self.subTest(body=body):
                result = self.check_event(body)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_setext_h1_is_checked_including_multiline_text(self):
        for body in ('Summary\n=======\n', 'English first line\n概要\n===\n'):
            result = self.check_event(body)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
            self.assertIn('h1=1', result.stderr)
        result = self.check_event('API 概要\nと変更点\n===\n')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('checked=1', result.stdout)

    def test_setext_h1_includes_lazy_paragraph_continuations(self):
        for body in ('概要\nEnglish heading content\n===\n',
                     '> 概要\nEnglish heading content\n> ===\n',
                     '- 概要\nEnglish heading content\n  ===\n',
                     '> - 概要\nEnglish heading content\n>   ===\n'):
            with self.subTest(body=body):
                result = self.check_event(body)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn('h1=1 japanese=2 letters=23', result.stderr)
        for body in ('> 概要\nEnglish paragraph content\n',
                     '- 概要\nEnglish paragraph content\n',
                     '> - 概要\nEnglish paragraph content\n'):
            with self.subTest(body=body):
                result = self.check_event(body)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn('checked=0', result.stdout)

    def test_inline_backticks_in_a_heading_do_not_start_a_block_fence(self):
        result = self.check_event('# ```日本語```\n')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_html_comments_and_raw_blocks_are_not_markdown_headings(self):
        for body in ('<!--\n# English template comment\n-->\n# 概要',
                     '<!--\n# English unclosed comment',
                     '<pre>\n# English code\n</pre>\n# 概要',
                     '<div>\n# English raw HTML text\n</div>\n\n# 概要'):
            result = self.check_event(body)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_non_paragraph_blocks_do_not_become_setext_h1(self):
        for body in ('- English list\n===\n', '> English quote\n===\n',
                     '* * *\n===\n', '[English]: https://example.invalid\n===\n',
                     ' \tEnglish indented code\n===\n', 'Text\u2028# English in same paragraph'):
            result = self.check_event(body)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn('checked=0', result.stdout)

    def test_headings_in_quotes_and_lists_are_checked(self):
        for body in ('> # English\n', '- # English\n', '> - > # English\n',
                     '- item\n  # English\n', '> Summary\n> ===\n'):
            result = self.check_event(body)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
            self.assertIn('h1=1', result.stderr)
        for body in ('- # 概要\n', '> - > # 概要\n', '- item\n  概要\n  ===\n'):
            result = self.check_event(body)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn('checked=1', result.stdout)

    def test_raw_html_h1_checks_only_text_including_nested_tags(self):
        for body in ('<h1>English</h1>', '<H1 title="日本語">English</H1>',
                     '<div><h1><span>English</span></h1></div>',
                     '> - <h1>English</h1>', '<h1><!-- 日本語 -->English</h1>'):
            result = self.check_event(body)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
            self.assertIn('h1=1', result.stderr)
        for body in ('<h1>概要</h1>', '<div><h1><span>API概要</span></h1></div>',
                     '<h1>&#x6982;&#x8981;</h1>'):
            result = self.check_event(body)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn('checked=1', result.stdout)

    def test_markdown_heading_html_attributes_and_comments_are_not_text(self):
        for body in ('# <span title="日本語の説明">Summary</span>',
                     '# <!-- 日本語の説明 -->Summary'):
            result = self.check_event(body)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        result = self.check_event('# <span>概要</span>')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_complete_custom_html_block_does_not_create_markdown_h1(self):
        result = self.check_event('<custom data-label="a > b">\n# English raw text\n</custom>\n\n# 概要')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('checked=1', result.stdout)

    def test_non_interrupting_ordered_marker_remains_in_setext_heading(self):
        result = self.check_event('English paragraph\n2. 概要\n===')
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('h1=1', result.stderr)

    def test_malformed_html_cannot_echo_parser_input(self):
        result = self.check_event('<![PRIVATE]>\n# 概要')
        # Older HTMLParser raises; newer patches treat this as a bogus comment.
        # Both must retain fixed diagnostics without exposing parser input.
        self.assertIn(result.returncode, (0, 1), result.stdout + result.stderr)
        self.assertIn('PR_HEADING_', result.stdout + result.stderr)
        self.assertNotIn('PRIVATE', result.stdout + result.stderr)
        self.assertNotIn('Traceback', result.stdout + result.stderr)

    def test_backslash_escaped_tags_and_raw_text_elements_do_not_create_h1(self):
        for body in (r'\<h1>English\</h1>', '<title><h1>English</h1></title>',
                     '<iframe><h1>English</h1></iframe>'):
            result = self.check_event(body)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn('checked=0', result.stdout)
        result = self.check_event(r'\`<h1>English</h1>`')
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('h1=1', result.stderr)

    def test_non_ascii_space_does_not_close_a_code_fence(self):
        result = self.check_event('```\n```\u00a0\n# English code')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('checked=0', result.stdout)

    # Post-hoc regression checks for behavior already present in the slices above.
    def test_workflow_passes_quoted_event_path_as_a_single_data_argument(self):
        workflow = CHECKER.parents[1] / '.github/workflows/commit-message-japanese.yml'
        command = next(line.split('run: ', 1)[1] for line in workflow.read_text().splitlines()
                       if 'run: python -I scripts/check_pr_headings.py --event' in line)
        self.assertTrue(command.endswith('--event "$GITHUB_EVENT_PATH"'), command)
        arguments = shlex.split(command)
        self.assertEqual(arguments, ['python', '-I', 'scripts/check_pr_headings.py',
                                     '--event', '$GITHUB_EVENT_PATH'])
        path = self.root / 'event $(touch shell-must-not-run).json'
        path.write_text(json.dumps({'action': 'edited', 'pull_request': {
            'base': {'ref': 'main'}, 'body': '# 概要'}}), encoding='utf-8')
        # Exercise the data argument on every OS without requiring a POSIX shell
        # on Windows; the workflow itself runs this command on Ubuntu.
        arguments[0], arguments[2], arguments[-1] = sys.executable, str(CHECKER), str(path)
        result = subprocess.run(arguments, cwd=self.root, capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('checked=1', result.stdout)
        self.assertFalse((self.root / 'shell-must-not-run').exists())
        self.assertNotIn(str(path), result.stdout + result.stderr)

    def test_container_fences_and_indented_code_hide_only_code_headings(self):
        for body in ('> ```\n> # English code\n> ```\n> # 概要',
                     '- ~~~\n  # English code\n  ~~~\n  # 概要',
                     '> - > ````\n>   > # English code\n>   > ````\n>   > # 概要',
                     '- item\n\n      # English code\n\n  # 概要',
                     '>     # English code\n>\n> # 概要', '-     # English code\n\n# 概要'):
            with self.subTest(body=body):
                result = self.check_event(body)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn('checked=1', result.stdout)
        for body in ('> ```\n> # English code\n# English outside',
                     '- ```\n  # English code\n# English outside',
                     '- > ```\n  > # English code\n  # English outside quote',
                     '> ~~~\n> # English code\n\n# English outside'):
            with self.subTest(body=body):
                result = self.check_event(body)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn('h1=1', result.stderr)

    def test_html_code_comments_entities_and_non_heading_elements_are_not_h1(self):
        for body in ('&lt;h1&gt;English&lt;/h1&gt;', '`<h1>English</h1>`',
                     '``<h1>English</h1>``', '`<h1>\nEnglish</h1>`',
                     '```html\n<h1>English</h1>\n```',
                     '> - ~~~html\n>   <h1>English</h1>\n>   ~~~',
                     '    <h1>English</h1>', '<!-- <h1>English</h1> -->',
                     '<script>const value = "<h1>English</h1>";</script>',
                     '<textarea><h1>English</h1></textarea>', '<h2>English</h2>'):
            with self.subTest(body=body):
                result = self.check_event(body)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn('checked=0', result.stdout)

    def test_mixed_heading_forms_report_individual_source_order(self):
        result = self.check_event('<h1>概要</h1>\n\n> # English quote\n\n'
                                  '- <h1>変更点</h1>\n\n# English root')
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('h1=2', result.stderr)
        self.assertIn('h1=4', result.stderr)
        self.assertNotIn('h1=1', result.stderr)
        self.assertNotIn('h1=3', result.stderr)

    def test_short_japanese_and_limited_technical_exclusions_pass(self):
        for heading in ('概要', 'API概要', 'API 概要', 'ｶﾅ', 'か\u3099',
                        '概要 `src/controller.py` https://example.invalid/long/path'):
            result = self.check_event('# ' + heading)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_each_h1_is_checked_independently(self):
        result = self.check_event('# 日本語の概要\n# English summary\n# 変更内容\n# English tests\n')
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('h1=2', result.stderr)
        self.assertIn('h1=4', result.stderr)
        self.assertNotIn('h1=1', result.stderr)
        self.assertNotIn('h1=3', result.stderr)

    def test_no_h1_is_allowed_and_paragraphs_lower_headings_and_title_are_ignored(self):
        for body in (None, '', 'English paragraph\n\n## English heading\n### English tests',
                     '#日本語なし\n####### English text\n\\# English escaped\n',
                     '    # English code\n\t# English tabbed code\n',
                     'English lower heading\n---\n', '===\n'):
            result = self.check_event(body)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn('checked=0', result.stdout)
        result = self.check_event('# 概要\n\nEntire English paragraph.\n## English lower heading\n')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('checked=1', result.stdout)

    def test_one_third_boundary_and_empty_or_non_japanese_headings_fail(self):
        self.assertEqual(self.check_event('# 概要abcd').returncode, 0)
        for heading in ('概要abcde', '', 'API', '😀１２３', 'ーーーー', 'Ελληνικά 한국어',
                        '`Complete English sentence`'):
            result = self.check_event('# ' + heading)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
            self.assertIn('h1=1', result.stderr)

    def test_fence_type_trailing_text_and_backtick_info_rules(self):
        for body in ('~~~\n```\n# English code\n~~~\n# 概要',
                     '```\n``` invalid closer\n# English code\n```\n# 概要'):
            result = self.check_event(body)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn('checked=1', result.stdout)
        result = self.check_event('```invalid`info\n# English heading')
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_edited_body_is_rechecked_as_data_without_echoing_untrusted_text(self):
        marker = self.root / 'shell-must-not-run'
        payload = '$(touch ' + str(marker) + ') `touch ' + str(marker) + '` ::error::PRIVATE'
        result = self.check_event('# English ' + payload, action='edited')
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertFalse(marker.exists())
        for raw in (payload, str(marker), '::error::', 'PRIVATE'):
            self.assertNotIn(raw, result.stdout + result.stderr)
        result = self.check_event('# 概要\n\n' + payload, action='edited')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(marker.exists())

    def test_malformed_or_non_pr_event_fails_without_echoing_input(self):
        path = self.root / 'bad-event.json'
        for content in ('PRIVATE malformed', '{"pull_request": null}',
                        '{"pull_request":{"base":{"ref":"other"},"body":"# 概要"}}',
                        '{"pull_request":{"base":{"ref":"main"},"body":12}}',
                        '{"ref":"refs/heads/main","before":"PRIVATE"}'):
            path.write_text(content, encoding='utf-8')
            result = subprocess.run([sys.executable, '-I', str(CHECKER), '--event', str(path)],
                                    cwd=self.root, capture_output=True, text=True, timeout=20)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
            self.assertIn('PR_HEADING_FAIL', result.stderr)
            self.assertNotIn('PRIVATE', result.stdout + result.stderr)


if __name__ == '__main__':
    unittest.main()
