//! Synchronous borrowed batches for range ingestion.

use crate::error::Result;

/// Finite synchronous source whose borrowed batches remain caller-owned.
pub trait RangeSource<R> {
    /// Return a nonempty batch, `None` for end, or an exact source error.
    fn next_batch(&mut self) -> Result<Option<&[R]>>;
}

/// One finite borrowed slice exposed through [`RangeSource`].
#[derive(Debug)]
pub struct SliceSource<'a, R> {
    remaining: Option<&'a [R]>,
}

impl<'a, R> SliceSource<'a, R> {
    pub fn new(records: &'a [R]) -> Self {
        Self {
            remaining: Some(records),
        }
    }
}

impl<R> RangeSource<R> for SliceSource<'_, R> {
    fn next_batch(&mut self) -> Result<Option<&[R]>> {
        Ok(self.remaining.take().filter(|batch| !batch.is_empty()))
    }
}
