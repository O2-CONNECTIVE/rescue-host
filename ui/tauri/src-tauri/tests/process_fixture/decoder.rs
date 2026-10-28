use crate::owned_process::frame::read_frame;
use serde::Deserialize;
use std::io::{BufReader, ErrorKind};
use std::process::ChildStdout;
use std::sync::mpsc::SyncSender;

#[derive(Deserialize)]
#[serde(tag = "event", deny_unknown_fields)]
enum Event {
    #[serde(rename = "boot")]
    Boot {
        #[serde(rename = "fixtureVersion")]
        version: u8,
        pid: u32,
    },
    #[serde(rename = "final")]
    Final {
        #[serde(rename = "fixtureVersion")]
        version: u8,
        stopped: bool,
    },
}

pub fn read(stdout: ChildStdout, pid: u32, boot: SyncSender<()>) -> Result<(), &'static str> {
    let mut reader = BufReader::with_capacity(129, stdout);
    let (mut count, mut total) = (0, 0);
    loop {
        let line = read_frame(&mut reader, 128).map_err(|error| match error.kind() {
            ErrorKind::InvalidInput | ErrorKind::InvalidData | ErrorKind::UnexpectedEof => {
                "FIXTURE_PROTOCOL_REJECTED"
            }
            _ => "FIXTURE_IO_FAILED",
        })?;
        let Some(line) = line else {
            return if count == 2 {
                Ok(())
            } else {
                Err("FIXTURE_PROTOCOL_REJECTED")
            };
        };
        let length = line.len();
        total += length;
        if length > 128 || total > 192 || line.last() != Some(&b'\n') || count >= 2 {
            return Err("FIXTURE_PROTOCOL_REJECTED");
        }
        let event: Event =
            serde_json::from_slice(&line).map_err(|_| "FIXTURE_PROTOCOL_REJECTED")?;
        match (count, event) {
            (0, Event::Boot { version: 1, pid: p }) if p == pid => {
                boot.try_send(()).map_err(|_| "FIXTURE_IO_FAILED")?;
            }
            (
                1,
                Event::Final {
                    version: 1,
                    stopped: true,
                },
            ) => {}
            _ => return Err("FIXTURE_PROTOCOL_REJECTED"),
        }
        count += 1;
    }
}
