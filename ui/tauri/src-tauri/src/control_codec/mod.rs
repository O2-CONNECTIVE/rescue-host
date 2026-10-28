pub(crate) const MAX_FRAME_BYTES: usize = 16_384;
pub(crate) const MAX_DEPTH: usize = 8;

#[derive(Debug, PartialEq, Eq)]
pub(crate) enum Code {
    InvalidMessage,
}

pub(crate) struct Message;

pub(crate) fn decode_frame(_frame: &[u8]) -> Result<Message, Code> {
    Err(Code::InvalidMessage)
}
