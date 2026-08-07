use std::path::{Path, PathBuf};

use crate::bootstrap::{BootstrapError, OpenMode};
use crate::cancellation::CancellationToken;
use crate::contract::MetaV4;
use crate::database;
use crate::error::{combine_errors, finish_with_cleanup, Error, Result};
use crate::live_lock::{self, Mode};
use crate::live_sidecar::{self, Identity, MAIN_LIFETIME_LOCK};
use crate::recovery::RecoverySourceCleanupGuard;

use super::LocalFileIdentity;

pub(crate) struct ImmutableSource {
    pub(crate) file: std::fs::File,
    path: PathBuf,
    sidecar: PathBuf,
    identity: Identity,
}

#[derive(Debug)]
pub(super) struct LiveSource {
    pub(super) file: std::fs::File,
    path: PathBuf,
    identity: Identity,
    sidecar: live_sidecar::Sidecar,
    slot: u32,
    pub(super) meta: MetaV4,
    gate_locked: bool,
    registration: RegistrationState,
    lifetime_locked: bool,
    owner_pid: u32,
}

pub(super) enum LiveOpened {
    Selected(LiveSource),
    Bootstrap(LiveBootstrapSource, BootstrapError),
}

#[derive(Debug)]
pub(super) struct LiveBootstrapSource {
    file: std::fs::File,
    path: PathBuf,
    identity: Identity,
    sidecar: live_sidecar::Sidecar,
    gate_locked: bool,
    lifetime_locked: bool,
    owner_pid: u32,
}

#[derive(Debug)]
pub(crate) struct ValidationCleanupSource(ValidationCleanupKind);

#[derive(Debug)]
enum ValidationCleanupKind {
    Selected(LiveSource),
    Bootstrap(LiveBootstrapSource),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum RegistrationState {
    Active,
    Clearing,
    Cleared,
    Released,
}

#[derive(Debug)]
pub(super) struct LiveSourceEnd {
    pub(super) cause: Option<Error>,
    pub(super) guard: Option<RecoverySourceCleanupGuard>,
}

#[derive(Debug)]
pub(super) struct LiveOpenFailure {
    pub(super) cause: Error,
    pub(super) guard: Option<RecoverySourceCleanupGuard>,
}

impl ImmutableSource {
    pub(crate) fn open(path: &Path, cancellation: &CancellationToken) -> Result<Self> {
        let sidecar = crate::path::canonical_sidecar(path)?;
        database::require_sidecar_absent(&sidecar)?;
        let file = database::open_read_only(path)?;
        let identity = live_sidecar::identity_any_link(&file)?;
        live_lock::lock_file_cancellable(&file, MAIN_LIFETIME_LOCK, Mode::Shared, cancellation)?;
        if let Err(cause) = live_sidecar::verify_path_any_link(path, identity)
            .and_then(|()| database::require_sidecar_absent(&sidecar))
        {
            return Err(combine_errors(
                cause,
                live_lock::unlock_file(&file, MAIN_LIFETIME_LOCK),
            ));
        }
        let source = Self {
            file,
            path: path.to_path_buf(),
            sidecar,
            identity,
        };
        match source.verify() {
            Ok(()) => Ok(source),
            Err(cause) => Err(combine_errors(
                cause,
                live_lock::unlock_file(&source.file, MAIN_LIFETIME_LOCK),
            )),
        }
    }

    pub(crate) fn verify(&self) -> Result<()> {
        live_sidecar::verify_path_any_link(&self.path, self.identity)?;
        database::require_sidecar_absent(&self.sidecar)
    }

    pub(crate) fn public_identity(&self) -> LocalFileIdentity {
        public_identity(self.identity)
    }

    pub(crate) fn require_available(&self, database_id: [u8; 16]) -> Result<()> {
        crate::live_cleanup::require_main_available(&self.path, self.identity, database_id)
    }
}

impl LiveSource {
    pub(super) fn open(
        path: &Path,
        cancellation: &CancellationToken,
    ) -> std::result::Result<LiveOpened, LiveOpenFailure> {
        let file = database::open_read_only(path).map_err(open_failure)?;
        let identity = live_sidecar::identity(&file).map_err(open_failure)?;
        live_lock::lock_file_cancellable(&file, MAIN_LIFETIME_LOCK, Mode::Shared, cancellation)
            .map_err(open_failure)?;
        finish_live_open(open_live_locked(file, path, identity, cancellation))
    }

    pub(super) fn public_identity(&self) -> LocalFileIdentity {
        public_identity(self.identity)
    }

    pub(super) fn finish(self, operation: Result<()>) -> LiveSourceEnd {
        let mut source = ValidationCleanupSource(ValidationCleanupKind::Selected(self));
        let released = source.release();
        terminal(source, operation.err(), released)
    }
}

fn open_live_locked(
    file: std::fs::File,
    path: &Path,
    identity: Identity,
    cancellation: &CancellationToken,
) -> std::result::Result<LiveOpened, LiveOpenStageFailure> {
    let database_id = match bind_live_main(&file, path, identity) {
        Ok(database_id) => database_id,
        Err(cause) => return Err(LiveOpenStageFailure::Unclaimed(file, cause)),
    };
    let sidecar = match live_sidecar::Sidecar::open(path, database_id) {
        Ok(sidecar) => sidecar,
        Err(cause) => return Err(LiveOpenStageFailure::Unclaimed(file, cause)),
    };
    if let Err(cause) = sidecar.lock_gate_cancellable(Mode::Exclusive, cancellation) {
        return Err(LiveOpenStageFailure::Unclaimed(file, cause));
    }
    match register_live(&file, path, identity, &sidecar, cancellation) {
        Ok(LiveRegistration::Selected(meta, slot)) => {
            let mut source = selected_source(file, path, identity, sidecar, slot, meta);
            match source.release_gate() {
                Ok(()) => Ok(LiveOpened::Selected(source)),
                Err(cause) => Err(LiveOpenStageFailure::Claimed(Box::new(source), cause)),
            }
        }
        Ok(LiveRegistration::Bootstrap(problem)) => Ok(LiveOpened::Bootstrap(
            bootstrap_source(file, path, identity, sidecar),
            problem,
        )),
        Err(RegistrationFailure::Unclaimed(cause)) => Err(LiveOpenStageFailure::Unclaimed(
            file,
            combine_errors(cause, sidecar.unlock_gate()),
        )),
        Err(RegistrationFailure::Claimed(claimed)) => Err(LiveOpenStageFailure::Claimed(
            Box::new(selected_source(
                file,
                path,
                identity,
                sidecar,
                claimed.slot,
                claimed.meta,
            )),
            claimed.cause,
        )),
    }
}

impl LiveBootstrapSource {
    pub(super) fn public_identity(&self) -> LocalFileIdentity {
        public_identity(self.identity)
    }

    pub(super) fn finish(self, operation: Result<()>) -> LiveSourceEnd {
        let verified = self.verify();
        let mut source = ValidationCleanupSource(ValidationCleanupKind::Bootstrap(self));
        let released = source.release();
        terminal(
            source,
            finish_with_cleanup(operation, verified).err(),
            released,
        )
    }

    fn verify(&self) -> Result<()> {
        live_sidecar::verify_path(&self.path, self.identity)?;
        self.sidecar.verify_path()?;
        self.sidecar.verify_header()?;
        selected_or_bound_database_id(&self.file).and_then(|database_id| {
            if database_id == self.sidecar.header.database_id {
                Ok(())
            } else {
                Err(Error::WrongMode(
                    "reader table belongs to a different database",
                ))
            }
        })
    }
}

enum LiveRegistration {
    Selected(MetaV4, u32),
    Bootstrap(BootstrapError),
}

enum RegistrationFailure {
    Unclaimed(Error),
    Claimed(Box<ClaimedRegistration>),
}

enum LiveOpenStageFailure {
    Unclaimed(std::fs::File, Error),
    Claimed(Box<LiveSource>, Error),
}

struct ClaimedRegistration {
    meta: MetaV4,
    slot: u32,
    cause: Error,
}

impl ValidationCleanupSource {
    pub(crate) fn release(&mut self) -> Result<()> {
        match &mut self.0 {
            ValidationCleanupKind::Selected(source) => source.release(),
            ValidationCleanupKind::Bootstrap(source) => source.release(),
        }
    }
}

impl LiveSource {
    fn release(&mut self) -> Result<()> {
        self.require_owner()?;
        self.release_registration()?;
        self.release_gate()?;
        self.release_lifetime()
    }

    fn release_registration(&mut self) -> Result<()> {
        if self.registration != RegistrationState::Released {
            self.ensure_gate()?;
            self.begin_registration_clear()?;
            self.clear_registration()?;
            self.unlock_registration()?;
        }
        Ok(())
    }

    fn begin_registration_clear(&mut self) -> Result<()> {
        if self.registration == RegistrationState::Active {
            if let Err(cause) = self.verify_registration() {
                return Err(combine_errors(cause, self.release_gate()));
            }
            self.registration = RegistrationState::Clearing;
        }
        Ok(())
    }

    fn clear_registration(&mut self) -> Result<()> {
        if self.registration == RegistrationState::Clearing {
            self.sidecar.clear_reader(self.slot)?;
            self.registration = RegistrationState::Cleared;
        }
        Ok(())
    }

    fn unlock_registration(&mut self) -> Result<()> {
        if self.registration == RegistrationState::Cleared {
            self.sidecar.unlock_reader(self.slot)?;
            self.registration = RegistrationState::Released;
        }
        Ok(())
    }

    fn ensure_gate(&mut self) -> Result<()> {
        if !self.gate_locked {
            self.sidecar.lock_gate(Mode::Shared)?;
            self.gate_locked = true;
        }
        Ok(())
    }

    fn release_gate(&mut self) -> Result<()> {
        if self.gate_locked {
            self.sidecar.unlock_gate()?;
            self.gate_locked = false;
        }
        Ok(())
    }

    fn release_lifetime(&mut self) -> Result<()> {
        if self.lifetime_locked {
            live_lock::unlock_file(&self.file, MAIN_LIFETIME_LOCK)?;
            self.lifetime_locked = false;
        }
        Ok(())
    }

    fn verify_registration(&self) -> Result<()> {
        live_sidecar::verify_path(&self.path, self.identity)?;
        self.sidecar.verify_path()?;
        self.sidecar.verify_header()?;
        self.sidecar.verify_reader(self.slot, self.meta.txn_id)
    }

    fn require_owner(&self) -> Result<()> {
        if self.owner_pid == std::process::id() {
            Ok(())
        } else {
            Err(Error::ForkedHandle)
        }
    }
}

impl LiveBootstrapSource {
    fn release(&mut self) -> Result<()> {
        if self.owner_pid != std::process::id() {
            return Err(Error::ForkedHandle);
        }
        if self.gate_locked {
            self.sidecar.unlock_gate()?;
            self.gate_locked = false;
        }
        if self.lifetime_locked {
            live_lock::unlock_file(&self.file, MAIN_LIFETIME_LOCK)?;
            self.lifetime_locked = false;
        }
        Ok(())
    }
}

fn bind_live_main(file: &std::fs::File, path: &Path, identity: Identity) -> Result<[u8; 16]> {
    live_sidecar::verify_path(path, identity)?;
    let database_id = selected_or_bound_database_id(file)?;
    crate::live_cleanup::require_main_available(path, identity, database_id)?;
    Ok(database_id)
}

fn selected_source(
    file: std::fs::File,
    path: &Path,
    identity: Identity,
    sidecar: live_sidecar::Sidecar,
    slot: u32,
    meta: MetaV4,
) -> LiveSource {
    LiveSource {
        file,
        path: path.to_path_buf(),
        identity,
        sidecar,
        slot,
        meta,
        gate_locked: true,
        registration: RegistrationState::Active,
        lifetime_locked: true,
        owner_pid: std::process::id(),
    }
}

fn bootstrap_source(
    file: std::fs::File,
    path: &Path,
    identity: Identity,
    sidecar: live_sidecar::Sidecar,
) -> LiveBootstrapSource {
    LiveBootstrapSource {
        file,
        path: path.to_path_buf(),
        identity,
        sidecar,
        gate_locked: true,
        lifetime_locked: true,
        owner_pid: std::process::id(),
    }
}

fn finish_live_open(
    opened: std::result::Result<LiveOpened, LiveOpenStageFailure>,
) -> std::result::Result<LiveOpened, LiveOpenFailure> {
    match opened {
        Ok(source) => Ok(source),
        Err(LiveOpenStageFailure::Unclaimed(file, cause)) => Err(open_failure(combine_errors(
            cause,
            live_lock::unlock_file(&file, MAIN_LIFETIME_LOCK),
        ))),
        Err(LiveOpenStageFailure::Claimed(source, cause)) => {
            let end = (*source).finish(Err(cause));
            Err(LiveOpenFailure {
                cause: end.cause.expect("failed live open retains its cause"),
                guard: end.guard,
            })
        }
    }
}

fn terminal(
    source: ValidationCleanupSource,
    cause: Option<Error>,
    released: Result<()>,
) -> LiveSourceEnd {
    match released {
        Ok(()) => LiveSourceEnd { cause, guard: None },
        Err(cleanup) => {
            let guard = RecoverySourceCleanupGuard::for_validation(source, &cleanup);
            let cause = Some(match cause {
                Some(cause) => combine_errors(cause, Err(cleanup)),
                None => cleanup,
            });
            LiveSourceEnd {
                cause,
                guard: Some(guard),
            }
        }
    }
}

fn open_failure(cause: Error) -> LiveOpenFailure {
    LiveOpenFailure { cause, guard: None }
}

fn register_live(
    file: &std::fs::File,
    path: &Path,
    identity: Identity,
    sidecar: &live_sidecar::Sidecar,
    cancellation: &CancellationToken,
) -> std::result::Result<LiveRegistration, RegistrationFailure> {
    live_sidecar::verify_path(path, identity).map_err(RegistrationFailure::Unclaimed)?;
    sidecar
        .verify_path()
        .map_err(RegistrationFailure::Unclaimed)?;
    sidecar
        .verify_header()
        .map_err(RegistrationFailure::Unclaimed)?;
    let bootstrap = match database::bootstrap_file_faultable(file, OpenMode::LiveReader) {
        Ok(bootstrap) => bootstrap,
        Err(Error::Format(problem)) => {
            return register_bootstrap(file, path, identity, sidecar, problem, cancellation)
                .map_err(RegistrationFailure::Unclaimed)
        }
        Err(cause) => return Err(RegistrationFailure::Unclaimed(cause)),
    };
    if bootstrap.meta.database_id != sidecar.header.database_id {
        return Err(RegistrationFailure::Unclaimed(Error::WrongMode(
            "reader table belongs to a different database",
        )));
    }
    sidecar
        .scan_at_most_cancellable(bootstrap.meta.txn_id, cancellation)
        .map_err(RegistrationFailure::Unclaimed)?;
    let slot = sidecar
        .claim_reader_cancellable(bootstrap.meta.txn_id, cancellation)
        .map_err(RegistrationFailure::Unclaimed)?;
    let verified = cancellation
        .check()
        .and_then(|()| live_sidecar::verify_path(path, identity))
        .and_then(|()| sidecar.verify_path())
        .and_then(|()| sidecar.verify_header());
    if let Err(cause) = verified {
        return Err(RegistrationFailure::Claimed(Box::new(
            ClaimedRegistration {
                meta: bootstrap.meta,
                slot,
                cause,
            },
        )));
    }
    Ok(LiveRegistration::Selected(bootstrap.meta, slot))
}

fn register_bootstrap(
    file: &std::fs::File,
    path: &Path,
    identity: Identity,
    sidecar: &live_sidecar::Sidecar,
    problem: BootstrapError,
    cancellation: &CancellationToken,
) -> Result<LiveRegistration> {
    if selected_or_bound_database_id(file)? != sidecar.header.database_id {
        return Err(Error::WrongMode(
            "reader table belongs to a different database",
        ));
    }
    sidecar.scan_readers_cancellable(cancellation, |_| Ok(()))?;
    live_sidecar::verify_path(path, identity)?;
    sidecar.verify_path()?;
    sidecar.verify_header()?;
    Ok(LiveRegistration::Bootstrap(problem))
}

pub(crate) fn selected_or_bound_database_id(file: &std::fs::File) -> Result<[u8; 16]> {
    match database::bootstrap_file_faultable(file, OpenMode::LiveReader) {
        Ok(bootstrap) => Ok(bootstrap.meta.database_id),
        Err(Error::Format(_)) => database::database_id_from_file_faultable(file),
        Err(cause) => Err(cause),
    }
}

pub(crate) fn public_identity(identity: Identity) -> LocalFileIdentity {
    LocalFileIdentity {
        kind: crate::publication::namespace::IDENTITY_KIND,
        bytes: identity.encode(),
    }
}

#[cfg(test)]
#[path = "source_tests.rs"]
mod tests;
