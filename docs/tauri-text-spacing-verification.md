# 未接続画面の文字間隔と情報欠落の検証

[Issue #25](https://github.com/O2-CONNECTIVE/rescue-host/issues/25) / [UI 確認項目](design-accessibility-verification.md)

## 選んだ gap

現在の browser 回帰は320 CSS px と文字200%で文書全体の横幅を確認しています。
探索では320/360/1200 CSS px、文字100/200%、文字間隔変更の有無の12条件が正常でした。
既存画面が対応しているため、UI や token の値を変更しません。
一方、文書幅が正常でも、内部の overflow:hidden 等で説明文が切れて失われる場合は既存検査を通過します。
この情報欠落の見逃しを、今回追加する検証の対象にします。

## 検証する境界

診断を展開した現在の未接続 shell で、見出し、状態、許可条件、操作と無効理由、診断、終了注意の文字矩形を確認します。
文字矩形が hidden/clip の祖先から切れる場合や、見えない内容、横方向のはみ出しを検出します。
通常の縦スクロールは許可します。
汎用 a11y scanner、全フローの遮蔽検査、AA 達成判定ではありません。

ユーザーの文字間隔変更を CSSOM で模擬し、行高1.5倍以上、段落後2倍、字間0.12倍、単語間0.16倍を与えます。
CSP を緩めず、実際に設定値が反映されたことも確認します。
320 CSS px と200%文字は別の参照条件です。
両方を組み合わせた条件は追加の stress 確認で、WCAG の定義を置き換えません。

[WCAG2.2 Text Spacing](https://www.w3.org/TR/2024/REC-WCAG22-20241212/#text-spacing)、[Reflow](https://www.w3.org/TR/2024/REC-WCAG22-20241212/#reflow)、[Resize Text](https://www.w3.org/TR/2024/REC-WCAG22-20241212/#resize-text)と既存 UI-07 に対応する限定検査です。
一時的な clip を注入する負例は checker の拒否能力を確かめる fixture であり、実装が現在壊れていることを示しません。
browser の CSSOM 模擬を native DPI、OS 設定、支援技術、新 PC の実測へ読み替えません。
Go/SSH/sidecar、認可や停止の根拠、製品接続ゲートには触れません。

## 優先度変更による保留

Windows 実機検証に向けた配線棚卸しを優先し、UI 改善を広げずこの差分を保留します。
現在の実装は、正常な現画面と注入した内部 clip を区別する検査です。
文字間隔の12条件は探索結果であり、CSSOM 模擬の恒久回帰や native の検査は未接続です。
独立レビュー、draft PR、最終 head CI は未実施で、製品 UI と tokens は変更していません。
