//! Whole-component rejection and canonical membership-range output.

use crate::contract::MetaV4;
use crate::error::Result;
use crate::immutable_output::Builder;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;
use crate::range_tree::Record;
use crate::validation::{ValidationObject, ValidationReason};

use super::direct_output::{report_overlap, DirectKey};
use super::membership_index::{Locator, MembershipIndex};
use super::range_components::Policy;
use super::report::{RecoverySink, Reporter, Unknown};
use super::tables::Tables;

pub(super) struct MembershipOutput<'a, 'b, S, K> {
    mapping: &'a Mapping,
    meta: MetaV4,
    memberships: &'a MembershipIndex,
    tables: &'a Tables,
    builder: &'a mut Builder,
    reporter: &'a mut Reporter<'b, S>,
    output: Option<OutputRange<K>>,
}

#[derive(Clone, Copy)]
struct OutputRange<K> {
    from: K,
    to: K,
    membership: Locator,
}

impl<'a, 'b, S: RecoverySink, K: MembershipKey> MembershipOutput<'a, 'b, S, K> {
    pub(super) fn new(
        mapping: &'a Mapping,
        meta: MetaV4,
        memberships: &'a MembershipIndex,
        tables: &'a Tables,
        builder: &'a mut Builder,
        reporter: &'a mut Reporter<'b, S>,
    ) -> Self {
        Self {
            mapping,
            meta,
            memberships,
            tables,
            builder,
            reporter,
            output: None,
        }
    }

    fn finish_output(&mut self) -> Result<()> {
        if let Some(output) = self.output.take() {
            self.push_output(output)?;
        }
        Ok(())
    }

    fn resolve(&mut self, record: Record<K>) -> Result<Option<Locator>> {
        let membership = self.memberships.get(self.tables, record.value)?;
        if membership.is_none() {
            self.reporter.unknown(Unknown {
                reason: ValidationReason::MembershipMissing,
                object: ValidationObject::MembershipDictionary,
                page_number: None,
                physical_bytes: None,
                address_fence: Some(<K as DirectKey>::fence(record.from, record.to)),
                contributes_to_possible_span: false,
                has_unbounded_extent: false,
            })?;
        }
        Ok(membership)
    }

    fn coalesce(&mut self, current: OutputRange<K>) -> Result<()> {
        let Some(mut previous) = self.output.take() else {
            self.output = Some(current);
            return Ok(());
        };
        let adjacent = previous.to.checked_next() == Some(current.from);
        if adjacent
            && previous
                .membership
                .equal(current.membership, self.mapping, self.meta)?
        {
            previous.to = current.to;
            self.output = Some(previous);
        } else {
            self.push_output(previous)?;
            self.output = Some(current);
        }
        Ok(())
    }

    fn push_output(&mut self, output: OutputRange<K>) -> Result<()> {
        let words = output.membership.words(self.mapping, self.meta);
        K::push_membership(self.builder, output.from, output.to, &words)
    }
}

impl<S: RecoverySink, K: MembershipKey> Policy<K> for MembershipOutput<'_, '_, S, K> {
    type Resolved = Locator;

    fn resolve(&mut self, record: Record<K>) -> Result<Option<Self::Resolved>> {
        MembershipOutput::resolve(self, record)
    }

    fn accept(&mut self, record: Record<K>, membership: Option<Self::Resolved>) -> Result<()> {
        let Some(membership) = membership else {
            return self.reporter.ranges_rejected(1, record.from, record.to);
        };
        self.reporter.range_accepted(record.from, record.to)?;
        self.coalesce(OutputRange {
            from: record.from,
            to: record.to,
            membership,
        })
    }

    fn reject_overlap(&mut self, count: u64, from: K, to: K) -> Result<()> {
        report_overlap(self.reporter, count, from, to)
    }

    fn finish(&mut self) -> Result<()> {
        self.finish_output()
    }
}

pub(crate) trait MembershipKey: DirectKey {
    fn push_membership(
        builder: &mut Builder,
        from: Self,
        to: Self,
        words: &impl crate::immutable_output::MembershipWords,
    ) -> Result<()>;
}

impl MembershipKey for Ipv4Key {
    fn push_membership(
        builder: &mut Builder,
        from: Self,
        to: Self,
        words: &impl crate::immutable_output::MembershipWords,
    ) -> Result<()> {
        builder.push_membership_v4(from, to, words)
    }
}

impl MembershipKey for Ipv6Key {
    fn push_membership(
        builder: &mut Builder,
        from: Self,
        to: Self,
        words: &impl crate::immutable_output::MembershipWords,
    ) -> Result<()> {
        builder.push_membership_v6(from, to, words)
    }
}
