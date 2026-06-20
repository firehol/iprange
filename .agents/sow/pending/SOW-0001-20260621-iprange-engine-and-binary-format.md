# SOW-0001 - iprange multi-language engine, binary threat-intel format & SDKs

## Status

Status: open

Sub-state: Design captured in spec; awaiting (a) grounding in the C oracle and (b) per-phase plan approval before any implementation. No code started.

## Requirements

### Purpose

Add real-time threat intelligence to Netdata. Evolve `iprange` into a
multi-language engine (C/Rust/Go) with a portable, architecture-neutral signed
binary format, so `update-ipsets` can publish threat-intel artifacts and SDKs.
Netdata consumes the threat intel **indirectly** via the update-ipsets SDK
(Rust netflow, Go topology/network-viewer), which embeds `iprange`.

Full design (decisions, layout, interval-map core, phasing): see
`.agents/sow/specs/design-iprange-engine.md`.

### User Request

Make `iprange` available as C/Rust/Go libraries + binaries with one shared test
& benchmark harness and performance parity; introduce an architecture-independent
binary format and an `mmap` memory mode; pursue zero/minimal allocation and an
optimal lookup path. End goal: `update-ipsets` + `iprange` become a public,
high-performance threat-intel source with ready-to-use SDKs; Netdata is the first
consumer.

### Assistant Understanding

Facts:

- `update-ipsets` already depends on an `iprange` engine for all set ops, already
  persists binary mmap range files, and already hand-builds an interval-map for
  geo (spec §9, with file:line evidence).
- The range/binary pipeline is IPv4-`uint32`-only today; IPv6 is opaque text
  (structural gap).
- Measured full-run profile of the new daemon: metadata 30.6% (overlap matrix +
  JSON), publish 27.6% (pure I/O, untouchable), sources 20.5%, asn 12.1% (spec §9.3).
- Pipeline is effectively single-threaded by config (`max_ingest_workers: 1`).

Inferences:

- The engine can attack ~40–55% of a full run (overlap matrix + retention +
  bogon/ASN overlaps); it cannot help publish I/O, JSON serialization, parsing,
  or downloads.
- Lookup hot path is memory-latency-bound → C/Rust/Go parity is realistic there.

Unknowns (resolved only by the C-oracle grounding, not yet done):

- Exact C `iprange` public API surface, in-memory representation, IPv6 handling
  (`wiki/ipv6.md`), test/harness structure, and how library-shaped it is.

### Acceptance Criteria (Phase 1)

- Dual-stack interval-map engine in C (the oracle) with the portable single-feed
  binary format (sectioned, signed, streamed). Verified by: round-trip + lookup
  tests.
- One shared black-box conformance corpus + benchmark harness drives the C binary;
  Rust and Go reach green within the 5–10% performance band. Verified by: harness
  run reports (wall-time + alloc count + RSS).
- Byte-identical binary read/write across implementations. Verified by: corpus
  binary-equality cases.

## Analysis

Sources checked:

- `.agents/sow/specs/design-iprange-engine.md` (the design).
- `firehol/update-ipsets` Go code (excluding `pkg/iprange`) — data model,
  analytics, retention, pipeline.
- The live update-ipsets daemon — `/api/v1/status`, admin Prometheus `/metrics`,
  process counters (internal host; details in parent FireHOL ops notes).

Current state:

- `iprange` (this repo): mature C CLI; autotools + CMake; tests under `tests.d/`,
  `tests.unit/`, sanitizer suites; some IPv6 support (`wiki/ipv6.md`) to reconcile.

Risks:

- Performance parity for Go on compute-heavy set algebra (mitigated by the
  5–10% band + I/O-vs-CPU split policy in the spec).
- IPv6 dual-stack is the largest single design+effort item.
- Scope creep: update-ipsets adoption must stay incremental, not a rewrite.

## Pre-Implementation Gate

Status: blocked

Blocked on: (1) grounding in the C `iprange` oracle (operations, in-memory model,
IPv6 handling, test/harness structure, library-shape); (2) user approval of the
Phase-1 plan. Implementation must not begin until this gate is filled from that
grounding and the user approves. The remaining gate fields (affected contracts,
patterns to reuse, blast radius, ordered implementation plan, validation plan)
will be completed from the C-oracle read.

Sensitive data handling plan:

- Evidence may reference the internal update-ipsets daemon and FireHOL infra.
  Durable artifacts here cite only non-identifying measurements and generic host
  descriptions; host/endpoint/credential details stay in the parent
  `~/src/firehol/AGENTS.md`, never in this public repo.

## Implications And Decisions

Decisions already made (full rationale in the spec, §2.3 + §12):

1. Full native libraries in C, Rust, Go.
2. Three CLIs as a shared conformance + benchmark harness (not products).
3. Performance parity tolerance 5–10%.
4. Benchmarks measure wall-time + allocation count + RSS.
5. Binary format doubles as a byte-identical conformance check.
6. C is the behavioral oracle.
7. Single self-contained signed sectioned file per feed.
8. Multi-feed (phase 2) = merged index + catalog + per-feed dossiers.
9. Dual-stack IPv6 from day one.
10. Public artifacts include `dont_redistribute` feeds, marked (handling deferred).
11. Netdata consumes indirectly via the update-ipsets SDK (Rust/Go).

Open (low-level, settle at implementation):

- O3: index representation — explicit `{start,end,value_id}` recommended.
- O4: membership-set encoding — interned sorted id-lists recommended.

## Plan

1. Phase 1 — engine + single-feed format (C oracle) + conformance/benchmark
   harness; Rust + Go to green within 5–10%.
2. Phase 2 — merged multi-feed format (catalog + merged index + per-feed dossiers
   + feed-id registry).
3. Phase 3 — update-ipsets adoption: replace `pkg/iprange`; migrate overlap
   matrix, then age/retention. (Independent quick win: raise `max_ingest_workers`.)
4. Phase 4 — update-ipsets SDK (Rust + Go) embedding `iprange`.
5. Phase 5 — Netdata consumes the SDK → real-time IP annotation.

Each phase will get its own SOW (or this SOW split) before implementation.

## Execution Log

### 2026-06-21

- Design discussion completed; decisions 1–11 captured.
- Investigated `update-ipsets` (4 subagents, excluding `pkg/iprange`) + measured
  the live daemon's per-phase profile.
- Authored the design spec; bootstrapped repo (cross-tool files) and SOW system.
- No implementation started.

## Validation

Pending — no implementation yet.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- Next action: ground in the C `iprange` oracle, then fill the Pre-Implementation
  Gate and split Phase 1 into an executable SOW.

## Regression Log

None yet.
