# 共有artifact候補と未完事項

固定 source `1eda7a4e964d34a751dcc89509ee07b3482917a7`。ユーザーから公開artifactと限定CIの承認は得られたが、ライセンス・実装の確認が済むまでは公開しない。以下は候補であり、現時点では実ファイル・最終hash・license ZIPは生成していない。

| 候補 | 内容・状態 |
|---|---|
| `RescueHostTauriUnwiredEvaluation-x64.zip` | 固定sourceのrelease GUIとその配布用license/notice/lock。既存の検証手順で全ファイルhash、gate閉を確認する必要がある。 |
| `tauri-driver.exe` | 固定crate由来の診断用driver。直接・推移依存のライセンス資料を確認するまで公開不可。 |
| `driver-provenance.json` | 固定crate・patch・binaryの検証済来歴。secret・個人情報を除く。 |
| `driver-licenses.zip` | MIT本文・著作権表示、推移依存とRust runtimeの必要資料。完成したZIPはまだ未生成。 |
| `manifest.json` | 上記4ファイル、EXE、sourceのhash。manifest自体のhashも別job outputsと照合する。 |

固定crate原本は共有せず各runnerで既存手順により公式取得し、SHA-256 `2f2e29bd6900fb718dfdc06f2e7a371d6087c085daaf83709847ef3e6b0c1953` を照合する。

`msedgedriver.exe` は共有artifactへ含めない。Microsoft公式EULAは共有・公開・配布に制限を定めており、CI限定artifactの例外は確認できない。各runnerが公式配布元から取得し、固定版 `131.0.2903.86` / SHA-256 `c34a6b22814a18599f2a67bef922ba879ae44158f216d4d2df797d293f545665` と署名を確認する案を検討する。版固有のEULAと通知も未確認。

固定crate内の `LICENSE-MIT` / `LICENSE-APACHE-2.0` は本文ではなく外側への参照。crateのVCS commit `447fa9f3f993fe77724189e355078b38ce20baea` の正規本文を補う必要がある。driver推移依存とRust runtimeの資料はアプリ用inventoryでは代用できず、固定Cargo.lockに全platform合計95 packageがある。空の一時CARGO_HOMEで公式registryから取得した Windows metadata は64 reachable package（root 1とregistry依存63）、宣言license欠落0。registry依存63の公式archive hashがdriver lockと一致し、既存収集器で各本文を収集できることを一時的に確認した。固定 upstream MIT 本文を公式から取得しSHA-256 `9dd42ea92cff2ede5cd477cbfcce051b2d0115c0ac7f368ee88cb545055dff1d` を確認した。これは収集可能性の確認であり、Rust 1.99.1 runtime本文とdriver root補完を含む完成inventory／zipは未生成。

固定資料:
- Microsoft Edge WebDriver EULA: https://developer.microsoft.com/en-us/microsoft-edge/tools/webdriver/eula
- tauri-driver manifest: https://raw.githubusercontent.com/tauri-apps/tauri/447fa9f3f993fe77724189e355078b38ce20baea/crates/tauri-driver/Cargo.toml
- MIT本文: https://raw.githubusercontent.com/tauri-apps/tauri/447fa9f3f993fe77724189e355078b38ce20baea/LICENSE-MIT
- Apache本文: https://raw.githubusercontent.com/tauri-apps/tauri/447fa9f3f993fe77724189e355078b38ce20baea/LICENSE-APACHE-2.0

使い捨てrunner内では、固定SHAの公式artifact action、同一runのartifact ID/digest、producerが別途渡すmanifestと各ファイルのhashを照合する。製品ホストの敵対的guestに対する隔離を保証するものではない。rootの安全な削除を保証できないため、各試行の所有process終了確認は行い、rootの削除は試みずrunnerの破棄に委ねる。runner破棄による消去はこの手順では確認しない。Windows CIとartifact公開は未実施。
