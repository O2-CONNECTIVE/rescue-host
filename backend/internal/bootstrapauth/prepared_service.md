# RUNNING 前の service 自主体の準備

`Factory.PrepareService(ctx, borrowedPrimaryToken)` は、保護された固定 profile の service 自主体と supervisor image を確認します。
SCM の RUNNING、service/process object の ACL、service binary 設定、listener の準備、operator、peer、承認、grant は証明しません。
これらの準備と許可0の service readiness は上位が別々に確認します。
`PreparedService` は constructor と内部状態を公開せず、`ServiceScope` や `Peer` への変換も提供しません。
既存の `VerifyService` と `Authenticate` は引き続き SCM RUNNING を要求します。
operator 認証の代用として、いずれの自主体検査も使えません。

`Factory.CheckServiceCaller(ctx)` は SCM entry の元 caller を service owner goroutine へ移す前に拒否するためだけの guard です。
元 caller の固定 thread、thread token 不存在、primary User・enabled/non-deny service SID、current PID 非0、固定 supervisor image path を確認します。
有限 context と前後の主体を確認し、dirty/unknown なら `ErrFatal` を返します。
owned handle、file/key、SCM、pipe、別 process は開かず、opaque proof も返しません。
成功しても ready/RUNNING、server、operator、approval の証明ではなく、operator 認証には使えません。
移送先の owner は `PrepareService` の完全な hash/ACL/metadata 検証を引き続き行います。
元 caller の guard と owner 側の full 検証は、一方を他方の代用にしません。

factory 自体は既存の静的検証・deep copy を使います。
Prepare の入口では固定 profile、有効な期限付き context、借用した既存 primary token の実 handle を要求します。
caller は専用 goroutine を `runtime.LockOSThread` で固定し、Prepare と Recheck を同じ thread で行います。
最初に既存 `begin` が thread token 不存在と現在の primary User・enabled/non-deny service SID を確認し、失敗時は image を開きません。
新しい token の作成、権限の追加、impersonation の解除、新しい worker への retry は行いません。

その後は固定 supervisor image の全 ancestor と leaf を保持します。
既存 image 検査で、owner、完全一致 ACL、path、volume/filesystem、非 reparse、FileID と metadata の前後一致、leaf の単一 link、明示サイズ上限、SHA256 を照合します。
current image path、最初に保持した非0の current PID、借用した primary token と現在の primary の type・User・全 groups を、hash の前後に照合します。
SCM status、named pipe、別 process の open/query は呼びません。
現在の ACL・hash・共有 flag は過去からの保護成立や取得済み mutation handle の不存在を証明しません。
実配置の protection/provisioning evidence は別途必要です。

`Recheck(ctx)` は同じ自主体、固定 PID/path、借用 token、保持した image を再確認します。
専用の admission gate は context で待機を打ち切り、scope の判定と回収を直列化します。
admission の打切りは成功の証明ではなく、呼出しは `ErrDenied` を返します。
その場合、既に gate 内で進行中の別処理や所有参照を外から変更しません。
上位 readiness owner はこの失敗を受けて受理を失効させ、認可成功として扱わない契約です。
admission 後の自主体・token・image の不一致は、この準備 scope を不可逆に拒否します。
thread の dirty/unknown は既存と同じく `ErrFatal` と poison を保持し、上位 owner の thread 退役契約を維持します。

途中失敗でも取得済み参照があれば `PreparedService` と error を同時に返します。
caller は失敗時も object を保持し、有効な独立 cleanup context で `Close` を呼びます。
Close の admission が終わらない場合は `ErrUnconfirmed` として全参照を保持します。
gate 内の Close は既存 `closeScope` に回収を委ね、既所有 image handle だけを解放します。
借用 token は解放せず、SCM 状態や認証を新たに確認して権限を復活させません。
foreign/dirty/poison 済みの回収、失敗参照だけの保持・再回収、正常な回収済み scope の冪等 Close、closed 後も poison が優先する規則は既存と同じです。
必要な場合は `errors.Is` で `ErrFatal` と `ErrUnconfirmed` の両方を判定します。

context は admission と処理境界の判定です。
同期 query/hash/close の OS 非 return、sleep/hang を中断したり、gate 待機の打切りだけで先行処理の join が完了したりする保証はありません。
この package は処理を代替 worker へ移送しません。

試験は既存の private memory kernel を注入し、公開 Prepare/Recheck/Close を通します。
RUNNING 前に SCM/peer/pipe を使わず準備する経路と、Recheck の bounded admission・PID 変更拒否を、同じ入力の red/green で確認しました。
借用 token・image の変更、部分取得、dirty thread、cancel と fatal の優先、回収の失敗保持、旧 RUNNING 必須経路の拒否維持は追加の回帰確認です。
元 caller guard は clean/dirty・User/service SID・PID/path・query 失敗・検査後の dirty を同入力の red/green で確認し、有限 context、cancel と fatal、guard 成功でも image hash 不一致を拒否する完全検証の維持を追加回帰で確認しました。
実 DLL、file/key、SCM、pipe、service/account/ACL の操作は行いません。
Windows 向け cross compile は型の確認で、実際の準備・保護成立や RUNNING の成功証拠ではありません。
製品 gate と既存受入条件の判定は変更しません。
