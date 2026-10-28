pub(crate) use super::object::Object;
use super::scalar::{Code, Decimal, GoState, Hex, Operation, Protocol, Token, WireBool};
use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(crate) struct Identity {
    pub launch_id: Token,
    pub epoch: Decimal<false>,
    pub policy_digest: Hex<64>,
}

// A binding value is data, not peer authentication or authorization evidence.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(crate) struct Binding {
    pub artifact_sha256: Hex<64>,
    pub channel_nonce: Hex<64>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct Bootstrap {
    pub protocol: Protocol,
    pub generation: Decimal<true>,
    pub identity: Object<Identity>,
    pub binding: Object<Binding>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(crate) struct Control<P> {
    pub protocol: Protocol,
    pub generation: Decimal<true>,
    pub sequence: Decimal<true>,
    pub request_id: Decimal<true>,
    pub identity: Object<Identity>,
    pub payload: Object<P>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct Event<P> {
    pub protocol: Protocol,
    pub generation: Decimal<true>,
    pub sequence: Decimal<true>,
    pub identity: Object<Identity>,
    pub payload: Object<P>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct Empty {}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(crate) struct Start {
    pub policy_ref: Token,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub(crate) struct Receipt {
    pub operation: Operation,
    pub accepted: WireBool<true>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(crate) struct Snapshot {
    pub go_state: GoState,
    pub pending: Decimal<false>,
    pub active: Decimal<false>,
    pub owned: Decimal<false>,
    pub go_close_confirmed: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub(crate) struct ErrorPayload {
    pub code: Code,
    pub retryable: WireBool<false>,
    pub go_close_confirmed: bool,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub(crate) enum Message {
    Hello(Bootstrap),
    HelloAck(Bootstrap),
    Start(Control<Start>),
    Snapshot(Control<Empty>),
    Stop(Control<Empty>),
    Receipt(Control<Receipt>),
    SnapshotResult(Control<Snapshot>),
    Error(Control<ErrorPayload>),
    StateChanged(Event<Snapshot>),
}
