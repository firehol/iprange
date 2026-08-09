//! Narrow logical edit surface over one file-backed COW draft.

use crate::bootstrap::Bootstrap;
use crate::cancellation::CancellationToken;
use crate::contract::MembershipOperation;
use crate::draft_store::{DraftStore, FeedMerge, RetentionMerge};
use crate::error::Result;
use crate::feed::{FeedEntry, FeedName};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::membership_dictionary::Interned;

pub(crate) struct WriterEdit<'a> {
    pub(super) store: DraftStore<'a>,
    base: Bootstrap,
}

impl<'a> WriterEdit<'a> {
    pub(super) fn new(store: DraftStore<'a>, base: Bootstrap) -> Self {
        Self { store, base }
    }

    #[inline]
    pub(crate) fn assign_v4(&mut self, from: Ipv4Key, to: Ipv4Key, value: u32) -> Result<bool> {
        self.store.assign_v4(from, to, value)
    }

    #[inline]
    pub(crate) fn assign_v6(&mut self, from: Ipv6Key, to: Ipv6Key, value: u32) -> Result<bool> {
        self.store.assign_v6(from, to, value)
    }

    #[inline]
    pub(crate) fn clear_v4(&mut self, from: Ipv4Key, to: Ipv4Key) -> Result<bool> {
        self.store.clear_v4(from, to)
    }

    #[inline]
    pub(crate) fn clear_v6(&mut self, from: Ipv6Key, to: Ipv6Key) -> Result<bool> {
        self.store.clear_v6(from, to)
    }

    pub(crate) fn set_metadata(&mut self, input: &[u8]) -> Result<bool> {
        self.store.set_metadata(input)
    }

    pub(crate) fn clear_metadata(&mut self) -> Result<bool> {
        self.store.clear_metadata()
    }

    pub(crate) fn lookup_feed(&self, name: &FeedName) -> Result<Option<FeedEntry>> {
        self.store.lookup_feed(name)
    }

    pub(crate) fn ensure_feed(&mut self, name: FeedName) -> Result<(FeedEntry, bool)> {
        self.store.ensure_feed(name)
    }

    pub(crate) fn insert_feed(&mut self, name: FeedName) -> Result<FeedEntry> {
        self.store.insert_feed(name)
    }

    pub(crate) fn rename_current_feed(
        &mut self,
        feed: FeedEntry,
        name: FeedName,
    ) -> Result<FeedEntry> {
        self.store.rename_current_feed(feed, name)
    }

    pub(crate) fn rename_current_feed_known_available(
        &mut self,
        feed: FeedEntry,
        name: FeedName,
    ) -> Result<FeedEntry> {
        self.store.rename_current_feed_known_available(feed, name)
    }

    pub(crate) fn add_feed_index_to_membership(
        &mut self,
        id: u32,
        words: u32,
        feed_index: u32,
    ) -> Result<Interned> {
        self.store
            .add_feed_index_to_membership(id, words, feed_index)
    }

    pub(crate) fn apply_membership_v4(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        id: u32,
        words: u32,
        operation: MembershipOperation,
    ) -> Result<bool> {
        self.store
            .apply_membership_v4(from, to, id, words, operation)
    }

    pub(crate) fn apply_membership_v6(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        id: u32,
        words: u32,
        operation: MembershipOperation,
    ) -> Result<bool> {
        self.store
            .apply_membership_v6(from, to, id, words, operation)
    }

    pub(crate) fn apply_membership_cancellable<K: IpKey, F>(
        &mut self,
        from: K,
        to: K,
        id: u32,
        words: u32,
        operation: MembershipOperation,
        checkpoint: &mut F,
    ) -> Result<bool>
    where
        F: FnMut() -> Result<()>,
    {
        self.store
            .apply_membership_cancellable(from, to, id, words, operation, checkpoint)
    }

    pub(crate) fn add_feed_coverage<K: IpKey>(&mut self, from: K, to: K) -> Result<()> {
        self.store.add_feed_coverage(from, to)
    }

    pub(crate) fn merge_feed(
        &mut self,
        member: Interned,
        create: bool,
        cancellation: &CancellationToken,
    ) -> Result<FeedMerge> {
        self.store
            .merge_feed(&self.base, member, create, cancellation)
    }

    pub(crate) fn delete_current_feed_membership(&mut self, feed: FeedEntry) -> Result<()> {
        self.store.delete_current_feed_membership(feed)
    }

    pub(crate) fn delete_current_feed_membership_cancellable<F>(
        &mut self,
        feed: FeedEntry,
        checkpoint: &mut F,
    ) -> Result<()>
    where
        F: FnMut() -> Result<()>,
    {
        self.store
            .delete_current_feed_membership_cancellable(feed, checkpoint)
    }

    pub(crate) fn finalize_membership_workflow(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.store.finalize_membership_workflow(cancellation)
    }

    pub(crate) fn finish_membership_workflow(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.store.finish_membership_workflow(cancellation)
    }

    pub(crate) fn finish_direct_workflow(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        self.store.finish_direct_workflow(&self.base, cancellation)
    }

    pub(crate) fn merge_retention(
        &mut self,
        refresh_value: u32,
        cancellation: &CancellationToken,
    ) -> Result<RetentionMerge> {
        self.store
            .merge_retention(&self.base, refresh_value, cancellation)
    }
}
