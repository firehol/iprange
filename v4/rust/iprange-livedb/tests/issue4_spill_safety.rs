//! Issue 4: spill files must be unique across processes.
//!
//! `RUN_COUNTER` is process-local, so two processes both start it at 0. Without
//! the PID in the spill filename, both processes write `iprange_extsort_0_0` —
//! and the old `create(true).truncate(true)` silently overwrote whichever
//! spilled first, corrupting its data. The fix embeds the PID in the name and
//! uses `create_new(true)` so any residual collision is a hard error.
//!
//! This is the cross-process proof: two forked children (each with its OWN pid)
//! spill into the SAME temp dir. Before the fix, both wrote the same filename
//! and one spill was lost; after the fix, two distinct spill files survive.
//!
//! Fork-based: run with `--test-threads 1` (forking inside a multithreaded test
//! process is only safe when serialized — mirrors tests/multiprocess_test.rs).

#![cfg(feature = "os")]

use iprange_livedb::extsort::{ExtSortConfig, ExtSorter};
use iprange_livedb::key::Ipv4Key;
use std::path::PathBuf;

fn shared_dir(tag: &str) -> PathBuf {
    let p = std::env::temp_dir().join(format!("iprange_issue4_{}_{}", tag, std::process::id(),));
    let _ = std::fs::remove_dir_all(&p);
    std::fs::create_dir_all(&p).unwrap();
    p
}

/// Forked child: spill ONE record (chunk_size=1 forces a spill on the first
/// add) into the shared dir, then _exit without running destructors. The spill
/// file persists so the parent can inspect it.
unsafe fn spill_child(dir: &str, key: u32, scope: u32) -> ! {
    let mut sorter = ExtSorter::<Ipv4Key>::new(ExtSortConfig {
        chunk_size: 1,
        temp_dir: Some(PathBuf::from(dir)),
    });
    let _ = sorter.add(Ipv4Key(key), Ipv4Key(key), scope);
    // Intentionally do NOT call finish(): we only want the spill file on disk.
    // _exit skips destructors so the spill file is not removed.
    unsafe {
        libc::_exit(0);
    }
}

#[test]
fn cross_process_spill_files_do_not_collide() {
    let dir = shared_dir("xprocess");
    let dir_str = dir.to_string_lossy().into_owned();

    let run_child = |key: u32, scope: u32| {
        let pid = unsafe { libc::fork() };
        assert!(pid >= 0, "fork failed");
        if pid == 0 {
            unsafe {
                spill_child(&dir_str, key, scope);
            }
        }
        let mut status = 0;
        unsafe {
            libc::waitpid(pid, &mut status, 0);
        }
        assert!(libc::WIFEXITED(status), "child did not exit normally");
        assert_eq!(libc::WEXITSTATUS(status), 0, "spill child failed");
    };

    // Two children, sequential (serialized so each fork sees a quiescent
    // process). Both spill into the SAME dir with process-local counter=0.
    run_child(0, 100);
    run_child(10, 200);

    // Both spill files MUST survive. Before the fix (no PID in the name), both
    // children produced the same filename and the second truncated the first →
    // only one file remained. After the fix, the PID distinguishes them.
    let mut spill_files: Vec<PathBuf> = std::fs::read_dir(&dir)
        .unwrap()
        .filter_map(|e| e.ok().map(|e| e.path()))
        .filter(|p| {
            p.file_name()
                .map(|n| n.to_string_lossy().starts_with("iprange_extsort_"))
                .unwrap_or(false)
        })
        .collect();
    spill_files.sort();

    assert!(
        spill_files.len() >= 2,
        "cross-process spill collision: expected >=2 distinct spill files, found {}; \
         a second process overwrote the first (PID missing from spill filename)",
        spill_files.len()
    );

    // Cleanup.
    let _ = std::fs::remove_dir_all(&dir);
}
