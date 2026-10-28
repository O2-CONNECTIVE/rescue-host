use super::*;
use std::cell::Cell;
use std::time::Duration;

struct MessageRead {
    frame: Vec<u8>,
    starts: Cell<usize>,
}

impl Calls for MessageRead {
    fn event(&self) -> Result<HANDLE, Fault> {
        Ok(std::ptr::without_provenance_mut(2))
    }
    unsafe fn start(
        &self,
        _: HANDLE,
        kind: Kind,
        data: *mut u8,
        length: usize,
        _: *mut OVERLAPPED,
    ) -> Result<(), u32> {
        assert!(kind == Kind::Read);
        assert_eq!(length, crate::control_codec::MAX_FRAME_BYTES);
        let data = unsafe { std::slice::from_raw_parts_mut(data, length) };
        data[..self.frame.len()].copy_from_slice(&self.frame);
        self.starts.set(self.starts.get() + 1);
        Ok(())
    }
    fn wait(&self, _: HANDLE, _: u32) -> u32 {
        panic!("synchronous fake must not wait")
    }
    unsafe fn result(&self, _: HANDLE, _: *const OVERLAPPED) -> Result<usize, u32> {
        Ok(self.frame.len())
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
fn one_message_is_checked_whole_before_exposing_any_frame_to_the_reader() {
    let mut valid = serde_json::to_vec(&serde_json::json!({
        "kind": "hello_ack", "protocol": "rescue-host-control/1", "generation": "1",
        "identity": {"launchId": "launch-001", "epoch": "1", "policyDigest": "a".repeat(64)},
        "binding": {"artifactSha256": "b".repeat(64), "channelNonce": "c".repeat(64)}
    }))
    .unwrap();
    valid.push(b'\n');
    for (frame, expected) in [
        (valid.clone(), Ok(Some(valid.clone()))),
        (
            [valid, b"{}\n".to_vec()].concat(),
            Err(Fault::InvalidMessage),
        ),
    ] {
        let api = MessageRead {
            frame,
            starts: Cell::new(0),
        };
        assert_eq!(
            receive(
                &api,
                std::ptr::without_provenance_mut(1),
                &AtomicBool::new(false),
                Instant::now() + Duration::from_secs(1),
            ),
            expected
        );
        assert_eq!(api.starts.get(), 1);
    }
}
