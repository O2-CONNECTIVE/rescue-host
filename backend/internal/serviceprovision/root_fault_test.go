package serviceprovision

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"testing"
	"time"
)

func TestRootOpenRejectsDigestSizeMutationAndUnsafeHeldPaths(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(Anchor, *rootFakeKernel)
	}{
		{"digest mismatch", func(a Anchor, k *rootFakeKernel) { k.files[a.DigestPath].data[0] = 'a' }},
		{"noncanonical digest", func(a Anchor, k *rootFakeKernel) { k.files[a.DigestPath].data[0] = 'B' }},
		{"digest extra byte", func(a Anchor, k *rootFakeKernel) { k.files[a.DigestPath].state.size = 66 }},
		{"record zero size", func(a Anchor, k *rootFakeKernel) { k.files[a.RecordPath].state.size = 0 }},
		{"record oversize", func(a Anchor, k *rootFakeKernel) { k.files[a.RecordPath].state.size = 256*1024 + 1 }},
		{"read exceeds snapshot", func(a Anchor, k *rootFakeKernel) {
			k.files[a.RecordPath].data = append(k.files[a.RecordPath].data, '!')
		}},
		{"guest writer despite DENY", func(a Anchor, k *rootFakeKernel) {
			s := &k.files[a.RecordPath].state
			s.acl = append(s.acl, protectedkey.ACE{Type: 1, SID: "S-1-5-21-1-2-3-1002", Mask: 0x1f01ff}, protectedkey.ACE{SID: "S-1-5-21-1-2-3-1002", Mask: 2})
		}},
		{"unknown ancestor writer", func(a Anchor, k *rootFakeKernel) {
			s := &k.files[`C:\safe`].state
			s.acl = append(s.acl, protectedkey.ACE{SID: "S-1-5-21-9", Mask: 0x40000000})
		}},
		{"observed user owner is not trusted root", func(a Anchor, k *rootFakeKernel) { k.files[a.RecordPath].state.owner = rootUserSID }},
		{"reparse", func(a Anchor, k *rootFakeKernel) { k.files[`C:\safe`].state.reparse = true }},
		{"volume", func(a Anchor, k *rootFakeKernel) { k.files[a.DigestPath].state.volume = 8 }},
		{"filesystem", func(a Anchor, k *rootFakeKernel) { k.files[a.DigestPath].state.filesystem = "FAT32" }},
		{"path", func(a Anchor, k *rootFakeKernel) { k.files[a.RecordPath].state.path = `C:\safe\other` }},
		{"hardlink", func(a Anchor, k *rootFakeKernel) { k.files[a.RecordPath].state.links = 2 }},
		{"file ID changes after read", func(a Anchor, k *rootFakeKernel) {
			f := k.files[a.RecordPath]
			f.onInspect = func() {
				if f.inspects == 2 {
					f.state.id[0]++
				}
			}
		}},
		{"owner changes after read", func(a Anchor, k *rootFakeKernel) {
			f := k.files[a.RecordPath]
			f.onInspect = func() {
				if f.inspects == 2 {
					f.state.owner = rootServiceSID
				}
			}
		}},
		{"primary changes after read", func(a Anchor, k *rootFakeKernel) { k.files[a.RecordPath].onRead = func() { k.identity.modifiedID++ } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := rootAnchor()
			k := rootFixture(a)
			tc.mutate(a, k)
			r, _ := NewNativeRoot(a)
			r.api = k
			loaded, err := r.Open(rootContext(t))
			if loaded == nil || err == nil {
				t.Fatal("unsafe root accepted", err)
			}
			if data, e := loaded.Record(); data != nil || e == nil {
				t.Fatal("failed root exposed record")
			}
			closeErr := loaded.Close(rootContext(t))
			if errors.Is(err, ErrRootFatal) {
				if !errors.Is(closeErr, ErrRootFatal) {
					t.Fatal("Fatal cleanup lost", closeErr)
				}
			} else if closeErr != nil {
				t.Fatal(closeErr)
			}
		})
	}
}
func TestRootPendingReadKeepsCompletionOwnedUntilSameLoadedRecovers(t *testing.T) {
	a := rootAnchor()
	k := rootFixture(a)
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	file := k.files[a.RecordPath]
	file.op = &rootFakeOp{}
	file.onRead = cancel
	r, _ := NewNativeRoot(a)
	r.api = k
	loaded, err := r.Open(ctx)
	if loaded == nil || !errors.Is(err, ErrRootUnconfirmed) {
		t.Fatal(err)
	}
	if !file.op.cancelled || file.op.released || file.closes != 0 || file.reads != 1 {
		t.Fatal("pending Readを完了扱い")
	}
	if token, err := loaded.QueryToken(); token != 0 || err == nil {
		t.Fatal("partialはtokenを公開できない", err)
	}
	if err = loaded.Close(rootContext(t)); !errors.Is(err, ErrRootUnconfirmed) {
		t.Fatal(err)
	}
	if file.op.released || file.closes != 0 || file.reads != 1 {
		t.Fatal("Cancel/Closeをcompletion扱い")
	}
	file.op.done = true
	file.op.err = context.Canceled
	if err = loaded.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	if !file.op.released || file.closes != 1 || file.reads != 1 || k.token.closes != 1 {
		t.Fatal("同じownerで回収しない")
	}
}
