"""試験専用traceの固定schemaと完全性。実driver/OSの証拠ではない。"""
import json
import copy
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


def trace():
    events = []
    for index, (stage, state, kind) in enumerate([
            ('connection-open', 'idle', 'none'), ('session-received', 'received', 'none'),
            ('forward-start', 'forward-pending', 'none'),
            ('request-dropped', 'dropped', 'none'), ('connection-end', 'dropped', 'incomplete')], 1):
        events.append({'type': 'event', 'seq': index, 'connection': 1,
                       'request': 0 if index == 1 else 1, 'stage': stage,
                       'state': state, 'elapsedUs': index * 100, 'error': kind})
    return [{'type': 'header', 'schema': 1, 'variant': 'tauri-driver-2.1.0-stage-1'}, *events,
            {'type': 'summary', 'lastSequence': 5, 'attempted': 5, 'enqueued': 5,
             'written': 5, 'dropped': 0, 'sealed': True, 'pendingRequests': 0}]


class DriverTraceTests(unittest.TestCase):
    def test_same_post_forward_drop_and_connection_eof_are_correlated(self):
        with tempfile.TemporaryDirectory() as folder:
            file = Path(folder) / 'trace.jsonl'
            file.write_text('\n'.join(json.dumps(row) for row in trace()), encoding='utf-8')
            result = subprocess.run([sys.executable, '-B', str(ROOT / 'scripts/verify_driver_trace.py'),
                                     '--trace', str(file)], capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            report = json.loads(result.stdout)
            self.assertEqual(report['diagnostic'], 'complete')
            self.assertEqual(report['requestStage'], 'dropped-before-headers')
            self.assertEqual(report['connectionEnd'], 'incomplete')
            self.assertEqual(result.stderr, '')

    def test_service_future_drop_after_incoming_eof_keeps_same_request_correlation(self):
        rows = trace()
        dropped, ended = rows[4], rows[5]
        rows[4], rows[5] = ended, dropped
        rows[4].update(seq=4, elapsedUs=400, state='forward-pending')
        rows[5].update(seq=5, elapsedUs=500)
        with tempfile.TemporaryDirectory() as folder:
            file = Path(folder) / 'trace.jsonl'
            file.write_text('\n'.join(json.dumps(row) for row in rows), encoding='utf-8')
            result = subprocess.run([sys.executable, '-B', str(ROOT / 'scripts/verify_driver_trace.py'),
                                     '--trace', str(file)], capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            report = json.loads(result.stdout)
            self.assertEqual(report['diagnostic'], 'complete')
            self.assertEqual(report['requestStage'], 'dropped-before-headers')
            self.assertEqual(report['connectionEnd'], 'incomplete')

    def test_boolean_schema_and_regressing_monotonic_stage_are_incomplete(self):
        for mutate in [lambda rows: rows[0].update(schema=True),
                       lambda rows: rows[3].update(elapsedUs=1)]:
            rows = trace()
            mutate(rows)
            with tempfile.TemporaryDirectory() as folder:
                file = Path(folder) / 'trace.jsonl'
                file.write_text('\n'.join(json.dumps(row) for row in rows), encoding='utf-8')
                result = subprocess.run([sys.executable, '-B', str(ROOT / 'scripts/verify_driver_trace.py'),
                                         '--trace', str(file)], capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 1)
                self.assertEqual(json.loads(result.stdout)['diagnostic'], 'incomplete')


if __name__ == '__main__':
    unittest.main()
