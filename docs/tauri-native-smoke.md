# Windows native smoke の確認範囲

[未配線 shell](tauri-native-shell.md) / [UI 品質](tauri-react-ui.md)

## 目的と非目標

Issue [#25](https://github.com/O2-CONNECTIVE/rescue-host/issues/25) と [#9](https://github.com/O2-CONNECTIVE/rescue-host/issues/9) に関連する、Windows hosted runner 上の限定した実起動検証です。
実 WebView2 と実 Rust IPC から未接続・開始停止不可・終了未確認を読み、診断を開き、通常 close 後に所有プロセスの終了を確認します。
画面、tokens、Lucide 規則、製品 Rust、CSP、capability、Go/SSH/sidecar は変更しません。
新 PC、配布 installer、署名、更新、OS のアクセシビリティ設定と支援技術は別の未検証です。

## 方式

[公式 Tauri WebDriver](https://v2.tauri.app/develop/tests/webdriver/)の external tauri-driver と embedded provider を比較しました。
embedded は macOS を含む方式ですが、追加 test plugin が必要です。
今回は Windows のみ、外部 tauri-driver 2.1.0 と Microsoft Edge WebDriver を使い、製品に test plugin や debug permission を追加しません。
runner に既にある WebView2 を read-only で検出し、[実 Runtime と Driver の版合わせ](https://learn.microsoft.com/en-us/microsoft-edge/webview2/how-to/webdriver)を行います。
不足時に Evergreen installer、registry、サービス、Firewall、OS security 設定を変更しません。
通常 close は driver の session 削除や kill-on-job-close より前に観測し、強制 cleanup の成功とは分けます。

## 段階と証拠

最初は read-only 環境 inventory だけを CI へ公開します。
`nativeTestsRun: 0` であり、inventory job の成功は native smoke の合格ではありません。
その後に実 Runtime と対応 Driver の固定 version/hash を確認し、起動試験と0件拒否の受入を接続しました。

## 実装した境界

Node の標準 fetch で W3C WebDriver の必要な command だけを送り、追加 npm runtime は導入しません。
各 request は15秒、Node 全体は60秒、driver 起動は15秒、通常 close と子終了の待機は各10秒、smoke step は3分、job は20分で打ち切ります。
worker は1つで、試験再実行や成功するまでの retry は設定しません。
readiness と有限の状態観測 poll は試験の再実行とは区別します。

前提 inventory で実 Runtime `131.0.2903.86` と既存 Driver `156.0.4314.42` の不一致を観測しました。
`tools.json` の版・ZIP/EXE SHA-256 に固定した同版の公式 Microsoft Driver を一時領域に展開し、Microsoft Authenticode と実行版も照合します。
固定 tauri-driver crate の SHA-256 を照合し、その同梱 Cargo.lock と承認済み CI Rust で一時領域へ build します。
Runtime が変わったら検査を失敗にし、latest の自動取得や Runtime 更新で通しません。
製品 build へ試験 plugin、dependency、permission は追加しません。

WebView の DOM と生の `window.__TAURI__.core.invoke` の両方を確認し、fixture や global 上書きは native 実行に使いません。
許可相手、範囲、期限、終了はすべて未確認、開始と停止は disabled、診断は `SHELL_UNWIRED` を必須にします。
スクリーンショットは WebView の内容だけを撮り、desktop、他アプリ、driver log は公開しません。

所有 Rust app を EXE 完全パス、親子関係、PID と CreationDate で一意に同定し、WebView 子の観測数が正であることも要求します。
通常 close は `CloseMainWindow()` で要求し、exit code 0 と観測済み子の終了を確認します。
両 driver が同じ identity で生存している間に結果を確定し、その後で所有 driver の Job Object を cleanup します。
driver 異常終了による子の消滅、空集合、照会不能、timeout を正常 close の合格にしません。
観測した PID 群の終了であり、実通信や未実装 sidecar の終了証明ではありません。

## 試験の区別

harness の UI/IPC 条件は `NATIVE_ASSERTIONS_UNIMPLEMENTED` の赤から最小実装の緑を確認しました。
これは注入した fixture による harness の契約試験で、Windows 実起動の赤や成功ではありません。
別の PowerShell 契約試験では所有 path 不一致、子ゼロ、driver 先行終了、PID 再利用、close timeout の拒否を確認します。
実 native の最終判定は1件の実行と1件の成功、実 IPC、通常 close の観測を必須にし、0件を成功にしません。
CI の最終結果と native の実測は、実行後の PR に head とともに記録します。


## Windows hosted CI の限定した実測

head `35c5bb37504fdc90503144b52e2435e825b36322` の [native CI](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40515180926) で1件実行・1件成功しました。
Windows Server 2022 runner image `20261022.344.1`、WebView2/EdgeDriver `131.0.2903.86`、tauri-driver `2.1.0`、Rust `1.99.1`、Node `24.22.1` の組合せです。
未接続表示、開始停止 disabled、許可条件と終了未確認、診断 `SHELL_UNWIRED`、生の Rust IPC の厳密な未配線応答を観測しました。
通常 close の要求後、app exit code 0 と観測済み WebView 子6件の終了を、両 driver が同じ identity で生存し cleanup を開始する前に確認しました。
artifact の `normalCloseVerified`、`closeObservedBeforeCleanup`、`driverStableThroughClose` は true、失敗 phase は null です。
その後で試験用 driver を cleanup しています。

先行した [CI 40514392703](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40514392703) は listener 検査で app 起動前に停止し、native0件の環境/harness 失敗でした。
単一 IPv4 行という前提を修正し、成功した CI で native driver に IPv4/IPv6 の loopback2行、intermediary に IPv4 の1行、全行の所有一致を観測しました。
広い bind や所有外を許容した修正ではなく、PS 契約12 checks で wildcard、所有外、欠落の拒否も確認しました。
試験の再実行で成功を拾う設定はなく、根拠を伴う source 修正後の別 head として記録します。

画像は実 WebView の viewport です。
主画面と、診断を開いて内容へ scroll した画面を分け、他アプリや desktop を含めません。
これはこの runner/Runtime の debug executable の結果で、新 PC、配布 EXE/installer、全 Windows/WebView2 版、native keyboard/支援技術/OS 設定の合格ではありません。
既存44条件や Go/SSH/sidecar の終了条件は完了にしません。


## 失敗段階の観測

main `68ed17f2686e0ac4ae3c8976e944e047c7e7d389` の [run 40590119432](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40590119432) は、Node の一般エラーで native 成功0件になりました。
listener の loopback と所有一致、cleanup 済みは確認できましたが、主画面 PNG と WebView 結果 JSON がありません。
これは最初の撮影完了前に失敗した証拠であり、keyboard や refresh で失敗したと断定する材料ではありません。
旧 artifact には内側の例外や段階がなく、原因は未確定です。

以後の `native-observation.json` は、session 作成、origin、readiness、撮影、未配線契約、keyboard、結果保存を固定 phase で区別します。
request 種別、開始/正常/timeout/transport/HTTP/driver 拒否、経過時間、許可した driver error 区分だけを残します。
例外文、stack/cause、実行 path、session ID、URL、request/response body、資格情報、生ログは保存しません。
request は最大256件、phase 履歴は最大32件に制限し、上限後も最後の request と省略数を保持します。

request 15秒、全体60秒、readiness 10秒、worker1件と再実行なしの条件は変更しません。
観測追加を原因修正や実 SSH の成功へ読み替えません。
`webview-passed-awaiting-close` は UI/IPC 検査の段階結果であり、通常 close は従来どおり別の `native-result.json` で確認します。

## 初期documentの有限待ち

PR #60 head `f54038307716b17e1f7241d690d7fe15640543df` の配布run `40616726100` は、session作成後1回だけのorigin読取で `initial` を観測し、UI/IPC前に拒否されました。
通常nativeの同headは成功しています。旧証跡はorigin分類だけなので、実際のhrefがabout:blankだったとは断定しません。
同じcall siteとDriverにabout:blank→bundledのWebDriver応答を再生すると、同じ拒否を再現しました。

初期遷移の待ちは、originが文字列 `null`、hrefとdocumentURIが両方とも厳密な `about:blank` の場合だけです。
到達先は従来どおり `http://tauri.localhost` で、href/documentURIも同origin、資格情報なし、readyStateがinteractiveまたはcompleteであることを要求します。
同originでloading中だけは待ち、bundled観測後のabout:blank逆戻り、別scheme/host/port、browser error document、型不正、driver拒否は即時に失敗します。
HTTPSや証明書警告、security設定の変更を許可する条件は追加しません。

この段階はmonotonic時計で10秒以内、最大100観測、観測間隔最大100msです。
各WebDriver requestの期限も段階の残時間以下に縮めます。全体60秒・worker1件・試験再実行なしは維持します。
成功するまでの試験retryや、未許可originから後で戻ることを待つ処理ではありません。
観測は固定4分類、変化履歴最大16件と観測件数だけを追加し、生URLやdocument本文を保存しません。

新しいDriver境界の初期遷移と型不正拒否はred→greenを確認し、その他の拒否・期限・観測漏えいは修正後の回帰として区別します。
これは外部WebDriver応答fixtureを使ったharness契約試験で、Windows実起動や製品SSHの成功とは別です。
実CIは固定headとrunをPRへ記録し、無変更rerunで以前の失敗を消しません。
