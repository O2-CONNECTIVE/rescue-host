# 固定配置から無効guest準備への内部adapter

この差分は、guest側の固定期待値を取得・保持し、既存journal、creator、限定inspector、Lifecycleへ接続するsource候補です。
任意の設定値を正規manifestと認定するproducerではありません。
公開NewNative、SSH有効化、実行tokenの取得は閉鎖したままです。
初版の対象は隔離Windows一台・固定構成・手動provisioningであり、installerや常駐版は追加しません。

## 接続した区間

1. 固定profileにinstallation ID、machine/service/operator、全対象path・volume/file ID・owner・SDDL、journal root、PowerShellと依存moduleのhashを要求します。
   全祖先をdrive rootまで列挙し、未設定・重複・別対象・上限超過を拒否します。
   現在観測した値を期待値へ補完しません。
2. 元service threadで既存PreparedServiceの同じprofileを照合し、限定PreparedLeaseを保持します。
   lease内の検証は既存image/主体検証を使い、同gateへ再入しません。
   親のCloseは借用終了まで回収ackを返せません。
3. 既存ServiceWitnessで同じservice/processとSCM RUNNINGを照合します。
   service/processの読取handleと、ACL検査専用のTOKEN_QUERY|READ_CONTROL handleを取得します。
   子起動用の既存LaunchTokenは別に借用し、Recheck、同じtoken識別値と非継承を確認します。
   Service tokenをguest実行tokenに代用しません。
4. 固定fileと祖先を保持し、同handleのNTFS・volume/file ID・owner/DACLを照合します。
   ledgerは既存journalがshare=0で一度だけ開き、inspectorは同じj.fileを借ります。
   未完了の旧journalなら新account sourceを構成せず、旧状態を手動照合へ残します。
5. 既存creatorと限定inspectorを同じsourceへ接続し、journal意図→作成SID→静的検査→prepared-disabledへ進みます。
   sessionの正規revoke/quiesce sourceがなければ、取得・journal更新・account作成より前に拒否します。
6. 終了時は既存coreの失効・回収・SID清掃の終端を確認してからaccount source、journal、保持対象、SCM witness、PreparedLeaseを回収します。
   再試行可能な清掃失敗だけでaccount sourceを永久停止しません。
   core終端、account sourceのclosingとClose ackを別段階で保持し、後段の回収失敗では新queryを始めず残りのClose/joinだけを再試行します。
   作成前preflightと作成後inspectorのunknown queryでは新native照会を再開せず、borrowed対象とscopeを保持します。
   実return前の取消、停止要求、Close呼出し自体を回収ackとしません。

既存control pipeは、新しいpipeを作らず、同じService endpoint ownerの検査leaseを必須とします。
要求単位の照合専用複製を同じendpointから発行し、実query returnと再照合後に返却します。
長期sessionの保持対象へ要求pipeを残さず、raw endpointをadapterが閉じることもありません。
[固定sourceと要求単位lease](guest-fixed-source.md)がproducer・短期返却・full入口の現状を扱います。

未起動専用の明示入口は、同Endpointの検査leaseと元Execution ownerを実`newNativeStaging`へ接続する。
元要求の取消returnとSDKの実returnを区別し、内側stagerの回収ackまで部分参照を保持する。
全workload/auth joinと外側Root/tokenの解放はその後であり、内側Closeから全体joinを待たない。
このsource配線の正例は各packageのprivate OS/owner memory seamで検証し、同一製品callerによる実Windows認証・account操作の正例とは区別する。

## 未実装・未接続・未検証

| 分類 | 残件 |
| --- | --- |
| 未実装 | 内側sshdが実際に作るguest token/shellのresume前gateと実作業停止owner |
| 未接続 | 製品の正規operator認証policy source、実runtime証跡からのgrant/起動、動的SIDを使うkey/helper/sshd構成 |
| 未検証 | 実Windowsの配置・SID・ACL・API権限・依存解決、実account操作、Job最後handleと実停止、実SSHと再接続拒否 |

現Recordは固定GuestSIDを作成前から要求します。
この値を仮のSIDで埋めたり、今回得たSIDへ後書換して検証を通したりしません。
Service/installationの静的基盤と、creatorで得たSIDに結合するruntime profileの段階分離が統合側の残件です。

childjob.Startは割当後に内部でresumeするため、Start返却後の検査では実行前gateになりません。
将来のgateには正規guest token、同じsuspended child/Job、限定継承handleの照合と、停止に競合しないresume commitが必要です。
今回のPreparedは無効accountの静的検査であり、この実行許可を発行しません。

query unknownでadapterが保留する場合も、canonical authorityの失効は独立したService ownerが先行して行う必要があります。
adapterのローカルstopはその失効完了証拠ではありません。
OS APIが戻らない場合、監督hangやOS sleepをこのsource/fake試験で解決したとはしません。

## 実機操作が必要になる対象

以下は今回実行していません。
対象VM、変更前値、復元先を指定した別のaction-time承認が必要です。

| 対象 | 必要な確認・変更候補 | 復元・清掃の単位 |
| --- | --- | --- |
| 配置 | 固定service/Go/PowerShell/module/key/journalと全祖先の配置、digest・file ID・ACL | 事前snapshot/backupへ戻す。別installationのfileを削除しない |
| 実行主体 | service identityとper-service SID、SAM作成/停止/削除とtoken起動に必要な権限 | 登録前設定へ戻す。LocalSystemへ自動fallbackしない |
| 保護 | service object、process/thread/token、root、key、journal、control pipeのACL | 対象ごとの保存済みdescriptorへ戻す |
| account | 一回のintentに登録した一時非admin guestの作成・必要権限・無効化/削除 | 接続失効・work回収を先に確認し、同一SIDの所有対象だけ清掃 |
| 実接続 | guest token、key配置、sshd/固定転送先と接続、切断・期限・再接続拒否 | listener/workload停止、確認済み対象のkey/account設定を清掃 |

この表は実行許可や完成証拠ではありません。
実際のservice identityと必要権限は対象VMで確認してから確定します。

## 参照API

- [CreateFileW](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew)の既知未取得sentinelと共有mode
- [OpenProcessToken](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-openprocesstoken)の要求権限とClose責任
- [OpenServiceW](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/nf-winsvc-openservicew)のservice名と読取権限
- [GetVolumeInformationByHandleW](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-getvolumeinformationbyhandlew)の同handleによるfilesystem検査
