use super::{observations::CloseObservations, Conversation, Request};
use crate::control_codec::scalar::Code;
use crate::owned_process::owner::{ChildOwner, ExitObservation, KillAttempt};
use std::time::Instant;

#[derive(Clone, Copy, PartialEq, Eq)]
pub(crate) enum ReaderCompletionReport {
    Eof,
    Failed,
}

/// Retains an already owned Child. No launcher, path, args, PID lookup or
/// authentication is provided. The source of the Child/context remains unwired.
pub(crate) struct OwnedCoordinator {
    conversation: Conversation,
    child: ChildOwner,
    exit: Option<ExitObservation>,
    reader: Option<ReaderCompletionReport>,
}

impl OwnedCoordinator {
    pub fn new(conversation: Conversation, child: ChildOwner) -> Self {
        Self {
            conversation,
            child,
            exit: None,
            reader: None,
        }
    }

    pub fn hello(&mut self) -> Result<Vec<u8>, Code> {
        self.conversation.hello()
    }

    pub fn request(&mut self, request: Request) -> Result<Vec<u8>, Code> {
        self.conversation.request(request)
    }

    pub fn receive(&mut self, frame: &[u8]) -> Result<(), Code> {
        self.conversation.receive(frame)
    }

    pub fn transport_failed(&mut self) -> Result<(), Code> {
        self.conversation.transport_failed()
    }

    /// This is a local worker's report. This module does not create/join that
    /// worker or authenticate its pipe, and never promotes the report to proof.
    pub fn reader_completed(&mut self, report: ReaderCompletionReport) -> Result<(), Code> {
        if self.reader.is_some() {
            self.reader = Some(ReaderCompletionReport::Failed);
            return self.conversation.transport_failed();
        }
        self.reader = Some(report);
        match report {
            ReaderCompletionReport::Eof => {
                self.conversation.end_input();
                Ok(())
            }
            ReaderCompletionReport::Failed => self.conversation.transport_failed(),
        }
    }

    /// Polls the retained direct Child once. No descendant or OS isolation
    /// guarantee, process execution or force cleanup is added here.
    pub fn poll_child(&mut self) -> Result<(), Code> {
        match self.child.wait_until(Instant::now()) {
            Ok(exit) => {
                self.exit = Some(exit);
                Ok(())
            }
            Err(_) => {
                self.exit = None;
                Err(Code::TerminationUnconfirmed)
            }
        }
    }

    pub fn observations(&self) -> CloseObservations {
        let snapshot = self.conversation.peer_snapshot_claim();
        let status = self.exit.as_ref().and_then(|exit| exit.status);
        CloseObservations {
            peer_close_claim: snapshot.is_some_and(|value| value.go_close_confirmed),
            peer_state: snapshot.map(|value| value.go_state),
            stop_receipt: self.conversation.peer_stop_receipt(),
            context_matched: self.conversation.context_matched(),
            channel_failed: self.conversation.channel_fault().is_some(),
            reader_eof_completed_reported: self.reader == Some(ReaderCompletionReport::Eof),
            direct_child_reaped: status.is_some(),
            direct_child_exit0: status.is_some_and(|value| value.success()),
            cleanup_attempted: self.child.cleanup_was_attempted(),
            kill: self
                .exit
                .as_ref()
                .map_or(KillAttempt::None, |exit| exit.kill),
        }
    }

    /// Ownership, including an unconfirmed Child, is explicitly transferable.
    /// Dropping the owner still has only its existing bounded best-effort cleanup.
    pub fn into_parts(self) -> (Conversation, ChildOwner) {
        (self.conversation, self.child)
    }
}
