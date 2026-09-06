//! Authoritative logical reads over one selected healthy generation.

mod cursor;
mod generation;
mod live;

use std::fs::File;
use std::path::Path;

use crate::bootstrap::{Bootstrap, MetaSelection, OpenMode};
use crate::contract::{AddressFamily, DirectSemantic, StructureKind, ValueKind, ValueTag};
use crate::error::Result;
use crate::live_lock::{self, Mode};
use crate::live_namespace::Identity;
use crate::live_sidecar::MAIN_LIFETIME_LOCK;
use crate::mapping::Mapping;
use crate::process_identity::ProcessIdentity;

pub(crate) use cursor::{MembershipRange, MembershipRangeCursor};
pub use cursor::{MembershipToken, ReaderCursor, ReaderCursorItem};
pub(crate) use generation::GenerationReader;
pub(crate) use live::{LiveReaderClose, LiveReaderCore};

/// Public logical identity and selected generation.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct DatabaseInfo {
    pub address_family: AddressFamily,
    pub value_kind: ValueKind,
    pub structure_kind: StructureKind,
    pub value_tag: ValueTag,
    pub database_id: [u8; 16],
    pub transaction_id: u64,
    pub commit_nonce: [u8; 16],
    pub page_count: u64,
    pub range_record_count: u64,
    pub active_feed_count: u64,
    pub meta_selection: MetaSelection,
}

impl DatabaseInfo {
    /// Return the tag-derived semantic for a direct database.
    pub fn direct_semantic(&self) -> Option<DirectSemantic> {
        (self.value_kind == ValueKind::Direct).then(|| self.value_tag.direct_semantic())
    }
}

#[derive(Debug)]
pub(crate) struct ReaderCore {
    mapping: Mapping,
    bootstrap: Bootstrap,
    owner_identity: Option<ProcessIdentity>,
}

impl ReaderCore {
    fn new(
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

    pub(crate) fn open_immutable(path: &Path) -> Result<Self> {
        #[cfg(windows)]
        eprintln!("DIAG imm open step1 sidecar {:?}", crate::path::canonical_sidecar(path));
        let sidecar = crate::path::canonical_sidecar(path)?;
        #[cfg(windows)]
        eprintln!("DIAG imm open step2 require_sidecar_absent");
        crate::database_file::require_sidecar_absent(&sidecar)?;

        #[cfg(windows)]
        eprintln!("DIAG imm open step3 open_read_only");
        let file = crate::database_file::open_read_only(path)?;
        #[cfg(windows)]
        eprintln!("DIAG imm open step4 identity_any_link");
        let identity = crate::live_namespace::identity_any_link(&file)?;
        #[cfg(windows)]
        eprintln!("DIAG imm open step5 lock_file");
        live_lock::lock_file(&file, MAIN_LIFETIME_LOCK, Mode::Shared)?;
        #[cfg(windows)]
        eprintln!("DIAG imm open step6 verify_path_any_link");
        crate::live_namespace::verify_path_any_link(path, identity)?;
        #[cfg(windows)]
        eprintln!("DIAG imm open step7 require_sidecar_absent again");
        crate::database_file::require_sidecar_absent(&sidecar)?;

        #[cfg(windows)]
        eprintln!("DIAG imm open step8 map_reader");
        let (mapping, bootstrap) =
            crate::database_file::map_reader(file, OpenMode::ImmutableReader)?;
        #[cfg(windows)]
        eprintln!("DIAG imm open step9 require_main_available");
        crate::live_cleanup::require_main_available(path, identity, bootstrap.meta.database_id)?;
        #[cfg(windows)]
        eprintln!("DIAG imm open step10 verify_path_any_link again");
        crate::live_namespace::verify_path_any_link(path, identity)?;
        #[cfg(windows)]
        eprintln!("DIAG imm open step11 require_sidecar_absent third");
        crate::database_file::require_sidecar_absent(&sidecar)?;
        #[cfg(windows)]
        eprintln!("DIAG imm open done");
        Ok(Self::new(mapping, bootstrap, None))
    }

    pub(crate) fn info(&self) -> DatabaseInfo {
        let meta = self.bootstrap.meta;
        DatabaseInfo {
            address_family: meta.address_family,
            value_kind: meta.value_kind,
            structure_kind: meta
                .structure_kind()
                .expect("bootstrap rejects unsupported structures"),
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
