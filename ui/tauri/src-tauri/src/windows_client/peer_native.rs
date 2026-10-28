use super::native::Native;
use super::peer::{BoundPeer, PeerCalls};
use super::{Calls, Fault};
use windows_sys::Win32::Foundation::HANDLE;

// Windows query handles have no thread affinity. Only Native receives these
// impls: the handle is retained until the final owner drops, and all operations
// are read-only. The eventual shared PipeOwner must keep the pipe alive too.
unsafe impl Send for BoundPeer<Native> {}
unsafe impl Sync for BoundPeer<Native> {}

// RED checkpoint only: no native query is enabled by these placeholders.
impl PeerCalls for Native {
    fn pipe_server_pid(&self, _: HANDLE) -> Result<u32, Fault> {
        Err(Fault::Failed)
    }
    fn open_process(&self, _: u32, _: u32, _: bool) -> Result<HANDLE, Fault> {
        Err(Fault::Failed)
    }
    fn process_id(&self, _: HANDLE) -> Result<u32, Fault> {
        Err(Fault::Failed)
    }
    fn created(&self, _: HANDLE) -> Result<u64, Fault> {
        Err(Fault::Failed)
    }
    fn token_user(&self, _: HANDLE) -> Result<String, Fault> {
        Err(Fault::Failed)
    }
    fn alive(&self, _: HANDLE) -> Result<bool, Fault> {
        Err(Fault::Failed)
    }
    fn close(&self, handle: HANDLE) {
        <Self as Calls>::close(self, handle);
    }
    fn fatal(&self) -> ! {
        <Self as Calls>::fatal(self)
    }
}
