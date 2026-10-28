"""Bounded Go checks. A passing regression does not certify product acceptance."""

import argparse
import hashlib
import ipaddress
import json
import os
import re
from pathlib import Path
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
MODULE = ROOT / 'backend'
GO_VERSION = 'go1.27.3'
PRODUCT_FILES = {
    'github.com/O2-CONNECTIVE/rescue-host/backend': {'activity.go', 'session.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/cmd/rescue-backend': {'main.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/auth': {'keys.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/control': {'coordinator.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/forward': {'policy.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/lease': {'authority.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio': {'loop.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/supervisor': {'authority.go'},
    'github.com/O2-CONNECTIVE/rescue-host/backend/servicehost': {'control.go', 'host.go'},
}


SCM_FILES = {
    'linux': 'scm_other.go',
    'darwin': 'scm_other.go',
    'windows': 'scm_windows.go',
}


def product_files_for(goos):
    if goos not in SCM_FILES:
        raise ValueError('Unsupported Go target for production inventory')
    expected = {package: set(files) for package, files in PRODUCT_FILES.items()}
    expected['github.com/O2-CONNECTIVE/rescue-host/backend/servicehost'].add(SCM_FILES[goos])
    return expected


def environment(download=False):
    env = dict(os.environ)
    env.update(GOTOOLCHAIN='local', GOWORK='off', GOENV='off', GOFLAGS='',
               GOPROXY='https://proxy.golang.org' if download else 'off',
               GOSUMDB='sum.golang.org', GOPRIVATE='', GONOSUMDB='',
               GONOPROXY='', CGO_ENABLED='1')
    # Keep the one-second child-exit measurement; remove only the race runtime's
    # documented extra one-second sleep after the program has already exited.
    env['GORACE'] = 'atexit_sleep_ms=0'
    for key in tuple(env):
        if key.startswith('RESCUE_'):
            del env[key]
    return env


def command(arguments, *, download=False, timeout=600):
    started = time.monotonic()
    completed = subprocess.run(
        ['go', *arguments], cwd=MODULE, env=environment(download),
        capture_output=True, text=True, encoding='utf-8', errors='replace', timeout=timeout,
    )
    print(f'Go command {arguments[0]}: elapsed_s={time.monotonic() - started:.3f}, exit={completed.returncode}', flush=True)
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
    information = command(['env', '-json', 'GOOS', 'GOARCH', 'CGO_ENABLED', 'CC'])
    require_success(information)
    metadata = json.loads(information.stdout)
    compiler = metadata['CC']
    metadata['CC'] = Path(compiler).name
    print('Environment:', json.dumps(metadata, sort_keys=True), flush=True)
    try:
        compiler_version = subprocess.run([compiler, '--version'], capture_output=True, text=True, timeout=30)
        print('Compiler:', compiler_version.stdout.splitlines()[0][:200], flush=True)
    except (OSError, IndexError) as error:
        print('Compiler unavailable:', type(error).__name__, flush=True)
    dependencies = command(['mod', 'download', '-json'], download=True)
    require_success(dependencies)
    require_success(command(['mod', 'verify'], download=True))
    if module_hashes() != before:
        raise ValueError('Dependency acquisition changed go.mod/go.sum')
    print('Module inputs unchanged:', json.dumps(before, sort_keys=True), flush=True)
    for module in json_objects(dependencies.stdout):
        if module.get('Replace'):
            raise ValueError('Unreviewed module replacement')
        print('Module:', module['Path'], module.get('Version', '(main)'), flush=True)


def build():
    target = command(['env', 'GOOS'])
    require_success(target)
    goos = target.stdout.strip()
    expected = product_files_for(goos)
    packages = command(['list', '-mod=readonly', '-json', './...'])
    require_success(packages)
    actual = {}
    for package in json_objects(packages.stdout):
        files = set(package.get('GoFiles', [])) | set(package.get('CgoFiles', []))
        actual[package['ImportPath']] = files
    if actual != expected:
        raise ValueError('Production file/package boundary changed')
    require_success(command(['vet', '-mod=readonly', './...']))
    require_success(command(['build', '-mod=readonly', '-trimpath', './...']))
    print(f'Vet/build: 9 packages; 12 production Go files; GOOS={goos}; no fixture files or tags', flush=True)


def test_inventory():
    result = command(['list', '-mod=readonly', '-json', './...'])
    require_success(result)
    expected = set()
    for package in json_objects(result.stdout):
        for name in package.get('TestGoFiles', []) + package.get('XTestGoFiles', []):
            path = Path(package['Dir']) / name
            if not path.resolve().is_relative_to(MODULE):
                raise ValueError('Test source outside public module')
            for test in re.findall(r'^func (Test\w+)\(', path.read_text(encoding='utf-8'), re.MULTILINE):
                if test != 'TestMain':
                    expected.add((package['ImportPath'], test))
    if not expected:
        raise ValueError('Empty Go test inventory')
    return expected


def test_events(source, returncode, expected_cpu=None, expected_tests=None):
    terminals = {}
    packages = {}
    started = set()
    errors = []
    outputs = {}
    cpus = set()
    for line in source.splitlines():
        event = json.loads(line)
        if not isinstance(event, dict):
            raise ValueError('Go event is not an object')
        package, test, action = event.get('Package'), event.get('Test'), event.get('Action')
        if not package:
            if event.get('Output'):
                outputs.setdefault((event.get('ImportPath', 'build'), None), []).append(event['Output'])
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
        if action == 'output':
            outputs.setdefault((package, test), []).append(event.get('Output', ''))
            cpus.update(int(value) for value in re.findall(r'observed_test_GOMAXPROCS=(\d+)', event.get('Output', '')))
    top = {key: value for key, value in terminals.items() if '/' not in key[1]}
    counts = {status: sum(value == status for value in top.values())
              for status in ('pass', 'skip', 'fail')}
    print('Go tests:', json.dumps(counts, sort_keys=True),
          'subtests=', len(terminals) - len(top), flush=True)
    for package, status in sorted(packages.items()):
        print('Package:', package, status, flush=True)
    for error in errors:
        print(error.replace(str(ROOT), '<repo>'), flush=True)
    for key, status in terminals.items():
        if status in ('fail', 'skip'):
            print(''.join(outputs.get(key, []))[-6000:].replace(str(ROOT), '<repo>'), flush=True)
    if returncode and not any(value == 'fail' for value in terminals.values()):
        for (package, test), lines in outputs.items():
            if not test:
                print(''.join(lines)[-6000:].replace(str(ROOT), '<repo>'), flush=True)
    if expected_cpu is not None:
        print('Observed test GOMAXPROCS:', sorted(cpus), flush=True)
        if cpus != {int(expected_cpu)}:
            raise ValueError('Requested CPU lane was not observed inside tests')
    if returncode or any(value == 'fail' for value in terminals.values()) or any(value == 'fail' for value in packages.values()):
        raise ValueError(f'Go tests failed: exit={returncode}')
    if not top or not counts['pass'] or started - terminals.keys():
        raise ValueError('Zero/all-skipped/incomplete Go tests')
    if any(value == 'skip' for value in terminals.values()):
        raise ValueError('Unexpected Go test skip (no test-level exclusions approved)')
    if set(packages) != set(PRODUCT_FILES):
        raise ValueError('Missing package test result')
    if expected_tests is not None and set(top) != expected_tests:
        raise ValueError('Selected Go tests did not match the public-source inventory')
    return counts


def check_network_record():
    path = MODULE / 'go-result.json'
    if not path.is_file():
        raise ValueError('Missing fixture resource record')
    record = json.loads(path.read_text(encoding='utf-8'))
    try:
        counters = {key: record.get(key) if type(record.get(key)) is int else 'invalid'
                    for key in ('test_exit_code', 'remaining_listeners', 'remaining_connections',
                                'forbidden_dial_attempts', 'duration_ms')}
        print('Fixture counters:', json.dumps(counters, sort_keys=True), flush=True)
        for key in ('test_exit_code', 'remaining_listeners', 'remaining_connections',
                    'forbidden_dial_attempts'):
            if counters[key] != 0:
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
    inventory = None if focused else test_inventory()
    arguments = ['test', '-mod=readonly', '-trimpath', '-count=1', '-json',
                 '-p=1', '-parallel=1', '-cpu=' + cpu, '-timeout=5m' if race else '-timeout=2m']
    if race:
        arguments.append('-race')
    if focused:
        arguments.extend(['-run', '^TestChannelCloseHandshakeKeepsCapacity$'])
    arguments.append('./...')
    scope = 'focused channel lifecycle reproduction' if focused else 'full suite, no test exclusions'
    print(f'Lane: race={race}, requested CPU={cpu}; {scope}', flush=True)
    result = command(arguments, timeout=900 if race else 600)
    try:
        if result.stderr:
            print(result.stderr.replace(str(ROOT), '<repo>'), file=sys.stderr)
        failures = []
        try:
            test_events(result.stdout, result.returncode, expected_cpu=None if focused else cpu,
                        expected_tests=inventory)
        except ValueError as error:
            failures.append(str(error))
        try:
            check_network_record()
        except (OSError, ValueError) as error:
            failures.append(str(error))
        if failures:
            raise ValueError('; '.join(failures))
    finally:
        record.unlink(missing_ok=True)


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
