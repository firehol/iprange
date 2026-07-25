//! Linux/POSIX offline live-coordination transitions.

use std::path::{Path, PathBuf};

use crate::bootstrap::{Bootstrap, OpenMode};
use crate::cancellation::CancellationToken;
use crate::database;
use crate::error::{Error, Result};
use crate::live_lock::{self, Mode};
use crate::live_sidecar::{self, Identity, Sidecar, MAIN_LIFETIME_LOCK};
use crate::live_writer::LocalBasename;
use crate::random;
use crate::validation::LocalFileIdentity;

use super::{
    LiveCoordinationLocation, LiveTransitionOperation, LiveTransitionResult, LiveTransitionStatus,
};

pub(super) struct LockedMain {
    pub(super) path: PathBuf,
    pub(super) file: std::fs::File,
    pub(super) identity: Identity,
    pub(super) directory_identity: LocalFileIdentity,
    pub(super) public_identity: LocalFileIdentity,
    pub(super) basename: LocalBasename,
    pub(super) bootstrap: Bootstrap,
}

struct Attempt {
    operation: LiveTransitionOperation,
    database_id: [u8; 16],
    transaction_id: u64,
    commit_nonce: [u8; 16],
    directory_identity: LocalFileIdentity,
    main_identity: LocalFileIdentity,
    main_basename: LocalBasename,
    reader_capacity: u32,
    sidecar_id: [u8; 16],
    previous_sidecar_identity: Option<LocalFileIdentity>,
}

/// Convert one quiescent immutable database into a live database.
pub fn initialize_live(
    path: impl AsRef<Path>,
    reader_capacity: u32,
    cancellation: &CancellationToken,
) -> Result<LiveTransitionResult> {
    require_capacity(reader_capacity)?;
    let main = LockedMain::open(path.as_ref(), cancellation)?;
    database::require_sidecar_absent(&crate::path::canonical_sidecar(&main.path)?)?;
    let attempt = Attempt::new(
        LiveTransitionOperation::Initialize,
        &main,
        reader_capacity,
        None,
    )?;
    cancellation.check()?;

    let sidecar = match Sidecar::reserve(
        &main.path,
        attempt.database_id,
        attempt.sidecar_id,
        reader_capacity,
    ) {
        Ok(sidecar) => sidecar,
        Err(cause) => return Ok(attempt.reservation_failure(cause)),
    };
    let identity = sidecar.local_identity();
    let new_identity = live_sidecar::public_identity(identity);
    if let Err(cause) = cancellation.check() {
        return Ok(attempt.cleanup_created(
            &sidecar.path,
            identity,
            new_identity,
            cause,
            LiveCoordinationLocation::Canonical,
        ));
    }
    if let Err(cause) = initialize_sidecar(&main, &sidecar, cancellation) {
        return Ok(attempt.cleanup_created(
            &sidecar.path,
            identity,
            new_identity,
            cause,
            LiveCoordinationLocation::Canonical,
        ));
    }
    Ok(attempt.initialized(new_identity))
}

/// Replace missing, corrupt, or obsolete live coordination while quiescent.
pub fn reset_live_coordination(
    path: impl AsRef<Path>,
    reader_capacity: u32,
    cancellation: &CancellationToken,
) -> Result<LiveTransitionResult> {
    require_capacity(reader_capacity)?;
    let main = LockedMain::open(path.as_ref(), cancellation)?;
    let canonical = crate::path::canonical_sidecar(&main.path)?;
    let previous = existing_identity(&canonical)?;
    let attempt = Attempt::new(
        LiveTransitionOperation::Reset,
        &main,
        reader_capacity,
        previous.map(live_sidecar::public_identity),
    )?;
    let private = crate::path::live_transition_temp(&main.path)?;
    cancellation.check()?;

    let sidecar = match Sidecar::reserve_at(
        private,
        attempt.database_id,
        attempt.sidecar_id,
        reader_capacity,
    ) {
        Ok(sidecar) => sidecar,
        Err(cause) => return Ok(attempt.reservation_failure(cause)),
    };
    let identity = sidecar.local_identity();
    let new_identity = live_sidecar::public_identity(identity);
    if let Err(cause) = cancellation.check() {
        return Ok(attempt.cleanup_created(
            &sidecar.path,
            identity,
            new_identity,
            cause,
            LiveCoordinationLocation::Private,
        ));
    }
    if let Err(cause) = prepare_reset_sidecar(&main, &sidecar, cancellation) {
        return Ok(attempt.cleanup_created(
            &sidecar.path,
            identity,
            new_identity,
            cause,
            LiveCoordinationLocation::Private,
        ));
    }
    if let Err(cause) = verify_previous(&canonical, previous) {
        return Ok(attempt.cleanup_created(
            &sidecar.path,
            identity,
            new_identity,
            cause,
            LiveCoordinationLocation::Private,
        ));
    }

    crate::fault::crash("live_reset.before_replace");
    if let Err(cause) =
        super::namespace::install(&sidecar.path, &sidecar.file, &canonical, identity, previous)
    {
        if cause.residue_possible() {
            return Ok(attempt.unknown(
                new_identity,
                LiveCoordinationLocation::Unclassified,
                cause,
            ));
        }
        return Ok(attempt.cleanup_created(
            &sidecar.path,
            identity,
            new_identity,
            cause,
            LiveCoordinationLocation::Private,
        ));
    }
    crate::fault::crash("live_reset.after_replace");
    match finish_reset(&main, &sidecar, &canonical, identity, previous) {
        Ok(None) => Ok(attempt.initialized(new_identity)),
        Ok(Some(cause)) => Ok(attempt.initialized_with_residue(new_identity, cause)),
        Err(cause) => Ok(attempt.unknown(new_identity, LiveCoordinationLocation::Canonical, cause)),
    }
}

impl LockedMain {
    pub(super) fn open(path: &Path, cancellation: &CancellationToken) -> Result<Self> {
        let path = path.to_path_buf();
        let file = live_sidecar::open_rw(&path)?;
        let identity = live_sidecar::identity(&file)?;
        let directory_identity = live_sidecar::parent_identity(&path)?;
        let public_identity = live_sidecar::public_identity(identity);
        let basename = LocalBasename::from_path(&path)?;
        live_lock::lock_cancellable(&file, MAIN_LIFETIME_LOCK, Mode::Exclusive, cancellation)?;
        cancellation.check()?;
        live_sidecar::verify_path(&path, identity)?;
        let bootstrap = database::bootstrap_file(&file, OpenMode::Writer)?;
        if bootstrap.physical_bytes != bootstrap.committed_bytes {
            return Err(Error::WrongState(
                "offline transition requires exact committed length",
            ));
        }
        Ok(Self {
            path,
            file,
            identity,
            directory_identity,
            public_identity,
            basename,
            bootstrap,
        })
    }

    pub(super) fn verify(&self) -> Result<()> {
        live_sidecar::verify_path(&self.path, self.identity)?;
        let current = database::bootstrap_file(&self.file, OpenMode::Writer)?;
        if current.meta != self.bootstrap.meta
            || current.committed_bytes != self.bootstrap.committed_bytes
            || current.physical_bytes != self.bootstrap.physical_bytes
        {
            return Err(Error::CleanupConflict(
                "main generation changed during live transition",
            ));
        }
        Ok(())
    }
}

impl Attempt {
    fn new(
        operation: LiveTransitionOperation,
        main: &LockedMain,
        reader_capacity: u32,
        previous_sidecar_identity: Option<LocalFileIdentity>,
    ) -> Result<Self> {
        Ok(Self {
            operation,
            database_id: main.bootstrap.meta.database_id,
            transaction_id: main.bootstrap.meta.txn_id,
            commit_nonce: main.bootstrap.meta.commit_nonce,
            directory_identity: main.directory_identity,
            main_identity: main.public_identity,
            main_basename: main.basename,
            reader_capacity,
            sidecar_id: random::nonzero_128()?,
            previous_sidecar_identity,
        })
    }

    fn reservation_failure(self, cause: Error) -> LiveTransitionResult {
        let (status, location) = if cause.residue_possible() {
            (
                LiveTransitionStatus::OutcomeUnknown,
                LiveCoordinationLocation::Unclassified,
            )
        } else {
            (
                LiveTransitionStatus::Unchanged,
                LiveCoordinationLocation::Absent,
            )
        };
        let residue_possible = cause.residue_possible();
        self.result(status, None, location, residue_possible, Some(cause))
    }

    fn initialized(self, new_identity: LocalFileIdentity) -> LiveTransitionResult {
        self.result(
            LiveTransitionStatus::Initialized,
            Some(new_identity),
            LiveCoordinationLocation::Canonical,
            false,
            None,
        )
    }

    fn initialized_with_residue(
        self,
        new_identity: LocalFileIdentity,
        cause: Error,
    ) -> LiveTransitionResult {
        self.result(
            LiveTransitionStatus::Initialized,
            Some(new_identity),
            LiveCoordinationLocation::Canonical,
            true,
            Some(cause),
        )
    }

    fn unknown(
        self,
        new_identity: LocalFileIdentity,
        location: LiveCoordinationLocation,
        cause: Error,
    ) -> LiveTransitionResult {
        self.result(
            LiveTransitionStatus::OutcomeUnknown,
            Some(new_identity),
            location,
            true,
            Some(cause),
        )
    }

    fn cleanup_created(
        self,
        path: &Path,
        identity: Identity,
        public_identity: LocalFileIdentity,
        cause: Error,
        location: LiveCoordinationLocation,
    ) -> LiveTransitionResult {
        match remove_exact(path, identity) {
            Ok(()) => self.result(
                LiveTransitionStatus::Unchanged,
                Some(public_identity),
                LiveCoordinationLocation::Absent,
                false,
                Some(cause),
            ),
            Err(cleanup) => self.unknown(
                public_identity,
                location,
                Error::CleanupIncomplete {
                    cause: Box::new(cause),
                    cleanup: Box::new(cleanup),
                },
            ),
        }
    }

    fn result(
        self,
        status: LiveTransitionStatus,
        new_sidecar_identity: Option<LocalFileIdentity>,
        new_sidecar_location: LiveCoordinationLocation,
        residue_possible: bool,
        cause: Option<Error>,
    ) -> LiveTransitionResult {
        LiveTransitionResult {
            operation: self.operation,
            status,
            database_id: self.database_id,
            transaction_id: self.transaction_id,
            commit_nonce: self.commit_nonce,
            directory_identity: self.directory_identity,
            main_identity: self.main_identity,
            main_basename: self.main_basename,
            reader_capacity: self.reader_capacity,
            sidecar_id: self.sidecar_id,
            previous_sidecar_identity: self.previous_sidecar_identity,
            new_sidecar_identity,
            new_sidecar_location,
            residue_possible,
            cause,
        }
    }
}

fn initialize_sidecar(
    main: &LockedMain,
    sidecar: &Sidecar,
    cancellation: &CancellationToken,
) -> Result<()> {
    cancellation.check()?;
    sidecar.initialize_creating()?;
    crate::fault::crash("live_initialize.after_creating_sync");
    cancellation.check()?;
    live_sidecar::sync_parent(&sidecar.path)?;
    crate::fault::crash("live_initialize.after_creating_parent_sync");
    cancellation.check()?;
    main.verify()?;
    cancellation.check()?;
    sidecar.publish_ready()?;
    crate::fault::crash("live_initialize.after_ready_sync");
    live_sidecar::sync_parent(&sidecar.path)?;
    crate::fault::crash("live_initialize.after_ready_parent_sync");
    Ok(())
}

fn prepare_reset_sidecar(
    main: &LockedMain,
    sidecar: &Sidecar,
    cancellation: &CancellationToken,
) -> Result<()> {
    cancellation.check()?;
    sidecar.initialize_creating()?;
    crate::fault::crash("live_reset.after_creating_sync");
    cancellation.check()?;
    sidecar.publish_ready()?;
    crate::fault::crash("live_reset.after_ready_sync");
    cancellation.check()?;
    live_sidecar::sync_parent(&sidecar.path)?;
    crate::fault::crash("live_reset.after_private_parent_sync");
    cancellation.check()?;
    main.verify()
}

fn finish_reset(
    main: &LockedMain,
    sidecar: &Sidecar,
    canonical: &Path,
    identity: Identity,
    previous: Option<Identity>,
) -> Result<Option<Error>> {
    live_sidecar::sync_parent(canonical)?;
    crate::fault::crash("live_reset.after_directory_sync");
    main.verify()?;
    live_sidecar::verify_path(canonical, identity)?;
    sidecar.verify_header()?;
    if let Some(previous) = previous {
        if let Err(cause) = remove_exact(&sidecar.path, previous) {
            return Ok(Some(cause));
        }
    }
    Ok(None)
}

pub(super) fn existing_identity(path: &Path) -> Result<Option<Identity>> {
    live_sidecar::path_identity(path)
}

pub(super) fn verify_previous(path: &Path, previous: Option<Identity>) -> Result<()> {
    match previous {
        Some(identity) => live_sidecar::verify_path(path, identity),
        None => match live_sidecar::path_identity(path)? {
            None => Ok(()),
            Some(_) => Err(Error::CleanupConflict(
                "canonical sidecar appeared during reset",
            )),
        },
    }
}

pub(super) fn remove_exact(path: &Path, identity: Identity) -> Result<()> {
    live_sidecar::remove_exact(path, identity)
}

fn require_capacity(capacity: u32) -> Result<()> {
    if capacity == 0 {
        Err(Error::InvalidArgument(
            "reader capacity must be greater than zero",
        ))
    } else {
        Ok(())
    }
}
