# SOW-0035 - Creator-only follows the database everywhere

## Status

Status: in-progress

Sub-state: implementation started. Spec exception removed. Detecting snapshot tests pass. Not committed.

## Requirements

### Purpose

Creator-only protection is one choice. Nothing uses it unless the user asked for it, or the source database already has it.

### User Request

The user rejected the remaining exceptions. Creator-only must be under a flag everywhere. Nothing uses it by default unless the user said so, or the database already has it.

### Assistant Understanding

Facts:

- Live create already has the flag. JSON-RPC omits it as unprotected. Existing SDK callers pass true, so current creates stay protected.
- Export, metadata, removal, query, join, and recovery report outputs already follow the source sidecar.
- Five product creates still force creator-only without reading a user flag or a source sidecar: interrupted-create repair, publication snapshot private files, publication reservations, recovery scratch, and the worker control file plus GC envelope.
- `binary-format-v4.md` said publication reservations and recovery scratch remain creator-only. That sentence is the exception the user rejected.
- Test fixtures that write mode `0600` are not product creates.

Inferences:

- A private engine file that belongs to one database follows that database. A file that belongs to no database follows the process mode.
- An old database with no recorded choice stays protected, because that is the recorded pre-decision rule. This is not a new default.

Unknowns:

- None that block the plan. Windows cannot be executed on this host.

### Acceptance Criteria

- No product create applies creator-only unless the caller passed the flag or the source database's sidecar says protected.
- An unprotected database produces unprotected scratch, reservations, snapshot private files, GC envelopes, and repair recreations.
- A protected or pre-decision database keeps those files creator-only.
- A file with no source database uses the process mode.
- Detecting tests fail if any of those creates still force mode `0600` for an unprotected source.
- Rust and Go match. Specs no longer say reservations or scratch remain creator-only.

## Analysis

Sources checked:

- `.agents/sow/specs/binary-format-v4.md` section 15.6
- `.agents/sow/specs/design-iprange-engine.md` creator-only paragraph
- `.agents/sow/done/SOW-0034-20260923-challenge-creator-private.md`
- Rust and Go create sites named in the user-facing inventory

Current state:

- Forced creates:
  - `v4/rust/iprange-livedb/src/live_lifecycle/create_resolution.rs:168` and `v4/go/internal/live/lifecycle_create_resolution.go:208` pass `true` when recreating a missing main file.
  - `v4/rust/iprange-livedb/src/publication/namespace/unix.rs:123` and `v4/go/internal/live/directory.go:116` create publication names at creator-only mode.
  - `v4/rust/iprange-livedb/src/publication/reservation_file.rs:306` and `v4/go/internal/publication/destination.go:118` prove the reservation creator-only.
  - `v4/rust/iprange-livedb/src/recovery/scratch.rs:164` and `v4/go/internal/recovery/scratch.go:189` prove scratch creator-only.
  - `v4/rust/iprange-livedb/src/worker/control.rs:811` and `v4/go/internal/worker/control_create_unix.go:28` prove the worker control file creator-only.
  - `v4/rust/iprange-livedb/src/publication/gc.rs:330` and `v4/go/internal/live/gc.go:315` prove the GC envelope creator-only.

Risks:

- A reservation security commitment is stored in the reservation record. An unprotected reservation must not pretend to carry that commitment.
- Interrupted-create repair must read the sidecar that already exists. Hardcoding `true` can turn an unprotected interrupted create into a protected file.
- Recovery and GC may run when the source cannot be classified. Fail closed: unclassified means creator-only, so a private file does not become world-readable because classification failed.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- Creator-only was originally mandatory for every private artifact. SOW-0034 made the live database opt-in and made user outputs follow the database. The private engine creates were left on the old mandatory path. The user has now rejected that split.

Evidence reviewed:

- Specs and code listed above. No external open-source repository was needed; this is an internal policy change.

Affected contracts and surfaces:

- Live create resolution, publication namespace, reservation records, recovery scratch, worker control file, GC envelope.
- `binary-format-v4.md` section 15.6 and the reservation security-commitment field.
- `design-iprange-engine.md` creator-only paragraph.
- No new JSON-RPC flag and no new C ABI argument. These files follow the database they belong to.

Existing patterns to reuse:

- Sidecar generation and policy byte, already read by `source_is_creator_only` and `sourceIsCreatorOnly`.
- `create_private(..., creator_only)` and `createPrivate(..., creatorOnly)` already take the flag. The callers that still pass `true` are the defect.

Risk and blast radius:

- Publication and recovery are durability paths. A wrong mode is a security regression; a wrong commitment is a recovery regression. Both languages must change together.
- Existing protected files are not rewritten.

Sensitive data handling plan:

- No secrets, credentials, customer data, or personal data are involved. Evidence is file paths and mode bits.

Implementation plan:

1. Correct the spec exception.
2. Thread the source policy into interrupted-create repair.
3. Thread it into publication private files and reservations, including the stored commitment.
4. Thread it into recovery scratch, the worker control file, and the GC envelope.
5. Add detecting tests for an unprotected source and a protected source.

Validation plan:

- Targeted Rust and Go tests that create an unprotected database under umask 0 and prove the private artifact is not mode `0600`.
- The same test on a protected database proves mode `0600`.
- Same-failure search for `secure_creator_only`, `SecureCreatorOnly`, and hardcoded `true` at create sites.

Artifact impact plan:

- AGENTS.md: no workflow change.
- Runtime project skills: no change.
- Specs: `binary-format-v4.md` and `design-iprange-engine.md`.
- End-user/operator docs: none name the private engine files.
- End-user/operator skills: none.
- SOW lifecycle: this file, then move to current when implementation starts.

Open-source reference evidence:

- None. The policy is internal.

Open decisions:

- Resolved by the user: no exceptions. Unclassified source fails closed to creator-only. No new public flag for these files.

## Implications And Decisions

1. User decision, 2026-10-06: creator-only is used only when the user asked, or the database already has it. No exceptions.

## Plan

1. Spec correction. Remove the reservation and scratch exception.
2. Interrupted-create repair reads the existing sidecar.
3. Publication private files and reservations follow the source database.
4. Recovery scratch, worker control, and GC envelope follow the source database, or the process mode when there is no database.
5. Detecting tests, both languages.

## Execution Log

### 2026-10-06

- Recorded the inventory and the user decision. Spec exception corrected.
- Interrupted-create repair of a missing main file follows an existing sidecar. A missing sidecar still uses the old protected default, because no choice was recorded.
- Snapshot, recovery destination, recovery scratch, and GC envelope follow the source database. A zero reservation commitment is unprotected.
- Immutable feed and algebra publish have no source database, so they use the process mode.
- The worker control file has no source database, so it uses the process mode.
- Detecting tests: Rust `snapshot_follows_an_unprotected_database`; Go `TestSnapshotFollowsUnprotectedDatabase`. Both passed. Windows was not executed.

## Validation

Pending. No implementation has started.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.
