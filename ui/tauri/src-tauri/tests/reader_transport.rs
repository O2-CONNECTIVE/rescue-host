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
