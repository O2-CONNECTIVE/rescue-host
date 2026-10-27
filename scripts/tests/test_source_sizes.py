"""Exercise the tracked-source size gate through its command-line interface."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


class SourceSizeTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='source-size-fixture-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name) / 'repo'
        (self.root / 'scripts').mkdir(parents=True)
        checker = Path(__file__).resolve().parents[1] / 'check_source_sizes.py'
        if checker.exists():
            shutil.copy2(checker, self.root / 'scripts/check_source_sizes.py')
        shutil.copy2(Path(__file__).resolve().parents[1] / 'dev.py', self.root / 'scripts/dev.py')
        self.report = Path(temporary.name) / 'report.json'
        self.policy = {
            'version': 1,
            'scope': {'root': 'ui', 'extensions': ['.js', '.jsx', '.mjs', '.cjs', '.ts', '.tsx', '.mts', '.cts']},
            'limits': {'max_lines': 3, 'max_bytes': 128},
            'rationale': 'Finite fixture limits, not repository recommendations.',
            'roles': [], 'exclusions': [], 'exceptions': [],
        }
        self.git('init', '-q')
        self.write('ui/app.tsx', 'const a = 1;\nconst b = 2;\n')
        self.save_policy()
        self.git('add', '.')
        self.git('-c', 'user.name=Static fixture', '-c', 'user.email=fixture@example.invalid',
                 'commit', '-qm', 'finite fixture')

    def git(self, *arguments):
        return subprocess.run(['git', '-c', 'core.autocrlf=false', *arguments],
                              cwd=self.root, check=True, capture_output=True, timeout=20)

    def write(self, path, content):
        target = self.root / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(content.encode('utf-8') if isinstance(content, str) else content)

    def save_policy(self):
        self.write('.source-size-policy.json', json.dumps(self.policy))

    def run_gate(self, *arguments, nonce='fixture-run'):
        return subprocess.run(
            [sys.executable, str(self.root / 'scripts/check_source_sizes.py'), *arguments],
            cwd=self.root.parent, capture_output=True, text=True, timeout=30,
            env=dict(os.environ, SOURCE_SIZE_REPORT=str(self.report), SOURCE_SIZE_NONCE=nonce),
        )

    def test_below_line_limit_produces_nonempty_report(self):
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('SOURCE_SIZE_PASS', result.stdout)
        self.assertEqual(json.loads(self.report.read_text())['checked'], 1)

    def test_role_specific_limit_is_used(self):
        self.policy['roles'] = [{'name': 'tests', 'patterns': ['ui/**/tests/**'],
                                 'max_lines': 5, 'max_bytes': 128, 'reason': 'Scenario assertions.'}]
        self.save_policy()
        self.write('ui/tauri/tests/scenario.tsx', 'value\n' * 4)
        self.git('add', '.')
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('role=tests', result.stdout)

    def test_generated_source_exclusion_keeps_real_source_checked(self):
        self.policy['exclusions'] = [{'pattern': 'ui/**/dist/**', 'reason': 'Generated build output.'}]
        self.save_policy()
        self.write('ui/tauri/dist/generated.js', 'generated\n' * 9)
        self.git('add', '.')
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        report = json.loads(self.report.read_text())
        self.assertEqual((report['checked'], report['excluded']), (1, 1))

    def exception(self):
        return {'path': 'ui/app.tsx', 'max_lines': 4, 'max_bytes': 24,
                'reason': 'Existing fixture baseline; separate responsibilities in follow-up.',
                'owner': 'fixture-maintainers', 'review_when': 'Next source edit or #23 review.',
                'issue': '#23'}

    def test_documented_baseline_exception_is_bounded(self):
        self.policy['exceptions'] = [self.exception()]
        self.save_policy()
        self.write('ui/app.tsx', 'value\n' * 4)
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(json.loads(self.report.read_text())['exceptions'], 1)

    def test_verification_fails_if_checker_did_not_run(self):
        result = self.run_gate('--verify-report')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('report', result.stderr.lower())
        self.assertFalse(self.report.exists())

    def assert_failure(self, result, message):
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('SOURCE_SIZE_FAIL', result.stderr)
        self.assertIn(message, result.stderr)

    def test_exact_line_limit_passes_and_one_line_over_fails(self):
        self.write('ui/app.tsx', 'value\n' * 3)
        self.assertEqual(self.run_gate().returncode, 0)
        self.write('ui/app.tsx', 'value\n' * 4)
        self.assert_failure(self.run_gate(), 'Source size exceeded')
        self.assertFalse(self.report.exists())

    def test_byte_limit_rejects_one_large_unicode_line(self):
        self.write('ui/app.tsx', 'é' * 65 + '\n')
        self.assert_failure(self.run_gate(), 'Source size exceeded')

    def test_exact_byte_limit_passes_and_one_byte_over_fails(self):
        self.write('ui/app.tsx', 'a' * 127 + '\n')
        self.assertEqual(self.run_gate().returncode, 0)
        self.write('ui/app.tsx', 'a' * 128 + '\n')
        self.assert_failure(self.run_gate(), 'bytes=129/128')

    def test_bom_unicode_and_newline_encodings_have_same_metrics(self):
        expected = 'const 名 = 1;\n' * 3
        for newline in ('\n', '\r\n', '\r'):
            with self.subTest(newline=repr(newline)):
                self.write('ui/app.tsx', b'\xef\xbb\xbf' + expected.replace('\n', newline).encode('utf-8'))
                result = self.run_gate()
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                item = json.loads(self.report.read_text())['files'][0]
                self.assertEqual((item['lines'], item['bytes']), (3, len(expected.encode('utf-8'))))

    def test_final_newline_does_not_create_an_extra_line(self):
        for source in ('a\nb\nc', 'a\nb\nc\n'):
            self.write('ui/app.tsx', source)
            self.assertEqual(self.run_gate().returncode, 0)
            self.assertEqual(json.loads(self.report.read_text())['files'][0]['lines'], 3)

    def test_empty_source_is_rejected(self):
        self.write('ui/app.tsx', ' \t\r\n')
        self.assert_failure(self.run_gate(), 'Empty source')

    def test_no_tracked_ui_source_is_rejected(self):
        self.git('rm', '-q', 'ui/app.tsx')
        self.assert_failure(self.run_gate(), 'No tracked UI source files')

    def test_excluding_every_candidate_is_rejected(self):
        self.policy['exclusions'] = [{'pattern': 'ui/**', 'reason': 'Invalid all-source exclusion fixture.'}]
        self.save_policy()
        self.assert_failure(self.run_gate(), 'All tracked UI source files were excluded')

    def test_maintained_shadcn_component_is_checked(self):
        self.write('ui/tauri/src/components/ui/button.tsx', 'value\n' * 4)
        self.git('add', '.')
        self.assert_failure(self.run_gate(), 'components/ui/button.tsx')

    def test_typescript_module_extensions_are_checked(self):
        for extension in ('.mts', '.cts', '.d.mts', '.d.cts'):
            with self.subTest(extension=extension):
                path = 'ui/module' + extension
                self.write(path, 'value\n' * 4)
                self.git('add', path)
                self.assert_failure(self.run_gate(), path)
                self.git('rm', '--cached', '-q', path)
                (self.root / path).unlink()

    def test_recursive_patterns_match_zero_directory_levels(self):
        self.policy['exclusions'] = [{'pattern': 'ui/**/dist/**', 'reason': 'Generated.'}]
        self.policy['roles'] = [{'name': 'tests', 'patterns': ['ui/**/tests/**', 'ui/**/*.test.*'],
                                 'max_lines': 5, 'max_bytes': 128, 'reason': 'Scenario assertions.'}]
        self.save_policy()
        self.write('ui/dist/generated.js', 'generated\n' * 9)
        self.write('ui/tests/scenario.tsx', 'value\n' * 4)
        self.write('ui/app.test.tsx', 'value\n' * 4)
        self.git('add', '.')
        result = self.run_gate()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        report = json.loads(self.report.read_text())
        self.assertEqual((report['checked'], report['excluded']), (3, 1))
        self.assertEqual([item['role'] for item in report['files']].count('tests'), 2)

    def test_exception_cannot_grow_in_lines_or_bytes(self):
        self.policy['exceptions'] = [self.exception()]
        self.save_policy()
        for source in ('value\n' * 5, 'values\n' + 'value\n' * 3):
            with self.subTest(source=source):
                self.write('ui/app.tsx', source)
                self.assert_failure(self.run_gate(), 'Source size exceeded')

    def test_exception_requires_owner_and_review_condition(self):
        for field in ('owner', 'reason', 'review_when', 'issue'):
            with self.subTest(field=field):
                entry = self.exception()
                entry[field] = ''
                self.policy['exceptions'] = [entry]
                self.save_policy()
                self.assert_failure(self.run_gate(), 'Exception requires')

    def test_dangling_exception_is_rejected(self):
        entry = self.exception()
        entry['path'] = 'ui/removed.tsx'
        self.policy['exceptions'] = [entry]
        self.save_policy()
        self.assert_failure(self.run_gate(), 'Exception does not name a checked file')

    def test_unneeded_exception_must_be_removed(self):
        self.policy['exceptions'] = [self.exception()]
        self.save_policy()
        self.assert_failure(self.run_gate(), 'Unneeded baseline exception')

    def test_wildcard_and_duplicate_exceptions_are_rejected(self):
        entry = self.exception()
        entry['path'] = 'ui/*.tsx'
        self.policy['exceptions'] = [entry]
        self.save_policy()
        self.assert_failure(self.run_gate(), 'exact repository file')
        self.policy['exceptions'] = [self.exception(), self.exception()]
        self.save_policy()
        self.assert_failure(self.run_gate(), 'Duplicate baseline exception')

    def test_successful_report_is_verified_without_rerunning_checker(self):
        self.assertEqual(self.run_gate().returncode, 0)
        result = self.run_gate('--verify-report')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('SOURCE_SIZE_VERIFIED checked=1', result.stdout)
        self.assertNotIn('SOURCE_SIZE_PASS', result.stdout)

    def test_report_from_another_execution_is_rejected(self):
        self.assertEqual(self.run_gate().returncode, 0)
        self.assert_failure(self.run_gate('--verify-report', nonce='another-run'), 'does not match')

    def test_policy_change_invalidates_report(self):
        self.assertEqual(self.run_gate().returncode, 0)
        self.policy['limits']['max_lines'] = 5
        self.save_policy()
        self.assert_failure(self.run_gate('--verify-report'), 'does not match')

    def test_zero_count_report_is_rejected(self):
        self.assertEqual(self.run_gate().returncode, 0)
        report = json.loads(self.report.read_text())
        report.update(checked=0, files=[])
        self.report.write_text(json.dumps(report))
        self.assert_failure(self.run_gate('--verify-report'), 'no checked files')

    def test_report_cannot_replace_tracked_source(self):
        source = self.root / 'ui/app.tsx'
        before = source.read_bytes()
        self.report = source
        self.assert_failure(self.run_gate(), 'must not replace a tracked file')
        self.assertEqual(source.read_bytes(), before)

    def test_invalid_utf8_is_rejected(self):
        self.write('ui/app.tsx', b'\xff')
        self.assert_failure(self.run_gate(), 'decode')

    def test_common_entry_actually_runs_checker(self):
        result = subprocess.run(
            [sys.executable, str(self.root / 'scripts/dev.py'), 'sizes'],
            cwd=self.root.parent, capture_output=True, text=True, timeout=30,
            env=dict(os.environ, SOURCE_SIZE_REPORT=str(self.report), SOURCE_SIZE_NONCE='entry-fixture'),
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('SOURCE_SIZE_PASS checked=1', result.stdout)
        self.assertEqual(json.loads(self.report.read_text())['nonce'], 'entry-fixture')

    def test_common_entry_propagates_checker_failure(self):
        self.write('ui/app.tsx', 'value\n' * 4)
        result = subprocess.run(
            [sys.executable, str(self.root / 'scripts/dev.py'), 'sizes'],
            cwd=self.root.parent, capture_output=True, text=True, timeout=30,
            env=dict(os.environ, SOURCE_SIZE_REPORT=str(self.report), SOURCE_SIZE_NONCE='entry-fixture'),
        )
        self.assert_failure(result, 'lines=4/3')
        self.assertFalse(self.report.exists())


if __name__ == '__main__':
    unittest.main()
