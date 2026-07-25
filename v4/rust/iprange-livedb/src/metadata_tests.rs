use super::*;
use crate::bootstrap::tests::empty_direct_meta;
use crate::fixed_tree::Store as StoreTrait;
use crate::test_alloc::measure_thread_allocations;
use std::time::{SystemTime, UNIX_EPOCH};

struct TestStore {
    file: File,
    page_count: u64,
    txn: u64,
}

impl TestStore {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-metadata-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let file = File::options()
            .read(true)
            .write(true)
            .create_new(true)
            .open(&path)
            .unwrap();
        std::fs::remove_file(path).unwrap();
        file.set_len((2 * PAGE_SIZE) as u64).unwrap();
        Self {
            file,
            page_count: 2,
            txn: 7,
        }
    }
}

impl StoreTrait for TestStore {
    fn target_txn(&self) -> u64 {
        self.txn
    }

    fn page_limit(&self) -> u64 {
        self.page_count
    }

    fn read(&self, page_number: u32, page: &mut [u8; PAGE_SIZE]) -> Result<()> {
        file_io::read_page(&self.file, page_number, self.page_count, page)
    }

    fn allocate(&mut self) -> Result<u32> {
        let page = self.page_count as u32;
        self.page_count += 1;
        Ok(page)
    }

    fn write(&mut self, page_number: u32, page: &[u8; PAGE_SIZE]) -> Result<()> {
        file_io::write_exact_at(&self.file, page, u64::from(page_number) * PAGE_SIZE as u64)
    }

    fn discard_private(&mut self, _page_number: u32) -> Result<()> {
        unreachable!()
    }
}

fn build(input: &[u8]) -> (TestStore, MetaV4, Vec<u8>) {
    let compressed = compress(
        input,
        compressed_bound(input.len()) as u64 + DEFLATE_HEAP_OVERHEAD,
    )
    .unwrap();
    build_compressed(input.len() as u64, compressed)
}

fn build_compressed(uncompressed_len: u64, compressed: Vec<u8>) -> (TestStore, MetaV4, Vec<u8>) {
    let mut store = TestStore::new();
    let root = write_chain(&mut store, &compressed).unwrap();
    let mut meta = empty_direct_meta(store.txn);
    meta.page_count = store.page_count;
    meta.metadata_root = root;
    meta.metadata_uncompressed_len = uncompressed_len;
    meta.metadata_compressed_len = compressed.len() as u64;
    (store, meta, compressed)
}

fn pseudo_random(length: usize) -> Vec<u8> {
    let mut state = 0x52dce729u32;
    (0..length)
        .map(|_| {
            state ^= state << 13;
            state ^= state >> 17;
            state ^= state << 5;
            state as u8
        })
        .collect()
}

fn rewrite(store: &TestStore, meta: &MetaV4, edit: impl FnOnce(&mut [u8; PAGE_SIZE])) {
    let mut page = [0; PAGE_SIZE];
    file_io::read_page(&store.file, meta.metadata_root, meta.page_count, &mut page).unwrap();
    edit(&mut page);
    file_io::write_exact_at(
        &store.file,
        &page,
        u64::from(meta.metadata_root) * PAGE_SIZE as u64,
    )
    .unwrap();
}

#[test]
fn exact_bytes_round_trip_for_empty_text_and_maximum_payloads() {
    for input in [
        Vec::new(),
        b"not json; exact bytes\n".to_vec(),
        vec![b'x'; MAX_METADATA_UNCOMPRESSED as usize],
        pseudo_random(MAX_METADATA_UNCOMPRESSED as usize),
    ] {
        let (store, meta, _) = build(&input);
        let mut output = vec![0; input.len()];
        assert_eq!(
            read(&store.file, &meta, &mut output).unwrap(),
            Some(input.len())
        );
        assert_eq!(output, input);
    }
}

#[test]
fn compression_uses_real_deflate_and_bounded_stored_fallback() {
    let repeated = vec![b'a'; MAX_METADATA_UNCOMPRESSED as usize];
    let repeated_budget = compressed_bound(repeated.len()) as u64 + DEFLATE_HEAP_OVERHEAD;
    let compressed = compress(&repeated, repeated_budget).unwrap();
    assert!(compressed.len() < 2_000);

    let random = pseudo_random(MAX_METADATA_UNCOMPRESSED as usize);
    let bound = compressed_bound(random.len());
    let compressed = compress(&random, bound as u64 + DEFLATE_HEAP_OVERHEAD).unwrap();
    assert_eq!(compressed.len(), bound);
    assert_eq!(&compressed[..2], &[0x78, 0x01]);

    let stored = compress(&repeated, compressed_bound(repeated.len()) as u64).unwrap();
    assert_eq!(stored.len(), compressed_bound(repeated.len()));
    assert_eq!(&stored[..2], &[0x78, 0x01]);
}

#[test]
fn compressor_allocations_fit_the_charged_fixed_overhead() {
    for input in [
        vec![b'a'; MAX_METADATA_UNCOMPRESSED as usize],
        pseudo_random(MAX_METADATA_UNCOMPRESSED as usize),
    ] {
        let bound = compressed_bound(input.len());
        let (_, statistics) = measure_thread_allocations(|| {
            compress(&input, bound as u64 + DEFLATE_HEAP_OVERHEAD).unwrap()
        });
        assert!(
            statistics.bytes <= bound + DEFLATE_HEAP_OVERHEAD as usize,
            "compression allocated {} bytes against a {}-byte charge",
            statistics.bytes,
            bound + DEFLATE_HEAP_OVERHEAD as usize
        );
    }
}

#[test]
fn compression_checks_input_and_heap_bounds_before_allocation() {
    let too_large = vec![0; MAX_METADATA_UNCOMPRESSED as usize + 1];
    assert!(matches!(
        compress(&too_large, u64::MAX),
        Err(Error::InvalidArgument(_))
    ));
    assert!(matches!(
        compress(b"x", compressed_bound(1) as u64 - 1),
        Err(Error::BudgetExceeded(_))
    ));
}

#[test]
fn stored_zlib_matches_every_block_boundary() {
    for length in [0, 1, 65_534, 65_535, 65_536, 131_070, 1_048_576] {
        let input = pseudo_random(length);
        let bound = compressed_bound(length);
        let compressed = compress(&input, bound as u64).unwrap();
        assert_eq!(compressed.len(), bound);
        let (store, meta, _) = build_compressed(length as u64, compressed);
        assert_eq!(read_vec(&store.file, &meta).unwrap().unwrap(), input);
    }
}

#[test]
fn too_small_output_is_untouched_and_reports_the_exact_requirement() {
    let (store, meta, _) = build(b"metadata");
    let mut output = [0x55; 7];
    assert!(matches!(
        read(&store.file, &meta, &mut output),
        Err(Error::BufferTooSmall { required: 8 })
    ));
    assert_eq!(output, [0x55; 7]);
}

#[test]
fn ordinary_metadata_reads_do_not_check_page_crc() {
    let input = b"opaque bytes";
    let (store, meta, _) = build(input);
    rewrite(&store, &meta, |page| page[28] ^= 0xff);
    let mut output = vec![0; input.len()];
    assert_eq!(
        read(&store.file, &meta, &mut output).unwrap(),
        Some(input.len())
    );
    assert_eq!(output, input);
}

#[test]
fn selected_chain_structure_is_checked_without_a_page_set() {
    type Edit = fn(&mut [u8; PAGE_SIZE], u32);
    let edits: [Edit; 5] = [
        |page, _| page[4] = 12,
        |page, _| put_u64(page, 40, 1),
        |page, _| put_u16(page, 36, 1),
        |page, root| put_u32(page, 32, root),
        |page, _| page[PAGE_SIZE - 1] = 1,
    ];

    for edit in edits {
        let input = pseudo_random(8_000);
        let (store, meta, _) = build(&input);
        let root = meta.metadata_root;
        rewrite(&store, &meta, |page| edit(page, root));
        let mut output = vec![0; input.len()];
        assert!(matches!(
            read(&store.file, &meta, &mut output),
            Err(Error::Corrupt(_))
        ));
    }
}

#[test]
fn zlib_checksum_lengths_and_trailing_streams_are_exact() {
    let original = b"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
    let valid = compress(original, compressed_bound(original.len()) as u64).unwrap();

    let mut bad_checksum = valid.clone();
    *bad_checksum.last_mut().unwrap() ^= 1;
    let (store, meta, _) = build_compressed(original.len() as u64, bad_checksum);
    assert!(read_vec(&store.file, &meta).is_err());

    let (store, meta, _) = build_compressed((original.len() - 1) as u64, valid.clone());
    assert!(read_vec(&store.file, &meta).is_err());

    let (store, meta, _) = build_compressed((original.len() + 1) as u64, valid.clone());
    assert!(read_vec(&store.file, &meta).is_err());

    let mut concatenated = valid.clone();
    concatenated.extend_from_slice(&valid);
    let (store, meta, _) = build_compressed(original.len() as u64, concatenated);
    assert!(read_vec(&store.file, &meta).is_err());

    let mut trailing = valid;
    trailing.push(0);
    let (store, meta, _) = build_compressed(original.len() as u64, trailing);
    assert!(read_vec(&store.file, &meta).is_err());
}

#[test]
fn raw_gzip_and_dictionary_streams_are_rejected() {
    let input = b"aaaaaaaaaaaaaaaa";
    let valid = compress(
        input,
        compressed_bound(input.len()) as u64 + DEFLATE_HEAP_OVERHEAD,
    )
    .unwrap();
    let invalid = [
        valid[2..valid.len() - 4].to_vec(),
        vec![0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 3],
        vec![0x78, 0xbb, 0, 0, 0, 1, 3, 0, 0, 0, 0, 1],
    ];
    for compressed in invalid {
        let (store, meta, _) = build_compressed(input.len() as u64, compressed);
        assert!(read_vec(&store.file, &meta).is_err());
    }
}

#[test]
fn absent_metadata_never_touches_caller_storage() {
    let store = TestStore::new();
    let meta = empty_direct_meta(1);
    let mut output = [9; 4];
    assert_eq!(read(&store.file, &meta, &mut output).unwrap(), None);
    assert_eq!(output, [9; 4]);
}
