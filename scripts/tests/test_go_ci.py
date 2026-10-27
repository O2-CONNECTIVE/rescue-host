"""Synthetic event-stream checks for CI reporting, not product acceptance."""

import contextlib
import io
import json
from pathlib import Path
import sys
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from go_ci import test_events


class GoEventTests(unittest.TestCase):
    def events(self):
        module = 'github.com/O2-CONNECTIVE/rescue-host/backend'
        result = [
            {'Package': module, 'Test': 'TestPublicBoundary', 'Action': 'run'},
            {'Package': module, 'Test': 'TestPublicBoundary', 'Action': 'pass'},
            {'Package': module, 'Action': 'pass'},
        ]
        for name in ('auth', 'forward', 'lease'):
            result.append({'Package': module + '/internal/' + name, 'Action': 'skip'})
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


if __name__ == '__main__':
    unittest.main()
