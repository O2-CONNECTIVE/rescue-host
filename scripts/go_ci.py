"""Bounded Go checks. A passing regression does not certify product acceptance."""

import argparse
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
MODULE = ROOT / 'backend'
GO_VERSION = 'go1.27.3'
PRODUCT_FILES = {
    'github.com/O2-CONNECTIVE/rescue-host/backend': {'activity.go', 'session.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/auth': {'keys.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/forward': {'policy.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/lease': {'authority.go'},
}


def environment(download=False):
    env = dict(os.environ)
    env.update(GOTOOLCHAIN='local', GOWORK='off', GOENV='off', GOFLAGS='',
               GOPROXY='https://proxy.golang.org' if download else 'off',
               GOSUMDB='sum.golang.org', GOPRIVATE='', GONOSUMDB='',
               GONOPROXY='', CGO_ENABLED='1')
    for key in tuple(env):
        if key.startswith('RESCUE_'):
            del env[key]
    return env


def command(arguments, *, download=False, timeout=600):
    completed = subprocess.run(
        ['go', *arguments], cwd=MODULE, env=environment(download),
        capture_output=True, text=True, encoding='utf-8', errors='replace', timeout=timeout,
    )
    return completed


def require_success(result):
    if result.returncode:
        # Test inputs are public fixtures; strip the local checkout from diagnostics.
        detail = (result.stdout + result.stderr).replace(str(ROOT), '<repo>')
        print(detail[-12000:], file=sys.stderr)
        raise ValueError(f'Go command failed: exit={result.returncode}')


def module_hashes():
    return {name: hashlib.sha256((MODULE / name).read_bytes()).hexdigest()
            for name in ('go.mod', 'go.sum')}


def json_objects(source):
    decoder = json.JSONDecoder()
    while source.strip():
        source = source.lstrip()
        value, end = decoder.raw_decode(source)
        yield value
        source = source[end:]


def prepare():
    before = module_hashes()
    version = command(['version'])
    require_success(version)
    if version.stdout.split()[2] != GO_VERSION:
        raise ValueError('Unexpected Go version')
    print(version.stdout.strip(), flush=True)
    require_success(command(['mod', 'download'], download=True))
    require_success(command(['mod', 'verify'], download=True))
    if module_hashes() != before:
        raise ValueError('Dependency acquisition changed go.mod/go.sum')
    print('Module inputs unchanged:', json.dumps(before, sort_keys=True), flush=True)
    dependencies = command(['list', '-m', '-json', 'all'])
    require_success(dependencies)
    for module in json_objects(dependencies.stdout):
        if module.get('Replace'):
            raise ValueError('Unreviewed module replacement')
        print('Module:', module['Path'], module.get('Version', '(main)'), flush=True)


def build():
    packages = command(['list', '-mod=readonly', '-json', './...'])
    require_success(packages)
    actual = {}
    for package in json_objects(packages.stdout):
        files = set(package.get('GoFiles', [])) | set(package.get('CgoFiles', []))
        actual[package['ImportPath']] = files
    if actual != PRODUCT_FILES:
        raise ValueError('Production file/package boundary changed')
    require_success(command(['vet', '-mod=readonly', './...']))
    require_success(command(['build', '-mod=readonly', '-trimpath', './...']))
    print('Vet/build: 4 packages; 5 production Go files; no fixture files or tags', flush=True)


def test_events(source, returncode):
    terminals = {}
    packages = {}
    started = set()
    errors = []
    for line in source.splitlines():
        event = json.loads(line)
        package, test, action = event.get('Package'), event.get('Test'), event.get('Action')
        if not package:
            continue
        if test:
            key = (package, test)
            if action == 'run':
                started.add(key)
            elif action in ('pass', 'skip', 'fail'):
                terminals[key] = action
            if action == 'skip':
                print('SKIP:', package, test, flush=True)
        elif action in ('pass', 'skip', 'fail'):
            packages[package] = action
        if action == 'output' and ('--- FAIL:' in event.get('Output', '') or
                                  'Error:' in event.get('Output', '') or
                                  '背圧' in event.get('Output', '')):
            errors.append(event['Output'].strip())
    top = {key: value for key, value in terminals.items() if '/' not in key[1]}
    counts = {status: sum(value == status for value in top.values())
              for status in ('pass', 'skip', 'fail')}
    print('Go tests:', json.dumps(counts, sort_keys=True),
          'subtests=', len(terminals) - len(top), flush=True)
    for package, status in sorted(packages.items()):
        print('Package:', package, status, flush=True)
    for error in errors:
        print(error.replace(str(ROOT), '<repo>'), flush=True)
    if returncode or counts['fail'] or any(value == 'fail' for value in packages.values()):
        raise ValueError(f'Go tests failed: exit={returncode}')
    if not top or not counts['pass'] or started - terminals.keys():
        raise ValueError('Zero/all-skipped/incomplete Go tests')
    if set(packages) != set(PRODUCT_FILES):
        raise ValueError('Missing package test result')
    return counts


def check_network_record():
    path = MODULE / 'go-result.json'
    if not path.is_file():
        raise ValueError('Missing fixture resource record')
    record = json.loads(path.read_text(encoding='utf-8'))
    try:
        for key in ('test_exit_code', 'remaining_listeners', 'remaining_connections',
                    'forbidden_dial_attempts'):
            if record.get(key) != 0:
                raise ValueError('Fixture cleanup/connection check failed: ' + key)
        for address in record.get('listener_addresses', []) + record.get('dial_addresses', []):
            host, port = address.rsplit(':', 1)
            if ipaddress.ip_address(host) != ipaddress.ip_address('127.0.0.1') or int(port) <= 1024:
                raise ValueError('Unexpected fixture address')
        print('Fixture resources: remaining=0; forbidden dials=0; owned high loopback only', flush=True)
    finally:
        path.unlink()


def tests(race=False, focused=False):
    record = MODULE / 'go-result.json'
    record.unlink(missing_ok=True)
    cpu = '2' if race else '1'
    arguments = ['test', '-mod=readonly', '-trimpath', '-count=1', '-json',
                 '-p=1', '-parallel=1', '-cpu=' + cpu, '-timeout=5m' if race else '-timeout=2m']
    if race:
        arguments.append('-race')
    if focused:
        arguments.extend(['-run', '^TestChannelCloseHandshakeKeepsCapacity$'])
    arguments.append('./...')
    print(f'Lane: race={race}, requested CPU={cpu}; no test exclusions', flush=True)
    result = command(arguments, timeout=900 if race else 600)
    try:
        test_events(result.stdout, result.returncode)
        check_network_record()
    finally:
        record.unlink(missing_ok=True)
    if result.stderr:
        print(result.stderr.replace(str(ROOT), '<repo>'), file=sys.stderr)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('command', choices=('prepare', 'build', 'test', 'race', 'repro'))
    args = parser.parse_args()
    print('Acceptance: double-close NOT_MET (#5); Windows backpressure UNVERIFIED (#6).', flush=True)
    try:
        if not (MODULE / 'go.mod').is_file():
            raise ValueError('Missing public Go module')
        {'prepare': prepare, 'build': build, 'test': tests,
         'race': lambda: tests(True), 'repro': lambda: tests(focused=True)}[args.command]()
        return 0
    except (OSError, ValueError, KeyError, IndexError, subprocess.TimeoutExpired) as error:
        print('Go check failed:', str(error).replace(str(ROOT), '<repo>'), file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
