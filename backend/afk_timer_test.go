package backend

import (
	"testing"
	"time"
)

func TestAFKUnit3PreparedReadyAutonomousExpiry(t *testing.T) {
	for _, phase := range []string{"prepared", "ready"} {
		t.Run(phase, func(t *testing.T) {
			cfg, _ := config(t, "restricted")
			cfg.MaximumDuration = 80 * time.Millisecond
			cfg.LeaseDuration = 40 * time.Millisecond
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer closeSession(t, s)
			if phase == "ready" && s.Ready(cfg.Identity) != nil {
				t.Fatal("ready失敗")
			}
			// Snapshotや要求による期限評価を一切呼ばず、独立timerの取消を待つ。
			select {
			case <-s.authority.Context().Done():
			case <-time.After(200 * time.Millisecond):
				t.Fatal("自律期限が進まない")
			}
			if snap := s.Snapshot(); snap.State != "revoking" || snap.Reason != "lease-expired" || snap.Owned != 0 {
				t.Fatal("未開始状態の期限が不正")
			}
			if s.Activate(cfg.Identity) == nil || s.Renew(cfg.Identity, 0) == nil {
				t.Fatal("期限後の起動")
			}
		})
	}
}
