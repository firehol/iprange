//! Operation-bound advanced direct mutation.

use crate::cancellation::CancellationToken;
use crate::contract::{AddressFamily, ValueKind};
use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::random;

use super::{CommitResult, LiveWriter};

/// One ordered advanced direct transaction and its cancellation token.
#[derive(Debug)]
pub struct DirectTransaction<'a> {
    writer: &'a mut LiveWriter,
    operation_nonce: [u8; 16],
    cancellation: CancellationToken,
}

impl LiveWriter {
    /// Begin one direct transaction on a clean writer.
    pub fn begin_direct_transaction(
        &mut self,
        cancellation: &CancellationToken,
    ) -> Result<DirectTransaction<'_>> {
        cancellation.check()?;
        self.require_healthy()?;
        if self.base.meta.value_kind != ValueKind::Direct {
            return Err(Error::WrongMode(
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
        Ok(DirectTransaction {
            writer: self,
            operation_nonce,
            cancellation: cancellation.clone(),
        })
    }
}

impl DirectTransaction<'_> {
    /// Assign one inclusive IPv4 interval in exact call order.
    pub fn assign_v4(&mut self, from: Ipv4Key, to: Ipv4Key, value: u32) -> Result<bool> {
        self.require_family(AddressFamily::Ipv4, from <= to)?;
        self.run(|writer| writer.assign_direct_v4(from, to, value))
    }

    /// Assign one inclusive IPv6 interval in exact call order.
    pub fn assign_v6(&mut self, from: Ipv6Key, to: Ipv6Key, value: u32) -> Result<bool> {
        self.require_family(AddressFamily::Ipv6, from <= to)?;
        self.run(|writer| writer.assign_direct_v6(from, to, value))
    }

    /// Clear one inclusive IPv4 interval.
    pub fn clear_v4(&mut self, from: Ipv4Key, to: Ipv4Key) -> Result<bool> {
        self.require_family(AddressFamily::Ipv4, from <= to)?;
        self.run(|writer| writer.clear_direct_v4(from, to))
    }

    /// Clear one inclusive IPv6 interval.
    pub fn clear_v6(&mut self, from: Ipv6Key, to: Ipv6Key) -> Result<bool> {
        self.require_family(AddressFamily::Ipv6, from <= to)?;
        self.run(|writer| writer.clear_direct_v6(from, to))
    }

    /// Stage one exact metadata replacement in this transaction.
    pub fn set_metadata_json(&mut self, input: &[u8]) -> Result<bool> {
        self.run(|writer| writer.stage_metadata_json(input))
    }

    /// Stage metadata absence in this transaction.
    pub fn clear_metadata_json(&mut self) -> Result<bool> {
        self.run(LiveWriter::stage_clear_metadata_json)
    }

    /// Exact decompressed length of committed or staged metadata.
    pub fn metadata_json_len(&self) -> Result<Option<u64>> {
        self.require_active()?;
        self.writer.metadata_json_len()
    }

    /// Fill caller storage from committed or staged metadata.
    pub fn read_metadata_json(&self, output: &mut [u8]) -> Result<Option<usize>> {
        self.require_active()?;
        self.writer.read_metadata_json(output)
    }

    /// Return the complete committed or staged bounded metadata value.
    pub fn metadata_json(&self) -> Result<Option<Vec<u8>>> {
        self.require_active()?;
        self.writer.metadata_json()
    }

    /// Publish this transaction.
    pub fn commit(self) -> Result<CommitResult> {
        self.writer.commit_cancellable(&self.cancellation)
    }

    /// Discard this transaction.
    pub fn abort(self) -> Result<()> {
        self.writer.abort()?;
        Ok(())
    }

    fn run<T>(&mut self, operation: impl FnOnce(&mut LiveWriter) -> Result<T>) -> Result<T> {
        self.check_or_abort()?;
        let result = operation(self.writer);
        if result.is_ok() {
            self.check_or_abort()?;
        }
        result
    }

    fn require_family(&self, family: AddressFamily, ordered: bool) -> Result<()> {
        self.require_active()?;
        if !ordered {
            return Err(Error::InvalidArgument("range start exceeds range end"));
        }
        if self.writer.base.meta.address_family != family {
            return Err(Error::WrongMode(
                "direct mutation does not match the database family",
            ));
        }
        Ok(())
    }

    fn require_active(&self) -> Result<()> {
        let active = self
            .writer
            .draft
            .as_ref()
            .is_some_and(|draft| draft.meta.commit_nonce == self.operation_nonce);
        if !active {
            return Err(Error::WrongState("direct transaction is no longer active"));
        }
        self.writer.require_healthy()
    }

    fn check_or_abort(&mut self) -> Result<()> {
        self.require_active()?;
        self.cancellation
            .check()
            .map_err(|cause| self.writer.abort_after(cause))
    }
}

impl Drop for DirectTransaction<'_> {
    fn drop(&mut self) {
        if let Some(draft) = self.writer.draft.as_mut() {
            draft.abandon_operation();
        }
    }
}
