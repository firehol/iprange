//! Synchronous borrowed batches for range ingestion.

use crate::cancellation::CancellationToken;
use crate::error::{Error, Result};
use crate::feed_range_cursor::{FeedRangeCursorV4, FeedRangeCursorV6};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::range_cursor::{DirectCursorV4, DirectCursorV6, DirectRange};
use crate::workflow::AddressRange;

const MAPPED_BATCH_CAPACITY: usize = 256;

/// Finite synchronous source whose borrowed batches remain caller-owned.
pub trait RangeSource<R> {
    /// Return a nonempty batch, `None` for end, or an exact source error.
    fn next_batch(&mut self) -> Result<Option<&[R]>>;
}

pub(crate) fn drain<R, S, F>(
    source: &mut S,
    cancellation: &CancellationToken,
    input_records: &mut u64,
    mut apply: F,
) -> Result<()>
where
    R: Copy,
    S: RangeSource<R>,
    F: FnMut(R) -> Result<()>,
{
    crate::work::source_pass(1);
    crate::work::input_source_pass(1);
    loop {
        cancellation.check()?;
        let Some(batch) = source.next_batch()? else {
            return Ok(());
        };
        if batch.is_empty() {
            return Err(Error::InvalidArgument(
                "range source returned an empty batch",
            ));
        }
        for (chunk_index, chunk) in batch.chunks(4096).enumerate() {
            if chunk_index != 0 {
                cancellation.check()?;
            }
            for &record in chunk {
                let next = input_records
                    .checked_add(1)
                    .ok_or_else(|| Error::arithmetic_overflow("workflow input record count"))?;
                apply(record)?;
                crate::work::range_consumed(1);
                *input_records = next;
            }
        }
    }
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

macro_rules! mapped_source {
    ($name:ident, $cursor:ty, $record:ty, $empty:expr) => {
        #[derive(Debug)]
        pub struct $name<'a> {
            cursor: $cursor,
            batch: [$record; MAPPED_BATCH_CAPACITY],
            finished: bool,
        }

        impl<'a> $name<'a> {
            pub(crate) fn new(cursor: $cursor) -> Self {
                Self {
                    cursor,
                    batch: [$empty; MAPPED_BATCH_CAPACITY],
                    finished: false,
                }
            }
        }

        impl RangeSource<$record> for $name<'_> {
            fn next_batch(&mut self) -> Result<Option<&[$record]>> {
                if self.finished {
                    return Ok(None);
                }
                let mut count = 0;
                while count < self.batch.len() {
                    let Some(record) = self.cursor.next_range()? else {
                        self.finished = true;
                        break;
                    };
                    self.batch[count] = record;
                    count += 1;
                }
                Ok((count != 0).then_some(&self.batch[..count]))
            }
        }
    };
}

mapped_source!(
    FeedRangeSourceV4,
    FeedRangeCursorV4<'a>,
    AddressRange<Ipv4Key>,
    AddressRange {
        from: Ipv4Key::MIN,
        to: Ipv4Key::MIN,
    }
);
mapped_source!(
    FeedRangeSourceV6,
    FeedRangeCursorV6<'a>,
    AddressRange<Ipv6Key>,
    AddressRange {
        from: Ipv6Key::MIN,
        to: Ipv6Key::MIN,
    }
);
mapped_source!(
    DirectRangeSourceV4,
    DirectCursorV4<'a>,
    DirectRange<Ipv4Key>,
    DirectRange {
        from: Ipv4Key::MIN,
        to: Ipv4Key::MIN,
        value: 0,
    }
);
mapped_source!(
    DirectRangeSourceV6,
    DirectCursorV6<'a>,
    DirectRange<Ipv6Key>,
    DirectRange {
        from: Ipv6Key::MIN,
        to: Ipv6Key::MIN,
        value: 0,
    }
);
