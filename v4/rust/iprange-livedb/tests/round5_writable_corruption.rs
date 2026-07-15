use iprange_livedb::page_store::VecPageStore;
use iprange_livedb::spec;
use iprange_livedb::wire::{finalize_checksum, Meta, PageHeader};
use iprange_livedb::{Ipv4Key, Reader, Writer};

fn active_meta(image: &[u8]) -> Meta {
    let first = Meta::decode(&image[..spec::PAGE_SIZE]);
    let second = Meta::decode(&image[spec::PAGE_SIZE..2 * spec::PAGE_SIZE]);
    if first.txn_id >= second.txn_id {
        first
    } else {
        second
    }
}

fn active_meta_page(image: &mut [u8]) -> &mut [u8] {
    let first = Meta::decode(&image[..spec::PAGE_SIZE]);
    let second = Meta::decode(&image[spec::PAGE_SIZE..2 * spec::PAGE_SIZE]);
    if first.txn_id >= second.txn_id {
        &mut image[..spec::PAGE_SIZE]
    } else {
        &mut image[spec::PAGE_SIZE..2 * spec::PAGE_SIZE]
    }
}

fn u32_at(bytes: &[u8], offset: usize) -> u32 {
    u32::from_le_bytes(bytes[offset..offset + 4].try_into().unwrap())
}

fn put_u32(bytes: &mut [u8], offset: usize, value: u32) {
    bytes[offset..offset + 4].copy_from_slice(&value.to_le_bytes());
}

fn put_u64(bytes: &mut [u8], offset: usize, value: u64) {
    bytes[offset..offset + 8].copy_from_slice(&value.to_le_bytes());
}

fn committed_scalar_image(records: u32) -> Vec<u8> {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    for i in 0..records {
        writer
            .set(Ipv4Key(i * 2 + 10), Ipv4Key(i * 2 + 10), 1)
            .unwrap();
    }
    writer.commit(1, u64::MAX).unwrap();
    writer.into_image().unwrap()
}

#[test]
fn writable_open_rejects_every_committed_data_corruption_without_changing_file() {
    let one_record = committed_scalar_image(1);
    let branch_tree = committed_scalar_image(800);
    let mut cases = Vec::new();

    let mut leaf_crc = one_record.clone();
    let meta = active_meta(&leaf_crc);
    leaf_crc[meta.root_pgno as usize * spec::PAGE_SIZE + spec::PAGE_HEADER_SIZE] ^= 0x80;
    cases.push(("leaf-crc", leaf_crc));

    let mut reversed = one_record.clone();
    let meta = active_meta(&reversed);
    let base = meta.root_pgno as usize * spec::PAGE_SIZE;
    let leaf = &mut reversed[base..base + spec::PAGE_SIZE];
    put_u32(leaf, spec::PAGE_HEADER_SIZE, 30);
    finalize_checksum(leaf);
    cases.push(("reversed-record", reversed));

    let mut count_mismatch = one_record.clone();
    let meta_page = active_meta_page(&mut count_mismatch);
    put_u64(meta_page, spec::META_RECORD_COUNT, 2);
    finalize_checksum(meta_page);
    cases.push(("record-count-mismatch", count_mismatch));

    let mut separator = branch_tree.clone();
    let meta = active_meta(&separator);
    let base = meta.root_pgno as usize * spec::PAGE_SIZE;
    let root = &mut separator[base..base + spec::PAGE_SIZE];
    let header = PageHeader::decode(root);
    assert_eq!(header.page_type, spec::PAGE_TYPE_BRANCH);
    assert!(header.entry_count > 0);
    let sep_offset = spec::PAGE_HEADER_SIZE + 4;
    let value = u32_at(root, sep_offset);
    put_u32(root, sep_offset, value + 1);
    finalize_checksum(root);
    cases.push(("branch-separator-mismatch", separator));

    let mut empty_leaf_tail = branch_tree.clone();
    let meta = active_meta(&empty_leaf_tail);
    let root_base = meta.root_pgno as usize * spec::PAGE_SIZE;
    let root = &empty_leaf_tail[root_base..root_base + spec::PAGE_SIZE];
    let root_header = PageHeader::decode(root);
    assert_eq!(root_header.page_type, spec::PAGE_TYPE_BRANCH);
    assert!(root_header.entry_count > 0);
    let leaf_pgno = u32_at(root, spec::PAGE_HEADER_SIZE);
    let leaf_base = leaf_pgno as usize * spec::PAGE_SIZE;
    let leaf = &mut empty_leaf_tail[leaf_base..leaf_base + spec::PAGE_SIZE];
    let leaf_header = PageHeader::decode(leaf);
    assert_eq!(leaf_header.page_type, spec::PAGE_TYPE_LEAF);
    assert!(leaf_header.entry_count > 0);
    leaf[spec::PH_ENTRY_COUNT..spec::PH_ENTRY_COUNT + 2].copy_from_slice(&0u16.to_le_bytes());
    finalize_checksum(leaf);
    let record_count = meta.record_count - u64::from(leaf_header.entry_count);
    let meta_page = active_meta_page(&mut empty_leaf_tail);
    put_u64(meta_page, spec::META_RECORD_COUNT, record_count);
    finalize_checksum(meta_page);
    cases.push(("empty-leaf-nonzero-tail", empty_leaf_tail));

    let mut failures = Vec::new();
    for (name, image) in cases {
        let reader = Reader::open(&image).expect("cheap read open should defer tree validation");
        if reader.validate().is_ok() {
            failures.push(format!("Reader::validate accepted {name}"));
        }
        let core_accepted =
            Writer::<Ipv4Key>::open(Box::new(VecPageStore::new(image.clone()))).is_ok();
        if core_accepted {
            failures.push(format!("core writable open accepted {name}"));
        }

        #[cfg(unix)]
        {
            use iprange_livedb::os::FileWriter;
            let dir = std::env::temp_dir().join(format!(
                "iprange-round5-corrupt-{name}-{}-{:?}",
                std::process::id(),
                std::thread::current().id()
            ));
            let _ = std::fs::remove_dir_all(&dir);
            std::fs::create_dir_all(&dir).unwrap();
            let path = dir.join("corrupt.iprdb");
            std::fs::write(&path, &image).unwrap();
            let file_accepted = match FileWriter::<Ipv4Key>::open(&path) {
                Ok(writer) => {
                    writer.close();
                    true
                }
                Err(_) => false,
            };
            let after = std::fs::read(&path).unwrap();
            if file_accepted {
                failures.push(format!("file-backed writable open accepted {name}"));
            }
            if after != image {
                failures.push(format!(
                    "file-backed writable open changed rejected {name} file"
                ));
            }
            let _ = std::fs::remove_dir_all(dir);
        }
    }
    assert!(
        failures.is_empty(),
        "writable corruption gaps: {failures:?}"
    );
}
