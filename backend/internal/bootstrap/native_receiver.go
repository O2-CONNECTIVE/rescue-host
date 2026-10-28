package bootstrap

import (
	"context"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapauth"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/bootstrapio"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/protectedkey"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/runbackend"
	"golang.org/x/crypto/ssh"
)

var ErrNativeConsumed = errors.New("native bootstrap 受信権は消費済みです")
var ErrNativeUnconfirmed = errors.New("native bootstrap の回収を確認できません")

// NativeReceiverConfig は保護配置元が固定する必須上限です。既定値はありません。
type NativeReceiverConfig struct {
	Profile                                   bootstrapauth.Profile
	Limits                                    ReceiverLimits
	MaximumDuration, LeaseCeiling             time.Duration
	MaxChannels, MaxHandshakes, MaxTransports int
	StartupTimeout                            time.Duration
}

type receiverPeer interface {
	Recheck(context.Context) error
	Close(context.Context) error
}
type receiverReader interface {
	ReadOnce(context.Context) ([]byte, error)
	Close(context.Context) error
}
type receiverScope interface {
	Context() context.Context
	Close() error
}

// private seam は同 package の fake 試験専用です。公開 constructor は native だけを結びます。
type nativeReceiverOps struct {
	authenticate func(context.Context, uintptr) (receiverPeer, error)
	adopt        func(context.Context, uintptr, time.Duration) (receiverReader, error)
	begin        func(context.Context, protectedkey.Profile) (receiverScope, error)
	load         func(context.Context, string) (ssh.Signer, error)
	closeHandle  func(context.Context, uintptr) error
}

// sealedNativeContext は認証成功後にだけ作り、独立 argv 相関値と Reader を束縛します。
type sealedNativeContext struct {
	launch LaunchValues
	reader receiverReader
}

// NativeReceiver は一回だけ受信します。同じ caller-owned LockOSThread 上で使用します。
// ErrFatal は sticky です。caller は UnlockOSThread せずその goroutine を終了します。
type NativeReceiver struct {
	mu                             sync.Mutex
	cleanup                        sync.Mutex
	c                              NativeReceiverConfig
	ops                            nativeReceiverOps
	configured, used, busy, closed bool
	cancel                         context.CancelFunc
	peer                           receiverPeer
	sealed                         *sealedNativeContext
	raw                            uintptr
	scope                          receiverScope
	scopeFatal                     bool
	fatal                          error
}

// NewNativeReceiver は OS に触れず、静的検証と deep copy だけを行います。
func NewNativeReceiver(c NativeReceiverConfig) (*NativeReceiver, error) {
	if c.Limits.MaxFrames < 1 || int64(c.Limits.MaxFrames) > 2147483647 || c.Limits.MaxCleanupTimeout <= 0 || c.StartupTimeout <= 0 || c.MaximumDuration <= 0 || c.MaximumDuration > 2*time.Hour || c.LeaseCeiling <= 0 || c.LeaseCeiling > time.Minute || c.LeaseCeiling > c.MaximumDuration || c.MaxChannels < 1 || c.MaxChannels > 32 || c.MaxHandshakes < 1 || c.MaxHandshakes > 64 || c.MaxTransports < 1 || c.MaxTransports > 64 {
		return nil, ErrLimits
	}
	c.Profile = copyReceiverProfile(c.Profile)
	f, err := bootstrapauth.NewNativeFactory(c.Profile)
	if err != nil {
		return nil, err
	}
	l, err := protectedkey.NewNative(c.Profile.Keys)
	if err != nil {
		return nil, err
	}
	r := &NativeReceiver{c: c, configured: true}
	r.ops = nativeReceiverOps{
		authenticate: func(ctx context.Context, h uintptr) (receiverPeer, error) {
			v, e := f.Authenticate(ctx, h)
			if v == nil {
				return nil, e
			}
			return v, e
		},
		adopt: func(ctx context.Context, h uintptr, d time.Duration) (receiverReader, error) {
			v, e := bootstrapio.AdoptNativeReader(ctx, h, d)
			if v == nil {
				return nil, e
			}
			return v, e
		},
		begin: func(ctx context.Context, p protectedkey.Profile) (receiverScope, error) {
			v, e := protectedkey.BeginNativeScope(ctx, p)
			if v == nil {
				return nil, e
			}
			return v, e
		},
		load: l.Load, closeHandle: closeAuthenticatedReadHandle,
	}
	return r, nil
}

func (r *NativeReceiver) StartupTimeout() time.Duration {
	if r == nil {
		return 0
	}
	return r.c.StartupTimeout
}
func (r *NativeReceiver) CleanupTimeout() time.Duration {
	if r == nil {
		return 0
	}
	return r.c.Limits.MaxCleanupTimeout
}

func receiverLive(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	_, ok := ctx.Deadline()
	return ok
}

// ReadApproved は認証前に payload を読みません。Authority/Session の生成や activate は行いません。
func (r *NativeReceiver) ReadApproved(ctx context.Context, args []string, stdio [3]uintptr) (out runbackend.ApprovedConfig, err error) {
	if r == nil {
		return out, bootstrapauth.ErrConfig
	}
	r.mu.Lock()
	if !r.configured {
		r.mu.Unlock()
		return out, bootstrapauth.ErrConfig
	}
	if r.used || r.closed {
		r.mu.Unlock()
		return out, ErrNativeConsumed
	}
	r.used = true
	if !receiverLive(ctx) {
		r.mu.Unlock()
		return out, ErrLimits
	}
	runCtx, cancel := context.WithTimeout(ctx, r.c.StartupTimeout)
	r.cancel = cancel
	r.busy = true
	r.mu.Unlock()
	defer func() {
		cancel()
		r.mu.Lock()
		if r.closed {
			err = errors.Join(err, ErrNativeConsumed)
		}
		r.busy = false
		r.rememberFatal(err)
		r.mu.Unlock()
		cleanup := r.Close(ctx)
		err = errors.Join(err, cleanup)
		if err != nil {
			out = runbackend.ApprovedConfig{}
		}
	}()
	a, err := ParseLaunchArgs(args, stdio)
	if err != nil {
		return out, err
	}
	peer, err := r.ops.authenticate(runCtx, a.handle)
	r.mu.Lock()
	r.peer = peer
	r.mu.Unlock()
	if err != nil {
		return out, err
	}
	if peer == nil {
		return out, bootstrapauth.ErrDenied
	}
	r.mu.Lock()
	r.raw = a.handle
	r.sealed = &sealedNativeContext{launch: a.values}
	r.mu.Unlock()
	if !receiverLive(runCtx) {
		return out, errors.Join(ErrLimits, runCtx.Err())
	}
	reader, err := r.ops.adopt(runCtx, a.handle, r.c.Limits.MaxCleanupTimeout)
	r.mu.Lock()
	r.sealed.reader = reader
	if reader != nil {
		r.raw = 0
	}
	r.mu.Unlock()
	if err != nil {
		return out, err
	}
	if reader == nil {
		return out, bootstrapio.ErrInvalid
	}
	if !receiverLive(runCtx) {
		return out, errors.Join(ErrLimits, runCtx.Err())
	}
	record, err := reader.ReadOnce(runCtx)
	if err != nil {
		return out, err
	}
	if !receiverLive(runCtx) {
		return out, errors.Join(ErrLimits, runCtx.Err())
	}
	m, err := r.accept(record)
	if err != nil {
		return out, err
	}
	// 元の絶対期限をそのまま短い context に適用し、期限切れでは鍵を読みません。
	grantCtx, grantCancel := context.WithDeadline(runCtx, m.Grant.HardDeadline)
	defer grantCancel()
	if !receiverLive(grantCtx) {
		return out, errors.Join(ErrLimits, grantCtx.Err())
	}
	if err = peer.Recheck(grantCtx); err != nil {
		return out, err
	}
	if !receiverLive(grantCtx) {
		return out, errors.Join(ErrLimits, grantCtx.Err())
	}
	scope, err := r.ops.begin(grantCtx, r.c.Profile.Keys)
	r.mu.Lock()
	r.scope = scope
	r.mu.Unlock()
	if err != nil {
		return out, err
	}
	if scope == nil {
		return out, protectedkey.ErrClosed
	}
	if !receiverLive(grantCtx) {
		return out, errors.Join(ErrLimits, grantCtx.Err())
	}
	signer, loadErr := r.ops.load(scope.Context(), m.HostKeyRef)
	// 失敗した鍵読取りの後も held peer の変化を確認し、fatal を隠しません。
	err = errors.Join(loadErr, peer.Recheck(grantCtx))
	if err != nil {
		return out, err
	}
	if signer == nil || signer.PublicKey() == nil || signer.PublicKey().Type() != ssh.KeyAlgoED25519 || ssh.FingerprintSHA256(signer.PublicKey()) != r.c.Profile.Keys.Catalog[m.HostKeyRef].Fingerprint {
		return out, protectedkey.ErrDenied
	}
	if !receiverLive(grantCtx) {
		return out, errors.Join(ErrLimits, grantCtx.Err())
	}
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return out, ErrNativeConsumed
	}
	keys := make([]ssh.PublicKey, len(m.Config.AllowedKeys))
	for i, value := range m.Config.AllowedKeys {
		data, _ := base64.StdEncoding.Strict().DecodeString(value)
		keys[i], err = ssh.ParsePublicKey(data)
		if err != nil {
			return out, ErrRecord
		}
	}
	c := m.Config
	out = runbackend.ApprovedConfig{Grant: m.Grant, Wire: m.Wire, Limits: m.Limits, Backend: backend.Config{Identity: m.Grant.Binding.Identity, OperationMode: c.OperationMode, AllowedKeys: keys, HostKey: signer, MaximumDuration: c.MaximumDuration, LeaseDuration: c.LeaseDuration, MaxChannels: c.MaxChannels, MaxHandshakes: c.MaxHandshakes, MaxTransports: c.MaxTransports}}
	return out, nil
}

func (r *NativeReceiver) accept(record []byte) (Message, error) {
	if r.sealed == nil || r.sealed.reader == nil {
		return Message{}, bootstrapauth.ErrDenied
	}
	m, err := decodeShapeAndSemantics(record, r.c.Limits)
	if err != nil {
		return Message{}, err
	}
	if m.IssuerInstanceEpoch != r.sealed.launch.IssuerInstanceEpoch || m.Wire != r.sealed.launch.Wire {
		return Message{}, ErrExpected
	}
	c := m.Config
	if c.MaximumDuration > r.c.MaximumDuration || c.LeaseDuration > r.c.LeaseCeiling || m.Grant.BackendLeaseCeiling > r.c.LeaseCeiling || c.MaxChannels > r.c.MaxChannels || c.MaxHandshakes > r.c.MaxHandshakes || c.MaxTransports > r.c.MaxTransports {
		return Message{}, ErrLimits
	}
	if _, ok := r.c.Profile.Keys.Catalog[m.HostKeyRef]; !ok {
		return Message{}, protectedkey.ErrDenied
	}
	return m, nil
}

func (r *NativeReceiver) rememberFatal(err error) {
	if errors.Is(err, bootstrapauth.ErrFatal) || errors.Is(err, protectedkey.ErrFatal) {
		r.fatal = errors.Join(r.fatal, err)
	}
}

// Close は caller の有限 budget 内で既所有参照だけを回収します。認証や read を再試行しません。
func (r *NativeReceiver) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return ErrLimits
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return ErrLimits
	}
	if r.c.Limits.MaxCleanupTimeout > 0 {
		if d := time.Now().Add(r.c.Limits.MaxCleanupTimeout); d.Before(deadline) {
			deadline = d
		}
	}
	cleanup, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	r.mu.Lock()
	r.closed = true
	if r.cancel != nil {
		r.cancel()
	}
	sealed := r.sealed
	var reader receiverReader
	if sealed != nil {
		reader = sealed.reader
	}
	busy := r.busy
	r.mu.Unlock()
	if !r.cleanup.TryLock() {
		return ErrNativeUnconfirmed
	}
	defer r.cleanup.Unlock()
	// Reader は I/O 中も取消・completion待機でき、未完了時の参照を保持します。
	if busy {
		var err error
		if reader != nil {
			err = reader.Close(cleanup)
		}
		return errors.Join(ErrNativeUnconfirmed, err)
	}
	var result error
	if r.scope != nil && !r.scopeFatal {
		if !receiverLive(cleanup) {
			result = errors.Join(result, ErrNativeUnconfirmed, cleanup.Err())
		} else {
			err := r.scope.Close()
			result = errors.Join(result, err)
			if err == nil {
				r.scope = nil
			}
			if errors.Is(err, protectedkey.ErrFatal) {
				r.scopeFatal = true
			}
		}
	}
	if r.peer != nil {
		err := r.peer.Close(cleanup)
		result = errors.Join(result, err)
		if err == nil || errors.Is(err, bootstrapauth.ErrFatal) && !errors.Is(err, bootstrapauth.ErrUnconfirmed) {
			r.peer = nil
		}
	}
	if reader != nil {
		err := reader.Close(cleanup)
		result = errors.Join(result, err)
		if err == nil {
			r.mu.Lock()
			sealed.reader = nil
			r.mu.Unlock()
		}
	}
	if r.raw != 0 {
		err := r.ops.closeHandle(cleanup, r.raw)
		result = errors.Join(result, err)
		if err == nil {
			r.raw = 0
		}
	}
	r.mu.Lock()
	r.rememberFatal(result)
	fatal := r.fatal
	r.mu.Unlock()
	return errors.Join(result, fatal)
}

func copyReceiverProfile(p bootstrapauth.Profile) bootstrapauth.Profile {
	nodes := func(in []protectedkey.NodePolicy) []protectedkey.NodePolicy {
		out := append([]protectedkey.NodePolicy(nil), in...)
		for i := range out {
			if out[i].ACL != nil {
				out[i].ACL = append(make([]protectedkey.ACE, 0, len(out[i].ACL)), out[i].ACL...)
			}
		}
		return out
	}
	catalog := make(map[string]protectedkey.KeyProfile, len(p.Keys.Catalog))
	for ref, key := range p.Keys.Catalog {
		key.Nodes = nodes(key.Nodes)
		catalog[ref] = key
	}
	p.Keys.Catalog = catalog
	p.ChildImage.Nodes = nodes(p.ChildImage.Nodes)
	p.SupervisorImage.Nodes = nodes(p.SupervisorImage.Nodes)
	return p
}
