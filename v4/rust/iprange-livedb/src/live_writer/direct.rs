//! Operation-bound advanced direct mutation.

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, ValueKind};
use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};

use super::workflow::{require_transaction, run_transaction};
use super::{AbortResult, CommitResult, LiveWriter};

const INACTIVE: &str = "direct transaction is no longer active";

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
        if self.core.base_info().value_kind != ValueKind::Direct {
            return Err(Error::WrongValueKind(
                "direct transaction requires a direct database",
            ));
        }
        let operation_nonce = self.core.begin_transaction()?;
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
        self.require_mutation(writer, AddressFamily::Ipv4, from <= to)?;
        self.run(writer, |writer| {
            writer.mutate(|edit| edit.assign_v4(from, to, value))
        })
    }

    pub(crate) fn assign_v6(
        &mut self,
        writer: &mut LiveWriter,
        from: Ipv6Key,
        to: Ipv6Key,
        value: u32,
    ) -> Result<bool> {
        self.require_mutation(writer, AddressFamily::Ipv6, from <= to)?;
        self.run(writer, |writer| {
            writer.mutate(|edit| edit.assign_v6(from, to, value))
        })
    }

    pub(crate) fn clear_v4(
        &mut self,
        writer: &mut LiveWriter,
        from: Ipv4Key,
        to: Ipv4Key,
    ) -> Result<bool> {
        self.require_mutation(writer, AddressFamily::Ipv4, from <= to)?;
        self.run(writer, |writer| {
            writer.mutate(|edit| edit.clear_v4(from, to))
        })
    }

    pub(crate) fn clear_v6(
        &mut self,
        writer: &mut LiveWriter,
        from: Ipv6Key,
        to: Ipv6Key,
    ) -> Result<bool> {
        self.require_mutation(writer, AddressFamily::Ipv6, from <= to)?;
        self.run(writer, |writer| {
            writer.mutate(|edit| edit.clear_v6(from, to))
        })
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
        run_transaction(
            writer,
            self.operation_nonce,
            &self.cancellation,
            INACTIVE,
            operation,
        )
    }

    fn require_mutation(
        &self,
        writer: &LiveWriter,
        family: AddressFamily,
        ordered: bool,
    ) -> Result<()> {
        self.require_active(writer)?;
        writer.require_direct(family, ordered)
    }

    pub(crate) fn require_active(&self, writer: &LiveWriter) -> Result<()> {
        require_transaction(writer, self.operation_nonce, INACTIVE)
    }
}

impl Drop for DirectTransaction<'_> {
    fn drop(&mut self) {
        self.writer.core.abandon_operation();
    }
}
