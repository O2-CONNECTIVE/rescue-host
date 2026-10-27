# SSH セッションの製品 IPC 最小仕様案

[通信契約と既存実装の観察](communication-contract.md) / [脅威モデル](threat-model.md) / [Decision #36](https://github.com/O2-CONNECTIVE/rescue-host/issues/36)

**比較用の仕様案・未採用・未実装です。**
基点は公開 main `72e526e84a13db96a0a4d53f84d0644f80c9d38e` です。
本書は既存の責務を具体化する候補で、DEC-01（所有と終了保証）・DEC-02（契約正本）を決定しません。
製品 gate は閉鎖し、[PR #34](https://github.com/O2-CONNECTIVE/rescue-host/pull/34) の P2 hold と既知の未達・未検証を維持します。
2026-10-28 12:00 JST の実接続に向けても P2 hold が残り、本書や CI の成功では解除できません。
既存44条件の定義・合否、配布・本番利用の判定を変更しません。

## 適用する境界

| 経路 | 位置づけ |
| --- | --- |
| WebView → Rust | 狭い typed command の候補。Rust が入力と caller scope を検証し、任意 shell/path/引数/接続先/資格情報を受け取らない |
| Rust → Go | 本書の主題。所有する起動・方針・世代に束縛した制御経路。認証・保護主体と実 transport は未決 |
| Go → Rust → UI | Go の状態と Rust の観測状況を別に返す。UI へ生のログ、例外、鍵、接続 command を転送しない |
| 現在の shell v1 | [#48 の固定未接続契約](shell-status-contract-fixtures.md)。製品版交渉や start/stop は提供しない |
| Rust 有限自己子 | [#50](https://github.com/O2-CONNECTIVE/rescue-host/pull/50) の試験専用契約。製品 wire 版、IPC 認証、実 Go 終了の証明ではない |

認可、鍵集合、固定宛先、最大期限、lease、失効は [Go の既存責務](../SECURITY_BACKEND_CONTRACT.md)です。
UI の表示、確認ボタン、残り時間、Rust の command 受付を認可根拠にしません。
最小化の比較案は「1つの検証済み backend 起動に1セッション、終了後の再利用なし」です。
この寿命と資源台帳の所有者も DEC-01 の採用判断に含めます。

## 版確認から開始までの候補

1. 保護された所有者が、固定 artifact の版・hash・所在と起動権限を照合する。UI に実行 path を指定させない。
2. 所有した接続と backend 起動世代を束縛し、固定 bootstrap 形式で対応版を確認する。準備完了前の start は拒否する。
3. 両側が release ごとの信頼済み許可版集合から同じ1版を明示選択・確認する。未知版、共通版なし、確認不成立は閉鎖する。
4. 選択版が要求する schema・権限制約をすべて満たした時だけ制御受付へ進む。低い版、旧 CLI、評価 protocol への暗黙 fallback はしない。
5. start は検証済み不変方針への参照だけを扱い、Go が実際の Identity・方針・期限と準備条件を再検証する。受付応答と active を区別する。

bootstrap と選択後の envelope は別に上限を持つ必要があります。
bootstrap は選択確認までとし、各方向4 frame以内の有限手順を候補にします。交渉ループや自動 downgrade を行いません。
具体的な bootstrap 形式、製品版番号、許可版集合、artifact 信頼元と互換保証期間は未決です。
shell の `protocolVersion: 1` や fixture の `fixtureVersion: 1` を流用しません。
版確認と PID/pipe/hash の照合だけでは、同一ユーザー攻撃者への認証・隔離は成立しません。

## メッセージの候補と上限

以下は採用判断と parser 試験の出発値であり、現在の実装上限や承認済み製品定数ではありません。
raw JSON の実行例や schema は、正本・版・transport の決定まで追加しません。

| 項目 | 候補 |
| --- | --- |
| framing | UTF-8 の JSON object を LF で区切る。1 frame 1 object、BOM・不正 UTF-8・途中 EOF・余剰 JSON を拒否。stdout を一般ログと混在させない |
| frame/構造 | 両方向とも bootstrap を含め LF 込み16 KiB、構造の深さ8、ID は128 UTF-8 bytes以下、通常文字列は1 KiB以下。kind 別の field/配列件数も固定する |
| 受信/送信待ち | 方向別のアプリ queue はそれぞれ64 KiBまたは16 frameの先着上限。現在解析中の frame と OS pipe buffer は別途計上し、全メモリ上限と混同しない |
| 事前制限 | 読み取り・確保前に上限を適用し、超過検出用の1 byteで拒否。上限到達を正常 EOF にせず、無制限 buffer や無制限 queue を使わない |
| 厳密な parse | kind ごとの未知/重複/欠落 field、型不正、非有限値、未知 enum、数値範囲外を拒否。言語間で field 名・大文字小文字を完全一致にする |
| envelope の論理項目 | 選択版、kind、backend 起動世代、送信方向ごとの連番、要求 ID、必要な Go Identity、型の決まった payload。任意 map や拡張 command は受け取らない |
| 整数表現 | epoch/連番の uint64 は正規の10進文字列を候補とする。先頭0、符号、指数、小数、桁あふれを拒否し、JavaScript の Number へ丸めない |
| 情報開示 | 状態・安定 code・必要最小の識別子に限定。秘密鍵、raw command、実行 path、raw error/log は制御応答・公開診断に含めない |

16 KiB は方針や鍵の blob を渡すための枠ではありません。
方針参照の解決と許可済み鍵の投入は保護側で定義し、UI 由来の任意 file/URL を解決しません。
上限で停止要求を捨てたり、通常 queue の背圧で失効が止まったりしない経路が必要です。
停止用予約枠だけでは parser 停止や start の blocking IO は解消しないため、独立した失効・所有回収手段が DEC-01 の採用前提です。
停止用予約枠や追加制御経路にも有限上限が必要です。通常枠との配分と総量を決めるまでは、上の queue 値だけで設計済みと扱いません。

## 操作と状態の意味

| 操作の論理名 | 候補の意味と拒否条件 |
| --- | --- |
| start | 検証済み版・起動・不変方針で1回だけ準備する。受付は接続許可ではない。Go の active と必要な準備証拠まで接続情報を公開しない |
| snapshot | 同じ起動・Identity の完全な状態を読む。新しい認可、lease 更新、期限延長、副作用の再実行を起こさない |
| stop | prepared/ready/active の同じ Identity を不可逆に失効へ進める要求。start 処理中も受付可能にする。revoking/terminated から復活させず現状を返す。受付と通信/プロセスの終了は別 |

これは command の候補集合で、Tauri の新しい command 名や permission の追加ではありません。
通常処理は並行要求を制限し、snapshot は1件へ集約します。
start の長い作業と応答待ちが stop を塞がない設計を必須の検討条件とし、実現手段が未確定なら配線を止めます。
GUI から任意の renew/TTL 値を受け取る経路は作りません。
実運用に必要な lease 更新は、保護された更新者・同一 Identity・厳密増加連番を持つ別の契約が未決です。
この契約がないまま UI の poll を lease 更新へ置き換えず、既存の最大期限と短期 lease の責務を維持します。

| 事実の所有者 | 分けて表す状態 |
| --- | --- |
| Go | prepared/ready/active/revoking/terminated。既存 Go 状態の意味を維持し、wire 採用時に対応を固定する |
| Rust | 版確認中、応答待ち、停止要求済み、IPC 異常、所有終了の確認不能。これらを Go の認可状態へ変換しない |
| UI | 検証済み snapshot の表示と取得不能。接続中を安全保証と呼ばず、終了確認不能を終了済みにしない |

## 世代、順序、重複

backend 起動世代、Go Identity（LaunchID/Epoch/PolicyDigest）、request ID は用途が異なります。
保護された所有者と Go が生成・検証・束縛する手順は未決で、UI に権威ある値を自由生成させません。
名前や PID だけを起動世代と見なさず、終了回収も保持した所有 handle/台帳を使います。

- 選択確認後の channel の両方向で連番を1から開始し、連続した増加だけを受理する案です。bootstrap はこの連番に含めません。欠落・逆順・重複 frame・wrap は protocol 異常として閉鎖し、同じ channel で自動再同期しません。
- uint64 最大値で wrap/再利用しません。到達前に新規通常要求を閉じて失効/回収へ進む手順も必要です。新起動は旧起動の終了確認を飛ばす手段にしません。
- request ID は全操作共通の起動内 uint64 カウンタを候補とし、保護された所有者だけが新しい値を発行します。snapshot も同じ空間を使い、応答を要求と束縛します。event は同じ起動と Identity に束縛し、要求応答とは別に検証します。
- Go 側は受理した ID の最大値を起動内で保持します。新 ID は最大値より大きいものだけを受理し、schema/世代を検証した新要求の ID は業務拒否でも消費します。ID も wrap/再利用しません。
- 同じ request ID・同じ論理内容の start/stop は、保持した各1件の受付結果だけを返す候補です。比較には kind・Identity・方針参照等を含め、配送連番は除きます。副作用と期限を再実行せず、同じ ID・異なる内容は拒否します。再送 frame も新しい連番を必要とします。
- 最大値以下で保持した2件のどちらでもない ID は、同じ内容であっても拒否します。snapshot の旧 ID や、追加 stop の旧 ID もこの規則に含め、過去の payload 全体を記憶せずに再利用を拒否します。
- 再返却する受付結果に現在の active や接続情報を含めません。現在状態は新しく検証した snapshot から確認し、古い receipt を現在の認可にしません。
- 1起動1セッション案では最初の start/stop の受付記録を起動回収まで各1件保持し、途中 eviction、start の再作成・終了後の復活を許しません。新 ID の stop も同じ不可逆な停止に集約し、その ID は最大値へ反映します。記録容量不足で古い ID を新規として実行せず、無制限の再送台帳を作りません。
- timeout だけで start を再送・新規起動しません。stop の配送が不明なら、可能な検証済み経路で状態確認/停止を試み、独立した所有回収へ進む必要があります。自動再試行条件は未採用です。
- 破棄した UI/旧 pipe からの遅延 callback は表示に反映しません。現在の channel に異なる起動世代/Identity が現れた場合は、単に古い表示として無視せず protocol 異常にします。

## 不正入力と終了確認

不正入力、版不一致、世代不一致、順序欠落、上限超過、EOF/IPC 喪失では、新規 start・認可情報の公開・通常の制御受付を閉じます。
同一起動の正しい要求に対する処理中/既開始などの業務上の拒否とは区別し、業務拒否は副作用を再実行せずに返します。
既存セッションがあれば保護側が revoke/close と所有回収を要求しますが、pipe を閉じたことを既存通信の遮断証明にしません。
この失効経路まで故障した場合も終了確認不能を残す必要があり、実現前に製品配線できません。
終了処理中の期待された EOF は、検証済みの終端結果と所有終了を満たした場合だけ通常完了として扱います。

終了確認済みの候補条件は、同じ Identity の Go Close 成功（通信・予約・worker の終了）と、採用した所有範囲の物理終了証拠の両方です。
1起動1セッション案では、正常な終端結果、exit 0/回収、IPC reader/worker 完了と、必要な所有 process 群/Job 等の終了を別々に確認します。
必要な観測が取得不能、final 欠落、異常 exit、期限切れ、強制 cleanup だけの成功は終了確認済みにしません。
終了未確認の参照・回復情報の扱いは [回復処理](recovery.md) と未決 DEC-03 に従い、GUI close や鍵削除だけで消去しません。
内側の既実行操作、所有外 process、全環境での資源不存在や副作用取消を保証する表示はしません。

error は安定した code と秘匿した UI 文言を分け、再試行可否・所有終了の確実性を別項目として設計します。
版不一致/不正入力/世代・順序異常/受付競合/応答期限/終了未確認という分類を候補にし、既存 fixture code や Go の内部 error をそのまま採用しません。
code 一覧と retry 方針は DEC-02 の承認前に固定しません。

## 採用前に残る決定と検証

| 論点 | 必要な判断・証拠 |
| --- | --- |
| DEC-01 / #12 | 所有者・保護主体・IPC 認証・生成権限・台帳・途中失敗・独立失効手段。満杯/無応答でも停止可能かを含む |
| DEC-02 / #17 / #25 | 契約正本、schema/生成物/共通例、wire/bootstrap 版、互換と変更審査、状態/error 対応 |
| 予算と制限 | 上記候補値、各受付/準備/応答/失効/物理終了の個別予算、更新者と lease 更新契約。評価版の30/45秒や #50 の2/1/5秒を転用しない |
| 純粋試験の次段階 | 上限境界、未知/重複 field、整数桁あふれ、壊れた UTF-8/JSON、世代/連番/要求競合、不可逆失効を codec/state 境界で TDD。採用前はネットワーク・鍵・実行なし |
| 配線後の別受入 | artifact 改変/未対応版、所有回収失敗、背圧、親死、IPC 切断、停止の割込み、実 Go/SSH の両端観測。純粋試験で代替しない |
| 既存 blocker | P2 hold、L-R07 の未達、Windows 背圧と保護主体等の未検証。既存 Issue の定義・状態は更新しない |

今回の成果は文書とリンクの確認であり、新規 codec/state、通信、停止、認可の実装成功ではありません。
次にコード化する場合も、採用範囲を明示した純粋 codec/state から始め、製品起動は別の承認・安全受入に残します。

## 一次資料

- [RFC 8259](https://www.rfc-editor.org/rfc/rfc8259)：UTF-8、重複名の相互運用性、数値精度、parser の上限。上の具体値は本案の候補であり RFC の指定ではありません。
- [Go encoding/json の security considerations](https://pkg.go.dev/encoding/json#hdr-Security_Considerations)：重複 key、field 名照合、不正 UTF-8、整数精度の差を確認。`DisallowUnknownFields` だけで全拒否条件が成立したと扱いません。
- [Tauri security](https://v2.tauri.app/security/)：WebView/Core の信頼境界を確認。Tauri capability を Go 側の認可や保護主体の代用にしません。
