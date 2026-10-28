"""固定upstreamへの試験専用patch。文脈ずれ・未知file・未承認差分は拒否する。"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import sys
import tarfile

UPSTREAM = '2f2e29bd6900fb718dfdc06f2e7a371d6087c085daaf83709847ef3e6b0c1953'
PATCH_SHA256 = 'df8dd46174fc2b8aa4df4aed0c382713d7a3cadb5accda5f62aef74a960b5b9e'
CODE_SHA256 = {'diagnostic.rs': '495dc2a0a469a3a981d316571b91a6a1e775850058f7fee6c467a2929e2a6f54', 'diagnostic_tests.rs': '2807c3dfd7773ee1dbda590ca0c458c7d1e26f60af5f6f9d5fb2583415dbe86e'}
PREFIX = 'tauri-driver-2.1.0/'
POSTIMAGE_SHA256 = '9aa32c62f4f93dd8cef07b4103e7373a227ae1077ee4d016d72b1d29f50bd8c5'
ALLOWED = {'src/main.rs', 'src/server.rs', 'src/diagnostic.rs', 'src/diagnostic_tests.rs'}


class PatchError(Exception):
    pass


def need(value, code='DRIVER_PATCH_INVALID'):
    if not value:
        raise PatchError(code)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def apply_exact(original, patch):
    """小さなunified diffだけ。fuzz/offset探索/rename/binary/path選択は許さない。"""
    lines = patch.decode('utf-8').splitlines(keepends=True)
    result, index, seen = dict(original), 0, set()
    while index < len(lines):
        need(lines[index].startswith('--- '))
        old = lines[index][4:].rstrip('\n')
        index += 1
        need(index < len(lines) and lines[index].startswith('+++ b/'))
        name = lines[index][6:].rstrip('\n')
        index += 1
        need(name in ALLOWED and name not in seen)
        seen.add(name)
        need(old == 'a/' + name if name in original else old == '/dev/null')
        source = original.get(name, b'').decode('utf-8').splitlines(keepends=True)
        output, cursor, hunks = [], 0, 0
        while index < len(lines) and not lines[index].startswith('--- '):
            match = re.fullmatch(r'@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@\n', lines[index])
            need(match is not None)
            old_start, old_count, new_start, new_count = match.groups()
            start = int(old_start) - (0 if int(old_count or 1) == 0 else 1)
            need(cursor <= start <= len(source))
            output.extend(source[cursor:start])
            cursor = start
            need(len(output) == int(new_start) - (0 if int(new_count or 1) == 0 else 1))
            removed = added = 0
            index += 1
            while index < len(lines) and not lines[index].startswith(('@@ ', '--- ')):
                line = lines[index]
                need(line[:1] in {' ', '+', '-'})
                if line[0] in ' -':
                    need(cursor < len(source) and source[cursor] == line[1:])
                    cursor += 1
                    removed += 1
                if line[0] in ' +':
                    output.append(line[1:])
                    added += 1
                index += 1
            need(removed == int(old_count or 1) and added == int(new_count or 1))
            hunks += 1
        need(hunks > 0)
        result[name] = ''.join(output + source[cursor:]).encode()
    need(bool(seen))
    return result


def upstream_files(data):
    need(len(data) <= 1024 * 1024 and sha(data) == UPSTREAM, 'DRIVER_UPSTREAM_HASH_MISMATCH')
    files = {}
    with tarfile.open(fileobj=io.BytesIO(data)) as archive:
        for member in archive:
            need(member.name.startswith(PREFIX) and not member.issym() and not member.islnk())
            if member.isdir():
                continue
            name = member.name[len(PREFIX):]
            need(member.isfile() and member.size <= 512 * 1024 and name not in files
                 and name and all(part not in {'', '.', '..'} for part in name.split('/')))
            files[name] = archive.extractfile(member).read()
    return files


def snapshot(folder):
    need(folder.is_dir() and not folder.is_symlink())
    paths = list(folder.rglob('*'))
    need(len(paths) <= 256 and all(not path.is_symlink() for path in paths))
    need(all(path.is_dir() or path.is_file() for path in paths))
    return {path.relative_to(folder).as_posix(): path for path in paths if path.is_file()}


def bundle(patch_folder):
    need(patch_folder.is_dir() and not patch_folder.is_symlink())
    need({path.name for path in patch_folder.iterdir()} ==
         {'driver.patch', 'diagnostic.rs', 'diagnostic_tests.rs', 'postimage.json'},
         'DRIVER_BUNDLE_SET_MISMATCH')
    def read(name, limit):
        path = patch_folder / name
        need(path.is_file() and not path.is_symlink())
        with path.open('rb') as stream:
            data = stream.read(limit + 1)
        need(len(data) <= limit)
        return data
    patch = read('driver.patch', 65536)
    need(sha(patch) == PATCH_SHA256, 'DRIVER_PATCH_HASH_MISMATCH')
    code = {name: read(name, 65536) for name in CODE_SHA256}
    need(all(sha(data) == CODE_SHA256[name] for name, data in code.items()),
         'DRIVER_CODE_HASH_MISMATCH')
    metadata = read('postimage.json', 4096)
    need(sha(metadata) == POSTIMAGE_SHA256, 'DRIVER_POSTIMAGE_HASH_MISMATCH')
    expected = json.loads(metadata)
    return patch, code, expected


def check_tree(folder, expected):
    current = snapshot(folder)
    need(set(current) == set(expected), 'DRIVER_SOURCE_SET_MISMATCH')
    for name, path in current.items():
        need(path.stat().st_size == len(expected[name]) and sha(path.read_bytes()) == sha(expected[name]),
             'DRIVER_SOURCE_HASH_MISMATCH')


def apply(archive, folder, patch_folder, report, verify_only=False, binary=None):
    patch, code, expected = bundle(patch_folder)
    need(archive.is_file() and not archive.is_symlink())
    with archive.open('rb') as stream:
        before = upstream_files(stream.read(1024 * 1024 + 1))
    after = apply_exact(before, patch)
    for name, data in code.items():
        after['src/' + name] = data
    changed = {name for name in after if after[name] != before.get(name)}
    need(changed == set(expected) and changed <= ALLOWED
         and all(sha(after[name]) == digest for name, digest in expected.items()),
         'DRIVER_POSTIMAGE_MISMATCH')
    check_tree(folder, after if verify_only else before)
    if not verify_only:
        # 自己所有CI一時領域。敵対的filesystem競合は保証しない。
        for name in sorted(changed):
            target = folder / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(after[name])
        check_tree(folder, after)
    result = {'variant': 'tauri-driver-2.1.0-stage-1', 'upstreamSha256': UPSTREAM,
              'patchSha256': PATCH_SHA256, 'diagnosticCodeSha256': CODE_SHA256,
              'preimage': {name: sha(before[name]) if name in before else None for name in changed},
              'postimage': expected, 'postimageManifestSha256': POSTIMAGE_SHA256,
              'upstreamFileCount': len(before),
              'cargoLockSha256': sha(before['Cargo.lock'])}
    if binary is not None:
        need(verify_only and binary.is_file() and not binary.is_symlink())
        with binary.open('rb') as stream:
            data = stream.read(32 * 1024 * 1024 + 1)
        need(len(data) <= 32 * 1024 * 1024)
        result['binarySha256'] = sha(data)
    report.write_text(json.dumps(result, sort_keys=True), encoding='utf-8')


def main():
    class Parser(argparse.ArgumentParser):
        def error(self, message):
            raise PatchError('DRIVER_PATCH_ARGUMENT_INVALID')
    parser = Parser(description=__doc__)
    for arg in ['archive', 'source', 'patch-folder', 'provenance']:
        parser.add_argument('--' + arg, type=Path, required=True)
    parser.add_argument('--verify-patched', action='store_true')
    parser.add_argument('--binary', type=Path)
    try:
        args = parser.parse_args()
        apply(args.archive, args.source, args.patch_folder, args.provenance, args.verify_patched, args.binary)
    except Exception as error:
        print(json.dumps({'status': 'fail', 'code': str(error) if isinstance(error, PatchError)
                          else 'DRIVER_PATCH_INVALID'}))
        return 1
    print('DRIVER_DIAGNOSTIC_PATCH_VERIFIED')
    return 0


if __name__ == '__main__':
    sys.exit(main())
