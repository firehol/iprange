# SOW-0036 - Relative-path JSON-RPC inputs handled per spec

## Status

Status: open

Sub-state: discovered during SOW-0035 round 7 (2026-10-07), recorded
per fit-for-purpose R7-F3; not started. Queued behind SOW-0030 per the
user's sequencing decision (2026-10-07).

## Requirements

### Purpose

Make relative JSON-RPC path inputs behave per spec `iprange-jsonrpc-v1.md`
§Paths (line 189 permits relative inputs) — either supported correctly
on both engines or refused cleanly everywhere, replacing today's three
inconsistent failure shapes.

### User Request

Derived from review findings (fit-for-purpose R7-F3 decision request,
security round-7 finding 2, parity round-7 adjudication, portability
round-7 platform analysis); the user's standing rule is that recorded
follow-ups map to real SOWs.

### Assistant Understanding

Facts:

- `iprange.v1.database.create` with a bare-relative `path` (existing
  parent) fails BOTH engines with `-32010 unresolvable/outcome_unknown`
  "creation never proved its parent directory identity" — a misleading
  terminal (parity: `outcome_unknown` also violates the project's own
  proof-d taxonomy, pre-attempt belongs in `not_started`).
- `database.create` with a relative path under a MISSING parent
  correctly answers `invalid_path`.
- `iprange.v1.export` with a relative SOURCE answers
  `name_not_found`/"feed name does not exist" for a path that exists;
  a relative DESTINATION works.
- Absolute paths work everywhere; every committed harness uses
  absolute paths, which is why no gate observed this.
- Pre-existing since the SOW-0025 era (string at `create_resolution.rs`
  from 4966eafd, 2026-07-26); platform-neutral by construction
  (empty-parent path math is cfg-free; portability: reproduces on
  Windows by construction).
- Mechanism: `create_resolution.rs:396` requires
  `supplied.directory_identity`, which the create path cannot bind for
  a bare-relative parent.

Inferences:

- The fix is a design decision: (a) resolve relative inputs against the
  service working directory per spec and support them uniformly, or
  (b) amend spec §Paths to require absolute paths and refuse relatives
  consistently with `invalid_path`. Option (a) honors the current spec
  text; option (b) changes a public contract.

## Pre-Implementation Gate

- Problem / root-cause model: see Assistant Understanding; the
  resolution path cannot prove a bare-relative parent directory
  identity, and the export source classifier misreports the failure
  class.
- Evidence reviewed: SOW-0035 execution log (2026-10-07 round-6/7
  entries and the reproduction matrix), the four round-7 role reports
  (`.local/{fit-for-purpose,security,parity,portability}/report.md`),
  `create_resolution.rs:385-397`, spec `iprange-jsonrpc-v1.md:189`.
- Affected contracts and surfaces: JSON-RPC path members (create,
  export source/destination, likely snapshot/recovery/validate),
  both engines, the schema oracle, the conformance corpus surface, and
  spec §Paths.
- Existing patterns to reuse: the engines' `invalid_path` refusal
  class; the proof-d taxonomy rules for outcome naming.
- Risk and blast radius: public-contract change if option (b); both
  engines plus oracle plus parity detectors either way.
- Sensitive data handling plan: none — no secrets involved.
- Implementation plan: pending the design decision (user).
- Validation plan: cross-engine wire parity detectors for every path
  member shape (absolute, bare-relative, sub-relative, missing parent),
  plus the native Windows confirmation probe portability specified.
- Artifact impact plan: spec §Paths, both engines' path handling,
  oracle, kit detectors; SOW lifecycle only via this file.
- Open decisions: option (a) support vs option (b) amend-and-refuse —
  a user design decision that blocks implementation.

## Plan

Pending the open decision above.

## Validation

Pending implementation.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- The parity wire-matrix detector for the path-member shapes (parity
  round 7) belongs in this SOW's validation once implemented.
