use crate::cardinality::Cardinality129;
use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::validation::{
    PhysicalByteInterval, ValidationAddressFence, ValidationObject, ValidationReason,
};

/// Physical-page facts established while reading one recovery candidate.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct RecoveryPageCounts {
    pub examined: u64,
    pub accepted: u64,
    pub rejected: u64,
    pub io_unreadable: u64,
}

/// Logical-object facts established while reading one recovery candidate.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct RecoveryLogicalCounts {
    pub examined: u64,
    pub accepted: u64,
    pub rejected: u64,
}

/// Truthful completed or partial recovery facts.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct RecoveryReport {
    pub pages: RecoveryPageCounts,
    pub ranges: RecoveryLogicalCounts,
    pub catalog_entries: RecoveryLogicalCounts,
    pub membership_entries: RecoveryLogicalCounts,
    pub metadata_chunks: RecoveryLogicalCounts,
    pub retirement_records: RecoveryLogicalCounts,
    pub verified_addresses: Cardinality129,
    pub rejected_addresses: Cardinality129,
    pub bounded_possible_span_addresses: Cardinality129,
    pub has_unbounded_unknown: bool,
    pub unknown_envelopes: u64,
}

/// One independently established recovery-damage envelope.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct RecoveryUnknownEnvelope {
    pub sequence: u64,
    pub reason: ValidationReason,
    pub object: ValidationObject,
    pub page_number: Option<u32>,
    pub physical_bytes: Option<PhysicalByteInterval>,
    pub address_fence: Option<ValidationAddressFence>,
    pub contributes_to_possible_span: bool,
    pub has_unbounded_extent: bool,
}

/// Recovery-sink response for one borrowed damage envelope.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RecoverySinkControl {
    Continue,
    Stop,
}

/// Synchronous recovery-damage consumer.
pub trait RecoverySink {
    fn unknown(&mut self, envelope: &RecoveryUnknownEnvelope) -> Result<RecoverySinkControl>;
}

impl<F> RecoverySink for F
where
    F: FnMut(&RecoveryUnknownEnvelope) -> Result<RecoverySinkControl>,
{
    fn unknown(&mut self, envelope: &RecoveryUnknownEnvelope) -> Result<RecoverySinkControl> {
        self(envelope)
    }
}

pub(crate) struct Reporter<'a, S> {
    report: RecoveryReport,
    sequence: u64,
    sink: &'a mut S,
}

pub(crate) struct Unknown {
    pub(crate) reason: ValidationReason,
    pub(crate) object: ValidationObject,
    pub(crate) page_number: Option<u32>,
    pub(crate) physical_bytes: Option<PhysicalByteInterval>,
    pub(crate) address_fence: Option<ValidationAddressFence>,
    pub(crate) contributes_to_possible_span: bool,
    pub(crate) has_unbounded_extent: bool,
}

impl<'a, S: RecoverySink> Reporter<'a, S> {
    pub(crate) fn new(sink: &'a mut S) -> Self {
        Self {
            report: RecoveryReport::default(),
            sequence: 0,
            sink,
        }
    }

    pub(crate) fn resume(report: RecoveryReport, sink: &'a mut S) -> Self {
        Self {
            sequence: report.unknown_envelopes,
            report,
            sink,
        }
    }

    pub(crate) fn finish(self) -> RecoveryReport {
        self.report
    }

    pub(crate) fn report(&self) -> &RecoveryReport {
        &self.report
    }

    pub(crate) fn page_accepted(&mut self) -> Result<()> {
        increment(&mut self.report.pages.examined, "recovery pages examined")?;
        increment(&mut self.report.pages.accepted, "recovery pages accepted")
    }

    pub(crate) fn page_rejected(&mut self, io_unreadable: bool) -> Result<()> {
        increment(&mut self.report.pages.examined, "recovery pages examined")?;
        increment(&mut self.report.pages.rejected, "recovery pages rejected")?;
        if io_unreadable {
            increment(
                &mut self.report.pages.io_unreadable,
                "recovery I/O-unreadable pages",
            )?;
        }
        Ok(())
    }

    pub(crate) fn range_examined(&mut self) -> Result<()> {
        increment(&mut self.report.ranges.examined, "recovery ranges examined")
    }

    pub(crate) fn range_accepted<K: crate::key::IpKey>(&mut self, from: K, to: K) -> Result<()> {
        increment(&mut self.report.ranges.accepted, "recovery ranges accepted")?;
        add_addresses(&mut self.report.verified_addresses, from, to)
    }

    pub(crate) fn ranges_rejected<K: crate::key::IpKey>(
        &mut self,
        count: u64,
        from: K,
        to: K,
    ) -> Result<()> {
        self.report.ranges.rejected = self
            .report
            .ranges
            .rejected
            .checked_add(count)
            .ok_or(Error::ArithmeticOverflow("recovery ranges rejected"))?;
        add_addresses(&mut self.report.rejected_addresses, from, to)
    }

    pub(crate) fn range_rejected_without_bounds(&mut self) -> Result<()> {
        increment(&mut self.report.ranges.rejected, "recovery ranges rejected")
    }

    pub(crate) fn metadata_chunk_examined(&mut self) -> Result<()> {
        increment(
            &mut self.report.metadata_chunks.examined,
            "recovery metadata chunks examined",
        )
    }

    pub(crate) fn metadata_finished(&mut self, accepted: bool) -> Result<()> {
        let count = self.report.metadata_chunks.examined;
        let destination = if accepted {
            &mut self.report.metadata_chunks.accepted
        } else {
            &mut self.report.metadata_chunks.rejected
        };
        *destination = destination
            .checked_add(count)
            .ok_or(Error::ArithmeticOverflow("recovery metadata chunk outcome"))?;
        Ok(())
    }

    pub(crate) fn unknown(&mut self, unknown: Unknown) -> Result<()> {
        self.sequence = self
            .sequence
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("recovery unknown sequence"))?;
        self.report.unknown_envelopes = self.sequence;
        if unknown.contributes_to_possible_span {
            let fence = unknown.address_fence.ok_or(Error::Corrupt(
                "bounded recovery unknown is missing its address fence",
            ))?;
            self.add_possible_span(fence)?;
        }
        self.report.has_unbounded_unknown |= unknown.has_unbounded_extent;
        let envelope = RecoveryUnknownEnvelope {
            sequence: self.sequence,
            reason: unknown.reason,
            object: unknown.object,
            page_number: unknown.page_number,
            physical_bytes: unknown.physical_bytes,
            address_fence: unknown.address_fence,
            contributes_to_possible_span: unknown.contributes_to_possible_span,
            has_unbounded_extent: unknown.has_unbounded_extent,
        };
        match self.sink.unknown(&envelope) {
            Ok(RecoverySinkControl::Continue) => Ok(()),
            Ok(RecoverySinkControl::Stop) => Err(Error::StoppedBySink),
            Err(cause) => Err(Error::SinkFailed(Box::new(cause))),
        }
    }

    fn add_possible_span(&mut self, fence: ValidationAddressFence) -> Result<()> {
        let cardinality = match fence {
            ValidationAddressFence::Ipv4 { from, to } => from.inclusive_cardinality(to)?,
            ValidationAddressFence::Ipv6 { from, to } => from.inclusive_cardinality(to)?,
        };
        self.report.bounded_possible_span_addresses = self
            .report
            .bounded_possible_span_addresses
            .checked_add(cardinality)
            .map_err(|_| Error::ArithmeticOverflow("recovery possible-span cardinality"))?;
        Ok(())
    }
}

fn increment(value: &mut u64, purpose: &'static str) -> Result<()> {
    *value = value
        .checked_add(1)
        .ok_or(Error::ArithmeticOverflow(purpose))?;
    Ok(())
}

fn add_addresses<K: crate::key::IpKey>(total: &mut Cardinality129, from: K, to: K) -> Result<()> {
    *total = total
        .checked_add(from.inclusive_cardinality(to)?)
        .map_err(|_| Error::ArithmeticOverflow("recovery address cardinality"))?;
    Ok(())
}
