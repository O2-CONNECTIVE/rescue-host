package backend

import (
	"testing"
	"time"
)

func preparationElapsedInRange(monotonic, wall time.Duration) bool {
	return (monotonic >= 5*time.Second || wall >= 5*time.Second) && monotonic < 6*time.Second
}

func TestNextN2PreparationElapsedObserver(t *testing.T) {
	for _, tc := range []struct {
		name            string
		monotonic, wall time.Duration
		want            bool
	}{
		{"both-before", 5*time.Second - 1, 5*time.Second - 1, false},
		{"wall-first", 5*time.Second - 1, 5 * time.Second, true},
		{"monotonic-first", 5 * time.Second, 5*time.Second - 1, true},
		{"both-at-deadline", 5 * time.Second, 5 * time.Second, true},
		{"monotonic-upper-bound", 6 * time.Second, 5 * time.Second, false},
		{"wall-ahead-within-monotonic-budget", 5 * time.Second, 6 * time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := preparationElapsedInRange(tc.monotonic, tc.wall); got != tc.want {
				t.Fatalf("準備期限の観測が契約と不一致: monotonic=%s wall=%s got=%t want=%t", tc.monotonic, tc.wall, got, tc.want)
			}
		})
	}
}

func TestNextN2AutonomousFiveSecondPreparationCap(t *testing.T) {
	type pending struct {
		s     *Session
		c     Config
		start time.Time
		phase string
	}
	var all []pending
	for _, phase := range []string{"prepared", "ready"} {
		mode := "restricted"
		if phase == "ready" {
			mode = "full"
		}
		cfg, _ := config(t, mode)
		cfg.MaximumDuration = 20 * time.Second
		cfg.LeaseDuration = 10 * time.Second
		start := time.Now()
		s, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer closeSession(t, s)
		if phase == "ready" && s.Ready(cfg.Identity) != nil {
			t.Fatal("ready失敗")
		}
		all = append(all, pending{s, cfg, start, phase})
	}
	// 二つの未開始sessionを同時に待つ。listener/通信は作らない。
	for _, p := range all {
		waitOutcome := "context-done"
		select {
		case <-p.s.authority.Context().Done():
		case <-time.After(time.Until(p.start.Add(6 * time.Second))):
			waitOutcome = "timeout"
		}
		observed := time.Now()
		elapsed := observed.Sub(p.start)
		wallElapsed := observed.Round(0).Sub(p.start.Round(0))
		preSnapshotDone := false
		select {
		case <-p.s.authority.Context().Done():
			preSnapshotDone = true
		default:
		}
		// Snapshot自身も期限を検査するため、その前の自律失効を別に記録する。
		snap := p.s.Snapshot()
		t.Logf("phase=%s wait_outcome=%s pre_snapshot_done=%t monotonic_elapsed_ns=%d wall_elapsed_ns=%d wall_minus_monotonic_ns=%d snapshot_after_wait=true state=%s reason=%s owned=%d", p.phase, waitOutcome, preSnapshotDone, elapsed.Nanoseconds(), wallElapsed.Nanoseconds(), (wallElapsed - elapsed).Nanoseconds(), snap.State, snap.Reason, snap.Owned)
		if waitOutcome == "timeout" {
			t.Fatal("準備期限+1秒超過")
		}
		// 製品と同じく先に到達した時計で5秒を判定し、上限は従来の単調6秒。
		if !preparationElapsedInRange(elapsed, wallElapsed) {
			t.Fatal("準備期限実測が範囲外")
		}
		if snap.State != "revoking" || snap.Reason != "preparation-expired" || snap.Owned != 0 {
			t.Fatal("独立準備期限の終了が不正")
		}
		if p.s.Activate(p.c.Identity) == nil || p.s.Renew(p.c.Identity, 0) == nil {
			t.Fatal("準備期限後の開始")
		}
		t.Logf("phase=%s preparation_ms=%d lease_ms=10000 maximum_ms=20000 owned=0 requests=0", p.phase, elapsed.Milliseconds())
	}
}
