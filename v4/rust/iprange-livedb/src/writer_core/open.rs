//! Main-file mapping and committed-generation selection.

use std::fs::File;

use crate::bootstrap::OpenMode;
use crate::database;
use crate::draft_store::PageBudget;
use crate::error::Result;

use super::{WriterCore, WriterInfo};

impl WriterCore {
    pub(crate) fn map_writer(file: File, budget: PageBudget) -> Result<Self> {
        let (mapping, base) = database::map_writer(file)?;
        Ok(Self::new(mapping, base, budget))
    }

    pub(crate) fn select_committed(&mut self) -> Result<WriterInfo> {
        let physical_bytes = self.mapping.file().metadata()?.len();
        self.base = database::bootstrap_mapping(&self.mapping, physical_bytes, OpenMode::Writer)?;
        Ok(self.base_info())
    }

    pub(crate) fn trim_committed_tail(&mut self) -> Result<()> {
        if self.base.physical_bytes != self.base.committed_bytes {
            self.base.physical_bytes = self.mapping.shrink_or_retain(self.base.committed_bytes)?;
            self.mapping.sync_file()?;
        }
        Ok(())
    }
}
