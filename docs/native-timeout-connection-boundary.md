# 配布timeoutの接続割当て境界

関連 #91 / #94 / #97 / #98。変更base eace5b51de439a933fac2f3afe386d4c16105c03。
製品アプリ・通常配布feature・15秒/単一要求・正常close/cleanupの合否は変更しない。

## 既存の証拠

main eace5b51ではGo/Python/日本語のpush CIだけが適用され、配布試験の新しい失敗はない。
直近の関連失敗は統合41e6d355のrun40736215191/job122985373995/artifact12838660149。
同一POSTは受理→forward141µs、forward→drop14.963266秒、Node timeout15006ms/document0。
driver stage1は9件完全、同一requestでheaders/native-error未観測、Node側connection-endはincomplete。
失敗時app1/WebView3の相関があり、cleanup失敗0/driver終了確認。全WebView終了は未証明。
通常配布の診断featureは無効なので、同じ失敗試行のapp stageはない。

同sourceの別run40736219494では診断debug/releaseが各成功した。
app build7.189511/6.080341秒、driver forward→headers7.232452/6.118991秒。
これは別variant・別process・別試行であり、失敗した通常配布EXEのapp stageとして結合しない。
成功各1件からUI変更や元timeoutの因果も推定しない。

## 最小の追加観測

既存のHyper-util0.1.19 capture_connectionを、選択POSTの変換済みrequestへ一度だけ付ける。
内部request extensionへのmetadata捕捉であり、wire header/body/URIやretryは変更しない。
header返却/error返却/service future dropの直前に、同じguardから一度だけmetadataのSome/Noneを読む。
request guardの寿命を超えた監視taskは作らず、追加listener、poll、非同期wait、raw logや新依存も増やさない。

| 固定stage | 観測の意味 |
| --- | --- |
| native-connection-assigned | 終端でHyperがこのrequestへ割り当てた接続metadataを観測できた |
| native-connection-unobserved | 終端までにmetadataを観測できなかった。OS接続失敗やEdgeDriver未受信とは断定しない |

接続はpoolの既存接続の場合もある。「新しいTCP接続の確立」やその瞬間の時刻ではない。
upstreamはconnection_forの後、request送信処理より前にcaptureをsetする。
assignedが得られても、body送信完了・kernel flush・EdgeDriverのPOST受信/処理完了を証明しない。
snapshotの時刻をforwardから引いて「接続確立所要時間」と表示しない。
header未観測のままassignedでdropされれば、接続割当てが観測された後の待ちへ範囲を狭められる。
unobservedなら接続割当て前の待ちと、取消時点までにmetadataを取得できなかった場合を残す。
特定のdriver内部関数の停止やアプリcontrollerの準備状態はこの観測だけでは区別できない。

address/extra/Debug値は参照・出力しない。ライブラリのcapture objectは同じrequest guard内にだけ保持する。
snapshotはupstream watchの同期borrowを使う。request taskでI/O/flush/joinはしないが、厳密なwait-freeやOS非returnの実時間保証は主張しない。
記録ring32・行256bytes・全8KiB・既存finish budgetを維持する。

## 版と完全性

driver variantはtauri-driver-2.1.0-stage-2。
forwardしたrequestの終端前にsnapshotが1件必要。欠測・重複・余剰field・状態不整合・overflowはincomplete。
新validatorはstage1を新しい完全観測へ読み替えず拒否する。旧artifactは当時の版と判定を保存する。
schemaのfieldは増やさず固定stageだけを増やし、reportのnativeConnectionはassigned/unobserved/not-sampled/unconfirmedに限定する。
forward前の変換失敗/dropではnative request未実行なのでsnapshotを要求しない。

公式tauri-driver2.1.0/crate全体hash・Cargo.lockは不変。変更は既存許可4fileだけ。
patch/source/postimageの新hashはapply_driver_trace.pyに固定し、source前後・build binaryの対応を再照合する。
試験用driverは製品EXE/ZIPに同梱しない。アプリのstartup-diagnostics featureを通常配布へ追加しない。

## 検証の区別

診断CLIの新分類は499b59eでRED→a02ad19でGREEN（Python7件）。
Rustの単一loopback fixtureはe4866d3のCI run40774301920/job123119417251で、pin検証後capture未実装のE0599だけによるREDを確認した。
fixtureは自己所有IPv4 loopbackで1要求だけ受信しheaderを返さず、取消後にassignedを確認する。全体2秒deadline、所有server taskをabort/awaitする。
これは元Windows GUI timeoutの再現ではなく、接続観測と取消の相関を確かめる試験用境界のTDDである。
既存fixtureのindex更新は新snapshotの版に合わせた回帰で、旧合格条件を緩めない。
Mac既存Rust1.93はeditionを明示したformatter用途だけ。実build/RED/GREENは既存固定WindowsCI Rust1.99.1で確認する。
新headのCI・独立レビュー結果を別証跡として保存する。

## 参照と残る境界

- [Hyper-util0.1.19 CaptureConnection](https://docs.rs/hyper-util/0.1.19/hyper_util/client/legacy/connect/struct.CaptureConnection.html)
- 同版公式crateのSHA256は727805d60e7938b76b826a6ef209eb70eaa1812794f9424d4a4e2d740662df5f。既存Cargo.lockとcache crateの一致を確認し、capture.rs/client.rsを一次資料として読んだ。
- 元timeout根因、同失敗のapp stage、native内部の受信/attach/応答生成、旧失敗の全WebView終了は未確定。
- double-close NOT_MET、Windows背圧未検証、実SSH未有効・製品gate閉鎖は不変。
