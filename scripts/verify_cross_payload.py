"""一時CIの共有候補をupload前に検査する。実行・署名・秘密不在の証明はしない。"""
import argparse
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import re
import stat
import sys
import zipfile
import zlib

SOURCE = '1eda7a4e964d34a751dcc89509ee07b3482917a7'
MIT = '9dd42ea92cff2ede5cd477cbfcce051b2d0115c0ac7f368ee88cb545055dff1d'
CRATE = '2f2e29bd6900fb718dfdc06f2e7a371d6087c085daaf83709847ef3e6b0c1953'
LOCK = '94bf7caee3f76ca1d558e2636a846f12738db64264abe6dd7fa930548d3f03e1'
APP = 'RescueHostTauriUnwiredEvaluation-x64.zip'
FILES = {APP, 'tauri-driver.exe', 'driver-provenance.json', 'driver-licenses.zip', 'manifest.json'}
SECRET = re.compile(rb'-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----|ghp_[A-Za-z0-9]{36,255}|github_pat_[A-Za-z0-9_]{30,255}|AKIA[0-9A-Z]{16}')

def require(condition):
    if not condition:
        raise ValueError('PAYLOAD_REJECTED')

def digest(data):
    return hashlib.sha256(data).hexdigest()

def safe_name(name):
    return bool(name) and all(re.fullmatch(r'[A-Za-z0-9_.+-]+', part) and part not in ('.', '..') and not part.endswith('.') and not re.fullmatch(r'(?i)(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])', part.split('.')[0]) for part in name.split('/'))

def parse_json(data):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result)
            result[key] = value
        return result
    # 構造化データには生成元runnerのローカルpathを持たせない。
    require(re.search(rb'(?i)(?:/users/|/home/|[a-z]:\\\\users\\\\)', data) is None)
    result = json.loads(data, object_pairs_hook=unique)
    def check(value):
        if isinstance(value, str):
            require(re.search(r'(?i)(?:/users/|/home/|[a-z]:\\users\\)', value) is None)
            clean(value.encode('utf-8'))
        elif isinstance(value, list):
            for item in value: check(item)
        elif isinstance(value, dict):
            for key, item in value.items():
                check(key)
                check(item)
    check(result)
    return result

def clean(data):
    # 検出は代表的な候補のみ。合格は秘密が存在しない証明ではない。
    require(SECRET.search(data) is None)
    require(SECRET.search(data.replace(b'\x00', b'')) is None)

def ordinary(path, *, directory):
    info = path.lstat()
    return not (getattr(info, 'st_file_attributes', 0) & 0x400) and (stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode))

def entries(data):
    require(len(data) <= 64 * 1024 * 1024)
    result, directories = {}, set()
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        require(not archive.comment)
        infos = archive.infolist()
        require(len(infos) <= 4096 and sum(i.file_size for i in infos) <= 128 * 1024 * 1024)
        folded = set()
        for item in infos:
            name = item.filename[:-1] if item.is_dir() else item.filename
            require(item.orig_filename == item.filename and safe_name(name) and name.casefold() not in folded)
            folded.add(name.casefold())
            kind = (item.external_attr >> 16) & 0o170000
            require(kind in (0, stat.S_IFDIR if item.is_dir() else stat.S_IFREG) and not item.flag_bits & 1 and not item.comment and item.compress_type in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED) and item.file_size <= 32 * 1024 * 1024)
            clean(item.extra)
            if item.is_dir():
                require(item.file_size == 0)
                directories.add(name)
            else:
                content = archive.read(item)
                clean(content)
                result[name] = content
    require(all(any(name.startswith(d + '/') for name in result) for d in directories))
    return result

def license_check(data):
    files = entries(data)
    inventory = parse_json(files['DEPENDENCIES.json'])
    require(set(inventory) == {'scope','tools','packages','rustToolchainNotices','driverRoot','driverLockSha256'} and inventory['scope'] == 'Windows x64 tauri-driver 2.1.0 / Cargo resolve graph; linked SBOM ではない。')
    require(set(inventory['tools']) == {'rust','node','python','profile'} and inventory['tools']['rust'] == '1.99.1' and inventory['tools']['node'] == '24.22.1' and re.fullmatch(r'3\.13\.\d+', inventory['tools']['python']) is not None and inventory['tools']['profile'] == 'release')
    root = inventory['driverRoot']
    require(set(root) == {'version','selected','license','vcs','licenseSha256','crateSha256'} and root['version'] == '2.1.0' and root['selected'] == 'MIT' and root['license'] == 'Apache-2.0 OR MIT' and root['vcs'] == '447fa9f3f993fe77724189e355078b38ce20baea' and root['licenseSha256'] == MIT and root['crateSha256'] == CRATE and inventory['driverLockSha256'] == LOCK)
    refs = {'rust/tauri-driver-2.1.0/LICENSE-MIT': MIT}
    notices = inventory['rustToolchainNotices']
    require(set(notices) == {'COPYRIGHT.html', 'COPYRIGHT-library.html'})
    refs.update({'rust-toolchain/' + name: value for name, value in notices.items()})
    packages = inventory['packages']
    require(isinstance(packages, list) and bool(packages))
    package_ids = set()
    for package in packages:
        require(set(package) == {'name','version','source','license','texts','sourceArchive','crateChecksum','supplement'} and package['supplement'] is None and bool(package['license']))
        name = package['name'] + '-' + package['version']
        require(safe_name(name) and '/' not in name and name.casefold() not in package_ids)
        package_ids.add(name.casefold())
        require(package['source'] == 'registry+https://github.com/rust-lang/crates.io-index' and package['sourceArchive'] == 'https://static.crates.io/crates/' + package['name'] + '/' + name + '.crate' and re.fullmatch(r'[0-9a-f]{64}', package['crateChecksum']) is not None and bool(package['texts']))
        for name_part, expected in package['texts'].items():
            require(safe_name(name_part))
            file = Path(name_part)
            require(file.suffix.lower() in {'', '.txt', '.md', '.spdx', '.nuspec'} or file.name.lower() in {'license.mit','license.bsd-3-clause','license-apache-2.0'})
            key = 'rust/' + name + '/' + name_part
            require(key not in refs)
            refs[key] = expected
    require(set(files) == {'DEPENDENCIES.json'} | set(refs))
    for name, expected in refs.items():
        require(re.fullmatch(r'[0-9a-f]{64}', expected) is not None and not files[name].startswith(b'MZ') and digest(files[name]) == expected and bool(files[name].decode('utf-8-sig').strip()))

def provenance_check(data, expected_driver, app_scripts):
    result = parse_json(data)
    fields = {'variant','upstreamSha256','patchSha256','diagnosticCodeSha256','preimage','postimage','postimageManifestSha256','upstreamFileCount','cargoLockSha256','binarySha256'}
    require(set(result) == fields and result['variant'] == 'tauri-driver-2.1.0-stage-1' and result['upstreamSha256'] == CRATE and result['cargoLockSha256'] == LOCK and result['binarySha256'] == expected_driver and result['patchSha256'] == 'df8dd46174fc2b8aa4df4aed0c382713d7a3cadb5accda5f62aef74a960b5b9e')
    postimage_bytes = (app_scripts.resolve().parent / 'tools/native-driver-trace/postimage.json').read_bytes()
    require(digest(postimage_bytes) == '9aa32c62f4f93dd8cef07b4103e7373a227ae1077ee4d016d72b1d29f50bd8c5' and result['postimageManifestSha256'] == digest(postimage_bytes) and result['postimage'] == parse_json(postimage_bytes))
    require(result['diagnosticCodeSha256'] == {'diagnostic.rs': result['postimage']['src/diagnostic.rs'], 'diagnostic_tests.rs': result['postimage']['src/diagnostic_tests.rs']})
    pre = result['preimage']
    require(set(pre) == set(result['postimage']) and pre['src/diagnostic.rs'] is None and pre['src/diagnostic_tests.rs'] is None and all(isinstance(pre[name], str) and re.fullmatch(r'[0-9a-f]{64}', pre[name]) for name in ('src/main.rs','src/server.rs')) and isinstance(result['upstreamFileCount'], int) and 0 < result['upstreamFileCount'] < 10000)

def verify(payload, app_scripts):
    require(ordinary(payload, directory=True))
    children = list(payload.iterdir())
    require({p.name for p in children} == FILES and all(ordinary(p, directory=False) for p in children))
    raw = {}
    for path in children:
        require(0 < path.stat().st_size <= 64 * 1024 * 1024)
        raw[path.name] = path.read_bytes()
        if path.suffix != '.zip': clean(raw[path.name])
    manifest = parse_json(raw['manifest.json'])
    require(set(manifest) == {'schema','source','zipSha','exeSha','driverSha','provenanceSha','licensesSha'} and manifest['schema'] == 1 and manifest['source'] == SOURCE)
    for key, name in [('zipSha',APP), ('driverSha','tauri-driver.exe'), ('provenanceSha','driver-provenance.json'), ('licensesSha','driver-licenses.zip')]:
        require(manifest[key] == digest(raw[name]))
    provenance_check(raw['driver-provenance.json'], manifest['driverSha'], app_scripts)
    license_check(raw['driver-licenses.zip'])
    # 配布ZIP自身の認可条件・manifest・全license本文検査は既存固定sourceを再利用する。
    sys.path.insert(0, str(app_scripts.resolve()))
    import verify_tauri_evaluation
    verify_tauri_evaluation.verify(payload / APP, SOURCE, manifest['zipSha'], manifest['exeSha'])
    entries(raw[APP])
    return {'status': 'pass', 'source': SOURCE, 'sha256': {name: digest(value) for name, value in sorted(raw.items())}}

def main():
    class Parser(argparse.ArgumentParser):
        def error(self, message): raise ValueError('PAYLOAD_REJECTED')
    try:
        parser = Parser()
        parser.add_argument('--payload', type=Path, required=True)
        parser.add_argument('--app-scripts', type=Path, required=True)
        args = parser.parse_args()
        result = verify(args.payload, args.app_scripts)
    except Exception:
        print(json.dumps({'status': 'fail', 'code': 'PAYLOAD_REJECTED'}))
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0

if __name__ == '__main__':
    sys.exit(main())
