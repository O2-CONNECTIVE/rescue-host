use crate::control_codec::{scalar::Code, MAX_FRAME_BYTES};
use crate::owned_process::frame::read_frame;
use std::io::{self, BufReader, Read};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{mpsc, Arc};
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

const POLL_INTERVAL: Duration = Duration::from_millis(5);

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum ReaderEnd {
    Eof,
    Cancelled,
    Failed(Code),
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum ReaderEvent {
    Frame(Vec<u8>),
    Pending,
    Joined(ReaderEnd),
    Closed,
}

fn run(
    reader: impl Read,
    sender: mpsc::SyncSender<Vec<u8>>,
    cancel: &AtomicBool,
    frame_limit: u16,
) -> ReaderEnd {
    let mut reader = BufReader::with_capacity(4096, reader);
    loop {
        if cancel.load(Ordering::Acquire) {
            return ReaderEnd::Cancelled;
        }
        let result = read_frame(&mut reader, frame_limit);
        if cancel.load(Ordering::Acquire) {
            return ReaderEnd::Cancelled;
        }
        let mut frame = match result {
            Ok(Some(frame)) => frame,
            Ok(None) => return ReaderEnd::Eof,
            Err(error) => {
                let code = match error.kind() {
                    io::ErrorKind::InvalidInput
                    | io::ErrorKind::InvalidData
                    | io::ErrorKind::UnexpectedEof => Code::InvalidMessage,
                    _ => Code::TerminationUnconfirmed,
                };
                return ReaderEnd::Failed(code);
            }
        };
        loop {
            if cancel.load(Ordering::Acquire) {
                return ReaderEnd::Cancelled;
            }
            match sender.try_send(frame) {
                Ok(()) => break,
                Err(mpsc::TrySendError::Full(returned)) => {
                    frame = returned;
                    thread::sleep(POLL_INTERVAL);
                }
                Err(mpsc::TrySendError::Disconnected(_)) => return ReaderEnd::Cancelled,
            }
        }
    }
}

/// Keep this owner until Joined. Cancelling does not interrupt blocking Read.
/// Runtime wiring remains closed until read cancellation and owner lifetime are
/// established. Drop may detach an unfinished blocking read, never proving EOF.
/// Queue=1 frame; worker/consumer frames and the 4096-byte BufReader are separate.
#[must_use = "retain the reader until an actual join or explicit unresolved ownership transfer"]
pub(crate) struct ReaderTask {
    receiver: Option<mpsc::Receiver<Vec<u8>>>,
    thread: Option<JoinHandle<ReaderEnd>>,
    joined: Option<ReaderEnd>,
    cancel: Arc<AtomicBool>,
}

impl ReaderTask {
    pub fn start(reader: impl Read + Send + 'static) -> io::Result<Self> {
        let frame_limit = u16::try_from(MAX_FRAME_BYTES)
            .map_err(|_| io::Error::other("invalid frame limit"))?;
        let (sender, receiver) = mpsc::sync_channel(1);
        let cancel = Arc::new(AtomicBool::new(false));
        let requested = Arc::clone(&cancel);
        let thread = thread::Builder::new()
            .spawn(move || run(reader, sender, &requested, frame_limit))?;
        Ok(Self {
            receiver: Some(receiver),
            thread: Some(thread),
            joined: None,
            cancel,
        })
    }

    pub fn cancel(&mut self) {
        self.cancel.store(true, Ordering::Release);
    }

    fn frame(&self) -> Option<Vec<u8>> {
        self.receiver.as_ref()?.try_recv().ok()
    }

    pub fn poll(&mut self) -> ReaderEvent {
        if let Some(frame) = self.frame() {
            return ReaderEvent::Frame(frame);
        }
        if self
            .thread
            .as_ref()
            .is_some_and(|thread| thread.is_finished())
        {
            let thread = self.thread.take().unwrap();
            self.joined = Some(
                thread
                    .join()
                    .unwrap_or(ReaderEnd::Failed(Code::TerminationUnconfirmed)),
            );
        }
        // A frame can arrive between the first peek and thread completion.
        // Joining synchronizes that final send; drain it before reporting EOF.
        if let Some(frame) = self.frame() {
            return ReaderEvent::Frame(frame);
        }
        if let Some(end) = self.joined.take() {
            self.receiver = None;
            return ReaderEvent::Joined(end);
        }
        if self.thread.is_some() {
            ReaderEvent::Pending
        } else {
            ReaderEvent::Closed
        }
    }

    /// Waits for one frame/join event within a caller budget. Pending retains
    /// the handle; the deadline does not cancel Read or certify reader completion.
    pub fn poll_until(&mut self, deadline: Instant) -> ReaderEvent {
        loop {
            let event = self.poll();
            if event != ReaderEvent::Pending || Instant::now() >= deadline {
                return event;
            }
            let remaining = deadline.saturating_duration_since(Instant::now());
            thread::sleep(POLL_INTERVAL.min(remaining));
        }
    }
}

impl Drop for ReaderTask {
    fn drop(&mut self) {
        self.cancel();
        self.receiver = None;
        if self
            .thread
            .as_ref()
            .is_some_and(|thread| thread.is_finished())
        {
            let _ = self.thread.take().unwrap().join();
        }
    }
}
