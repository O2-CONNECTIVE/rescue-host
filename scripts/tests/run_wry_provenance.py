"""固定公式sourceを使うCLI回帰。fixture binaryはhash入力だけで実行しない。"""
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


def main():
    archive, pump_archive, bundle, source, lock = map(Path, sys.argv[1:6])
    commit = sys.argv[6]
    script = Path(__file__).resolve().parents[1] / 'apply_wry_trace.py'
    for mode in ['valid', 'source', 'extra', 'lock', 'code', 'pump', 'pump-archive']:
        with tempfile.TemporaryDirectory(prefix='rescue-wry-provenance-') as directory:
            root = Path(directory)
            tree = root / 'source'
            patch = root / 'bundle'
            shutil.copytree(source, tree)
            shutil.copytree(bundle, patch)
            saved_lock = root / 'Cargo.lock'
            saved_lock.write_bytes(lock.read_bytes())
            exe = root / 'fixture.bin'
            exe.write_bytes(b'NOT_AN_EXECUTABLE')
            saved_pump = root / 'pump.crate'
            saved_pump.write_bytes(pump_archive.read_bytes())
            if mode == 'source':
                with (tree / 'src/lib.rs').open('ab') as stream:
                    stream.write(b'PRIVATE_FIXTURE')
            elif mode == 'extra':
                (tree / 'unrelated.txt').write_text('PRIVATE_FIXTURE')
            elif mode == 'lock':
                saved_lock.write_bytes(b'PRIVATE_FIXTURE')
            elif mode == 'code':
                with (patch / 'probe.rs').open('ab') as stream:
                    stream.write(b'PRIVATE_FIXTURE')
            elif mode == 'pump':
                with (patch / 'pump.rs').open('ab') as stream:
                    stream.write(b'PRIVATE_FIXTURE')
            elif mode == 'pump-archive':
                saved_pump.write_bytes(b'PRIVATE_FIXTURE')
            result = subprocess.run([sys.executable, '-I', str(script), '--verify', '--archive', str(archive),
                '--pump-archive', str(saved_pump),
                '--bundle', str(patch), '--source', str(tree), '--app-lock', str(saved_lock),
                '--report', str(root / 'report.json'), '--source-commit', commit, '--exe', str(exe)],
                capture_output=True, timeout=5)
            if (result.returncode == 0) != (mode == 'valid') or result.stderr or b'PRIVATE_FIXTURE' in result.stdout:
                raise RuntimeError('WRY_PROVENANCE_FIXTURE_FAILED')
    print('WRY_PROVENANCE_FIXTURE_7_PASSED')


if __name__ == '__main__':
    main()
