// 診断専用。Tauriの組立て・IPC・初期化scriptを持たない単一WebView。
#![cfg_attr(target_os = "windows", windows_subsystem = "windows")]

#[cfg(windows)]
fn run() -> Result<(), Box<dyn std::error::Error>> {
    use tao::{
        dpi::LogicalSize,
        event::{Event, WindowEvent},
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
    let mut context = WebContext::new(Some(profile));
    let webview = WebViewBuilder::new_with_web_context(&mut context)
        .with_id("main")
        .with_incognito(false)
        .with_transparent(false)
        .with_navigation_handler(|url| url == "about:blank")
        .with_new_window_req_handler(|_, _| wry::NewWindowResponse::Deny)
        .with_html("<!doctype html><meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; base-uri 'none'; form-action 'none'\"><h1 id=\"wry-creation-contrast\">Wry creation contrast</h1>")
        .build(&window)?;
    let mut webview = Some(webview);
    let mut context = Some(context);
    // build()/CloseRequested/Dropは同じUI thread。別thread wakeは追加しない。
    event_loop.run(move |event, _, flow| {
        let _keep_window = &window;
        *flow = ControlFlow::Wait;
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
