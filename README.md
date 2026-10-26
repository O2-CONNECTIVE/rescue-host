# Rescue Host — v0.2.0-dev（安全性修正候補）

**新規接続はすべて無効です。本番利用できるリリースではありません。**
既存のUptermではTCP転送先を127.0.0.1:22だけに強制できず、同じユーザー権限の監視プロセスでは確実な失効も保証できません。安全要件を満たしたように見せないため、GUIと起動ワーカーの両方で開始を拒否します。設定・環境変数で解除するスイッチはありません。

## この候補でできること
- ソース、設定・公開鍵検証、回復処理をオフラインで検証
- 旧v0.1の保存済みセッションを確認・停止する回復経路（実Windowsでは未検証）
- 停止の成功応答だけでなく終了状態を確認。不明・失敗なら回復記録と鍵ファイルを保持
- Uptermユーザー設定と`UPTERM_*`環境変数の混入防止

既存セッションも安全になったわけではありません。接続共有・転送コマンド生成は無効です。GUIは既存セッションを「停止未確認」と表示します。v0.2起動だけでは、別プロセスの旧セッションを確実に失効させられません。

## ローカル検証
Python 3.11以上とTkinterを使用します。追加Python依存は不要です。

```bash
PYTHONPATH=src python3 -m unittest discover -s tests -v
python3 -m compileall -q src tests
```

Windows PowerShell:
```powershell
$env:PYTHONPATH = 'src'
py -3 -m unittest discover -s tests -v
```

テストはfake Uptermまたはモックを使い、公開中継・実PC・TCPポート22へ接続しません。ライフサイクルのfakeテストだけはリリースゲートをモックして内部の回復コードを確認します。これは実接続の安全性を実証するテストではありません。

## GUI・ビルド
`python src/rescue_host.py` はGUIを開きます。保存された旧セッションがあると回復処理を実行するため、本番PCでは承認前に実行しないでください。

`build_windows.ps1` はWindows x64向けの未検証ビルドスクリプトです。実行するとインターネットからUpterm/PyInstallerを取得します。この作業では実行していません。固定版はv0.39.0。`-UseLocalUpterm` では独立に確認した`-LocalUptermSha256`とライセンスを必須にしました。ダウンロードと同じ経路で取得するチェックサムだけでは配布元侵害を防げません。Pythonビルド依存の完全なハッシュ固定・再現ビルドは未実装です。

## 保証しないこと
- TCP宛先限定、guestの権限制限、タイムアウト時の確実な切断・再接続拒否
- Windows ACL/Job Object、実Upterm、実GUI、EXE、スリープ・再起動の動作
- 既にPowerShellで行われた変更・取得された認証情報の取り消し

Uptermの公開鍵認証はOS上の最小権限化ではありません。旧SSH転送モードはPowerShellも起動するため、内側のsshd認証だけを頼りにできません。`authorized_keys`の`permitopen`等もUptermでは強制されません。

## 引き継ぎ資料
- `HANDOFF_V02.md`: 作業範囲・変更・未検証事項
- `SECURITY_BACKEND_CONTRACT.md`: 新規接続を再有効化するための必須条件
- `SECURITY_REVIEW_V02.md`: 今回新規作成の独立レビュー（元添付レポートではない）
- `CHANGELOG.md`

元添付には依頼された`HANDOFF.md`とレビューレポートがありませんでした。原本の提供待ちです。元添付のプレビュー画像は旧v0.1の画面のため、v0.2配布候補から除外しています。

本番PCへの接続、外部公開、GitHubへのプッシュは別途承認が必要です。
