use super::endpoint::{open, NativeWriter};
use super::native::Native;
use super::peer::ExpectedServer;
use super::{Calls, Fault};
use crate::control_codec::{model::Bootstrap, scalar::Code};
use crate::control_coordinator::connection::{ConnectionObservation, ReaderConnection};
use crate::control_coordinator::reader::ReaderTask;
use crate::control_coordinator::{Conversation, Request};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

/// Candidate internal adapter only. The caller must supply independent owner
/// bootstrap data; no UI command, environment unlock or Start entry exists.
/// A bound server process is not grant/SCM/artifact authority or Go Close proof.
pub(crate) struct NativeClient {
    connection: ReaderConnection<Conversation>,
    writer: NativeWriter,
}

impl NativeClient {
    pub fn connect(
        context: Bootstrap,
        expected: ExpectedServer,
        deadline: Instant,
    ) -> Result<Self, Code> {
        let (reader, mut writer) = open(&context, expected, deadline).map_err(Fault::code)?;
        let mut conversation = Conversation::new(context);
        let hello = conversation.hello()?;
        writer.send_frame(&hello, deadline).map_err(Fault::code)?;
        let reader = ReaderTask::start_with_cancel(reader, writer.cancel_token())
            .map_err(|_| Code::TerminationUnconfirmed)?;
        Ok(Self {
            connection: ReaderConnection::new(conversation, reader),
            writer,
        })
    }

    fn absorb_transport_fault(&mut self) {
        // Load cancel before the typed fault: when the release-store of cancel
        // is observed, its preceding first-fault store is visible to this load.
        let cancelled = self.writer.cancel_token().load(Ordering::Acquire);
        if cancelled {
            // External cancellation is recorded in the same latch as IO faults.
            // CAS order is authoritative, not wall-clock request arrival order.
            self.writer.cancel();
        }
        if let Some(code) = self.writer.fault() {
            self.connection.transport_failed(code);
        } else if cancelled {
            self.connection.cancel();
        }
    }

    pub fn poll(&mut self) -> ConnectionObservation {
        // Preserve the typed message-prevalidation fault before a shared cancel
        // causes the worker to report Cancelled. Queued frames are discarded.
        self.absorb_transport_fault();
        let writer = &self.writer;
        let _ = self.connection.poll_with_transport_fault(|_| writer.fault());
        self.absorb_transport_fault();
        self.connection.observation()
    }

    /// Historical wire context matching only, never an authorization verdict.
    pub fn peer_context_matched(&self) -> bool {
        self.connection.target().context_matched()
    }

    pub fn request(&mut self, request: Request, deadline: Instant) -> Result<(), Code> {
        self.absorb_transport_fault();
        let frame = self.connection.request(request)?;
        let sent = self.writer.send_frame(&frame, deadline);
        self.absorb_transport_fault();
        if let Err(fault) = sent {
            self.connection.transport_failed(fault.code());
            return Err(self.connection.observation().fault.unwrap_or(fault.code()));
        }
        Ok(()) // Delivery only: never a receipt, authorization or completion.
    }

    /// May be retained outside a caller mutex so cancellation never waits for
    /// read/write or a state lock. The token is irreversible and never reset.
    pub fn cancel_token(&self) -> Arc<AtomicBool> {
        self.writer.cancel_token()
    }

    /// Ok certifies reader join only. The observation retains its fault and raw
    /// end; it never certifies stop, Go Close, backend process reap or authority.
    /// Pending ownership stays in self if the polling budget is exhausted.
    pub fn cancel_and_join_until(
        &mut self,
        deadline: Instant,
    ) -> Result<ConnectionObservation, Code> {
        self.absorb_transport_fault();
        self.writer.cancel();
        self.absorb_transport_fault();
        self.connection.cancel_and_join_until(deadline)
    }
}

impl Drop for NativeClient {
    fn drop(&mut self) {
        // Covers the native operation's one-second cancellation-completion wait
        // plus reader polling. This is not a hard OS scheduling/API time bound.
        // Never detach the native reader and silently release its last owner.
        if self
            .cancel_and_join_until(Instant::now() + Duration::from_secs(2))
            .is_err()
        {
            <Native as Calls>::fatal(&Native);
        }
    }
}
