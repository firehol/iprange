# SOW-0026 - v4 platform completion: Windows surface, authorized scratch, FreeBSD durable publication, Rust durability-shape pins

## Status

Status: open

Sub-state: follow-up container opened at the SOW-0025 milestone-4 close; individual items become work packages when this SOW is started.

## Requirements

### Purpose

Close the platform, durability, and scratch gaps that SOW-0025 (pure-Go exact v4 port, milestone 4) recorded as deferred, so the Go and Rust engines satisfy the full v4 acceptance contract.

### User Request

Follow-up discipline at milestone close: every deferred item must be carried by a real pending SOW (SOW-0025 close record; project AGENTS.md "Followup Discipline"). The user accepted the milestone-4 close with these items tracked.

### Assistant Understanding

Facts:

- SOW-0025 milestone-4 close records these open items (SOW-0025 close record, 2026-08-25):
  - Windows live/publication surface: Go keeps typed refusals; Rust namespace/windows.rs is the authority. Carried from the M4 scope as a tracked open item for the M5 platform acceptance review.
  - External sort + authorized scratch (recovery slice F): heap-only first with exact budget accounting; scratch/external-sort/mapped-window machinery deferred.
  - FreeBSD durable immutable publication in pure Go: refused today (no libc ACL in pure Go; Decision 2A); Rust implements it (publication/security/freebsd.rs). Scope decision requested from the user at the M4 close (reader-only amendment vs machine delivery).
  - Rust-side durability-shape pins on Apple Silicon: memmap2 floor-align extends flush ranges (16 KiB hardware pages); raw msync callers on XNU-16K need a pinned native flush shape.
  - Naked-SIGBUS asm role gate (sigbus_linux_amd64.s): roles 1..4 hardcoded; the new tripwire test (asm_role_gate_test.go) fails when a fifth MappingRole is added until the gate is extended.
  - Per-page heap allocation in the offline validation/recovery sweeps: format.InspectLayout returns *LayoutInspection (one heap alloc per visited page; Rust returns the layout by value). Tracked as a performance follow-up with a value-return refactor plus an offline-sweep allocation pin (M4 close review P3-3).
- SOW-0017 (snapshot signing, phase 2) already tracks the authenticated-snapshot work and is independent of these items.

Inferences:

- The M5 transition (SOW-0025 "Next" block) is the natural slot to expand this SOW into the acceptance-review work package.

Unknowns:

- None: the M4 close decision (2026-08-25, option A) resolved the FreeBSD scope; the remaining unknowns are implementation details discovered when this SOW starts.

### Acceptance Criteria

- Every item above is implemented, explicitly rejected with evidence, or re-mapped into a newer pending/current SOW.
- Complete SOW-0025's accepted M4 scope without carrying unexplained debt.

## Analysis

Sources checked:

- SOW-0025 close record (2026-08-25), `.agents/sow/current/SOW-0025-20260811-pure-go-exact-v4-port.md`
- Rust authority `v4/rust/iprange-livedb/src/publication/security/freebsd.rs`
- Go pure-Go stance records (Decision 2A in SOW-0025)

Current state:

- All items are recorded with evidence in SOW-0025's follow-up map.

Risks:

- FreeBSD durable publication and Windows live are the two acceptance-criteria gaps; both ship in Rust today.
- Pure-Go constraint (Decision 2A) blocks the macOS filesec machine; macOS live/publication refusals stay recorded.

## Pre-Implementation Gate

Status: blocked

Problem / root-cause model:

- Milestone-4 close produced deferred platform/durability items that belong to the milestone-5 transition; none of them block the milestone-4 functional reader/writer, so they are tracked here instead of in SOW-0025.

Evidence reviewed:

- SOW-0025 records: 4-12D native proofs, 4-12E audit, M4 close decision list.

Affected contracts and surfaces:

- Go live/publication/validation/recovery surfaces on Windows, FreeBSD; Rust flush semantics on Apple Silicon; recovery scratch budget surfaces.

Existing patterns to reuse:

- Platform gate pattern (TestMain + CreationSupported/CoordinationSupported authorities); freebsd.rs ACL machine; linux xattr machine.

Risk and blast radius:

- FreeBSD machine delivery is bounded platform code (~300-500 lines) plus a host proof cycle; Windows live is the large item.

Sensitive data handling plan:

- None of the items handle credentials or personal data.

Implementation plan:

- Not started; this container is expanded when the M5 transition begins.

Validation plan:

- Native host proofs per platform (plakam4mini, freebsd); full battery per item.

Artifact impact plan:

- Spec updates for the FreeBSD/macOS scope if amended; docs for any new capability.

Open decisions:

- 1. FreeBSD durable publication: resolved at the SOW-0025 milestone-4 close (2026-08-25) with option A: amend the M4 scope to reader-only FreeBSD and implement FreeBSD durable immutable publication here (the platform-completion milestone). The pure-Go ACL machine (raw syscalls, no libc) is the implementation path when this SOW starts; on-host proof on freebsd is required.
