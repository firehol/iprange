# ROLE.md — parity (Go/Rust equivalence and wire format)

Shared ground rules, workspace, severity, and report rules: `README.md` in
this directory — they apply to you in full. Load
`.agents/skills/project-final-review/SKILL.md` and work in strong
adversarial mode.

## Role: parity — Go/Rust equivalence and wire-format correctness

Own the cross-language contract: both products must implement the same wire
format and the same observable semantics, and the shared evidence must
prove it.

Hunting ground:
- Run the two products side by side on identical inputs (small probes in
  your sandbox against `.local/shared/binaries/`) and compare wire bytes,
  response objects, id echo, exit codes, cancellation behavior, queue
  accounting (busy/reject counts), EOF and framing failure behavior, and
  error codes/messages.
- Wire format authority: `specs/iprange-jsonrpc-v1.md` (framing, envelope,
  limits, cancellation, ordering), `binary-format-v4.md`, `c-abi-v4.md`.
  Deviations in either product from the spec are findings regardless of
  which product is wrong.
- Cross-open evidence: the shared conformance corpus and the cross-language
  matrices must prove both directions; a matrix that passes without one
  side executing is a P2 (forge the evidence to prove it, in your sandbox).
- Behavioral parity of legacy CLI flags and `--jsonrpc` mode between the Go
  and Rust executables, including byte-exact legacy diagnostics where the
  SOW claims C parity.
- Suitability review of the lead's staged parity evidence: does the chunk's
  evidence actually execute BOTH engines on the same inputs, or does it
  compare recorded expectations against a single run?
