//! Registered-reader ownership for one selected live generation.

use std::path::{Path, PathBuf};

use crate::bootstrap::OpenMode;
use crate::cancellation::CancellationToken;
use crate::error::{finish_with_cleanup, Error, Result};
use crate::live_lock::{self, Mode};
use crate::live_namespace::Identity;
use crate::live_sidecar::{Sidecar, MAIN_LIFETIME_LOCK};
use crate::process_identity::ProcessIdentity;
use crate::publication::CoordinationCleanup;

use super::{DatabaseInfo, GenerationReader, ReaderCore};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum State {
    Open,
    CloseOnly,
    GateHeldSlotActive,
    GateHeldSlotClearing,
    GateHeldSlotCleared,
    GateHeldSlotReleased,
    MainLockOnly,
    Closed,
}

#[derive(Debug)]
pub(crate) struct LiveReaderClose {
    pub(crate) closed: bool,
    pub(crate) coordination_cleanup: CoordinationCleanup,
    pub(crate) cause: Option<Error>,
}

#[derive(Debug)]
pub(crate) struct LiveReaderCore {
    reader: ReaderCore,
    main_path: PathBuf,
    main_identity: Identity,
    sidecar: Sidecar,
    slot: u32,
    state: State,
    owner_identity: ProcessIdentity,
}

impl LiveReaderCore {
    pub(crate) fn open(path: &Path, cancellation: &CancellationToken) -> Result<Self> {
        live_lock::require_live_supported()?;
        cancellation.check()?;
        let main_path = path.to_path_buf();
        let file = crate::database_file::open_read_only(&main_path)?;
        let main_identity = crate::live_namespace::identity(&file)?;
        live_lock::lock_file_cancellable(&file, MAIN_LIFETIME_LOCK, Mode::Shared, cancellation)?;
        crate::live_namespace::verify_path(&main_path, main_identity)?;

        let (mut mapping, initial) = crate::database_file::map_reader(file, OpenMode::LiveReader)?;
        crate::live_cleanup::require_main_available(
            &main_path,
            main_identity,
            initial.meta.database_id,
        )?;
        let sidecar = Sidecar::open(&main_path, initial.meta.database_id)?;
        sidecar.lock_gate_cancellable(Mode::Exclusive, cancellation)?;
        let registration = register(
            &mut mapping,
            &main_path,
            main_identity,
            &sidecar,
            cancellation,
        );
        let (bootstrap, slot) = finish_with_cleanup(registration, sidecar.unlock_gate())?;
        let owner_identity = ProcessIdentity::capture();
        Ok(Self {
            reader: ReaderCore::new(mapping, bootstrap, Some(owner_identity)),
            main_path,
            main_identity,
            sidecar,
            slot,
            state: State::Open,
            owner_identity,
        })
    }

    pub(crate) fn info(&self) -> Result<DatabaseInfo> {
        Ok(self.reader()?.info())
    }

    pub(crate) fn read(&self) -> Result<GenerationReader<'_>> {
        Ok(self.reader()?.read())
    }

    pub(crate) fn reader(&self) -> Result<&ReaderCore> {
        self.require_open()?;
        Ok(&self.reader)
    }

    pub(crate) fn close(&mut self) -> Result<LiveReaderClose> {
        self.require_owner()?;
        if self.state == State::Closed {
            return Ok(reader_closed());
        }
        if matches!(self.state, State::Open | State::CloseOnly) {
            if let Err(cause) = self.sidecar.lock_gate(Mode::Shared) {
                self.state = State::CloseOnly;
                return Ok(reader_close_incomplete(cause));
            }
            self.state = State::GateHeldSlotActive;
        }
        if self.state == State::GateHeldSlotActive {
            if let Err(cause) = self.verify_registration() {
                return Ok(self.release_gate_after_failure(cause));
            }
            self.reader.unmap();
            self.state = State::GateHeldSlotClearing;
        }
        if self.state == State::GateHeldSlotClearing {
            if let Err(cause) = self.sidecar.clear_reader(self.slot) {
                return Ok(reader_close_incomplete(cause));
            }
            self.state = State::GateHeldSlotCleared;
        }
        if self.state == State::GateHeldSlotCleared {
            if let Err(cause) = self.sidecar.unlock_reader(self.slot) {
                return Ok(reader_close_incomplete(cause));
            }
            self.state = State::GateHeldSlotReleased;
        }
        if self.state == State::GateHeldSlotReleased {
            if let Err(cause) = self.sidecar.unlock_gate() {
                return Ok(reader_close_incomplete(cause));
            }
            self.state = State::MainLockOnly;
        }
        if self.state == State::MainLockOnly {
            if let Err(cause) = live_lock::unlock_file(self.reader.file(), MAIN_LIFETIME_LOCK) {
                return Ok(reader_close_incomplete(cause));
            }
            self.state = State::Closed;
        }
        Ok(reader_closed())
    }

    fn verify_registration(&self) -> Result<()> {
        crate::live_namespace::verify_path(&self.main_path, self.main_identity)?;
        self.sidecar.verify_path()?;
        self.sidecar.verify_header()?;
        self.sidecar
            .verify_reader(self.slot, self.reader.info().transaction_id)
    }

    fn release_gate_after_failure(&mut self, cause: Error) -> LiveReaderClose {
        match self.sidecar.unlock_gate() {
            Ok(()) => {
                self.state = State::CloseOnly;
                reader_close_incomplete(cause)
            }
            Err(cleanup) => reader_close_incomplete(Error::CleanupIncomplete {
                cause: Box::new(cause),
                cleanup: Box::new(cleanup),
            }),
        }
    }

    fn require_open(&self) -> Result<()> {
        self.require_owner()?;
        if self.state == State::Open {
            Ok(())
        } else {
            Err(Error::WrongState("live reader is closing or closed"))
        }
    }

    fn require_owner(&self) -> Result<()> {
        if !self.owner_identity.is_current() {
            return Err(Error::ForkedHandle);
        }
        Ok(())
    }
}

fn reader_closed() -> LiveReaderClose {
    LiveReaderClose {
        closed: true,
        coordination_cleanup: CoordinationCleanup::None,
        cause: None,
    }
}

fn reader_close_incomplete(cause: Error) -> LiveReaderClose {
    LiveReaderClose {
        closed: false,
        coordination_cleanup: CoordinationCleanup::RetainedReaderCloseRequired,
        cause: Some(cause),
    }
}

fn register(
    mapping: &mut crate::mapping::Mapping,
    main_path: &Path,
    main_identity: Identity,
    sidecar: &Sidecar,
    cancellation: &CancellationToken,
) -> Result<(crate::bootstrap::Bootstrap, u32)> {
    let bootstrap =
        select_registered_generation(mapping, main_path, main_identity, sidecar, cancellation)?;
    mapping.remap(bootstrap.committed_bytes)?;
    let slot = sidecar.claim_reader_cancellable(bootstrap.meta.txn_id, cancellation)?;
    cancellation.check()?;
    crate::live_namespace::verify_path(main_path, main_identity)?;
    sidecar.verify_path()?;
    Ok((bootstrap, slot))
}

fn select_registered_generation(
    mapping: &crate::mapping::Mapping,
    main_path: &Path,
    main_identity: Identity,
    sidecar: &Sidecar,
    cancellation: &CancellationToken,
) -> Result<crate::bootstrap::Bootstrap> {
    crate::live_namespace::verify_path(main_path, main_identity)?;
    sidecar.verify_path()?;
    let physical_bytes = mapping.file().metadata()?.len();
    let bootstrap =
        crate::database_file::bootstrap_mapping(mapping, physical_bytes, OpenMode::LiveReader)?;
    if bootstrap.meta.database_id != sidecar.header.database_id {
        return Err(Error::WrongMode(
            "reader table belongs to a different database",
        ));
    }
    sidecar.scan_at_most_cancellable(bootstrap.meta.txn_id, cancellation)?;
    Ok(bootstrap)
}
