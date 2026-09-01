# SOW-0031 - history.project projection-report output path

## Status

Status: open

Sub-state: follow-up tracked from SOW-0028 round 9; not started.

## Requirements

### Purpose

Give `iprange.v1.history.project` a caller-selected output file (or bounded
cursor) for its complete `HistoryProjectionReport`, so publisher-scale
projections with hundreds or thousands of windows are representable instead of
being refused by the response-object ceiling.

### User Request

Tracked follow-up from SOW-0028 round 9 (glm-5.3-responses final review,
P1-5): the wire contract contradicted itself because a valid 1..4096-window
request whose complete inline report exceeds the 65,000-byte response object
cannot be answered. Round 9 resolved the contradiction with a pre-mutation
`output_limit` refusal (spec rule + `history.project`/`algebra.publish`
preflights). This SOW tracks the product option that lifts the refusal for
history projections: report output to a file or cursor.

### Assistant Understanding

Facts:

- The response-object ceiling is 65,000 bytes (iprange-jsonrpc-v1.md framing).
- `history.project` results are inline-only today; window reports are
  `HistoryWindowReport` (history.rs:16-24) with u64 and Cardinality129
  counters plus the window feed name.
- The round-9 preflight (algebra.rs `preflight_history_result`) refuses
  unrepresentable requests before any writer is opened or mutation occurs;
  the specification records the refusal rule.
- `algebra.publish` got the same-class preflight (`preflight_algebra_publish`)
  because its result scales with the live-source count.
- Export-family methods already have the caller-selected output-file pattern
  (`output` descriptors, `ExportWriter`, budgets) to mirror.

Inferences:

- A report-output option should reuse the export output-descriptor and budget
  machinery (publication policy, result budget, atomic publish, output facts)
  to stay consistent with the shipped API surface.

Unknowns:

- Whether the report file format should be JSONL (one window per line) or a
  single JSON document; the schema and goldens decide after implementation
  starts.

### Acceptance Criteria

- `history.project` accepts an optional output descriptor; when present, the
  complete window report is written to the caller-selected file and the
  response carries output facts plus commit/close facts, without consulting
  the inline ceiling for the window section.
- Refusal-free behavior for at least the 4096-window maximum.
- Runner case, oracle pin, and golden updated; Rust and Go suites green.

## Analysis

(Not started; implementation requires the SOW-0028 closure first. Design will
mirror the export output-descriptor and writer patterns.)

## Pre-Implementation Gate

Pending: opened as a tracked follow-up from SOW-0028 round 9; the gate is
filled when this SOW is activated.

## Validation Plan

(Not started.)

## Artifact Impact Plan

- Spec: add the optional output descriptor to `iprange.v1.history.project`.
- Schema: extend the method params and result schemas.
- Cases/goldens: new case with a large window count writing a report file.

## Open Decisions

None recorded yet; the inline/refusal behavior stays as decided in SOW-0028
round 9 until this SOW activates.
