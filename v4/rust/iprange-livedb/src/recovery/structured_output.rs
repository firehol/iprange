//! Canonical output for ranges backed by recovered typed structures.

use crate::contract::{MetaV4, StructureKind};
use crate::error::Result;
use crate::immutable_output::{Builder, MembershipWords};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;
use crate::range_tree::Record;
use crate::structured_value::{NetworkEnrichmentV1, NetworkEnrichmentV1Codec};
use crate::validation::{ValidationObject, ValidationReason};

use super::direct_output::{report_overlap, DirectKey};
use super::membership_index::{Locator as MembershipLocator, MembershipIndex};
use super::range_components::Policy;
use super::report::{RecoverySink, Reporter, Unknown};
use super::structure_table::{Locator as StructureLocator, StructureIndex};
use super::tables::Tables;

#[derive(Clone, Copy)]
pub(super) struct Resolved {
    structure: StructureLocator,
    membership: Option<MembershipLocator>,
}

pub(super) struct NetworkEnrichmentV1Output<'a, 'b, S, K> {
    mapping: &'a Mapping,
    meta: MetaV4,
    memberships: &'a MembershipIndex,
    structures: &'a StructureIndex,
    tables: &'a Tables,
    builder: &'a mut Builder,
    reporter: &'a mut Reporter<'b, S>,
    marker: std::marker::PhantomData<K>,
}

impl<'a, 'b, S: RecoverySink, K: StructuredKey> NetworkEnrichmentV1Output<'a, 'b, S, K> {
    pub(super) fn new(
        mapping: &'a Mapping,
        meta: MetaV4,
        memberships: &'a MembershipIndex,
        structures: &'a StructureIndex,
        tables: &'a Tables,
        builder: &'a mut Builder,
        reporter: &'a mut Reporter<'b, S>,
    ) -> Self {
        debug_assert_eq!(structures.kind(), StructureKind::NetworkEnrichmentV1);
        Self {
            mapping,
            meta,
            memberships,
            structures,
            tables,
            builder,
            reporter,
            marker: std::marker::PhantomData,
        }
    }

    fn resolve_record(&mut self, record: Record<K>) -> Result<Option<Resolved>> {
        let Some(structure) = self.structures.get(self.tables, record.value)? else {
            self.reporter.unknown(Unknown {
                reason: ValidationReason::StructureMissing,
                object: ValidationObject::StructureDictionary,
                page_number: None,
                physical_bytes: None,
                address_fence: Some(<K as DirectKey>::fence(record.from, record.to)),
                contributes_to_possible_span: false,
                has_unbounded_extent: false,
            })?;
            return Ok(None);
        };
        let membership = if structure.membership_id == 0 {
            None
        } else {
            let Some(membership) = self.memberships.get(self.tables, structure.membership_id)?
            else {
                self.reporter.unknown(Unknown {
                    reason: ValidationReason::StructureMembershipInvalid,
                    object: ValidationObject::StructureDictionary,
                    page_number: Some(structure.leaf_page),
                    physical_bytes: None,
                    address_fence: Some(<K as DirectKey>::fence(record.from, record.to)),
                    contributes_to_possible_span: false,
                    has_unbounded_extent: false,
                })?;
                return Ok(None);
            };
            Some(membership)
        };
        Ok(Some(Resolved {
            structure,
            membership,
        }))
    }

    fn push(&mut self, record: Record<K>, resolved: Resolved) -> Result<()> {
        let value = NetworkEnrichmentV1Codec::decode(&resolved.structure.payload)?;
        match resolved.membership {
            Some(membership) => {
                let words = membership.words(self.mapping, self.meta);
                <K as StructuredKey>::push(
                    self.builder,
                    record.from,
                    record.to,
                    value,
                    Some(&words),
                )
            }
            None => <K as StructuredKey>::push::<super::membership_words::RecoveredWords<'_>>(
                self.builder,
                record.from,
                record.to,
                value,
                None,
            ),
        }
    }
}

impl<S: RecoverySink, K: StructuredKey> Policy<K> for NetworkEnrichmentV1Output<'_, '_, S, K> {
    type Resolved = Resolved;

    fn resolve(&mut self, record: Record<K>) -> Result<Option<Self::Resolved>> {
        self.resolve_record(record)
    }

    fn accept(&mut self, record: Record<K>, resolved: Option<Self::Resolved>) -> Result<()> {
        let Some(resolved) = resolved else {
            return self.reporter.ranges_rejected(1, record.from, record.to);
        };
        self.push(record, resolved)?;
        self.reporter.range_accepted(record.from, record.to)
    }

    fn reject_overlap(&mut self, count: u64, from: K, to: K) -> Result<()> {
        report_overlap(self.reporter, count, from, to)
    }

    fn finish(&mut self) -> Result<()> {
        Ok(())
    }
}

pub(crate) trait StructuredKey: DirectKey {
    fn push<W: MembershipWords>(
        builder: &mut Builder,
        from: Self,
        to: Self,
        value: NetworkEnrichmentV1,
        membership: Option<&W>,
    ) -> Result<()>;
}

impl StructuredKey for Ipv4Key {
    fn push<W: MembershipWords>(
        builder: &mut Builder,
        from: Self,
        to: Self,
        value: NetworkEnrichmentV1,
        membership: Option<&W>,
    ) -> Result<()> {
        builder.push_network_enrichment_v1_v4(from, to, value, membership)
    }
}

impl StructuredKey for Ipv6Key {
    fn push<W: MembershipWords>(
        builder: &mut Builder,
        from: Self,
        to: Self,
        value: NetworkEnrichmentV1,
        membership: Option<&W>,
    ) -> Result<()> {
        builder.push_network_enrichment_v1_v6(from, to, value, membership)
    }
}
