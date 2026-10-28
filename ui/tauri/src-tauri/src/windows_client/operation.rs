use super::{Calls, Fault, Kind};
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};
use windows_sys::Win32::Foundation::{
    ERROR_BROKEN_PIPE, ERROR_IO_INCOMPLETE, ERROR_IO_PENDING, ERROR_MORE_DATA, ERROR_NOT_FOUND,
    HANDLE, WAIT_OBJECT_0, WAIT_TIMEOUT,
};
use windows_sys::Win32::System::IO::OVERLAPPED;

struct Work {
    op: OVERLAPPED,
    data: Vec<u8>,
}

// Retain both allocations even if a fake fatal unwinds. Native fatal aborts
// without unwinding, so pending OS references are never freed or reused.
fn unresolved(api: &impl Calls, work: Box<Work>) -> ! {
    let _ = Box::leak(work);
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
        op: OVERLAPPED {
            hEvent: event,
            ..unsafe { std::mem::zeroed() }
        },
        data: data.to_vec(),
    });
    let started = {
        let Work { op, data } = work.as_mut();
        // Box and Vec addresses remain stable until completed result, or leak
        // immediately before non-returning fatal. Native API does not unwind.
        unsafe { api.start(pipe, kind, data, op) }
    };
    let mut cancelled = false;
    if started == Err(ERROR_IO_PENDING) {
        let mut cleanup_deadline = None;
        loop {
            if !cancelled && (cancel.load(Ordering::Acquire) || api.now() >= deadline) {
                cancelled = true;
                cleanup_deadline = Some(api.now() + Duration::from_secs(1));
                match api.cancel(pipe, &mut work.op) {
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
        return Err(failure(kind, error));
    }
    let result = api.result(pipe, &mut work.op);
    if result == Err(ERROR_IO_INCOMPLETE) {
        unresolved(api, work);
    }
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
