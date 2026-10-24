# SOW-0038 - Legacy text parser as a reusable Rust library crate

## Status

Status: completed

Sub-state: implementation, documentation, independent review and required validation passed. The user authorized committing and closing this work on 2026-10-24. Parent M1 continues under update-ipsets SOW-0128.

Implementer instructions: load `.agents/skills/project-v4-rust/SKILL.md` before editing; build only into the existing `v4/rust/target/`; always use `nice`; commit only the reviewed scope as authorized on 2026-10-24; append every step to the Execution Log; stop and report to the lead if any step would change CLI output or needs a design choice not recorded here.

## Requirements

### Purpose

The update-ipsets v3 ingester (a separate Rust program in the
`update-ipsets` repository, tracked there as SOW-0128) must parse feed text
exactly as C `iprange` reads valid input. A Rust port of that grammar, parity
tested against the C binary, already exists inside the `iprange` CLI binary.
This SOW makes it a library both programs use, so there is one parser.

### User Request

User decisions (2026-10-15 to 2026-10-17):

- 16 A: extract the existing Rust parser of `iprange-cli` into a library
  crate in this Rust workspace, used by both the `iprange` CLI and the
  ingester.
- 18 A: the DNS resolver moves into the library too; its direct stderr
  printing is replaced by a caller-supplied diagnostics sink.
- For this work the `REVIEWS.md` reviewer rounds are not run (user decision
  for the v3 work: "take over sdk and make it right"); tests still run, with
  `nice`.
- The CLI's behavior does not change: same output, same stderr bytes, same
  exit codes, including the 2.x rule that one bad line or one failed
  hostname fails the load. The ingester applies its own policy (skip, count,
  log) on top of the same classifier.

### Assistant Understanding

Facts (`firehol/iprange @ 57d78314`):

- `v4/rust/iprange-cli` builds only the `iprange` binary; `main.rs:11-13`
  declares private `mod io; mod legacy; mod rpc`. There is no library target.
- Library material, already free of CLI dependencies except where noted:
  - line grammar: `src/legacy/parse.rs` `LineOutcome` and `classify_v4`,
    `classify_v6`, `hostname_v4`, the token/hostname helpers
    (about lines 880-1180) and `Records` (fgets splitter, `MAX_LINE` 1024,
    lines 1183-1215);
  - token parsing: `src/legacy/ipv4.rs` (inet_aton forms, CIDR, netmask,
    `convert_foreign` for `::ffff:` lines), `src/legacy/ipv6.rs`;
  - `src/legacy/range.rs` (`IpNum`, `Range`, `IpSet`);
  - `src/legacy/family.rs` (`Family`, `FamilyImpl`); its only CLI coupling is
    `default_prefix(&Options)` at line 59;
  - `src/legacy/dns.rs` (resolver pool, 1,264 lines); no `Options` use; its
    CLI coupling is 7 direct `crate::legacy::argv::eprint_line` calls
    (lines 149, 329, 341, 429, 666, 787, 860) and the `Batch` drop printing.
- CLI policy that stays in the CLI: `parse.rs` `load_all`, `expand_at`,
  `load_one`, `process_record`, `add_entry` (C `lines` accounting and the
  `-v` NON-OPTIMIZED line), contextual diagnostic builders (classifier warning builders move with grammar), binary
  detection and loading (`binary.rs`), `diag.rs`, `options.rs`, `argv.rs`.
- `check-architecture.sh:263-265` forbids `iprange_livedb` imports inside
  `iprange-cli/src/legacy`; the new crate must not depend on
  `iprange-livedb` either.
- `src/legacy/mod.rs:9-13` states the Go port mirrors the names of the
  legacy public surface (`run`, `Mode`, `Options`, `PrintMode`,
  `SourceKind`, `SourceSpec`, `IpNum`, `dns::{Resolver, DnsError}`).

Inferences:

- Moving code between crates does not change CLI behavior if the CLI calls
  the moved functions unchanged; the parity suites are the proof.

Resolved investigation:

- Go parity manifests do not pin the moved legacy source paths. No Go
  manifest change or Go test rerun is required for this extraction.

### Acceptance Criteria

- New crate `v4/rust/iprange-text` (library only) in the workspace; it
  depends on no other workspace crate and on no new third-party crate
  (`libc` already used).
- `iprange-cli` uses the library for the grammar, token parsing, ranges,
  family hooks and DNS; the moved code is deleted from `iprange-cli`, not
  duplicated.
- All of these pass, unchanged:
  - `nice cargo test --manifest-path v4/rust/Cargo.toml`;
  - `nice ./v4/rust/check-architecture.sh` and
    `nice ./v4/rust/check-source-graph.sh`;
  - the C `tests.d` pass through the Rust binary (`v4/cli/battery.sh`
    `--with-testsd-c`, see `v4/cli/README.md`);
  - `nice go -C v4/go test ./...` (only if a manifest path had to change).
- The library's public API (below) is documented in its `lib.rs` and has
  unit tests moved from the CLI (classifier, records, token parsing, DNS
  bookkeeping that does not need the binary).

## Analysis

Sources checked: listed under Facts.

Current state: parser is private to the binary; the ingester cannot use it.

Risks:

- Byte-exact stderr: DNS messages, their order relative to other
  diagnostics, and the `silent`/`progress`/`debug` gating must be identical.
  `tests/legacy_dns_bookkeeping.rs` and the battery cover this.
- Unit tests in `parse.rs` (lines 1377-2733) mix library and CLI concerns;
  only the classifier/records/token tests move.
- Disk: the workstation root filesystem is about 99% full. Build into the
  existing `v4/rust/target/`; do not create other target directories.

Migration update (2026-10-19): the workstation disk constraint is historical.
Normal builds still use the existing workspace target directory. The required
source-graph gate retains its existing isolated scratch targets, with scratch
redirected into a validated task-private directory outside the Rust workspace.
The initial target-root scratch choice exposed generated build outputs to the
source inventory; the gate requires its normal external scratch arrangement.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- The parser is reusable code locked in a binary crate; the ingester needs
  it with a different error policy.

Evidence reviewed:

- Files and lines under Facts.

Affected contracts and surfaces:

- `iprange-cli` internals (no user-visible change); workspace
  `v4/rust/Cargo.toml` members; new crate `v4/rust/iprange-text`.

Existing patterns to reuse:

- Workspace package fields (`edition.workspace` and so on) as in
  `iprange-cli/Cargo.toml`; exact `=` pins for any shared dependency.

Risk and blast radius:

- Parser extraction affects Rust CLI internals. The approved gate repair
  also consolidates SDK creator-only permission proof without changing policy
  or public APIs. The C tool and Go port are untouched.

Sensitive data handling plan:

- No secrets, hosts or personal data involved. Commits and comments must not
  mention deployment details or AI tools.

Implementation plan:

1. Check Go parity manifests for Rust legacy paths (read only).
2. Create `v4/rust/iprange-text` with `range`, `family`, `ipv4`, `ipv6`,
   `grammar` (classifier + `Records`), `dns` modules moved by `git mv`-style
   moves where possible so history follows. Public API:
   - `IpNum`, `Range<T>`, `IpSet<T>` (as today);
   - `Family`, `FamilyImpl` with `default_prefix` taking the prefix value,
     not `Options` (the CLI passes `options.default_prefix`);
   - `LineOutcome` and `classify_v4(rec, lineid)`, `classify_v6(rec)`,
     `colons(rec)`, `cstr`-equivalent NUL truncation helper, `Records`,
     `MAX_LINE`;
   - `parse_token::<F>(token, default_prefix, fix_network)`;
   - `dns::{Resolver, DnsError, ReplyRecord, Batch}` plus a
     `DiagnosticSink` trait (`fn line(&self, text: &str)`), passed to
     `Resolver::new`; every former `eprint_line` call goes through it with
     the same text and at the same moment.
3. Switch `iprange-cli` to the library; its sink calls `argv::eprint_line`.
   Delete the moved code from the CLI.
4. Move the matching unit tests; keep CLI-policy tests in the CLI.
5. Run the acceptance commands.

Validation plan:

- Acceptance commands above; a grep that no `fn classify_v4`, `inet_aton`
  or resolver pool remains in `iprange-cli/src`.

Artifact impact plan:

- AGENTS.md: add the crate to any crate list it keeps.
- Runtime project skills: none expected; check at close.
- Specs: note the crate split in the Rust architecture spec that lists the
  workspace crates, if one exists.
- End-user/operator docs: none (no behavior change).
- End-user/operator skills: none.
- SOW lifecycle: single SOW; close with code in one commit when the user
  asks for a commit.

Open-source reference evidence:

- Not applicable: the work moves first-party code.

Open decisions:

- Warning ownership resolved by user option A on 2026-10-21: the three private
  warning formatters move with classifiers, preserving exact warning text.
- The crate name `iprange-text` is a working name; the user may rename it
  before the commit.

## Implications And Decisions

- 16 A and 18 A as stated under User Request.
- `REVIEWS.md` rounds are not run for this SOW (user decision for the v3
  work).
- 2026-10-19 environment decision, option A (surgical): the user approved
  fetching the workspace's locked Cargo dependencies and adding
  `x86_64-pc-windows-gnu`, `aarch64-apple-darwin`, and
  `x86_64-unknown-freebsd` to the already installed stable toolchain. No
  compiler upgrade, production service change, or broader package installation
  is authorized. Complete the existing acceptance checks after extraction.

### Approved SDK ownership repair (2026-10-21)

The user approved scope option A (long-term-best) before implementation:
- Move the identical creator-only permission proof in `live_writer.rs` and
  `reader_core/live.rs` into `live_namespace::require_creator_only`.
- Preserve `database_file::open_read_only`, commitment checking, error mapping,
  and the conditional and timing before acquiring the sidecar gate lock.
- Replace the two blocks with calls and remove the obsolete error adapter.
- Extend existing permission tests to cover protected writer reopen, rejection
  after broadening permissions, recovery after restoring them, and unprotected
  writer reopen. No permission policy or public API changes.
- Rerun focused permission tests, architecture gate, full workspace tests and
  four-target source graph. The existing C corpus evidence remains applicable
  because no parser or CLI behavior is changed by this repair.

The first repair gate exposed two more inherited findings in the same permission
ownership class: sidecar retained-descriptor proof and an inline test's direct
security-lock import. The earlier log summary omitted single-file grep findings.
Minimal-complete handling keeps descriptor-based proof in the namespace owner
(shared with the path helper) and moves the existing policy test module into a
separate test file, following the existing sidecar test layout. No gate rule,
permission semantics, descriptor identity or lock timing changes.

## Plan

See Implementation plan.

## Execution Log

### 2026-10-17

- SOW created by the lead. No code.

### 2026-10-19

- User approved validation setup option A and resuming the approved parser
  extraction. Repository status was checked before editing; unrelated untracked
  files remain untouched. Read-only analysis covers module boundaries, grammar
  test ownership, and acceptance commands; implementation edits are sequential.
- Read-only analysis found the warning-ownership contradiction above, plus two
  classifier helpers still needed by CLI policy (`classify_token`, `skip_ws`).
  The latter stay authoritative in the library and are shared with the CLI.
  Go parity manifests do not pin the moved legacy source paths; no Go manifest
  change is expected. Raw record splitting must precede per-record NUL
  truncation, preserving current `fgets` semantics.
- Approved environment setup succeeded: `cargo fetch --locked` downloaded the
  seven missing registry packages; offline full-workspace metadata succeeds.
  Added Windows GNU, Apple ARM and FreeBSD standard libraries to existing
  stable Rust 1.97.1. Compiler selection and locked dependency versions are
  unchanged. No production service was changed.
- Baseline `cargo check --locked --offline --workspace --all-targets` passed
  on stable 1.97.1 before parser edits, including the C adapter build. The
  warning-ownership answer remains pending; parser source is untouched.

### 2026-10-21

- User approved warning ownership option A (long-term-best). Move
  `fmt_ignore_text`, `fmt_incomplete_v4`, and `fmt_incomplete_v6` with
  classifiers; preserve the public warning result and CLI output unchanged.

- Extraction implemented sequentially. Numeric modules moved intact apart from
  dependency paths; grammar and matching unit tests moved out of CLI policy.
  DNS uses an owned thread-safe sink, retaining emission sites and batch-drop
  timing. CLI delegates to its existing stderr writer. Architecture gate now
  covers the library's no-SDK boundary. Only local workspace lock entries changed.
- Full native workspace check passed after extraction. The new library's 57
  unit tests passed, including added NUL-boundary, family-prefix and diagnostic
  ordering/gating coverage. Full acceptance validation is running.

- Independent read-only review found no semantic move regression. Documented
  caller prefix bounds and corrected inherited DNS comments about batching;
  re-review closed both findings.
- All 121 legacy `tests.d` groups passed using a fixture-only scratch copy
  and the fresh Rust binary. Embedded comparisons use the installed C oracle.
  This is the C-corpus track, not the entire multi-language CLI battery.
- Initial workspace run failed two AF_UNIX fixture binds because the private
  scratch path was too long; both pass with a shorter private TMPDIR. The
  full run continues with that corrected test setup.
- All four source-graph compile targets passed. The initial inventory step
  rejected generated files from scratch placed inside the workspace; the
  unchanged gate is rerunning with its normal external scratch layout.
- Required architecture gate fails on pre-existing `live_writer.rs:126`,
  confirmed in HEAD. The matching rule is unchanged; the new parser boundary
  scan passes. Proposed scope addition: move duplicated creator-only checks
  from writer and reader into existing `live_namespace` owner, preserving
  read-only open, error translation and timing; add writer permission tests.
  Awaiting user scope approval; no SDK source was changed.

- Corrected full workspace run passed: 1,091 tests passed, eight existing
  ignored tests, no failures; 2m52s elapsed. Corrected source-graph gate passed:
  528 sources, four supported targets, one runtime-compiled fixture; 1m49s.
  Cross-target results prove compilation, not native execution on foreign OSes.
- Added the two demonstrated scratch constraints to the existing project Rust
  skill. README, architecture spec, parent brief and parent M1 SOW now describe
  the shared grammar. CLI/operator interfaces and v4 wire format are unchanged.

### Approved gate repair execution (2026-10-21)

- User approved repair option A. Writer and reader now call the namespace
  path helper, which delegates to a shared retained-file permission proof.
  Sidecar transition calls the retained-file helper directly, never reopening
  the path. Error classes and permission rules remain unchanged.
- Moved the existing sidecar source-policy test module without changing its
  cfg, module identity, cases or shared lock. Corrected the permission test's
  sidecar cleanup path while extending writer success/refusal/recovery checks.
- Focused `creator_only_flag` tests pass (4/4); architecture gate now passes
  without changing any existing pattern. Independent read-only review of the
  final repair found no findings, confirming retained-descriptor, error,
  policy, timing and test-cleanup preservation. Full workspace and four-target
  gate reruns are in progress against the final repair.

- Final repair validation passed: full workspace 1,091 tests passed / eight
  existing ignored, no failures (3m11s); source graph 529 sources across four
  targets, including the relocated policy test (1m48s). The architecture gate
  passes with unchanged patterns. The unchanged parser retains the earlier
  121/121 C corpus and warning-denied documentation evidence. New test-file
  formatting and final whitespace checks pass.

## Validation

- PASS: `cargo check --offline --workspace --all-targets`.
- PASS: `cargo test --locked --offline -p iprange-text` (57 tests).
- PASS: `cargo test --locked --offline --workspace` (1,091 passed, 8 ignored).
- PASS: unchanged `run-tests.sh` with all 121 `tests.d` groups, fresh Rust
  binary and C reference, fixture-only sandbox.
- PASS: unchanged `check-source-graph.sh` on final repair (529 sources, all four targets).
- PASS: warning-denied `cargo doc -p iprange-text --no-deps`, new-crate
  rustfmt check, `git diff --check`, and authoritative-implementation scan.
- PASS: `check-architecture.sh` after the approved permission ownership repair;
  existing gate patterns remain unchanged.
- Independent read-only review found no behavior regression; both documentation
  findings were corrected and rechecked. Independent final SDK repair review
  found no findings. Required SOW validation passes; broader M1 is incomplete.

## Outcome

The parser extraction and approved SDK ownership repair are implemented,
reviewed and validated. All required SOW checks pass. The user authorized the implementation and SOW closure checkpoint together on
2026-10-24. No deployment is authorized. The parent
M1 ingester remains incomplete; no ingester skeleton or storage code was added.

## Lessons Extracted

Source-graph scratch must be outside the Rust workspace; native test scratch
must be short enough for AF_UNIX fixtures. Both are recorded in the project
Rust skill. Public extraction also needs explicit formerly internal caller
preconditions, even when algorithms are moved unchanged.

## Followup

User scope question 1 resolved as A and implemented. No unresolved extraction
or permission-repair finding remains. User authorized commit/closure on 2026-10-24. Parent M1 step 2 is the next implementation step;
its decisions 19 and 20 remain tracked in SOW-0128.

## Regression Log

None yet.

### Closure authorization — 2026-10-24

The user accepted the recommendation to checkpoint this validated work and
continue with SDK gaps and the ingester skeleton. Only explicit reviewed files
are included; unrelated untracked files are preserved.
