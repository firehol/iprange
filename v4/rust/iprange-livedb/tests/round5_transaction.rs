use iprange_livedb::page_store::{PageStore, VecPageStore};
use iprange_livedb::spec;
use iprange_livedb::wire::Meta;
use iprange_livedb::{Error, Ipv4Key, Result, Writer};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};

struct FaultStore {
    inner: VecPageStore,
    events: Arc<Mutex<Vec<&'static str>>>,
    fail_alloc: Arc<AtomicBool>,
    fail_truncate: Arc<AtomicBool>,
}

impl PageStore for FaultStore {
    fn page(&self, pgno: u32) -> &[u8] {
        self.inner.page(pgno)
    }

    fn page_mut(&mut self, pgno: u32) -> &mut [u8] {
        if pgno < 2 {
            self.events.lock().unwrap().push("meta");
        }
        self.inner.page_mut(pgno)
    }

    fn copy_page(&mut self, src_pgno: u32, dst_pgno: u32) {
        self.inner.copy_page(src_pgno, dst_pgno);
    }

    fn alloc_page(&mut self) -> Result<u32> {
        if self.fail_alloc.swap(false, Ordering::Relaxed) {
            return Err(Error::State("round5 injected allocation failure"));
        }
        self.inner.alloc_page()
    }

    fn total_pages(&self) -> u32 {
        self.inner.total_pages()
    }

    fn committed_pages(&self) -> u32 {
        self.inner.committed_pages()
    }

    fn set_committed_pages(&mut self, pages: u32) {
        self.inner.set_committed_pages(pages);
    }

    fn committed_bytes(&self) -> &[u8] {
        self.inner.committed_bytes()
    }

    fn ensure_capacity(&mut self, min_pages: u32) -> Result<()> {
        self.inner.ensure_capacity(min_pages)
    }

    fn sync(&self) -> Result<()> {
        self.events.lock().unwrap().push("sync");
        Ok(())
    }

    fn truncate(&mut self, new_total_pages: u32) -> Result<()> {
        self.events.lock().unwrap().push("truncate");
        if self.fail_truncate.swap(false, Ordering::Relaxed) {
            return Err(Error::State("round5 injected truncate failure"));
        }
        self.inner.truncate(new_total_pages)
    }
}

type FaultControls = (
    Arc<Mutex<Vec<&'static str>>>,
    Arc<AtomicBool>,
    Arc<AtomicBool>,
);

fn scalar_tree_image(records: u32) -> Vec<u8> {
    let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_SCALAR, 0).unwrap();
    for i in 0..records {
        writer.set(Ipv4Key(i * 2), Ipv4Key(i * 2), 1).unwrap();
    }
    writer.commit(1, u64::MAX).unwrap();
    writer.into_image().unwrap()
}

fn open_fault_writer(image: Vec<u8>) -> (Writer<Ipv4Key>, FaultControls) {
    let events = Arc::new(Mutex::new(Vec::new()));
    let fail_alloc = Arc::new(AtomicBool::new(false));
    let fail_truncate = Arc::new(AtomicBool::new(false));
    let store = FaultStore {
        inner: VecPageStore::new(image),
        events: events.clone(),
        fail_alloc: fail_alloc.clone(),
        fail_truncate: fail_truncate.clone(),
    };
    let writer = Writer::<Ipv4Key>::open(Box::new(store)).unwrap();
    (writer, (events, fail_alloc, fail_truncate))
}

#[test]
fn commit_publishes_durable_metadata_before_physical_truncation() {
    let (mut writer, (events, _, _)) = open_fault_writer(scalar_tree_image(800));
    writer.delete(Ipv4Key(0), Ipv4Key(u32::MAX)).unwrap();
    events.lock().unwrap().clear();
    writer.commit(2, u64::MAX).unwrap();

    let events = events.lock().unwrap();
    let truncate_at = events
        .iter()
        .position(|event| *event == "truncate")
        .expect("fixture did not exercise truncation");
    let meta_at = events
        .iter()
        .position(|event| *event == "meta")
        .expect("commit did not publish metadata");
    let sync_after_meta = events
        .iter()
        .enumerate()
        .rfind(|(index, event)| *index > meta_at && **event == "sync")
        .map(|(index, _)| index)
        .expect("commit did not durably sync published metadata");
    assert!(
        truncate_at > sync_after_meta,
        "events={events:?}, want data sync -> metadata -> metadata sync -> truncate"
    );
}

struct SnapshotSyncStore {
    inner: VecPageStore,
    fail_sync: Arc<AtomicBool>,
    failed_image: Arc<Mutex<Option<Vec<u8>>>>,
}

impl PageStore for SnapshotSyncStore {
    fn page(&self, pgno: u32) -> &[u8] {
        self.inner.page(pgno)
    }

    fn page_mut(&mut self, pgno: u32) -> &mut [u8] {
        self.inner.page_mut(pgno)
    }

    fn copy_page(&mut self, src_pgno: u32, dst_pgno: u32) {
        self.inner.copy_page(src_pgno, dst_pgno);
    }

    fn alloc_page(&mut self) -> Result<u32> {
        self.inner.alloc_page()
    }

    fn total_pages(&self) -> u32 {
        self.inner.total_pages()
    }

    fn committed_pages(&self) -> u32 {
        self.inner.committed_pages()
    }

    fn set_committed_pages(&mut self, pages: u32) {
        self.inner.set_committed_pages(pages);
    }

    fn committed_bytes(&self) -> &[u8] {
        self.inner.committed_bytes()
    }

    fn ensure_capacity(&mut self, min_pages: u32) -> Result<()> {
        self.inner.ensure_capacity(min_pages)
    }

    fn sync(&self) -> Result<()> {
        if self.fail_sync.swap(false, Ordering::Relaxed) {
            let mut image = Vec::with_capacity(self.total_pages() as usize * spec::PAGE_SIZE);
            for pgno in 0..self.total_pages() {
                image.extend_from_slice(self.page(pgno));
            }
            *self.failed_image.lock().unwrap() = Some(image);
            return Err(Error::State("round5 injected sync failure"));
        }
        Ok(())
    }

    fn truncate(&mut self, new_total_pages: u32) -> Result<()> {
        self.inner.truncate(new_total_pages)
    }
}

#[test]
fn previous_generation_survives_sync_failure_after_truncation() {
    let fail_sync = Arc::new(AtomicBool::new(false));
    let failed_image = Arc::new(Mutex::new(None));
    let store = SnapshotSyncStore {
        inner: VecPageStore::new(scalar_tree_image(800)),
        fail_sync: fail_sync.clone(),
        failed_image: failed_image.clone(),
    };
    let mut writer = Writer::<Ipv4Key>::open(Box::new(store)).unwrap();
    writer.delete(Ipv4Key(0), Ipv4Key(u32::MAX)).unwrap();
    fail_sync.store(true, Ordering::Relaxed);
    assert!(writer.commit(2, u64::MAX).is_err());

    let image = failed_image
        .lock()
        .unwrap()
        .take()
        .expect("sync failure did not capture the durable image");
    let reader = iprange_livedb::Reader::open(&image)
        .expect("previous generation cannot be opened after failed commit");
    reader
        .validate()
        .expect("previous generation is invalid after failed commit");
    assert_eq!(reader.record_count(), 800);
    for ip in [0, 798, 1598] {
        assert_eq!(reader.lookup_v4(Ipv4Key(ip)).unwrap(), Some(1));
    }
}

#[test]
fn truncate_failure_poisons_writer() {
    let (mut writer, (_, _, fail_truncate)) = open_fault_writer(scalar_tree_image(800));
    writer.delete(Ipv4Key(0), Ipv4Key(u32::MAX)).unwrap();
    fail_truncate.store(true, Ordering::Relaxed);
    assert!(writer.commit(2, u64::MAX).is_err());
    assert!(
        writer.set(Ipv4Key(1), Ipv4Key(1), 2).is_err(),
        "writer accepted Set after truncate failed during commit"
    );
    assert!(
        writer.commit(3, u64::MAX).is_err(),
        "writer accepted Commit after truncate failed during commit"
    );
}

#[test]
fn scope_rebuild_allocation_failure_poisons_writer() {
    let created = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
    let (mut writer, (_, fail_alloc, _)) = open_fault_writer(created.into_image().unwrap());
    let id = writer.scope_intern(&vec![1u8; 257]).unwrap();
    fail_alloc.store(true, Ordering::Relaxed);
    assert!(writer.commit(2, u64::MAX).is_err());
    assert!(
        writer.set(Ipv4Key(1), Ipv4Key(1), id).is_err(),
        "writer accepted Set after scope rebuild allocation failure"
    );
    assert!(writer.commit(3, u64::MAX).is_err());
}

#[test]
fn free_list_allocation_failure_poisons_writer() {
    let (mut writer, (_, fail_alloc, _)) = open_fault_writer(scalar_tree_image(800));
    writer.delete(Ipv4Key(200), Ipv4Key(200)).unwrap();
    fail_alloc.store(true, Ordering::Relaxed);
    assert!(
        writer.commit(2, u64::MAX).is_err(),
        "fixture did not inject free-list allocation failure"
    );
    assert!(
        writer.set(Ipv4Key(2), Ipv4Key(2), 2).is_err(),
        "writer accepted Set after free-list allocation failure"
    );
    assert!(writer.commit(3, u64::MAX).is_err());
}

struct SwitchingStore {
    valid: VecPageStore,
    corrupt: VecPageStore,
    use_corrupt: Arc<AtomicBool>,
}

impl SwitchingStore {
    fn selected(&self) -> &VecPageStore {
        if self.use_corrupt.load(Ordering::Relaxed) {
            &self.corrupt
        } else {
            &self.valid
        }
    }

    fn selected_mut(&mut self) -> &mut VecPageStore {
        if self.use_corrupt.load(Ordering::Relaxed) {
            &mut self.corrupt
        } else {
            &mut self.valid
        }
    }
}

impl PageStore for SwitchingStore {
    fn page(&self, pgno: u32) -> &[u8] {
        self.selected().page(pgno)
    }

    fn page_mut(&mut self, pgno: u32) -> &mut [u8] {
        self.selected_mut().page_mut(pgno)
    }

    fn copy_page(&mut self, src_pgno: u32, dst_pgno: u32) {
        self.selected_mut().copy_page(src_pgno, dst_pgno);
    }

    fn alloc_page(&mut self) -> Result<u32> {
        self.selected_mut().alloc_page()
    }

    fn total_pages(&self) -> u32 {
        self.selected().total_pages()
    }

    fn committed_pages(&self) -> u32 {
        self.selected().committed_pages()
    }

    fn set_committed_pages(&mut self, pages: u32) {
        self.selected_mut().set_committed_pages(pages);
    }

    fn committed_bytes(&self) -> &[u8] {
        self.selected().committed_bytes()
    }

    fn ensure_capacity(&mut self, min_pages: u32) -> Result<()> {
        self.selected_mut().ensure_capacity(min_pages)
    }

    fn sync(&self) -> Result<()> {
        Ok(())
    }

    fn truncate(&mut self, new_total_pages: u32) -> Result<()> {
        self.selected_mut().truncate(new_total_pages)
    }
}

fn active_meta(image: &[u8]) -> Meta {
    let first = Meta::decode(&image[..spec::PAGE_SIZE]);
    let second = Meta::decode(&image[spec::PAGE_SIZE..2 * spec::PAGE_SIZE]);
    if first.txn_id >= second.txn_id {
        first
    } else {
        second
    }
}

#[test]
fn commit_rejects_scope_corruption_discovered_after_open() {
    let mut created = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
    let old_id = created.scope_intern(&[1]).unwrap();
    created.set(Ipv4Key(1), Ipv4Key(1), old_id).unwrap();
    created.commit(1, u64::MAX).unwrap();
    let valid = created.into_image().unwrap();
    let mut corrupt = valid.clone();
    let meta = active_meta(&corrupt);
    let scope_base = meta.scope_table_root as usize * spec::PAGE_SIZE;
    corrupt[scope_base + spec::PH_CHECKSUM] ^= 0x80;

    let use_corrupt = Arc::new(AtomicBool::new(false));
    let store = SwitchingStore {
        valid: VecPageStore::new(valid),
        corrupt: VecPageStore::new(corrupt),
        use_corrupt: use_corrupt.clone(),
    };
    let mut writer = Writer::<Ipv4Key>::open(Box::new(store)).unwrap();
    use_corrupt.store(true, Ordering::Relaxed);
    let new_id = writer.scope_intern(&[2]).unwrap();
    writer.set(Ipv4Key(2), Ipv4Key(2), new_id).unwrap();
    assert!(
        writer.commit(2, u64::MAX).is_err(),
        "Commit silently rebuilt the scope table after its committed input became corrupt"
    );
    assert!(
        writer.set(Ipv4Key(3), Ipv4Key(3), new_id).is_err(),
        "writer accepted Set after discovering corrupt committed scope data"
    );
}

#[test]
fn no_op_and_rejected_scope_mutations_do_not_rebuild_scope_table() {
    for case in ["set-present", "clear-absent", "unknown-scope"] {
        let mut writer = Writer::<Ipv4Key>::create(spec::SCOPE_MODE_INDIRECT, 0).unwrap();
        let id = writer.scope_intern(&[1]).unwrap();
        writer.commit(1, u64::MAX).unwrap();
        let before = writer.into_image().unwrap();
        let root_before = active_meta(&before).scope_table_root;
        assert_ne!(root_before, 0);

        let mut writer = Writer::<Ipv4Key>::open(Box::new(VecPageStore::new(before))).unwrap();
        match case {
            "set-present" => assert_eq!(writer.scope_bitmap_set_feed(id, 0).unwrap(), id),
            "clear-absent" => assert_eq!(writer.scope_bitmap_clear_feed(id, 8).unwrap(), id),
            "unknown-scope" => assert!(writer.scope_bitmap_set_feed(u32::MAX, 1).is_err()),
            _ => unreachable!(),
        }
        writer.commit(2, u64::MAX).unwrap();
        let after = writer.into_image().unwrap();
        assert_eq!(
            active_meta(&after).scope_table_root,
            root_before,
            "scope table rebuilt after {case}"
        );
    }
}
