use super::*;

#[test]
fn one_forward_is_correlated_with_drop_and_incoming_eof() {
    let recorder = Recorder::memory();
    let connection = Connection::open(Some(recorder.clone()));
    let mut request = connection.session();
    request.forward();
    drop(request);
    connection.end(Kind::Incomplete);
    let text = recorder.snapshot();
    let rows: Vec<serde_json::Value> = text
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect();
    assert_eq!(rows[2]["stage"], "session-received");
    assert_eq!(rows[3]["stage"], "forward-start");
    assert_eq!(rows[4]["stage"], "request-dropped");
    assert_eq!(rows[5]["stage"], "connection-end");
    assert_eq!(rows[3]["request"], rows[5]["request"]);
    assert_eq!(rows[3]["connection"], rows[5]["connection"]);
    assert_eq!(rows[5]["error"], "incomplete");
    assert_eq!(rows[6]["sealed"], true);
    assert_eq!(rows[6]["attempted"], 5);
    assert_eq!(rows[6]["dropped"], 0);
}

#[test]
fn incoming_eof_can_precede_service_future_drop_without_losing_slot() {
    let recorder = Recorder::memory();
    let connection = Connection::open(Some(recorder.clone()));
    let mut request = connection.session();
    request.forward();
    connection.end(Kind::Incomplete);
    drop(request);
    let rows: Vec<serde_json::Value> = recorder
        .snapshot()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect();
    assert_eq!(rows[4]["stage"], "connection-end");
    assert_eq!(rows[4]["state"], "forward-pending");
    assert_eq!(rows[5]["stage"], "request-dropped");
    assert_eq!(rows[4]["request"], rows[5]["request"]);
    assert_eq!(rows.last().unwrap()["sealed"], true);
}

#[test]
fn header_return_and_clean_connection_end_are_distinct_stages() {
    let recorder = Recorder::memory();
    let connection = Connection::open(Some(recorder.clone()));
    let mut request = connection.session();
    request.forward();
    request.headers();
    drop(request);
    connection.end(Kind::None);
    let rows: Vec<serde_json::Value> = recorder
        .snapshot()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect();
    assert_eq!(rows[4]["stage"], "native-headers");
    assert_eq!(rows[5]["stage"], "connection-end");
    assert_eq!(rows[5]["error"], "none");
    assert_eq!(rows.last().unwrap()["pendingRequests"], 0);
}

#[test]
fn fixed_ring_overflow_cannot_lose_the_independent_drop_count() {
    let recorder = Recorder::memory();
    for _ in 0..20 {
        Connection::open(Some(recorder.clone())).end(Kind::None);
    }
    let text = recorder.snapshot();
    let rows: Vec<serde_json::Value> = text
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect();
    let summary = rows.last().unwrap();
    assert_eq!(summary["attempted"], 40);
    assert_eq!(summary["enqueued"], 32);
    assert_eq!(summary["written"], 32);
    assert_eq!(summary["dropped"], 8);
    assert!(text.len() <= 8192);
    assert!(text.lines().all(|line| line.len() <= 256));
}

#[test]
fn live_request_makes_final_snapshot_unsealed() {
    let recorder = Recorder::memory();
    let connection = Connection::open(Some(recorder.clone()));
    let request = connection.session();
    let rows: Vec<serde_json::Value> = recorder
        .snapshot()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect();
    assert_eq!(rows.last().unwrap()["sealed"], false);
    assert_eq!(rows.last().unwrap()["pendingRequests"], 1);
    drop(request);
}
