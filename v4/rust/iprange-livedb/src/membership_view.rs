//! Lazy SDK-owned membership values.

use crate::blob_tree;
use crate::contract::{AddressFamily, MetaV4, ValueKind};
use crate::error::{Error, Result};
use crate::key::{Ipv4Key, Ipv6Key};
use crate::mapping::Mapping;
use crate::membership_tree::{self, Record, Storage};
use crate::range_tree;
use std::fmt;

/// One lazy membership bitmap tied to a pinned reader generation.
pub struct MembershipView<'a> {
    mapping: &'a Mapping,
    meta: MetaV4,
    record: Record,
    owner_pid: Option<u32>,
}

impl MembershipView<'_> {
    pub(crate) const fn id(&self) -> u32 {
        self.record.id
    }

    /// Number of canonical little-endian `u64` bitmap words.
    pub fn word_count(&self) -> Result<u32> {
        self.require_owner()?;
        Ok(self.record.word_count)
    }

    /// Read one word, or `None` when the index is beyond the canonical bitmap.
    pub fn word(&self, index: u32) -> Result<Option<u64>> {
        self.require_owner()?;
        if index >= self.record.word_count {
            return Ok(None);
        }
        let mut output = [0];
        self.read_words_inner(index, &mut output)?;
        Ok(Some(output[0]))
    }

    /// Fill as many sequential words as remain, returning the copied count.
    pub fn read_words(&self, start: u32, output: &mut [u64]) -> Result<usize> {
        self.require_owner()?;
        if start > self.record.word_count {
            return Err(Error::InvalidArgument(
                "membership word start exceeds its length",
            ));
        }
        let remaining = (self.record.word_count - start) as usize;
        let count = remaining.min(output.len());
        self.read_words_inner(start, &mut output[..count])?;
        Ok(count)
    }

    /// Test one observable feed index from the same pinned generation.
    pub fn contains_index(&self, feed_index: u32) -> Result<bool> {
        self.require_owner()?;
        if u64::from(feed_index) >= self.meta.feed_index_limit {
            return Err(Error::InvalidArgument(
                "feed index exceeds this catalog generation",
            ));
        }
        let word_index = feed_index / 64;
        let Some(word) = self.word(word_index)? else {
            return Ok(false);
        };
        Ok(word & (1u64 << (feed_index % 64)) != 0)
    }

    fn read_words_inner(&self, start: u32, output: &mut [u64]) -> Result<()> {
        if output.is_empty() {
            return Ok(());
        }
        match self.record.storage {
            Storage::Inline => membership_tree::read_inline_words(
                self.mapping,
                &self.meta,
                self.record,
                start,
                output,
            ),
            Storage::Blob(root) => blob_tree::read_words(
                self.mapping,
                &self.meta,
                root,
                self.record.word_count,
                start,
                output,
            ),
        }?;
        if start as usize + output.len() == self.record.word_count as usize
            && output.last() == Some(&0)
        {
            return Err(Error::Corrupt("membership bitmap has a trailing zero word"));
        }
        Ok(())
    }

    fn require_owner(&self) -> Result<()> {
        if self.owner_pid.is_some_and(|pid| pid != std::process::id()) {
            return Err(Error::ForkedHandle);
        }
        Ok(())
    }
}

impl fmt::Debug for MembershipView<'_> {
    fn fmt(&self, output: &mut fmt::Formatter<'_>) -> fmt::Result {
        output
            .debug_struct("MembershipView")
            .field("word_count", &self.record.word_count)
            .finish_non_exhaustive()
    }
}

pub(crate) fn lookup_v4<'a>(
    mapping: &'a Mapping,
    meta: &MetaV4,
    address: Ipv4Key,
    owner_pid: Option<u32>,
) -> Result<Option<MembershipView<'a>>> {
    require_kind(meta, AddressFamily::Ipv4)?;
    lookup(
        mapping,
        meta,
        range_tree::lookup(mapping, meta, address)?,
        owner_pid,
    )
}

pub(crate) fn lookup_v6<'a>(
    mapping: &'a Mapping,
    meta: &MetaV4,
    address: Ipv6Key,
    owner_pid: Option<u32>,
) -> Result<Option<MembershipView<'a>>> {
    require_kind(meta, AddressFamily::Ipv6)?;
    lookup(
        mapping,
        meta,
        range_tree::lookup(mapping, meta, address)?,
        owner_pid,
    )
}

pub(crate) fn id_contains_index(
    mapping: &Mapping,
    meta: &MetaV4,
    id: u32,
    feed_index: u32,
) -> Result<bool> {
    let view = lookup(mapping, meta, Some(id), None)?
        .ok_or(Error::Corrupt("range names an absent membership"))?;
    view.contains_index(feed_index)
}

pub(crate) fn by_id<'a>(
    mapping: &'a Mapping,
    meta: &MetaV4,
    id: u32,
    owner_pid: Option<u32>,
) -> Result<MembershipView<'a>> {
    if id == 0 {
        return Err(Error::Corrupt("range names the empty membership ID"));
    }
    lookup(mapping, meta, Some(id), owner_pid)?
        .ok_or(Error::Corrupt("range names an absent membership ID"))
}

fn lookup<'a>(
    mapping: &'a Mapping,
    meta: &MetaV4,
    id: Option<u32>,
    owner_pid: Option<u32>,
) -> Result<Option<MembershipView<'a>>> {
    let Some(id) = id else {
        return Ok(None);
    };
    let record = membership_tree::find(mapping, meta, id)?
        .ok_or(Error::Corrupt("range names an absent membership ID"))?;
    Ok(Some(MembershipView {
        mapping,
        meta: *meta,
        record,
        owner_pid,
    }))
}

fn require_kind(meta: &MetaV4, family: AddressFamily) -> Result<()> {
    if meta.value_kind != ValueKind::Membership {
        return Err(Error::WrongValueKind(
            "membership lookup requires a membership database",
        ));
    }
    if meta.address_family != family {
        return Err(Error::WrongAddressFamily(
            "lookup address family does not match the database",
        ));
    }
    Ok(())
}

#[cfg(test)]
#[path = "membership_view_tests.rs"]
mod tests;
