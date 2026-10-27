# Tauri の最小未配線 shell

[開発手順](development.md) / [設計基準案](design-accessibility.md) / [通信契約](communication-contract.md)

## 今回の意図と受入範囲

tokens → 共通部品 → 画面の最初の縦断として、Rust の狭い IPC から「バックエンド未接続、開始不可」を読み取ります。
既存[Issue #16](https://github.com/O2-CONNECTIVE/rescue-host/issues/16)と[操作性 #25](https://github.com/O2-CONNECTIVE/rescue-host/issues/25)の native shell 段階で、実通信を提供する変更ではありません。
視覚上は既存のライト tokens と部品を使い、主状態、許可相手と範囲と期限、目立つ停止、独立して開ける診断の順に読み取れる画面にします。

表示状態は「状態を確認中」「バックエンド未接続」「状態を取得できません」の三つです。
どの状態でも開始と停止は無効で、許可条件と終了状態は未確認のままです。
接続中、終了確認済、残り時間の成功表示は作りません。
ブラウザー表示で Rust IPC がなければ、その事実を示します。

受入は、IPC の失敗、不正応答、待機切れでも成功表示にしないこと、診断が IPC を待たずに開くこと、keyboard と focus と拡大の確認です。
画面破棄後の遅延応答を無視する UI 境界を検証します。
native のウィンドウを閉じる実操作とブラウザーの破棄試験は区別します。

Go、SSH、sidecar、資格情報、保存設定、旧セッション回復、配布、署名、更新、dark theme は非目標です。
既存 Tkinter の起動経路と製品ゲートを維持し、未公開 backend のコードを取り込みません。
認可、TTL、失効と終了の根拠は既存契約が所有します。

## IPC と権限

shell 専用 `get_shell_status` は `{ "request": { "protocolVersion": 1 } }` だけを受け付けます。
未知 field と未対応 version を拒否し、出力の型と値も UI で検証します。
この version は shell 専用で、未確定の Go 製品 protocol の採用ではありません。
Rust は未配線である事実だけを返し、既存の TCP や別プロセスの終了を観測しません。

application manifest と `main` 限定 capability で、この command だけを許可します。
shell、filesystem、外部 URL、イベント購読、プロセス起動の permission は追加しません。
bundled page だけを使い、CSP と navigation の制限、文字列の `textContent` 表示で外部ページと HTML 注入を避けます。
ウィンドウを閉じても通信終了の証明にはならないことを画面に示します。
sidecar の起動、親死、中断、停止のライフサイクルは未配線です。

## 検証と依存の扱い

TDD の境界は GUI controller から Rust IPC への要求と応答です。
ブラウザーの IPC fixture、Tauri の mock runtime による command 試験、native build、実機起動を別々に記録します。
fixture で実接続や実停止の成功を作りません。
システムに Rust を導入せず、標準 GitHub hosted Windows runner の専用 CI で公式 toolchain を使います。
CI は readonly、action SHA 固定、有限 timeout、secret なし、deploy なしです。
build が成功しても、新しい PC での起動、native WebView、支援技術、OS の DPI と高コントラストは合格済みとしません。

Rust と Node の版、lock、license、実行手順、最終結果は実装に合わせて追記します。
完全な依存 license 棚卸し、配布 bundle、WebView2 のない環境とオフライン配布は[配布条件](distribution.md)の後続で扱います。
