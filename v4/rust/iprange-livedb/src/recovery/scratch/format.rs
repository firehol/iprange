use crate::contract::MetaV4;
use crate::crc32c;
use crate::error::{Error, Result};
use crate::publication::namespace::Name;
use crate::publication::security::Profile;

pub(crate) const HEADER_SIZE: u64 = 128;
pub(super) const POSIX_IDENTITY: u16 = 1;
const OWNER_RECOVERY: u16 = 2;

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

pub(super) fn scratch_name(attempt: [u8; 16], ordinal: u32) -> Result<Name> {
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

pub(super) fn hex(value: u8) -> u8 {
    match value {
        0..=9 => b'0' + value,
        _ => b'a' + value - 10,
    }
}
