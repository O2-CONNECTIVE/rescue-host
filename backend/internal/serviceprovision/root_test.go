package serviceprovision

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"sync"
	"testing"
	"time"
)

func rootContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}
func rootAnchor() Anchor {
	return Anchor{ServiceName: "SyntheticService", RecordPath: `C:\safe\record.json`, DigestPath: `C:\safe\record.sha256`, Volume: protectedkey.VolumeProfile{Root: `C:\`, Serial: 7, Filesystem: "NTFS"}, ReadTimeout: time.Second, CleanupTimeout: 10 * time.Millisecond}
}
func TestRootRejectsUnconfiguredAnchorBeforeNativeAccess(t *testing.T) {
	if r, err := NewNativeRoot(Anchor{}); r != nil || !errors.Is(err, ErrRootConfig) {
		t.Fatal(r, err)
	}
	var r Root
	if l, err := r.Open(rootContext(t)); l != nil || !errors.Is(err, ErrRootConfig) {
		t.Fatal(l, err)
	}
	if r, err := NewNativeRoot(rootAnchor()); r == nil || err != nil {
		t.Fatal("pure configured constructor", err)
	}
}

const rootServiceSID = "S-1-5-80-1-2-3-4-5"
const rootUserSID = "S-1-5-21-1-2-3-1000"

type rootFakeToken struct {
	closes  int
	onClose func()
}

func (t *rootFakeToken) value() uintptr { return 73 }
func (t *rootFakeToken) close() error {
	t.closes++
	if t.onClose != nil {
		t.onClose()
	}
	return nil
}

type rootFakeOp struct {
	done, cancelled, released bool
	n                         uint32
	err                       error
}

func (o *rootFakeOp) poll() (bool, uint32, error) { return o.done, o.n, o.err }
func (o *rootFakeOp) cancel() error               { o.cancelled = true; return nil }
func (o *rootFakeOp) release() error {
	if !o.done {
		panic("未完了解放")
	}
	o.released = true
	return nil
}

type rootFakeFile struct {
	state           rootFileState
	data            []byte
	closes          int
	op              *rootFakeOp
	onRead          func()
	onInspect       func()
	inspects, reads int
}

func (f *rootFakeFile) inspect() (rootFileState, error) {
	f.inspects++
	if f.onInspect != nil {
		f.onInspect()
	}
	return f.state, nil
}
func (f *rootFakeFile) read(buffer []byte) (rootOperation, error) {
	f.reads++
	if f.onRead != nil {
		f.onRead()
	}
	copy(buffer, f.data)
	if f.op == nil {
		f.op = &rootFakeOp{done: true, n: uint32(len(f.data))}
	}
	return f.op, nil
}
func (f *rootFakeFile) close() error { f.closes++; return nil }

type rootFakeKernel struct {
	files       map[string]*rootFakeFile
	token       rootFakeToken
	identity    rootPrincipal
	thread      uint32
	opened      []string
	lookups     int
	onPrincipal func(uintptr)
	lookupErr   error
}

func (k *rootFakeKernel) threadID() uint32 { return k.thread }
func (k *rootFakeKernel) serviceSID(string) (string, error) {
	k.lookups++
	return rootServiceSID, k.lookupErr
}
func (k *rootFakeKernel) principal(handle uintptr) (rootPrincipal, error) {
	if k.onPrincipal != nil {
		k.onPrincipal(handle)
	}
	return k.identity, nil
}
func (k *rootFakeKernel) openQueryToken() (rootToken, error) { return &k.token, nil }
func (k *rootFakeKernel) open(path string, leaf bool) (rootFile, error) {
	k.opened = append(k.opened, path)
	return k.files[path], nil
}
func rootFixture(a Anchor) *rootFakeKernel {
	k := &rootFakeKernel{thread: 7, files: map[string]*rootFakeFile{}, identity: rootPrincipal{user: rootUserSID, kind: 1, tokenID: 1, groups: []rootGroup{{sid: rootServiceSID, attributes: 4}}}}
	for i, p := range []string{`C:\`, `C:\safe`, a.RecordPath, a.DigestPath} {
		data := []byte(nil)
		if p == a.RecordPath {
			data = []byte("hello world")
		}
		if p == a.DigestPath {
			data = []byte("b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9\n")
		}
		k.files[p] = &rootFakeFile{data: data, state: rootFileState{path: p, owner: "S-1-5-18", filesystem: "NTFS", volume: 7, id: [16]byte{byte(i + 1)}, disk: true, dacl: true, directory: i < 2, links: 1, size: int64(len(data)), acl: []protectedkey.ACE{{SID: rootServiceSID, Mask: 0x1f01ff}}}}
	}
	return k
}
func TestRootLoadsProtectedPairAndOwnsQueryOnlyToken(t *testing.T) {
	a := rootAnchor()
	k := rootFixture(a)
	r, err := NewNativeRoot(a)
	if err != nil {
		t.Fatal(err)
	}
	r.api = k
	loaded, err := r.Open(rootContext(t))
	if err != nil || loaded == nil {
		t.Fatal(err)
	}
	data, err := loaded.Record()
	if err != nil || string(data) != "hello world" {
		t.Fatal(string(data), err)
	}
	data[0] = 'X'
	data, err = loaded.Record()
	if err != nil || string(data) != "hello world" {
		t.Fatal("record alias", err)
	}
	subject, err := loaded.Subject()
	if err != nil || subject != (Subject{ProcessUserSID: rootUserSID, ServiceSID: rootServiceSID, ServiceName: a.ServiceName}) {
		t.Fatal(subject, err)
	}
	if token, err := loaded.QueryToken(); token != 73 || err != nil {
		t.Fatal(token, err)
	}
	if k.lookups != 1 || len(k.opened) != 4 {
		t.Fatal("固定rootを保持しない", k.lookups, k.opened)
	}
	if err = loaded.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	if _, err = loaded.Record(); err == nil {
		t.Fatal("closed record")
	}
	if token, err := loaded.QueryToken(); token != 0 || err == nil {
		t.Fatal(token, err)
	}
	if k.token.closes != 1 {
		t.Fatal("query token ownership")
	}
}
func TestRootGettersAndCleanupRejectSameThreadImpersonationWithStickyFatal(t *testing.T) {
	for _, action := range []string{"record", "subject", "query-token", "close"} {
		t.Run(action, func(t *testing.T) {
			a := rootAnchor()
			k := rootFixture(a)
			r, _ := NewNativeRoot(a)
			r.api = k
			loaded, err := r.Open(rootContext(t))
			if err != nil {
				t.Fatal(err)
			}
			k.identity.threadToken = true
			switch action {
			case "record":
				_, err = loaded.Record()
			case "subject":
				_, err = loaded.Subject()
			case "query-token":
				_, err = loaded.QueryToken()
			case "close":
				err = loaded.Close(rootContext(t))
			}
			if !errors.Is(err, ErrRootFatal) {
				t.Fatal("同threadのdirty主体を見逃した", action, err)
			}
			k.identity.threadToken = false
			if err = loaded.Close(rootContext(t)); !errors.Is(err, ErrRootFatal) {
				t.Fatal("Fatalが消えた", err)
			}
			if _, err = loaded.Record(); !errors.Is(err, ErrRootFatal) {
				t.Fatal(err)
			}
		})
	}
}
func TestRootRejectsInvalidUTF8AnchorBeforeNativeConversion(t *testing.T) {
	a := rootAnchor()
	a.RecordPath = "C:\\safe\\bad\xff.json"
	if r, err := NewNativeRoot(a); r != nil || !errors.Is(err, ErrRootConfig) {
		t.Fatal("invalid UTF8 anchor", err)
	}
}

func TestRootCleanupStopsAtCanceledCallerWithoutStartingMoreCloses(t *testing.T) {
	for _, already := range []bool{true, false} {
		t.Run(map[bool]string{true: "already canceled", false: "cancel during token close"}[already], func(t *testing.T) {
			a := rootAnchor()
			k := rootFixture(a)
			r, _ := NewNativeRoot(a)
			r.api = k
			loaded, err := r.Open(rootContext(t))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
			defer cancel()
			if already {
				cancel()
			} else {
				k.token.onClose = cancel
			}
			if err = loaded.Close(ctx); !errors.Is(err, ErrRootUnconfirmed) {
				t.Fatal("caller取消後も回収成功扱い", err)
			}
			expected := 1
			if already {
				expected = 0
			}
			if k.token.closes != expected {
				t.Fatal("期限後にtoken Close開始", k.token.closes)
			}
			for _, file := range k.files {
				if file.closes != 0 {
					t.Fatal("期限後にfile Close開始")
				}
			}
			k.token.onClose = nil
			if err = loaded.Close(rootContext(t)); err != nil {
				t.Fatal(err)
			}
			if k.token.closes != 1 {
				t.Fatal("同tokenを再回収しない", k.token.closes)
			}
			for _, file := range k.files {
				if file.closes != 1 {
					t.Fatal("同held fileを再回収しない", file.closes)
				}
			}
		})
	}
}

func TestRootInFlightGetterCannotSucceedAfterCloseRevokesIt(t *testing.T) {
	a := rootAnchor()
	k := rootFixture(a)
	r, _ := NewNativeRoot(a)
	r.api = k
	loaded, err := r.Open(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	k.onPrincipal = func(uintptr) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-gate
	}
	result := make(chan error, 1)
	go func() { _, err := loaded.Record(); result <- err }()
	<-entered
	var release sync.Once
	joined := false
	t.Cleanup(func() {
		release.Do(func() { close(gate) })
		if !joined {
			<-result
		}
	})
	short, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err = loaded.Close(short); !errors.Is(err, ErrRootUnconfirmed) {
		t.Fatal(err)
	}
	release.Do(func() { close(gate) })
	err = <-result
	joined = true
	if err == nil {
		t.Fatal("Close後にinflight getterが成功")
	}
	k.onPrincipal = nil
	if err = loaded.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestRootPartialLookupAndUnauthorizedRoleDoNotInventFatalButDetectLaterDirtyCaller(t *testing.T) {
	for _, name := range []string{"lookup denied", "lookup denied then dirty", "missing role", "deny only role"} {
		t.Run(name, func(t *testing.T) {
			a := rootAnchor()
			k := rootFixture(a)
			if name == "lookup denied" || name == "lookup denied then dirty" {
				k.lookupErr = ErrRootDenied
			}
			if name == "missing role" {
				k.identity.groups = nil
			}
			if name == "deny only role" {
				k.identity.groups[0].attributes = 0x14
			}
			r, _ := NewNativeRoot(a)
			r.api = k
			loaded, err := r.Open(rootContext(t))
			if loaded == nil || !errors.Is(err, ErrRootDenied) || errors.Is(err, ErrRootFatal) {
				t.Fatal("unauthorizedをdirty/Fatal扱い", err)
			}
			if len(k.opened) != 0 {
				t.Fatal("role確認前にファイルを開いた")
			}
			if name == "lookup denied then dirty" {
				k.identity.threadToken = true
			}
			err = loaded.Close(rootContext(t))
			if name == "lookup denied then dirty" {
				if !errors.Is(err, ErrRootFatal) {
					t.Fatal("partial後のdirtyを見逃す", err)
				}
			} else if err != nil {
				t.Fatal("clean partial cleanupをFatal扱い", err)
			}
		})
	}
}
