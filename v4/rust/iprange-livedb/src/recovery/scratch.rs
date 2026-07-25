//! Exact ownership and bounded I/O for authorized recovery scratch files.

use std::fs::File;
use std::os::unix::fs::MetadataExt;
use std::path::Path;

use crate::contract::MetaV4;
use crate::crc32c;
use crate::error::{Error, ErrorCode, Result};
use crate::file_io;
use crate::publication::namespace::{regular_identity, Directory, Identity, Name, NamespaceError};
use crate::publication::security::{self, Profile};
use crate::random;
use crate::validation::LocalFileIdentity;

const HEADER_SIZE: u64 = 128;
const OWNER_RECOVERY: u16 = 2;
const POSIX_IDENTITY: u16 = 1;
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
    retained_bytes: u64,
    owned: [Option<Owned>; MAX_OWNED],
}

struct Owned {
    file: File,
    name: Name,
    identity: Identity,
    ordinal: u32,
    length: u64,
}

impl Scratch {
    pub(crate) fn start(
        directory: &Path,
        source: MetaV4,
        max_bytes: u64,
        max_files: u32,
        max_open_files: u32,
    ) -> Result<Self> {
        if max_files < 2 || max_open_files < 4 {
            return Err(Error::BudgetExceeded(
                "external recovery sort requires two scratch files",
            ));
        }
        if max_bytes < 2 * HEADER_SIZE {
            return Err(Error::BudgetExceeded("recovery scratch bytes"));
        }
        let directory = Directory::open(directory).map_err(namespace_error)?;
        let profile = Profile::capture();
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
        let header = header(self.source, self.attempt_id, ordinal, self.profile);
        let owned = self.owned[slot].as_ref().expect("scratch owner installed");
        security::secure_creator_only(&owned.file, self.profile).map_err(namespace_error)?;
        file_io::write_exact_at(&owned.file, &header, 0)?;
        Ok(ScratchSlot(slot))
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
        let file = self.directory.create(&name).map_err(namespace_error)?;
        let identity =
            regular_identity(&file, self.directory.identity().device).map_err(namespace_error)?;
        self.owned[slot] = Some(Owned {
            file,
            name,
            identity,
            ordinal,
            length: HEADER_SIZE,
        });
        self.retained_bytes += HEADER_SIZE;
        Ok(())
    }

    pub(crate) fn length(&self, slot: ScratchSlot) -> u64 {
        self.owner(slot).length
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
        let old = self.owner(slot).length;
        let new = old.max(end);
        self.require_growth(old, new)?;
        self.owner_mut(slot).length = new;
        self.retained_bytes = self.retained_bytes - old + new;
        file_io::write_exact_at(&self.owner(slot).file, bytes, offset)
    }

    pub(crate) fn read(&self, slot: ScratchSlot, offset: u64, bytes: &mut [u8]) -> Result<()> {
        let end = offset
            .checked_add(bytes.len() as u64)
            .ok_or(Error::ArithmeticOverflow("recovery scratch read"))?;
        if offset < HEADER_SIZE || end > self.owner(slot).length {
            return Err(Error::Corrupt("scratch read exceeds its retained length"));
        }
        file_io::read_exact_at(&self.owner(slot).file, bytes, offset)
    }

    pub(crate) fn reset(&mut self, slot: ScratchSlot) -> Result<()> {
        let old = self.owner(slot).length;
        self.owner(slot).file.set_len(HEADER_SIZE)?;
        self.owner_mut(slot).length = HEADER_SIZE;
        self.retained_bytes = self.retained_bytes - old + HEADER_SIZE;
        Ok(())
    }

    pub(crate) fn cleanup(mut self) -> ScratchCleanup {
        let directory_identity = local(self.directory.identity());
        let (removed, mut problems) = self.remove_all();
        self.prove_removals(&removed, &mut problems);
        let residues = self.collect_residues(directory_identity, problems);
        ScratchCleanup {
            attempt_id: self.attempt_id,
            directory_identity,
            creation_security_kind: POSIX_IDENTITY,
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
            residues.push(residue(directory_identity, self.profile, owner, problem));
        }
        residues
    }

    fn owner(&self, slot: ScratchSlot) -> &Owned {
        self.owned
            .get(slot.0)
            .and_then(Option::as_ref)
            .expect("scratch slot is owned")
    }

    fn owner_mut(&mut self, slot: ScratchSlot) -> &mut Owned {
        self.owned
            .get_mut(slot.0)
            .and_then(Option::as_mut)
            .expect("scratch slot is owned")
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

fn set_removed_problems(
    removed: &[bool; MAX_OWNED],
    problems: &mut [Option<ScratchProblem>; MAX_OWNED],
    problem: ScratchProblem,
) {
    for index in 0..MAX_OWNED {
        if removed[index] {
            problems[index] = Some(problem);
        }
    }
}

fn remove(directory: &Directory, owner: &Owned) -> std::result::Result<(), ScratchProblem> {
    let metadata = owner
        .file
        .metadata()
        .map_err(|error| io_problem(&error, "inspect owned recovery scratch"))?;
    if metadata.nlink() > 1 {
        return Err(ScratchProblem {
            code: ErrorCode::CleanupConflict,
            os_code: None,
            detail: "owned recovery scratch has unexpected links",
        });
    }
    if metadata.nlink() == 0 {
        directory
            .require_absent(&owner.name)
            .map_err(|error| scratch_problem(&error))?;
        return Ok(());
    }
    let removed = directory
        .unlink_exact(&owner.name, owner.identity)
        .map_err(|error| scratch_problem(&error))?;
    if !removed {
        return Err(ScratchProblem {
            code: ErrorCode::CleanupConflict,
            os_code: None,
            detail: "owned recovery scratch lost its exact name",
        });
    }
    let links = owner
        .file
        .metadata()
        .map_err(|error| io_problem(&error, "recheck owned recovery scratch"))?
        .nlink();
    if links != 0 {
        return Err(ScratchProblem {
            code: ErrorCode::CleanupConflict,
            os_code: None,
            detail: "owned recovery scratch remained linked after removal",
        });
    }
    Ok(())
}

fn residue(
    directory_identity: LocalFileIdentity,
    profile: Profile,
    owner: Owned,
    problem: ScratchProblem,
) -> ScratchResidue {
    ScratchResidue {
        ordinal: owner.ordinal,
        directory_identity,
        basename: owner.name.bytes().into(),
        identity: local(owner.identity),
        creation_security_kind: POSIX_IDENTITY,
        creation_security_commitment: profile.commitment(),
        problem,
    }
}

fn header(source: MetaV4, attempt: [u8; 16], ordinal: u32, profile: Profile) -> [u8; 128] {
    let mut bytes = [0; 128];
    bytes[0..8].copy_from_slice(b"IPR4SCR1");
    bytes[8..10].copy_from_slice(&1u16.to_le_bytes());
    bytes[10..12].copy_from_slice(&(HEADER_SIZE as u16).to_le_bytes());
    bytes[12..14].copy_from_slice(&OWNER_RECOVERY.to_le_bytes());
    bytes[16..32].copy_from_slice(&source.database_id);
    bytes[32..40].copy_from_slice(&source.txn_id.to_le_bytes());
    bytes[40..56].copy_from_slice(&source.commit_nonce);
    bytes[56..72].copy_from_slice(&attempt);
    bytes[72..76].copy_from_slice(&ordinal.to_le_bytes());
    bytes[76..78].copy_from_slice(&POSIX_IDENTITY.to_le_bytes());
    bytes[80..112].copy_from_slice(&profile.commitment());
    let checksum = crc32c::crc32c_with_zeroed(&bytes, 124, 4).expect("fixed scratch header");
    bytes[124..128].copy_from_slice(&checksum.to_le_bytes());
    bytes
}

fn scratch_name(attempt: [u8; 16], ordinal: u32) -> Result<Name> {
    let mut bytes = [0u8; 62];
    bytes[..17].copy_from_slice(b".iprange-scratch-");
    let mut at = 17;
    for byte in attempt {
        bytes[at] = hex(byte >> 4);
        bytes[at + 1] = hex(byte & 0x0f);
        at += 2;
    }
    bytes[at] = b'-';
    at += 1;
    for shift in (0..8).rev() {
        bytes[at] = hex(((ordinal >> (shift * 4)) & 0x0f) as u8);
        at += 1;
    }
    bytes[at..].copy_from_slice(b".tmp");
    Name::new(&bytes).map_err(namespace_error)
}

fn hex(value: u8) -> u8 {
    match value {
        0..=9 => b'0' + value,
        _ => b'a' + value - 10,
    }
}

fn local(identity: Identity) -> LocalFileIdentity {
    LocalFileIdentity {
        kind: POSIX_IDENTITY,
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

fn scratch_problem(error: &NamespaceError) -> ScratchProblem {
    match error {
        NamespaceError::ForkedHandle => ScratchProblem {
            code: ErrorCode::ForkedHandle,
            os_code: None,
            detail: "scratch owner crossed fork",
        },
        NamespaceError::Io(source) | NamespaceError::IoAt { source, .. } => {
            io_problem(source, "recovery scratch cleanup failed")
        }
        _ => ScratchProblem {
            code: ErrorCode::CleanupConflict,
            os_code: None,
            detail: "recovery scratch ownership changed",
        },
    }
}

fn io_problem(error: &std::io::Error, detail: &'static str) -> ScratchProblem {
    ScratchProblem {
        code: ErrorCode::Io,
        os_code: error.raw_os_error(),
        detail,
    }
}

#[cfg(test)]
#[path = "scratch_tests.rs"]
mod tests;
