//! Operation-bound advanced direct mutation.

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, ValueKind};
use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::random;

use super::{AbortResult, CommitResult, LiveWriter};

/// One ordered advanced direct transaction and its cancellation token.
#[derive(Debug)]
pub struct DirectTransaction<'a> {
    writer: &'a mut LiveWriter,
    state: DirectState,
}

/// Borrow-free direct-operation state shared with language bindings.
#[derive(Debug)]
pub(crate) struct DirectState {
    operation_nonce: [u8; 16],
    cancellation: CancellationToken,
}

impl LiveWriter {
    /// Begin one direct transaction on a clean writer.
    pub fn begin_direct_transaction(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<DirectTransaction<'_>> {
        let state = self.begin_direct_state(cancellation)?;
        Ok(DirectTransaction {
            writer: self,
            state,
        })
    }

    pub(crate) fn begin_direct_state(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<DirectState> {
        cancellation.check()?;
        self.require_healthy()?;
        if self.base.meta.value_kind != ValueKind::Direct {
            return Err(Error::WrongValueKind(
                "direct transaction requires a direct database",
            ));
        }
        if self.draft.is_some() {
            return Err(Error::WrongState("a writer transaction is already pending"));
        }
        let operation_nonce = random::nonzero_128()?;
        self.draft = Some(crate::draft_store::Draft::new(
            self.base.meta,
            operation_nonce,
        )?);
        Ok(DirectState {
            operation_nonce,
            cancellation: cancellation.clone(),
        })
    }
}

impl DirectTransaction<'_> {
    /// Assign one inclusive IPv4 interval in exact call order.
    pub fn assign_v4(&mut self, from: Ipv4Key, to: Ipv4Key, value: u32) -> Result<bool> {
        self.state.assign_v4(self.writer, from, to, value)
    }

    /// Assign one inclusive IPv6 interval in exact call order.
    pub fn assign_v6(&mut self, from: Ipv6Key, to: Ipv6Key, value: u32) -> Result<bool> {
        self.state.assign_v6(self.writer, from, to, value)
    }

    /// Clear one inclusive IPv4 interval.
    pub fn clear_v4(&mut self, from: Ipv4Key, to: Ipv4Key) -> Result<bool> {
        self.state.clear_v4(self.writer, from, to)
    }

    /// Clear one inclusive IPv6 interval.
    pub fn clear_v6(&mut self, from: Ipv6Key, to: Ipv6Key) -> Result<bool> {
        self.state.clear_v6(self.writer, from, to)
    }

    /// Stage one exact metadata replacement in this transaction.
    pub fn set_metadata_json(&mut self, input: &[u8]) -> Result<bool> {
        self.state.set_metadata_json(self.writer, input)
    }

    /// Stage metadata absence in this transaction.
    pub fn clear_metadata_json(&mut self) -> Result<bool> {
        self.state.clear_metadata_json(self.writer)
    }

    /// Exact decompressed length of committed or staged metadata.
    pub fn metadata_json_len(&self) -> Result<Option<u64>> {
        self.state.require_active(self.writer)?;
        self.writer.metadata_json_len()
    }

    /// Fill caller storage from committed or staged metadata.
    pub fn read_metadata_json(&self, output: &mut [u8]) -> Result<Option<usize>> {
        self.state.require_active(self.writer)?;
        self.writer.read_metadata_json(output)
    }

    /// Return the complete committed or staged bounded metadata value.
    pub fn metadata_json(&self) -> Result<Option<Vec<u8>>> {
        self.state.require_active(self.writer)?;
        self.writer.metadata_json()
    }

    /// Publish this transaction.
    pub fn commit(self) -> Result<CommitResult> {
        self.writer.commit_operation(&self.state.cancellation)
    }

    /// Discard this transaction.
    pub fn abort(self) -> Result<AbortResult> {
        self.writer.abort()
    }
}

impl DirectState {
    pub(crate) fn assign_v4(
        &mut self,
        writer: &mut LiveWriter,
        from: Ipv4Key,
        to: Ipv4Key,
        value: u32,
    ) -> Result<bool> {
        self.require_family(writer, AddressFamily::Ipv4, from <= to)?;
        self.run(writer, |writer| writer.assign_direct_v4(from, to, value))
    }

    pub(crate) fn assign_v6(
        &mut self,
        writer: &mut LiveWriter,
        from: Ipv6Key,
        to: Ipv6Key,
        value: u32,
    ) -> Result<bool> {
        self.require_family(writer, AddressFamily::Ipv6, from <= to)?;
        self.run(writer, |writer| writer.assign_direct_v6(from, to, value))
    }

    pub(crate) fn clear_v4(
        &mut self,
        writer: &mut LiveWriter,
        from: Ipv4Key,
        to: Ipv4Key,
    ) -> Result<bool> {
        self.require_family(writer, AddressFamily::Ipv4, from <= to)?;
        self.run(writer, |writer| writer.clear_direct_v4(from, to))
    }

    pub(crate) fn clear_v6(
        &mut self,
        writer: &mut LiveWriter,
        from: Ipv6Key,
        to: Ipv6Key,
    ) -> Result<bool> {
        self.require_family(writer, AddressFamily::Ipv6, from <= to)?;
        self.run(writer, |writer| writer.clear_direct_v6(from, to))
    }

    pub(crate) fn set_metadata_json(
        &mut self,
        writer: &mut LiveWriter,
        input: &[u8],
    ) -> Result<bool> {
        self.run(writer, |writer| writer.stage_metadata_json(input))
    }

    pub(crate) fn clear_metadata_json(&mut self, writer: &mut LiveWriter) -> Result<bool> {
        self.run(writer, LiveWriter::stage_clear_metadata_json)
    }

    pub(crate) fn cancellation(&self) -> &CancellationToken {
        &self.cancellation
    }

    fn run<T>(
        &mut self,
        writer: &mut LiveWriter,
        operation: impl FnOnce(&mut LiveWriter) -> Result<T>,
    ) -> Result<T> {
        self.check_or_abort(writer)?;
        let result = operation(writer);
        if result.is_ok() {
            self.check_or_abort(writer)?;
        }
        result
    }

    fn require_family(
        &self,
        writer: &LiveWriter,
        family: AddressFamily,
        ordered: bool,
    ) -> Result<()> {
        self.require_active(writer)?;
        if !ordered {
            return Err(Error::InvalidArgument("range start exceeds range end"));
        }
        if writer.base.meta.address_family != family {
            return Err(Error::WrongAddressFamily(
                "direct mutation does not match the database family",
            ));
        }
        Ok(())
    }

    pub(crate) fn require_active(&self, writer: &LiveWriter) -> Result<()> {
        let active = writer
            .draft
            .as_ref()
            .is_some_and(|draft| draft.meta.commit_nonce == self.operation_nonce);
        if !active {
            return Err(Error::WrongState("direct transaction is no longer active"));
        }
        writer.require_healthy()
    }

    fn check_or_abort(&mut self, writer: &mut LiveWriter) -> Result<()> {
        self.require_active(writer)?;
        self.cancellation
            .check()
            .map_err(|cause| writer.abort_after(cause))
    }
}

impl Drop for DirectTransaction<'_> {
    fn drop(&mut self) {
        if let Some(draft) = self.writer.draft.as_mut() {
            draft.abandon_operation();
        }
    }
}
