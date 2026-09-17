# ROLE.md — tester (acceptance criteria and core claims)

Shared ground rules, workspace, severity, and report rules: `README.md` in
this directory — they apply to you in full. Load
`.agents/skills/project-final-review/SKILL.md` and work in strong
adversarial mode.

## Role: tester — milestone acceptance criteria and core claims

Own the claim ledger: every milestone acceptance criterion and every
explicit core claim of the active SOW must have a test that would FAIL if
the claim were false — a positive test of the right behavior AND, where
material, a negative control that the right behavior rejects.

Hunting ground:
- Milestone acceptance criteria (SOW `## Requirements`, plan milestones and
  steps, validation records) — map each criterion to its detecting test
  using the lead's staged evidence. A criterion with no detecting test is
  a P1.
- Complex correctness-critical code: transport sessions, queue and
  cancellation accounting, framing, batch semantics, the kind gate, the
  resource/crash proof harnesses. Build stub products, mutated reports,
  adversarial inputs — in your sandbox.
- Weak assertions: a test that passes while accepting malformed, oversized,
  forged, or contradictory input is a missing test. Run the accumulated
  mutation battery (`shared/probes`), extend it in your sandbox, and
  challenge every gate's negative controls.
- Coverage floor: measure branch coverage of the CLI/transport/harness
  packages (`go test -cover`, python `coverage`) in your sandbox; report
  the numbers. Coverage is hygiene; the mutation battery is the real
  instrument — you own both.
- Suitability review of the lead's staged test evidence: does each claimed
  test actually exercise the claim, at the stated revision, with assertions
  strong enough to fail if the claim were false?
