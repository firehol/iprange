//! Live recovery and snapshot source registration.

use super::*;
use crate::bootstrap::OpenMode;
use crate::contract::PAGE_SIZE;

impl LiveSource {
    pub(super) fn open(
        path: &Path,
        candidate: RecoveryCandidate,
        cancellation: &CancellationToken,
    ) -> std::result::Result<Source, SourceOpenFailure> {
        if candidate.label != RecoveryCandidateLabel::Newest {
            return Err(open_problem(Error::InvalidArgument(
                "live recovery requires the newest candidate",
            )));
        }
        open_file(path, cancellation, |file, identity| {
            open_candidate_locked(file, path, identity, candidate, cancellation)
        })
    }

    pub(super) fn open_current(
        path: &Path,
        cancellation: &CancellationToken,
    ) -> std::result::Result<Source, SourceOpenFailure> {
        open_file(path, cancellation, |file, identity| {
            open_current_locked(file, path, identity, cancellation)
        })
    }

    pub(super) fn final_check(
        &mut self,
        used: MetaV4,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.require_owner()?;
        cancellation.check()?;
        self.ensure_gate_cancellable(cancellation)?;
        if self.meta != used
            || self
                .candidate
                .is_some_and(|candidate| candidate.transaction_id != used.txn_id)
        {
            return Err(Error::RecoveryCandidateChanged);
        }
        verify_live_paths(&self.path, self.identity, &self.sidecar)?;
        self.sidecar
            .verify_reader(self.slot, used.txn_id)
            .map_err(live_coordination)
    }

    pub(super) fn release(&mut self) -> Result<()> {
        self.require_owner()?;
        self.release_slot()?;
        self.release_gate()?;
        self.release_lifetime()
    }

    fn release_slot(&mut self) -> Result<()> {
        if self.registration == RegistrationState::Released {
            return Ok(());
        }
        self.ensure_gate()?;
        if self.registration == RegistrationState::Active {
            self.registration = RegistrationState::Clearing;
        }
        if self.registration == RegistrationState::Clearing {
            self.sidecar
                .clear_reader(self.slot)
                .map_err(live_coordination)?;
            self.registration = RegistrationState::Cleared;
        }
        if self.registration == RegistrationState::Cleared {
            self.sidecar
                .unlock_reader(self.slot)
                .map_err(live_coordination)?;
            self.registration = RegistrationState::Released;
        }
        Ok(())
    }

    fn ensure_gate(&mut self) -> Result<()> {
        if !self.gate_locked {
            self.sidecar
                .lock_gate(Mode::Exclusive)
                .map_err(live_coordination)?;
            self.gate_locked = true;
        }
        Ok(())
    }

    fn ensure_gate_cancellable(&mut self, cancellation: &CancellationToken) -> Result<()> {
        if !self.gate_locked {
            live_lock::lock_cancellable(&self.sidecar.file, 0, Mode::Exclusive, cancellation)
                .map_err(live_coordination)?;
            self.gate_locked = true;
        }
        Ok(())
    }

    fn release_gate(&mut self) -> Result<()> {
        if self.gate_locked {
            self.sidecar.unlock_gate().map_err(live_coordination)?;
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

    fn require_owner(&self) -> Result<()> {
        if self.owner_pid != std::process::id() {
            Err(Error::ForkedHandle)
        } else {
            Ok(())
        }
    }
}

fn open_file(
    path: &Path,
    cancellation: &CancellationToken,
    open_locked: impl FnOnce(File, Identity) -> std::result::Result<LiveSource, LiveOpenFailure>,
) -> std::result::Result<Source, SourceOpenFailure> {
    let file = database::open_read_only(path).map_err(open_problem)?;
    let identity = live_sidecar::identity(&file).map_err(open_problem)?;
    live_lock::lock_file_cancellable(&file, MAIN_LIFETIME_LOCK, Mode::Shared, cancellation)
        .map_err(open_problem)?;
    finish_open(open_locked(file, identity))
}

fn finish_open(
    opened: std::result::Result<LiveSource, LiveOpenFailure>,
) -> std::result::Result<Source, SourceOpenFailure> {
    match opened {
        Ok(source) => Ok(Source::Live(source)),
        Err(LiveOpenFailure::Unclaimed(file, cause)) => Err(open_problem(combine_errors(
            cause,
            live_lock::unlock_file(&file, MAIN_LIFETIME_LOCK),
        ))),
        Err(LiveOpenFailure::Claimed(source, cause)) => {
            let end = Source::Live(*source).abandon(cause);
            Err(SourceOpenFailure {
                cause: end.cause.expect("failed open retains its cause"),
                guard: end.guard,
            })
        }
    }
}

fn open_candidate_locked(
    file: File,
    path: &Path,
    identity: Identity,
    candidate: RecoveryCandidate,
    cancellation: &CancellationToken,
) -> std::result::Result<LiveSource, LiveOpenFailure> {
    let initial = match bind_candidate(&file, path, identity, candidate, cancellation) {
        Ok(meta) => meta,
        Err(cause) => return Err(LiveOpenFailure::Unclaimed(file, cause)),
    };
    open_sidecar_locked(file, path, identity, initial, Some(candidate), cancellation)
}

fn open_current_locked(
    file: File,
    path: &Path,
    identity: Identity,
    cancellation: &CancellationToken,
) -> std::result::Result<LiveSource, LiveOpenFailure> {
    let initial = match bind_current(&file, path, identity, cancellation) {
        Ok(meta) => meta,
        Err(cause) => return Err(LiveOpenFailure::Unclaimed(file, cause)),
    };
    open_sidecar_locked(file, path, identity, initial, None, cancellation)
}

fn open_sidecar_locked(
    file: File,
    path: &Path,
    identity: Identity,
    initial: MetaV4,
    candidate: Option<RecoveryCandidate>,
    cancellation: &CancellationToken,
) -> std::result::Result<LiveSource, LiveOpenFailure> {
    let sidecar = match Sidecar::open(path, initial.database_id) {
        Ok(sidecar) => sidecar,
        Err(cause) => return Err(LiveOpenFailure::Unclaimed(file, live_coordination(cause))),
    };
    if let Err(cause) = live_lock::lock_cancellable(&sidecar.file, 0, Mode::Exclusive, cancellation)
        .map_err(live_coordination)
    {
        return Err(LiveOpenFailure::Unclaimed(file, cause));
    }
    let prepared = prepare_claim(
        &file,
        path,
        identity,
        &sidecar,
        candidate,
        initial,
        cancellation,
    );
    claim_or_release(
        file,
        path,
        identity,
        sidecar,
        candidate,
        prepared,
        cancellation,
    )
}

fn claim_or_release(
    file: File,
    path: &Path,
    identity: Identity,
    sidecar: Sidecar,
    candidate: Option<RecoveryCandidate>,
    prepared: Result<MetaV4>,
    cancellation: &CancellationToken,
) -> std::result::Result<LiveSource, LiveOpenFailure> {
    let meta = match prepared {
        Ok(meta) => meta,
        Err(cause) => {
            return Err(LiveOpenFailure::Unclaimed(
                file,
                combine_errors(cause, sidecar.unlock_gate()),
            ));
        }
    };
    match claim_prepared(file, path, identity, sidecar, candidate, meta, cancellation) {
        Ok(source) => Ok(source),
        Err(ClaimFailure::Unclaimed(file, sidecar, cause)) => Err(LiveOpenFailure::Unclaimed(
            file,
            combine_errors(cause, sidecar.unlock_gate()),
        )),
        Err(ClaimFailure::Claimed(mut source, cause)) => {
            if let Err(unlock) = source.sidecar.unlock_gate().map_err(live_coordination) {
                source.gate_locked = true;
                return Err(LiveOpenFailure::Claimed(
                    source,
                    combine_errors(cause, Err(unlock)),
                ));
            }
            source.gate_locked = false;
            Err(LiveOpenFailure::Claimed(source, cause))
        }
    }
}

fn prepare_claim(
    file: &File,
    path: &Path,
    identity: Identity,
    sidecar: &Sidecar,
    candidate: Option<RecoveryCandidate>,
    initial: MetaV4,
    cancellation: &CancellationToken,
) -> Result<MetaV4> {
    verify_live_paths(path, identity, sidecar)?;
    cancellation.check()?;
    let meta = match candidate {
        Some(candidate) => bind_candidate(file, path, identity, candidate, cancellation)?,
        None => bind_current(file, path, identity, cancellation)?,
    };
    if (candidate.is_some() && meta != initial) || meta.database_id != sidecar.header.database_id {
        return Err(Error::RecoveryCandidateChanged);
    }
    sidecar
        .scan_at_most_cancellable(meta.txn_id, cancellation)
        .map_err(live_coordination)?;
    Ok(meta)
}

#[allow(clippy::result_large_err)]
fn claim_prepared(
    file: File,
    path: &Path,
    identity: Identity,
    sidecar: Sidecar,
    candidate: Option<RecoveryCandidate>,
    meta: MetaV4,
    cancellation: &CancellationToken,
) -> std::result::Result<LiveSource, ClaimFailure> {
    let mapping = match Mapping::read_only_view(&file, meta.page_count * PAGE_SIZE as u64).and_then(
        |mut mapping| {
            mapping.set_unreadable_pages(&crate::worker::unreadable_source_pages())?;
            Ok(mapping)
        },
    ) {
        Ok(mapping) => mapping,
        Err(cause) => return Err(ClaimFailure::Unclaimed(file, sidecar, cause)),
    };
    let slot = match sidecar
        .claim_reader_cancellable(meta.txn_id, cancellation)
        .map_err(live_coordination)
    {
        Ok(slot) => slot,
        Err(cause) => return Err(ClaimFailure::Unclaimed(file, sidecar, cause)),
    };
    let mut source = LiveSource {
        mapping,
        file,
        path: path.to_path_buf(),
        identity,
        sidecar,
        slot,
        candidate,
        meta,
        gate_locked: true,
        registration: RegistrationState::Active,
        lifetime_locked: true,
        owner_pid: std::process::id(),
    };
    if let Err(cause) = verify_live_claim(&source) {
        return Err(ClaimFailure::Claimed(Box::new(source), cause));
    }
    if let Err(cause) = source.sidecar.unlock_gate().map_err(live_coordination) {
        return Err(ClaimFailure::Claimed(Box::new(source), cause));
    }
    source.gate_locked = false;
    Ok(source)
}

fn bind_candidate(
    file: &File,
    path: &Path,
    identity: Identity,
    candidate: RecoveryCandidate,
    cancellation: &CancellationToken,
) -> Result<MetaV4> {
    live_sidecar::verify_path(path, identity).map_err(candidate_changed)?;
    let classified = read_classified(file, cancellation)?;
    if classified.order == GenerationOrder::Unproven {
        return Err(Error::RecoveryCandidateChanged);
    }
    let meta = classified
        .selected_meta(&candidate)
        .ok_or(Error::RecoveryCandidateChanged)?;
    if candidate.label != RecoveryCandidateLabel::Newest {
        return Err(Error::RecoveryCandidateChanged);
    }
    crate::live_cleanup::require_main_available(path, identity, meta.database_id)?;
    live_sidecar::verify_path(path, identity).map_err(candidate_changed)?;
    Ok(meta)
}

fn bind_current(
    file: &File,
    path: &Path,
    identity: Identity,
    cancellation: &CancellationToken,
) -> Result<MetaV4> {
    live_sidecar::verify_path(path, identity).map_err(live_coordination)?;
    cancellation.check()?;
    let meta = database::bootstrap_file(file, OpenMode::LiveReader)?.meta;
    crate::live_cleanup::require_main_available(path, identity, meta.database_id)?;
    live_sidecar::verify_path(path, identity).map_err(live_coordination)?;
    Ok(meta)
}

fn verify_live_claim(source: &LiveSource) -> Result<()> {
    verify_live_paths(&source.path, source.identity, &source.sidecar)?;
    source
        .sidecar
        .verify_reader(source.slot, source.meta.txn_id)
        .map_err(live_coordination)
}

fn verify_live_paths(path: &Path, identity: Identity, sidecar: &Sidecar) -> Result<()> {
    live_sidecar::verify_path(path, identity)
        .and_then(|()| sidecar.verify_path())
        .and_then(|()| sidecar.verify_header())
        .map_err(live_coordination)
}

enum LiveOpenFailure {
    Unclaimed(File, Error),
    Claimed(Box<LiveSource>, Error),
}

enum ClaimFailure {
    Unclaimed(File, Sidecar, Error),
    Claimed(Box<LiveSource>, Error),
}
