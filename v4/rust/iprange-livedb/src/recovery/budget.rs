use std::path::PathBuf;

use crate::error::{Error, Result};

/// Maximum simultaneously retained resources for one recovery operation.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct RecoveryBudget {
    pub max_heap_bytes: u64,
    pub max_output_pages: u64,
    pub max_open_files: u32,
    pub max_scratch_bytes: u64,
    pub max_scratch_files: u32,
    pub scratch_directory: Option<PathBuf>,
}

impl RecoveryBudget {
    /// Recovery budget which forbids external scratch files.
    pub const fn heap_only(
        max_heap_bytes: u64,
        max_output_pages: u64,
        max_open_files: u32,
    ) -> Self {
        Self {
            max_heap_bytes,
            max_output_pages,
            max_open_files,
            max_scratch_bytes: 0,
            max_scratch_files: 0,
            scratch_directory: None,
        }
    }

    pub(crate) fn validate(&self) -> Result<()> {
        if self.max_open_files < 2 {
            return Err(Error::BudgetExceeded(
                "recovery requires source and output files",
            ));
        }
        if self.max_output_pages < 2 {
            return Err(Error::BudgetExceeded("recovery output pages"));
        }
        let scratch_limits = self.max_scratch_bytes != 0 && self.max_scratch_files != 0;
        if scratch_limits != self.scratch_directory.is_some()
            || (self.max_scratch_bytes == 0) != (self.max_scratch_files == 0)
        {
            return Err(Error::InvalidArgument(
                "recovery scratch path and limits must be supplied together",
            ));
        }
        Ok(())
    }
}
