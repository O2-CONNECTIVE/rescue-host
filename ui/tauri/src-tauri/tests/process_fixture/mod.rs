mod decoder;
mod owned;
mod reader;

use std::io::{Read, Write};
use std::time::Duration;

#[derive(Clone, Copy)]
pub enum Mode {
    Graceful,
    Overlong,
    ExtraOutput,
    NonzeroExit,
    FinalWithoutExit,
}

impl Mode {
    pub fn argument(self) -> &'static str {
        match self {
            Self::Graceful => "--owned-fixture-v1=graceful",
            Self::Overlong => "--owned-fixture-v1=overlong",
            Self::ExtraOutput => "--owned-fixture-v1=extra-output",
            Self::NonzeroExit => "--owned-fixture-v1=nonzero-exit",
            Self::FinalWithoutExit => "--owned-fixture-v1=final-without-exit",
        }
    }

    pub fn from_argument(argument: &str) -> Option<Self> {
        [
            Self::Graceful,
            Self::Overlong,
            Self::ExtraOutput,
            Self::NonzeroExit,
            Self::FinalWithoutExit,
        ]
        .into_iter()
        .find(|mode| mode.argument() == argument)
    }
}

// This is a test-only wire contract, not the Go/evaluation protocol.
pub fn child(mode: Mode) {
    std::thread::spawn(|| {
        std::thread::sleep(Duration::from_secs(5));
        std::process::exit(124);
    });
    if matches!(mode, Mode::Overlong) {
        std::io::stdout().write_all(&[b'x'; 129]).unwrap();
        std::io::stdout().flush().unwrap();
        return;
    }
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
    std::io::stdout().flush().unwrap();
    match mode {
        Mode::ExtraOutput => println!("{{}}"),
        Mode::NonzeroExit => std::process::exit(7),
        Mode::FinalWithoutExit => std::thread::sleep(Duration::from_secs(4)),
        Mode::Graceful | Mode::Overlong => {}
    }
}

pub fn run(mode: Mode) -> Result<(), &'static str> {
    owned::run(mode)
}

pub fn run_reader_boundary() {
    reader::run();
}
