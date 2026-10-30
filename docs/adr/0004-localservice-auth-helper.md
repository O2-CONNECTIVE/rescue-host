---
status: accepted
---

# 利用中の認証helperをLocalServiceへ分離する

2026-10-30、利用中だけLocalServiceの認証用serviceを置く設計を採用した。
[LocalSystem監督service](0003-owned-guest-workload.md)は承認・失効・guest停止・回収を所有し、認証helperは要求の読取りとmessage tokenの検査を担当する。
新しいWindowsユーザーやpasswordの管理を避ける一方、専用serviceの配置、起動世代、停止と撤去の管理が増える。
これは設計とsource実装の採用であり、実機のservice・account・ACL・privilege・資格情報の変更や製品gate解除の許可ではない。

## 主体と権限の宛先

LocalServiceのTokenUserだけで製品のhelperとは認証しない。
共有ユーザーSIDと製品専用service SIDを分け、固定設定、保持process世代、実token、object owner/DACLを照合する。
report書込み権の宛先を共有LocalServiceへ広げず、専用service SIDへ限定する。
source tokenと子tokenの専用SIDが欠落、無効、deny-only、または変化した場合は拒否する。
SIDの追加設定だけで実tokenへの反映や包括的な隔離が完成したとは扱わない。
既存の非共有helperユーザーの未接続fixture契約は残すが、LocalServiceで専用SIDが欠けた場合に旧経路へfallbackしない。LocalSystemやNetworkServiceで代用もしない。

## 供給元と所有責任

現在の単回helper launcherと、正規tokenを供給するserviceは別の役割である。
元Executionの保護設定から期待値を得て、同Service・issuer・要求へ束縛する。
候補process、message、argv、環境変数から不足する信頼値を補わない。
取得前にownerを保持し、取消・部分取得・失敗後も参照を失わない。
新規借用を閉じてから実取得処理のreturn、helper終了、Job/I/O回収を確認し、供給tokenと同じ起動世代のserviceを回収する。
supplier停止やtoken handle閉鎖だけで、既存子processの終了や複製済みtokenの失効を認定しない。
停止要求、実停止、handle回収、登録消失を分ける。

## 未成立の境界

SCMのservice object保護と、生成されるprocess/token objectの保護は別である。
生成から最初の検査までの間に、共有LocalService主体が危険なhandleを取得できなかったことは、後から安全なDACLを観測しただけでは証明できない。
初期生成時の保護を含む正規supplier、token借用の全寿命、固定配置、子側の同launch証拠は引き続き未完成または未接続である。
Serviceの有限な要求受付callerは明示runtime構成へ接続するが、helper未構成時は取得前に拒否し、実Windowsの認証・受付成功は未検証である。
今回のSID分離と現在状態の検査だけで、これらを信頼済み・構成完了へ昇格させない。
公開factoryの未構成拒否と製品gate閉鎖を維持し、source/fake試験やWindows compileを実機保護・実SSHの成功とは扱わない。

## 一次資料

- [LocalService account](https://learn.microsoft.com/en-us/windows/win32/services/localservice-account)：共有の定義済みアカウント。
- [Service SID](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/ns-winsvc-service_sid_info)：tokenへのservice SID追加と制限SIDの扱い。
- [Service security](https://learn.microsoft.com/en-us/windows/win32/services/service-security-and-access-rights)：SCMのservice objectに対する権限。
- [StartService](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/nf-winsvc-startservicew)：起動要求はprocess/tokenの初期security descriptorを受け取らない。
- [RevertToSelf](https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-reverttoself)：復帰失敗時にprocessを終了する指針と、監督の回収責任を別processへ残す理由。
