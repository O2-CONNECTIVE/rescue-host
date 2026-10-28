use crate::owned_process::frame::read_frame;
use crate::owned_process::owner::CleanupBudget;
use std::io::{Cursor, ErrorKind};
use std::time::Duration;

pub fn run() {
    let mut bytes = Cursor::new(b"{}\n{}\n");
    assert_eq!(read_frame(&mut bytes, 3).unwrap(), Some(b"{}\n".to_vec()));
    assert_eq!(read_frame(&mut bytes, 3).unwrap(), Some(b"{}\n".to_vec()));
    assert_eq!(read_frame(&mut bytes, 3).unwrap(), None);
    let mut too_long = Cursor::new(b"{}\n");
    assert_eq!(
        read_frame(&mut too_long, 2).unwrap_err().kind(),
        ErrorKind::InvalidData
    );
    let mut partial = Cursor::new(b"{}");
    assert_eq!(
        read_frame(&mut partial, 3).unwrap_err().kind(),
        ErrorKind::UnexpectedEof
    );
    let mut untouched = Cursor::new(b"{}\n");
    assert_eq!(
        read_frame(&mut untouched, 0).unwrap_err().kind(),
        ErrorKind::InvalidInput
    );
    assert_eq!(untouched.position(), 0);
    // Encoding and JSON interpretation belong to the codec, not this byte helper.
    let mut opaque = Cursor::new([0xff, b'\n']);
    assert_eq!(read_frame(&mut opaque, 2).unwrap(), Some(vec![0xff, b'\n']));
    assert!(CleanupBudget::new(Duration::ZERO).is_none());
    assert!(CleanupBudget::new(Duration::MAX).is_none());
    assert!(CleanupBudget::new(Duration::from_secs(1)).is_some());
    println!("owned-pipe: byte/budget regression assertions=11 passed=11");
}
