"""公式固定crateにだけ試験専用観測を適用する。通常buildからは呼ばない。"""
import argparse
import hashlib
import io
import json
from pathlib import Path
import re
import sys
import tarfile

PINS_SHA256 = 'ce3120bc3f555a2bf67352407e9a279f074d56e9302aef71f0857cfb5716d054'
ALLOWED = {'src/lib.rs', 'src/webview2/mod.rs', 'src/startup_probe.rs', 'src/startup_pump.rs', 'src/startup_script.rs'}


class Rejected(Exception):
    pass


def need(value):
    if not value:
        raise Rejected()


def sha(data):
    return hashlib.sha256(data).hexdigest()


def read(path, limit):
    need(path.is_file() and not path.is_symlink())
    with path.open('rb') as stream:
        data = stream.read(limit + 1)
    need(len(data) <= limit)
    return data


def derive_pump_source(source):
    """公式の末尾関数だけから生成。待ちAPI/分岐/戻り値を置換しない。"""
    function = source[source.index('pub fn wait_with_pump<T>'):]
    changes = [
        ('(rx: mpsc::Receiver<T>)', '(rx: mpsc::Receiver<T>, scope: PumpScope)'),
        ('    let mut msg = MSG::default();', '    observe_pump(scope, PumpSignal::Enter);\n    let mut msg = MSG::default();'),
        ('    loop {', '    loop {\n        observe_pump(scope, PumpSignal::Check);'),
        ('            return Ok(result);', '            observe_pump(scope, PumpSignal::Ready);\n            return Ok(result);'),
        ('        unsafe {', '        observe_pump(scope, PumpSignal::GetEnter);\n        unsafe {'),
        ('                    return Err(windows::core::Error::from_thread().into());',
         '                    let error = windows::core::Error::from_thread();\n                    observe_pump(scope, PumpSignal::GetError);\n                    return Err(error.into());'),
        ('                0 => return Err(Error::TaskCanceled),',
         '                0 => { observe_pump(scope, PumpSignal::GetQuit); return Err(Error::TaskCanceled); },'),
        ('                    let _ = WindowsAndMessaging::TranslateMessage(&msg);',
         '                    observe_pump(scope, PumpSignal::GetMessage);\n                    let _ = WindowsAndMessaging::TranslateMessage(&msg);\n                    observe_pump(scope, PumpSignal::DispatchEnter);'),
        ('                    WindowsAndMessaging::DispatchMessageA(&msg);',
         '                    WindowsAndMessaging::DispatchMessageA(&msg);\n                    observe_pump(scope, PumpSignal::DispatchReturn);'),
    ]
    changed = function
    for old, new in changes:
        need(changed.count(old) == 1)
        changed = changed.replace(old, new)
    header = ('// 固定公式関数から生成する試験専用loop。通常配布には使わない。\n'
              'use std::sync::mpsc;\nuse webview2_com::{Error, Result};\n'
              'use windows::Win32::UI::WindowsAndMessaging::{self, MSG};\n'
              'use crate::startup_probe::{observe_pump, PumpScope, PumpSignal};\n')
    return function.encode(), (header + changed).encode()


def prepare_bytes(archive, pump_archive, macro_archive, bundle):
    need(bundle.is_dir() and not bundle.is_symlink())
    need({x.name for x in bundle.iterdir()} == {'probe.rs', 'fixture.rs', 'pins.json', 'replacements.json',
         'UPSTREAM_LICENSE.txt', 'pump.rs', 'PUMP_UPSTREAM_LICENSE.txt', 'startup_script.rs'})
    pin_bytes = read(bundle / 'pins.json', 8192)
    need(sha(pin_bytes) == PINS_SHA256)
    pins = json.loads(pin_bytes)
    need(sha(archive) == pins['upstreamSha256'])
    need(sha(pump_archive) == pins['pumpUpstreamSha256'])
    need(sha(macro_archive) == pins['macroUpstreamSha256'])
    with tarfile.open(fileobj=io.BytesIO(macro_archive)) as source:
        member = source.getmember('webview2-com-macros-0.8.1/src/lib.rs')
        need(member.isfile() and member.size <= 65536)
        module = source.extractfile(member).read()
        function = module[module.index(b'fn impl_completed_callback('):module.index(b'/// Implement an `EventCallback`')]
        need(sha(function) == pins['macroFunctionSha256'])
    with tarfile.open(fileobj=io.BytesIO(pump_archive)) as source:
        member = source.getmember('webview2-com-0.39.1/src/lib.rs')
        need(member.isfile() and member.size <= 65536)
        function, generated = derive_pump_source(source.extractfile(member).read().decode())
    pump = read(bundle / 'pump.rs', 32768)
    need(sha(function) == pins['pumpFunctionSha256'] and pump == generated and sha(pump) == pins['pumpCodeSha256'])
    need(sha(read(bundle / 'PUMP_UPSTREAM_LICENSE.txt', 8192)) == pins['pumpLicenseSha256'])
    before = {}
    with tarfile.open(fileobj=io.BytesIO(archive)) as source:
        for member in source:
            need(member.name.startswith('wry-0.57.1/') and not member.issym() and not member.islnk())
            if member.isdir():
                continue
            name = member.name[len('wry-0.57.1/'):]
            need(member.isfile() and member.size <= 1024 * 1024 and name not in before
                 and all(x not in {'', '.', '..'} for x in name.split('/')))
            before[name] = source.extractfile(member).read()
            need(len(before) <= 256)
    need(read(bundle / 'UPSTREAM_LICENSE.txt', 8192) == before['LICENSE-MIT'])
    patch = read(bundle / 'replacements.json', 65536)
    code = read(bundle / 'probe.rs', 32768)
    scripts = read(bundle / 'startup_script.rs', 32768)
    need(sha(scripts) == pins['scriptCodeSha256'])
    need(sha(patch) == pins['replacementsSha256'] and sha(code) == pins['codeSha256'])
    after = dict(before)
    for change in json.loads(patch):
        name, old, new = change['file'], change['old'], change['new']
        need(name in ALLOWED and name in before and old and after[name].decode().count(old) == 1)
        after[name] = after[name].decode().replace(old, new).encode()
    after['src/startup_probe.rs'] = code
    after['src/startup_pump.rs'] = pump
    after['src/startup_script.rs'] = scripts
    changed = {n for n in after if after[n] != before.get(n)}
    need(changed == ALLOWED and set(pins['postimage']) == ALLOWED)
    need(all(sha(after[n]) == pins['postimage'][n] for n in changed))
    need(all((sha(before[n]) if n in before else None) == pins['preimage'][n] for n in changed))
    return pins, after


def check_tree(folder, expected):
    need(folder.is_dir() and not folder.is_symlink())
    paths = list(folder.rglob('*'))
    need(len(paths) <= 512 and all(not p.is_symlink() and (p.is_file() or p.is_dir()) for p in paths))
    actual = {p.relative_to(folder).as_posix(): p for p in paths if p.is_file()}
    need(set(actual) == set(expected))
    need(all(read(actual[n], 1024 * 1024) == data for n, data in expected.items()))


def run(args):
    pins, after = prepare_bytes(read(args.archive, 2 * 1024 * 1024), read(args.pump_archive, 65536), read(args.macro_archive, 65536), args.bundle)
    lock = read(args.app_lock, 1024 * 1024)
    need(re.fullmatch('[0-9a-f]{40}', args.source_commit) and not args.report.exists())
    if args.verify:
        need(sha(lock) == pins['lockAfterSha256'])
        check_tree(args.source, after)
        binary = read(args.exe, 64 * 1024 * 1024)
        result = {k: pins[k] for k in ['variant', 'upstreamSha256', 'replacementsSha256', 'codeSha256',
                                     'postimage', 'lockBeforeSha256', 'lockAfterSha256', 'pumpUpstreamSha256',
                                     'pumpFunctionSha256', 'pumpCodeSha256', 'pumpLicenseSha256',
                                     'macroUpstreamSha256', 'macroFunctionSha256', 'scriptCodeSha256']}
        result.update(pinsSha256=PINS_SHA256, sourceCommit=args.source_commit, exeSha256=sha(binary))
    else:
        need(not args.source.exists() and sha(lock) == pins['lockBeforeSha256'])
        old = ('name = "wry"\nversion = "0.57.1"\nsource = "registry+https://github.com/rust-lang/crates.io-index"\n'
               'checksum = "' + pins['upstreamSha256'] + '"\n').encode()
        need(lock.count(old) == 1)
        patched = lock.replace(old, b'name = "wry"\nversion = "0.57.1"\n')
        need(sha(patched) == pins['lockAfterSha256'])
        # 自己所有の信頼されたCI tempだけ。敵対的filesystem/同一userへの隔離ではない。
        args.source.mkdir()
        for name, data in after.items():
            target = args.source / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(data)
        check_tree(args.source, after)
        args.app_lock.write_bytes(patched)
        result = {'status': 'prepared', 'pinsSha256': PINS_SHA256}
    args.report.write_text(json.dumps(result, sort_keys=True, separators=(',', ':')), encoding='utf-8')


def main():
    class Parser(argparse.ArgumentParser):
        def error(self, message):
            raise Rejected()
    parser = Parser(description=__doc__)
    for name in ['archive', 'pump-archive', 'macro-archive', 'bundle', 'source', 'app-lock', 'report']:
        parser.add_argument('--' + name, type=Path, required=True)
    parser.add_argument('--source-commit', required=True)
    parser.add_argument('--verify', action='store_true')
    parser.add_argument('--exe', type=Path)
    try:
        args = parser.parse_args()
        run(args)
    except Exception:
        print('WRY_DIAGNOSTIC_PROVENANCE_REJECTED')
        return 1
    print('WRY_DIAGNOSTIC_PROVENANCE_VERIFIED')
    return 0


if __name__ == '__main__':
    sys.exit(main())
