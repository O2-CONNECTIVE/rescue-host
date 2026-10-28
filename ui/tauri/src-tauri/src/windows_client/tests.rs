use super::*;
use std::cell::Cell;
use std::time::Duration;

struct ShortWrite {
    starts: Cell<usize>,
}

impl Calls for ShortWrite {
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
        assert!(kind == Kind::Write);
        self.starts.set(self.starts.get() + 1);
        Ok(())
    }
    fn wait(&self, _: HANDLE, _: u32) -> u32 {
        panic!("synchronous fake must not wait")
    }
    unsafe fn result(&self, _: HANDLE, _: *const OVERLAPPED) -> Result<usize, u32> {
        Ok(2)
    }
    unsafe fn cancel(&self, _: HANDLE, _: *const OVERLAPPED) -> Result<(), u32> {
        panic!("synchronous fake must not cancel")
    }
    fn close(&self, _: HANDLE) {}
    fn now(&self) -> Instant {
        Instant::now()
    }
    fn fatal(&self) -> ! {
        panic!("fake fatal")
    }
}

#[test]
fn a_short_message_write_is_not_retried_or_reported_as_delivered() {
    let api = ShortWrite {
        starts: Cell::new(0),
    };
    let mut frame = *b"{}\n";
    let result = send(
        &api,
        std::ptr::without_provenance_mut(1),
        &mut frame,
        &AtomicBool::new(false),
        Instant::now() + Duration::from_secs(1),
    );
    assert_eq!(result, Err(Fault::Failed));
    assert_eq!(api.starts.get(), 1);
}
