import os
from pathlib import Path
import shutil
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'src'))
from rescue_core import get_session_info, session_state_path, stop_session
from rescue_host import RescueApp
from rescue_core import Settings
from test_core import key_line


@unittest.skipIf(sys.platform == 'win32', 'This mocked shebang executable is only used on Unix CI')
class TestIntegration(unittest.TestCase):
    def test_start_and_stop_using_fake_upterm(self):
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp)
            fake = state / 'upterm'
            shutil.copy2(Path(__file__).parent / 'fake_upterm.py', fake)
            fake.chmod(0o755)
            settings = Settings(
                mode='terminal', duration_minutes=5, public_keys=key_line(),
                external_upterm=str(fake),
            )
            # Use a fake Upterm executable and no-op background watchdog.
            with patch.dict(os.environ, {'XDG_DATA_HOME': str(state), 'FAKE_UPTERM_STATE': str(state / 'fake')}), \
                 patch('rescue_host.launch_watchdog', return_value=None), \
                 patch('rescue_host.require_secure_session_backend', return_value=None):
                app = object.__new__(RescueApp)
                session = app._start_worker(settings, fake)
                self.assertIn('TokenMadeUp', session['ssh_command'])
                self.assertEqual(get_session_info(fake, session['name'])['status'], 'ready')
                self.assertTrue(session_state_path().exists())
                app._stop_worker(session)
                self.assertEqual(get_session_info(fake, session['name'])['status'], 'ended')
                self.assertFalse(session_state_path().exists())


if __name__ == '__main__':
    unittest.main()
