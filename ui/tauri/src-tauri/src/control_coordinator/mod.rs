//! Internal preparation only; context matching is not peer authentication.
use crate::control_codec::{
    encode_frame,
    model::{Bootstrap, Message},
    scalar::Code,
};

pub(crate) struct Conversation {
    expected: Bootstrap,
}

impl Conversation {
    pub fn new(expected: Bootstrap) -> Self {
        Self { expected }
    }

    pub fn hello(&mut self) -> Result<Vec<u8>, Code> {
        encode_frame(&Message::Hello(self.expected.clone()))
    }

    pub fn receive(&mut self, _frame: &[u8]) -> Result<(), Code> {
        Ok(())
    }

    pub fn is_closed(&self) -> bool {
        false
    }
}
