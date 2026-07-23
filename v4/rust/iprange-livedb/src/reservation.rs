//! Exact namespace-reservation header and conversion-selection codecs.

use crate::contract::{u16_le, u32_le, u64_le, PAGE_SIZE};
use crate::crc32c;
use crate::sidecar::{
    self, LocalIdentityKind, ProcessDomainKind, SidecarHeader, SidecarHeaderProblem, SidecarOrigin,
    SidecarState,
};

const RESERVATION_MAGIC: [u8; 8] = *b"IPR4RSV1";
const HEADER_RECORD_SIZE: u16 = 512;
const RESERVATION_VERSION: u16 = 1;
const HEADER_REGION_SIZE: usize = 2 * PAGE_SIZE;
const HEADER_CRC_OFFSET: usize = 508;
const FLAG_PREVIOUS_DESTINATION: u32 = 1 << 0;
const FLAG_PRIOR_SIDECAR: u32 = 1 << 1;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u32)]
pub(crate) enum ReservationState {
    Prepared = 1,
    MainNamespaceAttempted = 2,
}

impl ReservationState {
    const fn from_wire(value: u32) -> Option<Self> {
        match value {
            1 => Some(Self::Prepared),
            2 => Some(Self::MainNamespaceAttempted),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u16)]
pub(crate) enum ReservationOperation {
    FailIfExists = 1,
    ReplaceExisting = 2,
    CreateLive = 3,
    InitializeLive = 4,
    ResetLiveCoordination = 5,
}

impl ReservationOperation {
    const fn from_wire(value: u16) -> Option<Self> {
        match value {
            1 => Some(Self::FailIfExists),
            2 => Some(Self::ReplaceExisting),
            3 => Some(Self::CreateLive),
            4 => Some(Self::InitializeLive),
            5 => Some(Self::ResetLiveCoordination),
            _ => None,
        }
    }

    const fn live(self) -> bool {
        matches!(
            self,
            Self::CreateLive | Self::InitializeLive | Self::ResetLiveCoordination
        )
    }

    const fn sidecar_origin(self) -> Option<SidecarOrigin> {
        match self {
            Self::CreateLive => Some(SidecarOrigin::CreateLive),
            Self::InitializeLive => Some(SidecarOrigin::InitializeLive),
            Self::ResetLiveCoordination => Some(SidecarOrigin::ResetLiveCoordination),
            Self::FailIfExists | Self::ReplaceExisting => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct ReservationHeader {
    pub(crate) state: ReservationState,
    pub(crate) database_id: [u8; 16],
    pub(crate) attempted_txn_id: u64,
    pub(crate) attempted_commit_nonce: [u8; 16],
    pub(crate) reservation_id: [u8; 16],
    pub(crate) identity_kind: LocalIdentityKind,
    pub(crate) reservation_identity: [u8; 32],
    pub(crate) operation: ReservationOperation,
    pub(crate) output_identity_kind: Option<LocalIdentityKind>,
    pub(crate) record_flags: u32,
    pub(crate) attempted_output_bytes: u64,
    pub(crate) attempted_output_identity: [u8; 32],
    pub(crate) attempted_output_sha512: [u8; 64],
    pub(crate) previous_destination_identity: [u8; 32],
    pub(crate) previous_destination_sha512: [u8; 64],
    pub(crate) reader_capacity: u32,
    pub(crate) prior_coordination_identity: [u8; 32],
    pub(crate) prior_sidecar_id: [u8; 16],
    pub(crate) prior_reader_capacity: u32,
    pub(crate) process_domain_kind: Option<ProcessDomainKind>,
    pub(crate) process_domain_token: [u8; 32],
    pub(crate) basename_encoding: u16,
    pub(crate) basename_len: u32,
    pub(crate) basename_commitment: [u8; 32],
    pub(crate) previous_destination_bytes: u64,
    pub(crate) creation_security_kind: u16,
    pub(crate) creation_security_commitment: [u8; 32],
    pub(crate) header_seq: u64,
}

impl ReservationHeader {
    pub(crate) fn attempted_output_complete(self) -> bool {
        self.output_identity_kind.is_some()
    }

    pub(crate) fn encode_into(self, block: &mut [u8; PAGE_SIZE]) {
        block.fill(0);
        block[0..8].copy_from_slice(&RESERVATION_MAGIC);
        put_u16(block, 8, HEADER_RECORD_SIZE);
        put_u16(block, 10, RESERVATION_VERSION);
        put_u32(block, 12, self.state as u32);
        block[16..32].copy_from_slice(&self.database_id);
        put_u64(block, 32, self.attempted_txn_id);
        block[40..56].copy_from_slice(&self.attempted_commit_nonce);
        block[56..72].copy_from_slice(&self.reservation_id);
        put_u16(block, 72, self.identity_kind as u16);
        block[80..112].copy_from_slice(&self.reservation_identity);
        put_u16(block, 112, self.operation as u16);
        put_u16(
            block,
            114,
            self.output_identity_kind.map_or(0, |kind| kind as u16),
        );
        put_u32(block, 116, self.record_flags);
        put_u64(block, 120, self.attempted_output_bytes);
        block[128..160].copy_from_slice(&self.attempted_output_identity);
        block[160..224].copy_from_slice(&self.attempted_output_sha512);
        block[224..256].copy_from_slice(&self.previous_destination_identity);
        block[256..320].copy_from_slice(&self.previous_destination_sha512);
        put_u32(block, 320, self.reader_capacity);
        block[324..356].copy_from_slice(&self.prior_coordination_identity);
        block[356..372].copy_from_slice(&self.prior_sidecar_id);
        put_u32(block, 372, self.prior_reader_capacity);
        put_u16(
            block,
            376,
            self.process_domain_kind.map_or(0, |kind| kind as u16),
        );
        block[380..412].copy_from_slice(&self.process_domain_token);
        put_u16(block, 412, self.basename_encoding);
        put_u32(block, 416, self.basename_len);
        block[420..452].copy_from_slice(&self.basename_commitment);
        put_u64(block, 452, self.previous_destination_bytes);
        put_u16(block, 460, self.creation_security_kind);
        block[464..496].copy_from_slice(&self.creation_security_commitment);
        put_u64(block, 496, self.header_seq);
        let crc = crc32c::crc32c_with_zeroed(block, HEADER_CRC_OFFSET, 4).unwrap();
        put_u32(block, HEADER_CRC_OFFSET, crc);
    }

    fn immutable_attempt_eq(self, other: Self) -> bool {
        self.reservation_id == other.reservation_id
            && self.identity_kind == other.identity_kind
            && self.reservation_identity == other.reservation_identity
            && self.operation == other.operation
            && self.record_flags == other.record_flags
            && self.previous_destination_identity == other.previous_destination_identity
            && self.previous_destination_sha512 == other.previous_destination_sha512
            && self.reader_capacity == other.reader_capacity
            && self.prior_coordination_identity == other.prior_coordination_identity
            && self.prior_sidecar_id == other.prior_sidecar_id
            && self.prior_reader_capacity == other.prior_reader_capacity
            && self.basename_encoding == other.basename_encoding
            && self.basename_len == other.basename_len
            && self.basename_commitment == other.basename_commitment
            && self.previous_destination_bytes == other.previous_destination_bytes
            && self.creation_security_kind == other.creation_security_kind
            && self.creation_security_commitment == other.creation_security_commitment
    }

    fn attempted_output_eq(self, other: Self) -> bool {
        self.database_id == other.database_id
            && self.attempted_txn_id == other.attempted_txn_id
            && self.attempted_commit_nonce == other.attempted_commit_nonce
            && self.output_identity_kind == other.output_identity_kind
            && self.attempted_output_bytes == other.attempted_output_bytes
            && self.attempted_output_identity == other.attempted_output_identity
            && self.attempted_output_sha512 == other.attempted_output_sha512
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum ReservationHeaderProblem {
    Magic,
    FixedValue,
    Reserved,
    Checksum,
    State,
    DatabaseAttempt,
    ReservationId,
    IdentityKind,
    IdentityEncoding,
    Operation,
    Flags,
    AttemptedOutput,
    PreviousDestination,
    ReaderCapacity,
    PriorCoordination,
    ProcessDomain,
    Basename,
    CreationSecurity,
    Sequence,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum DomainSelectionPolicy {
    RequireSame,
    /// The caller already holds the resolver's offline locks; a changed newer
    /// block is accepted only when it contains this freshly derived domain.
    ResolverMayReplace {
        current_kind: ProcessDomainKind,
        current_token: [u8; 32],
    },
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum ReservationError {
    WrongFileSize {
        actual: u64,
    },
    NoValidHeader {
        block0: ReservationHeaderProblem,
        block1: ReservationHeaderProblem,
    },
    EqualSequenceDisagreement,
    NonAdjacentSequence {
        older: u64,
        newer: u64,
    },
    ImmutableAttemptMismatch,
    InvalidTransition,
}

/// Select the authoritative header of an exact 8,192-byte reservation.
pub(crate) fn select_reservation_header(
    bytes: &[u8],
    domain_policy: DomainSelectionPolicy,
) -> Result<ReservationHeader, ReservationError> {
    if bytes.len() != HEADER_REGION_SIZE {
        return Err(ReservationError::WrongFileSize {
            actual: u64::try_from(bytes.len()).unwrap_or(u64::MAX),
        });
    }
    let block0: &[u8; PAGE_SIZE] = bytes[..PAGE_SIZE].try_into().unwrap();
    let block1: &[u8; PAGE_SIZE] = bytes[PAGE_SIZE..].try_into().unwrap();
    let first = decode_header(block0);
    let second = decode_header(block1);
    match (first, second) {
        (Err(block0), Err(block1)) => Err(ReservationError::NoValidHeader { block0, block1 }),
        (Ok(header), Err(_)) | (Err(_), Ok(header)) => Ok(header),
        (Ok(left), Ok(right)) => {
            select_reservation_pair(block0, left, block1, right, domain_policy)
        }
    }
}

fn select_reservation_pair(
    left_block: &[u8; PAGE_SIZE],
    left: ReservationHeader,
    right_block: &[u8; PAGE_SIZE],
    right: ReservationHeader,
    domain_policy: DomainSelectionPolicy,
) -> Result<ReservationHeader, ReservationError> {
    if left.header_seq == right.header_seq {
        return if left_block == right_block {
            Ok(left)
        } else {
            Err(ReservationError::EqualSequenceDisagreement)
        };
    }
    let (older, newer) = if left.header_seq < right.header_seq {
        (left, right)
    } else {
        (right, left)
    };
    if older.header_seq.checked_add(1) != Some(newer.header_seq) {
        return Err(ReservationError::NonAdjacentSequence {
            older: older.header_seq,
            newer: newer.header_seq,
        });
    }
    if !reservation_transition_valid(older, newer, domain_policy) {
        return Err(if older.immutable_attempt_eq(newer) {
            ReservationError::InvalidTransition
        } else {
            ReservationError::ImmutableAttemptMismatch
        });
    }
    Ok(newer)
}

fn reservation_transition_valid(
    older: ReservationHeader,
    newer: ReservationHeader,
    domain_policy: DomainSelectionPolicy,
) -> bool {
    if !older.immutable_attempt_eq(newer)
        || !reservation_state_transition_valid(older.operation, older.state, newer.state)
        || !domain_transition_valid(
            older.operation,
            older.process_domain_kind,
            &older.process_domain_token,
            newer.process_domain_kind,
            &newer.process_domain_token,
            domain_policy,
        )
    {
        return false;
    }
    if older.attempted_output_eq(newer) {
        return true;
    }
    older.operation == ReservationOperation::CreateLive
        && !older.attempted_output_complete()
        && newer.attempted_output_complete()
        && older.state == ReservationState::Prepared
        && newer.state == ReservationState::Prepared
}

fn reservation_state_transition_valid(
    operation: ReservationOperation,
    older: ReservationState,
    newer: ReservationState,
) -> bool {
    if older == newer {
        return true;
    }
    matches!(
        (operation, older, newer),
        (
            ReservationOperation::FailIfExists | ReservationOperation::ReplaceExisting,
            ReservationState::Prepared,
            ReservationState::MainNamespaceAttempted
        )
    )
}

fn domain_transition_valid(
    operation: ReservationOperation,
    older_kind: Option<ProcessDomainKind>,
    older_token: &[u8; 32],
    newer_kind: Option<ProcessDomainKind>,
    newer_token: &[u8; 32],
    policy: DomainSelectionPolicy,
) -> bool {
    if older_kind == newer_kind && older_token == newer_token {
        return true;
    }
    if !operation.live() {
        return false;
    }
    match policy {
        DomainSelectionPolicy::RequireSame => false,
        DomainSelectionPolicy::ResolverMayReplace {
            current_kind,
            current_token,
        } => newer_kind == Some(current_kind) && newer_token == &current_token,
    }
}

pub(crate) fn decode_header(
    block: &[u8; PAGE_SIZE],
) -> Result<ReservationHeader, ReservationHeaderProblem> {
    if block[0..8] != RESERVATION_MAGIC {
        return Err(ReservationHeaderProblem::Magic);
    }
    if u16_le(block, 8) != HEADER_RECORD_SIZE || u16_le(block, 10) != RESERVATION_VERSION {
        return Err(ReservationHeaderProblem::FixedValue);
    }
    if block[74..80].iter().any(|&byte| byte != 0)
        || u16_le(block, 378) != 0
        || u16_le(block, 414) != 0
        || u16_le(block, 462) != 0
        || u32_le(block, 504) != 0
        || block[512..].iter().any(|&byte| byte != 0)
    {
        return Err(ReservationHeaderProblem::Reserved);
    }
    let stored_crc = u32_le(block, HEADER_CRC_OFFSET);
    if crc32c::crc32c_with_zeroed(block, HEADER_CRC_OFFSET, 4) != Some(stored_crc) {
        return Err(ReservationHeaderProblem::Checksum);
    }

    let state =
        ReservationState::from_wire(u32_le(block, 12)).ok_or(ReservationHeaderProblem::State)?;
    let reservation_id = array(block, 56);
    if reservation_id == [0; 16] {
        return Err(ReservationHeaderProblem::ReservationId);
    }
    let identity_kind = LocalIdentityKind::from_wire(u16_le(block, 72))
        .ok_or(ReservationHeaderProblem::IdentityKind)?;
    let reservation_identity = array(block, 80);
    if !sidecar::valid_local_identity(identity_kind, &reservation_identity) {
        return Err(ReservationHeaderProblem::IdentityEncoding);
    }
    let operation = ReservationOperation::from_wire(u16_le(block, 112))
        .ok_or(ReservationHeaderProblem::Operation)?;
    if state == ReservationState::MainNamespaceAttempted
        && !matches!(
            operation,
            ReservationOperation::FailIfExists | ReservationOperation::ReplaceExisting
        )
    {
        return Err(ReservationHeaderProblem::State);
    }

    let database_id = array(block, 16);
    let attempted_txn_id = u64_le(block, 32);
    let attempted_commit_nonce = array(block, 40);
    let output_identity_wire = u16_le(block, 114);
    let attempted_output_bytes = u64_le(block, 120);
    let attempted_output_identity = array(block, 128);
    let attempted_output_sha512 = array(block, 160);
    let empty_output = database_id == [0; 16]
        && attempted_txn_id == 0
        && attempted_commit_nonce == [0; 16]
        && output_identity_wire == 0
        && attempted_output_bytes == 0
        && attempted_output_identity == [0; 32]
        && attempted_output_sha512 == [0; 64];
    let output_identity_kind = if empty_output && operation == ReservationOperation::CreateLive {
        None
    } else {
        if database_id == [0; 16] || attempted_txn_id == 0 || attempted_commit_nonce == [0; 16] {
            return Err(ReservationHeaderProblem::DatabaseAttempt);
        }
        let kind = LocalIdentityKind::from_wire(output_identity_wire)
            .ok_or(ReservationHeaderProblem::AttemptedOutput)?;
        if kind != identity_kind
            || attempted_output_bytes < (2 * PAGE_SIZE) as u64
            || attempted_output_bytes % PAGE_SIZE as u64 != 0
            || !sidecar::valid_local_identity(kind, &attempted_output_identity)
            || attempted_output_identity == reservation_identity
        {
            return Err(ReservationHeaderProblem::AttemptedOutput);
        }
        Some(kind)
    };
    if output_identity_kind.is_none() && state != ReservationState::Prepared {
        return Err(ReservationHeaderProblem::AttemptedOutput);
    }

    let record_flags = u32_le(block, 116);
    if record_flags & !(FLAG_PREVIOUS_DESTINATION | FLAG_PRIOR_SIDECAR) != 0 {
        return Err(ReservationHeaderProblem::Flags);
    }
    let previous_destination_identity = array(block, 224);
    let previous_destination_sha512 = array(block, 256);
    let previous_destination_bytes = u64_le(block, 452);
    if operation == ReservationOperation::ReplaceExisting {
        if record_flags != FLAG_PREVIOUS_DESTINATION
            || previous_destination_bytes < (2 * PAGE_SIZE) as u64
            || previous_destination_bytes % PAGE_SIZE as u64 != 0
            || !sidecar::valid_local_identity(identity_kind, &previous_destination_identity)
        {
            return Err(ReservationHeaderProblem::PreviousDestination);
        }
    } else if record_flags & FLAG_PREVIOUS_DESTINATION != 0
        || previous_destination_identity != [0; 32]
        || previous_destination_sha512 != [0; 64]
        || previous_destination_bytes != 0
    {
        return Err(ReservationHeaderProblem::PreviousDestination);
    }

    let reader_capacity = u32_le(block, 320);
    if operation.live() != (reader_capacity != 0) {
        return Err(ReservationHeaderProblem::ReaderCapacity);
    }
    let prior_coordination_identity = array(block, 324);
    let prior_sidecar_id = array(block, 356);
    let prior_reader_capacity = u32_le(block, 372);
    if operation == ReservationOperation::ResetLiveCoordination {
        if prior_coordination_identity == [0; 32]
            || !sidecar::valid_local_identity(identity_kind, &prior_coordination_identity)
            || prior_coordination_identity == reservation_identity
            || (output_identity_kind.is_some()
                && prior_coordination_identity == attempted_output_identity)
        {
            return Err(ReservationHeaderProblem::PriorCoordination);
        }
        if record_flags & FLAG_PRIOR_SIDECAR != 0 {
            if prior_sidecar_id == [0; 16] || prior_reader_capacity == 0 {
                return Err(ReservationHeaderProblem::PriorCoordination);
            }
        } else if prior_sidecar_id != [0; 16] || prior_reader_capacity != 0 {
            return Err(ReservationHeaderProblem::PriorCoordination);
        }
    } else if record_flags & FLAG_PRIOR_SIDECAR != 0
        || prior_coordination_identity != [0; 32]
        || prior_sidecar_id != [0; 16]
        || prior_reader_capacity != 0
    {
        return Err(ReservationHeaderProblem::PriorCoordination);
    }

    let process_domain_wire = u16_le(block, 376);
    let process_domain_token = array(block, 380);
    let process_domain_kind = if operation.live() {
        let kind = ProcessDomainKind::from_wire(process_domain_wire)
            .ok_or(ReservationHeaderProblem::ProcessDomain)?;
        if !sidecar::valid_process_domain(kind, &process_domain_token)
            || (identity_kind == LocalIdentityKind::Windows
                && kind != ProcessDomainKind::HostGlobal)
        {
            return Err(ReservationHeaderProblem::ProcessDomain);
        }
        Some(kind)
    } else {
        if process_domain_wire != 0 || process_domain_token != [0; 32] {
            return Err(ReservationHeaderProblem::ProcessDomain);
        }
        None
    };

    let basename_encoding = u16_le(block, 412);
    let basename_len = u32_le(block, 416);
    if basename_encoding != identity_kind as u16
        || basename_len == 0
        || (basename_encoding == 2 && basename_len % 2 != 0)
    {
        return Err(ReservationHeaderProblem::Basename);
    }
    let creation_security_kind = u16_le(block, 460);
    if creation_security_kind != identity_kind as u16 {
        return Err(ReservationHeaderProblem::CreationSecurity);
    }
    let header_seq = u64_le(block, 496);
    if header_seq == 0 {
        return Err(ReservationHeaderProblem::Sequence);
    }

    Ok(ReservationHeader {
        state,
        database_id,
        attempted_txn_id,
        attempted_commit_nonce,
        reservation_id,
        identity_kind,
        reservation_identity,
        operation,
        output_identity_kind,
        record_flags,
        attempted_output_bytes,
        attempted_output_identity,
        attempted_output_sha512,
        previous_destination_identity,
        previous_destination_sha512,
        reader_capacity,
        prior_coordination_identity,
        prior_sidecar_id,
        prior_reader_capacity,
        process_domain_kind,
        process_domain_token,
        basename_encoding,
        basename_len,
        basename_commitment: array(block, 420),
        previous_destination_bytes,
        creation_security_kind,
        creation_security_commitment: array(block, 464),
        header_seq,
    })
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum ConversionHeader {
    Reservation(ReservationHeader),
    Sidecar(SidecarHeader),
}

impl ConversionHeader {
    const fn sequence(self) -> u64 {
        match self {
            Self::Reservation(header) => header.header_seq,
            Self::Sidecar(header) => header.header_seq,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum ConversionBlockProblem {
    UnknownMagic,
    Reservation(ReservationHeaderProblem),
    Sidecar(SidecarHeaderProblem),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum ConversionError {
    HeaderRegionTooShort,
    NoValidHeader {
        block0: ConversionBlockProblem,
        block1: ConversionBlockProblem,
    },
    EqualSequenceDisagreement,
    NonAdjacentSequence {
        older: u64,
        newer: u64,
    },
    InvalidTransition,
}

/// Select either magic while a reservation inode is being converted in place.
/// Phase-specific physical-size checks remain the resolver's responsibility.
pub(crate) fn select_conversion_header(
    bytes: &[u8],
    domain_policy: DomainSelectionPolicy,
) -> Result<ConversionHeader, ConversionError> {
    if bytes.len() < HEADER_REGION_SIZE {
        return Err(ConversionError::HeaderRegionTooShort);
    }
    let block0: &[u8; PAGE_SIZE] = bytes[..PAGE_SIZE].try_into().unwrap();
    let block1: &[u8; PAGE_SIZE] = bytes[PAGE_SIZE..HEADER_REGION_SIZE].try_into().unwrap();
    let first = decode_conversion_block(block0);
    let second = decode_conversion_block(block1);
    match (first, second) {
        (Err(block0), Err(block1)) => Err(ConversionError::NoValidHeader { block0, block1 }),
        (Ok(header), Err(_)) | (Err(_), Ok(header)) => Ok(header),
        (Ok(left), Ok(right)) => {
            if left.sequence() == right.sequence() {
                return if block0 == block1 {
                    Ok(left)
                } else {
                    Err(ConversionError::EqualSequenceDisagreement)
                };
            }
            let (older, newer) = if left.sequence() < right.sequence() {
                (left, right)
            } else {
                (right, left)
            };
            if older.sequence().checked_add(1) != Some(newer.sequence()) {
                return Err(ConversionError::NonAdjacentSequence {
                    older: older.sequence(),
                    newer: newer.sequence(),
                });
            }
            if conversion_transition_valid(older, newer, domain_policy) {
                Ok(newer)
            } else {
                Err(ConversionError::InvalidTransition)
            }
        }
    }
}

fn decode_conversion_block(
    block: &[u8; PAGE_SIZE],
) -> Result<ConversionHeader, ConversionBlockProblem> {
    if block[0..8] == RESERVATION_MAGIC {
        decode_header(block)
            .map(ConversionHeader::Reservation)
            .map_err(ConversionBlockProblem::Reservation)
    } else if block[0..8] == *b"IPR4RDRS" {
        sidecar::decode_header(block)
            .map(ConversionHeader::Sidecar)
            .map_err(ConversionBlockProblem::Sidecar)
    } else {
        Err(ConversionBlockProblem::UnknownMagic)
    }
}

fn conversion_transition_valid(
    older: ConversionHeader,
    newer: ConversionHeader,
    domain_policy: DomainSelectionPolicy,
) -> bool {
    match (older, newer) {
        (ConversionHeader::Reservation(older), ConversionHeader::Reservation(newer)) => {
            reservation_transition_valid(older, newer, domain_policy)
        }
        (ConversionHeader::Reservation(reservation), ConversionHeader::Sidecar(sidecar)) => {
            reservation_to_sidecar_valid(reservation, sidecar, domain_policy)
        }
        (ConversionHeader::Sidecar(older), ConversionHeader::Sidecar(newer)) => {
            sidecar_transition_valid(older, newer, domain_policy)
        }
        (ConversionHeader::Sidecar(_), ConversionHeader::Reservation(_)) => false,
    }
}

fn reservation_to_sidecar_valid(
    reservation: ReservationHeader,
    sidecar: SidecarHeader,
    domain_policy: DomainSelectionPolicy,
) -> bool {
    let Some(origin) = reservation.operation.sidecar_origin() else {
        return false;
    };
    reservation.state == ReservationState::Prepared
        && reservation.attempted_output_complete()
        && sidecar.state == SidecarState::Initializing
        && sidecar.identity_kind == reservation.identity_kind
        && sidecar.capacity == reservation.reader_capacity
        && sidecar.database_id == reservation.database_id
        && sidecar.main_identity == reservation.attempted_output_identity
        && sidecar.sidecar_identity == reservation.reservation_identity
        && sidecar.sidecar_id == reservation.reservation_id
        && sidecar.origin == origin
        && sidecar.attempted_txn_id == reservation.attempted_txn_id
        && sidecar.attempted_commit_nonce == reservation.attempted_commit_nonce
        && sidecar.attempted_main_bytes == reservation.attempted_output_bytes
        && sidecar.attempted_main_sha512 == reservation.attempted_output_sha512
        && sidecar.basename_encoding == reservation.basename_encoding
        && sidecar.basename_len == reservation.basename_len
        && sidecar.basename_commitment == reservation.basename_commitment
        && sidecar.creation_security_kind == reservation.creation_security_kind
        && sidecar.creation_security_commitment == reservation.creation_security_commitment
        && domain_transition_valid(
            reservation.operation,
            reservation.process_domain_kind,
            &reservation.process_domain_token,
            Some(sidecar.process_domain_kind),
            &sidecar.process_domain_token,
            domain_policy,
        )
}

fn sidecar_transition_valid(
    older: SidecarHeader,
    newer: SidecarHeader,
    domain_policy: DomainSelectionPolicy,
) -> bool {
    if !older.conversion_identity_eq(newer)
        || !sidecar_state_transition_valid(older.origin, older.state, newer.state)
    {
        return false;
    }
    let operation = match newer.origin {
        SidecarOrigin::CreateLive => ReservationOperation::CreateLive,
        SidecarOrigin::InitializeLive => ReservationOperation::InitializeLive,
        SidecarOrigin::ResetLiveCoordination => ReservationOperation::ResetLiveCoordination,
    };
    if older.process_domain_kind == newer.process_domain_kind
        && older.process_domain_token == newer.process_domain_token
    {
        return true;
    }
    older.state == SidecarState::Initializing
        && newer.state == SidecarState::Initializing
        && domain_transition_valid(
            operation,
            Some(older.process_domain_kind),
            &older.process_domain_token,
            Some(newer.process_domain_kind),
            &newer.process_domain_token,
            domain_policy,
        )
}

fn sidecar_state_transition_valid(
    origin: SidecarOrigin,
    older: SidecarState,
    newer: SidecarState,
) -> bool {
    sidecar::sidecar_state_transition_valid(origin, older, newer)
}

fn array<const N: usize>(bytes: &[u8], at: usize) -> [u8; N] {
    bytes[at..at + N].try_into().unwrap()
}

fn put_u16(bytes: &mut [u8], at: usize, value: u16) {
    bytes[at..at + 2].copy_from_slice(&value.to_le_bytes());
}

fn put_u32(bytes: &mut [u8], at: usize, value: u32) {
    bytes[at..at + 4].copy_from_slice(&value.to_le_bytes());
}

fn put_u64(bytes: &mut [u8], at: usize, value: u64) {
    bytes[at..at + 8].copy_from_slice(&value.to_le_bytes());
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{vec, vec::Vec};

    fn posix_identity(device: u64, inode: u64) -> [u8; 32] {
        let mut identity = [0u8; 32];
        identity[..8].copy_from_slice(&device.to_le_bytes());
        identity[8..16].copy_from_slice(&inode.to_le_bytes());
        identity
    }

    fn complete_header(operation: ReservationOperation, sequence: u64) -> ReservationHeader {
        let live = operation.live();
        let replace = operation == ReservationOperation::ReplaceExisting;
        let reset = operation == ReservationOperation::ResetLiveCoordination;
        ReservationHeader {
            state: ReservationState::Prepared,
            database_id: [1; 16],
            attempted_txn_id: 7,
            attempted_commit_nonce: [2; 16],
            reservation_id: [3; 16],
            identity_kind: LocalIdentityKind::Posix,
            reservation_identity: posix_identity(8, 100),
            operation,
            output_identity_kind: Some(LocalIdentityKind::Posix),
            record_flags: if replace {
                FLAG_PREVIOUS_DESTINATION
            } else if reset {
                FLAG_PRIOR_SIDECAR
            } else {
                0
            },
            attempted_output_bytes: 8192,
            attempted_output_identity: posix_identity(8, 101),
            attempted_output_sha512: [4; 64],
            previous_destination_identity: if replace {
                posix_identity(8, 102)
            } else {
                [0; 32]
            },
            previous_destination_sha512: if replace { [5; 64] } else { [0; 64] },
            reader_capacity: if live { 3 } else { 0 },
            prior_coordination_identity: if reset {
                posix_identity(8, 103)
            } else {
                [0; 32]
            },
            prior_sidecar_id: if reset { [6; 16] } else { [0; 16] },
            prior_reader_capacity: if reset { 2 } else { 0 },
            process_domain_kind: if live {
                Some(ProcessDomainKind::LinuxPidNamespace)
            } else {
                None
            },
            process_domain_token: if live { posix_identity(9, 10) } else { [0; 32] },
            basename_encoding: 1,
            basename_len: 7,
            basename_commitment: [7; 32],
            previous_destination_bytes: if replace { 8192 } else { 0 },
            creation_security_kind: 1,
            creation_security_commitment: [8; 32],
            header_seq: sequence,
        }
    }

    fn incomplete_create(sequence: u64) -> ReservationHeader {
        let mut header = complete_header(ReservationOperation::CreateLive, sequence);
        header.database_id = [0; 16];
        header.attempted_txn_id = 0;
        header.attempted_commit_nonce = [0; 16];
        header.output_identity_kind = None;
        header.attempted_output_bytes = 0;
        header.attempted_output_identity = [0; 32];
        header.attempted_output_sha512 = [0; 64];
        header
    }

    fn block(header: ReservationHeader) -> [u8; PAGE_SIZE] {
        let mut block = [0u8; PAGE_SIZE];
        header.encode_into(&mut block);
        block
    }

    fn image(left: ReservationHeader, right: ReservationHeader) -> Vec<u8> {
        let mut bytes = vec![0u8; HEADER_REGION_SIZE];
        left.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
        right.encode_into((&mut bytes[PAGE_SIZE..]).try_into().unwrap());
        bytes
    }

    fn sidecar_from(reservation: ReservationHeader, sequence: u64) -> SidecarHeader {
        SidecarHeader {
            identity_kind: reservation.identity_kind,
            capacity: reservation.reader_capacity,
            state: SidecarState::Initializing,
            database_id: reservation.database_id,
            main_identity: reservation.attempted_output_identity,
            sidecar_identity: reservation.reservation_identity,
            sidecar_id: reservation.reservation_id,
            origin: reservation.operation.sidecar_origin().unwrap(),
            attempted_txn_id: reservation.attempted_txn_id,
            attempted_commit_nonce: reservation.attempted_commit_nonce,
            attempted_main_bytes: reservation.attempted_output_bytes,
            attempted_main_sha512: reservation.attempted_output_sha512,
            process_domain_kind: reservation.process_domain_kind.unwrap(),
            process_domain_token: reservation.process_domain_token,
            basename_encoding: reservation.basename_encoding,
            basename_len: reservation.basename_len,
            basename_commitment: reservation.basename_commitment,
            creation_security_kind: reservation.creation_security_kind,
            creation_security_commitment: reservation.creation_security_commitment,
            header_seq: sequence,
        }
    }

    #[test]
    fn exact_offsets_crc_and_dual_reservation_selection() {
        let left = complete_header(ReservationOperation::FailIfExists, 1);
        let mut right = left;
        right.header_seq = 2;
        let bytes = image(left, right);
        assert_eq!(&bytes[0..8], b"IPR4RSV1");
        assert_eq!(u16_le(&bytes, 8), 512);
        assert_eq!(u16_le(&bytes, 10), 1);
        assert_eq!(u32_le(&bytes, 12), 1);
        assert_eq!(&bytes[16..32], &[1; 16]);
        assert_eq!(u64_le(&bytes, 32), 7);
        assert_eq!(&bytes[40..56], &[2; 16]);
        assert_eq!(&bytes[56..72], &[3; 16]);
        assert_eq!(u16_le(&bytes, 72), 1);
        assert!(bytes[74..80].iter().all(|&byte| byte == 0));
        assert_eq!(&bytes[80..112], &left.reservation_identity);
        assert_eq!(u16_le(&bytes, 112), 1);
        assert_eq!(u16_le(&bytes, 114), 1);
        assert_eq!(u32_le(&bytes, 116), 0);
        assert_eq!(u64_le(&bytes, 120), 8192);
        assert_eq!(&bytes[128..160], &left.attempted_output_identity);
        assert_eq!(&bytes[160..224], &[4; 64]);
        assert!(bytes[224..320].iter().all(|&byte| byte == 0));
        assert_eq!(u32_le(&bytes, 320), 0);
        assert!(bytes[324..412].iter().all(|&byte| byte == 0));
        assert_eq!(u16_le(&bytes, 412), 1);
        assert_eq!(u32_le(&bytes, 416), 7);
        assert_eq!(&bytes[420..452], &[7; 32]);
        assert_eq!(u64_le(&bytes, 452), 0);
        assert_eq!(u16_le(&bytes, 460), 1);
        assert_eq!(&bytes[464..496], &[8; 32]);
        assert_eq!(u64_le(&bytes, 496), 1);
        assert_eq!(u32_le(&bytes, 504), 0);
        assert!(bytes[512..PAGE_SIZE].iter().all(|&byte| byte == 0));
        assert_eq!(
            select_reservation_header(&bytes, DomainSelectionPolicy::RequireSame),
            Ok(right)
        );

        let mut corrupt = bytes;
        corrupt[PAGE_SIZE + 508] ^= 1;
        assert_eq!(
            select_reservation_header(&corrupt, DomainSelectionPolicy::RequireSame),
            Ok(left)
        );
    }

    #[test]
    fn operation_state_and_optional_field_rules_fail_closed() {
        for operation in [
            ReservationOperation::FailIfExists,
            ReservationOperation::ReplaceExisting,
            ReservationOperation::CreateLive,
            ReservationOperation::InitializeLive,
            ReservationOperation::ResetLiveCoordination,
        ] {
            assert!(decode_header(&block(complete_header(operation, 1))).is_ok());
        }
        assert!(decode_header(&block(incomplete_create(1))).is_ok());

        let mut invalid = complete_header(ReservationOperation::CreateLive, 1);
        invalid.state = ReservationState::MainNamespaceAttempted;
        assert_eq!(
            decode_header(&block(invalid)),
            Err(ReservationHeaderProblem::State)
        );

        invalid = complete_header(ReservationOperation::ReplaceExisting, 1);
        invalid.record_flags = 0;
        assert_eq!(
            decode_header(&block(invalid)),
            Err(ReservationHeaderProblem::PreviousDestination)
        );

        invalid = complete_header(ReservationOperation::ResetLiveCoordination, 1);
        invalid.prior_sidecar_id = [0; 16];
        assert_eq!(
            decode_header(&block(invalid)),
            Err(ReservationHeaderProblem::PriorCoordination)
        );

        invalid = complete_header(ReservationOperation::InitializeLive, 1);
        invalid.process_domain_token[31] = 1;
        assert_eq!(
            decode_header(&block(invalid)),
            Err(ReservationHeaderProblem::ProcessDomain)
        );
    }

    #[test]
    fn create_can_advance_from_empty_to_complete_but_static_fields_cannot_change() {
        let older = incomplete_create(1);
        let newer = complete_header(ReservationOperation::CreateLive, 2);
        assert_eq!(
            select_reservation_header(&image(older, newer), DomainSelectionPolicy::RequireSame),
            Ok(newer)
        );

        let mut changed_name = newer;
        changed_name.header_seq = 3;
        changed_name.basename_commitment[0] ^= 1;
        assert_eq!(
            select_reservation_header(
                &image(newer, changed_name),
                DomainSelectionPolicy::RequireSame
            ),
            Err(ReservationError::ImmutableAttemptMismatch)
        );

        let mut regressed = older;
        regressed.header_seq = 3;
        assert_eq!(
            select_reservation_header(&image(newer, regressed), DomainSelectionPolicy::RequireSame),
            Err(ReservationError::InvalidTransition)
        );
    }

    #[test]
    fn mixed_conversion_requires_newer_matching_initializing_sidecar() {
        for operation in [
            ReservationOperation::CreateLive,
            ReservationOperation::InitializeLive,
            ReservationOperation::ResetLiveCoordination,
        ] {
            let reservation = complete_header(operation, 4);
            let sidecar = sidecar_from(reservation, 5);
            let mut bytes = vec![0u8; HEADER_REGION_SIZE];
            reservation.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
            sidecar.encode_into((&mut bytes[PAGE_SIZE..]).try_into().unwrap());
            assert_eq!(
                select_conversion_header(&bytes, DomainSelectionPolicy::RequireSame),
                Ok(ConversionHeader::Sidecar(sidecar))
            );
        }

        let reservation = complete_header(ReservationOperation::CreateLive, 4);
        let sidecar = sidecar_from(reservation, 5);
        let mut bytes = vec![0u8; HEADER_REGION_SIZE];
        reservation.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
        sidecar.encode_into((&mut bytes[PAGE_SIZE..]).try_into().unwrap());
        assert_eq!(
            select_conversion_header(&bytes, DomainSelectionPolicy::RequireSame),
            Ok(ConversionHeader::Sidecar(sidecar))
        );

        let mut gap = sidecar;
        gap.header_seq = 6;
        gap.encode_into((&mut bytes[PAGE_SIZE..]).try_into().unwrap());
        assert_eq!(
            select_conversion_header(&bytes, DomainSelectionPolicy::RequireSame),
            Err(ConversionError::NonAdjacentSequence { older: 4, newer: 6 })
        );
        gap.header_seq = u64::MAX;
        gap.encode_into((&mut bytes[PAGE_SIZE..]).try_into().unwrap());
        assert_eq!(
            select_conversion_header(&bytes, DomainSelectionPolicy::RequireSame),
            Err(ConversionError::NonAdjacentSequence {
                older: 4,
                newer: u64::MAX,
            })
        );

        let mut older_sidecar = sidecar;
        older_sidecar.header_seq = 3;
        older_sidecar.encode_into((&mut bytes[PAGE_SIZE..]).try_into().unwrap());
        assert_eq!(
            select_conversion_header(&bytes, DomainSelectionPolicy::RequireSame),
            Err(ConversionError::InvalidTransition)
        );

        let mut mismatch = sidecar;
        mismatch.basename_commitment[0] ^= 1;
        mismatch.encode_into((&mut bytes[PAGE_SIZE..]).try_into().unwrap());
        assert_eq!(
            select_conversion_header(&bytes, DomainSelectionPolicy::RequireSame),
            Err(ConversionError::InvalidTransition)
        );
    }

    #[test]
    fn resolver_policy_controls_old_to_current_domain_transition() {
        let older = complete_header(ReservationOperation::InitializeLive, 1);
        let mut newer = older;
        newer.header_seq = 2;
        newer.process_domain_token = posix_identity(9, 11);
        let current_policy = DomainSelectionPolicy::ResolverMayReplace {
            current_kind: ProcessDomainKind::LinuxPidNamespace,
            current_token: newer.process_domain_token,
        };
        assert_eq!(
            select_reservation_header(&image(older, newer), DomainSelectionPolicy::RequireSame),
            Err(ReservationError::InvalidTransition)
        );
        assert_eq!(
            select_reservation_header(&image(older, newer), current_policy),
            Ok(newer)
        );

        let mixed_sidecar = sidecar_from(newer, 2);
        let mut bytes = vec![0u8; HEADER_REGION_SIZE];
        older.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
        mixed_sidecar.encode_into((&mut bytes[PAGE_SIZE..]).try_into().unwrap());
        assert_eq!(
            select_conversion_header(&bytes, DomainSelectionPolicy::RequireSame),
            Err(ConversionError::InvalidTransition)
        );
        assert_eq!(
            select_conversion_header(&bytes, current_policy),
            Ok(ConversionHeader::Sidecar(mixed_sidecar))
        );

        let mut old_sidecar = sidecar_from(older, 3);
        let mut new_sidecar = old_sidecar;
        new_sidecar.header_seq = 4;
        new_sidecar.process_domain_token = newer.process_domain_token;
        old_sidecar.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
        new_sidecar.encode_into((&mut bytes[PAGE_SIZE..]).try_into().unwrap());
        assert_eq!(
            select_conversion_header(&bytes, DomainSelectionPolicy::RequireSame),
            Err(ConversionError::InvalidTransition)
        );
        assert_eq!(
            select_conversion_header(&bytes, current_policy),
            Ok(ConversionHeader::Sidecar(new_sidecar))
        );

        old_sidecar.state = SidecarState::Ready;
        old_sidecar.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
        assert_eq!(
            select_conversion_header(&bytes, current_policy),
            Err(ConversionError::InvalidTransition)
        );
    }

    #[test]
    fn equal_sequence_disagreement_and_reserved_bytes_are_rejected() {
        let left = complete_header(ReservationOperation::FailIfExists, 1);
        let mut right = left;
        right.state = ReservationState::MainNamespaceAttempted;
        assert_eq!(
            select_reservation_header(&image(left, right), DomainSelectionPolicy::RequireSame),
            Err(ReservationError::EqualSequenceDisagreement)
        );

        for index in (74..80)
            .chain(378..380)
            .chain(414..416)
            .chain(462..464)
            .chain(504..508)
            .chain(512..PAGE_SIZE)
        {
            let mut raw = block(left);
            raw[index] = 1;
            let crc = crc32c::crc32c_with_zeroed(&raw, HEADER_CRC_OFFSET, 4).unwrap();
            put_u32(&mut raw, HEADER_CRC_OFFSET, crc);
            assert_eq!(
                decode_header(&raw),
                Err(ReservationHeaderProblem::Reserved),
                "reserved byte {index}"
            );
        }

        let mut zero_sequence = left;
        zero_sequence.header_seq = 0;
        assert_eq!(
            decode_header(&block(zero_sequence)),
            Err(ReservationHeaderProblem::Sequence)
        );
        assert_eq!(
            select_reservation_header(&image(left, left), DomainSelectionPolicy::RequireSame),
            Ok(left)
        );

        let mut gap = left;
        gap.header_seq = 3;
        assert_eq!(
            select_reservation_header(&image(left, gap), DomainSelectionPolicy::RequireSame),
            Err(ReservationError::NonAdjacentSequence { older: 1, newer: 3 })
        );
        let mut maximum = left;
        maximum.header_seq = u64::MAX;
        assert_eq!(
            select_reservation_header(&image(left, maximum), DomainSelectionPolicy::RequireSame),
            Err(ReservationError::NonAdjacentSequence {
                older: 1,
                newer: u64::MAX,
            })
        );

        let mut short = image(left, left);
        short.pop();
        assert_eq!(
            select_reservation_header(&short, DomainSelectionPolicy::RequireSame),
            Err(ReservationError::WrongFileSize { actual: 8191 })
        );
    }

    #[test]
    fn sidecar_conversion_phases_are_monotonic_for_each_origin() {
        let create = complete_header(ReservationOperation::CreateLive, 1);
        let initializing = sidecar_from(create, 2);
        let mut attempted = initializing;
        attempted.header_seq = 3;
        attempted.state = SidecarState::MainNamespaceAttempted;
        let mut ready = attempted;
        ready.header_seq = 4;
        ready.state = SidecarState::Ready;

        let mut bytes = vec![0u8; HEADER_REGION_SIZE];
        initializing.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
        attempted.encode_into((&mut bytes[PAGE_SIZE..]).try_into().unwrap());
        assert_eq!(
            select_conversion_header(&bytes, DomainSelectionPolicy::RequireSame),
            Ok(ConversionHeader::Sidecar(attempted))
        );
        attempted.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
        ready.encode_into((&mut bytes[PAGE_SIZE..]).try_into().unwrap());
        assert_eq!(
            select_conversion_header(&bytes, DomainSelectionPolicy::RequireSame),
            Ok(ConversionHeader::Sidecar(ready))
        );

        let initialize = complete_header(ReservationOperation::InitializeLive, 1);
        let initializing = sidecar_from(initialize, 2);
        let mut ready = initializing;
        ready.header_seq = 3;
        ready.state = SidecarState::Ready;
        initializing.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
        ready.encode_into((&mut bytes[PAGE_SIZE..]).try_into().unwrap());
        assert_eq!(
            select_conversion_header(&bytes, DomainSelectionPolicy::RequireSame),
            Ok(ConversionHeader::Sidecar(ready))
        );

        let mut invalid = ready;
        invalid.header_seq = 4;
        invalid.state = SidecarState::Initializing;
        ready.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
        invalid.encode_into((&mut bytes[PAGE_SIZE..]).try_into().unwrap());
        assert_eq!(
            select_conversion_header(&bytes, DomainSelectionPolicy::RequireSame),
            Err(ConversionError::InvalidTransition)
        );
    }
}
