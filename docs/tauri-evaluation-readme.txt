Rescue Host Tauri 未接続 GUI 評価版（内部評価専用・未署名）

旧 Tk 評価版とは別の Tauri GUI です。Go / SSH / sidecar は未配線です。
開始・停止は無効で、通信終了は未確認です。製品 gate と PR #34 P2 hold は維持します。
実 SSH、認証、転送、失効、本番利用の合格を示す版ではありません。

対象は Windows x64。Windows 11 Pro 22H2 x64 実機は本人確認待ちです。
既存の Microsoft WebView2 Runtime が必要です。不足時は停止し報告してください。
Runtime、依存 DLL、OS 設定を自動導入・変更する installer は含みません。
Rust、Node、Python、SSH 設定や資格情報の入力は不要です。

1. ZIP と提供 SHA256 を Get-FileHash で照合し、専用の空フォルダーへ全体を展開します。
   MANIFEST.json の sourceCommit と、提供された CI の対象 SHA を確認します。
2. 通常ユーザーで rescue-host-shell.exe を一回起動します。管理者起動はしません。
   SmartScreen / Defender / UAC / 未署名警告や遮断が出たら停止し報告します。
   保護解除、Unblock-File、「実行を継続」、policy 変更では回避しません。
3. 「バックエンド未接続」、開始と停止の無効、許可条件と通信終了の「未確認」を確認します。
   「診断情報」を開き SHELL_UNWIRED を確認し「状態を再確認」を一回操作します。
   キーボード Tab / Enter / Space と、文字・画面の収まりも本人が観測できます。
4. 右上の閉じる操作で通常終了し、今回 app と WebView 子の残留を確認します。
   取得不能や終了未確認を成功としません。名前による他プロセス一括終了はしません。

WebView2 は専用 app identifier com.o2connective.rescue-host.shell のプロファイル等を
ユーザー領域へ作る可能性があります。既存 Tk state の回復は行いません。
同じ app identifier の既存データがあれば上書き・削除せず、事前に報告してください。
GUI の CSP は外部接続を許しませんが、Runtime / OS 自体の通信やファイル作用の
完全な不存在を保証するものではありません。外部接続・予期しない動作は報告します。

1回5分以内で確認し、起動警告、OS / WebView2 の申告版、ZIP hash、画面操作、
終了後残留を観測済み・取得不能・未実施に分けて返してください。
秘密、既存 state / profile 内容、個人情報の映る画面全体は共有しません。
今回展開分の削除は終了確認後に行い、既存 Runtime / profile / 他者資源は削除しません。
