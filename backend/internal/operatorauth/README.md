# 単回認証helperと別processの回収ownerの準備

このpackageは未接続のsource候補である。
`RunFixed`と`NewNativeController`は全OSで未設定拒否し、環境変数・argv・UI・任意path・自己申告SIDから解除しない。
正規launcher、最小token、継承plan、report transport、operator用server認証profileを供給するnative factoryは未実装である。
本packageを追加してもserviceのdispatch/予約/issuerを変更せず、製品gateを開かない。

## 同じthreadでのreadと復帰

`Helper.Run`は専用helper processの一度だけの要求scopeである。
private kernel seamを通して、固定threadのNoThreadToken、保持endpoint/clientの照合、sole read、同じmessageへのimpersonation、query-only token照会とclose、RevertToSelf、復帰後NoThreadTokenの順を強制する。
tokenのuser・authentication ID・session・integrity・type・level・restricted/AppContainerを保護profileの不変値と完全一致させる。
guestとoperatorのuserは別であり、service SIDを自動的にoperatorへ加えない。
frameはread直後に複製し、復帰確認後に既存service/1の純粋codecで検査する。
追加fieldや認証済みboolをwireへ加えず、成功時だけ同じframe bytesを単回publishする。
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
reportのread/drainをwaitより先に行い、実EOF、最大16KiB、単一の既存service/1 frame、保持processのwaitと正常exit、同じbindingの再検査、report I/O回収、process handle closeを別々に確認する。
PID再openやreport内のSID/hash/nonce/成功boolからnative provenanceを作らない。
結果の`Read`はprivate producerが生成する単回消費権で、zero値や外部constructorから生成できない。
取消・期限・Close後の消費はburnしたまま拒否する。
同Serviceのdispatch側には、同じ停止critical sectionでowner/intent/予約を再検査するadapterが別途必要である。

不正report・期限・helper異常等は同じCollectorを不可逆に失敗させる。
`Close`はcleanup予算の検査より先に同ownerの発行拒否/失効callbackを要求し、workload回収をhelper waitより先に試みる。
reportが進行中でも発行拒否・workload回収・helper終了要求を先へ進める。
terminate成功をwait成功にせず、I/O completion/join・process終了が不明なら同じ参照と失敗を保持する。
再確認は同じownerと有限contextだけで行い、新しいissuer/helperを作って復活させない。

このreport seamにはnative factoryがまだない。
匿名pipeはOVERLAPPED非対応であり、既存async adapterを転用しない。
匿名pipeなら専用同期readerの取消/EOF/join、専用OVERLAPPED named pipeなら保護profileと両endpointの所有/権限を別に確定する必要がある。
親側のwrite endと全複製のclose、helperへの限定継承、第三者への漏出/複製の不存在が満たされなければ実EOFと真正reportを主張できない。
16KiBのbuffer指定をpipe容量の保証やblock回避策とは扱わない。

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
- launcherが事前保持したprocess/creation/image/tokenと専用report readerを同じlaunchへbindし、固定request deadline・累積起動上限・stop中の部分launch回収を実装する。
- reportのnative I/Oとclose/cancel/実EOF/join、正常helper終了後の次request開始条件を確定する。fault後の同起動内復活は禁止する。
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
