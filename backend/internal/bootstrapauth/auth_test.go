package bootstrapauth

import (
	"context"
	"errors"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
)

func authContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}
func TestAuthenticateRetainsOriginAndRejectsChangedSCM(t *testing.T) {
	p := testProfile()
	api := newFake(p)
	f, _ := NewNativeFactory(p)
	f.api = api
	peer, err := f.Authenticate(authContext(t), 99)
	if peer == nil || err != nil {
		t.Fatalf("固定 service の発信元を確認できません: %v", err)
	}
	api.service.pid = 77
	if peer.Recheck(authContext(t)) != ErrDenied {
		t.Fatal("SCM PID の変更を受理しました")
	}
	if err := peer.Close(authContext(t)); err != nil {
		t.Fatal(err)
	}
	if api.service.closes != 1 || api.process.closes != 1 {
		t.Fatal("所有する発信元 handle を回収しませんでした")
	}
}

type fakeOwned struct {
	closes  int
	fail    bool
	onClose func()
}

func (f *fakeOwned) close() error {
	f.closes++
	if f.onClose != nil {
		f.onClose()
	}
	if f.fail {
		return ErrUnconfirmed
	}
	return nil
}

type fakeService struct {
	fakeOwned
	pid        uint32
	state      uint32
	queryError error
	onQuery    func()
}

func (f *fakeService) status() (serviceState, error) {
	if f.onQuery != nil {
		f.onQuery()
	}
	return serviceState{pid: f.pid, state: f.state}, f.queryError
}

type fakeProcess struct {
	fakeOwned
	value      processState
	queryError error
	onQuery    func()
}

func (f *fakeProcess) inspect() (processState, error) {
	if f.onQuery != nil {
		f.onQuery()
	}
	return f.value, f.queryError
}

type fakeFile struct {
	fakeOwned
	value   fileState
	offset  int
	onRead  func()
	onQuery func()
}

func (f *fakeFile) inspect() (fileState, error) {
	if f.onQuery != nil {
		f.onQuery()
	}
	return f.value, nil
}
func (f *fakeFile) Read(b []byte) (int, error) {
	if f.onQuery != nil {
		f.onQuery()
	}
	if f.onRead != nil {
		f.onRead()
	}
	if f.offset == 3 {
		return 0, io.EOF
	}
	n := copy(b, []byte("exe")[f.offset:])
	f.offset += n
	return n, nil
}
func (f *fakeFile) rewind() error {
	if f.onQuery != nil {
		f.onQuery()
	}
	f.offset = 0
	return nil
}

type fakeKernel struct {
	p                Profile
	id               uint32
	identity         identity
	service          *fakeService
	process          *fakeProcess
	pipe             pipeState
	files            []*fakeFile
	onRead           func()
	dirty            bool
	openServiceError error
	openProcessError error
	pipeError        error
	fileError        error
	currentPath      string
	borrowed         *identity
	queryCalls       int
	threadCalls      int
}

func newFake(p Profile) *fakeKernel {
	ident := identity{user: user, kind: 1, groups: []group{{sid: service, attributes: 4}}}
	out := &fakeKernel{p: p, id: 3, identity: ident, service: &fakeService{pid: 42, state: 4}, process: &fakeProcess{value: processState{pid: 42, creation: 123, alive: true, path: p.SupervisorImage.Path, identity: identity{user: ident.user, kind: ident.kind, groups: append([]group(nil), ident.groups...)}}}, pipe: pipeState{kind: 3, flags: 5, mode: 2, access: 0x120089, owner: user, protected: true, acl: []protectedkey.ACE{{SID: service, Mask: 0x10000000}}, pid: 42}}
	out.service.onQuery = out.markQuery
	out.process.onQuery = out.markQuery
	return out
}
func (f *fakeKernel) markQuery()       { f.queryCalls++ }
func (f *fakeKernel) threadID() uint32 { f.threadCalls++; return f.id }
func (f *fakeKernel) principal(token uintptr) (identity, error) {
	f.markQuery()
	if token != 0 && f.borrowed != nil {
		return *f.borrowed, nil
	}
	return f.identity, nil
}
func (f *fakeKernel) cleanThread() error {
	f.markQuery()
	if f.dirty {
		return ErrFatal
	}
	return nil
}
func (f *fakeKernel) currentPID() uint32 { f.markQuery(); return 9 }
func (f *fakeKernel) currentImage() (string, error) {
	f.markQuery()
	if f.currentPath != "" {
		return f.currentPath, nil
	}
	return f.p.ChildImage.Path, nil
}
func (f *fakeKernel) inspectPipe(uintptr) (pipeState, error) {
	f.markQuery()
	return f.pipe, f.pipeError
}
func (f *fakeKernel) openService(string) (serviceHandle, error) {
	f.markQuery()
	return f.service, f.openServiceError
}
func (f *fakeKernel) openProcess(uint32) (processHandle, error) {
	f.markQuery()
	return f.process, f.openProcessError
}
func (f *fakeKernel) openFile(ctx context.Context, path string, root string, leaf bool) (imageFile, error) {
	f.markQuery()
	var image Image
	if path == f.p.ChildImage.Path {
		image = f.p.ChildImage
	} else if path == f.p.SupervisorImage.Path {
		image = f.p.SupervisorImage
	} else {
		image = f.p.ChildImage
	}
	node := image.Nodes[0]
	if leaf {
		node = image.Nodes[1]
	}
	state := fileState{path: path, owner: node.OwnerSID, acl: node.ACL, filesystem: "NTFS", volume: 7, id: [16]byte{1}, links: 1, size: 3, disk: true, directory: !leaf, dacl: true}
	file := &fakeFile{value: state, onRead: f.onRead, onQuery: f.markQuery}
	f.files = append(f.files, file)
	return file, f.fileError
}

func TestAuthenticateRechecksOriginAfterImageHash(t *testing.T) {
	p := testProfile()
	api := newFake(p)
	reads := 0
	api.onRead = func() {
		reads++
		if reads == 8 {
			api.service.state = 3
		}
	}
	f, _ := NewNativeFactory(p)
	f.api = api
	peer, err := f.Authenticate(authContext(t), 99)
	if peer == nil || err != ErrDenied {
		t.Fatalf("hash 完了直前の STOP_PENDING を受理しました: %v", err)
	}
	if e := peer.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
}

func TestAuthenticateFailureStillDetectsDirtyThread(t *testing.T) {
	p := testProfile()
	api := newFake(p)
	api.onRead = func() { api.id = 0; api.pipe.pid = 0 }
	f, _ := NewNativeFactory(p)
	f.api = api
	peer, err := f.Authenticate(authContext(t), 99)
	if peer == nil || err != ErrFatal {
		t.Fatalf("認証途中の thread 不明を通常拒否へ縮小しました: %v", err)
	}
	if peer.Close(authContext(t)) != ErrFatal {
		t.Fatal("回収しても fatal を保持しませんでした")
	}
	for _, file := range api.files {
		if file.closes != 1 {
			t.Fatal("不明な thread でも既所有 handle だけを回収しませんでした")
		}
	}
}

func TestOriginDenialsAndCleanupRegressions(t *testing.T) {
	for name, change := range map[string]func(*fakeKernel){
		"impersonation":     func(a *fakeKernel) { a.dirty = true },
		"wrong user":        func(a *fakeKernel) { a.identity.user = guest },
		"deny only service": func(a *fakeKernel) { a.identity.groups[0].attributes = 0x14 },
		"token kind":        func(a *fakeKernel) { a.identity.kind = 2 },
		"pipe kind":         func(a *fakeKernel) { a.pipe.kind = 1 },
		"client end":        func(a *fakeKernel) { a.pipe.flags = 4 },
		"byte mode":         func(a *fakeKernel) { a.pipe.mode = 0 },
		"no wait":           func(a *fakeKernel) { a.pipe.mode = 3 },
		"write rights":      func(a *fakeKernel) { a.pipe.access |= 2 },
		"no read rights":    func(a *fakeKernel) { a.pipe.access &^= 1 },
		"unprotected dacl":  func(a *fakeKernel) { a.pipe.protected = false },
		"wrong owner":       func(a *fakeKernel) { a.pipe.owner = guest },
		"foreign allow":     func(a *fakeKernel) { a.pipe.acl = append(a.pipe.acl, protectedkey.ACE{SID: guest, Mask: 0x10000000}) },
		"pid":               func(a *fakeKernel) { a.pipe.pid = 88 },
		"start pending":     func(a *fakeKernel) { a.service.state = 2 },
		"stop pending":      func(a *fakeKernel) { a.service.state = 3 },
		"dead process":      func(a *fakeKernel) { a.process.value.alive = false },
		"no generation":     func(a *fakeKernel) { a.process.value.creation = 0 },
		"wrong image":       func(a *fakeKernel) { a.process.value.path = `C:\other.exe` },
		"peer token":        func(a *fakeKernel) { a.process.value.identity.user = guest },
	} {
		t.Run(name, func(t *testing.T) {
			p := testProfile()
			a := newFake(p)
			change(a)
			f, _ := NewNativeFactory(p)
			f.api = a
			peer, e := f.Authenticate(authContext(t), 99)
			if e == nil {
				t.Fatal("不明または異なる発信元を受理しました")
			}
			if peer != nil {
				if e := peer.Close(authContext(t)); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}

func TestCloseFailureRetainsOwnedHandlesForRetry(t *testing.T) {
	p := testProfile()
	a := newFake(p)
	f, _ := NewNativeFactory(p)
	f.api = a
	peer, e := f.Authenticate(authContext(t), 99)
	if e != nil {
		t.Fatal(e)
	}
	a.process.fail = true
	if peer.Close(authContext(t)) != ErrUnconfirmed {
		t.Fatal("close 失敗を回収済み扱いしました")
	}
	if peer.Recheck(authContext(t)) != ErrDenied {
		t.Fatal("回収中の proof を再利用しました")
	}
	a.process.fail = false
	if e := peer.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
	if a.process.closes != 2 || a.service.closes != 1 {
		t.Fatal("失敗した参照の再回収または成功参照の保持が不正です")
	}
	for _, file := range a.files {
		if file.closes != 1 {
			t.Fatal("image handle の二重 close または漏れです")
		}
	}
	if peer.Recheck(authContext(t)) != ErrClosed {
		t.Fatal("close 済みを認証済み扱いしました")
	}
}

func TestPartialServiceOpenFailureReturnsOwnedScope(t *testing.T) {
	p := testProfile()
	a := newFake(p)
	a.openServiceError = ErrDenied
	f, _ := NewNativeFactory(p)
	f.api = a
	peer, e := f.Authenticate(authContext(t), 99)
	if e != ErrDenied || peer == nil {
		t.Fatal("部分取得した所有物を失いました")
	}
	a.service.fail = true
	if peer.Close(authContext(t)) != ErrUnconfirmed {
		t.Fatal("回収失敗を隠しました")
	}
	a.service.fail = false
	if e := peer.Close(authContext(t)); e != nil || a.service.closes != 2 {
		t.Fatal("保留参照を再回収できませんでした")
	}
}

func TestServiceScopeUsesExistingPrimaryAndCurrentFixedImage(t *testing.T) {
	p := testProfile()
	a := newFake(p)
	a.currentPath = p.SupervisorImage.Path
	a.service.pid = 9
	f, _ := NewNativeFactory(p)
	f.api = a
	proof, e := f.VerifyService(authContext(t), 73)
	if e != nil || proof == nil {
		t.Fatalf("既存 primary token の固定 service を拒否しました: %v", e)
	}
	a.borrowed = &identity{user: guest, kind: 1}
	if proof.Recheck(authContext(t)) != ErrDenied {
		t.Fatal("借用 token の変化を受理しました")
	}
	if e := proof.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
	if a.process.closes != 0 {
		t.Fatal("service 確認で別 process を開閉しました")
	}
}

func TestFactoryCopiesProtectedConfiguration(t *testing.T) {
	p := testProfile()
	a := newFake(testProfile())
	f, e := NewNativeFactory(p)
	if e != nil {
		t.Fatal(e)
	}
	f.api = a
	p.Keys.ServiceSID = guest
	p.ChildImage.Nodes[0].Path = `D:\`
	p.SupervisorImage.Nodes[1].ACL[0].SID = guest
	peer, e := f.Authenticate(authContext(t), 99)
	if e != nil {
		t.Fatalf("構築後の入力変更に影響されました: %v", e)
	}
	if e := peer.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
}

func TestImageMutationDeniesRecheckAndRetainsOwnership(t *testing.T) {
	for name, change := range map[string]func(*fileState){
		"reparse":   func(s *fileState) { s.reparse = true },
		"volume":    func(s *fileState) { s.volume = 8 },
		"fileid":    func(s *fileState) { s.id = [16]byte{2} },
		"path":      func(s *fileState) { s.path = `C:\other.exe` },
		"links":     func(s *fileState) { s.links = 2 },
		"acl":       func(s *fileState) { s.acl = []protectedkey.ACE{{SID: guest, Mask: 2}} },
		"null dacl": func(s *fileState) { s.dacl = false },
	} {
		t.Run(name, func(t *testing.T) {
			p := testProfile()
			a := newFake(p)
			f, _ := NewNativeFactory(p)
			f.api = a
			peer, e := f.Authenticate(authContext(t), 99)
			if e != nil {
				t.Fatal(e)
			}
			change(&a.files[1].value)
			if peer.Recheck(authContext(t)) != ErrDenied {
				t.Fatal("保持 image の変更を受理しました")
			}
			if e := peer.Close(authContext(t)); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestCloseDoesNotReauthenticateWhileReleasingOwnedReferences(t *testing.T) {
	p := testProfile()
	a := newFake(p)
	f, _ := NewNativeFactory(p)
	f.api = a
	peer, e := f.Authenticate(authContext(t), 99)
	if e != nil {
		t.Fatal(e)
	}
	afterFirstClose := 0
	a.files[len(a.files)-1].onClose = func() { a.dirty = true; afterFirstClose = a.queryCalls }
	if e := peer.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
	if a.queryCalls != afterFirstClose {
		t.Fatal("既所有 handle の回収中に主体を再 query しました")
	}
	if a.process.closes != 1 || a.service.closes != 1 {
		t.Fatal("回収専用の Close が残りの既所有 handle を失いました")
	}
}

func TestFatalKernelFailureCannotBecomeOrdinaryDenial(t *testing.T) {
	p := testProfile()
	a := newFake(p)
	a.fileError = ErrFatal
	f, _ := NewNativeFactory(p)
	f.api = a
	peer, e := f.Authenticate(authContext(t), 99)
	if peer == nil || e != ErrFatal {
		t.Fatalf("native 境界の fatal を通常拒否へ変換しました: %v", e)
	}
	if peer.Close(authContext(t)) != ErrFatal {
		t.Fatal("fatal scope を別の確認で再利用しました")
	}
}

func TestRecheckQueryFailureIsClosedEvenAfterWriterEOF(t *testing.T) {
	for name, fail := range map[string]func(*fakeKernel){
		"pipe query after EOF": func(a *fakeKernel) { a.pipeError = ErrDenied },
		"SCM query":            func(a *fakeKernel) { a.service.queryError = ErrDenied },
		"peer query":           func(a *fakeKernel) { a.process.queryError = ErrDenied },
		"generation reused":    func(a *fakeKernel) { a.process.value.creation++ },
	} {
		t.Run(name, func(t *testing.T) {
			p := testProfile()
			a := newFake(p)
			f, _ := NewNativeFactory(p)
			f.api = a
			peer, e := f.Authenticate(authContext(t), 99)
			if e != nil {
				t.Fatal(e)
			}
			fail(a)
			if peer.Recheck(authContext(t)) != ErrDenied {
				t.Fatal("query 不明または generation 変更を受理しました")
			}
			a.pipeError = nil
			a.service.queryError = nil
			a.process.queryError = nil
			if peer.Recheck(authContext(t)) != ErrDenied {
				t.Fatal("拒否した proof を再利用しました")
			}
			if e := peer.Close(authContext(t)); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestCancelledClosePreservesReferencesForIndependentCleanup(t *testing.T) {
	p := testProfile()
	a := newFake(p)
	f, _ := NewNativeFactory(p)
	f.api = a
	peer, e := f.Authenticate(authContext(t), 99)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(authContext(t))
	cancel()
	if peer.Close(ctx) != ErrUnconfirmed {
		t.Fatal("期限のない回収を完了扱いしました")
	}
	if a.process.closes != 0 || a.service.closes != 0 {
		t.Fatal("cancel 済みの cleanup で照会を続けました")
	}
	if e := peer.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
}

func TestPartialProcessAndImageFailuresKeepReferences(t *testing.T) {
	for name, fail := range map[string]func(*fakeKernel){
		"process token open": func(a *fakeKernel) { a.openProcessError = ErrDenied },
		"file open":          func(a *fakeKernel) { a.fileError = ErrDenied },
	} {
		t.Run(name, func(t *testing.T) {
			p := testProfile()
			a := newFake(p)
			fail(a)
			f, _ := NewNativeFactory(p)
			f.api = a
			peer, e := f.Authenticate(authContext(t), 99)
			if peer == nil || e != ErrDenied {
				t.Fatal("部分取得を拒否し所有 scope を返しませんでした")
			}
			if e := peer.Close(authContext(t)); e != nil {
				t.Fatal(e)
			}
			for _, file := range a.files {
				if file.closes != 1 {
					t.Fatal("途中取得した image handle を失いました")
				}
			}
			if name == "process token open" && a.process.closes != 1 {
				t.Fatal("途中取得した process を失いました")
			}
		})
	}
}

func TestImageHashAndDuringHashMetadataDeny(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(strconv.FormatBool(during), func(t *testing.T) {
			p := testProfile()
			a := newFake(p)
			if during {
				a.onRead = func() { a.files[1].value.size = 4 }
			} else {
				p.ChildImage.SHA256[0] ^= 1
			}
			f, _ := NewNativeFactory(p)
			f.api = a
			peer, e := f.Authenticate(authContext(t), 99)
			if peer == nil || e != ErrDenied {
				t.Fatal("hash または hash 中の metadata 変更を受理しました")
			}
			if e := peer.Close(authContext(t)); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestInvalidBorrowedHandlesNeverReachKernel(t *testing.T) {
	f, _ := NewNativeFactory(testProfile())
	for _, h := range []uintptr{0, ^uintptr(0), ^uintptr(0) - 1, ^uintptr(0) - 2, ^uintptr(0) - 3} {
		if peer, e := f.Authenticate(authContext(t), h); peer != nil || e != ErrDenied {
			t.Fatal("null または pseudo read handle を受理しました")
		}
		if proof, e := f.VerifyService(authContext(t), h); proof != nil || e != ErrDenied {
			t.Fatal("null または pseudo token handle を受理しました")
		}
	}
}

func TestCleanupOnlyCloseRetainsFatalAndRetriesOwnedReferences(t *testing.T) {
	type proof interface {
		Recheck(context.Context) error
		Close(context.Context) error
	}
	for _, serviceMode := range []bool{false, true} {
		for _, condition := range []string{"foreign", "dirty", "poisoned"} {
			t.Run(strconv.FormatBool(serviceMode)+"/"+condition, func(t *testing.T) {
				p := testProfile()
				a := newFake(p)
				if serviceMode {
					a.currentPath = p.SupervisorImage.Path
					a.service.pid = 9
				}
				f, _ := NewNativeFactory(p)
				f.api = a
				var scope proof
				var e error
				if serviceMode {
					scope, e = f.VerifyService(authContext(t), 73)
				} else {
					scope, e = f.Authenticate(authContext(t), 99)
				}
				if e != nil {
					t.Fatal(e)
				}
				expectedQueries := 0
				switch condition {
				case "foreign":
					a.id++
				case "dirty":
					a.dirty = true
					expectedQueries = 1
				case "poisoned":
					a.dirty = true
					if scope.Recheck(authContext(t)) != ErrFatal {
						t.Fatal("汚染した主体を受理しました")
					}
					a.dirty = false
				}
				before := a.queryCalls
				a.files[0].fail = true
				e = scope.Close(authContext(t))
				if !errors.Is(e, ErrFatal) || !errors.Is(e, ErrUnconfirmed) {
					t.Fatalf("fatal と未回収を併記しませんでした: %v", e)
				}
				if a.queryCalls != before+expectedQueries {
					t.Fatal("回収専用の Close が新しい認証 query/read/open を行いました")
				}
				for _, file := range a.files {
					if file.closes != 1 {
						t.Fatal("汚染後の既所有 image handle を回収しませんでした")
					}
				}
				if a.service.closes != 1 || (!serviceMode && a.process.closes != 1) {
					t.Fatal("汚染後の既所有 origin handle を回収しませんでした")
				}
				before = a.queryCalls
				a.files[0].fail = false
				if e := scope.Close(authContext(t)); !errors.Is(e, ErrFatal) || errors.Is(e, ErrUnconfirmed) {
					t.Fatalf("回収後に fatal を保持しませんでした: %v", e)
				}
				if a.queryCalls != before || a.files[0].closes != 2 || a.service.closes != 1 {
					t.Fatal("回収 retry が認証 query または成功済み参照の close を行いました")
				}
				if e := scope.Recheck(authContext(t)); !errors.Is(e, ErrFatal) {
					t.Fatal("回収後に認証を復活させました")
				}
				if e := scope.Close(authContext(t)); !errors.Is(e, ErrFatal) {
					t.Fatal("closed 後に fatal を忘れました")
				}
				if a.queryCalls != before {
					t.Fatal("poison 済みの認証を再 query しました")
				}
			})
		}
	}
}

func TestClosedCleanScopeCloseIsIdempotentAcrossThreads(t *testing.T) {
	p := testProfile()
	a := newFake(p)
	f, _ := NewNativeFactory(p)
	f.api = a
	peer, e := f.Authenticate(authContext(t), 99)
	if e != nil {
		t.Fatal(e)
	}
	if e := peer.Close(authContext(t)); e != nil {
		t.Fatal(e)
	}
	beforeQueries, beforeThreads := a.queryCalls, a.threadCalls
	a.id++
	if e := peer.Close(authContext(t)); e != nil {
		t.Fatalf("回収済みの正常な scope を新たに poison しました: %v", e)
	}
	if a.queryCalls != beforeQueries || a.threadCalls != beforeThreads {
		t.Fatal("回収済みの Close が主体または thread を再 query しました")
	}
	if e := peer.Recheck(authContext(t)); e != ErrClosed {
		t.Fatalf("回収済みの scope を閉鎖済みとして拒否しませんでした: %v", e)
	}
	for _, file := range a.files {
		if file.closes != 1 {
			t.Fatal("回収済みの image handle を再 close しました")
		}
	}
	if a.service.closes != 1 || a.process.closes != 1 {
		t.Fatal("回収済みの origin handle を再 close しました")
	}
}
