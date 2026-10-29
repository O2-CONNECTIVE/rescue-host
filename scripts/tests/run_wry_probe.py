"""stdlib record入口の自己所有fixture。COM/native成功の代替ではない。"""
import json
from pathlib import Path
import subprocess
import sys
import tempfile


def main():
    executable = Path(sys.argv[1]).resolve()
    for mode, last, count in [('environment', 'environment-api-return', 3),
                              ('controller', 'controller-api-return', 7),
                              ('dispatch', 'controller-api-return', 7),
                              ('complete', 'controller-wait-return', 9)]:
        with tempfile.TemporaryDirectory(prefix='rescue-wry-record-') as folder:
            completed = subprocess.run([str(executable), folder, mode], capture_output=True, timeout=5)
            assert completed.returncode == 0 and not completed.stdout and not completed.stderr
            raw = (Path(folder) / 'wry-startup-stages.json').read_bytes()
            assert len(raw) <= 2048
            value = json.loads(raw)
            assert value['variant'] == 'wry-webview2-stage-2' and value['sealed'] is True
            assert value['attempted'] == count and value['recorded'] == count
            assert value['dropped'] == 0 and value['inFlight'] == 0
            assert value['events'][-1]['stage'] == last
            assert value['windowEndUs'] >= value['events'][-1]['elapsedUs']
            assert all(set(row) == {'seq', 'stage', 'elapsedUs'} for row in value['events'])
            active = value['pumps']['environment' if mode == 'environment' else 'controller']
            assert active['phase'] == {'environment': 'get-message', 'controller': 'get-message',
                                       'dispatch': 'dispatch', 'complete': 'receiver-ready'}[mode]
            assert active['invalid'] is False and active['lastUs'] <= value['windowEndUs']
            assert active['dispatchCalls'] == (1 if mode in {'dispatch', 'complete'} else 0)
            assert active['dispatchReturns'] == (1 if mode == 'complete' else 0)
    print('WRY_PUBLIC_RECORD_FIXTURE_4_PASSED')


if __name__ == '__main__':
    main()
