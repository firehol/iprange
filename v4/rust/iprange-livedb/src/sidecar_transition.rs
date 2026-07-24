//! Process-local provenance for exact sidecar slot transitions.
//!
//! The OS layer must continuously retain the operation lock from `arm` until
//! target or cleanup confirmation disarms the record.

use crate::sidecar::{
    decode_stable_slot, encode_active_slot, ActiveSlot, SidecarHeader, SidecarState,
    SlotHostLimits, SlotProblem, SlotRole, StableSlot,
};

const SLOT_BYTES: usize = 64;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum DeathProof {
    PosixMissing { process_id: u64 },
    PosixPidReused { process_id: u64, current_start: u64 },
    WindowsSignaled { process_id: u64 },
    WindowsPidReused { process_id: u64, current_start: u64 },
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum SlotTransitionSource {
    Zero,
    OwnedActive(ActiveSlot),
    ProvenDeadActive {
        active: ActiveSlot,
        proof: DeathProof,
    },
}

impl SlotTransitionSource {
    const fn active(self) -> Option<ActiveSlot> {
        match self {
            Self::Zero => None,
            Self::OwnedActive(active) | Self::ProvenDeadActive { active, .. } => Some(active),
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum SlotTransitionKind {
    Claim,
    Update,
    Clear,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum SlotTransitionError {
    NotArmed,
    ProvenanceOccupied,
    HeaderNotReady,
    SlotIndexOutOfRange,
    SourceMalformed(SlotProblem),
    SourceMismatch,
    InvalidTarget(SlotProblem),
    OwnerChanged,
    InvalidDeathProof,
    TargetReadbackMismatch,
    CleanupConflict(SlotProblem),
    CleanupOwnerConflict,
    CleanupReadbackMismatch,
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) struct PreparedSlotTransition {
    header: SidecarHeader,
    role: SlotRole,
    slot_index: u32,
    kind: SlotTransitionKind,
    source: SlotTransitionSource,
    target: Option<ActiveSlot>,
    target_image: [u8; SLOT_BYTES],
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) struct ArmedSlotTransition {
    header: SidecarHeader,
    role: SlotRole,
    slot_index: u32,
    kind: SlotTransitionKind,
    source: SlotTransitionSource,
    target: Option<ActiveSlot>,
    target_image: [u8; SLOT_BYTES],
    armed: bool,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum CleanupDisposition {
    AlreadyAbsent,
    ClearOwned,
}

#[derive(Debug)]
pub(crate) enum InterruptedCause<E> {
    Io(E),
    Transition(SlotTransitionError),
}

#[derive(Debug)]
pub(crate) enum SlotCleanupError<E> {
    Io(E),
    Transition(SlotTransitionError),
}

impl PreparedSlotTransition {
    pub(crate) const fn header(&self) -> SidecarHeader {
        self.header
    }

    pub(crate) const fn slot_index(&self) -> u32 {
        self.slot_index
    }

    pub(crate) const fn role(&self) -> SlotRole {
        self.role
    }

    pub(crate) const fn kind(&self) -> SlotTransitionKind {
        self.kind
    }

    pub(crate) fn confirm_source(
        &self,
        observed: &[u8; SLOT_BYTES],
        host: SlotHostLimits,
    ) -> Result<(), SlotTransitionError> {
        match self.source {
            SlotTransitionSource::Zero => match decode_stable_slot(observed, self.role, host) {
                Ok(StableSlot::Free) => Ok(()),
                Ok(StableSlot::Active(_)) => Err(SlotTransitionError::SourceMismatch),
                Err(problem) => Err(SlotTransitionError::SourceMalformed(problem)),
            },
            SlotTransitionSource::OwnedActive(active)
            | SlotTransitionSource::ProvenDeadActive { active, .. } => {
                require_exact_active(observed, self.role, host, active)
            }
        }
    }

    pub(crate) fn claim(
        header: SidecarHeader,
        role: SlotRole,
        slot_index: u32,
        current: &[u8; SLOT_BYTES],
        target: ActiveSlot,
        host: SlotHostLimits,
    ) -> Result<Self, SlotTransitionError> {
        validate_header_slot(header, role, slot_index)?;
        match decode_stable_slot(current, role, host) {
            Ok(StableSlot::Free) => {}
            Ok(StableSlot::Active(_)) => return Err(SlotTransitionError::SourceMismatch),
            Err(problem) => return Err(SlotTransitionError::SourceMalformed(problem)),
        }
        let target_image = validated_active_image(target, role, host)?;
        Ok(Self {
            header,
            role,
            slot_index,
            kind: SlotTransitionKind::Claim,
            source: SlotTransitionSource::Zero,
            target: Some(target),
            target_image,
        })
    }

    pub(crate) fn update(
        header: SidecarHeader,
        role: SlotRole,
        slot_index: u32,
        current: &[u8; SLOT_BYTES],
        owned: ActiveSlot,
        target: ActiveSlot,
        host: SlotHostLimits,
    ) -> Result<Self, SlotTransitionError> {
        validate_header_slot(header, role, slot_index)?;
        require_exact_active(current, role, host, owned)?;
        if !same_owner(owned, target) {
            return Err(SlotTransitionError::OwnerChanged);
        }
        let target_image = validated_active_image(target, role, host)?;
        Ok(Self {
            header,
            role,
            slot_index,
            kind: SlotTransitionKind::Update,
            source: SlotTransitionSource::OwnedActive(owned),
            target: Some(target),
            target_image,
        })
    }

    pub(crate) fn clear_owned(
        header: SidecarHeader,
        role: SlotRole,
        slot_index: u32,
        current: &[u8; SLOT_BYTES],
        owned: ActiveSlot,
        host: SlotHostLimits,
    ) -> Result<Self, SlotTransitionError> {
        validate_header_slot(header, role, slot_index)?;
        require_exact_active(current, role, host, owned)?;
        Ok(Self::clear(
            header,
            role,
            slot_index,
            SlotTransitionSource::OwnedActive(owned),
        ))
    }

    pub(crate) fn clear_proven_dead(
        header: SidecarHeader,
        role: SlotRole,
        slot_index: u32,
        current: &[u8; SLOT_BYTES],
        active: ActiveSlot,
        proof: DeathProof,
        host: SlotHostLimits,
    ) -> Result<Self, SlotTransitionError> {
        validate_header_slot(header, role, slot_index)?;
        require_exact_active(current, role, host, active)?;
        if !valid_death_proof(header, active, proof) {
            return Err(SlotTransitionError::InvalidDeathProof);
        }
        Ok(Self::clear(
            header,
            role,
            slot_index,
            SlotTransitionSource::ProvenDeadActive { active, proof },
        ))
    }

    fn clear(
        header: SidecarHeader,
        role: SlotRole,
        slot_index: u32,
        source: SlotTransitionSource,
    ) -> Self {
        Self {
            header,
            role,
            slot_index,
            kind: SlotTransitionKind::Clear,
            source,
            target: None,
            target_image: [0; SLOT_BYTES],
        }
    }

    /// Arm immediately before the first attempt to publish state 2.
    pub(crate) const fn arm(self) -> ArmedSlotTransition {
        ArmedSlotTransition {
            header: self.header,
            role: self.role,
            slot_index: self.slot_index,
            kind: self.kind,
            source: self.source,
            target: self.target,
            target_image: self.target_image,
            armed: true,
        }
    }

    /// Execute the only legal state-2/body/final-state sequence.
    ///
    /// The supplied callbacks must operate on this slot through the retained
    /// descriptor while the caller continuously owns the exclusive operation
    /// lock. Every post-arm failure leaves the provenance in the caller-owned
    /// option; success disarms and removes it.
    pub(crate) fn execute<E>(
        self,
        provenance: &mut Option<ArmedSlotTransition>,
        mut write: impl FnMut(usize, &[u8]) -> Result<(), E>,
        mut read: impl FnMut(&mut [u8; SLOT_BYTES]) -> Result<(), E>,
    ) -> Result<(), InterruptedCause<E>> {
        if provenance.is_some() {
            return Err(InterruptedCause::Transition(
                SlotTransitionError::ProvenanceOccupied,
            ));
        }
        *provenance = Some(self.arm());
        let transition = provenance.as_mut().unwrap();
        let result = transition.execute_armed(&mut write, &mut read);
        if result.is_ok() {
            provenance.take();
        }
        result
    }
}

impl ArmedSlotTransition {
    pub(crate) fn execute_armed<E>(
        &mut self,
        mut write: impl FnMut(usize, &[u8]) -> Result<(), E>,
        mut read: impl FnMut(&mut [u8; SLOT_BYTES]) -> Result<(), E>,
    ) -> Result<(), InterruptedCause<E>> {
        let state2 = match self.state2_bytes() {
            Ok(bytes) => bytes,
            Err(cause) => {
                return Err(InterruptedCause::Transition(cause));
            }
        };
        if let Err(cause) = write(0, &state2) {
            return Err(InterruptedCause::Io(cause));
        }
        let body = match self.body_bytes() {
            Ok(bytes) => bytes,
            Err(cause) => {
                return Err(InterruptedCause::Transition(cause));
            }
        };
        if let Err(cause) = write(4, &body) {
            return Err(InterruptedCause::Io(cause));
        }
        let state = match self.publish_state_bytes() {
            Ok(bytes) => bytes,
            Err(cause) => {
                return Err(InterruptedCause::Transition(cause));
            }
        };
        if let Err(cause) = write(0, &state) {
            return Err(InterruptedCause::Io(cause));
        }
        let mut observed = [0u8; SLOT_BYTES];
        if let Err(cause) = read(&mut observed) {
            return Err(InterruptedCause::Io(cause));
        }
        self.confirm_target(&observed)
            .map_err(InterruptedCause::Transition)
    }
}

impl ArmedSlotTransition {
    /// Resolve an interrupted operation to exact all-zero state. On every
    /// error this value retains the still-armed provenance for another retry.
    pub(crate) fn retry_cleanup<IOError>(
        &mut self,
        host: SlotHostLimits,
        mut write: impl FnMut(usize, &[u8]) -> Result<(), IOError>,
        mut read: impl FnMut(&mut [u8; SLOT_BYTES]) -> Result<(), IOError>,
    ) -> Result<CleanupDisposition, SlotCleanupError<IOError>> {
        let mut observed = [0u8; SLOT_BYTES];
        read(&mut observed).map_err(SlotCleanupError::Io)?;
        match self
            .cleanup_disposition(&observed, host)
            .map_err(SlotCleanupError::Transition)?
        {
            CleanupDisposition::AlreadyAbsent => self
                .confirm_cleanup(&observed, host)
                .map_err(SlotCleanupError::Transition),
            CleanupDisposition::ClearOwned => {
                let state2 = self.state2_bytes().map_err(SlotCleanupError::Transition)?;
                write(0, &state2).map_err(SlotCleanupError::Io)?;
                let body = self
                    .cleanup_body_bytes()
                    .map_err(SlotCleanupError::Transition)?;
                write(4, &body).map_err(SlotCleanupError::Io)?;
                let state = self
                    .cleanup_publish_state_bytes()
                    .map_err(SlotCleanupError::Transition)?;
                write(0, &state).map_err(SlotCleanupError::Io)?;
                read(&mut observed).map_err(SlotCleanupError::Io)?;
                self.confirm_cleanup(&observed, host)
                    .map_err(SlotCleanupError::Transition)
            }
        }
    }
}

impl ArmedSlotTransition {
    pub(crate) const fn header(&self) -> SidecarHeader {
        self.header
    }

    pub(crate) const fn role(&self) -> SlotRole {
        self.role
    }

    pub(crate) const fn slot_index(&self) -> u32 {
        self.slot_index
    }

    pub(crate) const fn kind(&self) -> SlotTransitionKind {
        self.kind
    }

    pub(crate) const fn source(&self) -> SlotTransitionSource {
        self.source
    }

    pub(crate) const fn target(&self) -> Option<ActiveSlot> {
        self.target
    }

    pub(crate) const fn is_armed(&self) -> bool {
        self.armed
    }

    /// First positional write for every transition.
    pub(crate) const fn state2_bytes(&self) -> Result<[u8; 4], SlotTransitionError> {
        if !self.armed {
            return Err(SlotTransitionError::NotArmed);
        }
        Ok(2u32.to_le_bytes())
    }

    /// Second positional write, covering slot bytes `[4,64)`.
    pub(crate) fn body_bytes(&self) -> Result<[u8; SLOT_BYTES - 4], SlotTransitionError> {
        if !self.armed {
            return Err(SlotTransitionError::NotArmed);
        }
        Ok(self.target_image[4..].try_into().unwrap())
    }

    /// Final positional write publishes active state 1 or free state 0.
    pub(crate) const fn publish_state_bytes(&self) -> Result<[u8; 4], SlotTransitionError> {
        if !self.armed {
            return Err(SlotTransitionError::NotArmed);
        }
        Ok(match self.target {
            Some(_) => 1u32.to_le_bytes(),
            None => 0u32.to_le_bytes(),
        })
    }

    pub(crate) fn confirm_target(
        &mut self,
        observed: &[u8; SLOT_BYTES],
    ) -> Result<(), SlotTransitionError> {
        if !self.armed {
            return Err(SlotTransitionError::NotArmed);
        }
        if *observed != self.target_image {
            return Err(SlotTransitionError::TargetReadbackMismatch);
        }
        self.armed = false;
        Ok(())
    }

    /// Classify the only states that process-local provenance may safely
    /// abandon after an interrupted claim, update, or clear.
    pub(crate) fn cleanup_disposition(
        &self,
        observed: &[u8; SLOT_BYTES],
        host: SlotHostLimits,
    ) -> Result<CleanupDisposition, SlotTransitionError> {
        if !self.armed {
            return Err(SlotTransitionError::NotArmed);
        }
        if observed.iter().all(|&byte| byte == 0) {
            return Ok(CleanupDisposition::AlreadyAbsent);
        }
        if u32::from_le_bytes(observed[..4].try_into().unwrap()) == 2 {
            return Ok(CleanupDisposition::ClearOwned);
        }
        let active = match decode_stable_slot(observed, self.role, host) {
            Ok(StableSlot::Active(active)) => active,
            Ok(StableSlot::Free) => return Ok(CleanupDisposition::AlreadyAbsent),
            Err(problem) => return Err(SlotTransitionError::CleanupConflict(problem)),
        };
        let expected = self.source.active().or(self.target);
        let Some(expected) = expected else {
            return Err(SlotTransitionError::CleanupOwnerConflict);
        };
        if active.nonce != expected.nonce || !same_owner(active, expected) {
            return Err(SlotTransitionError::CleanupOwnerConflict);
        }
        let source_txn = self.source.active().map(|source| source.txn_id);
        let target_txn = self.target.map(|target| target.txn_id);
        if Some(active.txn_id) != source_txn && Some(active.txn_id) != target_txn {
            return Err(SlotTransitionError::CleanupOwnerConflict);
        }
        Ok(CleanupDisposition::ClearOwned)
    }

    /// Cleanup uses the same state-2/body/state-0 ordering, regardless of the
    /// interrupted transition's original target.
    pub(crate) const fn cleanup_body_bytes(
        &self,
    ) -> Result<[u8; SLOT_BYTES - 4], SlotTransitionError> {
        if !self.armed {
            return Err(SlotTransitionError::NotArmed);
        }
        Ok([0; SLOT_BYTES - 4])
    }

    pub(crate) const fn cleanup_publish_state_bytes(&self) -> Result<[u8; 4], SlotTransitionError> {
        if !self.armed {
            return Err(SlotTransitionError::NotArmed);
        }
        Ok(0u32.to_le_bytes())
    }

    pub(crate) fn confirm_cleanup(
        &mut self,
        observed: &[u8; SLOT_BYTES],
        host: SlotHostLimits,
    ) -> Result<CleanupDisposition, SlotTransitionError> {
        let disposition = self.cleanup_disposition(observed, host)?;
        match disposition {
            CleanupDisposition::AlreadyAbsent => {
                self.armed = false;
                Ok(disposition)
            }
            CleanupDisposition::ClearOwned => Err(SlotTransitionError::CleanupReadbackMismatch),
        }
    }
}

fn validate_header_slot(
    header: SidecarHeader,
    role: SlotRole,
    slot_index: u32,
) -> Result<(), SlotTransitionError> {
    if header.state != SidecarState::Ready {
        return Err(SlotTransitionError::HeaderNotReady);
    }
    let valid = match role {
        SlotRole::Writer => slot_index == 0,
        SlotRole::Reader => slot_index != 0 && slot_index <= header.capacity,
    };
    if !valid {
        return Err(SlotTransitionError::SlotIndexOutOfRange);
    }
    Ok(())
}

fn require_exact_active(
    current: &[u8; SLOT_BYTES],
    role: SlotRole,
    host: SlotHostLimits,
    expected: ActiveSlot,
) -> Result<(), SlotTransitionError> {
    match decode_stable_slot(current, role, host) {
        Ok(StableSlot::Active(active)) if active == expected => Ok(()),
        Ok(_) => Err(SlotTransitionError::SourceMismatch),
        Err(problem) => Err(SlotTransitionError::SourceMalformed(problem)),
    }
}

fn validated_active_image(
    active: ActiveSlot,
    role: SlotRole,
    host: SlotHostLimits,
) -> Result<[u8; SLOT_BYTES], SlotTransitionError> {
    let image = encode_active_slot(active);
    match decode_stable_slot(&image, role, host) {
        Ok(StableSlot::Active(decoded)) if decoded == active => Ok(image),
        Ok(_) => Err(SlotTransitionError::SourceMismatch),
        Err(problem) => Err(SlotTransitionError::InvalidTarget(problem)),
    }
}

fn same_owner(left: ActiveSlot, right: ActiveSlot) -> bool {
    left.process_id == right.process_id
        && left.process_start == right.process_start
        && left.task_id == right.task_id
        && left.nonce == right.nonce
}

fn valid_death_proof(header: SidecarHeader, active: ActiveSlot, proof: DeathProof) -> bool {
    let (windows, process_id, current_start, reused) = match proof {
        DeathProof::PosixMissing { process_id } => (false, process_id, 0, false),
        DeathProof::PosixPidReused {
            process_id,
            current_start,
        } => (false, process_id, current_start, true),
        DeathProof::WindowsSignaled { process_id } => (true, process_id, 0, false),
        DeathProof::WindowsPidReused {
            process_id,
            current_start,
        } => (true, process_id, current_start, true),
    };
    if windows
        != matches!(
            header.identity_kind,
            crate::sidecar::LocalIdentityKind::Windows
        )
        || process_id != active.process_id
    {
        return false;
    }
    !reused
        || (active.process_start != 0
            && current_start != 0
            && active.process_start != current_start)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::sidecar::{LocalIdentityKind, ProcessDomainKind, SidecarOrigin, SidecarState};
    use crate::test_alloc::count_thread_allocations;
    use std::cell::{Cell, RefCell};
    use std::vec::Vec;

    const HOST: SlotHostLimits = SlotHostLimits {
        process_id_max: u32::MAX as u64,
        task_id_max: u64::MAX,
    };

    fn header(kind: LocalIdentityKind) -> SidecarHeader {
        SidecarHeader {
            identity_kind: kind,
            capacity: 3,
            state: SidecarState::Ready,
            database_id: [1; 16],
            main_identity: [2; 32],
            sidecar_identity: [3; 32],
            sidecar_id: [4; 16],
            origin: SidecarOrigin::CreateLive,
            attempted_txn_id: 1,
            attempted_commit_nonce: [5; 16],
            attempted_main_bytes: 8192,
            attempted_main_sha512: [6; 64],
            process_domain_kind: ProcessDomainKind::HostGlobal,
            process_domain_token: [0; 32],
            basename_encoding: kind as u16,
            basename_len: 2,
            basename_commitment: [7; 32],
            creation_security_kind: kind as u16,
            creation_security_commitment: [8; 32],
            header_seq: 9,
        }
    }

    fn active(txn_id: u64, nonce: u8) -> ActiveSlot {
        ActiveSlot {
            txn_id,
            process_id: 123,
            process_start: 456,
            task_id: 789,
            nonce: [nonce; 16],
        }
    }

    fn apply_target(transition: &ArmedSlotTransition) -> [u8; SLOT_BYTES] {
        let mut raw = [0u8; SLOT_BYTES];
        raw[..4].copy_from_slice(&transition.state2_bytes().unwrap());
        raw[4..].copy_from_slice(&transition.body_bytes().unwrap());
        raw[..4].copy_from_slice(&transition.publish_state_bytes().unwrap());
        raw
    }

    #[test]
    fn claim_is_armed_before_state2_and_confirms_only_exact_target() {
        let target = active(0, 1);
        let prepared = PreparedSlotTransition::claim(
            header(LocalIdentityKind::Posix),
            SlotRole::Reader,
            1,
            &[0; SLOT_BYTES],
            target,
            HOST,
        )
        .unwrap();
        let mut armed = prepared.arm();
        assert!(armed.is_armed());
        assert_eq!(armed.kind(), SlotTransitionKind::Claim);
        assert_eq!(armed.slot_index(), 1);
        assert_eq!(armed.state2_bytes(), Ok(2u32.to_le_bytes()));

        let target_image = apply_target(&armed);
        assert_eq!(
            decode_stable_slot(&target_image, SlotRole::Reader, HOST),
            Ok(StableSlot::Active(target))
        );
        let mut wrong = target_image;
        wrong[24] ^= 1;
        assert_eq!(
            armed.confirm_target(&wrong),
            Err(SlotTransitionError::TargetReadbackMismatch)
        );
        assert!(armed.is_armed());
        armed.confirm_target(&target_image).unwrap();
        assert!(!armed.is_armed());
        assert_eq!(
            armed.confirm_target(&target_image),
            Err(SlotTransitionError::NotArmed)
        );
        assert_eq!(armed.state2_bytes(), Err(SlotTransitionError::NotArmed));
        assert_eq!(armed.body_bytes(), Err(SlotTransitionError::NotArmed));
        assert_eq!(
            armed.publish_state_bytes(),
            Err(SlotTransitionError::NotArmed)
        );
    }

    #[test]
    fn preparation_rejects_bad_role_index_nonce_and_source() {
        let header = header(LocalIdentityKind::Posix);
        assert_eq!(
            PreparedSlotTransition::claim(
                header,
                SlotRole::Writer,
                1,
                &[0; SLOT_BYTES],
                active(2, 1),
                HOST,
            ),
            Err(SlotTransitionError::SlotIndexOutOfRange)
        );
        assert_eq!(
            PreparedSlotTransition::claim(
                header,
                SlotRole::Reader,
                0,
                &[0; SLOT_BYTES],
                active(0, 1),
                HOST,
            ),
            Err(SlotTransitionError::SlotIndexOutOfRange)
        );
        assert_eq!(
            PreparedSlotTransition::claim(
                header,
                SlotRole::Reader,
                4,
                &[0; SLOT_BYTES],
                active(0, 1),
                HOST,
            ),
            Err(SlotTransitionError::SlotIndexOutOfRange)
        );
        assert_eq!(
            PreparedSlotTransition::claim(
                header,
                SlotRole::Reader,
                1,
                &[0; SLOT_BYTES],
                active(0, 0),
                HOST,
            ),
            Err(SlotTransitionError::InvalidTarget(SlotProblem::NonceZero))
        );
        let occupied = encode_active_slot(active(3, 3));
        assert_eq!(
            PreparedSlotTransition::claim(
                header,
                SlotRole::Reader,
                1,
                &occupied,
                active(0, 1),
                HOST,
            ),
            Err(SlotTransitionError::SourceMismatch)
        );
    }

    #[test]
    fn update_requires_exact_source_and_preserves_owner() {
        let old = active(0, 1);
        let new = active(11, 1);
        let old_image = encode_active_slot(old);
        let prepared = PreparedSlotTransition::update(
            header(LocalIdentityKind::Posix),
            SlotRole::Reader,
            2,
            &old_image,
            old,
            new,
            HOST,
        )
        .unwrap();
        assert_eq!(prepared.kind, SlotTransitionKind::Update);

        let mut changed_owner = new;
        changed_owner.process_start += 1;
        assert_eq!(
            PreparedSlotTransition::update(
                header(LocalIdentityKind::Posix),
                SlotRole::Reader,
                2,
                &old_image,
                old,
                changed_owner,
                HOST,
            ),
            Err(SlotTransitionError::OwnerChanged)
        );
        assert_eq!(
            PreparedSlotTransition::update(
                header(LocalIdentityKind::Posix),
                SlotRole::Reader,
                2,
                &encode_active_slot(active(1, 1)),
                old,
                new,
                HOST,
            ),
            Err(SlotTransitionError::SourceMismatch)
        );
    }

    #[test]
    fn cleanup_accepts_only_owned_interrupted_states_and_rejects_foreign_reuse() {
        let old = active(0, 1);
        let new = active(11, 1);
        let mut armed = PreparedSlotTransition::update(
            header(LocalIdentityKind::Posix),
            SlotRole::Reader,
            1,
            &encode_active_slot(old),
            old,
            new,
            HOST,
        )
        .unwrap()
        .arm();

        assert_eq!(
            armed.cleanup_disposition(&[0; SLOT_BYTES], HOST),
            Ok(CleanupDisposition::AlreadyAbsent)
        );
        let mut transition = encode_active_slot(old);
        transition[..4].copy_from_slice(&2u32.to_le_bytes());
        assert_eq!(
            armed.cleanup_disposition(&transition, HOST),
            Ok(CleanupDisposition::ClearOwned)
        );
        assert_eq!(
            armed.cleanup_disposition(&encode_active_slot(old), HOST),
            Ok(CleanupDisposition::ClearOwned)
        );
        assert_eq!(
            armed.cleanup_disposition(&encode_active_slot(new), HOST),
            Ok(CleanupDisposition::ClearOwned)
        );
        assert_eq!(
            armed.cleanup_disposition(&encode_active_slot(active(11, 2)), HOST),
            Err(SlotTransitionError::CleanupOwnerConflict)
        );

        let mut same_nonce_foreign = new;
        same_nonce_foreign.process_id += 1;
        assert_eq!(
            armed.cleanup_disposition(&encode_active_slot(same_nonce_foreign), HOST),
            Err(SlotTransitionError::CleanupOwnerConflict)
        );
        let mut same_nonce_wrong_txn = new;
        same_nonce_wrong_txn.txn_id += 1;
        assert_eq!(
            armed.cleanup_disposition(&encode_active_slot(same_nonce_wrong_txn), HOST),
            Err(SlotTransitionError::CleanupOwnerConflict)
        );

        assert_eq!(
            armed.confirm_cleanup(&encode_active_slot(active(11, 2)), HOST),
            Err(SlotTransitionError::CleanupOwnerConflict)
        );
        assert!(armed.is_armed());
    }

    #[test]
    fn cleanup_write_sequence_always_publishes_exact_zero() {
        let owned = active(5, 1);
        let mut armed = PreparedSlotTransition::clear_owned(
            header(LocalIdentityKind::Posix),
            SlotRole::Reader,
            1,
            &encode_active_slot(owned),
            owned,
            HOST,
        )
        .unwrap()
        .arm();
        let mut raw = encode_active_slot(owned);
        raw[..4].copy_from_slice(&armed.state2_bytes().unwrap());
        raw[4..].copy_from_slice(&armed.cleanup_body_bytes().unwrap());
        raw[..4].copy_from_slice(&armed.cleanup_publish_state_bytes().unwrap());
        assert_eq!(raw, [0; SLOT_BYTES]);
        assert_eq!(
            armed.confirm_cleanup(&raw, HOST),
            Ok(CleanupDisposition::AlreadyAbsent)
        );
        assert!(!armed.is_armed());
        assert_eq!(
            armed.cleanup_body_bytes(),
            Err(SlotTransitionError::NotArmed)
        );
        assert_eq!(
            armed.cleanup_publish_state_bytes(),
            Err(SlotTransitionError::NotArmed)
        );
    }

    #[test]
    fn proven_dead_source_requires_platform_exact_observations() {
        let active = active(8, 1);
        let image = encode_active_slot(active);
        let posix = header(LocalIdentityKind::Posix);
        assert!(PreparedSlotTransition::clear_proven_dead(
            posix,
            SlotRole::Writer,
            0,
            &image,
            active,
            DeathProof::PosixMissing { process_id: 123 },
            HOST,
        )
        .is_ok());
        assert!(PreparedSlotTransition::clear_proven_dead(
            posix,
            SlotRole::Writer,
            0,
            &image,
            active,
            DeathProof::PosixPidReused {
                process_id: 123,
                current_start: 999,
            },
            HOST,
        )
        .is_ok());
        assert_eq!(
            PreparedSlotTransition::clear_proven_dead(
                posix,
                SlotRole::Writer,
                0,
                &image,
                active,
                DeathProof::PosixPidReused {
                    process_id: 123,
                    current_start: 456,
                },
                HOST,
            ),
            Err(SlotTransitionError::InvalidDeathProof)
        );
        assert_eq!(
            PreparedSlotTransition::clear_proven_dead(
                posix,
                SlotRole::Writer,
                0,
                &image,
                active,
                DeathProof::WindowsSignaled { process_id: 123 },
                HOST,
            ),
            Err(SlotTransitionError::InvalidDeathProof)
        );

        let windows = header(LocalIdentityKind::Windows);
        assert!(PreparedSlotTransition::clear_proven_dead(
            windows,
            SlotRole::Writer,
            0,
            &image,
            active,
            DeathProof::WindowsSignaled { process_id: 123 },
            HOST,
        )
        .is_ok());
    }

    #[test]
    fn unarmed_state2_remains_malformed_to_ordinary_inspection() {
        let mut raw = encode_active_slot(active(1, 1));
        raw[..4].copy_from_slice(&2u32.to_le_bytes());
        assert_eq!(
            decode_stable_slot(&raw, SlotRole::Reader, HOST),
            Err(SlotProblem::Transition)
        );
    }

    #[test]
    fn executor_enforces_write_order_and_exact_readback() {
        let target = active(0, 1);
        let prepared = PreparedSlotTransition::claim(
            header(LocalIdentityKind::Posix),
            SlotRole::Reader,
            1,
            &[0; SLOT_BYTES],
            target,
            HOST,
        )
        .unwrap();
        let slot = RefCell::new([0u8; SLOT_BYTES]);
        let writes = RefCell::new(Vec::new());
        let mut provenance = None;
        prepared
            .execute(
                &mut provenance,
                |offset, bytes| {
                    writes.borrow_mut().push((offset, bytes.to_vec()));
                    slot.borrow_mut()[offset..offset + bytes.len()].copy_from_slice(bytes);
                    Ok::<(), ()>(())
                },
                |observed| {
                    observed.copy_from_slice(&*slot.borrow());
                    Ok::<(), ()>(())
                },
            )
            .unwrap();
        assert!(provenance.is_none());
        let writes = writes.into_inner();
        assert_eq!(writes.len(), 3);
        assert_eq!(writes[0].0, 0);
        assert_eq!(writes[0].1, 2u32.to_le_bytes());
        assert_eq!(writes[1].0, 4);
        assert_eq!(writes[1].1.len(), SLOT_BYTES - 4);
        assert_eq!(writes[2].0, 0);
        assert_eq!(writes[2].1, 1u32.to_le_bytes());
        assert_eq!(
            decode_stable_slot(&slot.into_inner(), SlotRole::Reader, HOST),
            Ok(StableSlot::Active(target))
        );
    }

    #[test]
    fn executor_allocates_nothing_across_armed_provenance_window() {
        let target = active(0, 1);
        let prepared = PreparedSlotTransition::claim(
            header(LocalIdentityKind::Posix),
            SlotRole::Reader,
            1,
            &[0; SLOT_BYTES],
            target,
            HOST,
        )
        .unwrap();
        let slot = RefCell::new([0u8; SLOT_BYTES]);
        let writes = Cell::new(0usize);
        let mut provenance = None;

        // Counting the complete call is stronger than counting only after the
        // first callback: execute arms provenance before making that callback.
        let (result, allocations) = count_thread_allocations(|| {
            prepared.execute(
                &mut provenance,
                |offset, bytes| {
                    writes.set(writes.get() + 1);
                    slot.borrow_mut()[offset..offset + bytes.len()].copy_from_slice(bytes);
                    Ok::<(), ()>(())
                },
                |observed| {
                    observed.copy_from_slice(&*slot.borrow());
                    Ok::<(), ()>(())
                },
            )
        });

        result.unwrap();
        assert_eq!(allocations, 0);
        assert_eq!(writes.get(), 3);
        assert!(provenance.is_none());
        assert_eq!(
            decode_stable_slot(&slot.into_inner(), SlotRole::Reader, HOST),
            Ok(StableSlot::Active(target))
        );
    }

    #[test]
    fn interrupted_executor_retains_authority_until_ordered_zero_cleanup() {
        let prepared = PreparedSlotTransition::claim(
            header(LocalIdentityKind::Posix),
            SlotRole::Reader,
            2,
            &[0; SLOT_BYTES],
            active(0, 1),
            HOST,
        )
        .unwrap();
        let slot = RefCell::new([0u8; SLOT_BYTES]);
        let writes = Cell::new(0usize);
        let mut provenance = None;
        let interrupted = prepared
            .execute(
                &mut provenance,
                |offset, bytes| {
                    let ordinal = writes.get();
                    writes.set(ordinal + 1);
                    if ordinal == 1 {
                        return Err("body");
                    }
                    slot.borrow_mut()[offset..offset + bytes.len()].copy_from_slice(bytes);
                    Ok(())
                },
                |observed| {
                    observed.copy_from_slice(&*slot.borrow());
                    Ok(())
                },
            )
            .unwrap_err();
        assert!(matches!(interrupted, InterruptedCause::Io("body")));
        assert!(provenance.as_ref().unwrap().is_armed());
        assert_eq!(
            u32::from_le_bytes(slot.borrow()[..4].try_into().unwrap()),
            2
        );

        let cleanup_writes = RefCell::new(Vec::new());
        let disposition = provenance
            .as_mut()
            .unwrap()
            .retry_cleanup(
                HOST,
                |offset, bytes| {
                    cleanup_writes.borrow_mut().push((offset, bytes.to_vec()));
                    slot.borrow_mut()[offset..offset + bytes.len()].copy_from_slice(bytes);
                    Ok::<(), ()>(())
                },
                |observed| {
                    observed.copy_from_slice(&*slot.borrow());
                    Ok::<(), ()>(())
                },
            )
            .unwrap();
        assert_eq!(disposition, CleanupDisposition::AlreadyAbsent);
        assert!(!provenance.as_ref().unwrap().is_armed());
        assert_eq!(slot.into_inner(), [0; SLOT_BYTES]);
        let cleanup_writes = cleanup_writes.into_inner();
        assert_eq!(cleanup_writes.len(), 3);
        assert_eq!(cleanup_writes[0].0, 0);
        assert_eq!(cleanup_writes[0].1, 2u32.to_le_bytes());
        assert_eq!(cleanup_writes[1].0, 4);
        assert_eq!(cleanup_writes[2].0, 0);
        assert_eq!(cleanup_writes[2].1, 0u32.to_le_bytes());
    }

    #[test]
    fn target_readback_mismatch_returns_still_armed_provenance() {
        let prepared = PreparedSlotTransition::claim(
            header(LocalIdentityKind::Posix),
            SlotRole::Reader,
            1,
            &[0; SLOT_BYTES],
            active(0, 1),
            HOST,
        )
        .unwrap();
        let mut provenance = None;
        let interrupted = prepared
            .execute(
                &mut provenance,
                |_, _| Ok::<(), ()>(()),
                |observed| {
                    observed.fill(0);
                    Ok::<(), ()>(())
                },
            )
            .unwrap_err();
        assert!(matches!(
            interrupted,
            InterruptedCause::Transition(SlotTransitionError::TargetReadbackMismatch)
        ));
        assert!(provenance.as_ref().unwrap().is_armed());
    }
}
