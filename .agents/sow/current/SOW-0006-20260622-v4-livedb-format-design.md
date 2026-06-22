# SOW-0006 - iprange v4 live-DB format: design and lock

## Status

Status: in-progress

Sub-state: v4 design spec drafted (`.agents/sow/specs/design-iprange-v4-livedb.md`);
**locked through an external reviewer panel** of seven independent reviewers,
anonymized **R1–R7** throughout this SOW (R1 was the strictest; R5/R6/R7 produced
output only intermittently and are non-voting). Locked when the reliable reviewers
all voted READY TO IMPLEMENT. No code was written until the spec was locked.

## Requirements

### Purpose

Design and **lock** the on-disk format for a live, mmap'd, mutable IP-range database
(v4) that fixes the retention performance problem in `update-ipsets`: removal must
not scan thousands of cohort files (CPU), and a change must not rewrite the whole
file (I/O). v4 is the live working store; v3 stays the sealed published snapshot v4
exports to.

### User Request

Costa: build a "live file format" as "v4, as a live db." Decisions reached in
discussion: fixed-size records → bitmap allocator; B+tree (page-fanout) not AVL;
**no concurrent read/write** — exclusive writer lock, readers pure-read, short-lived
API; COW + double-meta crash safety (kept despite no concurrency); little-endian
portable; behavioral/cross-read conformance (byte-identity likely dropped). Specific
constants (page size, fanout, scope width, checksum, fsync) are to be **measured**,
not guessed. "Write v4 format and review it with R1, R5, R6, R7, R4,
R2, R3 until all say READY TO IMPLEMENT."

### Assistant Understanding

Facts (decided in discussion, recorded in the spec §3):
- Fixed record `[from,to,scope]`; mmap mandatory (dataset > RAM); production pains
  are CPU (cohort-scan comparisons) + I/O (rebuilds), not memory.
- Verified current bottleneck: `update-ipsets/pkg/engine/retention_update.go:381-419`
  compares the current set against every cohort; add-date comes from the filename;
  no IP index.
- The fix is an IP-indexed mutable structure: COW B+tree of fixed records.

Inferences:
- Exclusive-writer + no-MVCC is the right simplification given open-change-close
  usage; COW is still required for crash safety (orthogonal to concurrency).

Unknowns → the MEASURED set (page size, fanout, scope width, checksum, fsync,
bitmap strategy) resolved by the SOW-0005 benchmark harness, not this SOW.

### Acceptance Criteria

- A complete v4 design spec that the **entire** reviewer panel (R1, R5, R6,
  R7, R4, R2, R3) rates **READY TO IMPLEMENT** with no remaining
  P0/P1/P2 findings.
- All locked decisions (D1–D10) and measured tunables (M1–M6) recorded; open
  decisions (O1–O3) resolved by the user.
- No implementation in this SOW (design + lock only). Implementation is a follow-up
  SOW gated on the locked spec.

## Analysis

Sources checked:
- `update-ipsets/pkg/engine/retention_update.go` (removal/cohort scan, verified).
- `.agents/sow/specs/binary-format-v3.md` (key encoding, snapshot it exports to).
- LMDB / bbolt / redb design (COW B+tree, meta-page commit, allocator) — to be read
  at source level during review iterations to confirm corner-cases.

Current state:
- Spec drafted with full page/record layouts, the COW + double-meta commit protocol,
  the flock rwlock model, range-op semantics, conformance model, corner cases.

Risks:
- It is an embedded database; crash-recovery and allocator/commit correctness are the
  high-risk areas — hence the multi-round adversarial review before any code.

## Pre-Implementation Gate

Status: needs-user-decision (O1–O3) + review-lock pending.

(Implementation is out of scope for this SOW. Gate will be completed in the
follow-up implementation SOW, after the spec is locked and O1–O3 are decided.)

Open decisions (block the lock): O1 conformance model; O2 re-add semantics; O3 C
writer/reader role — see spec §13. Recommendations recorded there.

## Implications And Decisions

D1–D10 locked (spec §3); M1–M6 measured (spec §3 + SOW-0005); O1–O3 pending user.

## Plan

1. Draft v4 spec. ✅
2. Run the reviewer panel (R1, R5, R6, R7, R4, R2, R3),
   unbiased/read-only, asking for soundness, crash/COW/lock correctness, corner
   cases, security, and an explicit READY-TO-IMPLEMENT verdict.
3. Fix every P0/P1/P2; re-run the **same** scope (+ fix notes) — never narrow scope.
4. Iterate until all reviewers vote READY TO IMPLEMENT.
5. Resolve O1–O3 with the user; mark the spec LOCKED.
6. Open the implementation SOW (Go reference + Rust port + behavioral/cross-read
   conformance corpus + crash-recovery tests).

## Execution Log

### 2026-06-22

- Drafted `.agents/sow/specs/design-iprange-v4-livedb.md`.
- **Round 1** (7 launched): R1, R5, R6, R4, R2, R3 all
  **NOT READY**; R7 returned empty (non-functional — excluded as non-voting).
  Unanimous: core architecture (COW + double‑meta commit) is **correct**; findings
  are spec‑precision gaps. Convergent union addressed in the round‑2 revision.
- **User decisions:** O1 = behavioral + cross‑read; **O2 dissolved** — `scope` is
  opaque, `set(from,to,scope)` unconditionally applies the caller's scope (the DB
  imposes no policy); O3 = C served via the Rust library (no native C).
- **Key simplifications adopted:** (a) the generic `set` primitive removes the whole
  overlap/re‑add‑policy finding class; (b) **eliminated the persistent on‑disk
  bitmap** — the writer derives the free set from the reachable tree at open (R5's
  insight, taken further), dissolving five high‑severity allocator findings and
  making allocator crash‑recovery automatic.
- **Round‑2 revision** rewrites the spec: fixed‑offset meta bootstrap; CRC32C +
  `checksum_algo`; `page_size = 4096`; full B+tree invariants + root/height rules;
  ordered reader validation (§9) with cycle/height/truncation bounds + typed‑reject;
  mmap‑safety §10; `flock` pinned (local‑fs, bounded wait); commit I/O‑error/ack +
  whole‑page meta write + file‑creation + txn_id init/tie/wrap; forward‑compat;
  reserved‑byte zeroing; honest `O(log n + k)`.
- **Round 2** (6 launched): **R4 READY** (0 P0/P1, doc P2/P3 only); R1,
  R2, R3 **NOT READY** on small precision items; R5 + R6 truncated
  mid‑output (flaky review harness — R6's partial transcript confirmed the spec
  consistent, only "pin meta_size" + "reject from>to"). Architecture validated by all
  reviewers across both rounds. First clean pass achieved.
- **Round‑3 revision** (targeted): crash‑contract wording (survivor old‑or‑new,
  unacked); `page_size` pinned 4096 + exact meta offset table + `meta_size = 90`;
  static check only among checksum‑valid metas; `created_unixtime` → static;
  min‑occupancy + `TREE_HEIGHT_MAX = 32`; threaded `[lo,hi)` separator bound; default
  full‑validate‑before‑expose + writer‑open validation; explicit geometry/overflow
  checks; forward‑compat (major bump for new flags‑bits/page‑types, fail‑closed);
  trailing‑page reclaim; CRC32C exact params + high‑32‑zero; `set`/`delete` input
  contract + `u128_dec`; meta `entry_count=0` + zero‑filled tail; `O_CLOEXEC` +
  flock‑auto‑release.
- **Round 3** (6 launched): **R4 READY + R2 READY**; R1 NOT READY (1 P0 +
  4 P1 + 2 P2 — all clarifications) + R3 NOT READY (4 P2 + 2 P3); R5 + R6
  truncated (non‑voting; R6 nits captured). Every reviewer affirmed the
  architecture; remaining backlog = ~24 clarification edits, the most‑cited being the
  `family_max+1` rightmost‑bound edge (all reviewers).
- **Round‑4 micro‑revision** (clarifications only, no architecture): bootstrap reads
  both meta candidates independently (tolerates torn meta‑A); rightmost threaded
  bound = `family_max` inclusive (`family_max+1` never computed); `page_size` fixed
  for all v4.x; balanced‑tree invariant (all leaves at depth = `tree_height`);
  unaligned‑read rule; writer `O_NOFOLLOW|O_CLOEXEC` + reader `MAP_SHARED` + fork;
  every branch (incl. root) ≥ 2 children + distinct `child_pgno`; explicit cross‑leaf
  overlap; full‑validate tail‑zero = MUST; `record_count`‑mismatch reject;
  `TREE_HEIGHT_MAX` reworded + writer refuses to exceed; well‑formed static fields
  defined; `family_min/max` + `scope_width`‑fixed defined; `set`/`delete` full‑family
  + single‑level + no‑op + transitive‑coalesce.
- **Round 4** (6 launched): **R2 + R3 + R4 READY**; R1 NOT READY on a
  single B+tree bound‑validation wording cluster (nested‑branch bound inheritance +
  separator underflow + `[lo,hi)`/`[lo,hi]` + "declared level" + `meta_size`
  condition); R5 + R6 truncated (non‑voting). 3 of 4 reliable reviewers READY.
- **Round‑5 micro‑fix:** rewrote §9 validation as an explicit recursive
  `validate_node(pgno, depth, lo, hi)` with **inherited** parent bounds (root uses
  `family_min/max`), separators constrained `lo < sep[0] < … ≤ hi` (no underflow,
  `family_max+1` never computed), depth‑based page‑type checks (no "declared level"),
  inclusive `[lo,hi]` + defined `capacity`, `meta_size` conditioned on
  `version_minor`. Added a §8 caller‑guidance note: scope reduction is a `set` (may
  merge), distinct from `delete` (erase, never merges) — per user's
  `{A,B}`→`{A}` merge‑on‑removal example.
- **Round 5** (6 launched): **R2 + R4 READY**; R1 NOT READY (0 P0, 1 P1 +
  3 P2 — all on the §13 v3‑export contract, a peripheral bridge earlier rounds hadn't
  stressed) + R3 NOT READY (0 P0/P1, 2 P2). R5 + R6 truncated. Core format
  validated by all; only the export bridge + pseudocode precision remained.
- **Round‑6 fix:** wrote a normative §13 **export contract** (`export_v3(file,
  type_id)`: opaque `scope` → v3 value mapping; typed `ExportUnrepresentable` error
  for the degenerate full‑IPv6 range v3 can't count); `validate_node` loop made
  unambiguous + leaf occupancy + threaded `prev_to`; explicit meta `page_type==1` /
  reserved / high‑checksum checks; `meta_size ≥ 90`; separators clarified as ROUTING
  keys (sparse gaps legal, no `sep == first‑key`); root page_type checked after CRC.
- **Round 6** (6 launched): **R2 READY**; R1 + R3 NOT READY but **0 P0/P1
  on the core** — every reviewer explicitly signed off the core v4 format (validation,
  COW, allocator, locking, mmap, conformance) and confined all findings to §13's
  v4→v3 export contract (totality/accumulation overflow, v3 coalescing, u32
  values‑cap, missing v3 metadata inputs); R3: "one revision away from READY". R5 +
  R6 truncated.
- **Round‑7 fix:** rewrote §13 export as a **thin delegation to the locked v3 writer**
  — `export_v3(file, type_id, v3_meta)` produces an ordered (range,value) stream + v3
  metadata; the v3 writer owns coalescing, interning, the u32 cap, and unique‑IP‑count
  accounting; `ExportUnrepresentable` surfaces v3's rejections (incl. total count 2^128
  / full‑IPv6 by one record *or many*). Added §9 branch upper bound `2 ≤ (s+1) ≤
  branch_max`. **Core v4 format is locked by reviewer consensus.**
- **Round 7** (6 launched): **R4 READY (zero findings of any severity)**; R1 +
  R2 + R6 all NOT READY on the **identical** set — a branch‑capacity
  **off‑by‑one I introduced in the round‑7 edit** (`branch_max` is max *separators*,
  not children, so a fully‑packed branch was wrongly rejected) plus the §13 values‑cap
  number and a `record_count` interop nit. §13 export delegation **confirmed resolved**
  by all. R5 truncated; R3 timed out (30‑min cap).
- **Round‑8 fix:** §9 branch bound corrected to `1 ≤ s ≤ branch_max`; §13 values‑cap
  now defers the exact number to the v3 writer; `record_count` exactness made a writer
  MUST so verifying/non‑verifying readers accept the same conforming files. (Lesson:
  the round‑7 "improvement" introduced a regression the confirm pass caught — exactly
  its purpose.)
- **Round 8** (6 launched): **R2, R3, R4, R5 READY** (five‑of‑six, zero
  P0/P1, cosmetic P2/P3 only); R1 lone NOT READY — 1 P1 (bootstrap "fail‑open": an
  intact‑but‑incompatible meta should fail closed, not be discarded as torn) + 1 P2
  (validation scope must be reachable pages only, not free/orphan pages) + 2 P3. The 3
  round‑7 fixes confirmed resolved by all.
- **Round‑9 fix:** §5.1 bootstrap now classifies a candidate as torn (discard),
  intact‑but‑incompatible (**reject the file, fail closed**), or valid; §9 limits
  CRC/structural checks to the **reachable** tree + metas (orphan/free pages never
  cause rejection); explicit empty‑tree guard; `record_count` intro qualifier; title.
- **Round 9** (4 reliable reviewers): **R1 READY — zero P0/P1/P2/P3 findings**;
  R2 READY (3 informational P3); R4 READY (1 cosmetic label P2, 4 P3); R3
  re‑run in progress (READY in round 8). R1 verified every round‑8 fix and found
  nothing left. **SPEC LOCKED** (`design-iprange-v4-livedb.md` status → LOCKED).
- **Outcome:** the v4 live‑DB on‑disk format is locked by reviewer consensus after 9
  rounds. Remaining items are cosmetic P3s (worked examples, label clarity) deferred
  to the implementation SOW. Awaiting user go‑ahead to (a) commit the locked spec +
  these SOWs, and (b) open the implementation SOW (Go reference + Rust port +
  conformance / crash‑recovery / fuzz corpus).

## Validation

Pending (review lock).

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- Implementation SOW (post-lock): Go reference writer (fork-bbolt-informed) + Rust
  port (redb-informed) + conformance corpus + crash-recovery + fuzz/oracle.
- SOW-0005 supplies M1–M6 (page size, fanout, scope width, checksum, fsync, bitmap).
- SOW-0004 test-hardening patterns (oracle, fuzz, malformed-input) extend to v4.

## Regression Log

None yet.
