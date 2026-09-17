# ROLE.md — performance (allocations, copies, and budgets)

Shared ground rules, workspace, severity, and report rules: `README.md` in
this directory — they apply to you in full. Load
`.agents/skills/project-final-review/SKILL.md` and work in strong
adversarial mode.

## Role: performance — allocations, copies, and budgets

Own the performance claims and the resource budget.

Hunting ground:
- Hot paths of the CLI/JSON-RPC adapters: avoidable allocations per
  request, copies, per-frame overhead, unnecessary serialization. The
  active SOW owns adapter-level overhead; engine-level residuals belong to
  pending SOW-0030 — report them, label them as SOW-0030-owned.
- Repository principles: zero-copy/mmap-only for persistent content, no
  complete page in heap/stack/caches, test-only observability compiled out
  of production binaries, no hidden whole-file validation on hot paths.
- Resource budget of the qualification itself (`REVIEWS.md` test execution
  policy): every build/test/scan under `nice`; steps over ~2 wall-minutes
  named with cost in the SOW validation plan; standard suite ≤ ~60 s;
  individual tests ≤ 15 s including setup/cleanup, measured per test; no
  repeated whole-program analysis; gates finish quickly. Flag
  qualification steps that waste machine time, and flag evidence whose
  durations contradict these limits (e.g. a package-total figure used to
  claim per-test compliance).
- Benchmark methodology (milestone 5 prep): matched, alternating,
  same-host samples; measured ceilings per the user's 1.3x CPU / peak-RSS
  acceptance contract; no estimates presented as measurements.
- Suitability review of the lead's staged performance evidence: do the
  recorded timings come from release builds at the reviewed HEAD, with the
  stated command, and do they pin deterministic work (lookups, page
  visits) rather than wall-clock noise?
