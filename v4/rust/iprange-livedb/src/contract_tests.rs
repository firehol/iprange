use crate::slotted_page::PageSink;

use super::*;

impl MetaV4 {
    pub(crate) fn encode_into(&self, page: &mut [u8; PAGE_SIZE]) {
        self.encode_fields(page).unwrap();
        let crc = crc32c::crc32c_with_zeroed(page, META_CRC_OFFSET, 4).unwrap();
        page.put_u32(META_CRC_OFFSET, crc).unwrap();
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
fn big_endian_portable_meta_matches_literal_bytes() {
    let mut meta = super::super::bootstrap::tests::empty_direct_meta(0x0807_0605_0403_0201);
    meta.page_count = 0x4433_2211;
    meta.range_record_count = 1;
    meta.range_root = 0x0403_0201;
    let mut page = [0u8; PAGE_SIZE];
    meta.encode_into(&mut page);
    assert_eq!(&page[0..8], b"IPRANGE4");
    assert_eq!(&page[8..10], &[0x00, 0x01]);
    assert_eq!(page[10], 12);
    assert_eq!(page[11], 4);
    assert_eq!(page[12], 1);
    assert_eq!(&page[48..56], &[1, 2, 3, 4, 5, 6, 7, 8]);
    assert_eq!(&page[72..80], &[0x11, 0x22, 0x33, 0x44, 0, 0, 0, 0]);
    assert_eq!(&page[144..148], &[1, 2, 3, 4]);
    assert_eq!(MetaV4::decode_unchecked(&page), Some(meta));
    assert_eq!(
        u32_le(&page, META_CRC_OFFSET),
        crc32c::crc32c_with_zeroed(&page, META_CRC_OFFSET, 4).unwrap()
    );
    assert!(page[256..].iter().all(|&byte| byte == 0));
}
