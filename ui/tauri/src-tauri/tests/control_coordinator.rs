#[allow(dead_code, reason = "candidate runtime is not activated")]
#[path = "../src/control_codec/mod.rs"]
mod control_codec;
#[allow(dead_code, reason = "candidate runtime is not activated")]
#[path = "../src/control_coordinator/mod.rs"]
mod control_coordinator;

use control_codec::{model::Bootstrap, scalar::Code};
use control_coordinator::Conversation;
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
