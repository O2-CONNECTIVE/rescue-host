# 許可0のservice準備と一つのissuer

`NewService`は保護された`serviceready.Config`、正規operatorから今回の承認を解決する`ProtectedProvisioning`、既存`Launcher`、有限のstartup/cleanup/観測予算を必須とする。
constructorは静的検査だけを行う。
UI・env・SCM引数や保存snapshotからこれらを組み立てる入口は提供しない。
既定mainのreceiverと既定`RunSCM`のHostはnilのままで、製品gateは閉鎖している。

service起動全体でfresh crypto epochの`ephemeral.Issuer`を一つだけ生成する。
最初はgeneration0・許可0で、session Identityを仮置きしない。
単回Host runnerは同じissuerを借用し、別issuerを作らず、そのgrantだけを失効させる。
service全体のissuerを閉じる権威はServiceが持つ。
現在のone-shot runnerを維持し、一つのservice起動内での複数再承認は提供しない。
終端後に新しい承認を使うには、回収確認後の新しいservice起動と新issuerが必要であり、保存した許可を再開しない。

## 準備からRUNNINGまで

SCM adapterは元callerを固定thread上で`CheckServiceCaller`へ渡す。
これはdirty/unknownなcallerをworkerへ移す前に拒否するためだけで、readyやoperator認証を証明しない。
成功後の専用service ownerでも、`PreparedService`による自主体・借用token・固定image/祖先ACL/hashの完全検証を行う。
既存`VerifyService`と`Authenticate`のRUNNING必須条件は緩めない。

`serviceready.Plane`は固定serviceとprocessのowner/ACL、service binary設定、保持process generation、作成したlocal listenerのproperties/ACL/所有を検査する。
未設定、失敗、未確認、取消ではRUNNINGへ進めない。
準備したlistenerはcontrol/1とは別で、承認済sessionを前提としない。
このlistenerはkernel上で接続準備された状態だけを表し、operatorのサーバ認証、命令codec、payloadの読取や認可は未実装である。
16KiBのpipe buffer指定をrequest framingの強制と呼ばない。

準備が終わった後にだけSCMへRUNNINGを送る。
status channelへのsendをkernelの状態確認に代用せず、同じservice/current PIDの実RUNNINGを`ConfirmRunning`で確認する。
その確認と再検査が終わるまで、承認sourceの読取も子起動も開始しない。
観測とInterrogateは発行・renewへ繋がらない。
readonlyの`ServiceSnapshot`はprocess内の観測値で、既存control/1のwireや外部向けcodecではない。

## 停止と失敗

停止は排他内の新規発行拒否を先に確定し、issuer全体を閉じて全grantを失効させてから、進行中の承認・launch・runtimeを取り消す。
同じHost/process/Jobとlistenerを、独立した有限cleanup contextで回収する。
launchの部分失敗でも、最初のprocess回収より先にservice全体の発行拒否を確定する。
failed issuerやchallengeを再利用できるようにはしない。
内部失敗を停止要求に偽装せず、最初の停止原因を保持する。

回収が未確認ならStopPendingに留まり、同じownerが明示された有限contextまたは追加SCM Stopで同じ所有物を再回収する。
新しいgrant/issuer/processを作って失敗を置き換えない。
`Close(ctx)`はcaller予算内で返り、期限切れを停止確認へ変換しない。
service ownerは未確認資源を保持しているため、未確認時に`Serve`が完了する保証はない。

fatalなowner threadはUnlockせず終了する。
健康なSCM entryは、fatal ownerが回収未確認のまま終了した場合にもStoppedを返さず、資源をServiceへ保持したStopPendingで外部介入を待つ。
このhold経路は新しい認証query・発行・activationを行わず、回収完了や実停止を自己申告しない。
`Execute`は`svc.Run`が所有する専用goroutineで使う契約とし、手動呼出し後に同じgoroutineでprivileged処理を続けない。
OS APIの非return、OS sleep、監督hang、最後のJob handle、既取得/継承/漏出したhandleの不存在は、このpure試験で解決したとは扱わない。

## 供給元と実機の未達

`RunReadySCM`は将来の保護された別service executableからだけ呼ぶsource接続点で、既定入口からは呼ばない。
実際のprotected provisioning loader、operator用server認証と命令codec、承認source、受理transport、service executable/固定artifactの配布・設定は未接続である。
具体的なservice identity/SID、登録、ACL、既存token/keyの供給値もこの変更で採用しない。
SCM service objectとprocessを非管理者guestから保護する前提はnative policyで照合するが、実OS成立は未実証である。
host operator/admin既侵害とadmin guestは初期保証の範囲外である。
実行時のservice登録/起動・account/ACL/FW・鍵・SSHの変更は、対象と戻し方を示した別承認を要する。

試験はprivate OS seamにfakeを置き、実issuer/coreと公開lifecycle/SCM adapterを通す。
通常/race、Windows test binary生成、独立静的レビューを別々の証跡にする。
実SCM配送、実listener/権限、実process停止、実SSHやP2専用/代替再現は実行しない。
EOF/Job/OS保護の受入を通常CIのgreenへ読み替えない。
