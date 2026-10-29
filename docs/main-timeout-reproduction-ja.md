# 固定 main の session timeout 再現計画

2026-10-29 の main `1eda7a4e964d34a751dcc89509ee07b3482917a7` における Windows 評価 run `40862901342` では、release ZIP の展開と hash 確認後、session 作成の単一 POST が 15 秒で timeout した。driver は forward を始めたが応答 header を観測せず、document は未実行だった。元の失敗 artifact には EXE／driver 実行物がないため、本実行は同一 commit からの再ビルドであり元のバイナリそのものとは証明できない。

## 事前仮説と終了条件

- 実行環境または間欠的な初期化遅延なら、固定 source の別 runner で今回だけ期限内に完了することがある。1回の成功は常時失敗という限定仮説に反するが、安定性や根因を証明しない。
- 固定 source 側に再現条件があるなら、同じ初期段階で同様の timeout が再び観測される可能性がある。1回の再現も製品の恒常的欠陥を証明しない。
- 実行時の runtime、driver、runner image が異なる場合、差分を環境要因候補として残し、source だけを原因と決めない。

当該 head につき一度だけ実行する。結果は session 成功、15秒 timeout、別の失敗、観測不足のいずれかで終了し、成功まで再実行しない。session 成功後は元手順どおり GUI・IPC・通常終了の確認が続く。単一なのは session 作成要求であり、全 WebDriver 操作ではない。

## 境界と証跡

公開 main の完全 commit を checkout し、当時の lock・公式ソース・release build・ZIP 検証・固定 driver・同じ supervisor を使う。新しい診断 feature を製品 EXE に混ぜず、source、再ビルド EXE と ZIP の hash、driver provenance、runtime inventory、匿名の session・driver・通常終了／失敗情報を固定ファイルから保存する。原文 URL、秘密、profile、画像、生ログ、バイナリを artifact に含めない。元 EXE の hash が不明なため再ビルドの hash 一致は確認できない。元手順は製品内部 startup stage を持たず、既存の phase と driver の観測だけで比較する。欠測は incomplete とする。

専用 UUID temporary root で profile と出力を分離する。元 supervisor の異常時 cleanup は通常 close とは区別する。証跡の回収が成功し、通常 close、観測した子の終了、driver 終了、対象 listener と固有 driver executable の不存在を確認できる場合だけ root を削除する。未知・失敗時は unconfirmed とし、削除を試みない。GitHub hosted runner の終了による環境破棄は観測されたプロセス終了や root 削除の証明ではない。CI runner は一時環境であり、所有していないプロセスを停止しない。

この試験は #125 の A/B fixture とは別であり、その不成立を製品不具合として数えない。実 SSH、外部公開、実機操作、OS 設定変更、資格情報登録は行わない。
