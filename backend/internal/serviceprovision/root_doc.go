package serviceprovision

// 固定配置readerの契約:
//
// Anchorはcompiled/protected provisioningだけが供給し、UI/env/argv pathを受けない。
// Volumeは事前のexpected値が必須で、現在観測したvolumeをexpectedへ昇格しない。
// OS保護されたrecordと別digestの整合を読むrootであり、runtime署名検証ではない。
// Root.Openはcaller-owned locked OS threadで呼ぶ。Loadedも同じthreadで使い、
// partial/unknownでは同じLoadedを保持し、fresh finite contextでCloseを再試行する。
// dirty/foreign/primary変更のFatalはstickyで、上位entryがthread退役を扱う。
//
// NoThreadToken、primary token、OS lookupしたNT SERVICE\固定名のenabled非deny SIDを
// record open前に検査する。SIDの数値/hashを推測・合成しない。
// 初期のrole欠落はunauthorizedとして拒否し、証明前のlookup失敗をdirty扱いにしない。
// 取得したprincipalはlookup前から保持し、partial後のdirty化もCloseで検出する。
// SubjectのProcessUserSIDは観測値だけで、root ACLのtrustedへ追加しない。
// rootを変更できるtrusted主体はSYSTEM/Administrators/derived service SIDだけ。
//
// 全祖先とleafをno-reparse・FILE_SHARE_READだけで保持し、固定drive、configured
// volume/filesystem、final path、file ID、leaf link数、owner、有効DACLを検査する。
// NULL/未知ACE/maskと未知主体のwrite/delete/owner/DACL変更ALLOWを拒否し、
// DENYで危険ALLOWを相殺しない。INHERIT_ONLYは当該nodeへ作用せず、各子を別検査する。
// DACL_PROTECTED自体をtrust boolにしない。全祖先を保持し実効ACLを検証するため、
// 継承があるだけではunsafeとせず、protected flagも前後snapshotの同一性へ含める。
// 既取得・漏出handleや既存writable mapping不在を、この現在ACL検査だけで証明しない。
//
// record最大256KiB、digestは64 lowercase hex+LFの65 byteを要求する。
// 両fileを保持して専用bufferにOVERLAPPED readし、各read後に全snapshotと主体を再検査し、
// SHA256一致後だけRecord/Subject/QueryToken getterを許す。codec解釈は別のstrict decoder。
// Recordはcopyを返す。getterでも現在NoThreadToken/primaryと保持query tokenを再照合する。
// QueryTokenは既存primaryをTOKEN_QUERYだけで開いた非継承handleの借用で、
// callerはclose/dup/adjustしてはならず、借用先scopeを先に閉じてからLoaded.Closeする。
// CreateProcessAsUser用強handle、token upgrade、launch認可は提供しない。
//
// ReadTimeout/CleanupTimeoutとcaller deadlineの短い方を使い、新しいopen/read/closeを
// 期限後に開始しない。既pendingへのCancel/GetOverlappedResult確認は回収専用で行う。
// CancelIoEx成功/NOT_FOUNDやCloseHandleをcompletionとせず、OVERLAPPEDと専用bufferをpinし、
// 実completion確認までevent/file handleも保持する。unknownならErrRootUnconfirmed。
// OS query/open等のinline API自身の非returnは解決せず、実nativeキャンセルも未実証。
// fake readerの保護照合成功、crosscompile、実OSでの成立は別の証拠である。
// 実service/token/ACL/key/SSH/listener操作やprovisioningの導入は実行していない。
//
// 確認したMicrosoft一次資料:
// https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-lookupaccountnamew
// https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-lookupaccountsidw
// https://learn.microsoft.com/en-us/windows/win32/api/securitybaseapi/nf-securitybaseapi-gettokeninformation
// https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew
// https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-readfile
// https://learn.microsoft.com/en-us/windows/win32/api/ioapiset/nf-ioapiset-getoverlappedresult
// https://learn.microsoft.com/en-us/windows/win32/api/ioapiset/nf-ioapiset-cancelioex
