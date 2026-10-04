# SOW-0034 - Challenge the creator-private access rule

## Status

Status: open

Sub-state: decision recorded 2026-10-05. The user selected option 3.
Implementation has not started. This file does not authorize editing the
engines until the plan gate returns GOOD TO IMPLEMENT.

## Requirements

### Purpose

Decide whether the engine must force creator-private access on every artifact
it creates, or whether a smaller rule is enough. The decision is for the
project over years, not for the current fix pass.

### User Request

Create an SOW to challenge the need for creator-private logic in the SDK and
in the implementation in general. Continue the current work as if this
challenge had not been made.

### Assistant Understanding

Facts:

- `design-iprange-engine.md:408` requires every engine-created artifact to
  start creator-private: mode `0600` on POSIX, a protected user-only DACL on
  Windows, independent of process defaults.
- `binary-format-v4.md:2359-2371` defines the proof: remove an inherited
  access ACL, apply mode `0600`, verify one regular link owned by the
  attempt-start effective uid, and store
  `SHA-256("IPR4PSEC" || uid || 0600)`.
- Rust implements that proof in
  `v4/rust/iprange-livedb/src/publication/security/posix.rs:45-77`.
  Go implements it in `v4/go/internal/security/security.go:60-74`.
- The current fix pass sets mode `0600` on CLI export, metadata, and removal
  outputs because those creates used the process default. That fix follows
  the current spec. It is not a decision that the spec is right.

Inferences:

- The rule exists to stop a caller umask, a directory default ACL, or a
  hard link from making a new database readable by another user before the
  application publishes it.
- The cost is a platform-specific security machine: ACL removal, uid
  commitment, Windows DACL, and a publication refusal when the filesystem
  cannot prove the policy.

Unknowns:

- Which real callers need the file to stay private before publication.
- Whether any supported filesystem cannot prove the policy and therefore
  cannot create a database today.
- Whether the application always widens the mode after publication, making
  the private window shorter than the machinery suggests.

### Acceptance Criteria

- The user decides one of the options below before any code or spec change.
- If the rule stays, this SOW closes with that decision and no code change.
- If the rule changes, the spec, both engines, the CLI adapters, and the
  detecting tests change in one later implementation SOW. This file does not
  implement that change.

## Analysis

Sources checked:

- `.agents/sow/specs/design-iprange-engine.md:408-410`
- `.agents/sow/specs/binary-format-v4.md:2359-2371`
- `v4/rust/iprange-livedb/src/publication/security/posix.rs:23-77`
- `v4/go/internal/security/security.go:28-74`

Current state:

- The rule is specified and implemented for SDK-created main and sidecar
  files. CLI export, metadata, and removal outputs were outside it and are
  being brought inside it by the current fix pass.
- A file is treated as creator-private only when the engine can prove mode
  `0600`, one link, no inherited ACL, and creator ownership. Failure is a
  hard error, not a warning.

Risks:

- Removing the rule without a replacement lets umask `000` or a directory
  default ACL expose a new database to the group or to everyone.
- Keeping the rule rejects creation on filesystems that cannot prove ACL
  state, even when the operator accepts the local permission model.
- Changing the commitment changes the bytes stored in existing sidecars.
  That is a format change, not a local cleanup.

## Pre-Implementation Gate

Status: needs-user-decision

Problem / root-cause model:

- The rule is a product decision, not a defect. It forces a private file
  because process defaults and directory ACLs are not under the engine's
  control. The challenge is whether that force is worth the platform
  machinery.

Evidence reviewed:

- The spec lines and security implementations listed above. No external
  open-source repository was checked for this challenge.

Affected contracts and surfaces:

- `design-iprange-engine.md`, `binary-format-v4.md`, the sidecar security
  commitment, Rust and Go security modules, CLI output creation, and any
  test that pins mode `0600` or `AccessPolicyUnsupported`.

Existing patterns to reuse:

- The current proof in `posix.rs` and `security.go` is the pattern to keep
  if the decision is to retain the rule.

Risk and blast radius:

- A later removal touches format commitment bytes, publication refusal, and
  both language engines. It is not safe to do inside the current fix pass.

Sensitive data handling plan:

- This SOW records modes, uids, and commitment layout only. It records no
  secrets, customer data, or private addresses.

Implementation plan:

1. Do nothing until the user selects an option.
2. If the user selects a change, open a separate implementation SOW. Do not
   implement it here.

Validation plan:

- No tests in this SOW. A later implementation SOW must prove the selected
  rule on POSIX and Windows, including umask `000` and a directory default
  ACL if those remain in scope.

Artifact impact plan:

- AGENTS.md: unaffected until a decision changes the rule.
- Runtime project skills: unaffected until a decision changes the rule.
- Specs: unchanged while this SOW is open. A decision to change the rule
  updates `design-iprange-engine.md` and `binary-format-v4.md` in the
  implementation SOW.
- End-user/operator docs: unaffected until the rule changes.
- End-user/operator skills: unaffected.
- SOW lifecycle: this file stays pending until the user decides.

Open-source reference evidence:

- None checked. The challenge is about this repository's own rule.

Open decisions:

1. Keep the current rule.
2. Keep mode `0600` at creation, but drop ACL stripping, uid commitment, and
   publication refusal.
3. Trust process umask and directory defaults, and delete the creator-private
   machinery.
4. Keep the rule for the database and sidecar only, and let CLI exports
   follow the process umask.

## Implications And Decisions

1. Keep the current rule.
   - Benefit: a new database is private even when the caller runs with umask
     `000` or the directory has a default ACL.
   - Cost: ACL, uid, and Windows DACL machinery stays, and some filesystems
     cannot create a database.
   - Risk: lowest. No format change.
2. Keep mode `0600`, drop the proof.
   - Benefit: removes most platform code.
   - Cost: a default ACL can still grant group access, and the sidecar
     commitment no longer proves the mode.
   - Risk: existing sidecars carry the old commitment. Readers must accept
     both or the format version must change.
3. Trust umask and directory defaults.
   - Benefit: smallest implementation.
   - Cost: the same export is private on one host and world-readable on
     another. This contradicts the current spec.
   - Risk: silent exposure. Not acceptable unless the user explicitly accepts
     that exposure.
4. Database private, CLI exports follow umask.
   - Benefit: the durable database stays protected, and exports match the
     caller's expectation.
   - Cost: two rules to document, and an export can still be world-readable.
   - Risk: medium. The current fix pass would need to be reverted for CLI
     outputs only.

Recommendation recorded before the decision: option 1. The user rejected
that recommendation.

## Decision (2026-10-05)

The user selected **option 3**.

The user's words, recorded as the decision and not softened: these files
are IP and security feeds, not sensitive data, and in most cases they
describe bad actors. The SDK must not add protection on top of what the
operating system does by default. The extra proof will confuse operators.

What this means:

- New artifacts are created with the process umask and the directory's
  default ACL or DACL. The SDK does not chmod to `0600`, does not strip
  an inherited ACL, and does not install a protected Windows DACL.
- The SDK does not refuse creation or publication because it cannot
  prove creator-only access.
- Close-on-exec and non-inheritable descriptors stay. Those are process
  hygiene, not a privacy policy.
- Existing files are opened as they are. A file another user can already
  read stays readable. The SDK does not tighten it and does not reject
  it for being readable.

Why the earlier recommendation does not override this: the recommendation
optimized for a threat this product does not have. The user named the
actual content and the actual operator cost. That is the product decision.

## What option 3 actually removes

This is not a mode constant. The proof is stored and checked again.

- `design-iprange-engine.md:408-410` requires every engine-created
  artifact to start creator-private, independent of process defaults.
- `binary-format-v4.md:2359-2400` stores a 32-byte `IPR4PSEC`
  commitment (POSIX kind 1, Windows kind 2) and requires a resolver to
  recompute it. A mismatch is `ChangedOrUnproven`.
- Rust implements the proof in
  `v4/rust/iprange-livedb/src/publication/security/posix.rs`. Go
  implements it in `v4/go/internal/security/security.go` and
  `security_windows.go`. Callers include publication, the live
  namespace, and the worker control page.
- The commitment is host-local coordination state. It is not part of
  the portable database bytes. An old file that carries one must still
  open after the proof is removed. Silently rejecting those files would
  be a data-loss bug, not a cleanup.

Implementation does not start in this edit. The plan gate has to return
GOOD TO IMPLEMENT first. The implementation SOW, if this file is that
SOW, must cover both engines, the CLI adapters, the specs, and a test
that an ordinary umask file is accepted and that a pre-decision file
still opens.

## Plan

1. Decision recorded: option 3 (2026-10-05).
2. Plan gate: external control reviews this SOW and returns GOOD TO
   IMPLEMENT before any engine edit. REVIEWS.md requires that gate for
   a SOW whose milestones were not already approved.
3. Implement only after that gate. Both engines, the CLI adapters, the
   specs, and the old-file open test move together. A pre-decision file
   that still carries an `IPR4PSEC` commitment must open.

## Execution Log

### 2026-09-23

- Recorded the challenge and the four options. No code or spec was changed.

### 2026-10-05

- User selected option 3. The decision, the rejected recommendation, and
  the stored-commitment blast radius are recorded above. No code or spec
  was changed. Implementation waits for the plan gate.

## Validation

Pending. No implementation is authorized.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.
