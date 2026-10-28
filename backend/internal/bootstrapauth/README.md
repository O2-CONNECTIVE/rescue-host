# bootstrap 発信元の native 認証

保護された固定 profile を使い、payload を読む前に現在の child と発信元 service を照合する内部 package です。
`NewNativeFactory` は検証と deep copy だけを行い、OS API、pipe の読取り、鍵の読取りを呼びません。
profile の構築は認証ではありません。
UI・env・argv・payload による profile、account、service、image、ACL の選択を認可しません。
actual の User/service SID、サービス名、配置証跡、鍵 catalog、image hash、正の `MaxBytes` が未設定なら構築を拒否します。
既定 account、LocalSystem、追加権限、別 path への fallback はありません。
service 名は固定 ASCII の英数字・`_`・`-`、1〜256 byte に限定します。

`Authenticate(ctx, borrowedServerRead)` は固定 child image と現在の primary User/service group、thread token 不存在を検査します。
借用 handle の PIPE 型、server/message、message-read/wait、読取権限のみ、owner、保護 DACL と service SID だけの ALLOW を確認します。
`NtQueryObject(ObjectBasicInformation)` の GrantedAccess を使用し、query 失敗・unknown を拒否します。
DACL の ALLOW は generic all またはその file generic mapping の完全一致だけを受理します。
`GetNamedPipeClientProcessId` の PID を、固定名を local SCM で開いた `SERVICE_RUNNING` の PID と照合します。
peer process は QUERY_LIMITED_INFORMATION と SYNCHRONIZE だけで保持し、既存 primary token の TOKEN_QUERY handle を保持します。
生存、creation time、User、enabled かつ非 deny-only の service SID、固定 supervisor image を照合します。
image の hash 前後にも origin を照合し、認証完了後は `Peer.Recheck` で再確認します。
START_PENDING、STOP_PENDING、照会失敗、PID・generation・主体の変更は拒否します。

`VerifyService(ctx, borrowedExistingPrimaryToken)` は現在の固定 supervisor image、SCM RUNNING/current PID を照合し、借用した既存 token の primary type・User・全 group が現在の primary と一致することを確認します。
token の作成・複製や account 変更は行いません。
proof の constructor と kernel seam は非公開です。
ゼロ値の factory は OS 照会へ進みません。
これらの proof は発信元の確認だけで、grant、Authority、起動 correlation の認可を作りません。

caller は認証と Recheck を専用 goroutine の `runtime.LockOSThread` で固定し、同じ thread で所有します。
Close だけは取得済み handle の回収専用として別 thread から呼べます。
この package は caller の thread 固定そのものを証明できません。
thread token の query は `ERROR_NO_TOKEN` だけを不存在として受理します。
current 主体や thread が dirty/unknown なら `ErrFatal` とし、上位 owner が Unlock せず goroutine を終了します。
library は `Goexit`、`RevertToSelf`、新しい clean worker での retry を行いません。
各呼出しには有効な期限付き context が必要です。
cleanup には独立した有効な期限付き context を渡せます。
同期 native query、hash read、close の非 return、OS sleep/hang をこの期限で中断できるとは主張しません。

`Peer` と `ServiceScope` は取得した image、SCM、process、query-token の handle を所有します。
借用 read pipe と借用 primary token は close しません。
途中の失敗でも取得済み参照があれば proof と error を同時に返すため、caller は error 時も proof を保持し `Close(ctx)` を呼びます。
Close は入口で認証を不可逆に失効させます。
同じ thread で未汚染のときだけ入口で主体を1回確認し、foreign/poison 済みの場合は新たな認証 query を行いません。
その後は既所有 image/service/process/query-token の close だけを行い、認証 query・read・新しい open に戻りません。
Close 失敗は `ErrUnconfirmed` とし、その参照だけを保持して同じ scope の Close で再回収できます。
成功した handle を二重 close しません。
回収開始後の Recheck は拒否します。
foreign/dirty/poison 済みでも既所有 handle の回収だけは行います。
認証の poison は不可逆で、回収完了・closed 後も `ErrFatal` を保持します。
正常に回収済みの scope は別 thread から Close されても query や新しい poison を行わず成功を返し、Recheck は `ErrClosed` を返します。
回収が未確認なら `errors.Join(ErrFatal, ErrUnconfirmed)` を返し、caller は `errors.Is` で両方を判定します。
回収成功は認証成功を意味せず、上位の dirty thread 退役契約も変わりません。

image は公開 exe なので untrusted な読取・実行 ALLOW を許します。
全 ancestor と leaf で、untrusted な実効 ALLOW の子作成・書込・EA/属性変更・子削除・削除・DACL/owner 変更を拒否します。
危険な ALLOW を別 DENY で相殺しません。
unknown ACE/flags/mask、NULL DACL、reparse、path/volume/filesystem/owner/ACL の不一致を拒否します。
全 node を read-only、shareREAD のみ、非継承で保持し、FileID と metadata を前後照合します。
ancestor は directory、leaf は非 directory かつ単一 link、正の明示サイズ予算内であることを要求します。
link 数 == 1 は leaf 限定です。
leaf の SHA256 と byte 数を確認し、同じ handle を保持したまま Recheck でも再確認します。
現在の ACL、hash、共有 flag は過去の mutation handle や mapping、配置前の保護成立を証明しません。
配置時から保護されていたことと過去の侵害不存在は別の provision evidence が必要です。

pipe の FIRST_PIPE_INSTANCE、REJECT_REMOTE_CLIENTS、作成時のランダム名、全 writer copy の管理、正確な継承 whitelist は生成側の source 契約です。
この認証側がそれらすべてを後から query して証明したとは主張しません。
EOF 後の GetNamedPipeClientProcessId の成功継続も未実証です。
EOF 後の Recheck で query が失敗した場合に例外受理する経路はありません。

試験は全 OS 共通の private memory kernel を注入し、公開 factory/Authenticate/VerifyService/Recheck/Close の判定経路を通します。
実 kernel へ到達する有効 profile の試験はありません。
red/green は未構成拒否、固定 image 構成、origin 保持、hash 後の再確認、失敗時の dirty thread、zero factory、native 境界の fatal 伝搬の各同入力に対応します。
追加の同入力 red/green は Peer/ServiceScope の foreign・dirty・既 poison を通じ、認証 query を増やさず回収し、失敗参照の retry と fatal 保持を確認します。
その他の拒否 matrix、部分回収、token、image 変更試験は実装後の回帰確認として区別します。
Windows test binary の cross compile は型の確認で、実認証の成功証拠ではありません。
実 pipe/service/account/ACL の操作、鍵生成・読取り、SSH/listener、P2 の専用試験は行っていません。
製品 gate と既存受入条件の判定は変更しません。

API 根拠:
[NtQueryObject](https://learn.microsoft.com/en-us/windows/win32/api/winternl/nf-winternl-ntqueryobject)、
[GetNamedPipeHandleState](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-getnamedpipehandlestatea)、
[GetNamedPipeClientProcessId](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-getnamedpipeclientprocessid)、
[QueryServiceStatusEx](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/nf-winsvc-queryservicestatusex)。
NtQueryObject は Microsoft が変更・削除の可能性を示している API であり、不在・未知の結果は拒否します。
EOF 後の照会成功や現在の配置保護を、これらの資料だけから推定しません。
