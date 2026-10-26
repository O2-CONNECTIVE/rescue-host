# Changes

## 0.2.0-dev
- Fail closed: disable new terminal and SSH forwarding sessions pending an enforcing backend.
- Disable unsafe connection sharing, including recovered legacy sessions.
- Harden public-key/state/version/output validation and isolate Upterm configuration.
- Require stop confirmation; preserve recovery evidence on uncertainty.
- Add acknowledged watchdog startup, monotonic deadline bound, and bounded retries (still best effort).
- Ignore stale session polling results; label legacy sessions as stop-unconfirmed.
- Require explicit SHA256 and license for local Upterm packaging; pin reviewed upstream version.
- Add offline negative regression tests and a security backend acceptance contract.

No production connection, external publication, GitHub push, real Upterm execution, or Windows build performed.

### 日本語要約
新規接続を安全側で停止し、公開鍵・設定・回復状態の検証を強化。停止確認、監視起動応答、時計巻き戻し対策、古い非同期応答の排除を追加しました。旧セッションの接続共有も無効です。42件のオフラインテストと構文検証に成功していますが、厳密な転送先限定・確実な失効のバックエンドは未実装です。
