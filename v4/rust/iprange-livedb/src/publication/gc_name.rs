//! Exact attempt-bound Windows housekeeping names.

use super::namespace::{Name, NamespaceError};

const ENVELOPE_PREFIX: &[u8] = b".iprange-gcauth-";
const INERT_PREFIX: &[u8] = b".iprange-gc-";
const SUFFIX: &[u8] = b".tmp";
const ATTEMPT_HEX: usize = 32;
const ORDINAL_HEX: usize = 8;
#[cfg(windows)]
const MAX_ASCII_NAME: usize = ENVELOPE_PREFIX.len() + ATTEMPT_HEX + 1 + ORDINAL_HEX + SUFFIX.len();

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Candidate {
    Envelope(Option<([u8; 16], u32)>),
    Inert(Option<([u8; 16], u32)>),
}

pub(crate) fn envelope(attempt: [u8; 16], ordinal: u32) -> Result<Name, NamespaceError> {
    Name::new(&encode(ENVELOPE_PREFIX, attempt, ordinal)?)
}

pub(crate) fn inert(attempt: [u8; 16], ordinal: u32) -> Result<Name, NamespaceError> {
    Name::new(&encode(INERT_PREFIX, attempt, ordinal)?)
}

pub(crate) fn decode_envelope(bytes: &[u8]) -> Option<([u8; 16], u32)> {
    decode(bytes, ENVELOPE_PREFIX)
}

pub(crate) fn decode_envelope_name(name: &Name) -> Option<([u8; 16], u32)> {
    decode_name(name, ENVELOPE_PREFIX)
}

pub(crate) fn decode_inert(bytes: &[u8]) -> Option<([u8; 16], u32)> {
    decode(bytes, INERT_PREFIX)
}

pub(crate) fn candidate(bytes: &[u8]) -> Option<Candidate> {
    if bytes.starts_with(ENVELOPE_PREFIX) {
        Some(Candidate::Envelope(decode_envelope(bytes)))
    } else if bytes.starts_with(INERT_PREFIX) {
        Some(Candidate::Inert(decode_inert(bytes)))
    } else {
        None
    }
}

fn encode(prefix: &[u8], attempt: [u8; 16], ordinal: u32) -> Result<Vec<u8>, NamespaceError> {
    if attempt == [0; 16] {
        return Err(NamespaceError::InvalidName);
    }
    let mut bytes = Vec::with_capacity(prefix.len() + ATTEMPT_HEX + 1 + ORDINAL_HEX + SUFFIX.len());
    bytes.extend_from_slice(prefix);
    for byte in attempt {
        bytes.push(hex(byte >> 4));
        bytes.push(hex(byte & 0x0f));
    }
    bytes.push(b'-');
    for shift in (0..ORDINAL_HEX).rev() {
        bytes.push(hex(((ordinal >> (shift * 4)) & 0x0f) as u8));
    }
    bytes.extend_from_slice(SUFFIX);
    Ok(bytes)
}

fn decode(bytes: &[u8], prefix: &[u8]) -> Option<([u8; 16], u32)> {
    let encoded = bytes.strip_prefix(prefix)?.strip_suffix(SUFFIX)?;
    if encoded.len() != ATTEMPT_HEX + 1 + ORDINAL_HEX || encoded[ATTEMPT_HEX] != b'-' {
        return None;
    }
    let mut attempt = [0; 16];
    for (slot, pair) in attempt
        .iter_mut()
        .zip(encoded[..ATTEMPT_HEX].chunks_exact(2))
    {
        *slot = nibble(pair[0])?.checked_shl(4)? | nibble(pair[1])?;
    }
    if attempt == [0; 16] {
        return None;
    }
    let mut ordinal = 0u32;
    for &byte in &encoded[ATTEMPT_HEX + 1..] {
        ordinal = ordinal.checked_shl(4)? | u32::from(nibble(byte)?);
    }
    Some((attempt, ordinal))
}

#[cfg(unix)]
fn decode_name(name: &Name, prefix: &[u8]) -> Option<([u8; 16], u32)> {
    decode(name.bytes(), prefix)
}

#[cfg(windows)]
fn decode_name(name: &Name, prefix: &[u8]) -> Option<([u8; 16], u32)> {
    let encoded = name.bytes();
    if encoded.len() % 2 != 0 || encoded.len() / 2 > MAX_ASCII_NAME {
        return None;
    }
    let mut ascii = [0u8; MAX_ASCII_NAME];
    for (output, unit) in ascii.iter_mut().zip(encoded.chunks_exact(2)) {
        if unit[1] != 0 || !unit[0].is_ascii() {
            return None;
        }
        *output = unit[0];
    }
    decode(&ascii[..encoded.len() / 2], prefix)
}

const fn hex(value: u8) -> u8 {
    match value {
        0..=9 => b'0' + value,
        _ => b'a' + value - 10,
    }
}

const fn nibble(value: u8) -> Option<u8> {
    match value {
        b'0'..=b'9' => Some(value - b'0'),
        b'a'..=b'f' => Some(value - b'a' + 10),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn names_use_exact_lowercase_fixed_width_encoding() {
        let attempt = [
            0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0xfe, 0xdc, 0xba, 0x98, 0x76, 0x54,
            0x32, 0x10,
        ];
        let name = envelope(attempt, 0x89ab_cdef).unwrap();
        assert_eq!(
            name,
            Name::new(b".iprange-gcauth-0123456789abcdeffedcba9876543210-89abcdef.tmp").unwrap()
        );
        assert_eq!(decode_envelope_name(&name), Some((attempt, 0x89ab_cdef)));
        assert_eq!(
            inert(attempt, 7).unwrap(),
            Name::new(b".iprange-gc-0123456789abcdeffedcba9876543210-00000007.tmp").unwrap()
        );
        assert_eq!(
            decode_inert(b".iprange-gc-0123456789abcdeffedcba9876543210-00000007.tmp"),
            Some((attempt, 7))
        );
        assert_eq!(
            candidate(b".iprange-gcauth-0123456789abcdeffedcba9876543210-89abcdef.tmp"),
            Some(Candidate::Envelope(Some((attempt, 0x89ab_cdef))))
        );
        assert_eq!(
            candidate(b".iprange-gc-0123456789abcdeffedcba9876543210-00000007.tmp"),
            Some(Candidate::Inert(Some((attempt, 7))))
        );
    }

    #[test]
    fn decoder_rejects_noncanonical_or_zero_attempts() {
        assert_eq!(
            decode_envelope(b".iprange-gcauth-0123456789ABCDEFFEDCBA9876543210-89abcdef.tmp"),
            None
        );
        assert_eq!(
            decode_envelope(b".iprange-gcauth-00000000000000000000000000000000-00000000.tmp"),
            None
        );
        assert_eq!(
            decode_envelope(b".iprange-gcauth-0123456789abcdeffedcba9876543210-1.tmp"),
            None
        );
    }
}
