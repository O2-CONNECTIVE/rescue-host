# 一時guestの実機操作を別途承認するための計画

この文書は実行手順の候補であり、実機作業の承認や実施記録ではない。
未決値を推測して埋めず、対象が確定した版を提示してからaction-time承認を得る。
資格情報や秘密鍵の値はこの文書・公開Issue・journalへ記録しない。
Operatorの認証根と、一時SSH guestの作成主体を区別する。

## 承認前に確定する入力

- 対象Windowsの版／architecture／local account-domain SIDと、domain controllerではない確認方法。
- 操作対象を限定するinstallation ID、Serviceの実行主体、保護process／directory／fileの配置と期待ID。
- account作成・無効化・削除を担当する最小の管理broker方式、固定artifact／moduleと検証digest。
- brokerへの必要権限と、remote非admin guestからの起動・変更・停止・handle複製を拒否する境界。
- journal初期化の担当、固定rootとledgerのowner／DACL、既存write handle／mappingを残さない作成・保持・引渡し。
- guestのgroup／実効privilege／logon rights／保護対象への権限を検査するproducerと、その許可範囲。
- 有効化する段階の資格情報、OpenSSHの接続権限、失効・保存・破棄方法。今回は未採用で生成しない。
- dynamic GuestSIDを同じ不変grant／profile／ACLへ結ぶ順序。承認済みhard deadlineを作成時間分延長しない。

通常のLocalServiceにaccount管理権限があると仮定せず、LocalSystemへfallbackしない。
必要権限が不足した場合も、DACLやpolicyを緩和して再試行しない。

## 最小権限の要求案

| 対象 | sourceが必要とする範囲 | 今回付与しない権限・未決 |
| --- | --- | --- |
| 事前配置directory | 保持参照の照会と固定leafの相対openに必要な読取・traverse | runtimeの新規作成、削除、owner／DACL変更は不要。祖先も含む保護取得・保持方法は別producer |
| 既存journal file | データread/write、属性とsecurity照会、同期、排他、flush | file再作成、rename、truncate、DELETE、WRITE_DAC／WRITE_OWNERによる自己修復は行わない |
| account診断 | clean primaryのquery、local role、LSAのPOLICY_VIEW_LOCAL_INFORMATION、対象accountのread | 権限不足は拒否。照会のためにaccount管理権限へ昇格しない |
| account変更broker候補 | SAMR上の作成DOMAIN_CREATE_USER、検索DOMAIN_LOOKUP、account設定USER_WRITE_ACCOUNT、削除DELETEに対応する対象限定権限 | これはAPI上の必要範囲の整理で、実主体への付与案ではない。PowerShell内部のMAXIMUM_ALLOWED要求との整合、broker identity／process保護／IPC方式は未決 |

変更brokerへ広いtokenを渡してguest実行へ転用しない。
必要な読み書き権限を確認する試験のために、既存DACLを変更して成功させない。
SAMRの権限名は[公開仕様](https://learn.microsoft.com/en-us/openspecs/windows_protocols/ms-samr/f37122b8-094c-4e5c-9633-3217d8988bd8)上の整理であり、samlib内部ABI採用やOS権限付与の承認ではない。

## 実際に変更する対象と影響

承認対象には、broker／Service設定、保護配置、初期journal headerとfile ID、account作成権限、必要なACL／logon rightsを個別に列挙する。
初期化は空ledgerの単なる作成ではなく、固定rootの保護と保持引渡し、flushと再open確認を含める。
既存ledgerがある場合は初期化で上書き・truncateしない。
backup復元や別ledgerへの交換で未清掃intentを消す手順は採用しない。

最初のaccount試験は、fresh承認に結びつく新規の無効accountだけを対象にする。
既存accountの名前・prefix・commentを根拠に変更・採用・削除しない。
SSH有効化はaccount作成試験と別の変更として、許可鍵・固定宛先・停止と期限・再接続拒否の受入を提示する。

## 失敗時の封じ込めと復元

1. canonical authorityの新規受付を止め、既存許可を失効させる。account workerやjournal I/O待ちを失効の代用品にしない。
2. 所有SIDが確定した対象だけを無効化し、旧guestの内側SSH／作業process／networkの回収を確認する。
3. exact対象の削除と不在確認、journalの終端保存を別々に記録する。未確認は要清掃として保持する。
4. SIDなしの途中作成、別SID、未知write／flush／delete、破損journalでは自動修復・再作成・盲目的削除を止める。保護された照合記録を残し、個別の手動復旧案を提示する。
5. 作成したprofile／home／追加ACL／作業データがある場合は、別の所有記録に従って清掃する。名前から任意pathを再帰削除しない。
6. 全所有worker／process／I/O／handleの回収を確認してから、今回追加したbroker／Service設定と権限を記録した旧値へ戻す。復元失敗時も完了扱いしない。

既存の他Service、既存ユーザー、顧客のログオン中ユーザーは削除・再設定しない。
旧journalとaccount残留を忘れて製品を再インストールした状態を、初期化済みとみなさない。

## 実機で別に必要な証拠

実account API、SID固定mutation、二重起動拒否、OS再起動後の清掃、disk flush／破損拒否、guestからの保護対象改変・停止拒否を確認する。
これらの手順・対象・復元を別承認し、通常unit／compileのgreenでpassedへ変更しない。
禁止済みP2専用scenarioの実行承認としてこの計画を使わない。
