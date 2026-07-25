use std::fs;
use std::path::PathBuf;
use std::time::{SystemTime, UNIX_EPOCH};

use iprange_livedb::{AddressFamily, ImmutableReader, Ipv6Key, MetaSelection, ValueKind, ValueTag};

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

fn single_range_image() -> Vec<u8> {
    let mut meta = [0u8; PAGE_SIZE];
    meta[0..8].copy_from_slice(b"IPRANGE4");
    meta[8..10].copy_from_slice(&256u16.to_le_bytes());
    meta[10] = 12;
    meta[11] = AddressFamily::Ipv6 as u8;
    meta[12] = ValueKind::Direct as u8;
    meta[16..32].copy_from_slice(ValueTag::RETENTION.as_wire());
    meta[32..48].fill(1);
    meta[48..56].copy_from_slice(&1u64.to_le_bytes());
    meta[56..72].fill(2);
    meta[72..80].copy_from_slice(&3u64.to_le_bytes());
    meta[80..88].copy_from_slice(&1u64.to_le_bytes());
    meta[144..148].copy_from_slice(&2u32.to_le_bytes());
    let checksum = crc32c(&meta);
    meta[252..256].copy_from_slice(&checksum.to_le_bytes());

    let mut leaf = [0u8; PAGE_SIZE];
    leaf[0..4].copy_from_slice(b"IP4P");
    leaf[4] = 2;
    leaf[6..8].copy_from_slice(&32u16.to_le_bytes());
    leaf[8..16].copy_from_slice(&1u64.to_le_bytes());
    leaf[16..18].copy_from_slice(&1u16.to_le_bytes());
    leaf[20..22].copy_from_slice(&34u16.to_le_bytes());
    let record_at = PAGE_SIZE - 36;
    leaf[22..24].copy_from_slice(&(record_at as u16).to_le_bytes());
    leaf[24..28].copy_from_slice(&(AddressFamily::Ipv6 as u32).to_le_bytes());
    leaf[32..34].copy_from_slice(&(record_at as u16).to_le_bytes());
    leaf[record_at..record_at + 16].copy_from_slice(&10u128.to_le_bytes());
    leaf[record_at + 16..record_at + 32].copy_from_slice(&20u128.to_le_bytes());
    leaf[record_at + 32..].copy_from_slice(&42u32.to_le_bytes());
    let checksum = crc32c(&leaf);
    leaf[28..32].copy_from_slice(&checksum.to_le_bytes());

    let mut image = Vec::with_capacity(3 * PAGE_SIZE);
    image.extend_from_slice(&meta);
    image.extend_from_slice(&meta);
    image.extend_from_slice(&leaf);
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
fn public_immutable_open_and_direct_lookup() {
    let path = TestPath::new();
    fs::write(&path.0, single_range_image()).unwrap();

    let reader = ImmutableReader::open(&path.0).unwrap();
    let info = reader.info();
    assert_eq!(info.address_family, AddressFamily::Ipv6);
    assert_eq!(info.value_kind, ValueKind::Direct);
    assert_eq!(info.value_tag, ValueTag::RETENTION);
    assert_eq!(info.transaction_id, 1);
    assert_eq!(info.page_count, 3);
    assert_eq!(info.range_record_count, 1);
    assert_eq!(info.meta_selection, MetaSelection::ProvenCurrent);
    assert_eq!(
        reader.lookup_direct_v6(Ipv6Key::from_u128(9)).unwrap(),
        None
    );
    assert_eq!(
        reader.lookup_direct_v6(Ipv6Key::from_u128(10)).unwrap(),
        Some(42)
    );
    assert_eq!(
        reader.lookup_direct_v6(Ipv6Key::from_u128(20)).unwrap(),
        Some(42)
    );
    assert_eq!(
        reader.lookup_direct_v6(Ipv6Key::from_u128(21)).unwrap(),
        None
    );
}
