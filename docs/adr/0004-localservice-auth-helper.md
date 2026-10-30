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

2026-10-30に保証範囲を明文化した。対象は、非管理者の遠隔guestによる認証の偽造、監督・停止処理の乗っ取りを防ぐことである。
host操作ユーザー・管理者の既侵害に加え、既に侵害された別のWindows serviceからの完全な隔離は保証対象に含めない。
これは専用service SID、固定配置、object owner/DACL、実message認証、期限・失効・回収の検査を省く理由にはならない。
同じLocalServiceとして動く別serviceの存在だけで認証を許さず、専用SID・保持process世代・実tokenの一致を要求する。

SCMのservice object保護と、生成されるprocess/token objectの保護は別である。
MicrosoftのMS09-012はServices.exeが起動するLocalService/NetworkServiceのtoken kidnapping対策として、service SIDの有効化、再起動後の反映確認と機能試験を案内している。
一方、Advisory 2264072はservice isolationを多層防御と位置付け、独立したsecurity boundaryとして扱わない。
この公式契約とguestの非管理者境界を設計根拠とし、後からDACLを2回観測しただけで、生成直後からあらゆる主体の危険なhandle取得がなかったと証明する条件は課さない。
この整理で実Windowsの設定適合・実token・停止回収を検証済みに変更することもない。

processの照会権とtokenの複製・偽装権は区別する。
OpenProcessTokenはprocessの照会権に加え、要求したtoken権限をtoken object自身のDACLで検査する。
照会権を含むprocess方針を受け入れるには、同じ保持processのtoken objectについて、guestや未知主体に複製・偽装・primary割当・変更権限が付与されていないことを別に確認する。
tokenのDefaultDACLをtoken objectのDACLの代用にせず、processの停止・VM・handle複製・DACL変更権限も緩めない。

正規supplier、token借用の全寿命、固定配置、子側の同launch証拠は引き続き未完成または未接続である。
Serviceの有限な要求受付callerは明示runtime構成へ接続するが、helper未構成時は取得前に拒否し、実Windowsの認証・受付成功は未検証である。
今回のSID分離と現在状態の検査だけで、これらを信頼済み・構成完了へ昇格させない。
公開factoryの未構成拒否と製品gate閉鎖を維持し、source/fake試験やWindows compileを実機保護・実SSHの成功とは扱わない。

## 一次資料

- [LocalService account](https://learn.microsoft.com/en-us/windows/win32/services/localservice-account)：共有の定義済みアカウント。
- [Service SID](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/ns-winsvc-service_sid_info)：tokenへのservice SID追加と制限SIDの扱い。
- [Service security](https://learn.microsoft.com/en-us/windows/win32/services/service-security-and-access-rights)：SCMのservice objectに対する権限。
- [StartService](https://learn.microsoft.com/en-us/windows/win32/api/winsvc/nf-winsvc-startservicew)：起動要求はprocess/tokenの初期security descriptorを受け取らない。
- [RevertToSelf](https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-reverttoself)：復帰失敗時にprocessを終了する指針と、監督の回収責任を別processへ残す理由。
- [MS09-012のServices.exe向け説明](https://support.microsoft.com/en-us/servicing/os/windows/2009/04/ms09-012-description-of-the-security-update-for-windows-service-isolation-april-2009)：service SID設定と起動後の確認。現行OSの設定適合を旧版KBの有無から推測しない。
- [Advisory 2264072](https://learn.microsoft.com/en-us/security-updates/securityadvisories/2010/2264072)：service isolationの保証範囲。
- [OpenProcessToken](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-openprocesstoken) / [token objectの権限](https://learn.microsoft.com/en-us/windows/win32/secauthz/access-rights-for-access-token-objects)：process照会権とtoken objectへのアクセス検査。
