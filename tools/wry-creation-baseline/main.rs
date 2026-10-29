// 診断専用。Tauriの組立て・IPC・初期化scriptを持たない単一WebView。
#![cfg_attr(target_os = "windows", windows_subsystem = "windows")]

#[cfg(any(windows, test))]
const FIXTURE_HTML: &str = "<!doctype html><meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; base-uri 'none'; form-action 'none'\"><h1 id=\"wry-creation-contrast\">Wry creation contrast</h1>";

// 限定data URIの固定HTML完全一致だけを匿名観測する。許可判定には使用しない。
#[cfg(any(windows, test))]
fn navigation_uri_matches_fixture(uri: &str) -> bool {
    if uri.len() > 8192 { return false; }
    let Some(payload) = uri.strip_prefix("data:text/html;charset=UTF-8,")
        .or_else(|| uri.strip_prefix("data:text/html;charset=utf-8,"))
        .or_else(|| uri.strip_prefix("data:text/html,")) else { return false };
    fn hex(b: u8) -> Option<u8> {
        match b { b'0'..=b'9' => Some(b - b'0'), b'a'..=b'f' => Some(b - b'a' + 10),
            b'A'..=b'F' => Some(b - b'A' + 10), _ => None }
    }
    let mut bytes = payload.bytes(); let mut index = 0;
    while let Some(byte) = bytes.next() {
        let decoded = if byte == b'%' {
            let Some(value) = bytes.next().and_then(hex).zip(bytes.next().and_then(hex)) else { return false };
            value.0 * 16 + value.1
        } else { byte };
        if FIXTURE_HTML.as_bytes().get(index) != Some(&decoded) { return false; }
        index += 1;
    }
    index == FIXTURE_HTML.len()
}

// callbackは固定分類を有界channelへ非待機で送る。I/Oは所有workerだけが行う。
#[cfg(windows)]
mod navigation_trace {
    use std::{fs::{self, OpenOptions}, io::Write, path::PathBuf, sync::{Arc, OnceLock, atomic::{AtomicUsize, Ordering}, mpsc::{self, SyncSender, TrySendError}}, thread, time::{Duration, Instant}};
    #[derive(Clone)]
    pub struct Sender { tx: SyncSender<String>, dropped: Arc<AtomicUsize> }
    impl Sender {
        pub fn record(&self, event: &str) {
            if let Err(TrySendError::Full(_) | TrySendError::Disconnected(_)) = self.tx.try_send(event.to_owned()) {
                self.dropped.fetch_add(1, Ordering::SeqCst);
            }
        }
    }
    static SENDER: OnceLock<Sender> = OnceLock::new();
    fn unknown() { if let Some(sender) = SENDER.get() { sender.record("PAIR:{\"slot\":0,\"kind\":\"unknown\"}"); } }
    fn start_event(slot: Option<u8>, class: u8, allow: bool, applied: bool) {
        let Some(sender) = SENDER.get() else { return };
        sender.record(if allow { "navigation-allowed" } else if class == 2 { "navigation-denied-fixture" } else { "navigation-denied" });
        let Some(slot) = slot else { unknown(); return };
        let uri = match class { 1 => "blank", 2 => "fixture", 3 => "other", _ => { unknown(); return } };
        let decision = if allow { "allowed" } else { "denied" }; let cancel = if applied { "call-ok" } else { "call-error" };
        sender.record(&format!("PAIR:{{\"slot\":{slot},\"kind\":\"start\",\"decision\":\"{decision}\",\"cancel\":\"{cancel}\",\"uri\":\"{uri}\"}}"));
    }
    fn complete_event(slot: Option<u8>, success: Option<bool>) {
        let Some(sender) = SENDER.get() else { return };
        let (Some(slot), Some(success)) = (slot, success) else { unknown(); return };
        let result = if success { "success" } else { "failure" };
        sender.record(&format!("PAIR:{{\"slot\":{slot},\"kind\":\"complete\",\"success\":\"{result}\"}}"));
    }
    pub fn install(sender: Sender) -> bool {
        if SENDER.set(sender).is_err() { return false; }
        wry::startup_probe::install_navigation_hooks(wry::startup_probe::NavigationHooks {
            fixture_html: super::FIXTURE_HTML, start: start_event, complete: complete_event,
        })
    }
    pub fn start(path: PathBuf) -> Option<Sender> {
        let (tx, rx) = mpsc::sync_channel::<String>(32);
        let dropped = Arc::new(AtomicUsize::new(0));
        let worker_dropped = dropped.clone();
        thread::Builder::new().name("baseline-navigation-trace".into()).spawn(move || {
            let until = Instant::now() + Duration::from_secs(120);
            let mut events = Vec::with_capacity(32);
            let mut pairs = Vec::with_capacity(32);
            let mut before = false; let mut after = false;
            loop {
                while let Ok(event) = rx.try_recv() {
                    if let Some(pair) = event.strip_prefix("PAIR:") {
                        if pairs.len() < 32 { pairs.push(pair.to_owned()); } else { worker_dropped.fetch_add(1, Ordering::SeqCst); }
                    } else if events.len() < 32 { events.push(event); } else { worker_dropped.fetch_add(1, Ordering::SeqCst); }
                }
                for (phase, done) in [("before", &mut before), ("after", &mut after)] {
                    if !*done && path.join(format!("navigation-{phase}.request")).is_file() {
                        let rows = events.iter().map(|e| format!("\"{e}\"")).collect::<Vec<_>>().join(",");
                        let dropped = worker_dropped.load(Ordering::SeqCst) + usize::from(wry::startup_probe::navigation_fault());
                        let body = format!("{{\"schema\":1,\"phase\":\"{phase}\",\"dropped\":{dropped},\"events\":[{rows}],\"pairs\":[{}]}}", pairs.join(","));
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
    if let Some(sender) = &trace { if !navigation_trace::install(sender.clone()) { sender.record("PAIR:{\"slot\":0,\"kind\":\"unknown\"}"); } }
    // build()/CloseRequested/Dropは同じUI thread。別thread wakeは追加しない。
    event_loop.run(move |event, _, flow| {
        let _keep_window = &window;
        *flow = ControlFlow::Wait;
        if begin_on_initial_event(&mut attempted, matches!(&event, Event::NewEvents(StartCause::Init))) {
            let load_trace = trace.clone();
            let created = WebViewBuilder::new_with_web_context(context.as_mut().expect("context"))
                .with_id("main")
                .with_incognito(false)
                .with_transparent(false)
                .with_navigation_handler(|url| url == "about:blank")
                .with_on_page_load_handler(move |stage, _| {
                    if let Some(trace) = &load_trace { trace.record(match stage { wry::PageLoadEvent::Started => "load-started", wry::PageLoadEvent::Finished => "load-finished" }); }
                })
                .with_new_window_req_handler(|_, _| wry::NewWindowResponse::Deny)
                .with_html(FIXTURE_HTML)
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
    fn 対照modeは二つの固定値だけを受け付ける() {
        assert!(matches!(super::parse_baseline_mode("with-html"), Some(super::BaselineMode::WithHtml)));
        assert!(matches!(super::parse_baseline_mode("load-html"), Some(super::BaselineMode::LoadHtml)));
        assert!(super::parse_baseline_mode("").is_none());
        assert!(super::parse_baseline_mode("other").is_none());
    }

    #[test]
    fn 固定htmlだけに対応する非network_uriを厳密に識別する() {
        let encoded = super::FIXTURE_HTML.bytes().map(|b| format!("%{b:02X}")).collect::<String>();
        let exact = format!("data:text/html;charset=UTF-8,{encoded}");
        assert!(super::navigation_uri_matches_fixture(&exact));
        assert!(super::navigation_uri_matches_fixture(&exact.to_lowercase()));
        assert!(super::navigation_uri_matches_fixture(&format!("data:text/html,{}", super::FIXTURE_HTML)));
        assert!(!super::navigation_uri_matches_fixture(&format!("data:text/html,{}#", super::FIXTURE_HTML)));
        assert!(!super::navigation_uri_matches_fixture(&format!("data:text/html,{}?", super::FIXTURE_HTML)));
        assert!(!super::navigation_uri_matches_fixture(&format!("data:text/html,{}%", super::FIXTURE_HTML)));
        assert!(!super::navigation_uri_matches_fixture(&format!("data:text/html,{}%2", super::FIXTURE_HTML)));
        assert!(!super::navigation_uri_matches_fixture(&format!("data:text/html,{}%ZZ", super::FIXTURE_HTML)));
        assert!(!super::navigation_uri_matches_fixture(&format!("data:text/html,{}%00", super::FIXTURE_HTML)));
        assert!(!super::navigation_uri_matches_fixture(&format!("data:text/html;charset=latin1,{}", super::FIXTURE_HTML)));
        assert!(!super::navigation_uri_matches_fixture(&format!("data:text/html,{}", "a".repeat(8193))));
        assert!(!super::navigation_uri_matches_fixture(&(exact + "%20")));
        assert!(!super::navigation_uri_matches_fixture("data:text/html,other"));
        assert!(!super::navigation_uri_matches_fixture("https://example.invalid/"));
        assert!(!super::navigation_uri_matches_fixture("about:blank"));
        assert!(!super::navigation_uri_matches_fixture("data:text/html,%"));
        assert!(!super::navigation_uri_matches_fixture("data:text/html,%2"));
        assert!(!super::navigation_uri_matches_fixture("data:text/html,%ZZ"));
        assert!(!super::navigation_uri_matches_fixture("data:text/html;base64,AAAA"));
    }

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
