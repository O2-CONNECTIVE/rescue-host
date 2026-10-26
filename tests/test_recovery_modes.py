"""オフラインで行う復旧と設定のテスト。Tk の画面表示や実際の接続は不要。"""
import json
import os
from pathlib import Path
import queue
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'src'))
from rescue_core import (RescueError, Settings, get_session_info, load_settings,
                         save_settings, record_session_state, session_state_path,
                         remove_session_state_if_name, stop_session)
from rescue_host import RescueApp
from rescue_watchdog import watchdog_main
from test_core import key_line

NAME = 'rescue-0123456789ab'


class RecoveryModeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.folder = Path(self.tmp.name)
        env = patch.dict(os.environ, {'XDG_DATA_HOME': self.tmp.name, 'LOCALAPPDATA': self.tmp.name})
        env.start()
        self.addCleanup(env.stop)

    def config(self, data):
        (self.folder / 'config.json').write_text(json.dumps(data))
        return load_settings(self.folder)

    def test_non_object_config_is_controlled_error(self):
        for data in ([], None, 'text', 3, True):
            with self.subTest(data=data), self.assertRaises(RescueError):
                self.config(data)

    def test_wrong_field_types_rejected(self):
        for field in ('relay', 'mode', 'operation_mode', 'public_keys', 'external_upterm', 'custom_known_hosts'):
            with self.subTest(field=field), self.assertRaises(RescueError):
                self.config({'schema_version': 2, field: []})

    def test_unknown_schema_and_modes_rejected(self):
        for data in ({'schema_version': True}, {'schema_version': 3}, {'schema_version': 2, 'operation_mode': 'admin'}, {'mode': 'other'}):
            with self.subTest(data=data), self.assertRaises(RescueError):
                self.config(data)

    def test_legacy_terminal_migrates_to_restricted(self):
        settings = self.config({'mode': 'terminal', 'operation_mode': 'full'})
        self.assertEqual(settings.mode, 'terminal')
        self.assertEqual(settings.operation_mode, 'restricted')
        self.assertEqual(settings.schema_version, 2)

    def test_modes_roundtrip_separately(self):
        for mode in ('restricted', 'full'):
            settings = Settings(operation_mode=mode, mode='terminal', public_keys=key_line())
            save_settings(settings, self.folder)
            self.assertEqual(load_settings(self.folder), settings)

    def test_settings_policy_bridge_modes_are_immutable_and_distinct(self):
        from dataclasses import FrozenInstanceError
        policies = []
        for mode in ('restricted', 'full'):
            settings = Settings(operation_mode=mode, mode='terminal')
            policy = settings.to_operation_policy()
            self.assertEqual(policy.operation_mode, mode)
            self.assertEqual(policy.allows_inner_shell, mode == 'full')
            self.assertFalse(policy.allows_admin)
            self.assertTrue(policy.permits_channel('direct-tcpip', '127.0.0.1', 22))
            with self.assertRaises(FrozenInstanceError):
                policy.operation_mode = 'full'
            settings.operation_mode = 'restricted' if mode == 'full' else 'full'
            self.assertEqual(policy.operation_mode, mode)
            policies.append(policy)
        self.assertNotEqual(policies[0].digest, policies[1].digest)

    def test_settings_policy_bridge_legacy_migration_restricted(self):
        settings = self.config({'mode': 'terminal', 'operation_mode': 'full'})
        self.assertEqual(settings.to_operation_policy().operation_mode, 'restricted')

    def test_settings_policy_bridge_rejects_invalid_settings(self):
        for settings in (Settings(operation_mode='admin'), Settings(schema_version=3), Settings(duration_minutes=True)):
            with self.assertRaises(RescueError):
                settings.to_operation_policy()

    def test_identityless_status_never_injects_name(self):
        for status in ('ready', 'ended'):
            result = subprocess.CompletedProcess([], 0, json.dumps({'status': status}), '')
            with patch('rescue_core.run_upterm', return_value=result), self.assertRaises(RescueError):
                get_session_info(Path('fake'), NAME)

    def test_named_ended_is_not_legacy_revocation_proof(self):
        result = subprocess.CompletedProcess([], 0, '', '')
        with patch('rescue_core.run_upterm', return_value=result), patch('rescue_core.get_session_info', return_value={'name': NAME, 'status': 'ended'}):
            with self.assertRaises(RescueError):
                stop_session(Path('fake'), NAME)

    def test_ended_poll_preserves_legacy_evidence(self):
        app = object.__new__(RescueApp)
        app.session = {'name': NAME, 'deadline': 1}
        app._log = Mock()
        with patch('rescue_host.remove_session_state_if_name') as cleanup:
            app._apply_info({'name': NAME, 'status': 'ended', 'launch_id': 'forged'})
        cleanup.assert_not_called()
        self.assertIsNotNone(app.session)

    def test_event_error_reschedules_and_next_drain_runs(self):
        app = object.__new__(RescueApp)
        app.root = Mock()
        app.events = queue.Queue()
        app.events.put(('info_ok', {'name': NAME, 'status': 'ended'}))
        app.events.put(('info_error', 'retry'))
        app.session = {'name': NAME}
        app.busy = app.poll_busy = app.expiry_stop_attempted = True
        app._apply_info = Mock(side_effect=PermissionError('cleanup denied'))
        app._refresh_state = Mock()
        app._log = Mock()
        app._drain_events()
        app.root.after.assert_called_with(150, app._drain_events)
        self.assertIsNotNone(app.session)
        self.assertFalse(app.expiry_stop_attempted)
        app._drain_events()
        self.assertTrue(app.events.empty())
        self.assertEqual(app.root.after.call_count, 2)

    def test_key_cleanup_permission_error_retains_record(self):
        record_session_state({'name': NAME, 'deadline': 1}, self.folder)
        original_unlink = Path.unlink
        def fail_key(path, *args, **kwargs):
            if path.name.endswith('.authorized_keys'):
                raise PermissionError('denied')
            return original_unlink(path, *args, **kwargs)
        with patch.object(Path, 'unlink', fail_key), self.assertRaises(PermissionError):
            remove_session_state_if_name(NAME, self.folder)
        self.assertTrue(session_state_path(self.folder).exists())
        remove_session_state_if_name(NAME, self.folder)
        self.assertFalse(session_state_path(self.folder).exists())

    def test_watchdog_permission_cleanup_retries_and_fails_closed(self):
        with patch('rescue_watchdog.resolve_upterm', return_value=Path('fake')), patch('rescue_watchdog.stop_session'), patch('rescue_watchdog.remove_session_state_if_name', side_effect=PermissionError('denied')) as cleanup, patch('rescue_watchdog.time.sleep'):
            self.assertEqual(watchdog_main(NAME, '1', 'fake'), 1)
        self.assertEqual(cleanup.call_count, 5)

    def test_watchdog_unverified_ended_preserves_record(self):
        from rescue_core import data_dir
        record_session_state({'name': NAME, 'deadline': 1})
        result = subprocess.CompletedProcess([], 0, '', '')
        with patch('rescue_watchdog.resolve_upterm', return_value=Path('fake')), patch('rescue_core.run_upterm', return_value=result), patch('rescue_core.get_session_info', return_value={'name': NAME, 'status': 'ended'}), patch('rescue_watchdog.time.sleep'):
            self.assertEqual(watchdog_main(NAME, '1', 'fake'), 1)
        self.assertTrue(session_state_path().exists())
        self.assertIn('UNCONFIRMED', (data_dir() / 'watchdog.log').read_text())

    def test_full_selection_cancel_restores_restricted(self):
        app = object.__new__(RescueApp)
        app.operation_var = Mock()
        app.operation_var.get.return_value = 'full'
        app._operation_note = Mock()
        with patch('rescue_host.messagebox.askyesno', return_value=False):
            app._operation_changed()
        app.operation_var.set.assert_called_once_with('restricted')
        app._operation_note.assert_called_once()

    def test_full_selection_is_preference_not_launch(self):
        app = object.__new__(RescueApp)
        app.operation_var = Mock()
        app.operation_var.get.return_value = 'full'
        app._operation_note = Mock()
        with patch('rescue_host.messagebox.askyesno', return_value=True), patch('rescue_host.run_upterm') as run:
            app._operation_changed()
        app.operation_var.set.assert_not_called()
        run.assert_not_called()

    def test_gate_blocked_for_both_operation_modes(self):
        app = object.__new__(RescueApp)
        for mode in ('restricted', 'full'):
            with patch('rescue_host.run_upterm') as run, self.assertRaises(RescueError):
                app._start_worker(Settings(operation_mode=mode), Path('fake'))
            run.assert_not_called()

    def test_close_unconfirmed_accept_retains_evidence(self):
        record_session_state({'name': NAME, 'deadline': 1})
        app = object.__new__(RescueApp)
        app.root = Mock()
        app.busy = False
        app.session = {'name': NAME, 'deadline': 1}
        with patch('rescue_host.messagebox.askyesno', return_value=True) as prompt, patch('rescue_host.stop_session') as stop, patch('rescue_host.remove_session_state_if_name') as cleanup:
            app._close_unconfirmed()
        self.assertEqual(prompt.call_args.kwargs['default'], 'no')
        app.root.destroy.assert_called_once()
        stop.assert_not_called()
        cleanup.assert_not_called()
        self.assertTrue(session_state_path().exists())
        self.assertIsNotNone(app.session)

    def test_close_unconfirmed_cancel_or_busy_stays_open(self):
        app = object.__new__(RescueApp)
        app.root = Mock()
        app.session = {'name': NAME}
        for busy in (False, True):
            app.busy = busy
            with patch('rescue_host.messagebox.askyesno', return_value=False), patch('rescue_host.messagebox.showwarning'):
                app._close_unconfirmed()
        app.root.destroy.assert_not_called()

    def test_corrupt_settings_does_not_block_recovery_scheduling(self):
        app = object.__new__(RescueApp)
        root = Mock()
        state = {'name': NAME, 'deadline': 1}
        with patch('rescue_host.load_session_state', return_value=state), patch('rescue_host.load_settings', side_effect=RescueError('bad config')), patch('rescue_host.messagebox.showwarning'), patch.object(RescueApp, '_styles'), patch.object(RescueApp, '_layout'), patch.object(RescueApp, '_load_into_form'), patch.object(RescueApp, '_refresh_state'):
            app.__init__(root)
        self.assertEqual(app.config.operation_mode, 'restricted')
        self.assertEqual(app.session, state)
        root.after.assert_any_call(400, app._recover)


if __name__ == '__main__':
    unittest.main()
