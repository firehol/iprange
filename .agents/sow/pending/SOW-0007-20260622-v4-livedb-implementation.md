# SOW-0007 - iprange v4 live-DB format: implementation (Go + Rust)

## Status

Status: open

Sub-state: Authored 2026-06-22 after the v4 format spec was **locked** (SOW-0006,
`.agents/sow/specs/design-iprange-v4-livedb.md`). **Not started** — blocked on the
open implementation decisions (O1–O4 below). No code until the user decides them.

## Requirements

### Purpose

Implement the **locked** v4 live-DB format — a portable, mmap'd, mutable copy-on-write
B+tree of fixed-size `[from, to, scope]` records — in **Go** (the reference writer; the
update-ipsets daemon is the sole writer) and **Rust** (reader + writer for cross-read
conformance; also the C-facing library, decision O3 of SOW-0006). Two independent
implementations that **interoperate** (a file written by one is read identically by
the other) and **safely reject hostile input**, validated by a shared behavioral
conformance corpus, crash-recovery tests, and fuzzing.

This is the engine that fixes the update-ipsets retention performance problem:
per-IP removal becomes `O(log n)` (no cohort scan) and a change rewrites only
`O(log n + k)` pages (no full rebuild), while the dataset may exceed RAM (mmap).

### User Request

Costa: build the v4 live DB, then implement it. The format was locked through 9
external review rounds (SOW-0006). Earlier direction: "get bbolt code, change it for
ranges, port to rust, same tests pass." As the spec converged it became **simpler and
different from bbolt** (fixed-size records, no MVCC, derived allocator, double-meta
commit, flock) — so the implementation follows the **locked spec**, using bbolt (Go)
and redb (Rust) as **technique references** for COW/mmap/crash, not as a literal fork
(see O2).

### Assistant Understanding

Facts:
- The contract is `design-iprange-v4-livedb.md` (LOCKED). It fully specifies the byte
  layout, the COW + double-meta-page commit, the derived allocator, flock concurrency,
  mmap safety, the recursive `validate_node`, and the v3-export delegation.
- Conformance is **behavioral + cross-read** (O1 resolved): byte-identity is **not**
  required; Go and Rust MAY differ in tree shape but MUST return identical query
  results and read each other's files.
- The v3 format libraries already exist in `go/` and `rust/iprange-format/` (from
  SOW-0002/0003); v4 is a **new** structure, not an extension of those.
- Reference implementations to study (technique only): **bbolt** (Go COW B+tree, page
  meta-flip, crash model) and **redb** (pure-Rust COW B+tree, safe mmap). Cite as
  `owner/repo @ commit` when used as evidence.

Inferences:
- Go should be implemented first: it is the daemon's writer + the immediate consumer,
  and shaking out the spec in the reference language de-risks the Rust port.
- Because byte-identity is dropped, the two implementations need only agree on the
  **on-disk format** (so cross-read works) and on **query results** — not on tree
  shape — which is exactly what the conformance corpus tests.

Unknowns → the implementation decisions O1–O4 below (for the user).

### Acceptance Criteria

- **Go v4 library**: writer (`set`/`delete`/commit, COW + double-meta, derived
  allocator, flock) + reader (mmap, recursive `validate_node`, lookup/scan) +
  `export_v3` delegation. Open-change-close API per spec §11.
- **Rust v4 library**: the same, byte-interoperable with the Go files; exposes the
  C-facing library (O3).
- **Shared behavioral conformance corpus** (language-neutral, like `conformance/` for
  v3): scripted op-sequences → expected query results; **cross-read** (Go-written file
  read by Rust and vice versa, identical results); **malformed-input** cases hitting
  every §9 reject path.
- **Crash-recovery tests**: a commit interrupted at each barrier (before Barrier 1,
  between barriers, before/after Barrier 2) leaves the defined surviving tree; torn
  meta / orphan pages handled per §6.3/§6.4.
- **Property/fuzz**: random op-sequences checked against an in-memory interval-map
  **oracle** (same `lookup` results); `cargo-fuzz` `Reader::open` + Go `FuzzOpen` over
  the on-disk format never panic/loop/OOB and reject hostile input.
- Both implementations green on the shared corpus; sanitizer/Miri clean where
  applicable; the round-9 cosmetic P3s (worked examples in the spec) folded in.

## Analysis

Sources checked:
- `.agents/sow/specs/design-iprange-v4-livedb.md` (the locked contract).
- `.agents/sow/specs/binary-format-v3.md` (the v3 writer the export delegates to).
- Existing `go/`, `rust/iprange-format/`, `conformance/` (patterns to mirror).
- bbolt / redb (technique references — to be pinned by commit when used).

Risks:
- It is an embedded database: **crash-recovery and allocator/commit correctness** are
  the high-risk areas. Mitigation: mirror bbolt/redb's proven mechanics; exhaustive
  crash-injection tests; the behavioral oracle; fuzzing.
- A shared logic bug could make Go and Rust agree on a wrong answer. Mitigation: the
  in-memory oracle (independent of the B+tree) cross-checks query results.

## Pre-Implementation Gate

Status: needs-user-decision (O1–O4).

(The substantive gate — root-cause model, evidence, contracts, patterns, risk,
validation plan — will be completed when the user moves this SOW to `current/` and
resolves O1–O4. The locked spec already supplies the design; the gate below records
the decisions that shape the build.)

Open decisions (block implementation):

- **O1 — Language order.** (a) **Go first**, then Rust port *(recommended — Go is the
  daemon's sole writer + immediate consumer; shaking out the spec in the reference
  language de-risks the port)*; (b) Rust first; (c) both in parallel.
- **O2 — Implementation base.** (a) **Fresh implementation of the locked spec**,
  using bbolt (Go) and redb (Rust) as technique references *(recommended — the locked
  spec is fixed-record, no-MVCC, derived-allocator: simpler and materially different
  from bbolt's variable-length + MVCC + freelist; a literal fork would import
  machinery we deliberately don't want)*; (b) fork bbolt and modify it for ranges
  (the earlier idea — heavier, drags in MVCC/freelist).
- **O3 — Code location.** (a) New v4 modules **alongside** the v3 libs — `go/` (new
  files) + a new `rust/iprange-livedb/` crate *(recommended — clean separation; v4 is
  a different artifact from the v3 snapshot format)*; (b) inside the existing v3
  crates.
- **O4 — Phase 1 scope.** (a) **Core single-feed v4** first (writer + reader +
  conformance + crash-recovery + fuzz), `export_v3` and the update-ipsets retention
  integration as Phase 2 *(recommended — lock the engine before wiring it in)*;
  (b) include `export_v3` in Phase 1.

## Implications And Decisions

Pending user decisions O1–O4 (record here before implementation). Recommendations:
O1=Go-first, O2=fresh-impl, O3=alongside, O4=core-first.

## Plan (proposed; finalized after O1–O4)

1. **Go reference** — page/meta structs, CRC32C, fixed-record B+tree (search/insert/
   delete/split/merge), derived free-set, COW + double-meta commit, flock, mmap
   reader + recursive `validate_node`, `set`/`delete`/`lookup`/`scan`.
2. **Conformance harness + corpus** — language-neutral op-sequence cases → expected
   query results; crash-injection cases; malformed-input cases; the in-memory oracle.
3. **Rust port** — same on-disk format; cross-read against the Go-produced corpus;
   C-facing library.
4. **Fuzz + crash-recovery** — `cargo-fuzz`/`go test -fuzz` on `open`; barrier-
   interruption recovery tests; sanitizer/Miri.
5. **(Phase 2)** `export_v3` delegation + update-ipsets retention integration.

## Execution Log

Not started.

## Validation

Pending.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- M1–M4 tunables (page-size already fixed at 4096; split/fill thresholds, fsync
  policy) measured by **SOW-0005** once the engine runs.
- Test-hardening patterns (oracle, fuzz, malformed-input) from **SOW-0004** apply.
- Capture the conformance + crash-injection + benchmark harness as the project's
  first `project-*` runtime skill once it stabilizes (SOW-0001 deferred-skill note).

## Regression Log

None yet.
