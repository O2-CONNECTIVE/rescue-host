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
実 Runtime と対応 Driver の固定 version/hash を確認してから、起動試験と0件拒否の受入を接続します。

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
