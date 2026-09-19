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
(20–26 GB each) from completed rounds. The controls below prevent that at
its source; they are mandatory. `.agents/tools/kit-gc.py` (committed) is a
**read-only usage reporter**: it measures disk allocation per sandbox,
reports cap violations and large build-target-named directories as
inspection data, and **never deletes, never archives, and never states
that anything is safe to remove** (user decision 2026-09-19: automatic
deletion and its safety classifier were removed as machinery beyond the
actual operational need, which is occasionally removing a few named
directories). Removal is the human procedure defined below.

- Roles must **never copy a buildable repo tree into the sandbox**. Probes
  that need source mutations use a **symlink farm**: symlink every file of
  the tree into the sandbox and materialize only the mutated file(s).
  Materializing means **replacing the link itself** — `cp --remove-destination
  <src> <link>` or `ln -sf` — never writing *through* it: `cp`, `open('w')`
  and editors follow symlinks and will mutate the real tree (operations
  wave-12 disclosed a ~30 s write into `v4/cli/run.py` this way; restored
  from the HEAD blob, verified byte-identical)
  (the `r13-kit` pattern). Probes that need compiled code use the staged
  binaries in `.local/shared/binaries/` — not a private build.
- If a role genuinely must build, it sets `CARGO_TARGET_DIR`/`GOCACHE` to
  one shared per-role location (`.local/<role>/.targets/`), never inside a
  tree copy, and reports the build in its round notes so the lead can
  prune it.
- Every role sandbox must be **≤ 1 GB after a gate closes**. The lead runs
  `.agents/tools/kit-gc.py` at each milestone-gate close and must resolve
  every reported over-cap sandbox before the gate is recorded. The
  reporter's exit code is part of the result: 0 within cap, 1 at least
  one sandbox over cap, 2 scan incomplete because an entry could not be
  measured (its numbers are then partial — an unreadable subtree is a
  finding to explain, never silently zeroed). A sandbox row carrying
  `PARTIAL (n inspection errors below)` has an incomplete measurement and
  must not be read as within-cap. Steady state on a QUIESCENT kit: two
  `chmod 000` privacy fixtures (parity `r2/p2/d1/unreadable` and
  w8-golegacy `sc/perm/dir000`) make exit 2 permanent while they exist —
  that is the reporter refusing to zero an unreadable subtree, not a
  regression. During a review wave the reporter can legitimately list
  MORE errors and more PARTIAL rows: reviewers create their own
  chmod-000 and byte-invalid-name fixtures inside `.local/<role>/`, and
  those are inspection errors until the fixture is removed (the wave-14 role
  runs recorded the reporter at 8 ERROR rows and at 7 in one of their
  three runs — security's, portability's and performance's round-14 reports
  (security rendered 8 ERROR rows; portability rendered 8 ERROR lines, of
  which it attributed 6 to other roles' fixtures; performance's three runs
  recorded 2, 7 and 4) — with the exit
  class and per-row attribution correct in every case). The gate-close
  signal is therefore: exit 2 whose errors are all attributable to known
  fixture paths (the two standing privacy fixtures plus any fixture a
  still-open reviewer session created), zero OVER-CAP rows, and no PARTIAL
  flag on any sandbox row outside those roles. Attachability of the errors
  is a human inspection judgment, not a machine check: the machine-readable
  parts are the exit class and the `--json` error entries, and the
  "zero OVER-CAP rows" half is a **precondition** for closing the gate, not
  a steady state — the lead's § Removal procedure below is what clears a row,
  and the count can also move without it: the wave-15 8→7 drop was the `tester`
  role pruning 18 named superseded scratch paths inside its own current kit to
  satisfy the ≤ 1 GB round-end rule. Its own round-15 report lists the 18 paths
  by name and reports its round-end size; the current size is read from the
  bound `kit-gc-report-real.txt`/`.json` `SANDBOX tester` row, whose TIMED footer
  dates the measurement. No figure is quoted here: a number copied out of a bound
  artifact goes stale the moment that artifact is re-captured, so the reference is
  the durable form. The sandbox is present and within cap, not removed, and no
  defect was involved in the count changing.
  Reducing the count to 0/1 requires the user relocating the standing
  fixtures.
- **Removal of disposable reviewer scratch is pre-authorized for the lead, and
  it must be executed by the guard tool, never by hand.** The owner authorized
  lead-initiated removal of unneeded reviewer scratch on 2026-09-19, with the
  condition that no accident is possible. That authorization covers *what* may
  be removed; it does not relax a single check. `.agents/tools/kit-rm.py` is
  the only sanctioned way to remove anything under `.local/`: it takes a list
  file of literal absolute paths, is dry-run by default, and removes a path
  only after eight guards pass for that exact path and are then **re-run
  immediately before the `rmtree`** (state can change between planning and
  acting):
  **G1** literal absolute path, no glob/quote/control characters, and equal to
  its own `realpath` (so no symlink component);
  **G2** strictly inside `<repo>/.local/`, never `.local` itself or above;
  **G3** depth ≥ 2 below `.local/`, so a **role root can never be removed** and
  every report, HEARTBEAT and brief survives;
  **G4** not `.local/shared` or `.local/_attic-md`, nor inside either — refused
  by identity, never by a name match;
  **G5** a real directory, not a symlink, file or missing path;
  **G6** no `.git` entry (a checkout or worktree is never scratch); no
  unreadable or mode-000 directory **at or inside** the target, since the
  standing privacy fixtures are part of the gate signal and a list line naming
  one must be refused rather than walked past; and no directory the operator
  cannot write, checked **before** deletion — without this, `rmtree` removes
  every writable sibling, then fails on the unwritable child, so a refusal is
  reported after partial destruction and nothing is logged;
  **G7** the path appears in **no** gate artifact — every tracked file,
  `status.md`, `head`, every evidence `manifest.json`, every astra-turn prompt —
  searched in **both** absolute and repo-relative form. Protection is
  **bidirectional**: an ancestor is refused because removing it removes the
  cited path, and a **descendant** is refused because a record citing
  `.local/<role>/kit` depends on everything inside it, so deleting
  `.local/<role>/kit/cache` destroys cited input even though no record spells
  out that deeper path (wave-19 found the descendant case unimplemented, and it
  had already been exercised by real removals);
  **G8** no non-terminal subagent run for this repository, because a role may
  be measuring inside its own sandbox right now; an unreadable status file or a
  missing runs root counts as live and refuses.
  `--execute` additionally requires a non-empty `--reason`, and each removal is
  appended to `.local/shared/removals.log` (timestamp, size, path, reason).
  List lines are paths and are used verbatim: a trailing space is part of a
  directory name, and stripping it would make `--execute` remove a *different*
  directory than the one listed while logging the stripped name. A line of only
  whitespace is a malformed list and is refused.
  Guards fail closed: an unrecognized run state, an unreadable status, a missing
  runs root, or a failed `git ls-files` all lead to refusal, not to removal.
  `kit-rm.py` is tested by an adversarial suite in which every attack must be
  refused **and** one legitimate scratch tree must be allowed, so it cannot pass
  by refusing everything or by removing everything; each guard there is also
  falsified by reverting the corresponding check, because a guard that cannot be
  shown to fail is not a guard.
- **Removal is a lead duty, not a reviewer one.** For each directory the lead decides to remove, all three checks
  must be established *for that path* first, and recorded in the gate
  note:
  1. **ownership** — which role/round created it, and that no session
     still uses it (a live kit is the newest round per role and is
     handled at the *next* gate's close, not this one);
  2. **inactivity** — last modification, and that no open review or
     pending re-review depends on the contents;
  3. **preservation** — whether the subtree holds anything durable:
     `report*.md`, `manifest*.json`, `SHASUMS*`, `*.sha256`, evidence
     referenced by any manifest under `.local/`, or anything
     manifest-referenced elsewhere. Copy such artifacts into
     `.local/shared/evidence/<gate>/` first and bind them in that
     gate's `manifest.json`; the reporter deliberately does no archiving
     for you.
  Then remove that single named path (`rm -rf <exact-path>`), never a
  glob and never a directory the lead has not individually inspected.
  `.local/shared/` is never a removal target.
- **Staging assertions are enforced, not asserted.** Every artifact the
  evidence manifest binds must exist on disk with the recorded sha256 and
  byte count; **every file at any depth** in an evidence directory must be
  bound (a stray staged into a subdirectory is reported like one at the top
  level); and a staged copy must equal the live source it came from.
  `__pycache__` bytecode caches are derived, not artifacts: the checker
  reports them on their own line and the suite's H2 section requires their
  absence, so the proof commands that import from an evidence directory run
  with `PYTHONDONTWRITEBYTECODE=1` (H2 requires the absence of caches
  regardless of how a command was invoked). This is checked by
  `.local/shared/tools/check-evidence-binding.py` (a bound copy of it lives
  in the evidence directory, so the checker is itself verifiable; its exit
  classes are 0 holds / 1 mismatch / 2 usage or uninterpretable manifest)
  and re-run by the kit-gc suite's H section with every staged/live pair.
  Both fail loudly, which is how a stale staged suite, two dropped
  mutation-check bindings, a nested `__pycache__` and a drifted harness
  were found in waves 13-15. Helper scripts a bound log depends on are
  bound alongside it so the directory is self-contained.

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
   - `INCONCLUSIVE AND POTENTIALLY WRONG` ⇒ not a finding; the lead records
     the item and what the role tried, and either resolves it by its own
     investigation or names where it is tracked. It never blocks approval
     and never becomes a fix obligation on its own.
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
   re-review, repeat. **Astra has no inconclusive verdict**: it must decide
   every item it examines — the role-side `INCONCLUSIVE AND POTENTIALLY
   WRONG` state (§ Reviewer work rules) is deliberately withheld from the
   gate reviewer, whose job is to adjudicate the milestone, not to park
   doubts. False positives are rebutted to astra in-session with
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
  (default 4 min) with **up to five attempts per incident** — the initial
  attempt plus four retries. An *incident* is one alleged defect; the five
  attempts are the budget for proving it (re-run, vary the input, isolate
  the mechanism, attack the control instead of the claim, measure the
  counterfactual). **If the fifth attempt has not produced the concrete
  scenario below, the role must report the item as
  `INCONCLUSIVE AND POTENTIALLY WRONG: <what was tried, what remains
  unproven>`** — a valid, reportable terminal outcome and explicit
  lead-adjudication input. Such an item is **not a finding**: it does not
  count toward the verdict, does not block a chunk, and must not be
  recorded with a severity, because the role did not establish it. The
  point of the state is to make an unproven suspicion cheaper to disclose
  than to drop silently; a role that suspects a defect and cannot prove it
  in five attempts has discharged its duty by naming it, not by asserting
  it. A role that files a P0-P2 without the scenario has skipped work it
  was given the budget to do; a total wall budget, and at 70 % of it the role stops probing and
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
- Report: `Reviewed HEAD: <sha>`, verdict **PASS** or **FAIL**, numbered
  findings (severity, file:line, trigger, expected, actual, impact, causal
  path), P3s last, then a separate `## Inconclusive and potentially wrong`
  section holding every item the five-attempt budget could not prove (what
  was tried, what remains unproven, what would settle it). Verdict counts
  findings only: an inconclusive item never makes a round FAIL. Written to
  `.local/<role>/report.md`; compact copy returned in the session reply.
- **This verdict state belongs to roles only.** The milestone gate reviewer
  (astra) does not get it: it must decide every item it examines, either as
  a verified finding with the scenario or as `PRODUCTION GRADE`. See
  § Milestone gate.
- Roles stay open across rounds; later messages are delta re-reviews.
