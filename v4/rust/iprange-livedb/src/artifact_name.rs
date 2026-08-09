//! Canonical ASCII encoding for attempt-bound artifact names.

pub(crate) const ATTEMPT_HEX_SIZE: usize = 32;
pub(crate) const ORDINAL_HEX_SIZE: usize = 8;
pub(crate) const SCRATCH_NAME_SIZE: usize = 62;

const SCRATCH_PREFIX: &[u8; 17] = b".iprange-scratch-";
const SCRATCH_SUFFIX: &[u8; 4] = b".tmp";
const SCRATCH_ATTEMPT_OFFSET: usize = SCRATCH_PREFIX.len();
const SCRATCH_ATTEMPT_END: usize = SCRATCH_ATTEMPT_OFFSET + ATTEMPT_HEX_SIZE;
const SCRATCH_SEPARATOR_OFFSET: usize = SCRATCH_ATTEMPT_END;
const SCRATCH_ORDINAL_OFFSET: usize = SCRATCH_SEPARATOR_OFFSET + 1;
const SCRATCH_ORDINAL_END: usize = SCRATCH_ORDINAL_OFFSET + ORDINAL_HEX_SIZE;
const SCRATCH_SUFFIX_OFFSET: usize = SCRATCH_ORDINAL_END;

pub(crate) fn scratch_name(attempt: [u8; 16], ordinal: u32) -> [u8; SCRATCH_NAME_SIZE] {
    let mut bytes = [0; SCRATCH_NAME_SIZE];
    bytes[..SCRATCH_ATTEMPT_OFFSET].copy_from_slice(SCRATCH_PREFIX);
    write_attempt(
        attempt,
        &mut bytes[SCRATCH_ATTEMPT_OFFSET..SCRATCH_ATTEMPT_END],
    );
    bytes[SCRATCH_SEPARATOR_OFFSET] = b'-';
    write_ordinal(
        ordinal,
        &mut bytes[SCRATCH_ORDINAL_OFFSET..SCRATCH_ORDINAL_END],
    );
    bytes[SCRATCH_SUFFIX_OFFSET..].copy_from_slice(SCRATCH_SUFFIX);
    bytes
}

pub(crate) fn decode_scratch_name(bytes: &[u8]) -> Option<([u8; 16], u32)> {
    if bytes.len() != SCRATCH_NAME_SIZE
        || &bytes[..SCRATCH_ATTEMPT_OFFSET] != SCRATCH_PREFIX
        || bytes[SCRATCH_SEPARATOR_OFFSET] != b'-'
        || &bytes[SCRATCH_SUFFIX_OFFSET..] != SCRATCH_SUFFIX
    {
        return None;
    }
    Some((
        decode_attempt(&bytes[SCRATCH_ATTEMPT_OFFSET..SCRATCH_ATTEMPT_END])?,
        decode_ordinal(&bytes[SCRATCH_ORDINAL_OFFSET..SCRATCH_ORDINAL_END])?,
    ))
}

pub(crate) fn write_attempt(attempt: [u8; 16], output: &mut [u8]) {
    debug_assert_eq!(output.len(), ATTEMPT_HEX_SIZE);
    for (pair, byte) in output.chunks_exact_mut(2).zip(attempt) {
        pair[0] = encode_nibble(byte >> 4);
        pair[1] = encode_nibble(byte & 0x0f);
    }
}

pub(crate) fn decode_attempt(bytes: &[u8]) -> Option<[u8; 16]> {
    if bytes.len() != ATTEMPT_HEX_SIZE {
        return None;
    }
    let mut attempt = [0; 16];
    for (output, pair) in attempt.iter_mut().zip(bytes.chunks_exact(2)) {
        *output = decode_nibble(pair[0])?.checked_shl(4)? | decode_nibble(pair[1])?;
    }
    Some(attempt)
}

pub(crate) fn write_ordinal(ordinal: u32, output: &mut [u8]) {
    debug_assert_eq!(output.len(), ORDINAL_HEX_SIZE);
    for (index, byte) in output.iter_mut().enumerate() {
        let shift = (ORDINAL_HEX_SIZE - index - 1) * 4;
        *byte = encode_nibble(((ordinal >> shift) & 0x0f) as u8);
    }
}

pub(crate) fn decode_ordinal(bytes: &[u8]) -> Option<u32> {
    if bytes.len() != ORDINAL_HEX_SIZE {
        return None;
    }
    let mut value = 0u32;
    for &byte in bytes {
        value = value.checked_shl(4)? | u32::from(decode_nibble(byte)?);
    }
    Some(value)
}

pub(crate) const fn encode_nibble(value: u8) -> u8 {
    match value {
        0..=9 => b'0' + value,
        _ => b'a' + value - 10,
    }
}

pub(crate) const fn decode_nibble(value: u8) -> Option<u8> {
    match value {
        b'0'..=b'9' => Some(value - b'0'),
        b'a'..=b'f' => Some(value - b'a' + 10),
        _ => None,
    }
}
