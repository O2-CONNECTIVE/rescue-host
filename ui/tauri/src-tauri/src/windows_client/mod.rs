#[cfg(test)]
mod cancel_tests;
mod native;
mod operation;
mod peer;
mod peer_native;
#[cfg(test)]
mod peer_tests;
#[cfg(test)]
mod read_tests;
#[cfg(test)]
mod tests;

use std::sync::atomic::AtomicBool;
use std::time::Instant;
use windows_sys::Win32::Foundation::HANDLE;
use windows_sys::Win32::System::IO::OVERLAPPED;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Fault {
    InvalidMessage,
    VersionMismatch,
    Closed,
    Cancelled,
    Failed,
}

impl Fault {
    fn from_code(code: crate::control_codec::scalar::Code) -> Self {
        match code {
            crate::control_codec::scalar::Code::VersionMismatch => Self::VersionMismatch,
            _ => Self::InvalidMessage,
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Kind {
    Read,
    Write,
}

// Private API seam only. Product code uses Native; tests never open OS objects.
trait Calls {
    fn event(&self) -> Result<HANDLE, Fault>;
    /// Safety: data/op remain at these addresses until actual completion, or
    /// the process aborts. No caller may access data while IO owns it.
    unsafe fn start(
        &self,
        pipe: HANDLE,
        kind: Kind,
        data: &mut [u8],
        op: &mut OVERLAPPED,
    ) -> Result<(), u32>;
    fn wait(&self, event: HANDLE, millis: u32) -> u32;
    fn result(&self, pipe: HANDLE, op: &mut OVERLAPPED) -> Result<usize, u32>;
    fn cancel(&self, pipe: HANDLE, op: &mut OVERLAPPED) -> Result<(), u32>;
    fn close(&self, handle: HANDLE);
    fn now(&self) -> Instant;
    fn fatal(&self) -> !;
}

fn send(
    api: &impl Calls,
    pipe: HANDLE,
    frame: &mut [u8],
    cancel: &AtomicBool,
    deadline: Instant,
) -> Result<(), Fault> {
    if frame.is_empty() || frame.len() > crate::control_codec::MAX_FRAME_BYTES {
        return Err(Fault::InvalidMessage);
    }
    let count = operation::transfer(api, pipe, Kind::Write, frame, cancel, deadline)?;
    if count != frame.len() {
        return Err(Fault::Failed);
    }
    Ok(())
}

fn receive(
    api: &impl Calls,
    pipe: HANDLE,
    cancel: &AtomicBool,
    deadline: Instant,
) -> Result<Option<Vec<u8>>, Fault> {
    let mut frame = vec![0; crate::control_codec::MAX_FRAME_BYTES];
    match operation::transfer(api, pipe, Kind::Read, &mut frame, cancel, deadline) {
        Ok(count) => {
            frame.truncate(count);
            crate::control_codec::decode_frame(&frame).map_err(Fault::from_code)?;
            Ok(Some(frame))
        }
        Err(Fault::Closed) => Ok(None),
        Err(error) => Err(error),
    }
}
