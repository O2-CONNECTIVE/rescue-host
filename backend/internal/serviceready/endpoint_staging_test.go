package serviceready

import (
	"context"
	"errors"
	"slices"
	"testing"
)

type inspectionSplitter interface {
	SplitForInspection(context.Context) (*SoleInput, *InspectionLease, error)
}

func stagingSplitter(t *testing.T, e *Endpoint) inspectionSplitter {
	t.Helper()
	s, ok := any(e).(inspectionSplitter)
	if !ok {
		t.Fatal("同endpointの検査借用とreader分離を結ぶ入口がない")
	}
	return s
}

func TestEndpointSplitForInspectionOrdersAndRetainsSameLease(t *testing.T) {
	e, k := inspectionFixture(t)
	input, lease, err := stagingSplitter(t, e).SplitForInspection(testContext(t))
	if err != nil || input == nil || lease == nil || input.endpoint != e || lease.endpoint != e || e.inspection != lease {
		t.Fatal("同endpointの所有対を返さない", err)
	}
	if !slices.Equal(k.pipe.duplicates, []uint32{endpointInspectionAccess, endpointReadAccess, endpointWriteAccess}) || k.pipe.closes != 1 || e.duplex != nil || k.listenerCreates != 1 {
		t.Fatal("検査複製より前にduplexを分離、または別listenerを作成")
	}
	if raw, err := input.Move(testContext(t)); err != nil || raw != 88 {
		t.Fatal("既存の単回reader移譲が失敗", raw, err)
	}
	k.thread++ // leaseの実照会は発行workerのscopeを再入しない。
	checks := k.scope.checks
	if raw, err := lease.Borrow(testContext(t)); err != nil || raw != 91 || k.scope.checks != checks {
		t.Fatal("Split/Move後に同leaseを照会できない", raw, err)
	}
	k.thread--
	if receipt, err := e.CloseEndpoint(testContext(t)); receipt != nil || !errors.Is(err, ErrUnconfirmed) || k.pipe.writer.closes != 0 || k.scope.closes != 0 {
		t.Fatal("lease回収前にendpointを回収済みとした", err)
	}
	if err := lease.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if receipt, err := e.CloseEndpoint(testContext(t)); err != nil || receipt == nil || k.inspectionPipe.inspection.closes != 1 || k.pipe.read.closes != 0 {
		t.Fatal("同lease回収後のackまたは移譲済みreader所有が不正", err)
	}
}

// NewInspectionLeaseがgateを返した後、Splitが取得する直前に取消する。
// 時間待ちや製品callbackを使わず、元有限contextの取消を境界へ固定する。
type stagingSplitCancelContext struct {
	context.Context
	endpoint *Endpoint
	cancel   context.CancelFunc
	fired    bool
}

func (c *stagingSplitCancelContext) Err() error {
	if !c.fired && c.endpoint.inspection != nil && c.endpoint.query.gate.TryLock() {
		c.endpoint.query.gate.Unlock()
		c.fired = true
		c.cancel()
	}
	return c.Context.Err()
}

func TestEndpointSplitForInspectionKeepsPartialLeaseOnFailure(t *testing.T) {
	for _, kind := range []string{"duplicate", "inspection", "between-cancel", "read", "writer", "duplex-close"} {
		t.Run(kind, func(t *testing.T) {
			e, k := inspectionFixture(t)
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			boundary := &stagingSplitCancelContext{Context: ctx, endpoint: e, cancel: cancel}
			switch kind {
			case "duplicate":
				k.pipe.duplicateErr = ErrDenied
			case "inspection":
				k.inspectionPipe.inspection.inspectErr = ErrDenied
			case "between-cancel":
				ctx = boundary
			case "read":
				k.pipe.read.err = ErrDenied
			case "writer":
				k.pipe.writer.err = ErrDenied
			case "duplex-close":
				k.pipe.closeErr = ErrUnconfirmed
			}
			input, lease, err := stagingSplitter(t, e).SplitForInspection(ctx)
			if err == nil || input != nil || lease == nil || lease != e.inspection || lease.endpoint != e || lease.ref != &k.inspectionPipe.inspection || e.inspectionActive.Load() != 1 {
				t.Fatal("失敗時の部分lease所有が失われた", err)
			}
			if kind == "between-cancel" && (!boundary.fired || e.split || len(k.pipe.duplicates) != 1) {
				t.Fatal("lease返却とSplitの間の取消で新規分離を開始")
			}
			if kind == "duplicate" || kind == "inspection" {
				if e.split || len(k.pipe.duplicates) != 1 {
					t.Fatal("検査借用の失敗後にreaderを分離")
				}
			}
			if receipt, err := e.CloseEndpoint(testContext(t)); receipt != nil || !errors.Is(err, ErrUnconfirmed) || k.scope.closes != 0 {
				t.Fatal("部分lease回収前にack", err)
			}
			if err := lease.Close(testContext(t)); err != nil {
				t.Fatal("健康な同ownerによる部分lease回収失敗", err)
			}
			k.pipe.closeErr = nil
			if _, err := e.CloseEndpoint(testContext(t)); err != nil || k.inspectionPipe.inspection.closes != 1 {
				t.Fatal("回収済みleaseを二重closeまたは残りの回収失敗", err)
			}
		})
	}
}

func TestEndpointSplitForInspectionRetainsUnknownDuplicate(t *testing.T) {
	e, k := inspectionFixture(t)
	k.pipe.onDuplicate = func() { panic("合成native未return") }
	input, lease, err := stagingSplitter(t, e).SplitForInspection(testContext(t))
	if input != nil || lease == nil || !errors.Is(err, ErrFatal) || lease != e.inspection || !lease.unknown || e.split || len(k.pipe.duplicates) != 1 {
		t.Fatal("unknown取得の元ownerを失った", err)
	}
	if err := lease.Close(testContext(t)); !errors.Is(err, ErrUnconfirmed) {
		t.Fatal("unknownを回収済みにした", err)
	}
	if receipt, err := e.CloseEndpoint(testContext(t)); receipt != nil || !errors.Is(err, ErrUnconfirmed) || k.pipe.closes != 0 || k.scope.closes != 0 {
		t.Fatal("unknownの参照を解放した", err)
	}
}

func TestEndpointSplitForInspectionRejectsExistingAndForeignLease(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "foreign"}[foreign], func(t *testing.T) {
			e, k := inspectionFixture(t)
			owner := e
			if foreign {
				owner, _ = inspectionFixture(t)
			}
			lease, err := owner.NewInspectionLease(testContext(t))
			if err != nil {
				t.Fatal(err)
			}
			if foreign {
				e.inspection = lease
			} // 内部slot改変でも別ownerを採用しない。
			before := len(k.pipe.duplicates)
			input, returned, err := stagingSplitter(t, e).SplitForInspection(testContext(t))
			if err == nil || input != nil || returned != nil || len(k.pipe.duplicates) != before || e.split || lease.closed || lease.endpoint != owner {
				t.Fatal("既存または別ownerのleaseを再利用", err)
			}
			if err := lease.Close(testContext(t)); err != nil {
				t.Fatal(err)
			}
			if _, err := owner.CloseEndpoint(testContext(t)); err != nil {
				t.Fatal(err)
			}
			if foreign {
				if _, err := e.CloseEndpoint(testContext(t)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestEndpointSplitForInspectionWaitsForBorrowReturnBeforeClose(t *testing.T) {
	e, k := inspectionFixture(t)
	_, lease, err := stagingSplitter(t, e).SplitForInspection(testContext(t))
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	k.inspectionPipe.inspection.onInspect = func() { close(entered); <-release }
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := lease.Borrow(ctx); done <- err }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatal("保持queryが開始しなかった", err)
	case <-ctx.Done():
		close(release)
		t.Fatal("保持queryの開始を確認できない")
	}
	cancel()
	closeErr := lease.Close(testContext(t))
	receipt, endpointErr := e.CloseEndpoint(testContext(t))
	close(release)
	borrowErr := <-done
	if !errors.Is(closeErr, ErrUnconfirmed) || receipt != nil || !errors.Is(endpointErr, ErrUnconfirmed) || borrowErr == nil || e.inspectionActive.Load() != 1 {
		t.Fatal("query実return前の取消を回収ackにした", closeErr, endpointErr, borrowErr)
	}
	if err := lease.Close(testContext(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CloseEndpoint(testContext(t)); err != nil {
		t.Fatal(err)
	}
}

func TestEndpointSplitForInspectionRejectsLateAndRepeatedSplit(t *testing.T) {
	for _, kind := range []string{"unconnected", "split", "closed", "repeat", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			e, k := inspectionFixture(t)
			ctx, cancel := context.WithCancel(testContext(t))
			defer cancel()
			var held *InspectionLease
			switch kind {
			case "unconnected":
				e.connected = false // 同memory fixtureの接続終端未確認状態。
			case "split":
				if _, err := e.Split(ctx); err != nil {
					t.Fatal(err)
				}
			case "closed":
				if _, err := e.CloseEndpoint(ctx); err != nil {
					t.Fatal(err)
				}
			case "repeat":
				var err error
				_, held, err = stagingSplitter(t, e).SplitForInspection(ctx)
				if err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			}
			before := len(k.pipe.duplicates)
			input, lease, err := stagingSplitter(t, e).SplitForInspection(ctx)
			if input != nil || lease != nil || err == nil || len(k.pipe.duplicates) != before {
				t.Fatal("未接続・閉鎖・分離後・取消を再取得へ変換", err)
			}
			if held != nil {
				if err := held.Close(testContext(t)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := e.CloseEndpoint(testContext(t)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
