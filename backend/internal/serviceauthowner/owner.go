// Package serviceauthowner binds service admission and workload observations to
// one concrete issuer and the Service's existing mutex. It grants no authority.
package serviceauthowner

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/O2-CONNECTIVE/rescue-host/backend/supervisor/ephemeral"
)

var ErrUnconfigured = errors.New("service owner is unconfigured")
var ErrDenied = errors.New("service owner admission is closed")
var ErrUnconfirmed = errors.New("service workload cleanup is unconfirmed")

type core struct {
	issuer    *ephemeral.Issuer
	gate      *sync.Mutex
	writer    *Writer
	owner     *Owner
	closed    bool
	stop      chan struct{}
	changed   chan struct{}
	confirmed bool
	fatal     error
	revokeErr error
}

// Writer is retained only by Service. Every Locked method requires its mutex.
// Copying a Writer or Owner invalidates that copy.
type Writer struct {
	self *Writer
	core *core
}
type Owner struct {
	self *Owner
	core *core
}

func New(issuer *ephemeral.Issuer, gate *sync.Mutex) (*Writer, *Owner, error) {
	if issuer == nil || gate == nil {
		return nil, nil, ErrUnconfigured
	}
	snapshot := issuer.Snapshot()
	if snapshot.InstanceEpoch == ([32]byte{}) || snapshot.Closed {
		return nil, nil, ErrUnconfigured
	}
	c := &core{issuer: issuer, gate: gate, stop: make(chan struct{}), changed: make(chan struct{})}
	w, o := &Writer{core: c}, &Owner{core: c}
	w.self, o.self, c.writer, c.owner = w, o, w, o
	return w, o, nil
}
func (w *Writer) valid() bool { return w != nil && w.self == w && w.core != nil && w.core.writer == w }
func (o *Owner) valid() bool  { return o != nil && o.self == o && o.core != nil && o.core.owner == o }
func (w *Writer) MatchesLocked(owner *Owner, issuer *ephemeral.Issuer) bool {
	return w.valid() && owner.valid() && w.core == owner.core && issuer != nil && w.core.issuer == issuer
}
func (w *Writer) OpenLocked() bool {
	if !w.valid() || w.core.closed {
		return false
	}
	snapshot, observed := w.core.issuer.TrySnapshot()
	return observed && !snapshot.Closed
}
func (o *Owner) MatchesIssuer(issuer *ephemeral.Issuer) bool {
	if !o.valid() {
		return false
	}
	return issuer != nil && o.core.issuer == issuer
}
func finite(ctx context.Context) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	deadline, ok := ctx.Deadline()
	return ok && time.Now().Before(deadline)
}

// Admit linearizes an acquisition before revocation. Native calls run only after
// this method releases the common mutex; late results remain with their owner.
func (o *Owner) Admit(ctx context.Context) error {
	if !o.valid() {
		return ErrUnconfigured
	}
	if !lockFinite(ctx, o.core.gate) {
		return ErrDenied
	}
	defer o.core.gate.Unlock()
	if !finite(ctx) || !o.core.writer.OpenLocked() || !finite(ctx) {
		return ErrDenied
	}
	return nil
}
func (o *Owner) StopRequested() <-chan struct{} {
	if !o.valid() {
		return nil
	}
	return o.core.stop
}
func (w *Writer) RevokeLocked() (result error) {
	if !w.valid() {
		return ErrUnconfigured
	}
	c := w.core
	if c.closed {
		return c.revokeErr
	}
	c.closed = true
	c.revokeErr = ErrUnconfirmed
	defer close(c.stop)
	defer func() {
		if recover() != nil {
			result = ErrUnconfirmed
		}
		c.revokeErr = result
	}()
	result = c.issuer.Close()
	// This sentinel means revocation committed while a launch is still in flight.
	// Workload acknowledgement is independently pending in this same core.
	if errors.Is(result, ephemeral.ErrLaunchUnconfirmed) {
		snapshot := c.issuer.Snapshot()
		if snapshot.Closed && !snapshot.HasAuthority {
			result = nil
		}
	}
	return result
}
func (o *Owner) Revoke() error {
	if !o.valid() {
		return ErrUnconfigured
	}
	o.core.gate.Lock()
	defer o.core.gate.Unlock()
	return o.core.writer.RevokeLocked()
}
func (w *Writer) NotifyPendingLocked() {
	if !w.valid() {
		return
	}
	close(w.core.changed)
	w.core.changed = make(chan struct{})
}
func (w *Writer) ConfirmWorkloadLocked() error {
	if !w.valid() {
		return ErrUnconfigured
	}
	c := w.core
	if !c.closed || c.revokeErr != nil || c.fatal != nil {
		return ErrUnconfirmed
	}
	c.confirmed = true
	w.NotifyPendingLocked()
	return nil
}

// FatalWorkloadLocked accepts only the fixed, sanitized classification selected
// by Service after checking the retained real workload. It does no OS work.
func (w *Writer) FatalWorkloadLocked(fatal error) error {
	if !w.valid() || fatal == nil {
		return ErrUnconfigured
	}
	c := w.core
	if !c.closed || c.confirmed {
		return ErrUnconfirmed
	}
	if c.fatal == nil {
		c.fatal = fatal
	}
	w.NotifyPendingLocked()
	return nil
}
func (o *Owner) AwaitWorkload(ctx context.Context) error {
	if !o.valid() {
		return ErrUnconfigured
	}
	if !finite(ctx) {
		return ErrUnconfirmed
	}
	deadline, _ := ctx.Deadline()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for finite(ctx) {
		c := o.core
		if !lockFinite(ctx, c.gate) {
			return ErrUnconfirmed
		}
		if !finite(ctx) {
			c.gate.Unlock()
			return ErrUnconfirmed
		}
		confirmed, fatal, changed := c.confirmed, c.fatal, c.changed
		c.gate.Unlock()
		if fatal != nil {
			return fatal
		}
		if confirmed {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ErrUnconfirmed
		case <-timer.C:
			return ErrUnconfirmed
		}
	}
	return ErrUnconfirmed
}

// WorkloadLocked observes only the state written after Service checked its
// retained workload. It is neither an authorization nor a whole-service proof.
func (w *Writer) WorkloadLocked() (bool, error) {
	if !w.valid() {
		return false, ErrUnconfigured
	}
	return w.core.confirmed, w.core.fatal
}

// lockFinite bounds memory-gate contention by the actual deadline, including
// contexts whose cancellation notification arrives late. No native call owns it.
func lockFinite(ctx context.Context, gate *sync.Mutex) bool {
	if !finite(ctx) {
		return false
	}
	if gate.TryLock() {
		if finite(ctx) {
			return true
		}
		gate.Unlock()
		return false
	}
	deadline, _ := ctx.Deadline()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	retry := time.NewTicker(time.Millisecond)
	defer retry.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return false
		case <-retry.C:
			if !finite(ctx) {
				return false
			}
			if gate.TryLock() {
				if finite(ctx) {
					return true
				}
				gate.Unlock()
				return false
			}
		}
	}
}
