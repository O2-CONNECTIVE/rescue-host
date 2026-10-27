"""Exercise commit-message policy through the CLI and finite local Git repos."""

import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


CHECKER = Path(__file__).resolve().parents[1] / 'check_commit_messages.py'


class CommitMessageTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='commit-message-fixture-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)

    def check_message(self, message):
        path = self.root / 'message.txt'
        path.write_text(message, encoding='utf-8')
        return subprocess.run([sys.executable, '-I', str(CHECKER), '--message-file', str(path)],
                              cwd=self.root, capture_output=True, text=True, timeout=20)

    def test_japanese_subject_and_explanation_pass(self):
        result = self.check_message('ci: 日本語の説明を検証する\n\n追加commitの説明を確認します。\n')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('COMMIT_MESSAGE_PASS checked=1', result.stdout)

    def test_technical_spans_do_not_outweigh_japanese_explanation(self):
        result = self.check_message(
            'fix: `src/very_long_controller_name.py` の終了処理を修正する\n\n'
            '終了時に資源を解放します。\n'
            'https://github.com/example/very-long-project/issues/123\n'
            '68ed17f2686e0ac4ae3c8976e944e047c7e7d389\n'
            'src/another_very_long_module/controller.py\n'
            '```python\nresource_owner.release_all_pending_channels()\n```\n')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('candidate body:', result.stdout)

    def test_only_valid_terminal_standard_trailers_need_no_explanation(self):
        result = self.check_message(
            'docs: 日本語の運用方針を追記する\n\n'
            'Refs: #23, #24\n'
            'Co-authored-by: Example Contributor <contributor@example.invalid>\n')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('body: metadata-only', result.stdout)

    def git(self, *arguments):
        return subprocess.run(
            ['git', '-c', 'user.name=Commit policy fixture', '-c', 'user.email=fixture@example.invalid',
             '-c', 'core.autocrlf=false', *arguments], cwd=self.repo, check=True,
            capture_output=True, text=True, timeout=20).stdout.strip()

    def commit(self, message):
        path = self.root / 'git-message.txt'
        path.write_text(message, encoding='utf-8')
        self.git('commit', '--allow-empty', '--no-gpg-sign', '-q', '-F', str(path))
        return self.git('rev-parse', 'HEAD')

    def init_repo(self):
        self.repo = self.root / 'repo'
        self.repo.mkdir()
        self.git('init', '-q', '--initial-branch=main')
        return self.commit('Legacy English message outside the new range')

    def check_range(self, *arguments):
        return subprocess.run([sys.executable, '-I', str(CHECKER), '--repo', str(self.repo), *arguments],
                              cwd=self.root, capture_output=True, text=True, timeout=20)

    def test_new_range_does_not_reject_english_ancestors(self):
        base = self.init_repo()
        head = self.commit('ci: 新規コミットの日本語説明を検証する')
        result = self.check_range('--base', base, '--head', head)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('COMMIT_MESSAGE_PASS checked=1', result.stdout)

    def check_event(self, event):
        path = self.root / 'event.json'
        path.write_text(json.dumps(event), encoding='utf-8')
        return self.check_range('--event', str(path))

    def test_pr_uses_event_base_and_head_after_main_has_advanced(self):
        old = self.init_repo()
        self.git('checkout', '-qb', 'feature')
        head = self.commit('ci: 日本語の新規範囲を確認する')
        self.git('checkout', '-q', 'main')
        base = self.commit('docs: 本線の説明文を追記する')
        self.assertNotEqual(base, old)
        result = self.check_event({'pull_request': {'base': {'ref': 'main', 'sha': base}, 'head': {'sha': head}}})
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('COMMIT_MESSAGE_PASS checked=1', result.stdout)

    def assert_rejected(self, result, field):
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('COMMIT_MESSAGE_FAIL', result.stderr)
        self.assertIn(field, result.stderr)

    def test_real_repository_japanese_subjects_fit_initial_budget(self):
        subjects = (
            'test: キー操作とfocusの反復回帰を統合（#41）',
            'Merge pull request #42: frontend noticeの欠落・改変を検出する',
            'Merge pull request #38: Windows native起動・IPC・通常closeのsmokeを追加',
            'Merge pull request #39: Windows Python build依存を版とhashで固定',
            'docs(ci): 依存総数と関連文書のlock検証範囲を揃える (#24)',
            'docs: 固定依存総数と開発ツール試験件数を明記 (#24)',
            'Merge pull request #37: UI sourceサイズの静的ゲートを追加',
            'docs: 全開発ツール試験のPython要件と件数を明記 (#23)',
            'Merge pull request #35: 未配線React UIと品質検査を追加',
            'docs: CI実行段階の説明を実装に揃える (#25)',
            'Merge pull request #33: 未配線Tauri shellを追加',
            'Merge pull request #30: デザインとa11yの基準案を文書化',
            'Merge pull request #29: 静的token検証を共通入口とCIへ接続',
            'Merge pull request #21: ライトテーマtokensと状態見本を追加',
            'Merge pull request #20: 共通開発入口と配布要件を整備',
            'Merge pull request #19: 通信契約と脅威モデルを整理',
            'Merge pull request #15: 既存Pythonオフラインテストを3 OSで自動化',
            'Merge pull request #10: リポジトリ配置方針を明記',
            'docs: リポジトリ配置方針と一時作業領域を明記 (#8)',
            'docs: 独立レビューに従い箇条書きの文を改行',
            'docs: READMEを価値と利用目標中心に整理し詳細をdocsへ分離',
            'v0.2.1-dev: オフライン安全性改善、日本語文書と構成図を追加（新規接続は無効）',
        )
        for subject in subjects:
            with self.subTest(subject=subject):
                result = self.check_message(subject)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_subject_and_body_cannot_rescue_each_other(self):
        self.assert_rejected(self.check_message('fix: improve the entire implementation\n\n変更理由を日本語で説明します。'), 'subject:')
        self.assert_rejected(self.check_message('fix: 終了時の資源解放を修正する\n\nThis explanatory body must be checked independently.'), 'body:')

    def test_minimum_japanese_and_one_third_boundary(self):
        self.assertEqual(self.check_message('ci: 説明追加abcdefgh').returncode, 0)
        self.assert_rejected(self.check_message('ci: 説明追加abcdefghi'), 'need >=4 and >=1/3')
        self.assert_rejected(self.check_message('ci: 追加のab'), 'japanese=3')
        self.assert_rejected(self.check_message('ci: This is a long English explanation with one appended 漢'), 'subject:')

    def test_nfkc_halfwidth_kana_and_decomposed_kana_pass(self):
        for subject in ('test: ｶﾀｶﾅの説明を確認する', 'test: か\u3099な表記の説明を確認する'):
            self.assertEqual(self.check_message(subject).returncode, 0)

    def test_non_japanese_unicode_and_punctuation_do_not_count_as_japanese(self):
        for subject in ('ci: 🇯🇵 improve Unicode bytes', 'ci: Ｆｉｘ ｔｈｅ ｅｎｔｉｒｅ ｓｙｓｔｅｍ',
                        'ci: Ελληνικά 한국어 Кириллица', 'ci: 😀😀！！！１２３４', 'ci: ーーーー'):
            self.assert_rejected(self.check_message(subject), 'subject:')

    def test_kanji_only_is_a_script_heuristic_not_language_identification(self):
        self.assertEqual(self.check_message('docs: 安全更新方針').returncode, 0)

    def test_empty_body_is_allowed_but_empty_or_technical_only_subject_is_not(self):
        self.assertEqual(self.check_message('ci: 日本語の運用方針を確認する\n\n').returncode, 0)
        for subject in ('', '\n日本語の本文だけを記載する', 'ci: https://example.invalid', 'ci: `src/entry.py`'):
            self.assert_rejected(self.check_message(subject), 'subject:')

    def test_code_or_technical_only_body_is_not_an_empty_body(self):
        for body in ('```text\nA complete English body inside a fence\n```',
                     '~~~python\nowner.close_all()\n~~~', 'https://example.invalid/src/controller.py',
                     '`english_identifier`', '```text\nThis is English prose\n```\n漢'):
            self.assert_rejected(self.check_message('ci: 日本語の説明文を確認する\n\n' + body), 'body:')

    def test_backticks_do_not_hide_an_english_sentence(self):
        self.assert_rejected(self.check_message(
            'ci: 日本語の説明文を確認する\n\n説明追加 `This complete English explanation must stay in the denominator.`'), 'body:')

    def test_unclosed_fence_is_rejected(self):
        self.assert_rejected(self.check_message('ci: 日本語の説明文を確認する\n\n追加理由を説明します。\n```python\nowner.close()'), 'Unclosed code fence')

    def test_invalid_or_nonterminal_trailers_are_not_removed(self):
        for body in ('Co-authored-by: This is an English body without an email address',
                     'Description: This ordinary English sentence is not a standard trailer',
                     'Co-authored-by: Very Long Example Contributor <fixture@example.invalid>\n\n説明追加'):
            self.assert_rejected(self.check_message('ci: 日本語の説明文を確認する\n\n' + body), 'body:')

    def test_english_commit_cannot_be_cancelled_by_later_japanese_commit(self):
        base = self.init_repo()
        bad_text = 'English message ::error:: do not echo this text'
        bad = self.commit(bad_text)
        head = self.commit('ci: 追加分の日本語説明を確認する')
        result = self.check_range('--base', base, '--head', head)
        self.assert_rejected(result, bad[:12])
        self.assertNotIn(bad_text, result.stdout + result.stderr)

    def merge_fixture(self, subject):
        self.init_repo()
        self.git('checkout', '-qb', 'feature')
        self.commit('test: 分岐側の日本語説明を確認する')
        self.git('checkout', '-q', 'main')
        before = self.commit('docs: 本線の説明文を更新する')
        self.git('merge', '--no-ff', '--no-gpg-sign', '-q', '-m', subject, 'feature')
        after = self.git('rev-parse', 'HEAD')
        return {'ref': 'refs/heads/main', 'before': before, 'after': after}

    def test_main_push_checks_actual_new_merge_commit(self):
        result = self.check_event(self.merge_fixture('Merge pull request #1: 日本語で分岐の変更を統合する'))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('COMMIT_MESSAGE_PASS checked=2', result.stdout)

    def test_default_english_merge_subject_is_rejected_after_push(self):
        result = self.check_event(self.merge_fixture('Merge pull request #1 from example/feature'))
        self.assert_rejected(result, 'subject:')

    def test_english_squash_message_is_rejected_after_push(self):
        before = self.init_repo()
        after = self.commit('Improve the entire implementation\n\n日本語の説明を追加します。')
        self.assert_rejected(self.check_event({'ref': 'refs/heads/main', 'before': before, 'after': after}), 'subject:')

    def test_empty_range_null_unknown_and_non_linear_push_fail_closed(self):
        base = self.init_repo()
        head = self.commit('ci: 新規の日本語説明を確認する')
        for arguments in (('--base', base, '--head', base), ('--base', '0' * 40, '--head', head),
                          ('--base', 'f' * 40, '--head', head), ('--base', 'main', '--head', head)):
            result = self.check_range(*arguments)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assert_rejected(self.check_event({'ref': 'refs/heads/main', 'before': head, 'after': base}), 'Git comparison failed')

    def test_other_branch_and_malformed_event_are_rejected(self):
        base = self.init_repo()
        head = self.commit('ci: 新規の日本語説明を確認する')
        self.assert_rejected(self.check_event({'ref': 'refs/heads/other', 'before': base, 'after': head}), 'Expected a main')
        self.assert_rejected(self.check_event({'pull_request': {}}), 'Invalid event structure')


if __name__ == '__main__':
    unittest.main()
