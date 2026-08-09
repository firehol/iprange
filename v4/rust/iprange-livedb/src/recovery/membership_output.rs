//! Whole-component rejection and canonical membership-range output.

use crate::cancellation::CancellationToken;
use crate::contract::MetaV4;
use crate::error::{Error, Result};
use crate::immutable_output::Builder;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;
use crate::range_tree::Record;
use crate::validation::{ValidationObject, ValidationReason};

use super::direct_output::DirectKey;
use super::membership_index::{Locator, MembershipIndex};
use super::range_build::RangeOutput;
use super::report::{RecoverySink, Reporter, Unknown};
use super::tables::Tables;

pub(crate) struct Components<'a, 'b, S, K> {
    mapping: &'a Mapping,
    meta: MetaV4,
    memberships: &'a MembershipIndex,
    tables: &'a Tables,
    builder: &'a mut Builder,
    reporter: &'a mut Reporter<'b, S>,
    cancellation: &'a CancellationToken,
    component: Option<Component<K>>,
    output: Option<OutputRange<K>>,
}

#[derive(Clone, Copy)]
struct Component<K> {
    first: Record<K>,
    first_membership: Option<Locator>,
    maximum_to: K,
    count: u64,
}

#[derive(Clone, Copy)]
struct OutputRange<K> {
    from: K,
    to: K,
    membership: Locator,
}

impl<'a, 'b, S: RecoverySink, K: MembershipKey> Components<'a, 'b, S, K> {
    pub(crate) fn new(
        mapping: &'a Mapping,
        meta: MetaV4,
        memberships: &'a MembershipIndex,
        tables: &'a Tables,
        builder: &'a mut Builder,
        reporter: &'a mut Reporter<'b, S>,
        cancellation: &'a CancellationToken,
    ) -> Self {
        Self {
            mapping,
            meta,
            memberships,
            tables,
            builder,
            reporter,
            cancellation,
            component: None,
            output: None,
        }
    }

    pub(crate) fn push(&mut self, record: Record<K>) -> Result<()> {
        self.cancellation.check()?;
        let membership = self.resolve(record)?;
        let Some(mut component) = self.component.take() else {
            self.component = Some(Component::new(record, membership));
            return Ok(());
        };
        if record.from < component.first.from {
            return Err(Error::RecoveryCandidateChanged);
        }
        if record.from <= component.maximum_to {
            component.maximum_to = component.maximum_to.max(record.to);
            component.count = component
                .count
                .checked_add(1)
                .ok_or(Error::ArithmeticOverflow("recovery overlap component"))?;
            self.component = Some(component);
            return Ok(());
        }
        self.finish_component(component)?;
        self.component = Some(Component::new(record, membership));
        Ok(())
    }

    fn finish(&mut self) -> Result<()> {
        if let Some(component) = self.component.take() {
            self.finish_component(component)?;
        }
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

    fn finish_component(&mut self, component: Component<K>) -> Result<()> {
        if component.count != 1 {
            self.reporter.ranges_rejected(
                component.count,
                component.first.from,
                component.maximum_to,
            )?;
            return self.reporter.unknown(Unknown {
                reason: ValidationReason::RangeOverlap,
                object: ValidationObject::RangeTree,
                page_number: None,
                physical_bytes: None,
                address_fence: Some(<K as DirectKey>::fence(
                    component.first.from,
                    component.maximum_to,
                )),
                contributes_to_possible_span: false,
                has_unbounded_extent: false,
            });
        }
        let Some(membership) = component.first_membership else {
            return self
                .reporter
                .ranges_rejected(1, component.first.from, component.first.to);
        };
        self.reporter
            .range_accepted(component.first.from, component.first.to)?;
        self.coalesce(OutputRange {
            from: component.first.from,
            to: component.first.to,
            membership,
        })
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

impl<S: RecoverySink, K: MembershipKey> RangeOutput<K> for Components<'_, '_, S, K> {
    fn push(&mut self, record: Record<K>) -> Result<()> {
        Components::push(self, record)
    }

    fn finish(&mut self) -> Result<()> {
        Components::finish(self)
    }
}

impl<K: Copy> Component<K> {
    fn new(first: Record<K>, first_membership: Option<Locator>) -> Self {
        Self {
            first,
            first_membership,
            maximum_to: first.to,
            count: 1,
        }
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
