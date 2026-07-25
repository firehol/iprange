//! Exact dual-block namespace-publication reservation.

use crate::contract::{u16_le, u32_le, u64_le, PAGE_SIZE};
use crate::crc32c;
use crate::slotted_page::{put_u16, put_u32, put_u64};

use super::namespace::{BASENAME_ENCODING_KIND, CREATION_SECURITY_KIND, IDENTITY_KIND};

const MAGIC: [u8; 8] = *b"IPR4RSV1";
const RECORD_SIZE: u16 = 512;
const VERSION: u16 = 1;
const FILE_SIZE: usize = 2 * PAGE_SIZE;
const CRC_OFFSET: usize = 508;
const PREVIOUS_PRESENT: u32 = 1;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u32)]
pub(crate) enum State {
    Prepared = 1,
    MainMayHaveBeenAttempted = 2,
}

impl State {
    fn decode(value: u32) -> Option<Self> {
        match value {
            1 => Some(Self::Prepared),
            2 => Some(Self::MainMayHaveBeenAttempted),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u16)]
pub(crate) enum Policy {
    FailIfExists = 1,
    ReplaceExisting = 2,
}

impl Policy {
    fn decode(value: u16) -> Option<Self> {
        match value {
            1 => Some(Self::FailIfExists),
            2 => Some(Self::ReplaceExisting),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Previous {
    pub(crate) identity: [u8; 32],
    pub(crate) byte_length: u64,
    pub(crate) sha512: [u8; 64],
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Header {
    pub(crate) state: State,
    pub(crate) database_id: [u8; 16],
    pub(crate) transaction_id: u64,
    pub(crate) commit_nonce: [u8; 16],
    pub(crate) attempt_id: [u8; 16],
    pub(crate) reservation_identity: [u8; 32],
    pub(crate) policy: Policy,
    pub(crate) output_byte_length: u64,
    pub(crate) output_identity: [u8; 32],
    pub(crate) output_sha512: [u8; 64],
    pub(crate) previous: Option<Previous>,
    pub(crate) basename_len: u32,
    pub(crate) basename_commitment: [u8; 32],
    pub(crate) security_commitment: [u8; 32],
    pub(crate) sequence: u64,
}

impl Header {
    pub(crate) fn encode(self, block: &mut [u8; PAGE_SIZE]) {
        block.fill(0);
        block[..8].copy_from_slice(&MAGIC);
        put_u16(block, 8, RECORD_SIZE);
        put_u16(block, 10, VERSION);
        put_u32(block, 12, self.state as u32);
        block[16..32].copy_from_slice(&self.database_id);
        put_u64(block, 32, self.transaction_id);
        block[40..56].copy_from_slice(&self.commit_nonce);
        block[56..72].copy_from_slice(&self.attempt_id);
        put_u16(block, 72, IDENTITY_KIND);
        block[80..112].copy_from_slice(&self.reservation_identity);
        put_u16(block, 112, self.policy as u16);
        put_u16(block, 114, IDENTITY_KIND);
        put_u64(block, 120, self.output_byte_length);
        block[128..160].copy_from_slice(&self.output_identity);
        block[160..224].copy_from_slice(&self.output_sha512);
        if let Some(previous) = self.previous {
            put_u32(block, 116, PREVIOUS_PRESENT);
            block[224..256].copy_from_slice(&previous.identity);
            block[256..320].copy_from_slice(&previous.sha512);
            put_u64(block, 452, previous.byte_length);
        }
        put_u16(block, 412, BASENAME_ENCODING_KIND);
        put_u32(block, 416, self.basename_len);
        block[420..452].copy_from_slice(&self.basename_commitment);
        put_u16(block, 460, CREATION_SECURITY_KIND);
        block[464..496].copy_from_slice(&self.security_commitment);
        put_u64(block, 496, self.sequence);
        let checksum =
            crc32c::crc32c_with_zeroed(block, CRC_OFFSET, 4).expect("fixed reservation CRC field");
        put_u32(block, CRC_OFFSET, checksum);
    }

    pub(crate) fn state2(self) -> Option<Self> {
        if self.state != State::Prepared || self.sequence != 1 {
            return None;
        }
        Some(Self {
            state: State::MainMayHaveBeenAttempted,
            sequence: 2,
            ..self
        })
    }

    fn attempt_eq(self, other: Self) -> bool {
        Self {
            state: State::Prepared,
            sequence: 1,
            ..self
        } == Self {
            state: State::Prepared,
            sequence: 1,
            ..other
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Selected {
    pub(crate) header: Header,
    pub(crate) block: usize,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Problem {
    Magic,
    Fixed,
    Reserved,
    Checksum,
    State,
    Attempt,
    Identity,
    Policy,
    Output,
    Previous,
    Basename,
    Security,
    Sequence,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum SelectError {
    WrongSize,
    NoValidHeader { block0: Problem, block1: Problem },
    EqualSequenceDisagreement,
    NonAdjacentSequence,
    AttemptMismatch,
    InvalidTransition,
}

#[derive(Clone, Copy)]
struct AttemptFields {
    database_id: [u8; 16],
    transaction_id: u64,
    commit_nonce: [u8; 16],
    attempt_id: [u8; 16],
}

#[derive(Clone, Copy)]
struct OutputFields {
    byte_length: u64,
    identity: [u8; 32],
    sha512: [u8; 64],
}

#[derive(Clone, Copy)]
struct CoreFields {
    state: State,
    policy: Policy,
    attempt: AttemptFields,
    reservation_identity: [u8; 32],
    output: OutputFields,
}

pub(crate) fn select(bytes: &[u8]) -> Result<Selected, SelectError> {
    if bytes.len() != FILE_SIZE {
        return Err(SelectError::WrongSize);
    }
    let left: &[u8; PAGE_SIZE] = bytes[..PAGE_SIZE].try_into().expect("checked size");
    let right: &[u8; PAGE_SIZE] = bytes[PAGE_SIZE..].try_into().expect("checked size");
    match (decode(left), decode(right)) {
        (Err(block0), Err(block1)) => Err(SelectError::NoValidHeader { block0, block1 }),
        (Ok(header), Err(_)) => selected_at(header, 0),
        (Err(_), Ok(header)) => selected_at(header, 1),
        (Ok(left_header), Ok(right_header)) => select_pair(left, left_header, right, right_header),
    }
}

pub(crate) fn contains_selectable_header(bytes: &[u8]) -> bool {
    if bytes.len() != FILE_SIZE {
        return false;
    }
    let left: &[u8; PAGE_SIZE] = bytes[..PAGE_SIZE].try_into().expect("checked size");
    let right: &[u8; PAGE_SIZE] = bytes[PAGE_SIZE..].try_into().expect("checked size");
    decode(left).is_ok() || decode(right).is_ok()
}

fn selected_at(header: Header, block: usize) -> Result<Selected, SelectError> {
    if header.sequence != block as u64 + 1 {
        return Err(SelectError::InvalidTransition);
    }
    Ok(Selected { header, block })
}

fn select_pair(
    left: &[u8; PAGE_SIZE],
    left_header: Header,
    right: &[u8; PAGE_SIZE],
    right_header: Header,
) -> Result<Selected, SelectError> {
    if left_header.sequence == right_header.sequence {
        return if left == right {
            selected_at(left_header, 0)
        } else {
            Err(SelectError::EqualSequenceDisagreement)
        };
    }
    let (older, newer, block) = if left_header.sequence < right_header.sequence {
        (left_header, right_header, 1)
    } else {
        (right_header, left_header, 0)
    };
    if older.sequence.checked_add(1) != Some(newer.sequence) {
        return Err(SelectError::NonAdjacentSequence);
    }
    if !older.attempt_eq(newer) {
        return Err(SelectError::AttemptMismatch);
    }
    if (older.state, older.sequence, newer.state, newer.sequence)
        != (State::Prepared, 1, State::MainMayHaveBeenAttempted, 2)
    {
        return Err(SelectError::InvalidTransition);
    }
    Ok(Selected {
        header: newer,
        block,
    })
}

fn decode(block: &[u8; PAGE_SIZE]) -> Result<Header, Problem> {
    let core = decode_core(block)?;
    let previous = decode_previous(
        block,
        core.policy,
        core.reservation_identity,
        core.output.identity,
    )?;
    let basename_len = decode_basename_len(block)?;
    let security_commitment = decode_security(block)?;
    let sequence = decode_sequence(block, core.state)?;

    Ok(Header {
        state: core.state,
        database_id: core.attempt.database_id,
        transaction_id: core.attempt.transaction_id,
        commit_nonce: core.attempt.commit_nonce,
        attempt_id: core.attempt.attempt_id,
        reservation_identity: core.reservation_identity,
        policy: core.policy,
        output_byte_length: core.output.byte_length,
        output_identity: core.output.identity,
        output_sha512: core.output.sha512,
        previous,
        basename_len,
        basename_commitment: array(block, 420),
        security_commitment,
        sequence,
    })
}

fn decode_security(block: &[u8; PAGE_SIZE]) -> Result<[u8; 32], Problem> {
    let commitment = array(block, 464);
    if commitment == [0; 32] {
        Err(Problem::Security)
    } else {
        Ok(commitment)
    }
}

fn decode_core(block: &[u8; PAGE_SIZE]) -> Result<CoreFields, Problem> {
    require_fixed(block)?;
    require_zeroes(block)?;
    require_checksum(block)?;

    let state = State::decode(u32_le(block, 12)).ok_or(Problem::State)?;
    let policy = Policy::decode(u16_le(block, 112)).ok_or(Problem::Policy)?;
    let attempt = decode_attempt(block)?;
    let reservation_identity = decode_reservation_identity(block)?;
    let output = decode_output(block, reservation_identity)?;
    Ok(CoreFields {
        state,
        policy,
        attempt,
        reservation_identity,
        output,
    })
}

fn decode_attempt(block: &[u8; PAGE_SIZE]) -> Result<AttemptFields, Problem> {
    let fields = AttemptFields {
        database_id: array(block, 16),
        transaction_id: u64_le(block, 32),
        commit_nonce: array(block, 40),
        attempt_id: array(block, 56),
    };
    if fields.database_id == [0; 16]
        || fields.transaction_id == 0
        || fields.commit_nonce == [0; 16]
        || fields.attempt_id == [0; 16]
    {
        return Err(Problem::Attempt);
    }
    Ok(fields)
}

fn decode_reservation_identity(block: &[u8; PAGE_SIZE]) -> Result<[u8; 32], Problem> {
    let identity = array(block, 80);
    if valid_identity(&identity) {
        Ok(identity)
    } else {
        Err(Problem::Identity)
    }
}

fn decode_output(
    block: &[u8; PAGE_SIZE],
    reservation_identity: [u8; 32],
) -> Result<OutputFields, Problem> {
    let fields = OutputFields {
        byte_length: u64_le(block, 120),
        identity: array(block, 128),
        sha512: array(block, 160),
    };
    if fields.byte_length < FILE_SIZE as u64
        || fields.byte_length % PAGE_SIZE as u64 != 0
        || !valid_identity(&fields.identity)
        || fields.identity == reservation_identity
    {
        return Err(Problem::Output);
    }
    Ok(fields)
}

fn decode_basename_len(block: &[u8; PAGE_SIZE]) -> Result<u32, Problem> {
    match u32_le(block, 416) {
        0 => Err(Problem::Basename),
        length => Ok(length),
    }
}

fn decode_sequence(block: &[u8; PAGE_SIZE], state: State) -> Result<u64, Problem> {
    let sequence = u64_le(block, 496);
    match (state, sequence) {
        (State::Prepared, 1) | (State::MainMayHaveBeenAttempted, 2) => Ok(sequence),
        _ => Err(Problem::Sequence),
    }
}

fn require_fixed(block: &[u8; PAGE_SIZE]) -> Result<(), Problem> {
    if block[..8] != MAGIC {
        return Err(Problem::Magic);
    }
    if u16_le(block, 8) != RECORD_SIZE
        || u16_le(block, 10) != VERSION
        || u16_le(block, 72) != IDENTITY_KIND
        || u16_le(block, 114) != IDENTITY_KIND
        || u16_le(block, 412) != BASENAME_ENCODING_KIND
        || u16_le(block, 460) != CREATION_SECURITY_KIND
    {
        return Err(Problem::Fixed);
    }
    Ok(())
}

fn require_zeroes(block: &[u8; PAGE_SIZE]) -> Result<(), Problem> {
    let reserved = [
        &block[74..80],
        &block[320..412],
        &block[414..416],
        &block[462..464],
        &block[504..508],
        &block[512..],
    ];
    if reserved
        .into_iter()
        .any(|bytes| bytes.iter().any(|&byte| byte != 0))
    {
        return Err(Problem::Reserved);
    }
    Ok(())
}

fn require_checksum(block: &[u8; PAGE_SIZE]) -> Result<(), Problem> {
    if crc32c::crc32c_with_zeroed(block, CRC_OFFSET, 4) != Some(u32_le(block, CRC_OFFSET)) {
        return Err(Problem::Checksum);
    }
    Ok(())
}

fn decode_previous(
    block: &[u8; PAGE_SIZE],
    policy: Policy,
    reservation_identity: [u8; 32],
    output_identity: [u8; 32],
) -> Result<Option<Previous>, Problem> {
    let flags = u32_le(block, 116);
    let identity = array(block, 224);
    let sha512 = array(block, 256);
    let byte_length = u64_le(block, 452);
    match policy {
        Policy::FailIfExists => decode_absent_previous(flags, identity, sha512, byte_length),
        Policy::ReplaceExisting => decode_present_previous(
            flags,
            identity,
            byte_length,
            sha512,
            reservation_identity,
            output_identity,
        ),
    }
}

fn decode_absent_previous(
    flags: u32,
    identity: [u8; 32],
    sha512: [u8; 64],
    byte_length: u64,
) -> Result<Option<Previous>, Problem> {
    if flags != 0 || identity != [0; 32] || sha512 != [0; 64] || byte_length != 0 {
        return Err(Problem::Previous);
    }
    Ok(None)
}

fn decode_present_previous(
    flags: u32,
    identity: [u8; 32],
    byte_length: u64,
    sha512: [u8; 64],
    reservation_identity: [u8; 32],
    output_identity: [u8; 32],
) -> Result<Option<Previous>, Problem> {
    if flags != PREVIOUS_PRESENT
        || !valid_identity(&identity)
        || identity == reservation_identity
        || identity == output_identity
    {
        return Err(Problem::Previous);
    }
    Ok(Some(Previous {
        identity,
        byte_length,
        sha512,
    }))
}

fn valid_identity(identity: &[u8; 32]) -> bool {
    identity != &[0; 32] && identity[16..].iter().all(|&byte| byte == 0)
}

fn array<const N: usize>(bytes: &[u8], offset: usize) -> [u8; N] {
    bytes[offset..offset + N].try_into().expect("fixed field")
}

#[cfg(test)]
#[path = "reservation_tests.rs"]
mod tests;
