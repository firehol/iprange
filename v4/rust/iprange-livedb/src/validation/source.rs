use std::path::{Path, PathBuf};

use crate::bootstrap::{BootstrapError, OpenMode};
use crate::contract::MetaV4;
use crate::database;
use crate::error::{Error, Result};
use crate::live_lock::{self, Mode};
use crate::live_sidecar::{self, Identity, MAIN_LIFETIME_LOCK};

use super::LocalFileIdentity;

pub(crate) struct ImmutableSource {
    pub(crate) file: std::fs::File,
    path: PathBuf,
    sidecar: PathBuf,
    identity: Identity,
}

pub(super) struct LiveSource {
    pub(super) file: std::fs::File,
    path: PathBuf,
    identity: Identity,
    sidecar: live_sidecar::Sidecar,
    slot: u32,
    pub(super) meta: MetaV4,
}

pub(super) enum LiveOpened {
    Selected(LiveSource),
    Bootstrap(LiveBootstrapSource, BootstrapError),
}

pub(super) struct LiveBootstrapSource {
    file: std::fs::File,
    path: PathBuf,
    identity: Identity,
    sidecar: live_sidecar::Sidecar,
}

impl ImmutableSource {
    pub(crate) fn open(path: &Path) -> Result<Self> {
        let sidecar = crate::path::canonical_sidecar(path)?;
        database::require_sidecar_absent(&sidecar)?;
        let file = database::open_read_only(path)?;
        let identity = live_sidecar::identity_any_link(&file)?;
        live_lock::lock(&file, MAIN_LIFETIME_LOCK, Mode::Shared)?;
        live_sidecar::verify_path_any_link(path, identity)?;
        database::require_sidecar_absent(&sidecar)?;
        let source = Self {
            file,
            path: path.to_path_buf(),
            sidecar,
            identity,
        };
        source.verify()?;
        Ok(source)
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
    pub(super) fn open(path: &Path) -> Result<LiveOpened> {
        let file = database::open_read_only(path)?;
        let identity = live_sidecar::identity(&file)?;
        live_lock::lock(&file, MAIN_LIFETIME_LOCK, Mode::Shared)?;
        live_sidecar::verify_path(path, identity)?;
        let database_id = selected_or_bound_database_id(&file)?;
        crate::live_cleanup::require_main_available(path, identity, database_id)?;
        let sidecar = live_sidecar::Sidecar::open(path, database_id)?;
        sidecar.lock_gate(Mode::Exclusive)?;
        let registration = register_live(&file, path, identity, &sidecar);
        finish_live_open(file, path, identity, sidecar, registration)
    }

    pub(super) fn public_identity(&self) -> LocalFileIdentity {
        public_identity(self.identity)
    }

    pub(super) fn close(self) -> Result<()> {
        self.sidecar.lock_gate(Mode::Shared)?;
        let released = self.sidecar.release_reader(self.slot);
        let main_path = live_sidecar::verify_path(&self.path, self.identity);
        let sidecar_path = self
            .sidecar
            .verify_path()
            .and_then(|()| self.sidecar.verify_header());
        let unlocked = self.sidecar.unlock_gate();
        released?;
        main_path?;
        sidecar_path?;
        unlocked
    }
}

fn finish_live_open(
    file: std::fs::File,
    path: &Path,
    identity: Identity,
    sidecar: live_sidecar::Sidecar,
    registration: Result<LiveRegistration>,
) -> Result<LiveOpened> {
    match registration {
        Ok(LiveRegistration::Selected(meta, slot)) => {
            sidecar.unlock_gate()?;
            Ok(LiveOpened::Selected(LiveSource {
                file,
                path: path.to_path_buf(),
                identity,
                sidecar,
                slot,
                meta,
            }))
        }
        Ok(LiveRegistration::Bootstrap(problem)) => Ok(LiveOpened::Bootstrap(
            LiveBootstrapSource {
                file,
                path: path.to_path_buf(),
                identity,
                sidecar,
            },
            problem,
        )),
        Err(cause) => Err(combine_cleanup(cause, sidecar.unlock_gate())),
    }
}

impl LiveBootstrapSource {
    pub(super) fn public_identity(&self) -> LocalFileIdentity {
        public_identity(self.identity)
    }

    pub(super) fn close(self) -> Result<()> {
        let main_path = live_sidecar::verify_path(&self.path, self.identity);
        let sidecar_path = self
            .sidecar
            .verify_path()
            .and_then(|()| self.sidecar.verify_header());
        let binding = selected_or_bound_database_id(&self.file).and_then(|database_id| {
            if database_id == self.sidecar.header.database_id {
                Ok(())
            } else {
                Err(Error::WrongMode(
                    "reader table belongs to a different database",
                ))
            }
        });
        let unlocked = self.sidecar.unlock_gate();
        main_path?;
        sidecar_path?;
        binding?;
        unlocked
    }
}

enum LiveRegistration {
    Selected(MetaV4, u32),
    Bootstrap(BootstrapError),
}

fn register_live(
    file: &std::fs::File,
    path: &Path,
    identity: Identity,
    sidecar: &live_sidecar::Sidecar,
) -> Result<LiveRegistration> {
    live_sidecar::verify_path(path, identity)?;
    sidecar.verify_path()?;
    sidecar.verify_header()?;
    let bootstrap = match database::bootstrap_file(file, OpenMode::LiveReader) {
        Ok(bootstrap) => bootstrap,
        Err(Error::Format(problem)) => {
            return register_bootstrap(file, path, identity, sidecar, problem)
        }
        Err(cause) => return Err(cause),
    };
    if bootstrap.meta.database_id != sidecar.header.database_id {
        return Err(Error::WrongMode(
            "reader table belongs to a different database",
        ));
    }
    sidecar.scan_at_most(bootstrap.meta.txn_id)?;
    let slot = sidecar.claim_reader(bootstrap.meta.txn_id)?;
    let verified = live_sidecar::verify_path(path, identity)
        .and_then(|()| sidecar.verify_path())
        .and_then(|()| sidecar.verify_header());
    if let Err(cause) = verified {
        return Err(combine_cleanup(cause, sidecar.release_reader(slot)));
    }
    Ok(LiveRegistration::Selected(bootstrap.meta, slot))
}

fn register_bootstrap(
    file: &std::fs::File,
    path: &Path,
    identity: Identity,
    sidecar: &live_sidecar::Sidecar,
    problem: BootstrapError,
) -> Result<LiveRegistration> {
    if selected_or_bound_database_id(file)? != sidecar.header.database_id {
        return Err(Error::WrongMode(
            "reader table belongs to a different database",
        ));
    }
    sidecar.scan_readers(|_| Ok(()))?;
    live_sidecar::verify_path(path, identity)?;
    sidecar.verify_path()?;
    sidecar.verify_header()?;
    Ok(LiveRegistration::Bootstrap(problem))
}

pub(crate) fn selected_or_bound_database_id(file: &std::fs::File) -> Result<[u8; 16]> {
    match database::bootstrap_file(file, OpenMode::LiveReader) {
        Ok(bootstrap) => Ok(bootstrap.meta.database_id),
        Err(Error::Format(_)) => {
            let mut metas = [0; 2 * crate::contract::PAGE_SIZE];
            crate::file_io::read_exact_at(file, &mut metas, 0)?;
            let meta0 = (&metas[..crate::contract::PAGE_SIZE]).try_into().unwrap();
            let meta1 = (&metas[crate::contract::PAGE_SIZE..]).try_into().unwrap();
            Ok(crate::bootstrap::database_id_from_meta_pages(meta0, meta1)?)
        }
        Err(cause) => Err(cause),
    }
}

pub(crate) fn public_identity(identity: Identity) -> LocalFileIdentity {
    LocalFileIdentity {
        kind: crate::publication::namespace::IDENTITY_KIND,
        bytes: identity.encode(),
    }
}

pub(crate) fn combine_cleanup(cause: Error, cleanup: Result<()>) -> Error {
    match cleanup {
        Ok(()) => cause,
        Err(cleanup) => Error::CleanupIncomplete {
            cause: Box::new(cause),
            cleanup: Box::new(cleanup),
        },
    }
}
