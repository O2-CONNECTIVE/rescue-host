// Windows x64用の有限fixtureだけのFFI。依存追加や製品へのlinkはしない。
#![allow(non_snake_case)]
use std::{ffi::c_void, ptr, sync::atomic::Ordering::SeqCst};
pub const COMPLETE: u32 = 0x8001;
pub const BENIGN: u32 = 0x8002;
pub const QUIT: u32 = 0x12;
const CONTEXT_SENT: u32 = 1;
const CONTEXT_DISPATCHED: u32 = 2;
const SMTO_BLOCK: u32 = 0x1;
const SMTO_ABORTIFHUNG: u32 = 0x2;
type Handle = *mut c_void;
#[repr(C)]
#[derive(Default)]
pub struct Message {
    hwnd: Handle, message: u32, wparam: usize, lparam: isize,
    time: u32, x: i32, y: i32, private: u32,
}
#[repr(C)]
struct WindowClass {
    style: u32, proc: Option<unsafe extern "system" fn(Handle, u32, usize, isize) -> isize>,
    class_extra: i32, window_extra: i32, instance: Handle, icon: Handle,
    cursor: Handle, background: Handle, menu: *const u16, name: *const u16,
}
#[link(name = "user32")]
extern "system" {
    fn RegisterClassW(class: *const WindowClass) -> u16;
    fn UnregisterClassW(name: *const u16, instance: Handle) -> i32;
    fn CreateWindowExW(ex: u32, class: *const u16, name: *const u16, style: u32,
        x: i32, y: i32, w: i32, h: i32, parent: Handle, menu: Handle,
        instance: Handle, param: Handle) -> Handle;
    fn DestroyWindow(window: Handle) -> i32;
    fn DefWindowProcW(window: Handle, message: u32, wparam: usize, lparam: isize) -> isize;
    fn GetMessageA(message: *mut Message, window: Handle, min: u32, max: u32) -> i32;
    fn TranslateMessage(message: *const Message) -> i32;
    fn DispatchMessageA(message: *const Message) -> isize;
    fn PostMessageW(window: Handle, message: u32, wparam: usize, lparam: isize) -> i32;
    fn SendMessageTimeoutW(window: Handle, message: u32, wparam: usize, lparam: isize,
        flags: u32, timeout: u32, result: *mut usize) -> isize;
    fn InSendMessageEx(reserved: Handle) -> u32;
    fn PostThreadMessageW(thread: u32, message: u32, wparam: usize, lparam: isize) -> i32;
}
#[link(name = "kernel32")]
extern "system" {
    fn GetModuleHandleW(name: *const u16) -> Handle;
    fn GetCurrentThreadId() -> u32;
}
unsafe extern "system" fn procedure(window: Handle, message: u32, wparam: usize, lparam: isize) -> isize {
    if message == COMPLETE {
        let s = crate::state();
        s.context.store(if InSendMessageEx(ptr::null_mut()) != 0 { CONTEXT_SENT } else { CONTEXT_DISPATCHED }, SeqCst);
        s.completion.fetch_add(1, SeqCst);
        s.ready.send(()).unwrap();
        return 0;
    }
    if message == BENIGN { return 0; }
    DefWindowProcW(window, message, wparam, lparam)
}
pub struct OwnedWindow { handle: Handle, name: Vec<u16>, instance: Handle }
impl OwnedWindow {
    pub fn new() -> Self {
        assert!(cfg!(all(target_os = "windows", target_arch = "x86_64")));
        assert_eq!(std::mem::size_of::<Message>(), 48);
        let name: Vec<u16> = "RescuePumpContractFixture".encode_utf16().chain(Some(0)).collect();
        unsafe {
            let instance = GetModuleHandleW(ptr::null());
            assert!(!instance.is_null());
            let class = WindowClass { style: 0, proc: Some(procedure), class_extra: 0, window_extra: 0,
                instance, icon: ptr::null_mut(), cursor: ptr::null_mut(), background: ptr::null_mut(),
                menu: ptr::null(), name: name.as_ptr() };
            assert_ne!(RegisterClassW(&class), 0);
            let handle = CreateWindowExW(0, name.as_ptr(), name.as_ptr(), 0, 0, 0, 0, 0,
                -3isize as Handle, ptr::null_mut(), instance, ptr::null_mut());
            assert!(!handle.is_null());
            Self { handle, name, instance }
        }
    }
    pub fn handle(&self) -> usize { self.handle as usize }
    pub fn close(self) -> bool {
        unsafe {
            let destroyed = DestroyWindow(self.handle) != 0;
            let unregistered = UnregisterClassW(self.name.as_ptr(), self.instance) != 0;
            destroyed && unregistered
        }
    }
}
pub fn post(handle: usize, message: u32) {
    assert_ne!(unsafe { PostMessageW(handle as Handle, message, 0, 0) }, 0);
}
pub fn send(handle: usize, message: u32) {
    let mut result = 0;
    assert_ne!(unsafe { SendMessageTimeoutW(handle as Handle, message, 0, 0,
        SMTO_BLOCK | SMTO_ABORTIFHUNG, 1000, &mut result) }, 0);
}
pub fn current_thread() -> u32 { unsafe { GetCurrentThreadId() } }
pub fn post_quit(thread: u32) {
    assert_ne!(unsafe { PostThreadMessageW(thread, QUIT, 0, 0) }, 0);
}
pub mod core {
    pub struct Error;
    impl Error { pub fn from_thread() -> Self { Self } }
}
pub mod Win32 { pub mod UI { pub mod WindowsAndMessaging {
    pub use super::super::super::Message as MSG;
    pub struct Bool(pub i32);
    pub unsafe fn GetMessageA(message: &mut MSG, window: Option<*mut std::ffi::c_void>, min: u32, max: u32) -> Bool {
        let s = crate::state();
        if s.get_calls.fetch_add(1, crate::SeqCst) + 1 == 19 { s.enter.send(()).unwrap(); }
        let result = super::super::super::GetMessageA(message, window.unwrap_or(std::ptr::null_mut()), min, max);
        s.get_returns.fetch_add(1, crate::SeqCst);
        Bool(result)
    }
    pub unsafe fn TranslateMessage(message: &MSG) -> i32 { super::super::super::TranslateMessage(message) }
    pub unsafe fn DispatchMessageA(message: &MSG) -> isize {
        let s = crate::state();
        s.dispatch_calls.fetch_add(1, crate::SeqCst);
        let result = super::super::super::DispatchMessageA(message);
        s.dispatch_returns.fetch_add(1, crate::SeqCst);
        result
    }
} } }
