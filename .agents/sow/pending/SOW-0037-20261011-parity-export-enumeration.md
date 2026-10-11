# SOW-0037 - Parity export enumeration from an authoritative source

## Status

Status: open

Sub-state: not started; recorded at the SOW-0035 close as the retiring
home for the two accepted round-15 residuals.

## Requirements

### Purpose

The Go-side parity export inventory gate must enumerate the Rust
crate's public surface from an authoritative source instead of a
hand-written lexical scanner, so that public-surface drift is caught
by construction rather than by one review round per lexical class.

### User Request

The user closed SOW-0035 over the two round-15 findings
(`pub async fn` visibility and `#[macro_export]` macros bypassing the
scan) as accepted residuals in a TEST-SIDE scanner, and directed that
the whole class be retired properly by a follow-up SOW that replaces
lexing with authoritative enumeration rather than iterating lexical
classes by review (user decision B, 2026-10-11). The standing
constraint applies: no new testing-framework machinery — the smallest
authoritative mechanism that closes the class.

### Assistant Understanding

Facts:

- Rounds 9-15 of SOW-0035's sol gate each hardened one lexical class
  of `v4/go/parity_gate_test.go`'s hand-written Rust scanner (line
  comments, whitespace, block comments, lexical context, character
  and raw-string literals, public items, combined chunks).
- The two remaining escapes: `pub async fn` (the async keyword is not
  in the recognized marker list) and `#[macro_export] macro_rules!`
  (visibility-bearing attribute stripped before inventory).
- The engine surface the gate protects is additionally pinned by the
  Rust-side suites and the frozen `parity_rust_public.tsv` inventory;
  the residual risk is confined to unrecorded NEW public names whose
  declarations use the two escaping forms.
- rustdoc JSON output (`cargo rustdoc -- -Z unstable-options
  --output-format json`) enumerates the public surface authoritatively
  but currently requires a nightly toolchain; a generation-time or
  CI-time approach must weigh that.

Inferences:

- Any lexical scanner will remain one class short; an authoritative
  enumeration (generated inventory checked into the repo, or a build
  step that regenerates and diffs it) retires the class permanently.
- The SOW should be small: swap the enumeration source, keep the
  existing ledger/manifest consumption, delete the hand-lexer.

## Pre-Implementation Gate

### Problem / root-cause model

The parity gate re-implements Rust lexing in Go test code. Rust's
lexical grammar has more classes than any review loop will enumerate,
so the scanner's fail-closed contract is unfinishable by iteration.

### Evidence reviewed

- v4/go/parity_gate_test.go (the walker, its detector, and the rounds
  9-15 fix history in the SOW-0035 Execution Log).
- The two round-15 findings and their static counterexamples.
- The rustdoc JSON output-format option and its nightly requirement.

### Affected contracts and surfaces

- v4/go/parity_gate_test.go (TestParityRustSourceExportsAreInventoried
  and the walker), v4/go/parity_rust_public.tsv,
  v4/go/parity_manifest.tsv; no engine surface.

### Existing patterns to reuse

- The frozen-inventory consumption (loadRustInventory) and the
  ledger/manifest machinery stay; only the enumeration of lib.rs
  source text is replaced.

### Risk and blast radius

Test tooling only; a bad enumeration fails the gate loudly (fail
closed), never the product.

### Sensitive data handling plan

No secrets involved; the artifacts are inventories of public names.

### Implementation plan

1. Enumerate options (generated inventory at generation time; a
   checked-in generated snapshot diffed in CI; nightly rustdoc in an
   opt-in track) with their toolchain constraints.
2. Implement the chosen mechanism; delete the hand-lexer.
3. Port the detector obligations to the new mechanism (the
   counterexamples from rounds 9-15 become its fixtures).

### Validation plan

The round 9-15 counterexample shapes (as fixtures) must all be caught;
the real inventory must match the current tsv exactly.

### Artifact impact plan

Specs unchanged; AGENTS.md unchanged unless the invocation changes;
this SOW plus the SOW-0035 residual records.

### Open decisions

The enumeration mechanism (generated snapshot vs CI generation vs
nightly track) is a user decision at plan time.

## Plan

To be drafted at pickup, per the implementation plan above.

## Execution Log

- Recorded at the SOW-0035 close (2026-10-11): created as the retiring
  home for the two round-15 accepted residuals.

## Validation

Pending.

## Outcome

Pending.
