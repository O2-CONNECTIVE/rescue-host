package operatorauth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func authenticatedCollectorFixture(t *testing.T, authentication uint64) (*Collector, *collectorFixture, batchOwner) {
	t.Helper()
	f := &collectorFixture{eof: true}
	c := f.collector()
	_, owner, _, _, _, _ := batchFixture(t)
	c.sidPolicy = fixedSIDPolicy()
	c.launchID = strings.Repeat("a", 64)
	c.authOwner = owner.owner
	c.authIssuer = owner.issuer
	actual := tokenProjection{user: "S-1-5-21-1", authentication: authentication, session: 3, integrity: 8192, kind: 2, level: 1}
	report, err := encodeAuthenticatedReport(requestFrame(), c.launchID, c.sidPolicy, actual)
	if err != nil {
		t.Fatal(err)
	}
	c.ops.drain = func(context.Context) (reportResult, error) {
		return reportResult{frame: report, eof: f.eof}, f.drainErr
	}
	return c, f, owner
}
func TestAuthenticatedConsumeSealsSubjectAndRejectsLegacyAndForeignCollector(t *testing.T) {
	c, _, owner := authenticatedCollectorFixture(t, 17)
	r, err := c.Collect(requestContext(t))
	if err != nil {
		t.Fatal(err)
	}
	if frame, err := c.Consume(requestContext(t), r); frame != nil || !errors.Is(err, ErrDenied) {
		t.Fatal("legacy dropped subject", err)
	}
	other, _, _ := authenticatedCollectorFixture(t, 17)
	if frame, binding, err := other.ConsumeAuthenticated(requestContext(t), r); frame != nil || binding != nil || !errors.Is(err, ErrDenied) {
		t.Fatal("foreign collector accepted", err)
	}
	frame, binding, err := c.ConsumeAuthenticated(requestContext(t), r)
	if err != nil || string(frame) != string(requestFrame()) || !binding.Matches(c, owner.owner, owner.issuer) {
		t.Fatal("sealed fact unavailable", err)
	}
	if binding.Matches(other, owner.owner, owner.issuer) {
		t.Fatal("binding transferred to collector")
	}
	_, owner2, _, _, _, _ := batchFixture(t)
	if binding.Matches(c, owner2.owner, owner2.issuer) {
		t.Fatal("binding transferred to owner")
	}
	copy := *binding
	if copy.Matches(c, owner.owner, owner.issuer) || copy.SameSubject(binding) {
		t.Fatal("copied binding accepted")
	}
	if frame, binding, err := c.ConsumeAuthenticated(requestContext(t), r); frame != nil || binding != nil || !errors.Is(err, ErrDenied) {
		t.Fatal("single use ignored", err)
	}
}
func TestAuthenticatedBindingRetainsFactsAfterOldRequestEndsAndDetectsLogonChange(t *testing.T) {
	ctx, cancel := context.WithCancel(requestContext(t))
	c, _, owner := authenticatedCollectorFixture(t, 17)
	r, err := c.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, old, err := c.ConsumeAuthenticated(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if !old.Matches(c, owner.owner, owner.issuer) {
		t.Fatal("immutable fact depended on completed request")
	}
	for _, auth := range []uint64{17, 29} {
		fresh, _, _ := authenticatedCollectorFixture(t, auth)
		proof, err := fresh.Collect(requestContext(t))
		if err != nil {
			t.Fatal(err)
		}
		_, binding, err := fresh.ConsumeAuthenticated(requestContext(t), proof)
		if err != nil {
			t.Fatal(err)
		}
		if old.SameSubject(binding) != (auth == 17) {
			t.Fatal("logon continuity incorrectly accepted")
		}
	}
}

// 以下は固定red/green境界への拒否回帰。memory callbackは実Windows成功を証明しない。
func TestAuthenticatedCollectorRejectsUnsealedOrIncompleteReport(t *testing.T) {
	for _, name := range []string{"raw-legacy", "old-launch", "duplicate-root", "duplicate-subject", "unknown", "zero-auth", "wrong-sid", "bad-kind", "bad-level", "restricted", "appcontainer", "no-eof", "exit", "unjoined-io", "cancel"} {
		t.Run(name, func(t *testing.T) {
			c, f, _ := authenticatedCollectorFixture(t, 17)
			ctx, cancel := context.WithCancel(requestContext(t))
			defer cancel()
			report, _ := c.ops.drain(ctx)
			raw := string(report.frame)
			switch name {
			case "raw-legacy":
				raw = string(requestFrame())
			case "old-launch":
				raw = strings.Replace(raw, strings.Repeat("a", 64), strings.Repeat("b", 64), 1)
			case "duplicate-root":
				raw = strings.Replace(raw, `"version":"2"`, `"version":"2","version":"2"`, 1)
			case "duplicate-subject":
				raw = strings.Replace(raw, `"authentication":"17"`, `"authentication":"17","authentication":"17"`, 1)
			case "unknown":
				raw = strings.Replace(raw, `"version":"2"`, `"version":"2","extra":false`, 1)
			case "zero-auth":
				raw = strings.Replace(raw, `"authentication":"17"`, `"authentication":"0"`, 1)
			case "wrong-sid":
				raw = strings.Replace(raw, `S-1-5-21-1`, `S-1-5-21-3`, 1)
			case "bad-kind":
				raw = strings.Replace(raw, `"kind":"2"`, `"kind":"1"`, 1)
			case "bad-level":
				raw = strings.Replace(raw, `"level":"1"`, `"level":"3"`, 1)
			case "restricted":
				raw = strings.Replace(raw, `"restricted":false`, `"restricted":true`, 1)
			case "appcontainer":
				raw = strings.Replace(raw, `"appContainer":false`, `"appContainer":true`, 1)
			case "no-eof":
				f.eof = false
			case "exit":
				f.exit = 1
			case "unjoined-io":
				f.ioErr = ErrUnconfirmed
			case "cancel":
				f.onWait = cancel
			}
			c.ops.drain = func(context.Context) (reportResult, error) { return reportResult{frame: []byte(raw), eof: f.eof}, nil }
			if r, err := c.Collect(ctx); r != nil || err == nil {
				t.Fatal("unconfirmed report became authenticated", err)
			}
			if r, err := c.Collect(requestContext(t)); r != nil || err == nil {
				t.Fatal("rejected report revived", err)
			}
		})
	}
}
func TestAuthenticatedConsumeBurnsOnCancellationAndReclamation(t *testing.T) {
	for _, name := range []string{"current-cancel", "original-cancel", "close"} {
		t.Run(name, func(t *testing.T) {
			c, _, _ := authenticatedCollectorFixture(t, 17)
			original, stopOriginal := context.WithCancel(requestContext(t))
			defer stopOriginal()
			c.request = original
			r, err := c.Collect(original)
			if err != nil {
				t.Fatal(err)
			}
			current, stopCurrent := context.WithCancel(requestContext(t))
			defer stopCurrent()
			switch name {
			case "current-cancel":
				stopCurrent()
			case "original-cancel":
				stopOriginal()
			case "close":
				if err = c.Close(requestContext(t)); err != nil {
					t.Fatal(err)
				}
			}
			if frame, binding, err := c.ConsumeAuthenticated(current, r); frame != nil || binding != nil || !errors.Is(err, ErrDenied) {
				t.Fatal("cancelled/reclaimed request accepted", err)
			}
			if frame, binding, err := c.ConsumeAuthenticated(requestContext(t), r); frame != nil || binding != nil || !errors.Is(err, ErrDenied) {
				t.Fatal("burnt request revived", err)
			}
		})
	}
}
func TestAuthenticatedCollectCannotPublishThroughConcurrentReclamation(t *testing.T) {
	c, _, _ := authenticatedCollectorFixture(t, 17)
	entered, release := make(chan struct{}), make(chan struct{})
	c.ops.closeIO = func(context.Context) error { close(entered); <-release; return nil }
	result := make(chan *Read, 1)
	go func() { r, _ := c.Collect(requestContext(t)); result <- r }()
	<-entered
	if err := c.Close(requestContext(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("busy collection falsely joined", err)
	}
	close(release)
	if r := <-result; r != nil {
		t.Fatal("proof escaped close race")
	}
}

func TestAuthenticatedBindingMatchesOnlyOriginalProtectedSIDPolicy(t *testing.T) {
	c, _, _ := authenticatedCollectorFixture(t, 17)
	proof, err := c.Collect(requestContext(t))
	if err != nil {
		t.Fatal(err)
	}
	_, binding, err := c.ConsumeAuthenticated(requestContext(t), proof)
	if err != nil {
		t.Fatal(err)
	}
	if !binding.MatchesSIDPolicy("S-1-5-21-1", "S-1-5-21-2") || binding.MatchesSIDPolicy("S-1-5-21-3", "S-1-5-21-2") || binding.MatchesSIDPolicy("S-1-5-21-1", "S-1-5-21-3") {
		t.Fatal("sealed policy substituted")
	}
}

func TestAuthenticatedBindingComparesWholeActualProjection(t *testing.T) {
	original, _, _ := authenticatedCollectorFixture(t, 17)
	r, err := original.Collect(requestContext(t))
	if err != nil {
		t.Fatal(err)
	}
	_, binding, err := original.ConsumeAuthenticated(requestContext(t), r)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"session", "integrity", "level"} {
		c, _, _ := authenticatedCollectorFixture(t, 17)
		report, _ := c.ops.drain(requestContext(t))
		raw := string(report.frame)
		switch field {
		case "session":
			raw = strings.Replace(raw, `"session":"3"`, `"session":"4"`, 1)
		case "integrity":
			raw = strings.Replace(raw, `"integrity":"8192"`, `"integrity":"12288"`, 1)
		case "level":
			raw = strings.Replace(raw, `"level":"1"`, `"level":"2"`, 1)
		}
		c.ops.drain = func(context.Context) (reportResult, error) { return reportResult{frame: []byte(raw), eof: true}, nil }
		r, err := c.Collect(requestContext(t))
		if err != nil {
			t.Fatal(err)
		}
		_, changed, err := c.ConsumeAuthenticated(requestContext(t), r)
		if err != nil {
			t.Fatal(err)
		}
		if binding.SameSubject(changed) {
			t.Fatal("actual subject drift ignored", field)
		}
	}
}
