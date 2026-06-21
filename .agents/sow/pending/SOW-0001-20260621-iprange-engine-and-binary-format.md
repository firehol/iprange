# SOW-0001 - iprange multi-language engine, binary threat-intel format & SDKs

## Status

Status: open

Sub-state: Decisions locked (Rust-core engine; native C dropped; pure-Go reader/lookup, NO cgo in go.d.plugin). Restructured into 3 steps; Step 1 = SOW-0002 (pending). No code started.

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

Status: resolved — decisions locked (see "Implications And Decisions"); per-step
implementation gates live in the step SOWs (Step 1 = SOW-0002).

Grounding complete (2026-06-21): 5 parallel analyses of the C oracle + external
prior art.

Problem / root-cause model:

- The engine is a **generalization of the existing C `iprange`**, not a ground-up
  build. The oracle already (a) stores sets as sorted, coalesced, disjoint
  inclusive ranges (`network_addr_t {addr,broadcast}` u32, `src/iprange.h:122-125`;
  `ipset.netaddrs[]`, `src/ipset.h:8-25`); (b) is **dual-stack** via a
  hand-duplicated `*6` family over `uint128_t` (`src/uint128.h`,
  `src/iprange6.h:12-15`); (c) ships a binary (de)serializer
  (`src/ipset_binary.c`, `src/ipset6_binary.c`). Gaps vs. target: the format
  container, mmap/zero-copy, signing, the interval-map value, and the
  multi-language libraries + harness.

Evidence reviewed (5 analyses, file:line):

- Data model & ops: flat range arrays; `optimize` = qsort+sweep
  (`src/ipset_optimize.c:35-108`); `merge`/`combine` = concat; `common`/`exclude`/
  `diff` = linear two-pointer sweeps (`src/ipset_common.c:12-90`,
  `src/ipset_exclude.c:12-125`, `src/ipset_diff.c:12-150`); `reduce` = prefix
  histogram, IPv4-only (`src/ipset_reduce.c`). No range carries a value today.
- Binary format: ASCII pseudo-header + **raw native-endian struct dump**; v1.0 v4
  (8B), v2.0 v6 (32B); **no mmap** (heap deserialize, `src/ipset_binary.c:298-300`),
  **no checksum/signature**, no directory/skip-unknown.
- Build/lib-shape: one `iprange` binary, **no library artifacts** (`Makefile.am:26`,
  `CMakeLists.txt:69`); set algebra already takes `ipset*` + returns owned;
  blockers = ~15 globals, deep `exit()`/stderr (`src/ipset.c:91-102`), stdout
  coupling (`src/ipset_print.c`), global single-instance DNS pool
  (`src/ipset_dns.c:33-35`), leaks-by-design.
- Tests: `tests.d/` (100) already black-box + binary-swappable (`run-tests.sh`,
  `IPRANGE_BIN`); whitespace-insensitive today; `tests.unit/` white-box C-only;
  **no benchmark harness exists**.
- Prior art: MMDB (offset-relative, fixed-endian, metadata-trailer-by-marker,
  mmap zero-copy); FlatBuffers/Cap'n Proto (front section directory, 8B alignment,
  ignore-unknown, **bounds-check-before-trust**); protobuf/wasm conformance
  (golden corpus + thin long-lived per-language driver, errors/skips first-class);
  lookup = **sorted-range binary search** baseline (matches iprange output) +
  optional DXR-lite v4 fast path.

Affected contracts and surfaces:

- New: the portable binary format (public artifact contract); the C/Rust/Go
  library APIs; the conformance corpus + benchmark harness; SDK consumers.
- Existing (must stay green): the CLI verb/flag surface (`src/iprange.c`,
  `src/iprange6_main.c`) + its 100-test suite; the legacy v1.0/v2.0 binary format
  (read-compat decision needed).

Existing patterns to reuse:

- The inclusive-range record encoding + metadata semantics (`ipset_binary.c`) as
  the INDEX payload.
- `optimize`'s sort+sweep and the op sweeps as interval-map split/merge sites
  (~5 sites).
- `uint128.h` as the generic-width seam for v4/v6 unification.
- `tests.d/` as the shared conformance corpus; `run-tests.sh` `IPRANGE_BIN` hook.

Risk and blast radius:

- v4/v6 hand-duplication multiplies every core change ×2 unless unified (D-A).
- libification refactors (globals→ctx, ~84 `exit()` sites→errors, stdout→callback,
  DNS lifecycle) are wide in C (D-E).
- **Signed ≠ safe**: a signed file still needs structural bounds-checking before
  offsets are trusted (FlatBuffers CVE class; Cap'n Proto traversal limits) —
  verify structure → verify signature → read.
- Go parity on compute-heavy set algebra (5–10% band; I/O-bound ops are fine).
- Legacy binary format read-compatibility.

Sensitive data handling plan:

- Evidence may reference the internal update-ipsets daemon and FireHOL infra.
  Durable artifacts here cite only non-identifying measurements and generic host
  descriptions; host/endpoint/credential details stay in the parent
  `~/src/firehol/AGENTS.md`, never in this public repo.

Implementation plan (Phase 1 — pending decisions; each chunk → detail in a Phase-1 SOW):

1. Resolve D-A/D-B/D-E; write the portable format spec (sectioned, signed, mmap,
   fixed-endian, dual-stack) reusing the existing record encoding.
2. C: format reader (mmap, sorted-range zero-alloc lookup, structural
   bounds-check) + writer (streaming + header backpatch); refactor scope per D-E.
3. Generalize the range structure with an interned `value_id` at the ~5
   coalescing/boundary sites; unify v4/v6 per D-A.
4. Conformance harness: reuse `tests.d/` for CLI byte-identical (drop whitespace
   flags) + JSON-lines op-level driver with per-language failure lists + 4-bucket
   reporting.
5. Benchmark harness: deterministic large corpora; wall-time + RSS + per-language
   alloc; 5–10% parity gate.
6. Rust then Go to green against the corpus + within the perf band (C = oracle).

Validation plan:

- 3-way byte-identical conformance over `tests.d/` (+ op-level driver corpus).
- Benchmark parity within 5–10%; alloc/RSS reported.
- Format reader fuzzing/bounds-check (malformed + hostile offsets); signature
  verification tests.
- Round-trip + mmap zero-copy tests; existing ASan/MSan/TSan suites.

Artifact impact plan:

- AGENTS.md: add project skills once the harness/build workflow is concrete.
- Specs: refresh `design-iprange-engine.md` with grounding refinements after
  decisions land (endianness, lookup structure, v4/v6 unification, signed≠safe).
- Runtime project skills: candidates — conformance/bench harness; C build/sanitizer;
  format invariants — once concrete.
- End-user docs: `wiki/` updates when the format/CLI surface changes.
- SOW lifecycle: split Phase 1 into an executable SOW once decisions are made.

Open-source reference evidence:

- maxmind/MaxMind-DB (spec) + maxmind/libmaxminddb; oschwald/maxminddb-golang.
- google/flatbuffers (internals; issues #9040, #8002); capnproto/capnproto.
- protocolbuffers/protobuf (`conformance/`); WebAssembly/testsuite + WebAssembly/wabt.
- Lookup: DPDK/dpdk (`lib/lpm/`), DXR (Zec/Rizzo/Mikuc), Poptrie (Asai/Ohara
  SIGCOMM'15), SAIL.
- (Commits to be pinned when used as implementation evidence in the Phase-1 SOW.)

Open decisions (block a `ready` gate; need user input):

- **D-A — v4/v6 unification:** unify behind a generic integer width (one algorithm,
  `uint128.h` seam) vs keep the hand-duplicated `*6` family. Rec: unify.
- **D-B — Endianness:** fixed **little-endian** (native on x86/ARM → true
  zero-copy) vs big-endian (MMDB/network precedent). Rec: little-endian.
- **D-C — Conformance harness:** reuse `tests.d/` (CLI byte-identical) + add a
  JSON-lines op-level driver, vs build fresh. Rec: reuse + extend.
- **D-D — stderr/diagnostic parity:** require Rust/Go to reproduce C's diagnostic
  wording verbatim vs relax conformance to exit-code + structured result.
  Rec: structured result.
- **D-E — C libification scope/sequence:** reader-lib first (small–medium) then
  full set-algebra lib (large), refactoring existing C in place vs a fresh C
  engine. Rec: reader-lib first, refactor in place, staged.
- **O3 — index representation:** explicit `{start,end,value_id}` (rec) vs breakpoints.
- **O4 — membership-set encoding:** interned sorted id-lists (rec) vs bitset.

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

### Decisions locked post-grounding (2026-06-21)

**Language architecture pivot — Rust is the single engine; native C is dropped.**

- **N-1 (revised 2026-06-21 per Costa):** **Two full native libraries — Rust and
  Go — with 100% feature parity.** Both implement the complete engine (format + set
  algebra + lookup). Rust also produces a C library (`cdylib`/`staticlib` +
  cbindgen) so C consumers use Rust. The Go library is **pure Go, no cgo** (hard
  constraint). Consumers: `netflow.plugin` (Rust) + `network-viewer.plugin` (C, via
  Rust's C library) → Rust; `go.d.plugin` (Go) + `update-ipsets` (Go) → Go. Go
  consumers need full operations, not just lookups.
- **N-2:** legacy C `iprange` retained only as the **behavioral oracle** to seed
  the golden conformance corpus, then retired.
- **N-3:** **cgo is NOT acceptable for `go.d.plugin`.** It is acceptable for the
  `update-ipsets` server (not `go.d.plugin`); the producer may call the Rust engine
  via cgo, or stay pure-Go by subprocessing the Rust `iprange` CLI. Producer
  mechanism deferred to the update-ipsets adoption SOW.
- **N-4:** format reader has three modes — metadata-only, mmap read-only view,
  owned-mutable — and reads both the new format and legacy v1.0/v2.0.

Revisions to the original spec decisions:

- D-1 (3 native libs) → **2 full native libs: Rust + Go, 100% parity; C via Rust's
  C library** (native C dropped).
- D-3 (parity) → parity is **Rust ↔ Go across ALL operations** (5–10% target). The
  heavy set-algebra parity is a **real risk for Go** (GC); the shared harness tracks
  it per operation; if an op can't reach 5–10%, surface numbers and decide.
- D-6 (oracle) → legacy C = retiring oracle; **Rust = reference impl**.
- **D-A = unify** (trivial in Rust via generics over an `Ip` trait `u32`/`u128`).
- **D-B = little-endian** + natural alignment (sections cast to `u32[]`/`u128[]`,
  zero byte-swap on x86/ARM).
- **D-C = reuse `tests.d/` + op-level driver** (protobuf-style length-prefixed
  stdin/stdout, spawn-once-loop; `TIME` command reuses the pipe for benchmarks).
- **D-D = structured result** (exit-code + canonical output; no verbatim diagnostics).
- **O3 = explicit `{start,end,value_id}`. O4 = interned sorted id-lists.**

Format refinements from prior art (Step 1 inputs):

- Front magic+version+endianness → typed-section **directory** `{kind, offset:u64,
  length, sha256}` → 8/16-byte-aligned sections → metadata/signature trailer.
- **Ed25519** over header+directory (per-section sha256 → verify + mmap a single
  section without hashing the whole file). **Signed ≠ safe**: bounds-check structure
  → verify signature → read.
- Lookup = **sorted disjoint ranges + binary search** baseline (uniform v4/v6, mmap
  zero-alloc); optional DIR-24-8-style v4 accel / Poptrie v6 are additive under the
  directory (no format break) — defer.

### Design-review outcomes & decisions (2026-06-21)

Seven external reviewers (codex/gpt-5.5, glm, minimax, kimi, mimo, deepseek, qwen)
reviewed the design + SOWs. Strong consensus. Costa's decisions on their findings:

Costa's decisions:

- **Keep two full libraries (Rust + Go), including the full writer + operations in
  Go.** Both update-ipsets and Netdata must *write*/combine the unified format
  (Netdata combines multiple sources locally for fast lookup), so a Go writer is
  required; external calls complicate things.
- **Attempt-then-decide on Go speed:** build full Go, measure vs Rust early. If Go
  is 2–5× slower on the heavy operations, **drop Go as a writer** and seriously
  consider **porting update-ipsets to Rust** (preferred over external calls).
  Decide on real numbers. Behavioral parity (identical results) is mandatory
  regardless; speed parity is the goal, conceded only if measurements force it.
- **Signatures: the format must support them, but signing is deferred** (low
  priority now). Reserve the signature section/fields from day one; do NOT build
  signing or key management in Step 1. Later: iplists.firehol.org signs published
  files; the iprange CLI + SDK gain sign/verify options; users sign/verify their own.

Accepted reviewer fixes (fold into spec + plan):

- Write the **complete byte-level format spec first**, as a gate before any code
  (exact field offsets/sizes — NOT "native struct layout"). Little-endian, with a
  small byte-swap penalty noted for big-endian targets (s390x/MIPS/PowerPC).
- **Correct the over-optimistic lookup estimate.** Plain binary search over
  millions of ranges is ~20+ cache misses (µs cold), not 1–2 (~100–200 ns); the
  merged file may be ~1–2 GB, not "tens of MB". **Measure at full (~430-feed) scale
  before committing**; the v4/v6 lookup accelerator is likely **required**, not
  optional.
- Resolve the **mmap-vs-verification** contradiction: verify the whole file once at
  download/install, then trust the local file (no per-lookup / whole-index
  re-hash). Mandate atomic publish + verify-from-the-same-handle.
- **Go has no native 128-bit integer** — IPv6 in Go needs an explicit `{hi,lo}`
  type with manual ops; drop the "written once in both languages" claim for Go.
- Lock **O3 = explicit {start,end,value_id}** and **O4 = interned sorted id-lists**
  (needed to write the format spec).
- Specify the interval map precisely: inclusive ranges (match legacy), sort
  tie-break, coalescing rules, gap semantics, value (V) serialization + type-id +
  skip-unknown, interning collision-safety, deterministic ordering (byte-identical
  output), IPv6 count saturation.
- Add **resource limits + overflow-safe bounds checks** to the format (max
  file/section/entry sizes) even with signing deferred — malformed input must never
  crash or over-allocate.
- **Re-slice Step 1** (too big): byte-level spec → Rust writer+reader+tests → Go
  library (measured vs Rust) → legacy read. **Rust first, then Go.** Build the
  shared conformance/benchmark harness early.
- Write a one-paragraph **"why a custom format, not FlatBuffers/Cap'n Proto/MMDB"**
  justification in the spec.

Deferred / open: feed-id registry details (note: interning feed *names* avoids a
global registry — evaluate); dont_redistribute enforcement vs metadata-only (legal
call); signing + key management; the Rust-port-of-update-ipsets decision (gated on
Go speed measurements).

Separate quick win (not part of this work): update-ipsets runs single-threaded by
config (`max_ingest_workers: 1`) — raising it is a large, near-free speed-up.

Note: the design spec (§0 supersedes affected sections) and SOW-0002 (Step 1) will
be refreshed/re-sliced to match the above as the next step.

## Plan

Rust-core build, in three steps (each its own SOW; **Step 1 = SOW-0002**):

1. **Step 1 — Format library** (Rust): read + write the portable sectioned/signed
   binary format; three read modes (metadata-only, mmap read-only, owned-mutable);
   reads legacy v1.0/v2.0 too. → `SOW-0002`.
2. **Step 2 — Processing engine** (Rust): `IntervalMap<V>` over a generic `Ip`
   width (u32/u128); set operations (optimize/merge/intersect/exclude/diff/reduce/
   compare) generalized with interned per-range values.
3. **Step 3 — iprange CLI** (Rust): backwards-compatible with the legacy C CLI
   (verbs/flags/output/exit codes + legacy binary read), validated byte-for-byte
   against the legacy C oracle via `tests.d/`.

Each of the three steps is built in **both Rust and Go** (Rust as the reference,
Go to match), producing identical files and results. Cross-cutting: the shared
conformance + benchmark harness (same tests run on both libraries); the C library
that Rust exposes for the C consumer.

Downstream (later SOWs, intent unchanged):

4. Merged multi-feed format (catalog + merged index + per-feed dossiers + feed-id
   registry).
5. update-ipsets adoption (replace `pkg/iprange`; overlap matrix, then retention;
   independent quick win: raise `max_ingest_workers`).
6. update-ipsets SDK (Rust + Go, both full) → Netdata consumes it.

## Execution Log

### 2026-06-21

- Design discussion completed; decisions 1–11 captured.
- Investigated `update-ipsets` (4 subagents, excluding `pkg/iprange`) + measured
  the live daemon's per-phase profile.
- Authored the design spec; bootstrapped repo (cross-tool files) and SOW system.
- Grounded in the C `iprange` oracle + external prior art (5 parallel analyses:
  data model, binary format, build/lib-shape, tests/harness, prior-art). Filled
  the Pre-Implementation Gate; surfaced decisions D-A..D-E.
- Locked the architecture: **Rust-core engine; native C dropped (Rust C ABI for C
  consumers); pure-Go reader/lookup with NO cgo in go.d.plugin**. Recorded
  N-1..N-4; resolved D-A..D-E/O3/O4. Restructured into 3 steps; created SOW-0002
  (Step 1 — format library).
- No implementation started.

## Validation

Pending — no implementation yet.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- Grounding done (2026-06-21). Next action: user decides the open gate decisions
  (D-A v4/v6 unification, D-B endianness, D-C conformance harness, D-D stderr
  parity, D-E C libification scope, O3 index rep, O4 set encoding). Then split
  Phase 1 into an executable SOW with a `ready` gate and refresh the design spec
  with the grounding refinements.

## Regression Log

None yet.
