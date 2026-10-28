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
    let rows: Vec<serde_json::Value> = text.lines().map(|line| serde_json::from_str(line).unwrap()).collect();
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
