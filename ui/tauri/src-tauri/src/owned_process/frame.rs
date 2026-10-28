use std::io::{self, BufRead};

// Transport bytes only. JSON fields and product limits belong to the codec owner.
pub fn read_frame(_reader: &mut impl BufRead, _max_bytes: u16) -> io::Result<Option<Vec<u8>>> {
    Ok(None)
}
