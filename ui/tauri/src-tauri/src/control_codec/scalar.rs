use serde::{de, Deserialize, Deserializer, Serialize, Serializer};

// String enums must not accept Serde's alternate object representation.
macro_rules! string_enum {
    ($name:ident { $($variant:ident => $text:literal),+ $(,)? }) => {
        #[derive(Debug, Clone, Copy, PartialEq, Eq)]
        pub(crate) enum $name { $($variant),+ }
        impl $name {
            pub fn as_str(&self) -> &'static str {
                match self { $(Self::$variant => $text),+ }
            }
        }
        impl Serialize for $name {
            fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
                serializer.serialize_str(self.as_str())
            }
        }
        impl<'de> Deserialize<'de> for $name {
            fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
                match String::deserialize(deserializer)?.as_str() {
                    $($text => Ok(Self::$variant)),+,
                    _ => Err(de::Error::custom("unknown enum")),
                }
            }
        }
    };
}

string_enum!(Code {
    VersionMismatch => "VERSION_MISMATCH",
    InvalidMessage => "INVALID_MESSAGE",
    ContextMismatch => "CONTEXT_MISMATCH",
    RequestRejected => "REQUEST_REJECTED",
    DeadlineExceeded => "DEADLINE_EXCEEDED",
    TerminationUnconfirmed => "TERMINATION_UNCONFIRMED",
});
string_enum!(Protocol { V1 => "rescue-host-control/1" });
string_enum!(Operation { Start => "start", Stop => "stop" });
string_enum!(GoState {
    Prepared => "prepared", Ready => "ready", Active => "active",
    Revoking => "revoking", Terminated => "terminated",
});

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Hex<const LENGTH: usize>(String);

impl<const LENGTH: usize> Serialize for Hex<LENGTH> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.serialize_str(&self.0)
    }
}

impl<'de, const LENGTH: usize> Deserialize<'de> for Hex<LENGTH> {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        let value = String::deserialize(deserializer)?;
        if value.len() != LENGTH
            || !value.bytes().all(|b| b.is_ascii_hexdigit() && !b.is_ascii_uppercase())
        {
            return Err(de::Error::custom("lowercase hex"));
        }
        Ok(Self(value))
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct Token(String);

impl Serialize for Token {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.serialize_str(&self.0)
    }
}

impl<'de> Deserialize<'de> for Token {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        let value = String::deserialize(deserializer)?;
        if value.is_empty()
            || value.len() > 128
            || !value.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
        {
            return Err(de::Error::custom("ASCII token"));
        }
        Ok(Self(value))
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct Decimal<const POSITIVE: bool>(u64);

impl<const POSITIVE: bool> Serialize for Decimal<POSITIVE> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.serialize_str(&self.0.to_string())
    }
}

impl<'de, const POSITIVE: bool> Deserialize<'de> for Decimal<POSITIVE> {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        let value = String::deserialize(deserializer)?;
        if value.is_empty()
            || value.len() > 20
            || (value.len() > 1 && value.starts_with('0'))
            || !value.bytes().all(|b| b.is_ascii_digit())
        {
            return Err(de::Error::custom("canonical uint64"));
        }
        let number = value
            .parse::<u64>()
            .map_err(|_| de::Error::custom("uint64 overflow"))?;
        if POSITIVE && number == 0 {
            return Err(de::Error::custom("positive uint64"));
        }
        Ok(Self(number))
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct WireBool<const VALUE: bool>;

impl<const VALUE: bool> Serialize for WireBool<VALUE> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.serialize_bool(VALUE)
    }
}

impl<'de, const VALUE: bool> Deserialize<'de> for WireBool<VALUE> {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        if bool::deserialize(deserializer)? != VALUE {
            return Err(de::Error::custom("boolean constant"));
        }
        Ok(Self)
    }
}
