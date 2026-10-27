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
