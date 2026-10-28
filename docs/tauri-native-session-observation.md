# session作成timeoutの匿名観測

関連 #74 / #25 / #9。製品やdriverのtimeout修正ではなく、native harnessの診断。
#74の失敗run40641482179（head36d10b56）は、session POSTが15,013msでtimeoutとなりdocument観測0。
事前の4444/4445はloopbackかつ記録driverのPID一致を確認し、cleanupFailures0・driver終了確認あり。
app/WebViewが一度も起動しなかったか、起動後に消えたか、driver内部で待っていたかは旧artifactから判断できない。
直前e03e6aaの配布run40640524022は成功し、差分は文書4件。
#65のorigin判定や#66の子終了と同じ原因とは断定しない。

## 記録する境界

- session要求前: 検証済みの既存snapshotを再利用し、driver/edgeの生成時刻付き同一性、app候補数、初回lineageの相関、待受の匿名状態を記録。
- webview/IPC試験が失敗した場合: 元のFAIL確定後、cleanup前のsnapshot/待受を一回だけ読み、同じ匿名factsとquery所要時間を保存。
- 所有node/driverの従来cleanup後: 一回だけ同じ状態を読み、残留候補の有無を保存。所有不明candidateには終了操作を行わない。

新しいsession作成・driverへの追加要求、成功までのretry、期限延長はしない。
15秒のrequest期限・既存の正常close/全Children/10秒条件を維持し、診断取得不能はunavailableとして元の合否を保つ。
queryは反復せず、既存CIの3分harness上限の中で行う。OS問い合わせ単体の厳密な完了期限を保証するものではない。
診断用snapshotは4096行まで、表示するlistenerは8行まで。上限を超えた情報を成功判定へ読み替えない。

## 匿名化と限界

JSONへPID・実行path・command line・絶対生成時刻・raw driver log・例外原文は出さない。
同一性は記録PID/CreationDateとの比較結果で、CreationDate欠測はunavailable。
lineageの整合はPPIDと生成順の相関であり、保護された所有証明ではない。
listenerのownerMatchesRecordedPidもPID比較だけで、世代の一致は別fieldを見る。
未知状態はnull/unconfirmed/unavailableとし、0件や成功へ変換しない。

snapshotは時点観測のため、app候補0件は「一度も起動していない」の証明ではない。
app候補や相関先が残れば、cleanupFailures0だけで全資源が消えたとは言わない。
cleanupFailuresは従来の自己所有node/driver/handle操作の結果である。
hosted runner廃棄の独立確認、driver内部forwardingやappの過去の起動・終了履歴は、この変更では証明しない。
既存stderrはCI artifactへ追加せず、raw値の収集や公開を広げない。

## session失敗時のdriver log hint

app/WebViewが存在してもsession応答が返らない実例をrun40648334716で観測した。
driver内部の待ち箇所を絞るため、session作成失敗時だけ自己所有の固定2ログを読む。
\`driver.stdout.log\`と\`driver.stderr.log\`を各64KiBまで一回読み、固定phraseの存在booleanと読取bytes/打切りだけを別JSONへ保存する。
取得不能・非通常file・symlinkはunavailable/null。原文・filename・path・引数・driver payloadは保存しない。
既存logを読むだけで、verbose設定や新しいdriver/session要求を追加しない。
hintはproxy error/session未作成/DevTools port欠測/profile使用中/browser到達不能・終了/version不整合の固定分類。
phraseが記録されたという情報であり、原因確定や正常性の判定ではない。
falseは読んだprefix内で未検出という意味だけで、打切り・未記録・読取時点によって取りこぼす可能性を残す。
JSON保存失敗も固定codeで扱い、元のFAILや15秒期限を変えない。

## 試験

既存の所有/close契約25チェックを通過後、追加module不存在のREDをrun40646275884で観測（実app未起動）。
新しいfixture契約は、app候補数と相関数の区別、app欠測を成功にしないこと、曖昧なPID世代と匿名化を検証する。
fixtureはprocessやportを作らず、native起動の証拠には数えない。
独立レビューの追加保存例外と未知住所は、run40647333973で失敗を先に観測した。
保存は既存の固定code writerを共有し、失敗しても元のFAIL/cleanup値を成功へ変えない。住所解析不能はloopback=nullとする。
実Windows native/配布CIの結果と、#74との結合後の固定head結果は別に記録する。
Go/runtime/Rust readerや製品gateは変更しない。
