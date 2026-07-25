//! Draft-owned membership interning and refcount finalization.

use crate::contract::{MembershipOperation, ValueKind};
use crate::error::{Error, Result};
use crate::feed::FeedEntry;
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::membership_delta;
use crate::membership_dictionary::{self, Interned, State};
use crate::range_mutation;

use super::DraftStore;

impl DraftStore<'_> {
    pub(crate) fn add_feed_to_membership(
        &mut self,
        base_id: u32,
        base_words: u32,
        feed: FeedEntry,
    ) -> Result<Interned> {
        if self.lookup_feed(&feed.name)? != Some(feed) {
            return Err(Error::StaleReference);
        }
        let mut state = self.membership_state();
        let interned = membership_dictionary::intern_added_bit(
            self, &mut state, base_id, base_words, feed.index,
        )?;
        if interned.created {
            let mut root = self.draft.membership_delta_root;
            membership_delta::track(self, &mut root, interned.id, 0)?;
            self.draft.membership_delta_root = root;
        }
        self.store_membership_state(state);
        Ok(interned)
    }

    pub(crate) fn apply_membership_v4(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        membership_id: u32,
        word_count: u32,
        operation: MembershipOperation,
    ) -> Result<bool> {
        self.apply_membership(from, to, membership_id, word_count, operation)
    }

    pub(crate) fn apply_membership_v6(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        membership_id: u32,
        word_count: u32,
        operation: MembershipOperation,
    ) -> Result<bool> {
        self.apply_membership(from, to, membership_id, word_count, operation)
    }

    pub(crate) fn delete_feed_membership(&mut self, feed: FeedEntry) -> Result<()> {
        let member = self.add_feed_to_membership(0, 0, feed)?;
        match self.draft.meta.address_family {
            crate::contract::AddressFamily::Ipv4 => {
                self.apply_membership(
                    Ipv4Key::MIN,
                    Ipv4Key::MAX,
                    member.id,
                    member.word_count,
                    MembershipOperation::Difference,
                )?;
            }
            crate::contract::AddressFamily::Ipv6 => {
                self.apply_membership(
                    Ipv6Key::MIN,
                    Ipv6Key::MAX,
                    member.id,
                    member.word_count,
                    MembershipOperation::Difference,
                )?;
            }
        }
        self.remove_feed(feed)
    }

    pub(crate) fn membership_reference_matches(&self, id: u32, word_count: u32) -> Result<bool> {
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

    pub(super) fn finish_membership_deltas(&mut self) -> Result<()> {
        if self.draft.meta.value_kind != ValueKind::Membership {
            return require_empty_delta(self.draft.membership_delta_root);
        }
        let mut state = self.membership_state();
        let mut root = self.draft.membership_delta_root;
        while let Some(delta) = membership_delta::take_first(self, &mut root)? {
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

    fn apply_membership<K: IpKey>(
        &mut self,
        from: K,
        to: K,
        membership_id: u32,
        word_count: u32,
        operation: MembershipOperation,
    ) -> Result<bool> {
        let mut root = self.draft.meta.range_root;
        let mut count = self.draft.meta.range_record_count;
        let changed =
            range_mutation::transform(self, &mut root, &mut count, from, to, |store, current| {
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

    fn combine_memberships(
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
        if interned.created {
            let mut root = self.draft.membership_delta_root;
            membership_delta::track(self, &mut root, interned.id, 0)?;
            self.draft.membership_delta_root = root;
        }
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
