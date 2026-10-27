"""Finite CLI fixtures for the Windows build dependency lock, not EXE tests."""

import json
import hashlib
import os
import platform
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
import zipfile


class BuildLockTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix='build-lock-fixture-')
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        (self.root / 'scripts').mkdir()
        repo = Path(__file__).resolve().parents[2]
        checker = repo / 'scripts/check_build_lock.py'
        if checker.exists():
            shutil.copy2(checker, self.root / 'scripts/check_build_lock.py')
        self.lock = json.loads((repo / 'build-python-lock.json').read_text(encoding='utf-8'))
        self.save()

    def save(self):
        (self.root / 'build-python-lock.json').write_text(json.dumps(self.lock), encoding='utf-8')
        requirements = '# Windows x64 only; see docs/distribution.md.\n'
        requirements += ''.join(f"{p['name']}=={p['version']} --hash=sha256:{p['sha256']}\n"
                                for p in self.lock['packages'])
        (self.root / 'requirements-build.txt').write_text(requirements, encoding='utf-8')

    def run_check(self, *arguments):
        return subprocess.run([sys.executable, '-I', str(self.root / 'scripts/check_build_lock.py'), *arguments],
                              cwd=self.root.parent, capture_output=True, text=True, timeout=20,
                              env=dict(os.environ, PYTHONOPTIMIZE='2'))

    def test_exact_pins_and_hashes_pass(self):
        result = self.run_check()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('BUILD_LOCK_PASS packages=7', result.stdout)

    def make_wheels(self):
        wheelhouse = self.root / 'wheels'
        wheelhouse.mkdir()
        for package in self.lock['packages']:
            wheel = wheelhouse / package['filename']
            metadata = f"Name: {package['name']}\nVersion: {package['version']}\n"
            if package['requires_python']:
                metadata += 'Requires-Python: ' + package['requires_python'] + '\n'
            with zipfile.ZipFile(wheel, 'w') as archive:
                archive.writestr(package['name'].replace('-', '_') + '-' + package['version'] + '.dist-info/METADATA', metadata)
                for license_file in package['license_files']:
                    archive.writestr(license_file, 'Finite fixture notice; not third-party package code.\n')
            package['sha256'] = hashlib.sha256(wheel.read_bytes()).hexdigest()
        self.save()
        return wheelhouse

    def test_wheel_hash_metadata_and_notices_are_verified(self):
        result = self.run_check('--wheelhouse', str(self.make_wheels()))
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('BUILD_WHEELS_PASS verified=7', result.stdout)

    def assert_rejected(self, result, reason):
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn('BUILD_LOCK_FAIL', result.stderr)
        self.assertIn(reason, result.stderr)

    def test_range_or_different_version_in_requirements_is_rejected(self):
        requirement = self.root / 'requirements-build.txt'
        for change in ('pyinstaller>=6.10,<8', 'pyinstaller==6.22.1'):
            self.save()
            requirement.write_text(requirement.read_text().replace('pyinstaller==6.22.3', change))
            self.assert_rejected(self.run_check(), 'Requirements differ')

    def test_pyinstaller_downgrade_in_both_files_is_rejected(self):
        self.lock['pyinstaller'] = '6.10.0'
        next(p for p in self.lock['packages'] if p['name'] == 'pyinstaller')['version'] = '6.10.0'
        self.save()
        self.assert_rejected(self.run_check(), 'Unsupported build lock or PyInstaller version')

    def test_requirements_hash_mismatch_is_rejected(self):
        requirement = self.root / 'requirements-build.txt'
        requirement.write_text(requirement.read_text().replace(self.lock['packages'][0]['sha256'], '0' * 64))
        self.assert_rejected(self.run_check(), 'Requirements differ')

    def test_changed_wheel_bytes_are_rejected(self):
        wheels = self.make_wheels()
        with (wheels / self.lock['packages'][0]['filename']).open('ab') as stream:
            stream.write(b'finite corruption fixture')
        self.assert_rejected(self.run_check('--wheelhouse', str(wheels)), 'Wheel SHA256 mismatch')

    def test_pip_hash_mode_rejects_corrupt_requirement_without_network(self):
        wheels = self.make_wheels()
        bad = self.root / 'bad-requirements.txt'
        bad.write_text('altgraph==0.17.5 --hash=sha256:' + '0' * 64 + '\n')
        result = subprocess.run(
            [sys.executable, '-I', '-m', 'pip', '--isolated', '--disable-pip-version-check',
             'download', '--no-index', '--find-links', str(wheels), '--no-cache-dir',
             '--require-hashes', '--only-binary=:all:', '--no-deps',
             '-r', str(bad), '--dest', str(self.root / 'download')],
            capture_output=True, text=True, timeout=30,
        )
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('DO NOT MATCH THE HASHES', result.stdout + result.stderr)

    def test_empty_missing_or_extra_wheelhouse_is_rejected(self):
        wheels = self.root / 'wheels'
        wheels.mkdir()
        self.assert_rejected(self.run_check('--wheelhouse', str(wheels)), 'exactly the seven')
        wheels.rmdir()
        wheels = self.make_wheels()
        (wheels / 'unlocked.whl').write_bytes(b'fixture')
        self.assert_rejected(self.run_check('--wheelhouse', str(wheels)), 'exactly the seven')
        (wheels / 'unlocked.whl').unlink()
        (wheels / self.lock['packages'][0]['filename']).unlink()
        self.assert_rejected(self.run_check('--wheelhouse', str(wheels)), 'exactly the seven')

    def test_missing_package_and_license_inventory_are_rejected(self):
        self.lock['packages'][0]['license_files'] = []
        self.save()
        self.assert_rejected(self.run_check(), 'License file inventory')
        self.lock['packages'].pop()
        self.save()
        self.assert_rejected(self.run_check(), 'all seven packages')

    def test_target_condition_matches_actual_environment(self):
        result = self.run_check('--target')
        supported = (sys.platform == 'win32' and platform.machine().lower() in ('amd64', 'x86_64')
                     and sys.maxsize > 2**32 and (3, 11) <= sys.version_info[:2] < (3, 16))
        if supported:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertIn('BUILD_TARGET_PASS', result.stdout)
        else:
            self.assert_rejected(result, 'Requires Windows x64 CPython 3.11–3.15')


if __name__ == '__main__':
    unittest.main()
