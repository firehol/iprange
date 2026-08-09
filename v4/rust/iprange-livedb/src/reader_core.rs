//! Authoritative logical reads over one selected healthy generation.

mod cursor;
mod generation;

use std::fs::File;

use crate::bootstrap::Bootstrap;
use crate::database::DatabaseInfo;
use crate::error::Result;
use crate::live_namespace::Identity;
use crate::mapping::Mapping;
use crate::process_identity::ProcessIdentity;

pub(crate) use cursor::{MembershipRange, MembershipRangeCursor};
pub use cursor::{MembershipToken, ReaderCursor, ReaderCursorItem};
pub(crate) use generation::GenerationReader;

#[derive(Debug)]
pub(crate) struct ReaderCore {
    mapping: Mapping,
    bootstrap: Bootstrap,
    owner_identity: Option<ProcessIdentity>,
}

impl ReaderCore {
    pub(crate) fn new(
        mapping: Mapping,
        bootstrap: Bootstrap,
        owner_identity: Option<ProcessIdentity>,
    ) -> Self {
        Self {
            mapping,
            bootstrap,
            owner_identity,
        }
    }

    pub(crate) fn info(&self) -> DatabaseInfo {
        let meta = self.bootstrap.meta;
        DatabaseInfo {
            address_family: meta.address_family,
            value_kind: meta.value_kind,
            value_tag: meta.value_tag,
            database_id: meta.database_id,
            transaction_id: meta.txn_id,
            commit_nonce: meta.commit_nonce,
            page_count: meta.page_count,
            range_record_count: meta.range_record_count,
            active_feed_count: meta.active_feed_count,
            meta_selection: self.bootstrap.selection,
        }
    }

    pub(crate) fn file(&self) -> &File {
        self.mapping.file()
    }

    pub(crate) fn file_identity(&self) -> Result<Identity> {
        crate::live_namespace::identity(self.mapping.file())
    }

    pub(crate) fn unmap(&mut self) {
        self.mapping.unmap();
    }

    pub(crate) fn read(&self) -> GenerationReader<'_> {
        GenerationReader::new(&self.mapping, self.bootstrap.meta, self.owner_identity)
    }
}
