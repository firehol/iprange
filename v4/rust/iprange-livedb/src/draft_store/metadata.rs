//! Metadata changes owned by the file-backed draft.

use crate::error::{Error, Result};
use crate::metadata;

use super::DraftStore;

impl DraftStore<'_> {
    pub(crate) fn set_metadata(&mut self, input: &[u8]) -> Result<bool> {
        self.require_metadata_available()?;
        let compressed = metadata::compress(input, self.budget.max_heap_bytes)?;
        let old_pages = metadata::collect_pages(self, &self.draft.base)?;
        let new_root = metadata::write_chain(self, &compressed)?;
        for &page in old_pages.as_slice() {
            self.retire_one(page)?;
        }
        self.draft.meta.metadata_root = new_root;
        self.draft.meta.metadata_uncompressed_len = input.len() as u64;
        self.draft.meta.metadata_compressed_len = compressed.len() as u64;
        self.finish_metadata_stage();
        Ok(true)
    }

    pub(crate) fn clear_metadata(&mut self) -> Result<bool> {
        self.require_metadata_available()?;
        if self.draft.meta.metadata_root == 0 {
            return Ok(false);
        }
        let old_pages = metadata::collect_pages(self, &self.draft.base)?;
        for &page in old_pages.as_slice() {
            self.retire_one(page)?;
        }
        self.draft.meta.metadata_root = 0;
        self.draft.meta.metadata_uncompressed_len = 0;
        self.draft.meta.metadata_compressed_len = 0;
        self.finish_metadata_stage();
        Ok(true)
    }

    fn require_metadata_available(&self) -> Result<()> {
        if self.draft.metadata_staged {
            return Err(Error::WrongState(
                "this transaction already staged metadata",
            ));
        }
        Ok(())
    }

    fn finish_metadata_stage(&mut self) {
        self.draft.metadata_staged = true;
        self.draft.changed = true;
    }
}
