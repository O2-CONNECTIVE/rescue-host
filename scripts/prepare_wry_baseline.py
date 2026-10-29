"""既存lockだけから一時Wry baseline projectを作り、来歴を照合する。"""
import argparse
import copy
import hashlib
import json
from pathlib import Path
import re
import sys
import tomllib

ROOT_DEPS = ['serde', 'serde_json', 'tauri', 'tauri-build', 'windows-sys 0.61.2']
PINS = {'tao': '0.37.2', 'wry': '0.57.1'}


class Rejected(Exception):
    pass


def need(value):
    if not value:
        raise Rejected()


def safe_path(path):
    # 自己所有CI temp用。敵対的な同時filesystem変更への隔離ではない。
    need(not path.is_symlink())


def read(path):
    safe_path(path)
    need(path.is_file())
    with path.open('rb') as stream:
        data = stream.read(1024 * 1024 + 1)
    need(len(data) <= 1024 * 1024)
    return data


def sha(data):
    return hashlib.sha256(data).hexdigest()


def manifest_bytes(data):
    before = tomllib.loads(data.decode())
    need(before['package']['name'] == 'rescue-host-shell' and before['package']['version'] == '0.1.0')
    need(not set(PINS).intersection(before['dependencies']))
    need(not {'autobins', 'autolib', 'build'}.intersection(before['package']))
    need('bin' not in before and 'lib' not in before)
    expected = copy.deepcopy(before)
    expected['package'].update(autobins=False, autolib=False, build=False)
    expected['dependencies'].update({name: {'version': '=' + version, 'default-features': False}
                                     for name, version in PINS.items()})
    expected.pop('test', None)
    expected['bin'] = [{'name': 'rescue-wry-creation-baseline', 'path': 'main.rs'}]
    parts = re.split(r'(?m)(?=^\[)', data.decode())
    output = []
    for part in parts:
        header = part.splitlines()[0].strip() if part else ''
        if header == '[[test]]':
            continue
        if header == '[package]':
            part += '\nautobins = false\nautolib = false\nbuild = false\n\n'
        elif header == '[dependencies]':
            part += ''.join(f'\n{name} = {{ version = "={version}", default-features = false }}\n'
                            for name, version in PINS.items()) + '\n'
        output.append(part)
    output.append('\n[[bin]]\nname = "rescue-wry-creation-baseline"\npath = "main.rs"\n')
    generated = ''.join(output).encode()
    need(tomllib.loads(generated.decode()) == expected)
    return generated


def lock_bytes(data):
    before = tomllib.loads(data.decode())
    packages = before['package']
    roots = [p for p in packages if p['name'] == 'rescue-host-shell']
    need(len(roots) == 1 and roots[0] == {'name': 'rescue-host-shell', 'version': '0.1.0',
                                       'dependencies': ROOT_DEPS})
    for name, version in PINS.items():
        selected = [p for p in packages if p['name'] == name]
        need(len(selected) == 1 and selected[0]['version'] == version)
        if name == 'wry':
            need('source' not in selected[0] and 'checksum' not in selected[0])
    expected = copy.deepcopy(before)
    next(p for p in expected['package'] if p['name'] == 'rescue-host-shell')['dependencies'] = sorted(ROOT_DEPS + list(PINS))
    parts = re.split(r'(?m)(?=^\[\[package\]\])', data.decode())
    for index, part in enumerate(parts):
        if part.startswith('[[package]]'):
            package = tomllib.loads(part)['package'][0]
            if package['name'] == 'rescue-host-shell':
                replacement = 'dependencies = [\n' + ''.join(f' "{name}",\n' for name in sorted(ROOT_DEPS + list(PINS))) + ']'
                parts[index], count = re.subn(r'dependencies = \[.*?\]', replacement, part, flags=re.S)
                need(count == 1)
    generated = ''.join(parts).encode()
    # 版、checksum、source、全packageの辺をrootへの2辺追加以外すべて照合する。
    need(tomllib.loads(generated.decode()) == expected)
    return generated, len(packages)


def run(args):
    need(re.fullmatch('[0-9a-f]{40}', args.source_commit))
    manifest, lock, fixture = read(args.manifest), read(args.lock), read(args.fixture)
    generated_manifest = manifest_bytes(manifest)
    generated_lock, count = lock_bytes(lock)
    expected = {'Cargo.toml': generated_manifest, 'Cargo.lock': generated_lock, 'main.rs': fixture}
    safe_path(args.output)
    safe_path(args.report)
    need(args.report.parent.is_dir() and args.report.parent.resolve() != args.output.resolve())
    need(args.report.resolve() != args.output.resolve())
    inputs = {p.resolve() for p in [args.manifest, args.lock, args.fixture]}
    need(args.report.resolve() not in inputs and args.output.resolve() not in inputs)
    report = {'schemaVersion': 1, 'sourceCommit': args.source_commit,
              'sourceManifestSha256': sha(manifest), 'sourceLockSha256': sha(lock),
              'manifestSha256': sha(generated_manifest), 'lockSha256': sha(generated_lock),
              'fixtureSha256': sha(fixture), 'packageCount': count,
              'rootDependenciesBefore': ROOT_DEPS, 'rootDependenciesAfter': sorted(ROOT_DEPS + list(PINS))}
    report_bytes = (json.dumps(report, sort_keys=True, separators=(',', ':')) + '\n').encode()
    if args.verify:
        need(args.output.is_dir() and {p.name for p in args.output.iterdir()} == set(expected))
        need(all(read(args.output / name) == data for name, data in expected.items()))
        need(read(args.report) == report_bytes)
    else:
        need(not args.report.exists())
        need(not args.output.exists() or (args.output.is_dir() and not any(args.output.iterdir())))
        need(not any(args.output.resolve() in p.parents for p in inputs))
        args.output.mkdir(exist_ok=True)
        for name, data in expected.items():
            (args.output / name).write_bytes(data)
        args.report.write_bytes(report_bytes)


def main():
    class Parser(argparse.ArgumentParser):
        def error(self, message):
            raise Rejected()
    parser = Parser(description=__doc__)
    for name in ['manifest', 'lock', 'fixture', 'output', 'report']:
        parser.add_argument('--' + name, type=Path, required=True)
    parser.add_argument('--source-commit', required=True)
    parser.add_argument('--verify', action='store_true')
    try:
        run(parser.parse_args())
    except Exception:
        print('WRY_BASELINE_PROVENANCE_REJECTED')
        return 1
    print('WRY_BASELINE_PROVENANCE_VERIFIED')
    return 0


if __name__ == '__main__':
    sys.exit(main())
