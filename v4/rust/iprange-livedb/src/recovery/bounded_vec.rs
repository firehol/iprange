use std::mem::size_of;

use crate::error::{Error, Result};

pub(crate) fn maximum<T>(bytes: u64) -> usize {
    usize::try_from(bytes / size_of::<T>() as u64).unwrap_or(usize::MAX)
}

pub(crate) fn push<T>(
    values: &mut Vec<T>,
    value: T,
    maximum: usize,
    purpose: &'static str,
) -> Result<()> {
    if values.len() == maximum {
        return Err(Error::BudgetExceeded(purpose));
    }
    if values.len() == values.capacity() {
        grow(values, maximum, purpose)?;
    }
    values.push(value);
    Ok(())
}

fn grow<T>(values: &mut Vec<T>, maximum: usize, purpose: &'static str) -> Result<()> {
    let wanted = values
        .capacity()
        .max(8)
        .saturating_mul(2)
        .min(maximum)
        .max(values.len() + 1);
    values
        .try_reserve_exact(wanted - values.len())
        .map_err(|_| Error::BudgetExceeded(purpose))?;
    if values.capacity() > maximum {
        return Err(Error::BudgetExceeded(purpose));
    }
    Ok(())
}
