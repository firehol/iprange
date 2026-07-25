//! Transaction-bound membership catalog references.

use std::fmt;

use crate::contract::{AddressFamily, MembershipOperation, ValueKind};
use crate::draft_store::{Draft, DraftStore};
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::feed_catalog::FeedCursor;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::random;

use super::{CommitResult, LiveWriter};

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
    id: u32,
    word_count: u32,
    catalog_epoch: u64,
}

impl fmt::Debug for MembershipRef {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output
            .debug_struct("MembershipRef")
            .field("word_count", &self.word_count)
            .finish_non_exhaustive()
    }
}

impl FeedRef {
    /// Return the feed's current structural name.
    pub const fn name(self) -> FeedName {
        self.entry.name
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
    database_id: [u8; 16],
    operation_nonce: [u8; 16],
    membership_epoch: u64,
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
    pub fn begin_membership_transaction(&mut self) -> Result<MembershipTransaction<'_>> {
        self.require_healthy()?;
        if self.base.meta.value_kind != ValueKind::Membership {
            return Err(Error::WrongMode(
                "membership transaction requires a membership database",
            ));
        }
        if self.draft.is_some() {
            return Err(Error::WrongState("a writer transaction is already pending"));
        }
        let operation_nonce = random::nonzero_128()?;
        self.draft = Some(Draft::new(self.base.meta, operation_nonce)?);
        Ok(MembershipTransaction {
            database_id: self.base.meta.database_id,
            operation_nonce,
            membership_epoch: 0,
            writer: self,
        })
    }
}

impl MembershipTransaction<'_> {
    /// Enumerate the current private catalog by ascending feed index.
    pub fn feed_cursor(&self) -> Result<TransactionFeedCursor<'_>> {
        self.require_active()?;
        let meta = self.writer.draft.as_ref().unwrap().meta;
        Ok(TransactionFeedCursor {
            cursor: FeedCursor::new_live(&self.writer.file, &meta, self.writer.owner_pid)?,
            database_id: self.database_id,
            operation_nonce: self.operation_nonce,
        })
    }

    /// Construct the empty membership without allocating an internal ID.
    pub fn empty_membership(&self) -> Result<MembershipRef> {
        self.require_active()?;
        Ok(self.membership_reference(0, 0))
    }

    /// Add one feed to a transaction-owned membership.
    pub fn add_feed(&mut self, membership: MembershipRef, feed: FeedRef) -> Result<MembershipRef> {
        self.require_current_membership(membership)?;
        self.require_current_feed(feed)?;
        let interned = self.writer.mutate(|store| {
            store.add_feed_to_membership(membership.id, membership.word_count, feed.entry)
        })?;
        Ok(self.membership_reference(interned.id, interned.word_count))
    }

    /// Apply one membership operation to an inclusive IPv4 interval.
    pub fn apply_v4(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        membership: MembershipRef,
        operation: MembershipOperation,
    ) -> Result<bool> {
        self.require_family(AddressFamily::Ipv4, from <= to)?;
        self.require_current_membership(membership)?;
        self.writer.mutate(|store| {
            store.apply_membership_v4(from, to, membership.id, membership.word_count, operation)
        })
    }

    /// Apply one membership operation to an inclusive IPv6 interval.
    pub fn apply_v6(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        membership: MembershipRef,
        operation: MembershipOperation,
    ) -> Result<bool> {
        self.require_family(AddressFamily::Ipv6, from <= to)?;
        self.require_current_membership(membership)?;
        self.writer.mutate(|store| {
            store.apply_membership_v6(from, to, membership.id, membership.word_count, operation)
        })
    }

    /// Return an exact existing feed without creating it.
    pub fn lookup_feed(&mut self, name: FeedName) -> Result<Option<FeedRef>> {
        self.require_active()?;
        let entry = self.writer.mutate(|store| store.lookup_feed(&name))?;
        Ok(entry.map(|entry| self.reference(entry)))
    }

    /// Return the exact feed, creating it at the lowest free index if absent.
    pub fn ensure_feed(&mut self, name: FeedName) -> Result<FeedRef> {
        self.require_active()?;
        let (entry, _) = self.writer.mutate(|store| store.ensure_feed(name))?;
        Ok(self.reference(entry))
    }

    /// Rename one referenced feed while preserving its membership.
    pub fn rename_feed(&mut self, feed: FeedRef, new_name: FeedName) -> Result<FeedRef> {
        self.require_current_feed(feed)?;
        let entry = self
            .writer
            .mutate(|store| store.rename_feed_ref(feed.entry, new_name))?;
        Ok(self.reference(entry))
    }

    /// Delete one feed and clear its bit from every stored membership.
    pub fn delete_feed(&mut self, feed: FeedRef) -> Result<()> {
        self.require_current_feed(feed)?;
        let next_epoch = self
            .membership_epoch
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("membership reference epoch"))?;
        self.writer
            .mutate(|store| store.delete_feed_membership(feed.entry))?;
        self.membership_epoch = next_epoch;
        Ok(())
    }

    /// Stage one exact opaque metadata replacement in this transaction.
    pub fn set_metadata_json(&mut self, input: &[u8]) -> Result<bool> {
        self.require_active()?;
        self.writer.set_metadata_json(input)
    }

    /// Publish this transaction through the alternate metadata page.
    pub fn commit(self) -> Result<CommitResult> {
        self.writer.commit()
    }

    /// Discard this transaction and invalidate all of its references.
    pub fn abort(self) -> Result<()> {
        self.writer.abort()?;
        Ok(())
    }

    fn reference(&self, entry: FeedEntry) -> FeedRef {
        FeedRef {
            database_id: self.database_id,
            operation_nonce: self.operation_nonce,
            entry,
        }
    }

    fn membership_reference(&self, id: u32, word_count: u32) -> MembershipRef {
        MembershipRef {
            database_id: self.database_id,
            operation_nonce: self.operation_nonce,
            id,
            word_count,
            catalog_epoch: self.membership_epoch,
        }
    }

    fn require_reference(&self, feed: FeedRef) -> Result<()> {
        self.require_active()?;
        if feed.database_id != self.database_id {
            return Err(Error::ForeignReference);
        }
        if feed.operation_nonce != self.operation_nonce {
            return Err(Error::StaleReference);
        }
        Ok(())
    }

    fn require_current_feed(&mut self, feed: FeedRef) -> Result<()> {
        self.require_reference(feed)?;
        if self.writer.feed_reference_current(feed.entry)? {
            Ok(())
        } else {
            Err(Error::StaleReference)
        }
    }

    fn require_membership_reference(&self, membership: MembershipRef) -> Result<()> {
        self.require_active()?;
        if membership.database_id != self.database_id {
            return Err(Error::ForeignReference);
        }
        if membership.operation_nonce != self.operation_nonce {
            return Err(Error::StaleReference);
        }
        if membership.catalog_epoch != self.membership_epoch {
            return Err(Error::StaleReference);
        }
        if (membership.id == 0) != (membership.word_count == 0) {
            return Err(Error::StaleReference);
        }
        Ok(())
    }

    fn require_current_membership(&mut self, membership: MembershipRef) -> Result<()> {
        self.require_membership_reference(membership)?;
        if self
            .writer
            .membership_reference_current(membership.id, membership.word_count)?
        {
            Ok(())
        } else {
            Err(Error::StaleReference)
        }
    }

    fn require_family(&self, family: AddressFamily, ordered: bool) -> Result<()> {
        self.require_active()?;
        if !ordered {
            return Err(Error::InvalidArgument("range start exceeds range end"));
        }
        if self.writer.base.meta.address_family != family {
            return Err(Error::WrongMode(
                "membership mutation does not match the database family",
            ));
        }
        Ok(())
    }

    fn require_active(&self) -> Result<()> {
        let active = self
            .writer
            .draft
            .as_ref()
            .is_some_and(|draft| draft.meta.commit_nonce == self.operation_nonce);
        if !active {
            return Err(Error::WrongState(
                "membership transaction is no longer active",
            ));
        }
        self.writer.require_healthy()
    }
}

impl LiveWriter {
    fn feed_reference_current(&self, entry: FeedEntry) -> Result<bool> {
        let meta = self
            .draft
            .as_ref()
            .ok_or(Error::WrongState("membership transaction is not active"))?
            .meta;
        Ok(crate::feed_catalog::lookup(&self.file, &meta, &entry.name)? == Some(entry))
    }

    fn membership_reference_current(&mut self, id: u32, word_count: u32) -> Result<bool> {
        let draft = self
            .draft
            .as_mut()
            .ok_or(Error::WrongState("membership transaction is not active"))?;
        let store = DraftStore::new(
            &self.file,
            self.base.meta.page_count,
            self.budget.pages(),
            draft,
        );
        store.membership_reference_matches(id, word_count)
    }
}

impl Drop for MembershipTransaction<'_> {
    fn drop(&mut self) {
        if let Some(draft) = self.writer.draft.as_mut() {
            draft.abandon_operation();
        }
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
