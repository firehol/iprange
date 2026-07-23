//! Exact external live-reader sidecar header and slot codecs.

use crate::contract::{u16_le, u32_le, u64_le, PAGE_SIZE};
use crate::crc32c;

const SIDECAR_MAGIC: [u8; 8] = *b"IPR4RDRS";
const SIDECAR_VERSION: u16 = 1;
const HEADER_RECORD_SIZE: u16 = 512;
const HEADER_REGION_SIZE: usize = 2 * PAGE_SIZE;
pub(crate) const SLOT_SIZE: u16 = 64;
const HEADER_CRC_OFFSET: usize = 508;
const SLOT_CRC_OFFSET: usize = 60;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u16)]
pub(crate) enum LocalIdentityKind {
    Posix = 1,
    Windows = 2,
}

impl LocalIdentityKind {
    pub(crate) const fn from_wire(value: u16) -> Option<Self> {
        match value {
            1 => Some(Self::Posix),
            2 => Some(Self::Windows),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u32)]
pub(crate) enum SidecarState {
    Ready = 1,
    Initializing = 2,
    MainNamespaceAttempted = 3,
}

impl SidecarState {
    const fn from_wire(value: u32) -> Option<Self> {
        match value {
            1 => Some(Self::Ready),
            2 => Some(Self::Initializing),
            3 => Some(Self::MainNamespaceAttempted),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u16)]
pub(crate) enum SidecarOrigin {
    CreateLive = 1,
    InitializeLive = 2,
    ResetLiveCoordination = 3,
}

impl SidecarOrigin {
    const fn from_wire(value: u16) -> Option<Self> {
        match value {
            1 => Some(Self::CreateLive),
            2 => Some(Self::InitializeLive),
            3 => Some(Self::ResetLiveCoordination),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[repr(u16)]
pub(crate) enum ProcessDomainKind {
    LinuxPidNamespace = 1,
    FreeBsdJail = 2,
    HostGlobal = 3,
}

impl ProcessDomainKind {
    pub(crate) const fn from_wire(value: u16) -> Option<Self> {
        match value {
            1 => Some(Self::LinuxPidNamespace),
            2 => Some(Self::FreeBsdJail),
            3 => Some(Self::HostGlobal),
            _ => None,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct SidecarHeader {
    pub(crate) identity_kind: LocalIdentityKind,
    pub(crate) capacity: u32,
    pub(crate) state: SidecarState,
    pub(crate) database_id: [u8; 16],
    pub(crate) main_identity: [u8; 32],
    pub(crate) sidecar_identity: [u8; 32],
    pub(crate) sidecar_id: [u8; 16],
    pub(crate) origin: SidecarOrigin,
    pub(crate) attempted_txn_id: u64,
    pub(crate) attempted_commit_nonce: [u8; 16],
    pub(crate) attempted_main_bytes: u64,
    pub(crate) attempted_main_sha512: [u8; 64],
    pub(crate) process_domain_kind: ProcessDomainKind,
    pub(crate) process_domain_token: [u8; 32],
    pub(crate) basename_encoding: u16,
    pub(crate) basename_len: u32,
    pub(crate) basename_commitment: [u8; 32],
    pub(crate) creation_security_kind: u16,
    pub(crate) creation_security_commitment: [u8; 32],
    pub(crate) header_seq: u64,
}

impl SidecarHeader {
    pub(crate) fn exact_file_size(self) -> Option<u64> {
        u64::from(self.capacity)
            .checked_add(1)?
            .checked_mul(u64::from(SLOT_SIZE))?
            .checked_add(HEADER_REGION_SIZE as u64)
    }

    pub(crate) fn encode_into(self, block: &mut [u8; PAGE_SIZE]) {
        block.fill(0);
        block[0..8].copy_from_slice(&SIDECAR_MAGIC);
        put_u16(block, 8, SIDECAR_VERSION);
        put_u16(block, 10, HEADER_RECORD_SIZE);
        put_u16(block, 12, SLOT_SIZE);
        put_u16(block, 14, self.identity_kind as u16);
        put_u32(block, 16, self.capacity);
        put_u32(block, 20, self.state as u32);
        block[24..40].copy_from_slice(&self.database_id);
        block[40..72].copy_from_slice(&self.main_identity);
        block[72..104].copy_from_slice(&self.sidecar_identity);
        block[104..120].copy_from_slice(&self.sidecar_id);
        put_u16(block, 120, self.origin as u16);
        put_u16(block, 122, 1);
        put_u64(block, 124, self.attempted_txn_id);
        block[132..148].copy_from_slice(&self.attempted_commit_nonce);
        put_u64(block, 148, self.attempted_main_bytes);
        block[156..220].copy_from_slice(&self.attempted_main_sha512);
        put_u16(block, 220, self.process_domain_kind as u16);
        block[224..256].copy_from_slice(&self.process_domain_token);
        put_u16(block, 256, self.basename_encoding);
        put_u32(block, 260, self.basename_len);
        block[264..296].copy_from_slice(&self.basename_commitment);
        put_u16(block, 296, self.creation_security_kind);
        block[300..332].copy_from_slice(&self.creation_security_commitment);
        put_u64(block, 496, self.header_seq);
        let crc = crc32c::crc32c_with_zeroed(block, HEADER_CRC_OFFSET, 4).unwrap();
        put_u32(block, HEADER_CRC_OFFSET, crc);
    }

    fn immutable_identity_eq(self, other: Self) -> bool {
        self.conversion_identity_eq(other)
            && self.process_domain_kind == other.process_domain_kind
            && self.process_domain_token == other.process_domain_token
    }

    pub(crate) fn conversion_identity_eq(self, other: Self) -> bool {
        self.identity_kind == other.identity_kind
            && self.capacity == other.capacity
            && self.database_id == other.database_id
            && self.main_identity == other.main_identity
            && self.sidecar_identity == other.sidecar_identity
            && self.sidecar_id == other.sidecar_id
            && self.origin == other.origin
            && self.attempted_txn_id == other.attempted_txn_id
            && self.attempted_commit_nonce == other.attempted_commit_nonce
            && self.attempted_main_bytes == other.attempted_main_bytes
            && self.attempted_main_sha512 == other.attempted_main_sha512
            && self.basename_encoding == other.basename_encoding
            && self.basename_len == other.basename_len
            && self.basename_commitment == other.basename_commitment
            && self.creation_security_kind == other.creation_security_kind
            && self.creation_security_commitment == other.creation_security_commitment
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum SidecarHeaderProblem {
    Magic,
    FixedValue,
    Reserved,
    Checksum,
    IdentityKind,
    IdentityEncoding,
    Capacity,
    State,
    DatabaseId,
    IdentityCollision,
    SidecarId,
    Origin,
    Attempt,
    ProcessDomain,
    Basename,
    CreationSecurity,
    Sequence,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum SidecarError {
    HeaderRegionTooShort,
    NoValidHeader {
        block0: SidecarHeaderProblem,
        block1: SidecarHeaderProblem,
    },
    EqualSequenceDisagreement,
    NonAdjacentSequence {
        older: u64,
        newer: u64,
    },
    ImmutableIdentityMismatch,
    InvalidStateTransition,
    NotReady(SidecarState),
    WrongFileSize {
        expected: u64,
        actual: u64,
    },
    SizeOverflow,
}

/// Select only a completed sidecar's two `IPR4RDRS` blocks.
///
/// Reservation-to-sidecar conversion uses a separate mixed-magic selector.
pub(crate) fn select_sidecar_header(bytes: &[u8]) -> Result<SidecarHeader, SidecarError> {
    if bytes.len() < HEADER_REGION_SIZE {
        return Err(SidecarError::HeaderRegionTooShort);
    }
    let block0: &[u8; PAGE_SIZE] = bytes[..PAGE_SIZE].try_into().unwrap();
    let block1: &[u8; PAGE_SIZE] = bytes[PAGE_SIZE..HEADER_REGION_SIZE].try_into().unwrap();
    let first = decode_header(block0);
    let second = decode_header(block1);
    match (first, second) {
        (Err(block0), Err(block1)) => Err(SidecarError::NoValidHeader { block0, block1 }),
        (Ok(header), Err(_)) | (Err(_), Ok(header)) => Ok(header),
        (Ok(left), Ok(right)) => {
            if left.header_seq == right.header_seq {
                return if block0 == block1 {
                    Ok(left)
                } else {
                    Err(SidecarError::EqualSequenceDisagreement)
                };
            }
            let (older, newer) = if left.header_seq < right.header_seq {
                (left, right)
            } else {
                (right, left)
            };
            if older.header_seq.checked_add(1) != Some(newer.header_seq) {
                return Err(SidecarError::NonAdjacentSequence {
                    older: older.header_seq,
                    newer: newer.header_seq,
                });
            }
            if !left.immutable_identity_eq(right) {
                return Err(SidecarError::ImmutableIdentityMismatch);
            }
            if !sidecar_state_transition_valid(older.origin, older.state, newer.state) {
                return Err(SidecarError::InvalidStateTransition);
            }
            Ok(newer)
        }
    }
}

pub(crate) fn sidecar_state_transition_valid(
    origin: SidecarOrigin,
    older: SidecarState,
    newer: SidecarState,
) -> bool {
    if older == newer {
        return true;
    }
    match origin {
        SidecarOrigin::CreateLive => matches!(
            (older, newer),
            (
                SidecarState::Initializing,
                SidecarState::MainNamespaceAttempted
            ) | (SidecarState::MainNamespaceAttempted, SidecarState::Ready)
        ),
        SidecarOrigin::InitializeLive | SidecarOrigin::ResetLiveCoordination => matches!(
            (older, newer),
            (SidecarState::Initializing, SidecarState::Ready)
        ),
    }
}

fn decode_ready_image(bytes: &[u8]) -> Result<SidecarHeader, SidecarError> {
    let header = select_sidecar_header(bytes)?;
    if header.state != SidecarState::Ready {
        return Err(SidecarError::NotReady(header.state));
    }
    let expected = header.exact_file_size().ok_or(SidecarError::SizeOverflow)?;
    let actual = u64::try_from(bytes.len()).map_err(|_| SidecarError::SizeOverflow)?;
    if actual != expected {
        return Err(SidecarError::WrongFileSize { expected, actual });
    }
    Ok(header)
}

pub(crate) fn decode_header(
    block: &[u8; PAGE_SIZE],
) -> Result<SidecarHeader, SidecarHeaderProblem> {
    if block[0..8] != SIDECAR_MAGIC {
        return Err(SidecarHeaderProblem::Magic);
    }
    if u16_le(block, 8) != SIDECAR_VERSION
        || u16_le(block, 10) != HEADER_RECORD_SIZE
        || u16_le(block, 12) != SLOT_SIZE
        || u16_le(block, 122) != 1
    {
        return Err(SidecarHeaderProblem::FixedValue);
    }
    if u16_le(block, 222) != 0
        || u16_le(block, 258) != 0
        || u16_le(block, 298) != 0
        || block[332..496].iter().any(|&byte| byte != 0)
        || u32_le(block, 504) != 0
        || block[512..].iter().any(|&byte| byte != 0)
    {
        return Err(SidecarHeaderProblem::Reserved);
    }
    let stored_crc = u32_le(block, HEADER_CRC_OFFSET);
    if crc32c::crc32c_with_zeroed(block, HEADER_CRC_OFFSET, 4) != Some(stored_crc) {
        return Err(SidecarHeaderProblem::Checksum);
    }

    let identity_kind = LocalIdentityKind::from_wire(u16_le(block, 14))
        .ok_or(SidecarHeaderProblem::IdentityKind)?;
    let capacity = u32_le(block, 16);
    if capacity == 0 {
        return Err(SidecarHeaderProblem::Capacity);
    }
    let state = SidecarState::from_wire(u32_le(block, 20)).ok_or(SidecarHeaderProblem::State)?;
    let database_id = array(block, 24);
    if database_id == [0; 16] {
        return Err(SidecarHeaderProblem::DatabaseId);
    }
    let main_identity = array(block, 40);
    let sidecar_identity = array(block, 72);
    if !valid_local_identity(identity_kind, &main_identity)
        || !valid_local_identity(identity_kind, &sidecar_identity)
    {
        return Err(SidecarHeaderProblem::IdentityEncoding);
    }
    if main_identity == sidecar_identity {
        return Err(SidecarHeaderProblem::IdentityCollision);
    }
    let sidecar_id = array(block, 104);
    if sidecar_id == [0; 16] {
        return Err(SidecarHeaderProblem::SidecarId);
    }
    let origin =
        SidecarOrigin::from_wire(u16_le(block, 120)).ok_or(SidecarHeaderProblem::Origin)?;
    if state == SidecarState::MainNamespaceAttempted && origin != SidecarOrigin::CreateLive {
        return Err(SidecarHeaderProblem::State);
    }
    let attempted_txn_id = u64_le(block, 124);
    let attempted_commit_nonce = array(block, 132);
    let attempted_main_bytes = u64_le(block, 148);
    if attempted_txn_id == 0
        || attempted_commit_nonce == [0; 16]
        || attempted_main_bytes < (2 * PAGE_SIZE) as u64
        || attempted_main_bytes % PAGE_SIZE as u64 != 0
    {
        return Err(SidecarHeaderProblem::Attempt);
    }
    let process_domain_kind = ProcessDomainKind::from_wire(u16_le(block, 220))
        .ok_or(SidecarHeaderProblem::ProcessDomain)?;
    let process_domain_token = array(block, 224);
    if !valid_process_domain(process_domain_kind, &process_domain_token)
        || (identity_kind == LocalIdentityKind::Windows
            && process_domain_kind != ProcessDomainKind::HostGlobal)
    {
        return Err(SidecarHeaderProblem::ProcessDomain);
    }
    let basename_encoding = u16_le(block, 256);
    let basename_len = u32_le(block, 260);
    if basename_encoding != identity_kind as u16
        || basename_len == 0
        || (basename_encoding == 2 && basename_len % 2 != 0)
    {
        return Err(SidecarHeaderProblem::Basename);
    }
    let creation_security_kind = u16_le(block, 296);
    if creation_security_kind != identity_kind as u16 {
        return Err(SidecarHeaderProblem::CreationSecurity);
    }
    let header_seq = u64_le(block, 496);
    if header_seq == 0 {
        return Err(SidecarHeaderProblem::Sequence);
    }

    Ok(SidecarHeader {
        identity_kind,
        capacity,
        state,
        database_id,
        main_identity,
        sidecar_identity,
        sidecar_id,
        origin,
        attempted_txn_id,
        attempted_commit_nonce,
        attempted_main_bytes,
        attempted_main_sha512: array(block, 156),
        process_domain_kind,
        process_domain_token,
        basename_encoding,
        basename_len,
        basename_commitment: array(block, 264),
        creation_security_kind,
        creation_security_commitment: array(block, 300),
        header_seq,
    })
}

pub(crate) fn valid_local_identity(kind: LocalIdentityKind, identity: &[u8; 32]) -> bool {
    match kind {
        LocalIdentityKind::Posix => identity[16..].iter().all(|&byte| byte == 0),
        LocalIdentityKind::Windows => identity[24..].iter().all(|&byte| byte == 0),
    }
}

pub(crate) fn valid_process_domain(kind: ProcessDomainKind, token: &[u8; 32]) -> bool {
    match kind {
        ProcessDomainKind::LinuxPidNamespace => token[16..].iter().all(|&byte| byte == 0),
        ProcessDomainKind::FreeBsdJail => token[4..].iter().all(|&byte| byte == 0),
        ProcessDomainKind::HostGlobal => token.iter().all(|&byte| byte == 0),
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum SlotRole {
    Writer,
    Reader,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct ActiveSlot {
    pub(crate) txn_id: u64,
    pub(crate) process_id: u64,
    pub(crate) process_start: u64,
    pub(crate) task_id: u64,
    pub(crate) nonce: [u8; 16],
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum StableSlot {
    Free,
    Active(ActiveSlot),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum SlotProblem {
    FreeNonzero,
    Transition,
    State(u32),
    Reserved,
    WriterTransactionZero,
    ProcessIdZero,
    ProcessIdUnrepresentable,
    TaskIdUnrepresentable,
    NonceZero,
    Checksum,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct SlotHostLimits {
    pub(crate) process_id_max: u64,
    pub(crate) task_id_max: u64,
}

pub(crate) fn decode_stable_slot(
    bytes: &[u8; SLOT_SIZE as usize],
    role: SlotRole,
    host: SlotHostLimits,
) -> Result<StableSlot, SlotProblem> {
    let state = u32_le(bytes, 0);
    if state == 0 {
        return if bytes.iter().all(|&byte| byte == 0) {
            Ok(StableSlot::Free)
        } else {
            Err(SlotProblem::FreeNonzero)
        };
    }
    if state == 2 {
        return Err(SlotProblem::Transition);
    }
    if state != 1 {
        return Err(SlotProblem::State(state));
    }
    if u32_le(bytes, 4) != 0 || u32_le(bytes, 56) != 0 {
        return Err(SlotProblem::Reserved);
    }
    let txn_id = u64_le(bytes, 8);
    if role == SlotRole::Writer && txn_id == 0 {
        return Err(SlotProblem::WriterTransactionZero);
    }
    let process_id = u64_le(bytes, 16);
    if process_id == 0 {
        return Err(SlotProblem::ProcessIdZero);
    }
    if process_id > host.process_id_max {
        return Err(SlotProblem::ProcessIdUnrepresentable);
    }
    let task_id = u64_le(bytes, 32);
    if task_id > host.task_id_max {
        return Err(SlotProblem::TaskIdUnrepresentable);
    }
    let nonce = array(bytes, 40);
    if nonce == [0; 16] {
        return Err(SlotProblem::NonceZero);
    }
    let stored_crc = u32_le(bytes, SLOT_CRC_OFFSET);
    if crc32c::crc32c_with_zeroed(bytes, SLOT_CRC_OFFSET, 4) != Some(stored_crc) {
        return Err(SlotProblem::Checksum);
    }
    Ok(StableSlot::Active(ActiveSlot {
        txn_id,
        process_id,
        process_start: u64_le(bytes, 24),
        task_id,
        nonce,
    }))
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct ReadySidecarExpectations {
    pub(crate) database_id: [u8; 16],
    pub(crate) main_identity: [u8; 32],
    pub(crate) sidecar_identity: [u8; 32],
    pub(crate) process_domain_kind: ProcessDomainKind,
    pub(crate) process_domain_token: [u8; 32],
    pub(crate) basename_encoding: u16,
    pub(crate) basename_len: u32,
    pub(crate) basename_commitment: [u8; 32],
    pub(crate) host_limits: SlotHostLimits,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct ReadySidecarInspection {
    pub(crate) header: SidecarHeader,
    pub(crate) writer: Option<ActiveSlot>,
    pub(crate) active_readers: u32,
    pub(crate) registering_readers: u32,
    pub(crate) oldest_reader_txn: Option<u64>,
    pub(crate) lowest_free_reader_slot: Option<u32>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum ReadySidecarError {
    Sidecar(SidecarError),
    DatabaseIdMismatch,
    MainIdentityMismatch,
    SidecarIdentityMismatch,
    ProcessDomainMismatch,
    BasenameMismatch,
    HeaderChanged,
    SlotIndexOutOfRange,
    SlotOffsetOverflow,
    Slot { index: u32, problem: SlotProblem },
    WriterTransactionMismatch { expected: u64, actual: u64 },
    ReaderTransactionFuture { selected: u64, actual: u64 },
}

impl From<SidecarError> for ReadySidecarError {
    fn from(value: SidecarError) -> Self {
        Self::Sidecar(value)
    }
}

/// Inspect a retained ready sidecar image after the OS layer has acquired the
/// operation lock and supplied identities derived from the retained main,
/// sidecar, directory/name, and current process domain.
///
/// File type, no-follow open, link count, path replacement, locks, and native
/// process-liveness proofs remain OS-layer responsibilities.
pub(crate) fn inspect_ready_sidecar(
    bytes: &[u8],
    expected: ReadySidecarExpectations,
) -> Result<ReadySidecarInspection, ReadySidecarError> {
    let header = decode_ready_image(bytes)?;
    if header.database_id != expected.database_id {
        return Err(ReadySidecarError::DatabaseIdMismatch);
    }
    if header.main_identity != expected.main_identity {
        return Err(ReadySidecarError::MainIdentityMismatch);
    }
    if header.sidecar_identity != expected.sidecar_identity {
        return Err(ReadySidecarError::SidecarIdentityMismatch);
    }
    if header.process_domain_kind != expected.process_domain_kind
        || header.process_domain_token != expected.process_domain_token
    {
        return Err(ReadySidecarError::ProcessDomainMismatch);
    }
    if header.basename_encoding != expected.basename_encoding
        || header.basename_len != expected.basename_len
        || header.basename_commitment != expected.basename_commitment
    {
        return Err(ReadySidecarError::BasenameMismatch);
    }

    summarize_ready_slots(bytes, header, expected.host_limits, None)
}

/// Validate transaction consistency only after the OS layer has classified and
/// reaped every proven-dead structurally valid owner under the operation lock.
pub(crate) fn validate_surviving_slot_transactions(
    bytes: &[u8],
    expected_header: SidecarHeader,
    selected_txn: u64,
    host: SlotHostLimits,
) -> Result<ReadySidecarInspection, ReadySidecarError> {
    let header = decode_ready_image(bytes)?;
    if header != expected_header {
        return Err(ReadySidecarError::HeaderChanged);
    }
    summarize_ready_slots(bytes, header, host, Some(selected_txn))
}

/// Read one structurally stable slot after ready-image inspection. The OS layer
/// uses this allocation-free accessor to classify liveness before transaction
/// consistency is checked.
pub(crate) fn ready_slot(
    bytes: &[u8],
    header: SidecarHeader,
    index: u32,
    host: SlotHostLimits,
) -> Result<(SlotRole, StableSlot), ReadySidecarError> {
    if index > header.capacity {
        return Err(ReadySidecarError::SlotIndexOutOfRange);
    }
    let role = if index == 0 {
        SlotRole::Writer
    } else {
        SlotRole::Reader
    };
    Ok((role, slot(bytes, index, role, host)?))
}

fn summarize_ready_slots(
    bytes: &[u8],
    header: SidecarHeader,
    host: SlotHostLimits,
    selected_txn: Option<u64>,
) -> Result<ReadySidecarInspection, ReadySidecarError> {
    let writer = match slot(bytes, 0, SlotRole::Writer, host)? {
        StableSlot::Free => None,
        StableSlot::Active(active) => {
            if let Some(selected_txn) = selected_txn {
                if active.txn_id != selected_txn {
                    return Err(ReadySidecarError::WriterTransactionMismatch {
                        expected: selected_txn,
                        actual: active.txn_id,
                    });
                }
            }
            Some(active)
        }
    };

    let mut active_readers = 0u32;
    let mut registering_readers = 0u32;
    let mut oldest_reader_txn = None;
    let mut lowest_free_reader_slot = None;
    for index in 1..=header.capacity {
        match slot(bytes, index, SlotRole::Reader, host)? {
            StableSlot::Free => {
                if lowest_free_reader_slot.is_none() {
                    lowest_free_reader_slot = Some(index);
                }
            }
            StableSlot::Active(active) => {
                active_readers += 1;
                if active.txn_id == 0 {
                    registering_readers += 1;
                } else {
                    if let Some(selected_txn) = selected_txn {
                        if active.txn_id > selected_txn {
                            return Err(ReadySidecarError::ReaderTransactionFuture {
                                selected: selected_txn,
                                actual: active.txn_id,
                            });
                        }
                    }
                    oldest_reader_txn = Some(match oldest_reader_txn {
                        Some(oldest) => core::cmp::min(oldest, active.txn_id),
                        None => active.txn_id,
                    });
                }
            }
        }
    }
    Ok(ReadySidecarInspection {
        header,
        writer,
        active_readers,
        registering_readers,
        oldest_reader_txn,
        lowest_free_reader_slot,
    })
}

fn slot(
    bytes: &[u8],
    index: u32,
    role: SlotRole,
    host: SlotHostLimits,
) -> Result<StableSlot, ReadySidecarError> {
    let start = usize::try_from(index)
        .ok()
        .and_then(|value| value.checked_mul(usize::from(SLOT_SIZE)))
        .and_then(|value| value.checked_add(HEADER_REGION_SIZE))
        .ok_or(ReadySidecarError::SlotOffsetOverflow)?;
    let end = start
        .checked_add(usize::from(SLOT_SIZE))
        .ok_or(ReadySidecarError::SlotOffsetOverflow)?;
    let raw: &[u8; SLOT_SIZE as usize] = bytes[start..end].try_into().unwrap();
    decode_stable_slot(raw, role, host)
        .map_err(|problem| ReadySidecarError::Slot { index, problem })
}

pub(crate) fn encode_active_slot(slot: ActiveSlot) -> [u8; SLOT_SIZE as usize] {
    let mut bytes = [0u8; SLOT_SIZE as usize];
    put_u32(&mut bytes, 0, 1);
    put_u64(&mut bytes, 8, slot.txn_id);
    put_u64(&mut bytes, 16, slot.process_id);
    put_u64(&mut bytes, 24, slot.process_start);
    put_u64(&mut bytes, 32, slot.task_id);
    bytes[40..56].copy_from_slice(&slot.nonce);
    let crc = crc32c::crc32c_with_zeroed(&bytes, SLOT_CRC_OFFSET, 4).unwrap();
    put_u32(&mut bytes, SLOT_CRC_OFFSET, crc);
    bytes
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

    fn header(sequence: u64, state: SidecarState) -> SidecarHeader {
        SidecarHeader {
            identity_kind: LocalIdentityKind::Posix,
            capacity: 3,
            state,
            database_id: [1; 16],
            main_identity: posix_identity(7, 11),
            sidecar_identity: posix_identity(7, 12),
            sidecar_id: [2; 16],
            origin: SidecarOrigin::CreateLive,
            attempted_txn_id: 1,
            attempted_commit_nonce: [3; 16],
            attempted_main_bytes: 8192,
            attempted_main_sha512: [4; 64],
            process_domain_kind: ProcessDomainKind::LinuxPidNamespace,
            process_domain_token: posix_identity(5, 9),
            basename_encoding: 1,
            basename_len: 7,
            basename_commitment: [6; 32],
            creation_security_kind: 1,
            creation_security_commitment: [7; 32],
            header_seq: sequence,
        }
    }

    fn image(left: SidecarHeader, right: SidecarHeader) -> Vec<u8> {
        let mut bytes = vec![0u8; left.exact_file_size().unwrap() as usize];
        left.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
        right.encode_into(
            (&mut bytes[PAGE_SIZE..HEADER_REGION_SIZE])
                .try_into()
                .unwrap(),
        );
        bytes
    }

    fn expectations(value: SidecarHeader) -> ReadySidecarExpectations {
        ReadySidecarExpectations {
            database_id: value.database_id,
            main_identity: value.main_identity,
            sidecar_identity: value.sidecar_identity,
            process_domain_kind: value.process_domain_kind,
            process_domain_token: value.process_domain_token,
            basename_encoding: value.basename_encoding,
            basename_len: value.basename_len,
            basename_commitment: value.basename_commitment,
            host_limits: SlotHostLimits {
                process_id_max: u32::MAX.into(),
                task_id_max: u32::MAX.into(),
            },
        }
    }

    fn put_slot(bytes: &mut [u8], index: usize, slot: ActiveSlot) {
        let start = HEADER_REGION_SIZE + index * usize::from(SLOT_SIZE);
        bytes[start..start + usize::from(SLOT_SIZE)].copy_from_slice(&encode_active_slot(slot));
    }

    #[test]
    fn exact_header_offsets_selection_and_ready_size() {
        let bytes = image(
            header(1, SidecarState::MainNamespaceAttempted),
            header(2, SidecarState::Ready),
        );
        let selected = decode_ready_image(&bytes).unwrap();
        assert_eq!(selected.header_seq, 2);
        assert_eq!(selected.capacity, 3);
        assert_eq!(selected.exact_file_size(), Some(8448));
        assert_eq!(&bytes[0..8], b"IPR4RDRS");
        assert_eq!(u16_le(&bytes, 10), 512);
        assert_eq!(u16_le(&bytes, 12), 64);
        assert!(bytes[512..PAGE_SIZE].iter().all(|&byte| byte == 0));
    }

    #[test]
    fn torn_newer_block_falls_back_but_equal_disagreement_does_not() {
        let mut bytes = image(
            header(1, SidecarState::Ready),
            header(2, SidecarState::Ready),
        );
        bytes[PAGE_SIZE + 508] ^= 1;
        assert_eq!(select_sidecar_header(&bytes).unwrap().header_seq, 1);

        let mut right = header(1, SidecarState::Initializing);
        right.capacity = 3;
        let bytes = image(header(1, SidecarState::Ready), right);
        assert_eq!(
            select_sidecar_header(&bytes),
            Err(SidecarError::EqualSequenceDisagreement)
        );

        assert_eq!(
            select_sidecar_header(&image(
                header(1, SidecarState::Ready),
                header(3, SidecarState::Ready)
            )),
            Err(SidecarError::NonAdjacentSequence { older: 1, newer: 3 })
        );
        assert_eq!(
            select_sidecar_header(&image(
                header(1, SidecarState::Ready),
                header(u64::MAX, SidecarState::Ready)
            )),
            Err(SidecarError::NonAdjacentSequence {
                older: 1,
                newer: u64::MAX,
            })
        );
        assert_eq!(
            select_sidecar_header(&image(
                header(1, SidecarState::Initializing),
                header(2, SidecarState::Ready)
            )),
            Err(SidecarError::InvalidStateTransition)
        );
        assert_eq!(
            select_sidecar_header(&image(
                header(1, SidecarState::Ready),
                header(2, SidecarState::Initializing)
            )),
            Err(SidecarError::InvalidStateTransition)
        );
    }

    #[test]
    fn immutable_identity_cannot_change_between_valid_copies() {
        let left = header(1, SidecarState::Initializing);
        let mut right = header(2, SidecarState::Ready);
        right.creation_security_commitment[0] ^= 1;
        assert_eq!(
            select_sidecar_header(&image(left, right)),
            Err(SidecarError::ImmutableIdentityMismatch)
        );
    }

    #[test]
    fn windows_identity_requires_windows_name_security_and_host_domain() {
        let mut windows = header(1, SidecarState::Ready);
        windows.identity_kind = LocalIdentityKind::Windows;
        windows.process_domain_kind = ProcessDomainKind::HostGlobal;
        windows.process_domain_token = [0; 32];
        windows.basename_encoding = 2;
        windows.basename_len = 8;
        windows.creation_security_kind = 2;
        let mut block = [0u8; PAGE_SIZE];
        windows.encode_into(&mut block);
        assert!(decode_header(&block).is_ok());

        windows.process_domain_kind = ProcessDomainKind::LinuxPidNamespace;
        windows.encode_into(&mut block);
        assert_eq!(
            decode_header(&block),
            Err(SidecarHeaderProblem::ProcessDomain)
        );
        windows.process_domain_kind = ProcessDomainKind::HostGlobal;
        windows.basename_encoding = 1;
        windows.encode_into(&mut block);
        assert_eq!(decode_header(&block), Err(SidecarHeaderProblem::Basename));
        windows.basename_encoding = 2;
        windows.creation_security_kind = 1;
        windows.encode_into(&mut block);
        assert_eq!(
            decode_header(&block),
            Err(SidecarHeaderProblem::CreationSecurity)
        );
    }

    #[test]
    fn ready_open_requires_exact_capacity_derived_size() {
        let mut bytes = image(
            header(1, SidecarState::Ready),
            header(2, SidecarState::Ready),
        );
        bytes.pop();
        assert_eq!(
            decode_ready_image(&bytes),
            Err(SidecarError::WrongFileSize {
                expected: 8448,
                actual: 8447
            })
        );
    }

    #[test]
    fn slots_fail_closed_and_reader_registration_zero_is_valid() {
        let host = SlotHostLimits {
            process_id_max: u32::MAX.into(),
            task_id_max: u32::MAX.into(),
        };
        let active = ActiveSlot {
            txn_id: 0,
            process_id: 42,
            process_start: 123,
            task_id: 7,
            nonce: [9; 16],
        };
        let bytes = encode_active_slot(active);
        assert_eq!(
            decode_stable_slot(&bytes, SlotRole::Reader, host),
            Ok(StableSlot::Active(active))
        );
        assert_eq!(
            decode_stable_slot(&bytes, SlotRole::Writer, host),
            Err(SlotProblem::WriterTransactionZero)
        );

        let mut transition = bytes;
        put_u32(&mut transition, 0, 2);
        assert_eq!(
            decode_stable_slot(&transition, SlotRole::Reader, host),
            Err(SlotProblem::Transition)
        );
        let mut corrupt = bytes;
        corrupt[24] ^= 1;
        assert_eq!(
            decode_stable_slot(&corrupt, SlotRole::Reader, host),
            Err(SlotProblem::Checksum)
        );
        let mut nonzero_free = [0u8; SLOT_SIZE as usize];
        nonzero_free[4] = 1;
        assert_eq!(
            decode_stable_slot(&nonzero_free, SlotRole::Reader, host),
            Err(SlotProblem::FreeNonzero)
        );

        let mut unrepresentable = active;
        unrepresentable.process_id = u64::from(u32::MAX) + 1;
        assert_eq!(
            decode_stable_slot(&encode_active_slot(unrepresentable), SlotRole::Reader, host),
            Err(SlotProblem::ProcessIdUnrepresentable)
        );
    }

    #[test]
    fn ready_inspection_binds_identity_and_scans_every_slot() {
        let ready = header(2, SidecarState::Ready);
        let mut bytes = image(header(1, SidecarState::Ready), ready);
        let writer = ActiveSlot {
            txn_id: 5,
            process_id: 10,
            process_start: 11,
            task_id: 12,
            nonce: [1; 16],
        };
        let registering = ActiveSlot {
            txn_id: 0,
            process_id: 20,
            process_start: 21,
            task_id: 22,
            nonce: [2; 16],
        };
        let reader = ActiveSlot {
            txn_id: 3,
            process_id: 30,
            process_start: 31,
            task_id: 32,
            nonce: [3; 16],
        };
        put_slot(&mut bytes, 0, writer);
        put_slot(&mut bytes, 1, registering);
        put_slot(&mut bytes, 2, reader);

        let expected = expectations(ready);
        assert_eq!(
            inspect_ready_sidecar(&bytes, expected).unwrap(),
            ReadySidecarInspection {
                header: ready,
                writer: Some(writer),
                active_readers: 2,
                registering_readers: 1,
                oldest_reader_txn: Some(3),
                lowest_free_reader_slot: Some(3),
            }
        );
        assert_eq!(
            validate_surviving_slot_transactions(&bytes, ready, 5, expected.host_limits).unwrap(),
            inspect_ready_sidecar(&bytes, expected).unwrap()
        );
        assert_eq!(
            ready_slot(&bytes, ready, 2, expected.host_limits),
            Ok((SlotRole::Reader, StableSlot::Active(reader)))
        );
        assert_eq!(
            ready_slot(&bytes, ready, 4, expected.host_limits),
            Err(ReadySidecarError::SlotIndexOutOfRange)
        );

        let mut wrong_database = expected;
        wrong_database.database_id[0] ^= 1;
        assert_eq!(
            inspect_ready_sidecar(&bytes, wrong_database),
            Err(ReadySidecarError::DatabaseIdMismatch)
        );

        let slot3 = HEADER_REGION_SIZE + 3 * usize::from(SLOT_SIZE);
        put_u32(&mut bytes, slot3, 2);
        assert_eq!(
            inspect_ready_sidecar(&bytes, expected),
            Err(ReadySidecarError::Slot {
                index: 3,
                problem: SlotProblem::Transition,
            })
        );
    }

    #[test]
    fn dead_owner_reaping_precedes_transaction_consistency() {
        let ready = header(2, SidecarState::Ready);
        let expected = expectations(ready);

        let mut stale_writer_bytes = image(header(1, SidecarState::Ready), ready);
        let stale_writer = ActiveSlot {
            txn_id: 4,
            process_id: 10,
            process_start: 11,
            task_id: 12,
            nonce: [1; 16],
        };
        put_slot(&mut stale_writer_bytes, 0, stale_writer);
        assert_eq!(
            inspect_ready_sidecar(&stale_writer_bytes, expected)
                .unwrap()
                .writer,
            Some(stale_writer)
        );
        assert_eq!(
            validate_surviving_slot_transactions(
                &stale_writer_bytes,
                ready,
                5,
                expected.host_limits,
            ),
            Err(ReadySidecarError::WriterTransactionMismatch {
                expected: 5,
                actual: 4,
            })
        );
        let writer_offset = HEADER_REGION_SIZE;
        stale_writer_bytes[writer_offset..writer_offset + usize::from(SLOT_SIZE)].fill(0);
        validate_surviving_slot_transactions(&stale_writer_bytes, ready, 5, expected.host_limits)
            .unwrap();

        let mut future_reader_bytes = image(header(1, SidecarState::Ready), ready);
        let future_reader = ActiveSlot {
            txn_id: 6,
            process_id: 20,
            process_start: 21,
            task_id: 22,
            nonce: [2; 16],
        };
        put_slot(&mut future_reader_bytes, 1, future_reader);
        assert_eq!(
            inspect_ready_sidecar(&future_reader_bytes, expected)
                .unwrap()
                .oldest_reader_txn,
            Some(6)
        );
        assert_eq!(
            validate_surviving_slot_transactions(
                &future_reader_bytes,
                ready,
                5,
                expected.host_limits,
            ),
            Err(ReadySidecarError::ReaderTransactionFuture {
                selected: 5,
                actual: 6,
            })
        );
    }
}
