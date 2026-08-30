# SOW-0030 - v4 Go read-path and validation specialization (performance residuals)

## Status

Status: open

Sub-state: pending; created 2026-08-29 to represent the measured
Go-vs-Rust read-path and validation residuals of SOW-0027, which remains
in-progress (reopened 2026-08-29; the performance acceptance is still
open). SOW-0017 (snapshot signing) is the next active SOW after the
current one; this pending SOW documents the tracked residuals so they
stay represented per the Followup Discipline. It is not startable until
SOW-0017 unblocks and the user schedules it.

## Requirements

### Purpose

Close the measured Go-vs-Rust read-path and validation performance gaps of
the v4 SDK against the binding SOW-0027 performance acceptance
(user 1A/2A, 2026-08-29): CPU <=1.3x Rust for every substantial
acceptance scenario and peak RSS <=1.3x, no unsafe ever. Current
committed evidence: live-direct-random-lookup 1.66x and
immutable-direct-random-lookup 1.63x (rust-ratio-reader-fixed-page-
20260830.csv), live-validation 2.299x (rust-ratio-acceptance-
20260828g.csv, historical). The bounded safe-Go write leads are
measured-and-rejected in SOW-0027; the write language-floor decision
returns to the user at SOW-0027's closure decision point, and this SOW
stays scoped to the read-path and validation specialization.

### User Request

No direct user request: this SOW exists to represent the SOW-0027
follow-up commitments (per the Followup Discipline) with the recorded
evidence, so the accepted closure of SOW-0027 leaves no untracked
deferred implementation item.

### Assistant Understanding

Facts:

- The SOW-0027 final performance reviewer (Hooke, PASS for closure)
  measured at the g identity (evidence/rust-ratio-acceptance-
  20260828g.csv): live-direct-random-lookup 1.784x,
  immutable-direct-random-lookup 1.792x, live-validation 2.299x,
  nested-overwrite 4.200x.
- The same reviewer identified the cheapest fix: per-width
  specialization of the fixedLowerBound probe loops (expected lookups
  ~1.4-1.5x, into/near the read envelope) and a parent/worker split
  profile to quantify the designed containment share of validation.
- The duplication/codegen option for nested-overwrite is measured and
  rejected: the dispatch-removal A/B measured a regression
  (dispatch-removal-ab-20260830.csv: +10.52% nested-overwrite) and the
  result-transport slice left nested-overwrite at 2.31x
  (rust-ratio-writer-transport-20260830.csv); no bounded safe-Go lead
  remains that can close the <=1.3x binding, so the language-floor
  decision returns to the user at SOW-0027's closure decision point.
- SOW-0027 remains in-progress (reopened 2026-08-29); SOW-0017 is the
  next active SOW only after SOW-0027 closes.

Inferences:

- The read-path win also shaves every write-path probe, so the lookup
  specialization benefits the write scenarios too.

Unknowns:

- Whether the read-path work alone brings the write ratios inside the
  envelope; measured only after implementation.

### Acceptance Criteria

- Live-direct and immutable-direct lookup ratios land inside or near
  the 1.2-1.6x envelope with the same matched 5-sample methodology as
  SOW-0027, no regression in the 18-case CI gate, and no mmap-only
  policy change.
- live-validation reports the worker-spawn vs walker cost split; the
  validation ratio improves or is explicitly accounted by the
  containment cost.
- All SOW-0027 validation rules apply: nice-only runs, evidence CSV +
  README updates, five-reviewer level-1 final round, follow-up
  mapping.

## Analysis

Sources checked:

- v4/go/cmd/iprange-v4-bench/evidence/rust-ratio-acceptance-
  20260828g.csv (the accepted SOW-0027 close ratios).
- v4/go/cmd/iprange-v4-bench/evidence/profiles-4c4d-summary.txt and
  the SOW-0027 final review record (Hooke scope) for the probe-chain
  cause and the per-width specialization estimate.
- internal/tree/page.go fixedLowerBound and the generic gap/replace
  machinery (the measured residual causes).

Current state:

- The Go tree probes dispatch per probe through a non-inlined call
  chain (fixedLowerBound, comparePrefixKey, CompareRawKey), the same
  chain on reads and writes; lookups are allocation-free.

Risks:

- Per-width specialization duplicates probe code; the SOW-0027 earlier
  attempt at a monomorphized width-switch regressed 1-3% and was
  reverted - the specialization must be measured before acceptance.
- 32-bit metadata addressability: the supported matrix stays 64-bit.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- Go's generic tree probe chain cannot be fully inlined under the
  one-authoritative-core rule; Rust's monomorphized per-key probes
  inline. Measured: lookups 1.79x, validation 2.30x Rust.

Evidence reviewed:

- SOW-0027 close records (Validation, Followup), the g-identity ratio
  CSV, ci-go-v4-local-20260828i.log, profiles-4c4d-summary.txt.

Affected contracts and surfaces:

- internal/tree probe internals only; no wire format, no public API,
  no conformance change.

Existing patterns to reuse:

- The 4c/4d slice pattern (measure, pin, commit evidence in the same
  commit), the SOW-0027 review-gate rules, the bench harness.

Risk and blast radius:

- Read/write hot paths; the earlier width-switch attempt regressed
  1-3% and was reverted, so every step is measured at the CI gate.

Sensitive data handling plan:

- No sensitive data; benchmark artifacts only.

Implementation plan:

- 1. Profile the parent/worker validation split and pin the lookup
  probe cost (evidence CSV).
- 2. Implement per-width probe specialization under the single tree
  core; measure against the 18-case gate and the matched Rust-ratio
  matrix.
- 3. Re-evaluate nested-overwrite only if the specialization wins
  leave it as the cheapest remaining gap.
- 4. Five-reviewer level-1 round, evidence refresh, close.

Validation plan:

- Same as SOW-0027: nice-only, plain + v4work + race + vet + gofmt,
  the 18-case CI gate, matched 5-sample Rust-ratio CSV, conformance
  battery, full-codebase mmap/file-I/O gate at close.

Artifact impact plan:

- v4/go/internal/tree and evidence files; SOW lifecycle per the
  project rules.

Open decisions:

- None; start sequencing is the user's call after SOW-0017.

## Validation

Acceptance criteria evidence:

- Pending (SOW not started).

Tests or equivalent validation:

- Pending.

Real-use evidence:

- Pending.

Reviewer findings:

- Pending.

Same-failure scan:

- Pending.

Sensitive data gate:

- This SOW contains repository paths, public API names, and benchmark
  references only; no secrets, credentials, customer data, or
  incident details.

Artifact maintenance gate:

- Pending at close.

Specs update:

- None planned; wire format and public APIs are unchanged.

Project skills update:

- None planned.

End-user/operator docs update:

- The bench evidence README gains the new identity's entries at
  implementation time.

End-user/operator skills update:

- None exist.

Lessons:

- Pending.

Follow-up mapping:

- The nested-overwrite duplication/codegen option stays rejected with
  evidence unless future measurements re-open it inside this SOW.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- This SOW's own residuals are recorded here at its close.

## Regression Log

None.
