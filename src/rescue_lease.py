"""実行可能なオフライン用ライフサイクルモデル。本番環境の有効化機構ではない。

SSH 実装、待ち受け処理、認証、スケジューラー、Windows の隔離機構は提供しない。
外部の呼び出し元がこのモデルをポーリングする必要があり、Python が応答不能な間の
失効は保証できない。実際に保護された監督プロセスと Go アダプターが未実装であることは、
引き続きリリースを妨げる要因である。"""
from dataclasses import dataclass
from enum import Enum
import math
import threading
from rescue_policy import OperationPolicy


class State(str, Enum):
    PREPARED = "prepared"
    READY = "ready"
    ACTIVE = "active"
    REVOKING = "revoking"
    TERMINATED = "terminated"


class Rejected(RuntimeError):
    pass


@dataclass(frozen=True)
class SessionIdentity:
    launch_id: str
    run_epoch: str
    policy_digest: str
    schema_version: int = 1

    def __post_init__(self):
        if (type(self.schema_version) is not int or self.schema_version != 1
                or any(type(v) is not str or not v or len(v) > 128
                       for v in (self.launch_id, self.run_epoch))
                or type(self.policy_digest) is not str or len(self.policy_digest) != 64
                or any(c not in "0123456789abcdef" for c in self.policy_digest)):
            raise ValueError("invalid versioned session identity")


class OfflineSessionAuthority:
    """単一起動のモデル。時計と、close()/dial() の模擬アダプターのみを注入する。
    
    clock() は (wall_seconds, elapsed_seconds_including_suspend) を返す。
    所有者が構築時に有効期間とリース期間を指定し、ゲストのメッセージでは指定しない。
    dial() はゲストのアドレスを受け付けない。アダプターの接続先は tcp4 127.0.0.1:22 に固定する。
    受け入れた接続とプロセスはすべて、クローズに成功するまで保持する。"""
    def __init__(self, policy, identity, *, clock, process, transport,
                 max_duration=900, lease_duration=15, max_channels=8):
        if type(policy) is not OperationPolicy or type(identity) is not SessionIdentity:
            raise ValueError("expected immutable policy and identity")
        if identity.policy_digest != policy.digest:
            raise ValueError("policy digest mismatch")
        for value in (max_duration, lease_duration):
            if type(value) not in (int, float) or not math.isfinite(value) or value <= 0:
                raise ValueError("invalid duration")
        if (max_duration > 7200 or lease_duration > 60 or lease_duration > max_duration
                or type(max_channels) is not int or max_channels < 1):
            raise ValueError("invalid lease/capacity")
        self._policy, self._identity = policy, identity
        self._clock, self._lock = clock, threading.RLock()
        self._state = State.PREPARED
        self._last = self._read_clock()
        self._deadlines = tuple(t + max_duration for t in self._last)
        self._lease_deadlines = tuple(t + lease_duration for t in self._last)
        if any(not math.isfinite(d) for d in self._deadlines + self._lease_deadlines):
            raise ValueError("clock deadline overflow")
        self._lease_duration = lease_duration
        self._max_channels = max_channels
        self._sequence = -1
        self._pending = 0
        self._channels = {}  # id → 保持中のクローズ可能な対象（クローズ失敗分を含む）
        self._owners = {id(process): process, id(transport): transport}
        self._reason = None
        self._close_errors = ()

    @property
    def identity(self):
        return self._identity

    @property
    def state(self):
        with self._lock:
            return self._state

    @property
    def evidence(self):
        with self._lock:
            return {"identity": self.identity, "state": self._state.value,
                    "reason": self._reason, "pending_dials": self._pending,
                    "unclosed_channels": len(self._channels),
                    "unclosed_owners": len(self._owners), "close_errors": self._close_errors}

    def _read_clock(self):
        values = self._clock()
        if len(values) != 2 or any(type(v) not in (int, float) or not math.isfinite(v) for v in values):
            raise ValueError("invalid clock")
        return tuple(values)

    def _check_identity(self, identity):
        if type(identity) is not SessionIdentity or identity != self.identity:
            raise Rejected("session namespace, version, or policy mismatch")

    def _check_time(self):
        if self._state in (State.REVOKING, State.TERMINATED):
            return
        try:
            now = self._read_clock()
            if any(n < p for n, p in zip(now, self._last)):
                self.revoke("clock-regression")
            elif any(n >= d for n, d in zip(now, self._deadlines)):
                self.revoke("maximum-deadline")
            elif any(n >= d for n, d in zip(now, self._lease_deadlines)):
                self.revoke("lease-expired")
            self._last = now
        except Exception:
            self.revoke("clock-failure")

    def poll(self):
        with self._lock:
            self._check_time()
            if self._state == State.REVOKING:
                self.revoke(self._reason)
            return self._state

    def mark_ready(self, identity):
        with self._lock:
            self._check_identity(identity)
            self._check_time()
            if self._state != State.PREPARED:
                raise Rejected("not prepared")
            self._state = State.READY

    def activate(self, identity):
        with self._lock:
            self._check_identity(identity)
            self._check_time()
            if self._state != State.READY:
                raise Rejected("not ready")
            self._state = State.ACTIVE

    def renew(self, identity, sequence):
        with self._lock:
            self._check_identity(identity)
            self._check_time()
            if self._state not in (State.READY, State.ACTIVE):
                raise Rejected("lease cannot reopen session")
            if type(sequence) is not int or sequence <= self._sequence:
                raise Rejected("stale lease sequence")
            self._sequence = sequence
            self._lease_deadlines = tuple(min(t + self._lease_duration, d)
                                          for t, d in zip(self._last, self._deadlines))

    def open_channel(self, identity, channel_type, destination, port, *, dial):
        with self._lock:
            self._check_identity(identity)
            self._check_time()
            if self._state != State.ACTIVE or not self._policy.permits_channel(channel_type, destination, port):
                raise Rejected("channel denied")
            if self._pending + len(self._channels) >= self._max_channels:
                raise Rejected("channel capacity reached")
            self._pending += 1
        try:
            channel = dial()  # ゲストからのアドレス指定は受け付けない
        except BaseException:
            with self._lock:
                self._pending -= 1
                self.revoke("dial-failure")
            raise
        with self._lock:
            self._pending -= 1
            self._channels[id(channel)] = channel
            self._check_time()
            if self._state != State.ACTIVE:
                self.revoke(self._reason or "dial-revoked")
                raise Rejected("session revoked during dial")
            return channel

    def close_channel(self, channel):
        with self._lock:
            if self._channels.get(id(channel)) is not channel:
                raise Rejected("unknown channel")
            try:
                channel.close()
            except Exception:
                self.revoke("channel-close-failure")
                raise
            self._channels.pop(id(channel), None)

    def revoke(self, reason="explicit-stop"):
        with self._lock:
            if self._state == State.TERMINATED:
                return
            self._state = State.REVOKING
            self._reason = self._reason or reason
            failures = []
            for collection in (self._channels, self._owners):
                for key, handle in list(collection.items()):
                    try:
                        handle.close()
                    except Exception as exc:
                        failures.append(type(exc).__name__)  # エラー文字列に含まれる秘密情報を取り込まない
                    else:
                        collection.pop(key, None)
            self._close_errors = tuple(failures)
            if not self._pending and not self._channels and not self._owners:
                self._state = State.TERMINATED
