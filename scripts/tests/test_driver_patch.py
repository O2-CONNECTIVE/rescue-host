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

    def test_context_drift_unrelated_file_and_truncated_hunk_are_rejected(self):
        # 実装後回帰。CLIで許可するarchive/hashをfixtureへ差替えない。
        spec = importlib.util.spec_from_file_location('driver_patch', ROOT / 'scripts/apply_driver_trace.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        for patch in [b'--- a/Cargo.lock\n+++ b/Cargo.lock\n@@ -1 +1 @@\n-LOCK\n+CHANGED\n',
                      b'--- a/src/main.rs\n+++ b/src/main.rs\n@@ -1 +1 @@\n-WRONG\n+new\n',
                      b'--- a/src/main.rs\n+++ b/src/main.rs\n@@ -1,2 +1 @@\n-base\n+new\n']:
            with self.subTest(patch=patch):
                with self.assertRaises(module.PatchError):
                    module.apply_exact({'src/main.rs': b'base\n', 'Cargo.lock': b'LOCK\n'}, patch)

    def test_reviewed_bundle_rejects_changed_patch_or_diagnostic_code(self):
        # 実装後回帰。未知source/追加dependencyを許可する引数は設けない。
        import shutil
        import tempfile
        spec = importlib.util.spec_from_file_location('driver_patch', ROOT / 'scripts/apply_driver_trace.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        module.bundle(ROOT / 'tools/native-driver-trace')
        for name in ['driver.patch', 'diagnostic.rs', 'diagnostic_tests.rs']:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as folder:
                target = Path(folder) / 'bundle'
                shutil.copytree(ROOT / 'tools/native-driver-trace', target)
                with (target / name).open('ab') as stream:
                    stream.write(b'PRIVATE_DRIFT')
                with self.assertRaises(module.PatchError) as error:
                    module.bundle(target)
                self.assertNotIn('PRIVATE_DRIFT', str(error.exception))

    def test_unrelated_file_in_reviewed_patch_bundle_is_rejected(self):
        import shutil
        import tempfile
        spec = importlib.util.spec_from_file_location('driver_patch', ROOT / 'scripts/apply_driver_trace.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as folder:
            target = Path(folder) / 'bundle'
            shutil.copytree(ROOT / 'tools/native-driver-trace', target)
            (target / 'extra.rs').write_text('PRIVATE_UNRELATED_SOURCE', encoding='utf-8')
            with self.assertRaises(module.PatchError):
                module.bundle(target)


if __name__ == '__main__':
    unittest.main()
