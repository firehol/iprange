use iprange_livedb::extsort::{ExtSortConfig, ExtSorter};
use iprange_livedb::Ipv4Key;

fn temp_dir(label: &str) -> std::path::PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "iprange-round5-{label}-{}-{:?}",
        std::process::id(),
        std::thread::current().id()
    ));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

#[test]
fn spill_failure_is_terminal() {
    let parent = temp_dir("spill-terminal");
    let temp_dir = parent.join("missing");
    let mut sorter = ExtSorter::<Ipv4Key>::new(ExtSortConfig {
        chunk_size: 1,
        temp_dir: Some(temp_dir.clone()),
    });
    assert!(
        sorter.add(Ipv4Key(10), Ipv4Key(10), 1).is_err(),
        "spill unexpectedly succeeded into a missing directory"
    );
    std::fs::create_dir(&temp_dir).unwrap();
    assert!(
        sorter.add(Ipv4Key(20), Ipv4Key(20), 2).is_err(),
        "sorter accepted Add after a spill I/O failure"
    );
    assert!(
        sorter.finish().is_err(),
        "sorter accepted Finish after a spill I/O failure"
    );
    assert_eq!(
        std::fs::read_dir(&temp_dir).unwrap().count(),
        0,
        "terminal spill failure left files behind"
    );
    let _ = std::fs::remove_dir_all(parent);
}

#[test]
fn multi_pass_failure_cleans_every_owned_run() {
    let dir = temp_dir("multipass-cleanup");
    let mut sorter = ExtSorter::<Ipv4Key>::new(ExtSortConfig {
        chunk_size: 1,
        temp_dir: Some(dir.clone()),
    });
    for i in 0..40u32 {
        sorter.add(Ipv4Key(i * 2), Ipv4Key(i * 2), i + 1).unwrap();
    }
    let mut paths: Vec<_> = std::fs::read_dir(&dir)
        .unwrap()
        .map(|entry| entry.unwrap().path())
        .collect();
    paths.sort();
    assert_eq!(paths.len(), 40, "fixture must force a multi-pass merge");
    std::fs::OpenOptions::new()
        .write(true)
        .open(&paths[0])
        .unwrap()
        .set_len(19)
        .unwrap();
    assert!(
        sorter.finish().is_err(),
        "Finish accepted a truncated run during multi-pass merge"
    );
    let leaked: Vec<_> = std::fs::read_dir(&dir)
        .unwrap()
        .map(|entry| entry.unwrap().file_name())
        .collect();
    assert!(
        leaked.is_empty(),
        "failed multi-pass merge leaked {} owned spill files: {leaked:?}",
        leaked.len()
    );
    let _ = std::fs::remove_dir_all(dir);
}
