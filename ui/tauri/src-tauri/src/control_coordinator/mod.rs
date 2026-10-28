//! Internal preparation only; context matching is not peer authentication.
pub(crate) mod observations;
pub(crate) mod owned;
use crate::control_codec::{
    decode_frame, encode_frame,
    model::{Bootstrap, Control, Empty, Identity, Message, Object, Snapshot},
    scalar::{Code, Decimal, Operation, Protocol},
};

#[derive(Clone, Copy, PartialEq, Eq)]
enum Phase {
    Fresh,
    AwaitingAck,
    Bound,
    InputEnded,
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
    next_tx: Option<u64>,
    next_rx: Option<u64>,
    next_request: Option<u64>,
    snapshot_pending: Option<Decimal<true>>,
    stop_pending: Option<Decimal<true>>,
    stop_generated: bool,
    peer_snapshot: Option<Snapshot>,
    peer_stop_receipt: bool,
    peer_error: Option<Code>,
    context_matched: bool,
}

impl Conversation {
    /// The expected context comes from the owner, never from a received frame.
    /// This does not establish the currently unwired protected IPC authority.
    pub fn new(expected: Bootstrap) -> Self {
        Self {
            expected,
            phase: Phase::Fresh,
            next_tx: Some(1),
            next_rx: Some(1),
            next_request: Some(1),
            snapshot_pending: None,
            stop_pending: None,
            stop_generated: false,
            peer_snapshot: None,
            peer_stop_receipt: false,
            peer_error: None,
            context_matched: false,
        }
    }

    fn close<T>(&mut self, code: Code) -> Result<T, Code> {
        let code = match self.phase {
            Phase::Closed(previous) => previous,
            Phase::InputEnded => return Err(Code::TerminationUnconfirmed),
            _ => code,
        };
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

    fn check_envelope(
        &mut self,
        generation: Decimal<true>,
        identity: &Identity,
        sequence: Decimal<true>,
    ) -> Result<(), Code> {
        if self.phase != Phase::Bound
            || generation != self.expected.generation
            || identity != &self.expected.identity.0
            || Some(sequence.get()) != self.next_rx
        {
            return self.close(Code::ContextMismatch);
        }
        self.next_rx = sequence.get().checked_add(1);
        Ok(())
    }

    pub fn receive(&mut self, frame: &[u8]) -> Result<(), Code> {
        match self.phase {
            Phase::Closed(code) => return Err(code),
            Phase::InputEnded => return Err(Code::TerminationUnconfirmed),
            _ => {}
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
                self.context_matched = true;
            }
            Message::SnapshotResult(value) => {
                self.check_envelope(value.generation, &value.identity.0, value.sequence)?;
                if self.snapshot_pending != Some(value.request_id) {
                    return self.close(Code::ContextMismatch);
                }
                self.snapshot_pending = None;
                self.peer_snapshot = Some(value.payload.0);
            }
            Message::Receipt(value) => {
                self.check_envelope(value.generation, &value.identity.0, value.sequence)?;
                if self.stop_pending != Some(value.request_id)
                    || value.payload.0.operation != Operation::Stop
                {
                    return self.close(Code::ContextMismatch);
                }
                self.stop_pending = None;
                self.peer_stop_receipt = true;
            }
            Message::Error(value) => {
                self.check_envelope(value.generation, &value.identity.0, value.sequence)?;
                if self.snapshot_pending == Some(value.request_id) {
                    self.snapshot_pending = None;
                } else if self.stop_pending == Some(value.request_id) {
                    self.stop_pending = None;
                } else {
                    return self.close(Code::ContextMismatch);
                }
                // Even goCloseConfirmed on an error is never a Close proof.
                let code = value.payload.0.code;
                self.peer_error = Some(code);
                if matches!(
                    code,
                    Code::VersionMismatch | Code::InvalidMessage | Code::ContextMismatch
                ) {
                    return self.close(code);
                }
            }
            Message::StateChanged(value) => {
                self.check_envelope(value.generation, &value.identity.0, value.sequence)?;
                self.peer_snapshot = Some(value.payload.0);
            }
            _ => return self.close(Code::ContextMismatch),
        }
        Ok(())
    }

    /// Frames must be delivered in generation order. There is no send queue or
    /// automatic retry. Unknown/partial delivery requires transport_failed().
    /// Only one snapshot plus one reserved stop can be pending (no Start API).
    pub fn request(&mut self, request: Request) -> Result<Vec<u8>, Code> {
        if self.phase != Phase::Bound {
            return self.close(Code::ContextMismatch);
        }
        if (request == Request::Snapshot && self.snapshot_pending.is_some())
            || (request == Request::Stop && self.stop_generated)
        {
            return Err(Code::RequestRejected);
        }
        let (Some(sequence), Some(request_id)) = (
            self.next_tx.and_then(Decimal::new),
            self.next_request.and_then(Decimal::new),
        ) else {
            return self.close(Code::ContextMismatch);
        };
        let control = Control {
            protocol: Protocol::V1,
            generation: self.expected.generation,
            sequence,
            request_id,
            identity: self.expected.identity.clone(),
            payload: Object(Empty {}),
        };
        let message = match request {
            Request::Snapshot => Message::Snapshot(control),
            Request::Stop => Message::Stop(control),
        };
        let frame = match encode_frame(&message) {
            Ok(frame) => frame,
            Err(code) => return self.close(code),
        };
        self.next_tx = sequence.get().checked_add(1);
        self.next_request = request_id.get().checked_add(1);
        match request {
            Request::Snapshot => self.snapshot_pending = Some(request_id),
            Request::Stop => {
                self.stop_pending = Some(request_id);
                self.stop_generated = true;
            }
        }
        Ok(frame)
    }

    pub fn transport_failed(&mut self) -> Result<(), Code> {
        self.close(Code::TerminationUnconfirmed)
    }

    pub fn peer_snapshot_claim(&self) -> Option<&Snapshot> {
        self.peer_snapshot.as_ref()
    }

    pub fn peer_stop_receipt(&self) -> bool {
        self.peer_stop_receipt
    }

    pub fn peer_error_claim(&self) -> Option<Code> {
        self.peer_error
    }

    /// Seals the channel after the local reader's completion report, without
    /// turning that report into independently authenticated EOF/Close evidence.
    pub fn end_input(&mut self) {
        if !self.is_closed() {
            self.phase = Phase::InputEnded;
        }
    }

    pub fn context_matched(&self) -> bool {
        self.context_matched
    }

    pub fn channel_fault(&self) -> Option<Code> {
        match self.phase {
            Phase::Closed(code) => Some(code),
            _ => None,
        }
    }

    pub fn is_closed(&self) -> bool {
        matches!(self.phase, Phase::Closed(_) | Phase::InputEnded)
    }
}
