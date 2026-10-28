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
Goの停止応答だけでcleanup予算を消費しないよう、残り予算の半分までをGo終了要求に使い、同じ元の期限内にJob回収へ進む。
Go CloseやJob回収が未確認なら正常成功としない。

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

現在の`ChildPlan.Bootstrap`とcommand側の`protectedBootstrap`は型付き接続点であり、Configや鍵を実際に別processへ配送する実装ではない。
以下を曖昧な「設定」へまとめず、実装・承認の対象として残す。

| 入力 | 生成者・信頼根 | 必須の照合 |
| --- | --- | --- |
| 今回の承認policy、絶対deadline、lease ceiling、予算 | kernel tokenでoperatorを認証した別の初回承認入口。guestは拒否。今回の人の操作が必要で、自動起動の保存値を承認にしない | 固定policyRefとraw Ed25519許可鍵集合を保護側で解決。初回承認へ既存Identityを要求しない |
| generation/full Identity | service内のfresh ephemeral issuer | 同じAuthority、単回challenge、known binding。新serviceは旧callback/旧grantを拒否 |
| artifact path/hashとservice/operator/guest SID | 承認済みの固定配布manifestとOS provisioning | 任意UI pathなし、guest分離、正規token、保護root/祖先/ACL、固定artifactと起動対象の一致 |
| channel nonceと所有pipe | serviceが今回の子用に新規発行・所有 | nonceは識別子で認証ではない。childjobの限定handle継承と同じ起動へ束縛 |
| grant snapshot、backend Config上限、WireContext、stdio予算 | 同じservice Authorityから導出した一回限りのbootstrap | generation/Identity/鍵digest/方針/絶対deadline/短leaseの一致。UI/env/helloを認可源にしない |
| 許可公開鍵 | 承認済みの最大8個のraw `ssh-ed25519` 公開鍵 | certificate/鍵options/追加認証源を認めず、runbackendのcanonical KeySetDigest候補と照合 |
| host signer | 保護された固定key referenceから読む既存Ed25519秘密鍵 | 最小候補は既存の非暗号化OpenSSH PEM Ed25519のみをparseし、別形式/未設定/暗号化鍵は拒否。生成fallback、秘密値のwire/log保存なし |

host key referenceは固定保護場所への参照であり、UIから渡す任意pathではない。
その配置・ACL・host公開鍵の確認先を実機評価前に承認する必要がある。
このPRは実鍵の作成・読取・記録をしていない。

配送の最小候補は、標準control/1と分けた一回限りの継承bootstrap pipeである。
immutable grant snapshotと非秘密のConfig/公開鍵/host key referenceを上限付きで復号し、host秘密鍵そのものをcontrol/1へ載せない。
現childjobは標準3handleだけを継承する。
専用bootstrap handleを追加する場合のhandle識別方法、単回codecの版・field・長さ/深さ上限、鍵referenceの解決規則は共有契約ownerの確認前で、追加していない。
既存control/1へapprove/renewや未知fieldを追加する案ではない。
初回承認の認証済み入口も、既存Identityを前提にしたlocalcontrolのhello/snapshot/stopとは別に必要である。

provisionerは借用するchild側pipe/tokenの元handleを所有し続け、起動完了/取消時に不要な親側複製を閉じる責務を持つ。
親側control pipeはOwnedControlが閉じる。
stderrの有限drain/診断の選別と、承認済み受理transportをRuntime.Serveへ渡す実入口も、未設定のbootstrap/transport factoryの責務として残る。
それらを実装済みと見せるfake childや常時成功callbackは製品sourceに追加しない。

## sourceで閉じられる作業と実機の別承認

共有契約が決まれば、専用bootstrap codec/reader/writer、固定key-referenceのstrict reader、追加handleのnative parameter、初回承認入口への接続はsourceとmemory fixtureで準備できる。
現在の型付き接続点だけで、保護bootstrapが完成したとは扱わない。
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
