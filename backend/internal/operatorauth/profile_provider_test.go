package operatorauth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type profileQueryFixture struct {
	snapshot             profileCandidateSnapshot
	queries              int
	closed               int
	onInspect            func(int)
	inspectErr, closeErr error
}

func (f *profileQueryFixture) inspect(context.Context) (profileCandidateSnapshot, error) {
	f.queries++
	if f.onInspect != nil {
		f.onInspect(f.queries)
	}
	return f.snapshot, f.inspectErr
}
func (f *profileQueryFixture) close(context.Context) error { f.closed++; return f.closeErr }

func providerFixture() (profileProviderPolicy, *profileQueryFixture) {
	op := tokenProjection{user: "S-1-5-21-1", authentication: 7, session: 3, integrity: 8192, kind: 2, level: 2}
	primary := op
	primary.kind, primary.level = 1, 0
	p := profileProviderPolicy{material: profilePipeMaterial{launchID: strings.Repeat("a", 64), profile: helperProfile{operator: op, guest: tokenProjection{user: "S-1-5-21-2"}}, cleanup: time.Second}, primary: primary, imagePath: `C:\Protected\operator.exe`}
	return p, &profileQueryFixture{snapshot: profileCandidateSnapshot{pid: 123, creation: 5, imagePath: p.imagePath, primary: primary, tokenID: 8, modifiedID: 9}}
}

func TestProtectedProfileProviderRejectsMissingPolicyWithoutQuery(t *testing.T) {
	p, q := providerFixture()
	p.primary.authentication = 0
	provider, err := newProtectedProfileProvider(p, q)
	if provider != nil || err == nil || q.queries != 0 {
		t.Fatalf("unconfigured policy accepted: %v %v queries=%d", provider, err, q.queries)
	}
}

func TestProtectedProfileProviderFatalRetiresBatchAcrossAcquireAndClose(t *testing.T) {
	for _, stage := range []string{"acquire", "close"} {
		t.Run(stage, func(t *testing.T) {
			p, q := providerFixture()
			provider, err := newProtectedProfileProvider(p, q)
			if err != nil {
				t.Fatal(err)
			}
			s, owner, ops, events, _, _ := batchFixture(t)
			s.producer = provider
			ops.newProfile = func(ctx context.Context) (batchProfile, error) {
				if _, _, err := prepareProfilePipe(ctx, provider); err != nil {
					return nil, err
				}
				return &memoryBatchProfile{events: events}, nil
			}
			if stage == "acquire" {
				q.inspectErr = ErrFatal
			}
			b, err := prepareBatch(s, owner, ops)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = b.Start(ctx)
			if stage == "close" {
				if err != nil {
					t.Fatal(err)
				}
				q.closeErr = ErrFatal
				err = b.Close(ctx)
			}
			if !errors.Is(err, ErrFatal) {
				t.Fatalf("fatal retired as ordinary error: %v", err)
			}
			q.closeErr, q.inspectErr = nil, nil
			if err = b.Close(ctx); !errors.Is(err, ErrFatal) {
				t.Fatalf("fatal recovered with clean fixture: %v", err)
			}
		})
	}
}

func TestProtectedProfileProviderDoesNotAdoptCandidateAsPolicy(t *testing.T) {
	p, q := providerFixture()
	q.snapshot.primary.authentication++
	provider, err := newProtectedProfileProvider(p, q)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if material, err := provider.prepare(ctx); err == nil || material != (profilePipeMaterial{}) {
		t.Fatalf("candidate became trusted policy: %#v %v", material, err)
	}
	q.snapshot.primary = p.primary
	if _, err := provider.prepare(ctx); err == nil {
		t.Fatal("failed attempt revived")
	}
}

func TestProtectedProfileProviderPinsCandidateWithoutChangingMessageKind(t *testing.T) {
	p, q := providerFixture()
	provider, err := newProtectedProfileProvider(p, q)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	material, err := provider.prepare(ctx)
	if err != nil || material != p.material || material.profile.operator.kind != 2 || material.profile.operator.level != 2 {
		t.Fatalf("fixed material changed: %#v %v", material, err)
	}
	q.snapshot.modifiedID++
	if err := provider.recheck(ctx); err == nil {
		t.Fatal("token drift accepted")
	}
	q.snapshot.modifiedID--
	if err := provider.recheck(ctx); err == nil {
		t.Fatal("drift failure revived")
	}
}

func TestProtectedProfileProviderRejectsPolicyAndEveryCandidateDrift(t *testing.T) {
	for _, field := range []string{"user", "authentication", "session", "integrity", "kind", "level", "restricted", "appContainer", "pid", "creation", "image", "token", "modified"} {
		t.Run(field, func(t *testing.T) {
			p, q := providerFixture()
			provider, err := newProtectedProfileProvider(p, q)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err = provider.prepare(ctx); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "user":
				q.snapshot.primary.user = "S-1-5-21-3"
			case "authentication":
				q.snapshot.primary.authentication++
			case "session":
				q.snapshot.primary.session++
			case "integrity":
				q.snapshot.primary.integrity++
			case "kind":
				q.snapshot.primary.kind = 2
			case "level":
				q.snapshot.primary.level = 2
			case "restricted":
				q.snapshot.primary.restricted = true
			case "appContainer":
				q.snapshot.primary.appContainer = true
			case "pid":
				q.snapshot.pid++
			case "creation":
				q.snapshot.creation++
			case "image":
				q.snapshot.imagePath = `C:\Other\operator.exe`
			case "token":
				q.snapshot.tokenID++
			case "modified":
				q.snapshot.modifiedID++
			}
			if err = provider.recheck(ctx); err == nil {
				t.Fatal("candidate drift accepted")
			}
		})
	}
}

func TestProtectedProfileProviderLateCancellationCannotPublishMaterial(t *testing.T) {
	p, q := providerFixture()
	provider, err := newProtectedProfileProvider(p, q)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	q.onInspect = func(int) { cancel() }
	if material, err := provider.prepare(ctx); err == nil || material != (profilePipeMaterial{}) {
		t.Fatal("late query cancellation accepted")
	}
}

func TestProtectedProfileProviderCannotExtendOriginalRequest(t *testing.T) {
	p, q := providerFixture()
	provider, err := newProtectedProfileProvider(p, q)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	if _, err = provider.prepare(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	fresh, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err = provider.recheck(fresh); err == nil {
		t.Fatal("fresh context revived original request")
	}
}

func TestProtectedProfileProviderRejectsOriginalCancellationDuringRecheck(t *testing.T) {
	p, q := providerFixture()
	provider, err := newProtectedProfileProvider(p, q)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = provider.prepare(ctx); err != nil {
		t.Fatal(err)
	}
	fresh, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	q.onInspect = func(int) { cancel() }
	if err = provider.recheck(fresh); err == nil {
		t.Fatal("late original cancellation accepted")
	}
}

func TestProtectedProfileProviderIsRetainedByPreparedBatch(t *testing.T) {
	p, q := providerFixture()
	provider, err := newProtectedProfileProvider(p, q)
	if err != nil {
		t.Fatal(err)
	}
	s, owner, ops, events, _, _ := batchFixture(t)
	s.producer = provider
	ops.newProfile = func(ctx context.Context) (batchProfile, error) {
		material, _, err := prepareProfilePipe(ctx, provider)
		if err != nil || material != p.material {
			return nil, ErrDenied
		}
		return &memoryBatchProfile{events: events}, nil
	}
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	if q.queries != 0 {
		t.Fatal("prepare queried candidate before registration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if q.queries < 2 {
		t.Fatal("candidate was not rechecked after preparation")
	}
	proof, err := b.Collect(ctx)
	if err != nil {
		t.Fatalf("successful Start lost original request before Collect: %v", err)
	}
	if _, err = b.Collector().Consume(ctx, proof); err != nil {
		t.Fatal(err)
	}
	if err = b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if q.closed != 1 {
		t.Fatalf("same provider not reclaimed: %d", q.closed)
	}
}
