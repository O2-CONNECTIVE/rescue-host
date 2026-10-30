"""record入口の有限fixture。分類/実file closeを検証し、COM成功の代替にしない。"""
import json
from pathlib import Path
import subprocess
import sys
import tempfile

SCENARIOS = {
    'environment': 'environment-callback-not-observed',
    'controller': 'controller-callback-not-observed',
    'dispatch': 'controller-callback-not-observed',
    'complete': 'webview-exit-guard-ok-observed',
    'script-pending': 'script-callback-not-observed',
    'script-callback': 'script-callback-observed-send-not-observed',
    'script-sent': 'script-send-observed-wait-return-not-observed',
    'script-multi': 'script-send-observed-wait-return-not-observed',
    'script-error-payload': 'webview-error-exit-observed',
    'script-api-error': 'webview-error-exit-observed',
    'script-send-error': 'webview-unwind-observed',
    'script-overflow': 'diagnostic-incomplete',
    'script-stale': 'diagnostic-incomplete',
    'script-early-seal': 'script-callback-observed-send-not-observed',
}


def main():
    executable = Path(sys.argv[1]).resolve()
    module = (Path(__file__).resolve().parents[2] / 'ui/tauri/native-smoke/wry-startup-stages.ts').as_uri()
    code = ('import {classifyWryStartup} from ' + json.dumps(module) + ';'
            'let raw="";for await(const v of process.stdin)raw+=v;'
            'console.log(JSON.stringify(classifyWryStartup(JSON.parse(raw))));')
    sizes = []
    bounded_sizes = []
    for mode, expected in SCENARIOS.items():
        with tempfile.TemporaryDirectory(prefix='rescue-wry-record-') as folder:
            completed = subprocess.run([str(executable), folder, mode], capture_output=True, timeout=5)
            assert completed.returncode == 0 and not completed.stdout and not completed.stderr, mode
            raw = (Path(folder) / 'wry-startup-stages.json').read_bytes()
            assert len(raw) <= 2048, (mode, len(raw))
            sizes.append(len(raw))
            value = json.loads(raw)
            # schemaが受理する最大桁数の保守的直積。意味上不整合でも長さ上界として使う。
            largest = json.loads(raw)
            largest['windowEndUs'] = 9007199254740991
            for event in largest['events']:
                event['elapsedUs'] = 9007199254740991
            for pump in largest['pumps'].values():
                pump['lastUs'] = 9007199254740991
                for key in ['iterations', 'getCalls', 'getReturns', 'dispatchCalls', 'dispatchReturns']:
                    pump[key] = 1000000
            for key in ['calls', 'apiReturns', 'callbacks', 'sends', 'returns', 'pumpOrdinal']:
                largest['scripts'][key] = 128
            for key in ['startedUs', 'apiStartUs', 'apiReturnUs', 'callbackUs', 'sentUs', 'returnUs']:
                largest['scripts'][key] = 9007199254740991
            largest['scripts']['result'] = 'pending'
            largest['scripts']['callbackOk'] = False
            upper = len(json.dumps(largest, separators=(',', ':')).encode())
            assert upper <= 2048, (mode, upper)
            bounded_sizes.append(upper)
            assert value['variant'] == 'wry-webview2-stage-3' and value['sealed'] is True
            assert value['attempted'] == value['recorded'] == len(value['events']) <= 12
            assert value['dropped'] == 0 and value['inFlight'] == 0
            assert all(set(row) == {'seq', 'stage', 'elapsedUs'} for row in value['events'])
            classified = subprocess.run(['node', '--input-type=module', '-e', code], input=raw,
                                        capture_output=True, timeout=5)
            assert classified.returncode == 0 and not classified.stderr, mode
            report = json.loads(classified.stdout)
            assert report['phase'] == expected, (mode, report['phase'])
            script = value['scripts']
            if mode == 'script-multi':
                assert script['calls'] == 2 and script['returns'] == 1 and script['pumpOrdinal'] == 2
            if mode == 'script-error-payload':
                assert script['callbackOk'] is False and script['sends'] == 1 and script['result'] == 'error'
            if mode in {'script-stale', 'script-overflow'}:
                assert script['invalid'] is True
            if mode == 'script-send-error':
                assert script['callbacks'] == 1 and script['sends'] == 0
            if mode == 'script-early-seal':
                assert script['sends'] == 0
    print('WRY_PUBLIC_RECORD_FIXTURE_14_PASSED maxBytes=' + str(max(sizes)) + ' maxBoundaryBytes=' + str(max(bounded_sizes)))


if __name__ == '__main__':
    main()
