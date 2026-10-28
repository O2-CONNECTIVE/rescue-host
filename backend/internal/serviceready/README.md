# sessionなしのservice準備

`serviceready`は、固定serviceの自主体・配置・OS objectの保護と、service所有listenerのkernel準備を保持する。
operator命令、operatorに対するserver認証、承認codec、session、grant、SSH接続を提供しない。
listenerが接続済みでも、payloadを一切読み書きせず、命令受理や認可の成功と呼ばない。

`NewNative(Config)`は静的検証とdeep copyだけを行う。
Profile、既存PrimaryToken、Endpoint、ServiceCommandLine、service/processの明示SecurityPolicy、CleanupTimeoutが必須である。
ProfileのSID、catalog、両imageの祖先・ACLを複製し、入力の後からの変更を採用しない。
Endpointは`\\.\pipe\rescue-service-<ServiceName>`に限定する。
名前やepochは認証証拠ではない。
ServiceCommandLineはSupervisorImage.Pathのquoted exact、またはspaceのないbare exactだけを許し、追加argsや曖昧な解釈を採らない。
別のsupervisor executableを想定するsource候補であり、実service identityや登録方式の採用ではない。

`CheckServiceCaller(ctx)`は保持したauth factoryの元SCM caller guardへ委譲する、拒否専用wrapperである。
proofやowned refsを作らず、元callerのimpersonation等をworker作成前に拒否するためだけに使う。
workerの完全なPrepare、固定image/hash/ACL/token確認、operator server認証を代替しない。

`Prepare(ctx)`はcaller所有の固定OS threadでauth.PrepareServiceを行う。
現在のservice principal、借用primary token、固定supervisor imageを確認し、SCMがRUNNINGであるとは仮定しない。
local SCMの固定serviceをREAD_CONTROL/QUERY_CONFIG/QUERY_STATUSだけで開き、現在processをREAD_CONTROL/QUERY_LIMITED_INFORMATION/SYNCHRONIZEだけで保持する。
held serviceのraw binarypathを明示ServiceCommandLineと照合し、own-process type0x10だけを許す。
SCMの実service account名を入力profileへ採用しない。
QueryServiceConfigの情報は次回起動の設定でもあり、現在imageの証明は独立したauth preflightが担う。

service/processのowner・protected DACLを明示expected policyへexact照合する。
信頼主体はUser/Service/Operator/SYSTEM/Administratorsで、Guestを常に除外する。
host operator/adminの既侵害は今回の脅威scope外であり、追加の別admin承認を実装しない。
未知主体・GuestのALLOWはserviceで0x2008d、processでREAD_CONTROL|SYNCHRONIZE（0x120000）だけを許す。
PROCESS_QUERY_LIMITED_INFORMATION、QUERY_INFORMATION、generic、MAXIMUM_ALLOWEDの例外を未知主体へ作らない。
PROCESS_QUERY_LIMITED_INFORMATIONはOpenProcessTokenのprocess側の前提であり、tokenに得られるアクセス権はtoken object自身のDACLで別に判定される。
query許可だけでtoken奪取が成立したとは扱わず、今回はtoken DACLを検査していないため不要な入口を閉じる。
trusted User/Service/Operator/SYSTEM/Administratorsのquery権限と、native照会自身のdesiredAccessは維持する。
既取得・継承・漏出したprocess/token handleが無いことは別の前提であり、このACL検査で不在を証明しない。
STOP、CHANGE_CONFIG、WRITE_DAC/OWNER、DELETE、VM_READ/WRITE/OPERATION、DUP_HANDLE、TERMINATE等を未知主体へ許さず、DENYで危険なALLOWを相殺しない。
未知ACE、継承flags、未知mask、NULL DACL、非protected policyを拒否する。

`PreparePreflight(ctx)`は、`Prepare`の同じquery-only prefixを共有し、auth scopeとheld service/process refsをopaqueな`Preflight`として保持する。
元の固定thread、no impersonation、profileの自主体・固定image、service binarypath・type・DACL、current processのalive・PID・creation・DACLを既存と同じ規則で検査する。
借用primary tokenはquery-onlyでよく、ここで強いtoken handleを開いたり、既存query handleを強いlaunch handleへ昇格させたりしない。
listenerの作成・connect・RUNNING待機・`ConfirmRunning`は行わない。
既存query検査が許すstateはSTART_PENDING/RUNNINGだけで、START_PENDINGのSCM PIDを証拠にせず、既にRUNNINGならcurrent PIDの一致条件を維持する。
Preflightの成功をlistener/SCM readiness、operator/server認証、approvalの証明と呼ばない。
`Prepare`はこの同じprefixの成功後にだけlistener準備へ進む。

`Preflight.Recheck(ctx)`と`Binding(ctx)`は同じscopeのquery証拠を再照合する。
`ProcessBinding{PID, Creation}`は保持processの相関値であり、値の構築や複製だけでは認証やtokenの取得元を証明しない。
取得元の証明はopaque Preflightに残し、strong-token supplierは強handleを開く前後に同じscopeを再検査する必要がある。
このPreflightはtoken object/default DACLや強handleのdesired access自体を検査しないため、その別検査も省略できない。
ctx/admission失敗、query不一致、元thread・主体の変化で受理をstickyに失効させ、並行処理も成功したBindingを返さない。
失敗時のBindingはzero値であり、部分取得のPreflightも同じpointerを保持してCloseする。
PreflightのCloseはlistenerを持たない同じ有限cleanup経路を使う。

listenerは非継承、DUPLEX、OVERLAPPED、FIRST_INSTANCE、REJECT_REMOTE_CLIENTS、MESSAGE/READMODE_MESSAGE/WAIT、単一instanceで作る。
ownerはProfileのProcessUserSID、protected DACLはGuest DENY0x1f01ff、Service ALLOW0x1f01ff、Operator ALLOW0x120183の順序とする。
OperatorにFILE_CREATE_PIPE_INSTANCE0x4やgeneric WRITEを与えない。
GetFileType、server/message mode、buffer、instance数、GetHandleInformation、kernel server PID、owner/DACLをconnect開始前と再検査時に照合する。
creation identityは未解放のCreateNamedPipe返却handleとkernel server PIDで保持し、pathを開き直したりFileIDを捏造したりしない。
16 KiBはbufferの指定とactual照合であり、Windows message長の強制ではない。
将来のoperator protocolには独立したpeer/server認証とframe上限が必要であり、今回は未実装である。

`ConfirmRunning(ctx)`はstatus channelへ送った値をkernel ACKとせず、held serviceの実SCM RUNNINGとPID=currentPIDを確認する。
START_PENDINGはcallerの有限deadline内だけ待ち、その間のSCM PIDを一致根拠に使わない。
他state、停止、PID・process creation・ACL・binarypath・listenerの変化、query失敗を拒否する。
RUNNING確認後にSTART_PENDINGへ戻ることも拒否する。
`Recheck`または`ConfirmRunning`の失敗はctx/admissionを含めて同じPlaneの受理権を不可逆に失効し、並行して進行中の検査も復活成功を返さない。
上位層はこの失効をissuer閉鎖へ合流させる。

`Close(ctx)`はcaller deadlineとCleanupTimeoutの短い方でGo mutex admissionと回収を制限する。
CancelIoEx成功やNOT_FOUND、handle closeをcompletionとせず、実GetOverlappedResultの既知の終端を確認する。
未完了connectのOVERLAPPEDをpinし、event/listener handleも保持する。
Planeを初回利用後にコピーせず、同じpointerを保持する。
op mutexもTryLockで取得し、別のquery/pollのGo mutexを無期限に待たない。
query refsとauth scopeはconnectの回収とは独立に試み、部分取得や不明状態では同じPlaneを保持する。
auth scopeのexact `ErrFatal`単独だけは同scopeの全refs回収済み契約に従って参照を解放し、Fatal自体はstickyに保持する。
`Fatal+Unconfirmed`やunknown errorとのjoinをこの例外へ広げず、同じscopeを保持して再回収する。
ErrUnconfirmedならfresh finite contextで同じCloseを再試行し、別operationで回収済み扱いにしない。
Fatalはctx不正、既取消、foreign thread、回収後も通常拒否へ縮小しない。
Fatal時のOS thread退役は上位entryの責務である。
worker、fatal exit、panic、Close-as-completionを追加しない。
inline native API自体の非returnやOS強制終了は解決・実証していない。

試験はprivate kernelのfakeと合成policyだけを使う。
公開factoryはnative/auth factoryへ固定し、valid profileでPrepareやcaller guardの実OS照会を行う試験はない。
従来の10組とPreflight/query-only prefix・確認済みFatal回収の追加2組の同入力red/greenに加え、ACL mask、kernel証拠変化、ctx/admission失効、部分取得、独立回収、Fatal保持を回帰確認する。
Windows両archのcrosscompileは実DLL、実pipe、SCMの動作試験ではない。
実service/account/ACL変更、鍵生成、SSH/listener実行、P2再現、製品有効化を行っていない。

- [Service security and access rights](https://learn.microsoft.com/en-us/windows/win32/services/service-security-and-access-rights)
- [Process security and access rights](https://learn.microsoft.com/en-us/windows/win32/procthread/process-security-and-access-rights)
- [QueryServiceConfig](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/nf-winsvc-queryserviceconfigw)
- [QueryServiceStatusEx](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/nf-winsvc-queryservicestatusex)
- [Named pipe security](https://learn.microsoft.com/en-us/windows/win32/ipc/named-pipe-security-and-access-rights)
- [CreateNamedPipe](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-createnamedpipea)
- [GetNamedPipeHandleState](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-getnamedpipehandlestatew)
- [GetNamedPipeInfo](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-getnamedpipeinfo)
- [GetNamedPipeServerProcessId](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-getnamedpipeserverprocessid)
- [GetHandleInformation](https://learn.microsoft.com/en-us/windows/win32/api/handleapi/nf-handleapi-gethandleinformation)

- [OpenProcessToken](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-openprocesstoken)
- [Access-token object rights](https://learn.microsoft.com/en-us/windows/win32/secauthz/access-rights-for-access-token-objects)

現candidateはservice/processの`SE_DACL_PROTECTED`も必須とする。
このflagは継承ACEによるDACL変更を抑える条件であり、単独でguestのstop/tamper拒否を証明しない。
中心条件はtrusted owner、expected ACL完全一致、危険なALLOWの排除である。
対象Windowsのdefault値、process/service objectでの必要性・実現性は未実証で、この変更は追加DACL設定を実施/承認したものではない。
条件が満たされなければ拒否し、実証なしにflag条件を緩めたりOS設定を変更してgreenにしたりしない。
このcandidateの採否と必要な実設定は、対象環境での根拠と別のaction-time承認に残す。
