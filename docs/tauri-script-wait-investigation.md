# 起動timeoutの識別とscript登録待機

関連 [#91](https://github.com/O2-CONNECTIVE/rescue-host/pull/91) / [#116](https://github.com/O2-CONNECTIVE/rescue-host/pull/116)。
実SSH・製品接続gateは変更しない。15秒の単一session要求、native成功、通常close、残留確認の条件を維持する。
この文書のscript観測は試験専用で、根因修正や製品配布への組込み完了を示さない。

## これまでの証拠

時刻の起点は各probeで異なる。別clock・別試行の値を差し引いたり合計したりしない。

| PR / 固定head | 同一試行内の観測 | その時点で未確定のこと |
|---|---|---|
| #102 / 2287b3f8 | releaseのdriver forward→drop14.977831秒、headerなし | 接続assignedは送信完了やnative driver受信の証明ではない。app内部は欠測 |
| #105 / aa2640ae | releaseのapp build-start→cutoff14.842299秒、戻りmarker未観測 | build関数の実所要時間やNode15秒の内訳ではない |
| #108 / 65699e2e | releaseの同Wry時計でenvironment wait-return3.299720秒、controller API-return3.598524秒、cutoff14.906635秒 | controller callbackは11.308111秒未観測。OS/COM/pump/schedulingは未分離。別debugは成功 |
| #112 / c6084348 | debugはDispatch18/18、GetMessage19入口/18戻り、最後の入口からcutoffまで2.891544秒 | callback入口も未観測。物理block未証明。別releaseはcontroller wait-return11.987125秒で成功 |
| #116 / 7f6c0ac9 | debugはcontroller callback13.557269秒、wait-return13.557327秒。同app時計では15.246242秒build-return未観測 | この試行にcontroller waitが最後まで未returnという停止像は当てはまらない。前段遅延の予算消費は除外できない |
| #116 / 同headの別release | GetMessage22/21、Dispatch21/21、最後の入口→cutoff1.504363秒、callback入口未観測 | debugの後段像を割り当てられない。似たtimeoutだけで同一根因としない |

#102/#108/#112は10成功2失敗、#105は11成功1失敗、#116は8成功4失敗のsource job結果。
これらは異なる固定headの結果で、成功件数を累積して現行suite成功へ読み替えない。
#116の原関数Win32 fixture3条件成功は、sent callback後でもGetMessageがposted messageを待つケースを確認した。
実WebView原障害を再現した試験ではない。callback入口未観測の過去試行は、この後段の待ちだけでは説明できない。

## 反証可能な仮説と次手

1. **script完了送信後に元loopがreceiverへ戻らない。** 同じ操作のchannel send成功と、対応するwait未returnを必要条件とする。
   callback入口だけならsend前の停止・errorも残る。callback未観測の試行やinit-return観測では、この説をその試行の停止像から外す。
2. **非同期初期化の前段・後段が予算を消費する。** 同じWry clockの段階が進んでも作成退出が未観測なら候補として残る。
   send成功後だけの待機が見えた場合は、その局所的待機欠陥説との識別へ進む。cold startやCI CPU制約そのものはまだ証明していない。
3. **Wry以外の組立て・driver/session経路にも未完了がある。** Wry退出guard、app build、pageの観測が進んでもNodeがtimeoutなら候補になる。
   退出guardは直前の正常終了処理の観測で、呼出元まで物理的にreturnしたという証明ではない。
4. **同じ待機パターンが複数箇所に現れる、または異なる遅延がある。** 各試行の最後の完了境界を対応づける。
   一つのcallback未観測と別のscript待機を同一原因にも別原因にも即断しない。

判定を先に固定する。

| 新headの観測 | 判断と次の最小対照 |
|---|---|
| controller callback未観測 | script説をその試行へ適用しない。供給遅延の理由は未分離 |
| script入口、callback未観測 | 完了後のreceiver待ち説を支持しない。API/COM完了経路が候補 |
| callback入口観測、send成功未観測 | callback入口からchannel送信までの未分離区間。送信失敗と断定しない |
| 同一操作send成功、wait未return | 送信成功後に一度だけ自己threadをwakeするtest-only対照の前提成立。今回wakeは入れない |
| init-return観測、Wry退出未観測 | init呼出中のscript待機をその時点の停止像から外し、残る後段を調べる |
| Wry退出guard/app build/page観測、Node timeout | Wry作成停止説を外し、driver/session経路へ進む |

無変更head rerun、負荷での再現率調整、timeout延長、成功までのretryは行わない。
今回の通常権限・既存CIだけで製品待機問題とCI環境制約を完全分離できなければ、判別不能を保持する。

## 試験専用の限定展開

Wry0.57.1、webview2-com0.39.1、macro0.8.1の公式archive hashを既存Cargo.lockへ照合する。
macroのimpl_completed_callback関数bytesも固定する。依存版は増やさず、Cargo path patchはWry一つのみ。
元のcreate/Invokeによる一度限りのclosure取得と引数変換は再利用し、再実装しない。
script helperだけをchannel生成・API closure消費・send/expect・元のwaitの順に限定展開する。
元のAPI closureをBox/FnOnceとして消費し、COM cloneをwait前にdropする所有関係も照合する。
元completed closureのerror payloadをchannelへ送れた場合と、channel send自体の失敗を区別する。
send失敗のexpectを握り潰さず、API/wait/内側Resultのerrorも変換規則を維持する。
これは等価性を照合する設計であり、観測追加によるタイミング差や完全な実行同等性まで保証しない。
macro VCS dffa41a8a46d3f5565eefbff2de57d38d399f158のMIT licenseは既存PUMP_UPSTREAM_LICENSE.txtとbyte一致を確認した。

raw variantはwry-webview2-stage-3、依存variantはwry-0.57.1-script-3。
旧variantは新consumerで不完全とし、過去artifactへ後段状態を後付けしない。
固定12 event枠とenvironment/controller/scriptのpump snapshot、scriptの開始/API終了/callback/send/return数を記録する。
最後の操作の序数・同Wry時刻・callback結果の成功boolを結び、前操作の観測や欠測を混ぜない。
直近の登録returnだけでは次の登録に進む前の窓も含むため、登録領域全体の終了とは表現しない。
128操作上限や記録競合は診断をinvalidにするだけで、129個目のAPI/send/waitを止めない。
controller完了でsealせず、Wryの正常/Err退出guard・panic unwindまたは既存app finishまで観測する。
それぞれ別stageで、panicを作成成功にしない。cutoff後の観測は窓外であり、イベント失敗の証明にしない。
UI/callbackはmemory/try_lockだけ、既存writerのcreate-new/closeと回収1秒budgetを維持する。
script内容、ID、URL、handle、thread/PID、HRESULTや原例外、資格情報は出力しない。

## 検証の種類と限界

分類のRED d1e5a3f → GREEN baeaa35は公開診断境界のTDD。実WebViewの失敗再現ではない。
記録producerの14条件と来歴CLIの9条件は実装後回帰。COMを起動せず、fake binaryもhash入力だけで実行しない。
producer fixtureはcallback先行、error payload送信、送信失敗のpanic、複数登録、旧序数、129操作、早いseal等を確認する。
panic fixtureはstdlib内でRust unwindがguardへ届いた場合だけを検査し、実COM境界でのpanic/abort回収の証明ではない。
同じ14条件で2KiBの出力上界を検査し、producer/consumer/collectorの上限を拡げない。
実callback・native合否・所有資源のcleanupは新head Windows CIで別判定する。
cleanup失敗0でも全WebView消滅が証明されなければ未証明と記録し、hosted runnerの破棄を実測済みとしない。

一次資料:
- [GetMessage](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-getmessagea)
- [WebView2 threading model](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/threading-model)
- [固定Wry source](https://docs.rs/crate/wry/0.57.1/source/src/webview2/mod.rs)
- [固定macro source](https://static.crates.io/crates/webview2-com-macros/webview2-com-macros-0.8.1.crate)
