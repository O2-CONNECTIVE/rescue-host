package backend

import (
	"testing"
	"time"
)

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
		select {
		case <-p.s.authority.Context().Done():
		case <-time.After(time.Until(p.start.Add(6 * time.Second))):
			t.Fatal("準備期限+1秒超過")
		}
		elapsed := time.Since(p.start)
		if elapsed < 5*time.Second || elapsed >= 6*time.Second {
			t.Fatal("準備期限実測が範囲外")
		}
		if snap := p.s.Snapshot(); snap.State != "revoking" || snap.Reason != "preparation-expired" || snap.Owned != 0 {
			t.Fatal("独立準備期限の終了が不正")
		}
		if p.s.Activate(p.c.Identity) == nil || p.s.Renew(p.c.Identity, 0) == nil {
			t.Fatal("準備期限後の開始")
		}
		t.Logf("phase=%s preparation_ms=%d lease_ms=10000 maximum_ms=20000 owned=0 requests=0", p.phase, elapsed.Milliseconds())
	}
}
