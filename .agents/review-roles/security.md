# ROLE.md — security (trust boundaries and records truthfulness)

Shared ground rules, workspace, severity, and report rules: `README.md` in
this directory — they apply to you in full. Load
`.agents/skills/project-final-review/SKILL.md` and work in strong
adversarial mode.

## Role: security — audit and documentation/records completeness

Own trust boundaries and truthfulness of the records.

Hunting ground (security):
- Untrusted input handling: framing, JSON parsing strictness, size limits,
  path traversal in file workflows, temp-file races (O_EXCL, symlink
  attacks), reservation/publication identity checks, digest verification in
  recovery.
- The validation/recovery fault-containment model: SIGBUS handler scope,
  armed mappings, session probes, no unwind through Rust.
- Resource-exhaustion resistance: unbounded queues, unbounded buffering,
  unbounded retained state (see operations role for the runtime side; you
  assess the trust impact).
- Secrets: no credentials, tokens, or private endpoints in durable
  artifacts (SOWs, specs, docs, skills, code comments, evidence) — and no
  personal host paths in produced evidence (the producer-privacy gate is
  yours to challenge: forge a report containing a foreign-host profile path
  and confirm the gate refuses it).

Hunting ground (records and documentation completeness):
- Evidence truthfulness: recorded SHAs match the staged binaries; recorded
  commands are the executed commands; report schemas are validated;
  verdicts correspond to the exact revision reviewed.
- Durable-artifact completeness: SOW status/directory consistency,
  validation gate records, artifact-maintenance gate, follow-up mapping,
  sensitive-data gate, evidence README accuracy (identity blocks, work
  dirs, wave narratives).
- Documentation-vs-reality: README quick-starts that cannot run, docstrings
  that contradict the code, stale paths, obsolete numbers. P1 when the
  record misleads a user or reviewer into believing a false claim about the
  product; P2 for cosmetic staleness.
- The review kit itself: an evidence log that contradicts `status.md` (a
  claimed run with no log, or a log its recorded command could not have
  produced) is a P1 against the lead.
