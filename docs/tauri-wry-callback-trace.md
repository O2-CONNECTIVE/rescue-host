# 試験専用Wry callback観測

関連 #91 / #102 / #105。#105の同一診断release失敗では、app内の保守的cutoffまで約14.842秒、build戻りmarkerが未観測だった。
driverは同じ試行でheader未到達。これは内部の待ち場所やOS原因の証明ではない。
#102/#105のFAILを保持し、自動mergeや成功までの再実行はしない。

## 仮説と観測境界

1. environment APIが戻ってもcallbackが未観測なら、窓の局所化はenvironment callback前。
2. environment waitが戻りcontroller API後のcallbackが未観測なら、局所化はcontroller callback前。
3. 両waitが戻った後もapp build戻りが未観測なら、さらに後段のinit/組立て境界が未観測として残る。

いずれも窓内のmarker不在を指す。callback内の成功、message pumpの原因、CPU時間を推定しない。
API開始markerは呼出直前、API returnは呼出が正常returnした後、callbackはhandlerに入った直後。
wait-returnは外側wait呼出が正常returnした後で、内側Resultの成功を示さない。HRESULTや原例外は保存しない。
callbackがAPI returnより先に入る順序も許容する。環境再利用は別markerで扱う。

| 固定stage | 観測 |
|---|---|
| webview-enter | Wryのnew_in_hwndへ入った |
| environment-reused | 既存environmentを選んだ |
| environment-api-start / environment-api-return | 環境作成APIの呼出前後 |
| environment-callback / environment-wait-return | 環境callback入口とwaitの外側return |
| controller-api-start / controller-api-return | 選択されたcontroller作成APIの呼出前後 |
| controller-callback / controller-wait-return | controller callback入口とwaitの外側return |

APIの即時errorや外側waitのerrorはreturn markerを通らない。marker不在を無限待ちの証明へ昇格しない。
callback開始後のsendや内側Resultの成功は追加観測していない。
WebView作成はSTA/message pumpを使うが、今回pumpや呼出順は変更しない。
[公式threading資料](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/threading-model)と
[固定版Wryソース](https://docs.rs/crate/wry/0.57.1/source/src/webview2/mod.rs)を参照した。

## 来歴と配布境界

公式 https://static.crates.io/crates/wry/wry-0.57.1.crate を既存Cargo.lockのSHA256
`759c50ddad0ef9913bb5496cb9d509a50433a3b78cb0238d61105526589cf0e9`へ照合する。
`tools/wry-startup-trace/pins.json`自体のhashを適用scriptとconsumerで固定する。
置換差分、追加Rust、変更3fileの前後hash、全upstream file集合を検査し、未知fileや無関係変更を拒否する。
診断CIの自己所有tempだけへ展開し、Cargoの一時path patchで選ぶ。
元lockの固定hashを検査し、Wry1entryのregistry source/checksumだけを外した固定postimageへ変換する。
依存版を更新せず、`--locked`とbuild後の全source/lock再検査を行う。
通常配布workflow、repoの追跡Cargo.lock、製品featureは変更しない。

appのBUILD.jsonは既存startup診断featureの契約のまま。Wry patchは別provenanceでsource SHA・EXE hash・patch hashへ結び付ける。
consumerは固定来歴、実EXE hash、回収証跡を照合し、不一致は拒否する。
これは信頼されたCI filesystem上の整合検査で、署名・新しいtrust root・同一user攻撃者への隔離ではない。
artifactには匿名reportだけを残し、診断EXE/ZIP/原driverログは配布しない。

## 時間・回収・不完全性

Wry probeは最大10枠のmemoryへtry_lockで記録する。UI/callbackでI/O・join・mutex待ちを行わない。
最初のWry入口で既存環境変数を読み、専用workerを1本だけ開始する。worker起動自体のコストは残る。
workerだけが20ms間隔、最大120秒で既存app-startup.finishまたはcontroller wait-returnを待つ。
その後同じWry Instantの保守的cutoffを採り、記録を止め、in-flight記録を最大100msだけ待つ。
create-newで固定wry-startup-stages.jsonを最大2KiB出力。directoryや既存fileは作成/上書きしない。
同じprocessの複数WebViewは固定10枠と重複stage検査でincompleteになり得る。今回fixtureは1WebViewのみ。

harnessはappとWryのclose済fileを同じ既存1秒diagnostic budgetで確認し、各結果を別fieldへ記録する。
元の15秒単一POST・Node/normal-close・全体harness期限を延長しない。
Wry時計とapp/driver/Node時計を差し引かない。欠測・overflow・後着event・count/順序不整合・不正UTF-8/余剰fieldはincomplete。
I/Oの非returnに厳密な実時間保証はない。回収失敗は局所化できないまま残す。
正常close成功と強制cleanupを区別し、cleanup0でもWebView相関nullなら全子process消滅は未証明とする。
実機/OS設定、サービス、権限、実SSH、接続gateを変更しない。

## 検証の種類

公開分類の先行RED `121fecd` → 最小GREEN `08a6825`。元Windows障害の再現ではなく分類のTDD。
その後の余剰field・順序・CLI hash拒否は実装後回帰。
`node --test tests/wry-startup-stages.test.ts tests/wry-startup-files.test.ts`を使う。
stdlib record入口の3つの有限fixtureはCOMを呼ばず、producerの実file/closeを確認する。
Windowsの実callback・EXE・元のnative合否は、新headのCIで別に判定する。
