package operatorauth

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"time"
)

// A lease comes only from retained, same-launch OS evidence. Its snapshot is
// read once and never supplemented by argv. prepare/check/close must honor the
// supplied finite context on this fixed thread. close releases evidence only;
// the entry owns all three inherited handles as soon as prepare returns a lease.
type helperEntrySource interface {
	prepare(context.Context) (helperEntryLease, error)
}
type helperEntryLease interface {
	config() helperEntryConfig
	receiverSource() profileReceiverSource
	check(context.Context, helperEntryConfig) error
	close(context.Context) error
}
type helperEntryConfig struct {
	binding                   profileReceiverBinding
	operatorRead, reportWrite uintptr
	runBudget, cleanup        time.Duration
}

func (c helperEntryConfig) valid() bool {
	return c.binding.valid() && reportHandle(c.operatorRead) && reportHandle(c.reportWrite) &&
		c.operatorRead != c.reportWrite && c.operatorRead != c.binding.profileRead && c.reportWrite != c.binding.profileRead &&
		c.runBudget > 0 && c.runBudget <= time.Minute && c.cleanup > 0 && c.cleanup <= time.Minute
}
func (c helperEntryConfig) matches(args []string) bool {
	return len(args) == 5 && args[0] == "--operator-auth-internal" &&
		args[1] == strconv.FormatUint(uint64(c.operatorRead), 10) &&
		args[2] == strconv.FormatUint(uint64(c.reportWrite), 10) &&
		args[3] == strconv.FormatUint(uint64(c.binding.profileRead), 10) && args[4] == c.binding.launchID
}

type helperEntryFactory struct {
	message  func(uintptr, endpointRole, time.Duration) (*messageEndpoint, error)
	profile  func(context.Context, uintptr, profileReceiverBinding, profileReceiverSource, time.Duration) (*profileReceiver, error)
	thread   func() threadKernel
	closeRaw func(uintptr) error
}

func (f helperEntryFactory) valid() bool {
	return f.message != nil && f.profile != nil && f.thread != nil && f.closeRaw != nil
}

// No production source exists. Arguments identify an expected retained launch;
// they cannot supply its protection, evidence, profile or execution budget.
func runNativeFixedHelper(ctx context.Context, args []string) error {
	return runFixedHelper(ctx, args, nil, helperEntryFactory{
		message: newNativeMessageEndpoint, profile: newNativeProfileReceiver,
		thread: func() threadKernel { return &nativeThread{} }, closeRaw: closeNativeHelperEntryHandle,
	})
}

// runFixedHelper is a private dedicated-process entry, never a registration API.
// A future source still needs an independently protected preparation deadline;
// a caller without a finite context cannot trigger even source preparation.
func runFixedHelper(ctx context.Context, args []string, source helperEntrySource, factory helperEntryFactory) (result error) {
	if source == nil {
		return ErrUnconfigured
	}
	if !reportContextValid(ctx) {
		return ErrDenied
	}
	runtime.LockOSThread()
	fatal := false
	var owner *helperEntryOwner
	defer func() {
		if recover() != nil {
			result = errors.Join(result, ErrFatal, ErrUnconfirmed)
		}
		if errors.Is(result, ErrFatal) {
			fatal = true
		}
		if !fatal {
			if owner != nil {
				result = errors.Join(result, owner.close())
				if errors.Is(result, ErrFatal) {
					return
				}
			}
			runtime.UnlockOSThread()
			if !reportContextValid(ctx) {
				result = errors.Join(result, ErrDenied)
			}
		}
	}()
	lease, err := source.prepare(ctx)
	if lease == nil {
		return errors.Join(ErrDenied, err)
	}
	owner = &helperEntryOwner{lease: lease, factory: factory}
	owner.config = lease.config()
	c := owner.config
	owner.raw = [3]uintptr{c.operatorRead, c.reportWrite, c.binding.profileRead}
	if err != nil {
		return errors.Join(ErrDenied, err)
	}
	if !reportContextValid(ctx) || !c.valid() || !c.matches(args) {
		return ErrDenied
	}
	if !factory.valid() {
		return ErrUnconfigured
	}
	run, cancel := context.WithTimeout(ctx, c.runBudget)
	defer cancel()
	check := func(checkCtx context.Context) error {
		if !reportContextValid(checkCtx) {
			return ErrDenied
		}
		if err := lease.check(checkCtx, c); err != nil {
			return errors.Join(ErrDenied, err)
		}
		if !reportContextValid(checkCtx) {
			return ErrDenied
		}
		return nil
	}
	if err = check(run); err != nil {
		return err
	}
	receiverSource := lease.receiverSource()
	if receiverSource == nil {
		return ErrUnconfigured
	}
	if err = check(run); err != nil {
		return err
	}
	if err = owner.adoptMessage(0, endpointServerRead); err != nil {
		return err
	}
	if err = check(run); err != nil {
		return err
	}
	if err = owner.adoptMessage(1, endpointClientWrite); err != nil {
		return err
	}
	if err = check(run); err != nil {
		return err
	}
	if err = owner.adoptProfile(run, receiverSource); err != nil {
		return err
	}
	if err = check(run); err != nil {
		return err
	}
	return owner.receiver.run(run, helperOps{thread: factory.thread(), pipe: c.operatorRead,
		check: check, read: owner.reader.read, publish: owner.writer.write})
}
