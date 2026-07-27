//! Shared bounded feed callback batching.

use std::ffi::c_void;

use iprange_livedb::{Error, FeedEntry};

use crate::abi::{FeedInfo, FeedSinkFn};
use crate::error::CallError;
use crate::reader::encode_feed;
use crate::sink::{self, Control};

const CAPACITY: usize = 256;

pub(crate) struct FeedBatch {
    callback: FeedSinkFn,
    context: *mut c_void,
    records: [FeedInfo; CAPACITY],
    length: usize,
    delivered: u64,
    count_label: &'static str,
    callback_error: Option<CallError>,
}

impl FeedBatch {
    pub(crate) fn new(
        callback: FeedSinkFn,
        context: *mut c_void,
        count_label: &'static str,
    ) -> Self {
        Self {
            callback,
            context,
            records: [FeedInfo::default(); CAPACITY],
            length: 0,
            delivered: 0,
            count_label,
            callback_error: None,
        }
    }

    pub(crate) fn push(&mut self, feed: FeedEntry) -> Result<bool, Error> {
        self.records[self.length] = encode_feed(feed);
        self.length += 1;
        if self.length == self.records.len() {
            self.flush()
        } else {
            Ok(true)
        }
    }

    pub(crate) fn finish(
        mut self,
        operation: Result<(), CallError>,
    ) -> (u64, Result<(), CallError>) {
        let result = match self.callback_error.take() {
            Some(error) => Err(error),
            None => operation.and_then(|_| self.flush_tail()),
        };
        (self.delivered, result)
    }

    fn flush(&mut self) -> Result<bool, Error> {
        match sink::feed(self.callback, self.context, &self.records[..self.length]) {
            Ok(control) => {
                self.delivered = self
                    .delivered
                    .checked_add(self.length as u64)
                    .ok_or(Error::ArithmeticOverflow(self.count_label))?;
                self.length = 0;
                Ok(control == Control::Continue)
            }
            Err(error) => {
                self.callback_error = Some(error);
                Err(Error::InvalidArgument("C feed sink failed"))
            }
        }
    }

    fn flush_tail(&mut self) -> Result<(), CallError> {
        if self.length == 0 {
            return Ok(());
        }
        if self.flush()? {
            Ok(())
        } else {
            Err(Error::StoppedBySink.into())
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::abi::CallbackFailure;
    use iprange_livedb::FeedName;

    unsafe extern "C" fn collect_batch_sizes(
        context: *mut c_void,
        _records: *const FeedInfo,
        count: u64,
        _failure: *mut CallbackFailure,
    ) -> u32 {
        // SAFETY: the test passes this callback one live Vec for the complete call.
        let batches = unsafe { &mut *context.cast::<Vec<u64>>() };
        batches.push(count);
        1
    }

    #[test]
    fn full_and_tail_batches_preserve_every_feed() {
        let mut batches = Vec::new();
        let mut batch = FeedBatch::new(
            Some(collect_batch_sizes),
            (&mut batches as *mut Vec<u64>).cast(),
            "test feed count",
        );
        let name = FeedName::new("feed").unwrap();
        for index in 0..257 {
            assert!(batch.push(FeedEntry { name, index }).unwrap());
        }
        let (delivered, result) = batch.finish(Ok(()));
        result.unwrap();
        assert_eq!(delivered, 257);
        assert_eq!(batches, [256_u64, 1]);
    }
}
