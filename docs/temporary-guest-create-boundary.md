# 一時guestの新規作成入口と検査拒否

既存journal・SID清掃・子processの所有基盤へ、無効accountの新規作成候補を接続する。
公開`NewNative`は未構成拒否で、実アカウント・資格情報・ACL・SSHをこの試験で操作しない。
完全なnative権限検査・有効化・製品配線の完成を示す差分ではない。

## 作成来歴と単回実行

既存Lifecycleが作成意図をjournalへ保存してから、同じintentの`creationSource.createDisabled`を一度だけ呼ぶ。
sourceは親のclean service主体、DCではないmachine role、local account-domain SIDを既存query kernelで前後照合してから、固定子を起動する。
子process・Job・stdio・実EOF・終了待ちには既存SID清掃と同じ実装を使う。
`childjob`と共有CI inventoryはこの差分で直接編集しない。

固定処理は[`New-LocalUser -Disabled -NoPassword`](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.localaccounts/new-localuser?view=powershell-5.1)を呼び、単一の返却LocalUserからSIDと無効状態を得る。
これは資格情報なしの無効staging候補であり、空passwordでの有効化・SSH logon方式の採用ではない。
名前・prefix・commentの照合や作成後の名前再lookupを、作成した主体の証拠にしない。
同名があれば失敗し、その主体を採用しない。
固定protocol、完全な要求、期待Plan、返却SID、正常exit/実EOF/実return/回収を照合し、要求と応答が別intentで自己一致していても拒否する。

確認した[第一者実装](https://github.com/PowerShell/PowerShell/blob/v7.4.0/src/Microsoft.PowerShell.LocalAccounts/LocalAccounts/Sam.cs#L1127-L1179)は、作成RIDと保持user handleからSIDを構成し、同じhandleでLocalUserを返す。
[cmdlet](https://github.com/PowerShell/PowerShell/blob/v7.4.0/src/Microsoft.PowerShell.LocalAccounts/LocalAccounts/Commands/NewLocalUserCommand.cs#L200-L244)はこの返却objectを出力する。
例外時の内部削除は成功確認されないため、errorや不完全な出力を不在・rollback成功へ変換しない。
真正な作成結果を確定できない場合はSIDなしのunknownとして隔離し、名前からの盲削除や再作成を拒否する。

一方、正常な作成結果として確定したSIDは、後続inspectorのconstructor、取消、無効状態や名前の検査失敗より先に保持する。
SIDとerrorが同時に返る場合も、既存coreは所有SIDを失わない。
同じSourceの別workerや新ownerで作成をやり直さない。

## 権限検査と回収の境界

native inspectorは既存のSID/flags診断へ接続するが、`complete=false`と未構成errorを維持する。
直接・間接group、privilege、logon rights、保護対象への危険な権限を未取得のまま、空の安全な一覧を作らない。
将来のlogon方式・実tokenの特性と、ログオン前の静的な割当権限は区別する。
それらのproducerや充足範囲を確定せず、既存の`complete`契約を弱めない。
[`AuthzInitializeContextFromSid`](https://learn.microsoft.com/en-us/windows/win32/api/authz/nf-authz-authzinitializecontextfromsid)の既定動作はS4U logonを試みるため、資格情報なしの静的検査として追加しない。

通常の未構成検査では、作成済みSIDを保持して準備を拒否し、canonical revokeとSID指定disableまでは既存順序で要求する。
完全検査がないため削除・清掃完了には進まない。
照会workerのclean return・scope健全・資源回収を確認した上での通常の拒否は、thread fatalへ自動変換しない。
通常のrenameや同名別SIDを観測しても作成来歴は失われず、名前fallbackをせず自分のSIDを対象とする。
DC/domain不一致はrenameと同義にせず、実機でも同じlocal machine/broker bindingの成立を別に確認する必要がある。
fatal/pending等の未知検査ではその状態を固定し、以後の検査・別native変更・回収成功を拒否して参照を保持する。
無効accountの存在と要清掃状態を残し、新しい安全なworkerへ付け替えない。

queryは専用goroutineで実行する。
caller期限が先着しても同じownerがworkerとnative参照を保持し、実return前にCloseを成功させない。
fatalで返ったlocked threadはnative処理を再開せずgoroutine終了により退役する。
既存queryの未知pending completion/join自体を実装したことにはしない。
Serviceの許可失効はこれらのaccount/query待機と独立して必要である。

## 必要な供給と実機受入

固定PowerShell・manifest・assemblyと全依存の保護配置、監査sourceとの一致、正規primary token、親と子の同じ主体/binding/handle所有は未設定のprovisioning責務である。
文字列のUserSID一致だけを実tokenの主体・service group証明としない。
PowerShellのruntime版だけではOS提供LocalAccounts moduleの監査版一致を証明できない。
将来のproducerがこの条件を満たすまで、raw profileをUI/env/argvから渡さず公開入口を閉じる。

作成APIが必要とするSAM管理権限をServiceが持つとは仮定しない。
`NoPassword`でも[内部のuser DACL更新経路](https://github.com/PowerShell/PowerShell/blob/v7.4.0/src/Microsoft.PowerShell.LocalAccounts/LocalAccounts/Sam.cs#L2289-L2313)があり、無権限・非変更のAPIではない。
実行権限が不足したら失敗し、LocalSystem fallbackやDACL緩和を追加しない。
実操作には、対象account、必要権限、内部ACL変更を含む影響、失敗時の隔離と復元を別途提示する。

fakeでは完全inspector成功系を用いて、Plannedから作成・所有SID保存・検査・SID清掃・journal終端までを縦断する。
native未構成の拒否系を別に検査し、fake成功を実権限の証明へ広げない。
Windows compileも型・linkの確認だけで、実作成・非管理者性・停止・再起動後清掃・実SSHは未実証である。
