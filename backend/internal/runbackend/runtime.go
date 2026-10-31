// Package runbackend は保護bootstrapから渡した型付き設定を本体Sessionへ接続する。
// argv/env/hello/UI stateからgrantや鍵を復元せず、listenerも作成しない。
// supervisorの唯一の権威は所有serviceに残し、ここでは新規作成しない。
package runbackend

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/control"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/stdio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor"
	"golang.org/x/crypto/ssh"
)

var ErrConfig = errors.New("承認済runtime設定を確認できません")
var ErrUnconfirmed = errors.New("runtimeの終了を確認できません")
var ErrAlreadyUsed = errors.New("runtimeの制御入口は使用済みです")

// ApprovedConfig は保護側が照合した固定値だけを渡すinternal入口。
// 同じIdentityのhelloを読めたことは、この入力の認証にならない。
type ApprovedConfig struct {
	// AcceptedSourceは保護された子profileが別途供給する。wire入力/認証の根拠ではない。
	// nilは既存stdioだけの基盤を維持し、listenerやendpointを選択しない。
	AcceptedSource backend.AcceptedSource `json:"-"`
	Grant          supervisor.ProvisionedGrant
	Backend        backend.Config
	Wire           control.WireContext
	Limits         stdio.Limits

	// InnerShellはfull modeの唯一のguest shell stdio端。保護bootstrapが同じlaunchのgrantと
	// 一緒に渡す。fullでnilなら拒否し、固定TCPへfallbackしない。restrictedではnil必須。
	// Newがerrorなら所有はcallerに残る。値の存在や戻り値は認証・所有証明ではない。
	InnerShell backend.ShellStreams `json:"-"`
}

// KeySetDigest は生Ed25519鍵の集合を一意に表す候補規則。
// domain prefix、sort/dedupしたmarshal bytesの件数と各長さをuint32 big endianで区切る。
// この値は認証ではなく、保護provisionerが同じ規則で固定する必要がある。
func KeySetDigest(keys []ssh.PublicKey) ([32]byte, error) {
	var zero [32]byte
	if len(keys) == 0 || len(keys) > 8 {
		return zero, ErrConfig
	}
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == nil || key.Type() != ssh.KeyAlgoED25519 {
			return zero, ErrConfig
		}
		data := key.Marshal()
		parsed, err := ssh.ParsePublicKey(data)
		if err != nil || parsed.Type() != ssh.KeyAlgoED25519 {
			return zero, ErrConfig
		}
		values = append(values, string(parsed.Marshal()))
	}
	slices.Sort(values)
	values = slices.Compact(values)
	hash := sha256.New()
	_, _ = hash.Write([]byte("rescue-host-keyset/1\x00"))
	_ = binary.Write(hash, binary.BigEndian, uint32(len(values)))
	for _, value := range values {
		_ = binary.Write(hash, binary.BigEndian, uint32(len(value)))
		_, _ = hash.Write([]byte(value))
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

type Runtime struct {
	mu          sync.Mutex
	used        bool
	session     *backend.Session
	adapter     *control.Adapter
	coordinator *control.Coordinator
	grant       supervisor.ProvisionedGrant
	limits      stdio.Limits
}

// New は接続・待受を始めない。Session内部の相対最大時間は変更せず、生成前後の
// 保守的照合で固定hard deadlineを超える余裕不足を拒否する。
func New(c ApprovedConfig) (*Runtime, error) {
	g, b := c.Grant, c.Backend
	id := g.Binding.Identity
	wireID := backend.Identity{LaunchID: c.Wire.Identity.LaunchID, Epoch: c.Wire.Identity.Epoch, PolicyDigest: c.Wire.Identity.PolicyDigest}
	if g.Binding.Generation == 0 || !id.Valid() || b.Identity != id || wireID != id || c.Wire.Generation != strconv.FormatUint(g.Binding.Generation, 10) || g.GuestBoundary != supervisor.SeparatedNonAdminGuest || g.KeySetDigest == ([32]byte{}) || g.BackendLeaseCeiling <= 0 || b.LeaseDuration <= 0 || b.LeaseDuration > g.BackendLeaseCeiling || b.MaximumDuration <= 0 || !withinDeadline(g.HardDeadline, b.MaximumDuration) || c.Limits.FrameBytes != control.FrameBytes || c.Limits.Frames < 1 || c.Limits.CleanupTimeout <= 0 {
		return nil, ErrConfig
	}
	// fullの通常shellを欠いたままexec専用や固定TCP 22へ縮退しない。restrictedのhelperは未定義。
	if (b.OperationMode == "full") != (c.InnerShell != nil) {
		return nil, ErrConfig
	}
	if _, err := control.NewState(c.Wire); err != nil {
		return nil, ErrConfig
	}
	// interface入力の鍵を一度parseして固定し、callerのslice/Marshal変更を保持しない。
	b.AllowedKeys = make([]ssh.PublicKey, len(c.Backend.AllowedKeys))
	for n, key := range c.Backend.AllowedKeys {
		if key == nil {
			return nil, ErrConfig
		}
		parsed, err := ssh.ParsePublicKey(key.Marshal())
		if err != nil {
			return nil, ErrConfig
		}
		b.AllowedKeys[n] = parsed
	}
	digest, err := KeySetDigest(b.AllowedKeys)
	if err != nil || digest != g.KeySetDigest {
		return nil, ErrConfig
	}
	// 承認済runtimeは所有内側endpointだけを使い、旧Newの固定TCP dialを持たない。
	session, err := backend.NewOwned(b)
	if err != nil {
		return nil, ErrConfig
	}
	reject := func() (*Runtime, error) {
		ctx, cancel := context.WithTimeout(context.Background(), c.Limits.CleanupTimeout)
		defer cancel()
		if session.Close(ctx) != nil {
			// 終了未確認の本体参照をcallerへ返し、回収対象を失わない。
			return &Runtime{session: session, grant: g, limits: c.Limits}, ErrUnconfirmed
		}
		return nil, ErrConfig
	}
	snapshot := session.Snapshot()
	if !withinDeadline(g.HardDeadline, b.MaximumDuration) || snapshot.State != "prepared" || snapshot.Identity != id {
		return reject()
	}
	coordinator, err := control.NewPrepared(session, g, c.Limits.CleanupTimeout)
	if err != nil {
		return reject()
	}
	adapter, err := control.NewAdapter(c.Wire, coordinator)
	if err != nil {
		return reject()
	}
	// 所有移転は最後のfallible step。sourceとshellを同時に一度だけ移し、
	// Newがerrorなら両方とも常にcallerの所有に残る。部分Runtimeが持つのは既存Sessionだけである。
	if c.AcceptedSource != nil || c.InnerShell != nil {
		if err := session.AttachOwned(c.AcceptedSource, c.InnerShell, g.HardDeadline, c.Limits.CleanupTimeout); err != nil {
			return reject()
		}
	}
	return &Runtime{session: session, adapter: adapter, coordinator: coordinator, grant: g, limits: c.Limits}, nil
}

func withinDeadline(deadline time.Time, maximum time.Duration) bool {
	now := time.Now().Round(0)
	return !deadline.IsZero() && now.Before(deadline.Round(0)) && !now.Add(maximum).After(deadline.Round(0))
}

// Run は継承済み・保護されたpipeだけを受け取る。pipeの由来はここでは認証しない。
// EOF/parse/I/O/期限/lease喪失ではstdioのClose経路から同じSessionを失効・回収する。
func (r *Runtime) Run(ctx context.Context, input io.ReadCloser, output io.WriteCloser) error {
	if r == nil || r.session == nil {
		return ErrConfig
	}
	r.mu.Lock()
	if r.used {
		r.mu.Unlock()
		return ErrAlreadyUsed
	}
	r.used = true
	r.mu.Unlock()
	if ctx == nil || r.adapter == nil {
		cleanup, cancel := context.WithTimeout(context.Background(), r.limits.CleanupTimeout)
		defer cancel()
		if err := r.Close(cleanup); err != nil {
			return ErrUnconfirmed
		}
		return ErrConfig
	}
	runCtx, cancel := context.WithDeadline(ctx, r.grant.HardDeadline)
	defer cancel()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		var terminal <-chan time.Time
		var terminalTimer *time.Timer
		defer func() {
			if terminalTimer != nil {
				terminalTimer.Stop()
			}
		}()
		for {
			select {
			case <-stop:
				return
			case <-runCtx.Done():
				return
			case <-terminal:
				cancel()
				return
			case <-ticker.C:
				s := r.session.Snapshot()
				// 正規stopのreceipt送信を取消で潰さず、自発的な短lease/期限喪失だけを閉じる。
				if s.State == "revoking" && s.Reason != "explicit-stop" {
					cancel()
					return
				}
				// 最初の終端観測から一度だけ計時し、pollで最終I/O猶予を延長しない。
				if s.State == "terminated" && terminalTimer == nil {
					terminalTimer = time.NewTimer(r.limits.CleanupTimeout)
					terminal = terminalTimer.C
				}
			}
		}
	}()
	result := stdio.Run(runCtx, input, output, r.limits, r.adapter)
	close(stop)
	<-done
	cleanup, stopCleanup := context.WithTimeout(context.Background(), r.limits.CleanupTimeout)
	defer stopCleanup()
	if err := r.Close(cleanup); err != nil {
		return ErrUnconfirmed
	}
	return result
}

// Serve は将来の保護された受理済transportの入口。listener/dial/relayを生成しない。
// ErrNotAcceptedでは所有を移さないため、callerが接続を閉じる。
func (r *Runtime) Serve(conn net.Conn) error {
	if r == nil || r.session == nil || !time.Now().Before(r.grant.HardDeadline) {
		if r != nil && r.session != nil {
			_ = r.session.Revoke(r.grant.Binding.Identity)
		}
		return backend.ErrNotAccepted
	}
	return r.session.Serve(conn)
}

func (r *Runtime) Snapshot() backend.Snapshot {
	if r == nil || r.session == nil {
		return backend.Snapshot{}
	}
	return r.session.Snapshot()
}

// Coordinator は保護bootstrapが同じSessionをlocalcontrolへ渡すためだけの参照。
// 別Session/ownerを生成しない。未完成runtimeの参照はnilで、認証/受付gateではない。
func (r *Runtime) Coordinator() *control.Coordinator {
	if r == nil || r.session == nil {
		return nil
	}
	return r.coordinator
}

func (r *Runtime) Close(ctx context.Context) error {
	if r == nil || r.session == nil {
		return ErrConfig
	}
	// cleanup予算が既に失われていても、認可失効は先に確定する。
	_ = r.session.Revoke(r.grant.Binding.Identity)
	if r.adapter == nil {
		if ctx == nil {
			return control.ErrBudget
		}
		if _, ok := ctx.Deadline(); !ok {
			return control.ErrBudget
		}
		return r.session.Close(ctx)
	}
	return r.adapter.Close(ctx)
}
