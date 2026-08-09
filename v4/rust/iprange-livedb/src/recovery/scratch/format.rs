use crate::contract::{u16_le, u32_le, MetaV4};
use crate::crc32c;
use crate::error::{Error, Result};
use crate::publication::namespace::{Name, CREATION_SECURITY_KIND};
use crate::publication::security::Profile;

pub(crate) const HEADER_SIZE: u64 = 128;
const HEADER_BYTES: usize = HEADER_SIZE as usize;
const MAGIC: [u8; 8] = *b"IPR4SCR1";
const VERSION: u16 = 1;
const OWNER_RECOVERY: u16 = 2;
const MAGIC_OFFSET: usize = 0;
const VERSION_OFFSET: usize = 8;
const HEADER_SIZE_OFFSET: usize = 10;
const OWNER_KIND_OFFSET: usize = 12;
const DATABASE_ID_OFFSET: usize = 16;
const TRANSACTION_ID_OFFSET: usize = 32;
const COMMIT_NONCE_OFFSET: usize = 40;
const ATTEMPT_ID_OFFSET: usize = 56;
const ORDINAL_OFFSET: usize = 72;
const CREATION_SECURITY_KIND_OFFSET: usize = 76;
const CREATION_SECURITY_COMMITMENT_OFFSET: usize = 80;
const CREATION_SECURITY_COMMITMENT_END: usize = 112;
const HEADER_CRC_OFFSET: usize = 124;
const HEADER_CRC_SIZE: usize = core::mem::size_of::<u32>();

#[derive(Clone, Copy)]
pub(crate) struct DecodedHeader {
    pub(crate) owner_kind: u16,
    pub(crate) attempt_id: [u8; 16],
    pub(crate) ordinal: u32,
    #[cfg(windows)]
    pub(crate) creation_security_kind: u16,
    #[cfg(windows)]
    pub(crate) creation_security_commitment: [u8; 32],
}

pub(super) fn header(
    source: MetaV4,
    attempt: [u8; 16],
    ordinal: u32,
    profile: &Profile,
) -> [u8; HEADER_BYTES] {
    let mut bytes = [0; HEADER_BYTES];
    bytes[MAGIC_OFFSET..VERSION_OFFSET].copy_from_slice(&MAGIC);
    bytes[VERSION_OFFSET..HEADER_SIZE_OFFSET].copy_from_slice(&VERSION.to_le_bytes());
    bytes[HEADER_SIZE_OFFSET..OWNER_KIND_OFFSET]
        .copy_from_slice(&(HEADER_SIZE as u16).to_le_bytes());
    bytes[OWNER_KIND_OFFSET..OWNER_KIND_OFFSET + 2].copy_from_slice(&OWNER_RECOVERY.to_le_bytes());
    bytes[DATABASE_ID_OFFSET..TRANSACTION_ID_OFFSET].copy_from_slice(&source.database_id);
    bytes[TRANSACTION_ID_OFFSET..COMMIT_NONCE_OFFSET].copy_from_slice(&source.txn_id.to_le_bytes());
    bytes[COMMIT_NONCE_OFFSET..ATTEMPT_ID_OFFSET].copy_from_slice(&source.commit_nonce);
    bytes[ATTEMPT_ID_OFFSET..ORDINAL_OFFSET].copy_from_slice(&attempt);
    bytes[ORDINAL_OFFSET..CREATION_SECURITY_KIND_OFFSET].copy_from_slice(&ordinal.to_le_bytes());
    bytes[CREATION_SECURITY_KIND_OFFSET..CREATION_SECURITY_KIND_OFFSET + 2]
        .copy_from_slice(&CREATION_SECURITY_KIND.to_le_bytes());
    bytes[CREATION_SECURITY_COMMITMENT_OFFSET..CREATION_SECURITY_COMMITMENT_END]
        .copy_from_slice(&profile.commitment());
    let checksum = crc32c::crc32c_with_zeroed(&bytes, HEADER_CRC_OFFSET, HEADER_CRC_SIZE)
        .expect("fixed scratch header");
    bytes[HEADER_CRC_OFFSET..HEADER_BYTES].copy_from_slice(&checksum.to_le_bytes());
    bytes
}

pub(crate) fn scratch_name(attempt: [u8; 16], ordinal: u32) -> Result<Name> {
    let bytes = crate::artifact_name::scratch_name(attempt, ordinal);
    Name::new(&bytes).map_err(|_| Error::InvalidArgument("invalid recovery scratch name"))
}

pub(crate) fn decode_name(bytes: &[u8]) -> Option<([u8; 16], u32)> {
    crate::artifact_name::decode_scratch_name(bytes)
}

pub(crate) fn decode_header(bytes: &[u8; HEADER_BYTES]) -> Option<DecodedHeader> {
    let owner_kind = u16_le(bytes, OWNER_KIND_OFFSET);
    let mut attempt_id = [0; 16];
    attempt_id.copy_from_slice(&bytes[ATTEMPT_ID_OFFSET..ORDINAL_OFFSET]);
    let valid = fixed_header_valid(bytes, owner_kind)
        && reserved_header_valid(bytes)
        && attempt_id != [0; 16]
        && bytes[CREATION_SECURITY_COMMITMENT_OFFSET..CREATION_SECURITY_COMMITMENT_END]
            .iter()
            .any(|&byte| byte != 0)
        && header_crc_valid(bytes);
    valid.then_some(DecodedHeader {
        owner_kind,
        attempt_id,
        ordinal: u32_le(bytes, ORDINAL_OFFSET),
        #[cfg(windows)]
        creation_security_kind: u16_le(bytes, CREATION_SECURITY_KIND_OFFSET),
        #[cfg(windows)]
        creation_security_commitment: bytes
            [CREATION_SECURITY_COMMITMENT_OFFSET..CREATION_SECURITY_COMMITMENT_END]
            .try_into()
            .expect("fixed commitment"),
    })
}

fn fixed_header_valid(bytes: &[u8; HEADER_BYTES], owner_kind: u16) -> bool {
    bytes[MAGIC_OFFSET..VERSION_OFFSET] == MAGIC
        && u16_le(bytes, VERSION_OFFSET) == VERSION
        && u16_le(bytes, HEADER_SIZE_OFFSET) == HEADER_SIZE as u16
        && matches!(owner_kind, 1 | 2)
        && u16_le(bytes, CREATION_SECURITY_KIND_OFFSET) == CREATION_SECURITY_KIND
}

fn reserved_header_valid(bytes: &[u8; HEADER_BYTES]) -> bool {
    bytes[OWNER_KIND_OFFSET + core::mem::size_of::<u16>()..DATABASE_ID_OFFSET]
        .iter()
        .all(|&byte| byte == 0)
        && bytes[CREATION_SECURITY_KIND_OFFSET + core::mem::size_of::<u16>()
            ..CREATION_SECURITY_COMMITMENT_OFFSET]
            .iter()
            .all(|&byte| byte == 0)
        && bytes[CREATION_SECURITY_COMMITMENT_END..HEADER_CRC_OFFSET]
            .iter()
            .all(|&byte| byte == 0)
}

fn header_crc_valid(bytes: &[u8; HEADER_BYTES]) -> bool {
    crc32c::crc32c_with_zeroed(bytes, HEADER_CRC_OFFSET, HEADER_CRC_SIZE)
        == Some(u32_le(bytes, HEADER_CRC_OFFSET))
}
