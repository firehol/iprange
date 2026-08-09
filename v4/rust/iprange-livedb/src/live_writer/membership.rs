//! Transaction-bound membership catalog references.

use std::fmt;

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, MembershipOperation, ValueKind};
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::feed_catalog::FeedCursor;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::writer_core::MembershipHandle;

use super::workflow::{check_transaction, require_transaction};
use super::{CommitResult, LiveWriter};

const INACTIVE: &str = "membership transaction is no longer active";

/// One SDK-owned feed reference valid only in its creating transaction.
#[derive(Clone, Copy, PartialEq, Eq)]
pub struct FeedRef {
    database_id: [u8; 16],
    operation_nonce: [u8; 16],
    entry: FeedEntry,
}

/// One SDK-owned membership valid only in its creating transaction.
#[derive(Clone, Copy, PartialEq, Eq)]
pub struct MembershipRef {
    database_id: [u8; 16],
    operation_nonce: [u8; 16],
    handle: MembershipHandle,
    catalog_epoch: u64,
}

impl fmt::Debug for MembershipRef {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output.debug_struct("MembershipRef").finish_non_exhaustive()
    }
}

impl FeedRef {
    /// Return the feed's current structural name.
    pub const fn name(self) -> FeedName {
        self.entry.name
    }

    /// Return the feed's current structural index.
    pub const fn index(self) -> u32 {
        self.entry.index
    }
}

impl fmt::Debug for FeedRef {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output
            .debug_struct("FeedRef")
            .field("name", &self.entry.name)
            .finish_non_exhaustive()
    }
}

/// One advanced logical membership operation over a clean live writer.
#[derive(Debug)]
pub struct MembershipTransaction<'a> {
    writer: &'a mut LiveWriter,
    state: MembershipState,
}

/// Borrow-free membership-operation state shared with language bindings.
#[derive(Debug)]
pub(crate) struct MembershipState {
    database_id: [u8; 16],
    operation_nonce: [u8; 16],
    membership_epoch: u64,
    cancellation: CancellationToken,
}

/// Ordered transaction-bound feed enumeration.
#[derive(Debug)]
pub struct TransactionFeedCursor<'a> {
    cursor: FeedCursor<'a>,
    database_id: [u8; 16],
    operation_nonce: [u8; 16],
}

impl LiveWriter {
    /// Begin one advanced membership transaction on a clean writer.
    pub fn begin_membership_transaction(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<MembershipTransaction<'_>> {
        let state = self.begin_membership_state(cancellation)?;
        Ok(MembershipTransaction {
            writer: self,
            state,
        })
    }

    pub(crate) fn begin_membership_state(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<MembershipState> {
        cancellation.check()?;
        self.require_healthy()?;
        if self.core.base_info().value_kind != ValueKind::Membership {
            return Err(Error::WrongValueKind(
                "membership transaction requires a membership database",
            ));
        }
        let database_id = self.core.base_info().database_id;
        let operation_nonce = self.core.begin_transaction()?;
        Ok(MembershipState {
            database_id,
            operation_nonce,
            membership_epoch: 0,
            cancellation: cancellation.clone(),
        })
    }
}

impl MembershipTransaction<'_> {
    /// Enumerate the current private catalog by ascending feed index.
    pub fn feed_cursor(&mut self) -> Result<TransactionFeedCursor<'_>> {
        self.state.feed_cursor(self.writer)
    }

    /// Construct the empty membership without allocating an internal ID.
    pub fn empty_membership(&mut self) -> Result<MembershipRef> {
        self.state.empty_membership(self.writer)
    }

    /// Add one feed to a transaction-owned membership.
    pub fn add_feed(&mut self, membership: MembershipRef, feed: FeedRef) -> Result<MembershipRef> {
        self.state.add_feed(self.writer, membership, feed)
    }

    /// Apply one membership operation to an inclusive IPv4 interval.
    pub fn apply_v4(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        membership: MembershipRef,
        operation: MembershipOperation,
    ) -> Result<bool> {
        self.state
            .apply_v4(self.writer, from, to, membership, operation)
    }

    /// Apply one membership operation to an inclusive IPv6 interval.
    pub fn apply_v6(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        membership: MembershipRef,
        operation: MembershipOperation,
    ) -> Result<bool> {
        self.state
            .apply_v6(self.writer, from, to, membership, operation)
    }

    /// Return an exact existing feed without creating it.
    pub fn lookup_feed(&mut self, name: FeedName) -> Result<Option<FeedRef>> {
        self.state.lookup_feed(self.writer, name)
    }

    /// Return the exact feed, creating it at the lowest free index if absent.
    pub fn ensure_feed(&mut self, name: FeedName) -> Result<FeedRef> {
        self.state.ensure_feed(self.writer, name)
    }

    /// Rename one referenced feed while preserving its membership.
    pub fn rename_feed(&mut self, feed: FeedRef, new_name: FeedName) -> Result<FeedRef> {
        self.state.rename_feed(self.writer, feed, new_name)
    }

    /// Delete one feed and clear its bit from every stored membership.
    pub fn delete_feed(&mut self, feed: FeedRef) -> Result<()> {
        self.state.delete_feed(self.writer, feed)
    }

    /// Stage one exact opaque metadata replacement in this transaction.
    pub fn set_metadata_json(&mut self, input: &[u8]) -> Result<bool> {
        self.state.set_metadata_json(self.writer, input)
    }

    /// Stage metadata absence in this transaction.
    pub fn clear_metadata_json(&mut self) -> Result<bool> {
        self.state.clear_metadata_json(self.writer)
    }

    /// Publish this transaction through the alternate metadata page.
    pub fn commit(self) -> Result<CommitResult> {
        self.writer.commit_operation(&self.state.cancellation)
    }

    /// Discard this transaction and invalidate all of its references.
    pub fn abort(self) -> Result<super::AbortResult> {
        self.writer.abort()
    }
}

impl MembershipState {
    pub(crate) fn feed_cursor<'a>(
        &mut self,
        writer: &'a mut LiveWriter,
    ) -> Result<TransactionFeedCursor<'a>> {
        self.require_active(writer)?;
        self.check_or_abort(writer)?;
        Ok(TransactionFeedCursor {
            cursor: writer.core.current_feed_cursor(writer.owner_identity)?,
            database_id: self.database_id,
            operation_nonce: self.operation_nonce,
        })
    }

    pub(crate) fn empty_membership(&mut self, writer: &mut LiveWriter) -> Result<MembershipRef> {
        self.require_active(writer)?;
        self.check_or_abort(writer)?;
        Ok(self.membership_reference(MembershipHandle::empty()))
    }

    pub(crate) fn add_feed(
        &mut self,
        writer: &mut LiveWriter,
        membership: MembershipRef,
        feed: FeedRef,
    ) -> Result<MembershipRef> {
        self.require_current_membership(writer, membership)?;
        self.require_current_feed(writer, feed)?;
        self.check_or_abort(writer)?;
        let handle =
            writer.mutate(|store| store.add_feed_to_membership(membership.handle, feed.entry))?;
        self.check_or_abort(writer)?;
        Ok(self.membership_reference(handle))
    }

    pub(crate) fn apply_v4(
        &mut self,
        writer: &mut LiveWriter,
        from: Ipv4Key,
        to: Ipv4Key,
        membership: MembershipRef,
        operation: MembershipOperation,
    ) -> Result<bool> {
        self.require_family(writer, AddressFamily::Ipv4, from <= to)?;
        self.require_current_membership(writer, membership)?;
        self.check_or_abort(writer)?;
        let changed = writer
            .mutate(|store| store.apply_membership_v4(from, to, membership.handle, operation))?;
        self.check_or_abort(writer)?;
        Ok(changed)
    }

    pub(crate) fn apply_v6(
        &mut self,
        writer: &mut LiveWriter,
        from: Ipv6Key,
        to: Ipv6Key,
        membership: MembershipRef,
        operation: MembershipOperation,
    ) -> Result<bool> {
        self.require_family(writer, AddressFamily::Ipv6, from <= to)?;
        self.require_current_membership(writer, membership)?;
        self.check_or_abort(writer)?;
        let changed = writer
            .mutate(|store| store.apply_membership_v6(from, to, membership.handle, operation))?;
        self.check_or_abort(writer)?;
        Ok(changed)
    }

    pub(crate) fn lookup_feed(
        &mut self,
        writer: &mut LiveWriter,
        name: FeedName,
    ) -> Result<Option<FeedRef>> {
        self.require_active(writer)?;
        self.check_or_abort(writer)?;
        let entry = writer.mutate(|store| store.lookup_feed(&name))?;
        self.check_or_abort(writer)?;
        Ok(entry.map(|entry| self.reference(entry)))
    }

    pub(crate) fn ensure_feed(
        &mut self,
        writer: &mut LiveWriter,
        name: FeedName,
    ) -> Result<FeedRef> {
        self.require_active(writer)?;
        self.check_or_abort(writer)?;
        let (entry, _) = writer.mutate(|store| store.ensure_feed(name))?;
        self.check_or_abort(writer)?;
        Ok(self.reference(entry))
    }

    pub(crate) fn rename_feed(
        &mut self,
        writer: &mut LiveWriter,
        feed: FeedRef,
        new_name: FeedName,
    ) -> Result<FeedRef> {
        self.require_current_feed(writer, feed)?;
        self.check_or_abort(writer)?;
        let entry = writer.mutate(|store| store.rename_current_feed(feed.entry, new_name))?;
        self.check_or_abort(writer)?;
        Ok(self.reference(entry))
    }

    pub(crate) fn delete_feed(&mut self, writer: &mut LiveWriter, feed: FeedRef) -> Result<()> {
        self.require_current_feed(writer, feed)?;
        self.check_or_abort(writer)?;
        let next_epoch = self
            .membership_epoch
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("membership reference epoch"))?;
        writer.mutate(|store| store.delete_current_feed_membership(feed.entry))?;
        self.check_or_abort(writer)?;
        self.membership_epoch = next_epoch;
        Ok(())
    }

    pub(crate) fn set_metadata_json(
        &mut self,
        writer: &mut LiveWriter,
        input: &[u8],
    ) -> Result<bool> {
        self.require_active(writer)?;
        self.check_or_abort(writer)?;
        let changed = writer.stage_metadata_json(input)?;
        self.check_or_abort(writer)?;
        Ok(changed)
    }

    pub(crate) fn clear_metadata_json(&mut self, writer: &mut LiveWriter) -> Result<bool> {
        self.require_active(writer)?;
        self.check_or_abort(writer)?;
        let changed = writer.stage_clear_metadata_json()?;
        self.check_or_abort(writer)?;
        Ok(changed)
    }

    pub(crate) fn cancellation(&self) -> &CancellationToken {
        &self.cancellation
    }

    fn reference(&self, entry: FeedEntry) -> FeedRef {
        FeedRef {
            database_id: self.database_id,
            operation_nonce: self.operation_nonce,
            entry,
        }
    }

    fn membership_reference(&self, handle: MembershipHandle) -> MembershipRef {
        MembershipRef {
            database_id: self.database_id,
            operation_nonce: self.operation_nonce,
            handle,
            catalog_epoch: self.membership_epoch,
        }
    }

    fn require_reference(&self, writer: &LiveWriter, feed: FeedRef) -> Result<()> {
        self.require_active(writer)?;
        if feed.database_id != self.database_id {
            return Err(Error::ForeignReference);
        }
        if feed.operation_nonce != self.operation_nonce {
            return Err(Error::StaleReference);
        }
        Ok(())
    }

    fn require_current_feed(&mut self, writer: &mut LiveWriter, feed: FeedRef) -> Result<()> {
        self.require_reference(writer, feed)?;
        if writer.feed_reference_current(feed.entry)? {
            Ok(())
        } else {
            Err(Error::StaleReference)
        }
    }

    fn require_membership_reference(
        &self,
        writer: &LiveWriter,
        membership: MembershipRef,
    ) -> Result<()> {
        self.require_active(writer)?;
        if membership.database_id != self.database_id {
            return Err(Error::ForeignReference);
        }
        if membership.operation_nonce != self.operation_nonce {
            return Err(Error::StaleReference);
        }
        if membership.catalog_epoch != self.membership_epoch {
            return Err(Error::StaleReference);
        }
        Ok(())
    }

    fn require_current_membership(
        &mut self,
        writer: &mut LiveWriter,
        membership: MembershipRef,
    ) -> Result<()> {
        self.require_membership_reference(writer, membership)?;
        if writer.membership_reference_current(membership.handle)? {
            Ok(())
        } else {
            Err(Error::StaleReference)
        }
    }

    fn require_family(
        &self,
        writer: &LiveWriter,
        family: AddressFamily,
        ordered: bool,
    ) -> Result<()> {
        self.require_active(writer)?;
        if !ordered {
            return Err(Error::InvalidArgument("range start exceeds range end"));
        }
        if writer.core.base_info().address_family != family {
            return Err(Error::WrongAddressFamily(
                "membership mutation does not match the database family",
            ));
        }
        Ok(())
    }

    pub(crate) fn require_active(&self, writer: &LiveWriter) -> Result<()> {
        require_transaction(writer, self.operation_nonce, INACTIVE)
    }

    fn check_or_abort(&mut self, writer: &mut LiveWriter) -> Result<()> {
        check_transaction(writer, self.operation_nonce, &self.cancellation, INACTIVE)
    }
}

impl LiveWriter {
    fn feed_reference_current(&mut self, entry: FeedEntry) -> Result<bool> {
        Ok(self.core.lookup_current_feed(&entry.name)? == Some(entry))
    }

    fn membership_reference_current(&mut self, membership: MembershipHandle) -> Result<bool> {
        self.core.membership_reference_matches(membership)
    }
}

impl Drop for MembershipTransaction<'_> {
    fn drop(&mut self) {
        self.writer.core.abandon_operation();
    }
}

impl TransactionFeedCursor<'_> {
    /// Return the next transaction-bound feed.
    pub fn next_feed(&mut self) -> Result<Option<FeedRef>> {
        Ok(self.cursor.next_feed()?.map(|entry| FeedRef {
            database_id: self.database_id,
            operation_nonce: self.operation_nonce,
            entry,
        }))
    }
}
