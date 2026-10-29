"""配布版受入記録CLIの有限fixture。実機やネットワークは使わない。"""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]
COMMON = ['revoked', 'both_end_eof', 'reader_join', 'process_exit_reap',
          'job_cleanup', 'resource_restoration', 'old_permission_rejected']
EXTRA = {
    'normal': ['authenticated_roundtrip'],
    'permission_denied': ['request_rejected', 'forbidden_dial_zero', 'forbidden_effect_zero'],
    'maximum_ttl': ['deadline_not_extended'],
    'inactivity': ['idle_definition_confirmed'],
    'lease_loss': ['deadline_not_extended'],
    'disconnect': ['disconnect_observed'],
    'stop_unconfirmed': ['ui_end_unconfirmed', 'restart_blocked'],
    'stale_permission': ['fresh_approval_required'],
}


def pending_record():
    return {
        'version': 1, 'source_sha': '1' * 40,
        'artifact': {'zip_sha256': None, 'exe_sha256': None},
        'ci_run_ids': [], 'environment': {'os': 'windows', 'architecture': 'x64', 'build': None},
        'cases': [
            {'id': case, 'status': 'not_run', 'reason': 'implementation_pending',
             'elapsed_ms': None, 'limit_ms': None, 'evidence_ids': [],
             'checks': dict.fromkeys(COMMON + extra, 'unobserved')}
            for case, extra in EXTRA.items()
        ],
    }


def observed_record(case_id='normal'):
    record = pending_record()
    record['artifact'] = {'zip_sha256': '2' * 64, 'exe_sha256': '3' * 64}
    record['ci_run_ids'] = [123]
    record['environment']['build'] = '22631.1234'
    case = next(item for item in record['cases'] if item['id'] == case_id)
    case.update(status='pass', reason='none', elapsed_ms=100, limit_ms=1000, evidence_ids=[1])
    case['checks'] = dict.fromkeys(case['checks'], 'confirmed')
    return record, case


class AcceptanceRecordTests(unittest.TestCase):
    def run_record(self, record, *options):
        with tempfile.TemporaryDirectory(prefix='acceptance-record-') as directory:
            path = Path(directory) / 'record.json'
            path.write_text(json.dumps(record), encoding='utf-8')
            return self.run_path(path, *options)

    def run_path(self, path, *options):
        return subprocess.run(
            [sys.executable, '-I', str(ROOT / 'scripts/check_windows_acceptance_record.py'),
             '--record', str(path), *options], cwd=ROOT.parent,
            capture_output=True, text=True, timeout=10,
            env=dict(os.environ, PYTHONOPTIMIZE='2'))

    def test_pending_is_format_valid_and_explicitly_not_evaluated(self):
        result = self.run_record(pending_record())
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('FORMAT_VALID executed=0 not_run=8 must_stop=0', result.stdout)
        self.assertIn('acceptance=NOT_EVALUATED', result.stdout)

    def test_success_cannot_hide_unconfirmed_exit_or_elapsed_limit(self):
        for field in ('process_exit_reap', 'both_end_eof', 'reader_join', 'revoked',
                      'old_permission_rejected', 'job_cleanup', 'resource_restoration'):
            with self.subTest(field=field):
                record, case = observed_record()
                case['checks'][field] = 'unavailable'
                self.assertEqual(self.run_record(record).returncode, 1)
        record, case = observed_record()
        case['elapsed_ms'] = 1001
        self.assertEqual(self.run_record(record).returncode, 1)

    def test_expected_stop_failure_preserves_stop_even_when_scenario_passes(self):
        record, case = observed_record('stop_unconfirmed')
        case['checks']['process_exit_reap'] = 'unavailable'
        result = self.run_record(record, '--require-executed')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('must_stop=1 acceptance=NOT_EVALUATED', result.stdout)
        case['checks']['restart_blocked'] = 'failed'
        self.assertEqual(self.run_record(record).returncode, 1)

    def test_real_observation_requires_candidate_and_numeric_private_references(self):
        record, _ = observed_record()
        self.assertEqual(self.run_record(record, '--require-executed').returncode, 0)
        for field in ('zip_sha256', 'exe_sha256'):
            candidate = json.loads(json.dumps(record))
            candidate['artifact'][field] = None
            self.assertEqual(self.run_record(candidate).returncode, 1)
        record['ci_run_ids'] = []
        self.assertEqual(self.run_record(record).returncode, 1)
        result = self.run_record(pending_record(), '--require-executed')
        self.assertEqual(result.returncode, 1)

    def test_public_record_rejects_free_text_and_never_echoes_it(self):
        canaries = ['-----BEGIN OPENSSH PRIVATE KEY-----', 'token=synthetic-canary',
                    'password=synthetic-canary', 'HOST-SYNTHETIC', '192.0.2.45',
                    'S-1-5-21-111-222-333-444', r'C:\Users\synthetic\log.txt']
        for canary in canaries:
            for location in ('extra', 'reason', 'evidence', 'build', 'check'):
                with self.subTest(location=location, canary=canary):
                    record = pending_record()
                    if location == 'extra':
                        record[canary] = canary
                    elif location == 'reason':
                        record['cases'][0]['reason'] = canary
                    elif location == 'evidence':
                        record['cases'][0]['evidence_ids'] = [canary]
                    elif location == 'build':
                        record['environment']['build'] = canary
                    else:
                        record['cases'][0]['checks']['revoked'] = canary
                    result = self.run_record(record)
                    self.assertEqual(result.returncode, 1)
                    self.assertEqual(result.stderr, 'RECORD_INVALID format_or_incomplete\n')
                    self.assertNotIn(canary, result.stdout + result.stderr)

    def test_missing_duplicate_empty_and_malformed_records_are_rejected(self):
        for transform in (
            lambda r: r.update(cases=[]),
            lambda r: r['cases'].pop(),
            lambda r: r['cases'].__setitem__(1, r['cases'][0]),
            lambda r: r.update(version=True),
            lambda r: r['cases'][0].update(elapsed_ms=True),
            lambda r: r['cases'][0]['checks'].pop('reader_join'),
        ):
            record = pending_record()
            transform(record)
            self.assertEqual(self.run_record(record).returncode, 1)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'synthetic-private-path.json'
            for data in (b'', b'{bad-secret-json', b'[]', b'{"version":1,"version":1}',
                         b'\xff', b' ' * 65537, b'[' * 1200):
                path.write_bytes(data)
                result = self.run_path(path)
                self.assertEqual(result.returncode, 1)
                self.assertEqual(result.stderr, 'RECORD_INVALID format_or_incomplete\n')
            path.unlink()
            self.assertEqual(self.run_path(path).stderr, 'RECORD_INVALID format_or_incomplete\n')

    def test_failure_and_unavailable_remain_distinct_and_stop_following_work(self):
        for status, observation in (('fail', 'failed'), ('unavailable', 'unavailable')):
            record, case = observed_record()
            case.update(status=status, reason='observation_missing')
            case['checks']['process_exit_reap'] = observation
            result = self.run_record(record)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn('must_stop=1 acceptance=NOT_EVALUATED', result.stdout)

    def test_checked_in_template_remains_unexecuted(self):
        result = self.run_path(ROOT / 'docs/contracts/windows-acceptance-record.template.json')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('executed=0 not_run=8', result.stdout)

    def test_unexecuted_cannot_use_an_execution_failure_reason(self):
        record = pending_record()
        record['cases'][0]['reason'] = 'unexpected_result'
        self.assertEqual(self.run_record(record).returncode, 1)


if __name__ == '__main__':
    unittest.main()
