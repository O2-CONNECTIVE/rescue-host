"""試験専用driver traceの固定schema/完全性。native試験の合否は変更しない。"""
import json
from pathlib import Path
import sys
from verify_tauri_evaluation import SafeParser

VARIANT = 'tauri-driver-2.1.0-stage-2'
STATES = {'idle', 'received', 'forward-pending', 'headers-returned', 'native-error', 'dropped'}
ERRORS = {'none', 'incomplete', 'canceled', 'closed', 'parse', 'other'}
STAGES = {'connection-open', 'session-received', 'forward-start', 'native-headers',
          'native-error', 'request-dropped', 'connection-end',
          'native-connection-assigned', 'native-connection-unobserved'}


def need(condition):
    if not condition:
        raise ValueError('invalid')


def integer(value):
    return type(value) is int and 0 <= value <= 1_000_000_000


def unique(pairs):
    obj = {}
    for key, value in pairs:
        need(key not in obj)
        obj[key] = value
    return obj


def inspect(data):
    need(len(data) <= 8192)
    lines = data.splitlines()
    need(2 <= len(lines) <= 34 and all(len(line) <= 256 for line in lines))
    rows = [json.loads(line, object_pairs_hook=unique) for line in lines]
    need(rows[0] == {'type': 'header', 'schema': 1, 'variant': VARIANT})
    need(type(rows[0]['schema']) is int)
    summary = rows[-1]
    need(isinstance(summary, dict) and set(summary) == {'type', 'lastSequence', 'attempted',
         'enqueued', 'written', 'dropped', 'sealed', 'pendingRequests'})
    need(summary['type'] == 'summary' and type(summary['sealed']) is bool
         and all(integer(summary[k]) for k in summary if k not in {'type', 'sealed'}))
    events = rows[1:-1]
    complete = (summary['sealed'] and summary['pendingRequests'] == 0
                and summary['lastSequence'] == summary['attempted'] == len(events)
                and summary['enqueued'] == summary['written'] == len(events)
                and summary['dropped'] == 0)
    connections, requests = {}, {}
    observed, sequence = set(), []
    last_elapsed = 0
    for row in events:
        need(isinstance(row, dict) and set(row) == {'type', 'seq', 'connection', 'request',
                                                  'stage', 'state', 'elapsedUs', 'error'})
        need(row['type'] == 'event' and row['stage'] in STAGES and row['state'] in STATES
             and row['error'] in ERRORS
             and all(integer(row[k]) for k in ['seq', 'connection', 'request', 'elapsedUs']))
        need(row['seq'] > 0 and 1 <= row['connection'] <= 32 and row['request'] <= 32)
        need(row['elapsedUs'] >= last_elapsed)
        last_elapsed = row['elapsedUs']
        sequence.append(row['seq'])
        observed.add(row['stage'])
        conn, req, stage = row['connection'], row['request'], row['stage']
        if stage == 'connection-open':
            need(conn not in connections and req == 0 and row['state'] == 'idle'
                 and row['error'] == 'none')
            connections[conn] = {'ended': False, 'last': 0, 'elapsed': row['elapsedUs']}
            continue
        need(conn in connections)
        need(row['elapsedUs'] >= connections[conn]['elapsed'])
        connections[conn]['elapsed'] = row['elapsedUs']
        if stage == 'session-received':
            need(not connections[conn]['ended'] and req > 0 and req not in requests
                 and row['state'] == 'received')
            requests[req] = {'connection': conn, 'state': 'received', 'forward': False,
                             'end': 'unobserved', 'nativeConnection': 'not-sampled'}
            connections[conn]['last'] = req
        elif stage == 'connection-end':
            need(not connections[conn]['ended'] and req == connections[conn]['last'])
            need(row['state'] == (requests[req]['state'] if req else 'idle'))
            connections[conn]['ended'] = True
            if req:
                requests[req]['end'] = 'clean' if row['error'] == 'none' else row['error']
        else:
            need(req in requests and requests[req]['connection'] == conn)
            current = requests[req]
            if stage == 'forward-start':
                need(current['state'] == 'received' and row['state'] == 'forward-pending')
                current['forward'] = True
            elif stage in {'native-connection-assigned', 'native-connection-unobserved'}:
                need(current['state'] == row['state'] == 'forward-pending'
                     and current['nativeConnection'] == 'not-sampled')
                current['nativeConnection'] = ('assigned' if stage == 'native-connection-assigned'
                                               else 'unobserved')
            elif stage in {'native-headers', 'native-error'}:
                need(current['state'] == 'forward-pending'
                     and current['nativeConnection'] != 'not-sampled'
                     and row['state'] == ('headers-returned' if stage == 'native-headers' else 'native-error'))
            elif stage == 'request-dropped':
                need(current['state'] in {'received', 'forward-pending'} and row['state'] == 'dropped'
                     and (not current['forward'] or current['nativeConnection'] != 'not-sampled'))
            current['state'] = row['state']
        if stage != 'connection-end':
            need(row['error'] == 'none')
    need(sequence == sorted(set(sequence)))
    complete = complete and sequence == list(range(1, len(events) + 1)) and len(requests) == 1
    request = next(iter(requests.values()), None)
    complete = complete and request['state'] in {'headers-returned', 'native-error', 'dropped'}
    result = {'check': 'native-driver-stage', 'diagnostic': 'complete' if complete else 'incomplete',
              'code': 'TRACE_COMPLETE' if complete else 'TRACE_INCOMPLETE',
              'observedStages': sorted(observed), 'requestStage': 'unconfirmed',
              'connectionEnd': 'unconfirmed', 'nativeConnection': 'unconfirmed',
              'events': events, 'summary': summary}
    if complete:
        result['requestStage'] = {'headers-returned': 'native-headers', 'native-error': 'native-error',
                                  'dropped': 'dropped-before-headers' if request['forward']
                                  else 'dropped-before-forward'}[request['state']]
        result['connectionEnd'] = request['end']
        result['nativeConnection'] = request['nativeConnection']
    return result


def main():
    parser = SafeParser(description=__doc__)
    parser.add_argument('--trace', type=Path, required=True)
    try:
        args = parser.parse_args()
        need(args.trace.is_file() and not args.trace.is_symlink())
        with args.trace.open('rb') as stream:
            report = inspect(stream.read(8193))
    except Exception:
        # 診断専用。原例外・filename・入力値は出さず、既存native判定にも接続しない。
        report = {'check': 'native-driver-stage', 'diagnostic': 'incomplete', 'code': 'TRACE_INVALID',
                  'observedStages': [], 'requestStage': 'unconfirmed', 'connectionEnd': 'unconfirmed',
                  'nativeConnection': 'unconfirmed',
                  'events': [], 'summary': None}
    print(json.dumps(report, sort_keys=True))
    return 0 if report['diagnostic'] == 'complete' else 1


if __name__ == '__main__':
    sys.exit(main())
