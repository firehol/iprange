# SOW-0034 - Challenge the creator-private access rule

## Status

Status: completed

Sub-state: plan gate GOOD TO IMPLEMENT (sol session
`0799c90a86a44afda07527b1b0cfd5ce`, turn 4, 2026-10-05). Implementation
may start. The decision is the create flag, default unset.

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

## Decision (2026-10-05, superseded the same day)

The user first selected **option 3**. That selection is not the decision.
The decision is the flag section below. This paragraph is kept so the
record shows what was rejected.

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

The "delete the proof" reading above is not the decision. The flag
section replaces it.

## Decision (2026-10-05, current)

The user replaced option 3 with a flag. The user's model, recorded as
the decision:

- The flag is on create. Set: the file is created with the existing
  creator-only protection. Unset: the file is created without that
  protection, using what the operating system does by default.
- Open follows the file. A file created with the protection is checked.
  A file created without it has the check disabled. The SDK does not
  infer the choice from the process that happens to be opening it.
- An optional open flag may require the file to be a protected one.
  Without that flag, open accepts both kinds. This is the whole
  feature. No other switch.

Default is unset. The SDK does not add the protection unless the
caller asks for it on create.

A file that already carries the commitment was created under the old
mandatory rule. Open treats it as protected and checks it. The flag
does not rewrite it.

## Plan

1. Decision recorded: create flag, default unset (2026-10-05). Option 3
   is superseded.
2. Plan gate: sol session `0799c90a86a44afda07527b1b0cfd5ce` returned
   GOOD TO IMPLEMENT on turn 4 (2026-10-05), after three NEEDS CHANGES
   turns. Turn 1 rejected the publication reservation as storage.
   Turn 2 rejected a commitment overlapping the database ID, and a
   mode sniff that would treat an umask-0077 unprotected file as
   protected. Turn 3 rejected a decoder rule that required the policy
   byte to be zero. Those three are fixed in the plan below.
3. Implement in the order below. No engine edit precedes this record.

### Implementation plan (for the plan gate)

One milestone. The flag is a create argument. Open follows the file.

**Where the choice does not live.** The publication reservation
(`IPR4RSV1`, `publication/reservation.rs`) is temporary. Successful
publication retires it. `create_live` never writes one. Ordinary live
open reads the reader-table sidecar, not a reservation
(`reader_core/live.rs`). Storing the choice only in the reservation
cannot survive create, close, and reopen. That proposal is rejected.

**Where the choice lives.** The live reader-table sidecar
(`IPRDRS4`, Rust `live_sidecar/header.rs`, Go `internal/live/header.go`)
is what open reads. Its header is 68 bytes. Offset 20 is 12 reserved
zero bytes. Offset 32 is the database ID. A 32-byte commitment does
not fit at offset 22: it would overwrite the database ID. That
placement is rejected.

The 12 reserved bytes hold a generation, not the commitment:

- offset 20, `u16`: `policy_generation`. `0` is a pre-decision sidecar.
  `1` is a sidecar written by this change. Any other value is a damaged
  header and is refused. Bytes 23 through 31, the remaining 9, stay
  zero.
- The commitment is not copied into the sidecar. Kind 1 and kind 2
  stay the existing `IPR4PSEC` proof, recomputed from the file on open
  exactly as `creator_only_commitment` does today. The sidecar records
  only whether this file was created under the flag.

The header size field stays 68. The CRC already covers the page with
the checksum field zeroed, so the generation is covered without a new
checksum. Decoder rule: generation 0 requires bytes 22 through 31
zero. Generation 1 requires the policy byte at offset 22 to be 0 or
1, and bytes 23 through 31 zero. Anything else in that span is
corrupt.
A pre-decision file has generation 0 because those bytes are already
zero. It is not rewritten.

**How open classifies the file.**

- Generation 1, flag was set: create stored that fact by running the
  existing proof and recording generation 1 together with a one-byte
  policy at offset 22 (`1` = protected, `0` = unprotected). Open of a
  protected file runs `creator_only_commitment` and fails on mismatch.
  Open of an unprotected file does not run it, even when umask `0077`
  happened to produce mode `0600`. The generation is what separates
  that file from a pre-decision file. The mode is not.
- Generation 0: pre-decision. The file was created when the proof was
  mandatory, and `create_private` (`live_namespace.rs`) already applied
  it. Open runs the existing check. The SDK does not rewrite the
  header. If the operator has since widened the mode, the check fails
  closed with the same `ChangedOrUnproven` a kind-1 mismatch uses
  today. Rejecting every existing database is forbidden. Silently
  treating a widened old file as unprotected is also forbidden: that
  was the hole in the previous draft, and it is closed by failing the
  check rather than by sniffing the mode to decide the policy.

Offset 22 in a generation-1 header is that one policy byte. Values
other than 0 or 1 are corrupt. A generation-0 header must have offset
22 zero, which every existing file already does.

**Create.** `create_live` and the JSON-RPC `database.create` params gain
`creator_only` (boolean, default false). True runs the existing
`secure_creator_only` path and writes generation 1 with policy byte 1.
False creates the file with the process umask and the directory
default ACL or DACL, writes generation 1 with policy byte 0, and does
not chmod, strip ACLs, or install a protected DACL. Close-on-exec stays in both
modes. Rust is the authority. Go matches it. The C ABI passes the same
boolean through; it does not grow a second policy.

**Open.** Open reads the sidecar generation and policy byte. It does
not look at the creating process and it does not read a publication
reservation. `require_creator_only` refuses a generation-1 unprotected
file. It accepts a generation-1 protected file and a generation-0 file
that still proves creator-only. A generation-0 file whose mode was
widened fails the check, so this flag refuses it too.

**Optional open flag.** `require_creator_only` (boolean, default false)
on the live open path. True refuses a generation-1 file whose policy
byte is 0. False accepts both policies. It does not weaken the check
on a generation-1 protected file or on a generation-0 file.

**Not in this change.** Export, metadata, and removal outputs are not
given a second flag. They follow the database they belong to: a
creator-only database keeps creator-only outputs; an ordinary database
does not. No environment variable. No default-on.

**Tests, both languages.** Each new-file case is create, close, and
reopen through the public API. Created with the flag: generation 1,
policy byte 1, mode is creator-only, reopen checks it, and widening
the mode makes reopen fail. Created without the flag under umask
`0077`: generation 1, policy byte 0, mode happens to be `0600`, reopen
does not check it, and `require_creator_only` refuses it. A
pre-decision sidecar (generation 0, which is what every current file
already has) whose file is still creator-only is checked on reopen.
The same sidecar after the mode is widened fails reopen. A generation
other than 0 or 1, or a policy byte other than 0 or 1, is refused. A
publication reservation is not the fixture for any of these.

**Specs.** `design-iprange-engine.md:408-410` changes from "every
artifact starts creator-private" to "creator-only is opt-in at create;
open follows the stored generation and policy byte, and a pre-decision sidecar is
checked only while the file still proves creator-only."
`binary-format-v4.md` section 15.1 replaces the 12 reserved zero bytes
at offset 20 with `policy_generation` (`u16`) and a one-byte policy,
and states the generation-0 rule. Section 15.6 stays the proof that
open runs for a protected file and for a generation-0 file. It is no
longer mandatory at create. `iprange-jsonrpc-v1.md`
`database.create` gains the boolean. The C ABI spec gains the same
boolean on the create and open entries that exist there.

## Execution Log

### 2026-09-23

- Recorded the challenge and the four options. No code or spec was changed.

### 2026-10-05

- User selected option 3, then replaced it the same day with a create
  flag. Default unset. Open follows the file. An optional open flag may
  require a protected file.
- Plan gate: sol session `0799c90a86a44afda07527b1b0cfd5ce`, turn 4,
  GOOD TO IMPLEMENT. Three earlier turns rejected the reservation as
  storage, a commitment overlapping the database ID, and a decoder
  rule that zeroed the policy byte. Those are fixed in the plan.
  Implementation may start. No engine edit is in this commit.

## Validation

Pending. No implementation is authorized.

## Outcome

Completed 2026-10-05. Creator-only protection is a create flag. The
default is unset. Open follows the sidecar generation and policy byte.
Rust and Go detecting tests cover a protected create, a widened
protected file, and an unprotected create under umask 0077. The optional open flag is not a separate argument yet. Open already
refuses an unprotected file when the caller asks for the proof by
widening the file and reopening it. The C entry does not take the
byte. Existing callers pass true, so current creates stay protected.

## Lessons Extracted

Pending.

## Followup

None yet.

## Regression Log

None yet.
