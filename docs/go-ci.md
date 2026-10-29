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

通常buildに選択される26package、Linux/macOSでは213実装、Windowsでは235実装ファイルを照合します。
guestaccountは下記の共通・OS別実装を限定登録し、memory/native API seamで準備・失効・清掃順序を検査します。実account・保護rootからのjournal供給・動的GuestSID・有効化は未接続です。
同じdeep snapshotからの制御用Factory pair、SCM所有threadのfresh Witness照合、応答write bridgeの共通3実装を限定登録します。正常縦断は既存memory auth/write fixtureの証拠で、実Native入力の認証・実SSH成功には広げません。
operatorauthのsole_input_batch.goは具体SoleInputの単回Moveと部分所有保持を追加します。Moveや候補取得を認証proofにはせず、Serviceへの正規factory供給と実Windows保護は未実証です。
追加の未接続CLI、coordinator、所有pipe入口も相対ファイル名を明示し、fixtureや将来のpackageを無差別に許可しません。
supervisorの追加はauthority.goの純粋grant判定だけで、service登録・IPC・timer・実OS保護を提供しません。
ephemeralの追加はissuer.goのmemory-onlyな単回発行だけです。durable store、実OSの承認口、サービス起動や実通信は追加しません。
serviceauthownerは共通owner.goだけを許可し、実issuerと共通gateの状態を扱います。正規profile sourceや実OSの認証は提供しません。
servicehostは共通control.go/host.go/approved.go/job.go/ownercontrol.go/issued.go/nativebootstrap.go/nativestdio.go/ownedstdio.go/scm_ready.go/service.go/outer.go/dispatch.go/dispatch_owner.go/reservation.go/delivery.go/auth_dispatch.go/workload_reclaim.go/auth_exchange.go/auth_exchange_endpoint.go/auth_exchange_worker.go/control_factory.go/control_readiness.go/control_exchange.go/preview.go/preview_dispatch.go/confirmation.goと、go env GOOSに対応するSCM実装1件・nativebootstrap実装1件・SCM ready実装1件だけを許可します。
Linux/macOSはscm_other.go、Windowsはscm_windows.goを厳密選択し、未知GOOSを拒否します。
nativebootstrapは非Windowsのnativebootstrap_other.goまたはWindowsのnativebootstrap_windows.goを選択します。
SCM readyは非Windowsのscm_ready_other.goまたはWindowsのscm_ready_windows.goを厳密選択します。
SCMの登録や起動、実機の権限分離を検証したとは扱いません。
localcontrolは共通policy.go/boundary.go/wire.goと、go env GOOSに対応する明示実装だけを許可します。
Linux/macOSはnative_other.go、Windowsはnative_windows.goとio_windows.goを選び、未知GOOSは拒否します。公開Openの接続ゲートは閉鎖中で、CIは実pipeや実token/ACL検証を行いません。
現在のfixtureは`_test.go`だけで、特別なfixture用build tagはありません。
OS別のテストファイル制約を尊重し、fixtureや注入adapterを通常buildへ混ぜません。
runbackendはruntime.goだけ、childjobは共通child.go/plan.go/fixed_command.goと、非Windowsのnative_other.go、
Windowsのnative_windows.go/artifact_windows.goだけを許可します。
protectedkeyは共通acl.go/format.go/loader.go/profile.go/scope.go/source.go/validation.goと、
非Windowsのnative_other.goまたはWindowsのnative_windows.goの1件だけを許可します。
bootstrapはcodec.go/launch.go/native_receiver.go/strict.go/types.go/validate.goと、
非Windowsのnative_receiver_other.goまたはWindowsのnative_receiver_windows.goだけを許可します。
bootstrapioはio.goとnative_other.go/native_windows.goの対象別1件、
bootstrapauthはauth.go/image.go/kernel.go/profile.go/validation.go/prepared_service.goと、非Windowsのnative_other.go、Windowsのnative_windows.go/image_windows.goを厳密選択します。
childstdioはoperation.go/pipes.go/streams.goと、非Windowsのnative_other.goまたはWindowsのnative_windows.goを厳密選択します。
servicereadyはconfig.go/operation.go/plane.go/security.go/preflight.go/service_witness.go/endpoint.go/endpoint_input.go/endpoint_operation.goと、非Windowsのnative_other.go、
Windowsのnative_windows.go/connect_windows.go/listener_windows.go/endpoint_windows.go/endpoint_io_windows.goを厳密選択します。
cmd/rescue-backendはmain.go/native.goの2件です。通常buildに試験用adapterを混ぜません。
serviceprovisionはcompose.go/errors.go/record.go/record_decode.go/record_strict.go/record_validate.go/root.go/root_doc.go/root_io.go/root_validate.go/service.go/preview.go/confirmation.goと、
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

operatorauthの共通・OS別実装は[検証入口](../scripts/go_ci.py)のPRODUCT_FILESとOPERATOR_AUTH_FILESを正本として限定照合します。
単回read/auth/復帰/report、launcher/report I/O、同Service adapterのsourceを含みますが、正規policy/認証供給・実transportの結合と実機保護は未接続・未実証です。
作者のローカル全module loopback bind試験はsandbox拒否のまま再実行せず、対象memory試験の保存結果と明示的に許可された既存CIの最終head結果を区別して確認します。

operatorauthのprofile照会は共通batch_scope.go/profile_provider.go/profile_query.goと、対象OSのprofile_query_other.goまたはprofile_query_windows.goの1件だけを追加します。正規policy/query leaseの供給や実Windows保護の検証を意味しません。

候補取得は共通endpoint_candidate.goと、非Windowsのendpoint_profile_other.go、Windowsのendpoint_candidate_windows.go/endpoint_profile_windows.goだけを追加します。正規AuthenticationId供給やSoleInputとの結合を実装済みと扱いません。


guestaccountは既存のcore 8件とaccount_query.go/account_query_buffer.go/account_query_read.go、
journal.go/journal_codec.go/journal_security.go、sid_codec.go/sid_run.go/sid_run_io.go/sid_script.go/sid_source.goを共通で選択します。
Linux/macOSではaccount_query_other.go/journal_other.go、Windowsではaccount_query_windows.go/account_query_windows_machine.go/journal_windows.go/journal_windows_pin.goだけを追加します。
childjobの固定commandは固定argvと依存file保持の構成を扱います。登録成功はguest作成・無効化・削除や実Windows保護の成立を証明せず、公開NewNativeの未設定拒否は維持します。


guest作成入口のsource候補はcreation_codec.go/creation_native.go/creation_query.go/creation_script.go/creation_source.go/sid_script_parts.goを共通登録します。
creation_query_other.goはLinux/macOS、creation_query_windows.goはWindowsだけで選択し、未知・欠落・他OS実装の混入を拒否します。
登録してもpublic NewNativeは未構成拒否を維持します。staging guestの限定権限検査は下記のsource候補へ接続しましたが、正規manifest/保持handleの供給、実行時token/handle gate、Service連携は未接続のため、製品の準備や削除の完了を成功扱いしません。
memory/source API seamとcrosscompileを、実Windowsアカウント操作やSSH接続成功へ読み替えません。


確認要求の再検査はservicehost/confirmation.goとinternal/serviceprovision/confirmation.goの2実装だけを追加登録します。
前者の単回予約/opaque ticketと後者の元owner RPC/保持Record再検査を結びますが、成功時もSERVICE_NOT_READYであり、ApprovedPolicy・Challenge・grantを作りません。
unit/raceの各レイヤの成功を実native認証と元threadを通したWindows縦断の成功へ合成しません。


一時guestの限定権限検査は、[対応範囲](guest-limited-inspection.md)の共通12実装とLinux/macOSの4実装またはWindowsの5実装だけをguestaccountへ追加登録します。
作成→journal→無効staging検査→SID清掃のsource/fake確認であり、未来の実行token/継承handle/runtimeの保護やSeparatedNonAdminGuestの成立をこの登録から推定しません。


[固定配置から無効guest準備へのadapter](guest-staging-adapter.md)は、guestaccountのstaging.go/staging_borrow.go/staging_native.go/staging_profile.go/staging_query.goと対象OSのstaging_native_other.goまたはstaging_native_windows.goだけを追加登録します。
bootstrapauth/prepared_lease.goは同じPreparedServiceのprofileと借用gateを保持する共通実装です。固定manifest/control検査leaseのsourceは以下へ接続しましたが、stagingへの呼出しと実guest token/resume前gateは未接続・未実装です。source/fakeやcrosscompileを実配置・実account・実SSHの成功へ広げません。


[固定guest設定と要求単位の検査借用](guest-fixed-source.md)は、guestaccountのfixed_layout.go/fixed_layout_strict.go/fixed_layout_validate.go/fixed_layout_wire.go、serviceprovision/guest_source.go、servicereadyのinspection_lease.go/inspection_lease_security.go/inspection_lease_token.goを共通で追加登録します。
Windowsだけinspection_lease_identity_windows.go/inspection_lease_windows.goを選択し、他OS・欠落・fixture混入を拒否します。固定配置の読取は強token取得前へ接続しますが、正規承認→同issuer Plan/session→staging、動的SIDの鍵設定と実sshd token/shell停止は未接続・未実装です。実配置・OS操作・実SSHの合格には広げません。
