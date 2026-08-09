//! Allocation-free ordered range cursors.

use std::fmt;

use crate::contract::MetaV4;
use crate::error::{Error, Result};
use crate::fixed_tree::{self, CursorDirection, CursorItem, CursorSeek, SeekPosition};
use crate::format::Generation;
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::mapping::{ByteSource, Mapping};
use crate::process_identity::ProcessIdentity;
use crate::range_tree::{self, Header, RangeCodec};

/// Direction of ordered cursor movement.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum RangeDirection {
    Forward,
    Backward,
}

/// One inclusive direct-value interval.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct DirectRange<K> {
    pub from: K,
    pub to: K,
    pub value: u32,
}

pub(crate) struct CursorState<K> {
    meta: MetaV4,
    pub(crate) direction: RangeDirection,
    inner: fixed_tree::Cursor<RangeCodec<K>>,
    owner_identity: Option<ProcessIdentity>,
}

impl<K: IpKey> CursorState<K> {
    pub(crate) fn new(
        mapping: &Mapping,
        meta: &MetaV4,
        direction: RangeDirection,
        owner_identity: Option<ProcessIdentity>,
    ) -> Result<Self> {
        if meta.address_family != K::FAMILY {
            return Err(Error::WrongAddressFamily(
                "range cursor address family does not match the database",
            ));
        }
        let source = Generation::new(mapping, *meta);
        Ok(Self {
            meta: *meta,
            direction,
            inner: fixed_tree::Cursor::new(
                &source,
                meta.range_root,
                physical_direction(direction),
            )?,
            owner_identity,
        })
    }

    pub(crate) fn seek(&mut self, mapping: &Mapping, target: K) -> Result<()> {
        self.require_owner()?;
        self.inner.seek(
            &Generation::new(mapping, self.meta),
            target,
            &mut RangeSeek { target },
        )
    }

    pub(crate) fn next(&mut self, mapping: &Mapping) -> Result<Option<DirectRange<K>>> {
        self.require_owner()?;
        let next = self
            .inner
            .next_leaf_mapped(mapping)?
            .map(|record| DirectRange {
                from: record.from,
                to: record.to,
                value: record.value,
            });
        crate::work::range_consumed(u64::from(next.is_some()));
        Ok(next)
    }

    fn require_owner(&self) -> Result<()> {
        if self.owner_identity.is_some_and(|owner| !owner.is_current()) {
            return Err(Error::ForkedHandle);
        }
        Ok(())
    }

    pub(crate) fn finished(&self) -> bool {
        self.inner.finished()
    }
}

pub(crate) struct RangeItem;

impl<K: IpKey> CursorItem<RangeCodec<K>> for RangeItem {
    type Output = DirectRange<K>;

    fn read<S: ByteSource>(
        &mut self,
        page: S,
        header: &Header,
        _page_number: u32,
        index: usize,
    ) -> Result<Self::Output> {
        let record = range_tree::leaf_record::<K, _>(page, header, index)?;
        Ok(DirectRange {
            from: record.from,
            to: record.to,
            value: record.value,
        })
    }
}

struct RangeSeek<K> {
    target: K,
}

impl<K: IpKey> CursorSeek<RangeCodec<K>> for RangeSeek<K> {
    fn select<S: ByteSource>(
        &mut self,
        page: S,
        header: &Header,
        position: usize,
        exact: bool,
        direction: CursorDirection,
    ) -> Result<SeekPosition> {
        let previous = if exact {
            Some(position)
        } else {
            position.checked_sub(1)
        };
        match (direction, previous) {
            (CursorDirection::Backward, Some(index)) => Ok(SeekPosition::Index(index)),
            (CursorDirection::Backward, None) => Ok(SeekPosition::Finished),
            (CursorDirection::Forward, None) => Ok(SeekPosition::Index(0)),
            (CursorDirection::Forward, Some(index)) => {
                let record = range_tree::leaf_record::<K, _>(page, header, index)?;
                if self.target <= record.to {
                    Ok(SeekPosition::Index(index))
                } else if index + 1 < header.item_count {
                    Ok(SeekPosition::Index(index + 1))
                } else {
                    Ok(SeekPosition::NextLeaf)
                }
            }
        }
    }
}

const fn physical_direction(direction: RangeDirection) -> CursorDirection {
    match direction {
        RangeDirection::Forward => CursorDirection::Forward,
        RangeDirection::Backward => CursorDirection::Backward,
    }
}

pub(crate) struct Cursor<'a, K> {
    mapping: &'a Mapping,
    state: CursorState<K>,
}

impl<'a, K: IpKey> Cursor<'a, K> {
    pub(crate) fn new(
        mapping: &'a Mapping,
        meta: &MetaV4,
        direction: RangeDirection,
        owner_identity: Option<ProcessIdentity>,
    ) -> Result<Self> {
        Ok(Self {
            mapping,
            state: CursorState::new(mapping, meta, direction, owner_identity)?,
        })
    }

    pub(crate) fn seek(&mut self, target: K) -> Result<()> {
        self.state.seek(self.mapping, target)
    }

    pub(crate) fn next(&mut self) -> Result<Option<DirectRange<K>>> {
        self.state.next(self.mapping)
    }
}

macro_rules! public_cursor {
    ($name:ident, $key:ty) => {
        pub struct $name<'a> {
            inner: Cursor<'a, $key>,
        }

        impl<'a> $name<'a> {
            pub(crate) fn new(
                mapping: &'a Mapping,
                meta: &MetaV4,
                direction: RangeDirection,
                owner_identity: Option<ProcessIdentity>,
            ) -> Result<Self> {
                Ok(Self {
                    inner: Cursor::new(mapping, meta, direction, owner_identity)?,
                })
            }

            /// Reposition to a containing range or the nearest range in this direction.
            pub fn seek(&mut self, target: $key) -> Result<()> {
                self.inner.seek(target)
            }

            /// Return the next range in the cursor's selected direction.
            pub fn next_range(&mut self) -> Result<Option<DirectRange<$key>>> {
                self.inner.next()
            }
        }

        impl fmt::Debug for $name<'_> {
            fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
                output
                    .debug_struct(stringify!($name))
                    .field("direction", &self.inner.state.direction)
                    .field("finished", &self.inner.state.finished())
                    .finish_non_exhaustive()
            }
        }
    };
}

public_cursor!(DirectCursorV4, Ipv4Key);
public_cursor!(DirectCursorV6, Ipv6Key);
