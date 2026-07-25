//! Allocation-free ordered projection of one named feed.

use std::fmt;
use std::fs::File;

use crate::contract::{MetaV4, ValueKind};
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::membership_view;
use crate::range_cursor::{Cursor, RangeDirection};
use crate::workflow::AddressRange;

pub(crate) struct ProjectionCursor<'a, K> {
    file: &'a File,
    meta: MetaV4,
    feed_index: u32,
    direction: RangeDirection,
    inner: Cursor<'a, K>,
    pending: Option<AddressRange<K>>,
    raw_finished: bool,
    finished: bool,
}

impl<'a, K: IpKey> ProjectionCursor<'a, K> {
    pub(crate) fn new(
        file: &'a File,
        meta: &MetaV4,
        feed_index: u32,
        direction: RangeDirection,
        owner_pid: Option<u32>,
    ) -> Result<Self> {
        require_feed(meta, feed_index)?;
        Ok(Self {
            file,
            meta: *meta,
            feed_index,
            direction,
            inner: Cursor::new(file, meta, direction, owner_pid)?,
            pending: None,
            raw_finished: false,
            finished: false,
        })
    }

    pub(crate) fn next_with<F>(&mut self, checkpoint: &mut F) -> Result<Option<AddressRange<K>>>
    where
        F: FnMut() -> Result<()>,
    {
        if self.finished {
            return Ok(None);
        }
        match self.next_inner(checkpoint) {
            Ok(next) => {
                self.finished = next.is_none()
                    || (self.raw_finished && self.pending.is_none() && next.is_some());
                Ok(next)
            }
            Err(error) => {
                self.finished = true;
                Err(error)
            }
        }
    }

    fn next_inner<F>(&mut self, checkpoint: &mut F) -> Result<Option<AddressRange<K>>>
    where
        F: FnMut() -> Result<()>,
    {
        loop {
            checkpoint()?;
            let Some(current) = self.next_member(checkpoint)? else {
                return Ok(self.pending.take());
            };
            let Some(pending) = self.pending else {
                self.pending = Some(current);
                continue;
            };
            if let Some(merged) = merge(self.direction, pending, current) {
                self.pending = Some(merged);
                continue;
            }
            self.pending = Some(current);
            return Ok(Some(pending));
        }
    }

    fn next_member<F>(&mut self, checkpoint: &mut F) -> Result<Option<AddressRange<K>>>
    where
        F: FnMut() -> Result<()>,
    {
        while !self.raw_finished {
            checkpoint()?;
            let Some(range) = self.inner.next()? else {
                self.raw_finished = true;
                break;
            };
            if membership_view::id_contains_index(
                self.file,
                &self.meta,
                range.value,
                self.feed_index,
            )? {
                return Ok(Some(AddressRange {
                    from: range.from,
                    to: range.to,
                }));
            }
        }
        Ok(None)
    }
}

fn merge<K: IpKey>(
    direction: RangeDirection,
    mut pending: AddressRange<K>,
    current: AddressRange<K>,
) -> Option<AddressRange<K>> {
    let adjacent = match direction {
        RangeDirection::Forward => pending.to.checked_next() == Some(current.from),
        RangeDirection::Backward => current.to.checked_next() == Some(pending.from),
    };
    if !adjacent {
        return None;
    }
    match direction {
        RangeDirection::Forward => pending.to = current.to,
        RangeDirection::Backward => pending.from = current.from,
    }
    Some(pending)
}

fn require_feed(meta: &MetaV4, feed_index: u32) -> Result<()> {
    if meta.value_kind != ValueKind::Membership {
        return Err(Error::WrongMode(
            "named-feed cursor requires a membership database",
        ));
    }
    if u64::from(feed_index) >= meta.feed_index_limit {
        return Err(Error::Corrupt("feed index exceeds the catalog namespace"));
    }
    Ok(())
}

macro_rules! public_cursor {
    ($name:ident, $key:ty) => {
        pub struct $name<'a> {
            inner: ProjectionCursor<'a, $key>,
        }

        impl<'a> $name<'a> {
            pub(crate) fn new(
                file: &'a File,
                meta: &MetaV4,
                feed_index: u32,
                direction: RangeDirection,
            ) -> Result<Self> {
                Ok(Self {
                    inner: ProjectionCursor::new(file, meta, feed_index, direction, None)?,
                })
            }

            pub(crate) fn new_live(
                file: &'a File,
                meta: &MetaV4,
                feed_index: u32,
                direction: RangeDirection,
                owner_pid: u32,
            ) -> Result<Self> {
                Ok(Self {
                    inner: ProjectionCursor::new(
                        file,
                        meta,
                        feed_index,
                        direction,
                        Some(owner_pid),
                    )?,
                })
            }

            /// Return the next coalesced interval belonging to this feed.
            pub fn next_range(&mut self) -> Result<Option<AddressRange<$key>>> {
                self.inner.next_with(&mut || Ok(()))
            }
        }

        impl fmt::Debug for $name<'_> {
            fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
                output
                    .debug_struct(stringify!($name))
                    .field("direction", &self.inner.direction)
                    .field("finished", &self.inner.finished)
                    .finish_non_exhaustive()
            }
        }
    };
}

public_cursor!(FeedRangeCursorV4, Ipv4Key);
public_cursor!(FeedRangeCursorV6, Ipv6Key);
