# Host起動段階の診断variant

関連 #9 / #25 / #91。[driver stage](tauri-driver-stage-trace.md)がnative応答待ちのまま終わった場合に、Host側のWebView準備段階を区別するための追加観測です。
元の15秒要求・単一POST・60秒Node・3分harness・origin/GUI/IPC/正常closeの合否は変更しません。
元失敗の根因修正、同一ZIPへの後付け観測、実SSHや製品gate解除の証拠ではありません。

## 有効化と所有

通常buildは無効です。診断候補だけ `--features custom-protocol,startup-diagnostics` でbuildし、source SHA・feature一覧・EXE hashを別に記録します。
起動元の専用・既存・非競合temp directoryを `RESCUE_HOST_STARTUP_TRACE_DIR` で明示した場合だけworkerを開始します。
環境変数・保存先pathはログへ出しません。directoryを作成せず、既存fileも上書きしません。
診断用directoryはdriver・appと同じ所有試行だけに対応させ、使い回しません。
これは信頼されたCI filesystemの前提であり、同一ユーザー攻撃者への隔離やACLの代替ではありません。

build-errorまたはsetup-returnと初回page-load-finishedの両方を観測すると、workerは記録窓を閉じます。
起動未完了の失敗時は、sessionの失敗確定後かつapp cleanup前に所有harnessが固定marker `app-startup.finish` を1回作ります。
workerは50ms間隔・最大120秒で終了条件を確認し、producerを閉じて実行中の記録だけ最大100ms待ちます。
UI threadはI/O・flush・join・mutex待ちを行いません。固定6枠のmemoryへtry_lockで記録し、競合/満杯は別atomic counterへ残します。
workerのみが固定 `app-startup-stages.json` をcreate-newで最大2KiB出力します。
強制終了・保存不能・marker欠落・未回収producer・overflow・count不整合・file欠落/途中書込みは観測不完全です。
出力待ちを元のsession要求へ加えず、既存診断回収budget内に収めます。OS I/Oの非returnに厳密な実時間保証はありません。

共有workflow/harnessは統合・配布担当の所有です。この差分はそのfileを変更しません。
接続時は診断buildと上記環境、失敗確定後のmarker、cleanup前の有限回収を一体で確認します。
成功時も通常close前にfile回収を確認し、app終了でwriterが打ち切られた場合は欠測として扱います。
marker前にappをkillすればworkerも終了し得るため、欠測をbuild前停止と判定しません。

## 固定stageと意味

| stage | 観測境界 |
| --- | --- |
| setup-enter | 既存setup closureへ入った |
| webview-build-start | 既存builderのbuildを呼ぶ直前 |
| webview-build-ok / webview-build-error | buildのResultが返った。原例外は記録しない |
| setup-return | build成功後、setupが正常returnする直前 |
| first-page-load-finished | main WebViewの最初のFinished callback。以降は記録しない |

現在のvariantはhost-startup-stage-2。fieldはschema/variant/purpose、seq/stage/elapsedUs、attempted/recorded/dropped/sealed/inFlightとwindowEndUsです。
URL・title・path・PID・端末情報・payload・capability・原例外・秘密は記録しません。
時刻は同じapp内のInstantからの経過で、driver/Nodeと絶対時刻差を取りません。
windowEndUsは記録の無効化前に採取する同じapp時計の保守的cutoffで、無効化の厳密な瞬間やworkerの書込完了時刻ではありません。
採取から無効化の間に後着eventが記録された場合は、終了時刻の整合検査で不完全とします。停止後の空白を観測区間へ加算しません。
最後のeventより古い終了時刻や、欠測/型不正/旧版は不完全として拒否します。
build開始後の戻りmarker未観測の窓だけ、buildReturnUnobservedUsに終了時刻との差を表示します。
これはmarker未観測の区間で、CPU実行時間、OS呼出の実所要時間、Node要求15秒の配分を直接証明するものではありません。
通常配布はfeature無効のままです。別試行の観測へ遡及適用しません。
Finished callbackがbuild-returnより前に来る可能性を許容し、その順を偽の保証にしません。
`sealed`は記録窓が閉じた意味で、setup/通信/プロセスの終了を意味しません。

`native-smoke/app-startup-stages.ts`はunknown入力を固定field/enum/整数/seq/count/状態順で検査し、安全なfieldだけを新しいreportへ写します。
未知field・欠落・未完了は空eventsのdiagnostic-incompleteとし、raw JSON/例外をreportへ複製しません。
完備した窓でのみbuild未観測、build済みでload未観測、初回load完了を区別します。どれもIPCやnative応答成功の判定ではありません。

## 検証と一次資料

分類fixtureの先行REDは `6468b77`、期待webview-build-not-observedに対してdiagnostic-incompleteでした。
これは元のWindows失敗の再現ではなく、観測分類のTDDです。追加の欠落/秘密field/順序の試験とRust memory試験は実装後回帰です。
実行入口は `node --test tests/app-startup-stages.test.ts` と既存Rust test（memory試験は通常testにも含む）です。
Rust診断feature付きcompileと、診断variantでのnative/ZIP観測は別証跡として記録します。

- [Microsoft WebView2 WebDriver](https://learn.microsoft.com/en-us/microsoft-edge/webview2/how-to/webdriver): launch方式は起動直後の単一WebViewを前提とし、最初のinstanceへ接続する。driver/runtime版対応も必要。
- [Tauri 2.12.5 WebviewWindowBuilder](https://docs.rs/tauri/2.12.5/tauri/webview/struct.WebviewWindowBuilder.html): 既存setup/buildとon_page_load境界を利用。window作成順・navigation許可条件を変えない。
- [PageLoadEvent](https://docs.rs/tauri/2.12.5/tauri/webview/enum.PageLoadEvent.html): Finishedはpage-load完了。origin/GUI/Rust IPCの既存assertを代替しない。
