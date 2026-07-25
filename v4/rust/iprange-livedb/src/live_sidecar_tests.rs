use std::fs;
use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use super::*;

struct TestFiles {
    main: PathBuf,
    extra: Vec<PathBuf>,
}

impl TestFiles {
    fn new(label: &str) -> Self {
        let unique = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        Self {
            main: std::env::temp_dir().join(format!(
                "iprange-v4-sidecar-{label}-{}-{unique}",
                std::process::id()
            )),
            extra: Vec::new(),
        }
    }

    fn create_main(&self) {
        create_private(&self.main).unwrap();
    }

    fn sidecar(&self) -> PathBuf {
        path::canonical_sidecar(&self.main).unwrap()
    }

    fn track(&mut self, path: PathBuf) {
        self.extra.push(path);
    }
}

impl Drop for TestFiles {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.main);
        let _ = fs::remove_file(self.sidecar());
        for path in &self.extra {
            let _ = fs::remove_file(path);
        }
    }
}

fn create_ready(files: &TestFiles, capacity: u32) -> Sidecar {
    files.create_main();
    let sidecar = Sidecar::create(&files.main, [1; 16], [2; 16], capacity).unwrap();
    sidecar.publish_ready().unwrap();
    sidecar
}

#[test]
fn ready_sidecar_reopens_with_exact_binding() {
    let files = TestFiles::new("open");
    let sidecar = create_ready(&files, 7);

    let reopened = Sidecar::open(&files.main, [1; 16]).unwrap();
    assert_eq!(reopened.header.capacity, 7);
    assert_eq!(reopened.header.sidecar_id, [2; 16]);
    reopened.verify_path().unwrap();
    drop(sidecar);

    assert!(matches!(
        Sidecar::open(&files.main, [3; 16]),
        Err(Error::WrongMode(_))
    ));
}

#[test]
fn creating_and_malformed_sidecars_are_rejected() {
    let files = TestFiles::new("malformed");
    files.create_main();
    let sidecar = Sidecar::create(&files.main, [1; 16], [2; 16], 2).unwrap();
    assert!(matches!(
        Sidecar::open(&files.main, [1; 16]),
        Err(Error::WrongState(_))
    ));

    sidecar.publish_ready().unwrap();
    sidecar.file.set_len(PAGE_SIZE as u64).unwrap();
    assert!(matches!(
        Sidecar::open(&files.main, [1; 16]),
        Err(Error::Corrupt(_))
    ));
}

#[test]
fn one_writer_owns_the_database() {
    let files = TestFiles::new("writer");
    let first = create_ready(&files, 2);
    let second = Sidecar::open(&files.main, [1; 16]).unwrap();

    first.claim_writer().unwrap();
    assert!(matches!(second.claim_writer(), Err(Error::WriterBusy)));
    drop(first);
    second.claim_writer().unwrap();
}

#[test]
fn reader_slots_report_capacity_scan_and_reuse() {
    let files = TestFiles::new("readers");
    let scanner = create_ready(&files, 2);
    let first = Sidecar::open(&files.main, [1; 16]).unwrap();
    let second = Sidecar::open(&files.main, [1; 16]).unwrap();
    let exhausted = Sidecar::open(&files.main, [1; 16]).unwrap();

    let first_slot = first.claim_reader(7).unwrap();
    let second_slot = second.claim_reader(11).unwrap();
    assert_ne!(first_slot, second_slot);
    assert!(matches!(
        exhausted.claim_reader(13),
        Err(Error::ReaderCapacityExhausted)
    ));

    let mut active = Vec::new();
    scanner
        .scan_readers(|txn| {
            active.push(txn);
            Ok(())
        })
        .unwrap();
    active.sort_unstable();
    assert_eq!(active, [7, 11]);

    first.release_reader(first_slot).unwrap();
    let reused = exhausted.claim_reader(13).unwrap();
    assert_eq!(reused, first_slot);
    exhausted.release_reader(reused).unwrap();
    second.release_reader(second_slot).unwrap();
}

#[test]
fn stale_slot_bytes_are_cleared_before_reuse() {
    let files = TestFiles::new("stale");
    let sidecar = create_ready(&files, 1);
    let offset = slot_offset(0).unwrap();
    file_io::write_exact_at(&sidecar.file, &[0x5a; SLOT_SIZE as usize], offset).unwrap();

    sidecar.scan_readers(|_| unreachable!()).unwrap();
    let mut slot = [0xff; SLOT_SIZE as usize];
    file_io::read_exact_at(&sidecar.file, &mut slot, offset).unwrap();
    assert_eq!(slot, [0; SLOT_SIZE as usize]);
}

#[test]
fn malformed_or_future_active_slots_fail_closed() {
    let files = TestFiles::new("active-malformed");
    let scanner = create_ready(&files, 1);
    let owner = Sidecar::open(&files.main, [1; 16]).unwrap();
    let slot = owner.claim_reader(7).unwrap();
    let offset = slot_offset(slot).unwrap();

    file_io::write_exact_at(&owner.file, &[0x5a; SLOT_SIZE as usize], offset).unwrap();
    assert!(matches!(scanner.scan_at_most(7), Err(Error::Corrupt(_))));

    owner.release_reader(slot).unwrap();
    let slot = owner.claim_reader(8).unwrap();
    assert!(matches!(scanner.scan_at_most(7), Err(Error::Corrupt(_))));
    owner.release_reader(slot).unwrap();
}

#[test]
fn replacement_at_the_canonical_path_is_detected() {
    let mut files = TestFiles::new("replace");
    let sidecar = create_ready(&files, 1);
    let old = files.sidecar().with_extension("readers.old");
    fs::rename(files.sidecar(), &old).unwrap();
    files.track(old);
    create_private(&files.sidecar()).unwrap();

    assert!(matches!(sidecar.verify_path(), Err(Error::WrongMode(_))));
}

#[test]
fn symlinks_are_not_followed() {
    #[cfg(unix)]
    {
        use std::os::unix::fs::symlink;

        let mut files = TestFiles::new("symlink");
        files.create_main();
        let target = files.main.with_extension("target");
        create_private(&target).unwrap();
        files.track(target.clone());
        symlink(&target, files.sidecar()).unwrap();

        let result = Sidecar::open(&files.main, [1; 16]);
        assert!(matches!(result, Err(Error::Io(_))));
    }
}

#[test]
fn sidecar_path_has_a_parent_for_durability_sync() {
    let files = TestFiles::new("parent-sync");
    let sidecar = create_ready(&files, 1);
    sync_parent(Path::new(&sidecar.path)).unwrap();
}
