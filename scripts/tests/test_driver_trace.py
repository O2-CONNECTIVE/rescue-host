"""試験専用traceの固定schemaと完全性。実driver/OSの証拠ではない。"""
import json
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


if __name__ == '__main__':
    unittest.main()
