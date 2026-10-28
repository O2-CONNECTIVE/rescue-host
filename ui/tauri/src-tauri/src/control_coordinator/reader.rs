use std::io::{self, Read};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{mpsc, Arc};
use std::thread::{self, JoinHandle};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum ReaderEnd {
    Eof,
    Cancelled,
    Failed,
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum ReaderEvent {
    Frame(Vec<u8>),
    Pending,
    Joined(ReaderEnd),
    Closed,
}

/// Keep this owner until Joined. Cancelling does not interrupt blocking Read.
/// Runtime wiring remains closed until read cancellation and owner lifetime are
/// established. Drop may detach an unfinished blocking read, never proving EOF.
#[must_use = "retain the reader until an actual join or explicit unresolved ownership transfer"]
pub(crate) struct ReaderTask {
    receiver: Option<mpsc::Receiver<Vec<u8>>>,
    thread: Option<JoinHandle<ReaderEnd>>,
    cancel: Arc<AtomicBool>,
}

impl ReaderTask {
    pub fn start(reader: impl Read + Send + 'static) -> io::Result<Self> {
        let (sender, receiver) = mpsc::sync_channel(1);
        let cancel = Arc::new(AtomicBool::new(false));
        let thread = thread::Builder::new().spawn(move || {
            drop(reader);
            drop(sender);
            ReaderEnd::Cancelled
        })?;
        Ok(Self {
            receiver: Some(receiver),
            thread: Some(thread),
            cancel,
        })
    }

    pub fn cancel(&mut self) {
        self.cancel.store(true, Ordering::Release);
    }

    pub fn poll(&mut self) -> ReaderEvent {
        if let Some(receiver) = &self.receiver {
            if let Ok(frame) = receiver.try_recv() {
                return ReaderEvent::Frame(frame);
            }
        }
        match self.thread.as_ref() {
            Some(thread) if thread.is_finished() => {
                self.receiver = None;
                let thread = self.thread.take().unwrap();
                let end = thread.join().unwrap_or(ReaderEnd::Failed);
                ReaderEvent::Joined(end)
            }
            Some(_) => ReaderEvent::Pending,
            None => ReaderEvent::Closed,
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
