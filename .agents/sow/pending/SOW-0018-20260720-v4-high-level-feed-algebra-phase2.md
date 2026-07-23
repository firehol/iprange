# SOW-0018 - V4 High-Level Feed Algebra Phase 2

## Status

Status: open

Sub-state: deferred until the Phase-1 v4 format and core SDK are reliable and
measured

## Requirements

### Purpose

Add useful multi-file threat-intelligence operations on top of the proven v4
SDK without complicating or delaying the core format.

### User Request

Agree on a preliminary high-level direction now as a format-feasibility check,
but defer detailed API design, implementation, and extensive analysis until the
v4 format and SDK are reliable and performant.

### Assistant Understanding

Facts:

- A result feed is always a materialized/published v4 file, never an in-memory
  feed object.
- Internal bounded streaming and k-way merging must avoid temporary materialized
  feeds; the only materialized output is the private inode that becomes the
  published v4 result.
- Feed names are global logical identities across supplied v4 files. Same-named
  feeds are virtually aggregated during enumeration/merge without first writing
  a temporary combined feed.
- Set-producing operations are expected to include merge/union, intersection,
  and exclusion. Analytical operations include comparison, equality, overlap,
  and counting. The specialized retention full-snapshot refresh is already a
  Phase-1 exact workflow and is not part of this SOW.
- Multi-feed results are expected to support preserving global named feeds or
  flattening coverage into one caller-named feed.

Inferences:

- The Phase-1 advanced logical layer, high-level single-feed/direct/retention
  workflows, name-based multi-feed import, catalog/cursors, publication, exact
  cardinality, and resource budgets should be sufficient without a new page type
  or second format.
- Exact API and statistics choices should use measured Phase-1 behavior rather
  than speculative optimization.

Unknowns:

- Exact operation signatures, arity, direct-value handling, statistics schema,
  intersection/exclusion feed projection, result identity, batching, and error
  precedence remain intentionally undecided. Phase-1 cancellation and resource
  budget semantics remain common requirements rather than open Phase-2 choices.

### Acceptance Criteria

- The final API implements the agreed operations using v4 inputs and publishes
  every set-producing result as a v4 file.
- Same-named feeds across inputs aggregate logically without mandatory temporary
  v4 materialization.
- Normal algebra uses bounded heap, ordered input enumeration, and the one
  private final-output inode; it does not create external sorting files.
- Preserve-feeds and flatten-to-one-feed output modes have exact tested semantics.
- Operations remain bounded in memory and file descriptors and have measured
  scaling on update-ipsets-shaped workloads.
- The work does not introduce another binary format or operation-specific v4
  storage structures without measured evidence and an explicit user decision.

## Analysis

Sources checked:

- `.agents/sow/current/SOW-0016-20260714-final-v4-reconciliation-and-production-hardening.md`
- `.agents/sow/specs/binary-format-v4.md`
- `.agents/sow/specs/design-iprange-engine.md`
- `wiki/merge.md`, `wiki/intersect.md`, `wiki/exclude.md`, and `wiki/compare.md`
- `firehol/update-ipsets @ e593366f7b0a`, including
  `pkg/scheduler/processing_loop.go:47-68`,
  `pkg/engine/run_pipeline.go:40-136`, and
  `pkg/engine/finalize.go:41-62`

Current state:

- The Phase-1 format/core SDK remains under design and has not established its
  final reliability or performance evidence.
- The current SOW had begun specifying detailed multi-file algebra before the
  core SDK existed; user decision 46 moved that work here.

Risks:

- Designing this API from unmeasured assumptions can stall or distort Phase 1.
- Deferring every feasibility check could miss a necessary primitive, but the
  Phase-1 catalog/cursor/writer requirements address that risk without freezing
  this API.
- Preserved provenance can produce many membership combinations and large
  outputs; Phase 2 must measure heap, descriptors, final-output storage, and
  scaling rather than assume it is cheap.

## Pre-Implementation Gate

Status: blocked

Problem / root-cause model:

- Multi-file algebra is product logic above the v4 storage engine. Freezing it
  before the storage SDK is proven creates speculative complexity and delays the
  format that it depends on.

Evidence reviewed:

- The sources listed under `## Analysis` establish the legacy operation set, the
  intended v4 catalog/cursor primitives, and update-ipsets' independent feed
  processing model.

Affected contracts and surfaces:

- Rust, Go, and C high-level SDK APIs; v4 input/output publication; global feed
  enumeration; statistics; resource budgets; tests; benchmarks; and user docs.

Existing patterns to reuse:

- Legacy `iprange` address algebra is the behavioral oracle.
- Phase-1 named-feed catalog enumeration and ordered cursors provide virtual
  same-name aggregation.
- Phase-1 bounded writers and publication provide v4 result materialization.

Risk and blast radius:

- Incorrect feed projection or name aggregation can silently change threat-
  intelligence meaning. Unbounded aggregation can exhaust memory, descriptors,
  or private final-output storage. Result publication failures can expose incomplete files
  unless Phase-1 publication contracts are reused exactly.

Sensitive data handling plan:

- Use synthetic feed names and IP ranges in specifications, tests, and
  benchmarks. Store no production feed contents, credentials, customer data,
  private endpoints, or identifying operational details in durable artifacts.

Implementation plan:

1. Reassess requirements against the completed Phase-1 SDK and measurements.
2. Resolve the open API/statistics/projection decisions with the user.
3. Specify and implement operations in Rust, Go, and the stable C surface.
4. Add cross-language semantic, failure, publication, and scaling coverage.
5. Update specifications, documentation, skills, and SOW evidence.

Validation plan:

- Cross-language fixtures for every operation and output mode.
- Same-name aggregation across multiple files without temporary combined feeds.
- Exact result-v4 reopen and validation in both languages.
- Failure/cancellation/publication fault injection.
- Memory, descriptor, private-output storage, and asymptotic benchmarks; normal
  algebra must demonstrate zero external sorting files.
- Update-ipsets-shaped end-to-end workflows.

Artifact impact plan:

- AGENTS.md: update commands/authority only if the delivered API changes repo
  workflow.
- Runtime project skills: capture the proven algebra/conformance workflow if it
  becomes reusable.
- Specs: add the exact high-level API contract without changing v4 bytes unless
  separately justified and approved.
- End-user/operator docs: document operations, output modes, statistics, and
  failure behavior.
- End-user/operator skills: update any downstream SDK skill introduced by then.
- SOW lifecycle: remain pending until SOW-0016 completes; do not execute both
  SOWs together.

Open-source reference evidence:

- No new external repository was needed to record this deferred user decision.
  Any implementation research will be performed when this SOW becomes current.

Open decisions:

- All exact Phase-2 API decisions listed under `Unknowns` remain open by design.
- Implementation is blocked until Phase 1 is complete and those decisions are
  presented with measured evidence.

## Implications And Decisions

1. User decision (2026-07-20): perform only a preliminary feasibility study
   during Phase 1; defer detailed multi-file feed-algebra API design and
   implementation.
2. User decision (2026-07-20): a result feed is always a v4 file. Internal
   streams are not public in-memory feed results.
3. User decision (2026-07-20): same-named feeds across input files represent one
   global logical feed and are aggregated virtually without mandatory temporary
   file materialization.
4. Preliminary direction (not a frozen API): set-producing operations return a
   v4 result plus statistics; analytical operations may return counters only;
   result shaping supports preserved global feeds or one flattened feed.

## Plan

1. Wait for SOW-0016 completion and its measured SDK evidence.
2. Reopen design with exact evidence and user decisions.
3. Implement and validate as one later SOW.

## Execution Log

### 2026-07-20

- Created this pending SOW to keep detailed multi-file algebra out of the Phase-1
  implementation critical path while preserving the agreed feasibility target.
- No implementation or detailed API analysis was performed.

## Validation

Acceptance criteria evidence:

- Pending; implementation has not started.

Tests or equivalent validation:

- Pending; this SOW records future work only.

Real-use evidence:

- Pending until the Phase-1 SDK is usable.

Reviewer findings:

- Pending until a meaningful implementation milestone exists.

Same-failure scan:

- Pending until implementation changes exist.

Sensitive data gate:

- The current planning artifact contains only synthetic examples and repository
  source references; no sensitive data is present.

Artifact maintenance gate:

- AGENTS.md: unaffected by creation of this pending tracker.
- Runtime project skills: unaffected because no workflow was implemented.
- Specs: Phase-1 specs will record only the feasibility boundary.
- End-user/operator docs: unaffected until the API exists.
- End-user/operator skills: none currently exist.
- SOW lifecycle: correctly remains `open` under `.agents/sow/pending/`.

Specs update:

- Pending Phase-1 feasibility-boundary synchronization in SOW-0016.

Project skills update:

- No implemented workflow exists to capture yet.

End-user/operator docs update:

- No public API exists to document yet.

End-user/operator skills update:

- No output/reference skills currently exist.

Lessons:

- Detailed product APIs should be designed from a proven storage SDK, while a
  small feasibility boundary prevents accidental format dead ends.

Follow-up mapping:

- High-level v4 algebra is tracked by this pending SOW.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

Wait for SOW-0016 completion.

## Regression Log

None yet.
