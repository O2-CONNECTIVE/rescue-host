# Windows bootstrap搬送

このpackageは、認証済みserviceから認証済みchildへ最大16,384 byte（LFを含む）のmessageを一回だけ渡す。
JSON、grant、発行権、caller認証は生成・解釈しない。
`Setup`のUserSIDとServiceSIDは保護provisioning由来の明示値を要求し、defaultを持たない。
現在のUser/tokenとの照合は認証層で行う。

serviceはcurrentService・SCM・token・image確認後に`NewNativePair`を呼ぶ。
32 byteの暗号学的乱数から作るper-launch pipe名は識別子であり、資格情報として使わない。
`O:<UserSID>D:P(A;;GA;;;<ServiceSID>)`の明示SDDL、FIRST_INSTANCE、INBOUND、OVERLAPPED、MESSAGE、READMODE_MESSAGE、WAIT、REJECT_REMOTE_CLIENTS、単一instanceを使う。
serviceが非継承writerを自分で開き、connect完了後にserver-readだけを継承可能な専用handleへ複製する。
`ChildReadHandle`はservice所有handleの借用であり、child作成時の明示HANDLE_LISTへstdio3本と合わせて渡す。
Job、token、key、root、writerを継承リストへ追加してはならない。
child作成後の`ReleaseChildRead`はservice側read複製とoriginal readを閉じ、childのhandleには触れない。

`Publish`は最初の呼出しでsend権をconsumeする。
不正frame、取消、部分write、OS errorでも再送できない。
状態mutexを解放してからOS I/Oを行い、write完了後に唯一のwriterを閉じる。
入力sliceは内部copyを使うが、呼出し中に呼出元が元sliceを並行変更してよいという契約ではない。

childは認証層によるhandle・principal・pipe/SCM/peer provenance確認後にだけ`AdoptNativeReader`を呼ぶ。
成功時は継承handleの所有権をReaderへ移し、それまでは呼出元が所有する。
`ReadOnce`は単一message全体、LF、16,384 byte上限、追加readの実`ERROR_BROKEN_PIPE`（転送0）を確認する。
0 byteの成功message、MORE_DATA、追加message、追加data、取消やselfClose由来の終端を拒否する。
record内容のcodec検証と認証再checkは上位層の責務である。

constructor、Publish、ReadOnceにはdeadline付きの有効なcontextを要求し、CloseとReleaseChildReadにも有限deadlineを要求する。
cleanupの予算はcallerのdeadlineと明示CleanupTimeoutの短い方とし、mutex admissionと全pending operationで共有する。
cleanup mutexはTryLockとcontext待機を使い、別Closeの長い回収を短いcallerが無期限に待たない。
呼出元のdeadlineを延ばしたり再基準化したりしない。
`Close`はcontextが既に取消済みでもCancelIoExと一度の非blocking pollを試み、未完了なら直ちに所有を保持してErrUnconfirmedを返す。
待機中のcontext取消後も内部予算による延長はしない。
上位層は明示したfresh cleanup contextで同じCloseを再試行する。
CancelIoEx成功・NOT_FOUNDやhandle closeをcompletionの証明にしない。
OVERLAPPED、buffer、eventは実completionが確認できるまで保持し、bufferとOVERLAPPEDをpinする。
成功／既知のI/O終端以外のGetOverlappedResult結果はunknownとして保持する。
開始時に同期的に拒否されたI/Oにはpending requestがない。

回収予算内にcompletionやcloseを確認できなければ`ErrUnconfirmed`を返す。
constructor失敗時も所有物が残る場合は非nil Pairとerrorを返すので、呼出元はPairを保持して同じ`Close`を再試行する。
Pair／Readerを破棄したり、別operationで回収済み扱いにしたりしない。
完了前のUnpin、fatal exit、panic、Close-as-completion、無限workerは使わない。
WinAPI自体がreturnしない故障はこのsource検証で解決・実証していない。

## 検証範囲

private kernelを注入した同じPair／Reader境界でメモリ試験を行う。
公開factoryはnative kernelだけを選び、試験用kernelを受け取らない。
Windowsでもテストはfakeだけを使い、有効なSetupで公開factoryを実行しない。
8組のred/greenは送信一回、受信message/EOF、無効handle、service側read回収、競合Close、cleanup error伝達、EOFに伴う追加data/取消拒否、既取消cleanupの単一pollとowner保持に対応する。
後付け回帰として、未完了constructor/writeのowner保持、同じoperationの再回収、上限、Setup拒否、selfClose、cleanup admission、cleanup途中の取消を確認する。

`go test -race ./internal/bootstrapio`、Windows amd64/arm64のtest binary crosscompileが対象である。
実pipe作成、Windows DLL実行、service/account/ACL変更、key生成、SSH/listener、製品起動、有効化を検証していない。
既存の製品gateを解除しない。

- [GetOverlappedResult](https://learn.microsoft.com/en-us/windows/win32/api/ioapiset/nf-ioapiset-getoverlappedresult)
- [CancelIoEx](https://learn.microsoft.com/en-us/windows/win32/api/ioapiset/nf-ioapiset-cancelioex)
- [ReadFile](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-readfile)
