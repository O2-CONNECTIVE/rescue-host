//go:build windows

package guestaccount

import (
	"context"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

type nativeJournalKernel struct {
	root                       journalRootCapability
	lockOperation              *windows.Overlapped
	pending                    bool
	openName                   *windows.NTUnicodeString
	openAttributes             *windows.OBJECT_ATTRIBUTES
	openStatus                 *windows.IO_STATUS_BLOCK
	opening                    *windows.Handle
	ioBuffer                   []byte
	tokens                     []windows.Token
	threadToken, processToken  *windows.Token
	ioDone                     *uint32
	openPins, lockPins, ioPins nativeJournalPins
}

var journalGetHandleInformation = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation")

// trusted root producer は未設定で、この private source を公開 factory に登録しない。
func newNativeJournal(ctx context.Context, p Plan, root *journalRootCapability) (*journalBackend, error) {
	if root == nil {
		return nil, ErrUnconfigured
	}
	return newJournalBackend(ctx, p, root, &nativeJournalKernel{root: *root})
}
func (k *nativeJournalKernel) subject(root journalRootCapability) (result error) {
	if k.pending || len(k.tokens) != 0 || k.threadToken != nil || k.processToken != nil || windows.GetCurrentThreadId() != root.thread {
		return ErrUnknown
	}
	k.threadToken = new(windows.Token)
	e := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, k.threadToken)
	thread := *k.threadToken
	if thread != 0 {
		k.tokens = append(k.tokens, thread)
		if k.closeToken(thread) != nil {
			return ErrUnknown
		}
	}
	k.threadToken = nil
	if e != windows.ERROR_NO_TOKEN || thread != 0 {
		return ErrUnknown
	}
	k.processToken = new(windows.Token)
	e = windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, k.processToken)
	token := *k.processToken
	if token != 0 {
		k.tokens = append(k.tokens, token)
		defer func() {
			if k.closeToken(token) != nil {
				result = ErrUnknown
			} else {
				k.processToken = nil
			}
		}()
	}
	if token == 0 {
		k.processToken = nil
	}
	if e != nil || token == 0 {
		return ErrUnknown
	}
	var kind, returned uint32
	if windows.GetTokenInformation(token, windows.TokenType, (*byte)(unsafe.Pointer(&kind)), 4, &returned) != nil || returned != 4 || kind != 1 {
		return ErrUnknown
	}
	user, e := token.GetTokenUser()
	if e != nil || user == nil || user.User.Sid == nil || !user.User.Sid.IsValid() || user.User.Sid.String() != root.service {
		return ErrUnknown
	}
	groups, e := token.GetTokenGroups()
	if e != nil || groups == nil || groups.GroupCount > 4096 {
		return ErrUnknown
	}
	matched := false
	for _, g := range groups.AllGroups() {
		if g.Sid != nil && g.Sid.IsValid() && g.Sid.String() == root.serviceSID && g.Attributes&4 != 0 && g.Attributes&0x10 == 0 {
			matched = true
		}
	}
	if !matched || windows.GetCurrentThreadId() != root.thread {
		return ErrUnknown
	}
	return nil
}
func (k *nativeJournalKernel) closeToken(token windows.Token) error {
	if e := token.Close(); e != nil {
		return e
	}
	for i, t := range k.tokens {
		if t == token {
			k.tokens = append(k.tokens[:i], k.tokens[i+1:]...)
			break
		}
	}
	return nil
}
func (k *nativeJournalKernel) open(root uintptr) (uintptr, error) {
	name, e := windows.NewNTUnicodeString(journalBasename)
	if e != nil {
		return 0, e
	}
	k.openName = name
	k.openAttributes = &windows.OBJECT_ATTRIBUTES{Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})), RootDirectory: windows.Handle(root), ObjectName: name, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE}
	k.openStatus = new(windows.IO_STATUS_BLOCK)
	k.opening = new(windows.Handle)
	k.openPins.hold(k.openName, k.openName.Buffer, k.openAttributes, k.openStatus, k.opening)
	e = windows.NtCreateFile(k.opening, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.SYNCHRONIZE, k.openAttributes, k.openStatus, nil, windows.FILE_ATTRIBUTE_NORMAL, 0, windows.FILE_OPEN, windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_WRITE_THROUGH, 0, 0)
	if e == windows.NTStatus(0x103) {
		k.pending = true
		// completion 前の out slot は読まず、pin した slot ごと保持する。
		return 0, journalPending{}
	}
	k.openPins.done()
	if e == nil && (k.openStatus.Status != 0 || k.openStatus.Information != 1) {
		e = ErrUnknown
	}
	return uintptr(*k.opening), e
}
func (k *nativeJournalKernel) inspect(raw uintptr) (journalFileInfo, error) {
	h := windows.Handle(raw)
	var out journalFileInfo
	kind, e := windows.GetFileType(h)
	if e != nil {
		return out, e
	}
	out.kind = kind
	var flags uint32
	if journalGetHandleInformation.Find() != nil {
		return out, ErrUnknown
	}
	ok, _, _ := journalGetHandleInformation.Call(uintptr(h), uintptr(unsafe.Pointer(&flags)))
	if ok == 0 {
		return out, ErrUnknown
	}
	out.inherit = flags&windows.HANDLE_FLAG_INHERIT != 0
	// protect-from-close 等の予期しない handle flag も受け入れない。
	if flags != 0 {
		return out, ErrManual
	}
	var basic windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(h, &basic) != nil {
		return out, ErrUnknown
	}
	var id struct {
		Volume uint64
		ID     [16]byte
	}
	if windows.GetFileInformationByHandleEx(h, windows.FileIdInfo, (*byte)(unsafe.Pointer(&id)), uint32(unsafe.Sizeof(id))) != nil {
		return out, ErrUnknown
	}
	var standard struct {
		Allocation, End          int64
		Links                    uint32
		DeletePending, Directory uint8
		_                        [2]byte
	}
	if windows.GetFileInformationByHandleEx(h, windows.FileStandardInfo, (*byte)(unsafe.Pointer(&standard)), uint32(unsafe.Sizeof(standard))) != nil || standard.DeletePending != 0 || standard.End < 0 {
		return out, ErrUnknown
	}
	out.identity.volume, out.identity.fileID = id.Volume, id.ID
	out.size = uint64(basic.FileSizeHigh)<<32 | uint64(basic.FileSizeLow)
	out.links = basic.NumberOfLinks
	out.directory = basic.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
	out.reparse = basic.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
	if standard.Links != out.links || standard.Directory != 0 != out.directory || !out.directory && uint64(standard.End) != out.size {
		return out, ErrManual
	}
	sd, e := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if e != nil || sd == nil || !sd.IsValid() {
		return out, ErrManual
	}
	defer runtime.KeepAlive(sd)
	owner, _, e := sd.Owner()
	if e != nil || owner == nil || !owner.IsValid() {
		return out, ErrManual
	}
	control, _, e := sd.Control()
	if e != nil || control&windows.SE_DACL_PRESENT == 0 || control&windows.SE_DACL_PROTECTED == 0 {
		return out, ErrManual
	}
	acl, _, e := sd.DACL()
	if e != nil || acl == nil || acl.AceCount > 128 {
		return out, ErrManual
	}
	aces := make([]journalACE, 0, acl.AceCount)
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var a *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &a) != nil || a == nil || a.Header.AceType > 1 || a.Header.AceSize < 16 {
			return out, ErrManual
		}
		sid := (*windows.SID)(unsafe.Pointer(&a.SidStart))
		if !sid.IsValid() || sid.Len() != int(a.Header.AceSize)-8 {
			return out, ErrManual
		}
		aces = append(aces, journalACE{a.Header.AceType, a.Header.AceFlags, uint32(a.Mask), sid.String()})
	}
	if !journalSecurityAllowed(k.root, owner.String(), aces) {
		return out, ErrManual
	}
	out.identity.owner, out.identity.security = owner.String(), sd.String()
	out.protected = true
	return out, nil
}
func (k *nativeJournalKernel) lock(h uintptr) error {
	k.lockOperation = new(windows.Overlapped)
	k.lockPins.hold(k.lockOperation)
	e := windows.LockFileEx(windows.Handle(h), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, ^uint32(0), ^uint32(0), k.lockOperation)
	if e == windows.ERROR_IO_PENDING {
		k.pending = true
		return journalPending{}
	}
	k.lockPins.done()
	return e
}
func (k *nativeJournalKernel) seek(h uintptr, off uint64) error {
	if off > journalHeaderBytes+journalMaxFrames*journalFrameBytes {
		return ErrUnknown
	}
	high := int32(0)
	low, e := windows.SetFilePointer(windows.Handle(h), int32(off), &high, windows.FILE_BEGIN)
	if e != nil || high != 0 || uint64(low) != off {
		return ErrUnknown
	}
	return nil
}
func (k *nativeJournalKernel) read(h uintptr, b []byte, off uint64) (int, error) {
	if e := k.seek(h, off); e != nil {
		return 0, e
	}
	k.ioBuffer = b
	k.ioDone = new(uint32)
	if len(b) > 0 {
		k.ioPins.hold(k.ioDone, &b[0])
	} else {
		k.ioPins.hold(k.ioDone)
	}
	e := windows.ReadFile(windows.Handle(h), b, k.ioDone, nil)
	if e == windows.ERROR_IO_PENDING {
		k.pending = true
		return 0, journalPending{}
	}
	n := *k.ioDone
	k.ioPins.done()
	k.ioDone = nil
	k.ioBuffer = nil
	return int(n), e
}
func (k *nativeJournalKernel) write(h uintptr, b []byte, off uint64) (int, error) {
	if e := k.seek(h, off); e != nil {
		return 0, e
	}
	k.ioBuffer = b
	k.ioDone = new(uint32)
	if len(b) > 0 {
		k.ioPins.hold(k.ioDone, &b[0])
	} else {
		k.ioPins.hold(k.ioDone)
	}
	e := windows.WriteFile(windows.Handle(h), b, k.ioDone, nil)
	if e == windows.ERROR_IO_PENDING {
		k.pending = true
		return 0, journalPending{}
	}
	n := *k.ioDone
	k.ioPins.done()
	k.ioDone = nil
	k.ioBuffer = nil
	return int(n), e
}

func (k *nativeJournalKernel) release() error {
	if k.pending || len(k.tokens) != 0 || k.threadToken != nil || k.processToken != nil || k.openPins.active || k.lockPins.active || k.ioPins.active {
		return ErrUnknown
	}
	k.openName = nil
	k.openAttributes = nil
	k.openStatus = nil
	k.opening = nil
	k.lockOperation = nil
	k.ioBuffer = nil
	k.ioDone = nil
	return nil
}
func (k *nativeJournalKernel) flush(h uintptr) error {
	return windows.FlushFileBuffers(windows.Handle(h))
}
func (k *nativeJournalKernel) close(h uintptr) error {
	if k.pending {
		return ErrUnknown
	}
	if e := windows.CloseHandle(windows.Handle(h)); e != nil {
		return e
	}
	k.lockOperation = nil
	return nil
}
