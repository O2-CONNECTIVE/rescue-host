# 保護bootstrapから本体Sessionへ接続する準備コード

このpackageは、保護側が今回の承認から固定した型付き`ApprovedConfig`を、本体の`backend.New`、`Ready`、`Activate`、既存control/1 adapterとstdio loopへ接続する。
新しい`supervisor.Authority`は作らず、hard ceilingの権威はserviceが持つ同じAuthorityに残す。
子側は保護bootstrapが渡すimmutable grant snapshotから、同一性・鍵集合・方針・short lease・最大期限の上限を照合する。

## 入力と未設定gate

`ApprovedConfig`の入力元は、将来の認証済みbootstrap loaderだけに限定する。
argv、環境変数、UI、任意path、hello frame、保存stateをこの型へ変換する入口は提供しない。
nonce/hashはbinding識別子であって認証ではない。
鍵・設定を子へ渡す保護bootstrap形式と信頼元は未実装で、既定commandと既定control constructorのstart拒否は維持する。
`Runtime.Coordinator()`は、同じ本体Sessionのsnapshot/stopをlocalcontrolへ渡す保護側constructor用の参照であり、認証・受付gateの解除ではない。
部分生成のruntimeではこの参照はnilとなる。

`New`はlistener、relay、SSH接続を開始しない。
`Runtime.Serve`は将来の保護された受理済みtransportを本体Sessionへ渡す入口だけで、待受/dial先/接続設定を追加しない。
この変更の試験ではServe、SSH handshake、listener、child processを実行していない。

## 不変値と期限

grantのgeneration/full Identityと、Session Config・wire contextを完全一致で照合する。
policy digestとConfigは本体Sessionでも検証し、raw Ed25519鍵集合は`KeySetDigest`で照合する。
`KeySetDigest`の候補規則は、ASCII domain prefix `rescue-host-keyset/1` とNUL、その後にsort/dedupしたSSH public key marshal bytesの件数と各byte長をuint32 big endianで区切ったSHA-256である。
鍵集合の並び順と重複では変化せず、鍵追加で変わる。
この規則は新wire fieldではなく、未配線provisionerが同じ規則を使う必要がある候補である。
host signerの保護・真正性は別の保護bootstrap責務で、digest一致から推定しない。

SessionのLeaseDurationは承認済みceiling以下とし、UI pollやwire startからRenewを呼ばない。
Session Configは現在相対MaximumDurationだけなので、その値を延長・再計算せず、生成前後に「現在時刻＋承認された相対時間」が元の絶対hard deadline以下か保守的に再検査する。
余裕不足や生成後の非prepared状態は拒否し、Revoke/期限付きCloseへ進む。
厳密な絶対期限をSession/lease内部へ直接渡す追加interfaceは、担当moduleの後続変更として必要である。
runtimeは元の絶対deadlineをcontextにも設定し、short leaseの自発失効も観測して制御pipeを閉じ、同じSessionへRevokeと期限付きCloseを行う。

control.NewPreparedだけが今回の固定grantに対応する同じSessionをReady→Activateへ進める。
失敗・取消を受理成功のreceiptへ変換せず、開始試行を再利用しない。
stopに使うClose予算は、caller contextと保護側が明示したCleanupTimeoutの短い方とする。
Runtime.Closeはcleanupの予算判定より先に同じSessionをRevokeし、取消済みcontextで終了確認できない場合もactiveを維持しない。
CleanupTimeoutは、終了後の最終I/O猶予にも同じ保護側明示予算を使う。
Sessionのterminatedを最初に観測した時点から一度だけ計時し、pollごとに延長しない。
期限内に書けないstop receiptを送信成功へ変換しない。ownerは正常stop receipt後も実EOFとprocess/Jobの回収を別に確認する。

`New`が終了未確認でruntimeとerrorを両方返した場合、callerはruntimeを捨てず、保持して`Close`と回復確認を続ける。
その部分runtimeからのRun/Coordinatorによる受付は許可しない。

## 検証の限界

純粋memory IOと本体Sessionの状態/終了を検証する。
最終出力の停止試験はSSH/handshakeを含まない純粋stdio fixtureで、拒否されたP2専用/代替再現ではない。
同じGo process内のticker、context、pipe Closeは、戻らないOS呼出しやOS sleep、service hang、物理TCP停止、Job最後handle、別非管理者guestからのOS保護を証明しない。
任意の壊れたI/O adapterを強制終了する機構ではなく、Closeで保留I/Oが解けるという既存stdioの前提を持つ。
service identity/token/ACL、実保護bootstrap、固定artifactの信頼元、別guest主体、実process/Job、実SSH/relayは別依存のまま残す。
製品gate閉鎖、既知のNOT_MET/UNVERIFIED、初期nonresume（再起動で許可0、旧grant復元なし）を維持する。
