use super::native::Native;
use super::peer::{bind, BoundPeer, ExpectedServer, PeerCalls};
use super::{receive, send, Calls, Fault};
use crate::control_codec::{decode_frame, model::Bootstrap, model::Message, scalar::Code};
use std::io::{self, Cursor, Read};
use std::ptr::{null, null_mut};
use std::sync::atomic::{AtomicBool, AtomicU8, Ordering};
use std::sync::Arc;
use std::time::Instant;
use windows_sys::Win32::Foundation::{GetLastError, ERROR_NO_TOKEN, HANDLE, INVALID_HANDLE_VALUE};
use windows_sys::Win32::Security::TOKEN_QUERY;
use windows_sys::Win32::Storage::FileSystem::{
    CreateFileW, FILE_FLAG_OVERLAPPED, OPEN_EXISTING, SECURITY_IDENTIFICATION,
    SECURITY_SQOS_PRESENT,
};
use windows_sys::Win32::System::Pipes::{
    GetNamedPipeInfo, SetNamedPipeHandleState, PIPE_READMODE_MESSAGE, PIPE_SERVER_END,
    PIPE_TYPE_MESSAGE,
};
use windows_sys::Win32::System::Threading::{GetCurrentThread, OpenThreadToken};

struct Owner<A: Calls + PeerCalls> {
    api: Arc<A>,
    pipe: HANDLE,
    peer: BoundPeer<A>,
    cancel: Arc<AtomicBool>,
    fault: AtomicU8,
}

// Only the Native owner is shared. Windows handles have no thread affinity;
// the unique reader and writer own separate operations and stable buffers.
// No duplicate handle, process mutation or arbitrary handle attach exists.
unsafe impl Send for Owner<Native> {}
unsafe impl Sync for Owner<Native> {}

impl<A: Calls + PeerCalls> Owner<A> {
    fn fail(&self, fault: Fault) -> Fault {
        let value = match fault {
            Fault::InvalidMessage => 1,
            Fault::VersionMismatch => 2,
            _ => 3,
        };
        let _ = self
            .fault
            .compare_exchange(0, value, Ordering::AcqRel, Ordering::Acquire);
        self.cancel.store(true, Ordering::Release);
        fault
    }

    fn check(&self) -> Result<(), Fault> {
        if self.cancel.load(Ordering::Acquire) {
            return Err(Fault::Cancelled);
        }
        self.peer.check(self.pipe).map_err(|error| self.fail(error))
    }
}

impl<A: Calls + PeerCalls> Drop for Owner<A> {
    fn drop(&mut self) {
        // The last half/worker owns this drop, after all its IO has completed.
        <A as Calls>::close(self.api.as_ref(), self.pipe);
    }
}

pub(super) struct MessageReader<A: Calls + PeerCalls> {
    owner: Arc<Owner<A>>,
    frame: Cursor<Vec<u8>>,
    deadline: Instant,
    ended: bool,
}

pub(super) struct MessageWriter<A: Calls + PeerCalls> {
    owner: Arc<Owner<A>>,
    deadline: Instant,
}

pub(super) type NativeReader = MessageReader<Native>;
pub(super) type NativeWriter = MessageWriter<Native>;

fn io_fault(error: Fault) -> io::Error {
    let kind = match error {
        Fault::InvalidMessage | Fault::VersionMismatch => io::ErrorKind::InvalidData,
        _ => io::ErrorKind::Other,
    };
    // Do not use Interrupted: read_until may retry it without observing cancel.
    io::Error::new(kind, "CONTROL_TRANSPORT_FAILED")
}

impl<A: Calls + PeerCalls> Read for MessageReader<A> {
    fn read(&mut self, output: &mut [u8]) -> io::Result<usize> {
        if output.is_empty() || self.ended {
            return Ok(0);
        }
        self.owner.check().map_err(io_fault)?;
        if self.frame.position() == self.frame.get_ref().len() as u64 {
            match receive(
                self.owner.api.as_ref(),
                self.owner.pipe,
                &self.owner.cancel,
                self.deadline,
            ) {
                Ok(Some(frame)) => {
                    self.owner.check().map_err(io_fault)?;
                    self.frame = Cursor::new(frame);
                }
                Ok(None) => {
                    self.ended = true;
                    return Ok(0);
                }
                Err(error) => return Err(io_fault(self.owner.fail(error))),
            }
        }
        self.frame.read(output)
    }
}

impl<A: Calls + PeerCalls> MessageWriter<A> {
    pub fn send_frame(&mut self, frame: &[u8], deadline: Instant) -> Result<(), Fault> {
        self.owner.check()?;
        let message =
            decode_frame(frame).map_err(|code| self.owner.fail(Fault::from_code(code)))?;
        if !matches!(
            message,
            Message::Hello(_) | Message::Snapshot(_) | Message::Stop(_)
        ) {
            return Err(self.owner.fail(Fault::InvalidMessage));
        }
        // Exactly one WriteFile; no std::io::Write/WriteAll fragmentation API.
        send(
            self.owner.api.as_ref(),
            self.owner.pipe,
            &mut frame.to_vec(),
            &self.owner.cancel,
            deadline.min(self.deadline),
        )
        .map_err(|error| self.owner.fail(error))?;
        self.owner.check()
    }

    pub fn cancel(&self) {
        self.owner.cancel.store(true, Ordering::Release);
    }

    pub fn cancel_token(&self) -> Arc<AtomicBool> {
        Arc::clone(&self.owner.cancel)
    }

    pub fn fault(&self) -> Option<Code> {
        match self.owner.fault.load(Ordering::Acquire) {
            0 => None,
            1 => Some(Code::InvalidMessage),
            2 => Some(Code::VersionMismatch),
            _ => Some(Code::TerminationUnconfirmed),
        }
    }
}

fn clean_thread() -> Result<(), Fault> {
    let mut token = null_mut();
    let opened = unsafe { OpenThreadToken(GetCurrentThread(), TOKEN_QUERY, 1, &mut token) };
    if opened != 0 {
        <Native as Calls>::close(&Native, token);
        return Err(Fault::Failed);
    }
    if unsafe { GetLastError() } != ERROR_NO_TOKEN {
        return Err(Fault::Failed);
    }
    Ok(())
}

pub(super) fn open(
    context: &Bootstrap,
    expected: ExpectedServer,
    deadline: Instant,
) -> Result<(NativeReader, NativeWriter), Fault> {
    if Instant::now() >= deadline {
        return Err(Fault::Cancelled);
    }
    expected.validate()?;
    clean_thread()?;
    let value = serde_json::to_value(&context.binding.0.channel_nonce)
        .map_err(|_| Fault::InvalidMessage)?;
    let nonce = value.as_str().ok_or(Fault::InvalidMessage)?;
    let name = format!(
        r"\\.\pipe\rescue-host-control-{}-{}",
        context.generation.get(),
        nonce
    );
    let name: Vec<u16> = name.encode_utf16().chain(Some(0)).collect();
    // Same individual rights as the server policy; no GENERIC_WRITE/instance
    // right, inheritance, alternate path, retry, credential or privilege change.
    let pipe = unsafe {
        CreateFileW(
            name.as_ptr(),
            0x00100183,
            0,
            null(),
            OPEN_EXISTING,
            FILE_FLAG_OVERLAPPED | SECURITY_SQOS_PRESENT | SECURITY_IDENTIFICATION,
            null_mut(),
        )
    };
    if pipe.is_null() || pipe == INVALID_HANDLE_VALUE {
        return Err(Fault::Failed);
    }
    let api = Arc::new(Native);
    let initialized = (|| {
        let (mut flags, mut instances) = (0, 0);
        let info =
            unsafe { GetNamedPipeInfo(pipe, &mut flags, null_mut(), null_mut(), &mut instances) };
        let mode = PIPE_READMODE_MESSAGE;
        if info == 0
            || flags & PIPE_SERVER_END != 0
            || flags & PIPE_TYPE_MESSAGE == 0
            || instances != 1
            || unsafe { SetNamedPipeHandleState(pipe, &mode, null(), null()) } == 0
        {
            return Err(Fault::Failed);
        }
        let peer = bind(Arc::clone(&api), pipe, expected)?;
        if Instant::now() >= deadline {
            return Err(Fault::Cancelled);
        }
        Ok(peer)
    })();
    let peer = match initialized {
        Ok(peer) => peer,
        Err(error) => {
            <Native as Calls>::close(&Native, pipe);
            return Err(error);
        }
    };
    Ok(halves(api, pipe, peer, deadline))
}

fn halves<A: Calls + PeerCalls>(
    api: Arc<A>,
    pipe: HANDLE,
    peer: BoundPeer<A>,
    deadline: Instant,
) -> (MessageReader<A>, MessageWriter<A>) {
    let owner = Arc::new(Owner {
        api,
        pipe,
        peer,
        cancel: Arc::new(AtomicBool::new(false)),
        fault: AtomicU8::new(0),
    });
    (
        MessageReader {
            owner: Arc::clone(&owner),
            frame: Cursor::new(Vec::new()),
            deadline,
            ended: false,
        },
        MessageWriter { owner, deadline },
    )
}

#[cfg(test)]
#[path = "endpoint_tests.rs"]
mod tests;
