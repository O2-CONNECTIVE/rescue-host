// 観測の序数と最後の操作だけを保持する。上限で実際の登録処理は止めない。
#[derive(Clone, Copy)]
pub(crate) enum Signal {
    ApiStart,
    ApiReturn,
    Callback(bool),
    Sent,
    Return(bool),
}
#[derive(Clone)]
pub(crate) struct Scripts {
    pub calls: u32,
    api_returns: u32,
    callbacks: u32,
    sends: u32,
    returns: u32,
    pub pump_ordinal: u32,
    started: Option<u128>,
    api_start: Option<u128>,
    api_return: Option<u128>,
    callback: Option<u128>,
    sent: Option<u128>,
    returned: Option<u128>,
    result: &'static str,
    callback_ok: Option<bool>,
    pub invalid: bool,
}
impl Scripts {
    pub fn empty() -> Self {
        Self {
            calls: 0,
            api_returns: 0,
            callbacks: 0,
            sends: 0,
            returns: 0,
            pump_ordinal: 0,
            started: None,
            api_start: None,
            api_return: None,
            callback: None,
            sent: None,
            returned: None,
            result: "idle",
            callback_ok: None,
            invalid: false,
        }
    }
    pub fn begin(&mut self, now: u128) -> u32 {
        if self.invalid || self.calls >= 128 || (self.calls > 0 && self.result != "ok") {
            self.invalid = true;
            return 0;
        }
        self.calls += 1;
        self.pump_ordinal = 0;
        self.started = Some(now);
        self.api_start = None;
        self.api_return = None;
        self.callback = None;
        self.sent = None;
        self.returned = None;
        self.result = "pending";
        self.callback_ok = None;
        self.calls
    }
    pub fn update(&mut self, ordinal: u32, signal: Signal, now: u128) {
        let valid = ordinal > 0
            && ordinal == self.calls
            && self.result == "pending"
            && match signal {
                Signal::ApiStart => self.api_start.is_none(),
                Signal::ApiReturn => self.api_start.is_some() && self.api_return.is_none(),
                Signal::Callback(_) => self.api_start.is_some() && self.callback.is_none(),
                Signal::Sent => self.callback.is_some() && self.sent.is_none(),
                Signal::Return(ok) => self.api_return.is_some() && (!ok || self.sent.is_some()),
            };
        if !valid || self.invalid {
            self.invalid = true;
            return;
        }
        match signal {
            Signal::ApiStart => self.api_start = Some(now),
            Signal::ApiReturn => {
                self.api_return = Some(now);
                self.api_returns += 1;
            }
            Signal::Callback(ok) => {
                self.callback = Some(now);
                self.callbacks += 1;
                self.callback_ok = Some(ok);
            }
            Signal::Sent => {
                self.sent = Some(now);
                self.sends += 1;
            }
            Signal::Return(ok) => {
                self.returned = Some(now);
                self.returns += 1;
                self.result = if ok { "ok" } else { "error" };
            }
        }
    }
    pub fn json(&self) -> String {
        fn time(value: Option<u128>) -> String {
            value
                .map(|v| v.to_string())
                .unwrap_or_else(|| "null".into())
        }
        format!("{{\"calls\":{},\"apiReturns\":{},\"callbacks\":{},\"sends\":{},\"returns\":{},\"pumpOrdinal\":{},\"startedUs\":{},\"apiStartUs\":{},\"apiReturnUs\":{},\"callbackUs\":{},\"sentUs\":{},\"returnUs\":{},\"result\":\"{}\",\"callbackOk\":{},\"invalid\":{}}}",
            self.calls, self.api_returns, self.callbacks, self.sends, self.returns, self.pump_ordinal,
            time(self.started), time(self.api_start), time(self.api_return), time(self.callback), time(self.sent), time(self.returned),
            self.result, self.callback_ok.map(|v| v.to_string()).unwrap_or_else(|| "null".into()), self.invalid)
    }
}
