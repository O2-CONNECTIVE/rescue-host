mod decoder;
mod owned;

use std::io::{Read, Write};
use std::time::Duration;

// This is a test-only wire contract, not the Go/evaluation protocol.
pub fn child() {
    std::thread::spawn(|| {
        std::thread::sleep(Duration::from_secs(5));
        std::process::exit(124);
    });
    println!(
        "{{\"fixtureVersion\":1,\"event\":\"boot\",\"pid\":{}}}",
        std::process::id()
    );
    std::io::stdout().flush().unwrap();
    let mut command = Vec::new();
    std::io::stdin()
        .lock()
        .take(65)
        .read_to_end(&mut command)
        .unwrap();
    assert_eq!(command, b"{\"fixtureStop\":1}\n");
    println!("{{\"fixtureVersion\":1,\"event\":\"final\",\"stopped\":true}}");
}

pub fn run() -> Result<(), &'static str> {
    owned::run()
}
