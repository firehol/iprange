# SOW-0005 - Format performance: profiling harness, real-scale measurement, and the kind-6 accelerator decision

## Status

Status: open

Sub-state: Authored 2026-06-22 alongside SOW-0004 (test hardening), from a
performance review of the v3.0/v3.1 format library. **Not started** — "measure
before optimize." This SOW builds the measurement so the reserved kind-6
accelerator decision (spec §17, decision D3) is made from data, not intuition.

## Requirements

### Purpose

The v3 format library currently ships a **deliberately simple baseline** — AoS
records + numeric binary search — with a lookup **accelerator (`kind 6`) reserved
but unimplemented**, gated by design decision D3 on "benchmark at full scale
first" (`spec §17:1137-1145`, `SOW-0003:137-138`). That benchmarking has not been
done. This SOW establishes a real profiling/benchmark harness, measures the
library where it will actually be stressed (the producer build, the **merge**
path, and lookups at **~430-feed / millions-of-ranges scale**, cold and warm
cache), compares against the legacy **C** oracle/ceiling, and produces a written
recommendation for whether — and which — kind-6 accelerator to build. It does
**not** implement any accelerator; it produces the data and the decision.

### User Request

Costa, after the performance review: confirmed "yes" to capturing a performance
SOW as a sibling to the test-hardening SOW — "measure before accelerate." Asked
specifically: what perf tests exist, have we profiled, is it cache-friendly, is it
the best possible, have we measured it, where are the hot spots. The review
established the honest answers (one microbenchmark; no profiling; AoS baseline not
claimed optimal; hot spots unmeasured for this library). This SOW closes that gap.

### Assistant Understanding

Facts (measured / read on 2026-06-22):

- **The only perf test today** is a single microbenchmark, `#[ignore]`/manual,
  single-feed, synthetic: `rust/.../tests/speed.rs` + `go` `TestSpeedReport`
  (recorded `SOW-0002:28-29`). Fresh run, 200k disjoint ranges → 2,249,880-byte
  file, 1M random lookups:
  - Rust build **45.0 ns/range**, lookup **29.0 ns/op**.
  - Go build **81.2 ns/range** (≈1.8×), lookup **49.9 ns/op** (≈1.7×).
  - Hit rate **0%** → it measures the search+miss path and **never exercises
    `value()`**.
- **Lookup** = textbook branchy binary search over a flat **AoS** array (12-byte
  v4, 40-byte v6 records), zero-alloc, read from borrowed/mmap bytes
  (`rust/.../src/reader.rs:502-530`; `go/reader.go:399`).
- **`value()` is O(N)** — a sequential walk of variable-length value entries
  (`reader.rs:202-222`); a latent hot spot if a consumer resolves a value per
  lookup without a side index.
- **The merge sweep** uses `BTreeMap` for events + active set
  (`merge.rs:114-160`) — correct, but allocation-heavy and cache-unfriendly; never
  benched.
- The spec itself states AoS "is **not the most cache-dense layout for v4**" and
  has no 16-byte record alignment for v6, and reserves SoA / DIR-24-8 / Poptrie
  under kind 6 once benchmarked (`spec §17:1137-1140`, `:397`).
- The **only existing profile** is of the separate Go update-ipsets **daemon**:
  metadata/overlap-matrix **30.6%** of a full run (`SOW-0001:40`) — i.e. the
  multi-x-multi set work, not point lookups. That is the Step-2 engine's hot path,
  not this library's.

Inferences:

- The production-relevant cost is likely **(a)** the producer/merge build at
  430-feed scale and **(b)** cold-cache lookups over a large merged index — neither
  measured. Point-lookup tuning on a warm 200k array may be optimizing the wrong
  thing.
- A meaningful accelerator decision needs **cold-cache** and **real-scale** numbers
  plus the **C** ceiling; warm microbenchmarks alone cannot justify kind-6.

Unknowns (to be resolved by this SOW's measurement):

- Actual cache-miss / branch-mispredict rates of the lookup (no `perf`/cachegrind
  data exists).
- Whether binary search at real scale is fast enough, or whether
  branchless/Eytzinger vs trie (DIR-24-8/Poptrie) is warranted.
- The merge build's cost curve as feed count and range count grow.
- How close Rust/Go are to the C ceiling (the design's 5–10% band target,
  `iprange/AGENTS.md` Goals).

### Acceptance Criteria

- **Reproducible benchmark harness**, committed and runnable, covering:
  - producer **build** (single-feed) and **merge build** (k feeds), parameterized
    by range count and feed count;
  - **lookup** with controlled **hit rate** (so `value()` resolution is measured,
    not just the 0%-hit miss path) and controlled **cache state** (cold vs warm);
  - both **IPv4 and IPv6**.
- **Real-scale workload**: at least one run approximating the iplists daemon scale
  (order ~430 feeds / millions of ranges), for build, merge, and lookup.
- **Profiling artifacts**: `perf stat` (cycles, cache-misses, branch-misses) and a
  cache profile (cachegrind or equivalent) for the lookup hot path at scale, cold
  and warm — captured as committed numbers/notes, not one-off console output.
- **C baseline**: the legacy C `iprange` measured on a comparable operation as the
  oracle/ceiling, so Rust/Go can be placed against it (the 5–10% band).
- **Written perf report** (a spec or SOW appendix) stating: measured hot spots,
  cache behavior, Rust-vs-Go-vs-C standing, and a **recommendation** on kind-6 —
  none / branchless-Eytzinger / DIR-24-8 / Poptrie — with the data that justifies
  it. The accelerator itself is a **separate** future SOW if the data warrants it.
- Statistical rigor: a real benchmark tool (criterion for Rust, Go `testing.B`),
  not the single-`Instant::now()` ±30%-noise probe.
- No change to any on-disk byte (measurement only; the baseline stays the shipped
  reader).

## Analysis

Sources checked:

- `rust/iprange-format/tests/speed.rs`, `go/iprangeformat_test.go` (TestSpeedReport)
- `rust/iprange-format/src/{reader,merge,writer,key}.rs`; `go/reader.go`
- `.agents/sow/specs/binary-format-v3.md` §9/§10/§17 (AoS, value table, reserved
  accelerator), `design-iprange-engine.md` (C oracle, 5–10% band, hot-path model)
- `.agents/sow/{done/SOW-0002,done/SOW-0003,pending/SOW-0001}.md` (recorded numbers,
  D3 deferral, daemon profile)

Current state:

- One warm, single-feed, 0%-hit microbenchmark. No profiling, no scale, no merge
  bench, no C comparison, no cache measurement. The accelerator slot is reserved
  but the data to decide it does not exist.

Risks (of NOT doing this work):

- The kind-6 decision gets made on intuition, or the baseline ships to Netdata/
  update-ipsets and a cold-cache lookup or 430-feed merge build turns out to be a
  production hot spot only discovered in the field.
- Optimizing point lookups (warm, microbenchmark) while the real cost is the
  producer/merge/overlap path — wasted effort on the wrong hot spot.

## Pre-Implementation Gate

Status: needs-user-decision

(User has not authorized implementation. Gate is drafted; do not begin until the
user moves this SOW to `current/` and resolves the open decisions below.)

Problem / root-cause model:

- The library was correctly built correctness-first with a simple, low-risk
  lookup. Performance was explicitly deferred (D3). The result is a baseline whose
  real-world behavior is unmeasured, and a reserved accelerator with no data to
  decide it.

Evidence reviewed:

- See "Sources checked"; specific file:line evidence in Requirements → Assistant
  Understanding.

Affected contracts and surfaces:

- New benchmark/profiling harness (`rust` benches via criterion, `go` `*_test.go`
  `Benchmark*`, possibly a `bench/` scripts dir), a committed perf report
  (`.agents/sow/specs/` appendix or a `done/` SOW Outcome), and CI (optional
  bench smoke). **No change** to `src/` byte-emitting code expected.
- If profiling reveals a cheap, safe, non-format-changing win (e.g. a branchless
  inner loop that keeps identical results, or a documented "build a value side
  index" consumer note), that may land here; anything touching the on-disk layout
  is **out of scope** and becomes a kind-6 accelerator SOW.

Existing patterns to reuse:

- The LCG workload generator + identical Rust/Go constants
  (`speed.rs:9-30`, `TestSpeedReport`) already give a shared, comparable workload —
  extend it (hit rate, scale, IPv6, merge) rather than inventing a new one.
- The 430-feed scale and the real list shapes exist on the iplists daemon / the
  `blocklist-ipsets` output — usable to build a realistic corpus (synthetic but
  shaped like production; no sensitive data needed).

Risk and blast radius:

- Low: measurement is additive. The elevated-risk path is a profiling-driven
  micro-optimization to `search()`; if taken, it is guarded by the existing
  goldens (byte-identity) + SOW-0004's oracle so results cannot silently change.

Sensitive data handling plan:

- Synthetic workloads only; if real list shapes are sampled, use counts/shapes,
  not customer-identifying content. Standard guardrails apply.

Implementation plan (ordered):

1. **Harness**: criterion benches (Rust) + Go `Benchmark*`, parameterized by
   range count, feed count, hit rate, IP family; cover build, merge build, lookup.
2. **Scale + cache**: a ~430-feed / millions-of-ranges run; capture `perf stat` +
   cachegrind cold vs warm for lookup at scale.
3. **C baseline**: measure legacy C `iprange` on a comparable op; place Rust/Go vs
   C (5–10% band).
4. **Report + decision**: write the perf report; recommend kind-6 direction (none /
   branchless-Eytzinger / DIR-24-8 / Poptrie) with the supporting data. Spin the
   accelerator itself out as a separate SOW only if warranted.

Validation plan:

- Benches run reproducibly (`cargo bench`, `go test -bench`); profiling artifacts
  committed as notes/CSVs, not transient output. The report's recommendation is
  backed by the captured numbers. No byte changes (goldens stay green).

Artifact impact plan:

- AGENTS.md: add the benchmark/profiling commands to "Project-specific commands".
- Runtime project skills: strong candidate to capture the
  **benchmark+profiling harness workflow** as a `project-*` skill (SOW-0001
  deferred skill creation; this is one of the named candidates).
- Specs: update `binary-format-v3.md §17` (and/or `design-iprange-engine.md`) with
  the measured numbers and the resolved/again-deferred kind-6 decision.
- End-user/operator docs, skills: unaffected (internal perf work).
- SOW lifecycle: on completion, move to `done/`; if kind-6 is warranted, open a new
  accelerator SOW and map it in Followup.

Open decisions:

- **D1 — Harness tooling.** (a) criterion (Rust) + Go `testing.B` (recommended:
  statistical, standard, low-noise); (b) keep the hand-rolled `Instant::now()`
  probe (rejected: ±30% noise, not decision-grade). **Recommendation: (a).**
- **D2 — C baseline now or later.** (a) include legacy C as the ceiling in this
  SOW (recommended: C is the decided oracle and perf ceiling, and d1/the build
  recipe already exist); (b) defer C comparison to the Step-2 engine SOW.
  **Recommendation: (a).**
- **D3 — Sequencing vs Step-2.** (a) run this in **parallel** with / before Step-2
  engine design — the engine's hot path is the overlap matrix, and these numbers
  inform both (recommended); (b) fold perf measurement into the Step-2 SOW.
  **Recommendation: (a)** — keep the format library's perf decision separate and
  data-driven so Step-2 starts on a measured foundation.

## Implications And Decisions

Pending user decision (D1–D3 above). Record choices here before implementation.
Note: this SOW **decides** kind-6 direction from data; it does **not** build an
accelerator (that is a separate SOW if warranted).

## Plan

See Pre-Implementation Gate → Implementation plan (items 1–4).

## Execution Log

Not started.

## Validation

Pending.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- If the report recommends an accelerator, open a dedicated **kind-6 accelerator
  SOW** (additive section kind; layout per spec §8/§17) — out of scope here.
- Consumer guidance: if `value()`-per-lookup is a measured hot spot, document /
  provide a value side-index helper (`reader.rs:200-201` already hints at it).

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and
later testing or use found broken behavior. Use a dated `## Regression -
YYYY-MM-DD` heading at the end of the file. Never prepend regression content above
the original SOW narrative.
