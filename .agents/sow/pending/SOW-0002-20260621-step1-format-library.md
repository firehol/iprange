# SOW-0002 - Step 1: binary-format library (read + write) — Rust + Go

## Status

Status: open

Sub-state: Re-sliced after the design review. Byte-level format spec drafted
(`.agents/sow/specs/binary-format-v3.md`). Awaiting user go-ahead to implement.
No code started. Parent: SOW-0001.

## Requirements

### Purpose

Step 1 of the iprange rewrite: the **binary-format library** — read + write the
portable, sectioned, mmap-able v3 format — in **both Rust and Go**, producing
**byte-identical** files. It is the foundation the engine (Step 2) and CLI
(Step 3) build on. Format contract: `.agents/sow/specs/binary-format-v3.md`.

### User Request

"First step: a file-format library, for reading and writing. Reading should have
two modes: minimal metadata, and full ready-for-processing." (Now three read
modes — §Acceptance.) Plus Costa's decisions (SOW-0001 review outcomes):
keep a full Go library (writer included); reserve signatures but defer signing;
measure Go vs Rust early.

### Assistant Understanding

Facts (from SOW-0001 decisions + design review):
- Two full native libraries (Rust + Go), 100% behavioral parity; Go is pure (no
  cgo); native C dropped (Rust exposes a C library for C consumers).
- The format spec is the gate; it is drafted and must be reviewed/locked first.
- Signing is deferred — only the signature **slot** is reserved.
- IP keys are little-endian integers compared numerically (v6 = two `u64`, `hi`
  then `lo`) → **no 128-bit integer needed on the lookup hot path** (handles Go's
  missing `u128`).

Unknowns: a few format sub-decisions flagged "OPEN" in the format spec §17.

### Acceptance Criteria

Delivered in **both Rust and Go** — byte-identical files; Rust is the reference,
Go matches. Signing is **not** implemented (slot reserved only).

- Format spec `binary-format-v3.md` reviewed and locked.
- **Writer:** streaming body + fixed-header backpatch; sections; little-endian
  scalars; little-endian integer IP keys; dual-stack; reserves the (empty)
  signature section.
- **Reader, three modes:** metadata-only · mmap read-only (zero-alloc numeric
  binary-search lookups) · owned-mutable.
- **Safety (normative, format spec §15):** structural bounds-checks with
  overflow-safe arithmetic + resource caps; malformed/hostile input never crashes
  or over-allocates. Verified by fuzz/negative tests + ASan/Miri.
- **Cross-language conformance:** a shared corpus proves Rust and Go produce
  byte-identical files and read each other's output.
- **Early speed check:** measure Rust vs Go on write + lookup on a large corpus
  (first data point for the parity/Go-viability decision).

## Analysis

Sources: `binary-format-v3.md`; SOW-0001 grounding + review outcomes; legacy
`src/ipset_binary.c`/`ipset6_binary.c`/`uint128.h`; prior art (MMDB/FlatBuffers).

Risks: format churn if the spec isn't locked first (mitigate: 1A gate);
Go performance (mitigate: measure in 1C, willing to drop Go writer per SOW-0001);
pure-Go mmap is platform-specific + uses `unsafe` (treat the Go reader as
security-critical, fuzz it).

## Pre-Implementation Gate

Status: ready (pending final lock of format-spec OPEN items, §17)

Problem/model, evidence, contracts, reuse, risk, sensitive-data plan: as recorded
in SOW-0001's gate; the format contract is now `binary-format-v3.md`.

Implementation plan (re-sliced; Rust first, then Go):

- **1A — Lock the format.** Review `binary-format-v3.md`; resolve its §17 OPEN
  items (set caps; legacy-read timing — IP-key integer-pair compare is confirmed).
  No code. *(This SOW's prerequisite; the draft exists.)*
- **1B — Rust library (reference).** Writer (streaming + backpatch) + reader
  (metadata-only, mmap read-only) + structural validation/caps + tests
  (round-trip, fuzz/negative, ASan/Miri). Build the shared conformance corpus here.
- **1C — Go library (matches Rust).** Same writer + three reader modes, pure Go,
  byte-identical files, reads Rust's output. **Early Rust-vs-Go speed check**
  (write + lookup). If Go is 2–5× slower on write, raise it per SOW-0001 (drop Go
  writer / consider Rust port of update-ipsets).
- **1D — Legacy read + owned-mutable mode.** Read legacy v1/v2 for migration;
  finish owned-mutable. (May be deferred to a later step.)

Each sub-step may be promoted to its own SOW when started.

Validation plan: shared conformance corpus (byte-identical, cross-read);
fuzz/negative + Miri/ASan; zero-alloc assertion on mmap lookups; Rust-vs-Go
speed numbers recorded.

Artifact impact: format spec locked under `.agents/sow/specs/`; project skill for
format invariants once stable; `wiki/` when user-visible; hand the on-disk
representation to Step 2 (engine, SOW-0003). *(The in-memory engine type is
defined in Step 2, not here — Step 1 owns the on-disk bytes.)*

Open decisions: format-spec §17 items (resolve in 1A). No others block starting.

## Implications And Decisions

Inherits SOW-0001's locked decisions + the 2026-06-21 review outcomes. Signing
deferred (slot reserved). IP keys as little-endian integer pairs compared
numerically (format spec §3) is confirmed (per Costa, 2026-06-21).

## Plan

See Pre-Implementation Gate → sub-steps 1A–1D.

## Execution Log

### 2026-06-21
- Drafted; then re-sliced after the 7-reviewer design review. Byte-level format
  spec drafted (`binary-format-v3.md`). No code started.

## Validation

Pending — no implementation yet.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- On go-ahead: lock the format (1A), then implement 1B (Rust) → 1C (Go) → 1D.
  Then Step 2 (engine) as SOW-0003.

## Regression Log

None yet.
