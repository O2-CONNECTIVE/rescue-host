mod process_fixture;

fn main() {
    let arguments: Vec<String> = std::env::args().skip(1).collect();
    if arguments == ["--owned-fixture-v1=graceful"] {
        process_fixture::child();
        return;
    }
    assert!(arguments.is_empty(), "unexpected test argument");
    assert_eq!(process_fixture::run(), Ok(()));
    println!("owned-process-fixture: checks=1 passed=1");
}
