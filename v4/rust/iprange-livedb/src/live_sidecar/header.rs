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
const DATABASE_ID_OFFSET: usize = 32;
const SIDECAR_ID_OFFSET: usize = 48;
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
        && page.all_zero(
            CAPACITY_OFFSET + core::mem::size_of::<u32>(),
            DATABASE_ID_OFFSET - CAPACITY_OFFSET - core::mem::size_of::<u32>(),
        )
        && page.all_zero(HEADER_SIZE as usize, PAGE_SIZE - HEADER_SIZE as usize)
}

fn header_checksum_valid<S: ByteSource>(page: S) -> bool {
    crc32c::crc32c_source_with_zeroed(page, HEADER_CRC, HEADER_CRC_SIZE)
        == Some(u32_le(page, HEADER_CRC))
}
