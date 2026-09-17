# SOW-0032 - Repo-local review and implementation process (REVIEWS.md)

## Status

Status: completed

Sub-state: delivered 2026-09-17 by direct user instruction; binding for the
remaining work of SOW-0028 and all future SOWs.

## Requirements

### Purpose

Make the user's way of working — planning with an astra plan gate, chunked
implementation with persistent adversarial reviewer roles, evidence-first
reviewers, a rare persistent external control, targeted tests per step and
one battery per milestone with autonomous push — permanent, repo-local
instructions for new assistant sessions.

### User Request

Faithful summary (2026-09-17, after evidence review of the SOW-0028 wave
protocol and the 2026-09-16 test-execution decision):

- Work planning: lead discusses needs, analyzes, drafts a SOW with
  milestones and steps; astra reviews the SOW (analysis, milestones,
  steps); iterate until astra says GOOD TO IMPLEMENT.
- Implementation: steps of reasonable size; implement → lead adversarial
  self-review + required tests until no findings → 7 persistent internal
  roles (final-review skill, strong adversarial) → negotiate/fix until all
  7 approve → astra reviews the delta at the gate with strong scope-drift
  instructions → full battery → commit, push → next milestone.
- Roles (final roster, user-confirmed): tester, operations, parity,
  portability, security, performance, fit-for-purpose. No internal glm role
  (retired; closure role retired; their duties belong to
  fit-for-purpose/astra).
- Reviewers must not rerun suites: the lead prepares all test evidence in a
  `.local/` folder structure (milestones, steps, status, evidence); the
  invocation message is only "role file / status file / go". Reviewers may
  write and run small targeted probes inside their own sandbox folder (not
  /tmp, not random places).
- Rationale: fast routine work (targeted tests only per step); reviewers
  review test suitability/gaps, not reruns; everyone reviews in
  final-review adversarial mode; reviewers spawned once and continued;
  astra runs rarely; the lead does not rely on reviewers for discovery.
- Deliverable: a document called REVIEWS.md; AGENTS.md and other repo docs
  reference it. Instructions for new sessions, not event logs.
- User decisions to questions: roles gate chunks smaller than a milestone
  (lead batches small steps at its judgment; milestone-only role review is
  prohibited because it hands all findings to astra); commit per step, push
  per milestone with autonomous push authorization; implementation workers
  allowed at lead's discretion with disjoint write scopes; astra = one
  session per lead assistant session per SOW (new lead session ⇒ new astra
  allowed); binding now, including SOW-0028's remaining close-out with the
  new 7-role roster; the SOW-0028 plan itself was astra-authored, so the
  plan gate is skipped for SOW-0028, not for future SOWs.

### Assistant Understanding

Facts:

- The prior protocol lived in SOW-0028 "Standing Review Rules" (lines 3-75)
  and eight gitignored `ROLE.md` files under `.local/<role>/` — durable
  instructions in volatile, non-committed storage.
- The 2026-09-16 test-suite execution policy (≤60 s standard suite, 15 s
  per-test, one concurrency budget with isolation, no nightly timers,
  remove-repetition-not-assertions) lived only inside SOW-0028 prose.
- The `external-reviewers` skill supports persistent sessions by ID,
  read-only reviewer protection, and standing authorization for later
  rounds — the mechanics for autonomous astra already exist.
- `.local/` is gitignored (`.gitignore:61`); `.agents/` is committed.

Inferences:

- Role definitions must move to a committed path (`.agents/review-roles/`)
  or the protocol dies with this workstation.
- The evidence-first kit must be a fixed layout so one short message can
  invoke any role at any gate.

Unknowns:

- None blocking.

### Acceptance Criteria

- `REVIEWS.md` at repo root contains the complete process; AGENTS.md
  points to it and no longer duplicates the policy text.
- Seven role files + shared README under `.agents/review-roles/`, committed,
  consistent with REVIEWS.md (sandbox-only probes, no-suite-rerun,
  persistent sessions, final-review adversarial mode).
- SOW-0028 "Standing Review Rules" is replaced by a dated supersession note
  pointing to REVIEWS.md; historical wave records remain untouched.
- The retired roles (internal glm, closure) are explicitly retired;
  fit-for-purpose owns whole-SOW and milestone-level truth.
- Test execution policy text in AGENTS.md defers to REVIEWS.md.

## Analysis

Sources checked:

- `AGENTS.md` (working principles, SOW system, commands)
- `.agents/sow/current/SOW-0028-…md` (standing rules lines 3-75;
  wave-19.26 test-execution decision; astra turn history)
- `.local/{tester,operations,parity,portability,security,performance,glm,
  closure}/ROLE.md` (current role text; `.local/closure/ROLE.md`
  mistakenly contains a copy of the parity role — evidence the old kit
  needed durable ownership)
- `.agents/skills/project-final-review/SKILL.md` (methodology base; its
  `/tmp/` probe allowance overridden by role sandbox rule)
- `external-reviewers` skill (session continuity, authorization, verdict
  protocol)
- `.gitignore`, `Makefile.am` EXTRA_DIST, `packaging/tar-compare`
  (dist/release impact of new top-level file)

Current state:

- Protocol exists but only in SOW prose + gitignored sandboxes; new
  assistant sessions inherit it only while SOW-0028 is current.

Risks:

- Two process authorities (REVIEWS.md vs old SOW text) drifting — mitigated
  by supersession note + "single authority" wording.
- Old role sessions may still hold stale `.local/*/ROLE.md` instructions —
  mitigated by invocation messages pointing at `.agents/review-roles/`.
- Autonomous push removes the user's manual gate — accepted explicitly by
  the user; backstopped by the astra milestone gate + battery + recorded
  verdicts and HEADs.

## Pre-Implementation Gate

Status: ready (delivered)

Problem / root-cause model:

- The working process is real and proven across 19+ waves, but it lives in
  the wrong artifacts (one huge SOW + gitignored sandboxes), so it is not
  inheritable by a fresh clone/session and its rules drift per wave.

Evidence reviewed:

- SOW-0028 lines 3-75 (eight-role protocol), "Test suite execution policy"
  (2026-09-16), `.local/astra-verdicts.md` (turn history), the eight
  `ROLE.md` files.

Affected contracts and surfaces:

- `AGENTS.md` working principles + required first checks.
- New `REVIEWS.md`; new `.agents/review-roles/*` (8 files).
- SOW-0028 standing-rules section (supersession note only; history intact).
- Future SOW lifecycle: plan gate (astra GOOD TO IMPLEMENT) and milestone
  gates become normative.
- Release packaging: `REVIEWS.md` is a new tracked top-level file; release
  work must classify it (EXTRA_DIST vs tar-compare filter) like
  `.codacy.yml`/`SECURITY.md` before the next tag.

Existing patterns to reuse:

- `.local/<role>/` sandboxes and `.local/shared/` (binaries/probes) already
  match the kit layout; role text from the current ROLE.md files; the
  external-reviewers session protocol; the 2026-09-16 test policy text.

Risk and blast radius:

- Documentation/process only; no product code or gate logic changes.
  Behavioral change: role roster (8→7, glm/closure retired), reviewer
  execution scope (sandbox probes), push authority (autonomous per
  milestone), astra invocation (lead-driven).

Sensitive data handling plan:

- No secrets or personal data introduced; role files reference repo paths
  and specs only; astra prompts remain neutral and free of operator paths;
  existing personal-path guard rules in the evidence READMEs are untouched.

Implementation plan:

1. Write `REVIEWS.md` (process authority): principles, roles, kit layout,
   invocation message, planning gate, implementation loop, milestone gate,
   astra identity/continuity, test execution policy, reviewer work rules.
2. Create `.agents/review-roles/README.md` (shared ground rules) + seven
   role files derived from the proven `.local/*/ROLE.md` text, amended with
   evidence-first, sandbox-probe, persistence, and the 2026-09-17
   platform-scoping ruling.
3. Update `AGENTS.md`: replace duplicated review/resource bullets with
   pointers to REVIEWS.md; add the process to Required First Checks and the
   skills index.
4. Supersede SOW-0028 standing rules with a dated pointer; keep history.
5. Record this SOW, validate, close in one commit.

Validation plan:

- Lead adversarial self-review (final-review skill) of consistency: roster
  agreement across AGENTS.md/REVIEWS.md/role files; no leftover normative
  duplication; paths resolve; gitignore still correct.
- grep sweep for stale references (glm role as normative, "static review
  only", `/tmp` probe instructions for roles, 8-role counts).
- First real exercise: the SOW-0028 Windows-leg repair chunk runs through
  the new loop (roles via `.agents/review-roles/`, kit per REVIEWS.md) —
  this SOW's real-use evidence.

Artifact impact plan:

- AGENTS.md: updated (pointers, first checks).
- Runtime project skills: no SKILL.md change needed — project-final-review
  is referenced unchanged; the process is AGENTS-level, not a skill.
- Specs: no change — REVIEWS.md is HOW we work (agent memory), not WHAT the
  product does; specs are product contracts.
- End-user/operator docs: unaffected (wiki/CLI docs unchanged; process is
  internal).
- End-user/operator skills: none exist.
- SOW lifecycle: SOW-0032 completed+done in the delivery commit; SOW-0028
  keeps running under the new protocol.

Open-source reference evidence:

- None needed; this is a local process consolidation.

Open decisions:

- All resolved by the user on 2026-09-17 (roster, chunks, commit/push
  granularity, astra continuity, worker use, binding now, SOW-0028 plan-gate
  skip).

## Implications And Decisions

1. Roster typo resolved: "performance" listed twice in the request was
   `operations`; final roster confirmed by user.
2. Chunking: roles review per step (or lead-batched small steps), never
   milestone-only — user rationale: roles match astra only on smaller
   scope.
3. Autonomous per-milestone push to `origin/master` approved; per-step local
   commits.
4. astra: one session per lead session per SOW; new lead session may start a
   new astra session; plan gate skipped only for SOW-0028 (astra-authored).
5. Binding immediately; the SOW-0028 re-anchor round uses the new roster.

## Plan

1. REVIEWS.md — done.
2. `.agents/review-roles/` — done.
3. AGENTS.md pointers — done.
4. SOW-0028 supersession note — done.
5. Close-out — this commit.

## Execution Log

### 2026-09-17

- Read the full standing-rules text, all eight `.local/*/ROLE.md`, the
  test-execution decision, astra verdict history, and the external-reviewers
  skill; asked the user the roster/execution/continuity/push/worker/binding
  questions and the two loop-shape questions; user answers recorded above.
- Wrote `REVIEWS.md`, `.agents/review-roles/README.md`, and seven role
  files; edited `AGENTS.md` (two places) and SOW-0028 (supersession note).
- Noted: `.local/closure/ROLE.md` contains a stale copy of the parity role —
  moot once roles read `.agents/review-roles/`.

## Validation

Acceptance criteria evidence:

- `REVIEWS.md` created (repo root); `AGENTS.md` bullets replaced with
  pointers (`grep -n REVIEWS.md AGENTS.md` shows both references);
  SOW-0028 standing rules replaced by the dated supersession note; role
  files created (`ls .agents/review-roles/` → README + 7 roles); retired
  roles named and reassigned in REVIEWS.md.

Tests or equivalent validation:

- Consistency sweep (lead): roster strings identical in AGENTS.md,
  REVIEWS.md role table, and file set; severity conventions identical in
  README.md and REVIEWS.md; no normative `/tmp`-probe or static-only text
  remains for roles (README overrides the final-review skill's `/tmp`
  allowance explicitly).

Real-use evidence:

- Deferred by design to the first SOW-0028 chunk executed under the new
  process (Windows-leg repairs), recorded there; this SOW is the contract,
  that chunk is its first proof.

Reviewer findings:

- Lead adversarial self-review only (meta/process change executed under
  direct user instruction; role rounds start with the first implementation
  chunk). Self-review caught: (a) swarm/own-model rules would have been
  lost from SOW-0028 — preserved in REVIEWS.md; (b) `.local` kit layout
  referenced a non-existent `plan/` — defined in REVIEWS.md as lead-created
  per SOW; (c) final-review skill's `/tmp` probe guidance conflicts with
  the sandbox rule — explicit override added.

Same-failure scan:

- Grepped AGENTS.md + SOW-0028 for stale normative duplicates of the moved
  policy (glm-as-gate, 8-role counts, "static review" reviewer clauses,
  resource-budget duplication): none remain outside historical wave
  records, which are explicitly marked history by the supersession note.

Sensitive data gate:

- New files contain only repo-relative paths, skill references, and
  process text; no credentials, hostnames, personal paths, or customer
  data.

Artifact maintenance gate:

- AGENTS.md: updated (process pointer, first-checks item 6, resource budget
  defers to REVIEWS.md).
- Runtime project skills: no change needed — final-review and external-
  reviewers consumed unchanged; project-v4-rust untouched.
- Specs: no change — process instructions are agent memory (AGENTS/REVIEWS),
  not product specifications; product contracts untouched.
- End-user/operator docs: unaffected — no CLI/product behavior changed.
- End-user/operator skills: none affected.
- SOW lifecycle: SOW-0032 completed and moved to `done/` in this commit;
  SOW-0028 remains the sole active SOW under the new protocol.

Specs update:

- Not needed; no product behavior or contract changed (reason recorded
  above).

Project skills update:

- Not needed; skills are referenced, not contradicted (reason recorded
  above).

End-user/operator docs update:

- Not needed; `wiki/` and CLI docs unaffected.

End-user/operator skills update:

- Not needed; none exist that consume this process.

Lessons:

- Proven process text must live in committed, discoverable files; SOW prose
  and gitignored sandboxes are the wrong homes for standing instructions.

Follow-up mapping:

- Release packaging classification of `REVIEWS.md` (new tracked top-level
  file; EXTRA_DIST vs tar-compare filter) → tracked here as a pre-tag
  release step in this section; iprange releases already carry this
  checklist step in `~/src/firehol/AGENTS.md`.
- First real-use proof of the process → SOW-0028's next chunk (tracked by
  SOW-0028 itself).
- `.local/astra-verdicts.md` history: verdicts become durable SOW records
  going forward; no migration of past turns is required (they remain in the
  wave records of SOW-0028).

## Outcome

REVIEWS.md is the repo-local process authority; roles are committed under
`.agents/review-roles/`; AGENTS.md and SOW-0028 reference it; binding now.

## Lessons Extracted

See Validation/Lessons: standing process belongs in committed, referenced
files, not in one active SOW's prose or volatile sandboxes.

## Followup

- Pre-release (next iprange release): decide tarball fate of `REVIEWS.md`
  (dev-process doc → likely tar-compare filter like `.codacy.yml`, next to
  the `.github` filters).
- SOW-0028: run the pending Windows-leg repair chunk through the new loop
  (7 roles, kit, astra milestone gate) — first real-use evidence.

## Regression Log

None yet.
