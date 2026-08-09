//! Authenticated Windows housekeeping envelope bytes.

use sha2::{Digest, Sha256};

use crate::contract::{u16_le, u32_le, u64_le, PAGE_SIZE};
use crate::crc32c;
use crate::error::Result as SdkResult;
use crate::mapping::{ByteRange, ByteSource};
use crate::name_binding::{basename_commitment, BasenameEncoding};
use crate::page_io::PageEdit;

use super::types::{ArtifactKind, DirectoryRole};

pub(crate) const FILE_SIZE: usize = 2 * PAGE_SIZE;
const MAGIC: [u8; 8] = *b"IPR4GCA1";
const RECORD_SIZE: u16 = 512;
const VERSION: u16 = 1;
const MAGIC_OFFSET: usize = 0;
const RECORD_SIZE_OFFSET: usize = 8;
const VERSION_OFFSET: usize = 10;
const KIND_OFFSET: usize = 12;
const BASENAME_ENCODING_OFFSET: usize = 14;
const ATTEMPT_ID_OFFSET: usize = 16;
const ORDINAL_OFFSET: usize = 32;
const DIRECTORY_IDENTITY_KIND_OFFSET: usize = 36;
const ARTIFACT_IDENTITY_KIND_OFFSET: usize = 38;
const DIRECTORY_IDENTITY_OFFSET: usize = 40;
const SOURCE_COMMITMENT_OFFSET: usize = 72;
const INERT_COMMITMENT_OFFSET: usize = 104;
const PAYLOAD_PRESENT_OFFSET: usize = 136;
const ARTIFACT_IDENTITY_OFFSET: usize = 144;
const PAYLOAD_LENGTH_OFFSET: usize = 176;
const PAYLOAD_SHA512_OFFSET: usize = 184;
const PAYLOAD_DATABASE_ID_OFFSET: usize = 248;
const PAYLOAD_TRANSACTION_ID_OFFSET: usize = 264;
const PAYLOAD_COMMIT_NONCE_OFFSET: usize = 272;
const CREATION_SECURITY_KIND_OFFSET: usize = 288;
const DIRECTORY_ROLE_OFFSET: usize = 290;
const CREATION_SECURITY_COMMITMENT_OFFSET: usize = 296;
const CRC_OFFSET: usize = 508;
const CRC_SIZE: usize = core::mem::size_of::<u32>();
const SOURCE_LENGTH_OFFSET: usize = 328;
const SEQUENCE_OFFSET: usize = 496;
const SOURCE_OFFSET: usize = 512;
const SOURCE_CAPACITY: usize = PAGE_SIZE - SOURCE_OFFSET;
const PAYLOAD_RESERVED: (usize, usize) = (138, 6);
const SECURITY_RESERVED: (usize, usize) = (292, 4);
const HEADER_TAIL_RESERVED: (usize, usize) = (332, 164);
const PRE_CRC_RESERVED: (usize, usize) = (504, 4);

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
        block.write(MAGIC_OFFSET, &MAGIC)?;
        block.put_u16(RECORD_SIZE_OFFSET, RECORD_SIZE)?;
        block.put_u16(VERSION_OFFSET, VERSION)?;
        block.put_u16(KIND_OFFSET, kind_code(self.kind))?;
        block.put_u16(BASENAME_ENCODING_OFFSET, self.basename_encoding)?;
        block.write(ATTEMPT_ID_OFFSET, &self.attempt_id)?;
        block.put_u32(ORDINAL_OFFSET, self.ordinal)?;
        block.put_u16(DIRECTORY_IDENTITY_KIND_OFFSET, self.directory_identity_kind)?;
        block.put_u16(ARTIFACT_IDENTITY_KIND_OFFSET, self.artifact_identity_kind)?;
        block.write(DIRECTORY_IDENTITY_OFFSET, &self.directory_identity)?;
        block.write(SOURCE_COMMITMENT_OFFSET, &self.source_commitment)?;
        block.write(INERT_COMMITMENT_OFFSET, &self.inert_commitment)?;
        block.write(ARTIFACT_IDENTITY_OFFSET, &self.artifact_identity)?;
        if let Some(payload) = self.payload {
            block.put_u16(PAYLOAD_PRESENT_OFFSET, 1)?;
            block.put_u64(PAYLOAD_LENGTH_OFFSET, payload.byte_length)?;
            block.write(PAYLOAD_SHA512_OFFSET, &payload.sha512)?;
            block.write(PAYLOAD_DATABASE_ID_OFFSET, &payload.database_id)?;
            block.put_u64(PAYLOAD_TRANSACTION_ID_OFFSET, payload.transaction_id)?;
            block.write(PAYLOAD_COMMIT_NONCE_OFFSET, &payload.commit_nonce)?;
        }
        block.put_u16(CREATION_SECURITY_KIND_OFFSET, self.creation_security_kind)?;
        block.put_u16(
            DIRECTORY_ROLE_OFFSET,
            directory_role_code(self.directory_role),
        )?;
        block.write(
            CREATION_SECURITY_COMMITMENT_OFFSET,
            &self.creation_security_commitment,
        )?;
        block.put_u32(
            SOURCE_LENGTH_OFFSET,
            u32::try_from(self.source_basename.len()).expect("bounded GC source filename"),
        )?;
        block.put_u64(SEQUENCE_OFFSET, self.sequence)?;
        block.write(SOURCE_OFFSET, &self.source_basename)?;
        let checksum = crc32c::crc32c_source_with_zeroed(block.view(), CRC_OFFSET, CRC_SIZE)
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
    let kind = decode_kind(u16_le(block, KIND_OFFSET))?;
    let payload = decode_payload(block)?;
    let source_basename = decode_source_basename(block)?;
    let header = Header {
        kind,
        basename_encoding: u16_le(block, BASENAME_ENCODING_OFFSET),
        attempt_id: array(block, ATTEMPT_ID_OFFSET),
        ordinal: u32_le(block, ORDINAL_OFFSET),
        directory_identity_kind: u16_le(block, DIRECTORY_IDENTITY_KIND_OFFSET),
        artifact_identity_kind: u16_le(block, ARTIFACT_IDENTITY_KIND_OFFSET),
        directory_identity: array(block, DIRECTORY_IDENTITY_OFFSET),
        source_commitment: array(block, SOURCE_COMMITMENT_OFFSET),
        inert_commitment: array(block, INERT_COMMITMENT_OFFSET),
        artifact_identity: array(block, ARTIFACT_IDENTITY_OFFSET),
        payload,
        creation_security_kind: u16_le(block, CREATION_SECURITY_KIND_OFFSET),
        directory_role: decode_directory_role(u16_le(block, DIRECTORY_ROLE_OFFSET))?,
        creation_security_commitment: array(block, CREATION_SECURITY_COMMITMENT_OFFSET),
        source_basename,
        sequence: u64_le(block, SEQUENCE_OFFSET),
    };
    valid(&header).then_some(header)
}

fn fixed_valid<P: ByteSource>(block: P) -> bool {
    block.equals(MAGIC_OFFSET, &MAGIC)
        && u16_le(block, RECORD_SIZE_OFFSET) == RECORD_SIZE
        && u16_le(block, VERSION_OFFSET) == VERSION
        && basename_encoding(u16_le(block, BASENAME_ENCODING_OFFSET)).is_some()
}

fn reserved_zero<P: ByteSource>(block: P) -> bool {
    block.all_zero(PAYLOAD_RESERVED.0, PAYLOAD_RESERVED.1)
        && block.all_zero(SECURITY_RESERVED.0, SECURITY_RESERVED.1)
        && block.all_zero(HEADER_TAIL_RESERVED.0, HEADER_TAIL_RESERVED.1)
        && block.all_zero(PRE_CRC_RESERVED.0, PRE_CRC_RESERVED.1)
}

fn checksum_valid<P: ByteSource>(block: P) -> bool {
    crc32c::crc32c_source_with_zeroed(block, CRC_OFFSET, CRC_SIZE)
        == Some(u32_le(block, CRC_OFFSET))
}

fn decode_payload<P: ByteSource>(block: P) -> Option<Option<Payload>> {
    match u16_le(block, PAYLOAD_PRESENT_OFFSET) {
        0 if payload_fields_zero(block) => Some(None),
        1 => Some(Some(Payload {
            byte_length: u64_le(block, PAYLOAD_LENGTH_OFFSET),
            sha512: array(block, PAYLOAD_SHA512_OFFSET),
            database_id: array(block, PAYLOAD_DATABASE_ID_OFFSET),
            transaction_id: u64_le(block, PAYLOAD_TRANSACTION_ID_OFFSET),
            commit_nonce: array(block, PAYLOAD_COMMIT_NONCE_OFFSET),
        })),
        _ => None,
    }
}

fn payload_fields_zero<P: ByteSource>(block: P) -> bool {
    block.all_zero(
        PAYLOAD_LENGTH_OFFSET,
        CREATION_SECURITY_KIND_OFFSET - PAYLOAD_LENGTH_OFFSET,
    )
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
#[path = "gc_codec_tests.rs"]
mod tests;
