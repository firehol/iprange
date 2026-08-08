//! Opaque cursor and membership state for borrow-free bindings and workflows.

use std::fmt;

use crate::contract::{AddressFamily, MetaV4, ValueKind};
use crate::error::{Error, Result};
use crate::feed_range_cursor::ProjectionState;
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;
use crate::process_identity::ProcessIdentity;
use crate::range_cursor::{Cursor, CursorState, DirectRange, RangeDirection};
use crate::workflow::AddressRange;

/// Opaque membership identity retained only inside an SDK-owned handle.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct MembershipToken(u32);

impl MembershipToken {
    pub(super) const fn new(id: u32) -> Self {
        Self(id)
    }

    pub(super) const fn id(self) -> u32 {
        self.0
    }

    pub(crate) const fn cache_key(self) -> u32 {
        self.0
    }
}

/// Borrow-free reader cursor state retained by a C child handle.
pub struct ReaderCursor {
    inner: ReaderCursorInner,
}

enum ReaderCursorInner {
    DirectV4(CursorState<Ipv4Key>),
    DirectV6(CursorState<Ipv6Key>),
    MembershipV4(CursorState<Ipv4Key>),
    MembershipV6(CursorState<Ipv6Key>),
    FeedV4(ProjectionState<Ipv4Key>),
    FeedV6(ProjectionState<Ipv6Key>),
}

/// One logical item returned by a binding reader cursor.
#[derive(Clone, Copy, Debug)]
pub enum ReaderCursorItem {
    DirectV4(DirectRange<Ipv4Key>),
    DirectV6(DirectRange<Ipv6Key>),
    MembershipV4 {
        range: AddressRange<Ipv4Key>,
        membership: MembershipToken,
    },
    MembershipV6 {
        range: AddressRange<Ipv6Key>,
        membership: MembershipToken,
    },
    FeedV4(AddressRange<Ipv4Key>),
    FeedV6(AddressRange<Ipv6Key>),
}

impl fmt::Debug for ReaderCursor {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output.write_str("ReaderCursor { .. }")
    }
}

impl ReaderCursor {
    pub(super) fn direct(
        mapping: &Mapping,
        meta: &MetaV4,
        direction: RangeDirection,
        owner_identity: Option<ProcessIdentity>,
    ) -> Result<Self> {
        if meta.value_kind != ValueKind::Direct {
            return Err(Error::WrongValueKind(
                "direct cursor requires a direct-value database",
            ));
        }
        let inner = match meta.address_family {
            AddressFamily::Ipv4 => ReaderCursorInner::DirectV4(CursorState::new(
                mapping,
                meta,
                direction,
                owner_identity,
            )?),
            AddressFamily::Ipv6 => ReaderCursorInner::DirectV6(CursorState::new(
                mapping,
                meta,
                direction,
                owner_identity,
            )?),
        };
        Ok(Self { inner })
    }

    pub(super) fn membership(
        mapping: &Mapping,
        meta: &MetaV4,
        direction: RangeDirection,
        owner_identity: Option<ProcessIdentity>,
    ) -> Result<Self> {
        if meta.value_kind != ValueKind::Membership {
            return Err(Error::WrongValueKind(
                "membership cursor requires a membership database",
            ));
        }
        let inner = match meta.address_family {
            AddressFamily::Ipv4 => ReaderCursorInner::MembershipV4(CursorState::new(
                mapping,
                meta,
                direction,
                owner_identity,
            )?),
            AddressFamily::Ipv6 => ReaderCursorInner::MembershipV6(CursorState::new(
                mapping,
                meta,
                direction,
                owner_identity,
            )?),
        };
        Ok(Self { inner })
    }

    pub(super) fn feed(
        mapping: &Mapping,
        meta: &MetaV4,
        feed_index: u32,
        direction: RangeDirection,
        owner_identity: Option<ProcessIdentity>,
    ) -> Result<Self> {
        let inner = match meta.address_family {
            AddressFamily::Ipv4 => ReaderCursorInner::FeedV4(ProjectionState::new(
                mapping,
                meta,
                feed_index,
                direction,
                owner_identity,
            )?),
            AddressFamily::Ipv6 => ReaderCursorInner::FeedV6(ProjectionState::new(
                mapping,
                meta,
                feed_index,
                direction,
                owner_identity,
            )?),
        };
        Ok(Self { inner })
    }

    pub(super) fn next(&mut self, mapping: &Mapping) -> Result<Option<ReaderCursorItem>> {
        Ok(match &mut self.inner {
            ReaderCursorInner::DirectV4(cursor) => {
                cursor.next(mapping)?.map(ReaderCursorItem::DirectV4)
            }
            ReaderCursorInner::DirectV6(cursor) => {
                cursor.next(mapping)?.map(ReaderCursorItem::DirectV6)
            }
            ReaderCursorInner::MembershipV4(cursor) => {
                cursor
                    .next(mapping)?
                    .map(|range| ReaderCursorItem::MembershipV4 {
                        range: AddressRange {
                            from: range.from,
                            to: range.to,
                        },
                        membership: MembershipToken::new(range.value),
                    })
            }
            ReaderCursorInner::MembershipV6(cursor) => {
                cursor
                    .next(mapping)?
                    .map(|range| ReaderCursorItem::MembershipV6 {
                        range: AddressRange {
                            from: range.from,
                            to: range.to,
                        },
                        membership: MembershipToken::new(range.value),
                    })
            }
            ReaderCursorInner::FeedV4(cursor) => cursor
                .next_with(mapping, &mut || Ok(()))?
                .map(ReaderCursorItem::FeedV4),
            ReaderCursorInner::FeedV6(cursor) => cursor
                .next_with(mapping, &mut || Ok(()))?
                .map(ReaderCursorItem::FeedV6),
        })
    }

    pub(super) fn seek_v4(&mut self, mapping: &Mapping, target: Ipv4Key) -> Result<()> {
        match &mut self.inner {
            ReaderCursorInner::DirectV4(cursor) | ReaderCursorInner::MembershipV4(cursor) => {
                cursor.seek(mapping, target)
            }
            ReaderCursorInner::FeedV4(cursor) => cursor.seek(mapping, target),
            _ => Err(Error::WrongAddressFamily(
                "cursor address family does not match the bound",
            )),
        }
    }

    pub(super) fn seek_v6(&mut self, mapping: &Mapping, target: Ipv6Key) -> Result<()> {
        match &mut self.inner {
            ReaderCursorInner::DirectV6(cursor) | ReaderCursorInner::MembershipV6(cursor) => {
                cursor.seek(mapping, target)
            }
            ReaderCursorInner::FeedV6(cursor) => cursor.seek(mapping, target),
            _ => Err(Error::WrongAddressFamily(
                "cursor address family does not match the bound",
            )),
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct MembershipRange<K> {
    pub(crate) from: K,
    pub(crate) to: K,
    pub(crate) membership: MembershipToken,
}

pub(crate) struct MembershipRangeCursor<'a, K> {
    inner: Cursor<'a, K>,
}

impl<'a, K: IpKey> MembershipRangeCursor<'a, K> {
    pub(super) fn new(
        mapping: &'a Mapping,
        meta: &MetaV4,
        owner_identity: Option<ProcessIdentity>,
    ) -> Result<Self> {
        Ok(Self {
            inner: Cursor::new(mapping, meta, RangeDirection::Forward, owner_identity)?,
        })
    }

    pub(crate) fn next(&mut self) -> Result<Option<MembershipRange<K>>> {
        Ok(self.inner.next()?.map(|range| MembershipRange {
            from: range.from,
            to: range.to,
            membership: MembershipToken::new(range.value),
        }))
    }
}
