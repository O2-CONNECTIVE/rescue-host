# 失効と回収の受入条件に対応する限定試験

[配布版の受入記録](windows-acceptance-records.md) / [子とJobの契約](../backend/internal/childjob/README.md) / [バックエンド契約](SECURITY_BACKEND_CONTRACT.md)

照合基点はmain `eb179ddf2858e1330de1a3e073855e8f7a8c56d4`（#161）です。
本書は受入条件とsource/fake試験の対応表で、実機の成功記録ではありません。
#155の8件、JSON schema、既存44条件の合否は変更しません。
実SSH、正規guest token生成、製品caller、実Windows保護は未実証で、製品gateは閉鎖中、C方式は未採用です。

## 既存条件と試験の対応

| 受入条件と期待する結果 | 現コードで確認する境界 | 既存試験と確認範囲 | 残る不足 |
| --- | --- | --- | --- |
| 異常終了を通常成功に変えず、失効と物理回収を分ける | `Child.Wait`、`Prepared.Wait`、Hostの単回所有 | [子の試験](../backend/internal/childjob/child_test.go)の `TestWaitReportsAbnormalExitWithoutInventingGoCloseEvidence` が非0退出を `ErrExit` と区別。[Hostの試験](../backend/servicehost/approved_test.go)の `TestFailedLaunchCanRetryOnlyOwnedCleanupAndNeverApproval` が部分起動の失敗後の回収と再起動拒否を確認 | token観測付きPreparedの異常退出とquery回収失敗の組合せを追加。実process異常終了から製品issuer失効までの縦断は未実証 |
| 最大期限、元の準備期限、呼出期限を新しいcontextやpollで延長しない | Preparedの元lease、再検査、issuer、Host | [検査の試験](../backend/internal/childjob/inspection_test.go)の `TestExpiredInspectionCannotBorrowFreshContextBeforeCancelNotification`、`TestCallDeadlineDuringReinspectionCannotStartUnderLongerLease`。[tokenの試験](../backend/internal/childjob/guest_token_test.go)の `TestGuestTokenObservationDoesNotOutliveOriginalAdmission`。[issuerの試験](../backend/supervisor/ephemeral/issuer_test.go)の `TestPolicyIsFrozenAndPollingDoesNotExtendHardDeadline`。[Hostの試験](../backend/servicehost/approved_test.go)の `TestHostHardDeadlineCancelsWaitAndRevokesBeforeProcessCleanup` | 同じ境界の期限拒否試験は重複追加しない。実Windowsでの独立期限執行、sleep中の停止は未実証。非活動の定義を短leaseで代用しない |
| 切断で旧許可を失効させ、再接続には新しい認証と承認を必要とする | 認証済logical channel、preview、同じissuer | [previewの試験](../backend/servicehost/preview_dispatch_test.go)の `TestPreviewExplicitAuthenticatedChannelDisconnectInvalidatesCandidate` が認証取得のEOFで候補とissuerを失効。[grant拒否試験](../backend/servicehost/confirmed_grant_reject_test.go)の `TestConfirmedGrantCannotReissueAfterWriteLossReconnectOrIndependentRevoke` が再hello、再prepare、再confirm等で再発行しないことを確認 | これは同じServiceの旧状態を拒否するfake境界。実接続の切断検知、再認証と新規承認が成立する製品の正例は未接続。新しいServiceや正規tokenを架空のfactoryで補わない |
| 古い証拠、copy、別target、別ownerや別issuerの証拠で開始しない | Inspection、GuestTokenObservation、保持grantとruntime proof | [検査の試験](../backend/internal/childjob/inspection_test.go)の `TestInspectedTargetRejectsForeignReceiptAndSingleUseReplay`。[tokenの試験](../backend/internal/childjob/guest_token_test.go)の `TestGuestTokenObservationCannotBeCopiedToAnotherReceipt`、`TestGuestTokenTargetAndAdmissionCannotBeSubstituted`。[targetの試験](../backend/servicehost/confirmed_target_test.go)の `TestConfirmedTargetRejectsUnapprovedForeignOrChangedEvidence` | 既存のpointer/lifetime/issuer照合を再実装するfixtureは追加しない。保存state、再起動後の正規配置と実認証への縦断は未実証 |
| 同じ許可と対象の二重開始は一度だけを受理する | issuerの単回消費、Serviceのlaunch所有、同じPreparedの開始 | [issuerの試験](../backend/supervisor/ephemeral/issuer_test.go)の `TestConcurrentChallengeConsumptionHasOneSideEffect` が16並行要求の1副作用を確認。[targetの試験](../backend/servicehost/confirmed_target_test.go)の `TestConfirmedTargetUsesActualIssuerLaunchExactlyOnce` と[tokenの試験](../backend/internal/childjob/guest_token_test.go)の `TestGuestTokenObservationIsReadOnlyUntilSingleInspectedCommit` が逐次再利用を拒否 | token観測後の同じPreparedに対して、2つのCommitが未消費時点で重なる場合を追加。製品全体の二重起動や別Service間の排他を確認したことにはしない |
| cleanup失敗は終了未確認を維持し、元所有者だけが回収を再確認する | query参照、target workerのjoin、SCM回収 | [検査の試験](../backend/internal/childjob/inspection_test.go)の `TestFailedSecondInspectionRetainsBothQueryReferencesUntilClose`。[tokenの試験](../backend/internal/childjob/guest_token_test.go)の `TestGuestTokenCanceledNativeReadRetainsPartialQuery`。[targetの試験](../backend/servicehost/confirmed_target_test.go)の `TestStopDuringTargetCommitWaitsForActualReturnAndRejectsLateSuccess`、`TestRegisteredActorSuccessIsNotReplayedAfterPlaneCleanupFailure` | 既存の失敗保持とjoin順序は重複追加しない。追加した異常退出との組合せでも回収成功で元の異常終了を消さない。実OSの物理終了とGUI表示の縦断は未実証 |

この調査はchildjobの同target検査、Serviceの承認bridge、issuerとHostの所有境界を対象にしています。
一覧に載せた試験の存在と、今回実際に再実行した範囲は分けて記録します。
source/fakeの正常値や固定codeは、正規tokenの来歴、保護DACL、全group/privilegeの製品policy、実OSの停止上限を確定しません。

## 追加した不足2件

[追加試験](../backend/internal/childjob/guest_token_lifecycle_test.go)は、既存の公開メソッドと既存kernel fakeを使います。
producer、承認bridge、API、配布物、共有README、Go CI inventoryは変更しません。
新しい機能は追加しておらず、初回から既存実装で成功した後付け回帰です。
新機能のTDDや製品の実機合格には数えません。

| 追加試験 | 固定する操作順と独立した判定 |
| --- | --- |
| `TestObservedGuestConcurrentCommitsHaveOnlyOneAcceptedStart` | 同じPreparedのInspectとtoken観測後、最初のCommitを再検査中に保持。次のCommitが同じ未消費観測で入り所有permitを待つことを `testing/synctest` で固定。一方だけ成功し、fakeのresumeは1回。消費済み観測のCurrentと再Commitは拒否 |
| `TestObservedGuestAbnormalExitKeepsUnconfirmedQueryCleanupAndExitFailure` | token観測後に開始し、非0退出とquery close失敗を同時に返す。最初のWaitは `ErrUnconfirmed`、古い観測の再利用は拒否。同じ所有者の再Waitで残るqueryを回収しても `ErrExit` が残る。以後のWait/Closeがqueryを再閉鎖せず、観測を復活させない |

異常退出後の試験では、Inspect、Observe、Commitが保持した3つのquery参照の回収を確認します。
最初の1回の失敗を成功した閉鎖へ数えず、その後の3回の回収と区別します。
Job内のprocessとquery参照の回収が揃っても、Go close応答、実SSH終端、全workload終了を生成しません。

## 実行範囲と未決事項

Goの版、依存の固定、通常とrace、結果件数とskipの扱いは[既存のGo CI手順](go-ci.md)に従います。
対象packageは `./internal/childjob` と `./servicehost` です。
追加2件だけを確認する場合は、同じGo環境で次を使います。

```sh
go test -count=1 -timeout=30s -run '^TestObservedGuest' ./internal/childjob
```

既存のGo CIはtest sourceを探索するため、テストの除外、skip、製品実装inventoryの拡張は不要です。
通常とraceのsource/fake成功を、Windows native試験や配布EXEの検証へ転記しません。
実機の操作、資格情報、service、ACL、Firewall、OS設定は変更しません。
P2専用再現、ps、loopback bindによる代替実証は実行しません。

正規token生成方式、製品caller、同じ実targetに対する正規承認の縦断は統合担当へ残します。
方式の決定前に、特定のWindows API、権限の付与、service identity、復元値を期待結果へ追加しません。
実機の8件は[既存記録](contracts/windows-acceptance-record.template.json)の未実行を維持し、本書の2件を実機の成功件数へ加算しません。
