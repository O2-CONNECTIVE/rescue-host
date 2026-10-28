use crate::control_codec::scalar::Code;
use crate::control_coordinator::connection::ReaderConnection;
use crate::control_coordinator::Conversation;
use crate::ShellStatus;
use std::sync::Mutex;

/// Default runtime has no endpoint. Monitoring never establishes authority,
/// enables an operation, or owns the protected service's backend process.
#[derive(Default)]
pub(crate) struct ShellRuntime {
    monitor: Mutex<Option<ReaderConnection<Conversation>>>,
}

impl ShellRuntime {
    #[allow(dead_code, reason = "authenticated endpoint bootstrap is not wired")]
    pub fn monitoring(connection: ReaderConnection<Conversation>) -> Self {
        Self {
            monitor: Mutex::new(Some(connection)),
        }
    }

    pub fn status(&self) -> Result<ShellStatus, &'static str> {
        let mut monitor = self
            .monitor
            .lock()
            .map_err(|_| Code::TerminationUnconfirmed.as_str())?;
        if let Some(connection) = monitor.as_mut() {
            // Poll one event only; blocking Read never runs under this mutex.
            let _ = connection.poll();
            if let Some(code) = connection.observation().fault {
                return Err(code.as_str());
            }
        }
        // Even active/terminated peer claims and a joined reader do not admit
        // the currently missing authenticated endpoint and approval authority.
        Ok(ShellStatus::unwired())
    }
}
