# service要求の純粋codec

`rescue-host-service/1`の確定済み要求subsetだけを扱う。
`DecodeUntrustedFrame`は`UntrustedRequest`を返し、認証・今回の承認・dispatch・issuer・transportを呼び出さない。
このpackageは既存のservice入口へ接続しておらず、製品gateは閉鎖したままである。
同じbyte列を復号し直してもchannel状態を更新しない。

## frameと構造

LF込み16KiB以下の単一UTF-8 JSON objectを要求する。
LF欠落・複数行・余剰record・不正UTF-8・未知または重複field・型不正を拒否する。
field名はJSON unescape後に重複検査し、大文字小文字を完全一致させる。
JSONの空白を許し、上限ちょうどのframeも受け取る。

root objectの深さは1で、新しいobjectまたはarrayに入るたび1を加える。
scalarとkeyは深さを増やさず、空containerも数える。
深さ8までを許し、9はschema検査より前に拒否する。
`ErrFrame`、`ErrDepth`、`ErrSchema`はcodec内の失敗段階であり、未採用の応答schemaやwire error codeではない。

| type / command | 正確なfield |
| --- | --- |
| hello | protocol、type、sequence=`"0"`、requestId、clientNonce |
| requestの共通部分 | protocol、type、bootEpoch、channelNonce、sequence、requestId、command、body |
| status / prepare-intent / revoke-all-and-quiesce | bodyは空object |
| confirm-approval | bodyはintentId、previewDigestだけ |

requestIdとintentIdは小文字hex32、nonce・bootEpoch・previewDigestは小文字hex64である。
requestのsequenceは正の正規10進uint64文字列とし、数値・小数・符号・先頭0・overflowを拒否する。
最大uint64は構造として受け取り、有限channelのquotaやwrapを許可したとは扱わない。
bootEpochは既存issuerのinstance epochを表す相関値で、control/1 Identity.Epochへ流用しない。

## 状態・認証との境界

復号成功では、nonceの新鮮性、同一boot/channel、前回sequence+1、requestId再利用、保護profileの1..1024要求上限を検証しない。
これらは別のchannel状態検査の責務であり、古い履歴を捨てて再受理する実装も追加していない。
相関値や`UntrustedRequest`をcallerが構築しても認証・承認の証拠にはならない。
将来のdispatchには、同じreadへ束縛された不透明な認証contextと実行ownerの検証が別途必要である。

応答型、canonical preview生成、operator認証、復元失敗時のprocess全体の処置、quiesce成功の証明は未採用である。
既存control/1、bootstrap/1、localcontrol、supervisor authorityへ新しいcommandを追加しない。
service登録、OS権限、鍵作成、listener、SSHやP2専用試験を実行する経路もない。

## 共通vectorと証拠

`testdata/request-vectors.json`はownerが確定した共通vectorの無変更コピーである。
SHA-256は`7ea1133cae14d30f8c74f5690049e47f5ad7ebc460d8b6c0fbc67660dc1f94e5`。
要求boundaryの出典SHA-256は`0e15a5442b17c9be49ffcd9105f959d949d74358da7485c1b344129bb5796452`である。
共通正本の配置を変更する場合も内容を独自変更せず、他言語と同じvectorへ追従する。

正例7・負例21を公開decoderで検査する。
generic depthの2件はどちらもservice要求として不正で、depth8はparser通過後のschema拒否、depth9はdepth拒否であることを区別する。
全vectorの`authenticationEstablished`はfalseのままである。
通常unit/race・compileの結果を、実Windows認証・承認・保護停止やSSH成立の証拠へ読み替えない。
