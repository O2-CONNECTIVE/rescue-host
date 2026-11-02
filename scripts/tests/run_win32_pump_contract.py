"""公式固定loopと自己所有windowの有限識別試験。WebView成功の代替ではない。"""
import hashlib
import io
import json
import os
import re
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile

ARCHIVE_SHA = '3f89fca7a704cee10dcb3654c1dbb8941d1783132f1917358af75bec37a7d7e6'
FUNCTION_SHA = '4d3cc45fbd1351d11b75950ddbfd8ea31d236209979e19dbbae3285aaa827010'


def run(archive, report):
    assert os.name == 'nt' and not report.exists()
    commit = os.environ.get('STARTUP_SOURCE', '')
    assert re.fullmatch('[0-9a-f]{40}', commit)
    raw = archive.read_bytes()
    assert len(raw) < 65536 and hashlib.sha256(raw).hexdigest() == ARCHIVE_SHA
    with tarfile.open(fileobj=io.BytesIO(raw)) as source:
        module = source.extractfile('webview2-com-0.39.1/src/lib.rs').read()
    function = module[module.index(b'pub fn wait_with_pump<T>'):]
    assert hashlib.sha256(function).hexdigest() == FUNCTION_SHA
    root = Path(__file__).resolve().parents[2]
    rows = []
    with tempfile.TemporaryDirectory(prefix='rescue-win32-pump-') as temporary:
        folder = Path(temporary)
        reference = folder / 'reference-loop.rs'
        reference.write_bytes(function)
        executable = folder / 'pump-contract.exe'
        environment = dict(os.environ, RESCUE_REFERENCE_PUMP=str(reference))
        compiled = subprocess.run(['rustc', '+1.99.1', '--edition', '2021',
                                   str(root / 'tools/win32-pump-contract/main.rs'), '-o', str(executable)],
                                  capture_output=True, env=environment, timeout=120)
        assert compiled.returncode == 0
        executable_sha = hashlib.sha256(executable.read_bytes()).hexdigest()
        for mode in ['sent-completion', 'posted-completion', 'no-completion']:
            completed = subprocess.run([str(executable), mode], capture_output=True, timeout=5)
            assert completed.returncode == 0 and not completed.stderr and len(completed.stdout) <= 2048
            value = json.loads(completed.stdout)
            assert value == {
                'schema': 1, 'purpose': 'win32-contract-only', 'mode': mode,
                'beforeWakeVerified': mode != 'posted-completion',
                'completionObserved': mode != 'no-completion', 'getReturns': 19,
                'dispatchReturns': 18 if mode == 'no-completion' else 19,
                'workerJoined': True, 'windowDestroyed': True, 'classUnregistered': True,
            }
            rows.append(value)
    assert not folder.exists()
    source_hashes = {name: hashlib.sha256((root / 'tools/win32-pump-contract' / name).read_bytes()).hexdigest()
                     for name in ['main.rs', 'win32.rs']}
    report.write_text(json.dumps({'schema': 1, 'sourceCommit': commit, 'sourceSha256': source_hashes,
                                 'fixtureExeSha256': executable_sha, 'archiveSha256': ARCHIVE_SHA,
                                 'functionSha256': FUNCTION_SHA, 'scenarios': rows,
                                 'temporaryDirectoryRemoved': True}, separators=(',', ':')), encoding='utf-8')


if __name__ == '__main__':
    try:
        run(Path(sys.argv[1]), Path(sys.argv[2]))
    except Exception:
        print('WIN32_PUMP_CONTRACT_FAILED')
        sys.exit(1)
    print('WIN32_PUMP_CONTRACT_3_PASSED')
