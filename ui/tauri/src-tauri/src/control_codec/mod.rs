//! Pure wire shape only. No IO, activation, authentication or replay/state authority.
pub(crate) mod model;
mod object;
pub(crate) mod scalar;

use model::{Message, Snapshot};
use object::Object;
use scalar::{Code, GoState, Protocol};
use serde::Deserialize;
use std::io::{self, Write};

pub(crate) const MAX_FRAME_BYTES: usize = 16_384;
pub(crate) const MAX_DEPTH: usize = 8;

fn check_raw(frame: &[u8]) -> Result<(), Code> {
    if frame.is_empty()
        || frame.len() > MAX_FRAME_BYTES
        || frame.last() != Some(&b'\n')
        || frame[..frame.len() - 1].contains(&b'\n')
        || frame.starts_with(b"\xef\xbb\xbf")
    {
        return Err(Code::InvalidMessage);
    }
    std::str::from_utf8(frame).map_err(|_| Code::InvalidMessage)?;
    let (mut depth, mut quoted, mut escaped) = (0usize, false, false);
    for &byte in frame {
        if quoted {
            if escaped {
                escaped = false;
            } else if byte == b'\\' {
                escaped = true;
            } else if byte == b'"' {
                quoted = false;
            }
        } else {
            match byte {
                b'"' => quoted = true,
                b'{' | b'[' => {
                    depth += 1;
                    if depth > MAX_DEPTH {
                        return Err(Code::InvalidMessage);
                    }
                }
                b'}' | b']' => {
                    depth = depth.checked_sub(1).ok_or(Code::InvalidMessage)?;
                }
                _ => {}
            }
        }
    }
    if quoted || depth != 0 {
        return Err(Code::InvalidMessage);
    }
    Ok(())
}

#[derive(Deserialize)]
struct Probe {
    protocol: String,
    #[serde(rename = "kind")]
    _kind: String,
}

fn valid_snapshot(value: &Snapshot) -> bool {
    !value.go_close_confirmed || value.go_state == GoState::Terminated
}

pub(crate) fn decode_frame(frame: &[u8]) -> Result<Message, Code> {
    check_raw(frame)?;
    // This probe only prioritizes the version code. Full typed parsing below still
    // validates every field/duplicate before any Message is returned.
    let probe: Object<Probe> = serde_json::from_slice(frame).map_err(|_| Code::InvalidMessage)?;
    if probe.0.protocol != Protocol::V1.as_str() {
        return Err(Code::VersionMismatch);
    }
    let message: Message = serde_json::from_slice(frame).map_err(|_| Code::InvalidMessage)?;
    let valid = match &message {
        Message::SnapshotResult(value) => valid_snapshot(&value.payload.0),
        Message::StateChanged(value) => valid_snapshot(&value.payload.0),
        _ => true,
    };
    if !valid {
        return Err(Code::InvalidMessage);
    }
    Ok(message)
}

// Reserve one byte for the terminal LF; never extend beyond the wire budget.
struct FrameWriter(Vec<u8>);

impl Write for FrameWriter {
    fn write(&mut self, bytes: &[u8]) -> io::Result<usize> {
        let length = self.0.len().checked_add(bytes.len());
        if length.is_none_or(|length| length >= MAX_FRAME_BYTES) {
            return Err(io::Error::other("frame limit exceeded"));
        }
        self.0.extend_from_slice(bytes);
        Ok(bytes.len())
    }

    fn flush(&mut self) -> io::Result<()> {
        Ok(())
    }
}

pub(crate) fn encode_frame(message: &Message) -> Result<Vec<u8>, Code> {
    let mut writer = FrameWriter(Vec::with_capacity(MAX_FRAME_BYTES));
    serde_json::to_writer(&mut writer, message).map_err(|_| Code::InvalidMessage)?;
    writer.0.push(b'\n');
    // Internal callers can construct cross-field-invalid snapshots. Apply the
    // same shape constraint before handing bytes to any future transport.
    decode_frame(&writer.0)?;
    Ok(writer.0)
}

#[cfg(test)]
mod tests {
    #[test]
    fn frame_writer_reserves_lf_and_rejects_before_extension() {
        let mut writer = super::FrameWriter(Vec::with_capacity(super::MAX_FRAME_BYTES));
        let data = vec![b'x'; super::MAX_FRAME_BYTES - 1];
        std::io::Write::write_all(&mut writer, &data).unwrap();
        assert!(std::io::Write::write_all(&mut writer, b"x").is_err());
        assert_eq!(writer.0.len(), super::MAX_FRAME_BYTES - 1);
        writer.0.push(b'\n');
        assert_eq!(writer.0.len(), super::MAX_FRAME_BYTES);
    }
}
