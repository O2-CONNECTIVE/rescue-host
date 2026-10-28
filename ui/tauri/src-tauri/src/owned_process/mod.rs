//! Internal transport/ownership preparation; no production launcher or IPC command.
//! Artifact trust, inherited-handle policy, Job ownership and protected principals
//! belong to the bootstrap/coordinator design. No backend is launched by this module.
pub(crate) mod frame;
pub(crate) mod owner;
