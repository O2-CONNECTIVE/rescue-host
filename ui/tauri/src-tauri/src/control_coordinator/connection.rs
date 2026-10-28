use super::{
    owned::{OwnedCoordinator, ReaderCompletionReport},
    reader::{ReaderEnd, ReaderEvent, ReaderTask},
    Conversation, Request,
};
use crate::control_codec::scalar::Code;

pub(crate) trait ReaderTarget {
    fn hello(&mut self) -> Result<Vec<u8>, Code>;
    fn request(&mut self, request: Request) -> Result<Vec<u8>, Code>;
    fn receive(&mut self, frame: &[u8]) -> Result<(), Code>;
    fn abort(&mut self) -> Result<(), Code>;
    fn complete(&mut self, report: ReaderCompletionReport) -> Result<(), Code>;
}

impl ReaderTarget for Conversation {
    fn hello(&mut self) -> Result<Vec<u8>, Code> {
        Conversation::hello(self)
    }
    fn request(&mut self, request: Request) -> Result<Vec<u8>, Code> {
        Conversation::request(self, request)
    }
    fn receive(&mut self, frame: &[u8]) -> Result<(), Code> {
        Conversation::receive(self, frame)
    }
    fn abort(&mut self) -> Result<(), Code> {
        self.transport_failed()
    }
    fn complete(&mut self, report: ReaderCompletionReport) -> Result<(), Code> {
        match report {
            ReaderCompletionReport::Eof => {
                self.end_input();
                Ok(())
            }
            ReaderCompletionReport::Failed => self.transport_failed(),
        }
    }
}

impl ReaderTarget for OwnedCoordinator {
    fn hello(&mut self) -> Result<Vec<u8>, Code> {
        OwnedCoordinator::hello(self)
    }
    fn request(&mut self, request: Request) -> Result<Vec<u8>, Code> {
        OwnedCoordinator::request(self, request)
    }
    fn receive(&mut self, frame: &[u8]) -> Result<(), Code> {
        OwnedCoordinator::receive(self, frame)
    }
    fn abort(&mut self) -> Result<(), Code> {
        self.transport_failed()
    }
    fn complete(&mut self, report: ReaderCompletionReport) -> Result<(), Code> {
        self.reader_completed(report)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct ConnectionObservation {
    pub fault: Option<Code>,
    pub reader_join: Option<ReaderEnd>,
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum ReaderProgress {
    Frame,
    Discarded,
    Pending,
    Fault(Code),
    Joined { end: ReaderEnd, fault: Option<Code> },
    Closed,
}

/// Binds one reader to one target for its whole lifetime. No reconnect/resume.
#[must_use = "retain the reader and target until join or explicit ownership transfer"]
pub(crate) struct ReaderConnection<T: ReaderTarget> {
    target: T,
    reader: ReaderTask,
    observation: ConnectionObservation,
}

impl<T: ReaderTarget> ReaderConnection<T> {
    pub fn new(target: T, reader: ReaderTask) -> Self {
        Self {
            target,
            reader,
            observation: ConnectionObservation {
                fault: None,
                reader_join: None,
            },
        }
    }

    fn fail(&mut self, code: Code) {
        self.observation.fault.get_or_insert(code);
        // These run even when abort returns Err: cancellation cannot be skipped.
        self.reader.cancel();
        let _ = self.target.abort();
    }

    fn open(&self) -> Result<(), Code> {
        if let Some(code) = self.observation.fault {
            Err(code)
        } else if self.observation.reader_join.is_some() {
            Err(Code::TerminationUnconfirmed)
        } else {
            Ok(())
        }
    }

    pub fn hello(&mut self) -> Result<Vec<u8>, Code> {
        self.open()?;
        let result = self.target.hello();
        if let Err(code) = result {
            self.fail(code);
        }
        result
    }

    pub fn request(&mut self, request: Request) -> Result<Vec<u8>, Code> {
        self.open()?;
        let result = self.target.request(request);
        if let Err(code) = result {
            if code == Code::RequestRejected {
                return Err(code);
            }
            self.fail(code);
        }
        result
    }

    pub fn cancel(&mut self) {
        if self.observation.reader_join.is_none() {
            self.fail(Code::TerminationUnconfirmed);
        }
    }

    /// Processes at most one frame. Blocking Read is always in the worker;
    /// rejected/cancelled input is discarded without invoking the target again.
    pub fn poll(&mut self) -> ReaderProgress {
        match self.reader.poll() {
            ReaderEvent::Frame(frame) => {
                if self.observation.fault.is_some() {
                    return ReaderProgress::Discarded;
                }
                match self.target.receive(&frame) {
                    Ok(()) => ReaderProgress::Frame,
                    Err(code) => {
                        self.fail(code);
                        ReaderProgress::Fault(code)
                    }
                }
            }
            ReaderEvent::Pending => ReaderProgress::Pending,
            ReaderEvent::Joined(end) => {
                match end {
                    ReaderEnd::Eof => {}
                    ReaderEnd::Cancelled => self.fail(Code::TerminationUnconfirmed),
                    ReaderEnd::Failed(code) => self.fail(code),
                }
                let report = if end == ReaderEnd::Eof && self.observation.fault.is_none() {
                    ReaderCompletionReport::Eof
                } else {
                    ReaderCompletionReport::Failed
                };
                if let Err(code) = self.target.complete(report) {
                    self.fail(code);
                }
                self.observation.reader_join = Some(end);
                ReaderProgress::Joined {
                    end,
                    fault: self.observation.fault,
                }
            }
            ReaderEvent::Closed => ReaderProgress::Closed,
        }
    }

    pub fn target(&self) -> &T {
        &self.target
    }

    pub fn observation(&self) -> ConnectionObservation {
        self.observation
    }

    /// Live ownership transfer only; no persistence, reconnect or permission renewal.
    pub fn into_parts(self) -> (T, ReaderTask, ConnectionObservation) {
        (self.target, self.reader, self.observation)
    }
}
