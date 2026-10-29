# 保護所有者のWindows子/Job adapter

将来の保護supervisorが固定したartifact、既存primary token、標準pipe handleを渡すinternal module。
`Start`の入力をUI、argv、環境変数、保存stateから直接作らない。
tokenやACLの承認・取得、service登録、公開listener、認証情報や秘密鍵の生成は提供しない。
現在の製品入口は保護bootstrap未設定で拒否し、このmoduleの存在でgateを開かない。

## 起動と所有

- ローカルdriveの正規絶対exe pathとSHA-256を保護provisioningから固定する。
  artifactはread-onlyかつread sharingだけで開き、hashからresumeまでhandleを保持する。
  directory/reparse file、複数link、UNC/device/search path、余分なstream指定を拒否する。
- 既存の承認済primary tokenが必須で、typeを照合する。
  起動threadにimpersonation tokenがある場合や確認不能の場合は拒否する。
  token取得やprivilegeの有効化、別API/LocalSystemへのfallbackはしない。
- unnamed/non-inheritable Jobを作り、kill-on-last-handleだけを設定する。
  子は明示tokenでsuspended作成し、Job割当成功後だけresumeする。
- `STARTUPINFOEX/HANDLE_LIST`へ標準pipe 3本だけを列挙する。
  pipeはcallerが既に所有するinheritableなものだけを借用し、元handleを変更/閉鎖しない。
  Job、artifact、token、process/thread handleは子へ継承しない。
- Command/Bootstrap未指定のargvは固定imageと`--supervised-internal`だけ。
  environmentは空のUnicode block、cwdは固定image所在directory。
  親の秘密環境、任意引数、検索pathを引き継がない。

Go側の信頼済profileは、`NewFixedCommand(args, pinnedDependencies)`で固定した
`Spec.Command`を指定できる。これは認証proofではなく、UI等からの任意argvの入口にしない。
constructorとStartはsliceをcopyし、zero値とBootstrapとの併用を拒否する。
nil Commandの既存supervised/Bootstrap起動契約は変えない。
固定argvはWindows EscapeArg規則で引用し、最大64引数、32依存file、
command lineは末尾NULを除き32766 UTF-16文字までとする。
依存fileは正規絶対pathと非0 SHA-256を指定し、caseを無視した同pathとimage重複を拒否する。
異なるpathの同leaf名や同内容は許可する。この構文照合は物理file同一性の証明ではない。
各依存fileをimageと同じread-only/non-inheritable verifierで開き、hashを検査する。
write/delete sharingを許さない同じ依存handleをprocess退出かつJob active=0まで保持する。
module import、resume、leader退出だけでは解放しない。
部分検証の返却handleと閉鎖失敗の参照もChildに保持する。

`Start`成功はresumeまでの確認で、Go readyやSSH接続許可ではない。
起動contextはdeadline必須で、失敗時は独立した`CleanupTimeout`内で回収する。
回収を確認できない場合は`non-nil Child + ErrUnconfirmed`を返す。
呼出側はerrorだけでChildを捨てず、所有参照を保持して後続の回収を確認する。

`Wait(ctx)`はleader退出、Job active=0、所有handle回収を確認する。
非0退出は`ErrExit`であり、Go内Close応答を作らない。
`Close(ctx)`はleaderが退出済みでもJob全体を停止し、未割当/結果不明のprocessも停止する。
終了未確認では最後のJob handleとprocess参照を保持する。
同じOS handleを同時に二重閉鎖せず、PIDから他processを探索しない。

## 残る前提と検証境界

Jobは権限隔離ではない。
入力tokenが誰の承認済tokenか、service/guestの分離、保護rootと祖先directory/ACLの真正性は別の未完成provisioningの必須責務。
hashとshare modeだけでは、保護root外でのpath差替えや配布元の真正性を証明しない。
別session tokenでのhandle継承や必要privilegeが成立しなければ失敗し、設定を緩めない。
保護bootstrap transportとConfig/鍵の渡し方は未決で、新しいwireや鍵serializationは作らない。

試験はOS seamのin-memory応答とnativeが使う起動parameter組立の純粋試験だけ。
native WinAPI本体はcross compileの対象とし、実process、service、account、ACL、鍵、listener、SSHを試験で作らない。
通常CIの既存fixture成功も、P2専用再現、保護主体の成立、Job最後handleの実挙動、hang/sleep中の期限執行や物理停止の証明へ読み替えない。

## 公式APIの根拠

- [CreateProcessAsUserW](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-createprocessasuserw): 明示primary token、固定application、起動直後の状態と初期化完了は別。
- [UpdateProcThreadAttribute](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-updateprocthreadattribute): inherit対象handleを明示し、inheritable handleとTRUEの継承flagを要求。
- [Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects): Job所属とkill-on-last-handle、子孫の管理。最後のhandle閉鎖と監督hangは別。
- [CreateFileW](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew): share modeは保有handleの存続中に作用し、非共有write/deleteと既存openの競合を拒否する。
- [GetCurrentThreadToken](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-getcurrentthreadtoken): thread impersonation tokenのpseudo handle。取得handleの閉鎖は不要。
