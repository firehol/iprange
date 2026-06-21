# SOW-0002 - Step 1: binary-format library (read + write) — Rust + Go

## Status

Status: completed

Closed 2026-06-21. **All of Step 1 (1A lock + 1B Rust + 1C Go + 1D legacy/owned-
mutable) is delivered**, hardened across **4 unbiased external-review rounds** that
converged to a **unanimous PRODUCTION-GRADE verdict (zero P0/P1/P2; P3-only)** from
glm, deepseek, minimax, and mimo. Two byte-identical native libraries (Rust + pure
Go), a shared conformance corpus, and a legacy v1.0/v2.0 read/migration path verified
against real `iprange --print-binary` artifacts. Final commits incl. review hardening
`edb99c2`/`bc88b73`/`b637522`/`1cbf3a2`. See `## Validation` / `## Outcome`. Step 2
(engine) tracked as a future SOW-0003.

---

Sub-state (2026-06-21): **1A, 1B, and 1C COMPLETE.** 1A = format locked (`bb05b7e`).
1B = Rust reference library `rust/iprange-format` (writer + 3 reader modes, corpus,
robustness; 48 tests, clippy clean, Miri-clean, reader core `no_std`). 1C = pure-Go
library `go/` (no cgo, no third-party deps): writer + reader (metadata-only,
validated read, owned-mutable `ToWriter`), Go conformance harness, unit + panic-safety
tests. **Cross-language byte-identity PROVEN** — the Go writer reproduces all 7 Rust
goldens byte-for-byte and matches the 4 reject classes across the shared
`conformance/` corpus.

**Early Rust-vs-Go speed check** (identical LCG workload, both release; busy
workstation, ~±30% run-to-run noise): build Rust ~53–59 vs Go ~93 ns/range
(**Go ≈1.7×**); lookup Rust ~30 vs Go ~57 ns/op (**Go ≈1.9×**). Both emit the same
2,249,880-byte file. Go is **under the SOW-0001 2× "drop the Go writer" threshold** →
**keep the Go writer** (decision pending Costa's confirmation). Benches:
`rust .../tests/speed.rs` (`#[ignore]`) + `go TestSpeedReport`, same workload.

Commits: `59df113`/`720abec`/`f2937cf`/`c74885d` (1B), `db39f1a` (1C).

**1D COMPLETE — all of Step 1 is implemented.** Legacy read: a verified spec
(`.agents/sow/specs/legacy-binary-format.md`) of the legacy v1.0/v2.0
`iprange --print-binary` format; **real fixtures** generated from the built legacy C
`iprange` (`conformance/legacy/*.bin` + JSON manifests); Rust (`src/legacy.rs`) and Go
(`go/legacy.go`) legacy readers that parse those real artifacts to identical ranges
(incl. the v6 lo↔hi transposition) and migrate them to v3. Owned-mutable landed with
1B/1C. **Step 1 status: 1A+1B+1C+1D all done; cross-language byte-identity proven.**
Pending: offer the external-reviewer panel on the complete library, then formally
close the SOW (Validation/Outcome/Lessons).

---

Sub-state: **Sub-step 1A (lock the format) COMPLETE — format LOCKED after 12
external-review rounds** on 2026-06-21 (rounds 3–12 unbiased / no fix-list, per
user; per the P0/P1/P2 iteration policy below). Byte layout frozen since round 3
(header 72, dir entry 72, index sub-header 32, v4 record 12, v6 record 40 —
re-verified every round, 12×). Convergence: **0 P0 since round 6**; rounds 10–12 all
0 P0 with only clarity/forward-compat/implementer-guidance findings; **round 12
unanimous "ready to implement" across all 5 reviewers** (glm, deepseek, minimax,
qwen, mimo). Signing (§11) and phase-2 multi-feed (§13) are explicitly NON-NORMATIVE
/ out-of-scope for v3.0. Residual implementer-guidance (Rust `#[repr(C,packed)]` /
`read_unaligned`, C-consumer packing, O(N)-walk cost notes, the binary-search/
coalescing pseudocode) is deferred to **1B** where the two implementations + shared
conformance corpus validate it empirically. Next: **1B — Rust library** (path D8
Option 2). Parent: SOW-0001.

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

### Format spec external review + decisions (2026-06-21)

`binary-format-v3.md` was reviewed by 5 external reviewers (glm, minimax, kimi,
deepseek, qwen; mimo produced no usable output). Consensus: the **design is
sound** (sectioned, little-endian, mmap-first, two-`u64` IPv6 compare verified
against legacy C `uint128.h` — nothing on the hot path needs 128-bit; no-caps
safety model correct), but the **spec was not yet lockable** — two independent
implementations would not have produced byte-identical files. ~7 critical + ~14
high gaps, almost all one-line normative fixes; one real bug (the §15 size check
ignored the 32-byte index sub-header) and one factual error about legacy
unique-IP overflow behavior (verified in round 2: legacy C is inconsistent — the
in-memory IPv4 counter wraps, the IPv6 counter saturates at 2^128−1, the binary
loader rejects; v3 sidesteps all three by representing the exact 128-bit count).

User decisions (approved 2026-06-21), implemented in the spec rewrite that locks
sub-step 1A:

- **D1 — Section hash = full 32 bytes now** (directory entry 56→72 B). Permanent,
  signing-ready; avoids a later breaking change. (Was 16-byte truncated SHA-256
  = only 2^64 resistance, inadequate for a signed feed.)
- **D2 — Add `unique_ip_count_hi` (u64) to the header** (header 64→72 B), so an
  IPv6 count ≥ 2^64 is machine-readable; deletes the dangling "plus a flag".
- **D3 — Lock array-of-structs (AoS) for v3.0** and **reserve section kind 6
  (lookup-accelerator)** so a future SoA / DIR-24-8 / Poptrie layout is additive
  (no format break). Closes the §17 open item that blocked the lock.
- **D4 — Put machine-readable `license_flags` (bit0 `dont_redistribute`) in the
  header** (repurposing the former `reserved0` at offset 36) so consumers gate
  redistribution without parsing the body; the human-readable `license` token
  string stays in feed-meta. Reconciles the design-spec Tier-0 intent.
- **D5 — Remove the "strings" section from v3** (kind 4 → reserved); nothing uses
  it in v3 (feed-meta and values are inline length-prefixed).
- **D6 — Drop the header `optimized` flag** (bit1 → reserved=0); the index is
  always sorted+coalesced, so the flag carried no information.

Mechanical determinism/safety fixes applied in the same rewrite (no user
decision needed): all reserved/pad fields MUST be zero (and are rejected if not);
`value_id` bounds check; corrected §15 size check (`32 + record_count ×
record_size == index.length`); `header.entry_count == index.record_count`;
`align` validation (power of two, 8..4096); mandated canonical section order +
minimum-padding rule; IPv6 coalescing defined incl. the `2^128-1` boundary;
deterministic `value_id` assignment (sweep the sorted+coalesced index); dropped
the redundant `value_table_section_kind`; "packed, no compiler padding" note +
explicit IPv6 key byte layout; directory-region bounds/sortedness/non-overlap +
verify index/values hashes at load; required-vs-optional sections + reject
duplicate mandatory sections; values `type_id` registry (1 = membership set);
signature `must_understand=0`; `header_size >= 72` guard; corrected the legacy
overflow claim; mmap fd-not-path / atomic-rename / SIGBUS safety note.

Validation: re-run the same 6 reviewers on the revised spec (same scope + fix
notes), iterate until clean, before declaring 1A locked.

### Review iteration policy + two more decisions (2026-06-21)

Per Costa: **repeat unbiased reviews until a round yields only P3 findings; every
P0/P1/P2 MUST be fixed before implementation** (P0=Critical, P1=High, P2=Medium,
P3=Low). Reviews are not a waste — they de-risk a format three languages must agree
on byte-for-byte. (Captured in memory `iterate-reviews-until-only-p3`.)

Decisions (approved 2026-06-21, after round 10):
- **D7 — `type_id=1` stays usable in v3.0 (Option 1).** The format library treats
  value content as **opaque caller-supplied bytes**; byte-identical is defined over
  identical input *bytes*. The feed-id *mapping/registry* (semantic) is the engine's
  (Step 2) / phase-2 concern, not the format's. (Rejected the stricter "reject
  `type_id=1` in v3.0" because it would strip the values/attribute machinery — the
  format's key value-add — from v3.0; the single-process update-ipsets use case can
  use it today with its own consistent mapping.)
- **D8 — Path = Option 2:** a couple more focused unbiased rounds to drain genuine
  P1/P2, then start **1B (Rust)**; residual clarity items are validated empirically
  by the two implementations + shared conformance corpus (code is the strongest
  remaining oracle for cross-impl divergence).

### 1B kickoff decisions (approved 2026-06-21 — format locked at `bb05b7e`)

- **D9 — Rust + Go live in top-level `rust/` and `go/` dirs** (isolated from the
  autotools/C build in `src/`). `rust/` is a Cargo **workspace**; one crate now:
  **`iprange-format`** (v3 reader+writer). The Rust→C library (`cdylib`/`staticlib` +
  `cbindgen`) and the engine (Step 2) come later as separate workspace crates.
- **D10 — Minimal deps:** `sha2` (section hashes) + `memmap2` (mmap reader); pure
  Rust, no C deps; core types `no_std`-friendly where practical.
- **D11 — Build order inside 1B:** (a) on-disk types + byte (de)serialization, with
  the spec's worked examples as the first test vectors → (b) writer (streaming +
  header backpatch) → (c) reader 3 modes (metadata-only, mmap read-only,
  owned-mutable) → (d) **language-neutral** shared conformance corpus (later consumed
  by Go) → (e) fuzz/negative + Miri.
  - Refinement: the corpus lives at **top-level `conformance/`** (not under `rust/`),
    since it is shared by both language implementations (Option A, JSON manifests):
    `conformance/cases/<name>.json` + `conformance/golden/<name>.iprbin`
    (Rust-produced goldens) + `conformance/README.md` (schema, for the Go harness).

### 1C/1D decisions (approved 2026-06-21)

- **D12 — Keep the Go writer.** The early speed check put Go at ~1.7× (build) /
  ~1.9× (lookup) of Rust — under the SOW-0001 2× "drop Go writer" threshold. Full
  Go parity (read + write) is retained.
- **D13 — License stays `GPL-2.0-or-later`** (matches the iprange repo). Not switching
  to a permissive SDK license at this time.
- **D14 — Do 1D legacy read.** Implement reading the **legacy iprange v2.0 binary
  format** (`--print-binary` output: text header `BINARY_HEADER_V20` + endianness
  marker + fixed records, per `src/ipset_binary.c` / `src/ipset6_binary.c`) for
  migration, in both Rust and Go, validated by a shared corpus of legacy fixtures.

### 1B progress (2026-06-21)

- (a) types + (b) writer committed `59df113`; (c) reader committed `720abec`;
  (d) conformance corpus this commit. Writer + reader round-trip; **byte layout
  verified** against the spec (header hex-dump matches field-for-field); 34 unit
  tests + the conformance harness (11 cases: 7 golden, 4 reject) pass; clippy clean;
  reader core builds `no_std` without `alloc`.
- Remaining: **owned-mutable** reader mode (records into a `Vec` for editing);
  **(e) fuzz/negative + Miri**; then close the SOW.

> Release note (future): `rust/`+`go/` trees must be reconciled with `make dist` /
> `packaging/tar-compare` before the next iprange release (EXTRA_DIST or a filter),
> per the parent `~/src/firehol/AGENTS.md` release process. Not a 1B blocker.

## Plan

See Pre-Implementation Gate → sub-steps 1A–1D.

## Execution Log

### 2026-06-21
- Drafted; then re-sliced after the 7-reviewer design review. Byte-level format
  spec drafted (`binary-format-v3.md`). No code started.
- **Format lock (sub-step 1A).** Ran the external reviewer panel over
  `binary-format-v3.md` across **7 rounds** (glm, deepseek, minimax, qwen, mimo;
  kimi dropped after repeated empty runs). Rounds 1–2 used a fix-list prompt;
  rounds 3–7 used a clean from-scratch prompt (no fix-list) at the user's direction,
  to keep each read unbiased. Outcome by round (real byte-contract findings, all
  fixed): R1 ≈ 7 critical + 14 high (structural/safety); R2 confirmed 20/20 fixes
  (one legacy-overflow factual correction); R3 ≈ 8 determinism gaps; R4 ≈ 3
  (`header_size` pin, band ordering, lookup-disjoint) — a full-rewrite here
  introduced one contradiction; R5 ≈ 4 subtler (interning tuple, full-space
  overflow, circular sort key, the rewrite's trailing-header contradiction); R6 = 1
  new (`must_understand` per known kind) + deferred-scope criticals; R7 = 2
  procedure/wording (coalesce-on-content, safety-walk-vs-hash). Byte layout frozen
  and re-verified every round. Lessons: (a) **surgical edits + own end-to-end
  re-read** beat full rewrites — the round-4 rewrite caused the only self-inflicted
  contradiction; (b) **unbiased (no fix-list) prompts** surfaced the determinism
  gaps the verify-the-fixes framing masked; (c) detailing **deferred** signing in
  §11 kept attracting "incomplete" findings until §11/§13 were explicitly marked
  non-normative. Format **LOCKED**; design-spec `design-iprange-engine.md` §0
  doc-synced (the stale "cast to u128" claim). Ready for 1B (Rust).

## Validation

**Acceptance criteria evidence:**
- Two full native libraries — Rust reference `rust/iprange-format` + pure-Go `go/`
  (no cgo, no third-party deps) — with 100% behavioral parity.
- **Byte-identical writers:** the shared `conformance/` corpus has 7 golden
  `.iprbin` files produced by Rust; the Go writer reproduces each byte-for-byte
  (`go TestConformanceCorpus`), and a reviewer independently `cmp`-ed Rust vs Go
  outputs on a 200k-range workload.
- **Reader, 3 modes:** metadata-only, mmap read-only (Unix, §15-hardened:
  `O_NOFOLLOW` + `SEEK_HOLE` + re-`fstat` + probe), owned-mutable (`to_writer`
  byte-idempotent reload).
- **§15 safety:** full validation pipeline; never panics on hostile input (Rust
  robustness suite: every byte flipped, random buffers, all truncations; ~60k fuzz
  inputs per a reviewer; Miri-clean — no UB).
- **Legacy v1.0/v2.0 read + migration:** verified against **real** `iprange
  --print-binary` fixtures (v4 + v6), incl. the v6 lo↔hi transposition.
- **Signing NOT implemented** (slot reserved) — per scope.
- **Speed (D12):** Go ≈1.7× build / ≈1.9× lookup vs Rust — under the SOW-0001 2×
  threshold → Go writer kept.

**Tests:** Rust 44 lib + conformance + legacy + 11 robustness + speed(ignored); Go
conformance + legacy + unit + panic-safety + validation + craft/forward-compat. All
pass. **Miri-clean**; `clippy --all-targets --all-features` clean; `go vet` + `gofmt`
clean; `no_std` core builds (Rust, even without `alloc`).

**Real-use evidence:** legacy fixtures generated by the built legacy C `iprange`;
both readers parse them to identical ranges and migrate to v3 (build → read → lookup).

**Reviewer findings — 4 unbiased rounds** (panel: glm, deepseek, minimax, mimo; kimi
excluded per user's earlier "stop running kimi"), same scope each round + fix notes:
- **R1** → NOT production-grade: fixed 7 (P1 reader `type_id==1` ascending; Go writer
  UTF-8 + value-id overflow; mmap §15 hardening; eager feed-meta validation;
  `unique_ip_count` recompute; legacy consistency; `license_flags`).
- **R2** → glm found a **P1 panic** (short feed-meta OOB) → fixed + regression test;
  + u32 length guards, legacy LE-only, mmap `unix`-gate. (Also reverted a bogus
  self-inflicted change mimo caught.)
- **R3** → 2 low-risk P2s (mmap message clarity; legacy full-space symmetry) → fixed
  + tests; Go `dirCount` checked-mul; `dupMandatory` cleanup.
- **R4** → deepseek found a **P1** (feed-meta forward-compat — my own R2 exact-length
  check over-rejected future extra fields, violating §7) → fixed + tests both langs;
  **re-validated in-place by minimax/mimo/glm. All 4 reviewers: PRODUCTION GRADE,
  zero P0/P1/P2, P3-only.**

**Same-failure search:** every fix applied to BOTH languages; reviewers confirmed
"no missed parallel patterns."

**Sensitive-data gate:** PASS — code, specs, corpus, fixtures, and this SOW contain
no secrets/credentials/customer data; fixtures use documentation-range IPs only
(10.x, 192.0.2.x, 203.0.113.x, `2001:db8::`).

**Artifact maintenance gate:**
- `AGENTS.md`: no change needed (workflow/guardrails unchanged).
- Specs: `binary-format-v3.md` (locked) + **NEW** `legacy-binary-format.md`
  (current-reality legacy format); `design-iprange-engine.md` §0 doc-synced.
- Project skills: none created — per the project's documented incremental-skill
  decision (the conformance-harness + multi-lang build/test workflow is now a
  concrete candidate; tracked below).
- End-user/operator docs: `conformance/README.md` (corpus contract) + crate
  rustdoc / package godoc. No `wiki/` change — the library is not yet a CLI surface.
- SOW lifecycle: this completion.

**Spec update:** YES (`legacy-binary-format.md`). **Project-skill update:** none
(decision recorded). **End-user docs:** conformance README + API docs.

## Outcome

**Step 1 (binary-format library) — COMPLETE.** A locked v3 binary format plus two
byte-identical native libraries (Rust reference + pure Go) and a legacy v1.0/v2.0
read/migration path, all driven by a shared language-neutral conformance corpus and
validated by 4 unbiased external-review rounds that converged to a **unanimous
production-grade verdict (zero P0/P1/P2)**. Commits: format lock `bb05b7e`; 1B
`59df113`/`720abec`/`f2937cf`/`c74885d`; 1C `db39f1a`; 1D `9e0d41a`; review hardening
`edb99c2`/`bc88b73`/`b637522`/`1cbf3a2`; + this close.

## Lessons Extracted

1. **Iterating the review panel at constant scope pays off** — each round surfaced a
   real defect the prior missed (R2 a P1 panic; R4 a P1 forward-compat regression).
   Narrowing scope between rounds would have hidden them.
2. **A fix can introduce a regression** — the R2 feed-meta exact-length check (for a
   P2) broke the §7 forward-compat rule (R4 P1). Re-check each fix against the whole
   spec, not just the finding it addresses.
3. **Real artifacts beat synthetic tests** — generating legacy fixtures from the
   actual C `iprange --print-binary` independently validated the byte-layout
   reverse-engineering (esp. the v6 hi/lo transposition).
4. **Byte-identity is best proven by an independent oracle** — Rust goldens + Go
   reproduction + a reviewer's direct `cmp` gave high confidence.
5. **Reviewer scratch hygiene** — external reviewers wrote scratch files into the
   tree; keeping commits to explicit file lists (never `git add -A`) and cleaning up
   afterward kept the deliverable clean.

## Followup

- **Step 2 — the processing engine** (union/intersect/exclude/dedup/compare over the
  v3 format) → new SOW (SOW-0003).
- **Rust→C SDK** (`cdylib`/`staticlib` + `cbindgen`) and C reference alignment →
  future workspace crates.
- **Project skill candidate:** the multi-language conformance-harness + build/test/
  Miri workflow is now concrete enough to capture as a `project-*` skill.
- **P3 items (panel-agreed cosmetic, deferred):** (a) cache the parsed
  `FeedMetaView` in `Reader` to avoid per-call re-validation; (b) add reader-reject
  golden files to the shared corpus (pre-built malformed binaries + `reject_class`)
  so a 3rd-language impl inherits rejection tests; (c) rustdoc/godoc notes on the
  64-bit-host assumption + feed-meta forward-compat; (d) signing (§11) when
  prioritized.
- **Operational:** before the next iprange release, reconcile `rust/`+`go/`+
  `conformance/` with `make dist` / `packaging/tar-compare` (per `~/src/firehol/
  AGENTS.md`).

## Regression Log

None yet.
