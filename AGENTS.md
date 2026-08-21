# iprange — Agent Instructions

## Goals

`iprange` is FireHOL's high-performance IP range manipulation tool, written in C
(`src/`, autotools + CMake builds; CLI binary `iprange`). It is used by
`update-ipsets` for set operations (union, intersect, exclude, dedup, compare)
over large IPv4/IPv6 lists.

**Direction:** evolve `iprange` into native Rust and pure-Go engines with a
Rust-provided C ABI, one portable architecture-neutral **v4 threat-intel
database**, and ready-to-use SDKs. `update-ipsets` becomes a public
threat-intelligence publisher; Netdata is the first consumer through Rust, Go,
and C SDK surfaces.

Product architecture is defined by
[`.agents/sow/specs/design-iprange-engine.md`](.agents/sow/specs/design-iprange-engine.md).
The exact portable contract is defined by
[`.agents/sow/specs/binary-format-v4.md`](.agents/sow/specs/binary-format-v4.md).
The stable Rust-provided C boundary is defined by
[`.agents/sow/specs/c-abi-v4.md`](.agents/sow/specs/c-abi-v4.md).

Success = correct, fast, dual-stack Rust and Go engines that cross-open one
shared semantic conformance corpus, a stable C ABI over Rust, bounded-memory
live operation, and compact unsigned snapshots that prove SDK reliability and
performance. Authenticated public snapshots are Phase 2, tracked by pending
SOW-0017, and must not delay the core SDK.

### Working principles (non-negotiable)

These apply to every decision, design question, and implementation in this
repository. The user does not want to repeat them.

- **Long-term-best** — always choose the option that is best for the project
  over years, not the fastest to implement. Surgical short-cuts that create
  debt are rejected.
- **Minimal-complete** — the smallest implementation that fully delivers the
  approved outcome and covers its actual blast radius. No more, no less.
- **Absolute performance** — maximize what the v4 format can achieve without
  redesigning it. Hot paths must be optimal; unnecessary work is a defect.
- **Clean code** — separation of concerns, small files, single-purpose
  functions, low complexity, descriptive names. Code must read as if the
  maintainers wrote it.
- **Zero-copy / mmap-only** — persistent content is mmap-only; no complete
  page ever exists in a stack buffer, heap buffer, cache, or anonymous
  mapping.
- **One authoritative implementation** — every persistent operation has one
  low-level owner; adapters compose, never reimplement.
- **Test-only observability** — necessary-work counters, benchmarks, and
  profiles exist in test builds only; they compile to no-ops in production.

- **Gate = full-codebase review** — the mmap-only and file-I/O policies are
  enforced by adversarial reviewers who read the ENTIRE Go codebase, file by
  file: two concurrent reviewers hunt complete-page copies into or out of the
  mmap, two hunt file I/O on persistent content outside the mmap (fresh
  context, lead's model, run at milestone gates). No scanner-run mutation corpora: this is a small SDK, and CI-grade checks must finish in
  well under a minute to at most a couple of minutes.
- **Resource budget** — no wasted compute: every build/test/scan runs under
  `nice`; any step expected to exceed ~2 wall-minutes or ~10 core-minutes must be
  named with its expected cost in the report and recorded in the active SOW
  validation plan before it runs; never test a tiny aspect by repeating a
  whole-program analysis — full-module static analysis runs at most once per
  gate on the real tree (per OS config), and tripwire/sensitivity cases must be
  verified at unit scale or inside that single scan; if a gate design needs
  heavy per-case re-analysis, redesign the gate so the routine check is cheap.

When a design question arises, the answer must satisfy ALL of these
simultaneously. If two options both satisfy them, prefer the one that matches
the Rust reference implementation (the authority for v4 semantics).

### Design authority

During active work, authority is ordered as follows:

1. resolved user decisions in the sole active SOW;
2. the current normative specifications under `.agents/sow/specs/`;
3. implementation and tests as evidence of current behavior.

Completed SOWs are historical records. An old `locked`, `production grade`, or
acceptance claim does not override a later regression finding or current spec.

### v4 engineering philosophy

The v4 format and SDK must be surgical: simple, thin, clear, maintainable, and
fast because of their design. Correctness and durability are required, but
complexity is not evidence of either.

- Every mechanism must map to an approved requirement, a concrete failure if it
  is omitted, and the simplest implementation that prevents that failure.
- Prefer the smallest coherent design over speculative flexibility. Do not keep
  dead code, compatibility for unreleased formats, test-only production
  machinery, or abstractions without a current caller.
- Every persistent operation has one authoritative low-level implementation.
  Healthy-file readers and writers own mapped bytes, pages, roots, allocation,
  retirement, checksums, and committed-generation publication in the main
  file; public workflows and language adapters compose only logical operations
  over those owners. Keep untrusted validation/recovery, external reader
  coordination, and filesystem namespace/publication artifacts behind their
  separate boundaries. Variants may have narrow entry points, but must share
  the same invariant-owning helpers. Do not split code merely to satisfy a
  metric.
- Hardcoded structured values use one common mapped manager for IDs, hashing,
  equality, refcounts, COW lifecycle, validation, recovery, and snapshots. Each
  `StructureKind` keeps its fields, offsets, canonical checks, and typed
  translation in an independent codec module; never copy the manager for a new
  structure.
- Persistent SDK content is mmap-only. Production code must not transfer main,
  sidecar, snapshot, publication, recovery, or scratch bytes through
  read/write/seek APIs, and a complete database page must never exist in a stack
  buffer, heap buffer, cache, or anonymous mapping. Allocate and build pages only
  at their final offsets in file-backed mappings; lifecycle, mapping, locking,
  namespace, truncation, and durability syscalls remain required.
- Preserve the external reader-table, transaction-grouped retirement, explicit
  bounded reclaim, and lowest-free-page allocation contract. Active readers do
  not by themselves force tail allocation. Live mapped parsers must not expose
  ordinary Rust references whose validity assumes an unvalidated pointer cannot
  name a reused page.
- Explicit validation/recovery contains physical mapped-page faults in the
  version-matched SDK worker. Its POSIX `SIGBUS` handler claims only an armed
  SDK-owned mapping address and chains every unrelated signal to the previous
  disposition; it never unwinds through Rust.
- Aim for roughly 5,000 lines of production code per language implementation.
  This is a design target, not a hard limit. A justified 6,000, 7,000, or even
  10,000-line implementation is acceptable; unexplained growth requires
  rethinking the design before adding more code.
- Aim for files below roughly 500 lines. A cohesive 600, 700, or even
  1,000-line file is acceptable when splitting it would reduce clarity.
- Functions should normally have one purpose. Combining tightly coupled work is
  acceptable when it is simpler and clearer. Complexity measurements are review
  signals, not mechanical gates; helper chains and indirection created only to
  satisfy a metric are defects.
- Hot paths must make their costs visible: no hidden whole-file validation,
  temporary sorting files, file-sized heap state, or per-item allocation where
  reusable bounded workspace is sufficient. Test-only necessary-work counters
  should pin deterministic lookups, page visits/copies, range passes, and
  durability work without surviving in release binaries. Performance claims
  require representative release benchmarks and profiles of retained dominant
  costs.
- Build Rust first. Complete it, prove correctness and durability, benchmark
  realistic `update-ipsets` workflows, and demonstrate that it materially
  improves that architecture. Start the pure-Go port only after the user accepts
  the Rust result; then require Go to cross-open and semantically match Rust.

The governing review question is: **is this the simplest clear implementation
of the required behavior?**

## SOW System

This project uses a local Statement of Work system.

The SOW system is self-contained in this repository. Normal SOW work must not depend on `~/.agents`, `~/.AGENTS.md`, global skills, global templates, or global scripts. Use this `AGENTS.md`, project-local SOW files, project-local specs, project-local skills, and the active SOW.

### Roles

- **User responsibilities:** purpose, scope decisions, design forks, risk acceptance, destructive approvals, and final product judgment.
- **Assistant responsibilities:** investigation, evidence, implementation, tests or equivalent validation, reviews, documentation, memory updates, and concise reporting.

### Required First Checks

Before non-trivial work:

1. Read pending/current SOWs for overlap, contradictions, and existing decisions.
2. Read relevant specs under `.agents/sow/specs/`.
3. Inspect `.agents/skills/project-*/SKILL.md` and load every runtime project skill whose trigger matches the work.
4. Inspect code/docs/data as ground truth.
5. Ask the user only for irreducible product/design/risk decisions.

### Git Worktrees

Assistants must not create git worktrees on their own. Create a git worktree only when the user explicitly asks for it or approves it.

### Sensitive Data In Durable Artifacts

SOWs, specs, documentation, project skills, agent instructions, and code comments are commit-ready artifacts. Treat them as public unless a repository-specific policy explicitly says otherwise.

CRITICAL: Never write raw sensitive data to durable artifacts. This includes passwords, API keys, bearer tokens, SNMP communities, private keys, connection strings with embedded credentials, session cookies, community member names, customer names, customer identifiers, personal data, non-private IP addresses that can identify customers, private endpoints, account IDs, and proprietary incident details.

Write only sanitized evidence:

- use placeholders such as `[REDACTED_SECRET]`, `[CUSTOMER]`, `[ACCOUNT]`, `[PRIVATE_ENDPOINT]`;
- use stable aliases such as `customer-a` only when the real mapping is not stored in the repository;
- cite file paths, line numbers, command names, schema fields, or error classes instead of copying sensitive values;
- summarize logs and traces; include only minimal redacted snippets.

If sensitive data is required to continue, stop and ask the user for a secure handling path. If sensitive data is found in a durable artifact, sanitize it before any commit. If sensitive data was already committed, tell the user and do not rewrite history without explicit approval.

> Note (FireHOL specifics): operational details for FireHOL infrastructure
> (server names, paths, API keys, Disqus/MaxMind credentials, deployment steps)
> live in the parent `~/src/firehol/AGENTS.md` and **must never** be copied into
> this repo's durable artifacts or into public PRs/commits.

### Open-Source Reference Evidence

When a SOW uses external open-source repositories as evidence, record the upstream repository identity and checked commit, not the workstation mirror path.

For local mirrored or cloned open-source repositories, cite evidence in this form:

```text
owner/repo @ commit
relative/path/inside/repo:line
```

Rules:

- Never use workstation absolute paths for external open-source evidence in SOWs.
- Resolve `owner/repo` from the repository remote, not only from the local directory name.
- Record the commit with `git -C <repo> rev-parse --short=12 HEAD` or the full hash when precision matters.
- Use paths relative to the upstream repository root after the `owner/repo @ commit` line.
- If multiple repositories were checked, list each repository and commit separately.

### Pre-Implementation Gate

Implementation must not begin until the active SOW contains a concrete `## Pre-Implementation Gate` section. Before moving a SOW from `pending/open` to `current/in-progress`, or before continuing implementation in an existing current SOW that lacks this section, fill the gate.

The gate must record:

- Problem / root-cause model: what is happening, why it is happening, and what evidence supports that model.
- Evidence reviewed: specs, code, docs, tests, logs, traces, prior SOWs, issues, or external references checked. Open-source references from local mirrors or clones must be cited as `owner/repo @ commit` plus repository-relative paths, never as workstation absolute paths.
- Affected contracts and surfaces: APIs, schemas, files, commands, UI, docs, specs, skills, tests, integrations, operators, users.
- Existing patterns to reuse: local modules, helpers, conventions, tests, and docs that should shape the implementation.
- Risk and blast radius: regressions, compatibility, performance, security, data loss, migration, rollout, and operational risks.
- Sensitive data handling plan: whether the work may expose secrets, credentials, bearer tokens, SNMP communities, community/customer data, personal data, non-private customer-identifying IPs, private endpoints, or proprietary incident details; how evidence will be redacted in SOWs, specs, docs, skills, instructions, and code comments.
- Implementation plan: ordered chunks with scope, dependencies, and files or modules likely to change.
- Validation plan: tests, fixtures, manual checks, real-use evidence, review passes, and same-failure searches.
- Artifact impact plan: expected updates to `AGENTS.md`, runtime project skills, specs, end-user/operator docs, end-user/operator skills, and SOW lifecycle.
- Open decisions: resolved decisions or numbered options for the user; unresolved decisions block implementation.

Generic placeholders such as `TBD`, `N/A`, or "to be checked later" are invalid unless the SOW explains why the item truly does not apply. If the gate exposes an unknown that cannot be resolved by investigation, stop and ask the user before implementation.

### When A SOW Is Required

Create or reuse a SOW for non-trivial work:

- feature work;
- bug fixes with behavioral impact;
- refactors;
- migrations;
- documentation or content changes with product/business impact;
- process changes;
- regressions;
- spec hygiene;
- project skill changes;
- any work with unclear risk.

Trivial work does not need a SOW:

- typo fixes;
- formatting-only changes;
- mechanical rename with no behavior change;
- simple search/replace with low risk.

When unsure, treat the work as non-trivial.

### SOW Locations

- Pending: `.agents/sow/pending/`
- Current: `.agents/sow/current/`
- Done: `.agents/sow/done/`
- Specs: `.agents/sow/specs/`
- Template for new SOWs: `.agents/sow/SOW.template.md`
- Local audit: `.agents/sow/audit.sh`

Create new SOW files from `.agents/sow/SOW.template.md`. The template is project-local and may be customized for this repository.

Empty SOW directories must contain `.gitkeep` or `.keep` so the committed repository preserves the full SOW layout after clone/checkout.

Filename:

```text
SOW-NNNN-YYYYMMDD-{slug}.md
```

Status and directory must agree:

- `open` lives in `pending/`
- `in-progress` lives in `current/`
- `paused` lives in `current/`
- `completed` lives in `done/`
- `closed` lives in `done/`

### SOW Completion And Commit

The successful terminal SOW status is `completed`. `done` is a directory name, not a status value. Never write `Status: done` or `Status: complete`.

When a SOW's work is ready to close:

1. Finish implementation, docs, specs, skills, validation, and follow-up mapping.
2. Update the SOW to `Status: completed`.
3. Move the SOW file to `.agents/sow/done/`.
4. Commit the work, artifact updates, SOW status change, and SOW move together as one commit, unless the user explicitly requested a different commit split.

Do not create a separate commit just to mark or move the SOW. Do not claim a SOW is completed while the implementation and the SOW lifecycle change live in separate uncommitted or separately committed states.

### One SOW At A Time

Never execute multiple SOWs as one batch.

If work overlaps:

- merge or consolidate before implementation; or
- split into separate SOWs and complete one before starting the next.

Progress reports are not stop points. Once a SOW is in progress, continue until it is delivered, failed with evidence, blocked on a real user decision/approval, or superseded by newer user instructions.

### User Decisions

When user decisions are needed:

1. Present concrete evidence with files/lines or source references.
2. Provide numbered options.
3. Explain pros, cons, implications, and risks.
4. Recommend one option with reasoning.
5. Record the user's decision in the SOW before implementation.

### Followup Discipline

"Deferred" is not a terminal outcome.

Before a SOW can close, every valid deferred item must be:

- implemented in the current SOW; or
- explicitly rejected as not worth doing, with evidence; or
- represented by a real pending/current SOW file.

Pre-close, search the SOW for:

```text
defer|later|follow-up|future|TODO|pending
```

Map every remaining item to implemented, rejected, or tracked.

### Regressions

A regression is discovered after a SOW was considered completed or closed, later testing or use finds broken behavior, and the original SOW's claimed outcome is no longer true.

When behavior that a completed SOW claimed working stops working:

1. Find the original SOW in `done/`.
2. Move it back to `current/`.
3. Mark it `in-progress` with a regression note in `## Status`.
4. Append a new dated `## Regression - YYYY-MM-DD` section at the end of the file, after the original outcome, lessons, and follow-up content.
5. In that appended section, record what broke, evidence, why previous validation missed it, the repair plan, validation, and updates needed to specs, skills, docs, audits, or follow-up SOWs.
6. Fix and validate there.

Never prepend regression content above the original SOW narrative. The original requirements, analysis, plan, validation, outcome, lessons, and follow-up must remain readable first.
Do not create a new SOW for a true regression.

### Validation Gate

A SOW cannot be completed until Validation records:

- acceptance criteria evidence;
- tests or equivalent validation;
- real-use evidence when a runnable path exists;
- reviewer findings and how they were handled;
- same-failure search results;
- sensitive data gate: durable artifacts contain no raw secrets, credentials, bearer tokens, SNMP communities, community member names, customer names, personal data, non-private customer-identifying IPs, private endpoints, or proprietary incident details;
- artifact maintenance gate for `AGENTS.md`, runtime project skills, specs, end-user/operator docs, end-user/operator skills, and SOW lifecycle;
- SOW status/directory consistency;
- spec update or specific reason no spec update was needed;
- project skill update or specific reason no skill update was needed;
- end-user/operator docs update or evidence-backed reason none were affected;
- end-user/operator skill update or evidence-backed reason none were affected by docs/spec changes;
- lessons extracted or specific reason there were none;
- follow-up mapping.

Generic "N/A" is invalid.

### Artifact Maintenance Gate

Every SOW close must explicitly record whether each durable artifact class was updated or why no update was needed:

- `AGENTS.md` - workflow, responsibility, local framework, project-wide guardrails.
- Runtime project skills - `.agents/skills/project-*/SKILL.md` for HOW to work here.
- Specs - `.agents/sow/specs/` for WHAT the project does.
- End-user/operator docs - README, `wiki/`, published guides, help text, or other human-facing documentation.
- End-user/operator skills - output/reference skills copied or consumed outside normal repo work.
- SOW lifecycle - split, merge, status, directory, deferred work, regression reopening, and follow-up mapping.

This is an assistant responsibility. If a SOW changes behavior, docs, specs, commands, schemas, defaults, workflows, examples, or operating procedure, the assistant must update every affected artifact in the same SOW, or record the evidence-backed reason an artifact is unaffected.

### Specs

Specs are memory of WHAT this project does.

Update specs when shipped work changes:

- product behavior;
- public contracts;
- data formats;
- UX rules;
- business logic;
- operational guarantees;
- known edge cases.

Specs describe current reality, not aspiration. If specs and code disagree, record the discrepancy in the active SOW and resolve or track it.

> Current specs: `design-iprange-engine.md` is a **target-direction** design spec
> (the future engine/format/SDK), not a description of current C behavior. As the
> engine work proceeds, add current-reality specs (existing CLI operations,
> input/output formats — see `wiki/`) incrementally.

### Project Skills

Project skills are memory of HOW to work here.

Runtime input project skills should live under `.agents/skills/project-*/SKILL.md`. The `project-` prefix is the generic hook meaning "agents working in this repo must consider this skill." Before non-trivial work, inspect those skill descriptions and load every matching runtime skill. Skill descriptions are mandatory hooks, not suggestions.

Do not create generic `project-*` skills only to make the framework look complete. If this project intentionally grows project skills incrementally, record that in the active SOW and keep this section honest until concrete reusable knowledge exists.

Output/reference skills may also use `project-*` when that name is part of the exported artifact semantics. Do not rename, shorten, or change their frontmatter descriptions only to satisfy runtime discovery. Instead, list them separately below and exclude them from default runtime guidance unless editing or validating those artifacts.

Non-`project-*` skills under `.agents/skills/` are not automatically runtime instructions. If they are runtime input skills, rename them or add `project-*` wrappers. If the user explicitly defers conversion, preserve them under `Legacy runtime skills` below and track the unresolved alignment with a real SOW. If they are output/reference skills for end users, operators, or downstream assistants, list them separately below with their intended consumer.

Output/reference skills are part of the documentation/specification surface, not just internal agent memory. When docs, specs, schemas, commands, defaults, examples, or public/operator-facing workflows change, update every affected output/reference skill in the same SOW, or record the evidence-backed reason none are affected.

Skills must be updated during retrospection when:

- the user corrects the assistant's workflow;
- a reviewer finds a repeated mistake;
- validation misses a failure mode;
- a new command or workflow becomes canonical;
- a new project hazard is discovered;
- a new best or bad practice is learned;
- an output/reference skill would otherwise become stale after a docs/spec/product change.

### Project Skills Index

Runtime input project skills:

- `.agents/skills/project-final-review/SKILL.md` - load for any final,
  full-scope, acceptance, readiness, release, milestone-close, or post-fix
  re-review. It requires an independent adversarial audit of authority, public
  contracts, implementation, tests, records, and gates before PASS.
- `.agents/skills/project-v4-rust/SKILL.md` - load for changes, reviews,
  benchmarks, portability claims, conformance work, or C-ABI work under
  `v4/rust/` or `v4/conformance/`. It records the frozen Rust-first boundary and
  the proven release-verification workflow.

Legacy runtime skills:

- None.

Output/reference skills:

- None.

### Project-specific commands

Build (autotools):

```bash
./autogen.sh && ./configure && make            # produces ./iprange
```

Build (CMake): `cmake -S . -B build-cmake && cmake --build build-cmake` (see `CMakeLists.txt`).

Test (Rust v4):

```bash
nice ./v4/rust/check-source-graph.sh
nice cargo test --manifest-path v4/rust/Cargo.toml
nice cargo test --manifest-path v4/rust/Cargo.toml --all-features
```

Test (Go v4):

```bash
nice go -C v4/go test ./...
```

Legacy C tests (not v4):

```bash
nice ./run-tests.sh            # canonical test suite (tests.d/)
nice ./run-unit-tests.sh       # unit tests (tests.unit/)
nice ./run-build-tests.sh      # build-matrix tests
nice ./run-sanitizer-tests.sh  # ASan/MSan/TSan/valgrind variants (tests.sanitizers.d/, tests.tsan.d/)
```

> Always run builds, tests, and benchmarks with `nice` (user workstation
> policy): heavy toolchain jobs must never compete with interactive desktop
> work. Scripts in this repo apply `nice` internally; use it explicitly for
> any ad-hoc invocation too.

> Many untracked `build-*/` directories and autotools-generated files exist in the
> working tree. Never `git add -A`/`git add .`; add specific files by name.

**Multi-language v4 libraries:** the C CLI remains the released legacy tool; the
new database engine lives under `v4/` in Rust and Go. During SOW-0016, only the
exact current unsigned Phase-1 v4 contract is supported. Rust:
`nice cargo test --manifest-path v4/rust/Cargo.toml`;
Go: `nice go -C v4/go test ./...`. Shared cross-language artifacts live in
`v4/conformance/` and must be opened by both readers.

### Project-specific overrides

- **FireHOL-wide operational knowledge** (servers d1 and iplists, deployment,
  update-ipsets, the iprange release process, MaxMind/Disqus credentials) lives in
  the parent `~/src/firehol/AGENTS.md`. Consult it for deployment/release work;
  do not duplicate its secrets here.
- **iprange releases** are a direct admin push to `master` + signed tag (not a PR
  flow) — full procedure is in `~/src/firehol/AGENTS.md` under "iprange Release
  Process".
- **`docs/` is reserved for the GitHub wiki**; internal design/SOW docs live under
  `.agents/sow/`. End-user CLI docs live in `wiki/`.

### Preservation Notes

- Fresh bootstrap (2026-06-21). No pre-existing project `AGENTS.md` content
  existed before this initialization — the prior seed file (created during
  `bootstrap-repo`) was fully incorporated into `## Goals` and
  `### Project-specific overrides` above. No `AGENTS.md.pre-sow.bak` was needed
  (no project memory to preserve).
- Engine architecture and the exact v4 format are maintained under
  `.agents/sow/specs/` as current specifications; old SOWs remain historical
  evidence only.

Project SOW status: initialized
