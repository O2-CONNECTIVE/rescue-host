mod frame_checks;
#[path = "../src/owned_process/mod.rs"]
mod owned_process;
mod process_fixture;

use process_fixture::Mode;

fn main() {
    let arguments: Vec<String> = std::env::args().skip(1).collect();
    if let [argument] = arguments.as_slice() {
        let mode = Mode::from_argument(argument).expect("unexpected fixture mode");
        process_fixture::child(mode);
        return;
    }
    assert!(arguments.is_empty(), "unexpected test argument");
    let mut bytes = std::io::Cursor::new(b"{}\n");
    assert_eq!(
        owned_process::frame::read_frame(&mut bytes, 3).unwrap(),
        Some(b"{}\n".to_vec())
    );
    println!("owned-pipe: byte-frame checks=1 passed=1");
    frame_checks::run();
    let cases = [
        (Mode::Graceful, Ok(())),
        (Mode::Overlong, Err("FIXTURE_PROTOCOL_REJECTED")),
        (Mode::ExtraOutput, Err("FIXTURE_PROTOCOL_REJECTED")),
        (Mode::NonzeroExit, Err("FIXTURE_NONZERO_EXIT")),
        (Mode::FinalWithoutExit, Err("FIXTURE_DEADLINE")),
    ];
    let mut checked = 0;
    for (mode, expected) in cases {
        assert_eq!(process_fixture::run(mode), expected, "{}", mode.argument());
        checked += 1;
        println!("owned-process-fixture: case={} passed", mode.argument());
    }
    assert!(checked > 0, "zero fixture checks rejected");
    println!("owned-process-fixture: checks={checked} passed={checked}");
}
