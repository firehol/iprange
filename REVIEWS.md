# REVIEWS.md — Review and Implementation Process (iprange repo only)

This file is the single authority for HOW work is planned, implemented,
reviewed, and accepted in this repository. `AGENTS.md`, SOWs, and project
skills reference it; they must not restate it. It overrides any older
review-protocol text found in a SOW (including SOW-0028's wave-era standing
rules) unless the user states otherwise for a specific case.

Scope: this repo only. It does not change global skills or documents outside
the repository.

## Non-negotiable principles

1. **Every reviewer runs in strong adversarial mode with the
   `project-final-review` skill** — the lead, all seven internal roles, and
   the external astra control. No reviewer is a second pair of eyes; each
   tries to prove the work wrong within its role.
2. **Reviewers are not for discovery.** The lead audits its own work first
   (see Implementation step 3). A defect found first by a reviewer or by
   astra that the lead's self-review should have caught is a process
   failure — fix the self-review, not just the defect.
3. **Evidence-first, tests-never-rerun.** The lead runs every required test
   and stages the output for reviewers. Reviewers must not rerun test
   suites. They review the *suitability, practicality, and gaps* of the
   tests and evidence. A reviewer may write and run small targeted probes
   **only inside its own sandbox folder** (`.local/<role>/`) to prove or
   refute a specific finding — never in `/tmp`, never in the repo tree,
   never a full suite or battery.
4. **Reviewers are persistent.** Each role is spawned once per SOW and kept
   open; the lead continues the same session with "re-review at HEAD X".
   Roles are never restarted to "get a fresh view" unless a session is
   genuinely dead.
5. **No walls of text.** The lead prepares a shared review kit once per
   gate (files under `.local/shared/`) and invokes each role with a
   three-line message: role file, status file, go. Nothing else is
   regenerated per role.
6. **Chunked review beats big-bang.** Internal roles review **smaller
   chunks than a milestone** — one implementation step, or a batch of
   closely related small steps at the lead's judgment. The lead cannot
   match astra's depth on a whole milestone; it matches it on small scope.
   Deferring role review to milestone level just hands all findings to
   astra; that is prohibited.
7. **astra runs rarely** — plan gate (future SOWs) and milestone gates
   only. Never per step, never per fix round.
8. All builds, tests, probes, and reviewer commands run under `nice`
   (workstation policy, `AGENTS.md`). Every reviewer step and probe is
   bounded by an explicit timeout.

## Roles

Seven internal roles, definitions in `.agents/review-roles/` (durable,
committed); shared reviewer ground rules in `.agents/review-roles/README.md`:

| role | file | owns |
|---|---|---|
| tester | `tester.md` | claim ledger: every acceptance criterion has a detecting test; weak-assertion and mutation review |
| operations | `operations.md` | unhandled failures: transport, process lifecycle, deadlines, resource exhaustion |
| parity | `parity.md` | Go/Rust wire format and observable-semantics equivalence |
| portability | `portability.md` | native idioms, cross-OS behavior, mmap-only/zero-copy policy |
| security | `security.md` | trust boundaries, secrets, and truthfulness/completeness of records |
| performance | `performance.md` | allocations, copies, budgets, benchmark methodology |
| fit-for-purpose | `fit-for-purpose.md` | the SOW itself: did we complete what it says, on scope, without drift |

The older `glm` role and `closure` role from SOW-0028's wave rounds are
retired; their duties belong to fit-for-purpose (whole-SOW truth and
completeness) and to the external astra control.

Internal roles and implementer workers use the lead assistant's own model
(standing user instruction 2026-09-14). Parallelize with as many own-model
workers/reviewers as the work allows; never block on a single one; never
stop running workers — spawn in parallel instead. When the user asks to use
the swarm, read and follow the user's swarm rules file
(`~/.codex/SWARM.md`) in whole.

External control: **astra** (gpt-6-astra) via the `external-reviewers`
runner, static read-only review.

## Review kit (`.local/`, gitignored)

The lead maintains this structure continuously — it is the reviewers' only
input channel besides the repo:

```text
.local/shared/
  README.md              what is here and how to use it
  plan/<sow-slug>/       milestones and steps extracted/pointed-to from the active SOW
  head                   exact revision under review (full SHA, one line)
  status.md              current chunk: scope, changed files, contracts touched,
                         SOW requirement IDs, lead's self-review findings + dispositions
  evidence/<gate>/       one directory per gate:
    manifest.json        command, rc, wall time, sha256 of log, for every test run
    *.log                full test output, unedited
.local/<role>/           each role's private sandbox (probes, stubs, mutated
                         reports, its report.md) — set up once, never reset by the lead
```

Rules for the kit:

- Evidence logs are complete and unedited; the manifest lets a role verify
  the log belongs to the recorded command and revision. A role finding a
  mismatch between manifest and log is a P1 against the lead's process.
- `status.md` is the ONLY per-gate narrative the lead writes; roles read it
  instead of receiving regenerated summaries.
- Binaries under review are staged in `.local/shared/binaries/` with
  SHASUMS (existing pattern) so roles can run probes without building.

### Kit hygiene (binding — the 370 GB incident)

At milestone-4 close-out, `.local/` held 370 GB: eighteen reviewer sandboxes
each kept a full copy of the repo tree *with its cargo/go/C build target*
(20–26 GB each) from completed rounds. Prevention, enforced by
`.local/kit-gc.sh`:

- Roles must **never copy a buildable repo tree into the sandbox**. Probes
  that need source mutations use a **symlink farm**: symlink every file of
  the tree into the sandbox and materialize only the mutated file(s)
  (the `r13-kit` pattern). Probes that need compiled code use the staged
  binaries in `.local/shared/binaries/` — not a private build.
- If a role genuinely must build, it sets `CARGO_TARGET_DIR`/`GOCACHE` to
  one shared per-role location (`.local/<role>/.targets/`), never inside a
  tree copy, and reports the build in its round notes so the lead can
  prune it.
- Every role sandbox must be **≤ 1 GB after a gate closes**. The lead runs
  `.local/kit-gc.sh` (report) at each milestone-gate close, attics any
  `*.md` exhibits with `.local/kit-gc.sh --attic <dirs>`, then prunes stale
  build targets with `--prune-builds --apply`; the live (highest-numbered)
  kit per role is protected by the script and is handled at the *next*
  gate's close. Deleting stale round trees is a lead duty, not a reviewer
  one; reports, manifests, and anything manifest-referenced are never
  pruned (the script refuses `shared/`, reports, and `--keep` paths).

## Lead invocation message (exact shape)

```text
Reviewed HEAD: <sha>. Your role: .agents/review-roles/<role>.md — read it
in whole, plus .agents/review-roles/README.md. Kit: .local/shared/
(head, status.md, evidence/<gate>/). Go. Re-review of your prior findings
at this HEAD.
```

(First-ever invocation omits the last sentence.) Nothing longer.

## Work planning (new SOWs)

1. The lead discusses needs with the user, does the analysis, and drafts the
   SOW with **milestones and ordered steps per milestone**. Each step is
   reasonable work — not too small, not too big (rough guide: one step ≈
   one reviewable chunk for the 7 roles).
2. **astra reviews the plan**: it reads the SOW, verifies the lead's
   analysis, evaluates milestones and steps, and returns feedback.
3. The lead revises and re-runs astra on the delta until astra returns
   **GOOD TO IMPLEMENT**. Implementation may not start before that.
4. The user resolves scope/product/risk forks; astra does not override user
   decisions.

Exception: a SOW whose milestones astra already approved (e.g. SOW-0028)
skips this gate.

## Implementation loop (per step)

1. **Implement** the step (lead directly, or parallel implementer workers
   with disjoint write scopes at the lead's choice — the lead integrates,
   audits, and owns the result as its own).
2. **Lead self-review**: the lead audits adversarially with the
   final-review skill and runs the **targeted tests** for the step. Findings
   are fixed and the loop repeats (from "implement") until the lead finds
   no more issues. The self-review outcome (what was attacked, what was
   found, what was fixed) goes into `status.md`.
3. **Role round (small chunk)**: lead stages the evidence, updates the kit,
   invokes all 7 roles in parallel (one message each, continued sessions).
4. **Adjudication**: any role FAIL ⇒ the lead verifies the finding.
   - Real ⇒ fix, then ask the same role sessions to re-review (step 2→3).
   - False positive ⇒ the lead negotiates in the role's session with
     concrete counter-evidence; a finding is dropped only if the role is
     shown wrong or the user rules. The lead does not silently discard a
     role finding.
   - Loop until **all 7 roles approve** the chunk.
5. **Commit per step** locally once the chunk is approved (bisectable
   history; do not push yet).

## Milestone gate

1. All steps of the milestone are through the loop above, committed locally.
2. **astra delta review**: lead continues its astra session (see astra
   identity below) with the delta: commit list, kit pointers (head,
   status.md, evidence/). astra reviews the whole milestone with the
   final-review skill in strong adversarial mode, and receives explicit
   standing instructions to:
   - enforce the SOW's scope — flag any drift, unapproved addition, or
     silent behavior change the lead introduced;
   - verify claimed fixes from previous turns actually landed at this HEAD;
   - judge evidence suitability without rerunning suites (targeted probes
     in its own session workspace only).
   Verdicts: `PRODUCTION GRADE` passes; `NEEDS CHANGES` with any verified
   in-scope P0/P1/P2 blocks: fix ⇒ roles re-review affected code ⇒ astra
   re-review, repeat. False positives are rebutted to astra in-session with
   evidence and the exchange is preserved; unresolved P0-P2 findings are
   never waived by the lead alone.
3. **Full battery** runs once at the milestone boundary (not per step).
   A battery failure returns the milestone to the implementation loop; the
   affected chunk is re-reviewed by the roles before the battery reruns.
4. **Push** `origin master` autonomously — this process pre-authorizes
   per-milestone pushes after steps 2–3 pass. No signing/tags here (the
   release process is separate, see `AGENTS.md`).
5. Move to the next milestone. Record the gate (verdicts, HEAD, battery
   result) in the SOW.

## astra identity and continuity

- **One astra session per lead assistant session, per SOW.** While the same
  lead session works the same SOW, it keeps and resumes the same astra
  session (by exact session ID; never `-c`/`--last`). The session ID is
  recorded in the active SOW at first use.
- When the user stops the lead and starts a new lead session, the new lead
  starts a **new astra session** for continued work on the SOW, and notes
  the handoff (prior astra session ID + last verdict) in the SOW.
- Astra is prompted neutrally: factual scope, delta facts, pointers — never
  the lead's analysis, opinions, or steering; the exact prompt is shown to
  the user before each invocation but requires no renewed approval under
  this standing protocol.
- Astra runs the reviewer client's read-only mode; it does not modify the
  tree and does not run the project's test suites.

## Test execution policy (what the lead runs, and when)

Binding details recorded from user decisions 2026-09-16:

1. **During a step: targeted tests only** (`--filter`/`-run`/named cases).
   The full battery never runs per step.
2. **Full battery: once per milestone gate** (milestone gate step 3).
   Expensive axes (full pressure sweep, whole-program static analysis) run
   only inside that battery, at most once per gate, on the real tree.
3. The **standard suite stays ≤ ~60 s** wall as the default entry; any step
   expected to exceed ~2 wall-minutes must be named with its cost in the
   SOW validation plan before it runs; individual tests have a 15 s limit
   including setup/cleanup, measured per test, and grouping/sharding may
   not hide a slow test.
4. One overall concurrency budget across tracks; concurrent engine runs use
   per-run sandbox roots (never mutate shared repo state like the root
   `iprange` symlink).
5. Remove repetition, not assertions: a full sweep replaces its overlapping
   subset in the same invocation; no retry-on-failure in forgery batteries
   (first failure is the diagnosis); C-oracle comparisons stay invocable
   but are not automatic in the standard set.
6. No nightly timers. Expensive checks need a stated purpose and an
   explicit invocation.

## Reviewer work rules (all roles)

- Tree is **read-only**; no `git add/commit/checkout/stash/reset` ever.
- Sandbox: `.local/<role>/` only, plus read access to `.local/shared/` and
  the repo. Small probes there must be under `nice` with explicit timeouts;
  no heavy batteries, no `pkill`/`killall`; kill only own children (track
  PIDs). No whole-tree copies with build targets — use a symlink farm plus
  the materialized mutated file(s), and shared binaries
  (see § Kit hygiene); the sandbox must be ≤ 1 GB when your round ends.
- **Deliberation budget (binding):** roles have no ambient clock or turn
  counter, so the dispatch brief must carry the protocol and the role must
  self-administer it: `date +%s` once at start (T0) and before every
  numbered probe, printing elapsed in the probe line; a per-probe budget
  (default 4 min) with **at most two re-attempts** — the initial attempt
  plus two retries, three attempts in total; a fourth attempt is
  prohibited and the probe is recorded `INCONCLUSIVE: <named obstacle>`,
  which is a valid, reportable terminal outcome and lead-adjudication
  input; a total wall budget, and at 70 % of it the role stops probing and
  writes the report with what it has. A probe whose intent repeats a
  previously executed probe's intent is a stop-and-report condition, not a
  re-derivation invitation (re-deriving at greater depth is the documented
  overthinking failure mode). The role appends one heartbeat line per
  probe boundary (`[probe k/N attempt m elapsed Ts verdict]`) to
  `.local/<role>/HEARTBEAT` in its sandbox so the lead can see progress or
  silence from outside. Time/thinking budgets belong in every dispatch
  brief, not in ad-hoc instructions.
- Severity conventions (binding): **P0** corruption/crash/breach; **P1**
  wrong behavior on valid input, or an explicitly claimed contract with no
  detecting test; **P2** contract/records/measurable-performance defects,
  bypassable gates; **P3** cosmetic.
- A finding without a concrete scenario (trigger, expected, actual,
  material impact, causal path) is not a finding.
- Missing-test rule: name the specific input that violates the claim and
  show the suite accepts it.
- Same-failure search: when one instance of a class is found, hunt the
  class across the chunk.
- Report: `Reviewed HEAD: <sha>`, verdict PASS/FAIL, numbered findings
  (severity, file:line, trigger, expected, actual, impact, causal path),
  P3s last. Written to `.local/<role>/report.md`; compact copy returned in
  the session reply.
- Roles stay open across rounds; later messages are delta re-reviews.
