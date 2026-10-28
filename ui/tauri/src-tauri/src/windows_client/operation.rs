use super::{Calls, Fault, Kind};
use std::sync::atomic::AtomicBool;
use std::time::Instant;
use windows_sys::Win32::Foundation::HANDLE;

pub(super) fn transfer(
    _api: &impl Calls,
    _pipe: HANDLE,
    _kind: Kind,
    data: &mut [u8],
    _cancel: &AtomicBool,
    _deadline: Instant,
) -> Result<usize, Fault> {
    Ok(data.len())
}
