//! Network-order ABI address conversion.

use iprange_livedb::{Ipv4Key, Ipv6Key};

use crate::abi::{Ip, Range};
use crate::error::BoundaryError;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Key {
    V4(Ipv4Key),
    V6(Ipv6Key),
}

pub(crate) fn decode(value: Ip) -> Result<Key, BoundaryError> {
    match value.family {
        4 => {
            if value.bytes[4..].iter().any(|&byte| byte != 0) {
                return Err(BoundaryError::reserved(
                    "IPv4 trailing address bytes must be zero",
                ));
            }
            Ok(Key::V4(Ipv4Key(u32::from_be_bytes([
                value.bytes[0],
                value.bytes[1],
                value.bytes[2],
                value.bytes[3],
            ]))))
        }
        6 => Ok(Key::V6(Ipv6Key::from_u128(u128::from_be_bytes(
            value.bytes,
        )))),
        _ => Err(BoundaryError::invalid_enum("unknown IP address family")),
    }
}

pub(crate) fn encode(value: Key) -> Ip {
    match value {
        Key::V4(value) => {
            let mut bytes = [0; 16];
            bytes[..4].copy_from_slice(&value.0.to_be_bytes());
            Ip { family: 4, bytes }
        }
        Key::V6(value) => Ip {
            family: 6,
            bytes: value.to_u128().to_be_bytes(),
        },
    }
}

pub(crate) fn decode_range(value: Range) -> Result<(Key, Key), BoundaryError> {
    let from = decode(value.from)?;
    let to = decode(value.to)?;
    match (from, to) {
        (Key::V4(from), Key::V4(to)) if from <= to => Ok((Key::V4(from), Key::V4(to))),
        (Key::V6(from), Key::V6(to)) if from <= to => Ok((Key::V6(from), Key::V6(to))),
        (Key::V4(_), Key::V4(_)) | (Key::V6(_), Key::V6(_)) => Err(BoundaryError::range_reversed(
            "range start exceeds range end",
        )),
        _ => Err(BoundaryError::wrong_family(
            "range endpoints have different address families",
        )),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn network_order_round_trips_both_families() {
        for key in [
            Key::V4(Ipv4Key(0xc000_0201)),
            Key::V6(Ipv6Key::from_u128(
                0x2001_0db8_0000_0000_0000_0000_0000_0001,
            )),
        ] {
            assert_eq!(decode(encode(key)).unwrap(), key);
        }
    }
}
