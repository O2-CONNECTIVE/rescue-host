use super::Fault;
use std::sync::Arc;
use windows_sys::Win32::Foundation::{HANDLE, INVALID_HANDLE_VALUE};
use windows_sys::Win32::System::Threading::{
    PROCESS_QUERY_LIMITED_INFORMATION, PROCESS_SYNCHRONIZE,
};

/// Trusted bootstrap data, never values learned from the pipe or UI.
pub(crate) struct ExpectedServer {
    pub(crate) pid: u32,
    pub(crate) created: u64,
    pub(crate) service_sid: String,
}

impl ExpectedServer {
    pub(super) fn validate(&self) -> Result<(), Fault> {
        if self.pid == 0 || self.created == 0 || !canonical_service_sid(&self.service_sid) {
            return Err(Fault::Failed);
        }
        Ok(())
    }
}

// Same canonical TokenUser subset as docs/windows-local-control.md. Membership
// in a service group and broad/well-known identities are never substitutes.
pub(super) fn canonical_service_sid(value: &str) -> bool {
    if value.len() > 128 {
        return false;
    }
    let (suffix, required) = if let Some(suffix) = value.strip_prefix("S-1-5-21-") {
        (suffix, 4)
    } else if let Some(suffix) = value.strip_prefix("S-1-5-80-") {
        (suffix, 5)
    } else {
        return false;
    };
    let mut parts = suffix.split('-');
    for _ in 0..required {
        let Some(part) = parts.next() else {
            return false;
        };
        if part.is_empty()
            || part.len() > 10
            || !part.bytes().all(|byte| byte.is_ascii_digit())
            || part.parse::<u32>().is_err()
            || (part.len() > 1 && part.starts_with('0'))
        {
            return false;
        }
    }
    parts.next().is_none()
}

// Private kernel-query seam; tests supply data without opening OS objects.
pub(super) trait PeerCalls {
    fn pipe_server_pid(&self, pipe: HANDLE) -> Result<u32, Fault>;
    fn open_process(&self, pid: u32, access: u32, inherit: bool) -> Result<HANDLE, Fault>;
    fn process_id(&self, process: HANDLE) -> Result<u32, Fault>;
    fn created(&self, process: HANDLE) -> Result<u64, Fault>;
    fn token_user(&self, process: HANDLE) -> Result<String, Fault>;
    fn alive(&self, process: HANDLE) -> Result<bool, Fault>;
    fn close(&self, handle: HANDLE);
    fn fatal(&self) -> !;
}

/// Retains a query-only process handle. This is not SCM/artifact/grant authority.
pub(super) struct BoundPeer<A: PeerCalls> {
    api: Arc<A>,
    process: HANDLE,
    expected: ExpectedServer,
}

pub(super) fn bind<A: PeerCalls>(
    api: Arc<A>,
    pipe: HANDLE,
    expected: ExpectedServer,
) -> Result<BoundPeer<A>, Fault> {
    expected.validate()?;
    if pipe.is_null()
        || pipe == INVALID_HANDLE_VALUE
        || api.pipe_server_pid(pipe)? != expected.pid
    {
        return Err(Fault::Failed);
    }
    let process = api.open_process(
        expected.pid,
        PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_SYNCHRONIZE,
        false,
    )?;
    if process.is_null() || process == INVALID_HANDLE_VALUE {
        return Err(Fault::Failed);
    }
    // Construct the guard before any fallible identity query. Every mismatch or
    // query failure then closes this exact acquired process handle once.
    let peer = BoundPeer {
        api,
        process,
        expected,
    };
    peer.check(pipe)?;
    Ok(peer)
}

impl<A: PeerCalls> BoundPeer<A> {
    pub fn check(&self, pipe: HANDLE) -> Result<(), Fault> {
        if pipe.is_null()
            || pipe == INVALID_HANDLE_VALUE
            || self.api.pipe_server_pid(pipe)? != self.expected.pid
            || !self.api.alive(self.process)?
            || self.api.process_id(self.process)? != self.expected.pid
            || self.api.created(self.process)? != self.expected.created
            || self.api.token_user(self.process)? != self.expected.service_sid
        {
            return Err(Fault::Failed);
        }
        // The retained handle identifies the same process even across a PID
        // lookup race. Re-query the pipe and liveness after the TokenUser query.
        if self.api.pipe_server_pid(pipe)? != self.expected.pid || !self.api.alive(self.process)? {
            return Err(Fault::Failed);
        }
        Ok(())
    }
}

impl<A: PeerCalls> Drop for BoundPeer<A> {
    fn drop(&mut self) {
        self.api.close(self.process);
    }
}
