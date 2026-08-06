//! Allocation-free ordered projection of one named feed.

use std::fmt;
use std::fs::File;

use crate::contract::{MetaV4, ValueKind};
use crate::error::{Error, Result};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::membership_view;
use crate::range_cursor::{CursorState, RangeDirection};
use crate::workflow::AddressRange;

pub(crate) struct ProjectionState<K> {
    meta: MetaV4,
    feed_index: u32,
    direction: RangeDirection,
    inner: CursorState<K>,
    pending: Option<AddressRange<K>>,
    membership: Option<(u32, bool)>,
    raw_finished: bool,
    finished: bool,
}

impl<K: IpKey> ProjectionState<K> {
    pub(crate) fn new(
        file: &File,
        meta: &MetaV4,
        feed_index: u32,
        direction: RangeDirection,
        owner_pid: Option<u32>,
    ) -> Result<Self> {
        require_feed(meta, feed_index)?;
        Ok(Self {
            meta: *meta,
            feed_index,
            direction,
            inner: CursorState::new(file, meta, direction, owner_pid)?,
            pending: None,
            membership: None,
            raw_finished: false,
            finished: false,
        })
    }

    pub(crate) fn seek(&mut self, file: &File, target: K) -> Result<()> {
        self.inner.seek(file, target)?;
        self.pending = None;
        self.raw_finished = false;
        self.finished = false;
        Ok(())
    }

    pub(crate) fn next_with<F>(
        &mut self,
        file: &File,
        checkpoint: &mut F,
    ) -> Result<Option<AddressRange<K>>>
    where
        F: FnMut() -> Result<()>,
    {
        if self.finished {
            return Ok(None);
        }
        match self.next_inner(file, checkpoint) {
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

    fn next_inner<F>(&mut self, file: &File, checkpoint: &mut F) -> Result<Option<AddressRange<K>>>
    where
        F: FnMut() -> Result<()>,
    {
        loop {
            checkpoint()?;
            let Some(current) = self.next_member(file, checkpoint)? else {
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

    fn next_member<F>(&mut self, file: &File, checkpoint: &mut F) -> Result<Option<AddressRange<K>>>
    where
        F: FnMut() -> Result<()>,
    {
        while !self.raw_finished {
            checkpoint()?;
            let Some(range) = self.inner.next(file)? else {
                self.raw_finished = true;
                break;
            };
            let contains = cached_membership(&mut self.membership, range.value, || {
                membership_view::id_contains_index(file, &self.meta, range.value, self.feed_index)
            })?;
            if contains {
                return Ok(Some(AddressRange {
                    from: range.from,
                    to: range.to,
                }));
            }
        }
        Ok(None)
    }
}

fn cached_membership<F>(cache: &mut Option<(u32, bool)>, id: u32, load: F) -> Result<bool>
where
    F: FnOnce() -> Result<bool>,
{
    if let Some((cached_id, contains)) = *cache {
        if cached_id == id {
            return Ok(contains);
        }
    }
    let contains = load()?;
    *cache = Some((id, contains));
    Ok(contains)
}

pub(crate) struct ProjectionCursor<'a, K> {
    file: &'a File,
    state: ProjectionState<K>,
}

impl<'a, K: IpKey> ProjectionCursor<'a, K> {
    pub(crate) fn new(
        file: &'a File,
        meta: &MetaV4,
        feed_index: u32,
        direction: RangeDirection,
        owner_pid: Option<u32>,
    ) -> Result<Self> {
        Ok(Self {
            file,
            state: ProjectionState::new(file, meta, feed_index, direction, owner_pid)?,
        })
    }

    pub(crate) fn next_with<F>(&mut self, checkpoint: &mut F) -> Result<Option<AddressRange<K>>>
    where
        F: FnMut() -> Result<()>,
    {
        self.state.next_with(self.file, checkpoint)
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
        return Err(Error::WrongValueKind(
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
                    .field("direction", &self.inner.state.direction)
                    .field("finished", &self.inner.state.finished)
                    .finish_non_exhaustive()
            }
        }
    };
}

public_cursor!(FeedRangeCursorV4, Ipv4Key);
public_cursor!(FeedRangeCursorV6, Ipv6Key);

#[cfg(test)]
mod tests {
    use super::cached_membership;

    #[test]
    fn consecutive_membership_id_is_resolved_once() {
        let mut cache = None;
        let mut loads = 0;
        assert!(cached_membership(&mut cache, 7, || {
            loads += 1;
            Ok(true)
        })
        .unwrap());
        assert!(cached_membership(&mut cache, 7, || {
            loads += 1;
            Ok(false)
        })
        .unwrap());
        assert!(!cached_membership(&mut cache, 8, || {
            loads += 1;
            Ok(false)
        })
        .unwrap());
        assert_eq!(loads, 2);
    }
}
