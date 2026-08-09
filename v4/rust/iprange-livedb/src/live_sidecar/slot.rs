//! Exact reader-table slot codec.

use crate::contract::u64_le;
use crate::error::{Error, Result};
use crate::mapping::{ByteSource, BytesMut};

pub(super) const SIZE: u16 = 16;
const TRANSACTION_OFFSET: usize = 0;
const COMPLEMENT_OFFSET: usize = 8;

pub(super) fn write(slot: &mut BytesMut<'_>, transaction: u64) -> Result<()> {
    slot.put_u64(TRANSACTION_OFFSET, transaction)?;
    slot.put_u64(COMPLEMENT_OFFSET, !transaction)
}

pub(super) fn active_transaction<S: ByteSource>(slot: S) -> Result<u64> {
    let transaction = u64_le(slot, TRANSACTION_OFFSET);
    if transaction == 0 || u64_le(slot, COMPLEMENT_OFFSET) != !transaction {
        return Err(Error::Corrupt("active reader slot is malformed"));
    }
    Ok(transaction)
}

pub(super) fn is_clear<S: ByteSource>(slot: S) -> bool {
    slot.all_zero(0, SIZE as usize)
}
