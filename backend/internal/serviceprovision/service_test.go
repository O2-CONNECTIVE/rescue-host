package serviceprovision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

type noApprovalAtConstruction struct{ reads int }

func (a *noApprovalAtConstruction) ReadApproved(context.Context) (ephemeral.ApprovedPolicy, error) {
	a.reads++
	return ephemeral.ApprovedPolicy{}, ErrApprovalUnconfigured
}

func TestFixedRootDoesNotTrustRecordClaimOfAnotherService(t *testing.T) {
	a := rootAnchor()
	k := rootFixture(a)
	// 内容とdigest自体は対応するが、別serviceを指す固定record。
	data := []byte(strings.ReplaceAll(validJSON, "SyntheticService", "DifferentService"))
	if _, err := DecodeRecord(data); err != nil {
		t.Fatal("structurally valid fixture:", err)
	}
	k.files[a.RecordPath].data = data
	k.files[a.RecordPath].state.size = int64(len(data))
	hash := sha256.Sum256(data)
	k.files[a.DigestPath].data = []byte(hex.EncodeToString(hash[:]) + "\n")
	r, err := NewNativeRoot(a)
	if err != nil {
		t.Fatal(err)
	}
	r.api = k
	owner, err := r.PrepareService(rootContext(t))
	if owner == nil || !errors.Is(err, ErrRecord) {
		t.Fatalf("record subject replaced observed service: partialOwner=%t err=%v", owner != nil, err)
	}
	if service, err := owner.ComposeService(rootContext(t), 123, &noApprovalAtConstruction{}); service != nil || !errors.Is(err, ErrRootDenied) {
		t.Fatal("partial preparation became a service")
	}
	if err := owner.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	if k.token.closes != 1 {
		t.Fatal("partial owner lost query token")
	}
}

type subjectFaultKernel struct {
	*rootFakeKernel
	calls, failAt int
}

func (k *subjectFaultKernel) principal(handle uintptr) (rootPrincipal, error) {
	k.calls++
	if k.calls == k.failAt {
		return rootPrincipal{threadToken: true}, ErrRootFatal
	}
	return k.rootFakeKernel.principal(handle)
}

func provisionRecordKernel(a Anchor) *subjectFaultKernel {
	k := &subjectFaultKernel{rootFakeKernel: rootFixture(a)}
	data := []byte(validJSON)
	k.files[a.RecordPath].data = data
	k.files[a.RecordPath].state.size = int64(len(data))
	hash := sha256.Sum256(data)
	k.files[a.DigestPath].data = []byte(hex.EncodeToString(hash[:]) + "\n")
	return k
}

func TestPrepareServiceKeepsFatalFromSubjectObservation(t *testing.T) {
	a := rootAnchor()
	// 公開getterの境界を正常なkernel seamで一度観測し、次のSubject照会で故障させる。
	// 検証結果や信頼値のcallbackを置換せず、実Root/decoderを通す。
	probeKernel := provisionRecordKernel(a)
	probeRoot, _ := NewNativeRoot(a)
	probeRoot.api = probeKernel
	probe, err := probeRoot.Open(rootContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := probe.Record(); err != nil {
		t.Fatal(err)
	}
	failAt := probeKernel.calls + 1
	if err := probe.Close(rootContext(t)); err != nil {
		t.Fatal(err)
	}
	k := provisionRecordKernel(a)
	k.failAt = failAt
	r, _ := NewNativeRoot(a)
	r.api = k
	owner, err := r.PrepareService(rootContext(t))
	if owner == nil || !errors.Is(err, ErrRootFatal) {
		t.Fatalf("subject fatal lost: partialOwner=%t err=%v", owner != nil, err)
	}
	if service, err := owner.ComposeService(rootContext(t), 123, &noApprovalAtConstruction{}); service != nil || !errors.Is(err, ErrRootFatal) {
		t.Fatal("repeated composition laundered fatal")
	}
	if err := owner.Close(rootContext(t)); !errors.Is(err, ErrRootFatal) {
		t.Fatalf("cleanup lost fatal: %v", err)
	}
	if k.token.closes != 1 {
		t.Fatal("fatal partial owner lost query token")
	}
}

func TestComposeFixedValuesKeepsServiceAtZeroGrants(t *testing.T) {
	approval := &noApprovalAtConstruction{}
	// 純粋constructorの合成handle値。実tokenの取得/権限/信頼を証明しない。
	service, err := ComposeService(validRecord(), 123, approval)
	if err != nil || service == nil {
		t.Fatalf("fixed source composition: %v", err)
	}
	state := service.Snapshot()
	if approval.reads != 0 || state.Generation != 0 || state.HasAuthority || state.InstanceEpoch != ([32]byte{}) {
		t.Fatalf("composition issued/restored an approval: %+v reads=%d", state, approval.reads)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
