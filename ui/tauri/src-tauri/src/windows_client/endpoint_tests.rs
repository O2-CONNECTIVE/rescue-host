use super::super::Kind;
use super::*;
use std::sync::atomic::{AtomicUsize, Ordering as AtomicOrder};
use std::sync::Mutex;
use std::time::Duration;
use windows_sys::Win32::Foundation::ERROR_BROKEN_PIPE;
use windows_sys::Win32::System::Threading::{
    PROCESS_QUERY_LIMITED_INFORMATION, PROCESS_SYNCHRONIZE,
};
use windows_sys::Win32::System::IO::OVERLAPPED;

const SERVICE_SID: &str = "S-1-5-80-11-22-33-44-55";

// Every handle and API result below is synthetic. No Windows object is opened.
struct FakeApi {
    payload: Vec<u8>,
    calls: AtomicUsize,
    reads: AtomicUsize,
    writes: AtomicUsize,
    result_count: AtomicUsize,
    next_event: AtomicUsize,
    closed: Mutex<Vec<usize>>,
}

impl FakeApi {
    fn new(payload: Vec<u8>) -> Self {
        Self {
            payload,
            calls: AtomicUsize::new(0),
            reads: AtomicUsize::new(0),
            writes: AtomicUsize::new(0),
            result_count: AtomicUsize::new(0),
            next_event: AtomicUsize::new(100),
            closed: Mutex::new(Vec::new()),
        }
    }

    fn touch(&self) {
        self.calls.fetch_add(1, AtomicOrder::SeqCst);
    }

    fn close_handle(&self, handle: HANDLE) {
        self.touch();
        self.closed.lock().unwrap().push(handle.addr());
    }
}

impl Calls for FakeApi {
    fn event(&self) -> Result<HANDLE, Fault> {
        self.touch();
        Ok(std::ptr::without_provenance_mut(
            self.next_event.fetch_add(1, AtomicOrder::SeqCst),
        ))
    }

    unsafe fn start(
        &self,
        pipe: HANDLE,
        kind: Kind,
        data: *mut u8,
        length: usize,
        _: *mut OVERLAPPED,
    ) -> Result<(), u32> {
        self.touch();
        assert_eq!(pipe.addr(), 1);
        if kind == Kind::Read {
            assert_eq!(length, crate::control_codec::MAX_FRAME_BYTES);
            if self.reads.fetch_add(1, AtomicOrder::SeqCst) != 0 {
                return Err(ERROR_BROKEN_PIPE);
            }
            assert!(self.payload.len() <= length);
            // This fake completes synchronously. It retains no pointer or reference.
            unsafe {
                std::ptr::copy_nonoverlapping(self.payload.as_ptr(), data, self.payload.len());
            }
            self.result_count
                .store(self.payload.len(), AtomicOrder::SeqCst);
        } else {
            self.writes.fetch_add(1, AtomicOrder::SeqCst);
            self.result_count.store(length, AtomicOrder::SeqCst);
        }
        Ok(())
    }

    fn wait(&self, _: HANDLE, _: u32) -> u32 {
        panic!("synchronous fake must not wait")
    }

    unsafe fn result(&self, pipe: HANDLE, _: *const OVERLAPPED) -> Result<usize, u32> {
        self.touch();
        assert_eq!(pipe.addr(), 1);
        Ok(self.result_count.load(AtomicOrder::SeqCst))
    }

    unsafe fn cancel(&self, _: HANDLE, _: *const OVERLAPPED) -> Result<(), u32> {
        panic!("synchronous fake must not cancel an operation")
    }

    fn close(&self, handle: HANDLE) {
        assert!(handle.addr() == 1 || handle.addr() >= 100);
        self.close_handle(handle);
    }

    fn now(&self) -> Instant {
        self.touch();
        Instant::now()
    }

    fn fatal(&self) -> ! {
        panic!("fake endpoint fatal")
    }
}

impl PeerCalls for FakeApi {
    fn pipe_server_pid(&self, pipe: HANDLE) -> Result<u32, Fault> {
        self.touch();
        assert_eq!(pipe.addr(), 1);
        Ok(42)
    }

    fn open_process(&self, pid: u32, access: u32, inherit: bool) -> Result<HANDLE, Fault> {
        self.touch();
        assert_eq!(pid, 42);
        assert_eq!(
            access,
            PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_SYNCHRONIZE
        );
        assert!(!inherit);
        Ok(std::ptr::without_provenance_mut(7))
    }

    fn process_id(&self, process: HANDLE) -> Result<u32, Fault> {
        self.touch();
        assert_eq!(process.addr(), 7);
        Ok(42)
    }

    fn created(&self, process: HANDLE) -> Result<u64, Fault> {
        self.touch();
        assert_eq!(process.addr(), 7);
        Ok(100)
    }

    fn token_user(&self, process: HANDLE) -> Result<String, Fault> {
        self.touch();
        assert_eq!(process.addr(), 7);
        Ok(SERVICE_SID.into())
    }

    fn alive(&self, process: HANDLE) -> Result<bool, Fault> {
        self.touch();
        assert_eq!(process.addr(), 7);
        Ok(true)
    }

    fn close(&self, handle: HANDLE) {
        assert_eq!(handle.addr(), 7);
        self.close_handle(handle);
    }

    fn fatal(&self) -> ! {
        panic!("fake peer fatal")
    }
}

fn frame(protocol: &str, kind: &str) -> Vec<u8> {
    let mut bytes = serde_json::to_vec(&serde_json::json!({
        "protocol": protocol, "kind": kind, "generation": "1",
        "identity": {
            "launchId": "launch-001", "epoch": "1", "policyDigest": "a".repeat(64)
        },
        "binding": { "artifactSha256": "b".repeat(64), "channelNonce": "c".repeat(64) }
    }))
    .unwrap();
    bytes.push(b'\n');
    bytes
}

fn bound_halves(
    payload: Vec<u8>,
) -> (MessageReader<FakeApi>, MessageWriter<FakeApi>, Arc<FakeApi>) {
    let api = Arc::new(FakeApi::new(payload));
    let pipe = std::ptr::without_provenance_mut(1);
    let peer = bind(
        Arc::clone(&api),
        pipe,
        ExpectedServer {
            pid: 42,
            created: 100,
            service_sid: SERVICE_SID.into(),
        },
    )
    .unwrap();
    let (reader, writer) = halves(
        Arc::clone(&api),
        pipe,
        peer,
        Instant::now() + Duration::from_secs(30),
    );
    assert!(Arc::ptr_eq(&reader.owner.api, &api));
    assert!(Arc::ptr_eq(&writer.owner.api, &api));
    (reader, writer, api)
}

#[test]
fn one_message_is_sliced_after_validation_and_the_last_half_closes_owned_handles() {
    let expected = frame("rescue-host-control/1", "hello_ack");
    let (mut reader, writer, api) = bound_halves(expected.clone());
    drop(writer);
    assert!(api.closed.lock().unwrap().is_empty());

    let mut actual = Vec::new();
    for _ in 0..expected.len() {
        let mut byte = [0];
        assert_eq!(reader.read(&mut byte).unwrap(), 1);
        actual.push(byte[0]);
    }
    assert_eq!(actual, expected);
    assert_eq!(api.reads.load(AtomicOrder::SeqCst), 1);
    assert_eq!(*api.closed.lock().unwrap(), vec![100]);

    let mut byte = [0];
    assert_eq!(reader.read(&mut byte).unwrap(), 0); // Only the fake broken pipe is EOF.
    assert_eq!(api.reads.load(AtomicOrder::SeqCst), 2);
    let calls_after_eof = api.calls.load(AtomicOrder::SeqCst);
    assert_eq!(reader.read(&mut byte).unwrap(), 0);
    assert_eq!(api.calls.load(AtomicOrder::SeqCst), calls_after_eof);
    assert_eq!(*api.closed.lock().unwrap(), vec![100, 101]);

    drop(reader);
    assert_eq!(*api.closed.lock().unwrap(), vec![100, 101, 1, 7]);
    assert_eq!(api.writes.load(AtomicOrder::SeqCst), 0);
}

#[test]
fn an_unknown_protocol_is_not_exposed_as_bytes_and_seals_both_halves() {
    let (mut reader, mut writer, api) = bound_halves(frame("unknown-protocol", "hello_ack"));
    let mut byte = [0xa5];
    assert_eq!(
        reader.read(&mut byte).unwrap_err().kind(),
        io::ErrorKind::InvalidData
    );
    assert_eq!(byte, [0xa5]);
    assert_eq!(writer.fault(), Some(Code::VersionMismatch));
    assert!(writer.cancel_token().load(AtomicOrder::Acquire));

    let calls_after_fault = api.calls.load(AtomicOrder::SeqCst);
    assert!(reader.read(&mut byte).is_err());
    assert_eq!(byte, [0xa5]);
    let hello = frame("rescue-host-control/1", "hello");
    for _ in 0..2 {
        assert_eq!(
            writer.send_frame(&hello, Instant::now() + Duration::from_secs(30)),
            Err(Fault::Cancelled)
        );
    }
    writer.cancel();
    assert!(writer.cancel_token().load(AtomicOrder::Acquire));
    assert_eq!(writer.fault(), Some(Code::VersionMismatch));
    assert_eq!(api.writes.load(AtomicOrder::SeqCst), 0);
    assert_eq!(api.calls.load(AtomicOrder::SeqCst), calls_after_fault);
    drop(writer);
    assert_eq!(*api.closed.lock().unwrap(), vec![100]);
    drop(reader);
    assert_eq!(*api.closed.lock().unwrap(), vec![100, 1, 7]);
}

#[test]
fn native_half_types_satisfy_the_required_thread_and_read_traits() {
    fn read_and_send<T: Read + Send>() {}
    fn send_only<T: Send>() {}
    read_and_send::<NativeReader>();
    send_only::<NativeWriter>();
}
