use super::*;

#[tokio::test(flavor = "current_thread")]
async fn canceled_single_native_request_retains_connection_assignment() {
    use http_body_util::Full;
    use hyper::{body::Bytes, Request as HttpRequest};
    use hyper_util::{client::legacy::Client, rt::TokioExecutor};
    use tokio::{net::TcpListener, sync::oneshot};

    tokio::time::timeout(Duration::from_secs(2), async {
        // 自己所有の単一IPv4 loopback fixtureだけ。payloadやaddressを記録しない。
        let listener = TcpListener::bind(("127.0.0.1", 0)).await.unwrap();
        let address = listener.local_addr().unwrap();
        assert!(address.port() >= 1024);
        let (seen, received) = oneshot::channel();
        let server = tokio::spawn(async move {
            let (socket, _) = listener.accept().await.unwrap();
            let mut bytes = [0; 512];
            loop {
                socket.readable().await.unwrap();
                match socket.try_read(&mut bytes) {
                    Ok(count) => { assert!(count > 0); break; }
                    Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => continue,
                    Err(_) => panic!("owned fixture read failed"),
                }
            }
            let _ = seen.send(());
            std::future::pending::<()>().await;
        });
        let recorder = Recorder::memory();
        let connection = Connection::open(Some(recorder.clone()));
        let mut trace = connection.session();
        let mut outgoing = HttpRequest::post(format!("http://{address}/session"))
            .body(Full::new(Bytes::from_static(b"fixture"))).unwrap();
        trace.capture(&mut outgoing);
        trace.forward();
        let client = Client::builder(TokioExecutor::new())
            .retry_canceled_requests(false).build_http();
        {
            let response = client.request(outgoing);
            tokio::pin!(response);
            tokio::select! {
                _ = &mut response => panic!("fixture returned headers unexpectedly"),
                result = received => result.unwrap(),
            }
            // native headerを返さないfutureを一度だけ取り消す。
        }
        drop(trace);
        connection.end(Kind::Incomplete);
        server.abort();
        assert!(server.await.unwrap_err().is_cancelled());
        let rows: Vec<serde_json::Value> = recorder.snapshot().lines()
            .map(|line| serde_json::from_str(line).unwrap()).collect();
        assert_eq!(rows[4]["stage"], "native-connection-assigned");
        assert_eq!(rows[5]["stage"], "request-dropped");
        assert_eq!(rows[4]["request"], rows[5]["request"]);
        assert_eq!(rows.last().unwrap()["sealed"], true);
    }).await.expect("owned fixture deadline");
}


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
