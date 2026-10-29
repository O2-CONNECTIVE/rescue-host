# 登録後に固定 worker が取得する service scope

`prepareNativeBatch` は保護された `bootstrapauth.Factory` と借用 primary token を保持し、thread 照会・token 照会・pipe 作成・起動を行わない。
Service が同じ Batch と canonical Owner を登録した後、`Start` の admission 内で認証 worker 自身が `VerifyService` を呼ぶ。
借用 token は `nativeBatchSpec.launch.primaryToken` の同じ参照で、Batch は close しない。
Factory の供給元は保護配置であり、UI・env・argv・観測した SID から組み立てる公開入口は追加しない。

Service の認証交換は既存の `runtime.LockOSThread` で worker を固定する。
Batch 自体は別 thread を確保せず、scope 取得時の OS thread ID と後続 caller を照合する。
`Start`、`Collect`、`Close` は同じ worker で実行する。
scope の `Recheck` は service 自身の証拠であり、operator の認証・同意・grant を表さない。

constructor が scope と error を同時に返した場合も、同じ Batch が部分取得を保持する。
取得途中で元 request が取り消された場合や issuer が停止した場合、返った scope を捨てず、次の acquisition を拒否して回収へ進む。
constructor の panic は返らなかった参照が不明であるため、回収完了へ置き換えない。
未取得 scope の pre-start Close と回収済み scope の guard は新しい thread/token query を行わない。

正常な回収順序は profile I/O、report I/O、親の read handle、helper の保持 process/Job、protected provider の pending query 参照、service scope である。
scope の `Close` が完了するまで、Collector の I/O/process 回収完了記録を確定しない。
scope close の期限・回収失敗は同じ Batch に参照を残し、有限 Close の再確認へ渡す。
`bootstrapauth.ErrFatal` の単独返却は scope の参照回収済み記録を残すが、worker は再利用しない。
`ErrFatal` と `ErrUnconfirmed` の join は未回収参照を保持する。
いずれも Batch の `Close` は fatal を伝播し、成功 ack を返さない。

foreign/dirty/fatal caller では新しい native I/O 回収を開始しない。
この場合は scope Close を I/O/child 回収より先行せず、未確認 scope も保持する。
worker の退役は Service worker の既存 `keepLocked` が担い、Batch は `ErrFatal` を返す。
scope の回収を無条件に試みるためにこの契約を緩めない。

private `batchScopeOps` はメモリ試験の seam だけである。
任意の公開 callback や成功 bool から native provenance を生成しない。
通常試験は scope の遅延部分取得、取消、停止、foreign caller、panic、close failure と回収順序をメモリ内で検査する。
Windows source の compile 確認と、実 thread/token/ACL/service の成功は別である。
正規 profile と endpoint query lease の発行元は未接続で、製品 gate は閉鎖を維持する。
