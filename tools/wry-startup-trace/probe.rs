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
struct Recorder {
    start: Instant,
    events: Mutex<Vec<Event>>,
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
            events: Mutex::new(Vec::with_capacity(10)),
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
    if let Ok(mut events) = memory.events.try_lock() {
        if events.len() < 10 {
            events.push(Event {
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
        let mut events = self
            .events
            .try_lock()
            .map(|v| v.clone())
            .unwrap_or_default();
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
            "{{\"schema\":1,\"variant\":\"wry-webview2-stage-1\",\"purpose\":\"diagnostic-hints-only\",\"events\":[{}],\"attempted\":{},\"recorded\":{},\"dropped\":{},\"sealed\":{},\"inFlight\":{},\"windowEndUs\":{}}}",
            rows.join(","), self.attempted.load(SeqCst), events.len(), self.dropped.load(SeqCst), requested && flights == 0, flights, end);
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
