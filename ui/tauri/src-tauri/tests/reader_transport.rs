#[allow(dead_code, reason = "candidate runtime is not activated")]
#[path = "../src/control_codec/mod.rs"]
mod control_codec;
#[allow(dead_code, reason = "candidate runtime is not activated")]
#[path = "../src/control_coordinator/mod.rs"]
mod control_coordinator;
#[allow(dead_code, reason = "candidate runtime is not activated")]
#[path = "../src/owned_process/mod.rs"]
mod owned_process;

use control_coordinator::reader::{ReaderEnd, ReaderEvent, ReaderTask};
use std::io::Cursor;
use std::thread;
use std::time::{Duration, Instant};

#[test]
fn worker_forwards_the_frame_before_joining_eof() {
    let mut reader = ReaderTask::start(Cursor::new(b"{}\n")).unwrap();
    let deadline = Instant::now() + Duration::from_secs(2);
    let mut frames = Vec::new();
    loop {
        assert!(Instant::now() < deadline, "finite worker deadline");
        match reader.poll() {
            ReaderEvent::Frame(frame) => frames.push(frame),
            ReaderEvent::Pending => thread::sleep(Duration::from_millis(5)),
            ReaderEvent::Joined(end) => {
                assert_eq!(frames, vec![b"{}\n".to_vec()]);
                assert_eq!(end, ReaderEnd::Eof);
                assert_eq!(reader.poll(), ReaderEvent::Closed);
                break;
            }
            ReaderEvent::Closed => panic!("join result was not observed"),
        }
    }
}

fn expected_context() -> control_codec::model::Bootstrap {
    serde_json::from_value(serde_json::json!({
        "protocol": "rescue-host-control/1", "generation": "1",
        "identity": {"launchId": "launch-001", "epoch": "1", "policyDigest": "a".repeat(64)},
        "binding": {"artifactSha256": "b".repeat(64), "channelNonce": "c".repeat(64)}
    }))
    .unwrap()
}

#[test]
fn rejected_frame_seals_control_and_later_eof_cannot_clear_the_fault() {
    use control_codec::scalar::Code;
    use control_coordinator::connection::{ReaderConnection, ReaderProgress};
    use control_coordinator::{Conversation, Request};
    let mut ack = serde_json::to_value(expected_context()).unwrap();
    ack["kind"] = serde_json::json!("hello_ack");
    let mut bytes = b"not-json\n".to_vec();
    bytes.extend(serde_json::to_vec(&ack).unwrap());
    bytes.push(b'\n');
    let reader = ReaderTask::start(Cursor::new(bytes)).unwrap();
    let mut connection = ReaderConnection::new(Conversation::new(expected_context()), reader);
    connection.hello().unwrap();
    let deadline = Instant::now() + Duration::from_secs(2);
    loop {
        assert!(Instant::now() < deadline, "finite rejection deadline");
        match connection.poll() {
            ReaderProgress::Pending => thread::sleep(Duration::from_millis(5)),
            event => {
                assert_eq!(event, ReaderProgress::Fault(Code::InvalidMessage));
                break;
            }
        }
    }
    assert!(connection.target().is_closed());
    assert_eq!(connection.request(Request::Stop), Err(Code::InvalidMessage));
    loop {
        assert!(Instant::now() < deadline, "finite reader join deadline");
        match connection.poll() {
            ReaderProgress::Pending => thread::sleep(Duration::from_millis(5)),
            ReaderProgress::Discarded => {}
            ReaderProgress::Joined { fault, .. } => {
                assert_eq!(fault, Some(Code::InvalidMessage));
                break;
            }
            event => panic!("unexpected after rejection: {event:?}"),
        }
    }
    assert!(!connection.target().context_matched());
    assert_eq!(connection.observation().fault, Some(Code::InvalidMessage));
    assert!(connection.target().peer_snapshot_claim().is_none());
}

fn wire(value: serde_json::Value) -> Vec<u8> {
    let mut bytes = serde_json::to_vec(&value).unwrap();
    bytes.push(b'\n');
    bytes
}

fn next_frame(
    connection: &mut control_coordinator::connection::ReaderConnection<
        control_coordinator::Conversation,
    >,
) {
    use control_coordinator::connection::ReaderProgress;
    let deadline = Instant::now() + Duration::from_secs(2);
    loop {
        assert!(Instant::now() < deadline, "finite typed frame deadline");
        match connection.poll() {
            ReaderProgress::Pending => thread::sleep(Duration::from_millis(5)),
            event => {
                assert_eq!(event, ReaderProgress::Frame);
                break;
            }
        }
    }
}

#[test]
fn worker_drives_snapshot_stop_claims_and_eof_without_authorizing_termination() {
    use control_codec::scalar::{Code, GoState};
    use control_coordinator::connection::{ReaderConnection, ReaderProgress};
    use control_coordinator::{Conversation, Request};
    use serde_json::json;
    let bootstrap = serde_json::to_value(expected_context()).unwrap();
    let mut ack = bootstrap.clone();
    ack["kind"] = json!("hello_ack");
    let mut bytes = wire(ack);
    bytes.extend(wire(json!({
        "protocol": "rescue-host-control/1", "kind": "snapshot_result", "generation": "1",
        "identity": bootstrap["identity"], "sequence": "1", "requestId": "1",
        "payload": {"goState": "ready", "pending": "0", "active": "0", "owned": "0", "goCloseConfirmed": false}
    })));
    bytes.extend(wire(json!({
        "protocol": "rescue-host-control/1", "kind": "receipt", "generation": "1",
        "identity": bootstrap["identity"], "sequence": "2", "requestId": "2",
        "payload": {"operation": "stop", "accepted": true}
    })));
    bytes.extend(wire(json!({
        "protocol": "rescue-host-control/1", "kind": "state_changed", "generation": "1",
        "identity": bootstrap["identity"], "sequence": "3",
        "payload": {"goState": "terminated", "pending": "0", "active": "0", "owned": "0", "goCloseConfirmed": true}
    })));
    let reader = ReaderTask::start(Cursor::new(bytes)).unwrap();
    let mut connection = ReaderConnection::new(Conversation::new(expected_context()), reader);
    connection.hello().unwrap();
    next_frame(&mut connection);
    let snapshot = connection.request(Request::Snapshot).unwrap();
    let stop = connection.request(Request::Stop).unwrap();
    let snapshot: serde_json::Value = serde_json::from_slice(&snapshot).unwrap();
    let stop: serde_json::Value = serde_json::from_slice(&stop).unwrap();
    assert_eq!(snapshot["requestId"], "1");
    assert_eq!(stop["kind"], "stop");
    assert_eq!(stop["sequence"], "2");
    assert_eq!(stop["requestId"], "2");
    next_frame(&mut connection);
    assert_eq!(
        connection.target().peer_snapshot_claim().unwrap().go_state,
        GoState::Ready
    );
    assert!(!connection.target().peer_stop_receipt());
    next_frame(&mut connection);
    assert!(connection.target().peer_stop_receipt());
    assert_eq!(
        connection.target().peer_snapshot_claim().unwrap().go_state,
        GoState::Ready
    );
    next_frame(&mut connection);
    assert_eq!(
        connection.target().peer_snapshot_claim().unwrap().go_state,
        GoState::Terminated
    );
    let deadline = Instant::now() + Duration::from_secs(2);
    loop {
        assert!(Instant::now() < deadline, "finite typed EOF deadline");
        match connection.poll() {
            ReaderProgress::Pending => thread::sleep(Duration::from_millis(5)),
            event => {
                assert_eq!(
                    event,
                    ReaderProgress::Joined {
                        end: ReaderEnd::Eof,
                        fault: None
                    }
                );
                break;
            }
        }
    }
    assert!(connection.target().is_closed());
    assert_eq!(
        connection.request(Request::Snapshot),
        Err(Code::TerminationUnconfirmed)
    );
    let (target, mut reader, observation) = connection.into_parts();
    assert!(target.peer_snapshot_claim().unwrap().go_close_confirmed);
    assert_eq!(observation.reader_join, Some(ReaderEnd::Eof));
    assert_eq!(observation.fault, None);
    assert_eq!(reader.poll(), ReaderEvent::Closed);
}

#[test]
fn an_actual_reader_rejects_a_different_generation_ack() {
    use control_codec::scalar::Code;
    use control_coordinator::connection::{ReaderConnection, ReaderProgress};
    use control_coordinator::Conversation;
    let mut ack = serde_json::to_value(expected_context()).unwrap();
    ack["kind"] = serde_json::json!("hello_ack");
    ack["generation"] = serde_json::json!("2");
    let reader = ReaderTask::start(Cursor::new(wire(ack))).unwrap();
    let mut connection = ReaderConnection::new(Conversation::new(expected_context()), reader);
    connection.hello().unwrap();
    let deadline = Instant::now() + Duration::from_secs(2);
    loop {
        assert!(
            Instant::now() < deadline,
            "finite context rejection deadline"
        );
        match connection.poll() {
            ReaderProgress::Pending => thread::sleep(Duration::from_millis(5)),
            ReaderProgress::Fault(Code::ContextMismatch) => {}
            ReaderProgress::Joined { fault, .. } => {
                assert_eq!(fault, Some(Code::ContextMismatch));
                break;
            }
            event => panic!("unexpected generation event: {event:?}"),
        }
    }
    assert!(!connection.target().context_matched());
    assert!(connection.target().is_closed());
}

#[test]
fn a_typed_transport_fault_seals_before_an_already_queued_ack() {
    use control_codec::scalar::Code;
    use control_coordinator::connection::{ReaderConnection, ReaderProgress};
    use control_coordinator::{Conversation, Request};
    let mut ack = serde_json::to_value(expected_context()).unwrap();
    ack["kind"] = serde_json::json!("hello_ack");
    let reader = ReaderTask::start(Cursor::new(wire(ack))).unwrap();
    let mut connection = ReaderConnection::new(Conversation::new(expected_context()), reader);
    connection.hello().unwrap();
    connection.transport_failed(Code::VersionMismatch);
    assert_eq!(
        connection.request(Request::Stop),
        Err(Code::VersionMismatch)
    );
    let deadline = Instant::now() + Duration::from_secs(2);
    loop {
        assert!(
            Instant::now() < deadline,
            "finite typed fault join deadline"
        );
        match connection.poll() {
            ReaderProgress::Pending => thread::sleep(Duration::from_millis(5)),
            ReaderProgress::Discarded => {}
            ReaderProgress::Joined { fault, .. } => {
                assert_eq!(fault, Some(Code::VersionMismatch));
                break;
            }
            event => panic!("queued input crossed a sealed transport: {event:?}"),
        }
    }
    assert!(!connection.target().context_matched());
}

#[test]
fn transport_fault_after_event_acquisition_precedes_queued_frame_processing() {
    use control_codec::scalar::Code;
    use control_coordinator::connection::{ReaderConnection, ReaderProgress};
    use control_coordinator::{Conversation, Request};
    use std::io::{self, Read};
    use std::sync::mpsc;

    struct SignalEof {
        bytes: Cursor<Vec<u8>>,
        eof: mpsc::Sender<()>,
    }
    impl Read for SignalEof {
        fn read(&mut self, output: &mut [u8]) -> io::Result<usize> {
            let count = self.bytes.read(output)?;
            if count == 0 {
                let _ = self.eof.send(());
            }
            Ok(count)
        }
    }

    let mut ack = serde_json::to_value(expected_context()).unwrap();
    ack["kind"] = serde_json::json!("hello_ack");
    ack["generation"] = serde_json::json!("2");
    let (eof, observed) = mpsc::channel();
    let reader = ReaderTask::start(SignalEof {
        bytes: Cursor::new(wire(ack)),
        eof,
    })
    .unwrap();
    // The worker enqueues the complete frame before asking for the next read.
    // This is a deterministic ordering witness, not a scheduler delay.
    observed.recv_timeout(Duration::from_secs(2)).unwrap();
    let mut connection = ReaderConnection::new(Conversation::new(expected_context()), reader);
    connection.hello().unwrap();
    assert_eq!(connection.observation().fault, None);
    assert_eq!(
        connection.poll_with_transport_fault(|| Some(Code::VersionMismatch)),
        ReaderProgress::Discarded
    );
    assert_eq!(connection.observation().fault, Some(Code::VersionMismatch));
    assert_eq!(connection.request(Request::Stop), Err(Code::VersionMismatch));
    assert!(!connection.target().context_matched());
    let joined = connection
        .cancel_and_join_until(Instant::now() + Duration::from_secs(2))
        .unwrap();
    assert_eq!(joined.fault, Some(Code::VersionMismatch));
    assert!(joined.reader_join.is_some());
}
