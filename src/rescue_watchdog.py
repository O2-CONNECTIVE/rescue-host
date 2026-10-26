"""Recovery watchdog, not a tamper-resistant expiration authority.

The release gate blocks new sessions until a backend enforces expiration. This
process improves cleanup of legacy sessions but cannot survive hostile same-user
termination or prove remote TCP streams have closed.
"""
from __future__ import annotations
import math
import os
from pathlib import Path
import subprocess
import sys
import time
from rescue_core import (RescueError, atomic_json_write, data_dir,
    remove_session_state_if_name, resolve_upterm, stop_session, SESSION_NAME_PATTERN)


def launch_watchdog(session_name: str, deadline: float, external_upterm: str) -> None:
    if not SESSION_NAME_PATTERN.fullmatch(session_name) or not math.isfinite(deadline):
        raise RescueError("監視対象または期限が不正です。")
    # Per-session acknowledgement, not just a successful Popen.
    ready = data_dir() / "sessions" / (session_name + ".watchdog-ready.json")
    ready.parent.mkdir(parents=True, exist_ok=True)
    if ready.exists():
        raise RescueError("監視プロセスの確認ファイルが既に存在します。")
    launcher = [] if getattr(sys, "frozen", False) else [str(Path(__file__).with_name("rescue_host.py"))]
    cmd = [sys.executable, *launcher, "--watchdog", session_name, repr(deadline), external_upterm or "-", str(ready)]
    flags = 0
    if os.name == "nt":
        flags = subprocess.DETACHED_PROCESS | subprocess.CREATE_NO_WINDOW | subprocess.CREATE_NEW_PROCESS_GROUP
    process = None
    acknowledged = False
    try:
        with open(os.devnull, "wb") as null:
            process = subprocess.Popen(cmd, stdin=null, stdout=null, stderr=null,
                creationflags=flags, start_new_session=(os.name != "nt"), close_fds=True)
        limit = time.monotonic() + 5
        while time.monotonic() < limit:
            if process.poll() is not None:
                raise RescueError("監視プロセスが開始直後に終了しました。")
            if ready.is_file():
                import json
                receipt = json.loads(ready.read_text(encoding="utf-8"))
                if receipt != {"name": session_name, "pid": process.pid, "deadline": deadline}:
                    raise RescueError("監視プロセスの確認応答が一致しません。")
                ready.unlink()
                acknowledged = True
                return
            time.sleep(0.05)
        # This is our own just-created watchdog, not an Upterm process.
        raise RescueError("期限監視の起動確認がタイムアウトしました。新規接続は開始しません。")
    except (OSError, ValueError, subprocess.TimeoutExpired) as exc:
        raise RescueError("期限監視プロセスを起動できませんでした。") from exc
    finally:
        if not acknowledged:
            if process is not None and process.poll() is None:
                try:
                    process.terminate()
                    process.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=2)
                except OSError:
                    pass
            ready.unlink(missing_ok=True)


def watchdog_main(name: str, deadline_string: str, external: str, ready_path: str | None = None) -> int:
    if not SESSION_NAME_PATTERN.fullmatch(name):
        return 2
    try:
        deadline = float(deadline_string)
        if not math.isfinite(deadline) or deadline > time.time() + 3 * 3600:
            return 2
        exe = resolve_upterm("" if external == "-" else external)
        monotonic_deadline = time.monotonic() + max(0, deadline - time.time())
        if ready_path:
            expected = data_dir() / "sessions" / (name + ".watchdog-ready.json")
            if Path(ready_path).resolve() != expected.resolve():
                return 2
            atomic_json_write(expected, {"name": name, "pid": os.getpid(), "deadline": deadline})
        # Clock rollback cannot extend a running watchdog; forward jumps expire early.
        # Overdue legacy sessions are stopped immediately, never rejected as too old.
        while True:
            remaining = min(deadline - time.time(), monotonic_deadline - time.monotonic())
            if remaining <= 0:
                break
            time.sleep(min(remaining, 1))
        for attempt in range(5):
            try:
                stop_session(exe, name)
                remove_session_state_if_name(name)
                return 0
            except RescueError:
                if attempt == 4:
                    raise
                time.sleep(min(2 ** attempt, 8))
    except (ValueError, RescueError, OSError) as exc:
        log_path = data_dir() / "watchdog.log"
        log_path.parent.mkdir(parents=True, exist_ok=True)
        with log_path.open("a", encoding="utf-8") as out:
            out.write(f"{time.strftime('%Y-%m-%d %H:%M:%S')} stop {name} UNCONFIRMED ({type(exc).__name__}); state retained\n")
        return 1
    return 1
