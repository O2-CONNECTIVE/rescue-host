"""Synthetic event-stream checks for CI reporting, not product acceptance."""

import contextlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from go_ci import test_events
import go_ci


class GoEventTests(unittest.TestCase):
    def events(self):
        module = 'github.com/O2-CONNECTIVE/rescue-host/backend'
        result = [
            {'Package': module, 'Test': 'TestPublicBoundary', 'Action': 'run'},
            {'Package': module, 'Test': 'TestPublicBoundary', 'Action': 'pass'},
            {'Package': module, 'Action': 'pass'},
        ]
        for suffix in ('internal/auth', 'internal/control', 'internal/forward',
                       'internal/lease', 'internal/stdio', 'cmd/rescue-backend', 'supervisor', 'servicehost'):
            result.append({'Package': module + '/' + suffix, 'Action': 'skip'})
        return result

    def check(self, events, code=0):
        with contextlib.redirect_stdout(io.StringIO()):
            return test_events('\n'.join(json.dumps(event) for event in events), code)

    def test_one_passing_test_is_counted(self):
        self.assertEqual(self.check(self.events()), {'pass': 1, 'skip': 0, 'fail': 0})

    def test_empty_stream_is_rejected(self):
        with self.assertRaises(ValueError):
            self.check([])

    def test_package_without_tests_is_not_counted_as_pass(self):
        with self.assertRaises(ValueError):
            self.check([event for event in self.events() if 'Test' not in event])

    def test_all_skipped_tests_are_rejected(self):
        events = self.events()
        events[1]['Action'] = 'skip'
        with self.assertRaises(ValueError):
            self.check(events)

    def test_go_exit_failure_overrides_passing_events(self):
        with self.assertRaises(ValueError):
            self.check(self.events(), 1)

    def test_failed_subtest_is_not_hidden_by_parent_pass(self):
        events = self.events()
        events.append({'Package': events[0]['Package'], 'Test': 'TestPublicBoundary/child', 'Action': 'fail'})
        with self.assertRaises(ValueError):
            self.check(events)

    def test_unfinished_test_is_rejected(self):
        events = self.events()
        events.append({'Package': events[0]['Package'], 'Test': 'TestUnfinished', 'Action': 'run'})
        with self.assertRaises(ValueError):
            self.check(events)

    def test_missing_package_is_rejected(self):
        with self.assertRaises(ValueError):
            self.check(self.events()[:-1])

    def test_broken_json_is_rejected(self):
        with self.assertRaises(ValueError), contextlib.redirect_stdout(io.StringIO()):
            test_events('{', 0)

    def test_observed_cpu_matches_requested_lane(self):
        events = self.events()
        events.append({'Package': events[0]['Package'], 'Test': 'TestPublicBoundary',
                       'Action': 'output', 'Output': 'observed_test_GOMAXPROCS=2\n'})
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(test_events('\n'.join(map(json.dumps, events)), 0, expected_cpu='2')['pass'], 1)
            with self.assertRaises(ValueError):
                test_events('\n'.join(map(json.dumps, events)), 0, expected_cpu='1')

    def test_missing_public_test_is_rejected(self):
        expected = {(self.events()[0]['Package'], 'TestPublicBoundary'),
                    (self.events()[0]['Package'], 'TestRequiredBoundary')}
        with self.assertRaises(ValueError), contextlib.redirect_stdout(io.StringIO()):
            test_events('\n'.join(map(json.dumps, self.events())), 0, expected_tests=expected)

    def test_new_skip_is_rejected_even_with_another_pass(self):
        events = self.events()
        events.append({'Package': events[0]['Package'], 'Test': 'TestOtherBoundary', 'Action': 'skip'})
        with self.assertRaises(ValueError):
            self.check(events)


class GoResourceRecordTests(unittest.TestCase):
    COUNTERS = ('test_exit_code', 'remaining_listeners',
                'remaining_connections', 'forbidden_dial_attempts')

    def record(self):
        return {**dict.fromkeys(self.COUNTERS, 0), 'duration_ms': 1,
                'listener_addresses': [], 'dial_addresses': []}

    def check(self, record):
        with tempfile.TemporaryDirectory() as directory:
            module = Path(directory)
            path = module / 'go-result.json'
            path.write_text(json.dumps(record), encoding='utf-8')
            try:
                with patch.object(go_ci, 'MODULE', module), contextlib.redirect_stdout(io.StringIO()):
                    go_ci.check_network_record()
            finally:
                self.assertFalse(path.exists(), 'Synthetic resource record must be removed')

    def test_integer_zero_counters_are_accepted(self):
        self.check(self.record())

    def test_noninteger_counters_are_rejected(self):
        for key in self.COUNTERS:
            for value in (False, True, 0.0, None, '0'):
                with self.subTest(key=key, value=value):
                    record = self.record()
                    record[key] = value
                    with self.assertRaises(ValueError):
                        self.check(record)

    def test_missing_counters_are_rejected(self):
        for key in self.COUNTERS:
            with self.subTest(key=key):
                record = self.record()
                del record[key]
                with self.assertRaises(ValueError):
                    self.check(record)

    def test_nonzero_counters_are_rejected(self):
        for key in self.COUNTERS:
            for value in (-1, 1):
                with self.subTest(key=key, value=value):
                    record = self.record()
                    record[key] = value
                    with self.assertRaises(ValueError):
                        self.check(record)


class GoProductionInventoryTests(unittest.TestCase):
    def test_scm_sources_follow_selected_go_target(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/servicehost'
        for target, selected, excluded in (
                ('linux', 'scm_other.go', 'scm_windows.go'),
                ('darwin', 'scm_other.go', 'scm_windows.go'),
                ('windows', 'scm_windows.go', 'scm_other.go')):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[package], {'control.go', 'host.go', selected})
                self.assertNotIn(excluded, inventory[package])
                self.assertEqual(len(inventory), 9)
                self.assertEqual(sum(map(len, inventory.values())), 12)

    def test_unknown_go_target_is_rejected(self):
        for target in ('', 'plan9', 'windows/../linux'):
            with self.subTest(target=target), self.assertRaises(ValueError):
                go_ci.product_files_for(target)


if __name__ == '__main__':
    unittest.main()
