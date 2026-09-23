# SOW-0034 - Challenge the creator-private access rule

## Status

Status: open

Sub-state: recorded, not started. The current serial fix pass continues to
implement the existing rule. This SOW does not authorize removing or weakening
that rule.

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

Recommendation: option 1, long-term-best, until a real caller shows the proof
is too strict. The machinery is already implemented and the failure is
explicit. Option 3 is the one this challenge is really testing, and it should
not be chosen without naming the caller that needs a shared file before
publication.

No option is selected. Implementation is blocked.

## Plan

1. Wait for the user decision.
2. If the decision is option 1, close this SOW with no code change.
3. If the decision is option 2, 3, or 4, open an implementation SOW and do
   not change the current fix pass in place.

## Execution Log

### 2026-09-23

- Recorded the challenge and the four options. No code or spec was changed.

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
