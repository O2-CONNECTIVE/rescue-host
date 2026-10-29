// 固定公式loopを無変更includeし、自己所有message-only windowだけで区別する。
// WebView/COM/driverの再現ではなくWin32待機契約の識別試験。
#[path = "win32.rs"]
mod windows;
use std::sync::{atomic::{AtomicU32, Ordering::SeqCst}, mpsc, OnceLock};
use windows::Win32::UI::WindowsAndMessaging::{self, MSG};
#[derive(Debug)]
pub enum Error { TaskCanceled, Native }
pub type Result<T> = std::result::Result<T, Error>;
impl From<windows::core::Error> for Error {
    fn from(_: windows::core::Error) -> Self { Self::Native }
}
include!(env!("RESCUE_REFERENCE_PUMP"));

struct State {
    get_calls: AtomicU32, get_returns: AtomicU32,
    dispatch_calls: AtomicU32, dispatch_returns: AtomicU32,
    completion: AtomicU32, context: AtomicU32,
    enter: mpsc::Sender<()>, ready: mpsc::Sender<()>,
}
static STATE: OnceLock<State> = OnceLock::new();
fn state() -> &'static State { STATE.get().unwrap() }
fn counts() -> [u32; 4] {
    let s = state();
    [s.get_calls.load(SeqCst), s.get_returns.load(SeqCst),
     s.dispatch_calls.load(SeqCst), s.dispatch_returns.load(SeqCst)]
}
fn main() {
    let mode = std::env::args().nth(1).unwrap_or_default();
    assert!(["sent-completion", "posted-completion", "no-completion"].contains(&mode.as_str()));
    let (ready, receiver) = mpsc::channel();
    let (enter, entered) = mpsc::channel();
    assert!(STATE.set(State {
        get_calls: AtomicU32::new(0), get_returns: AtomicU32::new(0),
        dispatch_calls: AtomicU32::new(0), dispatch_returns: AtomicU32::new(0),
        completion: AtomicU32::new(0), context: AtomicU32::new(0), enter, ready,
    }).is_ok());
    let window = windows::OwnedWindow::new();
    // 18回の通常Dispatch後の19回目GetMessageを同じ制御点にする。
    for _ in 0..18 { windows::post(window.handle(), windows::BENIGN); }
    let handle = window.handle();
    let owner_thread = windows::current_thread();
    let scenario = mode.clone();
    let worker = std::thread::spawn(move || {
        entered.recv_timeout(std::time::Duration::from_secs(2)).unwrap();
        if scenario == "posted-completion" {
            windows::post(handle, windows::COMPLETE);
            return None;
        }
        // SendMessageTimeout完了でWndProcの戻りも確定する。raw値は出力しない。
        windows::send(handle, if scenario == "sent-completion" { windows::COMPLETE } else { windows::BENIGN });
        let before = counts();
        let completed = state().completion.load(SeqCst);
        let context = state().context.load(SeqCst);
        // callbackを送らない負例はWM_QUIT、送った正例はWM_NULLで自己所有待機を解く。
        if scenario == "no-completion" { windows::post_quit(owner_thread); }
        else { windows::post(handle, 0); }
        Some((before, completed, context))
    });
    let result = wait_with_pump(receiver);
    let observed = worker.join().unwrap();
    let after = counts();
    let cleanup = window.close();
    assert!(cleanup);
    if let Some((before, completed, context)) = observed {
        assert_eq!(before, [19, 18, 18, 18]);
        assert_eq!(completed, u32::from(mode == "sent-completion"));
        assert_eq!(context, u32::from(mode == "sent-completion"));
        assert_eq!(after, if mode == "sent-completion" { [19, 19, 19, 19] } else { [19, 19, 18, 18] });
    } else {
        assert_eq!(after, [19, 19, 19, 19]);
        assert_eq!(state().context.load(SeqCst), 2);
    }
    assert_eq!(state().completion.load(SeqCst), u32::from(mode != "no-completion"));
    assert!(match (mode.as_str(), result) {
        ("no-completion", Err(Error::TaskCanceled)) => true,
        ("sent-completion" | "posted-completion", Ok(())) => true,
        _ => false,
    });
    println!("{{\"schema\":1,\"purpose\":\"win32-contract-only\",\"mode\":\"{}\",\"beforeWakeVerified\":{},\"completionObserved\":{},\"getReturns\":{},\"dispatchReturns\":{},\"workerJoined\":true,\"windowDestroyed\":true,\"classUnregistered\":true}}",
        mode, observed.is_some(), mode != "no-completion", after[1], after[3]);
}
