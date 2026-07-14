#![cfg(unix)]

use iprange_livedb::extsort::{ExtSortConfig, ExtSorter};
use iprange_livedb::os::{FileWriter, MmapReader};
use iprange_livedb::spec;
use iprange_livedb::wire::Meta;
use iprange_livedb::Ipv4Key;
use std::path::{Path, PathBuf};

#[test]
fn create_file_validates_before_replacing_existing_file() {
    let dir = round4_temp_dir("create-preserve");
    let path = dir.join("existing.iprdb");
    let expected = b"preserve existing data";
    std::fs::write(&path, expected).unwrap();
    let result = FileWriter::<Ipv4Key>::create(&path, 99, 0);
    let accepted = result.is_ok();
    if let Ok(writer) = result {
        writer.close();
    }
    let actual = std::fs::read(&path).unwrap();
    let _ = std::fs::remove_dir_all(dir);
    assert!(
        !accepted && actual == expected,
        "FileWriter::create invalid mode: accepted={accepted} preserved={}",
        actual == expected
    );
}

#[test]
fn writable_open_rejects_invalid_geometry_without_changing_file() {
    let dir = round4_temp_dir("open-geometry");
    let path = dir.join("db.iprdb");
    let mut writer = FileWriter::<Ipv4Key>::create(&path, spec::SCOPE_MODE_SCALAR, 0).unwrap();
    for i in 0..40_000u32 {
        writer.set(Ipv4Key(i * 3), Ipv4Key(i * 3), i).unwrap();
    }
    writer.commit(1).unwrap();
    writer.close();

    let original = std::fs::read(&path).unwrap();
    assert!(original.len() > 66 * spec::PAGE_SIZE);
    let mut forged = original.clone();
    let active = active_meta_page(&forged);
    let base = active * spec::PAGE_SIZE;
    let mut meta = Meta::decode(&forged[base..base + spec::PAGE_SIZE]);
    meta.total_pages = 2;
    meta.encode_into(&mut forged[base..base + spec::PAGE_SIZE]);
    std::fs::write(&path, &forged).unwrap();

    let result = FileWriter::<Ipv4Key>::open(&path);
    let accepted = result.is_ok();
    if let Ok(writer) = result {
        writer.close();
    }
    let after = std::fs::read(&path).unwrap();
    let _ = std::fs::remove_dir_all(dir);
    assert!(!accepted, "writable open accepted impossible total_pages");
    assert_eq!(
        after, forged,
        "writable open changed the file while rejecting invalid geometry"
    );
}

#[test]
fn mmap_open_rejects_invalid_pinned_metadata() {
    for case in ["scope-mode", "empty-root-with-height"] {
        let dir = round4_temp_dir(case);
        let path = dir.join("db.iprdb");
        FileWriter::<Ipv4Key>::create(&path, spec::SCOPE_MODE_SCALAR, 0)
            .unwrap()
            .close();
        let mut raw = std::fs::read(&path).unwrap();
        let active = active_meta_page(&raw);
        let base = active * spec::PAGE_SIZE;
        let mut meta = Meta::decode(&raw[base..base + spec::PAGE_SIZE]);
        match case {
            "scope-mode" => meta.scope_mode = 99,
            "empty-root-with-height" => {
                meta.root_pgno = 0;
                meta.tree_height = 1;
            }
            _ => unreachable!(),
        }
        meta.encode_into(&mut raw[base..base + spec::PAGE_SIZE]);
        std::fs::write(&path, raw).unwrap();
        let result = MmapReader::open(&path);
        let _ = std::fs::remove_dir_all(dir);
        assert!(
            result.is_err(),
            "MmapReader accepted invalid {case} metadata"
        );
    }
}

#[test]
fn external_merge_uses_bounded_file_descriptors() {
    if std::env::var_os("IPRANGE_ROUND4_RUST_FD_HELPER").is_some() {
        set_fd_limit(64);
        run_bounded_fd_sort();
        return;
    }

    let output = std::process::Command::new(std::env::current_exe().unwrap())
        .args([
            "--exact",
            "external_merge_uses_bounded_file_descriptors",
            "--nocapture",
        ])
        .env("IPRANGE_ROUND4_RUST_FD_HELPER", "1")
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "external merge failed under RLIMIT_NOFILE=64:\nstdout:\n{}\nstderr:\n{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

fn run_bounded_fd_sort() {
    let dir = round4_temp_dir("fd-helper");
    let mut sorter = ExtSorter::<Ipv4Key>::new(ExtSortConfig {
        chunk_size: 1,
        temp_dir: Some(dir.clone()),
    });
    for i in 0..100u32 {
        sorter.add(Ipv4Key(i * 2), Ipv4Key(i * 2), 1).unwrap();
    }
    let mut stream = sorter.finish().unwrap();
    while stream.next().is_some() {}
    assert!(stream.err().is_none());
    drop(stream);
    assert_eq!(std::fs::read_dir(&dir).unwrap().count(), 0);
    let _ = std::fs::remove_dir_all(dir);
}

fn set_fd_limit(target: libc::rlim_t) {
    let mut limit = libc::rlimit {
        rlim_cur: 0,
        rlim_max: 0,
    };
    let get_result = unsafe { libc::getrlimit(libc::RLIMIT_NOFILE, &mut limit) };
    assert_eq!(get_result, 0, "getrlimit failed");
    limit.rlim_cur = limit.rlim_cur.min(target);
    let set_result = unsafe { libc::setrlimit(libc::RLIMIT_NOFILE, &limit) };
    assert_eq!(set_result, 0, "setrlimit failed");
}

fn active_meta_page(image: &[u8]) -> usize {
    let first = Meta::decode(&image[..spec::PAGE_SIZE]);
    let second = Meta::decode(&image[spec::PAGE_SIZE..2 * spec::PAGE_SIZE]);
    if first.txn_id >= second.txn_id {
        0
    } else {
        1
    }
}

fn round4_temp_dir(label: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "iprange-round4-{label}-{}-{:?}",
        std::process::id(),
        std::thread::current().id()
    ));
    remove_if_present(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

fn remove_if_present(path: &Path) {
    match std::fs::remove_dir_all(path) {
        Ok(()) => {}
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => {}
        Err(err) => panic!("remove {}: {err}", path.display()),
    }
}
