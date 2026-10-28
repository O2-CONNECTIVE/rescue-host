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
                       'internal/lease', 'internal/stdio', 'cmd/rescue-backend', 'supervisor', 'servicehost',
                       'supervisor/ephemeral', 'internal/localcontrol',
                       'internal/runbackend', 'internal/childjob',
                       'internal/protectedkey', 'internal/bootstrap',
                       'internal/bootstrapio', 'internal/bootstrapauth', 'internal/childstdio', 'internal/serviceready'):
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
        for target, selected, excluded, production_count in (
                ('linux', 'scm_other.go', 'scm_windows.go', 68),
                ('darwin', 'scm_other.go', 'scm_windows.go', 68),
                ('windows', 'scm_windows.go', 'scm_other.go', 73)):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[package], {'control.go', 'host.go', 'approved.go',
                                                      'job.go', 'ownercontrol.go', 'issued.go', 'nativebootstrap.go',
                                                      'nativestdio.go', 'ownedstdio.go', 'scm_ready.go', 'service.go',
                                                      'scm_ready_windows.go' if target == 'windows' else 'scm_ready_other.go',
                                                      'nativebootstrap_windows.go' if target == 'windows' else 'nativebootstrap_other.go', selected})
                self.assertNotIn(excluded, inventory[package])
                self.assertEqual(len(inventory), 19)
                self.assertEqual(sum(map(len, inventory.values())), production_count)

    def test_localcontrol_sources_follow_selected_go_target(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/localcontrol'
        for target, selected, excluded, production_count in (
                ('linux', {'native_other.go'}, 'native_windows.go', 68),
                ('darwin', {'native_other.go'}, 'native_windows.go', 68),
                ('windows', {'native_windows.go', 'io_windows.go'}, 'native_other.go', 73)):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[package], {'policy.go', 'boundary.go', 'wire.go'} | selected)
                self.assertNotIn(excluded, inventory[package])
                if target != 'windows':
                    self.assertNotIn('io_windows.go', inventory[package])
                self.assertEqual(len(inventory), 19)
                self.assertEqual(sum(map(len, inventory.values())), production_count)

    def test_runtime_and_childjob_sources_follow_selected_go_target(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/'
        for target, selected, excluded, production_count in (
                ('linux', {'native_other.go'}, {'native_windows.go', 'artifact_windows.go'}, 68),
                ('darwin', {'native_other.go'}, {'native_windows.go', 'artifact_windows.go'}, 68),
                ('windows', {'native_windows.go', 'artifact_windows.go'}, {'native_other.go'}, 73)):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[prefix + 'runbackend'], {'runtime.go'})
                self.assertEqual(inventory[prefix + 'childjob'], {'child.go', 'plan.go'} | selected)
                self.assertFalse(inventory[prefix + 'childjob'] & excluded)
                self.assertEqual(len(inventory), 19)
                self.assertEqual(sum(map(len, inventory.values())), production_count)

    def test_protectedkey_and_bootstrap_sources_follow_selected_go_target(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/'
        common = {'acl.go', 'format.go', 'loader.go', 'profile.go', 'scope.go',
                  'source.go', 'validation.go'}
        for target, selected, excluded, production_count in (
                ('linux', 'native_other.go', 'native_windows.go', 68),
                ('darwin', 'native_other.go', 'native_windows.go', 68),
                ('windows', 'native_windows.go', 'native_other.go', 73)):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[prefix + 'protectedkey'], common | {selected})
                self.assertNotIn(excluded, inventory[prefix + 'protectedkey'])
                self.assertEqual(inventory[prefix + 'bootstrap'],
                                 {'codec.go', 'launch.go', 'native_receiver.go', 'strict.go', 'types.go', 'validate.go',
                                  'native_receiver_windows.go' if target == 'windows' else 'native_receiver_other.go'})
                self.assertEqual(len(inventory), 19)
                self.assertEqual(sum(map(len, inventory.values())), production_count)

    def test_native_bootstrap_sources_follow_selected_go_target(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/'
        auth_common = {'auth.go', 'image.go', 'kernel.go', 'profile.go', 'validation.go', 'prepared_service.go'}
        for target, auth_native, io_native, production_count in (
                ('linux', {'native_other.go'}, 'native_other.go', 68),
                ('darwin', {'native_other.go'}, 'native_other.go', 68),
                ('windows', {'native_windows.go', 'image_windows.go'}, 'native_windows.go', 73)):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[prefix + 'internal/bootstrapauth'], auth_common | auth_native)
                self.assertEqual(inventory[prefix + 'internal/bootstrapio'], {'io.go', io_native})
                self.assertEqual(inventory[prefix + 'cmd/rescue-backend'], {'main.go', 'native.go'})
                self.assertEqual(len(inventory), 19)
                self.assertEqual(sum(map(len, inventory.values())), production_count)
                if target != 'windows':
                    self.assertNotIn('image_windows.go', inventory[prefix + 'internal/bootstrapauth'])

    def test_owned_stdio_sources_follow_selected_go_target(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/'
        for target, selected, excluded in (
                ('linux', 'native_other.go', 'native_windows.go'),
                ('darwin', 'native_other.go', 'native_windows.go'),
                ('windows', 'native_windows.go', 'native_other.go')):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[prefix + 'internal/childstdio'],
                                 {'operation.go', 'pipes.go', 'streams.go', selected})
                self.assertNotIn(excluded, inventory[prefix + 'internal/childstdio'])
                self.assertTrue({'nativestdio.go', 'ownedstdio.go'} <= inventory[prefix + 'servicehost'])
                self.assertEqual(len(inventory), 19)
                self.assertEqual(sum(map(len, inventory.values())), 73 if target == 'windows' else 68)

    def test_zero_grant_ready_sources_follow_selected_go_target(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/'
        common = {'config.go', 'operation.go', 'plane.go', 'security.go'}
        for target, selected, excluded in (
                ('linux', {'native_other.go'}, {'native_windows.go', 'connect_windows.go', 'listener_windows.go'}),
                ('darwin', {'native_other.go'}, {'native_windows.go', 'connect_windows.go', 'listener_windows.go'}),
                ('windows', {'native_windows.go', 'connect_windows.go', 'listener_windows.go'}, {'native_other.go'})):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[prefix + 'internal/serviceready'], common | selected)
                self.assertFalse(inventory[prefix + 'internal/serviceready'] & excluded)
                self.assertIn('prepared_service.go', inventory[prefix + 'internal/bootstrapauth'])
                self.assertTrue({'scm_ready.go', 'service.go'} <= inventory[prefix + 'servicehost'])
                self.assertIn('scm_ready_windows.go' if target == 'windows' else 'scm_ready_other.go',
                              inventory[prefix + 'servicehost'])
                self.assertEqual(len(inventory), 19)
                self.assertEqual(sum(map(len, inventory.values())), 73 if target == 'windows' else 68)

    def test_unknown_go_target_is_rejected(self):
        for target in ('', 'plan9', 'windows/../linux'):
            with self.subTest(target=target), self.assertRaises(ValueError):
                go_ci.product_files_for(target)


if __name__ == '__main__':
    unittest.main()
