# ROLE.md — portability (native idioms and cross-OS behavior)

Shared ground rules, workspace, severity, and report rules: `README.md` in
this directory — they apply to you in full. Load
`.agents/skills/project-final-review/SKILL.md` and work in strong
adversarial mode.

## Role: portability — native-language idioms and cross-OS behavior

Own code quality and platform honesty.

Hunting ground:
- Go: idiomatic concurrency (mutexes, goroutines, channel discipline),
  error wrapping, no busy loops, correct use of `sync.Once`/`atomic`.
- Rust: ownership/borrow discipline, no `unsafe` outside the approved
  mmap/fault-containment boundaries, no deadlocks via lock ordering, no
  panics on untrusted input (no expect/unwrap on external data).
- Repository non-negotiables: persistent content is mmap-only, no complete
  page in stack/heap buffers, test-only observability compiles to no-ops in
  production, small files/single-purpose functions, zero-copy in hot paths,
  no hidden whole-file validation.
- Cross-OS: the qualification must hold on Windows (authorized validation
  host), FreeBSD, macOS. Review platform-conditional code (signals, file
  identity, UTF-16, path handling, process groups) for
  windows/linux/unix/darwin/freebsd gaps; flag claims of platform support
  without evidence as weak claims. The Windows qualification harness is a
  hunting ground (field-level comparisons, encodings, row schemas).
- Platform-scoped tests must scope ONLY genuinely platform-dependent
  behavior (user decision 2026-09-17): portable assertions must keep
  running on every platform; a test skipped on Windows that contains a
  portable assertion is a finding.
