// 公開record入口の有限fixture。COM/native成功の代替ではない。
#[path = "probe.rs"]
mod startup_probe;
use startup_probe::{begin_script, observe_script, ScriptSignal as S};
use startup_probe::{observe_pump, record, PumpScope, PumpSignal::*, Stage::*};
use std::{
    path::PathBuf,
    thread,
    time::{Duration, Instant},
};
fn script(mode: &str) -> u32 {
    let ordinal = begin_script();
    observe_script(ordinal, S::ApiStart);
    if mode == "api-error" {
        observe_script(ordinal, S::ApiReturn);
        observe_script(ordinal, S::Return(false));
        return ordinal;
    }
    let (tx, rx) = std::sync::mpsc::channel::<Result<(), ()>>();
    let early = mode == "early";
    if early {
        observe_script(ordinal, S::Callback(true));
        tx.send(Ok(())).expect("send over mpsc channel");
        observe_script(ordinal, S::Sent);
    }
    observe_script(ordinal, S::ApiReturn);
    let scope = PumpScope::Script(ordinal);
    observe_pump(scope, Enter);
    observe_pump(scope, Check);
    if !early {
        observe_pump(scope, GetEnter);
    }
    if mode == "send-error" {
        std::panic::set_hook(Box::new(|_| {}));
        drop(rx);
        let failed = std::panic::catch_unwind(|| {
            let _scope = startup_probe::BuildScope::new();
            observe_script(ordinal, S::Callback(true));
            tx.send(Ok(())).expect("send over mpsc channel");
            observe_script(ordinal, S::Sent);
        });
        assert!(failed.is_err());
        return ordinal;
    }
    if !early && mode != "pending" {
        observe_script(ordinal, S::Callback(mode != "error-payload"));
        if mode != "callback" {
            tx.send(if mode == "error-payload" {
                Err(())
            } else {
                Ok(())
            })
            .expect("send over mpsc channel");
            observe_script(ordinal, S::Sent);
        }
    }
    if ["complete", "error-payload", "early"].contains(&mode) {
        if !early {
            observe_pump(scope, GetMessage);
            observe_pump(scope, DispatchEnter);
            observe_pump(scope, DispatchReturn);
            observe_pump(scope, Check);
        }
        let received = rx.try_recv().unwrap();
        observe_pump(scope, Ready);
        observe_script(ordinal, S::Return(received.is_ok()));
    }
    ordinal
}
fn wait_writer() {
    let until = Instant::now() + Duration::from_secs(3);
    while !startup_probe::fixture_writer_finished() {
        assert!(Instant::now() < until);
        thread::sleep(Duration::from_millis(10));
    }
}
fn main() {
    let args: Vec<_> = std::env::args().collect();
    assert_eq!(args.len(), 3);
    let mode = args[2].as_str();
    assert!([
        "environment",
        "controller",
        "dispatch",
        "complete",
        "script-pending",
        "script-callback",
        "script-sent",
        "script-multi",
        "script-error-payload",
        "script-api-error",
        "script-send-error",
        "script-overflow",
        "script-stale",
        "script-early-seal"
    ]
    .contains(&mode));
    let path = PathBuf::from(&args[1]);
    std::env::set_var("RESCUE_HOST_STARTUP_TRACE_DIR", &path);
    record(WebviewEnter);
    record(EnvironmentApiStart);
    record(EnvironmentApiReturn);
    observe_pump(PumpScope::Environment, Enter);
    observe_pump(PumpScope::Environment, Check);
    if mode == "environment" {
        observe_pump(PumpScope::Environment, GetEnter);
    } else {
        record(EnvironmentCallback);
        observe_pump(PumpScope::Environment, Ready);
        record(EnvironmentWaitReturn);
        record(ControllerApiStart);
        record(ControllerApiReturn);
        observe_pump(PumpScope::Controller, Enter);
        observe_pump(PumpScope::Controller, Check);
        observe_pump(PumpScope::Controller, GetEnter);
        if mode != "controller" {
            observe_pump(PumpScope::Controller, GetMessage);
            observe_pump(PumpScope::Controller, DispatchEnter);
            if mode != "dispatch" {
                record(ControllerCallback);
                observe_pump(PumpScope::Controller, DispatchReturn);
                observe_pump(PumpScope::Controller, Check);
                observe_pump(PumpScope::Controller, Ready);
                record(ControllerWaitReturn);
                record(InitEnter);
                match mode {
                    "complete" => {
                        record(InitReturn);
                        let mut scope = startup_probe::BuildScope::new();
                        scope.success();
                    }
                    "script-multi" => {
                        script("early");
                        script("sent");
                    }
                    "script-overflow" => {
                        let mut actual = 0;
                        for _ in 0..129 {
                            script("complete");
                            actual += 1;
                        }
                        assert_eq!(actual, 129);
                    }
                    "script-stale" => {
                        let old = script("complete");
                        script("pending");
                        observe_script(old, S::Sent);
                    }
                    "script-early-seal" => {
                        script("callback");
                        std::fs::write(path.join("app-startup.finish"), "finish").unwrap();
                        wait_writer();
                        observe_script(1, S::Sent);
                    }
                    _ => {
                        let scenario = mode.strip_prefix("script-").unwrap();
                        script(scenario);
                        if ["error-payload", "api-error"].contains(&scenario) {
                            record(InitReturn);
                            record(WebviewExitError);
                        }
                    }
                }
            }
        }
    }
    std::fs::write(path.join("app-startup.finish"), "finish").unwrap();
    wait_writer();
}
