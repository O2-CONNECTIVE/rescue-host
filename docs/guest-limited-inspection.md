# 初版一時guestの限定権限検査

この検査は、新規作成して無効のまま保持するローカルguestの準備段階を対象とします。
公開の製品入口、アカウント有効化、SSH接続は閉鎖したままです。
将来の実行token、継承handle、Job、runtimeの保護対象を検証済みとはしません。

## 支持する初版構成

固定provisioningが指定した、ドメイン非参加・非DCのWindowsだけを支持します。
USER_INFO_4の同じbufferで作成済みSID、通常USER、無効状態、primary RID 513、USER_PRIV_USER、auth_flags 0を観測します。
機械アカウント、信頼・委譲flag、未知flag、別primary、ドメイン参加、観測失敗を拒否します。
guestの作成来歴は既存creator/journalの責務であり、名前照会を作成証拠に昇格しません。

固定されたWindows OpenSSH公開鍵S4UのNetwork logon投影だけを扱います。
guest SID、観測済みprimary、Everyone、LOCAL、Authenticated Users、Local Account、This Organization、Networkから全ローカルaliasの閉包を求めます。
到達可能なaliasはUsersとprimaryに限り、Administratorsその他の未知aliasへの到達を拒否します。
無関係な他ユーザー・グループの存在だけでは拒否しません。
この投影は実tokenを列挙したものではなく、採用するsshdのartifactとlogon動作の確認は後続です。

閉包中の全SIDに対しLSA割当を取得します。
許容可能なprivilege上限はSeChangeNotifyPrivilege、SeIncreaseWorkingSetPrivilege、SeTimeZonePrivilegeです。
logon rightsも固定Planの明示一覧と照合し、未知の権限や取得失敗を空一覧に変換しません。
SeShutdownPrivilege等の既存禁止は緩めません。
既定のWindows clientでUsersにその権限があれば拒否しますが、既に制限済みの評価構成では正例が成立します。
この変更はローカル権限の追加・削除を実行しません。

## 観測と責務

| 対象 | 公開APIで観測する値 | 限界・外側の必須責務 |
| --- | --- | --- |
| 機械・主体 | DsRole、LSA local domain、保持tokenの主体と識別値 | 信頼済みservice実行主体の供給、host operator/admin侵害は保証外 |
| アカウント | NetUserGetInfo 23/4の名前、SID、flags、primary、priv、auth_flags | 初回作成の真正な結果とjournal結合はcreator所有 |
| 所属 | NetLocalGroupEnum、完全修飾LookupAccountName、NetLocalGroupGetMembers 0のraw SID | 64alias/256member/64KiB以内。MORE_DATAや曖昧名前は完了を返さない |
| 割当権限 | LsaEnumerateAccountRights、既知の空割当status | 外部brokerの全権限解析ではなく、信頼するOS構成が前提 |
| 保護対象 | 同じ保持handleのowner/DACL、対象種別と固定fileID等の前後照合 | 固定manifestの完全性、保護配置、handleの真正な由来はprovisioning所有 |
| 実行時の委譲 | この準備段階では未来の実token/handleを観測できない | 有効化・非信頼コード実行前に、実token、継承/複製handle、生成済みruntime対象を別途検査する必須gate |

保護manifestにはservice object、監督process/token、設定、journal、host key、実行artifactと全祖先を含めます。
既に生成済みのcontrol pipeは同じmanifestに含めます。
未来のpipe/runtimeを取得済みとはしません。
service SIDはper-service SID型だけに限定し、通常Users SIDを信頼主体にできません。
ownerの暗黙WRITE_DAC、秘密key読取、process memory読取、token複製、pipeへの偽入力等を許さず、未知ACEやGENERIC権限は拒否します。
型やPIDの一致だけでは真正なproducerを証明しません。

同じquery ownerの固定thread/主体で二回の完全観測を比較します。
呼出前後と解放前後に主体を再確認し、部分buffer、panic、取消、回収不明を元ownerに保持します。
正常な取消は元scopeでbufferを解放できますが、主体不明threadではnative cleanupを再開しません。
これは通常の管理者による同時変更を原子的に防ぐ証明ではありません。
破壊操作の無効化・削除は既存のSID対象方式を維持します。

## 未接続・未検証

正規の固定manifest/保持handle producer、実WindowsでのACL成立、実行時token/handle gate、sshd設定・動的guest SIDの配線は未接続です。
純粋fake試験とWindows cross compileの成功を実アカウント作成・保護supervisor成立・実SSH失効の成功へ読み替えません。
この文書とsource検査はOS設定、資格情報生成、実通信を許可しません。

## 一次資料

- [USER_INFO_4](https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/ns-lmaccess-user_info_4)
- [NetLocalGroupEnum](https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netlocalgroupenum)と[raw member SIDの取得](https://learn.microsoft.com/en-us/windows/win32/api/lmaccess/nf-lmaccess-netlocalgroupgetmembers)
- [LsaEnumerateAccountRights](https://learn.microsoft.com/en-us/windows/win32/api/ntsecapi/nf-ntsecapi-lsaenumerateaccountrights)
- [対象別のaccess mask](https://learn.microsoft.com/en-us/windows/win32/secauthz/access-rights-and-access-masks)
- [Windows OpenSSHのS4U実装](https://github.com/PowerShell/openssh-portable/blob/latestw_all/contrib/win32/win32compat/win32_usertoken_utils.c)（参照branchは可変であり、配布artifactの固定・照合を代替しない）
