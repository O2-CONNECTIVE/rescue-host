# 未接続 Tauri GUI の内部評価 ZIP

関連 Issue #25 / #9。旧 Tk 通信評価版とは別の配布物です。
Go / SSH / sidecar、開始と停止、製品 gate は変更せず、PR #34 P2 hold を維持します。
この工程は P2 再現・実接続・製品配布の承認ではありません。

専用 `tauri-evaluation.yml` は固定 checkout SHA から Windows x64 release を build します。
Node / Rust は既存 CI と同じ固定版、npm と Cargo は既存 lock と公式 registry を使います。
packaging の Python も既存 CI と同じ 3.13.17 x64 に固定します。
frontend notice と Windows x64 / custom-protocol の Cargo resolve graph にある registry package の license 本文を収集します。
後者は build / dev も含む保守的な集合で、リンク済み SBOM ではありません。
metadata は Cargo の正式な filter-platform を使い、未使用の別 OS package は収集対象にしません。
Windows graph 内の不足を除外や skip で成功へ変えません。
固定 Rust 1.99.1 の実配布に付属する COPYRIGHT.html / COPYRIGHT-library.html を丸ごと同梱し、各本文の hash を記録します。
これは compiler と runtime の保守的な notice 集合で、最終 native import の検査も別に行います。
license 本文が欠けた場合は失敗とし、名称だけで代用しません。
公式 crate に本文が未収録の固定5件は、crate に記録された VCS commit の upstream LICENSE、
または selectors の固定ソース本文が指定する Mozilla 公式 MPL 2.0 全文を補います。
crate checksum、版、SPDX、VCS commit/path、本文 hash を照合し、異なる revision や未登録 crate は拒否します。
selectors は参照元 lib.rs の全 bytes hash も照合します。全 crate の未改変 source 取得 URL と checksum を同梱します。
確認済み本文3件は `.gitattributes` で改行変換せず、Windows checkout でも元の bytes を保持します。
Microsoft native Loader は wrapper MIT と区別します。公式 NuGet SDK 1.0.3800.47 の x64 static library が
crate 内の library と bytes/hash 一致することを取得時に確認し、CI でも crate 内 library hash を照合します。
同SDKの LICENSE / NOTICE / nuspec を無改変で同梱し、package URL/hash と各本文 hash を記録します。
[公式の該当版license](https://www.nuget.org/packages/Microsoft.Web.WebView2/1.0.3800.47/License)の著作権・条件・免責を保持し、Microsoftの推奨・署名を示す表記はしません。
これら3原本も改行変換せず保持します。SDK のインストールや Runtime の再配布・更新は行いません。
不足は compile 前に全件検出し、欠落を無視して再試行しません。
ZIP には EXE、license、両 lock、source SHA と個別 hash の manifest、日本語手順を含めます。
署名のない EXE だけを対象とし、credential や installer を作りません。

ZIP 自体を新しい runner temp へ展開し、全 file hash と未接続境界を確認してから、
既存 native smoke の任意 `ApplicationPath` でその EXE の表示 / 実 Rust IPC / 通常終了を試します。
既存呼び出しは従来どおり debug EXE が既定です。新しい経路で成功したと推定しません。
実 Runtime と固定 driver の不一致では更新せず停止します。
通常終了判定後に自己所有 driver を回収し、結果が成功した時だけ評価 ZIP を artifact 保存します。
hosted runner 自体は使い捨てで、既存 CI ホストや新 PC へ接続しません。

`scripts/tests/test_tauri_evaluation_package.py` は CLI から生成 ZIP を読む配布ファイル境界です。
最初の未実装の非0から source 対応・閉じた gate・hash を含む ZIP の green、
次に入力外 license symlink の受理 red から拒否 green を確認しました。
独立レビュー後、空の license 子directory だけの受理も red から拒否 green を確認しました。
NOTICE だけの crate 拒否は修正後の回帰で、TDD とは区別します。
fixture bytes は Windows EXE ではなく、この試験を native 起動成功と数えません。
既存 native smoke は後付けで再利用する回帰で、新たに TDD を行ったと改称しません。

新PCでの互換性と既存WebView2版は未確認です。
source manifest の asInvoker / uiAccess=false と、配布 EXE の実測は区別します。
WebView2 / VC runtime の不足では自動導入せず報告します。unsigned の警告を回避しません。
利用者から実機起動と表示の確認報告があります。現在の確認範囲と配布CIの失敗は[検証状況](status.md)を参照してください。
keyboard、profileへの作用、終了後の残留は未確認で、表示確認を実SSHやOS保護の保証へ広げません。
操作は同梱 [日本語手順](tauri-evaluation-readme.txt) に限定します。
既存の起動と表示の確認を再度必須にせず、候補と環境の差分を確認します。
実機の事前確認、実装ownerへの確認事項、実行時承認、受け入れと撤去は[Windows限定評価手順](windows-evaluation-runbook.md)を正本とし、本書の未接続GUI評価と実SSHの評価を分けます。

配布候補ごとに完全 source SHA、ZIP / EXE hash、CI URL、実行数と終了結果を対応付けます。
native 0 件、失敗、timeout、取得不能を合格にせず、失敗 artifact は JSON 証跡だけです。
Library 保存は最終候補レビュー後の別作業で、GitHub Release / 自動更新は行いません。
