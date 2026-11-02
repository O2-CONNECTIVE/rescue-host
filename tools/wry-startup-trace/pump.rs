// 固定公式関数から生成する試験専用loop。通常配布には使わない。
use std::sync::mpsc;
use webview2_com::{Error, Result};
use windows::Win32::UI::WindowsAndMessaging::{self, MSG};
use crate::startup_probe::{observe_pump, PumpScope, PumpSignal};
pub fn wait_with_pump<T>(rx: mpsc::Receiver<T>, scope: PumpScope) -> Result<T> {
    observe_pump(scope, PumpSignal::Enter);
    let mut msg = MSG::default();

    loop {
        observe_pump(scope, PumpSignal::Check);
        if let Ok(result) = rx.try_recv() {
            observe_pump(scope, PumpSignal::Ready);
            return Ok(result);
        }

        observe_pump(scope, PumpSignal::GetEnter);
        unsafe {
            match WindowsAndMessaging::GetMessageA(&mut msg, None, 0, 0).0 {
                -1 => {
                    let error = windows::core::Error::from_thread();
                    observe_pump(scope, PumpSignal::GetError);
                    return Err(error.into());
                }
                0 => { observe_pump(scope, PumpSignal::GetQuit); return Err(Error::TaskCanceled); },
                _ => {
                    observe_pump(scope, PumpSignal::GetMessage);
                    let _ = WindowsAndMessaging::TranslateMessage(&msg);
                    observe_pump(scope, PumpSignal::DispatchEnter);
                    WindowsAndMessaging::DispatchMessageA(&msg);
                    observe_pump(scope, PumpSignal::DispatchReturn);
                }
            }
        }
    }
}
