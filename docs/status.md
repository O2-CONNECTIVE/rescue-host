# 提供段階と検証状況

[READMEへ戻る](../README.md)

2026-10-28に公開main `d84f2ccac36c276c48ac7db3d80ebc9e85205a47` と関連PRを照合した記録です。
**新規接続ゲートは閉鎖中で、本番利用できません。**
コードのmain統合、未接続GUIの評価、製品の遠隔接続とOS保護は別々に判定します。
変化するCI結果は[Actions](https://github.com/O2-CONNECTIVE/rescue-host/actions)と各PRの固定headで確認してください。

## 提供段階と証拠の読み方

| 提供段階 | 定義 |
| --- | --- |
| 計画 | 目標や仕様のみで、対象機能は未提供 |
| Alpha | 開発用の一部実装。制約があり、本番利用不可 |
| Beta | 対象用途を限定して試用提供する段階 |
| GA | 明示した対象環境と用途で一般提供する段階 |

現在の実装はAlphaの範囲で、BetaやGAとする機能はありません。
「部分検証済み」はモック、fixture、限定したCIや実機確認の証拠があることを示し、製品全体の合格を意味しません。
未接続GUI評価版を渡したことも、実通信版の提供開始には当たりません。

## mainに統合された範囲

| 対象 | 実装と確認範囲 | 未提供または未検証 |
| --- | --- | --- |
| Tauri GUI | 未接続状態、診断表示、状態再確認。browser試験、Rust IPC試験、限定したWindows native試験 | 開始と停止は無効。Go / SSH / sidecarと未配線 |
| Windows x64 GUI評価物 | sourceとhashを対応付けるZIP生成、license収集、展開EXEの検証入口 | 最新mainの配布CIは終了確認で失敗。新候補の配布可は未確定 |
| 既存Python版 | 設定保存、操作方針、期限と失効のオフラインモデル、旧記録の回復補助 | 実認可や確実な終了保証。GUI設定は管理者昇格の認可ではない |
| Go SSH基盤 | 鍵集合、固定宛先、期限、失効、所有接続、限定fixtureによる回帰 | 製品UIとの接続、実relay、内側SSHの操作認可 |
| Go制御入口とsupervisor core | stdio基盤、start拒否のdispatcher、純粋なgrant判定、制御契約のschemaとvector | 製品CLIは起動拒否。認証付きIPC、実service、OS上の期限執行と保護 |

Go基盤の詳細は[backend](../backend/README.md)、[stdio準備](go-stdio-preparation.md)、[制御契約ADR](adr/0001-control-contract-candidate.md)を参照してください。
既存の[構成図](architecture.md)はPython部分を示すもので、現在のGo実装全体の図ではありません。

## 未統合の候補

次の候補は照合時点でmain未統合です。
PR内の検証済みコードを、main実装や製品接続の成功として扱いません。

| 候補 | 確認した状態 |
| --- | --- |
| [Rust codec #60](https://github.com/O2-CONNECTIVE/rescue-host/pull/60)、[Rust coordinator #64](https://github.com/O2-CONNECTIVE/rescue-host/pull/64) | 限定CIと独立レビューを確認済み。実Goとの制御交換や製品配線は未確認 |
| [Windows service host #61](https://github.com/O2-CONNECTIVE/rescue-host/pull/61) | 未設定時の起動拒否と模擬制御を確認済み。実SCM登録、サービス稼働、tokenやACLの証明ではない |
| [Go codec #62](https://github.com/O2-CONNECTIVE/rescue-host/pull/62) | [fixture修正 #68](https://github.com/O2-CONNECTIVE/rescue-host/pull/68)の取り込みと最終CIを確認中 |
| [再起動後に許可を復元しないissuer #69](https://github.com/O2-CONNECTIVE/rescue-host/pull/69) | 単回発行の候補。取消競合の修正と最終検証を確認中 |

## OSと役割ごとの範囲

実リモート接続を製品として「対応済み」と呼べるOSはありません。

| 役割と確認対象 | Windows | macOS | Linux |
| --- | --- | --- | --- |
| host（アクセスを渡すPC） | 初期対象として開発中。実保護と実接続は未検証 | 未対応 | 未対応 |
| operator（操作側） | 構想段階 | 構想段階 | 構想段階 |
| Tauri GUI評価版 | x64の未接続評価物。利用者による実機起動と表示の確認報告あり | native評価未確認 | native評価未確認 |
| Python / Goの開発試験 | 固定したCI環境で限定回帰 | 固定したCI環境で限定回帰 | 固定したCI環境で限定回帰 |
| 製品の保護サービス、ACL、Job、認証付きIPC、実SSHの統合 | 未検証 | host未対応 | host未対応 |

CIのOSとCPU、toolchainは[Python CI](ci.md)、[Go CI](go-ci.md)、[Tauri手順](../ui/tauri/README.md)とworkflowを正本とします。
3 OS CIの成功を、各OSのhost対応や全OS版とCPUの互換性へ広げません。
実機起動と表示の報告は、実SSH、停止の強制力、OS保護、DPIや支援技術の全面検証を含みません。

## 公開証跡と現在の失敗

照合したmainでは[Python](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40621486881)、[Go](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40621486949)、[Tauri品質](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40621486956)、[Windows native](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40621486964)のCIが成功しています。
一方、同じmainの[配布CI](https://github.com/O2-CONNECTIVE/rescue-host/actions/runs/40621486976)は展開release EXEの終了確認で失敗しました。
buildとhash照合の成功を配布検証全体の成功へ読み替えず、子プロセスの終了観測を調査中です。
既存のmerge保留を維持し、他PRのgreenで解除しません。

未接続評価物の生成と照合は[評価版の説明](tauri-evaluation.md)、nativeの観測境界は[native smoke](tauri-native-smoke.md)を参照してください。
ブラウザー画像はRust IPCがない表示例であり、native起動や遠隔接続の証拠ではありません。
旧Python版の公開証跡は[検証集計](../verification/result.json)、[原ログ](../verification/unittest.txt)、[v0.2.1レビュー](../SECURITY_REVIEW_V021.md)に残しています。
過去の記録や候補ごとの成功を、現在のmain全体の成功へ流用しません。

## 初期検証の安全境界と残る条件

初期対象は、遠隔の非管理者ゲストから監督と停止処理を保護する範囲です。
host操作ユーザーまたは管理者が既に侵害された環境と、管理者権限を持つ遠隔ゲストは保証対象外です。
別主体のサービスに監督を分離する構想で、具体的なservice identity、token、ACL、正規承認、ゲストのIPC拒否は未実証です。
同一ユーザーの自己子、継承pipe、自己Jobの試験を、その保護の代用にしません。

初期評価ではPCや監督サービスの再起動後に許可をすべて失効させ、再承認を必要とする方針です。
保存UI状態や旧callbackからの自動再開は提供せず、実OSでの再起動と保護された承認口は別途検証します。
二重停止故障時の物理遮断の未達、Windows背圧の製品受入、実relayと内側操作、実service / ACL / Job / 認証付きIPC / 実SSHの統合検証は残ります。

[受入Issue #1](https://github.com/O2-CONNECTIVE/rescue-host/issues/1)、[安全要件](security.md)、[脅威モデル](threat-model.md)、[バックエンド契約](../SECURITY_BACKEND_CONTRACT.md)を参照してください。
既存受入の定義や合否をこの文書更新で変更しません。
接続を閉じても、既実行の変更や所有外のプロセスを取り消す保証はありません。
