//! Physical component floors used to classify SDK profiles.
//!
//! These kernels deliberately omit v4 semantics. They measure only mapped
//! memory access, search, construction, checksum, digest, or durability work;
//! no full SDK operation is expected to equal them.

use std::fs::{self, File, OpenOptions};
use std::hint::black_box;
use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};

use memmap2::{MmapMut, MmapOptions};
use sha2::{Digest, Sha512};

#[path = "update_ipsets/allocation.rs"]
mod allocation;
#[path = "update_ipsets/timing.rs"]
mod timing;

const PAGE_SIZE: usize = 4_096;
const SEARCH_KEYS: usize = 512;
const HEADER: &str =
    "scenario,units,bytes,elapsed_ns,units_per_second,alloc_calls,alloc_bytes,proof";
static NEXT_FILE: AtomicU64 = AtomicU64::new(0);

fn main() {
    if let Err(error) = run() {
        eprintln!("component-floor benchmark failed: {error}");
        std::process::exit(1);
    }
}

fn run() -> Result<(), String> {
    let arguments: Vec<String> = std::env::args()
        .skip(1)
        .filter(|argument| argument != "--bench")
        .collect();
    match arguments.first().map(String::as_str) {
        None | Some("suite") => run_suite(),
        Some("header") => {
            println!("{HEADER}");
            Ok(())
        }
        Some("case") if arguments.len() == 3 => {
            let units = arguments[2]
                .parse::<usize>()
                .map_err(|_| format!("invalid unit count {:?}", arguments[2]))?;
            println!("{HEADER}");
            run_case(&arguments[1], units)
        }
        Some("case") => Err("case requires: case SCENARIO UNITS".to_owned()),
        Some(other) => Err(format!(
            "unknown mode {other:?}; expected suite, header, or case"
        )),
    }
}

fn run_suite() -> Result<(), String> {
    println!("{HEADER}");
    for (name, units) in [
        ("mapped-record-scan", 1_000_000),
        ("fixed-page-search", 1_000_000),
        ("mapped-page-build", 16_384),
        ("page-crc32c", 16_384),
        ("mapped-file-sha512", 64 * 1024 * 1024),
        ("mapped-flush-sync", 16_384),
    ] {
        run_case(name, units)?;
    }
    Ok(())
}

fn run_case(name: &str, units: usize) -> Result<(), String> {
    if units == 0 {
        return Err("unit count must be positive".to_owned());
    }
    let result = match name {
        "mapped-record-scan" => mapped_record_scan(units)?,
        "fixed-page-search" => fixed_page_search(units)?,
        "mapped-page-build" => mapped_page_build(units)?,
        "page-crc32c" => page_crc32c(units)?,
        "mapped-file-sha512" => mapped_file_sha512(units)?,
        "mapped-flush-sync" => mapped_flush_sync(units)?,
        _ => return Err(format!("unknown component floor {name:?}")),
    };
    let rate = result.units as f64 / result.measurement.elapsed.as_secs_f64();
    println!(
        "{},{},{},{},{:.3},{},{},{}",
        name,
        result.units,
        result.bytes,
        result.measurement.elapsed.as_nanos(),
        rate,
        result.measurement.allocations.calls,
        result.measurement.allocations.bytes,
        result.proof,
    );
    Ok(())
}

struct FloorResult {
    units: usize,
    bytes: usize,
    proof: u64,
    measurement: timing::Timed,
}

fn mapped_record_scan(records: usize) -> Result<FloorResult, String> {
    let bytes = records
        .checked_mul(8)
        .ok_or_else(|| "mapped scan extent overflow".to_owned())?;
    let mut mapped = Mapping::new(bytes)?;
    for (index, record) in mapped.bytes.chunks_exact_mut(8).enumerate() {
        record.copy_from_slice(&(index as u64).to_le_bytes());
    }
    let (proof, measurement) = timing::operation(|| {
        mapped.bytes.chunks_exact(8).fold(0u64, |sum, record| {
            sum.wrapping_add(u64::from_le_bytes(record.try_into().unwrap()))
        })
    });
    Ok(FloorResult {
        units: records,
        bytes,
        proof: black_box(proof),
        measurement,
    })
}

fn fixed_page_search(queries: usize) -> Result<FloorResult, String> {
    let mut mapped = Mapping::new(PAGE_SIZE)?;
    for index in 0..SEARCH_KEYS {
        let offset = index * 4;
        mapped.bytes[offset..offset + 4].copy_from_slice(&(index as u32 * 2).to_le_bytes());
    }
    let keys = &mapped.bytes[..SEARCH_KEYS * 4];
    let (proof, measurement) = timing::operation(|| {
        let mut sum = 0u64;
        for query in 0..queries {
            let wanted = ((query.wrapping_mul(2_654_435_761) % (SEARCH_KEYS * 2)) as u32)
                .wrapping_add((query & 1) as u32);
            sum = sum.wrapping_add(lower_bound(keys, wanted) as u64);
        }
        sum
    });
    Ok(FloorResult {
        units: queries,
        bytes: PAGE_SIZE,
        proof: black_box(proof),
        measurement,
    })
}

#[inline]
fn lower_bound(keys: &[u8], wanted: u32) -> usize {
    let mut low = 0usize;
    let mut high = SEARCH_KEYS;
    while low < high {
        let middle = low + (high - low) / 2;
        let offset = middle * 4;
        let key = u32::from_le_bytes(keys[offset..offset + 4].try_into().unwrap());
        if key < wanted {
            low = middle + 1;
        } else {
            high = middle;
        }
    }
    low
}

fn mapped_page_build(pages: usize) -> Result<FloorResult, String> {
    let bytes = page_bytes(pages)?;
    let mut mapped = Mapping::new(bytes)?;
    mapped.bytes.fill(0xa5);
    let (proof, measurement) = timing::operation(|| {
        for (index, page) in mapped.bytes.chunks_exact_mut(PAGE_SIZE).enumerate() {
            page.fill(0);
            page[..8].copy_from_slice(&(index as u64).to_le_bytes());
        }
        mapped.bytes[bytes - PAGE_SIZE]
    });
    Ok(FloorResult {
        units: pages,
        bytes,
        proof: u64::from(black_box(proof)),
        measurement,
    })
}

fn page_crc32c(pages: usize) -> Result<FloorResult, String> {
    let bytes = page_bytes(pages)?;
    let mut mapped = Mapping::new(bytes)?;
    seed_bytes(&mut mapped.bytes);
    let (proof, measurement) = timing::operation(|| {
        mapped
            .bytes
            .chunks_exact(PAGE_SIZE)
            .fold(0u64, |sum, page| {
                sum.wrapping_add(u64::from(crc32c::crc32c(page)))
            })
    });
    Ok(FloorResult {
        units: pages,
        bytes,
        proof: black_box(proof),
        measurement,
    })
}

fn mapped_file_sha512(bytes: usize) -> Result<FloorResult, String> {
    let mut mapped = Mapping::new(bytes)?;
    seed_bytes(&mut mapped.bytes);
    let (proof, measurement) = timing::operation(|| {
        let digest = Sha512::digest(&mapped.bytes);
        u64::from_le_bytes(digest[..8].try_into().unwrap())
    });
    Ok(FloorResult {
        units: bytes,
        bytes,
        proof: black_box(proof),
        measurement,
    })
}

fn mapped_flush_sync(pages: usize) -> Result<FloorResult, String> {
    let bytes = page_bytes(pages)?;
    let mut mapped = Mapping::new(bytes)?;
    let (result, measurement) = timing::operation(|| -> std::io::Result<u64> {
        for (index, page) in mapped.bytes.chunks_exact_mut(PAGE_SIZE).enumerate() {
            page[0] = index as u8;
        }
        mapped.bytes.flush()?;
        mapped.file.sync_all()?;
        Ok(u64::from(mapped.bytes[bytes - PAGE_SIZE]))
    });
    Ok(FloorResult {
        units: pages,
        bytes,
        proof: black_box(result.map_err(|error| error.to_string())?),
        measurement,
    })
}

fn page_bytes(pages: usize) -> Result<usize, String> {
    pages
        .checked_mul(PAGE_SIZE)
        .ok_or_else(|| "mapped page extent overflow".to_owned())
}

fn seed_bytes(bytes: &mut [u8]) {
    for (index, byte) in bytes.iter_mut().enumerate() {
        *byte = index.wrapping_mul(131).wrapping_add(index >> 8) as u8;
    }
}

struct Mapping {
    file: File,
    bytes: MmapMut,
    name: PathBuf,
}

impl Mapping {
    fn new(bytes: usize) -> Result<Self, String> {
        let ordinal = NEXT_FILE.fetch_add(1, Ordering::Relaxed);
        let name = std::env::temp_dir().join(format!(
            "iprange-v4-component-floor-{}-{ordinal}",
            std::process::id()
        ));
        let file = OpenOptions::new()
            .read(true)
            .write(true)
            .create_new(true)
            .open(&name)
            .map_err(|error| error.to_string())?;
        file.set_len(bytes as u64)
            .map_err(|error| error.to_string())?;
        // SAFETY: the file remains open and unchanged in length for the mapping
        // lifetime, and this benchmark owns the newly created inode.
        let mapped = unsafe { MmapOptions::new().len(bytes).map_mut(&file) }
            .map_err(|error| error.to_string())?;
        Ok(Self {
            file,
            bytes: mapped,
            name,
        })
    }
}

impl Drop for Mapping {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.name);
    }
}
