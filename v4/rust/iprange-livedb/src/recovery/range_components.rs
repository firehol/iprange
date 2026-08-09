//! One ordered overlap-component pass for recovered ranges.

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::range_tree::Record;

use super::direct_output::DirectKey;
use super::range_build::RangeOutput;

pub(super) trait Policy<K: DirectKey> {
    type Resolved: Copy;

    fn resolve(&mut self, record: Record<K>) -> Result<Option<Self::Resolved>>;
    fn accept(&mut self, record: Record<K>, resolved: Option<Self::Resolved>) -> Result<()>;
    fn reject_overlap(&mut self, count: u64, from: K, to: K) -> Result<()>;
    fn finish(&mut self) -> Result<()>;
}

pub(super) struct Components<'a, K: DirectKey, P: Policy<K>> {
    cancellation: &'a CancellationToken,
    policy: P,
    component: Option<Component<K, P::Resolved>>,
}

#[derive(Clone, Copy)]
struct Component<K, R> {
    first: Record<K>,
    resolved: Option<R>,
    maximum_to: K,
    count: u64,
}

impl<'a, K: DirectKey, P: Policy<K>> Components<'a, K, P> {
    pub(super) fn new(cancellation: &'a CancellationToken, policy: P) -> Self {
        Self {
            cancellation,
            policy,
            component: None,
        }
    }

    fn push(&mut self, record: Record<K>) -> Result<()> {
        self.cancellation.check()?;
        let resolved = self.policy.resolve(record)?;
        let Some(mut component) = self.component.take() else {
            self.component = Some(Component::new(record, resolved));
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
        self.component = Some(Component::new(record, resolved));
        Ok(())
    }

    fn finish(&mut self) -> Result<()> {
        if let Some(component) = self.component.take() {
            self.finish_component(component)?;
        }
        self.policy.finish()
    }

    fn finish_component(&mut self, component: Component<K, P::Resolved>) -> Result<()> {
        if component.count != 1 {
            self.policy
                .reject_overlap(component.count, component.first.from, component.maximum_to)
        } else {
            self.policy.accept(component.first, component.resolved)
        }
    }
}

impl<K: DirectKey, P: Policy<K>> RangeOutput<K> for Components<'_, K, P> {
    fn push(&mut self, record: Record<K>) -> Result<()> {
        Components::push(self, record)
    }

    fn finish(&mut self) -> Result<()> {
        Components::finish(self)
    }
}

impl<K: Copy, R: Copy> Component<K, R> {
    fn new(first: Record<K>, resolved: Option<R>) -> Self {
        Self {
            first,
            resolved,
            maximum_to: first.to,
            count: 1,
        }
    }
}
