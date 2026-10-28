# 配置済みホスト鍵の参照 loader

保護された配置元が与えた固定 ref から、配置済みの raw Ed25519 ホスト鍵を読む内部 package です。
新しい鍵の生成・保存、ACL の変更、アカウントやサービスの設定は行いません。
製品の bootstrap、SCM、GUI、外部要求には接続しておらず、開始 gate は閉じたままです。

`NewNative(Profile)` は入力を検証・複製するだけで、ファイルも token も開きません。
実読取りには、認可済み receiver 所有の専用 goroutine が先に `runtime.LockOSThread` を行い、
同じ thread で `BeginNativeScope` → `Load(scope.Context(), fixedRef)` → `scope.Close` を呼ぶ必要があります。
scope は OS 上の主体・thread 一致を検査しますが、receiver の認可そのものではありません。
guest 要求 handler や任意 callback から直接呼び出してはいけません。
`ErrFatal` では上位の goroutine 所有者が Unlock せず終了し、別 worker で同じ要求を再試行しません。
この package 自体は caller を終了させず、impersonation 解除・権限変更・別 worker への移送もしません。

## 実配置前に確定する入力

| 入力 | 提供元と必要な前提 |
|---|---|
| process の期待 TokenUser SID・必須 service SID | 読取りを行う service または child の固定 identity。組込み account を自動選択・昇格せず、必須 service group が enabled かつ非 deny-only であることを検査 |
| operator SID・分離した非 admin guest SID | 保護された配置情報。trusted 主体・group に guest が含まれないことは配置側の契約 |
| ref → canonical local path・公開 host fingerprint | ref は ASCII の英数字・`_`・`-` の1〜128文字、最大8件。要求から任意 path を渡せない。fingerprint は公開鍵の SHA256 表現で、秘密鍵ファイルの hash ではない |
| volume root・serial・filesystem | 固定 local drive と NTFS/ReFS の profile。実 volume やその保護は未確認 |
| root から leaf の全 owner・期待 ACL | 各 node の実情報と完全一致を要求。未知の ACE/mask、NULL DACL、reparse、別 path/volume を拒否 |
| 保護成立時点・配置証跡の参照 | OS 保護が成立するまでに untrusted な旧 handle・mapping・秘密の複製が存在しなかった前提の根拠。単なる時刻や `verified=true` で代替できない |

これらの値の供給元と承認経路は未実装です。
`Profile` を UI・wire・env から直接構築しても、信頼された配置にはなりません。
LocalService、NetworkService、LocalSystem、明示した account SID は型として検証できますが、採用 identity は未決であり fallback はありません。
初期保証対象は別の remote 非 admin guest からの保護です。host operator/admin の侵害や admin guest からの隔離は保証しません。

## 読取りと拒否方針

thread token query の `ERROR_NO_TOKEN` だけを不存在として受理します。
process primary token の User と service group を確認し、scope の thread/profile が異なる場合や不明な場合は不可逆に scope を拒否します。
root から全 node を順に開いて保持し、読取り前後の file ID・owner・ACL・path・size 等を照合してから全 handle を閉じます。
成功・失敗の両方で終了時の主体を再検査します。
native API は query/open/read/close のみで、検査に失敗しても別 path や権限への fallback はありません。

全 ancestor で untrusted な実効 ALLOW の子作成・書込・EA/属性変更・子削除・削除・DACL/owner 変更を拒否します。
leaf ではデータ読取・実行も拒否します。generic rights は展開し、危険な ALLOW を DENY で相殺して受理しません。
この保守的な方針は通常の drive root ACL を拒否する場合があります。
create-child 例外、別 root の選定、既存 ACL の変更をこの実装から自動で行いません。

鍵は最大16KiB、単一の非暗号化 OpenSSH Ed25519 private-key envelope に限定します。
外側/内側の公開鍵、秘密鍵の公開 half、固定公開 fingerprint、署名による鍵の整合性を確認します。
暗号化鍵、certificate/security-key、別鍵種、複数鍵、余分な PEM/data は拒否します。
一時 byte slice の clear は最善努力で、Go runtime による全複製の消去を保証しません。

## 確認できない範囲

shareREAD と前後照合は directory tree 全体の変更排他や配置の真正性ではありません。
Volume/FileID は同じ object の照合用で、信頼された配置元の証明ではありません。
現在の ACL は過去の read/ACL変更 handle や取得済み秘密の不存在を証明しません。
shareWRITE を認めない open は既存の write-access mapping と競合し得ますが、過去の全アクセス履歴の証明にはなりません。
context の期限は各境界で確認しますが、同期 file I/O の非return、OS sleep/hang、実プロセスの停止を解決しません。

試験はメモリ上の OS 境界と公開済み RFC 8032 TEST 1 の合成 test vector のみです。
実ファイルへの秘密鍵保存、実鍵読取り、実 WinAPI の呼出し、native scope の実起動は行っていません。
Windows 向けコンパイルは API の型を確認するだけで、ACL・identity・保護された bootstrap の成立証明ではありません。

根拠: [file access rights](https://learn.microsoft.com/en-us/windows/win32/fileio/file-access-rights-constants)、
[CreateFileW](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew)、
[DACL の評価](https://learn.microsoft.com/en-us/windows/win32/secauthz/how-dacls-control-access-to-an-object)、
[pseudo thread token](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-getcurrentthreadtoken)、
[LockOSThread](https://pkg.go.dev/runtime#LockOSThread)。
