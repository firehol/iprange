use crate::error::Result;
use crate::immutable_output::Builder;
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::range_tree::Record;
use crate::validation::{ValidationAddressFence, ValidationObject, ValidationReason};

use super::range_components::Policy;
use super::report::{RecoverySink, Reporter, Unknown};

pub(super) struct DirectOutput<'a, 'b, S, K> {
    builder: &'a mut Builder,
    reporter: &'a mut Reporter<'b, S>,
    output: Option<Record<K>>,
}

impl<'a, 'b, S: RecoverySink, K: DirectKey> DirectOutput<'a, 'b, S, K> {
    pub(super) fn new(builder: &'a mut Builder, reporter: &'a mut Reporter<'b, S>) -> Self {
        Self {
            builder,
            reporter,
            output: None,
        }
    }

    fn finish_output(&mut self) -> Result<()> {
        if let Some(record) = self.output.take() {
            K::push(self.builder, record)?;
        }
        Ok(())
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

impl<S: RecoverySink, K: DirectKey> Policy<K> for DirectOutput<'_, '_, S, K> {
    type Resolved = ();

    fn resolve(&mut self, _record: Record<K>) -> Result<Option<Self::Resolved>> {
        Ok(None)
    }

    fn accept(&mut self, record: Record<K>, _resolved: Option<Self::Resolved>) -> Result<()> {
        self.reporter.range_accepted(record.from, record.to)?;
        self.coalesce(record)
    }

    fn reject_overlap(&mut self, count: u64, from: K, to: K) -> Result<()> {
        report_overlap(self.reporter, count, from, to)
    }

    fn finish(&mut self) -> Result<()> {
        self.finish_output()
    }
}

pub(super) fn report_overlap<K: DirectKey, S: RecoverySink>(
    reporter: &mut Reporter<'_, S>,
    count: u64,
    from: K,
    to: K,
) -> Result<()> {
    reporter.ranges_rejected(count, from, to)?;
    reporter.unknown(Unknown {
        reason: ValidationReason::RangeOverlap,
        object: ValidationObject::RangeTree,
        page_number: None,
        physical_bytes: None,
        address_fence: Some(K::fence(from, to)),
        contributes_to_possible_span: false,
        has_unbounded_extent: false,
    })
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
