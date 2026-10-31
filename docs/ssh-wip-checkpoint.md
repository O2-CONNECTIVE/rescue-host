# SSH途中実装のローカル保存

AI操作agentを主入口に変更したため、このブランチのSSH固有作業は保留しています。完成版・公開版ではありません。

## 基点と依存
- 原基点: 61c6e8c3287c4b5b07bcf96db4e778810b58cd85
- 直前commitは保存済み42ファイルの認証helper/supplierと所有回収の共通依存です。
- このcommitだけが内側SSH endpoint、duplex、鍵同一性、runbackend接続、関連試験/文書/inventoryの途中差分です。
- 後続のSCM bootstrap候補、query ACE修正、request-loop/Status/Revoke修正、AI command coreは含みません。再統合前に最新共通基盤へ差分レビューしてください。

## 検証状況
このSSH差分は未実行・未compile・未gofmt・未独立reviewです。内側SSHのsource/fake試験を追加しただけで成功とは扱いません。共通依存の以前の試験成功もこのcommitの成功を意味しません。実Windows・実SSH・GUI・配布・公開は未実施です。

## 接続点と未実装
runbackend.New -> backend.NewOwned/AttachOwned -> 所有duplex -> 内側SSH -> ShellStreams。wireの宛先表記と物理TCP接続は別で、stock sshdへのfallbackは禁止です。
保護bootstrapから同launchのguest stdioを子へ渡すproducer、固定guest shell spec、Job/stdioのnative所有、main失敗時の未移転shell回収、退出statusが未実装です。PTY/TUIとrestricted helper実体も未完成です。ShellStreams pointer検査はOSの所有/認証証明ではありません。
host signerは保護側に保持し、guestへ秘密鍵/token/control pipeを渡さない方針です。将来の再統合では同grant/同認証鍵/期限/revoke/cleanupと資源移転失敗を再試験・独立reviewしてください。

## 復元方法
このローカルブランチを参照して保存済みsourceを復元できます。共通依存とSSH差分を別commitに分けています。AI主系統へ一括mergeせず、必要になった時にSSH差分のみを再設計/検証して移植してください。ログ、生成物、秘密、実端末情報はこの保存に含めません。
