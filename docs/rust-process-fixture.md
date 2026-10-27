# Rust の所有子プロセスを検証する準備

[通信契約](communication-contract.md) / [未配線 shell](tauri-native-shell.md)

Issue #25 / #9 の GUI と子プロセス間の試験準備です。
製品から参照しない Cargo の `harness = false` integration test で、自分自身の既知の有限モードだけを子として起動します。
UI、Tauri command、capability、製品 gate、Go/SSH は変更しません。
任意実行 path、引数、shell、listener、鍵、ネットワーク、子孫プロセスは対象外です。

## 今回の試験専用契約

`fixtureVersion: 1` の boot と final を stdout の1行JSONとして使い、固定の停止要求だけを stdin に送ります。
これは製品 protocol や既存評価 fixture の protocol を採用したものではありません。
所有 `Child` の PID と boot、順序、版、構造、資源上限を確認する設計です。
停止要求の書込み、final 応答、exit code 0 と終了回収を別々に確認し、欠落や確認不能を成功にしません。
実 Go の artifact 検証、起動世代、再送、認可、期限、Windows Job、親死、子孫終了は未実装・未検証です。

## 最初の縦断

自己生成の無害な有限子が起動し、停止要求を受け、最終応答と正常終了を確認する1件から赤→緑を進めます。
ローカル Rust はないため、既存 Windows hosted CI の Cargo test を実行証跡とします。
赤の commit は launcher 未実装で失敗する予定で、動作確認済みとは扱いません。
追加の不正入力と予算切れは、最初の縦断が通ってから1件ずつ加えます。

[Rust Child](https://doc.rust-lang.org/std/process/struct.Child.html) は drop だけでは終了や回収を行わないため、所有した Child を明示して扱います。
[Cargo の harness 設定](https://doc.rust-lang.org/cargo/reference/cargo-targets.html#the-harness-field)で test 専用の main を使い、製品 binary に fixture mode を追加しません。
