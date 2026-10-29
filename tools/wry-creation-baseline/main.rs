// 診断専用。Tauriの組立て・IPC・初期化scriptを持たない単一WebView。
#![cfg_attr(target_os = "windows", windows_subsystem = "windows")]

// callbackは固定分類を有界channelへ非待機で送る。I/Oは所有workerだけが行う。
#[cfg(windows)]
mod navigation_trace {
    use std::{fs::{self, OpenOptions}, io::Write, path::PathBuf, sync::{Arc, atomic::{AtomicUsize, Ordering}, mpsc::{self, SyncSender, TrySendError}}, thread, time::{Duration, Instant}};
    #[derive(Clone)]
    pub struct Sender { tx: SyncSender<&'static str>, dropped: Arc<AtomicUsize> }
    impl Sender {
        pub fn record(&self, event: &'static str) {
            if let Err(TrySendError::Full(_) | TrySendError::Disconnected(_)) = self.tx.try_send(event) {
                self.dropped.fetch_add(1, Ordering::SeqCst);
            }
        }
    }
    pub fn start(path: PathBuf) -> Option<Sender> {
        let (tx, rx) = mpsc::sync_channel(32);
        let dropped = Arc::new(AtomicUsize::new(0));
        let worker_dropped = dropped.clone();
        thread::Builder::new().name("baseline-navigation-trace".into()).spawn(move || {
            let until = Instant::now() + Duration::from_secs(120);
            let mut events = Vec::with_capacity(32);
            let mut before = false; let mut after = false;
            loop {
                while let Ok(event) = rx.try_recv() {
                    if events.len() < 32 { events.push(event); } else { worker_dropped.fetch_add(1, Ordering::SeqCst); }
                }
                for (phase, done) in [("before", &mut before), ("after", &mut after)] {
                    if !*done && path.join(format!("navigation-{phase}.request")).is_file() {
                        let rows = events.iter().map(|e| format!("\"{e}\"")).collect::<Vec<_>>().join(",");
                        let body = format!("{{\"schema\":1,\"phase\":\"{phase}\",\"dropped\":{},\"events\":[{rows}]}}", worker_dropped.load(Ordering::SeqCst));
                        let temp = path.join(format!("navigation-{phase}.tmp"));
                        let final_path = path.join(format!("navigation-{phase}.json"));
                        let ok = OpenOptions::new().write(true).create_new(true).open(&temp)
                            .and_then(|mut f| f.write_all(body.as_bytes())).and_then(|_| fs::rename(&temp, &final_path)).is_ok();
                        *done = true;
                        if !ok { worker_dropped.fetch_add(1, Ordering::SeqCst); }
                    }
                }
                if path.join("app-startup.finish").is_file() || Instant::now() >= until { break; }
                thread::sleep(Duration::from_millis(10));
            }
            let _ = OpenOptions::new().write(true).create_new(true).open(path.join("navigation-worker.closed"));
        }).ok()?;
        Some(Sender { tx, dropped })
    }
}

#[cfg(any(windows, test))]
fn begin_on_initial_event(attempted: &mut bool, is_init: bool) -> bool {
    if !is_init || *attempted {
        return false;
    }
    *attempted = true;
    true
}

#[cfg(windows)]
fn run() -> Result<(), Box<dyn std::error::Error>> {
    use tao::{
        dpi::LogicalSize,
        event::{Event, StartCause, WindowEvent},
        event_loop::{ControlFlow, EventLoopBuilder},
        window::WindowBuilder,
    };
    use wry::{WebContext, WebViewBuilder};

    // supervisorがこの実行専用folderを渡す。既存利用者のprofileは使わない。
    let profile = std::env::var_os("RESCUE_BASELINE_PROFILE").ok_or("missing profile")?;
    let profile = std::path::PathBuf::from(profile);
    if !profile.is_absolute() {
        return Err("profile not absolute".into());
    }
    let event_loop = EventLoopBuilder::<()>::with_user_event().build();
    let window = WindowBuilder::new()
        .with_title("Wry creation contrast (diagnostic only)")
        .with_inner_size(LogicalSize::new(900.0, 780.0))
        .with_min_inner_size(LogicalSize::new(360.0, 480.0))
        .build(&event_loop)?;
    let context = WebContext::new(Some(profile));
    let mut context = Some(context);
    let mut webview = None;
    let mut attempted = false;
    let trace = std::env::var_os("RESCUE_HOST_STARTUP_TRACE_DIR").and_then(|p| navigation_trace::start(p.into()));
    // build()/CloseRequested/Dropは同じUI thread。別thread wakeは追加しない。
    event_loop.run(move |event, _, flow| {
        let _keep_window = &window;
        *flow = ControlFlow::Wait;
        if begin_on_initial_event(&mut attempted, matches!(&event, Event::NewEvents(StartCause::Init))) {
            let nav_trace = trace.clone(); let load_trace = trace.clone();
            let created = WebViewBuilder::new_with_web_context(context.as_mut().expect("context"))
                .with_id("main")
                .with_incognito(false)
                .with_transparent(false)
                .with_navigation_handler(move |url| {
                    let allowed = url == "about:blank";
                    if let Some(trace) = &nav_trace { trace.record(if allowed { "navigation-allowed" } else { "navigation-denied" }); }
                    allowed
                })
                .with_on_page_load_handler(move |stage, _| {
                    if let Some(trace) = &load_trace { trace.record(match stage { wry::PageLoadEvent::Started => "load-started", wry::PageLoadEvent::Finished => "load-finished" }); }
                })
                .with_new_window_req_handler(|_, _| wry::NewWindowResponse::Deny)
                .with_html("<!doctype html><meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; base-uri 'none'; form-action 'none'\"><h1 id=\"wry-creation-contrast\">Wry creation contrast</h1>")
                .build(&window);
            match created {
                Ok(value) => webview = Some(value),
                Err(_) => std::process::exit(1),
            }
        }
        if let Event::WindowEvent {
            window_id,
            event: WindowEvent::CloseRequested,
            ..
        } = event
        {
            if window_id == window.id() {
                webview.take();
                context.take();
                *flow = ControlFlow::Exit;
            }
        }
    });
}

fn main() {
    #[cfg(windows)]
    if run().is_err() {
        // 原例外やpathを出さない。失敗理由の分類は診断stageとsupervisor側で行う。
        std::process::exit(1);
    }
    #[cfg(not(windows))]
    std::process::exit(1);
}

#[cfg(test)]
mod tests {
    use super::begin_on_initial_event;

    #[test]
    fn 初回初期化だけで作成し以後は繰り返さない() {
        let mut attempted = false;
        let mut calls = 0;
        assert!(!begin_on_initial_event(&mut attempted, false));
        if begin_on_initial_event(&mut attempted, true) {
            calls += 1;
        }
        if begin_on_initial_event(&mut attempted, true) {
            calls += 1;
        }
        assert_eq!(calls, 1);
    }
}
