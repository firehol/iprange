use std::fs;
use std::io::{Read, Write};
use std::time::{Duration, Instant};

use crate::allocation::{self, AllocationStats};

pub(crate) struct Timed {
    pub(crate) elapsed: Duration,
    pub(crate) allocations: AllocationStats,
}

pub(crate) fn operation<T>(callback: impl FnOnce() -> T) -> (T, Timed) {
    profiler_command(b"enable\n");
    let started = Instant::now();
    let (result, allocations) = allocation::measure(callback);
    let elapsed = started.elapsed();
    profiler_command(b"disable\n");
    (
        result,
        Timed {
            elapsed,
            allocations,
        },
    )
}

fn profiler_command(command: &[u8]) {
    let Some(path) = std::env::var_os("IPRANGE_PERF_CONTROL") else {
        return;
    };
    let mut control = fs::OpenOptions::new()
        .write(true)
        .open(&path)
        .unwrap_or_else(|error| panic!("open perf control {path:?}: {error}"));
    control
        .write_all(command)
        .unwrap_or_else(|error| panic!("write perf control {path:?}: {error}"));
    drop(control);
    if let Some(ack_path) = std::env::var_os("IPRANGE_PERF_ACK") {
        let mut ack = fs::File::open(&ack_path)
            .unwrap_or_else(|error| panic!("open perf acknowledgement {ack_path:?}: {error}"));
        let mut response = [0u8; 5];
        ack.read_exact(&mut response)
            .unwrap_or_else(|error| panic!("read perf acknowledgement {ack_path:?}: {error}"));
        assert_eq!(&response, b"ack\n\0", "unexpected perf acknowledgement");
    }
}
