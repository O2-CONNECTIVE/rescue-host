import pathlib
import unittest


class MainTimeoutWorkflowContract(unittest.TestCase):
    def test_fixed_release_and_owned_cleanup(self):
        workflow = pathlib.Path('.github/workflows/main-timeout-repro.yml').read_text(encoding='utf-8')
        self.assertIn('ref: 1eda7a4e964d34a751dcc89509ee07b3482917a7', workflow)
        self.assertIn('cargo +1.99.1 build --release --locked', workflow)
        self.assertIn('native-smoke/supervise.ps1', workflow)
        self.assertIn('timeout-minutes: 3', workflow)
        self.assertIn('nativeTestsRun', workflow)
        self.assertIn('normalCloseVerified', workflow)
        self.assertIn('driverExitObservedAfterCleanup', workflow)
        self.assertIn('working-directory: source/ui/tauri', workflow)
        self.assertNotIn('working-directory: ui/tauri', workflow)
        self.assertIn('Get-NetTCPConnection -State Listen', workflow)
        self.assertIn('Get-CimInstance Win32_Process', workflow)
        self.assertIn('ExecutablePath', workflow)
        self.assertIn('OWNER_CLEANUP_UNCONFIRMED', workflow)
        self.assertNotIn('actions/download-artifact', workflow)


if __name__ == '__main__':
    unittest.main()
