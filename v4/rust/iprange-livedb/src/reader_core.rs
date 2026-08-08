//! Authoritative logical reads over one selected healthy generation.

mod cursor;

use std::fs::File;

use crate::bootstrap::Bootstrap;
use crate::contract::{AddressFamily, ValueKind};
use crate::database::DatabaseInfo;
use crate::error::{Error, Result};
use crate::feed::{FeedEntry, FeedName};
use crate::feed_catalog::{self, FeedCursor};
use crate::feed_range_cursor::{FeedRangeCursorV4, FeedRangeCursorV6};
use crate::key::{IpKey, Ipv4Key, Ipv6Key};
use crate::live_namespace::Identity;
use crate::mapping::Mapping;
use crate::membership_view::{self, MembershipView};
use crate::metadata;
use crate::process_identity::ProcessIdentity;
use crate::range_cursor::{DirectCursorV4, DirectCursorV6, RangeDirection};
use crate::range_tree;

pub(crate) use cursor::{MembershipRange, MembershipRangeCursor};
pub use cursor::{MembershipToken, ReaderCursor, ReaderCursorItem};

#[derive(Debug)]
pub(crate) struct ReaderCore {
    mapping: Mapping,
    bootstrap: Bootstrap,
    owner_identity: Option<ProcessIdentity>,
}

impl ReaderCore {
    pub(crate) fn new(
        mapping: Mapping,
        bootstrap: Bootstrap,
        owner_identity: Option<ProcessIdentity>,
    ) -> Self {
        Self {
            mapping,
            bootstrap,
            owner_identity,
        }
    }

    pub(crate) fn info(&self) -> DatabaseInfo {
        let meta = self.bootstrap.meta;
        DatabaseInfo {
            address_family: meta.address_family,
            value_kind: meta.value_kind,
            value_tag: meta.value_tag,
            database_id: meta.database_id,
            transaction_id: meta.txn_id,
            commit_nonce: meta.commit_nonce,
            page_count: meta.page_count,
            range_record_count: meta.range_record_count,
            active_feed_count: meta.active_feed_count,
            meta_selection: self.bootstrap.selection,
        }
    }

    pub(crate) fn file(&self) -> &File {
        self.mapping.file()
    }

    pub(crate) fn file_identity(&self) -> Result<Identity> {
        crate::live_namespace::identity(self.mapping.file())
    }

    pub(crate) fn unmap(&mut self) {
        self.mapping.unmap();
    }

    pub(crate) fn lookup_direct_v4(&self, address: Ipv4Key) -> Result<Option<u32>> {
        self.require_direct(AddressFamily::Ipv4)?;
        range_tree::lookup(&self.mapping, &self.bootstrap.meta, address)
    }

    pub(crate) fn lookup_direct_v6(&self, address: Ipv6Key) -> Result<Option<u32>> {
        self.require_direct(AddressFamily::Ipv6)?;
        range_tree::lookup(&self.mapping, &self.bootstrap.meta, address)
    }

    pub(crate) fn direct_cursor_v4(&self, direction: RangeDirection) -> Result<DirectCursorV4<'_>> {
        self.require_direct(AddressFamily::Ipv4)?;
        DirectCursorV4::new(
            &self.mapping,
            &self.bootstrap.meta,
            direction,
            self.owner_identity,
        )
    }

    pub(crate) fn direct_cursor_v6(&self, direction: RangeDirection) -> Result<DirectCursorV6<'_>> {
        self.require_direct(AddressFamily::Ipv6)?;
        DirectCursorV6::new(
            &self.mapping,
            &self.bootstrap.meta,
            direction,
            self.owner_identity,
        )
    }

    pub(crate) fn lookup_feed(&self, name: &str) -> Result<Option<FeedEntry>> {
        let name = FeedName::new(name)?;
        self.lookup_feed_name(&name)
    }

    pub(crate) fn lookup_feed_name(&self, name: &FeedName) -> Result<Option<FeedEntry>> {
        feed_catalog::require_membership(&self.bootstrap.meta)?;
        feed_catalog::lookup(&self.mapping, &self.bootstrap.meta, name)
    }

    pub(crate) fn feed_cursor(&self) -> Result<FeedCursor<'_>> {
        feed_catalog::require_membership(&self.bootstrap.meta)?;
        FeedCursor::new(&self.mapping, &self.bootstrap.meta, self.owner_identity)
    }

    pub(crate) fn feed_range_cursor_v4(
        &self,
        name: &str,
        direction: RangeDirection,
    ) -> Result<FeedRangeCursorV4<'_>> {
        self.require_membership_family(AddressFamily::Ipv4)?;
        let feed = self.require_feed(name)?;
        FeedRangeCursorV4::new(
            &self.mapping,
            &self.bootstrap.meta,
            feed.index,
            direction,
            self.owner_identity,
        )
    }

    pub(crate) fn feed_range_cursor_v6(
        &self,
        name: &str,
        direction: RangeDirection,
    ) -> Result<FeedRangeCursorV6<'_>> {
        self.require_membership_family(AddressFamily::Ipv6)?;
        let feed = self.require_feed(name)?;
        FeedRangeCursorV6::new(
            &self.mapping,
            &self.bootstrap.meta,
            feed.index,
            direction,
            self.owner_identity,
        )
    }

    pub(crate) fn lookup_membership_v4(
        &self,
        address: Ipv4Key,
    ) -> Result<Option<MembershipView<'_>>> {
        membership_view::lookup_v4(
            &self.mapping,
            &self.bootstrap.meta,
            address,
            self.owner_identity,
        )
    }

    pub(crate) fn lookup_membership_v6(
        &self,
        address: Ipv6Key,
    ) -> Result<Option<MembershipView<'_>>> {
        membership_view::lookup_v6(
            &self.mapping,
            &self.bootstrap.meta,
            address,
            self.owner_identity,
        )
    }

    pub(crate) fn membership_ranges<K: IpKey>(&self) -> Result<MembershipRangeCursor<'_, K>> {
        self.require_membership_family(K::FAMILY)?;
        MembershipRangeCursor::new(&self.mapping, &self.bootstrap.meta, self.owner_identity)
    }

    pub(crate) fn membership(&self, token: MembershipToken) -> Result<MembershipView<'_>> {
        membership_view::by_id(
            &self.mapping,
            &self.bootstrap.meta,
            token.id(),
            self.owner_identity,
        )
    }

    pub(crate) fn metadata_json_len(&self) -> Option<u64> {
        (self.bootstrap.meta.metadata_root != 0)
            .then_some(self.bootstrap.meta.metadata_uncompressed_len)
    }

    pub(crate) fn membership_entry_count(&self) -> u64 {
        self.bootstrap.meta.membership_entry_count
    }

    pub(crate) fn read_metadata_json(&self, output: &mut [u8]) -> Result<Option<usize>> {
        metadata::read(&self.mapping, &self.bootstrap.meta, output)
    }

    pub(crate) fn metadata_json(&self) -> Result<Option<Vec<u8>>> {
        metadata::read_vec(&self.mapping, &self.bootstrap.meta)
    }

    pub(crate) fn open_direct_state(&self, direction: RangeDirection) -> Result<ReaderCursor> {
        ReaderCursor::direct(
            &self.mapping,
            &self.bootstrap.meta,
            direction,
            self.owner_identity,
        )
    }

    pub(crate) fn open_membership_state(&self, direction: RangeDirection) -> Result<ReaderCursor> {
        ReaderCursor::membership(
            &self.mapping,
            &self.bootstrap.meta,
            direction,
            self.owner_identity,
        )
    }

    pub(crate) fn open_feed_state(
        &self,
        name: &str,
        direction: RangeDirection,
    ) -> Result<ReaderCursor> {
        let feed = self.require_feed(name)?;
        ReaderCursor::feed(
            &self.mapping,
            &self.bootstrap.meta,
            feed.index,
            direction,
            self.owner_identity,
        )
    }

    pub(crate) fn cursor_next(
        &self,
        cursor: &mut ReaderCursor,
    ) -> Result<Option<ReaderCursorItem>> {
        cursor.next(&self.mapping)
    }

    pub(crate) fn cursor_seek_v4(&self, cursor: &mut ReaderCursor, target: Ipv4Key) -> Result<()> {
        cursor.seek_v4(&self.mapping, target)
    }

    pub(crate) fn cursor_seek_v6(&self, cursor: &mut ReaderCursor, target: Ipv6Key) -> Result<()> {
        cursor.seek_v6(&self.mapping, target)
    }

    pub(crate) fn membership_token_v4(&self, address: Ipv4Key) -> Result<Option<MembershipToken>> {
        Ok(self
            .lookup_membership_v4(address)?
            .map(|view| MembershipToken::new(view.id())))
    }

    pub(crate) fn membership_token_v6(&self, address: Ipv6Key) -> Result<Option<MembershipToken>> {
        Ok(self
            .lookup_membership_v6(address)?
            .map(|view| MembershipToken::new(view.id())))
    }

    fn require_direct(&self, family: AddressFamily) -> Result<()> {
        if self.bootstrap.meta.value_kind != ValueKind::Direct {
            return Err(Error::WrongValueKind(
                "direct lookup requires a direct-value database",
            ));
        }
        if self.bootstrap.meta.address_family != family {
            return Err(Error::WrongAddressFamily(
                "lookup address family does not match the database",
            ));
        }
        Ok(())
    }

    fn require_membership_family(&self, family: AddressFamily) -> Result<()> {
        feed_catalog::require_membership(&self.bootstrap.meta)?;
        if self.bootstrap.meta.address_family != family {
            return Err(Error::WrongAddressFamily(
                "feed cursor address family does not match the database",
            ));
        }
        Ok(())
    }

    fn require_feed(&self, name: &str) -> Result<FeedEntry> {
        let name = FeedName::new(name)?;
        self.lookup_feed_name(&name)?.ok_or(Error::NameNotFound)
    }
}
