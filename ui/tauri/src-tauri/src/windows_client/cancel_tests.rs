use super::*;
use std::cell::Cell;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::time::Duration;
use windows_sys::Win32::Foundation::{ERROR_INVALID_HANDLE, ERROR_IO_PENDING, WAIT_OBJECT_0};

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
        _: &mut [u8],
        _: &mut OVERLAPPED,
    ) -> Result<(), u32> {
        assert!(kind == Kind::Read);
        self.starts.set(self.starts.get() + 1);
        Err(ERROR_IO_PENDING)
    }

    fn wait(&self, _: HANDLE, _: u32) -> u32 {
        WAIT_OBJECT_0
    }

    fn result(&self, _: HANDLE, _: &mut OVERLAPPED) -> Result<usize, u32> {
        self.results.set(self.results.get() + 1);
        Err(ERROR_INVALID_HANDLE)
    }

    fn cancel(&self, _: HANDLE, _: &mut OVERLAPPED) -> Result<(), u32> {
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
    assert!(
        outcome.is_err(),
        "unconfirmed completion must invoke fatal"
    );
    assert_eq!(api.fatals.get(), 1);
    assert_eq!(api.closed_events.get(), 0);
    assert_eq!(api.starts.get(), 1);
    assert_eq!(api.results.get(), 1);
}
