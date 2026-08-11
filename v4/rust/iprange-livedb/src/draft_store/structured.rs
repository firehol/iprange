//! Draft-owned typed structure interning and refcount finalization.

use crate::contract::{AddressFamily, MembershipOperation, StructureKind, ValueKind};
use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::membership_delta;
use crate::range_mutation;
use crate::structured_value::{self, NetworkEnrichmentV1, NetworkEnrichmentV1Codec, PayloadCodec};

use super::{DraftStore, MembershipHandle};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct StructureHandle {
    id: u32,
}

impl StructureHandle {
    pub(crate) const fn empty() -> Self {
        Self { id: 0 }
    }
}

impl DraftStore<'_> {
    pub(crate) fn intern_network_enrichment_v1(
        &mut self,
        value: NetworkEnrichmentV1,
        membership: MembershipHandle,
    ) -> Result<StructureHandle> {
        self.require_network_enrichment_v1()?;
        let (membership_id, _) = membership.stored();
        let payload = NetworkEnrichmentV1Codec::encode(value, membership_id)?;
        self.intern_payload(payload)
    }

    pub(crate) fn assign_structure_input_v4(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        structure: StructureHandle,
        input: &mut range_mutation::AssignmentInput<Ipv4Key>,
    ) -> Result<bool> {
        if structure.id == 0 {
            self.clear_v4(from, to)
        } else if self.draft.range_tree_private {
            self.assign_input_v4(from, to, structure.id, input)
        } else {
            self.assign_v4(from, to, structure.id)
        }
    }

    pub(crate) fn assign_structure_input_v6(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        structure: StructureHandle,
        input: &mut range_mutation::AssignmentInput<Ipv6Key>,
    ) -> Result<bool> {
        if structure.id == 0 {
            self.clear_v6(from, to)
        } else if self.draft.range_tree_private {
            self.assign_input_v6(from, to, structure.id, input)
        } else {
            self.assign_v6(from, to, structure.id)
        }
    }

    pub(crate) fn delete_current_structured_feed<F>(
        &mut self,
        feed: crate::feed::FeedEntry,
        checkpoint: &mut F,
    ) -> Result<()>
    where
        F: FnMut() -> Result<()>,
    {
        self.require_network_enrichment_v1()?;
        checkpoint()?;
        let member = self.add_feed_to_membership(MembershipHandle::empty(), feed)?;
        let mut root = self.draft.meta.range_root;
        let mut count = self.draft.meta.range_record_count;
        let changed = match self.draft.meta.address_family {
            AddressFamily::Ipv4 => range_mutation::transform(
                self,
                &mut root,
                &mut count,
                Ipv4Key::MIN,
                Ipv4Key::MAX,
                |store, current| store.remove_feed_from_structure(current, member, checkpoint),
            )?,
            AddressFamily::Ipv6 => range_mutation::transform(
                self,
                &mut root,
                &mut count,
                Ipv6Key::MIN,
                Ipv6Key::MAX,
                |store, current| store.remove_feed_from_structure(current, member, checkpoint),
            )?,
        };
        self.draft.meta.range_root = root;
        self.draft.meta.range_record_count = count;
        self.draft.changed |= changed;
        checkpoint()?;
        self.remove_current_feed(feed)
    }

    #[inline(always)]
    pub(super) fn track_structure_refcount(&mut self, id: u32, change: i64) -> Result<()> {
        let mut root = self.draft.structure_delta_root;
        let mut pending = self.draft.structure_delta_pending;
        let result = membership_delta::track_buffered(self, &mut root, &mut pending, id, change);
        self.draft.structure_delta_root = root;
        self.draft.structure_delta_pending = pending;
        result
    }

    pub(super) fn finish_structure_deltas_with_checkpoint<F>(
        &mut self,
        checkpoint: &mut F,
    ) -> Result<()>
    where
        F: FnMut() -> Result<()>,
    {
        if self.draft.meta.value_kind != ValueKind::Structured {
            return require_empty_delta(
                self.draft.structure_delta_root,
                self.draft.structure_delta_pending,
            );
        }
        let mut root = self.draft.structure_delta_root;
        let mut pending = self.draft.structure_delta_pending;
        membership_delta::flush(self, &mut root, &mut pending)?;
        self.draft.structure_delta_root = root;
        self.draft.structure_delta_pending = pending;
        if root == 0 {
            return Ok(());
        }
        let mut state = structured_value::State::from_meta(&self.draft.meta);
        let mut deltas = membership_delta::Drain::new(self, root)?;
        while let Some(delta) = deltas.next(self)? {
            checkpoint()?;
            let released = match self.draft.meta.structure_kind() {
                Some(StructureKind::NetworkEnrichmentV1) => {
                    structured_value::apply_delta::<NetworkEnrichmentV1Codec, _>(
                        self, &mut state, delta,
                    )?
                }
                Some(StructureKind::None) | None => {
                    return Err(Error::Corrupt(
                        "structured file has an invalid structure kind",
                    ));
                }
            };
            if let Some(membership_id) = released {
                self.track_membership_owner_refcount(membership_id, -1)?;
            }
        }
        self.draft.structure_delta_root = 0;
        self.draft.structure_delta_pending = membership_delta::Pending::new();
        state.write_to(&mut self.draft.meta);
        if self.draft.meta.structure_entry_count > self.draft.meta.range_record_count {
            return Err(Error::Corrupt(
                "structure dictionary exceeds the range-record count",
            ));
        }
        require_empty_delta(
            self.draft.structure_delta_root,
            self.draft.structure_delta_pending,
        )
    }

    fn intern_payload(&mut self, payload: structured_value::Payload) -> Result<StructureHandle> {
        let mut state = structured_value::State::from_meta(&self.draft.meta);
        let interned =
            structured_value::intern::<NetworkEnrichmentV1Codec, _>(self, &mut state, payload)?;
        if interned.created {
            self.track_structure_refcount(interned.id, 0)?;
            self.track_membership_owner_refcount(interned.membership_id, 1)?;
        }
        state.write_to(&mut self.draft.meta);
        Ok(StructureHandle { id: interned.id })
    }

    fn remove_feed_from_structure<F>(
        &mut self,
        current: Option<u32>,
        removed: MembershipHandle,
        checkpoint: &mut F,
    ) -> Result<Option<u32>>
    where
        F: FnMut() -> Result<()>,
    {
        let Some(structure_id) = current else {
            return Ok(None);
        };
        checkpoint()?;
        let record = structured_value::find::<NetworkEnrichmentV1Codec, _>(
            self,
            self.draft.meta.structure_id_root,
            self.draft.meta.structure_id_limit,
            structure_id,
        )?
        .ok_or(Error::Corrupt("range names a missing structure"))?;
        let (removed_id, removed_words) = removed.stored();
        let membership_id = NetworkEnrichmentV1Codec::membership_id(&record.payload);
        let replacement = self.combine_memberships(
            membership_id,
            removed_id,
            removed_words,
            MembershipOperation::Difference,
        )?;
        if replacement == Some(membership_id) || (replacement.is_none() && membership_id == 0) {
            return Ok(current);
        }
        let payload =
            NetworkEnrichmentV1Codec::with_membership(&record.payload, replacement.unwrap_or(0))?;
        let structure = self.intern_payload(payload)?;
        Ok((structure.id != 0).then_some(structure.id))
    }

    fn require_network_enrichment_v1(&self) -> Result<()> {
        if self.draft.meta.value_kind != ValueKind::Structured
            || self.draft.meta.structure_kind() != Some(NetworkEnrichmentV1Codec::KIND)
        {
            return Err(Error::WrongStructureKind(
                "operation requires a network_enrichment_v1 database",
            ));
        }
        Ok(())
    }
}

fn require_empty_delta(root: u32, pending: membership_delta::Pending) -> Result<()> {
    if root == 0 && pending.is_empty() {
        Ok(())
    } else {
        Err(Error::Corrupt(
            "transaction contains unexpected structure refcount state",
        ))
    }
}
