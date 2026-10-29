# 直接Wryのcontroller作成対照

関連 [#119](https://github.com/O2-CONNECTIVE/rescue-host/pull/119)、[過去の識別](tauri-script-wait-investigation.md)。
製品の接続gate・実Rust IPC・15秒の単一session要求・通常close条件を変更しない。
この対照が成功しても製品合格や根因修正とはしない。EXEは試験専用で、通常packageへ含めない。

## 目的と事前に固定する次手

既存releaseはcontroller API-return後8.847710秒の窓でcallback未観測、script登録0件。
別debugは10登録・UI/IPC/通常closeが成功した。script送信後waitを修正する必要条件は現状成立しない。
Tauri固有IPC・組立て・初期化scriptを持たない、直接Tao/Wryの単一WebViewで1回だけ比較する。

| 結果 | 判断・次の修正候補 |
|---|---|
| 同じcontroller API-returnまで到達、実POST timeout、callback未観測 | この境界はTauriなしでも起こり得る。ただし原障害と同一根因の証明ではない。COM/message文脈・runtime初期化を比較し、callback送信を観測せずwake修正を入れない |
| 対照だけ成功 | Tauri原因の証明ではない。設定・作成時点・manifest/DPI・起動順の交絡を比較してから、1変数の候補を選ぶ |
| 元試験も対照も成功 | 今回は原障害を捕捉していない。修復成功とせず、同じheadを成功まで再実行しない |
| Wry退出後の実POST timeout | Wry作成停止の支持にしない。driver/session完了経路へ調査を移す |
| POST成功、document失敗 | session timeoutと区別する。固定about:blank文書の準備/Driver観測条件を修正する |
| POSTの別エラー、実runtime不一致 | controller遅延と区別し、固定起動前提かrequest経路を修正する |
| 来歴・stage・session結果・所有集合が欠測/不整合 | incompleteまたはcleanup未確認。因果判断を保留し、該当fixtureの観測/所有契約を修正する |

結果を得る前の製品修正、timeout延長、単純retry、成功までrerun、負荷による再現率調整は行わない。
原障害が必ず再現する最小ループは未確立であり、今回の対照がその証明になるとは限らない。

## 固定入力と残る交絡

一時manifest/lockは既存appから生成し、rootへのtao/wryの2辺追加以外のpackage・版・checksum・依存辺を拒否する。
Wry0.57.1、Tao0.37.2を既存graphから選び、追加root依存はdefault-features=false。
元manifestの依存・feature・dev/build依存を保持し、app build scriptと自動target検出を無効にする。
通常appのmanifest/lockは変更しない。CIだけで従来と同じ固定5-file Wry patchを適用する。
Cargo metadataで両manifestのWry/Tao feature和集合を比較し、不一致なら起動しない。
新しいEXE hash、source、生成manifest/lock/fixture hashを結び、固定sourceと別のbinaryを混ぜない。

同じRuntime131/固定Driver131/tauri-driver2.1.0の検証済みlaunch経路、独立した新規profile、STAと元Wry pumpを使う。
browser flag、外部URL、待受、追加bridge、恒久設定は追加しない。
元のinventoryにあるDriver156は既存PATHの値で、実試験はsetupが検証した131を明示選択する。

直接TaoのwindowとWebContextからWryをbuildし、固定HTMLだけをabout:blankへ読み込む。
navigationはabout:blankだけ、新window要求は拒否、HTMLのCSPはdefault-src none。
通常WindowContent経路のbuildに対応させるが、Tauri setup中の呼出しと直接作成ではmessage文脈が異なる。
Tauriのbuild resource・manifest・DPIとの完全一致は未実証で、対照EXEに配布品質や署名の保証はない。
feature一致は設定・実行環境全体の一致を意味しない。fixedInputsVerifiedは限定した来歴・固定入力の検証だけを表す。
既存debug/releaseの後に別のfresh runnerでrelease対照1回を行うため、同一物理host・起動順・warm-upの統制もない。
これらの差を隠して単一変数対照や根因証明とは呼ばない。

後続の対照は上記direct Wryと同じwindow・context・設定を保ち、WebViewのbuild呼出しだけをevent loop開始前から初回`StartCause::Init`処理内へ移す。
保存済み固定runtime-wry 2.12.3はInitから同期的に`RunEvent::Ready`を呼ぶ。これはTauri setupと同じwindow作成順やcallback層を証明しない。
初回Init以外と重複Initではbuildせず、failureは非0終了する。独自wake・別thread・追加要求はない。
新対照が成功しても元障害の根因修復としない。同じcallback未観測なら、event loop開始前だけで生じる説明を弱める。
前回と別runner・別EXEである点、manifest/DPI・profile・設定の未照合部分は残る。欠測・所有不確定は判別不能のままにする。

## navigation の匿名観測

診断専用のイベントハンドラーは固定URIの許可・拒否とpage-load通知だけを、有界channelとworker経由で記録する。URI・本文・例外の生値は保存しない。
同一document要求の前後にworkerのsnapshotを確認し、前のsnapshotに含まれたイベントだけを要求前の観測とする。
後のsnapshotまでに記録されたイベントは要求との正確な前後を断定せず、未掲載も非発生を意味しない。
channel滞留やcallbackの競合、worker停止未確認があるため、この記録は常に不完全な診断とする。
page-loadのFinishedはnavigationの成功を証明せず、初期about:blankもreadyState completeになり得る。
追加イベントハンドラーとsnapshot待機は対照のタイミングを変え得る。15秒の要求期限、単一document要求、既存の合格条件は変えない。
worker markerは作成箇所への到達のみを示し、thread joinの証明ではない。既存Wry・driver traceは追加診断より優先する。
拒否callbackは、限定したdata URI表現を復号したbytesが固定HTMLと完全一致する場合だけ、その拒否と一致を単一の匿名イベントにする。
比較対象のURIやHTMLは保存せず、許可判定は従来のabout:blank完全一致のままとする。
非対応形式や未観測は固定HTMLでなかった証明にならず、一致してもNavigateToStringからの発生や完了eventとの対応を証明しない。

## 独立判定と終了

対照kindはdirect-wry-controller-contrast、productNativePassは常にfalse。
実POST成功とdocument検査を分けて保存し、controller/Wry退出・固定文書・実runtime一致を確認する。
通常close前後に両driverのPID+creation identity生存を確認し、同一appを通常closeする。
app終了10秒・保持した観測子handle終了10秒を超えたらFAILを保持する。後続終了を期限内成功にしない。
強制cleanup前の正常判定と、cleanup後の消滅を混同しない。

session POSTは従来Driverの15秒・単一要求を使用する。Nodeは全体35秒、監督stepは3分で終了する。
所有子は32上限、Win32 process handle・path・creation・時間整合lineageで同一性を確認する。
判定確定後だけ、自己所有app/verified child handleの終了と自己起動driver/Nodeのcleanupを行う。
所有不確定ならその集合をkillしない。既存集合を再取得で上書きせず、保持handleを最後にDisposeする。
強制終了を使った実行は対照成功にしない。cleanup失敗/所有不確定も明示する。
観測集合の終了確認であり、未観測の子やhosted runner破棄まで証明するものではない。

診断終了条件は、1回分の結果・来歴・stage・通常終了/cleanup結果を回収し、上表の次手を選べること。
Windowsでの対照成功そのものを終了条件にせず、失敗もそのまま保存して調査の分岐に使う。
製品の元FAILは維持し、対照で元jobを置換しない。通常mergeは統合担当だけが所有する。

## 検証の種類

対照kindの回収CLI、独立判定、driver異常終了、document後段失敗、POST別エラーの分類はRED→GREENで固定した。
来歴CLIは未実装CLIに対する初期1縦断のRED(exit2で期待不一致)→GREENで、残る拒否16例は後付け回帰。
この初期記録のsourceCommitはbase表示で、未commit試験・初期実装の固定hashが対応元。コミット単独でのRED再現とはしない。
対照CLIのsource/EXE改変拒否・秘密非出力も後付け回帰で、実EXEを起動する試験ではない。
これらのfixtureは実COM原障害の再現ではなく、実stage・EXE起動・終了は固定head Windows CIで別判定する。
stage writer/2KiB/既存回収budgetは維持し、secret、path、PID、URL、script内容、HRESULT、原例外を診断artifactへ出さない。
新しい診断用trust root、署名契約、credential、OS security変更、実SSHは含まない。

一次資料:
- [WebView2のthreading model](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/threading-model)
- [WebDriverでのlaunch](https://learn.microsoft.com/en-us/microsoft-edge/webview2/how-to/webdriver)
- [固定runtime-wry2.12.3 source](https://static.crates.io/crates/tauri-runtime-wry/tauri-runtime-wry-2.12.3.crate)

## Driver応答境界の回収条件

#124のInit対照はWry退出guard後もsession timeoutとなり、Driver応答headerは未観測だった。
元のsession verdict確定後、所有Driverを停止する前にfinishを送り、Wry回収と共有の1秒budget内で完了fileを確認する。
予算を使い切った場合は追加待機せず一度だけ確認し、欠測・未完了・不整合はincompleteでありheader不在を証明しない。
既存の固定source/binary来歴と匿名stage verifierだけを使い、原JSONL・stdout/stderr・URL・payload・端末情報は公開しない。
header観測はnative driverの応答header受信であり、session成功やbody完了ではない。header前のdropには取消も含む。
Wry退出guardは呼出元への物理returnを証明しない。Driver traceはEdgeDriver内部のWebView生成・検出を分離せず、異なる時計・別runnerを混算しない。
この観測の終了条件は、完了traceまたはincompleteを記録し、元FAILと所有cleanupの結果を保持すること。
