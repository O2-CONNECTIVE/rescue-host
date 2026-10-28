use super::Fault;
use std::sync::Arc;
use windows_sys::Win32::Foundation::HANDLE;
use windows_sys::Win32::System::Threading::{
    PROCESS_QUERY_LIMITED_INFORMATION, PROCESS_SYNCHRONIZE,
};

/// Trusted bootstrap data, never values learned from the pipe or UI.
pub(super) struct ExpectedServer {
    pub pid: u32,
    pub created: u64,
    pub service_sid: String,
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
    _pipe: HANDLE,
    expected: ExpectedServer,
) -> Result<BoundPeer<A>, Fault> {
    // RED checkpoint: identity comparisons deliberately not implemented yet.
    let process = api.open_process(
        expected.pid,
        PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_SYNCHRONIZE,
        false,
    )?;
    Ok(BoundPeer {
        api,
        process,
        expected,
    })
}

impl<A: PeerCalls> BoundPeer<A> {
    pub fn check(&self, _pipe: HANDLE) -> Result<(), Fault> {
        // RED checkpoint: pre/post-I/O identity checks follow the observed RED.
        Ok(())
    }
}

impl<A: PeerCalls> Drop for BoundPeer<A> {
    fn drop(&mut self) {
        self.api.close(self.process);
    }
}
