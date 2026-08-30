# SOW-0030 - v4 Go performance residuals (language-floor continuation)

## Status

Status: open

Sub-state: pending; created 2026-08-29 to represent the measured
Go-vs-Rust performance residuals of SOW-0027, rewritten 2026-08-30 to
the actual final-identity numbers. SOW-0027 is the sole active SOW and
remains in-progress until its language-floor decision is recorded.
This pending SOW starts only if the user chooses to continue
optimization after that decision; if the user accepts the measured
floor, the residuals recorded here close as accepted limitations and
this SOW is rejected with this record as the evidence. It has no
dependency on SOW-0017 (snapshot signing); the earlier text saying it
was "not startable until SOW-0017 unblocks" was wrong and is void.

## Requirements

### Purpose

Close, or explicitly accept, the measured Go-vs-Rust performance gaps
of the v4 SDK against the binding performance acceptance (user
decisions 1A/2A, 2026-08-29): CPU <=1.3x Rust for every substantial
acceptance scenario and peak RSS <=1.3x, no unsafe ever.

Final-identity evidence (rust-ratio-final-20260830.csv, five
alternating same-session release samples per scenario, 1M records,
medians): all eight acceptance scenarios fail the CPU binding
(membership-import 1.529x, nested-overwrite 2.355x,
update-ipsets-workflow 1.925x, live-direct-random-lookup 1.525x,
immutable-direct-random-lookup 1.582x, live-direct-random-lookup-v6
1.326x, immutable-direct-random-lookup-v6 1.357x, live-validation
1.322x) and two of eight fail the RSS binding (live v4 lookup 1.351x,
live-validation 1.402x). The bounded safe-Go leads of SOW-0027
direction items 1-6 are exhausted: the authoritative expected-tree-
header parser is retained (reads -7-9%), the KeyU32 probe A/B was
neutral and reverted, the dispatch-removal A/B regressed, and the
result-transport slice left nested-overwrite at 2.31x. The remaining
Go-only costs are quantified: per-page header decode and per-probe
extent validation on reads; the Go runtime/GC share of the tree
machinery on writes; the validation graph walk (the worker containment
cost is now separated and is small: ~1.2-1.5 ms fixed plus the walk,
validation-phases-20260830.csv); and the +3.4% bytes_moved counter
attribution (necessary-work-compare-20260830b.csv), which the
SOW-0027 final review round attributes before this SOW starts.

### User Request

No direct user request: this SOW exists to represent the residuals
that survive the SOW-0027 language-floor decision, per the Followup
Discipline. The user's decisions 1A/2A (2026-08-29) and decision 2
(2026-08-30, bounded continuation, then return the language-floor
decision) govern.

### Assistant Understanding

Facts:

- The user decision 2 (2026-08-30) bounded the continuation to
  direction items 1-6 and required returning to the user for the
  language-floor decision; that decision has NOT been recorded yet
  (SOW-0027 stays in-progress).
- Per-width probe specialization (the original cheapest-fix estimate
  of the 2026-08-28 review) is implemented in SOW-0027 and measured
  NEUTRAL on the read bench (the reader uses its own direct search
  loops, not the tree probes); it no longer appears in this SOW's
  plan.
- The parent/worker validation split is measured in SOW-0027
  (validation-phases-20260830.csv): the worker cost is ~1.2-1.5 ms
  fixed plus the graph walk; the walk is the residual.
- The write-path residual (nested-overwrite 2.355x) was reduced from
  4.2x by the SOW-0027 slices; no bounded safe-Go lead that reaches
  <=1.3x is known at this SOW's creation.

Inferences:

- Any further Go optimization must be measured as an A/B at the final
  identity before retention; the direction-item-4 retain rule applies
  ("only if measurement and assembly show a real win").

Unknowns:

- Whether any still-unexplored safe-Go lead (for example, amortizing
  per-page header decode on the read loops, or reducing the
  validation walk's parsed-page authoring) can move any scenario
  inside <=1.3x; measured only inside this SOW.

### Acceptance Criteria

- The recorded residual at the language-floor decision is either
  accepted with user sign-off (this SOW closes as rejected/not worth
  doing with this record as evidence), or the lead the user selects
  lands the scenario inside the binding <=1.3x CPU and <=1.3x peak
  RSS contract with the same matched 5-sample same-session
  methodology, no regression in the CI gate, and no mmap-only policy
  change.
- Any counter-parity residual (the +3.4% bytes_moved attribution) is
  either attributed to a real Go/Rust behavioral difference with
  Rust references, or closed by making the counting points
  identical.
- All SOW-0027 validation rules apply: nice-only runs, evidence CSV +
  README updates, five-reviewer level-1 final round, follow-up
  mapping.

## Analysis

Sources checked:

- rust-ratio-final-20260830.csv (the final-identity matrix, commit
  39df5b0b; the only acceptance evidence).
- validation-phases-20260830.csv and necessary-work-compare-
  20260830b.csv (the SOW-0027 item 2/3 evidence).
- The SOW-0027 Status sub-states for direction items 1-6 (the
  exhausted-lead records and profiles).

Current state:

- All eight scenarios fail the <=1.3x CPU binding; the floor decision
  is pending with the user.
- The retained tree-header parser improved reads ~7-9%; reads remain
  1.33x-1.58x.

Risks:

- Further micro-optimization without a measured A/B is wasted compute;
  the resource budget rules of AGENTS.md apply (no repeated
  whole-program analysis, unit-scale A/Bs, every heavy step named
  with its expected cost before it runs).
- No unsafe code is authorized under any option.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- Go's per-page header decode, per-probe extent validation, runtime/
  GC share on writes, and the validation walk are the quantified
  residual costs; the write machinery residual is partly a counting
  attribution question (bytes_moved +3.4%).

Evidence reviewed:

- rust-ratio-final-20260830.csv, validation-phases-20260830.csv,
  necessary-work-compare-20260830b.csv,
  tree-header-parser-ab-20260830.csv, probe-key-u32-ab-20260830.csv,
  rust-ratio-writer-transport-20260830.csv,
  dispatch-removal-ab-20260830.csv.

Affected contracts and surfaces:

- bench scenarios and v4/go internal reader/validation hot paths
  only; no wire format, no public API, no conformance change.

Existing patterns to reuse:

- The SOW-0027 A/B discipline (interleaved same-session 5x1,
  retain-only-if-win, evidence CSV in the same commit), the bench
  harness, the review-gate rules.

Risk and blast radius:

- Read/write/validation hot paths; every step measured at the CI gate
  and the matched Rust-ratio matrix before retention.

Sensitive data handling plan:

- No sensitive data; benchmark artifacts only.

Implementation plan (starts only after the user selects a lead):

- 1. If the user accepts the floor: close this SOW as rejected with
  the residual record as evidence; no code changes.
- 2. If the user selects a lead: implement it unit-scale with an
  interleaved A/B, retain only on a measured win, then re-run the
  full final-identity matrix.
- 3. Attribute the bytes_moved +3.4% residual against the Rust
  reference before any write-path work.
- 4. Five-reviewer level-1 round, evidence refresh, close or return.

Validation plan:

- Same as SOW-0027: nice-only, plain + v4work + race + vet + gofmt,
  the CI gate, matched 5-sample Rust-ratio CSV, conformance battery,
  full-codebase mmap/file-I/O gate at close.

Artifact impact plan:

- v4/go hot-path internals and evidence files; SOW lifecycle per the
  project rules.

Open decisions:

- The language-floor decision (SOW-0027 closure decision point):
  accept the measured floor, or select one of the residual leads.
  This SOW does not start until that decision is recorded.

## Validation

Acceptance criteria evidence:

- Pending (SOW not started; the final-identity matrix that defines
  the floor lives in SOW-0027).

Tests or equivalent validation:

- Pending.

Real-use evidence:

- Pending.

Reviewer findings:

- Pending; the SOW-0027 five-reviewer delta round (direction item 6)
  also attributes the +3.4% bytes_moved residual.

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

- The residuals in the Requirements/Purpose sections are this SOW's
  owned scope; anything the user accepts at the language-floor
  decision closes here with the decision record; anything rejected
  with evidence is recorded here as not worth doing.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- This SOW's own residuals are recorded here at its close.

## Regression Log

None.
