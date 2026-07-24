//! Linux retained-directory, identity, lock, and process primitives.

use crate::bootstrap::{open_meta_pages, Bootstrap, BootstrapError, MetaSelection, OpenMode};
use crate::contract::{MetaV4, MAX_PAGE_COUNT, PAGE_SIZE};
use crate::name_binding::{basename_commitment, BasenameBindingError, BasenameEncoding};
use crate::page_source::{PageIoEvidence, PageSourceError, PinnedPageSource, PositionalRead};
use crate::process_identity::{
    classify_posix_death, parse_linux_proc_stat_start, PosixProcessObservation,
};
use crate::sidecar::{
    decode_stable_slot, select_sidecar_header, ActiveSlot, LocalIdentityKind, ProcessDomainKind,
    ReadySidecarInspection, SidecarError, SidecarHeader, SidecarState, SlotHostLimits, SlotProblem,
    SlotRole, StableSlot, SLOT_SIZE,
};
use crate::sidecar_transition::{
    ArmedSlotTransition, CleanupDisposition, DeathProof, InterruptedCause, PreparedSlotTransition,
    SlotCleanupError, SlotTransitionError, SlotTransitionKind, SlotTransitionSource,
};
use std::ffi::{CStr, CString, OsStr, OsString};
use std::fs::{File, Metadata, OpenOptions};
use std::io::{self, Read};
use std::os::fd::{AsRawFd, FromRawFd};
use std::os::unix::ffi::{OsStrExt, OsStringExt};
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::path::Path;
#[cfg(test)]
use std::path::PathBuf;

mod live_cleanup;
pub(crate) mod live_reader;
pub(crate) mod live_writer;

#[derive(Debug)]
pub(crate) enum LinuxOsError {
    Io {
        operation: &'static str,
        source: io::Error,
    },
    InvalidPathComponent,
    NotDirectory,
    NotRegular,
    UnsupportedFilesystem(u32),
    CrossFilesystem,
    LinkCountNotOne(u64),
    PathIdentityMismatch,
    ForkedHandle,
    LockAlreadyHeld,
    LockNotHeld,
    LockBusy,
    Cancelled,
    OffsetOverflow,
    RandomFailure,
    RandomZero,
    OperationLockRequired,
    LifetimeLockRequired,
    ArmedTransition,
    PendingWriterCleanup,
    WriterClearRequiresMainTail,
    Bootstrap(BootstrapError),
    Sidecar(SidecarError),
    SidecarHeaderChanged,
    SidecarIdentityMismatch,
    SidecarDatabaseMismatch,
    SidecarMainIdentityMismatch,
    SidecarBasenameMismatch,
    SidecarProcessDomainMismatch,
    BasenameBinding(BasenameBindingError),
    SidecarSizeMismatch {
        expected: u64,
        actual: u64,
    },
    SlotOffsetOverflow,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PosixIdentity {
    pub(crate) device: u64,
    pub(crate) inode: u64,
}

impl PosixIdentity {
    pub(crate) fn encode(self) -> [u8; 32] {
        let mut bytes = [0u8; 32];
        bytes[..8].copy_from_slice(&self.device.to_le_bytes());
        bytes[8..16].copy_from_slice(&self.inode.to_le_bytes());
        bytes
    }
}

#[derive(Debug)]
pub(crate) struct RetainedDirectory {
    file: File,
    identity: PosixIdentity,
    creator_pid: u32,
}

impl RetainedDirectory {
    pub(crate) fn open_parent(path: &Path) -> Result<(Self, OsString), LinuxOsError> {
        let component = path.file_name().ok_or(LinuxOsError::InvalidPathComponent)?;
        validate_main_component(component)?;
        let parent = path.parent().unwrap_or_else(|| Path::new("."));
        let parent = if parent.as_os_str().is_empty() {
            Path::new(".")
        } else {
            parent
        };
        let file = OpenOptions::new()
            .read(true)
            .custom_flags(libc::O_DIRECTORY | libc::O_CLOEXEC)
            .open(parent)
            .map_err(|source| LinuxOsError::Io {
                operation: "open parent directory",
                source,
            })?;
        let metadata = file.metadata().map_err(|source| LinuxOsError::Io {
            operation: "inspect parent directory",
            source,
        })?;
        if !metadata.is_dir() {
            return Err(LinuxOsError::NotDirectory);
        }
        require_supported_local_filesystem(&file)?;
        let name_max = unsafe { libc::fpathconf(file.as_raw_fd(), libc::_PC_NAME_MAX) };
        let main_and_sidecar = component
            .as_bytes()
            .len()
            .checked_add(8)
            .ok_or(LinuxOsError::OffsetOverflow)?;
        if name_max >= 0
            && usize::try_from(name_max)
                .map(|limit| main_and_sidecar > limit)
                .unwrap_or(true)
        {
            return Err(LinuxOsError::InvalidPathComponent);
        }
        let mut owned_component = Vec::new();
        owned_component
            .try_reserve_exact(component.as_bytes().len())
            .map_err(|_| LinuxOsError::OffsetOverflow)?;
        owned_component.extend_from_slice(component.as_bytes());
        Ok((
            Self {
                identity: metadata_identity(&metadata),
                file,
                creator_pid: std::process::id(),
            },
            OsString::from_vec(owned_component),
        ))
    }

    pub(crate) const fn identity(&self) -> PosixIdentity {
        self.identity
    }

    pub(crate) fn sidecar_component(
        &self,
        main_component: &OsStr,
    ) -> Result<OsString, LinuxOsError> {
        self.check_creator()?;
        validate_main_component(main_component)?;
        let name_max = unsafe { libc::fpathconf(self.file.as_raw_fd(), libc::_PC_NAME_MAX) };
        let component_len = main_component
            .as_bytes()
            .len()
            .checked_add(8)
            .ok_or(LinuxOsError::OffsetOverflow)?;
        if name_max >= 0
            && usize::try_from(name_max)
                .map(|limit| component_len > limit)
                .unwrap_or(true)
        {
            return Err(LinuxOsError::InvalidPathComponent);
        }
        let mut bytes = Vec::new();
        bytes
            .try_reserve_exact(component_len)
            .map_err(|_| LinuxOsError::OffsetOverflow)?;
        bytes.extend_from_slice(main_component.as_bytes());
        bytes.extend_from_slice(b".readers");
        let component = OsString::from_vec(bytes);
        validate_component(&component)?;
        Ok(component)
    }

    pub(crate) fn open_regular(
        &self,
        component: &OsStr,
        writable: bool,
    ) -> Result<RetainedRegular, LinuxOsError> {
        self.check_creator()?;
        let component = component_c_string(component)?;
        let access = if writable {
            libc::O_RDWR
        } else {
            libc::O_RDONLY
        };
        let fd = unsafe {
            libc::openat(
                self.file.as_raw_fd(),
                component.as_ptr(),
                access | libc::O_NOFOLLOW | libc::O_CLOEXEC | libc::O_NONBLOCK,
            )
        };
        if fd < 0 {
            return Err(LinuxOsError::Io {
                operation: "open retained regular file",
                source: io::Error::last_os_error(),
            });
        }
        let file = unsafe { File::from_raw_fd(fd) };
        let metadata = file.metadata().map_err(|source| LinuxOsError::Io {
            operation: "inspect retained regular file",
            source,
        })?;
        validate_regular(&metadata)?;
        if metadata.dev() != self.identity.device {
            return Err(LinuxOsError::CrossFilesystem);
        }
        require_supported_local_filesystem(&file)?;
        Ok(RetainedRegular {
            identity: metadata_identity(&metadata),
            length: metadata.len(),
            file,
            creator_pid: self.creator_pid,
            lock: LockState::Unlocked,
            cleanup_authority: SidecarCleanupAuthority::None,
        })
    }

    pub(crate) fn verify_path(
        &self,
        component: &OsStr,
        retained: &RetainedRegular,
    ) -> Result<(), LinuxOsError> {
        let component = component_c_string(component)?;
        self.verify_path_cstr(&component, retained)
    }

    fn verify_path_cstr(
        &self,
        component: &CStr,
        retained: &RetainedRegular,
    ) -> Result<(), LinuxOsError> {
        self.check_creator()?;
        retained.check_creator()?;
        let current = retained
            .file
            .metadata()
            .map_err(|source| LinuxOsError::Io {
                operation: "recheck retained regular file",
                source,
            })?;
        validate_regular(&current)?;
        if metadata_identity(&current) != retained.identity {
            return Err(LinuxOsError::PathIdentityMismatch);
        }

        let mut stat = std::mem::MaybeUninit::<libc::stat>::uninit();
        let status = unsafe {
            libc::fstatat(
                self.file.as_raw_fd(),
                component.as_ptr(),
                stat.as_mut_ptr(),
                libc::AT_SYMLINK_NOFOLLOW,
            )
        };
        if status != 0 {
            return Err(LinuxOsError::Io {
                operation: "recheck canonical path",
                source: io::Error::last_os_error(),
            });
        }
        let stat = unsafe { stat.assume_init() };
        if stat.st_mode & libc::S_IFMT != libc::S_IFREG {
            return Err(LinuxOsError::NotRegular);
        }
        let links = stat.st_nlink;
        if links != 1 {
            return Err(LinuxOsError::LinkCountNotOne(unsigned_u64(links)));
        }
        let path_identity = PosixIdentity {
            device: stat.st_dev,
            inode: stat.st_ino,
        };
        if path_identity != retained.identity {
            return Err(LinuxOsError::PathIdentityMismatch);
        }
        Ok(())
    }

    pub(crate) fn verify_live_pair_binding(
        &self,
        main_component: &OsStr,
        main: &RetainedRegular,
        sidecar_component: &OsStr,
        sidecar: &RetainedRegular,
        bootstrap: Bootstrap,
        header: SidecarHeader,
    ) -> Result<(), LinuxOsError> {
        self.check_creator()?;
        if main.lock != LockState::Shared {
            return Err(LinuxOsError::LifetimeLockRequired);
        }
        sidecar.require_exclusive_lock()?;
        self.verify_path(main_component, main)?;
        self.verify_path(sidecar_component, sidecar)?;
        self.verify_live_pair_descriptor_binding(main_component, main, sidecar, bootstrap, header)
    }

    fn verify_live_pair_descriptor_binding(
        &self,
        main_component: &OsStr,
        main: &RetainedRegular,
        sidecar: &RetainedRegular,
        bootstrap: Bootstrap,
        header: SidecarHeader,
    ) -> Result<(), LinuxOsError> {
        self.check_creator()?;
        if main.lock != LockState::Shared {
            return Err(LinuxOsError::LifetimeLockRequired);
        }
        sidecar.require_exclusive_lock()?;
        if header.database_id != bootstrap.meta.database_id {
            return Err(LinuxOsError::SidecarDatabaseMismatch);
        }
        if header.main_identity != main.identity.encode() {
            return Err(LinuxOsError::SidecarMainIdentityMismatch);
        }
        let name = main_component.as_bytes();
        let name_len = u32::try_from(name.len()).map_err(|_| LinuxOsError::OffsetOverflow)?;
        let commitment = basename_commitment(BasenameEncoding::PosixBytes, name)
            .map_err(LinuxOsError::BasenameBinding)?;
        if header.basename_encoding != BasenameEncoding::PosixBytes as u16
            || header.basename_len != name_len
            || header.basename_commitment != commitment
        {
            return Err(LinuxOsError::SidecarBasenameMismatch);
        }
        let domain = linux_process_domain_token()?;
        if header.process_domain_kind != ProcessDomainKind::LinuxPidNamespace
            || header.process_domain_token != domain
        {
            return Err(LinuxOsError::SidecarProcessDomainMismatch);
        }
        Ok(())
    }

    fn check_creator(&self) -> Result<(), LinuxOsError> {
        if std::process::id() != self.creator_pid {
            return Err(LinuxOsError::ForkedHandle);
        }
        Ok(())
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum LockMode {
    Shared,
    Exclusive,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum LockState {
    Unlocked,
    Shared,
    Exclusive,
}

#[derive(Debug)]
pub(crate) struct RetainedRegular {
    file: File,
    identity: PosixIdentity,
    length: u64,
    creator_pid: u32,
    lock: LockState,
    cleanup_authority: SidecarCleanupAuthority,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct ProvenDeadWriter {
    pub(crate) header: SidecarHeader,
    pub(crate) source_image: [u8; SLOT_SIZE as usize],
    pub(crate) active: ActiveSlot,
    pub(crate) proof: DeathProof,
    pub(crate) bootstrap: Option<Bootstrap>,
    pub(crate) tail: Option<UnpublishedMainTail>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct UnpublishedMainTail {
    pub(crate) main_identity: PosixIdentity,
    pub(crate) database_id: [u8; 16],
    pub(crate) transaction_id: u64,
    pub(crate) commit_nonce: [u8; 16],
    pub(crate) committed_length: u64,
    pub(crate) observed_end_exclusive: u64,
}

// Dead-writer origin must stay allocation-free once a clear is armed.
#[allow(clippy::large_enum_variant)]
#[derive(Debug, PartialEq, Eq)]
enum SidecarCleanupAuthority {
    None,
    Armed {
        transition: ArmedSlotTransition,
        dead_writer: Option<ProvenDeadWriter>,
    },
    DeadWriter(ProvenDeadWriter),
}

#[derive(Debug)]
pub(crate) struct RetainedLiveFiles {
    directory: RetainedDirectory,
    main_component: OsString,
    main_component_c: CString,
    sidecar_component: OsString,
    sidecar_component_c: CString,
    main: RetainedRegular,
    sidecar: RetainedRegular,
    header: SidecarHeader,
    last_scan: Option<(Bootstrap, ReadySidecarInspection)>,
    writer_bootstrap: Option<Bootstrap>,
    writer_tail: Option<UnpublishedMainTail>,
    writer_publication: Option<WriterCommitPublication>,
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) struct OwnedReaderSlot {
    header: SidecarHeader,
    index: u32,
    active: ActiveSlot,
}

#[derive(Debug, PartialEq, Eq)]
pub(crate) struct OwnedWriterLease {
    header: SidecarHeader,
    active: ActiveSlot,
    claimed_bootstrap: Bootstrap,
}

/// One exact source-to-target publication attempt retained by a live writer.
///
/// This remains in memory until the writer has either proved pre-publication
/// cleanup, transferred its lease to the target generation, or explicitly
/// closed an indeterminate attempt. It prevents a target-selected Close from
/// treating the source generation's growth as disposable.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
struct WriterCommitPublication {
    source: Bootstrap,
    source_active: ActiveSlot,
    target: MetaV4,
    phase: WriterCommitPublicationPhase,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum WriterCommitPublicationPhase {
    PreMeta,
    MetaWriteStarted,
    MetaSynchronized { target: Bootstrap },
}

impl WriterCommitPublication {
    fn target_active(self) -> ActiveSlot {
        ActiveSlot {
            txn_id: self.target.txn_id,
            ..self.source_active
        }
    }

    fn target_committed_bytes(self) -> Result<u64, LinuxWriterLeaseError> {
        self.target
            .page_count
            .checked_mul(PAGE_SIZE as u64)
            .ok_or(LinuxWriterLeaseError::CommitAttemptMismatch)
    }
}

#[derive(Debug)]
pub(crate) struct LiveClaimCleanupOutcome {
    pub(crate) main_path: Result<(), LinuxOsError>,
    pub(crate) sidecar_path: Result<(), LinuxOsError>,
}

impl RetainedRegular {
    pub(crate) const fn identity(&self) -> PosixIdentity {
        self.identity
    }

    pub(crate) const fn length(&self) -> u64 {
        self.length
    }

    pub(crate) const fn lock_held(&self) -> bool {
        !matches!(self.lock, LockState::Unlocked)
    }

    pub(crate) const fn has_armed_transition(&self) -> bool {
        matches!(
            self.cleanup_authority,
            SidecarCleanupAuthority::Armed { .. }
        )
    }

    pub(crate) fn has_armed_writer_transition(&self) -> bool {
        matches!(
            &self.cleanup_authority,
            SidecarCleanupAuthority::Armed { transition, .. }
                if transition.role() == SlotRole::Writer && transition.slot_index() == 0
        )
    }

    pub(crate) fn has_dead_writer_cleanup(&self) -> bool {
        match self.cleanup_authority {
            SidecarCleanupAuthority::DeadWriter(_) => true,
            SidecarCleanupAuthority::Armed {
                ref transition,
                dead_writer: Some(dead),
            } => armed_transition_is_dead_writer_clear(transition, dead),
            SidecarCleanupAuthority::None
            | SidecarCleanupAuthority::Armed {
                dead_writer: None, ..
            } => false,
        }
    }

    pub(crate) fn acquire_lock(
        &mut self,
        mode: LockMode,
        nonblocking: bool,
    ) -> Result<(), LinuxOsError> {
        self.check_creator()?;
        if self.lock_held() {
            return Err(LinuxOsError::LockAlreadyHeld);
        }
        let mut operation = match mode {
            LockMode::Shared => libc::LOCK_SH,
            LockMode::Exclusive => libc::LOCK_EX,
        };
        if nonblocking {
            operation |= libc::LOCK_NB;
        }
        loop {
            if unsafe { libc::flock(self.file.as_raw_fd(), operation) } == 0 {
                self.lock = match mode {
                    LockMode::Shared => LockState::Shared,
                    LockMode::Exclusive => LockState::Exclusive,
                };
                return Ok(());
            }
            let source = io::Error::last_os_error();
            if source.raw_os_error() == Some(libc::EINTR) {
                continue;
            }
            if nonblocking && source.raw_os_error() == Some(libc::EWOULDBLOCK) {
                return Err(LinuxOsError::LockBusy);
            }
            return Err(LinuxOsError::Io {
                operation: "acquire flock",
                source,
            });
        }
    }

    pub(crate) fn acquire_lock_interruptible(
        &mut self,
        mode: LockMode,
        mut cancelled: impl FnMut() -> bool,
    ) -> Result<(), LinuxOsError> {
        self.check_creator()?;
        if self.lock_held() {
            return Err(LinuxOsError::LockAlreadyHeld);
        }
        let operation = match mode {
            LockMode::Shared => libc::LOCK_SH,
            LockMode::Exclusive => libc::LOCK_EX,
        } | libc::LOCK_NB;
        loop {
            if cancelled() {
                return Err(LinuxOsError::Cancelled);
            }
            if unsafe { libc::flock(self.file.as_raw_fd(), operation) } == 0 {
                self.lock = match mode {
                    LockMode::Shared => LockState::Shared,
                    LockMode::Exclusive => LockState::Exclusive,
                };
                return Ok(());
            }
            let source = io::Error::last_os_error();
            if source.raw_os_error() == Some(libc::EINTR) {
                continue;
            }
            if source.raw_os_error() != Some(libc::EWOULDBLOCK) {
                return Err(LinuxOsError::Io {
                    operation: "acquire interruptible flock",
                    source,
                });
            }
            if cancelled() {
                return Err(LinuxOsError::Cancelled);
            }
            std::thread::sleep(std::time::Duration::from_millis(1));
        }
    }

    pub(crate) fn release_lock(&mut self) -> Result<(), LinuxOsError> {
        self.check_creator()?;
        if !self.lock_held() {
            return Err(LinuxOsError::LockNotHeld);
        }
        match self.cleanup_authority {
            SidecarCleanupAuthority::None => {}
            SidecarCleanupAuthority::Armed { .. } => {
                return Err(LinuxOsError::ArmedTransition);
            }
            SidecarCleanupAuthority::DeadWriter(_) => {
                return Err(LinuxOsError::PendingWriterCleanup);
            }
        }
        loop {
            if unsafe { libc::flock(self.file.as_raw_fd(), libc::LOCK_UN) } == 0 {
                self.lock = LockState::Unlocked;
                return Ok(());
            }
            let source = io::Error::last_os_error();
            if source.raw_os_error() == Some(libc::EINTR) {
                continue;
            }
            return Err(LinuxOsError::Io {
                operation: "release flock",
                source,
            });
        }
    }

    pub(crate) fn read_exact_at(
        &self,
        mut bytes: &mut [u8],
        mut offset: u64,
    ) -> Result<(), LinuxOsError> {
        self.check_creator()?;
        use std::os::unix::fs::FileExt;
        while !bytes.is_empty() {
            let read = self
                .file
                .read_at(bytes, offset)
                .map_err(|source| LinuxOsError::Io {
                    operation: "positional read",
                    source,
                })?;
            if read == 0 {
                return Err(LinuxOsError::Io {
                    operation: "positional read",
                    source: io::Error::from(io::ErrorKind::UnexpectedEof),
                });
            }
            offset = offset
                .checked_add(u64::try_from(read).map_err(|_| LinuxOsError::OffsetOverflow)?)
                .ok_or(LinuxOsError::OffsetOverflow)?;
            bytes = &mut bytes[read..];
        }
        Ok(())
    }

    pub(crate) fn pinned_page_source(
        &self,
        bootstrap: Bootstrap,
    ) -> Result<PinnedPageSource<'_, Self>, PageSourceError> {
        PinnedPageSource::new(self, bootstrap)
    }

    pub(crate) fn write_all_at(
        &self,
        mut bytes: &[u8],
        mut offset: u64,
    ) -> Result<(), LinuxOsError> {
        self.check_creator()?;
        use std::os::unix::fs::FileExt;
        while !bytes.is_empty() {
            let written = self
                .file
                .write_at(bytes, offset)
                .map_err(|source| LinuxOsError::Io {
                    operation: "positional write",
                    source,
                })?;
            if written == 0 {
                return Err(LinuxOsError::Io {
                    operation: "positional write",
                    source: io::Error::from(io::ErrorKind::WriteZero),
                });
            }
            offset = offset
                .checked_add(u64::try_from(written).map_err(|_| LinuxOsError::OffsetOverflow)?)
                .ok_or(LinuxOsError::OffsetOverflow)?;
            bytes = &bytes[written..];
        }
        Ok(())
    }

    pub(crate) fn read_main_bootstrap(&self, mode: OpenMode) -> Result<Bootstrap, LinuxOsError> {
        self.check_creator()?;
        let metadata = self.file.metadata().map_err(|source| LinuxOsError::Io {
            operation: "inspect retained main file",
            source,
        })?;
        validate_regular(&metadata)?;
        if metadata_identity(&metadata) != self.identity {
            return Err(LinuxOsError::PathIdentityMismatch);
        }
        let mut pages = [0u8; 2 * PAGE_SIZE];
        self.read_exact_at(&mut pages, 0)?;
        let page0: &[u8; PAGE_SIZE] = pages[..PAGE_SIZE].try_into().unwrap();
        let page1: &[u8; PAGE_SIZE] = pages[PAGE_SIZE..].try_into().unwrap();
        open_meta_pages(page0, page1, metadata.len(), mode).map_err(LinuxOsError::Bootstrap)
    }

    pub(crate) fn read_ready_sidecar_header(
        &self,
        expected: Option<SidecarHeader>,
    ) -> Result<SidecarHeader, LinuxOsError> {
        self.require_exclusive_lock()?;
        let metadata = self.file.metadata().map_err(|source| LinuxOsError::Io {
            operation: "inspect retained sidecar",
            source,
        })?;
        validate_regular(&metadata)?;
        if metadata_identity(&metadata) != self.identity {
            return Err(LinuxOsError::SidecarIdentityMismatch);
        }
        let mut bytes = [0u8; 2 * PAGE_SIZE];
        self.read_exact_at(&mut bytes, 0)?;
        let header = select_sidecar_header(&bytes).map_err(LinuxOsError::Sidecar)?;
        if header.state != SidecarState::Ready {
            return Err(LinuxOsError::Sidecar(SidecarError::NotReady(header.state)));
        }
        let expected_size = header
            .exact_file_size()
            .ok_or(LinuxOsError::SlotOffsetOverflow)?;
        let actual_size = metadata.len();
        if actual_size != expected_size {
            return Err(LinuxOsError::SidecarSizeMismatch {
                expected: expected_size,
                actual: actual_size,
            });
        }
        if header.identity_kind != LocalIdentityKind::Posix
            || header.sidecar_identity != self.identity.encode()
        {
            return Err(LinuxOsError::SidecarIdentityMismatch);
        }
        if expected.is_some_and(|expected| expected != header) {
            return Err(LinuxOsError::SidecarHeaderChanged);
        }
        Ok(header)
    }

    pub(crate) fn read_sidecar_slot(
        &self,
        expected_header: SidecarHeader,
        index: u32,
    ) -> Result<[u8; SLOT_SIZE as usize], LinuxOsError> {
        let header = self.read_ready_sidecar_header(Some(expected_header))?;
        self.read_sidecar_slot_after_header(header, index)
    }

    fn read_sidecar_slot_after_header(
        &self,
        header: SidecarHeader,
        index: u32,
    ) -> Result<[u8; SLOT_SIZE as usize], LinuxOsError> {
        self.require_exclusive_lock()?;
        let offset = sidecar_slot_offset(header, index)?;
        let mut slot = [0u8; SLOT_SIZE as usize];
        self.read_exact_at(&mut slot, offset)?;
        Ok(slot)
    }

    pub(crate) fn scan_and_reap_ready_sidecar(
        &mut self,
        expected_header: SidecarHeader,
        selected_txn: u64,
    ) -> Result<ReadySidecarInspection, LinuxSidecarScanError> {
        self.scan_and_reap_ready_sidecar_with(expected_header, selected_txn, observe_posix_process)
    }

    fn scan_and_reap_ready_sidecar_with(
        &mut self,
        expected_header: SidecarHeader,
        selected_txn: u64,
        mut observe: impl FnMut(ActiveSlot) -> PosixProcessObservation,
    ) -> Result<ReadySidecarInspection, LinuxSidecarScanError> {
        self.scan_and_reap_ready_sidecar_with_cancel(
            expected_header,
            selected_txn,
            &mut observe,
            || false,
        )
    }

    fn scan_and_reap_ready_sidecar_with_cancel(
        &mut self,
        expected_header: SidecarHeader,
        selected_txn: u64,
        mut observe: impl FnMut(ActiveSlot) -> PosixProcessObservation,
        mut cancelled: impl FnMut() -> bool,
    ) -> Result<ReadySidecarInspection, LinuxSidecarScanError> {
        self.require_exclusive_lock()
            .map_err(LinuxSidecarScanError::Os)?;
        if selected_txn == 0 {
            return Err(LinuxSidecarScanError::SelectedTransactionZero);
        }
        match self.cleanup_authority {
            SidecarCleanupAuthority::None => {}
            SidecarCleanupAuthority::Armed { .. } => {
                return Err(LinuxSidecarScanError::OutstandingProvenance);
            }
            SidecarCleanupAuthority::DeadWriter(_) => {
                return Err(LinuxSidecarScanError::OutstandingWriterCleanup);
            }
        }

        let header = self
            .read_ready_sidecar_header(Some(expected_header))
            .map_err(LinuxSidecarScanError::Os)?;
        let current_domain = linux_process_domain_token().map_err(LinuxSidecarScanError::Os)?;
        if header.process_domain_kind != ProcessDomainKind::LinuxPidNamespace
            || header.process_domain_token != current_domain
        {
            return Err(LinuxSidecarScanError::ProcessDomainMismatch);
        }

        // Pass 1 is deliberately read-only. A malformed later slot must be
        // reported before an earlier dead reader can be changed.
        for index in 0..=header.capacity {
            if cancelled() {
                return Err(LinuxSidecarScanError::Cancelled);
            }
            self.decode_sidecar_slot_after_header(header, index)?;
        }
        self.read_ready_sidecar_header(Some(header))
            .map_err(LinuxSidecarScanError::Os)?;

        // Pass 2 establishes liveness from each exact valid image. Dead
        // writers retain their lease until their main-tail cleanup completes.
        for index in 0..=header.capacity {
            if cancelled() {
                return Err(LinuxSidecarScanError::Cancelled);
            }
            let (role, raw, stable) = self.decode_sidecar_slot_after_header(header, index)?;
            let StableSlot::Active(active) = stable else {
                continue;
            };
            let Some(proof) = classify_posix_death(active, observe(active)) else {
                continue;
            };
            if cancelled() {
                return Err(LinuxSidecarScanError::Cancelled);
            }
            if role == SlotRole::Writer {
                self.cleanup_authority = SidecarCleanupAuthority::DeadWriter(ProvenDeadWriter {
                    header,
                    source_image: raw,
                    active,
                    proof,
                    bootstrap: None,
                    tail: None,
                });
                return Err(LinuxSidecarScanError::DeadWriter { active, proof });
            }
            let prepared = PreparedSlotTransition::clear_proven_dead(
                header,
                role,
                index,
                &raw,
                active,
                proof,
                linux_slot_host_limits(),
            )
            .map_err(|cause| LinuxSidecarScanError::TransitionBeforeArm { index, cause })?;
            self.execute_sidecar_slot_transition(prepared, linux_slot_host_limits())
                .map_err(|cause| LinuxSidecarScanError::Transition { index, cause })?;
        }
        self.read_ready_sidecar_header(Some(header))
            .map_err(LinuxSidecarScanError::Os)?;

        // Pass 3 checks transactions only after every proven-dead reader has
        // been removed, then summarizes the surviving stable table.
        let mut writer = None;
        let mut active_readers = 0u32;
        let mut registering_readers = 0u32;
        let mut oldest_reader_txn: Option<u64> = None;
        let mut lowest_free_reader_slot = None;
        for index in 0..=header.capacity {
            if cancelled() {
                return Err(LinuxSidecarScanError::Cancelled);
            }
            let (role, _, stable) = self.decode_sidecar_slot_after_header(header, index)?;
            let StableSlot::Active(active) = stable else {
                if role == SlotRole::Reader && lowest_free_reader_slot.is_none() {
                    lowest_free_reader_slot = Some(index);
                }
                continue;
            };
            match role {
                SlotRole::Writer => {
                    if active.txn_id != selected_txn {
                        return Err(LinuxSidecarScanError::WriterTransactionMismatch {
                            expected: selected_txn,
                            actual: active.txn_id,
                        });
                    }
                    writer = Some(active);
                }
                SlotRole::Reader => {
                    active_readers = active_readers
                        .checked_add(1)
                        .ok_or(LinuxSidecarScanError::CounterOverflow)?;
                    if active.txn_id == 0 {
                        registering_readers = registering_readers
                            .checked_add(1)
                            .ok_or(LinuxSidecarScanError::CounterOverflow)?;
                    } else {
                        if active.txn_id > selected_txn {
                            return Err(LinuxSidecarScanError::ReaderTransactionFuture {
                                selected: selected_txn,
                                actual: active.txn_id,
                            });
                        }
                        oldest_reader_txn = Some(match oldest_reader_txn {
                            Some(oldest) => oldest.min(active.txn_id),
                            None => active.txn_id,
                        });
                    }
                }
            }
        }
        let final_header = self
            .read_ready_sidecar_header(Some(header))
            .map_err(LinuxSidecarScanError::Os)?;
        Ok(ReadySidecarInspection {
            header: final_header,
            writer,
            active_readers,
            registering_readers,
            oldest_reader_txn,
            lowest_free_reader_slot,
        })
    }

    fn decode_sidecar_slot_after_header(
        &self,
        header: SidecarHeader,
        index: u32,
    ) -> Result<(SlotRole, [u8; SLOT_SIZE as usize], StableSlot), LinuxSidecarScanError> {
        let role = slot_role(index);
        let raw = self
            .read_sidecar_slot_after_header(header, index)
            .map_err(LinuxSidecarScanError::Os)?;
        let stable = decode_stable_slot(&raw, role, linux_slot_host_limits())
            .map_err(|problem| LinuxSidecarScanError::Slot { index, problem })?;
        Ok((role, raw, stable))
    }

    pub(crate) fn execute_sidecar_slot_transition(
        &mut self,
        prepared: PreparedSlotTransition,
        host: SlotHostLimits,
    ) -> Result<(), LockedSlotExecutionError> {
        if prepared.role() == SlotRole::Writer && prepared.kind() == SlotTransitionKind::Clear {
            return Err(LockedSlotExecutionError::BeforeArm(
                LinuxOsError::WriterClearRequiresMainTail,
            ));
        }
        self.execute_sidecar_slot_transition_after_tail(prepared, host)
    }

    fn execute_sidecar_slot_transition_after_tail(
        &mut self,
        prepared: PreparedSlotTransition,
        host: SlotHostLimits,
    ) -> Result<(), LockedSlotExecutionError> {
        match self.cleanup_authority {
            SidecarCleanupAuthority::None => {}
            SidecarCleanupAuthority::Armed { .. } => {
                return Err(LockedSlotExecutionError::TransitionBeforeArm(
                    SlotTransitionError::ProvenanceOccupied,
                ));
            }
            SidecarCleanupAuthority::DeadWriter(_) => {
                return Err(LockedSlotExecutionError::BeforeArm(
                    LinuxOsError::PendingWriterCleanup,
                ));
            }
        }
        let header = self
            .read_ready_sidecar_header(Some(prepared.header()))
            .map_err(LockedSlotExecutionError::BeforeArm)?;
        let offset = sidecar_slot_offset(header, prepared.slot_index())
            .map_err(LockedSlotExecutionError::BeforeArm)?;
        let mut current = [0u8; SLOT_SIZE as usize];
        self.read_exact_at(&mut current, offset)
            .map_err(LockedSlotExecutionError::BeforeArm)?;
        prepared
            .confirm_source(&current, host)
            .map_err(LockedSlotExecutionError::TransitionBeforeArm)?;
        self.execute_preconfirmed_sidecar_slot_transition(prepared, offset, false)
    }

    fn execute_preconfirmed_sidecar_slot_transition(
        &mut self,
        prepared: PreparedSlotTransition,
        offset: u64,
        replace_dead_writer: bool,
    ) -> Result<(), LockedSlotExecutionError> {
        self.read_ready_sidecar_header(Some(prepared.header()))
            .map_err(LockedSlotExecutionError::BeforeArm)?;
        let dead_writer = match self.cleanup_authority {
            SidecarCleanupAuthority::None if !replace_dead_writer => None,
            SidecarCleanupAuthority::DeadWriter(dead)
                if replace_dead_writer
                    && prepared.role() == SlotRole::Writer
                    && prepared.kind() == SlotTransitionKind::Clear =>
            {
                Some(dead)
            }
            SidecarCleanupAuthority::None
            | SidecarCleanupAuthority::Armed { .. }
            | SidecarCleanupAuthority::DeadWriter(_) => {
                return Err(LockedSlotExecutionError::TransitionBeforeArm(
                    SlotTransitionError::ProvenanceOccupied,
                ));
            }
        };
        self.cleanup_authority = SidecarCleanupAuthority::Armed {
            transition: prepared.arm(),
            dead_writer,
        };
        let file = &self.file;
        let result = match &mut self.cleanup_authority {
            SidecarCleanupAuthority::Armed { transition, .. } => transition.execute_armed(
                |relative, bytes| {
                    write_all_at_file(
                        file,
                        bytes,
                        offset
                            .checked_add(relative as u64)
                            .ok_or(LinuxOsError::SlotOffsetOverflow)?,
                    )
                },
                |observed| read_exact_at_file(file, observed, offset),
            ),
            SidecarCleanupAuthority::None | SidecarCleanupAuthority::DeadWriter(_) => {
                unreachable!()
            }
        }
        .map_err(LockedSlotExecutionError::Interrupted);
        if result.is_ok() {
            self.cleanup_authority = SidecarCleanupAuthority::None;
        }
        result
    }

    pub(crate) fn retry_sidecar_slot_cleanup(
        &mut self,
        host: SlotHostLimits,
    ) -> Result<CleanupDisposition, SlotCleanupError<LinuxOsError>> {
        self.require_exclusive_lock()
            .map_err(SlotCleanupError::Io)?;
        let (expected_header, slot_index) = match &self.cleanup_authority {
            SidecarCleanupAuthority::Armed { transition, .. } => {
                (transition.header(), transition.slot_index())
            }
            SidecarCleanupAuthority::DeadWriter(_) => {
                return Err(SlotCleanupError::Io(LinuxOsError::PendingWriterCleanup));
            }
            SidecarCleanupAuthority::None => {
                return Err(SlotCleanupError::Transition(SlotTransitionError::NotArmed));
            }
        };
        let header = self
            .read_ready_sidecar_header(Some(expected_header))
            .map_err(SlotCleanupError::Io)?;
        let offset = sidecar_slot_offset(header, slot_index).map_err(SlotCleanupError::Io)?;
        let file = &self.file;
        let SidecarCleanupAuthority::Armed { transition, .. } = &mut self.cleanup_authority else {
            unreachable!();
        };
        let result = transition.retry_cleanup(
            host,
            |relative, bytes| {
                write_all_at_file(
                    file,
                    bytes,
                    offset
                        .checked_add(relative as u64)
                        .ok_or(LinuxOsError::SlotOffsetOverflow)?,
                )
            },
            |observed| read_exact_at_file(file, observed, offset),
        );
        if result.is_ok() {
            self.cleanup_authority = SidecarCleanupAuthority::None;
        }
        result
    }

    fn require_exclusive_lock(&self) -> Result<(), LinuxOsError> {
        self.check_creator()?;
        if self.lock != LockState::Exclusive {
            return Err(LinuxOsError::OperationLockRequired);
        }
        Ok(())
    }

    fn check_creator(&self) -> Result<(), LinuxOsError> {
        if std::process::id() != self.creator_pid {
            return Err(LinuxOsError::ForkedHandle);
        }
        Ok(())
    }
}

impl PositionalRead for RetainedRegular {
    fn check_access(&self) -> Result<(), PageSourceError> {
        if std::process::id() != self.creator_pid {
            return Err(PageSourceError::ForkedHandle);
        }
        Ok(())
    }

    fn read_exact_at(&self, mut offset: u64, mut bytes: &mut [u8]) -> Result<(), PageSourceError> {
        self.check_access()?;
        use std::os::unix::fs::FileExt;
        let initial_offset = offset;
        let expected = bytes.len();
        let mut actual = 0usize;
        while !bytes.is_empty() {
            let read = self
                .file
                .read_at(bytes, offset)
                .map_err(|error| PageSourceError::Io(PageIoEvidence::from_error(&error)))?;
            if read == 0 {
                return Err(PageSourceError::ShortRead {
                    offset: initial_offset,
                    expected,
                    actual,
                });
            }
            actual = actual
                .checked_add(read)
                .ok_or(PageSourceError::OffsetOverflow)?;
            offset = offset
                .checked_add(u64::try_from(read).map_err(|_| PageSourceError::OffsetOverflow)?)
                .ok_or(PageSourceError::OffsetOverflow)?;
            bytes = &mut bytes[read..];
        }
        Ok(())
    }
}

impl RetainedLiveFiles {
    pub(crate) fn open_locked(path: &Path) -> Result<Self, LinuxLivePairError> {
        Self::open_locked_with_cancel(path, || false)
    }

    fn open_locked_with_cancel(
        path: &Path,
        mut cancelled: impl FnMut() -> bool,
    ) -> Result<Self, LinuxLivePairError> {
        if cancelled() {
            return Err(LinuxLivePairError::Os(LinuxOsError::Cancelled));
        }
        let (directory, main_component) =
            RetainedDirectory::open_parent(path).map_err(LinuxLivePairError::Os)?;
        let sidecar_component = directory
            .sidecar_component(&main_component)
            .map_err(LinuxLivePairError::Os)?;
        // Retain canonical path arguments before taking either file lock.
        // Operation-barrier identity rechecks must not allocate.
        let main_component_c =
            component_c_string(&main_component).map_err(LinuxLivePairError::Os)?;
        let sidecar_component_c =
            component_c_string(&sidecar_component).map_err(LinuxLivePairError::Os)?;
        let mut main = directory
            .open_regular(&main_component, true)
            .map_err(LinuxLivePairError::Os)?;
        // Bootstrap before coordination, then retain the lifetime lock before
        // opening the sidecar. The selected generation is re-read below while
        // both locks are held.
        main.read_main_bootstrap(OpenMode::Writer)
            .map_err(LinuxLivePairError::Os)?;
        main.acquire_lock_interruptible(LockMode::Shared, &mut cancelled)
            .map_err(LinuxLivePairError::Os)?;
        if cancelled() {
            return Err(LinuxLivePairError::Os(LinuxOsError::Cancelled));
        }
        let mut sidecar = directory
            .open_regular(&sidecar_component, true)
            .map_err(LinuxLivePairError::Os)?;
        sidecar
            .acquire_lock_interruptible(LockMode::Exclusive, &mut cancelled)
            .map_err(LinuxLivePairError::Os)?;
        if cancelled() {
            return Err(LinuxLivePairError::Os(LinuxOsError::Cancelled));
        }
        let bootstrap = main
            .read_main_bootstrap(OpenMode::Writer)
            .map_err(LinuxLivePairError::Os)?;
        let header = sidecar
            .read_ready_sidecar_header(None)
            .map_err(LinuxLivePairError::Os)?;
        directory
            .verify_live_pair_binding(
                &main_component,
                &main,
                &sidecar_component,
                &sidecar,
                bootstrap,
                header,
            )
            .map_err(LinuxLivePairError::Os)?;
        Ok(Self {
            directory,
            main_component,
            main_component_c,
            sidecar_component,
            sidecar_component_c,
            main,
            sidecar,
            header,
            last_scan: None,
            writer_bootstrap: None,
            writer_tail: None,
            writer_publication: None,
        })
    }

    fn verify_live_pair_binding(
        &self,
        bootstrap: Bootstrap,
        header: SidecarHeader,
    ) -> Result<(), LinuxOsError> {
        if self.main.lock != LockState::Shared {
            return Err(LinuxOsError::LifetimeLockRequired);
        }
        self.sidecar.require_exclusive_lock()?;
        self.directory
            .verify_path_cstr(&self.main_component_c, &self.main)?;
        self.directory
            .verify_path_cstr(&self.sidecar_component_c, &self.sidecar)?;
        self.directory.verify_live_pair_descriptor_binding(
            &self.main_component,
            &self.main,
            &self.sidecar,
            bootstrap,
            header,
        )
    }

    pub(crate) fn scan_and_reap(&mut self) -> Result<ReadySidecarInspection, LinuxLivePairError> {
        self.scan_and_reap_with(observe_posix_process)
    }

    fn scan_and_reap_with(
        &mut self,
        observe: impl FnMut(ActiveSlot) -> PosixProcessObservation,
    ) -> Result<ReadySidecarInspection, LinuxLivePairError> {
        self.scan_and_reap_with_cancel(observe, || false)
    }

    fn scan_and_reap_with_cancel(
        &mut self,
        observe: impl FnMut(ActiveSlot) -> PosixProcessObservation,
        cancelled: impl FnMut() -> bool,
    ) -> Result<ReadySidecarInspection, LinuxLivePairError> {
        self.last_scan = None;
        let bootstrap = self
            .main
            .read_main_bootstrap(OpenMode::Writer)
            .map_err(LinuxLivePairError::Os)?;
        self.verify_live_pair_binding(bootstrap, self.header)
            .map_err(LinuxLivePairError::Os)?;
        let inspection = match self.sidecar.scan_and_reap_ready_sidecar_with_cancel(
            self.header,
            bootstrap.meta.txn_id,
            observe,
            cancelled,
        ) {
            Ok(inspection) => inspection,
            Err(cause @ LinuxSidecarScanError::DeadWriter { .. }) => {
                let SidecarCleanupAuthority::DeadWriter(dead) = &mut self.sidecar.cleanup_authority
                else {
                    unreachable!("dead-writer scan retains exact cleanup authority")
                };
                dead.bootstrap = Some(bootstrap);
                dead.tail = (bootstrap.physical_bytes > bootstrap.committed_bytes).then_some(
                    UnpublishedMainTail {
                        main_identity: self.main.identity,
                        database_id: bootstrap.meta.database_id,
                        transaction_id: bootstrap.meta.txn_id,
                        commit_nonce: bootstrap.meta.commit_nonce,
                        committed_length: bootstrap.committed_bytes,
                        observed_end_exclusive: bootstrap.physical_bytes,
                    },
                );
                return Err(LinuxLivePairError::Scan(cause));
            }
            Err(cause) => return Err(LinuxLivePairError::Scan(cause)),
        };
        self.last_scan = Some((bootstrap, inspection));
        Ok(inspection)
    }

    pub(crate) fn retry_dead_writer_cleanup(&mut self) -> Result<(), LinuxLivePairError> {
        self.retry_dead_writer_cleanup_with(
            |file, length| file.set_len(length),
            |file| file.sync_all(),
        )
    }

    fn retry_dead_writer_cleanup_with(
        &mut self,
        mut truncate: impl FnMut(&File, u64) -> io::Result<()>,
        mut synchronize: impl FnMut(&File) -> io::Result<()>,
    ) -> Result<(), LinuxLivePairError> {
        self.last_scan = None;
        if self.main.lock != LockState::Shared {
            return Err(LinuxLivePairError::Os(LinuxOsError::LifetimeLockRequired));
        }
        self.sidecar
            .require_exclusive_lock()
            .map_err(LinuxLivePairError::Os)?;
        match self.sidecar.cleanup_authority {
            SidecarCleanupAuthority::Armed {
                ref transition,
                dead_writer: Some(dead),
            } if armed_transition_is_dead_writer_clear(transition, dead) => {
                return self.retry_armed_dead_writer_cleanup(dead);
            }
            SidecarCleanupAuthority::Armed {
                dead_writer: None, ..
            }
            | SidecarCleanupAuthority::Armed {
                dead_writer: Some(_),
                ..
            } => return Err(LinuxLivePairError::NoDeadWriter),
            SidecarCleanupAuthority::None | SidecarCleanupAuthority::DeadWriter(_) => {}
        }
        let mut dead = match self.sidecar.cleanup_authority {
            SidecarCleanupAuthority::DeadWriter(dead) => dead,
            SidecarCleanupAuthority::Armed { .. } => unreachable!(),
            SidecarCleanupAuthority::None => {
                return Err(LinuxLivePairError::NoDeadWriter);
            }
        };

        let header = self
            .sidecar
            .read_ready_sidecar_header(Some(dead.header))
            .map_err(LinuxLivePairError::Os)?;
        let source = self
            .sidecar
            .read_sidecar_slot_after_header(header, 0)
            .map_err(LinuxLivePairError::Os)?;
        if source != dead.source_image {
            return Err(LinuxLivePairError::WriterSourceChanged);
        }
        if let Some(tail) = dead.tail {
            let metadata = self.main.file.metadata().map_err(|source| {
                LinuxLivePairError::Os(LinuxOsError::Io {
                    operation: "inspect retained main tail",
                    source,
                })
            })?;
            validate_regular(&metadata).map_err(LinuxLivePairError::Os)?;
            if metadata_identity(&metadata) != tail.main_identity {
                return Err(LinuxLivePairError::MainGenerationChanged);
            }
            if metadata.len() < tail.committed_length
                || metadata.len() > tail.observed_end_exclusive
            {
                return Err(LinuxLivePairError::TailLengthConflict {
                    target: tail.committed_length,
                    observed_end: tail.observed_end_exclusive,
                    actual: metadata.len(),
                });
            }
        }
        let before = self
            .main
            .read_main_bootstrap(OpenMode::Writer)
            .map_err(LinuxLivePairError::Os)?;
        if let Some(claimed) = dead.bootstrap {
            require_exact_dead_writer_bootstrap(claimed, before)?;
        } else {
            dead.bootstrap = Some(before);
        }
        self.directory
            .verify_live_pair_binding(
                &self.main_component,
                &self.main,
                &self.sidecar_component,
                &self.sidecar,
                before,
                header,
            )
            .map_err(LinuxLivePairError::Os)?;

        if let Some(tail) = dead.tail {
            require_same_tail_generation(tail, self.main.identity, before)?;
        } else if before.physical_bytes > before.committed_bytes {
            dead.tail = Some(UnpublishedMainTail {
                main_identity: self.main.identity,
                database_id: before.meta.database_id,
                transaction_id: before.meta.txn_id,
                commit_nonce: before.meta.commit_nonce,
                committed_length: before.committed_bytes,
                observed_end_exclusive: before.physical_bytes,
            });
        }
        self.sidecar.cleanup_authority = SidecarCleanupAuthority::DeadWriter(dead);

        let header = self
            .sidecar
            .read_ready_sidecar_header(Some(dead.header))
            .map_err(LinuxLivePairError::Os)?;
        let source = self
            .sidecar
            .read_sidecar_slot_after_header(header, 0)
            .map_err(LinuxLivePairError::Os)?;
        if source != dead.source_image {
            return Err(LinuxLivePairError::WriterSourceChanged);
        }

        if before.physical_bytes > before.committed_bytes {
            truncate(&self.main.file, before.committed_bytes).map_err(|source| {
                LinuxLivePairError::Os(LinuxOsError::Io {
                    operation: "truncate unpublished main tail",
                    source,
                })
            })?;
        }
        synchronize(&self.main.file).map_err(|source| {
            LinuxLivePairError::Os(LinuxOsError::Io {
                operation: "synchronize main tail cleanup",
                source,
            })
        })?;

        let header = self
            .sidecar
            .read_ready_sidecar_header(Some(dead.header))
            .map_err(LinuxLivePairError::Os)?;
        let source = self
            .sidecar
            .read_sidecar_slot_after_header(header, 0)
            .map_err(LinuxLivePairError::Os)?;
        if source != dead.source_image {
            return Err(LinuxLivePairError::WriterSourceChanged);
        }

        let after = self
            .main
            .read_main_bootstrap(OpenMode::Writer)
            .map_err(LinuxLivePairError::Os)?;
        let claimed = dead
            .bootstrap
            .ok_or(LinuxLivePairError::MainGenerationChanged)?;
        require_exact_dead_writer_bootstrap(claimed, after)?;
        if after.physical_bytes != claimed.committed_bytes {
            return Err(LinuxLivePairError::MainGenerationChanged);
        }
        self.directory
            .verify_live_pair_binding(
                &self.main_component,
                &self.main,
                &self.sidecar_component,
                &self.sidecar,
                after,
                header,
            )
            .map_err(LinuxLivePairError::Os)?;
        let final_source = self
            .sidecar
            .read_sidecar_slot(dead.header, 0)
            .map_err(LinuxLivePairError::Os)?;
        if final_source != dead.source_image {
            return Err(LinuxLivePairError::WriterSourceChanged);
        }
        let prepared = PreparedSlotTransition::clear_proven_dead(
            header,
            SlotRole::Writer,
            0,
            &final_source,
            dead.active,
            dead.proof,
            linux_slot_host_limits(),
        )
        .map_err(LinuxLivePairError::TransitionBeforeArm)?;
        let offset = sidecar_slot_offset(header, 0).map_err(LinuxLivePairError::Os)?;
        let result = self
            .sidecar
            .execute_preconfirmed_sidecar_slot_transition(prepared, offset, true)
            .map_err(LinuxLivePairError::Transition);
        if result.is_ok() {
            self.directory
                .verify_path(&self.main_component, &self.main)
                .map_err(LinuxLivePairError::PostClearPath)?;
            self.directory
                .verify_path(&self.sidecar_component, &self.sidecar)
                .map_err(LinuxLivePairError::PostClearPath)?;
        }
        result
    }

    fn retry_armed_dead_writer_cleanup(
        &mut self,
        dead: ProvenDeadWriter,
    ) -> Result<(), LinuxLivePairError> {
        let header = self
            .sidecar
            .read_ready_sidecar_header(Some(dead.header))
            .map_err(LinuxLivePairError::Os)?;
        self.sidecar
            .read_sidecar_slot_after_header(header, 0)
            .map_err(LinuxLivePairError::Os)?;
        let claimed = dead
            .bootstrap
            .ok_or(LinuxLivePairError::MainGenerationChanged)?;
        let current = self
            .main
            .read_main_bootstrap(OpenMode::Writer)
            .map_err(LinuxLivePairError::Os)?;
        require_exact_dead_writer_bootstrap(claimed, current)?;
        if current.physical_bytes != claimed.committed_bytes {
            return Err(LinuxLivePairError::MainGenerationChanged);
        }
        if let Some(tail) = dead.tail {
            require_same_tail_generation(tail, self.main.identity, current)?;
        }
        self.directory
            .verify_live_pair_binding(
                &self.main_component,
                &self.main,
                &self.sidecar_component,
                &self.sidecar,
                current,
                header,
            )
            .map_err(LinuxLivePairError::Os)?;
        self.sidecar
            .retry_sidecar_slot_cleanup(linux_slot_host_limits())
            .map_err(LinuxLivePairError::ArmedCleanup)?;
        self.directory
            .verify_path(&self.main_component, &self.main)
            .map_err(LinuxLivePairError::PostClearPath)?;
        self.directory
            .verify_path(&self.sidecar_component, &self.sidecar)
            .map_err(LinuxLivePairError::PostClearPath)
    }

    pub(crate) fn claim_reader_slot(&mut self) -> Result<OwnedReaderSlot, LinuxReaderSlotError> {
        self.claim_reader_slot_with(random_nonzero_128)
    }

    pub(crate) fn claim_writer_lease(&mut self) -> Result<OwnedWriterLease, LinuxWriterLeaseError> {
        self.claim_writer_lease_with(random_nonzero_128)
    }

    fn claim_writer_lease_with(
        &mut self,
        nonce: impl FnMut() -> Result<[u8; 16], LinuxOsError>,
    ) -> Result<OwnedWriterLease, LinuxWriterLeaseError> {
        self.claim_writer_lease_with_transition(nonce, |sidecar, prepared, _offset| {
            sidecar.execute_sidecar_slot_transition(prepared, linux_slot_host_limits())
        })
    }

    fn claim_writer_lease_with_transition(
        &mut self,
        mut nonce: impl FnMut() -> Result<[u8; 16], LinuxOsError>,
        execute: impl FnOnce(
            &mut RetainedRegular,
            PreparedSlotTransition,
            u64,
        ) -> Result<(), LockedSlotExecutionError>,
    ) -> Result<OwnedWriterLease, LinuxWriterLeaseError> {
        let (bootstrap, inspection) = self
            .last_scan
            .take()
            .ok_or(LinuxWriterLeaseError::ScanRequired)?;
        if inspection.header != self.header {
            return Err(LinuxWriterLeaseError::ScanChanged);
        }
        if inspection.writer.is_some() {
            return Err(LinuxWriterLeaseError::WriterBusy);
        }
        let current_bootstrap = self
            .main
            .read_main_bootstrap(OpenMode::Writer)
            .map_err(LinuxWriterLeaseError::Os)?;
        if current_bootstrap != bootstrap {
            return Err(LinuxWriterLeaseError::GenerationChanged);
        }
        self.directory
            .verify_live_pair_binding(
                &self.main_component,
                &self.main,
                &self.sidecar_component,
                &self.sidecar,
                current_bootstrap,
                inspection.header,
            )
            .map_err(LinuxWriterLeaseError::Os)?;

        // Entropy and every other fallible preparation step precede publication.
        let nonce = nonce().map_err(LinuxWriterLeaseError::Os)?;
        let current = self
            .sidecar
            .read_sidecar_slot_after_header(inspection.header, 0)
            .map_err(LinuxWriterLeaseError::Os)?;
        let active = current_active_slot(bootstrap.meta.txn_id, nonce);
        let prepared = PreparedSlotTransition::claim(
            inspection.header,
            SlotRole::Writer,
            0,
            &current,
            active,
            linux_slot_host_limits(),
        )
        .map_err(LinuxWriterLeaseError::TransitionBeforeArm)?;
        let offset =
            sidecar_slot_offset(inspection.header, 0).map_err(LinuxWriterLeaseError::Os)?;
        self.writer_bootstrap = Some(current_bootstrap);
        self.writer_tail =
            (bootstrap.physical_bytes > bootstrap.committed_bytes).then_some(UnpublishedMainTail {
                main_identity: self.main.identity,
                database_id: bootstrap.meta.database_id,
                transaction_id: bootstrap.meta.txn_id,
                commit_nonce: bootstrap.meta.commit_nonce,
                committed_length: bootstrap.committed_bytes,
                observed_end_exclusive: bootstrap.physical_bytes,
            });
        let result =
            execute(&mut self.sidecar, prepared, offset).map_err(LinuxWriterLeaseError::Transition);
        if result.is_err() && !self.sidecar.has_armed_transition() {
            self.writer_bootstrap = None;
            self.writer_tail = None;
        }
        result?;
        Ok(OwnedWriterLease {
            header: inspection.header,
            active,
            claimed_bootstrap: current_bootstrap,
        })
    }

    pub(crate) fn prepare_writer_for_exposure(
        &mut self,
        owned: &OwnedWriterLease,
    ) -> Result<Bootstrap, LinuxWriterLeaseError> {
        self.prepare_writer_for_exposure_with(
            owned,
            |file, length| file.set_len(length),
            |file| file.sync_all(),
        )
    }

    fn prepare_writer_for_exposure_with(
        &mut self,
        owned: &OwnedWriterLease,
        truncate: impl FnMut(&File, u64) -> io::Result<()>,
        synchronize: impl FnMut(&File) -> io::Result<()>,
    ) -> Result<Bootstrap, LinuxWriterLeaseError> {
        let bootstrap = self.freeze_owned_writer_tail(Some(owned))?;
        self.resolve_owned_writer_tail_with(Some(owned), truncate, synchronize)?;
        let final_bootstrap = self.freeze_owned_writer_tail(Some(owned))?;
        if self.writer_tail.is_some() {
            return Err(LinuxWriterLeaseError::TailCleanupRequired);
        }
        require_exact_writer_bootstrap(bootstrap, final_bootstrap)?;
        self.verify_owned_writer(owned)?;
        self.directory
            .verify_live_pair_binding(
                &self.main_component,
                &self.main,
                &self.sidecar_component,
                &self.sidecar,
                final_bootstrap,
                owned.header,
            )
            .map_err(LinuxWriterLeaseError::Os)?;
        self.sidecar
            .release_lock()
            .map_err(LinuxWriterLeaseError::Os)?;
        Ok(final_bootstrap)
    }

    /// Records one target generation before any target metadata byte is
    /// written. The caller must retain the operation lock through the later
    /// metadata and lease phases.
    pub(crate) fn begin_writer_commit_attempt(
        &mut self,
        owned: &OwnedWriterLease,
        target: MetaV4,
    ) -> Result<(), LinuxWriterLeaseError> {
        self.sidecar
            .require_exclusive_lock()
            .map_err(LinuxWriterLeaseError::Os)?;
        if !matches!(
            &self.sidecar.cleanup_authority,
            SidecarCleanupAuthority::None
        ) {
            return Err(LinuxWriterLeaseError::OutstandingWriterCleanup);
        }
        if self.writer_publication.is_some() || self.writer_tail.is_some() {
            return Err(LinuxWriterLeaseError::CommitPublicationState);
        }
        let source = self.verify_owned_writer(owned)?;
        let expected_txn = source
            .meta
            .txn_id
            .checked_add(1)
            .ok_or(LinuxWriterLeaseError::CommitAttemptMismatch)?;
        let target_bytes = target
            .page_count
            .checked_mul(PAGE_SIZE as u64)
            .ok_or(LinuxWriterLeaseError::CommitAttemptMismatch)?;
        if source.physical_bytes != source.committed_bytes
            || !source.meta.static_identity_eq(&target)
            || target.txn_id != expected_txn
            || target.commit_nonce == [0; 16]
            || !(2..=MAX_PAGE_COUNT).contains(&target.page_count)
            || target.page_count < source.meta.page_count
            || target_bytes < source.committed_bytes
        {
            return Err(LinuxWriterLeaseError::CommitAttemptMismatch);
        }
        self.writer_publication = Some(WriterCommitPublication {
            source,
            source_active: owned.active,
            target,
            phase: WriterCommitPublicationPhase::PreMeta,
        });
        Ok(())
    }

    /// Crosses the phase-3 boundary immediately before the first target-meta
    /// write. It freezes the exact source tail that remains disposable only
    /// while the source generation stays selected.
    pub(crate) fn begin_writer_meta_write(
        &mut self,
        owned: &OwnedWriterLease,
        target: MetaV4,
    ) -> Result<(), LinuxWriterLeaseError> {
        let publication = self.require_writer_publication(owned, target)?;
        if publication.phase != WriterCommitPublicationPhase::PreMeta {
            return Err(LinuxWriterLeaseError::CommitPublicationState);
        }
        let current = self
            .main
            .read_main_bootstrap(OpenMode::Writer)
            .map_err(LinuxWriterLeaseError::Os)?;
        require_exact_writer_bootstrap(publication.source, current)?;
        self.verify_live_pair_binding(current, owned.header)
            .map_err(LinuxWriterLeaseError::Os)?;
        let target_bytes = publication.target_committed_bytes()?;
        if current.physical_bytes != target_bytes {
            return Err(LinuxWriterLeaseError::TailLengthConflict {
                target: target_bytes,
                observed_end: target_bytes,
                actual: current.physical_bytes,
            });
        }
        if current.physical_bytes > publication.source.committed_bytes {
            self.writer_tail = Some(UnpublishedMainTail {
                main_identity: self.main.identity,
                database_id: publication.source.meta.database_id,
                transaction_id: publication.source.meta.txn_id,
                commit_nonce: publication.source.meta.commit_nonce,
                committed_length: publication.source.committed_bytes,
                observed_end_exclusive: current.physical_bytes,
            });
        }
        let publication = self.writer_publication.as_mut().unwrap();
        publication.phase = WriterCommitPublicationPhase::MetaWriteStarted;
        Ok(())
    }

    /// Confirms the exact target meta only after the caller has completed its
    /// phase-4 main-file synchronization.
    pub(crate) fn confirm_writer_meta_sync(
        &mut self,
        owned: &OwnedWriterLease,
        target: MetaV4,
    ) -> Result<Bootstrap, LinuxWriterLeaseError> {
        let publication = self.require_writer_publication(owned, target)?;
        if publication.phase != WriterCommitPublicationPhase::MetaWriteStarted {
            return Err(LinuxWriterLeaseError::CommitPublicationState);
        }
        let current = self
            .main
            .read_main_bootstrap(OpenMode::Writer)
            .map_err(LinuxWriterLeaseError::Os)?;
        if !is_exact_writer_target(publication.target, current)
            || current.physical_bytes != publication.target_committed_bytes()?
        {
            return Err(LinuxWriterLeaseError::CommitOutcomeUnresolved);
        }
        self.verify_live_pair_binding(current, owned.header)
            .map_err(LinuxWriterLeaseError::Os)?;
        let publication = self.writer_publication.as_mut().unwrap();
        publication.phase = WriterCommitPublicationPhase::MetaSynchronized { target: current };
        Ok(current)
    }

    /// Performs phase 5's source-to-target writer-lease transition. A failed
    /// transition deliberately leaves the publication state and any armed
    /// provenance intact for Close.
    pub(crate) fn update_writer_lease_after_meta(
        &mut self,
        owned: &mut OwnedWriterLease,
    ) -> Result<(), LinuxWriterLeaseError> {
        let target = self.writer_publication_target()?;
        let publication = self.require_writer_publication(owned, target)?;
        let WriterCommitPublicationPhase::MetaSynchronized { target } = publication.phase else {
            return Err(LinuxWriterLeaseError::CommitPublicationState);
        };
        let current = self
            .main
            .read_main_bootstrap(OpenMode::Writer)
            .map_err(LinuxWriterLeaseError::Os)?;
        if current != target || !is_exact_writer_target(publication.target, current) {
            return Err(LinuxWriterLeaseError::CommitOutcomeUnresolved);
        }
        self.verify_live_pair_binding(current, owned.header)
            .map_err(LinuxWriterLeaseError::Os)?;
        let source = self
            .sidecar
            .read_sidecar_slot(owned.header, 0)
            .map_err(LinuxWriterLeaseError::Os)?;
        let target_active = publication.target_active();
        let prepared = PreparedSlotTransition::update(
            owned.header,
            SlotRole::Writer,
            0,
            &source,
            publication.source_active,
            target_active,
            linux_slot_host_limits(),
        )
        .map_err(LinuxWriterLeaseError::TransitionBeforeArm)?;
        self.sidecar
            .execute_sidecar_slot_transition(prepared, linux_slot_host_limits())
            .map_err(LinuxWriterLeaseError::Transition)?;
        owned.active = target_active;
        owned.claimed_bootstrap = target;
        self.writer_bootstrap = Some(target);
        self.writer_tail = None;
        self.writer_publication = None;
        Ok(())
    }

    fn writer_publication_target(&self) -> Result<MetaV4, LinuxWriterLeaseError> {
        self.writer_publication
            .map(|publication| publication.target)
            .ok_or(LinuxWriterLeaseError::CommitPublicationState)
    }

    fn require_writer_publication(
        &self,
        owned: &OwnedWriterLease,
        target: MetaV4,
    ) -> Result<WriterCommitPublication, LinuxWriterLeaseError> {
        self.sidecar
            .require_exclusive_lock()
            .map_err(LinuxWriterLeaseError::Os)?;
        if !matches!(
            &self.sidecar.cleanup_authority,
            SidecarCleanupAuthority::None
        ) {
            return Err(LinuxWriterLeaseError::OutstandingWriterCleanup);
        }
        let publication = self
            .writer_publication
            .ok_or(LinuxWriterLeaseError::CommitPublicationState)?;
        if publication.target != target
            || owned.header != self.header
            || owned.active != publication.source_active
            || owned.claimed_bootstrap != publication.source
        {
            return Err(LinuxWriterLeaseError::CommitAttemptMismatch);
        }
        let source = self
            .sidecar
            .read_sidecar_slot_after_header(owned.header, 0)
            .map_err(LinuxWriterLeaseError::Os)?;
        if decode_stable_slot(&source, SlotRole::Writer, linux_slot_host_limits())
            != Ok(StableSlot::Active(publication.source_active))
        {
            return Err(LinuxWriterLeaseError::OwnerMismatch);
        }
        Ok(publication)
    }

    fn verify_owned_writer(
        &self,
        owned: &OwnedWriterLease,
    ) -> Result<Bootstrap, LinuxWriterLeaseError> {
        let (bootstrap, active) = self.inspect_owned_writer_for_cleanup(owned)?;
        if !active {
            return Err(LinuxWriterLeaseError::OwnerMismatch);
        }
        Ok(bootstrap)
    }

    fn verify_owned_writer_operation(
        &self,
        owned: &OwnedWriterLease,
        selected: Bootstrap,
    ) -> Result<Bootstrap, LinuxWriterLeaseError> {
        let bootstrap = self.verify_owned_writer(owned)?;
        require_exact_writer_bootstrap(selected, bootstrap)?;
        self.verify_live_pair_binding(bootstrap, owned.header)
            .map_err(LinuxWriterLeaseError::Os)?;
        Ok(bootstrap)
    }

    fn verify_owned_writer_for_cleanup(
        &self,
        owned: &OwnedWriterLease,
    ) -> Result<bool, LinuxWriterLeaseError> {
        self.inspect_owned_writer_for_cleanup(owned)
            .map(|(_, active)| active)
    }

    fn inspect_owned_writer_for_cleanup(
        &self,
        owned: &OwnedWriterLease,
    ) -> Result<(Bootstrap, bool), LinuxWriterLeaseError> {
        if owned.header != self.header || owned.active.txn_id == 0 {
            return Err(LinuxWriterLeaseError::OwnerMismatch);
        }
        let claimed = self
            .writer_bootstrap
            .ok_or(LinuxWriterLeaseError::NoCleanupAuthority)?;
        require_exact_writer_bootstrap(claimed, owned.claimed_bootstrap)?;
        if claimed.meta.txn_id != owned.active.txn_id {
            return Err(LinuxWriterLeaseError::OwnerMismatch);
        }
        let bootstrap = self
            .main
            .read_main_bootstrap(OpenMode::Writer)
            .map_err(LinuxWriterLeaseError::Os)?;
        require_exact_writer_bootstrap(claimed, bootstrap)?;
        self.directory
            .verify_live_pair_descriptor_binding(
                &self.main_component,
                &self.main,
                &self.sidecar,
                bootstrap,
                owned.header,
            )
            .map_err(LinuxWriterLeaseError::Os)?;
        let current = self
            .sidecar
            .read_sidecar_slot_after_header(owned.header, 0)
            .map_err(LinuxWriterLeaseError::Os)?;
        match decode_stable_slot(&current, SlotRole::Writer, linux_slot_host_limits()) {
            Ok(StableSlot::Active(active)) if active == owned.active => Ok((bootstrap, true)),
            Ok(StableSlot::Free) => Ok((bootstrap, false)),
            _ => Err(LinuxWriterLeaseError::OwnerMismatch),
        }
    }

    fn claim_reader_slot_with(
        &mut self,
        mut nonce: impl FnMut() -> Result<[u8; 16], LinuxOsError>,
    ) -> Result<OwnedReaderSlot, LinuxReaderSlotError> {
        let (bootstrap, inspection) = self
            .last_scan
            .take()
            .ok_or(LinuxReaderSlotError::ScanRequired)?;
        let index = inspection
            .lowest_free_reader_slot
            .ok_or(LinuxReaderSlotError::ReaderCapacityExhausted)?;
        if inspection.header != self.header {
            return Err(LinuxReaderSlotError::ScanChanged);
        }
        self.directory
            .verify_live_pair_binding(
                &self.main_component,
                &self.main,
                &self.sidecar_component,
                &self.sidecar,
                bootstrap,
                inspection.header,
            )
            .map_err(LinuxReaderSlotError::Os)?;
        let nonce = nonce().map_err(LinuxReaderSlotError::Os)?;
        let current = self
            .sidecar
            .read_sidecar_slot_after_header(inspection.header, index)
            .map_err(LinuxReaderSlotError::Os)?;
        let active = current_active_slot(0, nonce);
        let prepared = PreparedSlotTransition::claim(
            inspection.header,
            SlotRole::Reader,
            index,
            &current,
            active,
            linux_slot_host_limits(),
        )
        .map_err(LinuxReaderSlotError::TransitionBeforeArm)?;
        self.sidecar
            .execute_sidecar_slot_transition(prepared, linux_slot_host_limits())
            .map_err(LinuxReaderSlotError::Transition)?;
        Ok(OwnedReaderSlot {
            header: inspection.header,
            index,
            active,
        })
    }

    pub(crate) fn pin_reader_slot(
        &mut self,
        owned: &mut OwnedReaderSlot,
    ) -> Result<Bootstrap, LinuxReaderSlotError> {
        if owned.header != self.header || owned.index == 0 || owned.index > self.header.capacity {
            return Err(LinuxReaderSlotError::OwnerMismatch);
        }
        let bootstrap = self
            .main
            .read_main_bootstrap(OpenMode::LiveReader)
            .map_err(LinuxReaderSlotError::Os)?;
        self.directory
            .verify_live_pair_binding(
                &self.main_component,
                &self.main,
                &self.sidecar_component,
                &self.sidecar,
                bootstrap,
                owned.header,
            )
            .map_err(LinuxReaderSlotError::Os)?;
        let current = self
            .sidecar
            .read_sidecar_slot_after_header(owned.header, owned.index)
            .map_err(LinuxReaderSlotError::Os)?;
        let target = ActiveSlot {
            txn_id: bootstrap.meta.txn_id,
            ..owned.active
        };
        let prepared = PreparedSlotTransition::update(
            owned.header,
            SlotRole::Reader,
            owned.index,
            &current,
            owned.active,
            target,
            linux_slot_host_limits(),
        )
        .map_err(LinuxReaderSlotError::TransitionBeforeArm)?;
        self.sidecar
            .execute_sidecar_slot_transition(prepared, linux_slot_host_limits())
            .map_err(LinuxReaderSlotError::Transition)?;
        owned.active = target;
        Ok(bootstrap)
    }

    pub(crate) fn release_reader_registration_lock(
        &mut self,
        owned: &OwnedReaderSlot,
        pinned: Bootstrap,
    ) -> Result<(), LinuxReaderSlotError> {
        if owned.header != self.header
            || owned.index == 0
            || owned.index > self.header.capacity
            || owned.active.txn_id == 0
            || owned.active.txn_id != pinned.meta.txn_id
        {
            return Err(LinuxReaderSlotError::OwnerMismatch);
        }
        let current_bootstrap = self
            .main
            .read_main_bootstrap(OpenMode::LiveReader)
            .map_err(LinuxReaderSlotError::Os)?;
        if current_bootstrap != pinned {
            return Err(LinuxReaderSlotError::GenerationChanged);
        }
        self.directory
            .verify_live_pair_binding(
                &self.main_component,
                &self.main,
                &self.sidecar_component,
                &self.sidecar,
                current_bootstrap,
                owned.header,
            )
            .map_err(LinuxReaderSlotError::Os)?;
        let current = self
            .sidecar
            .read_sidecar_slot_after_header(owned.header, owned.index)
            .map_err(LinuxReaderSlotError::Os)?;
        if decode_stable_slot(&current, SlotRole::Reader, linux_slot_host_limits())
            != Ok(StableSlot::Active(owned.active))
        {
            return Err(LinuxReaderSlotError::OwnerMismatch);
        }
        self.sidecar
            .release_lock()
            .map_err(LinuxReaderSlotError::Os)
    }

    pub(crate) fn clear_owned_reader_slot(
        &mut self,
        owned: &OwnedReaderSlot,
    ) -> Result<(), LinuxReaderSlotError> {
        if owned.header != self.header || owned.index == 0 || owned.index > self.header.capacity {
            return Err(LinuxReaderSlotError::OwnerMismatch);
        }
        let current = self
            .sidecar
            .read_sidecar_slot(owned.header, owned.index)
            .map_err(LinuxReaderSlotError::Os)?;
        if current == [0; SLOT_SIZE as usize] {
            return Ok(());
        }
        let prepared = PreparedSlotTransition::clear_owned(
            owned.header,
            SlotRole::Reader,
            owned.index,
            &current,
            owned.active,
            linux_slot_host_limits(),
        )
        .map_err(LinuxReaderSlotError::TransitionBeforeArm)?;
        self.sidecar
            .execute_sidecar_slot_transition(prepared, linux_slot_host_limits())
            .map_err(LinuxReaderSlotError::Transition)
    }

    pub(crate) fn retry_reader_slot_cleanup(
        &mut self,
        owned: Option<&OwnedReaderSlot>,
    ) -> Result<LiveClaimCleanupOutcome, LinuxReaderSlotError> {
        self.last_scan = None;
        if self.main.lock != LockState::Shared {
            return Err(LinuxReaderSlotError::Os(LinuxOsError::LifetimeLockRequired));
        }
        match self.sidecar.cleanup_authority {
            SidecarCleanupAuthority::Armed { .. } => {
                self.sidecar
                    .retry_sidecar_slot_cleanup(linux_slot_host_limits())
                    .map_err(LinuxReaderSlotError::ArmedCleanup)?;
                return Ok(self.live_cleanup_paths());
            }
            SidecarCleanupAuthority::DeadWriter(_) => {
                return Err(LinuxReaderSlotError::OutstandingWriterCleanup);
            }
            SidecarCleanupAuthority::None => {}
        }
        let owned = owned.ok_or(LinuxReaderSlotError::NoCleanupAuthority)?;
        if self.sidecar.lock == LockState::Unlocked {
            self.sidecar
                .acquire_lock(LockMode::Exclusive, false)
                .map_err(LinuxReaderSlotError::Os)?;
        }
        self.clear_owned_reader_slot(owned)?;
        Ok(self.live_cleanup_paths())
    }

    pub(crate) fn retry_writer_lease_cleanup(
        &mut self,
        owned: Option<&OwnedWriterLease>,
    ) -> Result<LiveClaimCleanupOutcome, LinuxWriterLeaseError> {
        self.retry_writer_lease_cleanup_with(
            owned,
            |file, length| file.set_len(length),
            |file| file.sync_all(),
        )
    }

    fn retry_writer_lease_cleanup_with(
        &mut self,
        owned: Option<&OwnedWriterLease>,
        truncate: impl FnMut(&File, u64) -> io::Result<()>,
        synchronize: impl FnMut(&File) -> io::Result<()>,
    ) -> Result<LiveClaimCleanupOutcome, LinuxWriterLeaseError> {
        self.last_scan = None;
        if self.main.lock != LockState::Shared {
            return Err(LinuxWriterLeaseError::Os(
                LinuxOsError::LifetimeLockRequired,
            ));
        }
        if self.sidecar.lock == LockState::Unlocked {
            self.sidecar
                .acquire_lock(LockMode::Exclusive, false)
                .map_err(LinuxWriterLeaseError::Os)?;
        }

        if self.writer_publication.is_some() {
            return self.retry_writer_publication_cleanup_with(owned, truncate, synchronize);
        }

        match (&self.sidecar.cleanup_authority, owned) {
            (
                SidecarCleanupAuthority::Armed {
                    dead_writer: Some(_),
                    ..
                }
                | SidecarCleanupAuthority::DeadWriter(_),
                _,
            ) => return Err(LinuxWriterLeaseError::OutstandingWriterCleanup),
            (SidecarCleanupAuthority::Armed { transition, .. }, _) => {
                let header = self
                    .sidecar
                    .read_ready_sidecar_header(Some(transition.header()))
                    .map_err(LinuxWriterLeaseError::Os)?;
                let current = self
                    .sidecar
                    .read_sidecar_slot_after_header(header, transition.slot_index())
                    .map_err(LinuxWriterLeaseError::Os)?;
                if current == [0; SLOT_SIZE as usize] {
                    self.require_exact_zero_main_length()?;
                }
            }
            (SidecarCleanupAuthority::None, Some(owned)) => {
                let current = self
                    .sidecar
                    .read_sidecar_slot(owned.header, 0)
                    .map_err(LinuxWriterLeaseError::Os)?;
                if current == [0; SLOT_SIZE as usize] {
                    self.require_exact_zero_main_length()?;
                    if self.writer_tail.is_none() {
                        self.writer_bootstrap = None;
                        return Ok(self.live_cleanup_paths());
                    }
                }
            }
            (SidecarCleanupAuthority::None, None) => {}
        }

        self.freeze_owned_writer_tail(owned)?;
        self.resolve_owned_writer_tail_with(owned, truncate, synchronize)?;
        self.freeze_owned_writer_tail(owned)?;
        if self.writer_tail.is_some() {
            return Err(LinuxWriterLeaseError::TailCleanupRequired);
        }
        match self.sidecar.cleanup_authority {
            SidecarCleanupAuthority::Armed { .. } => {
                self.sidecar
                    .retry_sidecar_slot_cleanup(linux_slot_host_limits())
                    .map_err(LinuxWriterLeaseError::ArmedCleanup)?;
            }
            SidecarCleanupAuthority::DeadWriter(_) => {
                return Err(LinuxWriterLeaseError::OutstandingWriterCleanup);
            }
            SidecarCleanupAuthority::None => {
                let Some(owned) = owned else {
                    return Err(LinuxWriterLeaseError::NoCleanupAuthority);
                };
                self.clear_owned_writer_lease(owned)?;
            }
        }
        self.writer_bootstrap = None;
        Ok(self.live_cleanup_paths())
    }

    fn retry_writer_publication_cleanup_with(
        &mut self,
        owned: Option<&OwnedWriterLease>,
        truncate: impl FnMut(&File, u64) -> io::Result<()>,
        synchronize: impl FnMut(&File) -> io::Result<()>,
    ) -> Result<LiveClaimCleanupOutcome, LinuxWriterLeaseError> {
        let publication = self
            .writer_publication
            .ok_or(LinuxWriterLeaseError::CommitPublicationState)?;
        let owned = owned.ok_or(LinuxWriterLeaseError::NoCleanupAuthority)?;
        if owned.header != self.header
            || owned.active != publication.source_active
            || owned.claimed_bootstrap != publication.source
        {
            return Err(LinuxWriterLeaseError::OwnerMismatch);
        }
        let current = self
            .main
            .read_main_bootstrap(OpenMode::Writer)
            .map_err(LinuxWriterLeaseError::Os)?;
        self.verify_live_pair_binding(current, owned.header)
            .map_err(LinuxWriterLeaseError::Os)?;
        if is_same_writer_bootstrap(publication.source, current) {
            if matches!(
                publication.phase,
                WriterCommitPublicationPhase::MetaSynchronized { .. }
            ) {
                return Err(LinuxWriterLeaseError::CommitOutcomeUnresolved);
            }
            return self.retry_writer_publication_source_cleanup(
                publication,
                owned,
                truncate,
                synchronize,
            );
        }
        let target_or_later = matches!(
            publication.phase,
            WriterCommitPublicationPhase::MetaWriteStarted
                | WriterCommitPublicationPhase::MetaSynchronized { .. }
        ) && (is_exact_writer_target(publication.target, current)
            || is_later_writer_target(publication.source, publication.target, current));
        if !target_or_later {
            return Err(LinuxWriterLeaseError::CommitOutcomeUnresolved);
        }
        if is_exact_writer_target(publication.target, current)
            && current.physical_bytes != current.committed_bytes
        {
            return Err(LinuxWriterLeaseError::TailCleanupRequired);
        }
        if let Some(tail) = self.writer_tail {
            if !is_writer_tail_for_publication(tail, self.main.identity, publication)? {
                return Err(LinuxWriterLeaseError::GenerationChanged);
            }
        }
        self.clear_published_writer_lease(publication, owned)?;
        self.writer_tail = None;
        self.writer_bootstrap = None;
        self.writer_publication = None;
        Ok(self.live_cleanup_paths())
    }

    fn retry_writer_publication_source_cleanup(
        &mut self,
        publication: WriterCommitPublication,
        owned: &OwnedWriterLease,
        truncate: impl FnMut(&File, u64) -> io::Result<()>,
        synchronize: impl FnMut(&File) -> io::Result<()>,
    ) -> Result<LiveClaimCleanupOutcome, LinuxWriterLeaseError> {
        self.writer_publication = None;
        let result = self.retry_writer_lease_cleanup_with(Some(owned), truncate, synchronize);
        if result.is_err() {
            self.writer_publication = Some(publication);
        }
        result
    }

    fn clear_published_writer_lease(
        &mut self,
        publication: WriterCommitPublication,
        owned: &OwnedWriterLease,
    ) -> Result<(), LinuxWriterLeaseError> {
        let target_active = publication.target_active();
        match &self.sidecar.cleanup_authority {
            SidecarCleanupAuthority::Armed {
                transition,
                dead_writer: None,
            } => {
                if transition.header() != self.header
                    || transition.role() != SlotRole::Writer
                    || transition.slot_index() != 0
                    || transition.kind() != SlotTransitionKind::Update
                    || transition.source()
                        != SlotTransitionSource::OwnedActive(publication.source_active)
                    || transition.target() != Some(target_active)
                {
                    return Err(LinuxWriterLeaseError::OwnerMismatch);
                }
                self.sidecar
                    .retry_sidecar_slot_cleanup(linux_slot_host_limits())
                    .map_err(LinuxWriterLeaseError::ArmedCleanup)?;
                Ok(())
            }
            SidecarCleanupAuthority::Armed { .. } | SidecarCleanupAuthority::DeadWriter(_) => {
                Err(LinuxWriterLeaseError::OutstandingWriterCleanup)
            }
            SidecarCleanupAuthority::None => {
                let current = self
                    .sidecar
                    .read_sidecar_slot(owned.header, 0)
                    .map_err(LinuxWriterLeaseError::Os)?;
                if current == [0; SLOT_SIZE as usize] {
                    return Ok(());
                }
                let active = match decode_stable_slot(
                    &current,
                    SlotRole::Writer,
                    linux_slot_host_limits(),
                ) {
                    Ok(StableSlot::Active(active))
                        if active == publication.source_active || active == target_active =>
                    {
                        active
                    }
                    Ok(StableSlot::Free) | Ok(StableSlot::Active(_)) | Err(_) => {
                        return Err(LinuxWriterLeaseError::OwnerMismatch);
                    }
                };
                let prepared = PreparedSlotTransition::clear_owned(
                    owned.header,
                    SlotRole::Writer,
                    0,
                    &current,
                    active,
                    linux_slot_host_limits(),
                )
                .map_err(LinuxWriterLeaseError::TransitionBeforeArm)?;
                self.sidecar
                    .execute_sidecar_slot_transition_after_tail(prepared, linux_slot_host_limits())
                    .map_err(LinuxWriterLeaseError::Transition)
            }
        }
    }

    fn require_exact_zero_main_length(&self) -> Result<(), LinuxWriterLeaseError> {
        let claimed = self
            .writer_bootstrap
            .ok_or(LinuxWriterLeaseError::NoCleanupAuthority)?;
        let metadata = self.main.file.metadata().map_err(|source| {
            LinuxWriterLeaseError::Os(LinuxOsError::Io {
                operation: "inspect retained writer main",
                source,
            })
        })?;
        validate_regular(&metadata).map_err(LinuxWriterLeaseError::Os)?;
        if metadata_identity(&metadata) != self.main.identity {
            return Err(LinuxWriterLeaseError::Os(
                LinuxOsError::PathIdentityMismatch,
            ));
        }
        let actual = metadata.len();
        if actual != claimed.committed_bytes {
            return Err(LinuxWriterLeaseError::TailLengthConflict {
                target: claimed.committed_bytes,
                observed_end: actual.max(claimed.committed_bytes),
                actual,
            });
        }
        Ok(())
    }

    fn freeze_owned_writer_tail(
        &mut self,
        owned: Option<&OwnedWriterLease>,
    ) -> Result<Bootstrap, LinuxWriterLeaseError> {
        self.sidecar
            .require_exclusive_lock()
            .map_err(LinuxWriterLeaseError::Os)?;
        self.sidecar
            .read_ready_sidecar_header(Some(self.header))
            .map_err(LinuxWriterLeaseError::Os)?;
        let claimed = self
            .writer_bootstrap
            .ok_or(LinuxWriterLeaseError::NoCleanupAuthority)?;
        let has_destructive_authority = match &self.sidecar.cleanup_authority {
            SidecarCleanupAuthority::Armed { transition, .. }
                if transition.role() == SlotRole::Writer
                    && transition.slot_index() == 0
                    && transition.header() == self.header =>
            {
                true
            }
            SidecarCleanupAuthority::Armed { .. } => {
                return Err(LinuxWriterLeaseError::OwnerMismatch);
            }
            SidecarCleanupAuthority::DeadWriter(_) => {
                return Err(LinuxWriterLeaseError::OutstandingWriterCleanup);
            }
            SidecarCleanupAuthority::None => {
                let owned = owned.ok_or(LinuxWriterLeaseError::NoCleanupAuthority)?;
                self.verify_owned_writer_for_cleanup(owned)?
            }
        };

        let current = self
            .main
            .read_main_bootstrap(OpenMode::Writer)
            .map_err(LinuxWriterLeaseError::Os)?;
        require_exact_writer_bootstrap(claimed, current)?;
        self.directory
            .verify_live_pair_descriptor_binding(
                &self.main_component,
                &self.main,
                &self.sidecar,
                current,
                self.header,
            )
            .map_err(LinuxWriterLeaseError::Os)?;

        if let Some(tail) = self.writer_tail {
            if !has_destructive_authority {
                return Err(LinuxWriterLeaseError::OwnerMismatch);
            }
            require_same_owned_tail_generation(tail, self.main.identity, current)?;
        } else if current.physical_bytes > current.committed_bytes {
            if !has_destructive_authority {
                return Err(LinuxWriterLeaseError::OwnerMismatch);
            }
            self.writer_tail = Some(UnpublishedMainTail {
                main_identity: self.main.identity,
                database_id: claimed.meta.database_id,
                transaction_id: claimed.meta.txn_id,
                commit_nonce: claimed.meta.commit_nonce,
                committed_length: claimed.committed_bytes,
                observed_end_exclusive: current.physical_bytes,
            });
        }
        Ok(current)
    }

    fn resolve_owned_writer_tail_with(
        &mut self,
        owned: Option<&OwnedWriterLease>,
        mut truncate: impl FnMut(&File, u64) -> io::Result<()>,
        mut synchronize: impl FnMut(&File) -> io::Result<()>,
    ) -> Result<(), LinuxWriterLeaseError> {
        self.freeze_owned_writer_tail(owned)?;
        let Some(tail) = self.writer_tail else {
            return Ok(());
        };
        let claimed = self
            .writer_bootstrap
            .ok_or(LinuxWriterLeaseError::NoCleanupAuthority)?;

        let metadata = self.main.file.metadata().map_err(|source| {
            LinuxWriterLeaseError::Os(LinuxOsError::Io {
                operation: "inspect retained writer tail",
                source,
            })
        })?;
        validate_regular(&metadata).map_err(LinuxWriterLeaseError::Os)?;
        if metadata_identity(&metadata) != tail.main_identity {
            return Err(LinuxWriterLeaseError::GenerationChanged);
        }
        if metadata.len() < tail.committed_length || metadata.len() > tail.observed_end_exclusive {
            return Err(LinuxWriterLeaseError::TailLengthConflict {
                target: tail.committed_length,
                observed_end: tail.observed_end_exclusive,
                actual: metadata.len(),
            });
        }
        let before = self
            .main
            .read_main_bootstrap(OpenMode::Writer)
            .map_err(LinuxWriterLeaseError::Os)?;
        require_exact_writer_bootstrap(claimed, before)?;
        require_same_owned_tail_generation(tail, self.main.identity, before)?;
        self.directory
            .verify_live_pair_binding(
                &self.main_component,
                &self.main,
                &self.sidecar_component,
                &self.sidecar,
                before,
                self.header,
            )
            .map_err(LinuxWriterLeaseError::Os)?;

        if before.physical_bytes > before.committed_bytes {
            self.sidecar
                .read_ready_sidecar_header(Some(self.header))
                .map_err(LinuxWriterLeaseError::Os)?;
            truncate(&self.main.file, before.committed_bytes).map_err(|source| {
                LinuxWriterLeaseError::Os(LinuxOsError::Io {
                    operation: "truncate owned unpublished main tail",
                    source,
                })
            })?;
        }
        synchronize(&self.main.file).map_err(|source| {
            LinuxWriterLeaseError::Os(LinuxOsError::Io {
                operation: "synchronize owned main tail cleanup",
                source,
            })
        })?;
        let after = self
            .main
            .read_main_bootstrap(OpenMode::Writer)
            .map_err(LinuxWriterLeaseError::Os)?;
        require_exact_writer_bootstrap(claimed, after)?;
        if after.physical_bytes != claimed.committed_bytes {
            return Err(LinuxWriterLeaseError::GenerationChanged);
        }
        self.directory
            .verify_live_pair_binding(
                &self.main_component,
                &self.main,
                &self.sidecar_component,
                &self.sidecar,
                after,
                self.header,
            )
            .map_err(LinuxWriterLeaseError::Os)?;
        let final_bootstrap = self.freeze_owned_writer_tail(owned)?;
        if final_bootstrap.physical_bytes != claimed.committed_bytes {
            return Err(LinuxWriterLeaseError::TailCleanupRequired);
        }
        self.writer_tail = None;
        Ok(())
    }

    fn clear_owned_writer_lease(
        &mut self,
        owned: &OwnedWriterLease,
    ) -> Result<(), LinuxWriterLeaseError> {
        if owned.header != self.header || owned.active.txn_id == 0 {
            return Err(LinuxWriterLeaseError::OwnerMismatch);
        }
        let current = self
            .sidecar
            .read_sidecar_slot(owned.header, 0)
            .map_err(LinuxWriterLeaseError::Os)?;
        if current == [0; SLOT_SIZE as usize] {
            self.require_exact_zero_main_length()?;
            if self.writer_tail.is_some() || self.sidecar.has_armed_transition() {
                return Err(LinuxWriterLeaseError::TailCleanupRequired);
            }
            return Ok(());
        }
        if self.writer_tail.is_some() {
            return Err(LinuxWriterLeaseError::TailCleanupRequired);
        }
        let bootstrap = self.freeze_owned_writer_tail(Some(owned))?;
        if bootstrap.physical_bytes != bootstrap.committed_bytes || self.writer_tail.is_some() {
            return Err(LinuxWriterLeaseError::TailCleanupRequired);
        }
        let current = self
            .sidecar
            .read_sidecar_slot(owned.header, 0)
            .map_err(LinuxWriterLeaseError::Os)?;
        if current == [0; SLOT_SIZE as usize] {
            return Ok(());
        }
        let prepared = PreparedSlotTransition::clear_owned(
            owned.header,
            SlotRole::Writer,
            0,
            &current,
            owned.active,
            linux_slot_host_limits(),
        )
        .map_err(LinuxWriterLeaseError::TransitionBeforeArm)?;
        self.sidecar
            .execute_sidecar_slot_transition_after_tail(prepared, linux_slot_host_limits())
            .map_err(LinuxWriterLeaseError::Transition)
    }

    pub(crate) fn live_cleanup_paths(&self) -> LiveClaimCleanupOutcome {
        LiveClaimCleanupOutcome {
            main_path: self.directory.verify_path(&self.main_component, &self.main),
            sidecar_path: self
                .directory
                .verify_path(&self.sidecar_component, &self.sidecar),
        }
    }

    pub(crate) const fn writer_tail(&self) -> Option<UnpublishedMainTail> {
        self.writer_tail
    }

    pub(crate) const fn writer_bootstrap(&self) -> Option<Bootstrap> {
        self.writer_bootstrap
    }

    #[cfg(test)]
    fn set_writer_tail_for_test(&mut self, tail: Option<UnpublishedMainTail>) {
        self.writer_tail = tail;
    }

    #[cfg(test)]
    fn interrupt_dead_writer_clear_for_test(&mut self, completed_writes: u8) {
        assert!(completed_writes <= 3);
        let mut dead = match self.sidecar.cleanup_authority {
            SidecarCleanupAuthority::DeadWriter(dead) => dead,
            _ => panic!("dead-writer authority required"),
        };
        let header = self
            .sidecar
            .read_ready_sidecar_header(Some(dead.header))
            .unwrap();
        let source = self
            .sidecar
            .read_sidecar_slot_after_header(header, 0)
            .unwrap();
        assert_eq!(source, dead.source_image);
        let bootstrap = self.main.read_main_bootstrap(OpenMode::Writer).unwrap();
        dead.bootstrap = Some(bootstrap);
        if bootstrap.physical_bytes > bootstrap.committed_bytes {
            dead.tail = Some(UnpublishedMainTail {
                main_identity: self.main.identity,
                database_id: bootstrap.meta.database_id,
                transaction_id: bootstrap.meta.txn_id,
                commit_nonce: bootstrap.meta.commit_nonce,
                committed_length: bootstrap.committed_bytes,
                observed_end_exclusive: bootstrap.physical_bytes,
            });
            self.main.file.set_len(bootstrap.committed_bytes).unwrap();
        }
        self.main.file.sync_all().unwrap();
        let prepared = PreparedSlotTransition::clear_proven_dead(
            header,
            SlotRole::Writer,
            0,
            &source,
            dead.active,
            dead.proof,
            linux_slot_host_limits(),
        )
        .unwrap();
        let transition = prepared.arm();
        let offset = sidecar_slot_offset(header, 0).unwrap();
        if completed_writes >= 1 {
            self.sidecar
                .write_all_at(&transition.state2_bytes().unwrap(), offset)
                .unwrap();
        }
        if completed_writes >= 2 {
            self.sidecar
                .write_all_at(&transition.body_bytes().unwrap(), offset + 4)
                .unwrap();
        }
        if completed_writes >= 3 {
            self.sidecar
                .write_all_at(&transition.publish_state_bytes().unwrap(), offset)
                .unwrap();
        }
        self.sidecar.cleanup_authority = SidecarCleanupAuthority::Armed {
            transition,
            dead_writer: Some(dead),
        };
    }

    pub(crate) fn scanned_bootstrap(&self) -> Option<Bootstrap> {
        self.last_scan.as_ref().map(|(bootstrap, _)| *bootstrap)
    }
}

fn require_same_tail_generation(
    tail: UnpublishedMainTail,
    identity: PosixIdentity,
    bootstrap: Bootstrap,
) -> Result<(), LinuxLivePairError> {
    if tail.main_identity != identity
        || tail.database_id != bootstrap.meta.database_id
        || tail.transaction_id != bootstrap.meta.txn_id
        || tail.commit_nonce != bootstrap.meta.commit_nonce
        || tail.committed_length != bootstrap.committed_bytes
    {
        return Err(LinuxLivePairError::MainGenerationChanged);
    }
    if bootstrap.physical_bytes < tail.committed_length
        || bootstrap.physical_bytes > tail.observed_end_exclusive
    {
        return Err(LinuxLivePairError::TailLengthConflict {
            target: tail.committed_length,
            observed_end: tail.observed_end_exclusive,
            actual: bootstrap.physical_bytes,
        });
    }
    Ok(())
}

fn armed_transition_is_dead_writer_clear(
    transition: &ArmedSlotTransition,
    dead: ProvenDeadWriter,
) -> bool {
    transition.header() == dead.header
        && transition.role() == SlotRole::Writer
        && transition.slot_index() == 0
        && transition.kind() == SlotTransitionKind::Clear
        && transition.source()
            == SlotTransitionSource::ProvenDeadActive {
                active: dead.active,
                proof: dead.proof,
            }
}

fn require_exact_dead_writer_bootstrap(
    expected: Bootstrap,
    actual: Bootstrap,
) -> Result<(), LinuxLivePairError> {
    if actual.meta != expected.meta
        || actual.selection != expected.selection
        || actual.selected_meta_page != expected.selected_meta_page
        || actual.committed_bytes != expected.committed_bytes
    {
        return Err(LinuxLivePairError::MainGenerationChanged);
    }
    Ok(())
}

fn require_same_owned_tail_generation(
    tail: UnpublishedMainTail,
    identity: PosixIdentity,
    bootstrap: Bootstrap,
) -> Result<(), LinuxWriterLeaseError> {
    if tail.main_identity != identity
        || tail.database_id != bootstrap.meta.database_id
        || tail.transaction_id != bootstrap.meta.txn_id
        || tail.commit_nonce != bootstrap.meta.commit_nonce
        || tail.committed_length != bootstrap.committed_bytes
    {
        return Err(LinuxWriterLeaseError::GenerationChanged);
    }
    if bootstrap.physical_bytes < tail.committed_length
        || bootstrap.physical_bytes > tail.observed_end_exclusive
    {
        return Err(LinuxWriterLeaseError::TailLengthConflict {
            target: tail.committed_length,
            observed_end: tail.observed_end_exclusive,
            actual: bootstrap.physical_bytes,
        });
    }
    Ok(())
}

fn require_exact_writer_bootstrap(
    expected: Bootstrap,
    actual: Bootstrap,
) -> Result<(), LinuxWriterLeaseError> {
    if actual.meta != expected.meta
        || actual.selection != expected.selection
        || actual.selected_meta_page != expected.selected_meta_page
        || actual.committed_bytes != expected.committed_bytes
    {
        return Err(LinuxWriterLeaseError::GenerationChanged);
    }
    Ok(())
}

fn is_exact_writer_target(target: MetaV4, actual: Bootstrap) -> bool {
    target
        .page_count
        .checked_mul(PAGE_SIZE as u64)
        .is_some_and(|bytes| {
            actual.selection == MetaSelection::ProvenCurrent
                && actual.selected_meta_page == (target.txn_id & 1) as u8
                && actual.meta == target
                && actual.committed_bytes == bytes
        })
}

fn is_same_writer_bootstrap(expected: Bootstrap, actual: Bootstrap) -> bool {
    actual.meta == expected.meta
        && actual.selection == expected.selection
        && actual.selected_meta_page == expected.selected_meta_page
        && actual.committed_bytes == expected.committed_bytes
}

/// A later selected generation proves only that the old source tail must not be
/// truncated. It is not evidence that this writer's attempted generation was
/// durable; commit resolution classifies that separately.
fn is_later_writer_target(source: Bootstrap, target: MetaV4, actual: Bootstrap) -> bool {
    actual.selection == MetaSelection::ProvenCurrent
        && actual.meta.static_identity_eq(&source.meta)
        && actual.meta.txn_id > target.txn_id
        && actual.physical_bytes == actual.committed_bytes
}

fn is_writer_tail_for_publication(
    tail: UnpublishedMainTail,
    identity: PosixIdentity,
    publication: WriterCommitPublication,
) -> Result<bool, LinuxWriterLeaseError> {
    Ok(tail.main_identity == identity
        && tail.database_id == publication.source.meta.database_id
        && tail.transaction_id == publication.source.meta.txn_id
        && tail.commit_nonce == publication.source.meta.commit_nonce
        && tail.committed_length == publication.source.committed_bytes
        && tail.observed_end_exclusive == publication.target_committed_bytes()?
        && tail.observed_end_exclusive > tail.committed_length)
}

#[derive(Debug)]
pub(crate) enum LockedSlotExecutionError {
    BeforeArm(LinuxOsError),
    TransitionBeforeArm(SlotTransitionError),
    Interrupted(InterruptedCause<LinuxOsError>),
}

#[derive(Debug)]
pub(crate) enum LinuxLivePairError {
    Os(LinuxOsError),
    Scan(LinuxSidecarScanError),
    NoDeadWriter,
    WriterSourceChanged,
    MainGenerationChanged,
    TailLengthConflict {
        target: u64,
        observed_end: u64,
        actual: u64,
    },
    TransitionBeforeArm(SlotTransitionError),
    Transition(LockedSlotExecutionError),
    ArmedCleanup(SlotCleanupError<LinuxOsError>),
    PostClearPath(LinuxOsError),
}

#[derive(Debug)]
pub(crate) enum LinuxReaderSlotError {
    ScanRequired,
    ScanChanged,
    ReaderCapacityExhausted,
    OwnerMismatch,
    GenerationChanged,
    OutstandingWriterCleanup,
    NoCleanupAuthority,
    Os(LinuxOsError),
    TransitionBeforeArm(SlotTransitionError),
    Transition(LockedSlotExecutionError),
    ArmedCleanup(SlotCleanupError<LinuxOsError>),
}

#[derive(Debug)]
pub(crate) enum LinuxWriterLeaseError {
    ScanRequired,
    ScanChanged,
    WriterBusy,
    OwnerMismatch,
    GenerationChanged,
    CommitAttemptMismatch,
    CommitPublicationState,
    CommitOutcomeUnresolved,
    TailCleanupRequired,
    TailLengthConflict {
        target: u64,
        observed_end: u64,
        actual: u64,
    },
    OutstandingWriterCleanup,
    NoCleanupAuthority,
    Os(LinuxOsError),
    TransitionBeforeArm(SlotTransitionError),
    Transition(LockedSlotExecutionError),
    ArmedCleanup(SlotCleanupError<LinuxOsError>),
}

#[derive(Debug)]
pub(crate) enum LinuxSidecarScanError {
    Os(LinuxOsError),
    ProcessDomainMismatch,
    SelectedTransactionZero,
    OutstandingProvenance,
    OutstandingWriterCleanup,
    Cancelled,
    Slot {
        index: u32,
        problem: SlotProblem,
    },
    DeadWriter {
        active: ActiveSlot,
        proof: DeathProof,
    },
    TransitionBeforeArm {
        index: u32,
        cause: SlotTransitionError,
    },
    Transition {
        index: u32,
        cause: LockedSlotExecutionError,
    },
    WriterTransactionMismatch {
        expected: u64,
        actual: u64,
    },
    ReaderTransactionFuture {
        selected: u64,
        actual: u64,
    },
    CounterOverflow,
}

const fn slot_role(index: u32) -> SlotRole {
    if index == 0 {
        SlotRole::Writer
    } else {
        SlotRole::Reader
    }
}

fn sidecar_slot_offset(header: SidecarHeader, index: u32) -> Result<u64, LinuxOsError> {
    if index > header.capacity {
        return Err(LinuxOsError::SlotOffsetOverflow);
    }
    u64::from(index)
        .checked_mul(u64::from(SLOT_SIZE))
        .and_then(|bytes| bytes.checked_add((2 * PAGE_SIZE) as u64))
        .ok_or(LinuxOsError::SlotOffsetOverflow)
}

fn read_exact_at_file(
    file: &File,
    mut bytes: &mut [u8],
    mut offset: u64,
) -> Result<(), LinuxOsError> {
    use std::os::unix::fs::FileExt;
    while !bytes.is_empty() {
        let read = file
            .read_at(bytes, offset)
            .map_err(|source| LinuxOsError::Io {
                operation: "positional read",
                source,
            })?;
        if read == 0 {
            return Err(LinuxOsError::Io {
                operation: "positional read",
                source: io::Error::from(io::ErrorKind::UnexpectedEof),
            });
        }
        offset = offset
            .checked_add(u64::try_from(read).map_err(|_| LinuxOsError::OffsetOverflow)?)
            .ok_or(LinuxOsError::OffsetOverflow)?;
        bytes = &mut bytes[read..];
    }
    Ok(())
}

fn write_all_at_file(file: &File, mut bytes: &[u8], mut offset: u64) -> Result<(), LinuxOsError> {
    use std::os::unix::fs::FileExt;
    while !bytes.is_empty() {
        let written = file
            .write_at(bytes, offset)
            .map_err(|source| LinuxOsError::Io {
                operation: "positional write",
                source,
            })?;
        if written == 0 {
            return Err(LinuxOsError::Io {
                operation: "positional write",
                source: io::Error::from(io::ErrorKind::WriteZero),
            });
        }
        offset = offset
            .checked_add(u64::try_from(written).map_err(|_| LinuxOsError::OffsetOverflow)?)
            .ok_or(LinuxOsError::OffsetOverflow)?;
        bytes = &bytes[written..];
    }
    Ok(())
}

pub(crate) fn random_nonzero_128() -> Result<[u8; 16], LinuxOsError> {
    random_nonzero_128_with(|bytes| getrandom::fill(bytes).map_err(|_| ()))
}

fn random_nonzero_128_with(
    fill: impl FnOnce(&mut [u8]) -> Result<(), ()>,
) -> Result<[u8; 16], LinuxOsError> {
    let mut bytes = [0u8; 16];
    fill(&mut bytes).map_err(|()| LinuxOsError::RandomFailure)?;
    if bytes == [0; 16] {
        return Err(LinuxOsError::RandomZero);
    }
    Ok(bytes)
}

pub(crate) fn linux_process_domain_token() -> Result<[u8; 32], LinuxOsError> {
    let metadata = std::fs::metadata("/proc/self/ns/pid").map_err(|source| LinuxOsError::Io {
        operation: "inspect Linux PID namespace",
        source,
    })?;
    Ok(metadata_identity(&metadata).encode())
}

pub(crate) const fn linux_slot_host_limits() -> SlotHostLimits {
    SlotHostLimits {
        process_id_max: i32::MAX as u64,
        task_id_max: i32::MAX as u64,
    }
}

pub(crate) fn current_active_slot(txn_id: u64, nonce: [u8; 16]) -> ActiveSlot {
    let process_id = u64::from(std::process::id());
    ActiveSlot {
        txn_id,
        process_id,
        process_start: read_linux_process_start(process_id).unwrap_or(0),
        task_id: current_linux_task_id(),
        nonce,
    }
}

pub(crate) fn observe_posix_process(active: ActiveSlot) -> PosixProcessObservation {
    let Ok(pid) = libc::pid_t::try_from(active.process_id) else {
        return PosixProcessObservation::Uncertain;
    };
    if unsafe { libc::kill(pid, 0) } == 0 {
        return PosixProcessObservation::Exists {
            current_start: read_linux_process_start(active.process_id),
        };
    }
    match io::Error::last_os_error().raw_os_error() {
        Some(libc::ESRCH) => PosixProcessObservation::Missing,
        Some(libc::EPERM) => PosixProcessObservation::Exists {
            current_start: read_linux_process_start(active.process_id),
        },
        _ => PosixProcessObservation::Uncertain,
    }
}

fn read_linux_process_start(process_id: u64) -> Option<u64> {
    if process_id == 0 || process_id > i32::MAX as u64 {
        return None;
    }
    let mut path = [0u8; 64];
    let mut used_path = 0usize;
    append_stack_path(&mut path, &mut used_path, b"/proc/")?;
    append_stack_decimal(&mut path, &mut used_path, process_id)?;
    append_stack_path(&mut path, &mut used_path, b"/stat\0")?;
    let fd = unsafe {
        libc::open(
            path.as_ptr().cast(),
            libc::O_RDONLY | libc::O_CLOEXEC | libc::O_NONBLOCK,
        )
    };
    if fd < 0 {
        return None;
    }
    let mut file = unsafe { File::from_raw_fd(fd) };
    let mut bytes = [0u8; 4096];
    let mut used = 0usize;
    loop {
        if used == bytes.len() {
            return None;
        }
        let read = file.read(&mut bytes[used..]).ok()?;
        if read == 0 {
            break;
        }
        used += read;
    }
    parse_linux_proc_stat_start(&bytes[..used]).ok()
}

fn append_stack_path(target: &mut [u8], used: &mut usize, bytes: &[u8]) -> Option<()> {
    let end = used.checked_add(bytes.len())?;
    target.get_mut(*used..end)?.copy_from_slice(bytes);
    *used = end;
    Some(())
}

fn append_stack_decimal(target: &mut [u8], used: &mut usize, mut value: u64) -> Option<()> {
    let mut reversed = [0u8; 20];
    let mut digits = 0usize;
    while value != 0 {
        reversed[digits] = b'0' + u8::try_from(value % 10).ok()?;
        digits += 1;
        value /= 10;
    }
    let end = used.checked_add(digits)?;
    let destination = target.get_mut(*used..end)?;
    for (index, byte) in destination.iter_mut().enumerate() {
        *byte = reversed[digits - index - 1];
    }
    *used = end;
    Some(())
}

fn current_linux_task_id() -> u64 {
    let task = unsafe { libc::syscall(libc::SYS_gettid) };
    u64::try_from(task)
        .ok()
        .filter(|&value| value != 0)
        .unwrap_or(0)
}

fn validate_component(component: &OsStr) -> Result<(), LinuxOsError> {
    let bytes = component.as_bytes();
    if bytes.is_empty()
        || bytes == b"."
        || bytes == b".."
        || bytes.contains(&b'/')
        || bytes.contains(&0)
    {
        return Err(LinuxOsError::InvalidPathComponent);
    }
    Ok(())
}

fn validate_main_component(component: &OsStr) -> Result<(), LinuxOsError> {
    validate_component(component)?;
    let bytes = component.as_bytes();
    if bytes
        .get(..9)
        .is_some_and(|prefix| prefix.eq_ignore_ascii_case(b".iprange-"))
        || bytes
            .get(bytes.len().saturating_sub(8)..)
            .is_some_and(|suffix| suffix.eq_ignore_ascii_case(b".readers"))
    {
        return Err(LinuxOsError::InvalidPathComponent);
    }
    Ok(())
}

fn component_c_string(component: &OsStr) -> Result<CString, LinuxOsError> {
    validate_component(component)?;
    CString::new(component.as_bytes()).map_err(|_| LinuxOsError::InvalidPathComponent)
}

fn validate_regular(metadata: &Metadata) -> Result<(), LinuxOsError> {
    if !metadata.is_file() {
        return Err(LinuxOsError::NotRegular);
    }
    if metadata.nlink() != 1 {
        return Err(LinuxOsError::LinkCountNotOne(metadata.nlink()));
    }
    Ok(())
}

fn require_supported_local_filesystem(file: &File) -> Result<(), LinuxOsError> {
    let mut stat = std::mem::MaybeUninit::<libc::statfs>::uninit();
    if unsafe { libc::fstatfs(file.as_raw_fd(), stat.as_mut_ptr()) } != 0 {
        return Err(LinuxOsError::Io {
            operation: "inspect live-coordination filesystem",
            source: io::Error::last_os_error(),
        });
    }
    let filesystem = low_u32(unsafe { stat.assume_init() }.f_type);
    // Phase 1 is deliberately conservative. These are local, inode-based Linux
    // filesystems with flock, retained-descriptor identity, and fsync support.
    const EXT: u32 = 0x0000_ef53;
    const XFS: u32 = 0x5846_5342;
    const BTRFS: u32 = 0x9123_683e;
    const F2FS: u32 = 0xf2f5_2010;
    const ZFS: u32 = 0x2fc1_2fc1;
    const BCACHEFS: u32 = 0xca45_1a4e;
    if matches!(filesystem, EXT | XFS | BTRFS | F2FS | ZFS | BCACHEFS) {
        Ok(())
    } else {
        Err(LinuxOsError::UnsupportedFilesystem(filesystem))
    }
}

fn metadata_identity(metadata: &Metadata) -> PosixIdentity {
    PosixIdentity {
        device: metadata.dev(),
        inode: metadata.ino(),
    }
}

fn unsigned_u64<T: Into<u64>>(value: T) -> u64 {
    value.into()
}

trait LowU32 {
    fn low_u32(self) -> u32;
}

impl LowU32 for i32 {
    fn low_u32(self) -> u32 {
        self as u32
    }
}

impl LowU32 for i64 {
    fn low_u32(self) -> u32 {
        self as u32
    }
}

impl LowU32 for u32 {
    fn low_u32(self) -> u32 {
        self
    }
}

impl LowU32 for u64 {
    fn low_u32(self) -> u32 {
        self as u32
    }
}

fn low_u32<T: LowU32>(value: T) -> u32 {
    value.low_u32()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bootstrap::tests::empty_direct_meta;
    use crate::sidecar::{
        decode_stable_slot, encode_active_slot, ProcessDomainKind, SidecarOrigin, SlotRole,
        StableSlot,
    };
    use std::cell::Cell;
    use std::io::Write;
    use std::os::unix::fs::symlink;
    use std::sync::atomic::{AtomicU64, Ordering};

    static NEXT_DIRECTORY: AtomicU64 = AtomicU64::new(1);

    #[derive(Debug)]
    struct TestDirectory(PathBuf);

    impl TestDirectory {
        fn new() -> Self {
            let ordinal = NEXT_DIRECTORY.fetch_add(1, Ordering::Relaxed);
            let path = std::env::temp_dir()
                .join(format!("iprange-v4-os-{}-{ordinal}", std::process::id()));
            std::fs::create_dir(&path).unwrap();
            Self(path)
        }
    }

    impl Drop for TestDirectory {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }

    fn ready_sidecar(capacity: u32) -> (TestDirectory, RetainedRegular, SidecarHeader) {
        let directory = TestDirectory::new();
        let main = directory.0.join("main.iprdb");
        let sidecar_path = directory.0.join("main.iprdb.readers");
        let (parent, main_component) = RetainedDirectory::open_parent(&main).unwrap();
        let sidecar_component = parent.sidecar_component(&main_component).unwrap();
        let created = File::create(&sidecar_path).unwrap();
        created
            .set_len(8192 + (u64::from(capacity) + 1) * u64::from(SLOT_SIZE))
            .unwrap();
        drop(created);
        let sidecar = parent.open_regular(&sidecar_component, true).unwrap();
        let mut main_identity = sidecar.identity().encode();
        main_identity[0] ^= 1;
        let header = SidecarHeader {
            identity_kind: LocalIdentityKind::Posix,
            capacity,
            state: SidecarState::Ready,
            database_id: [1; 16],
            main_identity,
            sidecar_identity: sidecar.identity().encode(),
            sidecar_id: [2; 16],
            origin: SidecarOrigin::CreateLive,
            attempted_txn_id: 1,
            attempted_commit_nonce: [3; 16],
            attempted_main_bytes: 8192,
            attempted_main_sha512: [4; 64],
            process_domain_kind: ProcessDomainKind::LinuxPidNamespace,
            process_domain_token: linux_process_domain_token().unwrap(),
            basename_encoding: 1,
            basename_len: main_component.as_bytes().len() as u32,
            basename_commitment: [5; 32],
            creation_security_kind: 1,
            creation_security_commitment: [6; 32],
            header_seq: 1,
        };
        let mut block = [0u8; PAGE_SIZE];
        header.encode_into(&mut block);
        sidecar.write_all_at(&block, 0).unwrap();
        sidecar.write_all_at(&block, PAGE_SIZE as u64).unwrap();
        (directory, sidecar, header)
    }

    fn active(txn_id: u64, process_id: u64, nonce: u8) -> ActiveSlot {
        ActiveSlot {
            txn_id,
            process_id,
            process_start: process_id + 100,
            task_id: process_id + 200,
            nonce: [nonce; 16],
        }
    }

    fn write_active(
        sidecar: &RetainedRegular,
        header: SidecarHeader,
        index: u32,
        value: ActiveSlot,
    ) {
        sidecar
            .write_all_at(
                &encode_active_slot(value),
                sidecar_slot_offset(header, index).unwrap(),
            )
            .unwrap();
    }

    #[derive(Clone, Copy, Debug)]
    enum HeaderMutation {
        ValidChanged,
        Malformed,
        TornUpdate,
    }

    fn mutate_header(sidecar: &RetainedRegular, expected: SidecarHeader, mutation: HeaderMutation) {
        let mut original = [0u8; PAGE_SIZE];
        expected.encode_into(&mut original);
        let mut changed_header = expected;
        changed_header.header_seq += 1;
        let mut changed = [0u8; PAGE_SIZE];
        changed_header.encode_into(&mut changed);
        match mutation {
            HeaderMutation::ValidChanged => {
                sidecar.write_all_at(&changed, 0).unwrap();
                sidecar.write_all_at(&changed, PAGE_SIZE as u64).unwrap();
            }
            HeaderMutation::Malformed => {
                sidecar.write_all_at(&[0; PAGE_SIZE], 0).unwrap();
                sidecar
                    .write_all_at(&[0; PAGE_SIZE], PAGE_SIZE as u64)
                    .unwrap();
            }
            HeaderMutation::TornUpdate => {
                sidecar.write_all_at(&original, 0).unwrap();
                sidecar.write_all_at(&changed, PAGE_SIZE as u64).unwrap();
            }
        }
    }

    fn restore_header(sidecar: &RetainedRegular, header: SidecarHeader) {
        let mut block = [0u8; PAGE_SIZE];
        header.encode_into(&mut block);
        sidecar.write_all_at(&block, 0).unwrap();
        sidecar.write_all_at(&block, PAGE_SIZE as u64).unwrap();
    }

    fn live_pair_with_dead_writer(
        physical_pages: usize,
    ) -> (TestDirectory, RetainedLiveFiles, ActiveSlot) {
        assert!(physical_pages >= 2);
        let directory = TestDirectory::new();
        let main_path = directory.0.join("main.iprdb");
        let sidecar_path = directory.0.join("main.iprdb.readers");
        let meta = empty_direct_meta(1);
        let mut main_bytes = vec![0u8; physical_pages * PAGE_SIZE];
        meta.encode_into((&mut main_bytes[..PAGE_SIZE]).try_into().unwrap());
        meta.encode_into(
            (&mut main_bytes[PAGE_SIZE..2 * PAGE_SIZE])
                .try_into()
                .unwrap(),
        );
        std::fs::write(&main_path, main_bytes).unwrap();
        let (parent, main_component) = RetainedDirectory::open_parent(&main_path).unwrap();
        let sidecar_component = parent.sidecar_component(&main_component).unwrap();
        let created = File::create(&sidecar_path).unwrap();
        created.set_len(8192 + 2 * u64::from(SLOT_SIZE)).unwrap();
        drop(created);
        let main = parent.open_regular(&main_component, true).unwrap();
        let sidecar = parent.open_regular(&sidecar_component, true).unwrap();
        let header = SidecarHeader {
            identity_kind: LocalIdentityKind::Posix,
            capacity: 1,
            state: SidecarState::Ready,
            database_id: meta.database_id,
            main_identity: main.identity().encode(),
            sidecar_identity: sidecar.identity().encode(),
            sidecar_id: [2; 16],
            origin: SidecarOrigin::CreateLive,
            attempted_txn_id: 1,
            attempted_commit_nonce: [3; 16],
            attempted_main_bytes: 8192,
            attempted_main_sha512: [4; 64],
            process_domain_kind: ProcessDomainKind::LinuxPidNamespace,
            process_domain_token: linux_process_domain_token().unwrap(),
            basename_encoding: BasenameEncoding::PosixBytes as u16,
            basename_len: main_component.as_bytes().len() as u32,
            basename_commitment: basename_commitment(
                BasenameEncoding::PosixBytes,
                main_component.as_bytes(),
            )
            .unwrap(),
            creation_security_kind: 1,
            creation_security_commitment: [6; 32],
            header_seq: 1,
        };
        let mut block = [0u8; PAGE_SIZE];
        header.encode_into(&mut block);
        sidecar.write_all_at(&block, 0).unwrap();
        sidecar.write_all_at(&block, PAGE_SIZE as u64).unwrap();
        let writer = active(1, 41, 9);
        write_active(&sidecar, header, 0, writer);
        drop(sidecar);
        drop(main);
        drop(parent);
        let pair = RetainedLiveFiles::open_locked(&main_path).unwrap();
        (directory, pair, writer)
    }

    fn live_pair_without_writer() -> (TestDirectory, RetainedLiveFiles) {
        let (directory, pair, _) = live_pair_with_dead_writer(2);
        pair.sidecar
            .write_all_at(
                &[0; SLOT_SIZE as usize],
                sidecar_slot_offset(pair.header, 0).unwrap(),
            )
            .unwrap();
        (directory, pair)
    }

    fn claimed_writer_ready_for_commit(
        pair: &mut RetainedLiveFiles,
        nonce: [u8; 16],
    ) -> (OwnedWriterLease, Bootstrap) {
        pair.scan_and_reap().unwrap();
        let owned = pair.claim_writer_lease_with(|| Ok(nonce)).unwrap();
        let source = pair.prepare_writer_for_exposure(&owned).unwrap();
        pair.sidecar
            .acquire_lock(LockMode::Exclusive, false)
            .unwrap();
        (owned, source)
    }

    fn next_writer_target(source: Bootstrap, page_count: u64, nonce: u8) -> MetaV4 {
        let mut target = source.meta;
        target.txn_id += 1;
        target.commit_nonce = [nonce; 16];
        target.page_count = page_count;
        target
    }

    fn write_writer_meta(pair: &RetainedLiveFiles, target: MetaV4) {
        let mut page = [0u8; PAGE_SIZE];
        target.encode_into(&mut page);
        pair.main
            .write_all_at(&page, (target.txn_id & 1) * PAGE_SIZE as u64)
            .unwrap();
    }

    fn arm_writer_update_for_test(
        pair: &mut RetainedLiveFiles,
        owned: &OwnedWriterLease,
        target: MetaV4,
    ) {
        let source = pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap();
        let target_active = ActiveSlot {
            txn_id: target.txn_id,
            ..owned.active
        };
        let armed = PreparedSlotTransition::update(
            pair.header,
            SlotRole::Writer,
            0,
            &source,
            owned.active,
            target_active,
            linux_slot_host_limits(),
        )
        .unwrap()
        .arm();
        pair.sidecar
            .write_all_at(
                &armed.state2_bytes().unwrap(),
                sidecar_slot_offset(pair.header, 0).unwrap(),
            )
            .unwrap();
        pair.sidecar.cleanup_authority = SidecarCleanupAuthority::Armed {
            transition: armed,
            dead_writer: None,
        };
    }

    #[test]
    fn retained_open_rejects_symlink_hardlink_and_nonregular() {
        let directory = TestDirectory::new();
        let main = directory.0.join("main.iprdb");
        std::fs::write(&main, b"main").unwrap();
        let (parent, component) = RetainedDirectory::open_parent(&main).unwrap();
        let retained = parent.open_regular(&component, false).unwrap();
        assert_eq!(retained.length(), 4);
        assert_ne!(
            retained.identity(),
            PosixIdentity {
                device: 0,
                inode: 0
            }
        );

        symlink("main.iprdb", directory.0.join("link.iprdb")).unwrap();
        assert!(matches!(
            parent.open_regular(OsStr::new("link.iprdb"), false),
            Err(LinuxOsError::Io { .. })
        ));

        std::fs::hard_link(&main, directory.0.join("hard.iprdb")).unwrap();
        assert!(matches!(
            parent.open_regular(&component, false),
            Err(LinuxOsError::LinkCountNotOne(2))
        ));
        std::fs::remove_file(directory.0.join("hard.iprdb")).unwrap();

        std::fs::create_dir(directory.0.join("subdir")).unwrap();
        assert!(matches!(
            parent.open_regular(OsStr::new("subdir"), false),
            Err(LinuxOsError::NotRegular)
        ));

        let fifo = directory.0.join("fifo");
        let fifo_name = CString::new(fifo.as_os_str().as_bytes()).unwrap();
        assert_eq!(unsafe { libc::mkfifo(fifo_name.as_ptr(), 0o600) }, 0);
        assert!(matches!(
            parent.open_regular(OsStr::new("fifo"), false),
            Err(LinuxOsError::NotRegular)
        ));
    }

    #[test]
    fn canonical_recheck_detects_replacement_but_descriptor_keeps_old_inode() {
        let directory = TestDirectory::new();
        let main = directory.0.join("main.iprdb");
        std::fs::write(&main, b"old!").unwrap();
        let (parent, component) = RetainedDirectory::open_parent(&main).unwrap();
        let retained = parent.open_regular(&component, false).unwrap();
        parent.verify_path(&component, &retained).unwrap();

        std::fs::rename(&main, directory.0.join("old.iprdb")).unwrap();
        std::fs::write(&main, b"new!").unwrap();
        assert!(matches!(
            parent.verify_path(&component, &retained),
            Err(LinuxOsError::PathIdentityMismatch)
        ));
        let mut bytes = [0u8; 4];
        retained.read_exact_at(&mut bytes, 0).unwrap();
        assert_eq!(&bytes, b"old!");
    }

    #[test]
    fn pinned_page_source_keeps_descriptor_and_reports_short_read_evidence() {
        let directory = TestDirectory::new();
        let main = directory.0.join("main.iprdb");
        let old = directory.0.join("old.iprdb");
        let mut meta = empty_direct_meta(1);
        meta.page_count = 3;
        let mut original = vec![0u8; 3 * PAGE_SIZE];
        meta.encode_into((&mut original[..PAGE_SIZE]).try_into().unwrap());
        meta.encode_into(
            (&mut original[PAGE_SIZE..2 * PAGE_SIZE])
                .try_into()
                .unwrap(),
        );
        original[2 * PAGE_SIZE..].fill(0x5a);
        std::fs::write(&main, &original).unwrap();

        let (parent, component) = RetainedDirectory::open_parent(&main).unwrap();
        let retained = parent.open_regular(&component, false).unwrap();
        let bootstrap = retained.read_main_bootstrap(OpenMode::LiveReader).unwrap();
        let source = retained.pinned_page_source(bootstrap).unwrap();

        std::fs::rename(&main, &old).unwrap();
        let mut replacement = original;
        replacement[2 * PAGE_SIZE..].fill(0xa5);
        std::fs::write(&main, replacement).unwrap();
        let mut page = [0u8; PAGE_SIZE];
        source.read_page(2, &mut page).unwrap();
        assert!(page.iter().all(|&byte| byte == 0x5a));

        File::options()
            .write(true)
            .open(&old)
            .unwrap()
            .set_len((2 * PAGE_SIZE) as u64)
            .unwrap();
        assert_eq!(
            source.read_page(2, &mut page),
            Err(PageSourceError::ShortRead {
                offset: (2 * PAGE_SIZE) as u64,
                expected: PAGE_SIZE,
                actual: 0,
            })
        );
    }

    #[test]
    fn independent_open_descriptions_contend_on_flock() {
        let directory = TestDirectory::new();
        let main = directory.0.join("main.iprdb");
        std::fs::write(&main, b"main").unwrap();
        let (parent, component) = RetainedDirectory::open_parent(&main).unwrap();
        let mut first = parent.open_regular(&component, true).unwrap();
        let mut second = parent.open_regular(&component, true).unwrap();
        first.acquire_lock(LockMode::Exclusive, false).unwrap();
        assert!(matches!(
            second.acquire_lock(LockMode::Exclusive, true),
            Err(LinuxOsError::LockBusy)
        ));
        first.release_lock().unwrap();
        second.acquire_lock(LockMode::Exclusive, true).unwrap();
        second.release_lock().unwrap();
    }

    #[test]
    fn interruptible_flock_observes_cancellation_while_contended() {
        let directory = TestDirectory::new();
        let main = directory.0.join("main.iprdb");
        std::fs::write(&main, b"main").unwrap();
        let (parent, component) = RetainedDirectory::open_parent(&main).unwrap();
        let mut first = parent.open_regular(&component, true).unwrap();
        let mut second = parent.open_regular(&component, true).unwrap();
        first.acquire_lock(LockMode::Exclusive, false).unwrap();
        let checks = Cell::new(0u32);
        assert!(matches!(
            second.acquire_lock_interruptible(LockMode::Exclusive, || {
                checks.set(checks.get() + 1);
                checks.get() >= 3
            }),
            Err(LinuxOsError::Cancelled)
        ));
        assert!(!second.lock_held());
        first.release_lock().unwrap();
    }

    #[test]
    fn positional_io_sidecar_name_domain_and_random_are_exact() {
        let directory = TestDirectory::new();
        let main = directory.0.join("main.iprdb");
        let mut created = File::create(&main).unwrap();
        created.write_all(&[0; 16]).unwrap();
        drop(created);
        let (parent, component) = RetainedDirectory::open_parent(&main).unwrap();
        assert_eq!(
            parent.sidecar_component(&component).unwrap(),
            OsStr::new("main.iprdb.readers")
        );
        let retained = parent.open_regular(&component, true).unwrap();
        retained.write_all_at(b"exact", 5).unwrap();
        let mut bytes = [0u8; 5];
        retained.read_exact_at(&mut bytes, 5).unwrap();
        assert_eq!(&bytes, b"exact");
        assert_ne!(linux_process_domain_token().unwrap(), [0; 32]);
        assert_ne!(random_nonzero_128().unwrap(), [0; 16]);
        assert!(matches!(
            random_nonzero_128_with(|_| Err(())),
            Err(LinuxOsError::RandomFailure)
        ));
        assert!(matches!(
            random_nonzero_128_with(|bytes| {
                bytes.fill(0);
                Ok(())
            }),
            Err(LinuxOsError::RandomZero)
        ));
        assert!(matches!(
            RetainedDirectory::open_parent(&directory.0.join(".IPRANGE-owned")),
            Err(LinuxOsError::InvalidPathComponent)
        ));
        assert!(matches!(
            RetainedDirectory::open_parent(&directory.0.join("main.READERS")),
            Err(LinuxOsError::InvalidPathComponent)
        ));
        assert!(RetainedDirectory::open_parent(&directory.0.join(".İprange-main")).is_ok());
        assert_eq!(low_u32(0x9123_683e_u32 as i32), 0x9123_683e);
        assert_eq!(low_u32(u64::from(0x9123_683e_u32)), 0x9123_683e);

        let owner = current_active_slot(1, [1; 16]);
        assert_ne!(owner.process_id, 0);
        assert_ne!(owner.process_start, 0);
        assert!(owner.process_id <= linux_slot_host_limits().process_id_max);
        assert!(matches!(
            observe_posix_process(owner),
            PosixProcessObservation::Exists { .. }
        ));
    }

    #[test]
    fn retained_main_bootstrap_reads_only_the_two_meta_pages() {
        let directory = TestDirectory::new();
        let main = directory.0.join("main.iprdb");
        let mut bytes = [0u8; 3 * PAGE_SIZE];
        let meta = empty_direct_meta(1);
        meta.encode_into((&mut bytes[..PAGE_SIZE]).try_into().unwrap());
        meta.encode_into((&mut bytes[PAGE_SIZE..2 * PAGE_SIZE]).try_into().unwrap());
        std::fs::write(&main, bytes).unwrap();
        let (parent, component) = RetainedDirectory::open_parent(&main).unwrap();
        let retained = parent.open_regular(&component, true).unwrap();
        let opened = retained.read_main_bootstrap(OpenMode::LiveReader).unwrap();
        assert_eq!(opened.meta, meta);
        assert_eq!(opened.committed_bytes, 2 * PAGE_SIZE as u64);
        assert_eq!(opened.physical_bytes, 3 * PAGE_SIZE as u64);
        assert!(matches!(
            retained.read_main_bootstrap(OpenMode::ImmutableReader),
            Err(LinuxOsError::Bootstrap(
                BootstrapError::ImmutableLengthMismatch
            ))
        ));
    }

    #[test]
    fn live_pair_binding_requires_exact_locked_files_and_basename() {
        let directory = TestDirectory::new();
        let main_path = directory.0.join("main.iprdb");
        let sidecar_path = directory.0.join("main.iprdb.readers");
        let mut main_bytes = [0u8; 2 * PAGE_SIZE];
        let meta = empty_direct_meta(1);
        meta.encode_into((&mut main_bytes[..PAGE_SIZE]).try_into().unwrap());
        meta.encode_into((&mut main_bytes[PAGE_SIZE..]).try_into().unwrap());
        std::fs::write(&main_path, main_bytes).unwrap();
        let (parent, main_component) = RetainedDirectory::open_parent(&main_path).unwrap();
        let sidecar_component = parent.sidecar_component(&main_component).unwrap();
        let created = File::create(&sidecar_path).unwrap();
        created.set_len(8192 + 2 * u64::from(SLOT_SIZE)).unwrap();
        drop(created);
        let mut main = parent.open_regular(&main_component, true).unwrap();
        let mut sidecar = parent.open_regular(&sidecar_component, true).unwrap();
        let bootstrap = main.read_main_bootstrap(OpenMode::Writer).unwrap();
        let header = SidecarHeader {
            identity_kind: LocalIdentityKind::Posix,
            capacity: 1,
            state: SidecarState::Ready,
            database_id: meta.database_id,
            main_identity: main.identity().encode(),
            sidecar_identity: sidecar.identity().encode(),
            sidecar_id: [2; 16],
            origin: SidecarOrigin::CreateLive,
            attempted_txn_id: 1,
            attempted_commit_nonce: [3; 16],
            attempted_main_bytes: 8192,
            attempted_main_sha512: [4; 64],
            process_domain_kind: ProcessDomainKind::LinuxPidNamespace,
            process_domain_token: linux_process_domain_token().unwrap(),
            basename_encoding: BasenameEncoding::PosixBytes as u16,
            basename_len: main_component.as_bytes().len() as u32,
            basename_commitment: basename_commitment(
                BasenameEncoding::PosixBytes,
                main_component.as_bytes(),
            )
            .unwrap(),
            creation_security_kind: 1,
            creation_security_commitment: [6; 32],
            header_seq: 1,
        };
        let mut block = [0u8; PAGE_SIZE];
        header.encode_into(&mut block);
        sidecar.write_all_at(&block, 0).unwrap();
        sidecar.write_all_at(&block, PAGE_SIZE as u64).unwrap();

        assert!(matches!(
            parent.verify_live_pair_binding(
                &main_component,
                &main,
                &sidecar_component,
                &sidecar,
                bootstrap,
                header,
            ),
            Err(LinuxOsError::LifetimeLockRequired)
        ));
        main.acquire_lock(LockMode::Shared, false).unwrap();
        sidecar.acquire_lock(LockMode::Exclusive, false).unwrap();
        parent
            .verify_live_pair_binding(
                &main_component,
                &main,
                &sidecar_component,
                &sidecar,
                bootstrap,
                header,
            )
            .unwrap();
        let mut wrong_name = header;
        wrong_name.basename_commitment[0] ^= 1;
        assert!(matches!(
            parent.verify_live_pair_binding(
                &main_component,
                &main,
                &sidecar_component,
                &sidecar,
                bootstrap,
                wrong_name,
            ),
            Err(LinuxOsError::SidecarBasenameMismatch)
        ));
        sidecar.release_lock().unwrap();
        main.release_lock().unwrap();
    }

    #[test]
    fn locked_sidecar_transition_uses_retained_descriptor_and_exact_order() {
        let (_directory, mut sidecar, header) = ready_sidecar(2);

        let target = current_active_slot(0, [7; 16]);
        let prepared = PreparedSlotTransition::claim(
            header,
            SlotRole::Reader,
            1,
            &[0; SLOT_SIZE as usize],
            target,
            linux_slot_host_limits(),
        )
        .unwrap();
        assert!(matches!(
            sidecar.execute_sidecar_slot_transition(prepared, linux_slot_host_limits()),
            Err(LockedSlotExecutionError::BeforeArm(
                LinuxOsError::OperationLockRequired
            ))
        ));
        assert!(!sidecar.has_armed_transition());

        let prepared = PreparedSlotTransition::claim(
            header,
            SlotRole::Reader,
            1,
            &[0; SLOT_SIZE as usize],
            target,
            linux_slot_host_limits(),
        )
        .unwrap();
        sidecar.acquire_lock(LockMode::Exclusive, false).unwrap();
        sidecar
            .execute_sidecar_slot_transition(prepared, linux_slot_host_limits())
            .unwrap();
        assert!(!sidecar.has_armed_transition());
        let active_image = sidecar.read_sidecar_slot(header, 1).unwrap();
        assert_eq!(
            decode_stable_slot(&active_image, SlotRole::Reader, linux_slot_host_limits()),
            Ok(StableSlot::Active(target))
        );

        let prepared = PreparedSlotTransition::clear_owned(
            header,
            SlotRole::Reader,
            1,
            &active_image,
            target,
            linux_slot_host_limits(),
        )
        .unwrap();
        let armed = prepared.arm();
        let offset = sidecar_slot_offset(header, 1).unwrap();
        sidecar
            .write_all_at(&armed.state2_bytes().unwrap(), offset)
            .unwrap();
        sidecar.cleanup_authority = SidecarCleanupAuthority::Armed {
            transition: armed,
            dead_writer: None,
        };
        assert!(matches!(
            sidecar.release_lock(),
            Err(LinuxOsError::ArmedTransition)
        ));
        assert_eq!(
            sidecar
                .retry_sidecar_slot_cleanup(linux_slot_host_limits())
                .unwrap(),
            CleanupDisposition::AlreadyAbsent
        );
        assert!(!sidecar.has_armed_transition());
        assert_eq!(
            sidecar.read_sidecar_slot(header, 1).unwrap(),
            [0; SLOT_SIZE as usize]
        );
        sidecar.release_lock().unwrap();
    }

    #[test]
    fn sidecar_scan_reaps_dead_readers_before_transaction_checks() {
        let (_directory, mut sidecar, header) = ready_sidecar(4);
        let writer = active(7, 11, 1);
        let dead_future = active(9, 12, 2);
        let live = active(6, 13, 3);
        let registering = active(0, 14, 4);
        write_active(&sidecar, header, 0, writer);
        write_active(&sidecar, header, 1, dead_future);
        write_active(&sidecar, header, 2, live);
        write_active(&sidecar, header, 3, registering);
        sidecar.acquire_lock(LockMode::Exclusive, false).unwrap();

        let result = sidecar
            .scan_and_reap_ready_sidecar_with(header, 7, |owner| {
                if owner == dead_future {
                    PosixProcessObservation::Missing
                } else {
                    PosixProcessObservation::Exists {
                        current_start: Some(owner.process_start),
                    }
                }
            })
            .unwrap();
        assert_eq!(result.writer, Some(writer));
        assert_eq!(result.active_readers, 2);
        assert_eq!(result.registering_readers, 1);
        assert_eq!(result.oldest_reader_txn, Some(6));
        assert_eq!(result.lowest_free_reader_slot, Some(1));
        assert_eq!(
            sidecar.read_sidecar_slot(header, 1).unwrap(),
            [0; SLOT_SIZE as usize]
        );
        assert!(!sidecar.has_armed_transition());
        sidecar.release_lock().unwrap();
    }

    #[test]
    fn sidecar_scan_finishes_structural_pass_before_reaping() {
        let (_directory, mut sidecar, header) = ready_sidecar(2);
        let dead = active(7, 21, 1);
        write_active(&sidecar, header, 1, dead);
        let mut malformed = [0u8; SLOT_SIZE as usize];
        malformed[4] = 1;
        sidecar
            .write_all_at(&malformed, sidecar_slot_offset(header, 2).unwrap())
            .unwrap();
        sidecar.acquire_lock(LockMode::Exclusive, false).unwrap();

        assert!(matches!(
            sidecar.scan_and_reap_ready_sidecar_with(header, 7, |_| {
                PosixProcessObservation::Missing
            }),
            Err(LinuxSidecarScanError::Slot {
                index: 2,
                problem: SlotProblem::FreeNonzero
            })
        ));
        assert_eq!(
            sidecar.read_sidecar_slot(header, 1).unwrap(),
            encode_active_slot(dead)
        );
        assert!(!sidecar.has_armed_transition());
        sidecar.release_lock().unwrap();
    }

    #[test]
    fn sidecar_scan_cancellation_before_reaping_changes_no_slot() {
        let (_directory, mut sidecar, header) = ready_sidecar(2);
        let dead = active(7, 22, 1);
        write_active(&sidecar, header, 1, dead);
        sidecar.acquire_lock(LockMode::Exclusive, false).unwrap();
        let checks = Cell::new(0u32);

        assert!(matches!(
            sidecar.scan_and_reap_ready_sidecar_with_cancel(
                header,
                7,
                |_| panic!("cancellation in pass 1 must precede liveness checks"),
                || {
                    checks.set(checks.get() + 1);
                    checks.get() >= 2
                },
            ),
            Err(LinuxSidecarScanError::Cancelled)
        ));
        assert_eq!(
            sidecar.read_sidecar_slot(header, 1).unwrap(),
            encode_active_slot(dead)
        );
        assert!(matches!(
            sidecar.cleanup_authority,
            SidecarCleanupAuthority::None
        ));
        sidecar.release_lock().unwrap();
    }

    #[test]
    fn sidecar_scan_preserves_dead_writer_for_tail_cleanup() {
        let (_directory, mut sidecar, header) = ready_sidecar(1);
        let dead_writer = active(6, 31, 1);
        write_active(&sidecar, header, 0, dead_writer);
        sidecar.acquire_lock(LockMode::Exclusive, false).unwrap();

        assert!(matches!(
            sidecar.scan_and_reap_ready_sidecar_with(header, 7, |_| {
                PosixProcessObservation::Missing
            }),
            Err(LinuxSidecarScanError::DeadWriter {
                active,
                proof: DeathProof::PosixMissing { process_id: 31 }
            }) if active == dead_writer
        ));
        assert_eq!(
            sidecar.read_sidecar_slot(header, 0).unwrap(),
            encode_active_slot(dead_writer)
        );
        assert!(!sidecar.has_armed_transition());
        assert!(sidecar.has_dead_writer_cleanup());
        assert_eq!(
            sidecar.cleanup_authority,
            SidecarCleanupAuthority::DeadWriter(ProvenDeadWriter {
                header,
                source_image: encode_active_slot(dead_writer),
                active: dead_writer,
                proof: DeathProof::PosixMissing { process_id: 31 },
                bootstrap: None,
                tail: None,
            })
        );
        let direct_clear = PreparedSlotTransition::clear_proven_dead(
            header,
            SlotRole::Writer,
            0,
            &encode_active_slot(dead_writer),
            dead_writer,
            DeathProof::PosixMissing { process_id: 31 },
            linux_slot_host_limits(),
        )
        .unwrap();
        assert!(matches!(
            sidecar.execute_sidecar_slot_transition(direct_clear, linux_slot_host_limits()),
            Err(LockedSlotExecutionError::BeforeArm(
                LinuxOsError::WriterClearRequiresMainTail
            ))
        ));
        assert!(matches!(
            sidecar.release_lock(),
            Err(LinuxOsError::PendingWriterCleanup)
        ));
    }

    #[test]
    fn live_pair_truncates_syncs_and_only_then_clears_dead_writer() {
        let (_directory, mut pair, writer) = live_pair_with_dead_writer(3);
        assert!(matches!(
            pair.scan_and_reap_with(|_| PosixProcessObservation::Missing),
            Err(LinuxLivePairError::Scan(
                LinuxSidecarScanError::DeadWriter { active, .. }
            )) if active == writer
        ));
        assert!(pair.sidecar.has_dead_writer_cleanup());
        pair.retry_dead_writer_cleanup().unwrap();
        assert_eq!(
            pair.main.file.metadata().unwrap().len(),
            2 * PAGE_SIZE as u64
        );
        assert_eq!(
            pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap(),
            [0; SLOT_SIZE as usize]
        );
        assert!(matches!(
            pair.sidecar.cleanup_authority,
            SidecarCleanupAuthority::None
        ));
        let inspection = pair.scan_and_reap().unwrap();
        assert_eq!(inspection.writer, None);
    }

    #[test]
    fn writer_header_reselection_blocks_tail_mutation_and_clear() {
        for mutation in [
            HeaderMutation::ValidChanged,
            HeaderMutation::Malformed,
            HeaderMutation::TornUpdate,
        ] {
            let (_directory, mut pair) = live_pair_without_writer();
            pair.main.file.set_len(3 * PAGE_SIZE as u64).unwrap();
            pair.scan_and_reap().unwrap();
            let owned = pair.claim_writer_lease_with(|| Ok([0x71; 16])).unwrap();
            let owned_image = encode_active_slot(owned.active);
            mutate_header(&pair.sidecar, pair.header, mutation);

            assert!(matches!(
                pair.retry_writer_lease_cleanup(Some(&owned)),
                Err(LinuxWriterLeaseError::Os(_))
            ));
            assert_eq!(
                pair.main.file.metadata().unwrap().len(),
                3 * PAGE_SIZE as u64,
                "{mutation:?} changed the main tail"
            );
            assert_eq!(
                pair.sidecar
                    .read_sidecar_slot_after_header(pair.header, 0)
                    .unwrap(),
                owned_image,
                "{mutation:?} changed the writer lease"
            );

            restore_header(&pair.sidecar, pair.header);
            pair.retry_writer_lease_cleanup(Some(&owned)).unwrap();
            assert_eq!(
                pair.main.file.metadata().unwrap().len(),
                2 * PAGE_SIZE as u64
            );
            assert_eq!(
                pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap(),
                [0; SLOT_SIZE as usize]
            );
        }
    }

    #[test]
    fn exact_zero_still_requires_the_expected_sidecar_header() {
        for mutation in [
            HeaderMutation::ValidChanged,
            HeaderMutation::Malformed,
            HeaderMutation::TornUpdate,
        ] {
            let (_directory, mut pair) = live_pair_without_writer();
            pair.scan_and_reap().unwrap();
            let owned = pair.claim_writer_lease_with(|| Ok([0x73; 16])).unwrap();
            pair.sidecar
                .write_all_at(
                    &[0; SLOT_SIZE as usize],
                    sidecar_slot_offset(pair.header, 0).unwrap(),
                )
                .unwrap();
            mutate_header(&pair.sidecar, pair.header, mutation);

            assert!(matches!(
                pair.retry_writer_lease_cleanup(Some(&owned)),
                Err(LinuxWriterLeaseError::Os(_))
            ));
            assert_eq!(
                pair.sidecar
                    .read_sidecar_slot_after_header(pair.header, 0)
                    .unwrap(),
                [0; SLOT_SIZE as usize]
            );
            assert!(pair.writer_bootstrap().is_some());

            restore_header(&pair.sidecar, pair.header);
            pair.retry_writer_lease_cleanup(Some(&owned)).unwrap();
            assert!(pair.writer_bootstrap().is_none());
        }
    }

    #[test]
    fn writer_clear_reselects_header_at_the_transition_boundary() {
        for mutation in [
            HeaderMutation::ValidChanged,
            HeaderMutation::Malformed,
            HeaderMutation::TornUpdate,
        ] {
            let (_directory, mut pair) = live_pair_without_writer();
            pair.scan_and_reap().unwrap();
            let owned = pair.claim_writer_lease_with(|| Ok([0x74; 16])).unwrap();
            let current = pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap();
            let prepared = PreparedSlotTransition::clear_owned(
                pair.header,
                SlotRole::Writer,
                0,
                &current,
                owned.active,
                linux_slot_host_limits(),
            )
            .unwrap();
            mutate_header(&pair.sidecar, pair.header, mutation);

            assert!(matches!(
                pair.sidecar.execute_sidecar_slot_transition_after_tail(
                    prepared,
                    linux_slot_host_limits(),
                ),
                Err(LockedSlotExecutionError::BeforeArm(_))
            ));
            assert_eq!(
                pair.sidecar
                    .read_sidecar_slot_after_header(pair.header, 0)
                    .unwrap(),
                current
            );
            assert!(!pair.sidecar.has_armed_transition());

            restore_header(&pair.sidecar, pair.header);
            pair.retry_writer_lease_cleanup(Some(&owned)).unwrap();
        }
    }

    #[test]
    fn writer_pre_meta_target_selection_keeps_the_source_lease() {
        let (_directory, mut pair) = live_pair_without_writer();
        let (owned, source) = claimed_writer_ready_for_commit(&mut pair, [0x81; 16]);
        let target = next_writer_target(source, 3, 0x82);
        pair.begin_writer_commit_attempt(&owned, target).unwrap();
        pair.main
            .file
            .set_len(target.page_count * PAGE_SIZE as u64)
            .unwrap();
        write_writer_meta(&pair, target);

        assert!(matches!(
            pair.retry_writer_lease_cleanup_with(
                Some(&owned),
                |_, _| panic!("pre-meta close must not truncate"),
                |_| panic!("pre-meta close must not synchronize")
            ),
            Err(LinuxWriterLeaseError::CommitOutcomeUnresolved)
        ));
        assert_eq!(
            pair.main.file.metadata().unwrap().len(),
            target.page_count * PAGE_SIZE as u64
        );
        assert_eq!(
            pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap(),
            encode_active_slot(owned.active)
        );
        assert!(pair.writer_publication.is_some());
    }

    #[test]
    fn writer_commit_attempt_requires_clean_sidecar_provenance() {
        let (_directory, mut pair) = live_pair_without_writer();
        let (owned, source) = claimed_writer_ready_for_commit(&mut pair, [0x83; 16]);
        let current = pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap();
        let armed = PreparedSlotTransition::clear_owned(
            pair.header,
            SlotRole::Writer,
            0,
            &current,
            owned.active,
            linux_slot_host_limits(),
        )
        .unwrap()
        .arm();
        pair.sidecar
            .write_all_at(
                &armed.state2_bytes().unwrap(),
                sidecar_slot_offset(pair.header, 0).unwrap(),
            )
            .unwrap();
        pair.sidecar.cleanup_authority = SidecarCleanupAuthority::Armed {
            transition: armed,
            dead_writer: None,
        };

        assert!(matches!(
            pair.begin_writer_commit_attempt(&owned, next_writer_target(source, 3, 0x84)),
            Err(LinuxWriterLeaseError::OutstandingWriterCleanup)
        ));
        assert!(pair.sidecar.has_armed_transition());
        assert!(pair.writer_publication.is_none());
    }

    #[test]
    fn writer_close_truncates_only_a_still_selected_source_tail() {
        let (_directory, mut pair) = live_pair_without_writer();
        let (owned, source) = claimed_writer_ready_for_commit(&mut pair, [0x85; 16]);
        let target = next_writer_target(source, 3, 0x86);
        pair.begin_writer_commit_attempt(&owned, target).unwrap();
        pair.main
            .file
            .set_len(target.page_count * PAGE_SIZE as u64)
            .unwrap();
        pair.begin_writer_meta_write(&owned, target).unwrap();
        assert_eq!(
            pair.writer_tail(),
            Some(UnpublishedMainTail {
                main_identity: pair.main.identity,
                database_id: source.meta.database_id,
                transaction_id: source.meta.txn_id,
                commit_nonce: source.meta.commit_nonce,
                committed_length: source.committed_bytes,
                observed_end_exclusive: target.page_count * PAGE_SIZE as u64,
            })
        );

        pair.retry_writer_lease_cleanup(Some(&owned)).unwrap();
        let current = pair.main.read_main_bootstrap(OpenMode::Writer).unwrap();
        assert_eq!(current, source);
        assert_eq!(current.physical_bytes, source.committed_bytes);
        assert_eq!(
            pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap(),
            [0; SLOT_SIZE as usize]
        );
        assert!(pair.writer_publication.is_none());
    }

    #[test]
    fn writer_close_never_truncates_a_selected_target_after_meta_write_begins() {
        let (_directory, mut pair) = live_pair_without_writer();
        let (owned, source) = claimed_writer_ready_for_commit(&mut pair, [0x87; 16]);
        let target = next_writer_target(source, 3, 0x88);
        pair.begin_writer_commit_attempt(&owned, target).unwrap();
        pair.main
            .file
            .set_len(target.page_count * PAGE_SIZE as u64)
            .unwrap();
        pair.begin_writer_meta_write(&owned, target).unwrap();
        write_writer_meta(&pair, target);

        pair.retry_writer_lease_cleanup_with(
            Some(&owned),
            |_, _| panic!("target-selected close must not truncate"),
            |_| panic!("target-selected close must not synchronize"),
        )
        .unwrap();
        let current = pair.main.read_main_bootstrap(OpenMode::Writer).unwrap();
        assert_eq!(current.meta, target);
        assert_eq!(current.physical_bytes, target.page_count * PAGE_SIZE as u64);
        assert_eq!(
            pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap(),
            [0; SLOT_SIZE as usize]
        );
        assert!(pair.writer_publication.is_none());
        assert!(pair.writer_tail().is_none());
    }

    #[test]
    fn writer_close_clears_only_the_exact_interrupted_post_meta_update() {
        let (_directory, mut pair) = live_pair_without_writer();
        let (owned, source) = claimed_writer_ready_for_commit(&mut pair, [0x89; 16]);
        let target = next_writer_target(source, 3, 0x8a);
        pair.begin_writer_commit_attempt(&owned, target).unwrap();
        pair.main
            .file
            .set_len(target.page_count * PAGE_SIZE as u64)
            .unwrap();
        pair.begin_writer_meta_write(&owned, target).unwrap();
        write_writer_meta(&pair, target);
        assert_eq!(
            pair.confirm_writer_meta_sync(&owned, target).unwrap().meta,
            target
        );
        arm_writer_update_for_test(&mut pair, &owned, target);

        pair.retry_writer_lease_cleanup_with(
            Some(&owned),
            |_, _| panic!("post-meta cleanup must not truncate"),
            |_| panic!("post-meta cleanup must not synchronize"),
        )
        .unwrap();
        let current = pair.main.read_main_bootstrap(OpenMode::Writer).unwrap();
        assert_eq!(current.meta, target);
        assert_eq!(current.physical_bytes, target.page_count * PAGE_SIZE as u64);
        assert_eq!(
            pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap(),
            [0; SLOT_SIZE as usize]
        );
        assert!(!pair.sidecar.has_armed_transition());
        assert!(pair.writer_publication.is_none());
    }

    #[test]
    fn writer_close_refuses_a_different_armed_update_after_meta_publication() {
        let (_directory, mut pair) = live_pair_without_writer();
        let (owned, source) = claimed_writer_ready_for_commit(&mut pair, [0x8b; 16]);
        let target = next_writer_target(source, 3, 0x8c);
        pair.begin_writer_commit_attempt(&owned, target).unwrap();
        pair.main
            .file
            .set_len(target.page_count * PAGE_SIZE as u64)
            .unwrap();
        pair.begin_writer_meta_write(&owned, target).unwrap();
        write_writer_meta(&pair, target);
        pair.confirm_writer_meta_sync(&owned, target).unwrap();

        let source_image = pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap();
        let wrong_target = ActiveSlot {
            txn_id: target.txn_id + 1,
            ..owned.active
        };
        let armed = PreparedSlotTransition::update(
            pair.header,
            SlotRole::Writer,
            0,
            &source_image,
            owned.active,
            wrong_target,
            linux_slot_host_limits(),
        )
        .unwrap()
        .arm();
        pair.sidecar
            .write_all_at(
                &armed.state2_bytes().unwrap(),
                sidecar_slot_offset(pair.header, 0).unwrap(),
            )
            .unwrap();
        pair.sidecar.cleanup_authority = SidecarCleanupAuthority::Armed {
            transition: armed,
            dead_writer: None,
        };

        assert!(matches!(
            pair.retry_writer_lease_cleanup_with(
                Some(&owned),
                |_, _| panic!("mismatched update must not truncate"),
                |_| panic!("mismatched update must not synchronize")
            ),
            Err(LinuxWriterLeaseError::OwnerMismatch)
        ));
        assert!(pair.sidecar.has_armed_transition());
        assert!(pair.writer_publication.is_some());
    }

    #[test]
    fn writer_close_never_truncates_source_after_meta_sync_confirmation() {
        let (_directory, mut pair) = live_pair_without_writer();
        let (owned, source) = claimed_writer_ready_for_commit(&mut pair, [0x8d; 16]);
        let target = next_writer_target(source, 3, 0x8e);
        pair.begin_writer_commit_attempt(&owned, target).unwrap();
        pair.main
            .file
            .set_len(target.page_count * PAGE_SIZE as u64)
            .unwrap();
        pair.begin_writer_meta_write(&owned, target).unwrap();
        write_writer_meta(&pair, target);
        pair.confirm_writer_meta_sync(&owned, target).unwrap();
        let mut source_page = [0u8; PAGE_SIZE];
        source.meta.encode_into(&mut source_page);
        pair.main
            .write_all_at(&source_page, (target.txn_id & 1) * PAGE_SIZE as u64)
            .unwrap();

        assert!(matches!(
            pair.retry_writer_lease_cleanup_with(
                Some(&owned),
                |_, _| panic!("confirmed publication must not truncate source"),
                |_| panic!("confirmed publication must not synchronize source")
            ),
            Err(LinuxWriterLeaseError::CommitOutcomeUnresolved)
        ));
        assert_eq!(
            pair.main.file.metadata().unwrap().len(),
            target.page_count * PAGE_SIZE as u64
        );
        assert_eq!(
            pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap(),
            encode_active_slot(owned.active)
        );
        assert!(pair.writer_publication.is_some());
    }

    #[test]
    fn writer_phase_five_transfers_the_lease_before_later_close() {
        let (_directory, mut pair) = live_pair_without_writer();
        let (mut owned, source) = claimed_writer_ready_for_commit(&mut pair, [0x8f; 16]);
        let target = next_writer_target(source, 3, 0x90);
        pair.begin_writer_commit_attempt(&owned, target).unwrap();
        pair.main
            .file
            .set_len(target.page_count * PAGE_SIZE as u64)
            .unwrap();
        pair.begin_writer_meta_write(&owned, target).unwrap();
        write_writer_meta(&pair, target);
        let confirmed = pair.confirm_writer_meta_sync(&owned, target).unwrap();

        pair.update_writer_lease_after_meta(&mut owned).unwrap();
        assert_eq!(owned.active.txn_id, target.txn_id);
        assert_eq!(owned.claimed_bootstrap, confirmed);
        assert_eq!(pair.writer_bootstrap(), Some(confirmed));
        assert_eq!(
            pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap(),
            encode_active_slot(owned.active)
        );
        assert!(pair.writer_publication.is_none());

        pair.retry_writer_lease_cleanup_with(
            Some(&owned),
            |_, _| panic!("post-update close must not truncate"),
            |_| panic!("post-update close must not synchronize"),
        )
        .unwrap();
        assert_eq!(
            pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap(),
            [0; SLOT_SIZE as usize]
        );
    }

    #[test]
    fn writer_close_treats_a_valid_later_generation_as_tail_supersession() {
        let (_directory, mut pair) = live_pair_without_writer();
        let (owned, source) = claimed_writer_ready_for_commit(&mut pair, [0x91; 16]);
        let target = next_writer_target(source, 3, 0x92);
        pair.begin_writer_commit_attempt(&owned, target).unwrap();
        pair.main
            .file
            .set_len(target.page_count * PAGE_SIZE as u64)
            .unwrap();
        pair.begin_writer_meta_write(&owned, target).unwrap();
        write_writer_meta(&pair, target);

        let mut later = target;
        later.txn_id += 1;
        later.commit_nonce = [0x93; 16];
        later.page_count = 4;
        pair.main
            .file
            .set_len(later.page_count * PAGE_SIZE as u64)
            .unwrap();
        write_writer_meta(&pair, later);

        pair.retry_writer_lease_cleanup_with(
            Some(&owned),
            |_, _| panic!("later-generation close must not truncate"),
            |_| panic!("later-generation close must not synchronize"),
        )
        .unwrap();
        let current = pair.main.read_main_bootstrap(OpenMode::Writer).unwrap();
        assert_eq!(current.meta, later);
        assert_eq!(current.physical_bytes, later.page_count * PAGE_SIZE as u64);
        assert_eq!(
            pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap(),
            [0; SLOT_SIZE as usize]
        );
        assert!(pair.writer_publication.is_none());
    }

    #[test]
    fn dead_writer_header_reselection_blocks_tail_mutation_and_clear() {
        for mutation in [
            HeaderMutation::ValidChanged,
            HeaderMutation::Malformed,
            HeaderMutation::TornUpdate,
        ] {
            let (_directory, mut pair, writer) = live_pair_with_dead_writer(3);
            assert!(pair
                .scan_and_reap_with(|_| PosixProcessObservation::Missing)
                .is_err());
            mutate_header(&pair.sidecar, pair.header, mutation);

            assert!(matches!(
                pair.retry_dead_writer_cleanup(),
                Err(LinuxLivePairError::Os(_))
            ));
            assert_eq!(
                pair.main.file.metadata().unwrap().len(),
                3 * PAGE_SIZE as u64,
                "{mutation:?} changed the main tail"
            );
            assert_eq!(
                pair.sidecar
                    .read_sidecar_slot_after_header(pair.header, 0)
                    .unwrap(),
                encode_active_slot(writer),
                "{mutation:?} changed the writer lease"
            );

            restore_header(&pair.sidecar, pair.header);
            pair.retry_dead_writer_cleanup().unwrap();
            assert_eq!(
                pair.main.file.metadata().unwrap().len(),
                2 * PAGE_SIZE as u64
            );
            assert_eq!(
                pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap(),
                [0; SLOT_SIZE as usize]
            );
        }
    }

    #[test]
    fn dead_writer_reselects_header_after_tail_sync_before_clear() {
        for mutation in [
            HeaderMutation::ValidChanged,
            HeaderMutation::Malformed,
            HeaderMutation::TornUpdate,
        ] {
            let (_directory, mut pair, writer) = live_pair_with_dead_writer(3);
            assert!(pair
                .scan_and_reap_with(|_| PosixProcessObservation::Missing)
                .is_err());
            let mutating_sidecar = RetainedRegular {
                file: pair.sidecar.file.try_clone().unwrap(),
                identity: pair.sidecar.identity,
                length: pair.sidecar.length,
                creator_pid: pair.sidecar.creator_pid,
                lock: LockState::Unlocked,
                cleanup_authority: SidecarCleanupAuthority::None,
            };
            let header = pair.header;
            assert!(matches!(
                pair.retry_dead_writer_cleanup_with(
                    |file, length| file.set_len(length),
                    |file| {
                        file.sync_all()?;
                        mutate_header(&mutating_sidecar, header, mutation);
                        Ok(())
                    },
                ),
                Err(LinuxLivePairError::Os(_))
            ));
            assert_eq!(
                pair.main.file.metadata().unwrap().len(),
                2 * PAGE_SIZE as u64
            );
            assert_eq!(
                pair.sidecar
                    .read_sidecar_slot_after_header(pair.header, 0)
                    .unwrap(),
                encode_active_slot(writer)
            );
            assert!(pair.sidecar.has_dead_writer_cleanup());

            restore_header(&pair.sidecar, pair.header);
            pair.retry_dead_writer_cleanup().unwrap();
            assert_eq!(
                pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap(),
                [0; SLOT_SIZE as usize]
            );
        }
    }

    #[test]
    fn live_pair_retains_tail_record_across_truncate_and_sync_failures() {
        let (_directory, mut pair, writer) = live_pair_with_dead_writer(3);
        assert!(pair
            .scan_and_reap_with(|_| PosixProcessObservation::Missing)
            .is_err());
        let claimed = pair.main.read_main_bootstrap(OpenMode::Writer).unwrap();
        let sync_called = Cell::new(false);
        assert!(matches!(
            pair.retry_dead_writer_cleanup_with(
                |_, _| Err(io::Error::from(io::ErrorKind::Other)),
                |_| {
                    sync_called.set(true);
                    Ok(())
                },
            ),
            Err(LinuxLivePairError::Os(LinuxOsError::Io {
                operation: "truncate unpublished main tail",
                ..
            }))
        ));
        assert!(!sync_called.get());
        assert_eq!(
            pair.main.file.metadata().unwrap().len(),
            3 * PAGE_SIZE as u64
        );
        assert_eq!(
            pair.sidecar.cleanup_authority,
            SidecarCleanupAuthority::DeadWriter(ProvenDeadWriter {
                header: pair.header,
                source_image: encode_active_slot(writer),
                active: writer,
                proof: DeathProof::PosixMissing { process_id: 41 },
                bootstrap: Some(claimed),
                tail: Some(UnpublishedMainTail {
                    main_identity: pair.main.identity,
                    database_id: [1; 16],
                    transaction_id: 1,
                    commit_nonce: [2; 16],
                    committed_length: 2 * PAGE_SIZE as u64,
                    observed_end_exclusive: 3 * PAGE_SIZE as u64,
                }),
            })
        );

        assert!(matches!(
            pair.retry_dead_writer_cleanup_with(
                |file, length| file.set_len(length),
                |_| Err(io::Error::from(io::ErrorKind::Other)),
            ),
            Err(LinuxLivePairError::Os(LinuxOsError::Io {
                operation: "synchronize main tail cleanup",
                ..
            }))
        ));
        assert_eq!(
            pair.main.file.metadata().unwrap().len(),
            2 * PAGE_SIZE as u64
        );
        assert!(pair.sidecar.has_dead_writer_cleanup());

        let truncate_called = Cell::new(false);
        pair.retry_dead_writer_cleanup_with(
            |_, _| {
                truncate_called.set(true);
                Ok(())
            },
            |file| file.sync_all(),
        )
        .unwrap();
        assert!(!truncate_called.get());
        assert_eq!(
            pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap(),
            [0; SLOT_SIZE as usize]
        );
    }

    #[test]
    fn live_pair_exact_length_still_syncs_and_tail_growth_conflicts() {
        let (_directory, mut exact, _) = live_pair_with_dead_writer(2);
        assert!(exact
            .scan_and_reap_with(|_| PosixProcessObservation::Missing)
            .is_err());
        let sync_count = Cell::new(0u32);
        exact
            .retry_dead_writer_cleanup_with(
                |_, _| panic!("exact-length main must not truncate"),
                |file| {
                    sync_count.set(sync_count.get() + 1);
                    file.sync_all()
                },
            )
            .unwrap();
        assert_eq!(sync_count.get(), 1);

        let (_directory, mut grown, _) = live_pair_with_dead_writer(3);
        assert!(grown
            .scan_and_reap_with(|_| PosixProcessObservation::Missing)
            .is_err());
        assert!(grown
            .retry_dead_writer_cleanup_with(
                |_, _| Err(io::Error::from(io::ErrorKind::Other)),
                |_| Ok(()),
            )
            .is_err());
        grown.main.file.set_len(4 * PAGE_SIZE as u64).unwrap();
        assert!(matches!(
            grown.retry_dead_writer_cleanup(),
            Err(LinuxLivePairError::TailLengthConflict {
                target,
                observed_end,
                actual,
            }) if target == 2 * PAGE_SIZE as u64
                && observed_end == 3 * PAGE_SIZE as u64
                && actual == 4 * PAGE_SIZE as u64
        ));
        assert!(grown.sidecar.has_dead_writer_cleanup());
    }

    #[test]
    fn live_pair_source_change_fails_before_tail_mutation() {
        let (_directory, mut pair, writer) = live_pair_with_dead_writer(3);
        assert!(pair
            .scan_and_reap_with(|_| PosixProcessObservation::Missing)
            .is_err());
        let changed = ActiveSlot {
            nonce: [8; 16],
            ..writer
        };
        write_active(&pair.sidecar, pair.header, 0, changed);
        assert!(matches!(
            pair.retry_dead_writer_cleanup(),
            Err(LinuxLivePairError::WriterSourceChanged)
        ));
        assert_eq!(
            pair.main.file.metadata().unwrap().len(),
            3 * PAGE_SIZE as u64
        );
        assert!(pair.sidecar.has_dead_writer_cleanup());
    }

    #[test]
    fn dead_writer_scan_freezes_exact_main_generation_before_cleanup() {
        let (_directory, mut pair, writer) = live_pair_with_dead_writer(3);
        assert!(pair
            .scan_and_reap_with(|_| PosixProcessObservation::Missing)
            .is_err());
        let original = pair.main.read_main_bootstrap(OpenMode::Writer).unwrap();
        let mut changed = empty_direct_meta(2);
        changed.database_id = original.meta.database_id;
        changed.page_count = 3;
        let mut page = [0u8; PAGE_SIZE];
        changed.encode_into(&mut page);
        pair.main.write_all_at(&page, 0).unwrap();
        pair.main.write_all_at(&page, PAGE_SIZE as u64).unwrap();

        assert!(matches!(
            pair.retry_dead_writer_cleanup(),
            Err(LinuxLivePairError::MainGenerationChanged)
        ));
        assert_eq!(
            pair.main.file.metadata().unwrap().len(),
            3 * PAGE_SIZE as u64
        );
        assert_eq!(
            pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap(),
            encode_active_slot(writer)
        );

        original.meta.encode_into(&mut page);
        pair.main.write_all_at(&page, 0).unwrap();
        pair.main.write_all_at(&page, PAGE_SIZE as u64).unwrap();
        pair.retry_dead_writer_cleanup().unwrap();
        assert_eq!(
            pair.main.file.metadata().unwrap().len(),
            2 * PAGE_SIZE as u64
        );
        assert_eq!(
            pair.sidecar.read_sidecar_slot(pair.header, 0).unwrap(),
            [0; SLOT_SIZE as usize]
        );
    }

    #[test]
    fn reader_registration_claims_zero_pins_selected_and_cleans_exact_owner() {
        let (_directory, mut pair) = live_pair_without_writer();
        let inspection = pair.scan_and_reap().unwrap();
        assert_eq!(inspection.lowest_free_reader_slot, Some(1));

        let mut owned = pair.claim_reader_slot_with(|| Ok([0x44; 16])).unwrap();
        assert_eq!(owned.index, 1);
        assert_eq!(owned.active.txn_id, 0);
        assert_eq!(
            decode_stable_slot(
                &pair.sidecar.read_sidecar_slot(pair.header, 1).unwrap(),
                SlotRole::Reader,
                linux_slot_host_limits(),
            ),
            Ok(StableSlot::Active(owned.active))
        );

        let pinned = pair.pin_reader_slot(&mut owned).unwrap();
        assert_eq!(owned.active.txn_id, pinned.meta.txn_id);
        pair.release_reader_registration_lock(&owned, pinned)
            .unwrap();
        assert_eq!(pair.sidecar.lock, LockState::Unlocked);

        let cleanup = pair.retry_reader_slot_cleanup(Some(&owned)).unwrap();
        assert!(cleanup.main_path.is_ok());
        assert!(cleanup.sidecar_path.is_ok());
        assert_eq!(
            pair.sidecar.read_sidecar_slot(pair.header, 1).unwrap(),
            [0; SLOT_SIZE as usize]
        );
        pair.sidecar.release_lock().unwrap();
        pair.main.release_lock().unwrap();
    }

    #[test]
    fn reader_capacity_failure_precedes_nonce_generation_and_mutation() {
        let (_directory, mut pair) = live_pair_without_writer();
        let reader = current_active_slot(1, [0x55; 16]);
        write_active(&pair.sidecar, pair.header, 1, reader);
        let inspection = pair.scan_and_reap().unwrap();
        assert_eq!(inspection.lowest_free_reader_slot, None);

        assert!(matches!(
            pair.claim_reader_slot_with(|| panic!("full table must not request a nonce")),
            Err(LinuxReaderSlotError::ReaderCapacityExhausted)
        ));
        assert_eq!(
            decode_stable_slot(
                &pair.sidecar.read_sidecar_slot(pair.header, 1).unwrap(),
                SlotRole::Reader,
                linux_slot_host_limits(),
            ),
            Ok(StableSlot::Active(reader))
        );
        pair.sidecar.release_lock().unwrap();
        pair.main.release_lock().unwrap();
    }

    #[test]
    fn reader_cleanup_requires_authority_and_unlock_cross_binds_transaction() {
        let (_directory, mut pair) = live_pair_without_writer();
        let unowned = current_active_slot(1, [0x66; 16]);
        write_active(&pair.sidecar, pair.header, 1, unowned);
        assert!(matches!(
            pair.retry_reader_slot_cleanup(None),
            Err(LinuxReaderSlotError::NoCleanupAuthority)
        ));
        assert_eq!(
            decode_stable_slot(
                &pair.sidecar.read_sidecar_slot(pair.header, 1).unwrap(),
                SlotRole::Reader,
                linux_slot_host_limits(),
            ),
            Ok(StableSlot::Active(unowned))
        );
        pair.sidecar
            .write_all_at(
                &[0; SLOT_SIZE as usize],
                sidecar_slot_offset(pair.header, 1).unwrap(),
            )
            .unwrap();

        pair.scan_and_reap().unwrap();
        let mut owned = pair.claim_reader_slot_with(|| Ok([0x67; 16])).unwrap();
        let pinned = pair.pin_reader_slot(&mut owned).unwrap();
        owned.index = 0;
        assert!(matches!(
            pair.release_reader_registration_lock(&owned, pinned),
            Err(LinuxReaderSlotError::OwnerMismatch)
        ));
        owned.index = 1;
        owned.active.txn_id = 0;
        assert!(matches!(
            pair.release_reader_registration_lock(&owned, pinned),
            Err(LinuxReaderSlotError::OwnerMismatch)
        ));
        assert_eq!(pair.sidecar.lock, LockState::Exclusive);
        owned.active.txn_id = pinned.meta.txn_id;
        let cleanup = pair.retry_reader_slot_cleanup(Some(&owned)).unwrap();
        assert!(cleanup.main_path.is_ok());
        assert!(cleanup.sidecar_path.is_ok());
        pair.sidecar.release_lock().unwrap();
        pair.main.release_lock().unwrap();
    }

    #[test]
    fn reader_cleanup_clears_retained_slot_and_reports_canonical_replacement() {
        let (directory, mut pair) = live_pair_without_writer();
        pair.scan_and_reap().unwrap();
        let mut owned = pair.claim_reader_slot_with(|| Ok([0x68; 16])).unwrap();
        let pinned = pair.pin_reader_slot(&mut owned).unwrap();
        pair.release_reader_registration_lock(&owned, pinned)
            .unwrap();

        std::fs::rename(
            directory.0.join("main.iprdb.readers"),
            directory.0.join("old.readers"),
        )
        .unwrap();
        let cleanup = pair.retry_reader_slot_cleanup(Some(&owned)).unwrap();
        assert!(cleanup.main_path.is_ok());
        assert!(cleanup.sidecar_path.is_err());
        assert_eq!(
            pair.sidecar.read_sidecar_slot(pair.header, 1).unwrap(),
            [0; SLOT_SIZE as usize]
        );
        assert!(matches!(
            pair.sidecar.cleanup_authority,
            SidecarCleanupAuthority::None
        ));
        pair.sidecar.release_lock().unwrap();
        pair.main.release_lock().unwrap();
    }
}
