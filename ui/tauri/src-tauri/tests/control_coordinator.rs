#[allow(dead_code, reason = "candidate runtime is not activated")]
#[path = "../src/control_codec/mod.rs"]
mod control_codec;
#[allow(dead_code, reason = "candidate runtime is not activated")]
#[path = "../src/control_coordinator/mod.rs"]
mod control_coordinator;

use control_codec::{model::Bootstrap, scalar::Code};
use control_coordinator::{Conversation, Request};
use serde_json::{json, Value};

fn expected() -> Bootstrap {
    serde_json::from_value(json!({
        "protocol": "rescue-host-control/1", "generation": "1",
        "identity": {"launchId": "launch-001", "epoch": "1", "policyDigest": "a".repeat(64)},
        "binding": {"artifactSha256": "b".repeat(64), "channelNonce": "c".repeat(64)}
    }))
    .unwrap()
}

fn frame(value: Value) -> Vec<u8> {
    let mut frame = serde_json::to_vec(&value).unwrap();
    frame.push(b'\n');
    frame
}

#[test]
fn mismatched_ack_closes_without_learning_the_peer_context() {
    let mut conversation = Conversation::new(expected());
    let mut hello: Value = serde_json::from_slice(&conversation.hello().unwrap()).unwrap();
    assert_eq!(hello["kind"], "hello");
    hello["kind"] = json!("hello_ack");
    hello["generation"] = json!("2");
    assert_eq!(
        conversation.receive(&frame(hello)),
        Err(Code::ContextMismatch)
    );
    assert!(conversation.is_closed());
}

fn bound() -> Conversation {
    let mut conversation = Conversation::new(expected());
    conversation.hello().unwrap();
    let mut ack = serde_json::to_value(expected()).unwrap();
    ack["kind"] = json!("hello_ack");
    conversation.receive(&frame(ack)).unwrap();
    conversation
}

fn reply(kind: &str, sequence: &str, request_id: &str, payload: Value) -> Vec<u8> {
    frame(json!({
        "protocol": "rescue-host-control/1", "kind": kind, "generation": "1",
        "identity": {"launchId": "launch-001", "epoch": "1", "policyDigest": "a".repeat(64)},
        "sequence": sequence, "requestId": request_id, "payload": payload
    }))
}

#[test]
fn reserved_stop_and_current_responses_are_correlated_without_replay() {
    let mut conversation = bound();
    let snapshot_frame = conversation.request(Request::Snapshot).unwrap();
    let snapshot: Value = serde_json::from_slice(&snapshot_frame).unwrap();
    assert_eq!(snapshot["sequence"], "1");
    assert_eq!(snapshot["requestId"], "1");
    assert_eq!(
        conversation.request(Request::Snapshot),
        Err(Code::RequestRejected)
    );
    let stop_frame = conversation.request(Request::Stop).unwrap();
    let stop: Value = serde_json::from_slice(&stop_frame).unwrap();
    assert_eq!(stop["kind"], "stop");
    assert_eq!(stop["sequence"], "2");
    assert_eq!(stop["requestId"], "2");
    let ready = json!({
        "goState": "ready", "pending": "0", "active": "0", "owned": "0",
        "goCloseConfirmed": false
    });
    let snapshot = reply("snapshot_result", "1", "1", ready);
    conversation.receive(&snapshot).unwrap();
    assert_eq!(
        conversation.peer_snapshot_claim().unwrap().go_state,
        control_codec::scalar::GoState::Ready
    );
    assert!(!conversation.peer_stop_receipt());
    let receipt = reply("receipt", "2", "2", json!({"operation": "stop", "accepted": true}));
    conversation.receive(&receipt).unwrap();
    assert!(conversation.peer_stop_receipt());
    assert_eq!(
        conversation.peer_snapshot_claim().unwrap().go_state,
        control_codec::scalar::GoState::Ready
    );
    let terminated = json!({
        "goState": "terminated", "pending": "0", "active": "0", "owned": "0",
        "goCloseConfirmed": true
    });
    let late = reply("snapshot_result", "3", "1", terminated);
    assert_eq!(conversation.receive(&late), Err(Code::ContextMismatch));
    assert!(conversation.is_closed());
    assert_eq!(
        conversation.peer_snapshot_claim().unwrap().go_state,
        control_codec::scalar::GoState::Ready
    );
}
