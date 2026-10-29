# 一時guestの固定配置sourceと要求単位の検査借用

初版の隔離Windows一台・固定配置・手動provisioning向けのsource候補である。
別の非管理者guestから監督・停止処理を守る脅威範囲を維持する。
実Windowsの保護成立、製品gate、実SSH接続を検証済みにしない。

## 固定配置からの実入口

既存service recordの固定Anchorと同じdirectoryに、`guest-provisioning-v1.json`と独立した`guest-provisioning-v1.sha256`を要求する。
配置先をUI、環境変数、argv、実行場所から上書きしない。
新しいwire commandや承認操作を追加する方式ではない。
既存Recordへguest account作成前の仮SIDを入れる方式でもない。

`Prepared.LoadFixedGuest`は既存のnative Rootを使い、全祖先・同主体・保持file・独立digest・有限読取・実EOFを照合する。
guest側とservice側のservice名/user/service SID/operator SIDを照合し、実際の両record/digestのfile ID、service/Go imageのrole、既存host key参照の包含を要求する。
返却する`GuestConfiguration`はこの取得経路でだけ生成し、部分取得も元ownerが保持・回収する。
Layout/Digest取得時にも、元threadの主体と両Rootの保持metadataを再検査する。
decoder単独の成功、SHA-256一致、現在観測したSIDだけから配置元の正当性を生成しない。

従来のfull execution入口では、この固定sourceの読取・保持を強token取得より前に接続した。
未配置・不一致なら強取得やSCMへ進まない。
query-only preview入口は変更せず、承認、grant、guest有効化を発行しない。
固定sourceの読取成功を、正規承認からPlan/sessionを発行したことへ読み替えない。
ConfirmApproval、同issuer grant、動的guest runtime profileへの接続は統合側の別担当である。

## 非秘密file形式

形式は`rescue-guest-provisioning/1`の単一UTF-8 object、最大256KiB、深さ16である。
未知・欠落・重複key、大小文字違い、不正Unicode、null、余剰入力を拒否する。
正規10進uint64文字列、小文字hex、固定Windows絶対pathを要求する。
正確なfield集合は`fixed_layout_wire.go`とstrict decoderが所有する。

| field | 内容 |
| --- | --- |
| protocol / installation | 固定版と32byteのinstallation識別値 |
| machineSID / serviceUserSID / serviceSID / operatorSID / serviceName | 期待主体。実測値による自動補完はしない |
| journalRoot | 既存journalの固定親directory |
| powershell | image・LocalAccounts manifest・assemblyのpathとSHA-256 |
| files | path・role・directory・volume・fileID・ownerSID・securitySDDLの有限集合 |
| policy | 初版の既知network group、限定privilege、logon rightの許可集合 |

filesには両record/digest、service/Go/PowerShell/module、host key、journal、drive rootまでの全祖先を含める。
秘密値、password、秘密鍵bytes、token/handle、session epoch、grant、保存済み承認、deadlineは含めない。
policyはaccountの最大許可集合であり、実tokenのgroupやprivilegeを取得した証拠ではない。
recordとdigestを保護し同時に正規配置できるのは手動provisioning側であり、このsourceは生成・配置・ACL変更を行わない。
更新途中の不一致は拒否し、host operator/adminの改変や巻戻しへの保証は追加しない。

## 同endpointの要求単位lease

既存`Endpoint.NewInspectionLease`は、接続済みの同じ物理endpointから`0x120080`だけを複製する。
FILE_READ_DATA、FILE_WRITE_DATA、FILE_CREATE_PIPE_INSTANCEを含めず、reader移譲や認証証拠を発行しない。
同じ不変profile、保持processのPID/creation、現在主体/token識別値、image、pipe server PID、実access・flags・owner/DACLを発行前後に照合する。
raw番号やpayloadからleaseを構成する公開constructorはない。
Split/Move後も発行worker専用scopeへ再入せず、callerの固定threadで実照会する。

guest staging側のnative設定はこのopaque leaseと同じprofileだけを受け入れる。
最初の短期検査で同handleのmetadata/DACLを既存inspectorで読み、SDKが実returnして同じlease/主体を再確認した後にlogical leaseを返す。
長期sessionのpermission profileにはservice/process/token/filesだけを残し、要求pipeを残さない。
これにより要求応答・EndpointClosed・次のStopを、セッション終了までの借用で塞がない。
後続要求ごとの実endpoint検査は、同じ固定listener policyとService側の要求経路で別途行う必要がある。
明示的な`NewNativeStagingExecution`では、同Endpointのleaseを元Execution ownerへ渡し、未起動専用のPlan/sessionと実stager factoryへ接続した。
この経路は固定Recordとguest設定の結合digestをpreviewへ固定し、実作成receiptからSIDを単回束縛する。
未起動専用sessionは同Service・owner・namespaceと失効済みの未発行issuerを照合し、実行許可やruntime分離の証拠を返さない。
正規operator認証policyの供給は未構成のため、製品入口は取得前拒否のままである。

Endpointは部分leaseを含めて所有し、Closeは全leaseの回収確認前に完了を報告しない。
取消だけなら実return後に健康な同主体で有限回収を試す。
主体不明、panic、fatal観測ではwork/pin/refを保持し、状態が戻っても新native照会や解放を再開しない。
inline OS APIが戻らない場合、監督hang、sleep、強制終了の実証をこの試験で置き換えない。

## 実guest tokenとresume前gateの所在

Go backendは保護service側のtransportであり、保持host keyを使い、転送先を固定`127.0.0.1:22`へ限定する。
Go childをguest tokenで起動する変更は、実作業tokenの検査の代わりにならない。
実作業tokenとshellを作るのは内側のWindows sshdであり、現repoにはその起動・停止所有経路がない。

調査した上流Windows OpenSSHは、local userにS4U/Network logonを使う経路と、shellをspawnしてからJobへ割り当てる経路を持つ。
これはrepoのchildjobが提供するsuspended→Job割当→resumeの経路とは異なる。
別のS4U tokenを先に作って検査しても、実sshdが作るtoken・shellの同一性を証明できない。
Service tokenや任意のempty session sourceを代用してgateを開かない。

残る具体判断は、初版で使う固定sshd版・設定と、実sshd token/shellへ検査・失効・停止所有を接続する方法である。
専用sshdを保持する案も含め、必要な権限、実作業と子孫の所有、再接続拒否を先に確定する必要がある。
この差分ではsshdを追加起動せず、token/資格情報生成やprivilege fallbackも行わない。

一次資料は2026-10-29に確認した。
上流branchの観察は固定配布版の動作保証ではなく、実機評価には採用commit/版の固定が必要である。

- [Windows OpenSSH token実装](https://github.com/PowerShell/openssh-portable/blob/latestw_all/contrib/win32/win32compat/win32_usertoken_utils.c)
- [Windows OpenSSH shell/Job実装](https://github.com/PowerShell/openssh-portable/blob/latestw_all/contrib/win32/win32compat/w32-doexec.c)
- [LsaRegisterLogonProcessの要求権限](https://learn.microsoft.com/en-us/windows/win32/api/ntsecapi/nf-ntsecapi-lsaregisterlogonprocess)
- [CreateProcessと限定handle継承](https://learn.microsoft.com/en-us/windows/win32/procthread/creating-processes)

## 実機前の残件

| 分類 | 残件 |
| --- | --- |
| 未実装 | 固定sshdの実token/shell gateと実作業・子孫の停止owner |
| 未接続 | 製品の正規operator認証policy producer、実runtime証跡からのgrant/起動、動的SIDのkey/sshd設定 |
| 未検証 | 固定配置・SID・ACL・API権限・依存解決、実account操作、Job最後handleと実停止、実SSH・切断/期限・再接続拒否 |

実機承認の対象は固定file/digestと全祖先の配置・保護、service identityとper-service SID、service/process/token/pipe/key/journal ACL、一時guestとsshd/key構成である。
対象VMと変更前snapshot/descriptorを保存し、接続失効・実work回収後に同installation/同SIDの所有対象だけを清掃して戻す計画が必要である。
この一覧は実行許可ではない。
source/fake、race、Windows compile、通常CIは、これら実保護や禁止済みP2専用試験の成功証拠ではない。
