//! Narrow logical edit surface over one file-backed COW draft.

use crate::bootstrap::Bootstrap;
use crate::cancellation::CancellationToken;
use crate::contract::MembershipOperation;
use crate::draft_store::{
    DraftStore, FeedMerge, HistoryMerge, HistoryPlan, ImportCache, ImportMerge, ImportWords,
    MembershipHandle, TimestampMerge, TranslatedMembership,
};
use crate::error::Result;
use crate::feed::{FeedEntry, FeedName};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::reader_core::MembershipToken;
use crate::workflow::FirstSeenRemovalSink;
use crate::Cardinality129;

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
    pub(crate) fn assign_input_v4(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        value: u32,
        input: &mut crate::range_mutation::AssignmentInput<Ipv4Key>,
    ) -> Result<bool> {
        self.store.assign_input_v4(from, to, value, input)
    }

    #[inline]
    pub(crate) fn assign_input_v6(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        value: u32,
        input: &mut crate::range_mutation::AssignmentInput<Ipv6Key>,
    ) -> Result<bool> {
        self.store.assign_input_v6(from, to, value, input)
    }

    #[inline]
    pub(crate) fn clear_v4(&mut self, from: Ipv4Key, to: Ipv4Key) -> Result<bool> {
        self.store.clear_v4(from, to)
    }

    #[inline]
    pub(crate) fn clear_v6(&mut self, from: Ipv6Key, to: Ipv6Key) -> Result<bool> {
        self.store.clear_v6(from, to)
    }

    pub(crate) fn add_private_constant_range<K: IpKey>(
        &mut self,
        from: K,
        to: K,
        value: u32,
        state: &mut crate::range_mutation::UnionInput<K>,
    ) -> Result<()> {
        self.store
            .add_private_constant_range(from, to, value, state)
    }

    pub(crate) fn finish_private_constant_ranges<K: IpKey>(
        &mut self,
        state: &mut crate::range_mutation::UnionInput<K>,
    ) -> Result<()> {
        self.store.finish_private_constant_ranges(state)
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

    pub(crate) fn add_feed_to_membership(
        &mut self,
        membership: MembershipHandle,
        feed: FeedEntry,
    ) -> Result<MembershipHandle> {
        self.store.add_feed_to_membership(membership, feed)
    }

    pub(crate) fn apply_membership_v4(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        membership: MembershipHandle,
        operation: MembershipOperation,
    ) -> Result<bool> {
        self.store
            .apply_membership_v4(from, to, membership, operation)
    }

    pub(crate) fn apply_membership_v6(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        membership: MembershipHandle,
        operation: MembershipOperation,
    ) -> Result<bool> {
        self.store
            .apply_membership_v6(from, to, membership, operation)
    }

    pub(crate) fn begin_empty_map_feed(&mut self) -> Result<()> {
        self.store.begin_empty_map_feed()
    }

    pub(crate) fn add_empty_map_feed_range<K: IpKey>(
        &mut self,
        from: K,
        to: K,
        member: MembershipHandle,
        state: &mut crate::range_mutation::UnionInput<K>,
    ) -> Result<()> {
        self.store.add_empty_map_feed_range(from, to, member, state)
    }

    pub(crate) fn finish_empty_map_feed_ranges<K: IpKey>(
        &mut self,
        member: MembershipHandle,
        state: &mut crate::range_mutation::UnionInput<K>,
    ) -> Result<Option<Cardinality129>> {
        self.store.finish_empty_map_feed_ranges(member, state)
    }

    pub(crate) fn add_feed_coverage<K: IpKey>(
        &mut self,
        from: K,
        to: K,
        state: &mut crate::range_mutation::UnionInput<K>,
    ) -> Result<()> {
        self.store.add_feed_coverage(from, to, state)
    }

    pub(crate) fn finish_feed_coverage<K: IpKey>(
        &mut self,
        state: &mut crate::range_mutation::UnionInput<K>,
    ) -> Result<()> {
        self.store.finish_feed_coverage(state)
    }

    pub(crate) fn merge_feed(
        &mut self,
        member: MembershipHandle,
        create: bool,
        cancellation: &CancellationToken,
    ) -> Result<FeedMerge> {
        self.store
            .merge_feed(&self.base, member, create, cancellation)
    }

    pub(crate) fn begin_import_merge<K: IpKey>(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<ImportMerge<K>> {
        ImportMerge::new(&mut self.store, &self.base, cancellation)
    }

    pub(crate) fn map_import_feed(
        &mut self,
        cache: &mut ImportCache,
        source: FeedEntry,
        destination: FeedEntry,
    ) -> Result<()> {
        cache.map_feed(&mut self.store, source, destination)
    }

    pub(crate) fn cached_import_membership(
        &mut self,
        cache: &mut ImportCache,
        source: MembershipToken,
    ) -> Result<Option<TranslatedMembership>> {
        cache.membership(&self.store, source)
    }

    pub(crate) fn map_import_word_batch(
        &mut self,
        cache: &ImportCache,
        words: &mut ImportWords,
        start: u32,
        source_words: &[u64],
        cancellation: &CancellationToken,
    ) -> Result<bool> {
        cache.map_word_batch(&mut self.store, words, start, source_words, cancellation)
    }

    pub(crate) fn finish_import_membership(
        &mut self,
        cache: &mut ImportCache,
        source: MembershipToken,
        words: &mut ImportWords,
        cancellation: &CancellationToken,
    ) -> Result<TranslatedMembership> {
        cache.finish_membership(&mut self.store, source, words, cancellation)
    }

    pub(crate) fn release_import_cache(
        &mut self,
        cache: &mut ImportCache,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        cache.release(&mut self.store, cancellation)
    }

    pub(crate) fn push_import_range<K: IpKey>(
        &mut self,
        merge: &mut ImportMerge<K>,
        from: K,
        to: K,
        membership: TranslatedMembership,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        merge.push(&mut self.store, from, to, membership, cancellation)
    }

    pub(crate) fn finish_import_merge<K: IpKey>(
        &mut self,
        merge: ImportMerge<K>,
        cancellation: &CancellationToken,
    ) -> Result<crate::workflow::Comparison> {
        merge.finish(&mut self.store, cancellation)
    }

    pub(crate) fn prepare_history_from<K: IpKey, I>(
        &mut self,
        window_count: usize,
        windows: I,
        cancellation: &CancellationToken,
    ) -> Result<HistoryPlan<K>>
    where
        I: IntoIterator<Item = Result<crate::history::HistoryWindow>>,
    {
        HistoryPlan::prepare_from(&mut self.store, window_count, windows, cancellation)
    }

    pub(crate) fn begin_history<K: IpKey>(
        &mut self,
        plan: HistoryPlan<K>,
        cancellation: &CancellationToken,
    ) -> Result<HistoryMerge<K>> {
        plan.begin(&mut self.store, &self.base, cancellation)
    }

    pub(crate) fn push_history<K: IpKey>(
        &mut self,
        merge: &mut HistoryMerge<K>,
        from: K,
        to: K,
        last_seen: u32,
        cancellation: &CancellationToken,
    ) -> Result<()> {
        merge.push(&mut self.store, from, to, last_seen, cancellation)
    }

    pub(crate) fn finish_history<K: IpKey>(
        &mut self,
        merge: HistoryMerge<K>,
        source_range_count: u64,
        source_addresses: crate::cardinality::Cardinality129,
        cancellation: &CancellationToken,
    ) -> Result<crate::history::HistoryProjectionReport> {
        merge.finish(
            &mut self.store,
            cancellation,
            source_range_count,
            source_addresses,
        )
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

    pub(crate) fn merge_first_seen(
        &mut self,
        refresh_value: u32,
        cancellation: &CancellationToken,
    ) -> Result<TimestampMerge> {
        self.store
            .merge_first_seen(&self.base, refresh_value, cancellation)
    }

    pub(crate) fn merge_first_seen_v4_with_removals<S>(
        &mut self,
        refresh_value: u32,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<TimestampMerge>
    where
        S: FirstSeenRemovalSink<Ipv4Key>,
    {
        self.store.merge_first_seen_with_removals::<Ipv4Key, _>(
            &self.base,
            refresh_value,
            sink,
            cancellation,
        )
    }

    pub(crate) fn merge_first_seen_v6_with_removals<S>(
        &mut self,
        refresh_value: u32,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<TimestampMerge>
    where
        S: FirstSeenRemovalSink<Ipv6Key>,
    {
        self.store.merge_first_seen_with_removals::<Ipv6Key, _>(
            &self.base,
            refresh_value,
            sink,
            cancellation,
        )
    }

    pub(crate) fn merge_last_seen(
        &mut self,
        refresh_value: u32,
        cutoff: u32,
        cancellation: &CancellationToken,
    ) -> Result<TimestampMerge> {
        self.store
            .merge_last_seen(&self.base, refresh_value, cutoff, cancellation)
    }
}
