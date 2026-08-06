use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::immutable_output::Builder;
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::range_tree::Record;
use crate::validation::{ValidationAddressFence, ValidationObject, ValidationReason};

use super::report::{RecoverySink, Reporter, Unknown};

pub(super) struct Components<'a, 'b, S, K> {
    builder: &'a mut Builder,
    reporter: &'a mut Reporter<'b, S>,
    cancellation: &'a CancellationToken,
    component: Option<Component<K>>,
    output: Option<Record<K>>,
}

#[derive(Clone, Copy)]
struct Component<K> {
    first: Record<K>,
    maximum_to: K,
    count: u64,
}

impl<'a, 'b, S: RecoverySink, K: DirectKey> Components<'a, 'b, S, K> {
    pub(super) fn new(
        builder: &'a mut Builder,
        reporter: &'a mut Reporter<'b, S>,
        cancellation: &'a CancellationToken,
    ) -> Self {
        Self {
            builder,
            reporter,
            cancellation,
            component: None,
            output: None,
        }
    }

    pub(super) fn push(&mut self, record: Record<K>) -> Result<()> {
        self.cancellation.check()?;
        let Some(mut component) = self.component.take() else {
            self.component = Some(Component::new(record));
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
        self.component = Some(Component::new(record));
        Ok(())
    }

    pub(super) fn finish(mut self) -> Result<()> {
        if let Some(component) = self.component.take() {
            self.finish_component(component)?;
        }
        if let Some(record) = self.output.take() {
            K::push(self.builder, record)?;
        }
        Ok(())
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
                address_fence: Some(K::fence(component.first.from, component.maximum_to)),
                contributes_to_possible_span: false,
                has_unbounded_extent: false,
            });
        }
        self.reporter
            .range_accepted(component.first.from, component.first.to)?;
        self.coalesce(component.first)
    }

    fn coalesce(&mut self, record: Record<K>) -> Result<()> {
        let Some(mut previous) = self.output.take() else {
            self.output = Some(record);
            return Ok(());
        };
        if previous.value == record.value && previous.to.checked_next() == Some(record.from) {
            previous.to = record.to;
            self.output = Some(previous);
        } else {
            K::push(self.builder, previous)?;
            self.output = Some(record);
        }
        Ok(())
    }
}

impl<K: Copy> Component<K> {
    fn new(record: Record<K>) -> Self {
        Self {
            first: record,
            maximum_to: record.to,
            count: 1,
        }
    }
}

pub(super) trait DirectKey: IpKey {
    const SCRATCH_RECORD_SIZE: usize = Self::WIDTH * 2 + 4;

    fn push(builder: &mut Builder, record: Record<Self>) -> Result<()>;
    fn fence(from: Self, to: Self) -> ValidationAddressFence;

    fn encode_scratch(record: Record<Self>, output: &mut [u8]) {
        record.from.write_le(output);
        record
            .to
            .write_le(&mut output[Self::WIDTH..Self::WIDTH * 2]);
        output[Self::WIDTH * 2..Self::SCRATCH_RECORD_SIZE]
            .copy_from_slice(&record.value.to_le_bytes());
    }

    fn decode_scratch(bytes: &[u8]) -> Record<Self> {
        Record {
            from: Self::read_le(bytes, 0),
            to: Self::read_le(bytes, Self::WIDTH),
            value: u32::from_le_bytes(
                bytes[Self::WIDTH * 2..Self::SCRATCH_RECORD_SIZE]
                    .try_into()
                    .expect("fixed scratch value"),
            ),
        }
    }
}

impl DirectKey for Ipv4Key {
    fn push(builder: &mut Builder, record: Record<Self>) -> Result<()> {
        builder.push_direct_v4(record.from, record.to, record.value)
    }

    fn fence(from: Self, to: Self) -> ValidationAddressFence {
        ValidationAddressFence::Ipv4 { from, to }
    }
}

impl DirectKey for Ipv6Key {
    fn push(builder: &mut Builder, record: Record<Self>) -> Result<()> {
        builder.push_direct_v6(record.from, record.to, record.value)
    }

    fn fence(from: Self, to: Self) -> ValidationAddressFence {
        ValidationAddressFence::Ipv6 { from, to }
    }
}
