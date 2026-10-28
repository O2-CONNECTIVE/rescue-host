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

通常buildに選択される13package、Linux/macOSでは26実装、Windowsでは28実装ファイルを照合します。
追加の未接続CLI、coordinator、所有pipe入口も相対ファイル名を明示し、fixtureや将来のpackageを無差別に許可しません。
supervisorの追加はauthority.goの純粋grant判定だけで、service登録・IPC・timer・実OS保護を提供しません。
ephemeralの追加はissuer.goのmemory-onlyな単回発行だけです。durable store、実OSの承認口、サービス起動や実通信は追加しません。
servicehostは共通control.go/host.go/approved.go/job.go/ownercontrol.goと、go env GOOSに対応するSCM実装1件だけを許可します。
Linux/macOSはscm_other.go、Windowsはscm_windows.goを厳密選択し、未知GOOSを拒否します。
SCMの登録や起動、実機の権限分離を検証したとは扱いません。
localcontrolは共通policy.go/boundary.go/wire.goと、go env GOOSに対応する明示実装だけを許可します。
Linux/macOSはnative_other.go、Windowsはnative_windows.goとio_windows.goを選び、未知GOOSは拒否します。公開Openの接続ゲートは閉鎖中で、CIは実pipeや実token/ACL検証を行いません。
現在のfixtureは`_test.go`だけで、特別なfixture用build tagはありません。
OS別のテストファイル制約を尊重し、fixtureや注入adapterを通常buildへ混ぜません。
runbackendはruntime.goだけ、childjobは共通child.go/plan.goと、非Windowsのnative_other.go、
Windowsのnative_windows.go/artifact_windows.goだけを許可します。
Go JSONと終了コードを両方検査し、pass、fail、skip、no-test package、未終端を区別します。
選択された公開test sourceと実際のtop-level結果を照合し、全skipや欠落を成功にしません。
検査をJSON fixtureで検証する開発ツール試験は、製品のSSH境界のTDDとは別です。

## 既知の未達

double-closeの物理遮断はNOT_MET（#5）、Windows背圧は未検証（#6）のままです。
caseの無断除外、skip追加、continue-on-errorは行いません。
full suiteが既知fixture前提の不成立で失敗した場合も、その結果を保存し、他の試験の合格を全suite成功へ読み替えません。
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
