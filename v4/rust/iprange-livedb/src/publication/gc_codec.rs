//! Authenticated Windows housekeeping envelope bytes.

use sha2::{Digest, Sha256};

use crate::contract::{u16_le, u32_le, u64_le, PAGE_SIZE};
use crate::crc32c;
use crate::error::Result as SdkResult;
use crate::mapping::{ByteRange, ByteSource};
use crate::name_binding::{basename_commitment, BasenameEncoding};
use crate::slotted_page::PageEdit;

use super::types::{ArtifactKind, DirectoryRole};

pub(crate) const FILE_SIZE: usize = 2 * PAGE_SIZE;
const MAGIC: [u8; 8] = *b"IPR4GCA1";
const RECORD_SIZE: u16 = 512;
const VERSION: u16 = 1;
const CRC_OFFSET: usize = 508;
const SOURCE_LENGTH_OFFSET: usize = 328;
const SOURCE_OFFSET: usize = 512;
const SOURCE_CAPACITY: usize = PAGE_SIZE - SOURCE_OFFSET;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Payload {
    pub(crate) byte_length: u64,
    pub(crate) sha512: [u8; 64],
    pub(crate) database_id: [u8; 16],
    pub(crate) transaction_id: u64,
    pub(crate) commit_nonce: [u8; 16],
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Header {
    pub(crate) kind: ArtifactKind,
    pub(crate) basename_encoding: u16,
    pub(crate) attempt_id: [u8; 16],
    pub(crate) ordinal: u32,
    pub(crate) directory_identity_kind: u16,
    pub(crate) artifact_identity_kind: u16,
    pub(crate) directory_identity: [u8; 32],
    pub(crate) source_commitment: [u8; 32],
    pub(crate) inert_commitment: [u8; 32],
    pub(crate) artifact_identity: [u8; 32],
    pub(crate) payload: Option<Payload>,
    pub(crate) creation_security_kind: u16,
    pub(crate) directory_role: DirectoryRole,
    pub(crate) creation_security_commitment: [u8; 32],
    pub(crate) source_basename: Box<[u8]>,
    pub(crate) sequence: u64,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum SelectError {
    WrongSize,
    NoValidHeader,
    HeaderDisagreement,
}

impl Header {
    pub(crate) fn encode<P: PageEdit>(&self, block: &mut P) -> SdkResult<()> {
        assert!(
            !self.source_basename.is_empty() && self.source_basename.len() <= SOURCE_CAPACITY,
            "GC source filename exceeds its fixed block"
        );
        block.fill(0);
        block.write(0, &MAGIC)?;
        block.put_u16(8, RECORD_SIZE)?;
        block.put_u16(10, VERSION)?;
        block.put_u16(12, kind_code(self.kind))?;
        block.put_u16(14, self.basename_encoding)?;
        block.write(16, &self.attempt_id)?;
        block.put_u32(32, self.ordinal)?;
        block.put_u16(36, self.directory_identity_kind)?;
        block.put_u16(38, self.artifact_identity_kind)?;
        block.write(40, &self.directory_identity)?;
        block.write(72, &self.source_commitment)?;
        block.write(104, &self.inert_commitment)?;
        block.write(144, &self.artifact_identity)?;
        if let Some(payload) = self.payload {
            block.put_u16(136, 1)?;
            block.put_u64(176, payload.byte_length)?;
            block.write(184, &payload.sha512)?;
            block.write(248, &payload.database_id)?;
            block.put_u64(264, payload.transaction_id)?;
            block.write(272, &payload.commit_nonce)?;
        }
        block.put_u16(288, self.creation_security_kind)?;
        block.put_u16(290, directory_role_code(self.directory_role))?;
        block.write(296, &self.creation_security_commitment)?;
        block.put_u32(
            SOURCE_LENGTH_OFFSET,
            u32::try_from(self.source_basename.len()).expect("bounded GC source filename"),
        )?;
        block.put_u64(496, self.sequence)?;
        block.write(SOURCE_OFFSET, &self.source_basename)?;
        let checksum = crc32c::crc32c_source_with_zeroed(block.view(), CRC_OFFSET, 4)
            .expect("fixed GC CRC field");
        block.put_u32(CRC_OFFSET, checksum)
    }
}

pub(crate) fn select<S: ByteSource>(bytes: S) -> Result<Header, SelectError> {
    if bytes.len() != FILE_SIZE {
        return Err(SelectError::WrongSize);
    }
    let left = decode(ByteRange::new(bytes, 0, PAGE_SIZE).expect("checked GC block"));
    let right = decode(ByteRange::new(bytes, PAGE_SIZE, PAGE_SIZE).expect("checked GC block"));
    match (left, right) {
        (None, None) => Err(SelectError::NoValidHeader),
        (Some(header), None) | (None, Some(header)) => Ok(header),
        (Some(left), Some(right)) if same_authority(&left, &right) => {
            Ok(if left.sequence >= right.sequence {
                left
            } else {
                right
            })
        }
        (Some(_), Some(_)) => Err(SelectError::HeaderDisagreement),
    }
}

pub(crate) fn source_commitment(encoding: u16, name: &[u8]) -> [u8; 32] {
    name_commitment(b"IPR4GCAUTH", encoding, name)
}

pub(crate) fn inert_commitment(encoding: u16, name: &[u8]) -> [u8; 32] {
    name_commitment(b"IPR4GCNAME", encoding, name)
}

fn name_commitment(domain: &[u8], encoding: u16, name: &[u8]) -> [u8; 32] {
    let length = u32::try_from(name.len()).expect("retained basename length fits u32");
    let mut digest = Sha256::new();
    digest.update(domain);
    digest.update(encoding.to_le_bytes());
    digest.update(length.to_le_bytes());
    digest.update(name);
    digest.finalize().into()
}

fn decode<P: ByteSource>(block: P) -> Option<Header> {
    if !fixed_valid(block) || !reserved_zero(block) || !checksum_valid(block) {
        return None;
    }
    let kind = decode_kind(u16_le(block, 12))?;
    let payload = decode_payload(block)?;
    let source_basename = decode_source_basename(block)?;
    let header = Header {
        kind,
        basename_encoding: u16_le(block, 14),
        attempt_id: array(block, 16),
        ordinal: u32_le(block, 32),
        directory_identity_kind: u16_le(block, 36),
        artifact_identity_kind: u16_le(block, 38),
        directory_identity: array(block, 40),
        source_commitment: array(block, 72),
        inert_commitment: array(block, 104),
        artifact_identity: array(block, 144),
        payload,
        creation_security_kind: u16_le(block, 288),
        directory_role: decode_directory_role(u16_le(block, 290))?,
        creation_security_commitment: array(block, 296),
        source_basename,
        sequence: u64_le(block, 496),
    };
    valid(&header).then_some(header)
}

fn fixed_valid<P: ByteSource>(block: P) -> bool {
    block.equals(0, &MAGIC)
        && u16_le(block, 8) == RECORD_SIZE
        && u16_le(block, 10) == VERSION
        && basename_encoding(u16_le(block, 14)).is_some()
}

fn reserved_zero<P: ByteSource>(block: P) -> bool {
    block.all_zero(138, 6)
        && block.all_zero(292, 4)
        && block.all_zero(332, 164)
        && block.all_zero(504, 4)
}

fn checksum_valid<P: ByteSource>(block: P) -> bool {
    crc32c::crc32c_source_with_zeroed(block, CRC_OFFSET, 4) == Some(u32_le(block, CRC_OFFSET))
}

fn decode_payload<P: ByteSource>(block: P) -> Option<Option<Payload>> {
    match u16_le(block, 136) {
        0 if payload_fields_zero(block) => Some(None),
        1 => Some(Some(Payload {
            byte_length: u64_le(block, 176),
            sha512: array(block, 184),
            database_id: array(block, 248),
            transaction_id: u64_le(block, 264),
            commit_nonce: array(block, 272),
        })),
        _ => None,
    }
}

fn payload_fields_zero<P: ByteSource>(block: P) -> bool {
    block.all_zero(176, 112)
}

fn decode_source_basename<P: ByteSource>(block: P) -> Option<Box<[u8]>> {
    let length = usize::try_from(u32_le(block, SOURCE_LENGTH_OFFSET)).ok()?;
    if length == 0 || length > SOURCE_CAPACITY {
        return None;
    }
    let end = SOURCE_OFFSET.checked_add(length)?;
    if !block.all_zero(end, PAGE_SIZE - end) {
        return None;
    }
    let mut name = vec![0; length];
    block
        .copy_range_to(SOURCE_OFFSET, &mut name)
        .then(|| name.into_boxed_slice())
}

fn valid(header: &Header) -> bool {
    let source_valid = basename_encoding(header.basename_encoding)
        .and_then(|encoding| basename_commitment(encoding, &header.source_basename).ok())
        .is_some();
    header.attempt_id != [0; 16]
        && matches!(
            header.kind,
            ArtifactKind::PrivateOutput
                | ArtifactKind::PrivateReservation
                | ArtifactKind::OwnedCoordination
                | ArtifactKind::AuthorizedScratch
                | ArtifactKind::OwnedMain
        )
        && matches!(header.directory_identity_kind, 1 | 2)
        && matches!(header.artifact_identity_kind, 1 | 2)
        && header.directory_identity != [0; 32]
        && header.artifact_identity != [0; 32]
        && matches!(header.creation_security_kind, 1 | 2)
        && header.creation_security_commitment != [0; 32]
        && source_valid
        && source_commitment(header.basename_encoding, &header.source_basename)
            == header.source_commitment
        && header.sequence != 0
        && header.payload.as_ref().map_or(true, |payload| {
            payload.byte_length != 0
                && payload.sha512 != [0; 64]
                && ((payload.database_id == [0; 16]
                    && payload.transaction_id == 0
                    && payload.commit_nonce == [0; 16])
                    || (payload.database_id != [0; 16]
                        && payload.transaction_id != 0
                        && payload.commit_nonce != [0; 16]))
        })
}

const fn directory_role_code(role: DirectoryRole) -> u16 {
    match role {
        DirectoryRole::Destination => 1,
        DirectoryRole::ScratchDirectory => 2,
        DirectoryRole::MainFile => 3,
    }
}

const fn decode_directory_role(value: u16) -> Option<DirectoryRole> {
    match value {
        1 => Some(DirectoryRole::Destination),
        2 => Some(DirectoryRole::ScratchDirectory),
        3 => Some(DirectoryRole::MainFile),
        _ => None,
    }
}

fn same_authority(left: &Header, right: &Header) -> bool {
    let mut left = left.clone();
    let mut right = right.clone();
    left.sequence = 1;
    right.sequence = 1;
    left == right
}

fn basename_encoding(value: u16) -> Option<BasenameEncoding> {
    match value {
        #[cfg(any(test, unix))]
        1 => Some(BasenameEncoding::PosixBytes),
        #[cfg(any(test, target_os = "windows"))]
        2 => Some(BasenameEncoding::WindowsUtf16Le),
        _ => None,
    }
}

const fn kind_code(kind: ArtifactKind) -> u16 {
    match kind {
        ArtifactKind::PrivateOutput => 1,
        ArtifactKind::PrivateReservation => 2,
        ArtifactKind::OwnedCoordination => 3,
        ArtifactKind::AuthorizedScratch => 4,
        ArtifactKind::OwnedMain => 5,
        ArtifactKind::UnpublishedMainTail => 0,
    }
}

const fn decode_kind(value: u16) -> Option<ArtifactKind> {
    match value {
        1 => Some(ArtifactKind::PrivateOutput),
        2 => Some(ArtifactKind::PrivateReservation),
        3 => Some(ArtifactKind::OwnedCoordination),
        4 => Some(ArtifactKind::AuthorizedScratch),
        5 => Some(ArtifactKind::OwnedMain),
        _ => None,
    }
}

fn array<const N: usize, P: ByteSource>(bytes: P, offset: usize) -> [u8; N] {
    bytes.array(offset).expect("fixed GC field")
}

#[cfg(test)]
impl Header {
    fn file_bytes(&self) -> [u8; FILE_SIZE] {
        let mut bytes = [0; FILE_SIZE];
        let (left, right) = bytes.split_at_mut(PAGE_SIZE);
        let left: &mut [u8; PAGE_SIZE] = left.try_into().expect("fixed GC block");
        let right: &mut [u8; PAGE_SIZE] = right.try_into().expect("fixed GC block");
        self.encode(left).unwrap();
        self.encode(right).unwrap();
        bytes
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::slotted_page::put_u32;

    fn header() -> Header {
        let source_basename = b"s\0o\0u\0r\0c\0e\0".to_vec().into_boxed_slice();
        Header {
            kind: ArtifactKind::PrivateOutput,
            basename_encoding: 2,
            attempt_id: [1; 16],
            ordinal: 7,
            directory_identity_kind: 2,
            artifact_identity_kind: 2,
            directory_identity: [2; 32],
            source_commitment: source_commitment(2, &source_basename),
            inert_commitment: inert_commitment(2, b"inert"),
            artifact_identity: [3; 32],
            payload: Some(Payload {
                byte_length: 8192,
                sha512: [4; 64],
                database_id: [5; 16],
                transaction_id: 6,
                commit_nonce: [7; 16],
            }),
            creation_security_kind: 2,
            directory_role: DirectoryRole::Destination,
            creation_security_commitment: [8; 32],
            source_basename,
            sequence: 1,
        }
    }

    #[test]
    fn exact_layout_round_trips_with_either_complete_copy() {
        let expected = header();
        let mut bytes = expected.file_bytes();
        assert_eq!(select(&bytes), Ok(expected.clone()));
        assert_eq!(&bytes[..8], b"IPR4GCA1");
        assert_eq!(u16_le(&bytes, 8), 512);
        assert_eq!(u16_le(&bytes, 136), 1);
        assert_eq!(u16_le(&bytes, 290), 1);
        assert_eq!(u64_le(&bytes, 496), 1);
        bytes[508] ^= 1;
        assert_eq!(select(&bytes), Ok(expected));
    }

    #[test]
    fn malformed_reserved_payload_and_disagreement_fail_closed() {
        let expected = header();
        let mut bytes = expected.file_bytes();
        bytes[292] = 1;
        bytes[PAGE_SIZE + 292] = 1;
        assert_eq!(select(&bytes), Err(SelectError::NoValidHeader));

        let mut bytes = expected.file_bytes();
        let other = Header {
            ordinal: 8,
            sequence: 2,
            ..expected.clone()
        }
        .file_bytes();
        bytes[PAGE_SIZE..].copy_from_slice(&other[..PAGE_SIZE]);
        assert_eq!(select(&bytes), Err(SelectError::HeaderDisagreement));
    }

    #[test]
    fn unknown_payload_requires_every_optional_field_zero() {
        let expected = Header {
            payload: None,
            ..header()
        };
        let mut bytes = expected.file_bytes();
        assert_eq!(select(&bytes), Ok(expected.clone()));
        bytes[176] = 1;
        let checksum = crc32c::crc32c_with_zeroed(&bytes[..PAGE_SIZE], CRC_OFFSET, 4).unwrap();
        put_u32(&mut bytes, CRC_OFFSET, checksum);
        let (left, right) = bytes.split_at_mut(PAGE_SIZE);
        right.copy_from_slice(left);
        assert_eq!(select(&bytes), Err(SelectError::NoValidHeader));
    }

    #[test]
    fn source_filename_is_stored_without_a_path_and_authenticated() {
        let expected = header();
        let mut bytes = expected.file_bytes();
        assert_eq!(
            u32_le(&bytes, SOURCE_LENGTH_OFFSET) as usize,
            expected.source_basename.len()
        );
        assert_eq!(
            &bytes[SOURCE_OFFSET..SOURCE_OFFSET + expected.source_basename.len()],
            expected.source_basename.as_ref()
        );

        bytes[SOURCE_OFFSET] ^= 1;
        let checksum = crc32c::crc32c_with_zeroed(&bytes[..PAGE_SIZE], CRC_OFFSET, 4).unwrap();
        put_u32(&mut bytes, CRC_OFFSET, checksum);
        let (left, right) = bytes.split_at_mut(PAGE_SIZE);
        right.copy_from_slice(left);
        assert_eq!(select(&bytes), Err(SelectError::NoValidHeader));
    }

    #[test]
    fn exact_payload_may_describe_arbitrary_non_v4_bytes() {
        let expected = Header {
            payload: Some(Payload {
                byte_length: 7,
                sha512: [9; 64],
                database_id: [0; 16],
                transaction_id: 0,
                commit_nonce: [0; 16],
            }),
            ..header()
        };
        assert_eq!(select(&expected.file_bytes()), Ok(expected));
    }
}
