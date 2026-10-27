# 未配線の Tauri native shell

[意図と確認範囲](../../docs/tauri-native-shell.md) / [tokens の状態見本](design/README.md)

Rust が接続機能の未配線状態だけを返す開発用 shell です。
Go、SSH、sidecar の起動や停止は実装していません。
既存 Tkinter、旧記録の回復、製品接続ゲートと起動方法は変更しません。
開始と停止は常に無効で、通信終了は未確認のままです。

## ローカルのフロントエンド確認

Node 24 以上と導入済みの Chrome が必要です。
ブラウザーのインストール、Rust 導入、OS 設定変更はコマンドに含みません。
`ui/tauri` から実行します。

```bash
npm ci --ignore-scripts --no-audit --no-fund
npm test
npm run build
npm run test:browser
```

build は六つの許可したローカル資材だけを `dist/` へ複写します。
既存 `design/tokens.css` と `design/components.css` を再利用し、生成物をコミットしません。
ブラウザー試験は自己所有の loopback サーバーと既存 Chrome を使い、試験後に終了します。
`SHELL_SCREENSHOT_DIR` を指定した場合だけ、ブラウザーの実描画をその作業ディレクトリへ保存します。
IPC fixture は未配線応答の表示検証であり、Rust や実通信の成功証拠ではありません。

## 承認済みの Rust 環境での確認

Rust 1.99.1 と各 OS の Tauri ビルド前提が既にある環境で実行します。
この作業ではローカル Rust がなく、native コマンドは専用 CI で確認します。

```bash
cargo +1.99.1 test --locked --manifest-path src-tauri/Cargo.toml --features custom-protocol
cargo +1.99.1 build --locked --manifest-path src-tauri/Cargo.toml --features custom-protocol
cargo +1.99.1 run --locked --manifest-path src-tauri/Cargo.toml --features custom-protocol
```

起動する場合は先に `npm run build` を実行します。
通常のウィンドウ終了を使い、close handler から停止要求や終了確認を捏造しません。
ブラウザーの画面破棄と native の終了操作は別に確認します。
native ウィンドウの起動と close、支援技術、新しい PC での実動作は未検証です。

## 依存と配布

Node 24.22.1 と Rust 1.99.1 を専用 CI で固定します。
Rust 直接依存は Tauri 2.12.5、tauri-build 2.7.4、serde 1.0.229、試験用 serde_json 1.0.154 です。
これらは MIT または Apache-2.0 の選択制で、Cargo.lock で間接依存も固定します。
Playwright Core 1.64.1 は Apache-2.0 の試験専用 npm 依存で、native アプリには同梱しません。
アプリと自作 R アイコンはリポジトリの MIT ライセンスに従います。
アイコンは最小 shell の識別用の暫定資材で、ブランド採用の宣言ではありません。

CI は installer、bundle、署名、更新 artifact、Release を作成も公開もしません。
完全な依存 license 棚卸しと配布同梱物は[配布条件](../../docs/distribution.md)の後続です。
WebView2 未導入とオフライン Windows、macOS と Linux の native 表示差は未確認です。
