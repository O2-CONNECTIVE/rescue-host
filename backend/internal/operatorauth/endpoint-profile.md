# 同じ入力から取得する候補と未構成の正規policy

## 接続済みsource

private `prepareNativeEndpointProfileBatch` は同じ spec の入力と launch 相関へ候補leaseとproviderを構成し、既存 `prepareNativeBatch` へ渡す。
prepare段階にOS照会はなく、登録後の固定workerの `Start` から取得する。
Service adapterや公開入口へのfactory登録は行わない。

```text
同exchangeのSoleInput.Move（端点ownerの責務）
  → 所有readをspec.inputとしてprivate factoryへ
  → 同じBatch/Collector/canonical OwnerをServiceに登録
  → Start: 独立Service tokenでworkerのServiceScope取得
  → 同じinputのclient PID → 保持process → query-only primary token
  → 同じinputのPIDを再照合 → 独立した固定policyに照合
  → 既存helper起動・実message認証・Collect・Consume
  → helper/I/O回収 → 候補query/probe/token/process回収 → scope回収
```

入力は非継承・exact read権 `0x120089` のmessage server端で、kernel server PIDが現在processに一致することを既存のread validatorで検査する。
GetNamedPipeClientProcessIdで取得前後のclient PIDを照合し、そのPIDで開いたprocessの生存・creation・imageとprimary tokenを保持する。
processはQUERY_LIMITED_INFORMATIONとSYNCHRONIZE、tokenはTOKEN_QUERYだけを要求する。
元read端はBatch所有であり、このleaseはclose・read・impersonate・duplicateしない。
取得完了後は保持process/tokenだけを再照会し、Batchが閉じたreadの番号を再利用してqueryしない。
これは移譲後のendpoint検査成功や同一接続の再認証を意味しない。

queryの部分取得、取消、panic、fatal、close失敗は同じleaseへ残す。
親process/tokenを閉じる前に、既存profile queryのpending probeを回収する。
停止中のconstructorが実際に戻るまで、回収ackを発行しない。
OS inline API自体の非returnをcontextで中断できるとは主張しない。

## 正規AuthenticationIdが不足している理由

現 `serviceprovision.Record` は固定SID・配置・ACL・image等を持つが、登録済みoperatorログオンの証拠を持たない。
AuthenticationIdはログオンsessionのLUIDであり、永続のoperator SIDそのものではない。
候補の照会値をコピーして同じ候補を照合するだけでは、独立した期待値の正規発行にならない。
private factoryも欠けたAuthenticationIdを補完せず、既存providerの必須値検査を使う。

現在の厳密な期待値契約を維持するには、保護された事前登録記録または信頼済み起動ownerが保持するoperator process/tokenの来歴が必要である。
事前登録では発行主体、Serviceの起動世代、ログオン属性、有限期限、取消、再登録の条件を決める必要がある。
SCM control通知は登録者tokenの認証経路ではない。
LsaGetLogonSessionDataで取得できる情報も、そのsessionを承認済みとして選ぶ根拠にはならない。
WTSQueryUserTokenのLocalSystem/SE_TCB_NAME要件を満たすための昇格や、LogonUserによる新資格情報の作成は採用しない。

別案として、固定SIDとkernelが確認した実message主体を認証根にし、AuthenticationIdを接続後のbindingへ限定する方式はあり得る。
これは現在の「独立AuthenticationId必須」契約と同一SIDの別ログオンを許す範囲を変更するため、今回採用しない。
正規登録の選択は未決として統合ownerへ返す。

## Service adapterへの残る接続条件

- 正規policy発行元が候補照会前に不変の期待値を固定し、helper kind/levelも明示する。
- Service側の具体SoleInputから破壊的Moveした所有readだけを同exchangeのspecへ渡す。raw番号や成功boolを移譲proofへ変換しない。
- 同Service/issuer/canonical Owner/Batch/Collector/launchを登録時に照合し、未設定factoryへfallbackしない。
- 元端点のconnect completionとoperation release、全親read-copy閉鎖、write/close ack、次要求の予算・readinessは端点/Service ownerが確認する。
- 保護profileとFactoryの対応は同profile照合境界を通す。候補値からFactoryや期待profileを生成しない。

このsourceはService adapterを変更せず、正規policyの供給元を仮実装しない。
`operatorauth` から `serviceprovision.Root` を直接importすると、serviceprovision→servicehost→operatorauthの循環になる。
保護登録記録を採る場合は、共通の下位保護readerへの分離または既存compose側の正規producer設置をownerと合わせる必要がある。
任意bytesのdecode成功や型だけをrootの認証済み記録と扱わない。

## 実機検証は別操作

source/通常memory試験/Windows compileでは、実service/token/ACL/pipe保護やSSHの成功を認定しない。
実行前に対象端末、Serviceとoperator/guestの実SID、保護配置・image整合性、既存tokenをqueryする権限、登録方式と寿命、継承/複製制限、作成対象を確定する。
権限不足は拒否結果として記録し、privilege付与・LocalSystem・別資格情報へfallbackしない。
設定変更が必要な場合は変更前設定を退避し、停止→所有参照/Job回収確認→追加資源だけの除去→元ACL/配置復元の手順を対象ごとに提示して別承認を得る。
今回OS設定・アカウント・ACL・資格情報・実SSH・拒否済み専用試験は実行しない。製品gateは閉鎖を維持する。

## 一次資料

- [GetNamedPipeClientProcessId](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-getnamedpipeclientprocessid)
- [OpenProcessToken](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-openprocesstoken)
- [TOKEN_STATISTICS](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-token_statistics)
- [WTSQueryUserToken](https://learn.microsoft.com/en-us/windows/win32/api/wtsapi32/nf-wtsapi32-wtsqueryusertoken)
- [LsaGetLogonSessionData](https://learn.microsoft.com/en-us/windows/win32/api/ntsecapi/nf-ntsecapi-lsagetlogonsessiondata)
