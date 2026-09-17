# ROLE.md — operations (what can go wrong that is not handled)

Shared ground rules, workspace, severity, and report rules: `README.md` in
this directory — they apply to you in full. Load
`.agents/skills/project-final-review/SKILL.md` and work in strong
adversarial mode.

## Role: operations — unhandled failure

Hunt the unhandled failure: every external condition the product or
harness can meet and survives — or should survive and does not.

Hunting ground (seeded by real past failures):
- Transport failure composition: failing stdout/stderr, full bounded
  queues, shutdown joins, signals, EOF races, partial lines, oversized
  frames, pipelined floods. Seed probes: `shared/probes/stream_probes.py`,
  `broken_output_trace.py` (broken-stdout deadlock class).
- Process lifecycle: kill windows during publish/commit/finish/export/
  validate, retry behavior, orphan and residue artifacts, force-kill vs
  self-exit distinctions in the harnesses.
- Deadlines and stalls: every stdio exchange bounded? Every harness wait
  bounded? Seed probes: `proof_b_stubs.py`, `rpc_deadline.py`.
- Boundary and resource-exhaustion conditions: queue limits, frame limits,
  response-object limits, memory bounds under input pressure.
- Decide what the CONTRACT requires (spec `iprange-jsonrpc-v1.md`, SOW
  claims), then find the state in which the implementation or the
  qualification does not meet it. For each candidate, run a bounded probe
  in your sandbox; a hang, a wrong exit code, an accepted defect, or an
  unhandled exception is a finding.
- Suitability review of the lead's staged evidence: do the recorded runs
  actually cover the failure compositions this chunk can meet in
  production, or only the happy paths?
