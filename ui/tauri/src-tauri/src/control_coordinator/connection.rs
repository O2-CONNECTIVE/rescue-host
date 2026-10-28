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

    pub fn hello(&mut self) -> Result<Vec<u8>, Code> {
        self.target.hello()
    }

    pub fn request(&mut self, request: Request) -> Result<Vec<u8>, Code> {
        self.target.request(request)
    }

    pub fn poll(&mut self) -> ReaderProgress {
        match self.reader.poll() {
            ReaderEvent::Frame(_) => ReaderProgress::Frame,
            ReaderEvent::Pending => ReaderProgress::Pending,
            ReaderEvent::Joined(end) => {
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
