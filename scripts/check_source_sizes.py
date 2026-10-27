"""Check tracked UI JS/TS, including maintained shadcn component source.

Limits and ordered role overrides live in .source-size-policy.json. Physical
lines include comments/blanks; CRLF/CR become LF, UTF-8 BOM is ignored, and the
byte metric uses normalized UTF-8. Other languages require separate policies.
An exact-file baseline needs a reason, owner, review condition, linked issue,
and line/byte caps; growth, dangling entries, and obsolete exceptions fail.
Size is a review signal, not proof that a God component has been resolved.
Review responsibilities, state/effects, and dependencies; do not mechanically
split or minify source to satisfy a count. No wallclock performance gate exists.
"""

import hashlib
import fnmatch
import argparse
import json
import os
from pathlib import Path, PurePosixPath
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
POLICY = ROOT / '.source-size-policy.json'
EXTENSIONS = {'.js', '.jsx', '.mjs', '.cjs', '.ts', '.tsx'}


def git(*arguments):
    return subprocess.check_output(['git', *arguments], cwd=ROOT, timeout=30).decode('utf-8')


def limits(value):
    if not isinstance(value, dict) or set(value) != {'max_lines', 'max_bytes'} or any(type(n) is not int or n <= 0 for n in value.values()):
        raise ValueError('Limits must contain positive integer max_lines/max_bytes')
    return value


def load_policy():
    policy = json.loads(POLICY.read_text(encoding='utf-8'))
    fields = {'version', 'scope', 'limits', 'rationale', 'roles', 'exclusions', 'exceptions'}
    if not isinstance(policy, dict) or set(policy) != fields:
        raise ValueError('Policy fields are missing or unknown')
    if type(policy['version']) is not int or policy['version'] != 1 or policy['scope']['root'] != 'ui' or set(policy['scope']['extensions']) != EXTENSIONS:
        raise ValueError('Unsupported source-size scope')
    limits(policy['limits'])
    if not policy['rationale'].strip():
        raise ValueError('Policy rationale is required')
    for role in policy['roles']:
        limits({key: role[key] for key in ('max_lines', 'max_bytes')})
        if not role['name'].strip() or not role['reason'].strip() or not role['patterns'] or any(not pattern for pattern in role['patterns']):
            raise ValueError('Role requires a name, patterns, and reason')
    for exclusion in policy['exclusions']:
        if not exclusion['pattern'].strip() or not exclusion['reason'].strip():
            raise ValueError('Exclusion requires a pattern and reason')
    seen = set()
    for exception in policy['exceptions']:
        required = {'path', 'max_lines', 'max_bytes', 'reason', 'owner', 'review_when', 'issue'}
        if set(exception) != required or any(not isinstance(exception[key], str) or not exception[key].strip()
                                             for key in ('path', 'reason', 'owner', 'review_when', 'issue')):
            raise ValueError('Exception requires path, reason, owner, review_when, issue, and caps')
        path = exception['path']
        if str(PurePosixPath(path)) != path or PurePosixPath(path).is_absolute() or '..' in PurePosixPath(path).parts or any(c in path for c in '*?[]\\\r\n'):
            raise ValueError('Exception must name an exact repository file')
        if path in seen:
            raise ValueError('Duplicate baseline exception: ' + path)
        seen.add(path)
        limits({key: exception[key] for key in ('max_lines', 'max_bytes')})
    return policy


def inspect_source(path):
    target = ROOT / path
    if target.is_symlink() or not target.resolve().is_relative_to(ROOT):
        raise ValueError('Source must be a regular repository file: ' + path)
    if target.stat().st_size > 2 * 1024 * 1024:
        raise ValueError('Source exceeds the 2 MiB inspection budget: ' + path)
    text = target.read_bytes().decode('utf-8-sig').replace('\r\n', '\n').replace('\r', '\n')
    if not text.strip():
        raise ValueError('Empty source: ' + path)
    return {'path': path, 'lines': text.count('\n') + (not text.endswith('\n')),
            'bytes': len(text.encode('utf-8'))}


def check(report):
    tracked = git('ls-files', '-z').split('\0')[:-1]
    if report.is_symlink() or report.resolve() in {(ROOT / path).resolve() for path in tracked}:
        raise ValueError('Report must not replace a tracked file or symlink')
    if report.exists():
        previous = json.loads(report.read_text(encoding='utf-8'))
        if not isinstance(previous, dict) or previous.get('check') != 'ui-source-size':
            raise ValueError('Refusing to replace an unrelated report file')
    report.unlink(missing_ok=True)
    policy = load_policy()
    candidates = [path for path in tracked if path.startswith('ui/') and Path(path).suffix.lower() in EXTENSIONS]
    if not candidates:
        raise ValueError('No tracked UI source files')
    selected = []
    for path in candidates:
        exclusion = next((entry for entry in policy['exclusions'] if fnmatch.fnmatchcase(path, entry['pattern'])), None)
        if exclusion:
            print(f"EXCLUDED {path}: {exclusion['reason']}", flush=True)
        else:
            selected.append(path)
    if not selected:
        raise ValueError('All tracked UI source files were excluded')
    exceptions = {entry['path']: entry for entry in policy['exceptions']}
    if set(exceptions) - set(selected):
        raise ValueError('Exception does not name a checked file: ' + ', '.join(sorted(set(exceptions) - set(selected))))
    files = []
    for path in sorted(selected):
        item = inspect_source(path)
        role = next((role for role in policy['roles'] if any(fnmatch.fnmatchcase(path, pattern) for pattern in role['patterns'])), None)
        cap = role or policy['limits']
        item['role'] = role['name'] if role else 'default'
        exception = exceptions.get(path)
        if exception:
            if item['lines'] <= cap['max_lines'] and item['bytes'] <= cap['max_bytes']:
                raise ValueError('Unneeded baseline exception: ' + path)
            cap = exception
            print(f"BASELINE {path}: owner={exception['owner']}; review_when={exception['review_when']}; issue={exception['issue']}", flush=True)
        item['exception'] = bool(exception)
        item['limits'] = {key: cap[key] for key in ('max_lines', 'max_bytes')}
        if item['lines'] > cap['max_lines'] or item['bytes'] > cap['max_bytes']:
            raise ValueError(f"Source size exceeded: {path}; lines={item['lines']}/{cap['max_lines']}, bytes={item['bytes']}/{cap['max_bytes']}")
        files.append(item)
        print(f"SIZE {path}: lines={item['lines']}, bytes={item['bytes']}, role={item['role']}", flush=True)
    result = {'version': 1, 'check': 'ui-source-size', 'status': 'pass',
              'checked': len(files), 'candidates': len(candidates), 'excluded': len(candidates) - len(files),
              'exceptions': len(exceptions), 'files': files,
              'policy_sha256': hashlib.sha256(POLICY.read_bytes()).hexdigest(),
              'git_head': git('rev-parse', 'HEAD').strip(),
              'nonce': os.environ.get('SOURCE_SIZE_NONCE', 'local')}
    report.parent.mkdir(parents=True, exist_ok=True)
    report.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n', encoding='utf-8')
    print(f"SOURCE_SIZE_PASS checked={len(files)} excluded={result['excluded']} exceptions={len(exceptions)}", flush=True)


def verify_report(report):
    if not report.is_file():
        raise ValueError('Missing source-size report; checker did not run')
    result = json.loads(report.read_text(encoding='utf-8'))
    expected = {'version': 1, 'check': 'ui-source-size', 'status': 'pass',
                'policy_sha256': hashlib.sha256(POLICY.read_bytes()).hexdigest(),
                'git_head': git('rev-parse', 'HEAD').strip(),
                'nonce': os.environ.get('SOURCE_SIZE_NONCE', 'local')}
    if not isinstance(result, dict) or any(result.get(key) != value for key, value in expected.items()):
        raise ValueError('Source-size report does not match this execution/head/policy')
    if type(result.get('checked')) is not int or result['checked'] <= 0 or len(result.get('files', [])) != result['checked']:
        raise ValueError('Source-size report has no checked files')
    print(f"SOURCE_SIZE_VERIFIED checked={result['checked']}", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--verify-report', action='store_true')
    args = parser.parse_args()
    report = Path(os.environ.get('SOURCE_SIZE_REPORT', ROOT / '.work/source-size-result.json'))
    if not report.is_absolute():
        report = ROOT / report
    try:
        if args.verify_report:
            verify_report(report)
        else:
            check(report)
        return 0
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        print('SOURCE_SIZE_FAIL:', str(error).replace(str(ROOT), '<repo>'), file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
