use super::{decoder, Mode};
use crate::owned_process::owner::{ChildOwner, CleanupBudget, KillAttempt};
use std::io::Write;
use std::process::{Command, ExitStatus, Stdio};
use std::sync::mpsc;
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

struct Owned {
    child: ChildOwner,
    reader: Option<JoinHandle<Result<(), &'static str>>>,
    status: Option<ExitStatus>,
    cleanup_attempted: bool,
}

impl Owned {
    fn exchange(&mut self) -> Result<(), &'static str> {
        let deadline = Instant::now() + Duration::from_secs(2);
        let stdout = self.child.take_stdout().ok_or("FIXTURE_IO_FAILED")?;
        let pid = self.child.id();
        let (boot, received) = mpsc::sync_channel(1);
        self.reader = Some(
            thread::Builder::new()
                .spawn(move || decoder::read(stdout, pid, boot))
                .map_err(|_| "FIXTURE_IO_FAILED")?,
        );
        received
            .recv_timeout(deadline.saturating_duration_since(Instant::now()))
            .map_err(|error| match error {
                mpsc::RecvTimeoutError::Timeout => "FIXTURE_DEADLINE",
                mpsc::RecvTimeoutError::Disconnected => "FIXTURE_PROTOCOL_REJECTED",
            })?;
        // Only one short fixed write; stdin EOF is part of the fixture contract.
        let mut input = self.child.take_stdin().ok_or("FIXTURE_IO_FAILED")?;
        let sent = input
            .write_all(b"{\"fixtureStop\":1}\n")
            .and_then(|()| input.flush());
        drop(input);
        sent.map_err(|_| "FIXTURE_IO_FAILED")?;
        while Instant::now() < deadline {
            self.status = self.child.try_reap().map_err(|_| "FIXTURE_IO_FAILED")?;
            if self
                .reader
                .as_ref()
                .is_some_and(|reader| reader.is_finished())
            {
                self.reader
                    .take()
                    .unwrap()
                    .join()
                    .map_err(|_| "FIXTURE_READER_FAILED")??;
                return match self.status {
                    Some(status) if status.success() => Ok(()),
                    Some(_) => Err("FIXTURE_NONZERO_EXIT"),
                    None => self.await_exit(deadline),
                };
            }
            thread::sleep(Duration::from_millis(5));
        }
        Err("FIXTURE_DEADLINE")
    }

    fn await_exit(&mut self, deadline: Instant) -> Result<(), &'static str> {
        while Instant::now() < deadline {
            self.status = self.child.try_reap().map_err(|_| "FIXTURE_IO_FAILED")?;
            if let Some(status) = self.status {
                return if status.success() {
                    Ok(())
                } else {
                    Err("FIXTURE_NONZERO_EXIT")
                };
            }
            thread::sleep(Duration::from_millis(5));
        }
        Err("FIXTURE_DEADLINE")
    }

    fn cleanup(&mut self, force: bool) -> Result<(), &'static str> {
        self.cleanup_attempted = true;
        self.child.close_stdin();
        let deadline = Instant::now() + Duration::from_secs(1);
        let observation = if force {
            self.child.cleanup_until(deadline)
        } else {
            self.child.wait_until(deadline)
        }
        .map_err(|_| "FIXTURE_CLEANUP_UNCONFIRMED")?;
        self.status = observation.status;
        if self.status.is_none() || (!force && observation.kill != KillAttempt::None) {
            return Err("FIXTURE_CLEANUP_UNCONFIRMED");
        }
        while Instant::now() < deadline {
            if self.reader.as_ref().is_none_or(|r| r.is_finished()) {
                if let Some(reader) = self.reader.take() {
                    reader
                        .join()
                        .map_err(|_| "FIXTURE_CLEANUP_UNCONFIRMED")?
                        .ok();
                }
                return Ok(());
            }
            thread::sleep(Duration::from_millis(5));
        }
        Err("FIXTURE_CLEANUP_UNCONFIRMED")
    }
}

impl Drop for Owned {
    fn drop(&mut self) {
        if !self.cleanup_attempted {
            let _ = self.cleanup(true);
        }
    }
}

pub fn run(mode: Mode) -> Result<(), &'static str> {
    let fallback = CleanupBudget::new(Duration::from_secs(1)).ok_or("FIXTURE_IO_FAILED")?;
    let executable = std::env::current_exe().map_err(|_| "FIXTURE_LAUNCH_FAILED")?;
    let child = Command::new(executable)
        .arg(mode.argument())
        .env_clear()
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .map_err(|_| "FIXTURE_LAUNCH_FAILED")?;
    let mut owned = Owned {
        child: ChildOwner::adopt(child, fallback),
        reader: None,
        status: None,
        cleanup_attempted: false,
    };
    let result = owned.exchange();
    owned.cleanup(result.is_err())?;
    result
}
