#!/usr/bin/env python3
"""Offline Upterm CLI fake used ONLY by tests, not shipped in an EXE."""
import json
import os
from pathlib import Path
import sys

args = sys.argv[1:]
root = Path(os.environ['FAKE_UPTERM_STATE'])
root.mkdir(parents=True, exist_ok=True)
if args == ['version', '-o', 'json']:
    print(json.dumps({'version': 'v0.39.0', 'goVersion': 'go1.25.0'}))
    sys.exit(0)
if args and args[0] == 'host':
    assert '--accept' in args and '--detach' in args and '--no-sftp' in args
    name = args[args.index('--name') + 1]
    (root / name).write_text('ready')
    print(json.dumps({'name': name, 'sessionId': 'sample-session', 'sshCommand': 'ssh TokenMadeUp@uptermd.upterm.dev'}))
    sys.exit(0)
if args[:2] == ['session', 'stop']:
    file = root / args[2]
    if file.exists():
        file.write_text('ended')
        sys.exit(0)
    sys.exit(4)
if args[:2] == ['session', 'info']:
    file = root / args[2]
    if not file.exists():
        sys.exit(4)
    status = file.read_text()
    print(json.dumps({'status': status, 'guestCount': 1, 'sshCommand': 'ssh TokenMadeUp@uptermd.upterm.dev'}))
    sys.exit(0)
print('unknown fake call', args, file=sys.stderr)
sys.exit(1)
