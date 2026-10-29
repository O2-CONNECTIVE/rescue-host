"""Synthetic event-stream checks for CI reporting, not product acceptance."""

import contextlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from go_ci import test_events
import go_ci


class GoEventTests(unittest.TestCase):
    def test_accepted_transport_is_explicitly_in_product_inventory(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                self.assertEqual(go_ci.product_files_for(target)[package],
                                 {'activity.go', 'session.go', 'transport.go'})

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
                       'internal/bootstrapio', 'internal/bootstrapauth', 'internal/childstdio', 'internal/serviceready',
                       'internal/serviceprovision', 'cmd/rescue-service-host', 'cmd/rescue-auth-helper', 'internal/servicecontrol', 'internal/operatorauth', 'internal/serviceauthowner', 'internal/guestaccount'):
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

    def test_each_service_provision_package_result_is_required(self):
        prefix = 'github.com/O2-CONNECTIVE/rescue-host/backend/'
        for suffix in ('internal/serviceprovision', 'cmd/rescue-service-host', 'cmd/rescue-auth-helper'):
            with self.subTest(package=suffix), self.assertRaisesRegex(ValueError, 'Missing package test result'):
                self.check([event for event in self.events() if event['Package'] != prefix + suffix])

    def test_guest_package_result_is_required(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/guestaccount'
        with self.assertRaisesRegex(ValueError, 'Missing package test result'):
            self.check([event for event in self.events() if event['Package'] != package])

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
    def test_concrete_auth_owner_is_required_and_limited(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/serviceauthowner'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                self.assertIn(package, go_ci.product_files_for(target))
                self.assertEqual(go_ci.product_files_for(target)[package], {'owner.go'})
                for fault in ('missing-package', 'missing-file', 'unknown-file'):
                    actual = go_ci.product_files_for(target)
                    if fault == 'missing-package':
                        del actual[package]
                    elif fault == 'missing-file':
                        actual[package].remove('owner.go')
                    else:
                        actual[package].add('owner_unreviewed.go')
                    self.assert_build_boundary_rejected(target, actual)

    def test_workload_reclaim_source_is_required_and_limited(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/servicehost'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                self.assertIn('workload_reclaim.go', go_ci.product_files_for(target)[package])
                actual = go_ci.product_files_for(target)
                actual[package] = (actual[package] | {'workload_reclaim.go'}) - {'workload_reclaim.go'}
                self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('workload_unreviewed.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_auth_exchange_ownership_sources_are_required_and_limited(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/servicehost'
        required = {'auth_exchange.go', 'auth_exchange_endpoint.go', 'auth_exchange_worker.go'}
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                self.assertTrue(required <= go_ci.product_files_for(target)[package])
                for missing in required:
                    actual = go_ci.product_files_for(target)
                    actual[package].remove(missing)
                    self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('auth_exchange_unreviewed.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_service_auth_adapter_source_is_required_and_limited(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/servicehost'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                self.assertIn('auth_dispatch.go', go_ci.product_files_for(target)[package])
                actual = go_ci.product_files_for(target)
                actual[package] = (actual[package] | {'auth_dispatch.go'}) - {'auth_dispatch.go'}
                self.assert_build_boundary_rejected(target, actual)
                actual = go_ci.product_files_for(target)
                actual[package].add('auth_unreviewed.go')
                self.assert_build_boundary_rejected(target, actual)

    def test_auth_helper_command_is_explicit_and_required(self):
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/cmd/rescue-auth-helper'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory.get(package), {'main.go'})
                for fault in ('missing-package', 'missing-main', 'unknown-file'):
                    actual = go_ci.product_files_for(target)
                    if fault == 'missing-package':
                        del actual[package]
                    elif fault == 'missing-main':
                        actual[package].remove('main.go')
                    else:
                        actual[package].add('unreviewed.go')
                    self.assert_build_boundary_rejected(target, actual)

    def assert_build_boundary_rejected(self, target, actual):
        metadata = '\n'.join(json.dumps({'ImportPath': package, 'GoFiles': sorted(files)})
                             for package, files in actual.items())
        results = [subprocess.CompletedProcess([], 0, stdout=target),
                   subprocess.CompletedProcess([], 0, stdout=metadata)]
        with patch.object(go_ci, 'command', side_effect=results) as calls, \
                contextlib.redirect_stdout(io.StringIO()), \
                self.assertRaisesRegex(ValueError, 'Production file/package boundary changed'):
            go_ci.build()
        self.assertEqual(calls.call_count, 2, 'Invalid inventory reached vet/build')

    def test_guest_sources_are_explicit_for_each_target(self):
        common = {'close.go', 'lifecycle.go', 'model.go', 'plan.go', 'prepare.go',
                  'recovery.go', 'source.go', 'validation.go', 'account_query.go',
                  'account_query_buffer.go', 'account_query_read.go', 'journal.go',
                  'journal_codec.go', 'journal_security.go', 'sid_codec.go',
                  'sid_run.go', 'sid_run_io.go', 'sid_script.go', 'sid_source.go',
                  'creation_codec.go', 'creation_native.go', 'creation_query.go',
                  'creation_script.go', 'creation_source.go', 'sid_script_parts.go',
                  'permission_aliases.go', 'permission_aliases_buffer.go',
                  'permission_aliases_native.go', 'permission_aliases_read.go',
                  'permission_creation.go', 'permission_facts.go', 'permission_facts_buffer.go',
                  'permission_groups.go', 'permission_inspect.go', 'permission_objects.go',
                  'permission_objects_buffer.go', 'permission_policy.go',
                  'staging.go', 'staging_borrow.go', 'staging_native.go',
                  'staging_profile.go', 'staging_query.go',
                  'fixed_layout.go', 'fixed_layout_strict.go', 'fixed_layout_validate.go', 'fixed_layout_wire.go'}
        package = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/guestaccount'
        child = 'github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob'
        for target in ('linux', 'darwin', 'windows'):
            with self.subTest(target=target):
                selected = ({'account_query_windows.go', 'account_query_windows_machine.go',
                             'journal_windows.go', 'journal_windows_pin.go', 'creation_query_windows.go',
                             'permission_aliases_windows.go', 'permission_facts_windows.go',
                             'permission_native_windows.go', 'permission_objects_windows.go',
                             'permission_objects_windows_identity.go', 'staging_native_windows.go'} if target == 'windows'
                            else {'account_query_other.go', 'journal_other.go', 'creation_query_other.go',
                                  'permission_aliases_other.go', 'permission_facts_other.go',
                                  'permission_native_other.go', 'permission_objects_other.go', 'staging_native_other.go'})
                inventory = go_ci.product_files_for(target)
                self.assertEqual(inventory[package], common | selected)
                self.assertIn('fixed_command.go', inventory[child])
                self.assertEqual(len(inventory), 26)
                self.assertEqual(sum(map(len, inventory.values())), 237 if target == 'windows' else 215)
