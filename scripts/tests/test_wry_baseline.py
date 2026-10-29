"""一時projectの生成と来歴検査を実CLI境界で検証する。"""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import tomllib
import unittest


ROOT = Path(__file__).resolve().parents[2]
APP = ROOT / 'ui/tauri/src-tauri'
SCRIPT = ROOT / 'scripts/prepare_wry_baseline.py'
COMMIT = '279060bf3ac6a3f14f08068528ea1d2ef3ffcfeb'


class WryBaselineTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='rescue-baseline-test-')
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.manifest = self.root / 'source.toml'
        self.manifest.write_bytes((APP / 'Cargo.toml').read_bytes())
        self.lock = self.root / 'source.lock'
        source_lock = (APP / 'Cargo.lock').read_text()
        old = ('name = "wry"\nversion = "0.57.1"\n'
               'source = "registry+https://github.com/rust-lang/crates.io-index"\n'
               'checksum = "759c50ddad0ef9913bb5496cb9d509a50433a3b78cb0238d61105526589cf0e9"\n')
        self.assertEqual(source_lock.count(old), 1)
        self.lock.write_text(source_lock.replace(old, 'name = "wry"\nversion = "0.57.1"\n'))
        self.fixture = self.root / 'fixture.rs'
        self.fixture.write_text('fn main() {}\n')
        self.output = self.root / 'baseline'
        self.report = self.root / 'report.json'

    def cli(self, *extra):
        command = [sys.executable, '-I', str(SCRIPT), '--manifest', str(self.manifest),
                   '--lock', str(self.lock), '--fixture', str(self.fixture),
                   '--output', str(self.output), '--report', str(self.report),
                   '--source-commit', COMMIT, *extra]
        return subprocess.run(command, capture_output=True, timeout=5)

    def test_prepare_and_verify_preserve_dependency_graph(self):
        before = self.manifest.read_bytes(), self.lock.read_bytes()
        result = self.cli()
        self.assertEqual(result.returncode, 0, 'CLI prepare must succeed')
        self.assertEqual(result.stderr, b'')
        generated = tomllib.loads((self.output / 'Cargo.toml').read_text())
        self.assertEqual(generated['package']['name'], 'rescue-host-shell')
        self.assertEqual(generated['package']['version'], '0.1.0')
        for field in ['autobins', 'autolib', 'build']:
            self.assertIs(generated['package'][field], False)
        self.assertEqual(generated['bin'], [{'name': 'rescue-wry-creation-baseline', 'path': 'main.rs'}])
        self.assertNotIn('test', generated)
        for name, version in [('tao', '=0.37.2'), ('wry', '=0.57.1')]:
            self.assertEqual(generated['dependencies'][name], {'version': version, 'default-features': False})
        original = tomllib.loads(before[1].decode())
        actual = tomllib.loads((self.output / 'Cargo.lock').read_text())
        for package in original['package']:
            if package['name'] == 'rescue-host-shell':
                package['dependencies'] = sorted(package['dependencies'] + ['tao', 'wry'])
        self.assertEqual(actual, original)
        self.assertEqual((self.output / 'main.rs').read_bytes(), self.fixture.read_bytes())
        report = json.loads(self.report.read_text())
        self.assertEqual(report['sourceCommit'], COMMIT)
        self.assertEqual(report['fixtureSha256'], hashlib.sha256(self.fixture.read_bytes()).hexdigest())
        self.assertNotIn(str(self.root), self.report.read_text())
        self.assertEqual(self.cli('--verify').returncode, 0)
        self.assertEqual((self.manifest.read_bytes(), self.lock.read_bytes()), before)

    def rejected(self, result):
        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stdout, ('WRY_BASELINE_PROVENANCE_REJECTED' + os.linesep).encode('ascii'))
        self.assertEqual(result.stderr, b'')

    def test_empty_existing_output_is_allowed(self):
        self.output.mkdir()
        self.assertEqual(self.cli().returncode, 0)

    def test_original_manifest_sections_are_preserved(self):
        self.assertEqual(self.cli().returncode, 0)
        original = tomllib.loads(self.manifest.read_text())
        generated = tomllib.loads((self.output / 'Cargo.toml').read_text())
        for name in ['tao', 'wry']:
            del generated['dependencies'][name]
        for name in ['autobins', 'autolib', 'build']:
            del generated['package'][name]
        del generated['bin']
        original.pop('test')
        self.assertEqual(generated, original)

    def test_nonempty_output_is_untouched(self):
        self.output.mkdir()
        marker = self.output / 'existing'
        marker.write_bytes(b'PRIVATE_FIXTURE')
        self.rejected(self.cli())
        self.assertEqual(list(self.output.iterdir()), [marker])
        self.assertEqual(marker.read_bytes(), b'PRIVATE_FIXTURE')
        self.assertFalse(self.report.exists())

    def test_symlink_output_is_rejected(self):
        target = self.root / 'target'
        target.mkdir()
        try:
            self.output.symlink_to(target, target_is_directory=True)
        except OSError:
            self.skipTest('symlink creation unavailable')
        self.rejected(self.cli())
        self.assertEqual(list(target.iterdir()), [])

    def test_symlink_input_is_rejected(self):
        target = self.root / 'actual.rs'
        self.fixture.rename(target)
        try:
            self.fixture.symlink_to(target)
        except OSError:
            self.skipTest('symlink creation unavailable')
        self.rejected(self.cli())

    def test_unpatched_registry_wry_is_rejected(self):
        self.lock.write_bytes((APP / 'Cargo.lock').read_bytes())
        self.rejected(self.cli())
        self.assertFalse(self.output.exists())

    def test_root_edges_must_match_exact_original(self):
        self.lock.write_text(self.lock.read_text().replace(' "tauri-build",\n "windows-sys 0.61.2",',
                                                        ' "tauri-build",\n "wry",\n "windows-sys 0.61.2",'))
        self.rejected(self.cli())

    def test_pin_version_change_is_rejected(self):
        self.lock.write_text(self.lock.read_text().replace('name = "tao"\nversion = "0.37.2"',
                                                        'name = "tao"\nversion = "0.37.3"'))
        self.rejected(self.cli())

    def test_input_errors_do_not_expose_content_or_paths(self):
        self.manifest.write_text('PRIVATE_FIXTURE = [')
        self.rejected(self.cli())
        self.assertFalse(self.output.exists())

    def test_full_source_commit_is_required(self):
        self.rejected(self.cli('--source-commit', 'PRIVATE_FIXTURE'))

    def test_output_cannot_equal_report(self):
        self.rejected(self.cli('--report', str(self.output)))
        self.assertFalse(self.output.exists())

    def test_existing_report_is_untouched(self):
        self.report.write_bytes(b'PRIVATE_FIXTURE')
        self.rejected(self.cli())
        self.assertEqual(self.report.read_bytes(), b'PRIVATE_FIXTURE')
        self.assertFalse(self.output.exists())

    def test_verify_rejects_manifest_lock_fixture_and_report_drift(self):
        self.assertEqual(self.cli().returncode, 0)
        paths = [self.output / name for name in ['Cargo.toml', 'Cargo.lock', 'main.rs']] + [self.report]
        for path in paths:
            with self.subTest(name=path.name):
                original = path.read_bytes()
                path.write_bytes(original + b'\n# PRIVATE_FIXTURE\n')
                self.rejected(self.cli('--verify'))
                path.write_bytes(original)
        self.assertEqual(self.cli('--verify').returncode, 0)

    def test_verify_rejects_checksum_and_nonroot_edge_drift(self):
        self.assertEqual(self.cli().returncode, 0)
        target = self.output / 'Cargo.lock'
        original = target.read_bytes()
        for old, new in [(b'd5777b06', b'a5777b06'), (b' "bitflags 2.13.2",', b' "PRIVATE_FIXTURE",')]:
            with self.subTest(kind=old):
                self.assertIn(old, original)
                target.write_bytes(original.replace(old, new, 1))
                self.rejected(self.cli('--verify'))
        target.write_bytes(original)

    def test_verify_rejects_extra_files_and_symlinks(self):
        self.assertEqual(self.cli().returncode, 0)
        extra = self.output / 'extra.rs'
        extra.write_text('PRIVATE_FIXTURE')
        self.rejected(self.cli('--verify'))
        extra.unlink()
        generated = self.output / 'main.rs'
        generated.unlink()
        try:
            generated.symlink_to(self.fixture)
        except OSError:
            self.skipTest('symlink creation unavailable')
        self.rejected(self.cli('--verify'))

    def test_verify_rejects_changed_source_or_commit(self):
        self.assertEqual(self.cli().returncode, 0)
        self.rejected(self.cli('--verify', '--source-commit', 'a' * 40))
        self.fixture.write_text('PRIVATE_FIXTURE')
        self.rejected(self.cli('--verify'))


if __name__ == '__main__':
    unittest.main()
