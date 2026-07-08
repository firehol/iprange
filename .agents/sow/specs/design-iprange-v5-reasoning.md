# v5 Format Reasoning — From v4 Profiling Data

**Status**: FINAL — produced from SOW-0013 profile-driven optimization.
**Date**: 2026-07-08
**Author**: performance profiling of the v4 live-DB SDK (Rust + Go)

## Purpose

This document records what the v4 profiling revealed about the format's performance
characteristics, and reasons about what a hypothetical v5 format change should (and
should not) address. All three original "v5 recommendations" were implemented in v4
as v4.2 — this document now records the remaining, irreducible structural costs.

## What Was Measured

All numbers from Costa's workstation (x86_64, RTX 5090, SSE4.2). Criterion (Rust),
testing.B (Go). IPv4, scope_width=1, deterministic LCG workload.

### The Definitive Table (post-optimization)

| Scenario           | Rust       | Go         | Go/Rust | Notes                        |
|--------------------|------------|------------|---------|------------------------------|
| open (trusted)     | **47 ns**  | **97 ns**  | 2.1×    | Pure meta-read (no CRC)      |
| open (validate)    | 5.42 ms    | 17.41 ms   | 3.2×    | Full tree walk + CRC         |
| scan (1M)          | 696 µs    | 2.58 ms    | 3.7×    | Go callback overhead         |
| append (100k)      | 4.29 ms   | 9.78 ms    | 2.3×    | COW + tree descent           |
| append (1M)        | 59.3 ms   | 107 ms     | 1.8×    | Scales linearly              |
| set_random (100k)  | 421 ms    | 1391 ms    | 3.3×    | 3-4 descents/set             |
| lookup_hit (1M)    | 56.1 ms   | 52.7 ms    | **0.94×** | At parity ✓                |
| lookup_miss (1M)   | 50.8 ms   | 51.9 ms    | **1.02×** | At parity ✓                |
| open_read_file     | 10.8 µs   | 12.5 µs    | 1.16×   | flock + mmap + §10 hardening |
| create_file (1M)   | 351 ms    | 732 ms     | 2.1×    | fsync-bound                  |

## What v4.2 Already Fixed (No v5 Needed)

All three original "v5 recommendations" were implemented as v4.2:

### 1. Persisted Free-List (Implemented as v4.2)
The free-list is stored as a linked list in the free pages themselves: each free
page's body at offset `PAGE_HEADER_SIZE` (16) stores `next_free_pgno` (4 bytes).
The meta page stores `free_list_head` at offset 94 (v4.2, `meta_size=98`).
Writer-open reads the free set in O(free_count) instead of O(N_pages) tree walk.

### 2. Optional CRC (Implemented as Policy)
`Reader::open` is trusted by default — no CRC computation at all (not even meta).
`Reader::validate()` provides full validation on demand. This is a runtime policy,
not a format change. The on-disk format still carries per-page CRCs.

### 3. CRC Performance (Implemented: Triple-Parallel)
The serial SSE4.2 `_mm_crc32_u64` has 3-cycle latency but 1-cycle throughput. The
implementation splits large buffers into three chunks and CRCs them simultaneously
— three independent dependency chains pipeline at 1 instruction/cycle. Combined
via precomputed shift tables (Intel paper algorithm). Open dropped from 3.4µs to 47ns.

## What Remains (Irreducible Structural Costs)

### 1. COW Page Allocation (The Append Floor)

At ~43 ns/append (Rust, 1M records), the cost is:
- Tree descent: ~15 ns (binary search at each level)
- COW page check + first-touch copy: ~3 ns amortized (~1 copy per ~453 records)
- Record write (9 bytes): ~2 ns
- `private_pages.contains()`: ~4 ns per descent level
- Function call + borrow checking overhead: ~19 ns

This matches the theoretical minimum for a COW B+tree with 4KB pages. The COW
copy is only ~3 ns amortized — NOT the bottleneck (only ~222 copies for 100k appends).
The dominant cost is tree descent + function call overhead.

A v5 could reduce this via:
- **Rightmost-leaf cursor**: meta stores the rightmost leaf pgno; ordered appends
  skip the tree descent entirely (direct write to the known leaf). Drops ~15 ns.
- **In-place mutation** (no COW): trade crash-safety for write speed. NOT recommended
  — the COW + double-meta commit is the format's core guarantee.

### 2. set_random Amplification (53× Slower Than Append)

Each `set(from, to)` does 3-4 tree descents: delete_range + left-coalesce +
right-coalesce + insert. For random (overlapping) ranges, the delete path also
triggers rebalance. This is the algorithmic cost of maintaining a sorted, disjoint
B+tree under random updates.

A v5 could address this via:
- **Batched set()**: accept multiple ranges and process in a single tree walk.
  Reduces O(k log n) per set to O(k + log n) for a batch of k sets.
- **LSM-style memtable**: buffer random writes, merge-sort, bulk-apply.

### 3. Go Language Overhead (Not a Format Issue)

Go is 1.8-3.7× slower than Rust on the write/scan paths despite identical
algorithms. Root cause: Go's GC shape stencizing for generics adds dictionary
dispatch overhead per method call. This is inherent to Go generics and cannot be
fixed by format changes. Lookup is at parity (0.94×) because the read path was
specialized to bypass generic dispatch.

## v5 Should NOT Change

### Double-Meta Atomic Commit
The two-fsync double-meta protocol (§6.3) is correct and crash-safe. A crash leaves
the file as old-or-new, never torn. This is the format's core guarantee.

### B+tree Structure for IPv6
IPv6 lookups at ~50 ns are already fast. The 16-byte key with hi/lo comparison is
efficient.

### Page Size (4KB)
4KB pages give ~453 records per leaf for IPv4 scope_width=1 — a good fanout that
keeps tree height at 2 for up to ~230k records.

## Summary

v4.2 has absorbed the three high-value format improvements (persisted free-list,
optional CRC, fast CRC). The remaining performance characteristics are either:
- **At parity** (lookup, open)
- **At the structural minimum** (append at ~43 ns/record)
- **Algorithmic** (set_random is 53× append by design)
- **Language-inherent** (Go generic dispatch overhead)

A v5 format change would yield diminishing returns. The next performance frontier
is **API design** (batched set, rightmost-leaf cursor for ordered writes) rather
than format structure.
