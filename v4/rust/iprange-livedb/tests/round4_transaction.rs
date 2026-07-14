use iprange_livedb::page_store::{PageStore, VecPageStore};
use iprange_livedb::{Error, Ipv4Key, Result, Writer};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};

struct FaultStore {
    inner: VecPageStore,
    events: Arc<Mutex<Vec<&'static str>>>,
    fail_sync: Arc<AtomicBool>,
    fail_alloc: Arc<AtomicBool>,
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
            return Err(Error::State("injected allocation failure"));
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
        if self.fail_sync.swap(false, Ordering::Relaxed) {
            return Err(Error::State("injected sync failure"));
        }
        Ok(())
    }

    fn truncate(&mut self, new_total_pages: u32) -> Result<()> {
        self.inner.truncate(new_total_pages)
    }
}

type FaultControls = (
    Arc<Mutex<Vec<&'static str>>>,
    Arc<AtomicBool>,
    Arc<AtomicBool>,
);

fn open_fault_writer() -> (Writer<Ipv4Key>, FaultControls) {
    let created = Writer::<Ipv4Key>::create(0, 0).unwrap();
    let image = created.into_image().unwrap();
    let events = Arc::new(Mutex::new(Vec::new()));
    let fail_sync = Arc::new(AtomicBool::new(false));
    let fail_alloc = Arc::new(AtomicBool::new(false));
    let store = FaultStore {
        inner: VecPageStore::new(image),
        events: events.clone(),
        fail_sync: fail_sync.clone(),
        fail_alloc: fail_alloc.clone(),
    };
    let writer = Writer::<Ipv4Key>::open(Box::new(store)).unwrap();
    (writer, (events, fail_sync, fail_alloc))
}

#[test]
fn commit_flushes_data_before_publishing_metadata() {
    let (mut writer, (events, _, _)) = open_fault_writer();
    writer.set(Ipv4Key(1), Ipv4Key(10), 1).unwrap();
    events.lock().unwrap().clear();
    writer.commit(1, u64::MAX).unwrap();

    let events = events.lock().unwrap();
    let meta_index = events
        .iter()
        .position(|event| *event == "meta")
        .expect("commit did not publish metadata");
    let sync_before = events[..meta_index].contains(&"sync");
    let sync_after = events[meta_index + 1..].contains(&"sync");
    assert!(
        sync_before && sync_after,
        "commit events {events:?}, want data sync -> metadata publication -> metadata sync"
    );
}

#[test]
fn sync_failure_poisons_writer() {
    let (mut writer, (_, fail_sync, _)) = open_fault_writer();
    writer.set(Ipv4Key(1), Ipv4Key(10), 1).unwrap();
    fail_sync.store(true, Ordering::Relaxed);
    assert!(writer.commit(1, u64::MAX).is_err());
    assert!(
        writer.set(Ipv4Key(20), Ipv4Key(30), 2).is_err(),
        "writer accepted Set after a failed commit sync"
    );
    assert!(
        writer.commit(2, u64::MAX).is_err(),
        "writer accepted Commit after a failed commit sync"
    );
}

#[test]
fn allocation_failure_poisons_transaction() {
    let (mut writer, (_, _, fail_alloc)) = open_fault_writer();
    fail_alloc.store(true, Ordering::Relaxed);
    assert!(writer.set(Ipv4Key(1), Ipv4Key(10), 1).is_err());
    assert!(
        writer.commit(1, u64::MAX).is_err(),
        "Commit accepted a transaction after storage allocation failed"
    );
}
