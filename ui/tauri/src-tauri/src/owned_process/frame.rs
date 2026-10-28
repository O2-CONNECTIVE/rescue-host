use std::io::{self, BufRead, Read};

// Transport bytes only. JSON fields and product limits belong to the codec owner.
// The u16 limit bounds this allocation; caller/OS buffers are outside this helper.
pub fn read_frame(reader: &mut impl BufRead, max_bytes: u16) -> io::Result<Option<Vec<u8>>> {
    if max_bytes == 0 {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "FRAME_LIMIT_ZERO",
        ));
    }
    let probe = usize::from(max_bytes) + 1;
    let mut line = Vec::with_capacity(probe);
    let length = reader.take(probe as u64).read_until(b'\n', &mut line)?;
    if length == 0 {
        return Ok(None);
    }
    if length > usize::from(max_bytes) {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "FRAME_TOO_LARGE",
        ));
    }
    if line.last() != Some(&b'\n') {
        return Err(io::Error::new(
            io::ErrorKind::UnexpectedEof,
            "FRAME_INCOMPLETE",
        ));
    }
    Ok(Some(line))
}
