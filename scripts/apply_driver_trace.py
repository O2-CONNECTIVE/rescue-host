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
PATCH_SHA256 = '35f4f12a913603ebb6a201f40d20530827d54f94e64a359a618526d490609ae5'
CODE_SHA256 = {'diagnostic.rs': '5a0d00a4032ef5dd7f75b6cba10b75fe07f29a9448035497720c449fd562cf6e', 'diagnostic_tests.rs': 'ccce02206e54296cd2407b60ebbbdf11df1a2c72f7b0086b9c7771b9c77f3bb4'}
PREFIX = 'tauri-driver-2.1.0/'
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


def apply(archive, folder, patch_folder, report):
    need(archive.is_file() and not archive.is_symlink())
    with archive.open('rb') as stream:
        before = upstream_files(stream.read(1024 * 1024 + 1))
    patch = (patch_folder / 'driver.patch').read_bytes()
    need(sha(patch) == PATCH_SHA256, 'DRIVER_PATCH_HASH_MISMATCH')
    after = apply_exact(before, patch)
    for name, digest in CODE_SHA256.items():
        data = (patch_folder / name).read_bytes()
        need(sha(data) == digest, 'DRIVER_CODE_HASH_MISMATCH')
        after['src/' + name] = data
    expected = json.loads((patch_folder / 'postimage.json').read_text(encoding='utf-8'))
    changed = {name for name in after if after[name] != before.get(name)}
    need(changed == set(expected) and changed <= ALLOWED
         and all(sha(after[name]) == digest for name, digest in expected.items()),
         'DRIVER_POSTIMAGE_MISMATCH')
    current = snapshot(folder)
    need(set(current) == set(before), 'DRIVER_SOURCE_SET_MISMATCH')
    for name, path in current.items():
        need(path.stat().st_size == len(before[name]) and sha(path.read_bytes()) == sha(before[name]),
             'DRIVER_SOURCE_HASH_MISMATCH')
    # 全検査後だけ書込む。対象は自己所有CI一時領域、敵対的filesystem競合は保証しない。
    for name in sorted(changed):
        target = folder / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(after[name])
    current = snapshot(folder)
    need(set(current) == set(after) and all(sha(path.read_bytes()) == sha(after[name])
                                          for name, path in current.items()), 'DRIVER_POSTIMAGE_MISMATCH')
    result = {'variant': 'tauri-driver-2.1.0-stage-1', 'upstreamSha256': UPSTREAM,
              'patchSha256': PATCH_SHA256, 'diagnosticCodeSha256': CODE_SHA256, 'postimage': expected,
              'upstreamFileCount': len(before), 'cargoLockSha256': sha(before['Cargo.lock'])}
    report.write_text(json.dumps(result, sort_keys=True), encoding='utf-8')


def main():
    class Parser(argparse.ArgumentParser):
        def error(self, message):
            raise PatchError('DRIVER_PATCH_ARGUMENT_INVALID')
    parser = Parser(description=__doc__)
    for arg in ['archive', 'source', 'patch-folder', 'provenance']:
        parser.add_argument('--' + arg, type=Path, required=True)
    try:
        args = parser.parse_args()
        apply(args.archive, args.source, args.patch_folder, args.provenance)
    except Exception as error:
        print(json.dumps({'status': 'fail', 'code': str(error) if isinstance(error, PatchError)
                          else 'DRIVER_PATCH_INVALID'}))
        return 1
    print('DRIVER_DIAGNOSTIC_PATCH_VERIFIED')
    return 0


if __name__ == '__main__':
    sys.exit(main())
