package operatorauth

import (
	"context"
	"errors"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"reflect"
	"testing"
	"time"
)

type memoryServiceScope struct {
	events    *[]string
	closeErr  error
	onRecheck func()
}

func (s *memoryServiceScope) Recheck(context.Context) error {
	if s.onRecheck != nil {
		s.onRecheck()
	}
	return nil
}
func (s *memoryServiceScope) Close(context.Context) error {
	*s.events = append(*s.events, "close-scope")
	return s.closeErr
}

func scopedBatchFixture(t *testing.T) (*nativeBatch, *memoryServiceScope, *[]string) {
	t.Helper()
	s, owner, ops, events, _, _ := batchFixture(t)
	const servicePrimaryToken = uintptr(71)
	if servicePrimaryToken == s.launch.primaryToken {
		t.Fatal("fixture mixed service and helper subjects")
	}
	proof := &memoryServiceScope{events: events}
	var err error
	ops.scope, err = newBatchScope(servicePrimaryToken, batchScopeOps{
		threadID: func() uint32 { return 7 },
		clean:    func() error { return nil },
		verify: func(_ context.Context, token uintptr) (batchServiceScope, error) {
			if token != servicePrimaryToken {
				t.Error("service scope received helper-launch token instead of service token")
				return nil, ErrDenied
			}
			*events = append(*events, "verify-scope")
			return proof, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := prepareBatch(s, owner, ops)
	if err != nil {
		t.Fatal(err)
	}
	return b, proof, events
}

func TestBatchScopeAcquiresOnlyAtStartAndClosesAfterChild(t *testing.T) {
	b, _, events := scopedBatchFixture(t)
	if len(*events) != 0 {
		t.Fatal("prepare queried native scope", *events)
	}
	if err := b.Start(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Collect(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	if (*events)[0] != "verify-scope" {
		t.Fatal("scope was not acquired before I/O", *events)
	}
	want := []string{"close-profile", "close-report", "close-child", "close-scope"}
	if !reflect.DeepEqual((*events)[len(*events)-4:], want) {
		t.Fatal("scope close preceded I/O/child cleanup", *events)
	}
}

func TestBatchScopeCloseFailureCannotAcknowledgeCleanup(t *testing.T) {
	b, scope, _ := scopedBatchFixture(t)
	scope.closeErr = ErrUnconfirmed
	if err := b.Start(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(requestContext(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("scope close failure was acknowledged", err)
	}
}

func TestBatchScopePreStartCloseDoesNotQueryThread(t *testing.T) {
	b, _, events := scopedBatchFixture(t)
	b.ops.scope.ops.threadID = func() uint32 { t.Fatal("prestart thread query"); return 0 }
	b.ops.scope.ops.clean = func() error { t.Fatal("prestart token query"); return ErrFatal }
	if err := b.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*events, []string{"close-original-read"}) {
		t.Fatal(*events)
	}
}

func TestBatchScopeRejectsInvalidServiceTokenWithoutNativeQueries(t *testing.T) {
	ops := batchScopeOps{
		verify: func(context.Context, uintptr) (batchServiceScope, error) {
			t.Fatal("invalid service token queried")
			return nil, ErrDenied
		},
		threadID: func() uint32 { t.Fatal("pure scope preparation queried thread"); return 0 },
		clean:    func() error { t.Fatal("pure scope preparation queried token"); return ErrFatal },
	}
	for _, token := range []uintptr{0, ^uintptr(0), ^uintptr(0) - 1} {
		if scope, err := newBatchScope(token, ops); scope != nil || !errors.Is(err, ErrUnconfigured) {
			t.Errorf("invalid service token accepted: %x %v", token, err)
		}
	}
}

func TestBatchScopeRetainsLatePartialAcquisitionAfterStop(t *testing.T) {
	for _, stop := range []string{"cancel", "issuer"} {
		t.Run(stop, func(t *testing.T) {
			b, proof, events := scopedBatchFixture(t)
			ctx, cancel := context.WithCancel(requestContext(t))
			defer cancel()
			b.ops.scope.ops.verify = func(context.Context, uintptr) (batchServiceScope, error) {
				*events = append(*events, "verify-scope")
				if stop == "cancel" {
					cancel()
				} else {
					_ = b.owner.Revoke()
				}
				return proof, bootstrapauth.ErrDenied
			}
			if err := b.Start(ctx); err == nil {
				t.Fatal("stopped acquisition succeeded")
			}
			if b.ops.scope.proof != proof {
				t.Fatal("partial scope discarded")
			}
			if err := b.Close(requestContext(t)); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*events, []string{"verify-scope", "close-original-read", "close-scope"}) {
				t.Fatal(*events)
			}
		})
	}
}

func TestBatchScopeForeignCloseRetiresWithoutNativeCleanup(t *testing.T) {
	b, _, events := scopedBatchFixture(t)
	if err := b.Start(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	before := append([]string(nil), (*events)...)
	b.ops.scope.ops.threadID = func() uint32 { return 8 }
	if err := b.Close(requestContext(t)); !errors.Is(err, ErrFatal) {
		t.Fatal("foreign close lost fatal", err)
	}
	if !reflect.DeepEqual(*events, before) {
		t.Fatal("foreign caller performed native cleanup", *events)
	}
	b.ops.scope.ops.threadID = func() uint32 { return 7 }
	if err := b.Close(requestContext(t)); !errors.Is(err, ErrFatal) {
		t.Fatal("retired batch revived", err)
	}
}

func TestBatchScopeConstructorPanicRetainsUnknownReferences(t *testing.T) {
	b, _, events := scopedBatchFixture(t)
	b.ops.scope.ops.verify = func(context.Context, uintptr) (batchServiceScope, error) { panic("unknown acquired references") }
	if err := b.Start(requestContext(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	if err := b.Close(requestContext(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	if len(*events) != 0 {
		t.Fatal("unknown constructor ran native cleanup", *events)
	}
}

func TestBatchScopeFatalCloseCannotAcknowledgeSuccess(t *testing.T) {
	for _, fatal := range []error{bootstrapauth.ErrFatal, errors.Join(bootstrapauth.ErrFatal, bootstrapauth.ErrUnconfirmed)} {
		b, proof, _ := scopedBatchFixture(t)
		proof.closeErr = fatal
		if err := b.Start(requestContext(t)); err != nil {
			t.Fatal(err)
		}
		if err := b.Close(requestContext(t)); !errors.Is(err, ErrFatal) {
			t.Fatal("fatal scope close became cleanup success", err)
		}
		if b.ops.scope.closed != (fatal == bootstrapauth.ErrFatal) {
			t.Fatal("confirmed fatal and unconfirmed fatal ownership were conflated")
		}
	}
}

func TestBatchScopeLaterConstructorPanicRetainsScope(t *testing.T) {
	b, _, events := scopedBatchFixture(t)
	b.ops.newProfile = func(context.Context) (batchProfile, error) { panic("unknown profile references") }
	if err := b.Start(requestContext(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	if err := b.Close(requestContext(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*events, []string{"verify-scope"}) {
		t.Fatal("scope closed before unknown I/O was recovered", *events)
	}
}

type memoryScopeProviderQuery struct {
	events   *[]string
	closeErr error
}

func (*memoryScopeProviderQuery) inspect(context.Context) (profileCandidateSnapshot, error) {
	return profileCandidateSnapshot{}, ErrDenied
}
func (q *memoryScopeProviderQuery) close(context.Context) error {
	*q.events = append(*q.events, "close-provider")
	return q.closeErr
}

func TestBatchScopeClosesOnlyAfterProtectedProviderRecovery(t *testing.T) {
	b, _, events := scopedBatchFixture(t)
	query := &memoryScopeProviderQuery{events: events, closeErr: ErrUnconfirmed}
	b.spec.producer = &protectedProfileProvider{query: query}
	if err := b.Start(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(requestContext(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("provider failure acknowledged", err)
	}
	for _, e := range *events {
		if e == "close-scope" {
			t.Fatal("scope closed before provider query recovery")
		}
	}
	query.closeErr = nil
	if err := b.Close(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual((*events)[len(*events)-2:], []string{"close-provider", "close-scope"}) {
		t.Fatal(*events)
	}
}

func TestBatchScopeDirectCollectorCloseDoesNotWaitOnBusyScope(t *testing.T) {
	b, proof, _ := scopedBatchFixture(t)
	if err := b.Start(requestContext(t)); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	proof.onRecheck = func() { close(entered); <-release }
	collected := make(chan error, 1)
	go func() { _, err := b.Collect(requestContext(t)); collected <- err }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	closed := make(chan error, 1)
	go func() { closed <- b.Collector().Close(ctx) }()
	select {
	case err := <-closed:
		if !errors.Is(err, ErrUnconfirmed) {
			t.Error("busy scope acknowledged", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("finite cleanup blocked on scope mutex")
	}
	close(release)
	<-collected
}
