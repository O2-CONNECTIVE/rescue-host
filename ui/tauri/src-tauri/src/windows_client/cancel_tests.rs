use super::*;
use std::cell::{Cell, RefCell};
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::atomic::Ordering;
use std::time::Duration;
use windows_sys::Win32::Foundation::{
    ERROR_INVALID_HANDLE, ERROR_IO_PENDING, ERROR_NOT_FOUND, ERROR_OPERATION_ABORTED, WAIT_OBJECT_0,
    WAIT_TIMEOUT,
};

// Pure API fixture: these handle values are never passed to Windows.
struct UnconfirmedCompletion {
    now: Instant,
    starts: Cell<usize>,
    results: Cell<usize>,
    closed_events: Cell<usize>,
    fatals: Cell<usize>,
}

impl Calls for UnconfirmedCompletion {
    fn event(&self) -> Result<HANDLE, Fault> {
        Ok(std::ptr::without_provenance_mut(2))
    }

    unsafe fn start(
        &self,
        _: HANDLE,
        kind: Kind,
        _: *mut u8,
        _: usize,
        _: *mut OVERLAPPED,
    ) -> Result<(), u32> {
        assert!(kind == Kind::Read);
        self.starts.set(self.starts.get() + 1);
        Err(ERROR_IO_PENDING)
    }

    fn wait(&self, _: HANDLE, _: u32) -> u32 {
        WAIT_OBJECT_0
    }

    unsafe fn result(&self, _: HANDLE, _: *const OVERLAPPED) -> Result<usize, u32> {
        self.results.set(self.results.get() + 1);
        Err(ERROR_INVALID_HANDLE)
    }

    unsafe fn cancel(&self, _: HANDLE, _: *const OVERLAPPED) -> Result<(), u32> {
        panic!("this completion-query fixture must not request cancellation")
    }

    fn close(&self, _: HANDLE) {
        self.closed_events.set(self.closed_events.get() + 1);
    }

    fn now(&self) -> Instant {
        self.now
    }

    fn fatal(&self) -> ! {
        self.fatals.set(self.fatals.get() + 1);
        panic!("fake unresolved completion fatal")
    }
}

#[test]
fn an_unconfirmed_completion_query_is_fatal_without_closing_the_event() {
    let api = UnconfirmedCompletion {
        now: Instant::now(),
        starts: Cell::new(0),
        results: Cell::new(0),
        closed_events: Cell::new(0),
        fatals: Cell::new(0),
    };
    let mut data = [0; 3];
    let outcome = catch_unwind(AssertUnwindSafe(|| {
        operation::transfer(
            &api,
            std::ptr::without_provenance_mut(1),
            Kind::Read,
            &mut data,
            &AtomicBool::new(false),
            api.now + Duration::from_secs(1),
        )
    }));

    // A failed completion query is not evidence that pending OS references ended.
    // Native fatal must not unwind; the existing unresolved path also retains
    // its Work allocation when this test's fake fatal deliberately unwinds.
    assert!(outcome.is_err(), "unconfirmed completion must invoke fatal");
    assert_eq!(api.fatals.get(), 1);
    assert_eq!(api.closed_events.get(), 0);
    assert_eq!(api.starts.get(), 1);
    assert_eq!(api.results.get(), 1);
}

// Additional regression cases after the completion-query RED/GREEN. The fake
// advances time in wait(), never sleeps, and never dereferences OVERLAPPED.
struct CancelRace<'a> {
    kind: Kind,
    cancel: &'a AtomicBool,
    base: Instant,
    millis: Cell<u64>,
    now_calls: Cell<usize>,
    op: Cell<*const OVERLAPPED>,
    events: RefCell<Vec<&'static str>>,
    cancel_result: Result<(), u32>,
    completion: Result<usize, u32>,
    synchronous: bool,
    signals: bool,
}

impl<'a> CancelRace<'a> {
    fn new(kind: Kind, cancel: &'a AtomicBool) -> Self {
        Self {
            kind,
            cancel,
            base: Instant::now(),
            millis: Cell::new(0),
            now_calls: Cell::new(0),
            op: Cell::new(std::ptr::null()),
            events: RefCell::new(Vec::new()),
            cancel_result: Err(ERROR_NOT_FOUND),
            completion: Ok(3),
            synchronous: false,
            signals: true,
        }
    }

    fn mark(&self, event: &'static str) {
        self.events.borrow_mut().push(event);
    }
}

impl Calls for CancelRace<'_> {
    fn event(&self) -> Result<HANDLE, Fault> {
        self.mark("event");
        Ok(std::ptr::without_provenance_mut(2))
    }

    unsafe fn start(
        &self,
        _: HANDLE,
        kind: Kind,
        data: *mut u8,
        length: usize,
        op: *mut OVERLAPPED,
    ) -> Result<(), u32> {
        assert!(kind == self.kind);
        assert!(!data.is_null());
        assert_eq!(length, 3);
        assert!(!op.is_null());
        self.op.set(op.cast_const());
        self.mark("start");
        if kind == Kind::Read {
            // Only the owned fake IO buffer is written, without creating a reference.
            unsafe { data.write_bytes(9, length) };
        }
        self.cancel.store(true, Ordering::Release);
        if self.synchronous {
            Ok(())
        } else {
            Err(ERROR_IO_PENDING)
        }
    }

    fn wait(&self, _: HANDLE, millis: u32) -> u32 {
        self.mark("wait");
        self.millis.set(self.millis.get() + u64::from(millis));
        if self.signals {
            WAIT_OBJECT_0
        } else {
            WAIT_TIMEOUT
        }
    }

    unsafe fn result(&self, _: HANDLE, op: *const OVERLAPPED) -> Result<usize, u32> {
        assert_eq!(op, self.op.get(), "query the original OVERLAPPED only");
        self.mark("result");
        self.completion
    }

    unsafe fn cancel(&self, _: HANDLE, op: *const OVERLAPPED) -> Result<(), u32> {
        assert_eq!(op, self.op.get(), "cancel the original OVERLAPPED only");
        self.mark("cancel");
        self.cancel_result
    }

    fn close(&self, _: HANDLE) {
        assert_eq!(self.events.borrow().last().copied(), Some("result"));
        self.mark("close");
    }

    fn now(&self) -> Instant {
        self.now_calls.set(self.now_calls.get() + 1);
        self.base + Duration::from_millis(self.millis.get())
    }

    fn fatal(&self) -> ! {
        self.mark("fatal");
        panic!("fake unresolved cancellation fatal")
    }
}

#[test]
fn cancelled_reads_and_writes_wait_for_completion_even_when_cancel_finds_nothing() {
    for kind in [Kind::Read, Kind::Write] {
        for cancel_result in [Ok(()), Err(ERROR_NOT_FOUND)] {
            for completion in [Ok(3), Err(ERROR_OPERATION_ABORTED)] {
                let cancel = AtomicBool::new(false);
                let mut api = CancelRace::new(kind, &cancel);
                api.cancel_result = cancel_result;
                api.completion = completion;
                let mut data = [7; 3];
                assert_eq!(
                    operation::transfer(
                        &api,
                        std::ptr::without_provenance_mut(1),
                        kind,
                        &mut data,
                        &cancel,
                        api.base + Duration::from_secs(2),
                    ),
                    Err(Fault::Cancelled)
                );
                assert_eq!(data, [7; 3], "cancelled read bytes must not be exposed");
                assert_eq!(
                    api.events.into_inner(),
                    ["event", "start", "cancel", "wait", "result", "close"]
                );
            }
        }
    }
}

#[test]
fn cancellation_during_synchronous_completion_never_becomes_success() {
    for kind in [Kind::Read, Kind::Write] {
        let cancel = AtomicBool::new(false);
        let mut api = CancelRace::new(kind, &cancel);
        api.synchronous = true;
        let mut data = [7; 3];
        assert_eq!(
            operation::transfer(
                &api,
                std::ptr::without_provenance_mut(1),
                kind,
                &mut data,
                &cancel,
                api.base + Duration::from_secs(2),
            ),
            Err(Fault::Cancelled)
        );
        assert_eq!(data, [7; 3]);
        assert_eq!(api.events.into_inner(), ["event", "start", "result", "close"]);
    }
}

#[test]
fn uncompleted_cancellation_is_fatal_after_one_second_without_closing_the_event() {
    for kind in [Kind::Read, Kind::Write] {
        let cancel = AtomicBool::new(false);
        let mut api = CancelRace::new(kind, &cancel);
        api.signals = false;
        let mut data = [7; 3];
        let outcome = catch_unwind(AssertUnwindSafe(|| {
            operation::transfer(
                &api,
                std::ptr::without_provenance_mut(1),
                kind,
                &mut data,
                &cancel,
                api.base + Duration::from_secs(2),
            )
        }));
        assert!(outcome.is_err(), "pending cancellation must invoke fake fatal");
        assert_eq!(api.millis.get(), 1000);
        assert_eq!(data, [7; 3]);
        let events = api.events.into_inner();
        assert_eq!(events.last(), Some(&"fatal"));
        assert_eq!(events.iter().filter(|event| **event == "cancel").count(), 1);
        assert!(!events.contains(&"result"));
        assert!(!events.contains(&"close"));
        // The unresolved Work remains raw-owned; only this fake fatal unwinds.
    }
}

#[test]
fn cancellation_before_start_makes_no_api_calls() {
    for kind in [Kind::Read, Kind::Write] {
        let cancel = AtomicBool::new(true);
        let api = CancelRace::new(kind, &cancel);
        let mut data = [7; 3];
        assert_eq!(
            operation::transfer(
                &api,
                std::ptr::without_provenance_mut(1),
                kind,
                &mut data,
                &cancel,
                api.base + Duration::from_secs(2),
            ),
            Err(Fault::Cancelled)
        );
        assert_eq!(api.now_calls.get(), 0);
        assert!(api.events.into_inner().is_empty());
        assert_eq!(data, [7; 3]);
    }
}
