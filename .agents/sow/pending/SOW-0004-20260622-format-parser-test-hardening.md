# SOW-0004 - Binary-format parser test hardening (oracle, merged-file robustness, Go parity, fuzzing)

## Status

Status: open

Sub-state: Authored 2026-06-22 from a test-coverage review of the v3.0/v3.1
format library. **Not started** — implementation deferred by user; this SOW
records the scope/evidence so it can be picked up after the Step-2 engine
decision.

## Requirements

### Purpose

Harden the v3 binary-format **parsers** (Rust reference + pure-Go port) against
the only inputs they will see in production: untrusted, possibly-malformed
threat-intel files, and large multi-feed merged files. The format is frozen
(v3.0 + v3.1) and everything downstream — the Step-2 engine, update-ipsets, the
Netdata Rust/Go SDKs — depends on these two parsers never panicking, never
mis-reading, and rejecting hostile bytes cleanly. Close the test gaps **before**
higher layers are built on top, to avoid re-hardening through every layer later.

### User Request

Costa, after reviewing test strength: "We need behavior testing, including edge
cases." Decision: capture the four parser-safety items in a SOW now, implement
later. Scope = items 1–4 from the coverage review (oracle/property test for
merge; merged-file reader robustness; Go reader-hardening parity; coverage-guided
fuzzing). Items 5–6 (IPv6-merge depth, mmap §15 safety tests) are lower priority,
tracked in Followup.

### Assistant Understanding

Facts (from reading the test tree on 2026-06-22):

- **Writer/encoder + byte-identity are strongly covered**: 67 Rust `#[test]` + 18
  Go `func Test`, all green; a 21-case language-neutral corpus (`conformance/`)
  where Go replays Rust goldens byte-for-byte (`go/conformance_test.go:235-242`).
- **Merge writer correctness** is checked at interval boundaries, not just counts
  (`rust/.../src/merge.rs:220-245`, `go/merge_test.go:31-79`), plus dedup,
  coalescing, terminal-at-max, empty, determinism/order-independence.
- **Single-feed reader hardening is strong in Rust**: bit-flips, random buffers,
  truncations never panic; structural + tamper rejections
  (`rust/.../tests/robustness.rs:54-223`).

Gaps (the work):

1. **No oracle/property test for merge.** All merge correctness rests on
   hand-picked 1–3-feed cases plus Rust==Go byte-identity. A *shared* logic bug
   (e.g. wrong membership for 4-feed nested overlaps) would make both agree on the
   wrong answer and stay green. No brute-force cross-check exists; no case has >3
   feeds; IPv6 merge has exactly one case (`conformance/cases/merge-v6-overlap.json`).
2. **Merged-file reader validation is code-only.** `validate_catalog`
   (`rust/.../src/reader.rs:795`, ~10 distinct rejections) and
   `check_merged_invariants` (`:639`, 3 rejections) have **zero** crafted-bad-byte
   tests. The `reject-merge-*.json` corpus cases are **writer** rejections, not
   reader rejections of hostile bytes.
3. **The fuzz-ish sweeps never see a catalog.** `robustness.rs:118-149` runs only
   against `valid_v4()` (`:32`), a single-feed file. Merged-file reader paths
   (`catalog_feed_ids`, `check_non_sentinel`, `version_minor`⟺catalog) get no
   panic-safety coverage.
4. **Go reader hardening is a subset of Rust's.** Go has no bit-flip/random-buffer/
   truncation sweep (`func Fuzz` = 0; no `go/robustness_test.go`). Go's reader
   hardening = `go/validation_test.go` (3 tamper tests, single-feed) +
   `go/craft_test.go` (feed-meta crafts only). Go is a separate parser destined for
   update-ipsets and Netdata topology.
5. **No coverage-guided fuzzing.** 0 `cargo-fuzz` targets, 0 `go test -fuzz`.

Inferences:

- The highest-value single item is **#1 (merge oracle)** — it is the only thing
  that can catch a logic bug both implementations share, which byte-identity
  structurally cannot.
- #2/#3 (merged-file robustness) is the newest, least-exercised code and the most
  likely to harbor an out-of-bounds/panic on hostile input.

Unknowns:

- None that block authoring. Whether to also add a third-language differential
  (legacy C) as the oracle is a design question for the Step-2 engine SOW, not
  this one — here the oracle is an in-test brute-force scan, which is sufficient
  and dependency-free.

### Acceptance Criteria

- **Merge oracle/property test** in both Rust and Go: generate randomized feed
  sets (incl. ≥4 overlapping feeds, edge `feed_id`s, IPv6), build a merged file,
  and assert for sampled IPs that the reader's membership set equals an
  independent brute-force scan. Deterministic seeds; runs in the normal suite.
- **Merged-file reader robustness corpus**: crafted malformed merged files
  (bad/short catalog, non-ascending feed_ids, field length past section, non-UTF8
  field, sentinel record in a merged file, membership id absent from catalog,
  `version_minor==1` without catalog and vice-versa) each asserted to be rejected
  with the right error class — exercising every `validate_catalog` /
  `check_merged_invariants` branch, in both languages.
- **Panic-safety against merged files**: extend the bit-flip / random-buffer /
  truncation sweeps to also run over a valid merged file, in both languages.
- **Go reader-hardening parity**: a `go/robustness_test.go` mirroring
  `rust/.../tests/robustness.rs` (structural rejections, panic-safety sweeps,
  random round-trip property).
- **Fuzz targets**: a `cargo-fuzz` target on `Reader::open` and a Go `FuzzOpen`
  (`go test -fuzz`), each runnable in CI for a bounded time and seeded from the
  conformance corpus. CI wiring is in-scope only as a short smoke run; long
  campaigns stay manual.
- All existing tests stay green; no change to any on-disk byte (tests only).

## Analysis

Sources checked:

- `rust/iprange-format/tests/{robustness,conformance,speed,legacy}.rs`
- `rust/iprange-format/src/{merge,reader,writer,key,spec}.rs`
- `go/{merge_test,conformance_test,craft_test,validation_test,iprangeformat_test}.go`
- `conformance/{cases,golden,README.md}`
- `.agents/sow/specs/binary-format-v3.md` (§9 lookup, §10 values, §13 merge/catalog,
  §13.5 invariants, §15 validation)

Current state:

- Coverage is strong on the **producer** and on **single-feed reader** paths
  (Rust). It is thin-to-absent on **merged-file reader** paths and on the **Go
  reader** under adversarial input, and there is no independent oracle for merge
  semantics.

Risks (of NOT doing this work):

- A panic/OOB in the merged-file reader ships to update-ipsets/Netdata, which feed
  it real-world malformed lists → a parser DoS on an untrusted input path.
- A shared Rust/Go merge logic bug passes byte-identity and corpus, surfacing only
  as silently-wrong membership downstream (wrong bl/allow decisions).
- Go-side divergence (thinner hardening) means the embedded-in-Go consumer is the
  weak link precisely where the most malformed input arrives.

## Pre-Implementation Gate

Status: needs-user-decision

(User has deferred implementation. Gate is drafted; do not begin until the user
moves this SOW to `current/` and confirms scope/sequence.)

Problem / root-cause model:

- The library was built writer-first with byte-identity as the primary contract.
  That proved encoder equivalence but left the **decoder-under-attack** and
  **semantic-oracle** dimensions under-tested, especially for the v3.1 merged-file
  code added last in SOW-0003.

Evidence reviewed:

- See "Sources checked" above; specific file:line gaps enumerated in
  Requirements → Assistant Understanding.

Affected contracts and surfaces:

- Tests only (`rust/.../tests/`, `rust` fuzz crate, `go/*_test.go`, `conformance/`
  new malformed fixtures). No change to `src/` byte-emitting code is expected; if a
  test surfaces a real reader bug, that fix is in-scope and must preserve the
  byte contract (guarded by the existing goldens).
- CI workflow files (add a bounded fuzz smoke + the new Go test file).

Existing patterns to reuse:

- The LCG generator + `valid_v4()` helper (`robustness.rs:9-46`) and the
  `craft_v4_with_feed_meta` hand-builder (`reader.rs:1115`, `go/craft_test.go:11`)
  are the templates for crafted merged-file fixtures.
- The conformance harness (`expect: reject` + `reject_class`) already supports
  reader-rejection cases driven from JSON — extend it with merged-file reject
  fixtures rather than inventing a new mechanism.

Risk and blast radius:

- Low: additive test code. The one elevated-risk path is if a fuzz/oracle run
  uncovers a genuine parser bug — then a `src/` fix lands under this SOW, fully
  guarded by goldens + the new oracle.

Sensitive data handling plan:

- No sensitive data involved (synthetic IPs, no infra). Standard guardrails apply.

Implementation plan (ordered by value; each independently shippable):

1. **Merge oracle/property test** (Rust + Go) — highest value.
2. **Merged-file reject corpus + reader-robustness** (crafted malformed catalogs;
   extend conformance reject mechanism) — both languages.
3. **Panic-safety sweeps over a merged file** + **Go `robustness_test.go` parity**.
4. **Fuzz targets** (`cargo-fuzz` `Reader::open`, Go `FuzzOpen`) + bounded CI smoke.

Validation plan:

- All four items land with green `cargo test` + `go test ./...`; the oracle and
  robustness suites run in the default (non-`--ignored`) suite. Fuzz smoke runs
  bounded in CI; document the manual long-campaign command. Same-failure scan: after
  any reader fix, grep both parsers for the same missing-bound pattern.

Artifact impact plan:

- AGENTS.md: no change expected (test workflow already documented).
- Runtime project skills: candidate to finally capture the
  conformance+fuzz harness workflow as a `project-*` skill once it stabilizes
  (SOW-0001 deferred skill creation; revisit here).
- Specs: no normative change; possibly a note in `binary-format-v3.md` §15 that
  the listed reader checks are conformance-tested.
- End-user/operator docs, skills: unaffected (internal test work).
- SOW lifecycle: on completion, move to `done/`; map Followup items 5–6.

Open decisions:

- **D1 — Scope/sequence.** (a) All of 1–4 (recommended, long-term-best: this is
  the format's last hardening window before higher layers depend on it); (b)
  surgical 1–2 only (oracle + merged-file robustness — the two that catch
  correctness/safety bugs), defer 3–4. **Recommendation: (a).**
- **D2 — Fuzz in CI.** (a) bounded smoke each PR (recommended); (b) nightly only;
  (c) manual only. **Recommendation: (a)** — cheapest continuous insurance for an
  untrusted-input parser.

## Implications And Decisions

Pending user decision (D1, D2 above). Record choices here before implementation.

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

Tracked here (lower priority, out of the 1–4 core; do not lose):

- **IPv6 merge depth**: more multi-feed IPv6 cases (terminal-at-max, ≥3-feed
  partial overlap with lookups) — partially covered by the #1 oracle if it
  includes IPv6, but keep explicit corpus cases.
- **mmap §15 safety tests**: exercise `O_NOFOLLOW`, hole detection (PUNCH_HOLE),
  TOCTOU re-fstat, truncation-after-open using real tmpfiles
  (`rust/.../src/reader.rs:853-919`). Currently trust-by-reading.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and
later testing or use found broken behavior. Use a dated `## Regression -
YYYY-MM-DD` heading at the end of the file. Never prepend regression content above
the original SOW narrative.
