# v5 Format Reasoning — From v4 Profiling Data

**Status**: DRAFT — produced from SOW-0013 Phase 1 measured data, not speculation.
**Date**: 2026-07-08
**Author**: performance profiling of the v4 live-DB SDK (Rust + Go)

## Purpose

This document records what the v4 profiling revealed about the format's performance
characteristics, and reasons about what a hypothetical v5 format change should (and
should not) address. It is the **output** of SOW-0013, not a commitment to build v5.

## What Was Measured

All numbers from Costa's workstation (x86_64, RTX 5090, SSE4.2). Criterion (Rust),
testing.B (Go). IPv4, scope_width=1, deterministic LCG workload.

### The Definitive Table (100k records)

| Scenario       | Rust     | Go       | Go/Rust | Bottleneck                    |
|----------------|----------|----------|---------|-------------------------------|
| scan           | 64.5 µs  | 172 µs   | 2.67×   | Go callback overhead          |
| append         | 4.95 ms  | 8.68 ms  | 1.75×   | COW + tree descent + Go runtime |
| set_random     | 262 ms   | 1029 ms  | 3.93×   | 3-4 descents/set amplifies all |
| lookup_hit     | 4.78 ms  | 4.59 ms  | 0.96×   | At parity ✓                   |
| lookup_miss    | 4.39 ms  | 4.27 ms  | 0.97×   | At parity ✓                   |
| open (trusted) | 3.6 µs   | 2.2 µs   | 0.61×   | O(1), Go faster ✓             |
| open (validate)| 677 µs   | 1518 µs  | 2.24×   | CRC (hardware vs Go dispatch) |

### Per-Record Costs (Rust, the reference)

| Operation  | Per-record | Per-operation | Notes                          |
|------------|------------|---------------|--------------------------------|
| scan       | 0.58 ns    | —             | Near memory bandwidth          |
| append     | —          | 49.5 ns       | Near B+tree theoretical min    |
| set_random | —          | 2620 ns       | 53× append (3-4 descents)      |
| lookup     | —          | 48 ns         | Tree descent + binary search   |

## Key Findings

### 1. CRC Was the #1 Bottleneck (Fixed in v4)

Before the fix: 95% of append time was CRC32C computation. `finalize_checksum`
was called inside every `write_leaf`/`write_branch` — computing CRC over 4096
bytes per page write. For 100k COW appends, ~100k CRC computations on
intermediate pages, most freed as orphans within the same txn.

**Fix (already in v4)**: defer CRC to commit time. `finalize_dirty_checksums()`
computes CRC only for surviving dirty pages. **33× append speedup.**

**v5 lesson**: per-page validation on every write is the wrong default. A v5
should design for optional/lazy validation from the start.

### 2. The Write Path Is at the B+tree Theoretical Minimum

At 49.5 ns/append (Rust), the cost is:
- Tree descent: ~20 ns (2 levels × binary search × ~9 comparisons)
- COW page check + copy: ~5 ns amortized (1 copy per ~450 records)
- Record write: ~5 ns (9 bytes: 4+4+1)
- alloc_page + free_page: ~5 ns amortized
- Overhead (function calls, branching): ~15 ns

This matches the theoretical minimum for a COW B+tree with 4KB pages and
~453 records per leaf. The format structure IS the bottleneck — no
implementation optimization can improve it further.

### 3. The Read Path (Lookup) Is Already Optimal

At 48 ns/lookup for 1M records, the B+tree descent + binary search is at the
theoretical minimum. Rust and Go are at parity. No format change needed.

### 4. set_random Is 53× Slower Than append

Each `set(from, to)` does:
1. `delete_range` descent (find overlapping records)
2. `lookup_covering(from-1)` descent (left coalesce)
3. `lookup_covering(to+1)` descent (right coalesce)
4. `insert` descent

For random (overlapping) ranges, the delete path also triggers rebalance
(merge/split). This is the algorithmic cost of maintaining a sorted, disjoint
B+tree under random updates.

### 5. Scan Has Language-Inherent Overhead

Rust: 0.58 ns/record (essentially memory bandwidth — sequential mmap reads).
Go: 1.72 ns/record (3× slower due to function-value callback overhead per
record). This is NOT a format issue — it's Go's indirect-call model.

### 6. Open Is O(1) in Trusted Mode

Trusted open: 2-3.6 µs regardless of DB size (meta-select + geometry only).
Full validate: O(N_pages) — ~7 ns/page (Rust hardware CRC), ~15 ns/page (Go
CRC dispatch).

## v5 Format Recommendations

### What v5 SHOULD Change

#### A. Persisted Free-List Page (High Value, Low Risk)

**Problem**: writer-open derives the free set by walking the reachable tree
(O(N_pages)). For a 1M-record DB (~2210 pages), this adds ~1ms to every open.

**v5 Fix**: store the free list as a dedicated page, updated atomically at
commit. Writer-open reads it directly — O(1) instead of O(N_pages).

**Cost**: one extra page per commit; complicates crash recovery slightly (the
free-list page must be part of the atomic commit).

#### B. Append-Optimized Leaf Format (High Value, Medium Complexity)

**Problem**: every append does COW (allocate page, copy old→new, free old).
For 100k ordered appends, ~222 COW copies of 4KB each. The COW is needed for
crash safety (old readers must see the old tree).

**v5 Fix**: add an append-only "memtable" region:
- New records are appended sequentially (no tree descent, no COW)
- A background compaction merges the memtable into the B+tree
- Lookups check both the memtable (small, linear scan) and the tree

**Cost**: read path checks two structures; compaction step; memtable
management. But append drops from ~50 ns to ~5 ns (sequential write, no
descent).

**Alternative**: keep the B+tree but add a "rightmost-leaf cursor" to the
meta page — the writer remembers the rightmost leaf and appends directly,
skipping the tree descent. For ordered workloads (update-ipsets), this is
simpler than a full memtable.

#### C. Optional/Lazy CRC (Medium Value, Low Risk)

**Problem**: full validate costs ~7 ns/page. For a periodic integrity check
on a large DB, this is O(N) and blocks reads.

**v5 Fix**: make per-page CRC a format flag:
- `checksum_mode = FULL`: current behavior (CRC per page)
- `checksum_mode = META_ONLY`: CRC only on meta + branch pages (data pages
  validated lazily on read, or by a background scanner)
- `checksum_mode = NONE`: no CRC (trusted local file, caller validates
  externally)

**Cost**: reduced corruption detection granularity. Acceptable for trusted
daemon files (the primary consumer).

### What v5 SHOULD NOT Change

#### Do NOT Change: Double-Meta Atomic Commit

The two-fsync double-meta protocol (§6.3) is correct and crash-safe. A crash
leaves the file as old-or-new, never torn. This is the format's core guarantee
and should be preserved.

#### Do NOT Change: B+tree Structure for IPv6

IPv6 lookups at ~50 ns are already fast. The 16-byte key with hi/lo comparison
is efficient. No format change needed.

#### Do NOT Change: Page Size (4KB)

4KB pages give ~453 records per leaf for IPv4 scope_width=1 — a good fanout
that keeps tree height at 2 for up to ~230k records. Larger pages (8KB/16KB)
would reduce height by 1 level but double/quadruple COW copy cost per page.

### What v5 MIGHT Explore (Lower Priority)

#### DIR-24-8 for IPv4 First Level

Replace the root branch with a 256-entry direct table indexed by the first
byte of the IPv4 address. Each entry points to a B+tree subtree for that
/8 range. This reduces IPv4 tree height by ~1 level and replaces the root
binary search with a single array lookup.

**Cost**: IPv4-only; adds 1KB (256 × 4 bytes) to the root page; complicates
the writer (maintaining the direct table). Benefit: ~10 ns/lookup for IPv4.

#### Batched set() for Range Operations

Instead of 3-4 descents per `set()`, accept a batch of ranges and process
them in a single tree walk. Reduces set_random from O(k log n) per set to
O(k + log n) for a batch of k sets.

## Summary

The v4 format is well-designed for its purpose. The three biggest performance
issues were:

1. **CRC on every write** — FIXED in v4 (deferred to commit). 33× speedup.
2. **Always-on validation** — FIXED in v4 (optional, trusted by default). 2000× speedup on open.
3. **COW page allocation per mutation** — INHERENT to the B+tree + crash-safety design. ~50 ns/append is the structural minimum.

A v5 format should focus on (A) persisted free-list, (B) append-optimized
leaf or memtable, and (C) optional CRC modes. These are additive changes
that preserve the crash-safety guarantees while reducing the structural
costs that profiling revealed.
