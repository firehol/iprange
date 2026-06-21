# SOW-0002 - Step 1: binary-format library (read + write) — Rust + Go

## Status

Status: open

Sub-state: Plan complete, gate `ready`. Awaiting user go-ahead to move to
`current/in-progress`. No code started. Parent initiative: SOW-0001.

## Requirements

### Purpose

Step 1 of the iprange rewrite: a **format library**, built in **both Rust and Go**,
that reads and writes the portable, architecture-neutral, signed, sectioned binary
format — the foundation the engine (Step 2) and CLI (Step 3) build on. The two
libraries must produce **byte-identical files** and read each other's output.
Design context: `.agents/sow/specs/design-iprange-engine.md` + SOW-0001 (locked
decisions).

### User Request

"The first step would be to create a file format library, for reading and writing.
Reading should have 2 modes: minimal metadata extraction, full ready for
processing." (Refined to three read modes — see Acceptance Criteria.)

### Assistant Understanding

Facts:

- Two full native libraries (Rust + Go) with 100% parity; native C dropped (Rust
  ships a C library for C consumers); the Go library is pure Go, no cgo.
  [SOW-0001 N-1, revised]
- The legacy C format is ASCII-header + native-endian struct dump; its inclusive
  range record encoding (`[addr][broadcast]`, u32/u128) is the reusable asset; the
  container is replaced. [grounding, `src/ipset_binary.c`/`ipset6_binary.c`]
- Format = little-endian, 8/16-byte aligned, typed-section directory + Ed25519
  signature over header+directory with per-section sha256. [SOW-0001 D-B + prior art]

Inferences:

- A pure-Go zero-copy mmap reader is the linchpin of the Netdata story; validating
  it early de-risks the whole format.

Unknowns:

- Exact section-kind registry and header field set (settle during implementation
  via the format spec doc — chunk 1).

### Acceptance Criteria

Delivered in **both Rust and Go** — byte-identical files, identical results (Rust
is the reference, Go matches):

- **Format spec doc** written (byte layout, section kinds, versioning, signing).
- **Writer:** streaming body + header/directory backpatch; sectioned; LE; dual-stack
  (v4 u32 / v6 u128 via generic `Ip`); Ed25519-signed. Verified by: round-trip tests.
- **Reader, three modes:** (1) metadata-only (header+directory, no body), (2) mmap
  read-only zero-copy view, (3) owned-mutable load. Verified by: per-mode tests +
  zero-alloc assertion on mode 2 lookups.
- **Legacy read:** reads existing v1.0/v2.0 files. Verified by: load existing
  `tests.d/` binary fixtures.
- **Safety:** structural bounds-check before signature verify before use; malformed
  + hostile-offset inputs rejected without UB. Verified by: fuzz/negative tests +
  ASan/Miri clean.
- **Shared core type** defined (the range / interval-map representation reused by
  Step 2). Verified by: it compiles as the engine's input/output type.

## Analysis

Sources checked: SOW-0001 grounding (5 analyses); design spec; legacy
`src/ipset_binary.c`, `src/ipset6_binary.c`, `src/uint128.h`; prior art (MMDB,
FlatBuffers/Cap'n Proto, Ed25519).

Current state: no Rust code exists yet; legacy C format + fixtures exist as
reference.

Risks: format churn if the spec is under-specified before coding (mitigate: spec
doc first); "signed ≠ safe" (mitigate: bounds-check-before-trust); pure-Go
zero-copy assumption (mitigate: early Go reader spike).

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- No portable, signed, mmap-able format exists; the legacy one is native-endian,
  unsigned, heap-only. We build a fresh container around the existing record
  encoding.

Evidence reviewed:

- Legacy format internals (`src/ipset_binary.c:186-349`, `src/ipset6_binary.c:123-294`);
  record structs (`src/iprange.h:122-125`, `src/iprange6.h:12-15`); `src/uint128.h`.
- Prior art: maxmind/MaxMind-DB (offset-relative, fixed-endian, metadata trailer);
  google/flatbuffers + capnproto/capnproto (section directory, alignment,
  ignore-unknown, bounds-check-before-trust). Commits to be pinned when used.

Affected contracts and surfaces:

- New: the on-disk format (public artifact contract); the Rust format-lib API; the
  shared in-memory core type.
- Existing: must still read legacy v1.0/v2.0.

Existing patterns to reuse:

- Inclusive-range record encoding + metadata semantics from the legacy format.
- `uint128.h` semantics → Rust generic `Ip` (u32/u128).
- `tests.d/` binary fixtures for legacy-read tests.

Risk and blast radius:

- Format is a long-lived public contract — get versioning + ignore-unknown right
  from day one.
- Signed ≠ safe: bounds-check structure → verify signature → read.

Sensitive data handling plan:

- No sensitive data involved (format/code only). Standard public-repo hygiene.

Implementation plan:

1. Write the format spec doc (byte layout, section-kind registry, header,
   versioning, signing). Define the shared core range/interval-map type.
2. Writer: streaming + backpatch; sections; LE; dual-stack; Ed25519 sign.
3. Reader: metadata-only mode; structural validator (bounds-check); signature
   verify; mmap read-only view; owned-mutable load; legacy v1.0/v2.0 read.
4. Tests: round-trip, per-mode, legacy fixtures, fuzz/negative, zero-alloc
   assertion (mode 2), Miri/ASan.
5. Build the **Go** format library alongside Rust (same three read modes, same
   writer); prove its files are byte-identical to Rust's and that mmap reads work
   in pure Go with no cgo.

Validation plan:

- Round-trip + per-mode + legacy-read tests; fuzz/negative + Miri; zero-alloc
  assertion on mmap lookups; pure-Go reader spike reads Rust-written files.

Artifact impact plan:

- AGENTS.md: add a project skill for the format invariants once stable.
- Specs: the format spec doc lands under `.agents/sow/specs/`.
- End-user docs: `wiki/` once the format is user-visible.
- SOW lifecycle: on completion, hand off the shared core type to Step 2 (SOW-0003).

Open-source reference evidence:

- maxmind/MaxMind-DB; google/flatbuffers; capnproto/capnproto. (Commits pinned at use.)

Open decisions:

- None blocking. Sub-choices (exact section kinds; signature scheme details) settle
  in chunk 1 (the spec doc) under the locked decisions.

## Implications And Decisions

Inherits SOW-0001's locked decisions (N-1..N-4, D-A..D-E, O3/O4). No new user
decisions required to start Step 1.

## Plan

See Pre-Implementation Gate → Implementation plan (chunks 1–5).

## Execution Log

### 2026-06-21

- Drafted from SOW-0001 grounding. Gate filled and `ready`. No code started.

## Validation

Pending — no implementation yet.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- On go-ahead: move to `current/in-progress` and execute chunks 1–5. Then Step 2
  (engine) as SOW-0003.

## Regression Log

None yet.
