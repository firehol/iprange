use std::mem::size_of;

use crate::error::{Error, Result};
use crate::key::IpKey;
use crate::range_tree::Record;
use crate::validation::ValidationReason;

use super::range_scan::RangeEvents;
use super::RecoveryBudget;

pub(super) fn buffer_fits<K: IpKey>(
    records: u64,
    retained: u64,
    budget: &RecoveryBudget,
) -> Result<u64> {
    let bytes = records
        .checked_mul(size_of::<Record<K>>() as u64)
        .ok_or(Error::ArithmeticOverflow("recovery range buffer"))?;
    if bytes
        .checked_add(retained)
        .is_some_and(|total| total <= budget.max_heap_bytes)
    {
        Ok(bytes)
    } else {
        Err(Error::BudgetExceeded("recovery unordered ranges"))
    }
}

pub(super) fn reserve<K: IpKey>(records: u64, max_retained_bytes: u64) -> Result<Vec<Record<K>>> {
    let length =
        usize::try_from(records).map_err(|_| Error::BudgetExceeded("recovery unordered ranges"))?;
    let mut output = Vec::new();
    output
        .try_reserve_exact(length)
        .map_err(|_| Error::BudgetExceeded("recovery unordered ranges"))?;
    let retained = (output.capacity() as u64)
        .checked_mul(size_of::<Record<K>>() as u64)
        .ok_or(Error::ArithmeticOverflow("recovery range buffer"))?;
    if retained > max_retained_bytes {
        return Err(Error::BudgetExceeded("recovery unordered ranges"));
    }
    Ok(output)
}

pub(super) fn require_count(actual: u64, expected: u64) -> Result<()> {
    if actual == expected {
        Ok(())
    } else {
        Err(Error::RecoveryCandidateChanged)
    }
}

pub(super) fn events<K, F>(ordered: bool, emit: F) -> Events<K, F> {
    Events {
        ordered,
        previous_from: None,
        readable_records: 0,
        emit,
    }
}

pub(super) struct Events<K, F> {
    ordered: bool,
    previous_from: Option<K>,
    readable_records: u64,
    emit: F,
}

impl<K, F> Events<K, F> {
    pub(super) fn readable_records(&self) -> u64 {
        self.readable_records
    }
}

impl<K: IpKey, F: FnMut(Record<K>) -> Result<()>> RangeEvents<K> for Events<K, F> {
    fn page_accepted(&mut self) -> Result<()> {
        Ok(())
    }

    fn page_rejected(&mut self, _io_unreadable: bool) -> Result<()> {
        Ok(())
    }

    fn unknown(
        &mut self,
        _reason: ValidationReason,
        _page: Option<u32>,
        _unbounded: bool,
    ) -> Result<()> {
        Ok(())
    }

    fn range(&mut self, _page: u32, record: Option<Record<K>>) -> Result<()> {
        let Some(record) = record else {
            return Ok(());
        };
        if self.ordered
            && self
                .previous_from
                .is_some_and(|previous| previous >= record.from)
        {
            return Err(Error::RecoveryCandidateChanged);
        }
        self.previous_from = Some(record.from);
        self.readable_records = self
            .readable_records
            .checked_add(1)
            .ok_or(Error::ArithmeticOverflow("recovery readable ranges"))?;
        (self.emit)(record)
    }
}
