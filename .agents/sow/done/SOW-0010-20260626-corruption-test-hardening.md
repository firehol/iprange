# SOW-0010 - v4 corruption / hostile-input test hardening (close all coverage gaps)

## Status

Status: completed

Sub-state: Completed 2026-06-26. All three tiers + the KV canonical-packing validator fix delivered
in both languages; exhaustive per-check coverage (matrices); independent reviewer (codex) voted
COMPLETE CORRUPTION TESTS - 100% COVERAGE - PRODUCTION GRADE after four rounds. The KV wrong-answer
hole uncovered here is recorded as a regression on SOW-0009 (its validator) and fixed.

## Requirements

### Purpose

Regression-armor the v4 / v4.1 live-DB binary format against corrupt and hostile input. The
format is signed and consumed at scale (update-ipsets → Netdata SDKs); its readers/writers MUST
reject any corrupt, truncated, or hostile-but-checksum-valid file with a typed error — never
panic, loop, OOB, SIGBUS, or return a wrong answer. The validators already enforce this (125
checks inventoried); the gap is **test proof**: most checks have no dedicated test, so a future
refactor could silently delete one undetected. This SOW closes that gap in **both languages at
parity**.

### User Request

"create an SOW to close all corruption tests." (Following the analysis that found the v4.1
metadata work is well-covered but the broader v4.0 IP-tree + bootstrap paths are not.)

### Assistant Understanding

Facts:

- These are **test-coverage gaps, not active bugs** — the audit confirmed every check exists in
  code. The risk addressed is regression detection, not a present corruption hole.
- The CRC-restamping structural fuzz (`structural_mutation_fuzz_recrc_never_panics` /
  `TestStructuralMutationCRCValidNeverPanics`) **excludes IP-tree pages** by page-type filter and
  asserts **only no-panic** (it restamps the CRC then discards the result). Symmetric in both
  languages.
- Targeted CRC-valid rejection tests exist for the v4.1 metadata paths (dup-child scope/KV,
  shared overflow chain, shared kv_root, separator-misroute scope/KV, overflow `total_len=u64::MAX`)
  — added under SOW-0009. The v4.0 IP tree has only 2 targeted tests (unsorted leaf, record_count).
- Test infrastructure to reuse already exists: `restamp`/`finalizeChecksum`, `page_type`/`pageType`,
  `entry_count`, `kv_slot_off`/`slot`, `findPageOfType`, `pageOf`, and the build helpers
  (`Writer::create`/`CreateV4`, `into_image`/`Image()`).

Inferences:

- The single highest-leverage change is upgrading the structural fuzz to (a) include IP-tree
  pages and (b) assert "rejected OR byte-identical view" (no-wrong-answer) — this converts ~40
  checks from no-panic-only to real coverage at once.
- A few OS-layer checks (sparse hole, TOCTOU re-fstat, last-byte probe) are environment-dependent
  and may not be cleanly unit-testable; they will be attempted and, where impractical, recorded
  with rationale rather than forced.

Unknowns:

- None blocking. (Whether every OS-layer check can be deterministically unit-tested is the only
  open item; resolved during implementation, not a design decision.)

### Acceptance Criteria

- Every validator check in the audit's Inventory 1 (both languages) is either: (a) covered by a
  dedicated CRC-valid targeted test asserting rejection with the **specific typed error**; or (b)
  covered by the upgraded no-wrong-answer structural fuzz; or (c) explicitly documented in this SOW
  as environment-dependent/not-unit-testable with rationale. Verify: per-check mapping table in the
  Execution Log.
- The structural fuzz no longer excludes IP-tree pages and asserts no-wrong-answer (reject OR
  byte-identical scan/metadata vs a pristine baseline). Verify: the fuzz catches a planted
  wrong-answer (proven by temporarily defeating one validator check).
- Every new targeted test is **proven non-vacuous**: temporarily reverting the check it guards
  makes the test fail. Verify: recorded per-test in Validation.
- Rust↔Go parity: the targeted-test set and the fuzz behavior match 1:1 (including the previously
  Go-only `minor1_meta_size_pinned`). Verify: test-name diff between languages is empty for the
  corruption suite.
- Full suites green both languages: `cargo test` (default + `export-v3`) + `clippy --all-targets
  --all-features -D warnings` + `cargo fmt --check`; `go test ./...` + `go test -race -timeout 30m`
  + `go vet` + `gofmt -l`.
- Dead `BadMagic` error variant removed in both languages (or kept with a documented reason).
- External review (codex + open-model panel when nova permits) returns PRODUCTION GRADE.

## Analysis

Sources checked:

- Validators: `v4/rust/iprange-livedb/src/{reader,scope,kv,os,writer,wire,spec}.rs`;
  `v4/go/{reader,scope,kv,os,wire,spec}.go`.
- Existing tests: `v4/rust/iprange-livedb/tests/robustness.rs` + inline `#[cfg(test)]` in
  reader/writer/scope/kv/os; `v4/go/{robustness,reader,writer,kv,scope,crc32c,os}_test.go`.
- Spec threat model: `.agents/sow/specs/{design-iprange-v4-livedb.md,design-iprange-v4-scope-api.md}`
  (≈80 corruption/abuse invariants across meta/crash-safety/IP-tree/scope+KV/overflow/mmap/DoS).

Current state (the gaps to close):

- **Tier 1 — IP B+tree + fuzz (biggest hole, symmetric):** structural fuzz excludes IP-tree pages
  (`robustness.rs:~241-253` / `robustness_test.go:~248-252`) and asserts only no-panic. Only
  `unsorted leaf` and `record_count` IP checks are targeted-tested. No CRC-valid test for: IP-branch
  **duplicate-child** (`reader.rs validate_node` child pairwise-distinct / `reader.go` equivalent —
  note the IP tree uses an inline pairwise check, distinct from the scope/KV visitor mechanism),
  record **outside-node-bound / misroute**, cross-leaf overlap, `to<from`, separator bounds &
  ordering, child-pgno range, page-type-at-depth, leaf/branch tail nonzero, header reserved nonzero,
  self-pgno mismatch, empty-tree `record_count≠0`, depth/cycle guard.
- **Tier 2 — hostile read paths + bootstrap (cheap):** non-UTF-8 on the **validate/read** path for
  scope **name** (an explicitly named hardening goal in code comments) and KV **text value** (inline
  + overflow) — only the write path is tested. Meta/bootstrap field rejects: `page_size≠4096`,
  `checksum_algo`, unknown `flags` bit, `key_width`-vs-flags, `record_size` mismatch,
  **metas-disagree-on-static-identity**, minor-0 `meta_size` pin. Geometry rejects: `total_pages`
  range/overflow, `tree_height>32`, `root_pgno`/`scope_table_root`/`kv_root` out of range.
- **Tier 3 — parity + cleanup (small):** Rust lacks `minor1_meta_size_pinned` (Go has it; Rust
  enforces the rule). Dead `Error::BadMagic`/`errBadMagic()` never produced (both langs). OS-layer
  (non-regular-file, sparse hole, TOCTOU, last-byte probe) — only symlink + too-short tested.

Risks:

- **Over-strict tests rejecting valid files:** a bound/convention mistake in a crafted-corruption
  test could mask a real over-rejection. Mitigation: every targeted test first asserts the pristine
  file opens OK, then corrupts one field; and each is proven non-vacuous.
- **Fuzz upgrade false negatives:** the no-wrong-answer baseline must capture the full observable
  view (IP scan + scopes + per-scope KV) or it could miss a wrong answer. Mitigation: reuse the
  existing `kv_region_flip_never_silently_accepted` baseline approach; prove it catches a planted
  wrong-answer.
- **Line-number drift:** the audit's file:line cite the current tree; implementers must re-locate by
  symbol, not line.
- **Scope size:** ~30–40 new tests across two languages; larger commit. Low blast radius (test-only
  + possible dead-code removal); no format/behavior change.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- The v4 validators enforce ~125 corruption invariants, but the test suite proves only a fraction:
  the structural fuzz is no-panic-only and skips the IP tree, and targeted CRC-valid rejection tests
  exist mainly for the v4.1 metadata additions. A regression that deletes an unproven check would
  pass CI. Evidence: the two coverage audits (Rust, Go) + the spec threat model, summarized in
  Analysis with file:line.

Evidence reviewed:

- All v4 validators and all existing corruption/robustness tests in both languages (paths above).
- Both v4 specs' threat model (≈80 invariants). No external OSS was needed (the format is original).

Affected contracts and surfaces:

- Tests only (`tests/robustness.rs`, inline `#[cfg(test)]`, `*_test.go`), plus possible dead-code
  removal of the `BadMagic` error variant. No on-disk format, public API, or runtime behavior
  changes. No goldens change (the writer already emits correct structures; new tests reject only
  hand-corrupted images).

Existing patterns to reuse:

- Crafted-corruption helpers: `restamp`/`finalizeChecksum`, `page_type`/`pageType`, `entry_count`,
  `kv_slot_off`/`slot`, `findPageOfType`, `pageOf`, `decodePageHeader`/`PageHeader::decode`.
- The no-wrong-answer baseline pattern in `kv_region_flip_never_silently_accepted` /
  `TestScopeRegionFlipNeverSilentlyAccepted` (capture pristine view, assert equality on accepted
  reopen) — extend it into the restamping fuzz.
- The SOW-0009 targeted-test style (assert-open-ok → corrupt one field → restamp → assert typed Err)
  and the non-vacuous proof method (revert the check, confirm the test fails).

Risk and blast radius:

- Test-only; the only production change considered is removing a dead error variant. Regression risk
  near zero. Main hazard is an over-strict test masking over-rejection — mitigated as in Analysis.

Sensitive data handling plan:

- No sensitive data involved. All artifacts (SOW, tests, comments) are public-safe: synthetic IPs,
  scope names, and keys only. No secrets/credentials/customer data.

Implementation plan:

1. **Tier 1a — fuzz upgrade (both langs):** drop the IP-tree page-type exclusion in the restamping
   structural fuzz; capture a pristine baseline (IP scan + scope list + per-scope KV) and assert
   every accepted reopen is byte-identical (reject OR same-view). Prove it catches a planted
   wrong-answer. Files: `robustness.rs`, `robustness_test.go`.
2. **Tier 1b — targeted IP-tree tests (both langs):** mirror the scope/KV pattern onto IP
   branch/leaf — duplicate-child, record misroute (outside-node-bound), cross-leaf overlap,
   `to<from`, separator bounds/ordering, child-pgno range, page-type-at-depth, tail nonzero,
   self-pgno, reserved nonzero, empty-tree record_count, depth/cycle. Files: `robustness.rs`,
   `robustness_test.go` (or reader inline tests).
3. **Tier 2a — hostile non-UTF-8 read path (both langs):** scope name, KV inline text value, KV
   overflow text value crafted invalid → typed Err. Files: `robustness.rs`, `robustness_test.go`.
4. **Tier 2b — meta/bootstrap + geometry rejects (both langs):** restamp-and-reject tests for the
   classify field checks and geometry checks listed in Analysis.
5. **Tier 3 — parity + cleanup:** add Rust `minor1_meta_size_pinned`; attempt OS-layer tests
   (at least non-regular-file); remove dead `BadMagic`/`errBadMagic` (or document why kept).
6. Build the per-check coverage mapping table in the Execution Log; confirm Inventory-1 closure.

Validation plan:

- Each new targeted test proven non-vacuous (revert its guard → test fails; restore byte-exact).
- Fuzz upgrade proven by planting a wrong-answer (defeat one check) → fuzz fails.
- Full green both languages (commands in Acceptance Criteria), including `go test -race -timeout 30m`.
- Same-failure scan: ensure no other check class remains uncovered after the mapping table.
- External review (codex always; open-model panel when nova can sustain heavy reviews).

Artifact impact plan:

- AGENTS.md: likely unaffected (test commands already documented); update only if a new test runner
  convention is introduced.
- Runtime project skills: none exist; likely unaffected.
- Specs: likely unaffected (no behavior change); may add a one-line note that the threat-model
  invariants are now test-backed.
- End-user/operator docs: unaffected (internal format library).
- End-user/operator skills: unaffected.
- SOW lifecycle: on close, `Status: completed` + move to `done/` committed with the work in one
  commit.

Open-source reference evidence:

- None. The v4 format is original to this project; no mirrored/cloned OSS was used as evidence.

Open decisions:

- None. User selected "close all corruption tests" (all three tiers). The only OS-layer items that
  prove environment-dependent will be recorded with rationale rather than forced — not a user
  decision.

## Implications And Decisions

1. **Scope = all three tiers** (user decision, 2026-06-26): comprehensive corruption-test hardening,
   both languages, rather than the cheap-wins-now / IP-tree-later split. Reasoning: signed,
   security-critical format at scale; the validators deserve full regression armor.

## Plan

1. Tier 1a fuzz upgrade → 1b IP-tree targeted tests (highest value first).
2. Tier 2a non-UTF-8 read paths → 2b bootstrap/geometry rejects.
3. Tier 3 parity (`minor1_meta_size_pinned`) + dead-code removal + OS-layer attempts.
4. Coverage mapping table + non-vacuous proofs + full validation + external review.

## Execution Log

### 2026-06-26

Implemented all three tiers in both languages via two parallel agents (one per language, disjoint
files), then independently verified.

- **Rust** (`v4/rust/iprange-livedb/`): `tests/robustness.rs` +743 (fuzz upgrade + 31 targeted
  tests), `src/os.rs` +23 (inline `mmap_rejects_non_regular_file` test only), `src/error.rs` −3
  (dead `Error::BadMagic` variant + Display arm removed). Validators (reader/scope/kv/writer/
  wire/spec.rs) byte-exact; goldens unchanged.
- **Go** (`v4/go/`): `robustness_test.go` +697 (fuzz upgrade + targeted tests), `os_test.go` +11
  (`TestMmapRejectsNonRegularFile`), `errors.go` −1 (dead `errBadMagic` removed). Validators
  (reader/scope/kv/writer/os/wire/spec.go) byte-exact; goldens unchanged.
- **Tier 1a (fuzz upgrade, both langs):** the CRC-restamping structural fuzz now (a) includes
  IP-tree pages (previously excluded) by walking only *reachable* pages, and (b) asserts a
  no-wrong-answer baseline (reject OR byte-identical IP scan + scope list + per-target KV incl.
  FILE(0)) instead of no-panic-only. **Design refinement (both langs, deliberate):** only
  *redundant* structural fields are perturbed for the no-wrong-answer assertion; *data-authority*
  bytes (records/keys/values/scope-names, and KV leaf/branch `entry_count`, which has no
  cross-check) are excluded — re-CRC'ing those is a legitimately different valid file, not a wrong
  answer, so asserting baseline-equality there would be unsound. Those bytes stay covered for
  panic-safety by the pre-existing CRC-gated flip fuzzes.
- **Tier 1b:** 13 targeted IP-tree rejection tests per language (duplicate child, separator
  misroute, record to<from, cross-leaf overlap, separators-not-increasing, child-pgno range,
  page-type-at-depth, leaf/branch tail nonzero, header reserved, self-pgno, entry_count range,
  child cycle).
- **Tier 2a:** hostile non-UTF-8/NUL on the read path — scope name, KV inline text (UTF-8 + NUL),
  KV overflow text — per language.
- **Tier 2b:** 13 meta/bootstrap/geometry rejection tests per language (page_size, checksum_algo,
  flags, key_width-vs-flags, record_size, metas-disagree-static-identity, minor0 meta_size pin,
  total_pages range, tree_height>32, root_pgno/scope_table_root/kv_root range, file-size-not-
  page-multiple).
- **Tier 3:** Rust gained `minor1_meta_size_pinned` (Go already had it); both gained
  non-regular-file reject; dead `BadMagic`/`errBadMagic` removed in both.
- **Documented as not unit-testable** (environment-dependent): OS-layer sparse-hole /
  SEEK_HOLE-unavailable / TOCTOU re-fstat / last-byte probe (need a hole-less FS or a live racer);
  recorded with rationale in `src/os.rs` (Rust) and the Go report. The writer-path non-regular-file
  reject is pre-empted by `EISDIR` on `O_RDWR`; the MmapReader (`O_RDONLY`) path covers the
  Structural reject deterministically.

## Validation

Acceptance criteria evidence:

- Every validator rejection in `reader`/`scope`/`kv` (both languages) has a dedicated exact-message
  test, OR is documented unreachable-by-single-CRC-valid-mutation — proven by the two exhaustive
  coverage matrices produced this session. The KV canonical-packing wrong-answer hole the work
  uncovered is fixed (SOW-0009 `## Regression - 2026-06-26`) and tested.
- Independent reviewer verdict: **COMPLETE CORRUPTION TESTS - 100% COVERAGE - PRODUCTION GRADE**
  (codex, round 4).

Tests or equivalent validation:

- Rust: `cargo test` (default) + `cargo test --features export-v3` → 89 lib + 1 conformance + 2
  metadata + **111 robustness**, all pass; `clippy --all-targets --all-features -D warnings` + `fmt
  --check` clean.
- Go: `go test ./...` + `go test -race -timeout 30m ./...` (~506s) + `go vet` + `gofmt -l .` — all
  clean (~108 robustness Test funcs).
- Conformance + metadata goldens **byte-identical** (no regeneration) → the canonical-packing
  validator change does not over-reject.

Real-use evidence:

- Empirically reproduced the KV `entry_count` shrink wrong answer (3→2 accepted, read back 2)
  BEFORE the fix; confirmed it REJECTS after, in both languages (my own probes).

Reviewer findings:

- codex (independent OpenAI backend) carried all four rounds because the nova-backed open-model panel
  (glm/minimax/mimo/kimi/qwen/deepseek) was infra-down (30-min / 0-byte timeouts across days; tracked
  in Followup). R1 gaps → R2 more + the KV wrong-answer validator bug → R3 confirmed the canonical
  fix sound + remaining coverage → R4 voted COMPLETE. Every finding fixed; non-vacuity independently
  spot-checked each round.

Same-failure scan:

- The two exhaustive coverage matrices enumerate every `return Err` in the validators and map each to
  a dedicated test or a documented-unreachable reason — no rejection left fuzz-only.

Sensitive data gate:

- Clean. Tests/specs use only synthetic IPs, scope names, keys; no secrets/credentials/customer data.

Artifact maintenance gate:

- AGENTS.md: no update needed (test commands already documented; no new runner convention).
- Runtime project skills: none exist; unaffected.
- Specs: `design-iprange-v4-scope-api.md` updated — added "Canonical packing (anti-wrong-answer)" KV
  rule. (`design-iprange-v4-livedb.md` §6.3 commit-ordering invariant was recorded under SOW-0009.)
- End-user/operator docs: unaffected (internal format library).
- End-user/operator skills: unaffected.
- SOW lifecycle: SOW-0010 completed → `done/`, committed with the work. SOW-0009 received a
  `## Regression - 2026-06-26` (KV canonical-packing hole + fix), found+fixed in-session.

Specs update:

- `design-iprange-v4-scope-api.md` — canonical-packing rule (above).

Project skills update:

- None (no runtime project skills exist).

End-user/operator docs update:

- None affected (internal library).

End-user/operator skills update:

- None affected.

Lessons:

- A "no-wrong-answer" structural fuzz is **vacuous** for fields whose corruption returns the same
  observable view (header reserved / self-pgno) and for fixed-record counts on pages WITHOUT a
  tail-zero / canonical-packing cross-check (KV slot-directory pages) — those need DEDICATED
  exact-message tests, and the underlying canonical-packing invariant must be enforced by the
  validator (it was not — that was the wrong-answer bug).
- Broad-class assertions (`Err(Invariant(_))`) can be vacuous when a neighbouring check shares the
  class; assert the EXACT message so a test provably targets its own check.
- A corruption-test SOW can uncover real validator bugs — scope must allow a validator+spec fix
  (here, KV canonical packing), not just tests.

Follow-up mapping:

- Open-model review panel re-run when nova/litellm recovers (non-blocking; codex certified) — shared
  with SOW-0008/0009; reopen via regression if it ever surfaces a real finding.
- No deferred implementation items: every codex finding implemented; the one Rust gap the matrix
  flagged (`kv_separators_not_increasing`) was closed.

## Outcome

Completed. Exhaustive corruption / hostile-input test coverage for the v4/v4.1 live-DB format in both
Rust and Go: every validator rejection has a dedicated exact-message test or a documented unreachable
reason. The work additionally uncovered and fixed a real KV **wrong-answer** validator bug (canonical
packing; see SOW-0009 regression) and removed the dead `BadMagic` error variant. Independent reviewer
(codex) verdict: COMPLETE CORRUPTION TESTS - 100% COVERAGE - PRODUCTION GRADE.

## Lessons Extracted

See Validation → Lessons (fuzz vacuity for same-view fields; exact-message assertions; corruption-test
SOWs can surface real validator bugs).

## Followup

- Re-run the open-model review panel when nova/litellm can sustain heavy reviews (non-blocking; codex
  certified the work). Shared follow-up with SOW-0008/0009.

## Regression Log

None yet.

Append regression entries here only after this SOW was completed or closed and later testing or use
found broken behavior. Use a dated `## Regression - YYYY-MM-DD` heading at the end of the file. Never
prepend regression content above the original SOW narrative.
