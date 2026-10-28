#[allow(dead_code, reason = "candidate runtime is not activated")]
#[path = "../src/control_codec/mod.rs"]
mod control_codec;
#[allow(dead_code, reason = "candidate runtime is not activated")]
#[path = "../src/control_coordinator/mod.rs"]
mod control_coordinator;

#[allow(dead_code, reason = "candidate runtime is not activated")]
#[path = "../src/owned_process/mod.rs"]
mod owned_process;

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
    let receipt = reply(
        "receipt",
        "2",
        "2",
        json!({"operation": "stop", "accepted": true}),
    );
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

#[test]
fn shared_close_conditions_remain_unconfirmed_without_protected_authority() {
    use control_codec::scalar::GoState;
    use control_coordinator::observations::{CloseAssessment, CloseObservations};
    use owned_process::owner::KillAttempt;
    let fixture: Value = serde_json::from_str(include_str!(
        "../../../../contracts/control-v1/vectors.json"
    ))
    .unwrap();
    let cases = fixture["terminationCases"].as_array().unwrap();
    assert_eq!(cases.len(), 5);
    for case in cases {
        // The fixture's Go Close premise is only a claim at this unwired IPC.
        let observations = CloseObservations {
            peer_close_claim: case["goCloseSuccess"].as_bool().unwrap(),
            peer_state: Some(if case["terminatedState"].as_bool().unwrap() {
                GoState::Terminated
            } else {
                GoState::Ready
            }),
            stop_receipt: false,
            context_matched: case["sameIdentity"].as_bool().unwrap(),
            channel_failed: false,
            reader_eof_completed_reported: case["actualEof"].as_bool().unwrap()
                && case["readerCompleted"].as_bool().unwrap(),
            direct_child_reaped: case["ownedExit0AndReaped"].as_bool().unwrap(),
            direct_child_exit0: case["ownedExit0AndReaped"].as_bool().unwrap(),
            cleanup_attempted: case["forcedCleanup"].as_bool().unwrap(),
            kill: KillAttempt::None,
        };
        let expected = if case["normalTermination"].as_bool().unwrap() {
            CloseAssessment::AuthorityNotEstablished
        } else {
            CloseAssessment::EvidenceIncomplete
        };
        assert_eq!(observations.assess(), expected, "{}", case["id"]);
    }
}

fn ready_reply() -> Value {
    serde_json::from_slice(&reply(
        "snapshot_result",
        "1",
        "1",
        json!({"goState": "ready", "pending": "0", "active": "0", "owned": "0", "goCloseConfirmed": false}),
    ))
    .unwrap()
}

#[test]
fn wrong_envelope_identity_or_response_kind_never_updates_the_claim() {
    let mut cases = Vec::new();
    for field in ["generation", "sequence", "requestId"] {
        let mut value = ready_reply();
        value[field] = json!("2");
        cases.push(value);
    }
    let mut identity = ready_reply();
    identity["identity"]["launchId"] = json!("other-launch");
    cases.push(identity);
    let mut receipt = ready_reply();
    receipt["kind"] = json!("receipt");
    receipt["payload"] = json!({"operation": "stop", "accepted": true});
    cases.push(receipt);
    assert_eq!(cases.len(), 5);
    for response in cases {
        let mut conversation = bound();
        conversation.request(Request::Snapshot).unwrap();
        assert_eq!(
            conversation.receive(&frame(response)),
            Err(Code::ContextMismatch)
        );
        assert!(conversation.is_closed());
        assert!(conversation.peer_snapshot_claim().is_none());
    }
}

#[test]
fn a_correlated_error_close_claim_is_not_a_snapshot_or_stop_receipt() {
    let mut conversation = bound();
    conversation.request(Request::Stop).unwrap();
    let response = reply(
        "error",
        "1",
        "1",
        json!({"code": "TERMINATION_UNCONFIRMED", "retryable": false, "goCloseConfirmed": true}),
    );
    conversation.receive(&response).unwrap();
    assert_eq!(
        conversation.peer_error_claim(),
        Some(Code::TerminationUnconfirmed)
    );
    assert!(conversation.peer_snapshot_claim().is_none());
    assert!(!conversation.peer_stop_receipt());
    assert!(!conversation.is_closed());
}

#[test]
fn a_reader_completion_report_seals_input_without_changing_peer_state() {
    let mut event = ready_reply();
    event["kind"] = json!("state_changed");
    event.as_object_mut().unwrap().remove("requestId");
    let event = frame(event);
    let mut open = bound();
    open.receive(&event).unwrap();
    let mut ended = bound();
    ended.end_input();
    assert!(ended.is_closed());
    assert_eq!(ended.receive(&event), Err(Code::TerminationUnconfirmed));
    assert_eq!(
        ended.request(Request::Snapshot),
        Err(Code::TerminationUnconfirmed)
    );
    assert!(ended.peer_snapshot_claim().is_none());
    assert!(ended.channel_fault().is_none());
}
