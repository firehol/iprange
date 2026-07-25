use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{AddressFamily, ImmutableReader, MetaSelection, ValueKind, ValueTag};

const PAGE_SIZE: usize = 4096;

struct TestPath(PathBuf);

impl TestPath {
    fn new() -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self(
            std::env::temp_dir().join(format!("iprange-v4-public-{}-{unique}", std::process::id())),
        )
    }
}

impl Drop for TestPath {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.0);
    }
}

fn canonical_empty_image() -> Vec<u8> {
    let mut page = [0u8; PAGE_SIZE];
    page[0..8].copy_from_slice(b"IPRANGE4");
    page[8..10].copy_from_slice(&256u16.to_le_bytes());
    page[10] = 12;
    page[11] = AddressFamily::Ipv6 as u8;
    page[12] = ValueKind::Direct as u8;
    page[16..32].copy_from_slice(ValueTag::RETENTION.as_wire());
    page[32..48].fill(1);
    page[48..56].copy_from_slice(&1u64.to_le_bytes());
    page[56..72].fill(2);
    page[72..80].copy_from_slice(&2u64.to_le_bytes());
    let checksum = crc32c(&page);
    page[252..256].copy_from_slice(&checksum.to_le_bytes());

    let mut image = Vec::with_capacity(2 * PAGE_SIZE);
    image.extend_from_slice(&page);
    image.extend_from_slice(&page);
    image
}

fn crc32c(bytes: &[u8]) -> u32 {
    let mut crc = u32::MAX;
    for &byte in bytes {
        crc ^= u32::from(byte);
        for _ in 0..8 {
            let mask = 0u32.wrapping_sub(crc & 1);
            crc = (crc >> 1) ^ (0x82f6_3b78 & mask);
        }
    }
    !crc
}

#[test]
fn public_immutable_open_reads_the_selected_meta() {
    let path = TestPath::new();
    fs::write(&path.0, canonical_empty_image()).unwrap();

    let reader = ImmutableReader::open(&path.0).unwrap();
    let info = reader.info();
    assert_eq!(info.address_family, AddressFamily::Ipv6);
    assert_eq!(info.value_kind, ValueKind::Direct);
    assert_eq!(info.value_tag, ValueTag::RETENTION);
    assert_eq!(info.transaction_id, 1);
    assert_eq!(info.page_count, 2);
    assert_eq!(info.meta_selection, MetaSelection::ProvenCurrent);
}
