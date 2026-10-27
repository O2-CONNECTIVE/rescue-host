# 配布候補の準備条件

[開発手順へ戻る](development.md)

## 現在の入口と未確定事項

公開mainにある配布入口は[build_windows.ps1](../build_windows.ps1)です。
Windows 10/11 x64、CPython 3.11–3.15 x64、Pythonランチャー`py`、PowerShell、`tar`、取得先へのインターネット接続を前提とします。
PyInstallerは実行しているOSとPythonに対応した出力を作るため、Windows EXEの生成はWindows上で行います（[公式の説明](https://pyinstaller.org/en/stable/operating-mode.html)）。
macOSとLinuxの開発チェック成功をWindowsビルド成功へ読み替えません。

既存スクリプトはUpterm `v0.39.0`を固定し、アーカイブと同じ配布先のチェックサムを照合します。
同じ配布先のチェックサムは、独立した真正性の根拠にはなりません。
`-UseLocalUpterm`を使う場合は`vendor/upterm.exe`、独立に確認したSHA-256、対応する`third_party/UPTERM-LICENSE.txt`が必要です。
この指定でもpipによるビルド依存取得は行われ、完全オフラインビルドにはなりません。

現在の[requirements-build.txt](../requirements-build.txt)はWindows x64専用のwheel lockです。
PyInstaller本体と推移依存7個をexact版とSHA-256で固定し、[build-python-lock.json](../build-python-lock.json)に公式PyPI metadata、wheel URL、upstream source、Python条件、licenseとnoticeの所在を記録します。
Linux/macOS用のPyInstallerやmacOSのmacholibは含みません。
他OSやWindows ARM64/32bit向けの導入には使えません。
Python、Tk、pipのbootstrap、OS、Uptermを含むビルド環境全体の固定と再現ビルドは別途必要です。
既存スクリプトは`dist/RescueHost.exe`を生成する想定ですが、この基盤変更ではビルドせず、EXEの生成と動作の検証済み宣言をしません。
署名処理も含まれていません。

## Python build依存の検証と更新

| package | 固定版 | wheel同梱licenseの要約 |
| --- | --- | --- |
| PyInstaller | 6.22.3 | GPL-2.0-or-laterとbootloader exception、対象runtime filesはApache-2.0 |
| altgraph | 0.17.5 | MIT |
| packaging | 26.3 | Apache-2.0 OR BSD-2-Clause |
| pefile | 2024.8.26 | MIT |
| pyinstaller-hooks-contrib | 2026.9 | standard hooksはGPL-2.0-or-later、runtime hooksはApache-2.0 |
| pywin32-ctypes | 0.2.3 | BSD-3-Clause |
| setuptools | 84.2.1 | MIT、wheel内部のvendored noticesも保持 |

2026-10-27に各版の公式PyPI JSONと実wheelを照合しました。
各JSONの正確なURLとSHA-256はlockに記録しています。
PyInstallerの[公式license](https://pyinstaller.org/en/v6.22.3/license.html)とwheelの`COPYING.txt`、各依存のlicenseファイルを確認し、最終同梱物のlicense判定は配布候補ごとに行います。
PyInstaller 6.22.3は公式[GHSA-9fxf-4qw3-ghmr](https://github.com/pyinstaller/pyinstaller/security/advisories/GHSA-9fxf-4qw3-ghmr)の影響版条件`<6.22.1`に該当しません。
これは他の脆弱性がないという宣言ではなく、依存scanのseverity・例外・頻度はDecision #36で別途決定します。

`python scripts/check_build_lock.py`は、7個の版・hashとprovenance、requirementsの一致を追加依存なしで確認します。
`--wheelhouse <directory>`を指定すると、過不足のない7個のwheel、実bytesのSHA-256、Name/Version/Requires-Python、記録したlicenseファイルを検査します。
検証はwheelの展開・import・コード実行を伴いません。
`--target`はWindows x64 CPython 3.11–3.15を要求し、`--installed`はその条件に加えて導入版を照合します。
指定Python範囲は選定wheelのmetadataと整合する条件であり、全Python版での実ビルド確認ではありません。

[専用CI](../.github/workflows/python-build-lock.yml)はCPython 3.13.17 x64の3OSでlockと有限fixtureを検査し、Windows向けwheelを取得してhash・metadata・licenseを照合します。
cross-platform downloadの`--no-deps`は、既に列挙したWindows wheelだけを取得するために使います。
Windows jobはfresh venvへ`--no-index --require-hashes --only-binary=:all:`で導入し、依存解決・`pip check`・固定版照合・PyInstallerのversion表示を確認します。
Linux/macOSではWindowsへの実インストールとEXE buildを対象外として明示します。
専用fixtureは固定版以外、hash不一致、対象条件不一致などの非0を検証し、ネットワークなしのpip hash拒否も含みます。

公開build入口は取得前に対象とlockを確認し、一時venvへ[hash必須・wheel限定](https://pip.pypa.io/en/stable/topics/secure-installs/)で依存を導入します。
依存整合と固定版の確認後に従来の`--onefile`ビルドを実行し、venvは終了時に清掃します。
開発用Python全体へのpip installは行いません。
このCIはbuild入口のPowerShell構文も確認しますが、Upterm取得・EXE build・再現buildの実証は実行しません。

更新時は版ごとの公式PyPI JSONの依存条件・yanked状態・wheel対象・SHA-256・sourceとlicenseを読み、実wheelを再取得して照合します。
lockとrequirementsを同時更新し、PyInstallerの採用版変更はcheckerの明示的な版も更新してレビューします。
hashをエラー出力から無条件に採用せず、3OS検証とWindowsのfresh venv整合を再確認します。
PyPI由来のhashは取得後の差替えや破損を検出しますが、初回に信頼した配布元の真正性や再現ビルドを単独では保証しません。

## 候補ごとに対応付ける記録

配布候補を作る段階では、次の情報を一つの候補に対応付けます。
これは必要物の定義で、現在そろっているという報告ではありません。

| 必要物 | 記録する内容と照合方法 |
| --- | --- |
| source対応 | repository、完全なsource commit SHA、追跡対象の変更有無、候補識別子。ビルド入力に未追跡ソースを混ぜない |
| manifest | 候補名、開発版番号、対象OSとCPU、PythonとTk、ビルドツール版、依存版、同梱物の相対名、生成手順、署名の有無、検証範囲 |
| SHA-256 | EXE、同梱する各ファイル、配布アーカイブのハッシュ。配布単位と個別ファイルを取り違えない |
| 依存一覧 | 直接依存と推移依存の実際の版、取得元、ハッシュ、取得確認方法。範囲指定の入力だけで完了にしない |
| license | 本プロジェクトのLICENSE、Uptermの対応license、Python、Tk、PyInstaller等の同梱対象に必要なlicenseとnotice。最終の同梱物を照合する |
| 検証記録 | source SHAと候補ハッシュ、OSとCPU、起動、停止、異常終了、残存資源、既知の未達、未実行項目。失敗した試行も保持する |
| 評価の境界 | 未署名の候補であること、対象の評価者と用途、期限、開始ゲートの状態、利用できない機能、本番利用不可の説明 |

manifestには実ユーザー名、IP、秘密、端末の絶対パス、生ログを入れません。
作業証跡はリポジトリ外または除外済み`.work/`へ保管し、公開用の要約だけを選別します。
ハッシュ一致はファイルの対応を確認するもので、署名、配布元の真正性、実行時の安全性を単独では保証しません。

## 未署名候補と公開の判定

未署名の配布候補を生成できても、限定評価への配布、本番利用、受入44条件の合格は別判定です。
署名状態は`unsigned`など明示的な値で記録し、署名済みのように案内しません。
OSの警告や保護を無効にする手順は、この開発入口に含めません。
配布候補の実動作は合意したEXE境界で別途確認し、ソースのオフラインテストで代替しません。
接続再開には[安全要件](security.md)が残ります。

コード署名secret、自動更新、GitHub Releaseの作成、公開アップロード、配備はこの工程では実施しません。
未公開Go基盤、Tauri、限定実通信GUI、評価fixture、その配布スクリプトは公開成果物と実行条件が確定してから別工程で追加します。
現在存在しない成果物をmanifestへ実在するものとして列挙しません。
