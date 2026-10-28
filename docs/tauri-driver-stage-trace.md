# 試験専用driverの匿名stage観測

関連 #25 / #9 / #91。製品接続やtimeoutの修正ではなく、native CIの診断variant。
公式tauri-driver2.1.0、crate SHA256 `2f2e29bd6900fb718dfdc06f2e7a371d6087c085daaf83709847ef3e6b0c1953`と元Cargo.lockを保持する。
新しいCargo/PyPI依存、proxy/listener、driver要求、retry、EdgeDriver変更は追加しない。
製品アプリ・配布ZIPへ診断driverを含めず、元の15秒request、60秒node、3分harness、正常close/全観測子終了の判定を維持する。
診断hookのtiming影響がゼロとは保証しない。根因解消・実SSH成功・P2成立とも扱わない。

## sourceと実行物の対応

`tools/native-driver-trace/driver.patch`はmain.rs/server.rsだけへの厳密差分。
同directoryのdiagnostic.rsとdiagnostic_tests.rsだけを追加し、元sourceの他fileは一切変更しない。
`scripts/apply_driver_trace.py`が公式crate全体のhashを検証し、全展開fileの集合・bytesと公式原本を照合する。
patchと追加sourceのhashをscript内でpinし、文脈ずれ・fuzz・未知path・rename・binary差分を受け入れない。
全検査後にだけ書込み、全postimageも再検証する。Cargo.toml/Cargo.lock/推移依存は変更しない。
`.gitattributes`はこのdiagnostic bundleだけcheckout改行変換を禁止し、Windowsでもpin済みbytesを保持する。

CIでは既存の固定Rust1.99.1とPython3.13.17を選び、自己所有runner tempだけでfixture test/buildする。
元のupstream crate/hash、patch/hash、追加source/hash、変更対象の前後hash、Cargo.lock hashをprovenanceへ残す。
build後にsourceを再照合してbinary hashを記録し、起動直前・終了後も同じbinary hashを確認する。
これはreview済みrepositoryと公式固定buildを信頼する対応記録であり、新しい署名・信頼根ではない。
未変更の公式binaryであるとは表示しない。Edge binaryの既存hash・Microsoft署名・version検査は残す。

## eventの意味

| stage | 観測する境界 |
| --- | --- |
| connection-open | Node/準備client→tauri-driverのHTTP接続受理。端末情報やremote addressは記録しない |
| session-received | POST /sessionをhandlerが受け取り、短命request slotを割当てた |
| forward-start | 元のbody変換とforward先URI構築が完了し、既存native requestを呼ぶ直前 |
| native-headers | native response futureがheader付きで返った。body完了やNode側到達は未証明 |
| native-error | native response futureが失敗した。原例外/OS文言は記録しない |
| request-dropped | headers/errorより前にservice futureのguardがdropされた。物理socket closeとは別 |
| connection-end | Node側HTTP接続のserve futureが終端。native側TCP終端ではない |

connection/request slotは試験process内の整数で、資格情報・launch identityではなくwireへも追加しない。
同じPOSTに結び付け、準備GET/statusのEOFを誤帰属しない。POSTは1件のみを完全観測の条件とする。
connection末尾のrequest slotは、最後のrequestとの関係であって切断時active状態や原因とは同一視しない。
EOF後にpending service futureがdropされる順序も、同じslotとstate遷移で区別する。
headers返却後のbody streaming、native内部/DevTools/WebViewの待ち箇所、OSの物理TCP終端、アプリ全process終了は観測しない。

## 上限・完全性・匿名化

producerは固定32eventのmemory ringへtry_lockで記録する。request taskにI/Oやflush/join待ちはない。
seq/attempted/dropは独立atomic counterで保持し、ring満杯や競合でdrop通知自体が消える問題を避ける。
固定fieldはtype/seq/connection/request/stage/state/elapsedUs/errorだけ。errorはnone/incomplete/canceled/closed/parse/other。
時刻はdriverのInstant基準。node performance.nowや壁時計と直接差分を取らず、同じdriver trace内で比較する。
raw payload・body長・capability・header値・認証値・URL・query・path・PID・OS情報・session ID・原例外は記録しない。
元の共通Debug error出力を匿名eventへ置き換えるため、旧stdout hintのfalseをerror不存在と解釈しない。

既存native合否確定後、supervisorは自己所有ToolsDirectoryの固定finish markerを1回作る。
workerはこの制御markerを50ms間隔・最大120秒だけ確認する。ログのpoll頻度増加ではなく、event記録はhook駆動である。
workerがrecordingを止め、実行中のproducerだけを最大100ms待ち、summaryをring外で生成する。
summaryはlastSequence/attempted/enqueued/written/dropped/sealed/pendingRequests。request taskは待たない。
ここでsealedはrecording windowと選択POSTのheaders待ちfutureについての完全性で、全connectionやbody/OSの終了ではない。
未終了request、in-flight未回収、force終了、保存不能、overflow、seq/count/型/時刻/相関不整合、summary欠落はdiagnostic-incomplete。
sealed後に観測対象外のeventが発生し得るため、traceの無いeventを将来まで不存在とはしない。

出力は1行256bytes・全体8KiBまで。自己所有のpending fileへ書いてからrenameし、半端なfileを完成物にしない。
supervisorは既存判定の後で最大1秒だけ完成fileを待ち、元の所有driver cleanupを実行する。診断のためにdriver寿命やclose条件を延長しない。
raw traceはToolsDirectory内に保持し、そのままartifactへcopyしない。
`scripts/verify_driver_trace.py`が固定schema/enum/整数/seq/完全性と同一slotの状態遷移を検証し、安全なJSONだけを証跡へ保存する。
不正入力・未知field・parse errorは固定codeと空eventsで拒否し、原文/pathを出さない。
source/binary provenanceも固定sourceから再検証して再生成し、入力sidecarを丸ごとcopyしない。

診断判定はnative-result.jsonの既存合否と分離する。新しいCIの後段stepは診断不完全でも非0とし、native成功を診断完全性に読み替えない。
未完了・欠測を隠してgreenにしない。旧sourceの成功や不合格も最終headの結果へ読み替えない。
自己所有の非競合runner tempが前提であり、敵対的filesystemのrace、I/O非return、compiler/host侵害は保証しない。

## 検証の区別

CLI相関と厳密patch適用はmodule/CLI未実装のREDを先に観測した。
EOF後のfuture drop、schema型と単調時刻の不整合もCLIでRED→GREENを確認した。
Rust fixtureは固定sourceのproducer未実装によるcompile REDを既存Windows CIで確認し、同じfixtureを実装へ残す。
追加のheader/error/overflow/未終了request・秘密field・未知差分・pin driftは実装後回帰で、TDD先行件数とは区別する。
fixtureはmemoryと固定JSONだけで、追加listener/OS保護/実SSHの証明ではない。
最終独立security/規約review、固定headのtool fixtureと通常native/展開EXEの結果を別々に保存する。
