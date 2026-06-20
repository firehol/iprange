# iprange — Agent Instructions

## Goals

`iprange` is FireHOL's high-performance IP range manipulation tool, written in C
(`src/`, autotools + CMake builds; CLI binary `iprange`). It is used by
`update-ipsets` for set operations (union, intersect, exclude, dedup, compare)
over large IPv4/IPv6 lists.

**Direction:** evolve `iprange` into a multi-language **engine** (C, Rust, Go)
with a portable, architecture-neutral **binary threat-intel format** and
ready-to-use **SDKs**. `update-ipsets` becomes a public threat-intel source;
Netdata is the first consumer — **indirectly**, via the update-ipsets SDK (Rust
for netflow, Go for topology/network-viewer), which embeds `iprange`.

The full engine/format/SDK design (decisions, binary layout, interval-map core,
phasing) is the target-direction spec at
[`.agents/sow/specs/design-iprange-engine.md`](.agents/sow/specs/design-iprange-engine.md).

Success = a correct, fast, dual-stack engine where all three language
implementations pass one shared conformance corpus and stay within a 5–10%
performance band, plus a portable signed binary format consumable across C/Rust/Go.

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

No runtime input project skills exist yet. **Decision (2026-06-21): defer project-skill creation** — this project grows skills incrementally; a missing skill is better than a generic one. Strong candidates to capture once concrete: (a) the multi-language conformance + benchmark harness workflow, (b) the C build/test/sanitizer workflow, (c) the binary-format/interval-map invariants. Tracked by `.agents/sow/pending/SOW-0001-20260621-iprange-engine-and-binary-format.md`.

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

Test:

```bash
./run-tests.sh            # canonical test suite (tests.d/)
./run-unit-tests.sh       # unit tests (tests.unit/)
./run-build-tests.sh      # build-matrix tests
./run-sanitizer-tests.sh  # ASan/MSan/TSan/valgrind variants (tests.sanitizers.d/, tests.tsan.d/)
```

> Many untracked `build-*/` directories and autotools-generated files exist in the
> working tree. Never `git add -A`/`git add .`; add specific files by name.

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
- `design-iprange-engine.md` was authored before bootstrap and moved into
  `.agents/sow/specs/`; preserved as the engine target-direction spec.

Project SOW status: initialized
