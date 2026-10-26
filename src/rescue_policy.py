"""変更不可のオフライン用ポリシー仕様。SSH サーバーやセキュリティ境界ではない。"""
from dataclasses import dataclass
import hashlib
import json

OPERATION_MODES = ("restricted", "full")
RESTRICTED_HELPERS = ("diagnostics.system", "diagnostics.network", "repair.restart-approved-service")


@dataclass(frozen=True)
class OperationPolicy:
    operation_mode: str = "restricted"
    schema_version: int = 1

    def __post_init__(self):
        if self.operation_mode not in OPERATION_MODES or type(self.operation_mode) is not str:
            raise ValueError("unknown operation mode")
        if type(self.schema_version) is not int or self.schema_version != 1:
            raise ValueError("unsupported policy schema")

    @property
    def digest(self):
        body = {"schema": self.schema_version, "operation_mode": self.operation_mode,
                "channel": "direct-tcpip", "destination": ["127.0.0.1", 22],
                "restricted_helpers": RESTRICTED_HELPERS}
        return hashlib.sha256(json.dumps(body, sort_keys=True, separators=(",", ":")).encode()).hexdigest()

    def permits_channel(self, channel_type, destination, port):
        return (type(channel_type) is str and channel_type == "direct-tcpip"
                and type(destination) is str and destination == "127.0.0.1"
                and type(port) is int and port == 22)

    def permits_helper(self, operation, arguments=()):
        # 任意のヘルパー名、シェル文字列、ゲストが選択したサービス名は許可しない。
        return (type(operation) is str and operation in RESTRICTED_HELPERS
                and type(arguments) is tuple and arguments == ())

    @property
    def allows_inner_shell(self):
        # 宣言的な意図の記述にすぎない。内部 SSH サーバー側で別途強制する必要がある。
        # full でも権限昇格や管理者権限は認可しない。どちらのモードも外側のチャネルを変更しない。
        return self.operation_mode == "full"

    @property
    def allows_admin(self):
        return False
