# SOW-0024 - Structured Model Correctness

## Status

Status: completed

Sub-state: deterministic structured model proof, exposed storage repair, and
all required validation are complete.

## Requirements

### Purpose

Prove that Rust v4 structured transactions implement exact arrival-order range
semantics and transaction atomicity by comparing the public SDK against an
independent reference model across deterministic randomized operations.

### User Request

Add the missing randomized model/property correctness test for structured
mutation before treating Rust as fully proven.

### Assistant Understanding

Facts:

- Existing structured tests cover focused lifecycle, codec, recovery,
  conformance, validation, and performance cases.
- Existing randomized property tests exercise direct, membership, timestamp,
  import, query, and algebra behavior, but every such test creates
  `StructureKind::None`.
- The normative structured contract requires assignments and clears to apply in
  exact call/record arrival order, with later intervals affecting only their own
  coverage.
- The user authorized adding the missing test. Production semantics are already
  resolved by the normative specification.

Inferences:

- A per-address scalar model is independent of the engine's range splitting,
  coalescing, dictionary, refcount, and COW implementations and can therefore
  detect their semantic disagreement.
- A separate integration-test file is clearer than growing the existing
  462-line focused structured test file past the project's directional file-size
  target.

Unknowns:

- No product or design unknown remains. If the test exposes an implementation
  defect, the existing normative semantics determine the expected repair.

### Acceptance Criteria

- A permanent public-API integration test uses deterministic random operation
  sequences and an independent per-address model.
- The sequence guarantees nested overwrite and clear cases, adds random
  overlaps, and exercises both committed and aborted transactions.
- After every transaction, point lookup, canonical structured cursor output,
  named threat-feed projections, and explicit full validation agree with the
  committed model.
- The test covers scalar-only values, equal scalars with different threat
  memberships, optional and boundary locations, empty structured absence, and
  transaction-local interning/release.
- Focused, all-feature, no-default-feature, MSRV, formatting, lint,
  documentation, architecture, mmap, source-graph, and SOW gates pass.
- No Go or production-format change is made unless the new test exposes a real
  existing Rust defect.

## Analysis

Sources checked:

- `.agents/sow/specs/design-iprange-engine.md`
- `.agents/sow/specs/binary-format-v4.md`, especially sections 9A, 14, 16.2A,
  18, and 21
- `.agents/skills/project-v4-rust/SKILL.md`
- `v4/rust/iprange-livedb/tests/structured_values.rs`
- `v4/rust/iprange-livedb/tests/workflow_properties.rs`
- `v4/rust/iprange-livedb/tests/membership_import_properties.rs`
- `v4/rust/iprange-livedb/tests/membership_algebra_properties.rs`
- Rust's official integration-test organization documentation:
  `https://doc.rust-lang.org/book/ch11-03-test-organization.html`

Current state:

- Dedicated structured correctness tests are deterministic examples, not a
  randomized differential model.
- Searches across `tests/*properties.rs` find structured symbols only where the
  tests explicitly select `StructureKind::None`; no structured property test
  exists.
- The repository already uses small deterministic PRNGs and independent scalar
  arrays rather than adding a property-testing dependency.

Risks:

- A model that shares engine normalization logic would reproduce the same bug;
  the model must update addresses directly.
- Random-only input might miss the exact nested overwrite case; the sequence
  must force broad, nested, and clear operations before additional random work.
- Checking only points could miss noncanonical range splitting; cursor output
  and explicit validation must also be checked.
- Abort leakage can remain invisible if only committed rounds are tested; the
  model and database must be checked after every abort.
- Excessively large domains or unbounded seeds would make CI slow without
  materially improving semantic coverage.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- SOW-0023 proved individual structured mechanisms and representative fixed
  compositions, but its validation inventory did not include the same
  independent randomized state-machine proof used by other logical workflows.
  This leaves interactions among repeated splitting, coalescing, structure
  refcounts, membership ownership, commit, and abort under-tested.

Evidence reviewed:

- The normative specifications and project skill listed above.
- `structured_values.rs:69`, `:110`, and `:219` provide three broad fixed
  integration cases.
- `structured_value/manager_tests.rs:115` provides focused manager cases.
- `recovery/structured_tests.rs:62` provides focused damaged-input cases.
- `workflow_properties.rs:63`, `membership_import_properties.rs:117`, and the
  other property suites select `StructureKind::None`.
- Rust's official documentation confirms that a test under `tests/` exercises
  the library through its public interface.

Affected contracts and surfaces:

- Test surface: one new Rust integration-test crate.
- Runtime guidance: the Rust v4 project skill must retain this model-test gate
  for future structure kinds.
- Production API, C ABI, on-disk bytes, conformance fixtures, benchmarks, Go,
  and end-user behavior are expected to remain unchanged.

Existing patterns to reuse:

- `workflow_properties.rs` deterministic PRNG, bounded scalar domain, direct
  expected-state array, repeated public transactions, and full point checks.
- `structured_values.rs` public structured feed/membership/profile APIs,
  structured cursor checks, explicit validation helper, and cleanup pattern.

Risk and blast radius:

- Test-only expected blast radius is one new integration-test file plus the
  project skill and this SOW.
- The test may expose an existing correctness defect. Any repair remains inside
  the already normative Rust structured semantics; a required design change
  would stop implementation and return to the user.
- No compatibility, migration, security, or data-loss behavior changes are
  authorized.

Sensitive data handling plan:

- The test uses synthetic private paths, small synthetic addresses, synthetic
  feed names, and documentation-only evidence. No secret, customer, personal,
  operational, or identifying data is read or recorded.

Implementation plan:

1. Add `tests/structured_value_properties.rs` using only the public SDK.
2. Define fixed semantic profiles and threat masks plus a direct per-address
   reference model.
3. Run several deterministic seeds. In each transaction force broad/nested/
   clear transitions, add random operations, and deterministically choose
   commit or abort.
4. After every transaction compare all points, canonical typed cursor ranges,
   every named-feed projection, and a full explicit validation result.
5. Run focused and complete gates; repair only evidence-backed failures.
6. Add the permanent model-test expectation to the Rust project skill, complete
   artifact checks, close this SOW, and commit the task atomically.

Validation plan:

- Run the new integration test alone with output visible.
- Repeat it enough times to prove deterministic reproducibility and absence of
  residue/races.
- Run the complete current-toolchain all-feature and no-default-feature test
  matrices and Rust 1.74.1 in an isolated target directory.
- Run architecture, mmap storage/runtime, source-graph, Clippy with warnings
  denied, rustfmt, warning-denied rustdoc, diff checks, and the SOW audit.
- Search every property-test file again for structured coverage and search the
  new test for private IDs, physical state, content I/O, and shared engine
  normalization logic.

Artifact impact plan:

- AGENTS.md: no change expected; its project-wide architecture and testing
  philosophy already apply.
- Runtime project skills: add the structured randomized-model requirement to
  `.agents/skills/project-v4-rust/SKILL.md`.
- Specs: no change expected because behavior is already normative.
- End-user/operator docs: no change expected; this changes proof, not SDK use or
  behavior.
- End-user/operator skills: none exist in this repository.
- SOW lifecycle: SOW-0024 is the sole current SOW and will move to `done/` as
  `completed` in the same commit as the test and skill update.

Open-source reference evidence:

- No external implementation was needed. The repository's existing property
  tests are the exact local convention and the normative v4 semantics are
  project-specific. Official Rust integration-test documentation was checked
  only to confirm public-API test placement.

Open decisions:

- None. The user explicitly approved adding the missing model test; all tested
  semantics are already normative.

## Implications And Decisions

1. Use a deterministic, multi-seed differential state-machine test through the
   public SDK. This is the minimal-complete long-term choice: reproducible in CI,
   independent of engine normalization, and consistent with existing tests.
2. Do not add a property-testing dependency. The repository already uses fixed
   PRNGs and scalar models, and shrinking is not needed for this small bounded
   state machine because each failure reports seed, round, address, and
   operation context.
3. Validate after every commit and abort, not only at the end. This proves both
   visible semantics and hidden dictionary/refcount/allocation consistency at
   the exact failing transition.

## Plan

1. Implement the independent structured state-machine test.
2. Run focused validation and fix any exposed existing defect.
3. Run the complete proportional Rust gate matrix.
4. Update reusable testing guidance, close the SOW, and commit explicitly.

## Execution Log

### 2026-08-11

- Confirmed no structured randomized/model property test exists.
- Completed the pre-implementation analysis and recorded the approved test
  design before editing test code.
- Added the deterministic public-API reference-model test. Its first focused
  run reproduced a generic storage defect after an abort followed by a retry:
  visible values still matched the model, but explicit validation reported
  unsealed range/retirement pages, invalid root counts, structure-refcount
  mismatches, and allocation-partition errors.
- Root cause: committed free/reserve pages retain arbitrary unpublished bytes.
  An aborted draft and its retry use the same next logical transaction ID, so
  `existing_dirty_tag()` treated a stale nonzero checksum/link from the aborted
  attempt as proof that the page was already in the retry's dirty chain. The
  retry then neither linked nor charged that page, and commit could publish it
  without sealing it.
- Selected repair: transaction ID plus physical dirty-chain membership is the
  authoritative proof of current draft ownership. The membership walk occurs
  only when a reused page already claims the current logical transaction;
  ordinary first-use allocations and range mutations remain unchanged. This is
  deterministic, adds no heap state or on-disk format field, and also preserves
  intentional same-draft page reuse without duplicate dirty-chain entries.

## Validation

Acceptance criteria evidence:

- `v4/rust/iprange-livedb/tests/structured_value_properties.rs` runs three
  fixed seeds over 24 transactions each: 72 transactions including 18 aborts.
  Every transaction applies a forced full-domain assignment, nested overwrite,
  nested clear, and 12-32 additional random arrival-order operations over all
  128 modeled addresses.
- Seven fixed profiles cover scalar-only state, equal scalars with different
  threat memberships, absent and present-zero locations, both location bounds,
  `u32` boundary fields, scalar-zero membership, and the all-zero structure
  used as canonical absence.
- After every commit or abort, the test compares all point lookups, exact
  canonical structured cursor ranges, all six named-feed projections, lazy
  threat membership, and explicit full live validation against the independent
  per-address model.
- `draft_store_tests.rs` permanently proves that an allocated free page left by
  an aborted attempt is relinked by the retry both before and after commit-time
  sealing.
- `draft_store/storage.rs` now requires actual membership in the current
  draft's mapped dirty chain before reusing a claimed checksum/link. Ordinary
  first-use allocation still takes the prior constant-time path; there is no
  heap state, format field, page copy, or ordinary range-mutation work added.

Tests or equivalent validation:

- Focused current-toolchain model and sealed/unsealed regression tests pass.
- Current-toolchain all-feature and no-default-feature workspace correctness
  matrices pass; the exact `--all-targets` matrices also pass.
- Rust 1.74.1 `--workspace --all-features --all-targets` passes in an isolated
  target directory; the final compact profile fixture also passes a focused
  Rust 1.74.1 compile.
- `check-architecture.sh`, `check-mmap-storage.sh`,
  `check-mmap-runtime.sh`, and `check-source-graph.sh` pass. The source graph
  contains 458 sources across Linux, Windows, macOS, and FreeBSD plus the exact
  runtime-compiled native fixture.
- AddressSanitizer passes the 415-test Rust library boundary, the new structured
  model test, the 19-test Rust C boundary, and all 9 native C behavior tests.
- Clippy with warnings denied, rustfmt check, warning-denied rustdoc,
  `git diff --check`, and the project SOW audit pass.
- The release-profile `update_ipsets ci` performance guard passes every
  scenario. Representative medians are 3.52 million ranges/s for one-million
  direct replacement (1.085x accepted time) and 1.03 million ranges/s for
  one-million random structured build (1.047x accepted time); both and every
  other scenario remain within the deliberately loose CI limits.

Real-use evidence:

- This is a synthetic differential correctness test; no external dataset is
  needed because the reference domain deliberately enumerates every address
  after every operation. The release benchmark additionally runs the compiled
  public SDK over the representative one-million-range update-ipsets matrix.

Reviewer findings:

- No external reviewer or subagent was used, as required by the user. The
  implementation received a direct same-failure and full-gate audit.

Same-failure scan:

- Both production paths that claim reusable storage—free-bitmap allocation at
  `draft_store/storage.rs:56` and allocator-reserve allocation at `:280`—funnel
  through the repaired `claim_allocated()` authority.
- Other `born_txn == target_txn` comparisons operate only on tree pages already
  reachable from the selected draft roots; they cannot admit an unclaimed free
  page and do not reproduce this failure class.
- The regression exercises both possible stale tag states: an unsealed dirty
  link and a sealed CRC. The randomized model reproduces the public abort/retry
  sequence that originally exposed the defect.
- A property-suite search now finds exactly one structured randomized model
  using `NetworkEnrichmentV1`; the other property suites intentionally retain
  `StructureKind::None` for their independent direct/membership concerns.

Sensitive data gate:

- All added inputs are synthetic addresses, values, feed names, seeds, and
  temporary filenames. A durable-artifact scan found no secret, credential,
  customer, personal, private-endpoint, or proprietary incident data; the only
  URL is the official Rust documentation reference.

Artifact maintenance gate:

- AGENTS.md: unchanged; its existing v4 correctness, mmap-only, two-level API,
  and necessary-work rules already govern this repair.
- Runtime project skills: updated `project-v4-rust/SKILL.md` with the permanent
  structured public-API state-machine gate and its independence requirements.
- Specs: unchanged; committed bytes, public APIs, arrival-order semantics, and
  transaction atomicity did not change. The repair enforces the existing
  contract for unpublished internal ownership.
- End-user/operator docs: unchanged; no SDK call, output, command, default, or
  operating procedure changed.
- End-user/operator skills: none exist.
- SOW lifecycle: SOW-0024 is completed and moved to `done/` with the code,
  tests, and skill update in the same commit.

Specs update:

- No update: the defect violated existing atomic publication and checksum
  requirements; its repair introduces no new normative behavior or layout.

Project skills update:

- Added the exact structured model-test obligation and prohibited replacing its
  independent scalar model with engine normalization or physical IDs.

End-user/operator docs update:

- No update: this is internal correctness proof and repair with no public usage
  change.

End-user/operator skills update:

- None exist.

Lessons:

- Visible point/cursor correctness is insufficient evidence for COW storage:
  explicit validation after each state transition exposed hidden unsealed and
  ownership corruption while visible values still matched.
- A logical next transaction ID is not a unique unpublished-attempt identity
  because abort deliberately does not consume it. Current mapped-chain
  membership is the necessary deterministic ownership proof.
- The focused sealed/unsealed regression and independent public model are both
  needed: one pins the physical cause and the other pins user-visible semantics
  across realistic transaction sequences.

Follow-up mapping:

- No deferred implementation or valid finding remains. Earlier uses of
  “later” describe arrival-order semantics, and “future” describes when the
  permanent project-skill gate applies; neither is deferred work.

## Outcome

Rust v4 now has a deterministic randomized correctness proof for structured
mutation, named threat membership, normalization, commit, and abort. The proof
exposed and permanently repaired a generic abort/retry storage bug that could
publish unsealed reused pages. No public API, C ABI, format byte, Go code, or
ordinary mutation hot path changed.

## Lessons Extracted

- Retain the structured public-API scalar model as a mandatory gate for every
  structure-kind or structured-manager change.
- Treat current dirty-chain membership—not repeated logical transaction ID—as
  proof that mapped unpublished bytes belong to the active draft.
- Validate hidden physical invariants after every modeled commit and abort, not
  only after the final visible result.

## Followup

None.

## Regression Log

None yet.
