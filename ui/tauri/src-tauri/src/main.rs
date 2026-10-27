#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    rescue_host_shell::run();
}
