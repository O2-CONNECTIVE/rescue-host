use super::native::Native;
use super::peer::{canonical_service_sid, BoundPeer, PeerCalls};
use super::{Calls, Fault};
use std::mem::size_of;
use std::ptr::null_mut;
use windows_sys::Win32::Foundation::{
    FILETIME, HANDLE, INVALID_HANDLE_VALUE, WAIT_OBJECT_0, WAIT_TIMEOUT,
};
use windows_sys::Win32::Security::{
    GetLengthSid, GetTokenInformation, IsValidSid, TokenUser, TOKEN_QUERY, TOKEN_USER,
};
use windows_sys::Win32::System::Pipes::GetNamedPipeServerProcessId;
use windows_sys::Win32::System::Threading::{
    GetProcessId, GetProcessTimes, OpenProcess, OpenProcessToken, WaitForSingleObject,
    PROCESS_QUERY_LIMITED_INFORMATION, PROCESS_SYNCHRONIZE,
};

// Windows query handles have no thread affinity. Only Native receives these
// impls: the handle is retained until the final owner drops, and all operations
// are read-only. The eventual shared PipeOwner must keep the pipe alive too.
unsafe impl Send for BoundPeer<Native> {}
unsafe impl Sync for BoundPeer<Native> {}

fn valid_handle(handle: HANDLE) -> bool {
    !handle.is_null() && handle != INVALID_HANDLE_VALUE
}

// A query-only temporary token is released on every success/failure path. The
// existing Native close policy aborts without unwinding if CloseHandle fails.
struct TokenHandle(HANDLE);

impl Drop for TokenHandle {
    fn drop(&mut self) {
        <Native as Calls>::close(&Native, self.0);
    }
}

fn token_user(process: HANDLE) -> Result<String, Fault> {
    if !valid_handle(process) {
        return Err(Fault::Failed);
    }
    let mut token = null_mut();
    if unsafe { OpenProcessToken(process, TOKEN_QUERY, &mut token) } == 0 {
        return Err(Fault::Failed);
    }
    if !valid_handle(token) {
        return Err(Fault::Failed);
    }
    let token = TokenHandle(token);
    // TOKEN_USER needs pointer alignment. A fixed aligned buffer also bounds
    // all allocation before querying the kernel; no untrusted required-size loop.
    const CAPACITY: usize = 1024;
    let mut storage = [0usize; CAPACITY / size_of::<usize>()];
    let mut returned = 0u32;
    let ok = unsafe {
        GetTokenInformation(
            token.0,
            TokenUser,
            storage.as_mut_ptr().cast(),
            CAPACITY as u32,
            &mut returned,
        )
    };
    let length = returned as usize;
    if ok == 0 || length < size_of::<TOKEN_USER>() || length > CAPACITY {
        return Err(Fault::Failed);
    }
    // The buffer is aligned and large enough for this fixed header. Never query
    // TokenGroups or use membership to replace the primary TokenUser identity.
    let user = unsafe { std::ptr::read(storage.as_ptr().cast::<TOKEN_USER>()) };
    let offset = (user.User.Sid as usize)
        .checked_sub(storage.as_ptr() as usize)
        .ok_or(Fault::Failed)?;
    if offset < size_of::<TOKEN_USER>() || !offset.is_multiple_of(4) {
        return Err(Fault::Failed);
    }
    let bytes = unsafe { std::slice::from_raw_parts(storage.as_ptr().cast::<u8>(), length) };
    let header_end = offset.checked_add(8).ok_or(Fault::Failed)?;
    let header = bytes.get(offset..header_end).ok_or(Fault::Failed)?;
    let count = usize::from(header[1]);
    if header[0] != 1 || !matches!(count, 5 | 6) || header[2..8] != [0, 0, 0, 0, 0, 5] {
        return Err(Fault::Failed);
    }
    let sid_length = 8 + count * 4;
    let sid_end = offset.checked_add(sid_length).ok_or(Fault::Failed)?;
    let sid = bytes.get(offset..sid_end).ok_or(Fault::Failed)?;
    // Both API calls now receive a complete, bounded SID, not a pointer whose
    // range was assumed valid merely because it appeared in TOKEN_USER.
    if unsafe { IsValidSid(user.User.Sid) } == 0
        || unsafe { GetLengthSid(user.User.Sid) } != sid_length as u32
    {
        return Err(Fault::Failed);
    }
    let mut text = String::with_capacity(80);
    text.push_str("S-1-5");
    for part in sid[8..].chunks_exact(4) {
        let value = u32::from_le_bytes([part[0], part[1], part[2], part[3]]);
        text.push('-');
        text.push_str(&value.to_string());
    }
    if !canonical_service_sid(&text) {
        return Err(Fault::Failed);
    }
    Ok(text)
}

impl PeerCalls for Native {
    fn pipe_server_pid(&self, pipe: HANDLE) -> Result<u32, Fault> {
        if !valid_handle(pipe) {
            return Err(Fault::Failed);
        }
        let mut pid = 0;
        if unsafe { GetNamedPipeServerProcessId(pipe, &mut pid) } == 0 || pid == 0 {
            return Err(Fault::Failed);
        }
        Ok(pid)
    }
    fn open_process(&self, pid: u32, access: u32, inherit: bool) -> Result<HANDLE, Fault> {
        let rights = PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_SYNCHRONIZE;
        if pid == 0 || access != rights || inherit {
            return Err(Fault::Failed);
        }
        let process = unsafe { OpenProcess(access, 0, pid) };
        if !valid_handle(process) {
            return Err(Fault::Failed);
        }
        Ok(process)
    }
    fn process_id(&self, process: HANDLE) -> Result<u32, Fault> {
        if !valid_handle(process) {
            return Err(Fault::Failed);
        }
        let pid = unsafe { GetProcessId(process) };
        if pid == 0 {
            return Err(Fault::Failed);
        }
        Ok(pid)
    }
    fn created(&self, process: HANDLE) -> Result<u64, Fault> {
        if !valid_handle(process) {
            return Err(Fault::Failed);
        }
        let mut created = FILETIME::default();
        let mut exited = FILETIME::default();
        let mut kernel = FILETIME::default();
        let mut user = FILETIME::default();
        if unsafe { GetProcessTimes(process, &mut created, &mut exited, &mut kernel, &mut user) } == 0 {
            return Err(Fault::Failed);
        }
        Ok((u64::from(created.dwHighDateTime) << 32) | u64::from(created.dwLowDateTime))
    }
    fn token_user(&self, process: HANDLE) -> Result<String, Fault> {
        token_user(process)
    }
    fn alive(&self, process: HANDLE) -> Result<bool, Fault> {
        if !valid_handle(process) {
            return Err(Fault::Failed);
        }
        match unsafe { WaitForSingleObject(process, 0) } {
            WAIT_TIMEOUT => Ok(true),
            WAIT_OBJECT_0 => Ok(false),
            _ => Err(Fault::Failed),
        }
    }
    fn close(&self, handle: HANDLE) {
        <Self as Calls>::close(self, handle);
    }
    fn fatal(&self) -> ! {
        <Self as Calls>::fatal(self)
    }
}
