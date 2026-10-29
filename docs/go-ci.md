# Go基盤のCI

[Python CI](ci.md) / [Go基盤](../backend/README.md) / [Issue #9](https://github.com/O2-CONNECTIVE/rescue-host/issues/9)

公開Go基盤の専用workflowは[go-offline.yml](../.github/workflows/go-offline.yml)です。
Go 1.27.3と公式setup-go v7.0.0の照合済commitを固定し、標準3 OSで依存取得、vet、製品build境界、通常試験、race試験を実行します。
PythonとTauriのworkflowは独立しています。

各stepは`python scripts/go_ci.py prepare / build / test / race`の対応するコマンドを呼びます。
実行対象は公開`backend/`だけで、存在しない対象やテスト0件を成功にしません。
通常試験は`-cpu=1`、raceは`-cpu=2`で、試験内のGOMAXPROCS観測と照合します。
race検出の有無と、指定した並列度の観測を区別します。
Goのrace runtimeは終了直前に既定で1000ms待つため、CIでは`GORACE=atexit_sleep_ms=0`を明示します。
これは[race runtimeの終了時追加sleep](https://go.dev/doc/articles/race_detector)を除く設定で、検出や異常終了コード、子の終了確認1秒の条件は変更しません。
packageとtestの並列はともに1に制限しますが、個別試験内のgoroutineは動作します。

job上限は20分、通常のpackage timeoutは2分、raceは5分、各Go subprocessにも外側の上限があります。
raceはcgoと対応C compilerが必要です。
現行matrixのLinux/macOS/Windows amd64はGoの対応対象ですが、runnerで前提が成立しなければ失敗を報告し、黙ってraceを省略しません。
新しいcompilerやOS設定を導入しません。

取得段階だけ公式Go proxy/checksum DBを利用し、go.mod/go.sumの変化とmodule cacheの改変を検出します。
試験段階はGOPROXY=off、GOTOOLCHAIN=local、GOWORK=off、明示したGOFLAGSで追加取得と暗黙の版変更を防ぎます。
これだけで試験プログラムの全ネットワーク通信を遮断するわけではありません。
公開試験は自己所有の高位IPv4 loopbackとメモリ内鍵に限定し、host22、実ネット、外部relay、既存サービスを使いません。
TestMainの資源記録は残留と禁止dialの件数、loopback境界だけを確認して削除し、生の接続記録をartifactへ公開しません。
試験が失敗した場合も資源件数と経過時間を確認してから削除し、試験失敗と資源異常を両方報告します。

Darwinの背圧fixtureは、自己所有の対象socketへ接続前だけ`TCP_NOOPT`を設定し、接続前後の有効状態を検査します。
これは[Appleの接続単位のTCP option](https://github.com/apple-oss-distributions/xnu/blob/f6217f891ac0bb64f3d375211650a4c1ff8ca1ea/bsd/man/man4/tcp.4)を使う試験設定で、製品socketやOS全体の設定は変更しません。Linux/Windowsの設定は維持します。
accept側の受信準備後に固定65536 byteを送り、同じ初回TCP Write由来の先頭byte受信と、50ms間隔2回の未完了観測を対応させます。
現在のbuffer容量やMSSは診断値であり、それだけで通知済みwindowや背圧の成立を証明しません。
700ms以内に実Writeの未完了を観測できなければ失敗し、失効後1秒以内の同じWriteの中断・所有資源回収という条件は変更しません。
この限定fixtureの成功を、製品Windows背圧や全TCP条件の保証へ広げません。

準備期限の実測は、製品の単調時計・壁時計の先着判定に合わせ、少なくとも一方が5秒へ達したことと、単調時計で6秒未満の観測上限を検査します。
待機結果・Snapshot前のContext終了・両時計の経過・失効理由を分け、Snapshot自身の期限検査を自律失効の証拠にしません。
偽clockでは5秒直前・到達・壁時計だけの前進と失効後の復活拒否を確認しますが、実OSの時刻変更を再現したものではありません。
これは試験の時計領域を揃える変更で、製品の5秒期限・lease・maximumは変更しません。

通常buildに選択される24package、Linux/macOSでは128実装、Windowsでは142実装ファイルを照合します。
追加の未接続CLI、coordinator、所有pipe入口も相対ファイル名を明示し、fixtureや将来のpackageを無差別に許可しません。
supervisorの追加はauthority.goの純粋grant判定だけで、service登録・IPC・timer・実OS保護を提供しません。
ephemeralの追加はissuer.goのmemory-onlyな単回発行だけです。durable store、実OSの承認口、サービス起動や実通信は追加しません。
servicehostは共通control.go/host.go/approved.go/job.go/ownercontrol.go/issued.go/nativebootstrap.go/nativestdio.go/ownedstdio.go/scm_ready.go/service.go/outer.go/dispatch.go/dispatch_owner.go/reservation.go/delivery.goと、go env GOOSに対応するSCM実装1件・nativebootstrap実装1件・SCM ready実装1件だけを許可します。
Linux/macOSはscm_other.go、Windowsはscm_windows.goを厳密選択し、未知GOOSを拒否します。
nativebootstrapは非Windowsのnativebootstrap_other.goまたはWindowsのnativebootstrap_windows.goを選択します。
SCM readyは非Windowsのscm_ready_other.goまたはWindowsのscm_ready_windows.goを厳密選択します。
SCMの登録や起動、実機の権限分離を検証したとは扱いません。
localcontrolは共通policy.go/boundary.go/wire.goと、go env GOOSに対応する明示実装だけを許可します。
Linux/macOSはnative_other.go、Windowsはnative_windows.goとio_windows.goを選び、未知GOOSは拒否します。公開Openの接続ゲートは閉鎖中で、CIは実pipeや実token/ACL検証を行いません。
現在のfixtureは`_test.go`だけで、特別なfixture用build tagはありません。
OS別のテストファイル制約を尊重し、fixtureや注入adapterを通常buildへ混ぜません。
runbackendはruntime.goだけ、childjobは共通child.go/plan.goと、非Windowsのnative_other.go、
Windowsのnative_windows.go/artifact_windows.goだけを許可します。
protectedkeyは共通acl.go/format.go/loader.go/profile.go/scope.go/source.go/validation.goと、
非Windowsのnative_other.goまたはWindowsのnative_windows.goの1件だけを許可します。
bootstrapはcodec.go/launch.go/native_receiver.go/strict.go/types.go/validate.goと、
非Windowsのnative_receiver_other.goまたはWindowsのnative_receiver_windows.goだけを許可します。
bootstrapioはio.goとnative_other.go/native_windows.goの対象別1件、
bootstrapauthはauth.go/image.go/kernel.go/profile.go/validation.go/prepared_service.goと、非Windowsのnative_other.go、Windowsのnative_windows.go/image_windows.goを厳密選択します。
childstdioはoperation.go/pipes.go/streams.goと、非Windowsのnative_other.goまたはWindowsのnative_windows.goを厳密選択します。
servicereadyはconfig.go/operation.go/plane.go/security.go/preflight.goと、非Windowsのnative_other.go、
Windowsのnative_windows.go/connect_windows.go/listener_windows.goを厳密選択します。
cmd/rescue-backendはmain.go/native.goの2件です。通常buildに試験用adapterを混ぜません。
serviceprovisionはcompose.go/errors.go/record.go/record_decode.go/record_strict.go/record_validate.go/root.go/root_doc.go/root_io.go/root_validate.go/service.goと、
共通のexecution.go/execution_native.go/execution_owner.go/launch.go/launch_security.go、
Linux/macOSのroot_other.go/launch_other.go、Windowsのroot_windows.go/root_windows_io.go/launch_windows.go/launch_windows_security.go/launch_windows_threads.goだけを厳密選択します。
cmd/rescue-service-hostはmain.goの1件だけを許可し、未知package・未知file・対象OSと異なる実装を拒否します。
cmd/rescue-auth-helperもmain.goの1件だけを許可し、未設定の正規sourceを有効化しません。
servicecontrolは全OSでrequest.go/strict.goと応答専用response.go/response_fields.go/response_preview.goの5件だけを許可します。応答codecの成功は認証・承認・停止の証明ではありません。
純粋なDecodeUntrustedFrameの構造検証であり、認証・dispatch・issuer・transportへ配線せず製品gateを閉鎖したままにします。
native receiver・認証・所有I/OのsourceとAPI seam試験を、実Windowsでの保護主体・ACL・実EOF後PID照会の成立へ広げません。
保護stdio/stderrの所有・取消・EOF回収にはsourceとAPI seam試験がありますが、実Windowsでの保護成立や期限内回収は未実証です。
許可0のSCM readyとservice全体のissuer所有はsource候補とAPI seam試験の対象です。
固定provisioning読取りのsource候補は追加されていますが、実配置とfixedAnchorは未設定です。
保護preflight、強token取得、SCMの内側回収と元threadの外側回収ackを接続したsource/API seam試験を含みます。
固定実値、正規operator認証・承認source、受理transportは未設定・未接続で、既定の入口と製品接続gateは閉鎖を維持します。
実service/process ACLやlistenerの保護成立、元thread退役とOS上の物理停止をCIだけで確認済みとは扱いません。
Go JSONと終了コードを両方検査し、pass、fail、skip、no-test package、未終端を区別します。
選択された公開test sourceと実際のtop-level結果を照合し、全skipや欠落を成功にしません。
検査をJSON fixtureで検証する開発ツール試験は、製品のSSH境界のTDDとは別です。

## 既知の未達

double-closeの物理遮断はNOT_MET（#5）、Windows背圧は未検証（#6）のままです。
caseの無断除外、skip追加、continue-on-errorは行いません。
full suiteが既知fixture前提の不成立で失敗した場合も、その結果を保存し、他の試験の合格を全suite成功へ読み替えません。
未設定commandの単発試験不一致は原因未確定で、診断追加後の合格と分けて扱います。
故障が期待どおり再現できる回帰試験の成功も、実遮断や受入44条件の合格とは別判定です。

## 固定依存の一覧

| module | 固定版 | 権利確認の参照 |
| --- | --- | --- |
| golang.org/x/crypto | v0.58.0 | [LICENSE](https://github.com/golang/crypto/blob/v0.58.0/LICENSE) / [PATENTS](https://github.com/golang/crypto/blob/v0.58.0/PATENTS) |
| golang.org/x/sys | v0.49.0 | [LICENSE](https://github.com/golang/sys/blob/v0.49.0/LICENSE) / [PATENTS](https://github.com/golang/sys/blob/v0.49.0/PATENTS) |

両moduleは公開担当が固定版のBSD系LICENSEとPATENTSを確認済みです。
依存ソースやバイナリをこのPRで同梱せず、実配布物のnotice確認は#24へ残します。
checksumの一致をlicense審査や脆弱性検査の代替にはしません。

公式根拠：[Go testとbuild制約](https://pkg.go.dev/cmd/go)、[race要件](https://go.dev/doc/articles/race_detector)、[module検証](https://go.dev/ref/mod)、[toolchain選択](https://go.dev/doc/toolchain)。

許可0のservice dispatchは同じService/issuerへのprivate登録とstatus・失効受付だけを追加します。native認証provider、承認発行、workload回収receipt、実transportは未接続です。CIのmemory試験を実認証・実SSHの証明へ読み替えません。

reservation.goはprivateな所有順序だけを扱います。同じServiceのstopと予約を同じmutexで順序付け、未確認workerの参照を保持します。認証/承認、policy、configDigest、grant、wire成功応答を生成せず、今回のlocal検証は実worker adapterや実機の証明ではありません。

delivery.goは既存の保護された承認sourceから同じHostを登録し、実Host.Run workerの終了と保持Host.Closeを分けて確認する共通実装です。controlled経路からlegacy承認へのfallback、認証・policy生成、transportや製品接続の有効化は追加しません。

operatorauthは共通collector.go/controller.go/helper.go/types.goの4件と、Linux/macOSではnative_other.go、Windowsではnative_windows.go/native_token_windows.go/native_process_windows.goだけを許可します。
単回read/auth/復帰/reportと独立owner回収のsource境界で、native launcher/report I/O、server認証producer、同Service adapter、実機保護は未接続・未実証です。
作者のローカル全module loopback bind試験はsandbox拒否のまま再実行せず、対象memory試験の保存結果と明示的に許可された既存CIの最終head結果を区別して確認します。
