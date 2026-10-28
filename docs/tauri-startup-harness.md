# 診断featureを既存native harnessへ接続する契約

関連 #97 / #94 / #91。baseはPR #97固定head cd906bd030d2347a0cb1046f021001b473d96149。
実装所有はharness・診断専用CI・匿名検査だけ。既存Rust hookを変更しない。

## 分離した実行

通常native/evaluation workflowのbuild featureと配布validatorは変更しない。
専用tauri-startup-diagnostic.ymlでdebug/releaseを並列1で実行する。
featuresはcustom-protocol,startup-diagnosticsだけ、固定source/build profile/EXE hash/distribution=falseをBUILD.jsonへ記録する。
releaseはrunner内の診断fixture ZIPを再展開し、同一hashを検査したEXEだけを起動する。
このZIPは通常配布ZIPと別variantで、license一式を揃えた配布物ではない。
EXE/ZIP/生traceはartifactやLibraryへ配布せず、固定schemaの匿名reportだけを保存する。
元の配布ZIPと同一EXEを観測したとは表示しない。

superviseのStartupDiagnostic指定時だけ、起動前にsource/feature/hash manifestを検査する。
既存所有ToolsDirectoryに新規app-startup directoryを作り、RESCUE_HOST_STARTUP_TRACE_DIRをdriver起動前へ渡す。
通常入口ではこの環境変数を消し、終了時に元値を復元する。
既存driver source/build/pin、15秒単一POST、Node/harness期限、GUI/IPC/origin/通常close/観測子終了条件は維持する。

## 有限回収と合否

Node成功後・app通常close前、または失敗確定後・cleanup前にapp-startup.finishを一度作る。
既存driver診断の1秒wait budgetをアプリとdriverで共有する。
アプリ待機後はStopwatchを止め、通常close処理の時間を診断wait消費へ混ぜない。
実時間の非return保証ではなく、OS schedulingやI/Oを含むhard deadlineではない。
通常close前の診断waitはアプリの実寿命を変え得るため、timing影響がないとは保証しない。

Rust writerはcreate_new後に書くため、FileShare.Noneで一度開けるまで途中出力を完成物としない。
完成fileは2048bytes以内、readerも2049bytesまでしか読まず、不正UTF8/途中JSON/重複field/未知情報/欠落/未seal/dropはincomplete。
cleanup前のnative-result.appStartupFileClosed=trueを後段readerでも必須とし、false/欠測なら後からfileが完成してもincompleteを保持する。
既存classifyAppStartupにunknownとして入力し、固定reportだけを保存する。
観測窓完了はapp正常起動/IPC/全process終了の合格と同一ではない。
native-resultと診断reportは別判定。診断incompleteは専用CIの後段で非0とする。
source/EXEは起動前とcleanup後に照合する。新しい信頼根や署名ではない。

## 証跡の区別

reader未実装、build validator未実装、CLI未実装の各REDを先に観測し、1境界ずつ最小実装した。
不正file fixtureとPowerShell回収budget fixtureは実装後回帰であり、先行TDDと呼ばない。
ローカルNodeは26.10.0。固定Windows CIは既存の24.22.1/3.13.17/Rust1.99.1。
Windows feature有効buildと実stageは専用CIの固定head/run/artifactで別途確認する。

アプリstageは同じapp内で比較し、driver/Node時計との絶対時刻差を計算しない。
build/page-load完了が得られてもEdgeDriver内部や実SSHの成功とはしない。
元timeout根因、全WebView終了未証明の旧失敗、double-close NOT_MET、Windows背圧未検証、製品gate閉鎖は保持する。
