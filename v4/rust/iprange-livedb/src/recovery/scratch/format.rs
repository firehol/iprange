use crate::contract::{u16_le, u32_le, MetaV4};
use crate::crc32c;
use crate::error::{Error, Result};
use crate::publication::namespace::Name;
use crate::publication::security::Profile;

pub(crate) const HEADER_SIZE: u64 = 128;
pub(crate) const POSIX_IDENTITY: u16 = 1;
const OWNER_RECOVERY: u16 = 2;

#[derive(Clone, Copy)]
pub(crate) struct DecodedHeader {
    pub(crate) owner_kind: u16,
    pub(crate) attempt_id: [u8; 16],
    pub(crate) ordinal: u32,
}

pub(super) fn header(
    source: MetaV4,
    attempt: [u8; 16],
    ordinal: u32,
    profile: Profile,
) -> [u8; 128] {
    let mut bytes = [0; 128];
    bytes[0..8].copy_from_slice(b"IPR4SCR1");
    bytes[8..10].copy_from_slice(&1u16.to_le_bytes());
    bytes[10..12].copy_from_slice(&(HEADER_SIZE as u16).to_le_bytes());
    bytes[12..14].copy_from_slice(&OWNER_RECOVERY.to_le_bytes());
    bytes[16..32].copy_from_slice(&source.database_id);
    bytes[32..40].copy_from_slice(&source.txn_id.to_le_bytes());
    bytes[40..56].copy_from_slice(&source.commit_nonce);
    bytes[56..72].copy_from_slice(&attempt);
    bytes[72..76].copy_from_slice(&ordinal.to_le_bytes());
    bytes[76..78].copy_from_slice(&POSIX_IDENTITY.to_le_bytes());
    bytes[80..112].copy_from_slice(&profile.commitment());
    let checksum = crc32c::crc32c_with_zeroed(&bytes, 124, 4).expect("fixed scratch header");
    bytes[124..128].copy_from_slice(&checksum.to_le_bytes());
    bytes
}

pub(crate) fn scratch_name(attempt: [u8; 16], ordinal: u32) -> Result<Name> {
    let mut bytes = [0u8; 62];
    bytes[..17].copy_from_slice(b".iprange-scratch-");
    let mut at = 17;
    for byte in attempt {
        bytes[at] = hex(byte >> 4);
        bytes[at + 1] = hex(byte & 0x0f);
        at += 2;
    }
    bytes[at] = b'-';
    at += 1;
    for shift in (0..8).rev() {
        bytes[at] = hex(((ordinal >> (shift * 4)) & 0x0f) as u8);
        at += 1;
    }
    bytes[at..].copy_from_slice(b".tmp");
    Name::new(&bytes).map_err(|_| Error::InvalidArgument("invalid recovery scratch name"))
}

pub(crate) fn decode_name(bytes: &[u8]) -> Option<([u8; 16], u32)> {
    if !name_shape_valid(bytes) {
        return None;
    }
    Some((
        decode_attempt(&bytes[17..49])?,
        decode_ordinal(&bytes[50..58])?,
    ))
}

fn name_shape_valid(bytes: &[u8]) -> bool {
    bytes.len() == 62
        && &bytes[..17] == b".iprange-scratch-"
        && bytes[49] == b'-'
        && &bytes[58..] == b".tmp"
}

fn decode_attempt(bytes: &[u8]) -> Option<[u8; 16]> {
    let mut attempt = [0; 16];
    for (output, pair) in attempt.iter_mut().zip(bytes.chunks_exact(2)) {
        *output = nibble(pair[0])?.checked_shl(4)? | nibble(pair[1])?;
    }
    Some(attempt)
}

fn decode_ordinal(bytes: &[u8]) -> Option<u32> {
    let mut value = 0u32;
    for &byte in bytes {
        value = value.checked_shl(4)? | u32::from(nibble(byte)?);
    }
    Some(value)
}

pub(crate) fn decode_header(bytes: &[u8; 128]) -> Option<DecodedHeader> {
    let owner_kind = u16_le(bytes, 12);
    let mut attempt_id = [0; 16];
    attempt_id.copy_from_slice(&bytes[56..72]);
    let valid = fixed_header_valid(bytes, owner_kind)
        && reserved_header_valid(bytes)
        && attempt_id != [0; 16]
        && header_crc_valid(bytes);
    valid.then_some(DecodedHeader {
        owner_kind,
        attempt_id,
        ordinal: u32_le(bytes, 72),
    })
}

fn fixed_header_valid(bytes: &[u8; 128], owner_kind: u16) -> bool {
    &bytes[..8] == b"IPR4SCR1"
        && u16_le(bytes, 8) == 1
        && u16_le(bytes, 10) == HEADER_SIZE as u16
        && matches!(owner_kind, 1 | 2)
        && u16_le(bytes, 76) == POSIX_IDENTITY
}

fn reserved_header_valid(bytes: &[u8; 128]) -> bool {
    bytes[14..16].iter().all(|&byte| byte == 0)
        && bytes[78..80].iter().all(|&byte| byte == 0)
        && bytes[112..124].iter().all(|&byte| byte == 0)
}

fn header_crc_valid(bytes: &[u8; 128]) -> bool {
    crc32c::crc32c_with_zeroed(bytes, 124, 4) == Some(u32_le(bytes, 124))
}

pub(super) fn hex(value: u8) -> u8 {
    match value {
        0..=9 => b'0' + value,
        _ => b'a' + value - 10,
    }
}

fn nibble(value: u8) -> Option<u8> {
    match value {
        b'0'..=b'9' => Some(value - b'0'),
        b'a'..=b'f' => Some(value - b'a' + 10),
        _ => None,
    }
}
