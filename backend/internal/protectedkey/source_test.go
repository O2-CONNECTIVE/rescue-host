package protectedkey

import (
	"bytes"
	"context"
	"errors"
	"testing"
)

type memoryKernel struct {
	principalValue principal
	principalErr   error
	files          map[string]*memoryFile
	held           int
	principalCalls int
}

func (m *memoryKernel) principal(context.Context) (principal, error) {
	m.principalCalls++
	return m.principalValue, m.principalErr
}
func (m *memoryKernel) open(_ context.Context, path string, leaf bool) (protectedFile, error) {
	f := m.files[path]
	if f == nil {
		return nil, errors.New("synthetic failure")
	}
	m.held++
	return f, nil
}

type memoryFile struct {
	kernel                     *memoryKernel
	state                      snapshot
	reader                     *bytes.Reader
	reads, inspections, closes int
	after                      func(*snapshot)
	closeErr                   error
}

func (m *memoryFile) inspect() (snapshot, error) {
	m.inspections++
	s := m.state
	s.acl = append([]ACE(nil), s.acl...)
	if m.inspections > 1 && m.after != nil {
		m.after(&s)
	}
	return s, nil
}
func (m *memoryFile) Read(dst []byte) (int, error) {
	m.reads++
	if m.kernel.held != 3 {
		return 0, errors.New("ancestor handle was released")
	}
	return m.reader.Read(dst)
}
func (m *memoryFile) close() error { m.closes++; m.kernel.held--; return m.closeErr }

func protectedMemoryLoader(t *testing.T) (*Loader, *memoryKernel) {
	t.Helper()
	p := provisionedProfile()
	api := &memoryKernel{principalValue: principal{userSID: p.ProcessUserSID, tokenType: 1, groups: []tokenGroup{{sid: testService, attributes: 4}}}, files: map[string]*memoryFile{}}
	data := syntheticPEM(t)
	for i, node := range p.Catalog[testRef].Nodes {
		s := snapshot{path: node.Path, owner: node.OwnerSID, acl: append([]ACE(nil), node.ACL...), daclPresent: true, volume: 7, filesystem: "NTFS", id: [16]byte{byte(i + 1)}, disk: true, directory: i < 2, links: 1}
		if i == 2 {
			s.size = int64(len(data))
		}
		api.files[node.Path] = &memoryFile{kernel: api, state: s, reader: bytes.NewReader(data)}
	}
	return &Loader{catalog: p.Catalog, source: &securedSource{profile: p, api: api}}, api
}

func TestLoadRetainsAllNodesUntilAfterReadAndReinspection(t *testing.T) {
	loader, api := protectedMemoryLoader(t)
	signer, err := loader.Load(loadContext(t), testRef)
	if err != nil || signer == nil {
		t.Fatalf("Load failed: %v", err)
	}
	if api.held != 0 || api.principalCalls != 2 {
		t.Fatal("handles or principal lifecycle incomplete")
	}
	for _, file := range api.files {
		if file.inspections != 2 || file.closes != 1 {
			t.Fatal("node was not rechecked and closed")
		}
	}
}

func TestLoadRejectsUntrustedPrincipalBeforeOpen(t *testing.T) {
	for name, change := range map[string]func(*memoryKernel){
		"wrong user":          func(a *memoryKernel) { a.principalValue.userSID = testGuest },
		"missing service":     func(a *memoryKernel) { a.principalValue.groups = nil },
		"disabled service":    func(a *memoryKernel) { a.principalValue.groups[0].attributes = 0 },
		"deny-only service":   func(a *memoryKernel) { a.principalValue.groups[0].attributes = 0x14 },
		"impersonation token": func(a *memoryKernel) { a.principalValue.tokenType = 2 },
		"thread token":        func(a *memoryKernel) { a.principalValue.threadToken = true },
		"unknown query":       func(a *memoryKernel) { a.principalErr = errors.New("synthetic token query failure") },
	} {
		t.Run(name, func(t *testing.T) {
			loader, api := protectedMemoryLoader(t)
			change(api)
			signer, err := loader.Load(loadContext(t), testRef)
			if signer != nil || err != ErrFatal || api.held != 0 {
				t.Fatal("untrusted principal accepted")
			}
			for _, f := range api.files {
				if f.inspections != 0 {
					t.Fatal("opened before principal authorization")
				}
			}
		})
	}
}

func TestLoadRejectsUnsafeNodeBeforeReadingAndClosesEveryHandle(t *testing.T) {
	for name, mutate := range map[string]func(*snapshot){
		"owner":                func(s *snapshot) { s.owner = testGuest },
		"NULL DACL":            func(s *snapshot) { s.daclPresent = false },
		"unexpected ACL":       func(s *snapshot) { s.acl = []ACE{{SID: testService, Mask: 1}} },
		"unsafe ACL":           func(s *snapshot) { s.acl = append(s.acl, ACE{SID: testGuest, Mask: 2}) },
		"reparse":              func(s *snapshot) { s.reparse = true },
		"non-disk":             func(s *snapshot) { s.disk = false },
		"wrong path":           func(s *snapshot) { s.path = `C:\elsewhere` },
		"wrong volume":         func(s *snapshot) { s.volume++ },
		"wrong filesystem":     func(s *snapshot) { s.filesystem = "unknown" },
		"missing ID":           func(s *snapshot) { s.id = [16]byte{} },
		"wrong directory type": func(s *snapshot) { s.directory = true },
		"hardlink":             func(s *snapshot) { s.links = 2 },
		"oversize":             func(s *snapshot) { s.size = 16385 },
	} {
		t.Run(name, func(t *testing.T) {
			loader, api := protectedMemoryLoader(t)
			mutate(&api.files[`C:\protected\host-key`].state)
			signer, err := loader.Load(loadContext(t), testRef)
			if signer != nil || err != ErrDenied || api.held != 0 {
				t.Fatal("unsafe metadata accepted or handle retained")
			}
			for _, f := range api.files {
				if f.reads != 0 || f.closes != 1 {
					t.Fatal("read unsafe key or failed to close")
				}
			}
		})
	}
}

func TestLoadRejectsNodeChangesAfterReading(t *testing.T) {
	for _, path := range []string{`C:\`, `C:\protected`, `C:\protected\host-key`} {
		loader, api := protectedMemoryLoader(t)
		api.files[path].after = func(s *snapshot) { s.id[0]++ }
		signer, err := loader.Load(loadContext(t), testRef)
		if signer != nil || err != ErrDenied || api.held != 0 {
			t.Fatal("node replacement accepted or handles retained")
		}
	}
}

func TestLoadChecksPrincipalAfterFailedReadAndCleanup(t *testing.T) {
	loader, api := protectedMemoryLoader(t)
	api.files[`C:\protected\host-key`].after = func(s *snapshot) {
		s.id[0]++
		api.principalValue.threadToken = true
	}
	signer, err := loader.Load(loadContext(t), testRef)
	if signer != nil || err != ErrFatal || api.held != 0 {
		t.Fatalf("dirty cleanup must fail fatally without retained handles: %v", err)
	}
}
