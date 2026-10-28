use crate::control_codec::scalar::GoState;
use crate::owned_process::owner::KillAttempt;

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum CloseAssessment {
    EvidenceIncomplete,
    AuthorityNotEstablished,
}

/// Reports and local facts are not authenticated Go Close evidence.
pub(crate) struct CloseObservations {
    pub peer_close_claim: bool,
    pub peer_state: Option<GoState>,
    pub stop_receipt: bool,
    pub context_matched: bool,
    pub channel_failed: bool,
    pub reader_eof_completed_reported: bool,
    pub direct_child_reaped: bool,
    pub direct_child_exit0: bool,
    pub cleanup_attempted: bool,
    pub kill: KillAttempt,
}

impl CloseObservations {
    pub fn assess(&self) -> CloseAssessment {
        CloseAssessment::EvidenceIncomplete
    }
}
