# 開発とオフライン検証

[READMEへ戻る](../README.md)

対象は新規接続を無効にしたv0.2.1-devです。
Python 3.11以上とTkinterを想定し、実行時の追加Python依存はありません。
全OSや全Python版での検証済み宣言ではありません。
公開済みの結果と環境は[status](status.md)へまとめています。

## オフライン試験

```bash
PYTHONPATH=src python3 -m unittest discover -s tests -v
python3 -m compileall -q src tests
```

Windows PowerShellの場合：

```powershell
$env:PYTHONPATH = 'src'
py -3 -m unittest discover -s tests -v
```

模擬Uptermとモックによる試験で、公開中継、実PC、TCPポート22には接続しません。
模擬ライフサイクルだけは開始ゲートをテスト内で置き換えます。
この成功は実接続の安全性を証明しません。
モデルだけの試験は[既存の実行手順](../OFFLINE_BACKEND_MODEL.md#実行)を参照してください。

## GUIとWindowsビルド

`python src/rescue_host.py`はGUIを開きます。
保存された旧セッションがあると回復処理を実行するため、本番PCでは承認前に実行しないでください。
回復動作は[回復と画面終了](recovery.md)を参照してください。

[build_windows.ps1](../build_windows.ps1)はWindows x64向けの未検証スクリプトです。
想定前提はWindows 10/11、Python 3.11以上、Pythonランチャー`py`、インターネット接続です。
EXEは未生成で、ARM64も未検証です。
実行時に固定版Upterm v0.39.0とPyInstallerを取得します。
`-UseLocalUpterm`には独立に確認した`-LocalUptermSha256`と対応ライセンスが必要です。
ダウンロードと同じ経路のチェックサムだけでは配布元侵害を防げません。
Pythonビルド依存の完全なハッシュ固定と再現ビルドは未実装です。
ビルドしても新規接続は有効になりません。

## 詳細と履歴

- [構成と実装境界](architecture.md)
- [リポジトリの配置方針](repository-layout.md)
- [接続再開のバックエンド契約](../SECURITY_BACKEND_CONTRACT.md)
- [v0.2.1の引き継ぎ](../HANDOFF_V021.md)
- [前段候補の引き継ぎ](../HANDOFF_V02.md)（履歴。現仕様はv0.2.1を優先）
- [変更履歴](../CHANGELOG.md)

既存の引き継ぎにある元HANDOFFと元レビューの未提供事項は、この文書整理では解消していません。
2026-10-26の公開はソース候補の公開のみで、配備、稼働サービスの外部公開、本番PC接続ではありません。
今後の実接続と有効化は[安全要件](security.md)に従う別工程です。
