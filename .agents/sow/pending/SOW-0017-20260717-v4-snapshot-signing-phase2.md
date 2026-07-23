# SOW-0017 - v4 Snapshot Signing Phase 2

## Status

Status: open

Sub-state: intentionally blocked until SOW-0016 delivers a reliable, measured
unsigned SDK. No signing design or implementation work is authorized before that
evidence exists.

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

- SOW-0016 owns the unsigned core SDK and exact experimental Phase-1 v4 bytes.
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

- SOW-0016 has completed with cross-language conformance, resource bounds,
  durability evidence, real-use SDK evidence, and representative benchmarks.
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

- No final v4 SDK, signing API, or signing implementation exists.
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

Status: blocked; SOW-0016 reliability and performance evidence does not yet exist

Problem / root-cause model:

- The signing feature was being specified before the SDK that would host it
  existed. That inverted dependency stalled the core engine and made signing
  performance and API conclusions speculative.

Evidence reviewed:

- Binding decision 45 in SOW-0016.
- The prior signature wire/API draft and final-authority audit findings recorded
  in SOW-0016.
- Official Go and Rust Ed25519ph interfaces and the open-source reference below.

Affected contracts and surfaces:

- Final v4 meta/page layout, immutable snapshots, validation, publication,
  verification, Go/Rust/C APIs, update-ipsets integration, conformance, security
  documentation, and performance budgets.

Existing patterns to reuse:

- SOW-0016's proven unsigned snapshot, explicit validation, immutable-open,
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

1. Reopen this gate only after SOW-0016 completion evidence is available.
2. Benchmark representative unsigned snapshots and model signing/verification I/O.
3. Present unresolved wire, provider, trust, and rollout decisions to the user.
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
- SOW lifecycle: remain pending/open until SOW-0016 completes; never execute both
  SOWs as one batch.

Open-source reference evidence:

- `sigstore/cosign @ 089731c31f75`
  - `cmd/cosign/cli/signcommon/common.go:59-77`
  - `internal/key/svkeypair.go:37-57`
  - `internal/key/svkeypair.go:103-143`

Open decisions:

- Intentionally unresolved until the SOW-0016 evidence gate is satisfied: exact
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
