// SPDX-License-Identifier: MIT
// 試験専用。request taskは固定ringへのtry_lockだけで記録し、I/Oしない。
use hyper_util::client::legacy::connect::{CaptureConnection, capture_connection};
use std::{
    fs::OpenOptions,
    io::Write,
    path::PathBuf,
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, AtomicU8, AtomicU64, AtomicUsize, Ordering::SeqCst},
    },
    thread,
    time::{Duration, Instant},
};

#[derive(Clone, Copy)]
pub enum Kind {
    None,
    Incomplete,
    Canceled,
    Closed,
    Parse,
    Other,
}
impl Kind {
    fn name(self) -> &'static str {
        match self {
            Self::None => "none",
            Self::Incomplete => "incomplete",
            Self::Canceled => "canceled",
            Self::Closed => "closed",
            Self::Parse => "parse",
            Self::Other => "other",
        }
    }
}
pub fn classify(error: &(dyn std::error::Error + 'static)) -> Kind {
    if let Some(error) = error.downcast_ref::<hyper::Error>() {
        if error.is_incomplete_message() {
            Kind::Incomplete
        } else if error.is_canceled() {
            Kind::Canceled
        } else if error.is_closed() {
            Kind::Closed
        } else if error.is_parse() {
            Kind::Parse
        } else {
            Kind::Other
        }
    } else {
        Kind::Other
    }
}
#[derive(Clone, Copy)]
enum Stage {
    Open,
    Received,
    Forward,
    NativeAssigned,
    NativeUnobserved,
    Headers,
    NativeError,
    Dropped,
    End,
}
impl Stage {
    fn name(self) -> &'static str {
        match self {
            Self::Open => "connection-open",
            Self::Received => "session-received",
            Self::Forward => "forward-start",
            Self::NativeAssigned => "native-connection-assigned",
            Self::NativeUnobserved => "native-connection-unobserved",
            Self::Headers => "native-headers",
            Self::NativeError => "native-error",
            Self::Dropped => "request-dropped",
            Self::End => "connection-end",
        }
    }
}
fn state_name(state: u8) -> &'static str {
    match state {
        0 => "idle",
        1 => "received",
        2 => "forward-pending",
        3 => "headers-returned",
        4 => "native-error",
        5 => "dropped",
        _ => "unknown",
    }
}
#[derive(Clone, Copy)]
struct Event {
    seq: u64,
    connection: u64,
    request: u64,
    stage: Stage,
    state: u8,
    elapsed: u128,
    error: Kind,
}
pub struct Recorder {
    start: Instant,
    events: Mutex<Vec<Event>>,
    attempted: AtomicU64,
    dropped: AtomicU64,
    in_flight: AtomicUsize,
    enabled: AtomicBool,
    connections: AtomicU64,
    requests: AtomicU64,
    pending: AtomicU64,
}
struct Flight<'a>(&'a Recorder);
impl Drop for Flight<'_> {
    fn drop(&mut self) {
        self.0.in_flight.fetch_sub(1, SeqCst);
    }
}
impl Recorder {
    pub fn memory() -> Arc<Self> {
        Arc::new(Self {
            start: Instant::now(),
            events: Mutex::new(Vec::with_capacity(32)),
            attempted: AtomicU64::new(0),
            dropped: AtomicU64::new(0),
            in_flight: AtomicUsize::new(0),
            enabled: AtomicBool::new(true),
            connections: AtomicU64::new(0),
            requests: AtomicU64::new(0),
            pending: AtomicU64::new(0),
        })
    }
    fn event(&self, connection: u64, request: u64, stage: Stage, state: u8, error: Kind) {
        self.in_flight.fetch_add(1, SeqCst);
        let _flight = Flight(self);
        if !self.enabled.load(SeqCst) {
            return;
        }
        let seq = self.attempted.fetch_add(1, SeqCst) + 1;
        if let Ok(mut events) = self.events.try_lock() {
            if events.len() < 32 {
                events.push(Event {
                    seq,
                    connection,
                    request,
                    stage,
                    state,
                    elapsed: self.start.elapsed().as_micros(),
                    error,
                });
                return;
            }
        }
        self.dropped.fetch_add(1, SeqCst);
    }
    fn finish(&self, requested: bool) -> String {
        self.enabled.store(false, SeqCst);
        // writer側だけの有限待ち。request taskはflush/joinを待たない。
        for _ in 0..100 {
            if self.in_flight.load(SeqCst) == 0 {
                break;
            }
            thread::sleep(Duration::from_millis(1));
        }
        let mut events = self
            .events
            .try_lock()
            .map(|events| events.clone())
            .unwrap_or_default();
        events.sort_by_key(|event| event.seq);
        let mut text = String::from(
            "{\"type\":\"header\",\"schema\":1,\"variant\":\"tauri-driver-2.1.0-stage-2\"}\n",
        );
        let mut written = 0;
        for event in &events {
            let line = format!(
                "{{\"type\":\"event\",\"seq\":{},\"connection\":{},\"request\":{},\"stage\":\"{}\",\"state\":\"{}\",\"elapsedUs\":{},\"error\":\"{}\"}}\n",
                event.seq,
                event.connection,
                event.request,
                event.stage.name(),
                state_name(event.state),
                event.elapsed,
                event.error.name()
            );
            if line.len() <= 256 && text.len() + line.len() <= 7680 {
                text.push_str(&line);
                written += 1;
            }
        }
        let pending = self.pending.load(SeqCst);
        let sealed = requested && self.in_flight.load(SeqCst) == 0 && pending == 0;
        let attempted = self.attempted.load(SeqCst);
        text.push_str(&format!("{{\"type\":\"summary\",\"lastSequence\":{},\"attempted\":{},\"enqueued\":{},\"written\":{},\"dropped\":{},\"sealed\":{},\"pendingRequests\":{}}}\n",
            attempted, attempted, events.len(), written, self.dropped.load(SeqCst), sealed, pending));
        if text.len() > 8192 {
            String::new()
        } else {
            text
        }
    }
    #[cfg(test)]
    pub fn snapshot(&self) -> String {
        self.finish(true)
    }
}
#[derive(Clone)]
pub struct Connection {
    recorder: Option<Arc<Recorder>>,
    slot: u64,
    last: Arc<AtomicU64>,
    state: Arc<AtomicU8>,
}
impl Connection {
    pub fn open(recorder: Option<Arc<Recorder>>) -> Self {
        let slot = recorder
            .as_ref()
            .map(|r| r.connections.fetch_add(1, SeqCst) + 1)
            .unwrap_or(0);
        let this = Self {
            recorder,
            slot,
            last: Arc::new(AtomicU64::new(0)),
            state: Arc::new(AtomicU8::new(0)),
        };
        this.emit(0, Stage::Open, Kind::None);
        this
    }
    fn emit(&self, request: u64, stage: Stage, error: Kind) {
        if let Some(recorder) = &self.recorder {
            recorder.event(self.slot, request, stage, self.state.load(SeqCst), error);
        }
    }
    pub fn session(&self) -> Request {
        let slot = self
            .recorder
            .as_ref()
            .map(|r| {
                r.pending.fetch_add(1, SeqCst);
                r.requests.fetch_add(1, SeqCst) + 1
            })
            .unwrap_or(0);
        self.last.store(slot, SeqCst);
        self.state.store(1, SeqCst);
        self.emit(slot, Stage::Received, Kind::None);
        Request {
            connection: self.clone(),
            slot,
            terminal: false,
            native_connection: None,
            connection_observed: false,
        }
    }
    pub fn end(&self, kind: Kind) {
        self.emit(self.last.load(SeqCst), Stage::End, kind);
    }
}
pub struct Request {
    connection: Connection,
    slot: u64,
    terminal: bool,
    native_connection: Option<CaptureConnection>,
    connection_observed: bool,
}
impl Request {
    pub fn capture<B>(&mut self, request: &mut hyper::Request<B>) {
        if self.connection.recorder.is_some() {
            self.native_connection = Some(capture_connection(request));
        }
    }
    fn observe_connection(&mut self) {
        if self.connection.state.load(SeqCst) != 2 || self.connection_observed {
            return;
        }
        // 終端時点のmetadata有無だけ。address/extra/Debug値は読まず保存しない。
        // upstreamの同期borrowであり、接続確立の瞬間や物理送信完了ではない。
        let assigned = self
            .native_connection
            .as_ref()
            .is_some_and(|capture| capture.connection_metadata().is_some());
        self.connection.emit(
            self.slot,
            if assigned {
                Stage::NativeAssigned
            } else {
                Stage::NativeUnobserved
            },
            Kind::None,
        );
        self.connection_observed = true;
    }
    pub fn forward(&mut self) {
        self.connection.state.store(2, SeqCst);
        self.connection.emit(self.slot, Stage::Forward, Kind::None);
    }
    pub fn headers(&mut self) {
        self.observe_connection();
        self.terminal = true;
        self.connection.state.store(3, SeqCst);
        self.connection.emit(self.slot, Stage::Headers, Kind::None);
    }
    pub fn native_error(&mut self) {
        self.observe_connection();
        self.terminal = true;
        self.connection.state.store(4, SeqCst);
        self.connection
            .emit(self.slot, Stage::NativeError, Kind::None);
    }
}
impl Drop for Request {
    fn drop(&mut self) {
        if !self.terminal {
            self.observe_connection();
            self.connection.state.store(5, SeqCst);
            self.connection.emit(self.slot, Stage::Dropped, Kind::None);
        }
        if let Some(recorder) = &self.connection.recorder {
            recorder.pending.fetch_sub(1, SeqCst);
        }
    }
}
pub fn start() -> Option<Arc<Recorder>> {
    let output = PathBuf::from(std::env::var_os("RESCUE_DRIVER_TRACE")?);
    let recorder = Recorder::memory();
    let worker = recorder.clone();
    let spawned = thread::Builder::new().spawn(move || {
        let finish = output.with_extension("finish");
        let mut requested = false;
        for _ in 0..2400 {
            if finish.is_file() {
                requested = true;
                break;
            }
            thread::sleep(Duration::from_millis(50));
        }
        let text = worker.finish(requested);
        let temporary = output.with_extension("pending");
        let saved = OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&temporary)
            .and_then(|mut file| file.write_all(text.as_bytes()))
            .and_then(|_| {
                if output.exists() {
                    return Err(std::io::ErrorKind::AlreadyExists.into());
                }
                std::fs::rename(&temporary, &output)
            });
        if saved.is_err() {
            eprintln!("NATIVE_STAGE_WRITE_FAILED");
        }
    });
    if spawned.is_err() {
        eprintln!("NATIVE_STAGE_WORKER_FAILED");
        return None;
    }
    Some(recorder)
}
#[cfg(test)]
#[path = "diagnostic_tests.rs"]
mod tests;
