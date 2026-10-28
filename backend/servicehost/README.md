# 保護serviceのruntime接続と残るbootstrap

初期評価はremote nonadmin guestからsupervisor/停止処理を守る範囲とし、host operator/adminが既に侵害されている場合とadmin guestは対象外である。
製品gateは閉鎖している。
以下のsource接続は、別主体・ACL・実process/SSHの成立や既存受入の合格を意味しない。

## この変更で接続する経路

`ProtectedProvisioning.ReadApproved`はIdentityを要求しない今回の型付き`ephemeral.ApprovedPolicy`だけを返す。
Hostがfresh issuerを作り、Prepare→Launchで単回消費して得た同じAuthorityをLauncherへ渡す。
Host.Runは別の`supervisor.New`を作らない。
再起動時は許可0から始まり、保存UI state/旧grantを復元しない。

`JobRuntime`は保護provisionerが準備した固定artifact/既存primary token/pipeだけをchildjobへ渡す。
childjobはsuspended start→Job割当→resumeを実装し、Go準備完了とは区別する。
その後の保護bootstrapと`OwnedControl.Start`に成功した場合だけ起動を受理する。
OwnedControlは既存control/1のhello/ack、start/receipt、snapshot、stop/receipt/終端snapshotを照合する。
pollはrenewや再承認ではない。
IPC喪失/予期しない状態/期限でHostがcoreを失効し、Go停止の確認とJob回収を別々に行う。
Goの停止応答だけでcleanup予算を消費しないよう、残り予算の半分までをGo終了要求と実EOFに使う。
その後の残り予算の半分までを正常exit0/Job空のWaitへ使い、失敗時だけ元の期限内で強制回収へ進む。
強制回収が成功しても、正常終了の証拠へ変換せず未確認とする。

`runbackend.ApprovedConfig`は保護側が渡したgrant snapshotとConfig/鍵集合/wire binding/短lease/期限を照合し、本体Session.New→Ready→Activate→既存stdioへ接続する。
子は新しいsupervisor Authorityを作らない。
Runtime.Serveは受理済みtransportだけを本体Sessionへ渡し、listenerや任意dial先を追加しない。
既定commandはbootstrap providerがnilで、`--supervised-internal`だけでは解除されない。
既定SCM入口もprovisioning未設定のままである。

| 入口 | 呼出主体と許可範囲 |
| --- | --- |
| 初回承認の型付きsource | operator認証後の別入口。Identity発行前の今回の承認だけ。実装/認証経路は未設定 |
| service-owned child pipeのOwnedControl | 保護serviceだけが所有する子pipe。今回のbootstrap後にhello/startを送る。GUI要求をここへ転送しない |
| operator向けlocalcontrol | mainの認証付きpipe候補。hello/snapshot/stopだけ。Start/Renewを許可する入口ではなく、非admin guestを拒否する |

初回限定評価でも、実経路での対象鍵の署名認証、固定127.0.0.1:22、停止/最大期限/再接続拒否、Close・EOF・worker join・process reapの証跡は必要である。
初回評価と44条件のGA判定を分け、通常CIで未実証のP2や既知NOT_MET/UNVERIFIEDを消さない。

## 保護bootstrapの具体的な残件

旧`ChildPlan.Bootstrap`は型付き接続点として残す。
native経路は`NewNativeChildProvisioning`と`PreparedBootstrap`を使い、同じAuthority/issuer相関から非秘密記録を専用pipeへ一回送るsourceへ接続した。
child側は`bootstrap.NativeReceiver`がpayload前のnative認証、messageと実EOF、独立argv相関、保護上限、固定key catalogを照合し、同じ保護主体の鍵scopeからConfigを返す。
秘密鍵そのものは配送しない。pure codecの`Expected`へpayloadを自己コピーする経路も使わない。
具体的なプロフィール値の供給と実OS成立は未確認であり、以下を曖昧な「設定」へまとめない。

| 入力 | 生成者・信頼根 | 必須の照合 |
| --- | --- | --- |
| 今回の承認policy、絶対deadline、lease ceiling、予算 | kernel tokenでoperatorを認証した別の初回承認入口。guestは拒否。今回の人の操作が必要で、自動起動の保存値を承認にしない | 固定policyRefとraw Ed25519許可鍵集合を保護側で解決。初回承認へ既存Identityを要求しない |
| generation/full Identity | service内のfresh ephemeral issuer | 同じAuthority、単回challenge、known binding。新serviceは旧callback/旧grantを拒否 |
| artifact path/hashとservice/operator/guest SID | 承認済みの固定配布manifestとOS provisioning | 任意UI pathなし、guest分離、正規token、保護root/祖先/ACL、固定artifactと起動対象の一致 |
| channel nonceと所有pipe | serviceが今回の子用に新規発行・所有 | nonceは識別子で認証ではない。childjobの限定handle継承と同じ起動へ束縛 |
| grant snapshot、backend Config上限、WireContext、stdio予算 | 同じservice Authorityから導出した一回限りのbootstrap | generation/Identity/鍵digest/方針/絶対deadline/短leaseの一致。UI/env/helloを認可源にしない |
| 許可公開鍵 | 承認済みの最大8個のraw `ssh-ed25519` 公開鍵 | certificate/鍵options/追加認証源を認めず、runbackendのcanonical KeySetDigest候補と照合 |
| host signer | 保護された固定key referenceから読む既存Ed25519秘密鍵 | sourceのstrict readerは既存の非暗号化OpenSSH PEM Ed25519だけを扱い、別形式/未設定/暗号化鍵を拒否。生成fallback、秘密値のwire/log保存なし |

host key referenceは固定保護場所への参照であり、UIから渡す任意pathではない。
その配置・ACL・host公開鍵の確認先を実機評価前に承認する必要がある。
このPRは実鍵の作成・読取・記録をしていない。

配送は標準control/1と分けた`rescue-host-bootstrap/1`の一回限りの継承pipeである。
immutable grant snapshotと非秘密のConfig/公開鍵/host key referenceを上限付きで復号し、host秘密鍵そのものをcontrol/1へ載せない。
native childjobは標準3handleと専用server-read handleだけを明示継承する。
16KiB/depth8、固定順の非秘密locator/相関引数を使う。locator/nonceは認証ではなく、SCM RUNNING/PIDと保持peerのtoken/image等を別に照合する。
OVERLAPPEDの取消後は実completion前にbuffer/event/handleを解放せず、不明時は同じ所有参照を保持して回収を再試行する。
EOF後にもpipe client PIDを再照会できるかは実Windowsで未実証であり、失敗を可用性のために受理しない。
既存control/1へapprove/renewや未知fieldを追加する案ではない。
初回承認の認証済み入口も、既存Identityを前提にしたlocalcontrolのhello/snapshot/stopとは別に必要である。

legacy provisionerは借用するchild側pipe/tokenの元handleを所有し続ける。
native adapterへ渡すbase provisionerは、返したchild-side stdio複製の所有を移し、adapterが継承後または失敗時に閉じる。primary tokenは借用のままである。
専用bootstrap writerはserviceのみが所有し、書込完了後に全writer copyを閉じる。
親側control pipeはOwnedControlが閉じる。
stderrは以下の所有者が内容を保存せずに有限drainする。承認済み受理transportをRuntime.Serveへ渡す実入口は未設定のtransport factoryの責務として残る。
それらを実装済みと見せるfake childや常時成功callbackは製品sourceに追加しない。

## 保護stdioの明示構成

`NewNativeStdioProvisioning`は保護されたprofileと借用primary token、policyRef、poll/操作/cleanup予算、正の`StderrBytes`上限を必須とする。
wrapperとのprocess/service SID、service名、supervisor/childの固定path/hash、借用tokenの一致をnative呼出前に照合する。
pipe作成に使うこれらの識別値はconstructorで値として固定し、stdio側で未使用のACL/catalog mapは保持しない。
ACLや配置の検証はwrapperが保持する認証scopeの権威に残す。
このbaseを`NewNativeChildProvisioning`へ渡した場合だけ、同じservice認証scopeの再照合後に`childstdio.NewNative`で3本のbyte pipeを作る。
裸のbaseの`PrepareChild`は拒否し、検証済みを表すboolや任意callbackを公開しない。
生成前後の再照合、元hard deadline、grant由来のgeneration/full Identity、fresh channel nonce、固定artifactを保持する。
pipe生成または回収に失敗しても同じbaseは再利用しない。

親serverは非継承のOVERLAPPED、子clientは同期・非継承原本から作る継承複製である。
3本の子複製はchildjobへ借用し、bootstrapの第4handleと合わせた明示HANDLE_LISTだけで継承する。
子起動後のservice側複製はstdio所有者だけが閉じ、コピーしたraw handleとの二重closeを避ける。
部分生成でも所有者を返し、生成失敗時はstderr Readを開始せず回収だけを許す。

`OwnedStdio`はstderrを一定サイズのbufferへ読み、内容をlog、UI、errorへ保存・転送しない。
総byte上限を超える、Readが失敗する、0byteかつerrorなしのReadの場合は失敗として同じAuthorityを失効させる。
上限まで捨てて接続を成功扱いにする動作ではない。
別Authorityへの付替えとClose後の再利用も両方のgrantを失効させて拒否する。

正常回収は既存Go Closeとstdout実EOF、process exit0/Job空に加えて、stderr実EOFとdrain worker joinを待つ。
資源回収の`Close`だけで実EOFを代用しない。
stream Closeは取消と非blocking確認だけを行い、未完了なら同じPipesの参照を保持する。
有限deadline付きの`Pipes.Close(ctx)`で同じoperationを回収し、buffer/event/handleをcompletion前に解放しない。
このsourceは戻らないOS API、監督hang、OS sleep、実guest分離を解決済みとするものではない。

## 許可0でのSCM readyと残る依存

明示構成の`Service`と`RunReadySCM`は、sessionなしのnative準備とSCM実RUNNING確認、一つのissuer、全失効から同じ回収へ合流するsourceへ接続した。
詳細と回収未確認/fatal時の契約は[service lifecycle](service.md)を参照する。
既定`RunSCM`は引き続き未設定Hostで拒否し、製品gateは開放しない。
control/1とは別のlistenerのkernel準備までを扱い、operatorのserver認証と命令codec、実protected provisioning値の供給は未接続である。
現localcontrolのIdentity必須の入口や公開Openを転用せず、空Identityや仮grantを作らない。

## sourceで閉じられる作業と実機の別承認

専用codec、native reader/writer、固定key-reference reader、第4handleの生成/継承/closeはsourceで接続した。
保護stdioの生成とstderrの有限drainは、以下の明示構成sourceへ接続した。固定provisioning値の読取、正規operatorの初回承認入口と受理transportの供給は未設定である。許可0のSCM readyは明示構成sourceであり、既定入口は未設定を維持する。
既定mainのreceiverとSCMのHostはnilのままで、RUNNINGやSSH activeを自己申告しない。
Session/lease内部へ絶対hard deadlineを直接渡すinterfaceも後続の担当module変更で、現runtimeは元のdeadlineを保持し、相対時間の生成前後照合とcontextで保守的に拒否する。

実行前の別承認が必要なのは、対象Windows VM、実service/guest主体と権限、固定artifact/保護root、service登録/起動、ACL、既存key配置または不足時のkey作成、pipe作成、Go process/Job起動、SSH受理入口・port/relay/接続先である。
承認時は変更前のservice設定・ACL等の退避、作成する対象の一覧、停止/Job回収の確認、今回作った対象だけの削除と既存設定の復元手順を具体化する。
既存account/鍵/SSH設定の削除や置換を暗黙に含めない。
この変更では実行せず、秘密値も作成・記録しない。

## 検証の境界

Host/core、実Session/stdio、所有者側control clientはmemory IOで検証する。
childjobのnativeが使うparameterとOS seamの順序/失敗保持は純粋試験、WinAPI本体はWindows向けcompileで確認する。
実WinAPIでのchild起動、service/account/ACL/鍵/SSH/新listener、拒否されたP2専用/代替再現は実行しない。
通常CIの既存fixture成功をP2証明やOS保護証明にしない。
最後のJob handle、別guest主体の実保護、物理停止、監督hang/OS sleep、実relayとhost key信頼設定は別受入である。

## 許可0のservice dispatchの接続範囲

`dispatch.go`のprivate constructorは利用前の同じServiceへ一度だけ登録し、Serveが生成したissuerの実参照へ束縛する。
実ConfirmRunningとRecheck後だけhelloを受理する。controlled経路はlegacy ReadApprovedを呼ばず、許可0を維持する。
request/responseは既存rescue-host-service/1 codecを使い、同じboot/channel、hello後の連続sequence、有限request ID集合と単回read capabilityを照合する。
nonceやframe hashの一致自体は認証ではない。native read capabilityの生成とprotected profileの供給は未接続で、製品入口は変えない。

statusは同じownerの許可0観測だけを返し、bindingやapprovalを捏造しない。
prepare-intent/confirm-approvalは固定UNCONFIGUREDで拒否し、issuer.Prepareやlaunchへ到達しない。
quiesceは発行拒否・同じissuerの実Close成功からprivate receiptを作り、quiesce-acceptedだけを返す。
workloadCleanupConfirmedは常にfalseとし、workload-quiesced・SCM STOPPED・SSH停止成功を生成しない。
失効後の既存Service ownerによる回収は独立に進む。handlerからService.Closeを待たないため、自身のplane終了との循環を作らない。

channelは1本・inflight応答は1件・requestLimitは1..1024で、IDやread ticketをevictして再利用しない。
単一writeの実completion前は次要求を受けず、write失敗・取消・相関違反・quota終端でchannelを不可逆に閉じる。
すでに束縛したchannelの喪失時は同issuerを失効し、自動再送・再接続・承認復元をしない。
初回readの認証拒否から別のServiceや未束縛issuerを停止する権限は作らない。

このmemory-only縦断はnative認証方式、configDigest producer、同issuer承認予約、workload/planeの二段階回収receiptを採用・実装したものではない。
#100のinner/outer SCM cleanup proofはwire receiptへ転用しない。実Windows・OS設定・新鍵・実SSH・拒否済みP2専用試験は実行しない。

## 承認予約に先立つ所有順序のprivate境界

`reservation.go`は、将来の同Service予約をstopと同じmutexで順序付ける所有ticketだけを扱う。
intentをburnして同Service/issuerへ予約を保存してから受渡し、consumeとworker終了ackはそれぞれ単回である。
stopが先なら予約を拒否する。予約が先なら同issuerを失効してからlock外でworkerを取り消す。
ctx取消やquota/失敗でburnを戻さず、完了後も記録を再利用しない。

Serviceの回収は、未claimの将来配送をstop後に排除してabandonするか、claim済みworkerの明示終了ackを待つ。
予算切れなら参照とStopPendingを維持し、plane.Close・inner cleanup proof・Serve完了へ先に進まない。
fatal分類でnative回収の再試行を止める場合も、Serve完了前には未ack workerのmemory待機を残す。ack後のreturnはfatal分類と回収未確認を保持し、native処理を再開しない。これはprocess全体のfatal処置を採用・実装したものではない。
worker終了ackは、同じworker/配送ownerが全所有処理の終了と追加処理の不在を確認した後にだけ渡す内部契約である。
取消やContext.Doneだけから作らず、wireから送れるboolやcleanup成功receiptにも変換しない。

productionのintent作成口、認証済み主体、candidate/configDigest、policy、issuer.Prepare/launch、実Host workerへの配送・終了join adapterは未接続である。
private intentの同owner登録はmemory試験のfixtureだけが行い、正規承認を模擬して実行権限を作るものではない。
cutoff/配送budgetの実値と供給源、現在の認証・candidateの照合を行うconfirm producerは未決のまま、この所有ticketだけを承認済みと扱わない。
既存controlled経路のprepare/confirmはSERVICE_UNCONFIGUREDを維持する。
所有intent/予約がある間は、未実装の承認ID/binding/phaseをstatusへ合成せず、SERVICE_NOT_READYを返す。
実Hostの部分起動失敗/Job/IO回収をこのackへ結ぶ際は、Service.Closeから自身の終了を待つ循環を作らず、同ownerの実joinを別途検証する必要がある。
このlocal変更は実worker・OS保護・実SSHの成立を保証しない。
