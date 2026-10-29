// 試験専用依存patch。UI/COM callbackは固定memoryとtry_lockだけを使う。
use std::{
    fs::OpenOptions,
    io::Write,
    path::PathBuf,
    sync::{
        atomic::{AtomicBool, AtomicU64, AtomicUsize, Ordering::SeqCst},
        Arc, Mutex, OnceLock,
    },
    thread,
    time::{Duration, Instant},
};
#[derive(Clone, Copy)]
pub(crate) enum Stage {
    WebviewEnter,
    EnvironmentReused,
    EnvironmentApiStart,
    EnvironmentApiReturn,
    EnvironmentCallback,
    EnvironmentWaitReturn,
    ControllerApiStart,
    ControllerApiReturn,
    ControllerCallback,
    ControllerWaitReturn,
}
impl Stage {
    fn name(self) -> &'static str {
        match self {
            Self::WebviewEnter => "webview-enter",
            Self::EnvironmentReused => "environment-reused",
            Self::EnvironmentApiStart => "environment-api-start",
            Self::EnvironmentApiReturn => "environment-api-return",
            Self::EnvironmentCallback => "environment-callback",
            Self::EnvironmentWaitReturn => "environment-wait-return",
            Self::ControllerApiStart => "controller-api-start",
            Self::ControllerApiReturn => "controller-api-return",
            Self::ControllerCallback => "controller-callback",
            Self::ControllerWaitReturn => "controller-wait-return",
        }
    }
}
#[derive(Clone)]
struct Event {
    seq: u64,
    stage: Stage,
    elapsed: u128,
}
#[derive(Clone, Copy)]
pub(crate) enum PumpScope {
    Environment,
    Controller,
}
#[derive(Clone, Copy)]
pub(crate) enum PumpSignal {
    Enter,
    Check,
    Ready,
    GetEnter,
    GetMessage,
    GetQuit,
    GetError,
    DispatchEnter,
    DispatchReturn,
}
#[derive(Clone)]
struct Pump {
    entered: bool,
    phase: &'static str,
    iterations: u32,
    get_calls: u32,
    get_returns: u32,
    dispatch_calls: u32,
    dispatch_returns: u32,
    last_us: u128,
    invalid: bool,
}
impl Pump {
    fn empty() -> Self {
        Self {
            entered: false,
            phase: "not-entered",
            iterations: 0,
            get_calls: 0,
            get_returns: 0,
            dispatch_calls: 0,
            dispatch_returns: 0,
            last_us: 0,
            invalid: false,
        }
    }
    fn update(&mut self, signal: PumpSignal, elapsed: u128) {
        use PumpSignal::*;
        let valid = match signal {
            Enter => !self.entered,
            Check => {
                self.entered && self.phase == "receiver-check" && self.iterations == self.get_calls
            }
            Ready | GetEnter => {
                self.phase == "receiver-check" && self.iterations == self.get_calls + 1
            }
            GetMessage | GetQuit | GetError => self.phase == "get-message",
            DispatchEnter => self.phase == "translate",
            DispatchReturn => self.phase == "dispatch",
        };
        if !valid
            || self.invalid
            || [
                self.iterations,
                self.get_calls,
                self.get_returns,
                self.dispatch_calls,
                self.dispatch_returns,
            ]
            .iter()
            .any(|v| *v >= 1000000)
        {
            self.invalid = true;
            return;
        }
        self.last_us = elapsed;
        match signal {
            Enter => {
                self.entered = true;
                self.phase = "receiver-check";
            }
            Check => {
                self.iterations += 1;
            }
            Ready => self.phase = "receiver-ready",
            GetEnter => {
                self.get_calls += 1;
                self.phase = "get-message";
            }
            GetMessage | GetQuit | GetError => {
                self.get_returns += 1;
                self.phase = match signal {
                    GetMessage => "translate",
                    GetQuit => "quit",
                    _ => "os-error",
                };
            }
            DispatchEnter => {
                self.dispatch_calls += 1;
                self.phase = "dispatch";
            }
            DispatchReturn => {
                self.dispatch_returns += 1;
                self.phase = "receiver-check";
            }
        }
    }
    fn json(&self) -> String {
        format!(
        "{{\"entered\":{},\"phase\":\"{}\",\"iterations\":{},\"getCalls\":{},\"getReturns\":{},\"dispatchCalls\":{},\"dispatchReturns\":{},\"lastUs\":{},\"invalid\":{}}}",
        self.entered, self.phase, self.iterations, self.get_calls, self.get_returns,
        self.dispatch_calls, self.dispatch_returns, self.last_us, self.invalid)
    }
}
#[derive(Clone)]
struct Observation {
    events: Vec<Event>,
    pumps: [Pump; 2],
}
struct Recorder {
    start: Instant,
    state: Mutex<Observation>,
    attempted: AtomicU64,
    dropped: AtomicU64,
    in_flight: AtomicUsize,
    enabled: AtomicBool,
    complete: AtomicBool,
}
static TRACE: OnceLock<Option<Arc<Recorder>>> = OnceLock::new();
// stdlib fixtureだけでwriterの通常closeを確認する。依存buildには含まれない。
#[cfg(test)]
static WRITER_FINISHED: AtomicBool = AtomicBool::new(false);
#[cfg(test)]
pub(crate) fn fixture_writer_finished() -> bool {
    WRITER_FINISHED.load(SeqCst)
}
struct Flight<'a>(&'a Recorder);
impl Drop for Flight<'_> {
    fn drop(&mut self) {
        self.0.in_flight.fetch_sub(1, SeqCst);
    }
}

pub(crate) fn record(stage: Stage) {
    let trace = TRACE.get_or_init(|| {
        let path = PathBuf::from(std::env::var_os("RESCUE_HOST_STARTUP_TRACE_DIR")?);
        let memory = Arc::new(Recorder {
            start: Instant::now(),
            state: Mutex::new(Observation {
                events: Vec::with_capacity(10),
                pumps: [Pump::empty(), Pump::empty()],
            }),
            attempted: AtomicU64::new(0),
            dropped: AtomicU64::new(0),
            in_flight: AtomicUsize::new(0),
            enabled: AtomicBool::new(true),
            complete: AtomicBool::new(false),
        });
        let worker = memory.clone();
        thread::Builder::new()
            .name("wry-startup-diagnostic".into())
            .spawn(move || worker.publish(path))
            .ok()?;
        Some(memory)
    });
    let Some(memory) = trace else { return };
    memory.in_flight.fetch_add(1, SeqCst);
    let _flight = Flight(memory);
    if !memory.enabled.load(SeqCst) {
        return;
    }
    let seq = memory.attempted.fetch_add(1, SeqCst) + 1;
    if let Ok(mut state) = memory.state.try_lock() {
        if state.events.len() < 10 {
            state.events.push(Event {
                seq,
                stage,
                elapsed: memory.start.elapsed().as_micros(),
            });
            if matches!(stage, Stage::ControllerWaitReturn) {
                memory.complete.store(true, SeqCst);
            }
            return;
        }
    }
    memory.dropped.fetch_add(1, SeqCst);
}
pub(crate) fn observe_pump(scope: PumpScope, signal: PumpSignal) {
    let Some(Some(memory)) = TRACE.get() else {
        return;
    };
    memory.in_flight.fetch_add(1, SeqCst);
    let _flight = Flight(memory);
    if !memory.enabled.load(SeqCst) {
        return;
    }
    if let Ok(mut state) = memory.state.try_lock() {
        let index = match scope {
            PumpScope::Environment => 0,
            PumpScope::Controller => 1,
        };
        state.pumps[index].update(signal, memory.start.elapsed().as_micros());
    } else {
        memory.dropped.fetch_add(1, SeqCst);
    }
}
impl Recorder {
    fn publish(&self, path: PathBuf) {
        let marker = path.join("app-startup.finish");
        let until = Instant::now() + Duration::from_secs(120);
        while !self.complete.load(SeqCst) && !marker.is_file() && Instant::now() < until {
            thread::sleep(Duration::from_millis(20));
        }
        let requested = self.complete.load(SeqCst) || marker.is_file();
        // 同じWry clockの保守的cutoff。後着eventはconsumerがincompleteにする。
        let end = self.start.elapsed().as_micros();
        self.enabled.store(false, SeqCst);
        for _ in 0..100 {
            if self.in_flight.load(SeqCst) == 0 {
                break;
            }
            thread::sleep(Duration::from_millis(1));
        }
        let state = self
            .state
            .try_lock()
            .map(|v| v.clone())
            .unwrap_or(Observation {
                events: Vec::new(),
                pumps: [Pump::empty(), Pump::empty()],
            });
        let mut events = state.events;
        events.sort_by_key(|e| e.seq);
        let rows: Vec<_> = events
            .iter()
            .map(|e| {
                format!(
                    "{{\"seq\":{},\"stage\":\"{}\",\"elapsedUs\":{}}}",
                    e.seq,
                    e.stage.name(),
                    e.elapsed
                )
            })
            .collect();
        let flights = self.in_flight.load(SeqCst);
        let text = format!(
            "{{\"schema\":1,\"variant\":\"wry-webview2-stage-2\",\"purpose\":\"diagnostic-hints-only\",\"events\":[{}],\"attempted\":{},\"recorded\":{},\"dropped\":{},\"sealed\":{},\"inFlight\":{},\"windowEndUs\":{},\"pumps\":{{\"environment\":{},\"controller\":{}}}}}",
            rows.join(","), self.attempted.load(SeqCst), events.len(), self.dropped.load(SeqCst), requested && flights == 0, flights, end,
            state.pumps[0].json(), state.pumps[1].json());
        if text.len() <= 2048 {
            if let Ok(mut file) = OpenOptions::new()
                .write(true)
                .create_new(true)
                .open(path.join("wry-startup-stages.json"))
            {
                let _ = file.write_all(text.as_bytes());
            }
        }
        // 上のscopeを出てfileがdropされた後だけ完了通知する。
        #[cfg(test)]
        WRITER_FINISHED.store(true, SeqCst);
    }
}
