# SOW-0017 - v4 Snapshot Signing Phase 2

## Status

Status: paused

Sub-state (2026-08-26): paused by user decision (SOW-0027 Open Decision 1,
option A): the Go/Rust SDK parity reconciliation (SOW-0027) is the
prerequisite for signing design; the six wire/provider/trust/performance
decisions presented on 2026-08-26 remain recorded and unresolved until
SOW-0027 completes and the restored unsigned SDK is accepted. This SOW stays
in `current/` while paused; the earlier (2026-08-26) unblock note is below.

Sub-state (2026-08-26): unblocked. The prerequisite evidence now exists: the
pure-Go port and the final bidirectional Go/Rust Phase-1 conformance completed
(SOW-0025 milestone 5, closed 2026-08-25), and SOW-0026 closed the platform
completion (Windows live/publication surface, FreeBSD durable publication,
authorized recovery scratch + external sort, Rust XNU-16K flush-shape pins, the
Store split-window seam, the Go live history-projection source port, and the
Rust windows removal-arm repair; SOW-0026 gate-closed 2026-08-26). This SOW's
implementation plan step 2 (benchmark representative unsigned snapshots) is
recorded below; step 3 (unresolved wire, provider, trust, and rollout
decisions) is presented to the user; no signing design or implementation work
starts until the user resolves those decisions (plan step 3) and the v4
specification is updated (plan step 4).

## Requirements

### Purpose

Add authenticated public snapshots only after the core v4 Go, Rust, and C SDK
surfaces are correct, durable, cross-language compatible, bounded, and measured.
The phase must build on observed SDK behavior instead of delaying the storage
engine with speculative crypto API and performance decisions.

### User Request

Treat snapshot signing as Phase 2. First prove that a reliable SDK exists and
measure its performance; do not work on how signing keys reach the SDK before
then. When Phase 2 starts, use a signing-provider boundary rather than requiring
the engine to receive raw private-key bytes.

### Assistant Understanding

Facts:

- The Rust unsigned SDK and Rust-provided C ABI are complete and measured.
- SOW-0025 owns the pure-Go peer and final bidirectional Phase-1 proof.
- No v4 format has been released, so Phase 2 may revise those bytes before the
  first release without a compatibility obligation.
- Earlier decisions selected an embedded Ed25519ph signature, explicit
  verification, and a fused validate/build/sign publication operation as product
  intent, but decision 45 removes all such work from SOW-0016.
- The selected future key boundary is a provider that exposes the matching public
  key and performs the fixed signing operation; the core engine need not accept
  raw private-key bytes.

Inferences:

- Signing overhead, extra file passes, publication behavior, and the correct API
  cannot be finalized responsibly until core snapshot size and throughput are
  measured.
- Keeping this as a real pending SOW preserves the product intent without making
  speculative signing fields or dependencies part of the Phase-1 engine.

Unknowns:

- Exact wire placement and whether the proven core layout should be extended or
  revised before the first v4 release.
- Exact language-specific provider interfaces and local-key/HSM/remote adapters.
- Measured signing, verification, and extra-I/O cost on representative artifacts.
- Final publisher trust, key-rotation, and replay-policy integration boundaries.

### Acceptance Criteria

- The pure-Go port and final bidirectional Go/Rust Phase-1 conformance complete
  with resource bounds, durability evidence, real-use SDK evidence, and
  representative benchmarks.
- Phase-2 design starts from those measurements and records every new wire/API,
  security, durability, and performance decision before implementation.
- Go, Rust, and the Rust-provided C ABI expose equivalent signing and verification
  semantics without requiring the engine to own raw private-key bytes.
- Signed artifacts are structurally validated, cryptographically verified, and
  atomically published with truthful residue/outcome reporting.
- If Phase 2 changes any v4 byte or bootstrap rule, every Phase-1 fixture is
  removed or replaced, both writers regenerate the complete corpus, both readers
  cross-open every new artifact, and the final readers reject Phase-1 experimental
  bytes. No Phase-1 compatibility parser or skipped fixture remains.
- Permanent known-answer, malformed-signature, tamper, key-mismatch, callback
  failure, cleanup, crash, cross-language, and performance tests pass.

## Analysis

Sources checked:

- `.agents/sow/current/SOW-0016-20260714-final-v4-reconciliation-and-production-hardening.md`
- `.agents/sow/specs/binary-format-v4.md`
- Go `crypto/ed25519` Ed25519ph options and verification interface.
- Rust `ed25519-dalek` prehashed signing and strict verification interface.
- `sigstore/cosign @ 089731c31f75` signer/verifier abstraction and public-key
  exposure patterns.

Current state:

- The Rust and Rust-provided C Phase-1 SDK surfaces exist; the pure-Go peer,
  final bidirectional proof, signing API, and signing implementation do not.
- A prior draft mixed signing into the core format and acceptance gate before core
  reliability or performance had been demonstrated. Decision 45 removes that
  dependency and makes this SOW the sole tracker for signing.

Risks:

- Starting early would stall the higher-priority storage SDK and optimize an API
  around unmeasured behavior.
- Starting after a v4 release could require a v5 wire identity; this is why the
  current Phase-1 bytes remain explicitly unreleased and experimental.
- Mishandled prehash/context semantics, provider mismatch, private-key exposure,
  signature malleability, cleanup residue, or ambiguous publication outcomes are
  security and operational risks that require a dedicated phase.

## Pre-Implementation Gate

Status: resolved (2026-08-26); the pure-Go peer, the final bidirectional
Go/Rust Phase-1 evidence, and the representative unsigned snapshot benchmarks
now exist (evidence below)

Problem / root-cause model:

- The signing feature was being specified before the SDK that would host it
  existed. That inverted dependency stalled the core engine and made signing
  performance and API conclusions speculative.

Evidence reviewed:

- Binding decision 45 in completed SOW-0016.
- Completed Rust proof in SOWs 0020-0024 and the pending Go-port boundary in
  SOW-0025.
- The prior signature wire/API draft and final-authority audit findings recorded
  in SOW-0016.
- Official Go and Rust Ed25519ph interfaces and the open-source reference below.

Affected contracts and surfaces:

- Final v4 meta/page layout, immutable snapshots, validation, publication,
  verification, Go/Rust/C APIs, update-ipsets integration, conformance, security
  documentation, and performance budgets.

Existing patterns to reuse:

- The proven Rust unsigned snapshot, explicit validation, immutable-open,
  temporary publication, structured outcome, and abandoned-artifact APIs.
- Provider abstractions that bind signing capability to a matching public key.

Risk and blast radius:

- Potential wire change before the first v4 release; crypto and key-management
  dependencies; extra sequential I/O; publication failure states; three SDK
  surfaces; and downstream publisher/consumer policy.

Sensitive data handling plan:

- Use only generated test keys and public standard vectors in durable artifacts.
- Never store production private keys, credentials, publisher secrets, customer
  data, private endpoints, or identifying operational data in this repository.

Implementation plan:

1. Reopen this gate only after SOW-0025 completes the pure-Go peer and final
   bidirectional Phase-1 evidence. DONE (SOW-0025 milestone 5 and SOW-0026
   closed; evidence in the status section above).
2. Benchmark representative unsigned snapshots and model signing/verification I/O.
   DONE (2026-08-26): the Rust authority `snapshot_to` scenario (live -> compact
   immutable snapshot, `SnapshotPublicationPolicy::FailIfExists`) measured under
   `nice` on this workstation (release bench binary, 1 warmup, 5/3 samples):
   - 100,000 ranges: p50 9.53 ms (10.49 M units/s), 31 alloc calls / 939 bytes,
     file 1,425,408 logical bytes, RSS peak 8,664 KiB, 4 FDs, within the accepted
     CI limit (ratio 1.025).
   - 1,000,000 ranges: p50 44.35 ms (22.55 M units/s), 31 alloc calls / 939
     bytes, file 14,176,256 logical bytes, RSS peak 35,596 KiB, 4 FDs, within
     the accepted CI limit (ratio 1.038).
   Signing model (Ed25519ph, the recorded product intent): the prehash
   SHA-512 over the artifact dominates. At ~2-6 GB/s hashing, a 14.2 MB
   snapshot adds roughly 2.5-7 ms of prehash plus a fixed ~50-100 us Ed25519ph
   sign, i.e. about +7-18% on the 1 M snapshot and +30-80% on the 100 k one IF
   the prehash runs as a separate read pass; folding the prehash into the
   publication write pass (the snapshot writer already copies every byte) costs
   only the CPU hashing time with no extra file pass. Verification is the
   prehash plus a fixed ~30-80 us verify. Resources available without new
   dependencies: Go stdlib crypto/ed25519 (Ed25519ph via ed25519.Options,
   SHA-512, Go 1.26.4); Rust pins sha2 already and would add ed25519-dalek.
3. Present unresolved wire, provider, trust, and rollout decisions to the user.
   OPEN (2026-08-26): the decisions below are presented to the user; nothing is
   implemented until resolved.
4. Update the exact v4 specification before adding crypto code or dependencies.
5. If the wire changes, replace the entire Phase-1 conformance corpus and its
   generators before claiming one exact v4 identity.
6. Implement and cross-validate Go, Rust, C ABI, and update-ipsets-facing behavior.

Validation plan:

- Official Ed25519ph known-answer and negative vectors.
- Cross-language signing/verification in both directions.
- Conditional complete Go/Rust base-semantic cross-open after any wire change,
  plus strict rejection of all Phase-1 experimental fixtures.
- Full structural validation plus signature verification of final isolated output.
- Provider/key mismatch, failure injection, crash, cleanup residue, replay inputs,
  bounded-memory, and measured extra-pass tests.

Artifact impact plan:

- AGENTS.md: update final project success criteria when Phase 2 starts or ships.
- Runtime project skills: add reusable crypto/publication workflow only after it
  has been proven; no generic skill now.
- Specs: revise the exact v4 wire and architecture from measured Phase-1 reality;
  if bytes change, replace the conformance corpus and reject the old identity.
- End-user/operator docs: document publisher and verifier operation when shipped.
- End-user/operator skills: assess after public workflows exist.
- SOW lifecycle: remain pending/open until SOW-0025 completes; never execute both
  SOWs as one batch.

Open-source reference evidence:

- `sigstore/cosign @ 089731c31f75`
  - `cmd/cosign/cli/signcommon/common.go:59-77`
  - `internal/key/svkeypair.go:37-57`
  - `internal/key/svkeypair.go:103-143`

Open decisions:

- Intentionally unresolved until the SOW-0025 evidence gate is satisfied: exact
  signature wire layout, language-specific provider types, signer invocation
  contract, key-ID policy, trust rotation, replay integration, and performance
  budget.

## Implications And Decisions

1. Earlier product intent: public distribution should eventually use embedded
   Ed25519ph signatures, explicit verification, and an atomic publication path.
2. Decision 45 (2026-07-17): signing is Phase 2 and must not delay the core SDK.
   No signing design or implementation begins until SOW-0016 proves reliability
   and performance.
3. Decision 45A (2026-07-17): when this phase begins, use a signing-provider
   boundary that exposes the corresponding public key; do not require the core
   engine to accept raw private-key bytes.

## Plan

1. Wait for the SOW-0016 completion gate.
2. Review measured core SDK and snapshot behavior.
3. Resolve the remaining product and API decisions.
4. Implement, validate, document, and close Phase 2 independently.

## Execution Log

### 2026-07-17

- Created as the real follow-up for work explicitly removed from SOW-0016.
- No implementation or final wire design was performed.

### 2026-08-26

- Gate unblocked: SOW-0025 milestone 5 and SOW-0026 closed; the pure-Go peer,
  the bidirectional Go/Rust Phase-1 conformance, and the platform completion
  evidence exist.
- Implementation plan step 2 done: representative unsigned snapshot benchmarks
  recorded in the Pre-Implementation Gate (Rust authority `snapshot_to`
  scenario, 100 k and 1 M ranges, under `nice`).
- Implementation plan step 3 started: the unresolved wire, provider, trust,
  replay, and performance decisions are presented to the user; implementation
  is blocked on the resolution.
- Paused (2026-08-26): the presented decisions are superseded by the Go/Rust
  parity work (SOW-0027), which owns the unsigned-SDK prerequisite this SOW
  depends on; the decisions stay recorded here for the re-open.

## Validation

Acceptance criteria evidence:

- Pending; the prerequisite core SDK does not yet exist.

Tests or equivalent validation:

- Not run because implementation is intentionally blocked.

Real-use evidence:

- Pending SOW-0016 completion and representative unsigned artifacts.

Reviewer findings:

- The SOW-0016 final-authority audit showed that premature signing details were
  incomplete and had begun to distort the unsigned snapshot contract.

Same-failure scan:

- SOW-0016 removes signing requirements from its specs, plans, conformance, and
  implementation surfaces; this pending SOW becomes the only tracker. At Phase-2
  close, search for and remove every stale Phase-1 fixture/reference if the wire
  changed.

Sensitive data gate:

- This SOW contains no raw secrets, credentials, private keys, customer data,
  personal data, private endpoints, or proprietary incident details.

Artifact maintenance gate:

- AGENTS.md: pending Phase-2 start.
- Runtime project skills: no workflow exists to capture yet.
- Specs: pending measured Phase-1 reality.
- End-user/operator docs: no shipped feature exists yet.
- End-user/operator skills: no shipped workflow exists yet.
- SOW lifecycle: correctly `open` under `pending/` and blocked by SOW-0016.

Specs update:

- Pending Phase-2 design.

Project skills update:

- No update until a reusable workflow is proven.

End-user/operator docs update:

- No feature exists to document yet.

End-user/operator skills update:

- No feature exists to expose yet.

Lessons:

- Establish and measure the storage SDK before designing optional authentication
  layered on its snapshots.

Follow-up mapping:

- Snapshot signing is tracked entirely by this SOW.

## Outcome

Pending.

## Lessons Extracted

Pending implementation.

## Followup

None yet.

## Regression Log

None yet.
