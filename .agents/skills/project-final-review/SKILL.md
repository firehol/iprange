---
name: project-final-review
description: Perform independent adversarial final reviews of milestones, releases, migrations, refactors, fixes, close-outs, and other work claimed ready, complete, production-grade, accepted, or safe to proceed. Use for final review, full-scope review, acceptance review, readiness review, release review, milestone close, re-review after fixes, or any PASS/FAIL gate. Do not use this as a substitute for implementation review during ordinary development.
---

# Project Final Review

Final review is an attempt to disprove readiness, not a confirmation that the
named fixes and mechanical gates pass. Treat prior verdicts, close-out records,
and statements that findings were fixed as untrusted claims until independently
verified at the exact reviewed revision.

## Preserve Independence

- Review read-only unless the user separately requests fixes.
- Record the exact revision and initial working-tree state.
- Do not let the user's checklist, prior findings, or repair narrative define
  the complete search space. Verify them, but also perform an open-world audit.
- Do not inherit a previous PASS. A re-review starts from zero trust.
- Separate facts, unresolved deviations, and working theories.
- Keep the final verdict with the primary reviewer. Optional parallel reviews
  supplement this workflow; they do not replace it.

## 1. Establish Authority

Read authority in the project's declared order before judging implementation.
At minimum inspect:

1. current user decisions and the active work record;
2. normative specifications and accepted designs;
3. approved plans, parity matrices, schemas, API inventories, or migration maps;
4. reference implementations or cross-language authorities;
5. implementation and tests as evidence of current behavior;
6. repository instructions governing lifecycle, validation, documentation,
   security, compatibility, and completion.

Do not silently resolve disagreements. An implementation/spec/API mismatch is a
finding unless an authorized deviation is recorded by the proper authority.

## 2. Reconstruct Scope

Build an independent inventory of what the work claims to deliver:

- behavior and invariants;
- public APIs, schemas, commands, configuration, and file formats;
- architecture and single-authority boundaries;
- concurrency, lifetime, error, security, and failure semantics;
- compatibility, migration, deletion, and deprecation promises;
- performance and resource guarantees;
- supported and unsupported platforms;
- tests, fixtures, generated artifacts, docs, skills, and lifecycle records;
- explicitly deferred or rejected work.

Inspect both the changed range and enough surrounding implementation to verify
the claims. A range-scoped review must still detect violations of contracts that
the range claims to close.

## 3. Audit Before Running Gates

Perform the static and semantic review before using green automation as
evidence. Cover every applicable lane.

### Public Contract

- Inventory exported/public symbols and compare names, signatures, values,
  ownership, mutability, and error behavior with authority.
- Search for mutable exported global state used as semantic authority.
- Check zero values, copies, aliases, stale handles, repeated close/release,
  concurrency, cancellation, and misuse behavior.
- Treat unexplained public API drift as a finding even when behavior is similar.
- Do not preserve compatibility for an unreleased API unless authority requires
  it.

### Correctness And Failure Semantics

- Trace normal, absent, boundary, malformed, corrupted, stale, and conflicting
  states through the real implementation.
- Check validation and error precedence, not only final error codes.
- Look for state corruption, double release, underflow/overflow, stale caches,
  partial publication, races, unsafe aliasing, and time-of-check/time-of-use
  gaps.
- Verify that comments and types describe what the code actually does.

### Architecture And Authority

- Identify the authoritative implementation for persistent formats, registries,
  constants, codecs, state machines, and public translation.
- Search for duplicate numbering, copied algorithms, bypasses, dead parallel
  implementations, and unwired source.
- Confirm high-level adapters do not acquire forbidden low-level authority.

### Performance And Resources

- Verify allocation, synchronization, copy, syscall, memory, descriptor,
  bounded-work, and hot-path claims with both source inspection and measurement.
- Identify what is measured, what is excluded, and whether the measurement uses
  the public path at the claimed grain.
- Treat successful benchmarks as evidence only for the exact operation measured.

### Security And Operations

- Review trust boundaries, path/namespace handling, permissions, secret and
  sensitive-data exposure, crash behavior, cleanup, recovery, rollback, and
  unsupported environments.
- Distinguish cross-compilation from native runtime proof.
- Verify producers, refresh triggers, repair paths, and read-only serving paths
  for generated runtime artifacts.

## 4. Audit Tests As Claims

- Map each important contract to a regression test or equivalent proof.
- For a claimed repair, confirm the regression fails for the intended reason on
  the actual pre-fix revision when that revision is available.
- Ensure tests exercise the public behavior, not only a private helper.
- Search for missing negative, mutation, concurrency, copy, boundary, and misuse
  cases.
- Reject tests that encode the implementation's mistake as the expected result.
- Search the full affected area for the same failure class.

## 5. Audit Records As Data

Read complete close-out and status records, not only their summaries. Extract
and verify absolute claims containing terms such as:

- `all`, `every`, `only`, `none`, `zero`, `exact`, `never`;
- `pending`, `resolved`, `complete`, `closed`, `supported`, `unsupported`;
- counts, fixtures, allocations, files, commits, reviewers, and platforms;
- deletion, compatibility, scope, deferral, and next-milestone statements.

Check current statements against historical sections in the same document.
Stale facts remain defects even when a newer paragraph is correct. Status,
directory, gate history, implementation state, and next-step claims must agree.

## 6. Run Mechanical Gates

Run the exact project-required tests, race/sanitizer checks, static analysis,
formatting, architecture/import/source-graph gates, cross-compiles, native tests,
cross-language conformance, audits, and reproducible counts that apply.

Report commands and factual outcomes. A green gate cannot overrule a contract,
API, architecture, or record defect that the gate does not test.

## 7. Disprove PASS

Before issuing a verdict, perform a separate final pass with this question:

> What are the strongest remaining reasons this work may not be ready?

At minimum reconsider:

- an omitted authority or approved plan;
- a public symbol or mutable global missed by behavioral tests;
- a false absolute statement in records or source documentation;
- an untested misuse, race, copy, stale-state, or failure-precedence path;
- a deferred item with no real tracking artifact;
- a gate that proves less than the report claims;
- a fix that introduced a new issue outside the original checklist.

Resolve each candidate with evidence. Do not convert uncertainty into PASS.

## 8. Verdict

Use the project's severity model. If none exists, use:

- **P0**: catastrophic safety, security, data-loss, or unusable-result defect;
- **P1**: major correctness, contract, architecture, or release-blocking defect;
- **P2**: material incomplete behavior, unapproved deviation, missing proof, or
  contradictory close-out evidence;
- **P3**: cosmetic or non-blocking clarity issue.

Report every finding with precise file and line evidence, impact, and the
smallest valid correction. State explicitly whether findings block acceptance.

PASS requires all of the following:

- no unresolved P0-P2 finding;
- no unapproved contract or public-interface deviation;
- no contradictory current record or false absolute claim;
- required tests and gates pass at the exact reviewed revision;
- required regression evidence is credible;
- deferred work is implemented, rejected with evidence, or tracked;
- the final status and next-step claim match reality.

If any requirement is not established, do not say PASS.

## Common Review Failures

- **Checklist trap:** verifying only defects named by the requester.
- **Green-gate fallacy:** treating passing automation as complete correctness.
- **Narrative anchoring:** trusting that prior findings were fixed.
- **Authority omission:** reviewing code without accepted plans or public API
  inventories.
- **Summary sampling:** reading corrected summaries while stale contradictions
  remain elsewhere.
- **Scope substitution:** reviewing the diff while ignoring the contract the
  diff claims to complete.
- **Premature stopping:** issuing PASS before the disproof pass.
