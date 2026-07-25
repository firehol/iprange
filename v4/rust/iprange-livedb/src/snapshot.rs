//! Compact unsigned snapshots of one pinned v4 generation.

mod api;
#[cfg(target_os = "linux")]
mod build;
mod terminal;

use crate::error::{Error, Result};

pub use api::snapshot_to;
pub use terminal::{SnapshotPreparationFailure, SnapshotResult};

/// Source coordination used by one snapshot.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum SnapshotSourceMode {
    Immutable,
    Live,
}

/// Canonical destination publication policy.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum SnapshotPublicationPolicy {
    FailIfExists,
    ReplaceExisting,
}

/// Maximum simultaneously retained snapshot construction resources.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct SnapshotBudget {
    pub max_heap_bytes: u64,
    pub max_output_pages: u64,
    pub max_open_files: u32,
}

impl SnapshotBudget {
    pub const fn new(max_heap_bytes: u64, max_output_pages: u64, max_open_files: u32) -> Self {
        Self {
            max_heap_bytes,
            max_output_pages,
            max_open_files,
        }
    }

    pub(crate) fn validate(
        &self,
        mode: SnapshotSourceMode,
        policy: SnapshotPublicationPolicy,
    ) -> Result<()> {
        if self.max_output_pages < 2 {
            return Err(Error::BudgetExceeded("snapshot output pages"));
        }
        let required_files = if matches!(mode, SnapshotSourceMode::Live)
            || matches!(policy, SnapshotPublicationPolicy::ReplaceExisting)
        {
            3
        } else {
            2
        };
        if self.max_open_files < required_files {
            return Err(Error::BudgetExceeded("snapshot open files"));
        }
        Ok(())
    }
}

pub type SnapshotOutcome = std::result::Result<SnapshotResult, Box<SnapshotPreparationFailure>>;
