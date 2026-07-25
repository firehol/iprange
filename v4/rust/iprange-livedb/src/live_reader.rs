//! Reader pinned to one registered live generation.

use std::path::{Path, PathBuf};

use crate::bootstrap::OpenMode;
use crate::database::{self, DatabaseInfo, ReaderCore};
use crate::error::{Error, Result};
use crate::feed::FeedEntry;
use crate::feed_catalog::FeedCursor;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::live_lock::{self, Mode};
use crate::live_sidecar::{self, Identity, Sidecar, MAIN_LIFETIME_LOCK};
use crate::range_cursor::{DirectCursorV4, DirectCursorV6, RangeDirection};

/// Reader registered against one committed generation of a live database.
#[derive(Debug)]
pub struct LiveReader {
    core: ReaderCore,
    main_path: PathBuf,
    main_identity: Identity,
    sidecar: Sidecar,
    slot: u32,
    owner_pid: u32,
}

impl LiveReader {
    /// Open and register a live reader without validating either page graph.
    pub fn open(path: impl AsRef<Path>) -> Result<Self> {
        let main_path = path.as_ref().to_path_buf();
        let file = database::open_read_only(&main_path)?;
        let main_identity = live_sidecar::identity(&file)?;
        live_lock::lock(&file, MAIN_LIFETIME_LOCK, Mode::Shared)?;
        live_sidecar::verify_path(&main_path, main_identity)?;

        let initial = database::bootstrap_file(&file, OpenMode::LiveReader)?;
        let sidecar = Sidecar::open(&main_path, initial.meta.database_id)?;
        sidecar.lock_gate(Mode::Exclusive)?;
        let registration = register(&file, &main_path, main_identity, &sidecar);
        let unlocked = sidecar.unlock_gate();

        let (bootstrap, slot) = match (registration, unlocked) {
            (Ok(registered), Ok(())) => registered,
            (Err(error), _) | (Ok(_), Err(error)) => return Err(error),
        };
        Ok(Self {
            core: ReaderCore::new(file, bootstrap),
            main_path,
            main_identity,
            sidecar,
            slot,
            owner_pid: std::process::id(),
        })
    }

    /// Identity and counters from this reader's pinned generation.
    pub fn info(&self) -> Result<DatabaseInfo> {
        self.require_owner()?;
        Ok(self.core.info())
    }

    /// Look up one address in an IPv4 direct-value database.
    pub fn lookup_direct_v4(&self, address: Ipv4Key) -> Result<Option<u32>> {
        self.require_owner()?;
        self.core.lookup_direct_v4(address)
    }

    /// Look up one address in an IPv6 direct-value database.
    pub fn lookup_direct_v6(&self, address: Ipv6Key) -> Result<Option<u32>> {
        self.require_owner()?;
        self.core.lookup_direct_v6(address)
    }

    /// Open an ordered cursor over an IPv4 direct-value database.
    pub fn direct_cursor_v4(&self, direction: RangeDirection) -> Result<DirectCursorV4<'_>> {
        self.require_owner()?;
        self.core.direct_cursor_v4_live(direction, self.owner_pid)
    }

    /// Open an ordered cursor over an IPv6 direct-value database.
    pub fn direct_cursor_v6(&self, direction: RangeDirection) -> Result<DirectCursorV6<'_>> {
        self.require_owner()?;
        self.core.direct_cursor_v6_live(direction, self.owner_pid)
    }

    /// Look up one exact feed name in this pinned membership generation.
    pub fn lookup_feed(&self, name: &str) -> Result<Option<FeedEntry>> {
        self.require_owner()?;
        self.core.lookup_feed(name)
    }

    /// Enumerate this generation's feeds in ascending feed-index order.
    pub fn feed_cursor(&self) -> Result<FeedCursor<'_>> {
        self.require_owner()?;
        self.core.feed_cursor_live(self.owner_pid)
    }

    /// Exact decompressed metadata length, or absence.
    pub fn metadata_json_len(&self) -> Result<Option<u64>> {
        self.require_owner()?;
        Ok(self.core.metadata_json_len())
    }

    /// Fill caller storage with this generation's exact opaque metadata bytes.
    pub fn read_metadata_json(&self, output: &mut [u8]) -> Result<Option<usize>> {
        self.require_owner()?;
        self.core.read_metadata_json(output)
    }

    /// Return this generation's complete bounded metadata value, or absence.
    pub fn metadata_json(&self) -> Result<Option<Vec<u8>>> {
        self.require_owner()?;
        self.core.metadata_json()
    }

    /// Clear this registration. Dropping without close remains crash-safe.
    pub fn close(self) -> Result<()> {
        self.require_owner()?;
        self.sidecar.lock_gate(Mode::Shared)?;

        let released = self.sidecar.release_reader(self.slot);
        let main_path = live_sidecar::verify_path(&self.main_path, self.main_identity);
        let sidecar_path = self.sidecar.verify_path();
        let unlocked = self.sidecar.unlock_gate();

        released?;
        main_path?;
        sidecar_path?;
        unlocked
    }

    fn require_owner(&self) -> Result<()> {
        if self.owner_pid != std::process::id() {
            return Err(Error::ForkedHandle);
        }
        Ok(())
    }
}

fn register(
    file: &std::fs::File,
    main_path: &Path,
    main_identity: Identity,
    sidecar: &Sidecar,
) -> Result<(crate::bootstrap::Bootstrap, u32)> {
    let bootstrap = select_registered_generation(file, main_path, main_identity, sidecar)?;
    let slot = sidecar.claim_reader(bootstrap.meta.txn_id)?;
    live_sidecar::verify_path(main_path, main_identity)?;
    sidecar.verify_path()?;
    Ok((bootstrap, slot))
}

fn select_registered_generation(
    file: &std::fs::File,
    main_path: &Path,
    main_identity: Identity,
    sidecar: &Sidecar,
) -> Result<crate::bootstrap::Bootstrap> {
    live_sidecar::verify_path(main_path, main_identity)?;
    sidecar.verify_path()?;
    let bootstrap = database::bootstrap_file(file, OpenMode::LiveReader)?;
    if bootstrap.meta.database_id != sidecar.header.database_id {
        return Err(Error::WrongMode(
            "reader table belongs to a different database",
        ));
    }
    sidecar.scan_at_most(bootstrap.meta.txn_id)?;
    Ok(bootstrap)
}
