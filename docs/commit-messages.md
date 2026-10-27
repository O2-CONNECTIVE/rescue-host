# 日本語を基本とするcommit message

今後mainへ導入するcommitのsubjectと、存在する説明bodyを日本語ベースにします。
対象はcommit messageです。
PRのtitle/bodyは今回の検査対象に含みません。
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
stdout/stderrには原文を出さず、SHA、subject/bodyの区別、字数と失敗理由だけを出します。

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
