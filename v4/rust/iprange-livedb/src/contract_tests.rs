use crate::slotted_page::{put_u16, put_u32, put_u64};

use super::*;

impl MetaV4 {
    pub(crate) fn encode_into(&self, page: &mut [u8; PAGE_SIZE]) {
        page.fill(0);
        page[0..8].copy_from_slice(&META_MAGIC);
        put_u16(page, 8, META_SIZE);
        page[10] = PAGE_SHIFT;
        page[11] = self.address_family as u8;
        page[12] = self.value_kind as u8;
        page[16..32].copy_from_slice(self.value_tag.as_wire());
        page[32..48].copy_from_slice(&self.database_id);
        put_u64(page, 48, self.txn_id);
        page[56..72].copy_from_slice(&self.commit_nonce);
        put_u64(page, 72, self.page_count);
        put_u64(page, 80, self.range_record_count);
        put_u64(page, 88, self.active_feed_count);
        put_u64(page, 96, self.feed_index_limit);
        put_u64(page, 104, self.membership_entry_count);
        put_u64(page, 112, self.membership_id_limit);
        put_u64(page, 120, self.metadata_uncompressed_len);
        put_u64(page, 128, self.metadata_compressed_len);
        put_u64(page, 136, self.retired_extent_count);
        put_u32(page, 144, self.range_root);
        put_u32(page, 148, self.catalog_name_root);
        put_u32(page, 152, self.catalog_index_root);
        put_u32(page, 156, self.feed_used_root);
        put_u32(page, 160, self.membership_id_root);
        put_u32(page, 164, self.membership_hash_root);
        put_u32(page, 168, self.membership_used_root);
        put_u32(page, 172, self.metadata_root);
        put_u32(page, 176, self.free_bitmap_root);
        put_u32(page, 180, self.retirement_root);
        for (index, page_number) in self.allocator_reserve.iter().enumerate() {
            put_u32(page, 184 + index * 4, *page_number);
        }
        let crc = crc32c::crc32c_with_zeroed(page, META_CRC_OFFSET, 4).unwrap();
        put_u32(page, META_CRC_OFFSET, crc);
    }
}

#[test]
fn tag_is_canonical_and_retention_is_exact() {
    assert_eq!(ValueTag::RETENTION.bytes(), b"retention");
    assert!(ValueTag::new(b"123456789012345").is_some());
    assert!(ValueTag::new(b"1234567890123456").is_none());
    assert!(ValueTag::new(b"bad\0tag").is_none());
    assert!(ValueTag::from_wire(*b"bad\0x\0\0\0\0\0\0\0\0\0\0\0").is_none());
}

#[test]
fn exact_meta_offsets_and_crc() {
    let meta = super::super::bootstrap::tests::empty_direct_meta(7);
    let mut page = [0u8; PAGE_SIZE];
    meta.encode_into(&mut page);
    assert_eq!(&page[0..8], b"IPRANGE4");
    assert_eq!(u16_le(&page, 8), 256);
    assert_eq!(page[10], 12);
    assert_eq!(page[11], 4);
    assert_eq!(page[12], 1);
    assert_eq!(u64_le(&page, 48), 7);
    assert_eq!(u64_le(&page, 72), 2);
    assert_eq!(
        u32_le(&page, META_CRC_OFFSET),
        crc32c::crc32c_with_zeroed(&page, META_CRC_OFFSET, 4).unwrap()
    );
    assert!(page[256..].iter().all(|&byte| byte == 0));
}
