//! Exact reader-table header codec and file geometry.

use std::fs::File;

use crate::contract::{u16_le, u32_le, PAGE_SIZE};
use crate::crc32c;
use crate::error::{Error, Result};
use crate::file_io;
use crate::slotted_page::{put_u16, put_u32};

use super::SLOT_SIZE;

const MAGIC: [u8; 8] = *b"IPRDRS4\0";
const HEADER_SIZE: u16 = 68;
const HEADER_CRC: usize = 64;
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

pub(super) fn write_header(file: &File, header: Header, state: State) -> Result<()> {
    let mut page = [0; PAGE_SIZE];
    page[..8].copy_from_slice(&MAGIC);
    put_u16(&mut page, 8, HEADER_SIZE);
    put_u16(&mut page, 10, SLOT_SIZE);
    put_u32(&mut page, 12, state.wire());
    put_u32(&mut page, 16, header.capacity);
    page[32..48].copy_from_slice(&header.database_id);
    page[48..64].copy_from_slice(&header.sidecar_id);
    let checksum = crc32c::crc32c_with_zeroed(&page, HEADER_CRC, 4)
        .ok_or(Error::Corrupt("reader table checksum field is invalid"))?;
    put_u32(&mut page, HEADER_CRC, checksum);
    file_io::write_exact_at(file, &page, 0)
}

pub(crate) fn read_header(file: &File) -> Result<(State, Header)> {
    let mut page = [0; PAGE_SIZE];
    file_io::read_exact_at(file, &mut page, 0)?;
    if !header_shape_valid(&page) || !header_checksum_valid(&page) {
        return Err(Error::Corrupt("reader table header is invalid"));
    }
    let state = State::from_wire(u32_le(&page, 12))
        .ok_or(Error::Corrupt("reader table state is invalid"))?;
    let mut database_id = [0; 16];
    database_id.copy_from_slice(&page[32..48]);
    let mut sidecar_id = [0; 16];
    sidecar_id.copy_from_slice(&page[48..64]);
    if database_id == [0; 16] || sidecar_id == [0; 16] {
        return Err(Error::Corrupt("reader table identity is invalid"));
    }
    Ok((
        state,
        Header {
            capacity: u32_le(&page, 16),
            database_id,
            sidecar_id,
        },
    ))
}

pub(crate) fn has_selectable_header(file: &File) -> Result<bool> {
    if file.metadata()?.len() < PAGE_SIZE as u64 {
        return Ok(false);
    }
    let mut page = [0; PAGE_SIZE];
    file_io::read_exact_at(file, &mut page, 0)?;
    Ok(header_shape_valid(&page) && header_checksum_valid(&page))
}

pub(super) fn sidecar_length(capacity: u32) -> Result<u64> {
    u64::from(capacity)
        .checked_mul(u64::from(SLOT_SIZE))
        .and_then(|bytes| bytes.checked_add(PAGE_SIZE as u64))
        .ok_or(Error::InvalidArgument("reader table length overflows"))
}

fn header_shape_valid(page: &[u8; PAGE_SIZE]) -> bool {
    let fixed = (&page[..8], u16_le(page, 8), u16_le(page, 10));
    fixed == (&MAGIC, HEADER_SIZE, SLOT_SIZE)
        && State::from_wire(u32_le(page, 12)).is_some()
        && u32_le(page, 16) != 0
        && page[20..32].iter().all(|&byte| byte == 0)
        && page[68..].iter().all(|&byte| byte == 0)
}

fn header_checksum_valid(page: &[u8; PAGE_SIZE]) -> bool {
    crc32c::crc32c_with_zeroed(page, HEADER_CRC, 4) == Some(u32_le(page, HEADER_CRC))
}
