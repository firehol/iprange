# SOW-0033 - Post-guard acceptance-verifier controls

## Status

Status: open

Opened from SOW-0028 round 23 (2026-09-19). Nothing here is implemented.
The plan requires the astra plan gate (REVIEWS.md § Work planning) before
any implementation; no code may start on the strength of this file alone.

## Requirements

### Purpose

The runner's acceptance verifiers that run **after** the frozen
`expect_error` guard (run.py:673-716) are outside both the golden
snapshot and the identity arms. Two of them now carry direct pins
(`check_expected_error` — 11 pins, SOW-0028 round 16;
`check_expected_params_rejected` (4 arms) + `check_request_is_contract_invalid`
(2 arms) — 6 direct pins, round 23). The remaining gap is `matches_expected`
(run.py:648-670), the verifier that decides whether an engine result
matches a case's expected value tree (`expect_result` and error
`details` values route through it).

### Why it matters

A security wave-12 probe (SOW-0028 round-23 record) mutated
`matches_expected`'s nested-dict branch from exact member-set matching to
`all(key in got ...)` and the runner self-test stayed green, so the
single committed `details`-bearing step would silently accept a wrong
counter value; a list-branch relaxation similarly accepted duplicate
items against a distinct expected list. Today both are latent (no
committed case carries a wrong expectation such a relaxation would
pass), so this is preventive: the moment a future case pins a nested
value, an unnoticed relaxation would launder it.

### Scope (candidate, to be refined at the plan gate)

- Direct, subprocess-free self-test pins on `matches_expected`: a
  matching nested tree must pass; a changed leaf value must fail; an
  extra member in a non-partial expected dict must fail; a missing
  member must fail; list length/order/value mismatches must fail; the
  explicit `{"$ignore": True}` exemption must pass on any value and only
  there. One-sidedness matters: pins must fail a verifier that
  over-rejects as well as one that under-rejects.
- Mutation proof: each relaxation mutation inside `matches_expected`
  (dict branch, list branch, `$ignore` short-circuit, partial-dict rule)
  must make the self-test fail naming a pin, with the expected
  diagnostic AND the expected failing frame (the round-20 scorer
  discipline; timeout and unrelated failures score FAIL, never pass).

### Out of scope (unless a concrete scenario appears)

- `substitute`, `record_ledger`, `record_digest`, `inventory`, capture
  paths: no executed exhibit yet shows a relaxation there flipping a
  committed verdict; per REVIEWS.md a finding needs a concrete scenario.
- Any change to the engines, the wire format, the frozen guard region,
  or the golden snapshot.

## Validation plan

Targeted only: the runner self-test (pristine + mutant farms, symlink
farms per Kit hygiene), plus the existing corpus filters
(`negative`, `live.lifecycle`) to prove no scoring drift. Guard-region
hash `abacfa593c3d...` must stay unchanged; if it must move, that is a
deliberate re-stamp with a review record, not part of this SOW.

## Followup

No other pending work depends on this SOW. If the milestone-5 battery
rotation (parity P3-2) already witnesses the `details` arms end-to-end,
this SOW still keeps the direct pins: end-to-end witness and verifier-
own pin are different guarantees (round-16 precedent).
