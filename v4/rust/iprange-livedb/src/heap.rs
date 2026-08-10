//! Exact accounting for caller-bounded retained heap.

use std::mem::size_of;

use crate::error::{Error, Result};

pub(crate) struct Heap {
    remaining: u64,
    used: u64,
}

impl Heap {
    pub(crate) const fn new(max_heap_bytes: u64) -> Self {
        Self {
            remaining: max_heap_bytes,
            used: 0,
        }
    }

    pub(crate) const fn used(&self) -> u64 {
        self.used
    }

    pub(crate) const fn remaining(&self) -> u64 {
        self.remaining
    }

    pub(crate) fn reserve_bytes(&mut self, bytes: usize, label: &'static str) -> Result<()> {
        let bytes = u64::try_from(bytes).map_err(|_| Error::BudgetExceeded(label))?;
        self.remaining = self
            .remaining
            .checked_sub(bytes)
            .ok_or(Error::BudgetExceeded(label))?;
        self.used = self
            .used
            .checked_add(bytes)
            .ok_or(Error::BudgetExceeded(label))?;
        Ok(())
    }

    pub(crate) fn vector<T>(&mut self, capacity: usize, label: &'static str) -> Result<Vec<T>> {
        self.charge::<T>(capacity, label)?;
        let mut values = Vec::new();
        values
            .try_reserve_exact(capacity)
            .map_err(|_| Error::BudgetExceeded(label))?;
        self.charge::<T>(values.capacity().saturating_sub(capacity), label)?;
        Ok(values)
    }

    pub(crate) fn filled<T: Clone>(
        &mut self,
        length: usize,
        value: T,
        label: &'static str,
    ) -> Result<Vec<T>> {
        let mut values = self.vector(length, label)?;
        values.resize(length, value);
        Ok(values)
    }

    fn charge<T>(&mut self, count: usize, label: &'static str) -> Result<()> {
        let bytes = count
            .checked_mul(size_of::<T>())
            .and_then(|bytes| u64::try_from(bytes).ok())
            .ok_or(Error::BudgetExceeded(label))?;
        self.remaining = self
            .remaining
            .checked_sub(bytes)
            .ok_or(Error::BudgetExceeded(label))?;
        self.used = self
            .used
            .checked_add(bytes)
            .ok_or(Error::BudgetExceeded(label))?;
        Ok(())
    }
}
