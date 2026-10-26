"""入力検証と Upterm CLI の互換性に関する前提を確認するオフラインテスト。"""
import base64
import json
from pathlib import Path
import struct
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'src'))
from rescue_core import (
    DEFAULT_RELAY, RescueError, Settings, atomic_json_write,
    build_mac_forward_command, check_upterm_version, get_session_info,
    official_known_hosts_text, parse_host_json, prepare_authorized_keys,
    is_official_relay, load_settings, record_session_state,
    remove_session_state_if_name, resolve_known_hosts, run_upterm, save_settings,
    session_state_path, upterm_cmd_start, validate_public_keys,
    validate_relay, version_from_output,
)


def key_line() -> str:
    label = b'ssh-ed25519'
    raw = struct.pack('>I', len(label)) + label + struct.pack('>I', 32) + bytes([23] * 32)
    return 'ssh-ed25519 ' + base64.b64encode(raw).decode('ascii') + ' test@mac'


class TestSettings(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = Path(self.tmp.name)
        self.settings = Settings(public_keys=key_line())

    def test_key_validation(self):
        validate_public_keys(key_line())
        with self.assertRaises(RescueError):
            validate_public_keys('ssh-ed25519 NOT_BASE64')
        with self.assertRaises(RescueError):
            validate_public_keys('ssh-rsa ' + key_line().split()[1])
        with self.assertRaises(RescueError):
            validate_public_keys('')

    def test_settings_roundtrip(self):
        save_settings(self.settings, self.dir)
        restored = load_settings(self.dir)
        self.assertEqual(restored, self.settings)
        self.assertTrue((self.dir / 'config.json').exists())

    def test_relay_validation(self):
        validate_relay(DEFAULT_RELAY)
        validate_relay('wss://uptermd.upterm.dev:443')
        self.assertTrue(is_official_relay('wss://uptermd.upterm.dev'))
        for value in ('http://relay.com', 'ssh://user:pw@relay.com', 'ssh://relay.com:99999', 'ssh://relay.com/path'):
            with self.assertRaises(RescueError):
                validate_relay(value)

    def test_reject_custom_relay_without_trust(self):
        self.settings.relay = 'ssh://relay.example.com:22'
        with self.assertRaises(RescueError):
            self.settings.validate()

    def test_known_hosts_pin(self):
        text = official_known_hosts_text()
        self.assertIn('@cert-authority uptermd.upterm.dev', text)
        path = resolve_known_hosts(self.settings, self.dir)
        self.assertTrue(path.is_file())

    def test_arg_builder(self):
        with self.assertRaises(RescueError):
            upterm_cmd_start(self.settings, 'rescue-123456abcdef', self.dir / 'kh', self.dir / 'keys')

    def test_terminal_mode(self):
        self.settings.mode = 'terminal'
        args = upterm_cmd_start(self.settings, 'rescue-123456abcdef', self.dir / 'kh', self.dir / 'keys')
        self.assertNotIn('--allow-local-tcp-forwarding', args)
        self.assertEqual(args[args.index('--join-timeout') + 1], '10m')

    def test_public_key_is_staged(self):
        file = prepare_authorized_keys(self.settings, 'rescue-123456abcdef', self.dir)
        self.assertEqual(file.read_text().strip(), key_line())
        with self.assertRaises(RescueError):
            prepare_authorized_keys(self.settings, '../../foo', self.dir)

    def test_parse_join(self):
        out = json.dumps({'name': 'rescue-123456abcdef', 'sessionId': 'tok', 'sshCommand': 'ssh TOKEN@uptermd.upterm.dev'})
        parsed = parse_host_json(out)
        self.assertEqual(parsed['ssh_command'], 'ssh TOKEN@uptermd.upterm.dev')
        with self.assertRaises(RescueError):
            parse_host_json('{}')

    def test_mac_tunnel_command(self):
        with self.assertRaises(RescueError):
            build_mac_forward_command('ssh TOKEN@uptermd.upterm.dev')

    def test_session_record_cleanup(self):
        name = 'rescue-123456abcdef'
        record_session_state({'name': name, 'deadline': 100}, self.dir)
        self.assertTrue(session_state_path(self.dir).exists())
        remove_session_state_if_name('rescue-aaaaaaaaaaaa', self.dir)
        self.assertTrue(session_state_path(self.dir).exists())
        remove_session_state_if_name(name, self.dir)
        self.assertFalse(session_state_path(self.dir).exists())

    def test_version_parser(self):
        self.assertEqual(version_from_output('{"version":"v0.39.0"}'), (0, 39, 0))
        self.assertEqual(version_from_output('Upterm 1.20.3'), (1, 20, 3))
        with self.assertRaises(RescueError):
            version_from_output('garbled')


if __name__ == '__main__':
    unittest.main()
