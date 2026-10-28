"""固定差分の純粋適用。任意archiveを実driverとして許可するfixtureではない。"""
import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]


class DriverPatchTests(unittest.TestCase):
    def test_exact_context_patch_preserves_unrelated_upstream_files(self):
        spec = importlib.util.spec_from_file_location('driver_patch', ROOT / 'scripts/apply_driver_trace.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        original = {'src/main.rs': b'first\nlast\n', 'Cargo.lock': b'UNCHANGED\n'}
        patch = ('--- a/src/main.rs\n+++ b/src/main.rs\n@@ -1,2 +1,3 @@\n first\n+trace\n last\n').encode()
        changed = module.apply_exact(original, patch)
        self.assertEqual(changed['src/main.rs'], b'first\ntrace\nlast\n')
        self.assertEqual(changed['Cargo.lock'], b'UNCHANGED\n')
        self.assertEqual(original['src/main.rs'], b'first\nlast\n')


if __name__ == '__main__':
    unittest.main()
