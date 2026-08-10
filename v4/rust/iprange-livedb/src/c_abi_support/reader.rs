use std::path::Path;

use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::range_cursor::RangeDirection;
use crate::reader_core::ReaderCore;
use crate::{
    CancellationToken, CloseOutcome, DatabaseInfo, FeedEntry, ImmutableReader, LiveReader,
    MatchingFeedSink, MatchingFeedsReport, MembershipImportSource, MembershipQuery,
};

pub use crate::reader_core::{MembershipToken, ReaderCursor, ReaderCursorItem};

/// Reader ownership that can be retained by C child handles.
#[derive(Debug)]
pub struct Reader {
    inner: ReaderInner,
}

#[derive(Debug)]
enum ReaderInner {
    Immutable(ImmutableReader),
    Live(LiveReader),
    Closed,
}

impl Reader {
    pub fn open_immutable(path: impl AsRef<Path>) -> Result<Self> {
        Ok(Self {
            inner: ReaderInner::Immutable(ImmutableReader::open(path)?),
        })
    }

    pub fn open_live(path: impl AsRef<Path>, cancellation: &CancellationToken) -> Result<Self> {
        Ok(Self {
            inner: ReaderInner::Live(LiveReader::open(path, cancellation)?),
        })
    }

    pub fn info(&self) -> Result<DatabaseInfo> {
        Ok(self.core()?.info())
    }

    pub fn metadata_json_len(&self) -> Result<Option<u64>> {
        Ok(self.core()?.read().metadata_json_len())
    }

    pub fn lookup_direct_v4(&self, address: Ipv4Key) -> Result<Option<u32>> {
        self.core()?.read().lookup_direct_v4(address)
    }

    pub fn lookup_direct_v6(&self, address: Ipv6Key) -> Result<Option<u32>> {
        self.core()?.read().lookup_direct_v6(address)
    }

    pub fn lookup_feed(&self, name: &str) -> Result<Option<FeedEntry>> {
        self.core()?.read().lookup_feed(name)
    }

    pub fn matching_feeds_v4<S: MatchingFeedSink>(
        &self,
        address: Ipv4Key,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<MatchingFeedsReport> {
        MembershipQuery::new(self.core()?)?.matching_feeds_v4(address, sink, cancellation)
    }

    pub fn matching_feeds_v6<S: MatchingFeedSink>(
        &self,
        address: Ipv6Key,
        sink: &mut S,
        cancellation: &CancellationToken,
    ) -> Result<MatchingFeedsReport> {
        MembershipQuery::new(self.core()?)?.matching_feeds_v6(address, sink, cancellation)
    }

    pub fn open_direct_cursor(&self, direction: RangeDirection) -> Result<ReaderCursor> {
        self.core()?.read().open_direct_state(direction)
    }

    pub fn open_membership_cursor(&self, direction: RangeDirection) -> Result<ReaderCursor> {
        self.core()?.read().open_membership_state(direction)
    }

    pub fn open_feed_cursor(&self, name: &str, direction: RangeDirection) -> Result<ReaderCursor> {
        self.core()?.read().open_feed_state(name, direction)
    }

    pub fn cursor_next(&self, cursor: &mut ReaderCursor) -> Result<Option<ReaderCursorItem>> {
        self.core()?.read().cursor_next(cursor)
    }

    pub fn cursor_seek_v4(&self, cursor: &mut ReaderCursor, target: Ipv4Key) -> Result<()> {
        self.core()?.read().cursor_seek_v4(cursor, target)
    }

    pub fn cursor_seek_v6(&self, cursor: &mut ReaderCursor, target: Ipv6Key) -> Result<()> {
        self.core()?.read().cursor_seek_v6(cursor, target)
    }

    pub fn lookup_membership_token_v4(&self, address: Ipv4Key) -> Result<Option<MembershipToken>> {
        self.core()?.read().membership_token_v4(address)
    }

    pub fn lookup_membership_token_v6(&self, address: Ipv6Key) -> Result<Option<MembershipToken>> {
        self.core()?.read().membership_token_v6(address)
    }

    pub fn membership_word_count(&self, token: MembershipToken) -> Result<u32> {
        self.with_membership_token(token, |view| view.word_count())
    }

    pub fn membership_word(&self, token: MembershipToken, index: u32) -> Result<Option<u64>> {
        self.with_membership_token(token, |view| view.word(index))
    }

    pub fn membership_words(
        &self,
        token: MembershipToken,
        start: u32,
        output: &mut [u64],
    ) -> Result<usize> {
        self.with_membership_token(token, |view| view.read_words(start, output))
    }

    pub fn membership_contains(&self, token: MembershipToken, index: u32) -> Result<bool> {
        self.with_membership_token(token, |view| view.contains_index(index))
    }

    pub fn enumerate_feeds(&self, mut sink: impl FnMut(FeedEntry) -> Result<bool>) -> Result<u64> {
        let mut cursor = self.core()?.read().feed_cursor()?;
        let mut count = 0u64;
        while let Some(feed) = cursor.next_feed()? {
            if !sink(feed)? {
                return Err(Error::StoppedBySink);
            }
            count = count
                .checked_add(1)
                .ok_or(Error::ArithmeticOverflow("feed scan count"))?;
        }
        Ok(count)
    }

    pub fn membership_present_v4(&self, address: Ipv4Key) -> Result<bool> {
        self.with_membership_v4(address, |membership| Ok(membership.is_some()))
    }

    pub fn membership_present_v6(&self, address: Ipv6Key) -> Result<bool> {
        self.with_membership_v6(address, |membership| Ok(membership.is_some()))
    }

    pub fn membership_word_count_v4(&self, address: Ipv4Key) -> Result<u32> {
        self.with_membership_v4(address, |membership| {
            membership.ok_or(Error::StaleReference)?.word_count()
        })
    }

    pub fn membership_word_count_v6(&self, address: Ipv6Key) -> Result<u32> {
        self.with_membership_v6(address, |membership| {
            membership.ok_or(Error::StaleReference)?.word_count()
        })
    }

    pub fn membership_word_v4(&self, address: Ipv4Key, index: u32) -> Result<Option<u64>> {
        self.with_membership_v4(address, |membership| {
            membership.ok_or(Error::StaleReference)?.word(index)
        })
    }

    pub fn membership_word_v6(&self, address: Ipv6Key, index: u32) -> Result<Option<u64>> {
        self.with_membership_v6(address, |membership| {
            membership.ok_or(Error::StaleReference)?.word(index)
        })
    }

    pub fn membership_words_v4(
        &self,
        address: Ipv4Key,
        start: u32,
        output: &mut [u64],
    ) -> Result<usize> {
        self.with_membership_v4(address, |membership| {
            membership
                .ok_or(Error::StaleReference)?
                .read_words(start, output)
        })
    }

    pub fn membership_words_v6(
        &self,
        address: Ipv6Key,
        start: u32,
        output: &mut [u64],
    ) -> Result<usize> {
        self.with_membership_v6(address, |membership| {
            membership
                .ok_or(Error::StaleReference)?
                .read_words(start, output)
        })
    }

    pub fn membership_contains_v4(&self, address: Ipv4Key, index: u32) -> Result<bool> {
        self.with_membership_v4(address, |membership| {
            membership
                .ok_or(Error::StaleReference)?
                .contains_index(index)
        })
    }

    pub fn membership_contains_v6(&self, address: Ipv6Key, index: u32) -> Result<bool> {
        self.with_membership_v6(address, |membership| {
            membership
                .ok_or(Error::StaleReference)?
                .contains_index(index)
        })
    }

    pub fn read_metadata_json(&self, output: &mut [u8]) -> Result<Option<usize>> {
        self.core()?.read().read_metadata_json(output)
    }

    pub fn close(&mut self) -> Result<()> {
        match &mut self.inner {
            ReaderInner::Immutable(_) => {}
            ReaderInner::Live(reader) => {
                let result = reader.close()?;
                if result.outcome != CloseOutcome::Closed {
                    return Err(result
                        .cause
                        .unwrap_or(Error::CleanupInProgress("live reader close is incomplete")));
                }
            }
            ReaderInner::Closed => return Err(Error::WrongState("reader is closed")),
        }
        self.inner = ReaderInner::Closed;
        Ok(())
    }

    pub(super) fn import_source(&self) -> Result<MembershipImportSource<'_>> {
        match &self.inner {
            ReaderInner::Immutable(reader) => Ok(MembershipImportSource::Immutable(reader)),
            ReaderInner::Live(reader) => Ok(MembershipImportSource::Live(reader)),
            ReaderInner::Closed => Err(Error::WrongState("reader is closed")),
        }
    }

    pub(super) fn history_source(&self) -> Result<crate::HistoryProjectionSource<'_>> {
        match &self.inner {
            ReaderInner::Immutable(reader) => Ok(crate::HistoryProjectionSource::Immutable(reader)),
            ReaderInner::Live(reader) => Ok(crate::HistoryProjectionSource::Live(reader)),
            ReaderInner::Closed => Err(Error::WrongState("reader is closed")),
        }
    }

    fn with_membership_v4<T>(
        &self,
        address: Ipv4Key,
        operation: impl FnOnce(Option<crate::MembershipView<'_>>) -> Result<T>,
    ) -> Result<T> {
        operation(self.core()?.read().lookup_membership_v4(address)?)
    }

    fn with_membership_v6<T>(
        &self,
        address: Ipv6Key,
        operation: impl FnOnce(Option<crate::MembershipView<'_>>) -> Result<T>,
    ) -> Result<T> {
        operation(self.core()?.read().lookup_membership_v6(address)?)
    }

    pub(super) fn core(&self) -> Result<&ReaderCore> {
        match &self.inner {
            ReaderInner::Immutable(reader) => Ok(reader.core()),
            ReaderInner::Live(reader) => reader.core(),
            ReaderInner::Closed => Err(Error::WrongState("reader is closed")),
        }
    }

    fn with_membership_token<T>(
        &self,
        token: MembershipToken,
        operation: impl FnOnce(crate::MembershipView<'_>) -> Result<T>,
    ) -> Result<T> {
        operation(self.core()?.read().membership(token)?)
    }
}
