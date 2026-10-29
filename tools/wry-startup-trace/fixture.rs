// 公開record入口の有限fixture。WebView/COM/networkは起動しない。
#[path = "probe.rs"]
mod startup_probe;
use startup_probe::{observe_pump, PumpScope, PumpSignal::*};
use startup_probe::{record, Stage::*};
use std::{
    path::PathBuf,
    thread,
    time::{Duration, Instant},
};
fn main() {
    let args: Vec<_> = std::env::args().collect();
    if args.len() != 3
        || !["environment", "controller", "dispatch", "complete"].contains(&args[2].as_str())
    {
        std::process::exit(2);
    }
    let path = PathBuf::from(&args[1]);
    std::env::set_var("RESCUE_HOST_STARTUP_TRACE_DIR", &path);
    record(WebviewEnter);
    record(EnvironmentApiStart);
    record(EnvironmentApiReturn);
    observe_pump(PumpScope::Environment, Enter);
    observe_pump(PumpScope::Environment, Check);
    if args[2] != "environment" {
        record(EnvironmentCallback);
        observe_pump(PumpScope::Environment, Ready);
        record(EnvironmentWaitReturn);
        record(ControllerApiStart);
        record(ControllerApiReturn);
        observe_pump(PumpScope::Controller, Enter);
        observe_pump(PumpScope::Controller, Check);
        observe_pump(PumpScope::Controller, GetEnter);
    } else {
        observe_pump(PumpScope::Environment, GetEnter);
    }
    if args[2] == "dispatch" || args[2] == "complete" {
        observe_pump(PumpScope::Controller, GetMessage);
        observe_pump(PumpScope::Controller, DispatchEnter);
    }
    if args[2] == "complete" {
        record(ControllerCallback);
        observe_pump(PumpScope::Controller, DispatchReturn);
        observe_pump(PumpScope::Controller, Check);
        observe_pump(PumpScope::Controller, Ready);
        record(ControllerWaitReturn);
    }
    std::fs::write(path.join("app-startup.finish"), "finish").unwrap();
    let until = Instant::now() + Duration::from_secs(3);
    while !startup_probe::fixture_writer_finished() {
        if Instant::now() >= until {
            std::process::exit(3);
        }
        thread::sleep(Duration::from_millis(10));
    }
    // 完了通知はwriterのfile drop後。main終了による強制closeを代替にしない。
}
