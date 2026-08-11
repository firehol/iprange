//! Allocation-free ordered cursors over typed structured ranges.

use std::fmt;

use crate::contract::MetaV4;
use crate::error::Result;
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;
use crate::process_identity::ProcessIdentity;
use crate::range_cursor::{CursorState, RangeDirection};

use super::view::{by_id, require_kind};
use super::NetworkEnrichmentV1View;

/// One inclusive typed enrichment interval.
pub struct NetworkEnrichmentV1Range<'a, K> {
    pub from: K,
    pub to: K,
    pub value: NetworkEnrichmentV1View<'a>,
}

impl<K: fmt::Debug> fmt::Debug for NetworkEnrichmentV1Range<'_, K> {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output
            .debug_struct("NetworkEnrichmentV1Range")
            .field("from", &self.from)
            .field("to", &self.to)
            .field("value", &self.value)
            .finish()
    }
}

struct Cursor<'a, K> {
    mapping: &'a Mapping,
    meta: MetaV4,
    owner_identity: Option<ProcessIdentity>,
    inner: CursorState<K>,
}

impl<'a, K: IpKey> Cursor<'a, K> {
    fn new(
        mapping: &'a Mapping,
        meta: &MetaV4,
        direction: RangeDirection,
        owner_identity: Option<ProcessIdentity>,
    ) -> Result<Self> {
        require_kind(meta, K::FAMILY)?;
        Ok(Self {
            mapping,
            meta: *meta,
            owner_identity,
            inner: CursorState::new(mapping, meta, direction, owner_identity)?,
        })
    }

    fn seek(&mut self, target: K) -> Result<()> {
        self.inner.seek(self.mapping, target)
    }

    fn next(&mut self) -> Result<Option<NetworkEnrichmentV1Range<'a, K>>> {
        let Some(range) = self.inner.next(self.mapping)? else {
            return Ok(None);
        };
        Ok(Some(NetworkEnrichmentV1Range {
            from: range.from,
            to: range.to,
            value: by_id(self.mapping, &self.meta, range.value, self.owner_identity)?,
        }))
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

            /// Return the next typed range in the selected direction.
            pub fn next_range(&mut self) -> Result<Option<NetworkEnrichmentV1Range<'a, $key>>> {
                self.inner.next()
            }
        }

        impl fmt::Debug for $name<'_> {
            fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
                output
                    .debug_struct(stringify!($name))
                    .field("direction", &self.inner.inner.direction)
                    .field("finished", &self.inner.inner.finished())
                    .finish_non_exhaustive()
            }
        }
    };
}

public_cursor!(NetworkEnrichmentV1CursorV4, Ipv4Key);
public_cursor!(NetworkEnrichmentV1CursorV6, Ipv6Key);
