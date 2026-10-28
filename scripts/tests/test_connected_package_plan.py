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


if __name__ == '__main__':
    unittest.main()
