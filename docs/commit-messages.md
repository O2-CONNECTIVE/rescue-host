# commit messageとPR本文H1の日本語方針

今後mainへ導入するcommitのsubjectと、存在する説明bodyを日本語ベースにします。
commit messageに加えて、PR本文のH1見出しを一つずつ日本語ベースか検査します。
PRのtitle、本文の通常段落、H2以下は検査しません。
空bodyと、終端の有効な標準trailerだけのbodyは許可します。
subjectと説明bodyは別々に評価し、一方の日本語量で他方を相殺しません。

## 判定する文字

UTF-8のbyte数や「ASCII以外」の割合は使いません。
NFKCで半角カナ・全角英数・結合文字などを正規化し、Unicode category `L`の文字を説明letterとして数えます。
このうちUnicode名が`CJK UNIFIED IDEOGRAPH-`、`CJK COMPATIBILITY IDEOGRAPH-`、`HIRAGANA LETTER`、`KATAKANA LETTER`、`KATAKANA LIGATURE`で始まる文字を日本語候補として数えます。
句読点、空白、数字、emoji、結合記号はletterへ加算しません。
長音記号だけを並べても日本語候補を増やせません。
CIの固定版CPython 3.13.17ではUnicode database 15.1.0を使用します。
追加の言語判定依存はありません。

技術表記を以下の規則で除いた後、各欄に**日本語候補4字以上、かつ説明letter全体の1/3以上**を要求します。
2つの条件は両方必要です。
短い`修正`だけでなく、`終了処理を修正する`のように対象と変更を記載します。
開始main `68ed17f2686e0ac4ae3c8976e944e047c7e7d389`の直近60件から日本語を含むsubject 22件を固定fixtureにしました。
単純50%では技術語の多い日本語が落ちるため、下記の限定的な技術名除外と1/3を初期基準にしました。
4字の下限により、英語説明に漢字1字だけを足す方法は合格しません。

これはUnicode文字種によるヒューリスティックです。
漢字は複数の言語で共有され、漢字だけの文も合格し得ます。
自然言語として日本語を完全に認識する保証や、説明の内容・品質・真実性の保証ではありません。
Scriptと文字コードの違いは[Unicode Script Property](https://www.unicode.org/reports/tr24/)を参照してください。

## 技術表記とmetadata

| 表記 | 扱い |
| --- | --- |
| conventional prefix | subject先頭の`feat/fix/docs/test/refactor/perf/build/ci/style/revert/chore`と任意のASCII scope、`!`、`:`だけを除外 |
| merge prefix | subject先頭の`Merge pull request #数字:`だけを除外。後ろの説明は通常判定 |
| 製品名・技術略語 | checkerの`TECHNICAL_NAMES`に列挙した大文字小文字を区別する完全tokenだけを除外。例: Windows/Python/CI/UI/SSH/IPC。自由な英単語の許可リストではない |
| URL | `http://`、`https://`から始まるASCII URLを除外。直後の日本語説明は保持 |
| SHA | tokenとして独立した7–64桁の16進数を除外 |
| path/file | 空白を含まないASCIIのslash/backslash path、checkerで列挙した拡張子のfile名を除外 |
| inline code | backtick内が空白を含まないASCIIの1 tokenなら除外。英語の文を囲っただけの場合は文を集計する |
| fenced code | 行頭0–3空白のbacktickまたはtilde 3個以上で開き、同じ種類・同数以上の記号だけの行で閉じる範囲を除外。未閉鎖は失敗。indentだけのcodeは除外しない |
| 人物trailer | body終端のSigned-off-by/Co-authored-by/Reviewed-by/Acked-by/Tested-byで、`名前 <email>`形式に合う単一行だけを除外 |
| Issue trailer | body終端のRefs/Closes/Fixes/Related-toで、`#数字`の単独またはカンマ区切りだけを除外 |

trailer名は大文字小文字を区別しません。
未定義trailer、不正な値、説明の途中に置いたtrailerは普通の説明として集計します。
説明があるbodyを、code/URL/pathなどの除外後に「空body」へ読み替えることはありません。
全体をcode fenceで囲んだbodyは日本語候補0字として失敗します。
除外されたcodeの外側に残る説明も、同じ日本語量と比率を満たす必要があります。

## PR本文のH1

[PR見出しchecker](../scripts/check_pr_headings.py)は、PR本文の各H1へ**日本語候補1字以上、かつ説明letter全体の1/3以上**を要求します。
NFKC、Unicode文字種と限定的な技術表記の除外はcommit方針と共通です。
commit用の4字下限はH1へ適用せず、`# 概要`と`# API概要`は合格します。
`# Summary`は失敗し、日本語の別H1や本文を加えても相殺できません。
H1がない本文、空本文と未設定本文は許可し、H1の追加を必須にしません。

Markdownの対象は、ATX H1（`#`の直後が空白、tabまたは行末）と、段落に`=`だけの下線を続けるSetext H1です。
root、blockquote、bullet list、ordered listと、それらを入れ子にした内部を調べ、コンテナのprefixを除いた後の行頭0–3空白を許可します。
listの継続行にはmarkerと開始時の空白から求めたcontent indentを使います。
Setext H1の複数行は同じ見出しとして評価し、prefixを省略したlazy continuationだけでは新しい見出しを開始しません。
lazy continuationの本文は段落に保持し、後で明示的なprefix付きのSetext下線が続く場合は見出し全体の文字数へ含めます。
backtickまたはtildeを3個以上使うcode fence内と、コンテナ内のcontent indentに加えて4空白の独立したcode行を見出しへ数えません。
fenceは同じ記号を開始時以上の長さで並べ、後ろが空白だけの行で閉じます。
未閉鎖fenceは現在のコンテナの終端または文末までcodeとして扱い、外側へ戻った見出しを隠しません。
backtick fenceのinfo stringにbacktickを含む行は、fenceの開始にしません。
tabは構造判定時に4桁のtab stopで展開します。

raw HTMLの`<h1>`も対象で、`<div><h1>概要</h1></div>`のような内部のH1を検査します。
標準の[HTMLParser](https://docs.python.org/3.13/library/html.parser.html)でtextを取り出し、tag名、属性、コメントを日本語候補へ加算しません。
文字参照を復号するため、`<h1>&#x6982;&#x8981;</h1>`も日本語H1です。
`&lt;h1&gt;`、inline code、fenced code、indented code内のHTML表記はH1を作りません。
HTMLコメント、raw HTML block内の`# English`と、`script/style/textarea/title`などのraw text要素の内容もMarkdown見出しとして検査しません。
HTML blockは[CommonMarkの7種類の開始と終端](https://spec.commonmark.org/0.31.2/#html-blocks)を使い、通常のtagだけの行は段落の途中でblockを開始しません。

このcheckerは上記の構文を扱う依存追加なしの検査で、Markdown rendererではありません。
完全なlink参照解決、GFM tableやtask-listなど全拡張のblock相互作用、不正に入れ子にしたHTMLのDOM修復は実装していません。
標準HTMLParserのpatch差によって、不正HTMLをコメントとして扱うか固定理由で拒否するかは異なる場合があります。
[GitHub自身もGFM変換後に追加の処理とsanitizationを行う](https://github.github.com/gfm/#what-is-github-flavored-markdown-)ため、GitHubの最終DOMとの完全一致は保証しません。

workflowは`opened/synchronize/reopened/edited`で動き、PR本文だけの編集でも検査を再実行します。
[GitHubの既定PRイベント](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#pull_request)へ`edited`を明示的に追加し、既存commit検査も維持しています。
mainへのpushではPR本文検査stepを実行せず、新規commit範囲の検査を実行します。
PR本文は引用した`GITHUB_EVENT_PATH`を引数として渡し、PythonがJSONから読みます。
本文をActions式でrunへ挿入したり、shellで展開、evalしたりしません。
出力はH1の件数、位置、日本語候補数、letter数と固定の理由に限り、原文は出しません。

```sh
python -I scripts/check_pr_headings.py --event <PRイベントJSONのpath>
python -I -m unittest discover -s scripts/tests -p test_pr_headings.py -v
```

CLIで英語と日本語H1、fence、Setext、見出し内backtick、HTML block、引用とlist内のH1、raw HTML H1とtext抽出の順にRED→GREENを確認しました。
非段落block、段落を中断しないordered marker、Setext内のlazy continuation、backslash escape、fence終端のASCII空白と不正HTMLの原文非表示もRED→GREENで確認しました。
短い見出し、複数H1の独立判定、コンテナ内codeとコンテナ終端、対象外本文、文字比率、edited event、shell記号を含む本文と不正eventは後付け回帰検証です。

## 新規commitの範囲

[専用workflow](../.github/workflows/commit-message-japanese.yml)は`pull_request`とmainへの`push`で動作します。
full historyを取得し、GitHubのevent payloadから読み取った完全SHAを比較元と比較先にします。
branch名、PRに書かれた文字列、現在のcheckout HEADから比較範囲を推測しません。

- PR: `pull_request.base.sha..pull_request.head.sha`にある全commitを検査します。mainが先に進んでheadの祖先でなくても、共通祖先と両SHAが存在すれば検査できます。Actionsの合成merge commitはこの範囲へ入れません。
- main push: `before..after`の全commitを検査し、新たなmerge/squash messageも含めます。beforeはafterの祖先であることを要求します。
- 過去mainの祖先commitは遡って判定しません。導入前から未mergeのbranchにある英語commitも、新規導入範囲へ残っていれば失敗します。後の日本語commitで前の不合格は相殺しません。

対象0、履歴不足、存在しないSHA、共通祖先なし、null before/after、非線形main pushは成功にしません。
1回の範囲は1–1000commit、Git操作は各30秒、jobは5分を上限とし、超えた場合も失敗を報告します。
履歴のrewrite/force pushはこの導入に含みません。
既存の未merge commitをどう統合するかは、統合担当が対象範囲を確認して判断します。

## ローカル確認とmerge時の限界

commit候補をUTF-8 fileへsubject、空行、bodyの順に書いて確認します。

```sh
python scripts/check_commit_messages.py --message-file .work/commit-message.txt
python scripts/check_commit_messages.py --base <完全baseSHA> --head <完全headSHA>
python -m unittest discover -s scripts/tests -p test_commit_messages.py -v
```

別のrepositoryを調べる場合は`--repo <directory>`を指定します。
初期機能はメッセージ、技術表記、trailer、Git範囲、PR eventの順にCLIでRED→GREENを確認し、後続の境界・拒否fixtureは回帰検証として追加しています。
これは開発ツールの検証で、製品のSSH/UI TDDとは区別します。
stdout/stderrには原文を出さず、SHA、subject/bodyの区別、字数と失敗理由だけをASCIIで出します。

統合担当は実際に使うmerge/squashのsubject/bodyを同じ`--message-file`で確認します。
GitHubの標準`Merge pull request #N from owner/branch`は日本語subjectではないため、そのままなら失敗します。
例として`Merge pull request #N: 日本語で変更の要点を記載する`を使えます。
[GitHubはmerge時にmessageを入力・変更できる](https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/merging-a-pull-request)ため、PR CIだけでは未作成の最終merge messageを保証できません。
push CIの検出は実merge後です。
branch保護やorg設定の変更、mergeの技術的な強制停止、履歴の自動修正は行いません。

workflowはcontents:read、credentials非保持、固定SHA action、標準hosted runnerを使用し、PR codeへsecretを渡しません。
`pull_request_target`は使いません。
[PR eventのbase/headとActionsのGITHUB_SHA](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#pull_request)を区別して比較します。
PR自身はworkflow/checkerを変更できるため、その差分もレビュー対象です。
CIの自己変更に対する改ざん耐性や、必須checkの管理者設定を、このPRだけで保証するものではありません。
