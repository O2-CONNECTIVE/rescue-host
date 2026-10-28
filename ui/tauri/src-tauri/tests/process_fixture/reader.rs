use super::Mode;
use crate::control_codec::scalar::Code;
use crate::control_coordinator::connection::{ReaderConnection, ReaderProgress};
use crate::control_coordinator::observations::CloseAssessment;
use crate::control_coordinator::owned::OwnedCoordinator;
use crate::control_coordinator::reader::ReaderTask;
use crate::control_coordinator::Conversation;
use crate::owned_process::owner::{ChildOwner, CleanupBudget, KillAttempt};
use std::io::Write;
use std::process::{Command, Stdio};
use std::thread;
use std::time::{Duration, Instant};

pub fn run() {
    let child = Command::new(std::env::current_exe().unwrap())
        .arg(Mode::Graceful.argument())
        .env_clear()
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    let budget = CleanupBudget::new(Duration::from_secs(1)).unwrap();
    let mut child = ChildOwner::adopt(child, budget);
    let stdout = child.take_stdout().unwrap();
    let mut stdin = child.take_stdin().unwrap();
    // Existing finite fixture v1 only. It is deliberately not product control/1.
    stdin.write_all(b"{\"fixtureStop\":1}\n").unwrap();
    drop(stdin);
    let deadline = Instant::now() + Duration::from_secs(2);
    let exit = child.wait_until(deadline).unwrap();
    assert!(exit.status.unwrap().success());
    assert_eq!(exit.kill, KillAttempt::None);
    assert!(!child.cleanup_was_attempted());

    let expected = serde_json::from_value(serde_json::json!({
        "protocol": "rescue-host-control/1", "generation": "1",
        "identity": {"launchId": "launch-001", "epoch": "1", "policyDigest": "a".repeat(64)},
        "binding": {"artifactSha256": "b".repeat(64), "channelNonce": "c".repeat(64)}
    }))
    .unwrap();
    let coordinator = OwnedCoordinator::new(Conversation::new(expected), child);
    let reader = ReaderTask::start(stdout).unwrap();
    let mut connection = ReaderConnection::new(coordinator, reader);
    connection.hello().unwrap();
    connection.poll_child().unwrap();
    let observed = connection.target().observations();
    assert!(observed.direct_child_reaped, "actual reap must reach the adapter");
    assert!(observed.direct_child_exit0);
    assert!(!observed.reader_eof_completed_reported);
    assert_eq!(observed.assess(), CloseAssessment::EvidenceIncomplete);

    let deadline = Instant::now() + Duration::from_secs(2);
    loop {
        assert!(Instant::now() < deadline, "finite owned reader deadline");
        match connection.poll() {
            ReaderProgress::Pending => thread::sleep(Duration::from_millis(5)),
            ReaderProgress::Fault(Code::InvalidMessage) | ReaderProgress::Discarded => {}
            ReaderProgress::Joined { fault, .. } => {
                assert_eq!(fault, Some(Code::InvalidMessage));
                break;
            }
            event => panic!("unexpected fixture reader event: {event:?}"),
        }
    }
    let observed = connection.target().observations();
    assert!(observed.direct_child_reaped && observed.direct_child_exit0);
    assert!(!observed.peer_close_claim && !observed.reader_eof_completed_reported);
    assert!(observed.channel_failed);
    assert_eq!(observed.assess(), CloseAssessment::EvidenceIncomplete);
    println!("owned-reader: existing-stdio-rejected-and-reap-observed checks=1 passed=1");
}
