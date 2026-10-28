# 実接続評価パッケージの準備契約

関連 #25 / #9 / #85。これは配布担当の準備用契約で、接続版の生成器・起動器・認可判定ではない。
base main 2af1bd7aと、未統合の#85 head03f40309のnative bootstrap境界を読んだ時点の案。
README・ユーザーガイド・実機受入手順は別担当の所有とし、ここでは配布の入力と拒否境界を定義する。
既存の未接続ZIP生成器・validator・既定拒否は変更しない。

## 実行可能な範囲

`scripts/verify_connected_package_plan.py`は128KiB以下の準備JSONだけを読み、宣言の整合を検査する。
引数は`--manifest`、完全40桁の`--source`、外部記録した`--gui-sha256`・`--service-sha256`・`--backend-sha256`。
CLIは不正なら1、整合しても2を返し、固定codeのJSONを1件だけ出力する。
整合時も`status=blocked`、`manifestValid=true`、`productionGateClosed=true`で、配布可能・起動可能・認証済みとはしない。
明示的な`--help`だけは通常のhelpを出して0。validatorの実行をinstaller/launcherの成功判定へ流用しない。

schemaは`rescue-host-connected-evaluation-plan/1`、repositoryは`O2-CONNECTIVE/rescue-host`、platformは`windows-x64`。
sourceCommitは引数と一致させ、activationはblocked、productionGateClosedはboolean trueだけを許す。
protocolsはcontrol=`rescue-host-control/1`、bootstrap=`rescue-host-bootstrap/1`の完全一致。
componentsはgui/service/backendの3roleだけで、各項目はpath・sourceCommit・sha256・versionのみ。
全componentは同じsourceCommitを宣言し、hashは外部期待値とfiles内の宣言の両方に一致させる。
versionは現在unverifiedだけを許す。GUI package版、製品版、Goのビルド版やIPC互換版は同一概念ではなく、
実行物からの独立した版照会が未契約なので、manifestの自己申告で確認済みへ進めない。
JSONの未知field・重複field、role欠落、別source/hash、gate開放、準備完了の主張を拒否する。

## 必要ファイルの提案

以下は新しい準備schema内だけの提案layoutで、既存buildの実出力名やservice名の決定ではない。
特にservicehostは現状Go libraryであり、`rescue-service-host.exe`を既存完成物として扱わない。
実layoutやcomponent構成を変更する際はschemaと検査を更新し、既存未接続ZIPへ混ぜない。

| role/分類 | 提案相対path |
| --- | --- |
| GUI | `bin/rescue-host-shell.exe` |
| 保護service host | `bin/rescue-service-host.exe` |
| Go backend | `bin/rescue-backend.exe` |
| project本文 | `LICENSE` |
| 再現用lock | `locks/Cargo.lock`、`locks/package-lock.json`、`locks/go.mod`、`locks/go.sum` |
| component依存本文へのinventory | `licenses/gui/DEPENDENCIES.json`、`licenses/service/DEPENDENCIES.json`、`licenses/backend/DEPENDENCIES.json` |
| frontend本文 | `licenses/FRONTEND_THIRD_PARTY_NOTICES.txt` |

filesはこの集合の各SHA256宣言だけを持つ。未知の追加実行物・相対path変種は受け入れない。
これは最終ZIPの全license本文一覧ではなく、inventoryから推移依存・toolchain本文を閉じた集合として導出する工程は未実装。
実ファイルの存在・bytes・PE/Authenticode・依存DLL・署名・ZIP安全性は、この純粋manifest検査では確認しない。
同じ配布元がmanifestと期待hashを両方差し替える攻撃も防がない。新しい信頼根・署名契約は作らない。

## 起動前checkの契約と阻害

実行可能な起動前checkはまだ実装しない。将来のlauncherは次の証拠を別々に確認し、不明・不一致なら起動しない。

1. 固定source/buildから独立に取得したbinary hashと、実際に展開した同じbytesの対応。埋込み版・protocol照会との一致。
2. GUI・service host・backendの役割分離、release/arch、必要runtimeとPE依存、各languageのlicense本文とpin/lockの照合。
   WebView2/VC runtime等は自動導入せず、欠落・互換性不明は阻害とする。
3. 保護serviceのprovisioning・SCM token/image/peer・guest境界・専用stdio/Job所有・最後のhandleと停止確認。
   GUIの正常終了や配布hashだけでservice所有Go子の回収を証明しない。
4. #85のfresh issuer/単回bootstrap/実EOF/期限内受信と、製品でのoperator承認・受理transport供給。
   launch ID/epoch/nonce/grant/鍵参照は起動ごとの保護通信で扱い、配布manifestへ固定・複製しない。
5. 実Windows・異なる実行主体・故障時の実測。fake kernelの成功をOS保護成立としない。

現時点の固定阻害codeはSERVICE_PROVISIONING_UNSET、PROTECTED_STDIO_UNVERIFIED、OPERATOR_APPROVAL_UNWIRED、
RUNTIME_VERSION_UNVERIFIED、DEPENDENCIES_UNVERIFIED、WINDOWS_PROTECTION_UNVERIFIED、DISTRIBUTION_TRUST_UNDECIDED。
#85の既定receiver/SCM Hostはnil、保護stdio・provisioning・承認入口等は未設定で、開始拒否を維持する。
既知double-close NOT_MET、Windows背圧未検証、P2 hold、本番44条件の未達はこの検査では変化しない。

## 診断と検証

出力項目は固定check/status/codeと、整合時のmanifestValid/productionGateClosed/blockersだけ。
入力のfilename/path・例外原文・license本文・環境変数・SID・実IP・ログを診断へ写さない。
秘密鍵・証明書・token・hostKeyRef・nonce・grant・policyRef等のfieldはschema外として拒否する。
ただし任意binaryやlicense本文内の秘密を検出するscannerではなく、診断bundleを生成・収集もしない。
将来のruntime診断bundleは別の固定allowlistとサイズ/保持期限を持ち、profile/state/原ログの全収集を既定にしない。

CLIから有限fixtureのRED→GREENを確認する。fixture hashは実binaryの証拠ではない。
追加の欠落/混在/秘密field/重複/サイズ負例は実装後回帰として区別する。
既存Python offline CIのscripts/tests discoveryで検証し、共通CI inventory/workflowは編集しない。
