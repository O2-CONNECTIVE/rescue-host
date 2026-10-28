"""試験専用traceの固定schemaと完全性。実driver/OSの証拠ではない。"""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


def trace(connection='unobserved'):
    events = []
    for index, (stage, state, kind) in enumerate([
            ('connection-open', 'idle', 'none'), ('session-received', 'received', 'none'),
            ('forward-start', 'forward-pending', 'none'),
            ('native-connection-' + connection, 'forward-pending', 'none'),
            ('request-dropped', 'dropped', 'none'), ('connection-end', 'dropped', 'incomplete')], 1):
        events.append({'type': 'event', 'seq': index, 'connection': 1,
                       'request': 0 if index == 1 else 1, 'stage': stage,
                       'state': state, 'elapsedUs': index * 100, 'error': kind})
    return [{'type': 'header', 'schema': 1, 'variant': 'tauri-driver-2.1.0-stage-2'}, *events,
            {'type': 'summary', 'lastSequence': 6, 'attempted': 6, 'enqueued': 6,
             'written': 6, 'dropped': 0, 'sealed': True, 'pendingRequests': 0}]


def encode(rows):
    return '\n'.join(json.dumps(row) for row in rows)


class DriverTraceTests(unittest.TestCase):
    def verify(self, raw):
        with tempfile.TemporaryDirectory() as folder:
            file = Path(folder) / 'trace.jsonl'
            file.write_text(raw, encoding='utf-8')
            result = subprocess.run([sys.executable, '-B', str(ROOT / 'scripts/verify_driver_trace.py'),
                                     '--trace', str(file)], capture_output=True, text=True, timeout=10)
            self.assertNotIn(str(file), result.stdout)
            return result

    def test_terminal_connection_assignment_is_distinct_from_native_headers(self):
        rows = trace('assigned')
        result = self.verify(encode(rows))
        self.assertEqual(result.returncode, 0, result.stderr)
        report = json.loads(result.stdout)
        self.assertEqual(report['requestStage'], 'dropped-before-headers')
        self.assertEqual(report['nativeConnection'], 'assigned')

    def test_same_post_forward_drop_and_connection_eof_are_correlated(self):
        result = self.verify(encode(trace()))
        self.assertEqual(result.returncode, 0, result.stderr)
        report = json.loads(result.stdout)
        self.assertEqual(report['diagnostic'], 'complete')
        self.assertEqual(report['requestStage'], 'dropped-before-headers')
        self.assertEqual(report['connectionEnd'], 'incomplete')
        self.assertEqual(result.stderr, '')

    def test_service_future_drop_after_incoming_eof_keeps_same_request_correlation(self):
        rows = trace()
        dropped, ended = rows[5], rows[6]
        rows[5], rows[6] = ended, dropped
        rows[5].update(seq=5, elapsedUs=500, state='forward-pending')
        rows[6].update(seq=6, elapsedUs=600)
        result = self.verify(encode(rows))
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
            result = self.verify(encode(rows))
            self.assertEqual(result.returncode, 1)
            self.assertEqual(json.loads(result.stdout)['diagnostic'], 'incomplete')

    def test_missing_overflow_inconsistent_or_secret_trace_is_incomplete(self):
        # 実装後の完全性/非開示回帰。
        cases = [trace()[:-1]]
        for update in [{'attempted': 7, 'lastSequence': 7, 'dropped': 1},
                       {'written': 5}, {'sealed': False, 'pendingRequests': 1}]:
            rows = trace()
            rows[-1].update(update)
            cases.append(rows)
        rows = trace()
        rows[3]['authorization'] = 'PRIVATE_AUTH_MARKER'
        cases.append(rows)
        rows = trace()
        rows[6]['request'] = 2
        cases.append(rows)
        rows = trace()
        rows[4]['seq'] = 3
        cases.append(rows)
        rows = trace()
        rows[3]['stage'] = 'PRIVATE_STAGE_MARKER'
        cases.append(rows)
        cases.append([*trace(), trace()[1]])
        raw_cases = [encode(rows) for rows in cases]
        raw_cases.extend(['PRIVATE_TOO_BIG_' + 'x' * 8193,
                          json.dumps(trace()[0]).replace('"schema": 1', '"schema":1,"schema":1')])
        for index, raw in enumerate(raw_cases):
            with self.subTest(case=index):
                result = self.verify(raw)
                self.assertEqual(result.returncode, 1)
                record = json.loads(result.stdout)
                self.assertEqual(record['diagnostic'], 'incomplete')
                self.assertEqual(record['requestStage'], 'unconfirmed')
                self.assertEqual(result.stderr, '')
                self.assertNotIn('PRIVATE_', result.stdout)

    def test_header_and_native_error_are_distinct_from_drop(self):
        # 実装後のstage分類回帰。
        for stage, state, expected in [('native-headers', 'headers-returned', 'native-headers'),
                                       ('native-error', 'native-error', 'native-error')]:
            rows = trace()
            rows[5].update(stage=stage, state=state)
            rows[6].update(state=state, error='none')
            result = self.verify(encode(rows))
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout)['requestStage'], expected)

    def test_monotonic_time_must_not_regress_across_connections(self):
        rows = trace()
        rows.insert(-1, {'type': 'event', 'seq': 7, 'connection': 2, 'request': 0,
                         'stage': 'connection-open', 'state': 'idle', 'elapsedUs': 1, 'error': 'none'})
        rows.insert(-1, {'type': 'event', 'seq': 8, 'connection': 2, 'request': 0,
                         'stage': 'connection-end', 'state': 'idle', 'elapsedUs': 2, 'error': 'none'})
        rows[-1].update(lastSequence=8, attempted=8, enqueued=8, written=8)
        result = self.verify(encode(rows))
        self.assertEqual(result.returncode, 1)
        self.assertEqual(json.loads(result.stdout)['diagnostic'], 'incomplete')


if __name__ == '__main__':
    unittest.main()
