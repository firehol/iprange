//! Transaction-bound typed structure references and range mutation.

use std::fmt;

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, StructureKind};
use crate::error::{Error, Result};
use crate::feed::FeedName;
use crate::key::{Ipv4Key, Ipv6Key};
use crate::writer_core::MembershipHandle;
use crate::writer_core::StructureHandle;
use crate::NetworkEnrichmentV1;

use super::membership::{FeedRef, MembershipRef, MembershipState, TransactionFeedCursor};
use super::{CommitResult, LiveWriter};

/// One SDK-owned structure valid only in its creating transaction.
#[derive(Clone, Copy, PartialEq, Eq)]
pub struct StructureRef {
    database_id: [u8; 16],
    operation_nonce: [u8; 16],
    handle: StructureHandle,
    catalog_epoch: u64,
}

impl fmt::Debug for StructureRef {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output.debug_struct("StructureRef").finish_non_exhaustive()
    }
}

/// One advanced typed structure operation over a clean live writer.
#[derive(Debug)]
pub struct StructuredTransaction<'a> {
    writer: &'a mut LiveWriter,
    state: MembershipState,
}

impl LiveWriter {
    /// Begin one advanced transaction for the file's hardcoded structure kind.
    pub fn begin_structured_transaction(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<StructuredTransaction<'_>> {
        if self.core.base_info().structure_kind != StructureKind::NetworkEnrichmentV1 {
            return Err(Error::WrongStructureKind(
                "no typed transaction exists for this structure kind",
            ));
        }
        let state = self.begin_structured_state(cancellation)?;
        Ok(StructuredTransaction {
            writer: self,
            state,
        })
    }
}

impl StructuredTransaction<'_> {
    /// Enumerate the file's threat-feed catalog by ascending index.
    pub fn feed_cursor(&mut self) -> Result<TransactionFeedCursor<'_>> {
        self.state.feed_cursor(self.writer)
    }

    /// Return an exact existing threat feed without creating it.
    pub fn lookup_feed(&mut self, name: FeedName) -> Result<Option<FeedRef>> {
        self.state.lookup_feed(self.writer, name)
    }

    /// Return the exact threat feed, creating it at the lowest free index if absent.
    pub fn ensure_feed(&mut self, name: FeedName) -> Result<FeedRef> {
        self.state.ensure_feed(self.writer, name)
    }

    /// Rename one threat feed while preserving its index and membership.
    pub fn rename_feed(&mut self, feed: FeedRef, new_name: FeedName) -> Result<FeedRef> {
        self.state.rename_feed(self.writer, feed, new_name)
    }

    /// Delete one threat feed and clear it from every stored structure.
    pub fn delete_feed(&mut self, feed: FeedRef) -> Result<()> {
        self.state.delete_structured_feed(self.writer, feed)
    }

    /// Construct the empty threat membership without allocating an ID.
    pub fn empty_membership(&mut self) -> Result<MembershipRef> {
        self.state.empty_membership(self.writer)
    }

    /// Add one threat feed to a transaction-owned membership.
    pub fn add_feed(&mut self, membership: MembershipRef, feed: FeedRef) -> Result<MembershipRef> {
        self.state.add_feed(self.writer, membership, feed)
    }

    /// Intern one typed enrichment profile with optional threat membership.
    pub fn intern_network_enrichment_v1(
        &mut self,
        value: NetworkEnrichmentV1,
        membership: Option<MembershipRef>,
    ) -> Result<StructureRef> {
        self.state
            .intern_network_enrichment_v1(self.writer, value, membership)
    }

    /// Apply one structure to an inclusive IPv4 interval.
    pub fn assign_v4(
        &mut self,
        from: Ipv4Key,
        to: Ipv4Key,
        structure: StructureRef,
    ) -> Result<bool> {
        self.state
            .assign_structure_v4(self.writer, from, to, structure)
    }

    /// Apply one structure to an inclusive IPv6 interval.
    pub fn assign_v6(
        &mut self,
        from: Ipv6Key,
        to: Ipv6Key,
        structure: StructureRef,
    ) -> Result<bool> {
        self.state
            .assign_structure_v6(self.writer, from, to, structure)
    }

    /// Clear one inclusive IPv4 interval.
    pub fn clear_v4(&mut self, from: Ipv4Key, to: Ipv4Key) -> Result<bool> {
        self.state.clear_structure_v4(self.writer, from, to)
    }

    /// Clear one inclusive IPv6 interval.
    pub fn clear_v6(&mut self, from: Ipv6Key, to: Ipv6Key) -> Result<bool> {
        self.state.clear_structure_v6(self.writer, from, to)
    }

    /// Stage one exact opaque metadata replacement.
    pub fn set_metadata_json(&mut self, input: &[u8]) -> Result<bool> {
        self.state.set_metadata_json(self.writer, input)
    }

    /// Stage metadata absence.
    pub fn clear_metadata_json(&mut self) -> Result<bool> {
        self.state.clear_metadata_json(self.writer)
    }

    /// Publish this transaction through the alternate metadata page.
    pub fn commit(self) -> Result<CommitResult> {
        self.writer.commit_operation(self.state.cancellation())
    }

    /// Discard this transaction and invalidate all of its references.
    pub fn abort(self) -> Result<super::AbortResult> {
        self.writer.abort()
    }
}

impl MembershipState {
    pub(crate) fn delete_structured_feed(
        &mut self,
        writer: &mut LiveWriter,
        feed: FeedRef,
    ) -> Result<()> {
        self.require_current_feed(writer, feed)?;
        self.check_or_abort(writer)?;
        let cancellation = self.cancellation().clone();
        writer.mutate(|edit| {
            edit.delete_current_structured_feed(feed.entry, &mut || cancellation.check())
        })?;
        self.check_or_abort(writer)?;
        self.invalidate_memberships()
    }

    pub(crate) fn intern_network_enrichment_v1(
        &mut self,
        writer: &mut LiveWriter,
        value: NetworkEnrichmentV1,
        membership: Option<MembershipRef>,
    ) -> Result<StructureRef> {
        let membership = match membership {
            Some(membership) => {
                self.require_current_membership(writer, membership)?;
                membership.handle
            }
            None => MembershipHandle::empty(),
        };
        let handle = writer.mutate(|edit| edit.intern_network_enrichment_v1(value, membership))?;
        self.check_or_abort(writer)?;
        Ok(self.structure_reference(writer, handle))
    }

    pub(crate) fn assign_structure_v4(
        &mut self,
        writer: &mut LiveWriter,
        from: Ipv4Key,
        to: Ipv4Key,
        structure: StructureRef,
    ) -> Result<bool> {
        self.require_structure_family(writer, AddressFamily::Ipv4, from <= to)?;
        self.require_current_structure(writer, structure)?;
        let changed = writer.mutate(|edit| {
            edit.assign_structure_input_v4(
                from,
                to,
                structure.handle,
                &mut self.structured_input_v4,
            )
        })?;
        self.check_or_abort(writer)?;
        Ok(changed)
    }

    pub(crate) fn assign_structure_v6(
        &mut self,
        writer: &mut LiveWriter,
        from: Ipv6Key,
        to: Ipv6Key,
        structure: StructureRef,
    ) -> Result<bool> {
        self.require_structure_family(writer, AddressFamily::Ipv6, from <= to)?;
        self.require_current_structure(writer, structure)?;
        let changed = writer.mutate(|edit| {
            edit.assign_structure_input_v6(
                from,
                to,
                structure.handle,
                &mut self.structured_input_v6,
            )
        })?;
        self.check_or_abort(writer)?;
        Ok(changed)
    }

    pub(crate) fn clear_structure_v4(
        &mut self,
        writer: &mut LiveWriter,
        from: Ipv4Key,
        to: Ipv4Key,
    ) -> Result<bool> {
        let empty = self.structure_reference(writer, StructureHandle::empty());
        self.assign_structure_v4(writer, from, to, empty)
    }

    pub(crate) fn clear_structure_v6(
        &mut self,
        writer: &mut LiveWriter,
        from: Ipv6Key,
        to: Ipv6Key,
    ) -> Result<bool> {
        let empty = self.structure_reference(writer, StructureHandle::empty());
        self.assign_structure_v6(writer, from, to, empty)
    }

    fn structure_reference(&self, writer: &LiveWriter, handle: StructureHandle) -> StructureRef {
        StructureRef {
            database_id: writer.core.base_info().database_id,
            operation_nonce: self.operation_nonce(),
            handle,
            catalog_epoch: self.reference_epoch(),
        }
    }

    fn require_current_structure(
        &mut self,
        writer: &mut LiveWriter,
        structure: StructureRef,
    ) -> Result<()> {
        self.require_active(writer)?;
        if structure.database_id != writer.core.base_info().database_id {
            return Err(Error::ForeignReference);
        }
        if structure.operation_nonce != self.operation_nonce() {
            return Err(Error::StaleReference);
        }
        if structure.catalog_epoch != self.reference_epoch() {
            return Err(Error::StaleReference);
        }
        Ok(())
    }

    fn require_structure_family(
        &self,
        writer: &LiveWriter,
        family: AddressFamily,
        ordered: bool,
    ) -> Result<()> {
        self.require_active(writer)?;
        if !ordered {
            return Err(Error::InvalidArgument("range start exceeds range end"));
        }
        if writer.core.base_info().address_family != family {
            return Err(Error::WrongAddressFamily(
                "structured mutation does not match the database family",
            ));
        }
        Ok(())
    }
}

impl Drop for StructuredTransaction<'_> {
    fn drop(&mut self) {
        self.writer.core.abandon_operation();
    }
}
