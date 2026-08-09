//! Exact ownership and bounded I/O for authorized recovery scratch files.

use std::borrow::Cow;
use std::fs::File;
use std::path::Path;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, MutexGuard};

use crate::contract::MetaV4;
use crate::error::{Error, ErrorCode, Result};
use crate::mapping::Mapping;
use crate::publication::namespace::{
    local_identity, regular_identity, Directory, Identity, Name, NamespaceError,
    CREATION_SECURITY_KIND,
};
use crate::publication::security::{self, Profile};
use crate::random;
use crate::validation::LocalFileIdentity;

#[path = "scratch/cleanup.rs"]
mod cleanup;
pub(crate) use cleanup::residue_error;
#[cfg(unix)]
use cleanup::{remove, residue, scratch_problem, set_removed_problems};
#[path = "scratch/fixed.rs"]
mod fixed;
pub(crate) use fixed::ScratchFile;
#[path = "scratch/format.rs"]
pub(super) mod format;
#[cfg(test)]
#[cfg(all(test, unix))]
use crate::artifact_name::encode_nibble as hex;
pub(crate) use format::HEADER_SIZE;
use format::{header, scratch_name};

const MAX_OWNED: usize = 2;
const MAPPING_WINDOW: u64 = 8 * 1024 * 1024;

/// One retained scratch slot.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct ScratchSlot(usize);

/// Exact cleanup problem retained for one possible scratch residue.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct ScratchProblem {
    pub(crate) code: ErrorCode,
    pub(crate) os_code: Option<i32>,
    pub(crate) detail: Cow<'static, str>,
}

/// One authorized scratch artifact whose durable absence was not proved.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct ScratchResidue {
    pub(crate) ordinal: u32,
    pub(crate) directory_identity: LocalFileIdentity,
    pub(crate) basename: Box<[u8]>,
    pub(crate) identity: LocalFileIdentity,
    pub(crate) creation_security_kind: u16,
    pub(crate) creation_security_commitment: [u8; 32],
    pub(crate) problem: ScratchProblem,
}

pub(crate) fn checkpoint_basename(attempt_id: [u8; 16], ordinal: u32) -> Result<Box<[u8]>> {
    Ok(scratch_name(attempt_id, ordinal)?.bytes().into())
}

/// Terminal facts from one scratch attempt.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct ScratchCleanup {
    pub(crate) attempt_id: [u8; 16],
    pub(crate) directory_identity: LocalFileIdentity,
    pub(crate) creation_security_kind: u16,
    pub(crate) creation_security_commitment: [u8; 32],
    pub(crate) residues: Vec<ScratchResidue>,
    pub(crate) housekeeping: crate::publication::Housekeeping,
    pub(crate) visible_housekeeping: Vec<crate::publication::HousekeepingArtifact>,
}

impl ScratchCleanup {
    pub(crate) fn clean(&self) -> bool {
        self.residues.is_empty()
    }
}

/// One lazily established recovery scratch namespace.
pub(crate) struct Scratch {
    directory: Directory,
    profile: Profile,
    pub(super) attempt_id: [u8; 16],
    next_ordinal: u64,
    source: MetaV4,
    max_bytes: u64,
    max_files: usize,
    max_open_files: u32,
    retained_bytes: u64,
    owned: [Option<Owned>; MAX_OWNED],
}

struct Owned {
    shared: Arc<SharedFile>,
    name: Name,
    identity: Identity,
    ordinal: u32,
}

struct SharedFile {
    file: File,
    mapping: Mutex<Option<Mapping>>,
    length: AtomicU64,
    capacity: AtomicU64,
}

impl Scratch {
    pub(crate) fn start(
        directory: &Path,
        source: MetaV4,
        max_bytes: u64,
        max_files: u32,
        max_open_files: u32,
    ) -> Result<Self> {
        if max_files == 0 || max_open_files < 3 {
            return Err(Error::BudgetExceeded(
                "recovery scratch requires one file descriptor",
            ));
        }
        if max_bytes < HEADER_SIZE {
            return Err(Error::BudgetExceeded("recovery scratch bytes"));
        }
        let directory = Directory::open(directory).map_err(namespace_error)?;
        let profile = Profile::capture().map_err(namespace_error)?;
        let attempt_id = new_attempt(&directory)?;
        let scratch = Self {
            directory,
            profile,
            attempt_id,
            next_ordinal: 0,
            source,
            max_bytes,
            max_files: usize::try_from(max_files)
                .unwrap_or(usize::MAX)
                .min(MAX_OWNED),
            max_open_files,
            retained_bytes: 0,
            owned: [None, None],
        };
        crate::worker::start_scratch_checkpoint(
            scratch.attempt_id,
            local(scratch.directory.identity()),
            &crate::publication::CreationSecurity {
                kind: CREATION_SECURITY_KIND,
                commitment: scratch.profile.commitment(),
            },
        )?;
        Ok(scratch)
    }

    pub(crate) fn create(&mut self) -> Result<ScratchSlot> {
        let slot = self.free_slot()?;
        let ordinal = self.take_ordinal()?;
        self.install(slot, ordinal)?;
        let header = header(self.source, self.attempt_id, ordinal, &self.profile);
        let owned = self.owned[slot].as_ref().expect("scratch owner installed");
        let file = &owned.shared.file;
        security::secure_creator_only(file, &self.profile).map_err(namespace_error)?;
        owned.shared.write(0, &header)?;
        Ok(ScratchSlot(slot))
    }

    pub(crate) fn require_external_sort(&self) -> Result<()> {
        if self.max_files < 2 || self.max_open_files < 4 {
            return Err(Error::BudgetExceeded(
                "external recovery sort requires two scratch files",
            ));
        }
        Ok(())
    }

    pub(crate) fn remaining_bytes(&self) -> u64 {
        self.max_bytes - self.retained_bytes
    }

    fn free_slot(&self) -> Result<usize> {
        let slot = self
            .owned
            .iter()
            .take(self.max_files)
            .position(Option::is_none)
            .ok_or(Error::BudgetExceeded("recovery scratch files"))?;
        Ok(slot)
    }

    fn take_ordinal(&mut self) -> Result<u32> {
        let ordinal = u32::try_from(self.next_ordinal)
            .map_err(|_| Error::ArithmeticOverflow("recovery scratch ordinal"))?;
        self.next_ordinal = self
            .next_ordinal
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("recovery scratch ordinal"))?;
        Ok(ordinal)
    }

    fn install(&mut self, slot: usize, ordinal: u32) -> Result<()> {
        self.compact_mapping_slack(None)?;
        self.require_growth(0, HEADER_SIZE)?;
        let name = scratch_name(self.attempt_id, ordinal)?;
        let file = self
            .directory
            .create(&name, &self.profile)
            .map_err(namespace_error)?;
        let identity =
            regular_identity(&file, self.directory.identity()).map_err(namespace_error)?;
        self.owned[slot] = Some(Owned {
            shared: Arc::new(SharedFile {
                file,
                mapping: Mutex::new(None),
                length: AtomicU64::new(0),
                capacity: AtomicU64::new(0),
            }),
            name,
            identity,
            ordinal,
        });
        let owner = self.owned[slot].as_ref().expect("scratch owner installed");
        crate::worker::add_scratch_checkpoint(ordinal, local(owner.identity))?;
        let shared = &self.owned[slot]
            .as_ref()
            .expect("scratch owner installed")
            .shared;
        shared.remap(HEADER_SIZE, HEADER_SIZE)?;
        self.retained_bytes += HEADER_SIZE;
        Ok(())
    }

    pub(crate) fn length(&self, slot: ScratchSlot) -> u64 {
        self.owner(slot).shared.length.load(Ordering::Relaxed)
    }

    pub(crate) fn write(&mut self, slot: ScratchSlot, offset: u64, bytes: &[u8]) -> Result<()> {
        if offset < HEADER_SIZE {
            return Err(Error::InvalidArgument(
                "scratch records cannot overwrite their ownership header",
            ));
        }
        let end = offset
            .checked_add(bytes.len() as u64)
            .ok_or(Error::ArithmeticOverflow("recovery scratch write"))?;
        let old = self.length(slot);
        let new = old.max(end);
        self.ensure_write_capacity(slot, end)?;
        self.owner(slot).shared.write(offset, bytes)?;
        self.owner(slot).shared.length.store(new, Ordering::Relaxed);
        Ok(())
    }

    pub(crate) fn read(&self, slot: ScratchSlot, offset: u64, bytes: &mut [u8]) -> Result<()> {
        let end = offset
            .checked_add(bytes.len() as u64)
            .ok_or(Error::ArithmeticOverflow("recovery scratch read"))?;
        if offset < HEADER_SIZE || end > self.length(slot) {
            return Err(Error::Corrupt("scratch read exceeds its retained length"));
        }
        self.owner(slot).shared.read(offset, bytes)
    }

    pub(crate) fn reset(&mut self, slot: ScratchSlot) -> Result<()> {
        self.resize(slot, HEADER_SIZE)
    }

    pub(crate) fn resize(&mut self, slot: ScratchSlot, length: u64) -> Result<()> {
        if length < HEADER_SIZE {
            return Err(Error::InvalidArgument(
                "scratch length cannot exclude its ownership header",
            ));
        }
        let old = self.capacity(slot);
        self.require_growth(old, length)?;
        self.owner(slot).shared.remap(length, length)?;
        self.retained_bytes = self.retained_bytes - old + length;
        Ok(())
    }

    pub(crate) fn detach(&mut self, slot: ScratchSlot) -> Result<ScratchFile> {
        let owner = self.owner(slot);
        Ok(ScratchFile {
            slot,
            shared: Arc::clone(&owner.shared),
        })
    }

    pub(crate) fn attach(&mut self, detached: ScratchFile) -> ScratchSlot {
        let owner = self.owner(detached.slot);
        assert!(
            Arc::ptr_eq(&owner.shared, &detached.shared),
            "detached scratch ownership changed"
        );
        detached.slot
    }

    pub(crate) fn cleanup(mut self) -> ScratchCleanup {
        #[cfg(unix)]
        {
            self.cleanup_unix()
        }
        #[cfg(windows)]
        {
            self.cleanup_windows()
        }
    }

    #[cfg(unix)]
    fn cleanup_unix(&mut self) -> ScratchCleanup {
        let directory_identity = local(self.directory.identity());
        let (removed, mut problems) = self.remove_all();
        self.prove_removals(&removed, &mut problems);
        let residues = self.collect_residues(directory_identity, problems);
        ScratchCleanup {
            attempt_id: self.attempt_id,
            directory_identity,
            creation_security_kind: CREATION_SECURITY_KIND,
            creation_security_commitment: self.profile.commitment(),
            residues,
            housekeeping: crate::publication::Housekeeping::None,
            visible_housekeeping: Vec::new(),
        }
    }

    #[cfg(windows)]
    fn cleanup_windows(&mut self) -> ScratchCleanup {
        use crate::publication::gc::{self, Authority};
        use crate::publication::{
            ArtifactKind, CreationSecurity, DirectoryRole, Housekeeping, HousekeepingArtifact,
        };

        let directory_identity = local(self.directory.identity());
        let mut residues = Vec::new();
        let mut housekeeping = Housekeeping::None;
        let mut visible_housekeeping: Vec<HousekeepingArtifact> = Vec::new();
        for owner in self.owned.iter_mut().filter_map(Option::take) {
            let retirement = gc::retire(
                &self.directory,
                Authority {
                    attempt_id: self.attempt_id,
                    ordinal: owner.ordinal,
                    kind: ArtifactKind::AuthorizedScratch,
                    directory_role: DirectoryRole::ScratchDirectory,
                    source_name: &owner.name,
                    source_file: &owner.shared.file,
                    identity: owner.identity,
                    creation_security: CreationSecurity {
                        kind: CREATION_SECURITY_KIND,
                        commitment: self.profile.commitment(),
                    },
                    payload: None,
                },
            );
            housekeeping = housekeeping.merge(retirement.housekeeping);
            visible_housekeeping.extend(retirement.visible);
            if let Some(problem) = retirement.problem {
                residues.push(cleanup::residue(
                    directory_identity,
                    &self.profile,
                    owner,
                    ScratchProblem {
                        code: problem.code,
                        os_code: problem.os_code,
                        detail: problem.detail,
                    },
                ));
            }
        }
        ScratchCleanup {
            attempt_id: self.attempt_id,
            directory_identity,
            creation_security_kind: CREATION_SECURITY_KIND,
            creation_security_commitment: self.profile.commitment(),
            residues,
            housekeeping,
            visible_housekeeping,
        }
    }

    #[cfg(unix)]
    fn remove_all(&self) -> ([bool; MAX_OWNED], [Option<ScratchProblem>; MAX_OWNED]) {
        let mut removed = [false; MAX_OWNED];
        let mut problems = std::array::from_fn(|_| None);
        for index in 0..MAX_OWNED {
            let Some(owner) = self.owned[index].as_ref() else {
                continue;
            };
            match remove(&self.directory, owner) {
                Ok(()) => removed[index] = true,
                Err(problem) => problems[index] = Some(problem),
            }
        }
        (removed, problems)
    }

    #[cfg(unix)]
    fn prove_removals(
        &self,
        removed: &[bool; MAX_OWNED],
        problems: &mut [Option<ScratchProblem>; MAX_OWNED],
    ) {
        if !removed.iter().any(|&value| value) {
            return;
        }
        if let Err(error) = self.directory.sync().and_then(|()| self.directory.verify()) {
            set_removed_problems(removed, problems, scratch_problem(&error));
            return;
        }
        for index in 0..MAX_OWNED {
            let Some(owner) = self.owned[index].as_ref() else {
                continue;
            };
            if removed[index] {
                if let Err(error) = self.directory.require_absent(&owner.name) {
                    problems[index] = Some(scratch_problem(&error));
                }
            }
        }
    }

    #[cfg(unix)]
    fn collect_residues(
        &mut self,
        directory_identity: LocalFileIdentity,
        problems: [Option<ScratchProblem>; MAX_OWNED],
    ) -> Vec<ScratchResidue> {
        let mut residues = Vec::new();
        for (index, problem) in problems.into_iter().enumerate() {
            let Some(problem) = problem else {
                continue;
            };
            let owner = self.owned[index].take().expect("problem has an owner");
            residues.push(residue(directory_identity, &self.profile, owner, problem));
        }
        residues
    }

    fn owner(&self, slot: ScratchSlot) -> &Owned {
        self.owned
            .get(slot.0)
            .and_then(Option::as_ref)
            .expect("scratch slot is owned")
    }

    fn capacity(&self, slot: ScratchSlot) -> u64 {
        self.owner(slot).shared.capacity.load(Ordering::Relaxed)
    }

    fn ensure_write_capacity(&mut self, slot: ScratchSlot, required: u64) -> Result<()> {
        let old = self.capacity(slot);
        if required <= old {
            return Ok(());
        }
        let mut available = self.available_capacity(old)?;
        if required > available {
            self.compact_mapping_slack(Some(slot.0))?;
            available = self.available_capacity(old)?;
        }
        if required > available {
            return Err(Error::BudgetExceeded("recovery scratch bytes"));
        }
        let rounded = required
            .checked_add(MAPPING_WINDOW - 1)
            .map(|value| value / MAPPING_WINDOW * MAPPING_WINDOW)
            .unwrap_or(u64::MAX);
        let capacity = rounded.min(available).max(required);
        let length = self.length(slot);
        self.owner(slot).shared.remap(capacity, length)?;
        self.retained_bytes = self.retained_bytes - old + capacity;
        Ok(())
    }

    fn available_capacity(&self, replaced: u64) -> Result<u64> {
        self.max_bytes
            .checked_sub(
                self.retained_bytes
                    .checked_sub(replaced)
                    .ok_or(Error::ArithmeticOverflow("recovery scratch bytes"))?,
            )
            .ok_or(Error::ArithmeticOverflow("recovery scratch bytes"))
    }

    fn compact_mapping_slack(&mut self, except: Option<usize>) -> Result<()> {
        for index in 0..MAX_OWNED {
            if except == Some(index) || self.owned[index].is_none() {
                continue;
            }
            let slot = ScratchSlot(index);
            let capacity = self.capacity(slot);
            let length = self.length(slot);
            if capacity != length {
                self.owner(slot).shared.remap(length, length)?;
                self.retained_bytes = self.retained_bytes - capacity + length;
            }
        }
        Ok(())
    }

    fn require_growth(&self, old: u64, new: u64) -> Result<()> {
        let total = self
            .retained_bytes
            .checked_sub(old)
            .and_then(|value| value.checked_add(new))
            .ok_or(Error::ArithmeticOverflow("recovery scratch bytes"))?;
        if total > self.max_bytes {
            return Err(Error::BudgetExceeded("recovery scratch bytes"));
        }
        Ok(())
    }
}

impl SharedFile {
    fn lock_mapping(&self) -> Result<MutexGuard<'_, Option<Mapping>>> {
        self.mapping
            .lock()
            .map_err(|_| Error::WrongState("scratch mapping lock is poisoned"))
    }

    fn read(&self, offset: u64, output: &mut [u8]) -> Result<()> {
        let mapping = self.lock_mapping()?;
        let mapping = mapping
            .as_ref()
            .ok_or(Error::WrongState("scratch mapping is unavailable"))?;
        crate::worker::probe_scratch(mapping, || {
            let bytes = mapping.bytes(offset, output.len())?;
            if bytes.copy_to(output) {
                Ok(())
            } else {
                Err(Error::Corrupt("scratch mapping changed while reading"))
            }
        })
    }

    fn write(&self, offset: u64, input: &[u8]) -> Result<()> {
        let mut mapping = self.lock_mapping()?;
        let mapping = mapping
            .as_mut()
            .ok_or(Error::WrongState("scratch mapping is unavailable"))?;
        let region = mapping.region()?;
        crate::worker::probe_scratch_region(region, || {
            mapping.bytes_mut(offset, input.len())?.write(0, input)
        })
    }

    fn remap(&self, capacity: u64, length: u64) -> Result<()> {
        if length > capacity {
            return Err(Error::InvalidArgument(
                "scratch logical length exceeds mapped capacity",
            ));
        }
        let mut mapping = self.lock_mapping()?;
        *mapping = None;
        self.file.set_len(capacity)?;
        *mapping = Some(Mapping::read_write_view(&self.file, capacity)?);
        self.length.store(length, Ordering::Relaxed);
        self.capacity.store(capacity, Ordering::Relaxed);
        Ok(())
    }
}

#[cfg(not(windows))]
fn new_attempt(_directory: &Directory) -> Result<[u8; 16]> {
    random::nonzero_128()
}

#[cfg(windows)]
fn new_attempt(directory: &Directory) -> Result<[u8; 16]> {
    loop {
        let attempt_id = random::nonzero_128()?;
        let mut collision = false;
        for ordinal in 0..MAX_OWNED as u32 {
            let source = scratch_name(attempt_id, ordinal)?;
            let envelope = crate::publication::gc_name::envelope(attempt_id, ordinal)
                .map_err(namespace_error)?;
            let inert =
                crate::publication::gc_name::inert(attempt_id, ordinal).map_err(namespace_error)?;
            for name in [&source, &envelope, &inert] {
                match directory.require_absent(name) {
                    Ok(()) => {}
                    Err(NamespaceError::Exists) => {
                        collision = true;
                        break;
                    }
                    Err(error) => return Err(namespace_error(error)),
                }
            }
            if collision {
                break;
            }
        }
        if !collision {
            return Ok(attempt_id);
        }
    }
}

pub(super) fn local(identity: Identity) -> LocalFileIdentity {
    local_identity(identity)
}

fn namespace_error(error: NamespaceError) -> Error {
    match error {
        NamespaceError::InvalidName => Error::InvalidArgument("invalid recovery scratch name"),
        NamespaceError::Exists => Error::NameExists,
        NamespaceError::ForkedHandle => Error::ForkedHandle,
        NamespaceError::Io(source) | NamespaceError::IoAt { source, .. } => Error::Io(source),
        NamespaceError::Unsupported => {
            Error::Unsupported("scratch directory lacks required local operations")
        }
        NamespaceError::NotDirectory
        | NamespaceError::NotRegular
        | NamespaceError::Missing
        | NamespaceError::IdentityChanged
        | NamespaceError::LinkCount(_)
        | NamespaceError::CrossFilesystem
        | NamespaceError::AccessPolicy => {
            Error::Unsupported("scratch directory does not meet the ownership contract")
        }
    }
}

#[cfg(all(test, unix))]
#[path = "scratch_tests.rs"]
mod tests;
