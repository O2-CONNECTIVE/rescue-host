use std::io;
use std::process::{Child, ChildStdin, ChildStdout, ExitStatus};
use std::thread;
use std::time::{Duration, Instant};

#[derive(Clone, Copy)]
pub(crate) struct CleanupBudget(Duration);

impl CleanupBudget {
    pub fn new(value: Duration) -> Option<Self> {
        if value.is_zero() || Instant::now().checked_add(value).is_none() {
            return None;
        }
        Some(Self(value))
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum KillAttempt {
    None,
    Requested,
    Failed,
}

/// Direct Child facts only: no pipe worker, protocol, Go Close or descendants.
#[must_use = "check direct Child facts separately from protocol completion"]
pub(crate) struct ExitObservation {
    pub status: Option<ExitStatus>,
    pub kill: KillAttempt,
}

pub(crate) struct ChildOwner {
    child: Child,
    status: Option<ExitStatus>,
    kill: KillAttempt,
    cleanup_attempted: bool,
    fallback: CleanupBudget,
}

impl ChildOwner {
    /// Ownership transfer from a trusted launcher, not artifact verification/authentication.
    /// No executable path, arguments, shell or PID lookup is accepted here.
    pub fn adopt(child: Child, fallback: CleanupBudget) -> Self {
        Self {
            child,
            status: None,
            kill: KillAttempt::None,
            cleanup_attempted: false,
            fallback,
        }
    }

    pub fn cleanup_was_attempted(&self) -> bool {
        self.cleanup_attempted
    }

    pub fn id(&self) -> u32 {
        self.child.id()
    }

    // Taking a pipe transfers its close and reader/writer completion duty to the caller.
    pub fn take_stdin(&mut self) -> Option<ChildStdin> {
        self.child.stdin.take()
    }

    pub fn take_stdout(&mut self) -> Option<ChildStdout> {
        self.child.stdout.take()
    }

    pub fn close_stdin(&mut self) {
        drop(self.child.stdin.take());
    }

    pub fn try_reap(&mut self) -> io::Result<Option<ExitStatus>> {
        if self.status.is_none() {
            self.status = self.child.try_wait()?;
        }
        Ok(self.status)
    }

    /// A wait deadline does not mean cleanup happened. None leaves ownership in self.
    pub fn wait_until(&mut self, deadline: Instant) -> io::Result<ExitObservation> {
        loop {
            self.try_reap()?;
            if self.status.is_some() || Instant::now() >= deadline {
                return Ok(ExitObservation {
                    status: self.status,
                    kill: self.kill,
                });
            }
            let remaining = deadline.saturating_duration_since(Instant::now());
            thread::sleep(remaining.min(Duration::from_millis(5)));
        }
    }

    /// Force cleanup targets the retained Child handle only. Kill is not exit evidence.
    pub fn cleanup_until(&mut self, deadline: Instant) -> io::Result<ExitObservation> {
        self.cleanup_attempted = true;
        self.close_stdin();
        if self.try_reap().ok().flatten().is_none() {
            self.kill = if self.child.kill().is_ok() {
                KillAttempt::Requested
            } else {
                KillAttempt::Failed
            };
        }
        self.wait_until(deadline)
    }
}

impl Drop for ChildOwner {
    fn drop(&mut self) {
        if !self.cleanup_attempted && self.status.is_none() {
            let deadline = Instant::now()
                .checked_add(self.fallback.0)
                .unwrap_or_else(Instant::now);
            // Best effort only. A coordinator must retain unconfirmed owners explicitly.
            let _ = self.cleanup_until(deadline);
        }
    }
}
