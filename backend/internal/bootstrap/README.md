# 非秘密bootstrap記録とlaunch相関値

`rescue-host-bootstrap/1` の純粋codecです。
`control/1` やGUI/localcontrolのhello/snapshot/stopは変更しません。
`EncodeRecord` / `DecodeRecord` はbyte sliceを扱い、pipe・listener・鍵・サービスを開きません。
`Message` / `Config` にsigner、秘密鍵、token、任意pathはありません。

記録はUTF-8のJSON object一つとLFで、LF込み16KiB以下・container深さ8以下です。
未知・重複・大小文字違いfield、BOM、不正UTF-8、複数record、余分なbyte、途中終端は拒否します。
sliceの末尾は実pipeのEOF観測ではありません。I/O層による実EOFと取消完了/joinの確認は別途必要です。

| root field | 内容 |
|---|---|
| `protocol` | 固定版名 |
| `issuerInstanceEpoch` | 起動instanceの64文字lowerhex。session epochとは別の識別子 |
| `grant` | generation/full identity、keySetDigest、guestBoundary、元の絶対hardDeadline、backendLeaseCeilingNs |
| `binding` / `policyRef` | 固定artifact digest・channel nonce・既存policy参照 |
| `backend` | mode、raw Ed25519公開鍵1〜8、最大時間・短lease・既存capacity上限 |
| `hostKeyRef` | 配置済みcatalogのASCII token1〜128文字。pathではない |
| `stdio` | 明示したframesとcleanupTimeoutNs。frameBytesは16384固定で送らない |

整数はoverflow検査付きの正規decimal string、時刻はUTC RFC3339Nanoの`Z`表現をparse/formatで完全一致させます。
公開鍵はraw SSH Ed25519 marshal bytesの標準base64正規形で、parse・re-encode・既存KeySetDigestを照合します。
maximum/lease/capacityは既存backend上限を超えず、leaseはgrantの短lease ceilingも超えません。
現在時刻は読まず、deadlineを相対時間へ再基準化せず、Authority/Sessionも作成しません。
失効・期限・認可の判定は保護された所有者と既存runtimeの責務です。

`Expected` は将来の保護factoryがpayloadと独立に供給する全grant/config/binding値です。
`ReceiverLimits` のframes/cleanup ceilingも保護側が必ず明示し、既定値はありません。
現時点でこのfactoryは接続されていません。payloadをExpectedへ複製して照合成功と称してはいけません。
一致は構造上の相関確認であり、認証・承認済provisioning・grantの真正性を証明しません。

`FormatLaunchArgs` は固定順10引数を整形し、`ParseLaunchArgs` は未認証の`UntrustedLaunchArgs`だけを返します。
ASCII予算4096byte（各引数の終端分込み）、canonical positive uintptr、stdio3と第4handleの非zero・相互非重複を検査します。
handleはlocatorであり、実handleの型・権利・継承・由来は検査しません。
argv・nonce・epoch・hashを認証根拠とするAPIや、未認証結果からtrusted contextを作るfactoryはありません。

## 接続前の未実装境界

予定するnative受信側は、payload読取り前に保護profile、current primary User/service SID、impersonation不在、固定child imageを検査する必要があります。
継承するのは専用named-pipeのserver read endpointで、serviceだけがclient write endを保持します。
kernelのclient PID、固定serviceのSCM RUNNING/PID、保持peer processのlive/creation time/token/imageを照合・再確認します。
START_PENDING/STOP_PENDINGや不明な結果では拒否します。
SCMのRUNNINGはgrant0の制御面readyであり、SSH activeを意味しません。

同じephemeral issuer/Authority由来の送信権はlock内で一回burnし、blocking I/O・callback・cleanupはlock外で行う必要があります。
partial write/取消/失敗で再送・rollbackせず、serviceが全writer copyを閉じ、子がmessage終端と実EOFを確認する必要があります。
OVERLAPPEDの取消はCancelIoEx後の実completion/joinまでbuffer/event/handleを保持し、Closeを完了の代わりにしてはいけません。
この送信state・native pipe/factory・SCM ready配線は今回実装していません。

鍵は同じ保護主体のGo childが、固定provisioning profileとhostKeyRefで既存loaderから読み取る予定です。
実際のaccount/service SID、catalog path、ACL、鍵配置は未設定で、UI/env/argvから補いません。
remote非admin guestと保護主体を分離し、host operator/admin侵害やadmin guest隔離は保証しません。
実OS設定・実鍵・service/SSH起動・P2専用試験は実行しておらず、製品gateは閉鎖を維持します。

試験は公開RFC 8032の公開鍵と合成byte sliceだけです。
独立literalのdecode、literalと一致するencode、launch grammarは公開境界で検証します。
初期のliteral受理/末尾byte拒否とlaunch formatは同一入力のRed→Greenです。
launch parseと追加の拒否行列は回帰として扱い、TDD証拠の件数へ加えません。
