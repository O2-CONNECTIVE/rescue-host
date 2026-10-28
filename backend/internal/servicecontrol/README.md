# service要求・応答の純粋codec

`rescue-host-service/1`の確定済み要求・応答subsetの純粋な構造検査を扱う。
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
`ErrFrame`、`ErrDepth`、`ErrSchema`は共有codec内の失敗段階であり、応答body内のwire error codeとは別である。

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

応答の純粋codecとcanonical previewは下記の構造検証に限定する。
operator認証、復元失敗時のprocess全体の処置、quiesce成功のproducer証明は未採用である。
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

## 応答の純粋codec

`DecodeUntrustedResponseFrame`と`EncodeResponse`は`UntrustedResponse`だけを扱う。
server helloの相関値と要求上限、状態snapshot、表示候補、承認受付、quiesce受付、作業回収の主張、固定error codeを構造検査する。
応答型の値をcallerが構築した場合も、認証・同一issuer・今回の同意・実行や停止の権限は得られない。

要求と同じ`decodeFrame`で未知field・unescape後の重複key・16KiB・深さ8・型を検査する。
server helloはsequenceが`"0"`、要求上限が`"1"`..`"1024"`で、client helloとは別の正確なfield集合である。
その他の応答は正のuint64 sequenceと既知command/result/bodyの組合せを必要とする。
encoderは単一variantだけを許し、非active variantやhelloへのresponse field混入を拒否する。
有限なstring/sliceの上限をmarshal前に検査し、出力も同じdecoderで検証してcompact JSONとLFを返す。

| command / result | bodyの範囲と意味 |
| --- | --- |
| status / snapshot | servicePhase、消費済みgeneration、nullableなapprovalRequestIdとbinding、launchPhase、workloadCleanupConfirmed。Service内部観測の主張で、実SCMやSSH成功ではない |
| prepare-intent / intent-prepared | intentId、previewDigest、期限、preview。intent/boot相関、期限の大小、canonical digest一致だけを検査 |
| confirm-approval / approval-accepted | 要求IDと一致するapprovalRequestId。binding発行や起動の成功ではない |
| revoke-all-and-quiesce / quiesce-accepted | revocationApplied=true、workloadCleanupConfirmed=falseの主張 |
| revoke-all-and-quiesce / workload-quiesced | 両bool=trueの主張。codec成功はowner回収やplane/SCM停止を証明しない |
| 既知command / error | 固定codeだけ。内部error、任意detail、pathや資格情報を追加しない |

snapshotのgenerationはuint64の正規10進文字列で、消費済みcounterが正でもbindingがnullの場合を保持する。
nonnull bindingは正のgeneration、既存control/1と同じidentity形を持ち、counter以下であることを検査する。
identityのepochはuint64で`"0"`を許し、serviceのbootEpochへ流用しない。
launch失敗後にcleanup=trueでもlaunchPhase=`failed`を維持でき、成功へ読み替えない。
phaseとbindingの構造整合を検査するだけで、現在の同一owner観測を生成する経路はない。

`CanonicalPreview`はbootEpoch、intentId、configDigest、policyDigest、keySetDigest、allowedKeyFingerprints、target、permissionSummary、hardDeadlineUnixMsの順にcompact ASCII JSONを返す。
targetは`127.0.0.1`、文字列`"22"`、`direct-tcpip`だけ、guestClassはpermissionSummary内の`nonadmin`だけである。
鍵fingerprintは32byteのSHA-256を表すunpadded標準base64を使い、1..8件、重複なしのASCII昇順とする。
`PreviewDigest`は`rescue-host-service/1:preview` + NUL 1byte + LFなしのcanonical bytesをSHA-256にする。
これは内容相関であり、署名、同意、raw鍵とkeySetDigestの対応、未決のconfigDigest producer、現在時刻や期限内を証明しない。

応答の共通vectorは`testdata/response-vectors.json`の無変更コピーで、SHA-256は`9905eccb3a7d646f357b502ebe27510d3e7b0bada2d5ef9275b24e01f49068d8`。
正例17・負例68・generic depth2・canonical preview2を検査し、全例の認証・owner proof・producer送信許可のフラグはfalseである。
vector内のexecution欄は設計時点の記録を保持しており、この実装の試験結果はPR/CIで別途記録する。

固定error codeの認識は初期dispatcherの送信許可ではない。
`SERVICE_BAD_FRAME`、`SERVICE_UNSUPPORTED_VERSION`、`SERVICE_AUTH_DENIED`、`SERVICE_STALE_CONTEXT`、`SERVICE_REPLAY_DENIED`、`SERVICE_REQUEST_LIMIT`は予約codeとして構造上認識するが、対応する不正入力への新しい安全な相関を作って応答する実装はない。
認証、同一channel、pending request相関、有限replay、取消/停止優先、実issuerへのdispatch、成功receipt生成はこのcodecに追加しない。
製品gateは閉じたままであり、実OS・native認証・実SSHの検証結果として扱わない。
