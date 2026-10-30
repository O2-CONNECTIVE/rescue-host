# 固定 artifact の新規 runner 比較計画（未実行）

## 目的・現在の観測
実製品の session-create が15秒を超える症状の feedback loop を作るため、同一 binary の新規 runner における初回と後続の差を観測する。root cause の証明や修正ではない。

run 40881061735 で session 全体は 14895 / 953 / 993 ms。既存 driver の同一 request slot で forward-start → native-headers は 14844.137 / 887.530 / 923.418 ms。driver trace は3回とも complete、drop 0。1回目の10秒 sample は要求pendingでstdout/stderr 0 bytes、固定hintsは全false。これは native 応答 header までに時間を費やしたという境界であり、WebView生成・driver内部・IPC等のどれが遅れたかは分からない。時間原点が別のため session全体との差を同一タイムラインの詳細と決めつけない。

## artifact の実現可能性と限界
GitHub Actions は同一 workflow 内の先行job artifactを後続jobに渡せる（https://docs.github.com/actions/tutorials/store-and-share-data）。run 40881061735 は既に executable を保存せず、root removed なので、その時の同一 binary は再利用できない。新たな準備jobで main `1eda7a4e964d34a751dcc89509ee07b3482917a7` の lock と既存の正式手順から一度だけ release EXE と検証済 tauri-driver を作り、同一 artifact を共有する必要がある。元の timeout binary と今回の3試行binaryのどちらとも同一とは主張できない。

候補は製品評価 ZIP、tauri-driver.exe、driver provenance、tauri-driver と依存のライセンス資料 ZIP、固定 manifest。固定crateは各runnerで公式取得しhashを照合する。profile、ログ、鍵、secret、端末情報は含めない。Microsoft Edge WebDriver の公式 EULA に公開・配布制限があるため、EdgeDriver は artifact に含めない。各 runner は Microsoft 公式配布元から取得し、固定hashと署名を照合する（https://developer.microsoft.com/en-us/microsoft-edge/tools/webdriver/eula）。ユーザーによるartifact公開の承認は取得済み。最終payloadのlicense・秘密除外確認と通常の実行審査は必要。

固定manifest・各 SHA256 を作成し、公式 upload / download action を完全SHAでpin。準備jobは artifact 内manifestとは別の job outputs として artifact ID・digest・source・各ファイル hash を渡す。後続jobは同一workflowのその ID を指定して公式 action で取得し、manifestと実際の各ファイルを準備job outputs に照合し、binary hash を実行直前・直後に検証する。manifestと中身の相互照合だけに依存しない。実際のファイル hash 不一致・欠測は fail closed。これは準備job自体の侵害を検出するものではなく、新たな署名やtrust rootは設けない。独自ZIP展開は使わない。

## CI の信頼境界
対象は GitHub 管理の job ごとに新規作成される Windows hosted runner、固定の公開 source と公式 action、固定 registry の依存のみ。未信頼 guest や実SSHは起動しない。専用rootを書き換え得るのは runner agent、同じjobの checkout/build/test/driver/app とその子、同じ権限を得たプロセス、runner管理者である。hashは比較する入力を固定するが同じ権限のwriter不在やTOCTOUの完全排除を保証しない。runnerを所有する GitHub、固定actionや依存の健全性は信頼前提。期限切れ/失敗時は不明なprocessを名前で終了せず、後続試行を止める。安全な削除を確認できなければ個別削除せず未確認と記録し、VM破棄に委ねる。破棄はプロセス停止やファイル削除の実測証明ではない。

artifactは公開repoの Actions 上に一時保存され得る。診断用 binary の公開範囲と最短保持期間（提案1日）を明示し、公開 release やGitHub外への配布には使わない。今はuploadしていない。実行前には公開対象ファイル一覧、hash、artifact の閲覧範囲と削除/期限を確認する。

## 最小実行案と事前停止条件
- 最大2台の新規 GitHub hosted Windows runner を直列に使い、各 runner で同一 artifact に対し初回1回＋後続1回、最大4試行。各試行 session POST 1回、15秒、各自新しいUUID profile、固定loopbackと所有cleanup。計画外のretryや成功までの反復はしない。
- 準備と各runnerは固定 source・EXE / driver hash、runtime、runner image情報、15秒、同一supervisorを記録。各runnerの第一試行がfresh runnerでの初回、第二が同じrunnerの後続。別runner・同じrunnerの差を分け、第一runner後に第二runnerを開始する。
- 各試行の所要時間・成否・trace・正常close・cleanupを個別保存。試行前に not-started / running の状態を記録する。保守的に、試行の失敗やtimeoutでも後続を開始せず、cleanup未確認なら資源も保留する。runner 2 は runner 1 の二試行成功・回収・cleanup確認 outputs のすべてが揃う場合だけ開始し、job timeout / cancel・upload失敗・証拠欠落や未定義値を成功と扱わない。後続は not-started と明記。
- 今回の3試行とは合算せず、4件の生データを表示する。4件全成功でも発生率・安定性を保証しない。ランナーごとに初回だけ遅ければ cold起動との関連が候補となるが原因ではなく、差がなければこの小規模比較だけで棄却しない。
- 固定できる: 作成した artifact の binary bytes/hash、固定source/lock、固定依存、手順、session要求数・15秒、各UUID profile。固定できない: 物理ホスト、スケジューリング、cold状態、共用負荷、network/OS imageの中身、実効WebView runtime、clock・OS cache。imageやruntimeは記録し相違・欠測を表示、期待と異なる runtime では比較不成立とする。
- rootは削除を試みずrunner破棄に委ね、削除の成否は未確認とする。観測対象外のプロセスの不存在は主張しない。runnerが中断され回収できなければ cleanup不明。旧run 40879729894 の未確認cleanupは履歴として残し、新結果で上書きしない。

## 最小の次の境界計測（未実装）
現在のforward-start → native-headersは約14.844秒の区間内訳を持たない。比較で実症状が再び得られ、なお分類が必要な場合だけ、既存の固定 test-only driver trace の範囲で native driver受領／既存callback完了／header返却の関係を匿名の monotonic 時刻と単一slotで結び、収集できない箇所はunknownとする。raw URI / body / credentials / machine情報は記録しない。固定製品バイナリへ計測を追加する場合は binaryが変わるので同一binaryの比較とは別実験。まだこの計測を追加しない。

## 実施前の必要操作
別branchに計画に対応する最小workflowとテストを作成し、固定配布範囲・manifest・cleanup・早期停止・4行の未開始/結果の独立reviewを行う。CI上での実行は、その後に対象headと新artifactの一時公開範囲を提示し、通常の審査を通して行う。実機準備、OS設定、資格情報、SSH有効化、公開release、main mergeは含まない。
