//! Exact dual-block namespace-publication reservation.

use crate::contract::{u16_le, u32_le, u64_le, PAGE_SIZE};
use crate::crc32c;
use crate::error::Result as SdkResult;
use crate::mapping::{ByteRange, ByteSource};
use crate::page_io::PageEdit;

use super::namespace::{BASENAME_ENCODING_KIND, CREATION_SECURITY_KIND, IDENTITY_KIND};

const MAGIC: [u8; 8] = *b"IPR4RSV1";
const RECORD_SIZE: u16 = 512;
const VERSION: u16 = 1;
pub(super) const FILE_SIZE: usize = 2 * PAGE_SIZE;
pub(super) const OPERATION_LOCK: u64 = 0;
const MAGIC_OFFSET: usize = 0;
const RECORD_SIZE_OFFSET: usize = 8;
const VERSION_OFFSET: usize = 10;
const STATE_OFFSET: usize = 12;
const DATABASE_ID_OFFSET: usize = 16;
const TRANSACTION_ID_OFFSET: usize = 32;
const COMMIT_NONCE_OFFSET: usize = 40;
const ATTEMPT_ID_OFFSET: usize = 56;
const RESERVATION_IDENTITY_KIND_OFFSET: usize = 72;
const RESERVATION_IDENTITY_OFFSET: usize = 80;
const POLICY_OFFSET: usize = 112;
const OUTPUT_IDENTITY_KIND_OFFSET: usize = 114;
const PREVIOUS_FLAGS_OFFSET: usize = 116;
const OUTPUT_LENGTH_OFFSET: usize = 120;
const OUTPUT_IDENTITY_OFFSET: usize = 128;
const OUTPUT_SHA512_OFFSET: usize = 160;
const PREVIOUS_IDENTITY_OFFSET: usize = 224;
const PREVIOUS_SHA512_OFFSET: usize = 256;
const BASENAME_ENCODING_OFFSET: usize = 412;
const BASENAME_LENGTH_OFFSET: usize = 416;
const BASENAME_COMMITMENT_OFFSET: usize = 420;
const PREVIOUS_LENGTH_OFFSET: usize = 452;
const CREATION_SECURITY_KIND_OFFSET: usize = 460;
const SECURITY_COMMITMENT_OFFSET: usize = 464;
const SEQUENCE_OFFSET: usize = 496;
const CRC_OFFSET: usize = 508;
const CRC_SIZE: usize = core::mem::size_of::<u32>();
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
    ReplaceExistingNoRollback = 3,
}

impl Policy {
    fn decode(value: u16) -> Option<Self> {
        match value {
            1 => Some(Self::FailIfExists),
            2 => Some(Self::ReplaceExisting),
            3 => Some(Self::ReplaceExistingNoRollback),
            _ => None,
        }
    }

    pub(crate) const fn is_replacement(self) -> bool {
        matches!(
            self,
            Self::ReplaceExisting | Self::ReplaceExistingNoRollback
        )
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
    pub(crate) fn encode<P: PageEdit>(self, block: &mut P) -> SdkResult<()> {
        block.fill(0);
        block.write(MAGIC_OFFSET, &MAGIC)?;
        block.put_u16(RECORD_SIZE_OFFSET, RECORD_SIZE)?;
        block.put_u16(VERSION_OFFSET, VERSION)?;
        block.put_u32(STATE_OFFSET, self.state as u32)?;
        block.write(DATABASE_ID_OFFSET, &self.database_id)?;
        block.put_u64(TRANSACTION_ID_OFFSET, self.transaction_id)?;
        block.write(COMMIT_NONCE_OFFSET, &self.commit_nonce)?;
        block.write(ATTEMPT_ID_OFFSET, &self.attempt_id)?;
        block.put_u16(RESERVATION_IDENTITY_KIND_OFFSET, IDENTITY_KIND)?;
        block.write(RESERVATION_IDENTITY_OFFSET, &self.reservation_identity)?;
        block.put_u16(POLICY_OFFSET, self.policy as u16)?;
        block.put_u16(OUTPUT_IDENTITY_KIND_OFFSET, IDENTITY_KIND)?;
        block.put_u64(OUTPUT_LENGTH_OFFSET, self.output_byte_length)?;
        block.write(OUTPUT_IDENTITY_OFFSET, &self.output_identity)?;
        block.write(OUTPUT_SHA512_OFFSET, &self.output_sha512)?;
        if let Some(previous) = self.previous {
            block.put_u32(PREVIOUS_FLAGS_OFFSET, PREVIOUS_PRESENT)?;
            block.write(PREVIOUS_IDENTITY_OFFSET, &previous.identity)?;
            block.write(PREVIOUS_SHA512_OFFSET, &previous.sha512)?;
            block.put_u64(PREVIOUS_LENGTH_OFFSET, previous.byte_length)?;
        }
        block.put_u16(BASENAME_ENCODING_OFFSET, BASENAME_ENCODING_KIND)?;
        block.put_u32(BASENAME_LENGTH_OFFSET, self.basename_len)?;
        block.write(BASENAME_COMMITMENT_OFFSET, &self.basename_commitment)?;
        block.put_u16(CREATION_SECURITY_KIND_OFFSET, CREATION_SECURITY_KIND)?;
        block.write(SECURITY_COMMITMENT_OFFSET, &self.security_commitment)?;
        block.put_u64(SEQUENCE_OFFSET, self.sequence)?;
        let checksum = crc32c::crc32c_source_with_zeroed(block.view(), CRC_OFFSET, CRC_SIZE)
            .expect("fixed reservation CRC field");
        block.put_u32(CRC_OFFSET, checksum)
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

pub(crate) fn select<S: ByteSource>(bytes: S) -> Result<Selected, SelectError> {
    if bytes.len() != FILE_SIZE {
        return Err(SelectError::WrongSize);
    }
    let left = ByteRange::new(bytes, 0, PAGE_SIZE).expect("checked size");
    let right = ByteRange::new(bytes, PAGE_SIZE, PAGE_SIZE).expect("checked size");
    match (decode(left), decode(right)) {
        (Err(block0), Err(block1)) => Err(SelectError::NoValidHeader { block0, block1 }),
        (Ok(header), Err(_)) => selected_at(header, 0),
        (Err(_), Ok(header)) => selected_at(header, 1),
        (Ok(left_header), Ok(right_header)) => select_pair(left, left_header, right, right_header),
    }
}

pub(crate) fn contains_selectable_header<S: ByteSource>(bytes: S) -> bool {
    if bytes.len() != FILE_SIZE {
        return false;
    }
    let left = ByteRange::new(bytes, 0, PAGE_SIZE).expect("checked size");
    let right = ByteRange::new(bytes, PAGE_SIZE, PAGE_SIZE).expect("checked size");
    decode(left).is_ok() || decode(right).is_ok()
}

fn selected_at(header: Header, block: usize) -> Result<Selected, SelectError> {
    if header.sequence != block as u64 + 1 {
        return Err(SelectError::InvalidTransition);
    }
    Ok(Selected { header, block })
}

fn select_pair<P: ByteSource>(
    left: P,
    left_header: Header,
    right: P,
    right_header: Header,
) -> Result<Selected, SelectError> {
    if left_header.sequence == right_header.sequence {
        return if left.same(right, 0, PAGE_SIZE) {
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

fn decode<P: ByteSource>(block: P) -> Result<Header, Problem> {
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
        basename_commitment: array(block, BASENAME_COMMITMENT_OFFSET),
        security_commitment,
        sequence,
    })
}

fn decode_security<P: ByteSource>(block: P) -> Result<[u8; 32], Problem> {
    let commitment = array(block, SECURITY_COMMITMENT_OFFSET);
    if commitment == [0; 32] {
        Err(Problem::Security)
    } else {
        Ok(commitment)
    }
}

fn decode_core<P: ByteSource>(block: P) -> Result<CoreFields, Problem> {
    require_fixed(block)?;
    require_zeroes(block)?;
    require_checksum(block)?;

    let state = State::decode(u32_le(block, STATE_OFFSET)).ok_or(Problem::State)?;
    let policy = Policy::decode(u16_le(block, POLICY_OFFSET)).ok_or(Problem::Policy)?;
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

fn decode_attempt<P: ByteSource>(block: P) -> Result<AttemptFields, Problem> {
    let fields = AttemptFields {
        database_id: array(block, DATABASE_ID_OFFSET),
        transaction_id: u64_le(block, TRANSACTION_ID_OFFSET),
        commit_nonce: array(block, COMMIT_NONCE_OFFSET),
        attempt_id: array(block, ATTEMPT_ID_OFFSET),
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

fn decode_reservation_identity<P: ByteSource>(block: P) -> Result<[u8; 32], Problem> {
    let identity = array(block, RESERVATION_IDENTITY_OFFSET);
    if valid_identity(&identity) {
        Ok(identity)
    } else {
        Err(Problem::Identity)
    }
}

fn decode_output<P: ByteSource>(
    block: P,
    reservation_identity: [u8; 32],
) -> Result<OutputFields, Problem> {
    let fields = OutputFields {
        byte_length: u64_le(block, OUTPUT_LENGTH_OFFSET),
        identity: array(block, OUTPUT_IDENTITY_OFFSET),
        sha512: array(block, OUTPUT_SHA512_OFFSET),
    };
    if !crate::bootstrap::geometry_valid(fields.byte_length)
        || !valid_identity(&fields.identity)
        || fields.identity == reservation_identity
    {
        return Err(Problem::Output);
    }
    Ok(fields)
}

fn decode_basename_len<P: ByteSource>(block: P) -> Result<u32, Problem> {
    match u32_le(block, BASENAME_LENGTH_OFFSET) {
        0 => Err(Problem::Basename),
        length => Ok(length),
    }
}

fn decode_sequence<P: ByteSource>(block: P, state: State) -> Result<u64, Problem> {
    let sequence = u64_le(block, SEQUENCE_OFFSET);
    match (state, sequence) {
        (State::Prepared, 1) | (State::MainMayHaveBeenAttempted, 2) => Ok(sequence),
        _ => Err(Problem::Sequence),
    }
}

fn require_fixed<P: ByteSource>(block: P) -> Result<(), Problem> {
    if !block.equals(MAGIC_OFFSET, &MAGIC) {
        return Err(Problem::Magic);
    }
    if u16_le(block, RECORD_SIZE_OFFSET) != RECORD_SIZE
        || u16_le(block, VERSION_OFFSET) != VERSION
        || u16_le(block, RESERVATION_IDENTITY_KIND_OFFSET) != IDENTITY_KIND
        || u16_le(block, OUTPUT_IDENTITY_KIND_OFFSET) != IDENTITY_KIND
        || u16_le(block, BASENAME_ENCODING_OFFSET) != BASENAME_ENCODING_KIND
        || u16_le(block, CREATION_SECURITY_KIND_OFFSET) != CREATION_SECURITY_KIND
    {
        return Err(Problem::Fixed);
    }
    Ok(())
}

fn require_zeroes<P: ByteSource>(block: P) -> Result<(), Problem> {
    let reserved = [
        (
            RESERVATION_IDENTITY_KIND_OFFSET + core::mem::size_of::<u16>(),
            RESERVATION_IDENTITY_OFFSET
                - RESERVATION_IDENTITY_KIND_OFFSET
                - core::mem::size_of::<u16>(),
        ),
        (
            PREVIOUS_SHA512_OFFSET + 64,
            BASENAME_ENCODING_OFFSET - PREVIOUS_SHA512_OFFSET - 64,
        ),
        (
            BASENAME_ENCODING_OFFSET + core::mem::size_of::<u16>(),
            BASENAME_LENGTH_OFFSET - BASENAME_ENCODING_OFFSET - core::mem::size_of::<u16>(),
        ),
        (
            CREATION_SECURITY_KIND_OFFSET + core::mem::size_of::<u16>(),
            SECURITY_COMMITMENT_OFFSET
                - CREATION_SECURITY_KIND_OFFSET
                - core::mem::size_of::<u16>(),
        ),
        (
            SEQUENCE_OFFSET + core::mem::size_of::<u64>(),
            CRC_OFFSET - SEQUENCE_OFFSET - core::mem::size_of::<u64>(),
        ),
        (RECORD_SIZE as usize, PAGE_SIZE - RECORD_SIZE as usize),
    ];
    if reserved
        .into_iter()
        .any(|(at, len)| !block.all_zero(at, len))
    {
        return Err(Problem::Reserved);
    }
    Ok(())
}

fn require_checksum<P: ByteSource>(block: P) -> Result<(), Problem> {
    if crc32c::crc32c_source_with_zeroed(block, CRC_OFFSET, CRC_SIZE)
        != Some(u32_le(block, CRC_OFFSET))
    {
        return Err(Problem::Checksum);
    }
    Ok(())
}

fn decode_previous<P: ByteSource>(
    block: P,
    policy: Policy,
    reservation_identity: [u8; 32],
    output_identity: [u8; 32],
) -> Result<Option<Previous>, Problem> {
    let flags = u32_le(block, PREVIOUS_FLAGS_OFFSET);
    let identity = array(block, PREVIOUS_IDENTITY_OFFSET);
    let sha512 = array(block, PREVIOUS_SHA512_OFFSET);
    let byte_length = u64_le(block, PREVIOUS_LENGTH_OFFSET);
    match policy {
        Policy::FailIfExists => decode_absent_previous(flags, identity, sha512, byte_length),
        Policy::ReplaceExisting | Policy::ReplaceExistingNoRollback => decode_present_previous(
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

fn array<const N: usize, P: ByteSource>(bytes: P, offset: usize) -> [u8; N] {
    bytes.array(offset).expect("fixed field")
}

#[cfg(test)]
#[path = "reservation_tests.rs"]
mod tests;
