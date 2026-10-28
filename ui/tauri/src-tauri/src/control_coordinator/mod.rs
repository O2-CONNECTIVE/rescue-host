//! Internal preparation only; context matching is not peer authentication.
use crate::control_codec::{
    decode_frame, encode_frame,
    model::{Bootstrap, Message, Snapshot},
    scalar::Code,
};

#[derive(Clone, Copy, PartialEq, Eq)]
enum Phase {
    Fresh,
    AwaitingAck,
    Bound,
    Closed(Code),
}

#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum Request {
    Snapshot,
    Stop,
}

pub(crate) struct Conversation {
    expected: Bootstrap,
    phase: Phase,
}

impl Conversation {
    /// The expected context comes from the owner, never from a received frame.
    /// This does not establish the currently unwired protected IPC authority.
    pub fn new(expected: Bootstrap) -> Self {
        Self {
            expected,
            phase: Phase::Fresh,
        }
    }

    fn close<T>(&mut self, code: Code) -> Result<T, Code> {
        self.phase = Phase::Closed(code);
        Err(code)
    }

    pub fn hello(&mut self) -> Result<Vec<u8>, Code> {
        if self.phase != Phase::Fresh {
            return self.close(Code::ContextMismatch);
        }
        let frame = encode_frame(&Message::Hello(self.expected.clone()))?;
        self.phase = Phase::AwaitingAck;
        Ok(frame)
    }

    pub fn receive(&mut self, frame: &[u8]) -> Result<(), Code> {
        if let Phase::Closed(code) = self.phase {
            return Err(code);
        }
        let message = match decode_frame(frame) {
            Ok(message) => message,
            Err(code) => return self.close(code),
        };
        match message {
            Message::HelloAck(value)
                if self.phase == Phase::AwaitingAck && value == self.expected =>
            {
                self.phase = Phase::Bound;
                Ok(())
            }
            _ => self.close(Code::ContextMismatch),
        }
    }

    pub fn request(&mut self, _request: Request) -> Result<Vec<u8>, Code> {
        Err(Code::RequestRejected)
    }

    pub fn peer_snapshot_claim(&self) -> Option<&Snapshot> {
        None
    }

    pub fn peer_stop_receipt(&self) -> bool {
        false
    }

    pub fn is_closed(&self) -> bool {
        matches!(self.phase, Phase::Closed(_))
    }
}
