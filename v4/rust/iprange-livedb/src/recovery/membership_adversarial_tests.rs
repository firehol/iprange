use sha2::{Digest, Sha256};

use super::*;
use crate::mapping::test_support as file_io;
use crate::mapping::{ByteSource, Mapping};

#[test]
fn equal_membership_bytes_with_different_source_ids_coalesce_exactly() {
    let paths = Paths::new("coalesce");
    let mut source = source_builder(&paths.source, 64);
    source.push_feed(FeedName::new("a").unwrap(), 1).unwrap();
    source.push_feed(FeedName::new("b").unwrap(), 5).unwrap();
    source
        .push_membership_v4(Ipv4Key(0), Ipv4Key(9), &Words(vec![1 << 1]))
        .unwrap();
    source
        .push_membership_v4(Ipv4Key(10), Ipv4Key(19), &Words(vec![1 << 5]))
        .unwrap();
    let finished = source.finish_owned().unwrap();
    let meta = finished.meta;
    rewrite_inline_word(&finished.file, meta, Ipv4Key(15), 1 << 1);
    drop(finished.file);

    let (report, unknown) = recover(&paths, meta);
    assert!(unknown.is_empty());
    assert_eq!(report.ranges.accepted, 2);
    let reader = ImmutableReader::open(&paths.output).unwrap();
    assert_eq!(reader.info().range_record_count, 1);
    for address in [0, 9, 10, 19] {
        let membership = reader
            .lookup_membership_v4(Ipv4Key(address))
            .unwrap()
            .unwrap();
        assert!(membership.contains_index(1).unwrap());
        assert!(!membership.contains_index(5).unwrap());
    }
    validate_clean(&paths.output);
}

#[test]
fn maximal_membership_overlap_component_is_rejected_whole() {
    let paths = Paths::new("overlap");
    let mut source = source_builder(&paths.source, 64);
    source.push_feed(FeedName::new("a").unwrap(), 1).unwrap();
    source.push_feed(FeedName::new("b").unwrap(), 5).unwrap();
    source
        .push_membership_v4(Ipv4Key(0), Ipv4Key(9), &Words(vec![1 << 1]))
        .unwrap();
    source
        .push_membership_v4(Ipv4Key(10), Ipv4Key(19), &Words(vec![1 << 5]))
        .unwrap();
    let finished = source.finish_owned().unwrap();
    let meta = finished.meta;
    rewrite_second_range_start(&finished.file, meta, 5);
    drop(finished.file);

    let (report, unknown) = recover(&paths, meta);
    assert_eq!(report.ranges.accepted, 0);
    assert_eq!(report.ranges.rejected, 2);
    assert!(unknown
        .iter()
        .any(|item| item.reason == ValidationReason::RangeOverlap));
    assert_eq!(
        ImmutableReader::open(&paths.output)
            .unwrap()
            .info()
            .range_record_count,
        0
    );
    validate_clean(&paths.output);
}

#[test]
fn duplicate_membership_ids_are_not_selected_as_a_winner() {
    let paths = Paths::new("duplicate-id");
    let mut source = source_builder(&paths.source, 64);
    source.push_feed(FeedName::new("a").unwrap(), 1).unwrap();
    source.push_feed(FeedName::new("b").unwrap(), 5).unwrap();
    source
        .push_membership_v4(Ipv4Key(0), Ipv4Key(9), &Words(vec![1 << 1]))
        .unwrap();
    source
        .push_membership_v4(Ipv4Key(20), Ipv4Key(29), &Words(vec![1 << 5]))
        .unwrap();
    let finished = source.finish_owned().unwrap();
    let meta = finished.meta;
    let first = range_tree::lookup(&finished.mapping, &meta, Ipv4Key(5))
        .unwrap()
        .unwrap();
    rewrite_membership_id(&finished.file, meta, Ipv4Key(25), first);
    drop(finished.file);

    let (report, unknown) = recover(&paths, meta);
    assert_eq!(report.membership_entries.accepted, 0);
    assert_eq!(report.membership_entries.rejected, 2);
    assert_eq!(report.ranges.rejected, 2);
    assert!(unknown
        .iter()
        .any(|item| item.reason == ValidationReason::MembershipInvalid));
    validate_clean(&paths.output);
}

#[cfg(any(unix, windows))]
#[test]
fn disordered_membership_ranges_use_the_bounded_shared_external_sort() {
    let paths = Paths::new("external");
    let mut source = source_builder(&paths.source, 64);
    source.push_feed(FeedName::new("a").unwrap(), 1).unwrap();
    source.push_feed(FeedName::new("b").unwrap(), 5).unwrap();
    for index in 0..120u32 {
        let words = if index % 2 == 0 {
            Words(vec![1 << 1])
        } else {
            Words(vec![1 << 5])
        };
        source
            .push_membership_v4(Ipv4Key(index * 3), Ipv4Key(index * 3 + 1), &words)
            .unwrap();
    }
    let finished = source.finish_owned().unwrap();
    let meta = finished.meta;
    swap_first_two_ranges(&finished.file, meta);
    drop(finished.file);
    let budget = RecoveryBudget {
        max_heap_bytes: 128,
        max_output_pages: 100_000,
        max_open_files: 4,
        max_scratch_bytes: 64 * 1024,
        max_scratch_files: 2,
        scratch_directory: Some(paths.scratch.clone()),
    };
    let source = File::open(&paths.source).unwrap();
    let source = Mapping::read_only(source, meta.page_count * PAGE_SIZE as u64).unwrap();
    let result = construct(
        &source,
        meta,
        output_builder(&paths.output, meta),
        &budget,
        &CancellationToken::new(),
        &mut |_: &RecoveryUnknownEnvelope| Ok(RecoverySinkControl::Continue),
    )
    .unwrap();
    drop(result.finished.file);

    assert!(result.scratch.as_ref().is_some_and(|value| value.clean()));
    assert_eq!(result.report.ranges.accepted, 120);
    assert_eq!(fs::read_dir(&paths.scratch).unwrap().count(), 0);
    assert_eq!(
        ImmutableReader::open(&paths.output)
            .unwrap()
            .info()
            .range_record_count,
        120
    );
    validate_clean(&paths.output);
}

fn rewrite_inline_word(file: &File, meta: MetaV4, address: Ipv4Key, word: u64) {
    let mapping = Mapping::read_only_view(file, file.metadata().unwrap().len()).unwrap();
    let id = range_tree::lookup(&mapping, &meta, address)
        .unwrap()
        .unwrap();
    let record = membership_tree::find(&mapping, &meta, id).unwrap().unwrap();
    drop(mapping);
    assert_eq!(record.storage, membership_tree::Storage::Inline);
    let mut page = read_page(file, record.leaf_page, meta.page_count);
    let start = cell_start(&page, record.leaf_index);
    page[start + codec_offset::DIGEST..start + codec_offset::BITMAP]
        .copy_from_slice(&Sha256::digest(word.to_le_bytes()));
    page[start + codec_offset::BITMAP..start + codec_offset::BITMAP + 8]
        .copy_from_slice(&word.to_le_bytes());
    stamp(&mut page);
    write_page(file, record.leaf_page, &page);
}

fn rewrite_membership_id(file: &File, meta: MetaV4, address: Ipv4Key, id: u32) {
    let mapping = Mapping::read_only_view(file, file.metadata().unwrap().len()).unwrap();
    let old = range_tree::lookup(&mapping, &meta, address)
        .unwrap()
        .unwrap();
    let record = membership_tree::find(&mapping, &meta, old)
        .unwrap()
        .unwrap();
    drop(mapping);
    let mut page = read_page(file, record.leaf_page, meta.page_count);
    let start = cell_start(&page, record.leaf_index);
    page[start + 4..start + 8].copy_from_slice(&id.to_le_bytes());
    stamp(&mut page);
    write_page(file, record.leaf_page, &page);
}

fn rewrite_second_range_start(file: &File, meta: MetaV4, from: u32) {
    let leaf = leftmost_range_leaf(file, meta);
    let mut page = read_page(file, leaf, meta.page_count);
    let start = cell_start(&page, 1);
    page[start..start + 4].copy_from_slice(&from.to_le_bytes());
    stamp(&mut page);
    write_page(file, leaf, &page);
}

fn swap_first_two_ranges(file: &File, meta: MetaV4) {
    let leaf = leftmost_range_leaf(file, meta);
    let mut page = read_page(file, leaf, meta.page_count);
    let first = cell_start(&page, 0);
    let second = cell_start(&page, 1);
    let left: [u8; 12] = page[first..first + 12].try_into().unwrap();
    let right: [u8; 12] = page[second..second + 12].try_into().unwrap();
    page[first..first + 12].copy_from_slice(&right);
    page[second..second + 12].copy_from_slice(&left);
    stamp(&mut page);
    write_page(file, leaf, &page);
}

fn leftmost_range_leaf(file: &File, meta: MetaV4) -> u32 {
    let mut page_number = meta.range_root;
    let mut expected = None;
    loop {
        let page = read_page(file, page_number, meta.page_count);
        let level = u16_le(&page, 18);
        let page_type = if level == 0 {
            range_tree::RANGE_LEAF
        } else {
            range_tree::RANGE_BRANCH
        };
        let header = slotted_page::parse(
            &page,
            meta.txn_id,
            page_type,
            AddressFamily::Ipv4 as u32,
            expected,
        )
        .unwrap();
        if level == 0 {
            return page_number;
        }
        page_number = u32::from_le_bytes(
            slotted_page::cell(&page, &header, 0, 8)
                .unwrap()
                .array(4)
                .unwrap(),
        );
        expected = Some(level - 1);
    }
}

fn read_page(file: &File, page: u32, limit: u64) -> [u8; PAGE_SIZE] {
    let mut bytes = [0; PAGE_SIZE];
    file_io::read_page(file, page, limit, &mut bytes).unwrap();
    bytes
}

fn write_page(file: &File, page: u32, bytes: &[u8; PAGE_SIZE]) {
    file_io::write_exact_at(file, bytes, u64::from(page) * PAGE_SIZE as u64).unwrap();
}

fn cell_start(page: &[u8; PAGE_SIZE], index: usize) -> usize {
    usize::from(u16_le(page, slotted_page::HEADER_SIZE + index * 2))
}

mod codec_offset {
    pub(super) const DIGEST: usize = 32;
    pub(super) const BITMAP: usize = 64;
}
