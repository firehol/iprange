//! Exact reader-table header codec and file geometry.

use std::fs::File;

use crate::contract::{u16_le, u32_le, PAGE_SIZE};
use crate::crc32c;
use crate::error::{Error, Result};
use crate::mapping::{ByteSource, Mapping, PageMut};

use super::slot::SIZE as SLOT_SIZE;

const MAGIC: [u8; 8] = *b"IPRDRS4\0";
const HEADER_SIZE: u16 = 68;
const MAGIC_OFFSET: usize = 0;
const HEADER_SIZE_OFFSET: usize = 8;
const SLOT_SIZE_OFFSET: usize = 10;
const STATE_OFFSET: usize = 12;
const CAPACITY_OFFSET: usize = 16;
const POLICY_GENERATION_OFFSET: usize = 20;
const POLICY_OFFSET: usize = 22;
const POLICY_RESERVED_OFFSET: usize = 23;
const DATABASE_ID_OFFSET: usize = 32;
const SIDECAR_ID_OFFSET: usize = 48;
const POLICY_GENERATION_V1: u16 = 1;
const POLICY_UNPROTECTED: u8 = 0;
const POLICY_PROTECTED: u8 = 1;
const HEADER_CRC: usize = 64;
const HEADER_CRC_SIZE: usize = core::mem::size_of::<u32>();
const STATE_CREATING: u32 = 0;
const STATE_READY: u32 = 1;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum State {
    Creating,
    Ready,
}

impl State {
    const fn wire(self) -> u32 {
        match self {
            Self::Creating => STATE_CREATING,
            Self::Ready => STATE_READY,
        }
    }

    const fn from_wire(value: u32) -> Option<Self> {
        match value {
            STATE_CREATING => Some(Self::Creating),
            STATE_READY => Some(Self::Ready),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Header {
    pub(crate) capacity: u32,
    pub(crate) database_id: [u8; 16],
    pub(crate) sidecar_id: [u8; 16],
    pub(crate) policy: Policy,
}

/// Whether open checks creator-only access, as recorded in the sidecar.
///
/// Generation 0 is a sidecar written before the create flag existed.
/// Those bytes are zero, and open still runs the check. Generation 1
/// records the flag: protected runs the check, unprotected does not,
/// even when the process umask happened to produce mode 0600.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Policy {
    Legacy,
    Unprotected,
    Protected,
}

pub(super) fn write_header_mapping(
    mapping: &mut Mapping,
    header: Header,
    state: State,
) -> Result<()> {
    let mut page = mapping.page_mut(0, 1)?;
    encode_header(&mut page, header, state)
}

fn encode_header(page: &mut PageMut<'_>, header: Header, state: State) -> Result<()> {
    page.fill(0);
    page.write(MAGIC_OFFSET, &MAGIC)?;
    page.put_u16(HEADER_SIZE_OFFSET, HEADER_SIZE)?;
    page.put_u16(SLOT_SIZE_OFFSET, SLOT_SIZE)?;
    page.put_u32(STATE_OFFSET, state.wire())?;
    page.put_u32(CAPACITY_OFFSET, header.capacity)?;
    encode_policy(page, header.policy)?;
    page.write(DATABASE_ID_OFFSET, &header.database_id)?;
    page.write(SIDECAR_ID_OFFSET, &header.sidecar_id)?;
    let checksum = crc32c::crc32c_source_with_zeroed(page.view(), HEADER_CRC, HEADER_CRC_SIZE)
        .ok_or(Error::Corrupt("reader table checksum field is invalid"))?;
    page.put_u32(HEADER_CRC, checksum)
}

pub(crate) fn read_header(file: &File) -> Result<(State, Header)> {
    let mapping = Mapping::read_only_view(file, PAGE_SIZE as u64)?;
    read_header_mapping(&mapping)
}

pub(super) fn read_header_mapping(mapping: &Mapping) -> Result<(State, Header)> {
    let page = mapping.page(0, 1)?;
    if !header_shape_valid(page) || !header_checksum_valid(page) {
        return Err(Error::Corrupt("reader table header is invalid"));
    }
    let state = State::from_wire(u32_le(page, STATE_OFFSET))
        .ok_or(Error::Corrupt("reader table state is invalid"))?;
    let database_id = page
        .array(DATABASE_ID_OFFSET)
        .ok_or(Error::Corrupt("reader table identity is invalid"))?;
    let sidecar_id = page
        .array(SIDECAR_ID_OFFSET)
        .ok_or(Error::Corrupt("reader table identity is invalid"))?;
    if database_id == [0; 16] || sidecar_id == [0; 16] {
        return Err(Error::Corrupt("reader table identity is invalid"));
    }
    Ok((
        state,
        Header {
            capacity: u32_le(page, CAPACITY_OFFSET),
            database_id,
            sidecar_id,
            policy: decode_policy(page)?,
        },
    ))
}

#[cfg(any(unix, windows))]
pub(crate) fn has_selectable_header(file: &File) -> Result<bool> {
    if file.metadata()?.len() < PAGE_SIZE as u64 {
        return Ok(false);
    }
    let mapping = Mapping::read_only_view(file, PAGE_SIZE as u64)?;
    let page = mapping.page(0, 1)?;
    Ok(header_shape_valid(page) && header_checksum_valid(page))
}

pub(super) fn sidecar_length(capacity: u32) -> Result<u64> {
    u64::from(capacity)
        .checked_mul(u64::from(SLOT_SIZE))
        .and_then(|bytes| bytes.checked_add(PAGE_SIZE as u64))
        .ok_or(Error::InvalidArgument("reader table length overflows"))
}

fn header_shape_valid<S: ByteSource>(page: S) -> bool {
    page.equals(MAGIC_OFFSET, &MAGIC)
        && u16_le(page, HEADER_SIZE_OFFSET) == HEADER_SIZE
        && u16_le(page, SLOT_SIZE_OFFSET) == SLOT_SIZE
        && State::from_wire(u32_le(page, STATE_OFFSET)).is_some()
        && u32_le(page, CAPACITY_OFFSET) != 0
        && policy_span_canonical(page)
        && page.all_zero(HEADER_SIZE as usize, PAGE_SIZE - HEADER_SIZE as usize)
}

fn encode_policy(page: &mut PageMut<'_>, policy: Policy) -> Result<()> {
    let (generation, byte) = match policy {
        Policy::Legacy => (0, 0),
        Policy::Unprotected => (POLICY_GENERATION_V1, POLICY_UNPROTECTED),
        Policy::Protected => (POLICY_GENERATION_V1, POLICY_PROTECTED),
    };
    page.put_u16(POLICY_GENERATION_OFFSET, generation)?;
    page.write(POLICY_OFFSET, &[byte])
}

fn decode_policy<S: ByteSource>(page: S) -> Result<Policy> {
    match (
        u16_le(page, POLICY_GENERATION_OFFSET),
        page.byte(POLICY_OFFSET).unwrap_or(0xff),
    ) {
        (0, 0) => Ok(Policy::Legacy),
        (POLICY_GENERATION_V1, POLICY_UNPROTECTED) => Ok(Policy::Unprotected),
        (POLICY_GENERATION_V1, POLICY_PROTECTED) => Ok(Policy::Protected),
        _ => Err(Error::Corrupt("reader table protection policy is invalid")),
    }
}

fn policy_span_canonical<S: ByteSource>(page: S) -> bool {
    let generation = u16_le(page, POLICY_GENERATION_OFFSET);
    let byte = page.byte(POLICY_OFFSET).unwrap_or(0xff);
    let tail_zero = page.all_zero(
        POLICY_RESERVED_OFFSET,
        DATABASE_ID_OFFSET - POLICY_RESERVED_OFFSET,
    );
    tail_zero
        && match (generation, byte) {
            (0, 0) | (POLICY_GENERATION_V1, POLICY_UNPROTECTED | POLICY_PROTECTED) => true,
            _ => false,
        }
}

fn header_checksum_valid<S: ByteSource>(page: S) -> bool {
    crc32c::crc32c_source_with_zeroed(page, HEADER_CRC, HEADER_CRC_SIZE)
        == Some(u32_le(page, HEADER_CRC))
}
