//! Immutable and caller-quiesced recovery source protection.

use super::*;
use crate::contract::PAGE_SIZE;

impl BasicSource {
    pub(super) fn open(
        path: &Path,
        candidate: RecoveryCandidate,
        immutable: bool,
        cancellation: &CancellationToken,
    ) -> Result<Self> {
        let sidecar = sidecar_path(path, immutable)?;
        require_sidecar_absent(sidecar.as_deref())?;
        let file = open_file(path, immutable)?;
        let identity = live_sidecar::identity_any_link(&file)?;
        live_lock::lock_file_cancellable(
            &file,
            MAIN_LIFETIME_LOCK,
            lifetime_mode(immutable),
            cancellation,
        )?;
        finish_open(file, path, sidecar, identity, candidate, cancellation)
    }

    pub(super) fn open_current(path: &Path, cancellation: &CancellationToken) -> Result<Self> {
        let sidecar = crate::path::canonical_sidecar(path)?;
        require_sidecar_absent(Some(&sidecar))?;
        let file = database::open_read_only(path)?;
        let identity = live_sidecar::identity_any_link(&file)?;
        live_lock::lock_file_cancellable(&file, MAIN_LIFETIME_LOCK, Mode::Shared, cancellation)?;
        finish_current_open(file, path, sidecar, identity, cancellation)
    }

    pub(super) fn final_check(&self, used: MetaV4, cancellation: &CancellationToken) -> Result<()> {
        if self.meta != used {
            return Err(Error::RecoveryCandidateChanged);
        }
        let selected = match self.selection {
            BasicSelection::Candidate(candidate) => bind(
                &self.file,
                &self.path,
                self.sidecar.as_deref(),
                self.identity,
                candidate,
                cancellation,
            )?,
            BasicSelection::Current => bind_current(
                &self.file,
                &self.path,
                self.sidecar.as_deref(),
                self.identity,
                cancellation,
            )?,
        };
        if selected != used {
            return Err(Error::RecoveryCandidateChanged);
        }
        Ok(())
    }

    pub(super) fn release(&mut self) -> Result<()> {
        if self.lifetime_locked {
            live_lock::unlock_file(&self.file, MAIN_LIFETIME_LOCK)?;
            self.lifetime_locked = false;
        }
        Ok(())
    }
}

fn finish_open(
    file: File,
    path: &Path,
    sidecar: Option<PathBuf>,
    identity: Identity,
    candidate: RecoveryCandidate,
    cancellation: &CancellationToken,
) -> Result<BasicSource> {
    match bind(
        &file,
        path,
        sidecar.as_deref(),
        identity,
        candidate,
        cancellation,
    ) {
        Ok(meta) => Ok(BasicSource {
            mapping: map_available(&file, meta)?,
            file,
            path: path.to_path_buf(),
            sidecar,
            identity,
            selection: BasicSelection::Candidate(candidate),
            meta,
            lifetime_locked: true,
        }),
        Err(cause) => Err(combine_errors(
            cause,
            live_lock::unlock_file(&file, MAIN_LIFETIME_LOCK),
        )),
    }
}

fn finish_current_open(
    file: File,
    path: &Path,
    sidecar: PathBuf,
    identity: Identity,
    cancellation: &CancellationToken,
) -> Result<BasicSource> {
    match bind_current(&file, path, Some(&sidecar), identity, cancellation) {
        Ok(meta) => Ok(BasicSource {
            mapping: map_available(&file, meta)?,
            file,
            path: path.to_path_buf(),
            sidecar: Some(sidecar),
            identity,
            selection: BasicSelection::Current,
            meta,
            lifetime_locked: true,
        }),
        Err(cause) => Err(combine_errors(
            cause,
            live_lock::unlock_file(&file, MAIN_LIFETIME_LOCK),
        )),
    }
}

fn sidecar_path(path: &Path, immutable: bool) -> Result<Option<PathBuf>> {
    immutable
        .then(|| crate::path::canonical_sidecar(path))
        .transpose()
}

fn require_sidecar_absent(sidecar: Option<&Path>) -> Result<()> {
    if let Some(path) = sidecar {
        database::require_sidecar_absent(path)?;
    }
    Ok(())
}

fn open_file(path: &Path, immutable: bool) -> Result<File> {
    if immutable {
        database::open_read_only(path)
    } else {
        live_sidecar::open_rw(path)
    }
}

fn lifetime_mode(immutable: bool) -> Mode {
    if immutable {
        Mode::Shared
    } else {
        Mode::Exclusive
    }
}

fn map_available(file: &File, meta: MetaV4) -> Result<Mapping> {
    let declared = meta
        .page_count
        .checked_mul(PAGE_SIZE as u64)
        .ok_or(Error::ArithmeticOverflow("recovery source mapping length"))?;
    let available = file.metadata()?.len().min(declared);
    let mut mapping = Mapping::read_only_view(file, available)?;
    mapping.set_unreadable_pages(&crate::worker::unreadable_source_pages())?;
    Ok(mapping)
}

fn bind(
    file: &File,
    path: &Path,
    sidecar: Option<&Path>,
    identity: Identity,
    candidate: RecoveryCandidate,
    cancellation: &CancellationToken,
) -> Result<MetaV4> {
    verify_path(path, sidecar, identity)?;
    let meta = select(file, public_identity(identity), candidate, cancellation)?;
    crate::live_cleanup::require_main_available(path, identity, meta.database_id)?;
    verify_path(path, sidecar, identity)?;
    Ok(meta)
}

fn bind_current(
    file: &File,
    path: &Path,
    sidecar: Option<&Path>,
    identity: Identity,
    cancellation: &CancellationToken,
) -> Result<MetaV4> {
    verify_path(path, sidecar, identity)?;
    cancellation.check()?;
    let meta = database::bootstrap_file(file, crate::bootstrap::OpenMode::ImmutableReader)?.meta;
    crate::live_cleanup::require_main_available(path, identity, meta.database_id)?;
    verify_path(path, sidecar, identity)?;
    Ok(meta)
}

fn verify_path(path: &Path, sidecar: Option<&Path>, identity: Identity) -> Result<()> {
    live_sidecar::verify_path_any_link(path, identity).map_err(candidate_changed)?;
    if let Some(path) = sidecar {
        database::require_sidecar_absent(path).map_err(candidate_changed)?;
    }
    Ok(())
}

fn select(
    file: &File,
    identity: crate::validation::LocalFileIdentity,
    candidate: RecoveryCandidate,
    cancellation: &CancellationToken,
) -> Result<MetaV4> {
    if candidate.source_identity != identity {
        return Err(Error::RecoveryCandidateChanged);
    }
    read_classified(file, cancellation)?
        .selected_meta(&candidate)
        .ok_or(Error::RecoveryCandidateChanged)
}
