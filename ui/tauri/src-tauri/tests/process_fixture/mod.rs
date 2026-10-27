use std::io::{BufRead, Write};
use std::time::Duration;

// This is a test-only wire contract, not the Go/evaluation protocol.
pub fn child() {
    std::thread::spawn(|| {
        std::thread::sleep(Duration::from_secs(2));
        std::process::exit(124);
    });
    println!(
        "{{\"fixtureVersion\":1,\"event\":\"boot\",\"pid\":{}}}",
        std::process::id()
    );
    std::io::stdout().flush().unwrap();
    let mut command = String::new();
    std::io::stdin().lock().read_line(&mut command).unwrap();
    assert_eq!(command, "{\"fixtureStop\":1}\n");
    println!("{{\"fixtureVersion\":1,\"event\":\"final\",\"stopped\":true}}");
}

pub fn run() -> Result<(), &'static str> {
    Err("FIXTURE_LAUNCH_UNIMPLEMENTED")
}
