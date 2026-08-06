//! Reader pinned to one registered live generation.

use std::path::{Path, PathBuf};

use crate::bootstrap::OpenMode;
use crate::cancellation::CancellationToken;
use crate::contract::MetaV4;
use crate::database::{self, DatabaseInfo, ReaderCore};
use crate::error::{finish_with_cleanup, Error, Result};
use crate::feed::FeedEntry;
use crate::feed_catalog::FeedCursor;
use crate::feed_range_cursor::{FeedRangeCursorV4, FeedRangeCursorV6};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::live_lock::{self, Mode};
use crate::live_sidecar::{self, Identity, Sidecar, MAIN_LIFETIME_LOCK};
use crate::live_writer::CloseOutcome;
use crate::mapping::Mapping;
use crate::membership_view::MembershipView;
use crate::publication::{CleanupState, CoordinationCleanup};
use crate::range_cursor::{DirectCursorV4, DirectCursorV6, RangeDirection};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum State {
    Open,
    CloseOnly,
    GateHeldSlotActive,
    GateHeldSlotClearing,
    GateHeldSlotCleared,
    GateHeldSlotReleased,
    MainLockOnly,
    Closed,
}

/// Factual, retryable live-reader close result.
#[derive(Debug)]
pub struct ReaderCloseResult {
    pub outcome: CloseOutcome,
    pub coordination_cleanup: CoordinationCleanup,
    pub cause: Option<Error>,
}

impl ReaderCloseResult {
    pub const fn cleanup_state(&self) -> CleanupState {
        if matches!(self.coordination_cleanup, CoordinationCleanup::None) {
            CleanupState::Clean
        } else {
            CleanupState::ResiduePossible
        }
    }
}

/// Reader registered against one committed generation of a live database.
#[derive(Debug)]
pub struct LiveReader {
    core: ReaderCore,
    main_path: PathBuf,
    main_identity: Identity,
    sidecar: Sidecar,
    slot: u32,
    state: State,
    owner_pid: u32,
}

impl LiveReader {
    /// Open and register a live reader without validating either page graph.
    pub fn open(path: impl AsRef<Path>, cancellation: &CancellationToken) -> Result<Self> {
        cancellation.check()?;
        let main_path = path.as_ref().to_path_buf();
        let file = database::open_read_only(&main_path)?;
        let main_identity = live_sidecar::identity(&file)?;
        live_lock::lock_cancellable(&file, MAIN_LIFETIME_LOCK, Mode::Shared, cancellation)?;
        live_sidecar::verify_path(&main_path, main_identity)?;

        let (mut mapping, initial) = database::map_reader(file, OpenMode::LiveReader)?;
        crate::live_cleanup::require_main_available(
            &main_path,
            main_identity,
            initial.meta.database_id,
        )?;
        let sidecar = Sidecar::open(&main_path, initial.meta.database_id)?;
        sidecar.lock_gate_cancellable(Mode::Exclusive, cancellation)?;
        let registration = register(
            &mut mapping,
            &main_path,
            main_identity,
            &sidecar,
            cancellation,
        );
        let (bootstrap, slot) = finish_with_cleanup(registration, sidecar.unlock_gate())?;
        Ok(Self {
            core: ReaderCore::new(mapping, bootstrap),
            main_path,
            main_identity,
            sidecar,
            slot,
            state: State::Open,
            owner_pid: std::process::id(),
        })
    }

    /// Identity and counters from this reader's pinned generation.
    pub fn info(&self) -> Result<DatabaseInfo> {
        self.require_open()?;
        Ok(self.core.info())
    }

    /// Look up one address in an IPv4 direct-value database.
    pub fn lookup_direct_v4(&self, address: Ipv4Key) -> Result<Option<u32>> {
        self.require_open()?;
        self.core.lookup_direct_v4(address)
    }

    /// Look up one address in an IPv6 direct-value database.
    pub fn lookup_direct_v6(&self, address: Ipv6Key) -> Result<Option<u32>> {
        self.require_open()?;
        self.core.lookup_direct_v6(address)
    }

    /// Open an ordered cursor over an IPv4 direct-value database.
    pub fn direct_cursor_v4(&self, direction: RangeDirection) -> Result<DirectCursorV4<'_>> {
        self.require_open()?;
        self.core.direct_cursor_v4_live(direction, self.owner_pid)
    }

    /// Open an ordered cursor over an IPv6 direct-value database.
    pub fn direct_cursor_v6(&self, direction: RangeDirection) -> Result<DirectCursorV6<'_>> {
        self.require_open()?;
        self.core.direct_cursor_v6_live(direction, self.owner_pid)
    }

    /// Look up one exact feed name in this pinned membership generation.
    pub fn lookup_feed(&self, name: &str) -> Result<Option<FeedEntry>> {
        self.require_open()?;
        self.core.lookup_feed(name)
    }

    /// Enumerate this generation's feeds in ascending feed-index order.
    pub fn feed_cursor(&self) -> Result<FeedCursor<'_>> {
        self.require_open()?;
        self.core.feed_cursor_live(self.owner_pid)
    }

    /// Open an ordered cursor over one exact IPv4 named feed.
    pub fn feed_range_cursor_v4(
        &self,
        name: &str,
        direction: RangeDirection,
    ) -> Result<FeedRangeCursorV4<'_>> {
        self.require_open()?;
        self.core
            .feed_range_cursor_v4_live(name, direction, self.owner_pid)
    }

    /// Open an ordered cursor over one exact IPv6 named feed.
    pub fn feed_range_cursor_v6(
        &self,
        name: &str,
        direction: RangeDirection,
    ) -> Result<FeedRangeCursorV6<'_>> {
        self.require_open()?;
        self.core
            .feed_range_cursor_v6_live(name, direction, self.owner_pid)
    }

    /// Look up one address in this pinned IPv4 membership generation.
    pub fn lookup_membership_v4(&self, address: Ipv4Key) -> Result<Option<MembershipView<'_>>> {
        self.require_open()?;
        self.core
            .lookup_membership_v4(address, Some(self.owner_pid))
    }

    /// Look up one address in this pinned IPv6 membership generation.
    pub fn lookup_membership_v6(&self, address: Ipv6Key) -> Result<Option<MembershipView<'_>>> {
        self.require_open()?;
        self.core
            .lookup_membership_v6(address, Some(self.owner_pid))
    }

    /// Exact decompressed metadata length, or absence.
    pub fn metadata_json_len(&self) -> Result<Option<u64>> {
        self.require_open()?;
        Ok(self.core.metadata_json_len())
    }

    /// Fill caller storage with this generation's exact opaque metadata bytes.
    pub fn read_metadata_json(&self, output: &mut [u8]) -> Result<Option<usize>> {
        self.require_open()?;
        self.core.read_metadata_json(output)
    }

    /// Return this generation's complete bounded metadata value, or absence.
    pub fn metadata_json(&self) -> Result<Option<Vec<u8>>> {
        self.require_open()?;
        self.core.metadata_json()
    }

    pub(crate) fn import_parts(&self) -> Result<(&Mapping, MetaV4)> {
        self.require_open()?;
        Ok(self.core.import_parts())
    }

    pub(crate) fn c_abi_parts(&self) -> Result<(&Mapping, MetaV4, Option<u32>)> {
        self.require_open()?;
        let (mapping, meta) = self.core.import_parts();
        Ok((mapping, meta, Some(self.owner_pid)))
    }

    /// Clear this registration. An incomplete close retains retry authority.
    pub fn close(&mut self) -> Result<ReaderCloseResult> {
        self.require_owner()?;
        if self.state == State::Closed {
            return Ok(reader_closed());
        }
        if matches!(self.state, State::Open | State::CloseOnly) {
            if let Err(cause) = self.sidecar.lock_gate(Mode::Shared) {
                self.state = State::CloseOnly;
                return Ok(reader_close_incomplete(cause));
            }
            self.state = State::GateHeldSlotActive;
        }
        if self.state == State::GateHeldSlotActive {
            if let Err(cause) = self.verify_registration() {
                return Ok(self.release_gate_after_failure(cause));
            }
            self.state = State::GateHeldSlotClearing;
        }
        if self.state == State::GateHeldSlotClearing {
            if let Err(cause) = self.sidecar.clear_reader(self.slot) {
                return Ok(reader_close_incomplete(cause));
            }
            self.state = State::GateHeldSlotCleared;
        }
        if self.state == State::GateHeldSlotCleared {
            if let Err(cause) = self.sidecar.unlock_reader(self.slot) {
                return Ok(reader_close_incomplete(cause));
            }
            self.state = State::GateHeldSlotReleased;
        }
        if self.state == State::GateHeldSlotReleased {
            if let Err(cause) = self.sidecar.unlock_gate() {
                return Ok(reader_close_incomplete(cause));
            }
            self.state = State::MainLockOnly;
        }
        if self.state == State::MainLockOnly {
            if let Err(cause) = live_lock::unlock(self.core.file(), MAIN_LIFETIME_LOCK) {
                return Ok(reader_close_incomplete(cause));
            }
            self.state = State::Closed;
        }
        Ok(reader_closed())
    }

    fn verify_registration(&self) -> Result<()> {
        live_sidecar::verify_path(&self.main_path, self.main_identity)?;
        self.sidecar.verify_path()?;
        self.sidecar.verify_header()?;
        self.sidecar
            .verify_reader(self.slot, self.core.info().transaction_id)
    }

    fn release_gate_after_failure(&mut self, cause: Error) -> ReaderCloseResult {
        match self.sidecar.unlock_gate() {
            Ok(()) => {
                self.state = State::CloseOnly;
                reader_close_incomplete(cause)
            }
            Err(cleanup) => reader_close_incomplete(Error::CleanupIncomplete {
                cause: Box::new(cause),
                cleanup: Box::new(cleanup),
            }),
        }
    }

    fn require_open(&self) -> Result<()> {
        self.require_owner()?;
        if self.state == State::Open {
            Ok(())
        } else {
            Err(Error::WrongState("live reader is closing or closed"))
        }
    }

    fn require_owner(&self) -> Result<()> {
        if self.owner_pid != std::process::id() {
            return Err(Error::ForkedHandle);
        }
        Ok(())
    }
}

fn reader_closed() -> ReaderCloseResult {
    ReaderCloseResult {
        outcome: CloseOutcome::Closed,
        coordination_cleanup: CoordinationCleanup::None,
        cause: None,
    }
}

fn reader_close_incomplete(cause: Error) -> ReaderCloseResult {
    ReaderCloseResult {
        outcome: CloseOutcome::CloseIncomplete,
        coordination_cleanup: CoordinationCleanup::RetainedReaderCloseRequired,
        cause: Some(cause),
    }
}

fn register(
    mapping: &mut Mapping,
    main_path: &Path,
    main_identity: Identity,
    sidecar: &Sidecar,
    cancellation: &CancellationToken,
) -> Result<(crate::bootstrap::Bootstrap, u32)> {
    let bootstrap =
        select_registered_generation(mapping, main_path, main_identity, sidecar, cancellation)?;
    mapping.remap(bootstrap.committed_bytes)?;
    let slot = sidecar.claim_reader_cancellable(bootstrap.meta.txn_id, cancellation)?;
    cancellation.check()?;
    live_sidecar::verify_path(main_path, main_identity)?;
    sidecar.verify_path()?;
    Ok((bootstrap, slot))
}

fn select_registered_generation(
    mapping: &Mapping,
    main_path: &Path,
    main_identity: Identity,
    sidecar: &Sidecar,
    cancellation: &CancellationToken,
) -> Result<crate::bootstrap::Bootstrap> {
    live_sidecar::verify_path(main_path, main_identity)?;
    sidecar.verify_path()?;
    let physical_bytes = mapping.file().metadata()?.len();
    let bootstrap = database::bootstrap_mapping(mapping, physical_bytes, OpenMode::LiveReader)?;
    if bootstrap.meta.database_id != sidecar.header.database_id {
        return Err(Error::WrongMode(
            "reader table belongs to a different database",
        ));
    }
    sidecar.scan_at_most_cancellable(bootstrap.meta.txn_id, cancellation)?;
    Ok(bootstrap)
}
