# 所有するWindows標準pipe

`childstdio`はserviceが所有するstdin・stdout・stderrの3本と、親のRead／Writeに使う非同期I/Oを扱う。
factoryはcaller認証を行わず、Setupやpipe名を認証証明として扱わない。
保護profile・service principal等は上位のnative bootstrap経路で先に確認する。
UserSID、ServiceSID、CleanupTimeoutは保護設定から明示し、defaultや環境変数による代替を持たない。

`NewNative(ctx, Setup)`は有限startup contextを要求する。
3本とも32 byteの暗号学的乱数によるfresh名、`O:<UserSID>D:P(A;;GA;;;<ServiceSID>)`、FIRST_PIPE_INSTANCE、REJECT_REMOTE_CLIENTS、BYTE/WAIT、単一instanceを使う。
名前は識別子であり、鍵や資格情報ではない。
stdinの親serverはOUTBOUND、stdout/stderrの親serverはINBOUNDで、いずれもOVERLAPPED・非継承である。
serviceが開くchild client原本は対応するGENERIC_READ/WRITE、同期・非継承とし、connect完了後に継承可能な複製を作る。
原本を閉じた後の3つの複製だけを`ChildHandles`で借用する。
childのGo stdioにOVERLAPPED対応を仮定しない。

上位層はstdio3本と専用bootstrap readの4本だけをchildjobの明示HANDLE_LISTへ渡す。
Job、token、key、root、親server、eventを継承させない。
child作成後に`ReleaseChild(ctx)`でservice側に残るchild handleの複製を閉じる。
child process内の継承済みhandleには触れない。

`Input() io.WriteCloser`、`Output() io.ReadCloser`、`Stderr() io.ReadCloser`は同じendpointを共有する。
各endpointのRead／Writeを直列化し、通常のidleにtimeoutを作らない。
内部I/Oは最大64 KiBずつのowned bufferを使い、Readは実completion後にcallerへcopyし、Writeはcallerから内部copyを作ってからkernelへ渡す。
Writeは大きな入力を複数の内部chunkに分け、部分writeは`io.ErrShortWrite`と実転送数を返す。
呼出し中のcaller sliceの並行変更は通常のio.Reader/Writerと同じく許容しない。
成功した0 byte readをEOFにせず、実ERROR_BROKEN_PIPEだけをEOFへ変換する。
取消・selfCloseと混ざる終端はEOFにしない。

streamの`Close()`はstopを通知し、CancelIoExと一度の非blocking completion/cleanup確認だけを行う。
stream CloseはSetup予算によるfresh contextや待機を作らず、busy・未完了・mutex admission不可なら直ちにErrUnconfirmedを返す。
cleanup mutexとoperation mutexはTryLockで一度だけ確認し、実completion未確認のbuffer/OVERLAPPED/event/handleを保持する。
`Pipes.Close(ctx)`は全endpointへ先にstopを通知し、caller deadlineとCleanupTimeoutの短い方を全体で共有する。
`ReleaseChild`と`Pipes.Close`には有限contextを要求し、cleanup mutexとoperation mutexのadmissionもTryLockとcontextで待機を制限する。
有限poll・呼出し終了確認・再回収を待つ責務はPipes.Closeだけに集約する。
既取消contextでは一度の非blocking pollで未完了を返し、内部予算でdeadlineを延長しない。
通常Read／Writeはstop通知で待機を打ち切り、kernel requestが未完了ならcallerへ戻った後もPipesが同じoperationとowned bufferを保持する。
呼出元が戻ったbufferを再利用しても、kernelに渡したbufferは再利用されない。

OVERLAPPEDとbufferをpinし、event・handleも実completion確認前に解放しない。
CancelIoEx成功・NOT_FOUND、handle close、未知のquery errorをcompletionとして扱わない。
回収不明は`ErrUnconfirmed`とし、同じPipesを保持してfresh finite contextで`Close`を再試行する。
部分constructor失敗でもPipesを返し、未完了connectや途中のhandleの所有を残す。
完了不明の参照を捨てる、別operationで回収を代替する、fatal exit、panic、無限workerは使わない。
OS API自体がreturnしない故障への強制終了保証はなく、native実行でも実証していない。

5組の同入力red/greenは構築と所有、byte転送/EOF、取消EOF拒否、未構成拒否、stream Closeの単一pollと所有保持を確認する。
後付けのfake回帰はowned bufferの遅延completion、部分constructor、idle待機、既取消cleanupのpoll数、cleanup admission、別pollのGo mutexを保持したままのstream Closeと短期限Pipes.Closeを確認する。
公開factoryはnativeだけを選び、テストはprivate kernelのみを使う。
有効なSetupで公開NewNativeを実行するテストはない。
Windowsのtest binary crosscompileは実DLL・pipe作成の試験ではない。
実service/account/ACL変更、鍵生成、SSH/listener、P2再現、製品有効化は行っていない。

- [GetOverlappedResult](https://learn.microsoft.com/en-us/windows/win32/api/ioapiset/nf-ioapiset-getoverlappedresult)
- [CancelIoEx](https://learn.microsoft.com/en-us/windows/win32/api/ioapiset/nf-ioapiset-cancelioex)
- [ReadFile](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-readfile)
