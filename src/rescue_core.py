"""Upterm Rescue Host: validated settings and Upterm CLI integration.

No remote commands are executed by this module unless explicitly requested by
its caller. All subprocesses are invoked as argument vectors, never via shell.
"""
from __future__ import annotations

import base64
import hashlib
import json
import math
import os
from dataclasses import asdict, dataclass
from pathlib import Path
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time
from typing import Any
from urllib.parse import urlparse

APP_NAME = "RescueHost"
DEFAULT_RELAY = "ssh://uptermd.upterm.dev:22"
PINNED_FINGERPRINT = "SHA256:9ajV8JqMe6jJE/s3TYjb/9xw7T0pfJ2+gADiBIJWDPE"
APP_VERSION = "0.2.0-dev"
MIN_UPTERM = (0, 39, 0)
START_BLOCKED_REASON = (
    "v0.2 開発版では新規接続を停止しています。Upterm単体ではTCP転送先限定と"
    "強制失効を保証できません。専用バックエンド・Windows隔離環境の検証完了が必要です。"
)


def require_secure_session_backend() -> None:
    """Release gate, deliberately not configurable through settings/environment.

    Remove only with an independently reviewed enforcement backend and E2E evidence.
    Existing sessions can still be stopped. No executable is invoked here.
    """
    raise RescueError(START_BLOCKED_REASON)
SUPPORTED_KEYS = ("ssh-ed25519", "sk-ssh-ed25519@openssh.com")
SESSION_NAME_PATTERN = re.compile(r"^rescue-[0-9a-f]{12}$")


class RescueError(RuntimeError):
    pass


@dataclass
class Settings:
    relay: str = DEFAULT_RELAY
    duration_minutes: int = 30
    mode: str = "ssh"  # ssh: local TCP forwarding; terminal: PowerShell only
    public_keys: str = ""
    external_upterm: str = ""
    custom_known_hosts: str = ""

    def validate(self) -> None:
        validate_relay(self.relay)
        if self.mode not in ("ssh", "terminal"):
            raise RescueError("接続方式は 'ssh' または 'terminal' を指定してください。")
        if type(self.duration_minutes) is not int or not 5 <= self.duration_minutes <= 120:
            raise RescueError("公開時間は5〜120分にしてください。")
        validate_public_keys(self.public_keys)
        if self.external_upterm and not Path(self.external_upterm).is_file():
            raise RescueError("指定された upterm.exe が見つかりません。")
        if self.custom_known_hosts and not Path(self.custom_known_hosts).is_file():
            raise RescueError("指定された known_hosts ファイルがありません。")
        if not is_official_relay(self.relay) and not self.custom_known_hosts:
            raise RescueError("独自中継サーバーには、検証済み known_hosts ファイルが必要です。")


def data_dir() -> Path:
    if sys.platform == "win32":
        base = Path(os.environ.get("LOCALAPPDATA", str(Path.home())))
        return base / APP_NAME
    return Path(os.environ.get("XDG_DATA_HOME", str(Path.home() / ".local" / "share"))) / APP_NAME


def atomic_json_write(path: Path, obj: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temp_name = tempfile.mkstemp(prefix=path.name + ".", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as out:
            json.dump(obj, out, ensure_ascii=False, indent=2)
        if os.name != "nt":
            os.chmod(temp_name, 0o600)
        os.replace(temp_name, path)
    finally:
        if os.path.exists(temp_name):
            os.unlink(temp_name)


def load_settings(folder: Path | None = None) -> Settings:
    path = (folder or data_dir()) / "config.json"
    if not path.exists():
        return Settings()
    try:
        obj = json.loads(path.read_text(encoding="utf-8"))
        allowed = {k for k in Settings.__dataclass_fields__}
        return Settings(**{k: v for k, v in obj.items() if k in allowed})
    except (ValueError, TypeError, OSError) as exc:
        raise RescueError("設定ファイルが破損しています: " + str(exc)) from exc


def save_settings(settings: Settings, folder: Path | None = None) -> None:
    settings.validate()
    atomic_json_write((folder or data_dir()) / "config.json", asdict(settings))


def validate_public_keys(keys: str) -> None:
    if not isinstance(keys, str) or len(keys) > 16384 or any(ord(c) < 32 and c not in "\r\n\t" for c in keys):
        raise RescueError("公開鍵の入力が不正です。")
    lines = [line.strip() for line in keys.splitlines() if line.strip()]
    if not lines:
        raise RescueError("Mac側のSSH公開鍵（ssh-ed25519 ...）を設定してください。")
    if len(lines) > 8:
        raise RescueError("公開鍵は最大8件までです。")
    for index, line in enumerate(lines, 1):
        pieces = line.split()
        if len(pieces) < 2 or pieces[0] not in SUPPORTED_KEYS:
            raise RescueError(f"{index}行目: ed25519形式の公開鍵のみ利用できます。")
        try:
            raw = base64.b64decode(pieces[1], validate=True)
        except ValueError as exc:
            raise RescueError(f"{index}行目: 公開鍵のBase64が不正です。") from exc
        # Parse every SSH string, not just the key-type prefix. No options or certificates.
        offset = 0
        def field():
            nonlocal offset
            if offset + 4 > len(raw):
                raise RescueError(f"{index}行目: SSH鍵が途中で切れています。")
            length = int.from_bytes(raw[offset:offset + 4], "big")
            offset += 4
            if length > 1024 or offset + length > len(raw):
                raise RescueError(f"{index}行目: SSH鍵の長さが不正です。")
            value = raw[offset:offset + length]
            offset += length
            return value
        if field() != pieces[0].encode("ascii") or len(field()) != 32:
            raise RescueError(f"{index}行目: SSH鍵の型または鍵長が不正です。")
        if pieces[0] == "sk-ssh-ed25519@openssh.com":
            application = field()
            if not application.startswith(b"ssh:") or b"\x00" in application:
                raise RescueError(f"{index}行目: セキュリティキーのapplicationが不正です。")
        if offset != len(raw):
            raise RescueError(f"{index}行目: SSH鍵に余分なデータがあります。")



def validate_relay(relay: str) -> None:
    if not isinstance(relay, str) or any(c.isspace() or ord(c) < 32 for c in relay):
        raise RescueError("中継先に空白や制御文字は使用できません。")
    parsed = urlparse(relay)
    if parsed.scheme not in ("ssh", "wss") or not parsed.hostname:
        raise RescueError("中継先は ssh://host:22 または wss://host:443 の形式にしてください。")
    if parsed.username or parsed.password or parsed.path or parsed.query or parsed.fragment:
        raise RescueError("中継先にはユーザー名・パス・クエリを指定できません。")
    try:
        if parsed.port is not None and not 1 <= parsed.port <= 65535:
            raise ValueError()
    except ValueError as exc:
        raise RescueError("中継サーバーのポート番号が不正です。") from exc


def is_official_relay(relay: str) -> bool:
    parsed = urlparse(relay)
    if parsed.hostname != "uptermd.upterm.dev":
        return False
    return (parsed.scheme == "ssh" and parsed.port in (None, 22)) or (
        parsed.scheme == "wss" and parsed.port in (None, 443)
    )


def resource_path(relative: str) -> Path:
    base = Path(getattr(sys, "_MEIPASS", Path(__file__).resolve().parent.parent))
    return base / relative


def resolve_upterm(external: str = "") -> Path:
    candidates = []
    if external:
        candidates.append(Path(external))
    else:
        candidates += [resource_path("upterm.exe"), resource_path("vendor/upterm.exe")]
        candidates.append(Path(__file__).resolve().parent / "upterm.exe")
        candidates.append(Path(sys.executable).resolve().parent / "upterm.exe")
        found = shutil.which("upterm.exe" if os.name == "nt" else "upterm")
        if found:
            candidates.append(Path(found))
    for candidate in candidates:
        if candidate.is_file():
            return candidate.resolve()
    raise RescueError("upterm.exe が見つかりません。設定から指定するか、Windowsでビルドしてください。")


def official_known_hosts_text() -> str:
    src = resource_path("assets/upterm_known_hosts")
    contents = src.read_text(encoding="ascii")
    actual = []
    for line in contents.splitlines():
        parts = line.split()
        if len(parts) != 4 or parts[0] != "@cert-authority":
            continue
        if parts[2] != "ssh-ed25519":
            continue
        key = base64.b64decode(parts[3])
        finger = "SHA256:" + base64.b64encode(hashlib.sha256(key).digest()).decode().rstrip("=")
        if finger != PINNED_FINGERPRINT:
            raise RescueError("同梱のknown_hostsのフィンガープリントが一致しません。")
        actual.append(parts[1])
    if set(actual) != {"uptermd.upterm.dev", "[uptermd.upterm.dev]:443"}:
        raise RescueError("既定中継サーバーのホスト鍵が不完全です。")
    return contents


def resolve_known_hosts(settings: Settings, folder: Path | None = None) -> Path:
    if settings.custom_known_hosts:
        return Path(settings.custom_known_hosts).resolve()
    if not is_official_relay(settings.relay):
        raise RescueError("独自中継サーバー用 known_hosts が指定されていません。")
    target = (folder or data_dir()) / "upterm_known_hosts"
    target.parent.mkdir(parents=True, exist_ok=True)
    src = official_known_hosts_text()
    if not target.is_file() or target.read_text(encoding="ascii") != src:
        target.write_text(src, encoding="ascii")
    return target


def prepare_authorized_keys(settings: Settings, session_name: str, folder: Path | None = None) -> Path:
    if not SESSION_NAME_PATTERN.fullmatch(session_name):
        raise RescueError("セッション名が不正です。")
    validate_public_keys(settings.public_keys)
    target = (folder or data_dir()) / "sessions" / (session_name + ".authorized_keys")
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(settings.public_keys.strip() + "\n", encoding="utf-8")
    if os.name != "nt":
        target.chmod(0o600)
    return target


def check_local_sshd() -> bool:
    try:
        with socket.create_connection(("127.0.0.1", 22), timeout=0.8):
            return True
    except OSError:
        return False


def upterm_cmd_start(settings: Settings, name: str, known_hosts: Path, key_file: Path) -> list[str]:
    settings.validate()
    if not SESSION_NAME_PATTERN.fullmatch(name):
        raise RescueError("セッション名が不正です。")
    if settings.mode == "ssh":
        raise RescueError("SSH転送は禁止です。UptermのTCP転送先を限定できません。")
    args = [
        "host", "--detach", "--accept", "-o", "json",
        "--name", name,
        "--server", settings.relay,
        "--known-hosts", str(known_hosts),
        "--authorized-keys", str(key_file),
        "--no-sftp", "--hide-client-ip",
    ]
    args += ["--join-timeout", f"{min(10, settings.duration_minutes)}m"]
    args += ["--", "powershell.exe", "-NoLogo", "-NoProfile"]
    return args


def subprocess_flags() -> int:
    return subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0


def run_upterm(executable: Path, args: list[str], *, timeout: float = 45) -> subprocess.CompletedProcess[str]:
    # Never let environment UPTERM_* defaults silently override selected UI settings.
    env = {key: value for key, value in os.environ.items() if not key.startswith("UPTERM_")}
    # Upterm also reads XDG_CONFIG_HOME/upterm/config.yaml. Strip neither state nor
    # runtime locations: those must still locate pre-v0.2 sessions for recovery.
    config_root = data_dir() / "isolated-upterm-config"
    config_folder = config_root / "upterm"
    for path in (config_root, config_folder, config_folder / "config.yaml"):
        if path.is_symlink():
            raise RescueError("Upterm専用設定パスがシンボリックリンクです。")
    config_folder.mkdir(parents=True, exist_ok=True)
    if os.name != "nt":
        config_root.chmod(0o700)
        config_folder.chmod(0o700)
    atomic_json_write(config_folder / "config.yaml", {})  # JSON is valid YAML.
    env["XDG_CONFIG_HOME"] = str(config_root.resolve())
    try:
        return subprocess.run(
            [str(executable), *args], capture_output=True, text=True,
            encoding="utf-8", errors="replace", timeout=timeout,
            env=env, creationflags=subprocess_flags(), check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise RescueError(f"Uptermの実行に失敗しました: {exc}") from exc


def version_from_output(text: str) -> tuple[int, int, int]:
    # Prefer the explicit version property, not another semver field (e.g. Go).
    try:
        payload = json.loads(text)
    except (ValueError, TypeError):
        payload = None
    value = None
    if isinstance(payload, dict):
        value = payload.get("version") or payload.get("Version")
    if isinstance(payload, dict) and not isinstance(value, str):
        raise RescueError("Upterm versionフィールドが不正です。")
    source = str(value).strip() if value else text.strip()
    found = re.fullmatch(r"(?:[Uu]pterm\s+)?v?(\d+)\.(\d+)\.(\d+)", source)
    if not found:
        raise RescueError("安定版Uptermのバージョンを取得できません。")
    return tuple(int(number) for number in found.groups())


def check_upterm_version(executable: Path) -> tuple[int, int, int]:
    result = run_upterm(executable, ["version", "-o", "json"], timeout=10)
    if result.returncode != 0:
        raise RescueError("Uptermのバージョン確認に失敗しました: " + result.stderr[:300])
    version = version_from_output(result.stdout)
    if version < MIN_UPTERM:
        raise RescueError("Upterm v0.39.0以上を使用してください（旧版には重要な脆弱性があります）。")
    return version


def parse_host_json(output: str) -> dict[str, str]:
    try:
        data = json.loads(output)
    except (json.JSONDecodeError, TypeError) as exc:
        raise RescueError("Uptermから有効なセッション情報を受け取れませんでした。") from exc
    if not isinstance(data, dict):
        raise RescueError("UptermのJSON出力形式が想定と違います。")
    join = data.get("sshCommand")
    if not isinstance(join, str) or not re.fullmatch(r"ssh [A-Za-z0-9._~+/=-]+@[A-Za-z0-9.-]+(?: -p [0-9]{1,5})?", join):
        raise RescueError("Uptermの接続コマンドが取得できません。")
    return {"ssh_command": join, "session_id": str(data.get("sessionId", ""))}


def build_mac_forward_command(ssh_command: str, local_port: int = 2222) -> str:
    raise RescueError("転送先限定が未実装のためSSH転送コマンドは生成しません。")


def session_state_path(folder: Path | None = None) -> Path:
    return (folder or data_dir()) / "active_session.json"


def validate_session_state(data: Any) -> dict[str, Any]:
    if not isinstance(data, dict) or not isinstance(data.get("name"), str) or not SESSION_NAME_PATTERN.fullmatch(data["name"]):
        raise RescueError("セッション回復情報の名前が不正です。ファイルは保持します。")
    deadline = data.get("deadline")
    if type(deadline) not in (int, float) or not math.isfinite(deadline) or deadline <= 0:
        raise RescueError("セッション回復情報の期限が不正です。ファイルは保持します。")
    if "mode" in data and data["mode"] not in {"ssh", "terminal"}:
        raise RescueError("セッション回復情報の操作方式が不正です。")
    if "external_upterm" in data and not isinstance(data["external_upterm"], str):
        raise RescueError("セッション回復情報の実行パスが不正です。")
    return data


def load_session_state(folder: Path | None = None) -> dict[str, Any] | None:
    path = session_state_path(folder)
    if path.is_symlink():
        raise RescueError("セッション回復情報のシンボリックリンクは許可しません。")
    if not path.exists():
        return None
    if not path.is_file():
        raise RescueError("セッション回復情報のファイル種別が不正です。")
    try:
        return validate_session_state(json.loads(path.read_text(encoding="utf-8")))
    except (ValueError, OSError) as exc:
        raise RescueError("セッション回復情報を読み取れません。停止済みとは判断せずファイルを保持します。") from exc


def record_session_state(state: dict[str, Any], folder: Path | None = None) -> None:
    validate_session_state(state)
    atomic_json_write(session_state_path(folder), {k: v for k, v in state.items() if k != "ssh_command"})


def remove_session_state_if_name(name: str, folder: Path | None = None) -> None:
    if not isinstance(name, str) or not SESSION_NAME_PATTERN.fullmatch(name):
        raise RescueError("セッション名が不正です。")
    state = load_session_state(folder)
    if state and state.get("name") == name:
        session_state_path(folder).unlink(missing_ok=True)
    key_file = (folder or data_dir()) / "sessions" / (name + ".authorized_keys")
    key_file.unlink(missing_ok=True)


def stop_session(executable: Path, name: str, *, confirmation_timeout: float = 5) -> None:
    if not SESSION_NAME_PATTERN.fullmatch(name):
        raise RescueError("停止対象のセッション名が不正です。")
    if not math.isfinite(confirmation_timeout) or not 0 <= confirmation_timeout <= 30:
        raise RescueError("停止確認の制限時間が不正です。")
    result = run_upterm(executable, ["session", "stop", name], timeout=65)
    if result.returncode not in (0, 4):
        raise RescueError(f"セッション停止失敗 (exit={result.returncode}): " + result.stderr[:300])
    end = time.monotonic() + confirmation_timeout
    while True:
        info = get_session_info(executable, name)
        if info.get("status") == "ended":
            return
        if time.monotonic() >= end:
            raise RescueError("停止要求後も終了を確認できません。状態と鍵を保存して再試行してください。")
        time.sleep(0.1)


def get_session_info(executable: Path, name: str) -> dict[str, Any]:
    if not SESSION_NAME_PATTERN.fullmatch(name):
        raise RescueError("セッション名が不正です。")
    result = run_upterm(executable, ["session", "info", name, "-o", "json"], timeout=15)
    if result.returncode not in (0, 4):
        raise RescueError(result.stderr[:300] or "セッションの状態取得に失敗しました。")
    if result.returncode == 4:
        raise RescueError("セッションが見つかりませんが、旧セッションの検索環境・実行ファイル同一性を保証できません。回復情報を保持します。")
    try:
        obj = json.loads(result.stdout)
    except ValueError as exc:
        raise RescueError("セッション状態のJSONが不正です。") from exc
    if not isinstance(obj, dict):
        raise RescueError("セッション状態JSONの形式が不正です。")
    if obj.get("name", name) != name:
        raise RescueError("セッション状態の対象が一致しません。")
    if obj.get("status") not in {"starting", "ready", "reconnecting", "disconnected", "ending", "ended"}:
        raise RescueError("不明なセッション状態です。終了とは判断しません。")
    obj["name"] = name
    return obj


def is_session_active(status: str) -> bool:
    return status in {"starting", "ready", "reconnecting", "disconnected", "ending"}
