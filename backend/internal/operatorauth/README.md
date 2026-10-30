# 単回認証helperと別processの回収ownerの準備

このpackageは未接続のsource候補である。
`RunFixed`と`NewNativeController`は全OSで未設定拒否し、環境変数・argv・UI・任意path・自己申告SIDから解除しない。
privateなWindows launcher/report I/Oのsource候補を含むが、最小token・保護profile・sole-read移譲・operator用server認証の正規供給元は未接続である。
本packageを追加してもserviceのdispatch/予約/issuerを変更せず、製品gateを開かない。

## 同じthreadでのreadと復帰

`Helper.Run`は専用helper processの一度だけの要求scopeである。
private kernel seamを通して、固定threadのNoThreadToken、保持endpoint/clientの照合、sole read、同じmessageへのimpersonation、query-only token照会とclose、RevertToSelf、復帰後NoThreadTokenの順を強制する。
互換profile/1はtokenのuser・authentication ID・session・integrity・type・level・restricted/AppContainerを事前profileと完全一致させる。
採用した[方式B](../../../docs/adr/0002-operator-sid-policy.md)のprofile/2は固定OperatorSIDと実message tokenの制約を検査し、実AuthId/session/integrity等は認証後のbindingへ保持する。
guestとoperatorのuserは別であり、service SIDを自動的にoperatorへ加えない。
frameはread直後に複製し、復帰確認後に既存service/1の純粋codecで検査する。
公開service/1 frameへ追加fieldや認証済みboolは加えない。v1は元frame bytes、Bは復帰後の実主体と元frameを結合したreport/2を成功時だけ単回publishする。
認証結果は今回の人の同意、grant、停止receiptではない。

query/close失敗やpanicでも、impersonation成功後のrevertを省略しない。
復帰・token解放・thread主体の不明ではreportを作らず、helper processの終了primitiveへ進める。
Windows primitiveは`os.Exit(1)`であり、同processのcleanupを待たない。
Supervisorにはこのprimitiveを接続しない。
失敗process自身が回収済みと報告する設計ではなく、別processの健康なownerが保持handleから終了waitを確認する必要がある。

Windowsのtoken queryと保持processのterminate/wait/closeは実APIを呼ぶsourceを含む。
TokenIsRestricted予約値の代わりにTokenHasRestrictionsを照会する。
TokenIsAppContainer=0をIdentification以下のcallerから認可根拠へ使えないという公式制約を拒否として保持する。
合成testのIdentification tokenが実Windowsで成功するとは主張せず、必要privilegeを付与したりlevelを黙って変更したりしない。

## reportと健康なowner

`Collector.Collect`のprivate seamは、launch前から保持した専用report reader・実helper process・同じ固定artifact/主体とのbindingを前提とする。
reportのread/drainをwaitより先に行い、実EOF、保持processのwaitと正常exit、同じbindingの再検査、report I/O回収、process handle closeを別々に確認する。
v1 raw reportは最大16KiBの公開frame。B report/2は内部32KiB以内で、内包する元の公開frameは16KiB以内を維持する。
PID再openやreport内のSID/hash/nonce/成功boolからnative provenanceを作らない。
結果の`Read`はprivate producerが生成する単回消費権で、zero値や外部constructorから生成できない。
取消・期限・Close後の消費はburnしたまま拒否する。
同Serviceのdispatch側には、同じ停止critical sectionでowner/intent/予約を再検査するadapterが別途必要である。

不正report・期限・helper異常等は同じCollectorを不可逆に失敗させる。
`Close`はcleanup予算の検査より先に同ownerの発行拒否/失効callbackを要求し、workload回収をhelper waitより先に試みる。
reportが進行中でも発行拒否・workload回収・helper終了要求を先へ進める。
terminate成功をwait成功にせず、I/O completion/join・process終了が不明なら同じ参照と失敗を保持する。
再確認は同じownerと有限contextだけで行い、新しいissuer/helperを作って復活させない。

privateなreport factoryは、暗号学的乱数名の専用message-mode OVERLAPPED named pipeを作るsourceを含む。名前は識別子であり認証ではない。
匿名pipeはOVERLAPPED非対応なので転用しない。
合成試験はReadFile/WriteFile/CancelIoEx/completion/CloseHandle境界を置換し、実Windowsの成功を証明しない。
親側のwrite endと全複製のclose、helperへの限定継承、第三者への漏出/複製の不存在が満たされなければ実EOFと真正reportを主張できない。
内部report用32KiBのbuffer指定をpipe容量の保証やblock回避策とは扱わない。

## native launcherと単回batch

`startNativeHelper`は固定artifactを保持してhash/最終pathを検査し、`CreateProcessAsUserW`でsuspended childを作り、専用Jobへ割り当ててからresumeするsourceである。
継承はoperator reader・report writer・profile readerの3handleだけで、親側の子向けreaderと不要なwriter/read複製を閉じてからresumeする。profile送信はresumeと保持binding再検査の後に開始する。
同sessionの保有primary token・process/threadの明示SDDLが必須で、LocalSystemへのfallback・logon・privilege付与はしない。
OS APIが呼出し中に既存privilegeを一時enableする可能性は、変更なしの保証へ読み替えない。
保持processのcreation/image/tokenとsource tokenの初回観測を固定し、後の変化を拒否する。初回観測以前の変更がないことや発行主体の真正性は証明しない。
Jobのkill-on-closeとactive process上限1は寿命制御であり、privilege隔離やhandleの第三者複製防止ではない。
leafの固定hashはancestor directoryのACL/保護を証明しない。

`prepareNativeBatch`は保護Factoryと借用primary tokenを保持し、Service登録後の`Start`で認証worker自身が`ServiceScope`を取得するprivate接続である。
取得・再検査・回収は同じ固定workerで行い、SCM側のscopeを移送しない。部分取得と回収の契約は[batch-scope.md](batch-scope.md)に記す。
候補queryと固定policyを分離するprivate providerのsourceは[profile-provider.md](profile-provider.md)に記す。正規policyとendpoint lease発行元は未接続であり、query成功から認証を作らない。
service自己認証をoperator用server認証には流用しない。
sole readerの所有移譲が事前条件で、service側に別のread/duplex複製が残る場合は呼び出せない。
reportを実EOFまでdrainし、保持helperの終了waitとbinding再照合の後、I/OとJob/process/imageの回収を有限context内で確認する。
取消や回収失敗は同じCollectorの失効/回収へ進み、未確認参照を保持する。
context-freeな終端確認は、既に有限回収が完了した記録だけを読み、新しいOS callを開始しない。

privateなprofile receiverのrunは、保持source確認・frame・実EOF・同source再確認後に既存Helper.Runへ接続する。CreatePipe/sender/receiver/batchのsourceと接続したメモリfixtureを含む。実helper executable入口の保護source供給、最小token/実SID/保護SDDLのproducer、service-owned endpointのsole-read移譲adapter、起動の累積上限、controller server認証producer、同Service adapterは未接続である。
`messageEndpoint`は継承handleの権限/種別確認と単回read/write/closeまでを扱い、handle番号だけからprovenanceを作らない。
これらの未設定値をUI・argv・環境変数・自己申告SIDで埋めない。

## controller側

`Controller.Exchange`のprivate seamは一つの要求scopeだけを扱い、保持serverの認証前にpayloadをwriteしない。
同じpeerを各I/Oの前後に再照合し、既存codecのrequest ID/sequence/command/boot/channel、helloのclient nonceとの一致を要求する。
応答bytesも同ownerのgrant/物理停止receiptとして発行しない。
close不明ではpeer参照を保持し、同じControllerの有限Closeで再確認する。
channel全体の連続会話、serverのnative認証producer、Rust/UIへの接続は未実装である。
service自身用の`bootstrapauth.VerifyService`をoperator側へ流用しない。
SCM RUNNING PIDとservice endpoint creator、実際のhelper readerを同じPIDと仮定しない。

## 次の接続条件

- protected provisioningからhelper artifact/hash・実主体/token/最小権利・controller主体と固定server profileを供給する。現在は未設定。
- service-owned endpointのread権限をhelperだけに限定し、Supervisor側writerへreadを許さない。native上のcreator/移送後の観測を実証する。
- launcherの保持process/creation/image/tokenと専用report readerを同じlaunchへbindするsource候補は追加済み。累積起動上限と同Service stopへの接続は未実装。
- reportのnative I/Oとclose/cancel/実EOF/joinのsource/API seam試験を含む。正常helper終了後の次request開始条件と実Windows検証は別途必要。fault後の同起動内復活は禁止する。
- 統合ownerが同Serviceのverifier/予約/二段階stopへadapterを接続する。共有control/1・service/1・bootstrap/1とCI inventoryはこのpackageから変更しない。

具体的なSID/ACL/account/service登録・鍵参照と利用・実SSH/relay・配布配置を使うときは、対象・影響・退避/復元・所有資源の停止確認を示した別の実機承認が必要である。
実機での権限不足をLocalSystem/別ログオン/privilege追加で回避しない。
初期保証はremote非admin guestからの監督/停止保護で、host operator/admin既侵害とadmin guestは対象外。
別processというだけで権限隔離を証明しない。

## 検証境界と一次資料

通常unit/raceはprivate seamの合成状態とメモリだけを使い、helper process・実pipe/token・新listener・鍵・SSHを実行しない。
Windows native sourceはcompile対象であり、実APIの成功・process終了の実観測とは区別する。
元の受入計画のnative条件、既存44条件、P2専用未実証、監督hang/OS sleep/最後のJob handleを解消したとは扱わない。

- [ImpersonateNamedPipeClient](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-impersonatenamedpipeclient): 最後にreadしたmessageとcalling threadのcontext。
- [RevertToSelf](https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-reverttoself): 失敗後のprocess終了指針。
- [OpenThreadToken](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-openthreadtoken)、[Token information class](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ne-winnt-token_information_class): queryとIdentification/AppContainerの制約。
- [TerminateProcess](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-terminateprocess)、[GetExitCodeProcess](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-getexitcodeprocess): 終了要求・wait・exit codeの区別。
- [Anonymous pipe operations](https://learn.microsoft.com/en-us/windows/win32/ipc/anonymous-pipe-operations): OVERLAPPED非対応とread/writeのblock。

今回のlauncher/report試験のRed→Greenには、部分取得・取消時の参照保持・期限切れ後のallocation拒否・cleanup期限・lock競合・handle alias拒否を含む。
初期の未定義symbolによるcompile-red、実装後に追加したbatch順序の回帰試験、振る舞いを先に失敗させたTDDは区別して記録する。
Windows compileはnative APIの型/リンク境界だけを確認し、13組のnative受入試験を実行済みにしない。

`cmd/rescue-auth-helper` の専用入口は `RunFixed` へ接続済みだが、固定保護sourceは未登録で既定拒否する。入口と所有回収の規約は [helper-entry.md](helper-entry.md) を参照する。

方式Bの[具体入力の単回移譲](sole-input-batch.md#方式bの入力移譲)は、固定SID profileから既存Batchの認証・消費・回収へ接続するprivate sourceである。
正例はOS境界を置き換えたmemory試験で、正規helper資源producer・同launchの子側証拠・Service要求loopの製品接続は未完了である。公開factoryは取得前の拒否を維持する。
