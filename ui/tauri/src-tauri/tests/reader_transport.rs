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
