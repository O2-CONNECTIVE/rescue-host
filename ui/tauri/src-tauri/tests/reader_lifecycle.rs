#[allow(dead_code, reason = "these checks exercise only the reader lifecycle")]
#[path = "../src/control_codec/mod.rs"]
mod control_codec;
#[allow(dead_code, reason = "these checks exercise only the reader lifecycle")]
#[path = "../src/control_coordinator/mod.rs"]
mod control_coordinator;
#[allow(dead_code, reason = "these checks exercise only the reader lifecycle")]
#[path = "../src/owned_process/mod.rs"]
mod owned_process;

use control_codec::scalar::Code;
use control_coordinator::reader::{ReaderEnd, ReaderEvent, ReaderTask};
use std::io::{self, Cursor, Read};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{mpsc, Arc, Condvar, Mutex};
use std::time::{Duration, Instant};

fn join(reader: &mut ReaderTask) -> ReaderEnd {
    let deadline = Instant::now() + Duration::from_secs(2);
    loop {
        match reader.poll_until(deadline) {
            ReaderEvent::Frame(_) => {}
            ReaderEvent::Joined(end) => return end,
            event => panic!("reader not joined within test budget: {event:?}"),
        }
    }
}

struct HeldRead {
    gate: Arc<(Mutex<bool>, Condvar)>,
    entered: Option<mpsc::SyncSender<()>>,
}

impl Read for HeldRead {
    fn read(&mut self, _: &mut [u8]) -> io::Result<usize> {
        if let Some(entered) = self.entered.take() {
            entered.try_send(()).unwrap();
        }
        let (lock, ready) = &*self.gate;
        let state = lock.lock().unwrap();
        let (_state, result) = ready
            .wait_timeout_while(state, Duration::from_secs(5), |released| !*released)
            .unwrap();
        if result.timed_out() {
            Err(io::ErrorKind::TimedOut.into())
        } else {
            Ok(0)
        }
    }
}

#[test]
fn cancel_does_not_claim_to_interrupt_blocking_read_or_consume_the_owner() {
    let gate = Arc::new((Mutex::new(false), Condvar::new()));
    let (entered, received) = mpsc::sync_channel(1);
    let mut reader = ReaderTask::start(HeldRead {
        gate: Arc::clone(&gate),
        entered: Some(entered),
    })
    .unwrap();
    received.recv_timeout(Duration::from_secs(2)).unwrap();
    reader.cancel();
    let pending = reader.poll_until(Instant::now() + Duration::from_millis(10));
    // Always release the controlled read before asserting, including regressions.
    let (lock, ready) = &*gate;
    *lock.lock().unwrap() = true;
    ready.notify_all();
    assert_eq!(pending, ReaderEvent::Pending);
    assert_eq!(join(&mut reader), ReaderEnd::Cancelled);
    assert_eq!(reader.poll(), ReaderEvent::Closed);
}

struct CountedRead {
    input: Cursor<Vec<u8>>,
    reads: Arc<AtomicUsize>,
    full: mpsc::SyncSender<()>,
    released: mpsc::SyncSender<()>,
}

impl Read for CountedRead {
    fn read(&mut self, bytes: &mut [u8]) -> io::Result<usize> {
        if bytes.is_empty() {
            return Ok(0);
        }
        let length = self.input.read(&mut bytes[..1])?;
        let count = self.reads.fetch_add(length, Ordering::SeqCst) + length;
        if count == 6 {
            self.full.try_send(()).unwrap();
        }
        Ok(length)
    }
}

impl Drop for CountedRead {
    fn drop(&mut self) {
        let _ = self.released.try_send(());
    }
}

#[test]
fn full_queue_stops_read_ahead_and_cancel_releases_the_same_reader() {
    let reads = Arc::new(AtomicUsize::new(0));
    let (full, observed) = mpsc::sync_channel(1);
    let (released, release_observed) = mpsc::sync_channel(1);
    let mut reader = ReaderTask::start(CountedRead {
        input: Cursor::new(b"{}\n{}\n{}\n".to_vec()),
        reads: Arc::clone(&reads),
        full,
        released,
    })
    .unwrap();
    observed.recv_timeout(Duration::from_secs(2)).unwrap();
    reader.cancel();
    // Do not drain the queued first frame until the input resource was released.
    release_observed.recv_timeout(Duration::from_secs(2)).unwrap();
    assert_eq!(reads.load(Ordering::SeqCst), 6);
    assert_eq!(join(&mut reader), ReaderEnd::Cancelled);
}

struct FailedRead;
impl Read for FailedRead {
    fn read(&mut self, _: &mut [u8]) -> io::Result<usize> {
        Err(io::Error::other("fixture-only detail must not reach GUI"))
    }
}

struct PanickedRead;
impl Read for PanickedRead {
    fn read(&mut self, _: &mut [u8]) -> io::Result<usize> {
        panic!("fixture-only reader panic")
    }
}

#[test]
fn truncation_limit_io_error_and_panic_never_become_eof() {
    let cases: Vec<(Box<dyn Read + Send>, ReaderEnd)> = vec![
        (
            Box::new(Cursor::new(b"{}")),
            ReaderEnd::Failed(Code::InvalidMessage),
        ),
        (
            Box::new(Cursor::new(vec![b'x'; control_codec::MAX_FRAME_BYTES + 1])),
            ReaderEnd::Failed(Code::InvalidMessage),
        ),
        (
            Box::new(FailedRead),
            ReaderEnd::Failed(Code::TerminationUnconfirmed),
        ),
        (
            Box::new(PanickedRead),
            ReaderEnd::Failed(Code::TerminationUnconfirmed),
        ),
    ];
    assert_eq!(cases.len(), 4);
    for (input, expected) in cases {
        let mut reader = ReaderTask::start(input).unwrap();
        let event = reader.poll_until(Instant::now() + Duration::from_secs(2));
        assert_eq!(event, ReaderEvent::Joined(expected));
        assert_eq!(reader.poll(), ReaderEvent::Closed);
    }
}
