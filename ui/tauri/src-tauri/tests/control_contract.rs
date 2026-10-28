#[allow(dead_code, reason = "the candidate coordinator is not activated")]
#[path = "../src/control_codec/mod.rs"]
mod control_codec;

use serde_json::Value;
use std::collections::HashSet;

fn hex(value: &str) -> Vec<u8> {
    assert!(value.len().is_multiple_of(2));
    assert!(value.as_bytes().iter().all(u8::is_ascii_hexdigit));
    value
        .as_bytes()
        .chunks_exact(2)
        .map(|pair| u8::from_str_radix(std::str::from_utf8(pair).unwrap(), 16).unwrap())
        .collect()
}

fn expand(case: &Value) -> Vec<u8> {
    let mut bytes = Vec::with_capacity(16_385);
    for part in case["frameParts"].as_array().unwrap() {
        let object = part.as_object().unwrap();
        let mut keys: Vec<_> = object.keys().map(String::as_str).collect();
        keys.sort_unstable();
        match keys.as_slice() {
            ["hex"] => {
                let value = hex(part["hex"].as_str().unwrap());
                assert!(bytes.len() + value.len() <= 16_385);
                bytes.extend(value);
            }
            ["count", "repeatByteHex"] => {
                let value = hex(part["repeatByteHex"].as_str().unwrap());
                assert_eq!(value.len(), 1);
                let count: usize = part["count"].as_u64().unwrap().try_into().unwrap();
                let length = bytes.len().checked_add(count).unwrap();
                assert!(length <= 16_385);
                bytes.resize(length, value[0]);
            }
            _ => panic!("invalid vector part"),
        }
    }
    bytes
}

#[test]
fn shared_wire_vectors_match_the_pure_rust_codec() {
    let fixture: Value = serde_json::from_str(include_str!(
        "../../../../contracts/control-v1/vectors.json"
    ))
    .unwrap();
    assert_eq!(
        fixture["scope"],
        "control-contract-implementation-candidate"
    );
    assert_eq!(fixture["protocol"], "rescue-host-control/1");
    assert_eq!(fixture["runtimeActivationAllowed"], false);
    assert_eq!(fixture["framePartExpansionLimitBytes"], 16_385);
    assert_eq!(
        fixture["limits"]["frameBytesIncludingLF"],
        control_codec::MAX_FRAME_BYTES
    );
    assert_eq!(
        fixture["limits"]["containerDepth"],
        control_codec::MAX_DEPTH
    );
    let cases = fixture["wireCases"].as_array().unwrap();
    assert_eq!(cases.len(), 36);
    let mut ids = HashSet::new();
    let mut accepted = 0;
    for case in cases {
        let id = case["id"].as_str().unwrap();
        assert!(!id.is_empty() && ids.insert(id));
        let expected = case["frameAccepted"].as_bool().unwrap();
        let actual = control_codec::decode_frame(&expand(case)).is_ok();
        assert_eq!(actual, expected, "{id}");
        accepted += usize::from(expected);
    }
    assert_eq!(accepted, 12);
    println!("control-codec: vectors=36 accepted=12 rejected=24");
}
