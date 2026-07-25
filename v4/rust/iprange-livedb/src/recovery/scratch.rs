//! Exact ownership and bounded I/O for authorized recovery scratch files.

use std::fs::File;
use std::path::Path;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;

use crate::contract::MetaV4;
use crate::error::{Error, ErrorCode, Result};
use crate::file_io;
use crate::publication::namespace::{
    regular_identity, Directory, Identity, Name, NamespaceError, CREATION_SECURITY_KIND,
    IDENTITY_KIND,
};
use crate::publication::security::{self, Profile};
use crate::random;
use crate::validation::LocalFileIdentity;

#[path = "scratch/cleanup.rs"]
mod cleanup;
pub(crate) use cleanup::residue_error;
use cleanup::{remove, residue, scratch_problem, set_removed_problems};
#[path = "scratch/fixed.rs"]
mod fixed;
pub(crate) use fixed::ScratchFile;
#[path = "scratch/format.rs"]
pub(super) mod format;
#[cfg(test)]
use format::hex;
pub(crate) use format::HEADER_SIZE;
use format::{header, scratch_name};

const MAX_OWNED: usize = 2;

/// One retained scratch slot.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct ScratchSlot(usize);

/// Exact cleanup problem retained for one possible scratch residue.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct ScratchProblem {
    pub(crate) code: ErrorCode,
    pub(crate) os_code: Option<i32>,
    pub(crate) detail: &'static str,
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

/// Terminal facts from one scratch attempt.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct ScratchCleanup {
    pub(crate) attempt_id: [u8; 16],
    pub(crate) directory_identity: LocalFileIdentity,
    pub(crate) creation_security_kind: u16,
    pub(crate) creation_security_commitment: [u8; 32],
    pub(crate) residues: Vec<ScratchResidue>,
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
    attempt_id: [u8; 16],
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
    length: AtomicU64,
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
        let attempt_id = random::nonzero_128()?;
        Ok(Self {
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
        })
    }

    pub(crate) fn attempt_id(&self) -> [u8; 16] {
        self.attempt_id
    }

    pub(crate) fn create(&mut self) -> Result<ScratchSlot> {
        let slot = self.free_slot()?;
        let ordinal = self.take_ordinal()?;
        self.install(slot, ordinal)?;
        let header = header(self.source, self.attempt_id, ordinal, &self.profile);
        let owned = self.owned[slot].as_ref().expect("scratch owner installed");
        let file = &owned.shared.file;
        security::secure_creator_only(file, &self.profile).map_err(namespace_error)?;
        file_io::write_exact_at(file, &header, 0)?;
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
                length: AtomicU64::new(HEADER_SIZE),
            }),
            name,
            identity,
            ordinal,
        });
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
        self.require_growth(old, new)?;
        self.owner(slot).shared.length.store(new, Ordering::Relaxed);
        self.retained_bytes = self.retained_bytes - old + new;
        file_io::write_exact_at(self.file(slot), bytes, offset)
    }

    pub(crate) fn read(&self, slot: ScratchSlot, offset: u64, bytes: &mut [u8]) -> Result<()> {
        let end = offset
            .checked_add(bytes.len() as u64)
            .ok_or(Error::ArithmeticOverflow("recovery scratch read"))?;
        if offset < HEADER_SIZE || end > self.length(slot) {
            return Err(Error::Corrupt("scratch read exceeds its retained length"));
        }
        file_io::read_exact_at(self.file(slot), bytes, offset)
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
        let old = self.length(slot);
        self.require_growth(old, length)?;
        self.file(slot).set_len(length)?;
        self.owner(slot)
            .shared
            .length
            .store(length, Ordering::Relaxed);
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
        }
    }

    fn remove_all(&self) -> ([bool; MAX_OWNED], [Option<ScratchProblem>; MAX_OWNED]) {
        let mut removed = [false; MAX_OWNED];
        let mut problems = [None; MAX_OWNED];
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

    fn file(&self, slot: ScratchSlot) -> &File {
        &self.owner(slot).shared.file
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

pub(super) fn local(identity: Identity) -> LocalFileIdentity {
    LocalFileIdentity {
        kind: IDENTITY_KIND,
        bytes: identity.encode(),
    }
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

#[cfg(test)]
#[path = "scratch_tests.rs"]
mod tests;
