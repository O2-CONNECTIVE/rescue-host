# 保護policyと候補照会を分離するprovider

このsourceは正規policy供給元やendpoint leaseの発行元を追加しない。
`RunFixed`とcontrollerの既定入口は未設定拒否のままであり、Service adapterへの配線も行わない。
保護配置の実SID・権限・ACLと実Windowsでの成立は未実証である。

## 入力と出力

private providerは候補を照会する前に固定したpolicyを保持する。
operatorのuser、AuthenticationId、session、integrity、restricted/AppContainerと、messageに要求するkind/levelが必須である。
AuthenticationIdを含む正規入力が供給されなければ構築を拒否する。
primary用の期待projectionも独立入力とし、message用のkind/levelをprimary tokenから生成しない。
共通属性が一致しないpolicyを拒否し、候補値から欠落欄を埋めない。
guestのuserはoperatorと異なる必須値であり、他のguest token属性を暗黙補完しない。

`prepare`は一度だけ照会し、policyへの完全一致を確認してから固定materialを返す。
候補から固定するprocess generationとTokenId/ModifiedIdは、その後の同一性・drift検出にだけ使用する。
これらの識別値やimage pathは、正規の発行者・署名・配置保護を証明しない。
その後のBatch parent検査も同じcandidateを再照会し、一度の不一致後は復活させない。
helperの実message read、同thread impersonation、token比較、revert確認は省略しない。

## native queryの所有

native queryはlease期間中に外側ownerが保持するprocess/token参照を借用する。
processは非継承のQUERY_LIMITED_INFORMATIONとSYNCHRONIZE、tokenは非継承のTOKEN_QUERYのみを要求する。
read、impersonate、duplicate、tokenの発行や権限の追加を行わない。
同じ固定workerでNoThreadTokenと実threadを確認し、保持processの生存・creation・imageを前後照合する。
保持primary tokenの各照会はTokenStatisticsのTokenId/ModifiedIdを前後照合する。
processの現在のprimary tokenも前後でquery-onlyに開き、保持tokenとの対応と照会間に観測した変化を照合する。
この照会列はatomic snapshotではなく、最終照会以降の変更がないことも証明しない。
保持tokenだけの観測へ縮小せず、helper側でも実messageの主体を別に検査する。

一時query tokenの部分取得はerrorと同時に元ownerへ保持する。
close失敗、取消、panicでは再利用を拒否し、pending参照を同じproviderから回収する。
constructor内のpanicで返らなかった参照は不明として残し、既知handleを閉じただけで回収完了にしない。
BatchはI/Oとchild回収後、scope回収前にproviderのpending参照を有限Closeする。
借用process/tokenそのものはここでcloseしない。外側leaseの寿命と回収ackが別に必要である。

## 未接続の境界

このqueryはendpointのconnect完了、operation release、ACL、service/process、保持参照と同endpointの結合を証明しない。
raw handle・queryの成功・payload・PIDを根拠に、その前提を成立済みにはしない。
正規のpeer-query leaseはendpoint ownerが発行する必要があり、未構成のprivate factoryから製品へ接続しない。
現Planeのduplex listener保持とsole-reader移譲は別の未完契約であり、Recheckを成功noopにしない。
移譲後の照会可能性、次要求と論理channel、保持参照の回収順序は統合担当の範囲である。

実機評価の前には、対象端末、実SID、借用tokenの供給と寿命、process/token queryに必要な権限、保護配置とimage整合性、SCM/service objectとpipe ACL、継承/複製漏洩防止、既存鍵参照、作成対象と復元手順が必要である。
権限不足時にdebug privilegeや別主体へfallbackしない。
このPRはOS設定・アカウント・ACL・資格情報・SSH接続を変更せず、それらの実行承認を代用しない。

## 一次資料と検証境界

- [OpenProcessToken](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-openprocesstoken): process query権とtokenのDesiredAccess。
- [GetTokenInformation](https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-gettokeninformation): TOKEN_QUERYと情報種別の制約。
- [TOKEN_STATISTICS](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ns-winnt-token_statistics): TokenIdとModifiedId。
- [ImpersonateNamedPipeClient](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-impersonatenamedpipeclient): 最後にreadしたmessageのsecurity context。

通常unit/raceは合成policyとmemory queryだけを使う。
Windows compileは実APIの型・リンク確認に限り、実主体・ACL・endpoint binding・実SSH・監督hangやP2専用条件を検証済みにしない。
