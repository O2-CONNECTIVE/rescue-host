"""準備manifestのCLI契約。fixture hashは実行物・信頼根の証拠ではない。"""
import copy
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
CLI = ROOT / 'scripts/verify_connected_package_plan.py'
SOURCE = 'a' * 40
FILES = ['bin/rescue-host-shell.exe', 'bin/rescue-service-host.exe', 'bin/rescue-backend.exe',
         'LICENSE', 'locks/Cargo.lock', 'locks/package-lock.json', 'locks/go.mod', 'locks/go.sum',
         'licenses/gui/DEPENDENCIES.json', 'licenses/service/DEPENDENCIES.json',
         'licenses/backend/DEPENDENCIES.json', 'licenses/FRONTEND_THIRD_PARTY_NOTICES.txt']


def fixture():
    return {'schema': 'rescue-host-connected-evaluation-plan/1',
            'repository': 'O2-CONNECTIVE/rescue-host', 'sourceCommit': SOURCE,
            'platform': 'windows-x64', 'activation': 'blocked', 'productionGateClosed': True,
            'protocols': {'control': 'rescue-host-control/1', 'bootstrap': 'rescue-host-bootstrap/1'},
            'components': {role: {'path': file, 'sourceCommit': SOURCE, 'sha256': digest,
                                   'version': 'unverified'}
                           for role, file, digest in [('gui', FILES[0], '1' * 64),
                                                     ('service', FILES[1], '2' * 64),
                                                     ('backend', FILES[2], '3' * 64)]},
            'files': {name: (str(i + 1) * 64 if i < 3 else 'f' * 64)
                      for i, name in enumerate(FILES)}}


class ConnectedPackagePlanTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.path = Path(self.temporary.name) / 'plan.json'

    def run_plan(self, plan):
        self.path.write_text(json.dumps(plan), encoding='utf-8')
        return self.run_raw()

    def run_raw(self):
        return subprocess.run([sys.executable, '-B', str(CLI), '--manifest', str(self.path),
                               '--source', SOURCE, '--gui-sha256', '1' * 64,
                               '--service-sha256', '2' * 64, '--backend-sha256', '3' * 64],
                              capture_output=True, text=True, timeout=10)

    def test_consistent_plan_is_blocked_and_never_distribution_success(self):
        result = self.run_plan(fixture())
        self.assertEqual(result.returncode, 2, result.stderr)
        record = json.loads(result.stdout)
        self.assertEqual(record['status'], 'blocked')
        self.assertTrue(record['manifestValid'])
        self.assertTrue(record['productionGateClosed'])
        self.assertIn('SERVICE_PROVISIONING_UNSET', record['blockers'])
        self.assertEqual(result.stderr, '')

    def test_rejects_incomplete_or_mixed_plan(self):
        # 最小実装後の負例回帰。各caseは独立したfixtureコピーを使う。
        cases = [
            ('missing-backend', lambda p: p['components'].pop('backend')),
            ('missing-license', lambda p: p['files'].pop('licenses/backend/DEPENDENCIES.json')),
            ('extra-binary', lambda p: p['files'].update({'payload.exe': 'f' * 64})),
            ('source-mix', lambda p: p['components']['backend'].update(sourceCommit='b' * 40)),
            ('binary-hash', lambda p: p['components']['backend'].update(sha256='f' * 64)),
            ('catalog-hash', lambda p: p['files'].update({'bin/rescue-backend.exe': 'f' * 64})),
            ('traversal', lambda p: p['components']['backend'].update(path='../backend.exe')),
            ('protocol', lambda p: p['protocols'].update(bootstrap='rescue-host-bootstrap/2')),
            ('gate', lambda p: p.update(productionGateClosed=False)),
            ('gate-type', lambda p: p.update(productionGateClosed=1)),
            ('activation', lambda p: p.update(activation='ready')),
            ('runtime-claim', lambda p: p['components']['backend'].update(version='verified')),
            ('secret-field', lambda p: p.update(privateKey='PRIVATE_VALUE_DO_NOT_COPY')),
        ]
        for name, change in cases:
            with self.subTest(case=name):
                plan = copy.deepcopy(fixture())
                change(plan)
                result = self.run_plan(plan)
                self.assertEqual(result.returncode, 1)
                self.assertEqual(json.loads(result.stdout)['status'], 'fail')
                self.assertEqual(result.stderr, '')
                self.assertNotIn('PRIVATE_VALUE_DO_NOT_COPY', result.stdout)
                self.assertNotIn(str(self.path), result.stdout)

    def test_duplicate_fields_and_size_limit_fail_without_raw_diagnostics(self):
        # 実装後の入力境界回帰。
        payload = json.dumps(fixture())
        for raw, expected in [(payload.replace('{', '{"schema":"PRIVATE_DUPLICATE",', 1),
                               'PLAN_DUPLICATE_FIELD'), ('x' * (128 * 1024 + 1), 'PLAN_SIZE_LIMIT'),
                              ('not JSON PRIVATE_PARSE', 'PLAN_INVALID')]:
            with self.subTest(code=expected):
                self.path.write_text(raw, encoding='utf-8')
                result = self.run_raw()
                self.assertEqual(result.returncode, 1)
                self.assertEqual(json.loads(result.stdout)['code'], expected)
                self.assertEqual(result.stderr, '')
                self.assertNotIn('PRIVATE_', result.stdout)


if __name__ == '__main__':
    unittest.main()
