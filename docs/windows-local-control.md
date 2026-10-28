# Windowsローカル制御IPCの認可境界候補

[Issue #25](https://github.com/O2-CONNECTIVE/rescue-host/issues/25) / [共通契約](adr/0001-control-contract-candidate.md) / [安全要件](security.md)

`backend/internal/localcontrol` は設定検査・kernel token照合・既存dispatcherへの権限境界です。
public Openは常に拒否し、configや環境変数の解除flagはありません。
native accept/read/共通codecの結合は未実装でErrClosedです。
pipe/service/account/password/ACLを実際に作成する実行、実SSH、OS設定変更は行っていません。

## 2026-10-28版の脅威範囲

初期版は、遠隔作業を行う非管理者guestから監督serviceと停止処理を保護する範囲を対象にします。
hostoperatorや管理者が既に侵害されている場合、管理者guestは対象外です。
別保護service、guest SID、immutable grant、hard TTL、許可鍵と固定宛先、不可逆revokeの要件を維持します。
正規hostoperatorの承認、guestからのIPC拒否、ACLとpeer identity照合は必要です。
毎回の別admin sign-inは追加の必須条件にしません。
本書と純粋試験の成功は、そのOS保護や従来の全受入条件が達成された証拠ではありません。

## 明示的な設定とpipe plan

Configはoperator/service/guestの固有SID、generation、既存Identity、channel nonceを必須にします。
SIDの未設定・同値・broad well-known SID・非正規表現を拒否します。
初期の対応subsetは通常account SID（S-1-5-21配下）と固有virtual service SID（S-1-5-80の5 subauthority）です。
ServiceSIDはprocess/client tokenの**TokenUserと一致する主アカウントSID**です。service groupに含まれるだけでは許可しません。
未知のservice identity、LocalSystem、共有service SIDへのfallbackはありません。
設定したguestが実際に非管理者か、service accountが正しく分離されているかは実機受入で別に確認します。

pipe名は固定local namespaceとgeneration/nonceだけから内部生成し、UIからpath/commandを受け取りません。
hash/nonceを相手の認証やgrantの正当性とは扱いません。
protected DACLはguestを明示deny、serviceに個別FILE_ALL_ACCESS mask、operatorには0x00100183だけを許可します。
operatorのmaskはread/write data、read/write attributes、synchronizeで、pipe-instance作成権を含みません。
clientも同じ個別maskで開く必要があり、GENERIC_READ/GENERIC_WRITEでは開けません。
GENERIC_WRITEがFILE_CREATE_PIPE_INSTANCEと同じbitを含むため、generic rightsへ広げない設計です。[Microsoftの権限仕様](https://learn.microsoft.com/en-us/windows/win32/ipc/named-pipe-security-and-access-rights)

native create adapterはmessage/read-message/local-only、first-instance、overlapped、最大1 instance、非継承handleを指定します。
既存名との衝突から別instanceや名前へfallbackしません。
16KiBは要求buffer値であり、kernelの実確保量や総メモリの上限ではありません。[CreateNamedPipeW](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-createnamedpipew)
現段階ではcreate adapterもpublic gateから呼ばれず、実DACLやclient拒否は実行未検証です。

## token確認とdispatch

Endpointはfactoryが所有したhandleだけを保持し、外部のhandleをattachする入口はありません。
readから認証・Revert・dispatchまでと、closeは同じgateで直列化します。
現native readは未実装のため、read-token相関がWindows runtimeで成立したとは扱いません。
GetNamedPipeInfoはserver/messageの防御検査だけで、作成時ACL・remote拒否・instance同一性の証明には使いません。

process TokenUserが明示ServiceSIDと一致することを確認します。これをclient tokenのfallbackには使いません。
OS threadを固定し、既存thread tokenがあれば処理を止めます。
pipe作成前にも同じthread検査を行い、clientの実効contextで作成しません。[Access Tokens](https://learn.microsoft.com/en-us/windows/win32/secauthz/access-tokens)
ImpersonateNamedPipeClientの成功後だけ、同じthreadのOpenThreadToken(TOKEN_QUERY, OpenAsSelf=true)を使います。
client TokenUser・TokenType・impersonation levelを照合し、SID自己申告、PID、group名を認可根拠にしません。
Identification以上のlevelで本人確認だけを行い、clientのcontextで保護資源を操作しません。[ImpersonateNamedPipeClient](https://learn.microsoft.com/en-us/windows/win32/api/namedpipeapi/nf-namedpipeapi-impersonatenamedpipeclient)、[OpenThreadToken](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-openthreadtoken)、[level定義](https://learn.microsoft.com/en-us/windows/win32/api/winnt/ne-winnt-security_impersonation_level)

Impersonate失敗時はtoken照合や要求実行へ進みません。
token handleを閉じ、Revert成功を確認してからthread固定を解除し、server側のdispatcherを呼びます。
既存thread token・復元不能・token/pipe handle終了不明ではpoisonedにし、nativeでは固定exit codeでprocessを終了します。
Revert失敗のthreadをpoolへ戻さず、mockでfatalがreturnしても後続要求を拒否します。[RevertToSelf](https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-reverttoself)

操作は同じgeneration/Identityのsnapshotとstopだけで、start/renew/任意PID/path/commandは提供しません。
CommandはGo内部の型でありwireを追加するものではなく、将来のreceive側は共通rescue-host-control/1のcodec/stateを通す必要があります。
認可拒否と、認可済み要求の完了未確認を固定errorで区別し、nativeの生errorやSIDを公開診断へ返しません。
Endpoint.Closeはpipeだけを閉じ、Sessionの停止やprocess回収の成功とは扱いません。

## 証跡と残る境界

TDDは明示設定のplan、kernel由来operatorの限定stop、認可済みstop失敗の完了未確認をRED→GREENで確認します。
factoryの既存thread tokenも負例で拒否前のFAILを観測し、作成前検査でGREENを確認します。
その後のSID/世代/操作範囲、API失敗、復元順序、close競合はin-memory回帰です。
Windows専用試験もSDK定数の照合だけで、native APIを呼びません。
Windows amd64 crosscompileはABI型のコンパイル確認であり、native実動作や実guestの拒否を証明しません。

accept/read/cancel/writeと共通codecの結合、実ACL/peer確認、SCM/guest分離設定、独立失効、hard deadline執行、process回収は未接続です。
APIの非returnやOS停止に対する時間保証も未実証です。
一時runner pipeを使う実API試験が必要なら、接続数・寿命・cleanup・対象APIを先に別報告します。新guest/password作成や既存OS security設定変更は行いません。
P2専用runtime未実証と拒否された専用/代替再現の停止、double-close NOT_MET、Windows背圧UNVERIFIED、製品gate閉鎖、44条件と配布/本番の別判定を維持します。
