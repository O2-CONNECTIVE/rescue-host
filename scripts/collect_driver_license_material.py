"""固定 driver の Windows Cargo graph を既存収集器に渡すための事前検査。"""
import re
import hashlib
import pathlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import argparse
import tarfile
import tomllib

REGISTRY = 'registry+https://github.com/rust-lang/crates.io-index'
DRIVER_LOCK_SHA256 = '94bf7caee3f76ca1d558e2636a846f12738db64264abe6dd7fa930548d3f03e1'


def parse_fixed_driver_lock(content):
    if not isinstance(content, bytes) or hashlib.sha256(content).hexdigest() != DRIVER_LOCK_SHA256:
        raise ValueError('driver lock mismatch')
    return tomllib.loads(content.decode('utf-8'))


def verify_root_manifest(crate, source):
    """固定crateのmanifestと実際にmetadataを生成するmanifestを照合する。"""
    manifest = pathlib.Path(source) / 'Cargo.toml'
    if manifest.is_symlink() or not manifest.is_file() or manifest.stat().st_size > 65536:
        raise ValueError('driver manifest unavailable')
    with tarfile.open(crate, 'r:gz') as archive:
        found = [m for m in archive.getmembers() if m.name == 'tauri-driver-2.1.0/Cargo.toml']
        if len(found) != 1 or not found[0].isfile() or found[0].size > 65536:
            raise ValueError('driver manifest invalid')
        stream = archive.extractfile(found[0])
        if stream is None or stream.read() != manifest.read_bytes():
            raise ValueError('driver manifest mismatch')


def validate_driver_graph(metadata, lock):
    """driver root と到達可能な registry package を driver lock で固定する。"""
    try:
        packages = metadata['packages']
        nodes = metadata['resolve']['nodes']
        root_id = metadata['resolve']['root']
        by_id = {p['id']: p for p in packages}
        by_node = {n['id']: n for n in nodes}
        if len(by_id) != len(packages) or len(by_node) != len(nodes):
            raise ValueError('duplicate metadata')
        root = by_id[root_id]
        if (root['name'], root['version'], root['source'], root['license']) != ('tauri-driver', '2.1.0', None, 'Apache-2.0 OR MIT'):
            raise ValueError('root mismatch')
        lock_index = {}
        for entry in lock['package']:
            key = (entry['name'], entry['version'])
            if key in lock_index:
                raise ValueError('ambiguous lock')
            lock_index[key] = entry
        seen = set()
        pending = [root_id]
        while pending:
            item = pending.pop()
            if item in seen:
                continue
            seen.add(item)
            pending.extend(by_node[item]['dependencies'])
        result = []
        for item in seen - {root_id}:
            package = by_id[item]
            record = lock_index[(package['name'], package['version'])]
            if (package['source'] != REGISTRY or record.get('source') != REGISTRY
                    or not package.get('license') or not re.fullmatch(r'[0-9a-f]{64}', record.get('checksum', ''))):
                raise ValueError('dependency mismatch')
            result.append(package)
        if not result:
            raise ValueError('empty graph')
        return sorted(result, key=lambda p: (p['name'], p['version']))
    except (KeyError, TypeError, AttributeError) as error:
        raise ValueError('invalid metadata') from error


def verify_cached_archives(metadata, lock, cargo_home):
    """公式registryのarchive bytesをdriver lockのchecksumへ照合する。"""
    packages = validate_driver_graph(metadata, lock)
    index = {(p['name'], p['version']): p for p in lock['package']}
    cache = pathlib.Path(cargo_home) / 'registry' / 'cache'
    for package in packages:
        name, version = package['name'], package['version']
        if not re.fullmatch(r'[A-Za-z0-9_.+-]+', name) or not re.fullmatch(r'[A-Za-z0-9_.+-]+', version):
            raise ValueError('invalid crate name')
        matches = list(cache.glob(f'*/{name}-{version}.crate'))
        if len(matches) != 1 or matches[0].is_symlink() or not matches[0].is_file():
            raise ValueError('crate archive unavailable')
        if hashlib.sha256(matches[0].read_bytes()).hexdigest() != index[(name, version)]['checksum']:
            raise ValueError('crate checksum mismatch')


def verify_registry_license_files(crate, source, package_name, license_file=None):
    """既存収集器が読む本文を検証済registry archiveの同名bytesと照合する。"""
    root = pathlib.Path(source).resolve(strict=True)
    candidates = [p for p in root.iterdir() if re.match(r'(?i)^(licen[cs]e|copying|notice|copyright)([._-]|$)', p.name)]
    if license_file:
        candidates.append(root / license_file)
    files = []
    for candidate in candidates:
        if candidate.is_symlink():
            raise ValueError('license symlink')
        files.extend(candidate.rglob('*') if candidate.is_dir() else [candidate])
    files = [p for p in files if p.is_file() or p.is_symlink()]
    if not files:
        raise ValueError('license unavailable')
    with tarfile.open(crate, 'r:gz') as archive:
        entries = archive.getmembers()
        prefix = package_name + '/'
        expected = []
        for entry in entries:
            if not entry.name.startswith(prefix) or entry.isdir():
                continue
            relative = entry.name[len(prefix):]
            first = relative.split('/', 1)[0]
            if re.match(r'(?i)^(licen[cs]e|copying|notice|copyright)([._-]|$)', first) or relative == license_file:
                if not entry.isfile():
                    raise ValueError('license member invalid')
                expected.append(relative)
        actual = [file.relative_to(root).as_posix() for file in files]
        if len(expected) != len(set(expected)) or set(expected) != set(actual):
            raise ValueError('license member set mismatch')
        for file in files:
            if file.is_symlink() or not file.resolve(strict=True).is_relative_to(root) or file.stat().st_size > 1024 * 1024:
                raise ValueError('license path invalid')
            name = package_name + '/' + file.relative_to(root).as_posix()
            found = [item for item in entries if item.name == name]
            if len(found) != 1 or not found[0].isfile() or found[0].size > 1024 * 1024:
                raise ValueError('license member unavailable')
            stream = archive.extractfile(found[0])
            if stream is None or stream.read() != file.read_bytes():
                raise ValueError('license member mismatch')


def collect_driver_material(driver_source, driver_crate, mit_license, app_collector, rust_docs, output):
    """固定driverからcargo metadataを生成し、既存collectorで本文を収集する。"""
    source = pathlib.Path(driver_source).resolve(strict=True)
    crate = pathlib.Path(driver_crate)
    mit = pathlib.Path(mit_license)
    collector = pathlib.Path(app_collector)
    output = pathlib.Path(output)
    expected = {
        crate: '2f2e29bd6900fb718dfdc06f2e7a371d6087c085daaf83709847ef3e6b0c1953',
        mit: '9dd42ea92cff2ede5cd477cbfcce051b2d0115c0ac7f368ee88cb545055dff1d',
        collector: '02b5194eca021eef62fe06584c98cfdebdccba8b8b2d550bf56a938a2140ca57',
    }
    for file, digest in expected.items():
        if file.is_symlink() or not file.is_file() or hashlib.sha256(file.read_bytes()).hexdigest() != digest:
            raise ValueError('fixed input mismatch')
    verify_root_manifest(crate, source)
    lock_bytes = (source / 'Cargo.lock').read_bytes()
    lock = parse_fixed_driver_lock(lock_bytes)
    vcs = json.loads((source / '.cargo_vcs_info.json').read_bytes())
    if vcs != {'git': {'sha1': '447fa9f3f993fe77724189e355078b38ce20baea'}, 'path_in_vcs': 'crates/tauri-driver'}:
        raise ValueError('driver provenance mismatch')
    if output.exists() or output.is_symlink():
        raise ValueError('output exists')
    cargo_home = pathlib.Path(os.environ.get('CARGO_HOME') or pathlib.Path.home() / '.cargo').resolve(strict=True)
    command = ['cargo', '+1.99.1', 'metadata', '--locked', '--format-version', '1', '--filter-platform',
               'x86_64-pc-windows-msvc', '--manifest-path', str(source / 'Cargo.toml')]
    metadata = json.loads(subprocess.run(command, check=True, capture_output=True, timeout=240).stdout)
    packages = validate_driver_graph(metadata, lock)
    root = next(p for p in metadata['packages'] if p['id'] == metadata['resolve']['root'])
    if pathlib.Path(root['manifest_path']).resolve(strict=True) != source / 'Cargo.toml':
        raise ValueError('driver source mismatch')
    registry_src = (cargo_home / 'registry' / 'src').resolve(strict=True)
    for package in packages:
        manifest = pathlib.Path(package['manifest_path'])
        if manifest.is_symlink() or not manifest.resolve(strict=True).is_relative_to(registry_src):
            raise ValueError('registry source mismatch')
    verify_cached_archives(metadata, lock, cargo_home)
    for package in packages:
        name = package['name'] + '-' + package['version']
        archives = list((cargo_home / 'registry/cache').glob('*/' + name + '.crate'))
        if len(archives) != 1:
            raise ValueError('crate archive unavailable')
        verify_registry_license_files(archives[0], pathlib.Path(package['manifest_path']).parent, name, package.get('license_file'))
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='driver-licenses-', dir=output.parent) as folder:
        work = pathlib.Path(folder)
        fixed = work / 'collector'
        (fixed / 'scripts').mkdir(parents=True)
        (fixed / 'ui/tauri/src-tauri').mkdir(parents=True)
        (fixed / 'ui/tauri/third-party').mkdir(parents=True)
        shutil.copyfile(collector, fixed / 'scripts/collect_tauri_licenses.py')
        (fixed / 'ui/tauri/src-tauri/Cargo.lock').write_bytes(lock_bytes)
        (fixed / 'ui/tauri/third-party/rust-license-supplements.json').write_text('[]', encoding='utf-8')
        (work / 'metadata.json').write_text(json.dumps(metadata), encoding='utf-8')
        (work / 'notice.txt').write_text('Driver-only collection; no frontend.\n', encoding='utf-8')
        generated = work / 'generated'
        subprocess.run([sys.executable, str(fixed / 'scripts/collect_tauri_licenses.py'), '--metadata', str(work / 'metadata.json'),
                        '--frontend-notice', str(work / 'notice.txt'), '--rust-docs', str(rust_docs), '--output', str(generated)],
                       check=True, capture_output=True, timeout=60)
        inventory_path = generated / 'DEPENDENCIES.json'
        inventory = json.loads(inventory_path.read_bytes())
        actual = {(p['name'], p['version'], p['crateChecksum']) for p in inventory['packages']}
        lock_index = {(p['name'], p['version']): p for p in lock['package']}
        wanted = {(p['name'], p['version'], lock_index[(p['name'], p['version'])]['checksum']) for p in packages}
        if len(inventory['packages']) != len(wanted) or actual != wanted:
            raise ValueError('inventory graph mismatch')
        inventory['scope'] = 'Windows x64 tauri-driver 2.1.0 / Cargo resolve graph; linked SBOM ではない。'
        inventory['driverRoot'] = {'version': '2.1.0', 'crateSha256': expected[crate], 'vcs': vcs['git']['sha1'],
                                   'license': 'Apache-2.0 OR MIT', 'selected': 'MIT', 'licenseSha256': expected[mit]}
        inventory['driverLockSha256'] = DRIVER_LOCK_SHA256
        inventory_path.write_text(json.dumps(inventory, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
        (generated / 'FRONTEND_THIRD_PARTY_NOTICES.txt').unlink()
        driver_license = generated / 'rust/tauri-driver-2.1.0/LICENSE-MIT'
        driver_license.parent.mkdir(parents=True)
        shutil.copyfile(mit, driver_license)
        shutil.copytree(generated, output)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description='固定driverのライセンス資料を一時領域に収集する')
    for option in ('driver-source', 'driver-crate', 'mit-license', 'app-collector', 'rust-docs', 'output'):
        parser.add_argument('--' + option, required=True)
    args = parser.parse_args()
    try:
        collect_driver_material(args.driver_source, args.driver_crate, args.mit_license, args.app_collector, args.rust_docs, args.output)
        print('DRIVER_LICENSE_MATERIAL_VERIFIED')
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError, json.JSONDecodeError):
        print('DRIVER_LICENSE_MATERIAL_UNCONFIRMED')
        sys.exit(1)
