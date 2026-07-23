//! Shared failed-open cleanup authority for Linux live claims.

use super::*;

#[derive(Debug)]
pub(crate) enum LinuxLiveCleanupError {
    ForkedHandle,
    Pair(LinuxLivePairError),
    Reader(LinuxReaderSlotError),
    Writer(LinuxWriterLeaseError),
}

#[derive(Debug)]
pub(super) enum OwnedLiveClaim {
    Reader(OwnedReaderSlot),
    Writer(OwnedWriterLease),
}

/// Opaque retry authority returned when live open cannot prove cleanup.
///
/// Dropping this value only closes this process's descriptor copies. It never
/// begins or continues a coordination transition.
#[derive(Debug)]
pub(crate) struct LinuxLiveCleanupGuard {
    files: Option<RetainedLiveFiles>,
    owned: Option<OwnedLiveClaim>,
    creator_pid: u32,
}

impl LinuxLiveCleanupGuard {
    pub(super) fn new(files: RetainedLiveFiles, owned: Option<OwnedLiveClaim>) -> Self {
        Self {
            files: Some(files),
            owned,
            creator_pid: std::process::id(),
        }
    }

    pub(crate) fn retry_cleanup(
        &mut self,
    ) -> Result<Option<LiveClaimCleanupOutcome>, LinuxLiveCleanupError> {
        self.check_creator()?;
        let Some(files) = self.files.as_mut() else {
            return Ok(None);
        };
        let outcome = retry_any_cleanup(files, self.owned.as_ref())?;
        self.owned = None;
        self.files = None;
        Ok(Some(outcome))
    }

    pub(crate) fn close(
        &mut self,
    ) -> Result<Option<LiveClaimCleanupOutcome>, LinuxLiveCleanupError> {
        self.retry_cleanup()
    }

    fn check_creator(&self) -> Result<(), LinuxLiveCleanupError> {
        if std::process::id() != self.creator_pid {
            return Err(LinuxLiveCleanupError::ForkedHandle);
        }
        Ok(())
    }

    #[cfg(test)]
    pub(super) fn files(&self) -> Option<&RetainedLiveFiles> {
        self.files.as_ref()
    }

    #[cfg(test)]
    pub(super) fn files_mut(&mut self) -> Option<&mut RetainedLiveFiles> {
        self.files.as_mut()
    }

    #[cfg(test)]
    pub(super) fn owned_reader(&self) -> Option<&OwnedReaderSlot> {
        match self.owned.as_ref() {
            Some(OwnedLiveClaim::Reader(owned)) => Some(owned),
            Some(OwnedLiveClaim::Writer(_)) | None => None,
        }
    }

    #[cfg(test)]
    pub(super) fn owned_writer(&self) -> Option<&OwnedWriterLease> {
        match self.owned.as_ref() {
            Some(OwnedLiveClaim::Writer(owned)) => Some(owned),
            Some(OwnedLiveClaim::Reader(_)) | None => None,
        }
    }

    #[cfg(test)]
    pub(super) fn make_forked_for_test(&mut self) {
        self.creator_pid = self.creator_pid.wrapping_add(1);
    }
}

pub(super) fn retry_any_cleanup(
    files: &mut RetainedLiveFiles,
    owned: Option<&OwnedLiveClaim>,
) -> Result<LiveClaimCleanupOutcome, LinuxLiveCleanupError> {
    if files.sidecar.has_dead_writer_cleanup() {
        match files.retry_dead_writer_cleanup() {
            Ok(()) => return Ok(files.live_cleanup_paths()),
            Err(_cause) if !requires_cleanup(files, owned) => {
                return Ok(files.live_cleanup_paths());
            }
            Err(cause) => return Err(LinuxLiveCleanupError::Pair(cause)),
        }
    }

    if files.writer_bootstrap().is_some()
        || files.writer_tail().is_some()
        || matches!(owned, Some(OwnedLiveClaim::Writer(_)))
        || files.sidecar.has_armed_writer_transition()
    {
        return files
            .retry_writer_lease_cleanup(match owned {
                Some(OwnedLiveClaim::Writer(owned)) => Some(owned),
                Some(OwnedLiveClaim::Reader(_)) | None => None,
            })
            .map_err(LinuxLiveCleanupError::Writer);
    }

    files
        .retry_reader_slot_cleanup(match owned {
            Some(OwnedLiveClaim::Reader(owned)) => Some(owned),
            Some(OwnedLiveClaim::Writer(_)) | None => None,
        })
        .map_err(LinuxLiveCleanupError::Reader)
}

pub(super) fn requires_cleanup(files: &RetainedLiveFiles, owned: Option<&OwnedLiveClaim>) -> bool {
    owned.is_some()
        || files.writer_bootstrap().is_some()
        || files.writer_tail().is_some()
        || files.sidecar.has_armed_transition()
        || files.sidecar.has_dead_writer_cleanup()
}
