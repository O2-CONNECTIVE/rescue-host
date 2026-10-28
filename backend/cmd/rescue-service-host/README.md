# 保護service専用の未設定入口

このcommandは固定provisioning sourceの専用入口候補である。
現在は必要な固定Anchor・起動tokenの保護供給・operator承認入口が未設定なので終了値2で拒否する。
設定path、SID、handle、鍵、policyをargvや環境変数から受け取らない。
`--install`、`--config`、debug/console fallbackを提供せず、SCM登録やOS設定を変更しない。

固定file reader、純粋record検証、既存service構成への接続と残る信頼条件は[serviceprovision](../../internal/serviceprovision/README.md)を参照する。
commandのbuild/拒否試験は実service・実SSH・OS保護成立の証明ではない。
