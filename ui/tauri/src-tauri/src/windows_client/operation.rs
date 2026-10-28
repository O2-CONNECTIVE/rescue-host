use super::{Calls, Fault, Kind};
use std::cell::UnsafeCell;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};
use windows_sys::Win32::Foundation::{
    ERROR_BROKEN_PIPE, ERROR_IO_PENDING, ERROR_MORE_DATA, ERROR_NOT_FOUND, ERROR_OPERATION_ABORTED,
    HANDLE, WAIT_OBJECT_0, WAIT_TIMEOUT,
};
use windows_sys::Win32::System::IO::OVERLAPPED;

struct Work {
    op: UnsafeCell<OVERLAPPED>,
    data: Vec<u8>,
}

// Raw ownership is deliberately not reconstructed before completion. No Rust
// reference is created while the kernel may update the OVERLAPPED. Native fatal
// aborts; a fake fatal may unwind without freeing either allocation.
fn unresolved(api: &impl Calls, _work: *mut Work) -> ! {
    api.fatal()
}

fn failure(kind: Kind, error: u32) -> Fault {
    match error {
        ERROR_MORE_DATA => Fault::InvalidMessage,
        ERROR_BROKEN_PIPE if kind == Kind::Read => Fault::Closed,
        _ => Fault::Failed,
    }
}

pub(super) fn transfer(
    api: &impl Calls,
    pipe: HANDLE,
    kind: Kind,
    data: &mut [u8],
    cancel: &AtomicBool,
    deadline: Instant,
) -> Result<usize, Fault> {
    if cancel.load(Ordering::Acquire) || api.now() >= deadline {
        return Err(Fault::Cancelled);
    }
    if data.is_empty() || data.len() > crate::control_codec::MAX_FRAME_BYTES {
        return Err(Fault::InvalidMessage);
    }
    let event = api.event()?;
    let mut work = Box::new(Work {
        op: UnsafeCell::new(OVERLAPPED {
            hEvent: event,
            ..unsafe { std::mem::zeroed() }
        }),
        data: data.to_vec(),
    });
    let op = work.op.get();
    let bytes = work.data.as_mut_ptr();
    // Transfer ownership before starting IO. Until a terminal result, only these
    // raw pointers are used: no &mut to Work/OVERLAPPED/data is regenerated.
    let work = Box::into_raw(work);
    let started = unsafe { api.start(pipe, kind, bytes, data.len(), op) };
    let mut cancelled = false;
    if started == Err(ERROR_IO_PENDING) {
        let mut cleanup_deadline = None;
        loop {
            if !cancelled && (cancel.load(Ordering::Acquire) || api.now() >= deadline) {
                cancelled = true;
                cleanup_deadline = Some(api.now() + Duration::from_secs(1));
                match unsafe { api.cancel(pipe, op) } {
                    Ok(()) | Err(ERROR_NOT_FOUND) => {}
                    Err(_) => unresolved(api, work),
                }
            }
            match api.wait(event, 10) {
                WAIT_OBJECT_0 => break,
                WAIT_TIMEOUT => {}
                _ => unresolved(api, work),
            }
            if cleanup_deadline.is_some_and(|limit| api.now() >= limit) {
                unresolved(api, work);
            }
        }
    } else if let Err(error) = started {
        // Anything except IO_PENDING is a completed start failure.
        api.close(event);
        // No pending IO exists on this synchronous start-error path.
        drop(unsafe { Box::from_raw(work) });
        return Err(failure(kind, error));
    }
    let result = unsafe { api.result(pipe, op) };
    match result {
        Ok(_) | Err(ERROR_OPERATION_ABORTED | ERROR_BROKEN_PIPE | ERROR_MORE_DATA) => {}
        // An incomplete/invalid query cannot establish that the kernel stopped
        // using our addresses, even if the event appeared signalled.
        Err(_) => unresolved(api, work),
    }
    // The kernel has stopped using both allocations; Rust ownership/references
    // may now be reconstructed.
    let work = unsafe { Box::from_raw(work) };
    api.close(event); // Only after a terminal completed result.
    if cancelled || cancel.load(Ordering::Acquire) || api.now() >= deadline {
        return Err(Fault::Cancelled);
    }
    let count = result.map_err(|error| failure(kind, error))?;
    if count > data.len() {
        return Err(Fault::Failed);
    }
    if kind == Kind::Read {
        data[..count].copy_from_slice(&work.data[..count]);
    }
    Ok(count)
}
