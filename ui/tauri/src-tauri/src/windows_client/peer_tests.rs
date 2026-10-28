use super::peer::{bind, ExpectedServer, PeerCalls};
use super::Fault;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use windows_sys::Win32::Foundation::HANDLE;
use windows_sys::Win32::System::Threading::{PROCESS_QUERY_LIMITED_INFORMATION, PROCESS_SYNCHRONIZE};

struct DifferentUser {
    closed: AtomicUsize,
}

impl PeerCalls for DifferentUser {
    fn pipe_server_pid(&self, _: HANDLE) -> Result<u32, Fault> {
        Ok(42)
    }
    fn open_process(&self, pid: u32, access: u32, inherit: bool) -> Result<HANDLE, Fault> {
        assert_eq!(pid, 42);
        let rights = PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_SYNCHRONIZE;
        assert_eq!(access, rights);
        assert!(!inherit);
        Ok(std::ptr::without_provenance_mut(7))
    }
    fn process_id(&self, _: HANDLE) -> Result<u32, Fault> {
        Ok(42)
    }
    fn created(&self, _: HANDLE) -> Result<u64, Fault> {
        Ok(100)
    }
    fn token_user(&self, _: HANDLE) -> Result<String, Fault> {
        Ok("S-1-5-80-11-22-33-44-56".into())
    }
    fn alive(&self, _: HANDLE) -> Result<bool, Fault> {
        Ok(true)
    }
    fn close(&self, handle: HANDLE) {
        assert_eq!(handle, std::ptr::without_provenance_mut(7));
        self.closed.fetch_add(1, Ordering::SeqCst);
    }
    fn fatal(&self) -> ! {
        panic!("fake peer fatal")
    }
}

#[test]
fn a_different_primary_token_user_is_rejected_and_the_process_handle_is_closed() {
    let api = Arc::new(DifferentUser {
        closed: AtomicUsize::new(0),
    });
    let result = bind(
        Arc::clone(&api),
        std::ptr::without_provenance_mut(1),
        ExpectedServer {
            pid: 42,
            created: 100,
            service_sid: "S-1-5-80-11-22-33-44-55".into(),
        },
    );
    assert!(
        matches!(result, Err(Fault::Failed)),
        "a different TokenUser must not bind as the expected service"
    );
    assert_eq!(api.closed.load(Ordering::SeqCst), 1);
}
