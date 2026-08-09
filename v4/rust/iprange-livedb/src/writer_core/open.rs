//! Main-file mapping and committed-generation selection.

use std::fs::File;

use crate::bootstrap::OpenMode;
use crate::database_file;
use crate::draft_store::PageBudget;
use crate::error::Result;

use super::{WriterCore, WriterInfo};

impl WriterCore {
    pub(crate) fn map_writer(
        file: File,
        max_heap_bytes: u64,
        max_private_pages: u64,
        max_growth_pages: u64,
    ) -> Result<Self> {
        let (mapping, base) = database_file::map_writer(file)?;
        let budget = PageBudget {
            max_heap_bytes,
            max_private_pages,
            max_growth_pages,
        };
        Ok(Self::new(mapping, base, budget))
    }

    pub(crate) fn select_committed(&mut self) -> Result<WriterInfo> {
        let physical_bytes = self.mapping.file().metadata()?.len();
        self.base =
            database_file::bootstrap_mapping(&self.mapping, physical_bytes, OpenMode::Writer)?;
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
