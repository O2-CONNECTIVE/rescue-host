use super::{Calls, Fault, Kind};
use std::ptr::{null, null_mut};
use std::time::Instant;
use windows_sys::Win32::Foundation::{CloseHandle, GetLastError, HANDLE};
use windows_sys::Win32::Storage::FileSystem::{ReadFile, WriteFile};
use windows_sys::Win32::System::Threading::{CreateEventW, WaitForSingleObject};
use windows_sys::Win32::System::IO::{CancelIoEx, GetOverlappedResult, OVERLAPPED};

pub(super) struct Native;

fn result(ok: i32) -> Result<(), u32> {
    if ok == 0 {
        Err(unsafe { GetLastError() })
    } else {
        Ok(())
    }
}

impl Calls for Native {
    fn event(&self) -> Result<HANDLE, Fault> {
        // Fresh unnamed manual-reset event, no inherited handle.
        let handle = unsafe { CreateEventW(null(), 1, 0, null()) };
        if handle.is_null() {
            Err(Fault::Failed)
        } else {
            Ok(handle)
        }
    }

    unsafe fn start(
        &self,
        pipe: HANDLE,
        kind: Kind,
        data: *mut u8,
        length: usize,
        op: *mut OVERLAPPED,
    ) -> Result<(), u32> {
        let length = u32::try_from(length).expect("bounded frame length");
        // Caller retains both addresses until completion. Count=NULL for async IO;
        // only GetOverlappedResult may supply the completed transfer count.
        let ok = unsafe {
            match kind {
                Kind::Read => ReadFile(pipe, data, length, null_mut(), op),
                Kind::Write => WriteFile(pipe, data, length, null_mut(), op),
            }
        };
        result(ok)
    }

    fn wait(&self, event: HANDLE, millis: u32) -> u32 {
        unsafe { WaitForSingleObject(event, millis) }
    }

    unsafe fn result(&self, pipe: HANDLE, op: *const OVERLAPPED) -> Result<usize, u32> {
        let mut count = 0;
        result(unsafe { GetOverlappedResult(pipe, op, &mut count, 0) })?;
        Ok(count as usize)
    }

    unsafe fn cancel(&self, pipe: HANDLE, op: *const OVERLAPPED) -> Result<(), u32> {
        // Never NULL: cancel only this owner's operation, not unrelated IO.
        result(unsafe { CancelIoEx(pipe, op) })
    }

    fn close(&self, handle: HANDLE) {
        if unsafe { CloseHandle(handle) } == 0 {
            self.fatal();
        }
    }

    fn now(&self) -> Instant {
        Instant::now()
    }

    fn fatal(&self) -> ! {
        // Pending kernel references must never outlive freed Rust resources.
        // No unwind, raw error text, reconnect or fallback path.
        std::process::abort()
    }
}
