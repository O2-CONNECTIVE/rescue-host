"""Validate the Windows x64 wheel lock without installing or executing wheels."""

import argparse
import email
import hashlib
import importlib.metadata
import json
from pathlib import Path
import platform
import re
import sys
from urllib.parse import urlsplit
import zipfile


ROOT = Path(__file__).resolve().parents[1]
NAMES = {'altgraph', 'packaging', 'pefile', 'pyinstaller',
         'pyinstaller-hooks-contrib', 'pywin32-ctypes', 'setuptools'}
PYINSTALLER = '6.22.3'


def load_lock():
    lock = json.loads((ROOT / 'build-python-lock.json').read_text(encoding='utf-8'))
    if (not isinstance(lock, dict) or set(lock) != {'version', 'target', 'python', 'pyinstaller', 'packages'}
            or type(lock['version']) is not int or lock['version'] != 1
            or lock['target'] != 'windows-x64' or lock['python'] != '>=3.11,<3.16'
            or lock['pyinstaller'] != PYINSTALLER):
        raise ValueError('Unsupported build lock or PyInstaller version')
    packages = lock['packages']
    if not isinstance(packages, list) or len(packages) != len(NAMES):
        raise ValueError('Build lock must contain all seven packages')
    names = []
    fields = {'name', 'version', 'filename', 'sha256', 'pypi', 'url', 'source',
              'requires_python', 'license', 'license_files'}
    for package in packages:
        if not isinstance(package, dict) or set(package) != fields:
            raise ValueError('Incomplete package provenance')
        if any(not isinstance(package[key], str) or not package[key].strip()
               for key in fields - {'requires_python', 'license_files'}):
            raise ValueError('Empty package provenance')
        if package['requires_python'] is not None and not isinstance(package['requires_python'], str):
            raise ValueError('Invalid Requires-Python provenance')
        name, version = package['name'], package['version']
        names.append(name)
        if not re.fullmatch(r'[0-9]+(?:\.[0-9]+)*', version):
            raise ValueError('Package version must be exact')
        if name == 'pyinstaller' and version != PYINSTALLER:
            raise ValueError('PyInstaller version must be ' + PYINSTALLER)
        if not re.fullmatch(r'[0-9a-f]{64}', package['sha256']):
            raise ValueError('Invalid SHA256 for ' + name)
        filename = package['filename']
        if (Path(filename).name != filename or not filename.startswith(name.replace('-', '_') + '-' + version + '-')
                or not filename.endswith(('-none-any.whl', '-none-win_amd64.whl'))):
            raise ValueError('Unsupported wheel filename for ' + name)
        if package['pypi'] != f'https://pypi.org/pypi/{name}/{version}/json':
            raise ValueError('Expected version-specific official PyPI metadata')
        url = urlsplit(package['url'])
        if url.scheme != 'https' or url.netloc != 'files.pythonhosted.org' or not url.path.endswith('/' + filename):
            raise ValueError('Expected official PyPI wheel URL')
        if not package['source'].startswith('https://github.com/'):
            raise ValueError('Expected reviewed upstream source URL')
        if not isinstance(package['license_files'], list) or not package['license_files'] or any(
                not isinstance(path, str) or not path or path.startswith('/') or '..' in path.split('/')
                for path in package['license_files']):
            raise ValueError('License file inventory is required')
    if names != sorted(NAMES):
        raise ValueError('Missing, duplicate, unordered, or unexpected package')
    expected = [f"{p['name']}=={p['version']} --hash=sha256:{p['sha256']}" for p in packages]
    actual = [line.strip() for line in (ROOT / 'requirements-build.txt').read_text(encoding='utf-8').splitlines()
              if line.strip() and not line.lstrip().startswith('#')]
    if actual != expected:
        raise ValueError('Requirements differ from the exact version/hash lock')
    return packages


def verify_wheels(packages, wheelhouse):
    if not wheelhouse.is_dir() or {p.name for p in wheelhouse.iterdir()} != {p['filename'] for p in packages}:
        raise ValueError('Wheelhouse must contain exactly the seven locked wheels')
    for package in packages:
        wheel = wheelhouse / package['filename']
        if wheel.is_symlink() or wheel.stat().st_size > 64 * 1024 * 1024:
            raise ValueError('Wheel is not a bounded regular file: ' + package['filename'])
        with wheel.open('rb') as stream:
            digest = hashlib.file_digest(stream, 'sha256').hexdigest()
        if digest != package['sha256']:
            raise ValueError('Wheel SHA256 mismatch: ' + package['filename'])
        with zipfile.ZipFile(wheel) as archive:
            metadata_file = package['name'].replace('-', '_') + '-' + package['version'] + '.dist-info/METADATA'
            metadata = email.message_from_bytes(archive.read(metadata_file))
            if (metadata.get('Name', '').lower().replace('_', '-') != package['name']
                    or metadata.get('Version') != package['version']
                    or metadata.get('Requires-Python') != package['requires_python']):
                raise ValueError('Wheel metadata differs from lock: ' + package['filename'])
            for license_file in package['license_files']:
                if not archive.read(license_file).strip():
                    raise ValueError('Empty license notice: ' + package['filename'])
        print(f"WHEEL_OK {package['filename']} sha256={package['sha256']} notices={len(package['license_files'])}")
    print(f'BUILD_WHEELS_PASS verified={len(packages)}')


def require_target():
    if (sys.platform != 'win32' or platform.machine().lower() not in ('amd64', 'x86_64')
            or sys.maxsize <= 2**32 or sys.implementation.name != 'cpython'
            or not (3, 11) <= sys.version_info[:2] < (3, 16)):
        raise ValueError('Requires Windows x64 CPython 3.11–3.15')
    print(f'BUILD_TARGET_PASS windows-x64 CPython={platform.python_version()}')


def verify_installed(packages):
    for package in packages:
        try:
            actual = importlib.metadata.version(package['name'])
        except importlib.metadata.PackageNotFoundError:
            raise ValueError('Missing installed dependency: ' + package['name']) from None
        if actual != package['version']:
            raise ValueError('Installed dependency differs from lock: ' + package['name'])
    print(f'BUILD_INSTALLED_PASS verified={len(packages)}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--wheelhouse', type=Path)
    parser.add_argument('--target', action='store_true', help='Require the Windows build target')
    parser.add_argument('--installed', action='store_true', help='Require target and verify installed versions')
    args = parser.parse_args()
    try:
        packages = load_lock()
        if args.target or args.installed:
            require_target()
        if args.wheelhouse:
            verify_wheels(packages, args.wheelhouse)
        if args.installed:
            verify_installed(packages)
        print(f'BUILD_LOCK_PASS packages={len(packages)} target=windows-x64 python=>=3.11,<3.16')
        return 0
    except (OSError, ValueError, TypeError, KeyError, zipfile.BadZipFile) as error:
        print('BUILD_LOCK_FAIL:', str(error).replace(str(ROOT), '<repo>'), file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
