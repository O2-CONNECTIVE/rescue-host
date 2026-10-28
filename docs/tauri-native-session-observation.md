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
`driver.stdout.log`と`driver.stderr.log`を各64KiBまで一回読み、固定phraseの存在booleanと読取bytes/打切りだけを別JSONへ保存する。
監督下の自己所有CI一時領域を使い、他worker・攻撃者によるファイル差替えがないことを前提とする。
取得時に検出した非通常file・symlinkと取得不能はunavailable/null。
lstatからopenまでの競合、敵対的filesystemへのno-follow・所有隔離は保証しない。
原文・filename・path・引数・driver payloadは保存しない。
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

## 単一session要求の前後観測

main ba8c595eのrun40667680395はsession POST15,151ms timeoutで、stdoutにproxyConnectionErrorを検出した。
この句はtauri-driverの接続処理全般のerrorに使われ、原因かクライアント取消の結果かを現在のartifactだけでは区別できない。
Rust/client追加が原因との証拠もない。

session要求を1回だけ開始し、未完了なら10秒timerで固定2ログを各64KiBまで1回読む。
要求が先に完了したらtimerを解除する。失敗時には要求確定後にも1回読む。
各sampleには要求開始を基準とするperformance.nowの読取開始・完了ms、読取開始時の要求未完了boolean、固定stageを付ける。
要求完了時刻と固定outcomeも別に記録する。壁時計・端末時刻・PID・path・例外原文は付けない。
HyperのIncompleteMessage/Canceled/Ioの固定表記だけを種類hintへ写し、未知のproxy errorはunclassifiedとする。
元ログはrequest IDを持たず、分類は同一requestへの帰属や内部error型の証明ではない。

前sampleでerrorが存在すれば、その読取完了までに記録されていたという証拠になる。
前false・後trueでも、10秒以降の先行errorと要求取消の結果は区別できない。
読取windowが要求確定と重なった場合も、開始時booleanだけから記録順を断定しない。
記録時点はevent発生時点ではなく、単調時刻はnodeプロセス内だけで比較できる。

既存15秒request期限・単一要求・合否・正常終了/全子終了条件・cleanupを維持する。
診断のI/Oは要求と並行に開始し、要求確定後に回収する。filesystem単体の厳密な完了期限は保証せず、既存60秒node期限と3分harness上限を維持する。
診断取得不能はnull、保存不能は固定code。元の成功値/失敗例外は置換しない。
これはharness診断であり、製品修正・旧timeout解消・実SSH成功の証拠ではない。
