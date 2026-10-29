"""公開用受入要約の形式を検査する。実機操作と合否認定は行わない。"""

import argparse
import json
from pathlib import Path
import re
import sys


COMMON = {'revoked', 'both_end_eof', 'reader_join', 'process_exit_reap',
          'job_cleanup', 'resource_restoration', 'old_permission_rejected'}
EXTRA = {
    'normal': {'authenticated_roundtrip'},
    'permission_denied': {'request_rejected', 'forbidden_dial_zero', 'forbidden_effect_zero'},
    'maximum_ttl': {'deadline_not_extended'},
    'inactivity': {'idle_definition_confirmed'},
    'lease_loss': {'deadline_not_extended'},
    'disconnect': {'disconnect_observed'},
    'stop_unconfirmed': {'ui_end_unconfirmed', 'restart_blocked'},
    'stale_permission': {'fresh_approval_required'},
}
STATES = {'confirmed', 'failed', 'unavailable', 'unobserved'}
REASONS = {'none', 'implementation_pending', 'definition_pending', 'approval_pending',
           'candidate_unavailable', 'observation_missing', 'unexpected_result', 'time_limit'}


def require(condition):
    if not condition:
        raise ValueError('invalid record')


def fields(value, names):
    require(type(value) is dict and set(value) == set(names.split()))


def digest(value, length):
    return type(value) is str and re.fullmatch('[0-9a-f]{' + str(length) + '}', value) is not None


def number(value, minimum=0):
    return type(value) is int and minimum <= value <= 9007199254740991


def unique_numbers(value):
    return (type(value) is list and len(value) <= 32
            and all(number(item, 1) for item in value) and len(value) == len(set(value)))


def validate(record):
    fields(record, 'version source_sha artifact ci_run_ids environment cases')
    require(type(record['version']) is int and record['version'] == 1)
    require(digest(record['source_sha'], 40))
    fields(record['artifact'], 'zip_sha256 exe_sha256')
    require(all(value is None or digest(value, 64) for value in record['artifact'].values()))
    require(unique_numbers(record['ci_run_ids']))
    environment = record['environment']
    fields(environment, 'os architecture build')
    require(environment['os'] == 'windows' and environment['architecture'] == 'x64')
    require(environment['build'] is None or (type(environment['build']) is str
            and re.fullmatch(r'[1-9][0-9]{4}(?:\.[0-9]{1,5})?', environment['build']) is not None))
    cases = record['cases']
    require(type(cases) is list and len(cases) == len(EXTRA))
    seen, executed, not_run, must_stop = set(), 0, 0, False
    for case in cases:
        fields(case, 'id status reason elapsed_ms limit_ms evidence_ids checks')
        case_id = case['id']
        require(type(case_id) is str and case_id in EXTRA and case_id not in seen)
        seen.add(case_id)
        require(type(case['status']) is str and case['status'] in {'pass', 'fail', 'unavailable', 'not_run'})
        require(type(case['reason']) is str and case['reason'] in REASONS)
        require(unique_numbers(case['evidence_ids']))
        checks = case['checks']
        require(type(checks) is dict and set(checks) == COMMON | EXTRA[case_id])
        require(all(type(value) is str and value in STATES for value in checks.values()))
        require(all(value is None or number(value) for value in (case['elapsed_ms'], case['limit_ms'])))
        if case['status'] == 'not_run':
            not_run += 1
            require(case['reason'] in {'implementation_pending', 'definition_pending',
                                      'approval_pending', 'candidate_unavailable'})
            require(not case['evidence_ids'])
            require(case['elapsed_ms'] is None and case['limit_ms'] is None)
            require(set(checks.values()) == {'unobserved'})
        else:
            executed += 1
            require(all(record['artifact'].values()) and record['ci_run_ids'] and environment['build'])
            require(case['elapsed_ms'] is not None and number(case['limit_ms'], 1))
            require(case['evidence_ids'])
            must_stop |= case['status'] in {'fail', 'unavailable'}
            if case['status'] == 'pass':
                require(case['reason'] == 'none' and case['elapsed_ms'] <= case['limit_ms'])
                if case_id == 'stop_unconfirmed':
                    require(all(checks[key] == 'confirmed' for key in
                                {'revoked', 'old_permission_rejected'} | EXTRA[case_id]))
                    physical = COMMON - {'revoked', 'old_permission_rejected'}
                    require(any(checks[key] in {'unavailable', 'failed'} for key in physical))
                    must_stop = True
                else:
                    require(set(checks.values()) == {'confirmed'})
            else:
                require(case['reason'] in {'observation_missing', 'unexpected_result', 'time_limit'})
                require(('failed' if case['status'] == 'fail' else 'unavailable') in checks.values())
    require(seen == set(EXTRA))
    return executed, not_run, must_stop


def no_duplicates(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result)
        result[key] = value
    return result


class Parser(argparse.ArgumentParser):
    def error(self, message):
        self.exit(2, 'RECORD_INVALID arguments\n')


def main():
    parser = Parser(description=__doc__)
    parser.add_argument('--record', required=True)
    parser.add_argument('--require-executed', action='store_true')
    arguments = parser.parse_args()
    try:
        path = Path(arguments.record)
        require(path.is_file() and not path.is_symlink())
        with path.open('rb') as stream:
            data = stream.read(65537)
        require(0 < len(data) <= 65536)
        record = json.loads(data.decode('utf-8'), object_pairs_hook=no_duplicates)
        executed, not_run, must_stop = validate(record)
        require(not arguments.require_executed or executed > 0)
    except (OSError, ValueError, TypeError, RecursionError):
        print('RECORD_INVALID format_or_incomplete', file=sys.stderr)
        return 1
    print(f'FORMAT_VALID executed={executed} not_run={not_run} must_stop={int(must_stop)} acceptance=NOT_EVALUATED')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
