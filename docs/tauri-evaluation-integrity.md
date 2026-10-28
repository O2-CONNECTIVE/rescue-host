# 評価ZIPの整合性とbackend同梱の引継ぎ

関連 Issue #25 / #9。[現行配布工程](tauri-evaluation.md)の追加検証。
この版はWindows x64の未接続GUIのみを対象とし、backendを同梱・起動しない。
Go/runtime/Rust reader、製品gate、既存の接続条件を変更しない。

## 現行の検証入口

`scripts/verify_tauri_evaluation.py`は、ZIPを展開・実行せず、次の期待値を呼出元から受け取る。

- `--archive`: 検証するZIP
- `--source`: 完全な40桁のsource commit
- `--archive-sha256`: 別途記録したZIPのSHA256
- `--exe-sha256`: 固定sourceからbuildしたGUI EXEのSHA256

CLIは正常なら0、不整合なら非0で終了し、stdoutに1件のJSONを出す。
source、ZIP、EXEの対応、manifestの未接続・gate閉・release・unsignedという宣言、
正確なmember集合、ファイルhash、GUIと必須ファイル、dependency一覧が参照するlicense本文、
Rust compiler/library noticeとfrontend noticeを確認する。
現行で許可していないbackendをmanifestへ追記しても拒否する。
backend欠落を許す将来の接続版モードは追加していない。

展開前に重複名、大文字小文字の衝突、symlink等の非通常member、暗号化member、
親参照・予約名・末尾dot等のWindowsで曖昧になる名前を拒否する。
現行生成物のASCII名だけを許可する。
[Microsoftの命名規則](https://learn.microsoft.com/en-us/windows/win32/fileio/naming-a-file)を参照した。
ZIPは64MiB、展開後の申告合計128MiB、各member32MiB、4096 memberまでに制限する。
hashと内容検証は一度取得した同じbytesへ適用し、検証中にpathを再読込しない。
これは汎用の安全なZIP展開器ではなく、この配布形式の検証器である。

既存CIではbuildしたEXEのhashと固定checkout SHAを外部期待値にし、ZIPを検証してから展開する。
展開後の従来hash確認とnative起動も残す。前者は実際に展開されたbytes、後者は実UI/IPC/通常終了の境界であり、ZIP検証だけで代用しない。
検証JSONはZIPの外側のCI artifactへ保存する。

## 信頼と診断の限界

manifestと同じZIPにあるhashやsidecarは署名・信頼根ではない。
同じ配布元のZIPと期待hashの両方が差し替わる攻撃は、このチェックだけでは検出できない。
固定CI/buildと照合先を信頼する前提を保持し、署名鍵・credential・新しい固定manifest信頼根は作らない。

source対応は固定checkoutのbuild工程と照合する宣言であり、EXE内の埋込みsource versionを検証したものではない。
`unsigned`もmanifest宣言であり、CIの実EXE Authenticode検査とは区別する。
PE import、WebView2/VC runtimeの実在と互換性、既存profileへの作用、実SSH認証・転送・失効の証明は別途必要である。
新PCへ接続せず、runtimeを自動導入しない。

検証JSONは固定check/status/code、正常時のsource/hash/件数と閉鎖境界だけを出力する。
ZIPの未知フィールド、license本文、ファイル名、ローカルpath、環境変数、credential、原ログや例外原文を転記しない。
未知のmanifest診断fieldは拒否する。
これは製品の診断bundle生成器ではなく、任意のファイル・license本文に秘密がないと証明するscannerでもない。
製品diagnosticsのallowlistや保持期限は別契約で、ユーザーprofileやstateを丸ごと収集しない。

## 実backend同梱前にownerが確定する契約

1. GUI/backendそれぞれのcomponent名、固定相対path、製品版・IPC版、完全source SHAとdirty状態。
   同一sourceが必要な箇所と、別版互換を許す場合の規則を明記する。
2. build出力から独立に記録する各binary hash、runtimeの版照会結果とsource/IPC対応。
   manifestの自己申告だけで一致としない。現在は埋込み版照会・信頼根が未契約。
3. 接続版で必須のbackendを欠いたZIP、重複、旧版混在、別source/hash、gate不整合の拒否。
   現行validatorはbackendを含む構成自体を拒否するため、そのまま接続版の検証へ流用できない。
4. backendのGo toolchain、go.mod/go.sumと推移依存license/runtime notice、対象OS/arch、
   PE importや必要DLL、動的依存の検証。現Rust/frontend inventoryをGoの証拠へ流用しない。
5. GUIからの自己所有子process起動・停止、IPC認可と失効、TTL・close故障、診断allowlist、
   cleanupと残留の実行証跡。静的hash一致から動作成功を推定しない。
6. 配布先でのhash照合元と更新/署名方針。信頼根・signing・credentialは必要性と具体操作を別途確認する。

これらの不足を理由に未完成runtimeを同梱したり、製品gateを解除したりしない。
既知のdouble-close NOT_MET、Windows背圧未検証、P2 holdを維持する。

## テストの区別

CLIからpackagerで作ったfixture ZIPを検証する。
余剰backend、manifestで承認されたbackend、license本文欠落、未知診断field、
Windows曖昧path、重複/別名/symlink拒否は、それぞれ失敗を先に観測して最小修正した。
外部source/ZIP/EXE期待値、gate閉鎖とGUI欠落は実装後の回帰であり、TDDと改称しない。
fixture bytesは実行物ではなく、実EXE/backend試験の件数に加算しない。
既存評価ZIPのread-only検証とCIの実native起動結果も分けて記録する。
