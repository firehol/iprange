//! Draft-owned membership interning and refcount finalization.

use crate::contract::{MembershipOperation, ValueKind};
use crate::error::{Error, Result};
use crate::feed::FeedEntry;
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::membership_delta;
use crate::membership_dictionary::{self, Interned, State};
use crate::range_mutation;

use super::DraftStore;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct MembershipHandle {
    id: u32,
    word_count: u32,
}

impl MembershipHandle {
    pub(crate) const fn empty() -> Self {
        Self {
            id: 0,
            word_count: 0,
        }
    }

    pub(crate) const fn is_empty(self) -> bool {
        self.id == 0
    }

    pub(super) const fn stored(self) -> (u32, u32) {
        (self.id, self.word_count)
    }
}

impl From<Interned> for MembershipHandle {
    fn from(value: Interned) -> Self {
        Self {
            id: value.id,
            word_count: value.word_count,
        }
    }
}

impl DraftStore<'_> {
    pub(crate) fn add_feed_to_membership(
        &mut self,
        base: MembershipHandle,
        feed: FeedEntry,
    ) -> Result<MembershipHandle> {
        let (base_id, base_words) = base.stored();
        let mut state = self.membership_state();
        let interned = membership_dictionary::intern_added_bit(
            self, &mut state, base_id, base_words, feed.index,
        )?;
        self.track_new_membership(&interned)?;
        self.store_membership_state(state);
        Ok(interned.into())
    }

    pub(crate) fn intern_membership<W>(&mut self, words: &W) -> Result<Interned>
    where
        W: membership_dictionary::Words<Self>,
    {
        let mut state = self.membership_state();
        let interned = membership_dictionary::intern(self, &mut state, words)?;
        self.track_new_membership(&interned)?;
        self.store_membership_state(state);
        Ok(interned)
    }

    pub(crate) fn apply_membership_v4(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        membership: MembershipHandle,
        operation: MembershipOperation,
    ) -> Result<bool> {
        let (membership_id, word_count) = membership.stored();
        self.apply_membership(from, to, membership_id, word_count, operation, &mut || {
            Ok(())
        })
    }

    pub(crate) fn apply_membership_v6(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        membership: MembershipHandle,
        operation: MembershipOperation,
    ) -> Result<bool> {
        let (membership_id, word_count) = membership.stored();
        self.apply_membership(from, to, membership_id, word_count, operation, &mut || {
            Ok(())
        })
    }

    pub(crate) fn delete_current_feed_membership(&mut self, feed: FeedEntry) -> Result<()> {
        self.delete_current_feed_membership_cancellable(feed, &mut || Ok(()))
    }

    pub(crate) fn delete_current_feed_membership_cancellable<F>(
        &mut self,
        feed: FeedEntry,
        checkpoint: &mut F,
    ) -> Result<()>
    where
        F: FnMut() -> Result<()>,
    {
        checkpoint()?;
        let member = self.add_feed_to_membership(MembershipHandle::empty(), feed)?;
        let (member_id, member_words) = member.stored();
        match self.draft.meta.address_family {
            crate::contract::AddressFamily::Ipv4 => {
                self.apply_membership(
                    Ipv4Key::MIN,
                    Ipv4Key::MAX,
                    member_id,
                    member_words,
                    MembershipOperation::Difference,
                    checkpoint,
                )?;
            }
            crate::contract::AddressFamily::Ipv6 => {
                self.apply_membership(
                    Ipv6Key::MIN,
                    Ipv6Key::MAX,
                    member_id,
                    member_words,
                    MembershipOperation::Difference,
                    checkpoint,
                )?;
            }
        }
        checkpoint()?;
        self.remove_current_feed(feed)
    }

    pub(crate) fn membership_reference_matches(
        &self,
        membership: MembershipHandle,
    ) -> Result<bool> {
        if membership.is_empty() {
            return Ok(true);
        }
        let (id, word_count) = membership.stored();
        membership_dictionary::reference_matches(
            self,
            self.draft.meta.membership_id_root,
            id,
            word_count,
        )
    }

    pub(super) fn track_membership_refcount(&mut self, id: u32, change: i64) -> Result<()> {
        if self.draft.meta.value_kind == ValueKind::Membership {
            let mut root = self.draft.membership_delta_root;
            membership_delta::track(self, &mut root, id, change)?;
            self.draft.membership_delta_root = root;
            Ok(())
        } else {
            Ok(())
        }
    }

    pub(super) fn finish_membership_deltas_with_checkpoint<F>(
        &mut self,
        checkpoint: &mut F,
    ) -> Result<()>
    where
        F: FnMut() -> Result<()>,
    {
        if self.draft.meta.value_kind != ValueKind::Membership {
            return require_empty_delta(self.draft.membership_delta_root);
        }
        let mut state = self.membership_state();
        let mut root = self.draft.membership_delta_root;
        while let Some(delta) = membership_delta::take_first(self, &mut root)? {
            checkpoint()?;
            membership_dictionary::apply_delta(self, &mut state, delta)?;
        }
        self.draft.membership_delta_root = root;
        self.store_membership_state(state);
        if self.draft.meta.membership_entry_count > self.draft.meta.range_record_count {
            return Err(Error::Corrupt(
                "membership dictionary exceeds the range-record count",
            ));
        }
        require_empty_delta(self.draft.membership_delta_root)
    }

    fn membership_state(&self) -> State {
        State {
            id_root: self.draft.meta.membership_id_root,
            hash_root: self.draft.meta.membership_hash_root,
            used_root: self.draft.meta.membership_used_root,
            entry_count: self.draft.meta.membership_entry_count,
            id_limit: self.draft.meta.membership_id_limit,
        }
    }

    fn store_membership_state(&mut self, state: State) {
        self.draft.meta.membership_id_root = state.id_root;
        self.draft.meta.membership_hash_root = state.hash_root;
        self.draft.meta.membership_used_root = state.used_root;
        self.draft.meta.membership_entry_count = state.entry_count;
        self.draft.meta.membership_id_limit = state.id_limit;
    }

    fn track_new_membership(&mut self, interned: &Interned) -> Result<()> {
        if !interned.created {
            return Ok(());
        }
        let mut root = self.draft.membership_delta_root;
        membership_delta::track(self, &mut root, interned.id, 0)?;
        self.draft.membership_delta_root = root;
        Ok(())
    }

    fn apply_membership<K: IpKey, F>(
        &mut self,
        from: K,
        to: K,
        membership_id: u32,
        word_count: u32,
        operation: MembershipOperation,
        checkpoint: &mut F,
    ) -> Result<bool>
    where
        F: FnMut() -> Result<()>,
    {
        let mut root = self.draft.meta.range_root;
        let mut count = self.draft.meta.range_record_count;
        let changed =
            range_mutation::transform(self, &mut root, &mut count, from, to, |store, current| {
                checkpoint()?;
                store.combine_memberships(
                    current.unwrap_or(0),
                    membership_id,
                    word_count,
                    operation,
                )
            })?;
        self.draft.meta.range_root = root;
        self.draft.meta.range_record_count = count;
        self.draft.changed |= changed;
        Ok(changed)
    }

    pub(super) fn combine_memberships(
        &mut self,
        current: u32,
        supplied: u32,
        supplied_words: u32,
        operation: MembershipOperation,
    ) -> Result<Option<u32>> {
        let mut state = self.membership_state();
        let interned = membership_dictionary::combine(
            self,
            &mut state,
            current,
            supplied,
            supplied_words,
            operation,
        )?;
        self.track_new_membership(&interned)?;
        self.store_membership_state(state);
        Ok((interned.id != 0).then_some(interned.id))
    }
}

impl range_mutation::RangeStore for DraftStore<'_> {
    fn range_record_added(&mut self, value: u32) -> Result<()> {
        self.track_membership_refcount(value, 1)
    }

    fn range_record_removed(&mut self, value: u32) -> Result<()> {
        self.track_membership_refcount(value, -1)
    }
}

fn require_empty_delta(root: u32) -> Result<()> {
    if root == 0 {
        Ok(())
    } else {
        Err(Error::Corrupt(
            "direct transaction contains membership refcount state",
        ))
    }
}
