# SOW-0035 - Creator-only follows the database everywhere

## Status

Status: in-progress

Sub-state: implementation started. Spec exception removed. Detecting snapshot tests pass. Not committed.

## Requirements

### Purpose

Creator-only protection is one choice. Nothing uses it unless the user asked for it, or the source database already has it.

### User Request

The user rejected the remaining exceptions. Creator-only must be under a flag everywhere. Nothing uses it by default unless the user said so, or the database already has it.

### Assistant Understanding

Facts:

- Live create already has the flag. JSON-RPC omits it as unprotected. Existing SDK callers pass true, so current creates stay protected.
- Export, metadata, removal, query, join, and recovery report outputs already follow the source sidecar.
- Five product creates still force creator-only without reading a user flag or a source sidecar: interrupted-create repair, publication snapshot private files, publication reservations, recovery scratch, and the worker control file plus GC envelope.
- `binary-format-v4.md` said publication reservations and recovery scratch remain creator-only. That sentence is the exception the user rejected.
- Test fixtures that write mode `0600` are not product creates.

Inferences:

- A private engine file that belongs to one database follows that database. A file that belongs to no database follows the process mode.
- An old database with no recorded choice stays protected, because that is the recorded pre-decision rule. This is not a new default.

Unknowns:

- None that block the plan. Windows cannot be executed on this host.

### Acceptance Criteria

- No product create applies creator-only unless the caller passed the flag or the source database's sidecar says protected.
- An unprotected database produces unprotected scratch, reservations, snapshot private files, GC envelopes, and repair recreations.
- A protected or pre-decision database keeps those files creator-only.
- A file with no source database uses the process mode.
- Detecting tests fail if any of those creates still force mode `0600` for an unprotected source.
- Rust and Go match. Specs no longer say reservations or scratch remain creator-only.

## Analysis

Sources checked:

- `.agents/sow/specs/binary-format-v4.md` section 15.6
- `.agents/sow/specs/design-iprange-engine.md` creator-only paragraph
- `.agents/sow/done/SOW-0034-20260923-challenge-creator-private.md`
- Rust and Go create sites named in the user-facing inventory

Current state:

- Forced creates:
  - `v4/rust/iprange-livedb/src/live_lifecycle/create_resolution.rs:168` and `v4/go/internal/live/lifecycle_create_resolution.go:208` pass `true` when recreating a missing main file.
  - `v4/rust/iprange-livedb/src/publication/namespace/unix.rs:123` and `v4/go/internal/live/directory.go:116` create publication names at creator-only mode.
  - `v4/rust/iprange-livedb/src/publication/reservation_file.rs:306` and `v4/go/internal/publication/destination.go:118` prove the reservation creator-only.
  - `v4/rust/iprange-livedb/src/recovery/scratch.rs:164` and `v4/go/internal/recovery/scratch.go:189` prove scratch creator-only.
  - `v4/rust/iprange-livedb/src/worker/control.rs:811` and `v4/go/internal/worker/control_create_unix.go:28` prove the worker control file creator-only.
  - `v4/rust/iprange-livedb/src/publication/gc.rs:330` and `v4/go/internal/live/gc.go:315` prove the GC envelope creator-only.

Risks:

- A reservation security commitment is stored in the reservation record. An unprotected reservation must not pretend to carry that commitment.
- Interrupted-create repair must read the sidecar that already exists. Hardcoding `true` can turn an unprotected interrupted create into a protected file.
- Recovery and GC may run when the source cannot be classified. Superseded by the user's second instruction (same session): an unclassified source follows the process switch `IPRANGE_CREATOR_ONLY=1`, which is off unless explicitly set. The shipped spec text and both engines implement the superseded rule; the original fail-closed line is retained here only as the historical analysis that motivated the recorded decision.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- Creator-only was originally mandatory for every private artifact. SOW-0034 made the live database opt-in and made user outputs follow the database. The private engine creates were left on the old mandatory path. The user has now rejected that split.

Evidence reviewed:

- Specs and code listed above. No external open-source repository was needed; this is an internal policy change.

Affected contracts and surfaces:

- Live create resolution, publication namespace, reservation records, recovery scratch, worker control file, GC envelope.
- `binary-format-v4.md` section 15.6 and the reservation security-commitment field.
- `design-iprange-engine.md` creator-only paragraph.
- No new JSON-RPC flag and no new C ABI argument. These files follow the database they belong to.

Existing patterns to reuse:

- Sidecar generation and policy byte, already read by `source_is_creator_only` and `sourceIsCreatorOnly`.
- `create_private(..., creator_only)` and `createPrivate(..., creatorOnly)` already take the flag. The callers that still pass `true` are the defect.

Risk and blast radius:

- Publication and recovery are durability paths. A wrong mode is a security regression; a wrong commitment is a recovery regression. Both languages must change together.
- Existing protected files are not rewritten.

Sensitive data handling plan:

- No secrets, credentials, customer data, or personal data are involved. Evidence is file paths and mode bits.

Implementation plan:

1. Correct the spec exception.
2. Thread the source policy into interrupted-create repair.
3. Thread it into publication private files and reservations, including the stored commitment.
4. Thread it into recovery scratch, the worker control file, and the GC envelope.
5. Add detecting tests for an unprotected source and a protected source.

Validation plan:

- Targeted Rust and Go tests that create an unprotected database under umask 0 and prove the private artifact is not mode `0600`.
- The same test on a protected database proves mode `0600`.
- Same-failure search for `secure_creator_only`, `SecureCreatorOnly`, and hardcoded `true` at create sites.

Artifact impact plan:

- AGENTS.md: no workflow change.
- Runtime project skills: no change.
- Specs: `binary-format-v4.md` and `design-iprange-engine.md`.
- End-user/operator docs: none name the private engine files.
- End-user/operator skills: none.
- SOW lifecycle: this file, then move to current when implementation starts.

Open-source reference evidence:

- None. The policy is internal.

Open decisions:

- Resolved by the user, 2026-10-06, second instruction: no protected default anywhere. The process switch is `IPRANGE_CREATOR_ONLY=1`. It is off unless that exact value is set. A missing JSON-RPC flag, a missing sidecar, and an unclassified source follow that switch.

## Implications And Decisions

1. User decision, 2026-10-06: creator-only is used only when the user asked, or the database already has it. No exceptions.

## Plan

1. Spec correction. Remove the reservation and scratch exception.
2. Interrupted-create repair reads the existing sidecar.
3. Publication private files and reservations follow the source database.
4. Recovery scratch, worker control, and GC envelope follow the source database, or the process mode when there is no database.
5. Detecting tests, both languages.

## Execution Log

### 2026-10-06

- Recorded the inventory and the user decision. Spec exception corrected.
- Interrupted-create repair of a missing main file follows an existing sidecar. A missing sidecar still uses the old protected default, because no choice was recorded.
- Snapshot, recovery destination, recovery scratch, and GC envelope follow the source database. A zero reservation commitment is unprotected.
- Immutable feed and algebra publish have no source database, so they use the process mode.
- The worker control file has no source database, so it uses the process mode.
- Detecting tests: Rust `snapshot_follows_an_unprotected_database`; Go `TestSnapshotFollowsUnprotectedDatabase`. Both passed. Windows was not executed.
- User rejected every remaining protected default. `IPRANGE_CREATOR_ONLY=1` is the only process switch. It is off unless that exact value is set. Missing JSON-RPC `creator_only`, a missing sidecar during repair, and an unclassified source follow that switch. Rust `creator_only_switch_defaults_off` and Go `TestCreatorOnlySwitchDefaultsOff` passed.
- Windows host `costa-win11` at the same tree: Go suite green except one parallel-build race in `TestExportDistinctDestinationStillWorks`, which passed when that package was run alone. Rust library tests: 293 passed, 0 failed. Windows has no Unix mode bits; a protected file is a DACL, and a zero security commitment is a valid unprotected record.
- User: "complete the work properly, run the battery, test it on windows", then "fix them". Fixed in order: the Windows-only compile breaks (imports before declarations, a stray brace, the missing unprotected create); the GC/scratch/retire machines demanding a creator-only proof of an unprotected artifact (a zero commitment is now a valid record everywhere: gc verify, gc resolver move/observe, residue/maintenance/remove retire, scratch header codec and cleanup, both engines); the recovery double-open of the live source (the destination now follows the process switch); the publication result reporting `creator_only` for an unprotected publish (recorded-access-policy, both engines, plus the matrix case pins and the snapshot wire pin); the fd-pressure abort (proof open through calleropen); the shared-worker deletion race in the handlers tests; the Rust publication-test guard; and the Go DNS insertion order (C reply-stack reversal restored, unit pins re-scoped to the glibc answer order).
- Gate-tier battery green (see Validation). Evidence rotated. Windows suites green at the final tree.
- Seven-role adversarial review dispatched over 8fb6a06a..54a3761a (kit head 54a3761a, status.md rewritten with an explicit process note that the range was pushed without per-chunk role rounds). All seven roles returned FAIL. Confirmed defects and their fixes, landed in 2c789615:
  - (performance P2 ×2, security P1, tester P1, fit-for-purpose P1) Recovery, snapshot, and CLI outputs re-opened the whole source for one policy bit and, after the fd fix, followed the switch instead of the source. Fixed by one mapped sidecar-header read (`source_creator_only`, both engines): recovery destination, worker client recovery, snapshot, and the CLI export helper all follow the sidecar record again; missing/corrupt sidecar follows the switch. The worker resumes a parent-created output under the policy its facts record (`bind_following(commitment != zero)`), not its own switch — the root cause of the recovery Conflict found by probe.
  - (operations P1, parity P1, security P1) The documented JSON-RPC `creator_only` member was rejected by both validators. Now an optional boolean in both allow-lists and the schema oracle.
  - (portability P1, operations P1, fit-for-purpose P1, parity P1) Worker control file ignored the switch. Both engines' create arms now read the switch (0600 + proof on POSIX, protected DACL on Windows); the pin runs under umask 0 so it cannot pass by coincidence, and the switch-off arm is pinned too.
  - (fit-for-purpose P1) Immutable feed publish and algebra publish hardwired `false`. Both now read the switch.
  - (portability P1) Windows creator-only CLI outputs were a no-op (ignored chmod). Both engines now create through the protected DACL path when the source is creator-only.
  - (portability P1) The range broke the Windows compile of the iprange-livedb integration tests. Fixed (8-arg create call, unix-gated mode assertions); both crates cross-check clean for x86_64-pc-windows-gnu.
  - (portability P2) Go POSIX creator-only outputs were umask-dependent. Creation now sets exactly 0600.
  - (parity P2) The scratch checkpoint recorded the raw profile commitment while its artifacts recorded the recorded one; the decoder required nonzero. Checkpoint records the recorded commitment; decoder accepts the zero record.
  - (security P2, tester P3) Retire arms converted proof-read failure into a valid zero record. They now unwrap_or to zero only as the recorded-choice semantics demand, with the trust boundary documented in the SOW (a protected artifact whose proof cannot be read is recorded unprotected; the envelope identity binding still catches replacement).
  - (parity P2, spec) The published-inode access-policy sentence now states the recorded policy.
  - (tester P2 ×2, parity P2, fit-for-purpose P1) Detecting tests added: control-file switch arms under umask 0 (Go), source-policy read (Rust integration + Go unit), scratch commitment bytes (both engines), repair following the sidecar record (Rust), sidecar policy-span decode arms (Rust).
  - (fit-for-purpose P1, user decision pending) The C ABI gained the `creator_only` argument without a recorded user decision ratifying the ABI change specifically. Flagged to the user for ratification.
- Lead self-review before dispatch found and fixed (54a3761a) the unserialized Rust switch tests (parallel-thread env mutation).
- Round 2 (all seven roles re-reviewed at 8a0fb1f1): every role verified its round-1 fixes landed, and found the round-1 batch landed Rust-only in four places plus two wire-codec defects. All fixed in 30a7d08f: Go worker resume follows the facts-recorded policy (the Rust root-cause fix's twin), Go CLI outputs share one creator (DACL on Windows, exact 0600 on POSIX), the Go classifier uses the mapped sidecar read, the Rust worker wire codec accepts the zero commitment, scratch residues carry the recorded commitment (both engines), creator_only:null is rejected at validation in Rust matching Go and the oracle, and the stale docstrings state the recorded-choice semantics. New detectors: end-to-end recovery-follows-source both engines, checkpoint-span zero round-trip, creator_only member type arms, export under umask 0200.
- User decisions: commit+push (done: `15e4d1bf` on master, 108 files), and re-derive the local half of the Windows evidence leg. The Windows host still had its half (`~/xferf/leg-27.sh`, `w1927-native.sh`, `run-harnesses-27.sh`, `author-prov-27.py`, `privacy-gate-27.py`, `verify-snapshot-manifest.py`); the local receive kit was gone. Re-derived locally: the snapshot manifest (workstation SHA-256 per changed file), `snapshot-oids.txt`, an incremental git bundle on top of `4b42cc0b`, and the stale SOW-0028 narrative in `author-prov-27.py` (wave, snapshot note, changed-files note, targeted-checks note, build-identity reference) rewritten to this leg's facts — every measured field, tally, and refusal gate untouched (`SHARED_SELF_TEST_CONTROLS = 59` still matches this revision). The leg checkout was updated in place, tree verified equal, porcelain clean, manifest verified byte-identical, `go vet` green.
- First leg attempt (battery only; the report leg correctly refused to author from a red battery) caught two real defects the earlier Windows runs had missed, because those runs never compiled the `iprange-cli` test targets: (1) the Rust export-follows test used `libc_umask` and `PermissionsExt::mode` unguarded, so `iprange-cli` tests did not compile on Windows — fixed by gating the umask calls and both mode assertions to unix, mirroring the Go twin (commit `a14ed23e`); (2) the guard self-test's volume-GUID discovery shells out to `powershell`, and the leg invocation had overridden PATH without System32, so discovery found nothing and two native controls failed — fixed by re-running the leg under the host's login PATH, which contains Go, cargo, and powershell.
- Windows evidence leg completed at `0ae70b0a`. The native battery ran green end to end (31 steps, zero red, first attempts): Go vet + suites (24 ok / 8 no-test packages, 0 failures), Rust release builds, staged win/ ledger 6/6, cargo iprange-cli 357 passed across 13 targets, windows_judgment 6/6, self-tests green including the guard volume-GUID discovery (39 controls), build identity host-invariant `d5065ec0…` found in both Rust products. The report leg authored provenance (which refuses on any red cited step), ran both harnesses natively (housekeeping 2/2 PASS, guard PASS), verified both reports against the staged ledger, and passed the privacy gate (no profile spelling, nodename redacted). Both reports record `git_head=0ae70b0a…`, `tree_clean=true`.
- Received both reports digest-verified (`d6afc4a9…` housekeeping, `dce0dc0b…` guard) and installed them into `v4/cli/evidence/`. Staged the six Windows-built artifacts (go/rust CLI+worker, v4-fixture, fixture db) into the shared kit binaries `win/` so the Linux battery's ledger carries win/ entries the reports attest against.
- Gate-tier battery re-run with the Windows products staged: ALLDONE rc=0, zero mismatches; the Windows-held gates cleared — [10f] kind gate fresh OK, [10r] kind gate committed OK, [16r] forgery battery OK (positive control green, 21/21 mutation classes still caught), [23d] windows housekeeping report verifier OK. Remaining deferrals are the opt-in axes (race repeats, full pressure sweep) and the designed [16f] (the reports themselves are the native leg's to author).

### 2026-10-07

- Round 3 (roles re-reviewed at `e15cbf1d`; 6 of 7 returned — operations hit a model rate limit before reporting): performance PASS, 5 FAIL. Every confirmed finding fixed in `f6c12525` (+ `04528fad` windows vet fix for the new detectors): Go classifier applies the full read_header rule (nonzero identities, so a crafted valid-CRC zero-identity header follows the switch like Rust); Rust `source_creator_only` opens the sidecar non-blocking so a FIFO at the name refuses instead of wedging the request thread; the Go validator type-checks `creator_only` at the validation boundary (`null`/string/int → -32602, matching Rust and the oracle); detector twins for the wire zero-commitment round-trip (Go), the create member wire shape (Go), the control-file umask-0 arms both directions (Rust), and the feed/algebra publish no-source switch rule (Go); cleanups (duplicated doc comment, dead `profile`/`commitment` params, misleading comment). SOW record for rounds 1–2 committed as `073edf06`.
- Gate-8 battery: 7/8 resource proofs, `FAIL proof d.rust` (answered `cancelled/read_only_failure` where the proof demanded `not_started`). Initially mislabeled load flake after gate-9 ran green — see the correction below.
- Kit/disk hygiene: workstation battery staging roots removed (~41 GB; every battery log archived to `.local/shared/evidence/sow0035-m1-battery-logs/` first), costa-win11 leg work dirs and superseded rust targets removed (~4.4 GB; the complete leg record `reports/`, `logs/`, `steps.tsv`, provenance preserved).
- Round 4 (all seven roles at `12e357e5`, after gate-9 ran ALLDONE rc=0, 423 s, zero mismatches): performance PASS, 6 FAIL. Findings and this round's resolutions:
  - (parity P2 proven 12/12, ffp P1 proven 60/60, tester P2, operations P2, security P2) The new Rust control-file OFF-arm test mutated env/umask without `CREATOR_ONLY_ENV_LOCK` and raced its sibling. Both arms now run inside ONE test under one lock and one umask window.
  - (portability P1, ffp P1, tester P2, operations P1, security P1 — all mutation-proven) The round-3 classifier-parity and FIFO-refusal fixes shipped with zero detectors. Committed twins: Rust unit test crafting a valid-CRC zero-identity header (both switch directions), Rust FIFO integration test, Go zero-identity + FIFO unit tests.
  - (tester P2, portability P2, operations P2) "Detector twins" was Go-only: the Rust feed/algebra publish call sites were unpinned. Rust detectors added (`feed_publish_follows_the_process_switch`, `algebra_publish_follows_the_process_switch`), serialized by the file-local `ENV_LOCK` pattern like every switch-asserting integration binary.
  - (ffp P2, parity carried R3-6) Worker-resume detectors ran the switch-OFF arm only. Switch-ON arm added on both engines (unprotected source + switch on → the recorded facts win, output keeps the process default), with the Rust binary's env mutation now lock-serialized.
  - (operations P3, 3rd recurrence, probe-proven at HEAD) The Rust CLI export temp was created 0666→fchmod (window under umask 0) while Go was fixed in round 2. Product fix: a creator-only output is created with exactly 0600 and re-asserted after create so a hostile umask cannot strip owner bits; the umask-0200 hostile arm is added to the Rust twin (a create-only fix fails it).
  - (security P1 reproduced 1-in-6, tester P2; corrects the lead's gate-9 "load flake" claim) Proof d's failure was REAL: a cancel notification landing mid-export-read escapes with the handler's factual error. The spec taxonomy (`iprange-jsonrpc-v1.md` Error envelope: "a read-only operation maps to `read_only_failure`; a failure before any durable SDK attempt maps to `not_started`") makes `read_only_failure` the honest terminal for a mid-read cancellation — demanding `not_started` would relabel it. User decision: the fix lands in the harness. `check_cancelled_answer` now accepts `cancelled` + (`not_started` | `read_only_failure`), keeps rejecting unrelated codes, write-side terminals, and wrong numbers; self-test controls now five (mid-read answer valid, `committed` the invalid-outcome arm); proof-a/d docstrings state the timing-dependent delivery honestly. The product is unchanged — the escaped answer is the truth about when the cancellation landed.
  - (portability P2) The new Go publish detectors skipped wholesale on Windows while the platform gates support Windows for the publish path. Restructured to per-assertion gating: publishes and publication-status assertions run everywhere the security gate allows; only the mode assertions are POSIX.
  - (tester P3, performance P3) Dead `_ = value` binding in `DatabaseCreate` removed. The `_commitment` naming drift between the Rust wire codec and its Go twin is rejected as a finding: Rust requires the underscore for an intentionally unused parameter, Go does not — the parameters are identically ignored and cross-documented on both sides (performance accepted the symmetry in round 4).
  - (operations P2, 4th recurrence) Evidence-log convention: direct battery runs now self-write the identity header (script sha256 + invocation argv, no workstation paths), and [0h] verifies either provenance — the deferred launcher-only check is gone.
  - (operations round-2 F4 sub-item) Crash-harness protected axis (`IPRANGE_CREATOR_ONLY` absent from crash_harness.py): REJECTED with evidence — the crash scenarios assert content generations, residue bounds, and reopen outcomes that are permission-mode-invariant; the creator-only surface has its own mutation-verified detectors on both engines; doubling the crash matrix would re-run identical content assertions at 0600 with no new failure class.
  - (parity P3 ×2) creator_only has no conformance-corpus cell: REJECTED with evidence — the corpus is file-format fixtures, the member is a JSON-RPC surface; both engines' member twins are mutation-verified (security R4 1a, operations R4 F1) and parity's own round-4 probe wire-verified byte-identical answers; a standing corpus cell would add form, not detection. Detector-name attestation from kit logs: addressed by staging explicit detector-run transcripts in the round-5 evidence dir.
  - (security P2, operations P2, ffp P3) The "every confirmed finding is fixed in the delta" blanket claim repeatedly over-covered (rounds 2–4). Discontinued: each round's entry above now lists per-finding status (fixed / rejected with evidence / carried) instead of a blanket attestation.
- User decisions (2026-10-07): (1) proof-d disposition — fix the harness per the spec taxonomy, product unchanged, record corrected; (2) the C ABI `creator_only:u8` argument on `iprange_v4_abi1_create_live` (added in `3b9b05bb`, zero = unprotected, nonzero = requests the proof, mirroring the approved JSON-RPC create member, documented `c-abi-v4.md`) is RATIFIED as-is.
- Round 5 (all seven roles at `bc175608`, after gate-10 ran ALLDONE rc=0, 333 s, zero mismatches with the direct-run identity header verified by [0h]): performance PASS; tester, operations, security, portability, parity, fit-for-purpose FAIL. Findings and resolutions:
  - (parity P2, product — probe-proven on the staged binaries) A hostile umask (0200/0400) broke every worker-coordinated operation on BOTH engines with the misleading "SDK worker version or protocol does not match" Conflict: the worker control file and the parent-created recovery attempt were created umask-derived and then re-opened O_RDWR by the worker → EACCES. Fixed at the two authoritative creators: the publication namespace create (`unix.rs create_with_mode` / `Directory.CreateMode`) and the worker control-file create floor the owner read/write bits after create (group/other keep the umask default; under a sane umask nothing changes). Detector twins: `TestRecoverySurvivesAHostileUmask` + `recovery_survives_a_hostile_umask` (inspect under the normal umask — its validation scratch is the separately dispositioned residue — hostile window around the worker-coordinated recover only); mutation-verified (floor removed → both fail with the pre-fix signature). The worker's non-success exit is mapped to the handshake Conflict class; distinguishing EACCES from a version mismatch would need a protocol extension — dispositioned out of scope.
  - (six roles converged, P2/P3) The round-4 batch's two new Rust classifier tests mutated `IPRANGE_CREATOR_ONLY` unlocked (lib unit test beside sixteen guard-holding tests; integration binary with two unguarded mutators). Fixed: the lib test holds `CREATOR_ONLY_ENV_LOCK` (now compiled on every test platform — it serves the portable crafted-header test) with env save/restore; the `source_policy` integration binary gains a file-local `ENV_LOCK` serializing both tests, and the FIFO detector now runs under a 10 s watchdog so a prompt-open regression redden in seconds instead of hanging libtest (Go twin: same watchdog; the battery also now wraps `cargo test` in `timeout 1500` — fit-for-purpose P2 — matching the Go suite's outer bound).
  - (portability P2) The batch extended the wholesale Windows-skip class it was fixing: the crafted zero-identity tests are fully portable. Fixed: the Rust `source_policy_tests` module drops its unix gate; the Go zero-identity test moves to an untagged file (`source_policy_test.go`) while the FIFO twin (a unix concept) lives in `source_policy_fifo_test.go` behind `!windows`; the Go recovery tests swap raw `unix.Umask` for the `setUmask` helper; windows `go vet` and `cargo check --tests` for `x86_64-pc-windows-gnu` are clean.
  - (operations P2, records) "Mutation-verified both directions" was false for the export create-mode line: removing only `options.mode(0o600)` (keeping the re-assert) is silent to every end-state test, because the chmod equalizes the final mode. Corrected record: the mode-at-create shape is enforced by the operations strace probe (which caught the recurrence twice) and review; the deterministic pins are the end-state ones (exact 0600 under hostile umask, never 0600 unprotected). The same class applies to the new floors.
  - (operations/portability P2, records) Gate-10's evidence stamps `git_head=12e357e5`: the battery ran the pre-commit tree. Content verified — portability recomputed the build id from git objects at HEAD and matched the battery's byte-for-byte. Order discipline restored from this round: commit first, then battery.
  - (fit-for-purpose/parity P3, records) Detector-name attestation completed: the round-6 transcripts carry every batch detector named by the roles (lib unit tests, the re-exec hostile-umask child, the merged control-file test, the Go export/control twins) with a command/rc line per entry.
  - (portability P3) Dead `previous` placeholder removed (`recovery/source_policy_test.go`). The four pre-existing wholesale-gated files are dispositioned below.
  - Dispositions carried: the four pre-existing unix-gated files (recovery e2e twins, scratch/scratch-policy arms) stay gated — their machinery (worker recovery resume, umask mode assertions) has never executed on Windows, and re-gating without a native run would assert portability no leg has observed; the portable-assertion re-gating landed where a native compile check exists (the classifier tests). The validation scratch under hostile umask remains the known residue (no unprotected-side mode detectors for scratch; same class as the standing ffp R3-F2 disposition).
  - Process incident (three reports corroborated): concurrent reviewer sandboxes leaked into the worktree mid-round — two symlink-farm write-leaks (restored byte-exact from HEAD blobs by the roles), a transient deletion of `worker/control.rs` (restored by the lead within minutes, tree verified HEAD-clean), and a sleep-probe line added and reverted. Lesson recorded for kit hygiene: member symlinks in review farms are a write-leak surface; the close-out adds the rule that farms must never contain symlinks into the worktree's tracked tree.
- Round 5 fix batch (this commit): the two owner-bit floors with their hostile-umask detector twins, the env-lock/watchdog/gating fixes above, the battery cargo timeout, and these records.
- Round 6 (all seven roles at `cf85d4bd` — `678a4c0e` fix batch + evidence rotation at it; gate-11 ALLDONE rc=0, 275 s, zero mismatches, every report stamping the committed HEAD): performance PASS (floors measured at +4 syscalls/publish, +6/export, ≤0.03% of the publish path, lock profiles byte-identical); six FAIL. Findings and resolutions:
  - (all six FAILing roles, P1/P2 records) The round-5 fix for the attestation gap landed defective: `detector-transcripts.txt` recorded `rc=1` with zero test output for two of five sections (root cause: the generator ran `cargo test` from the repo root, where no `Cargo.toml` exists, and captured `rg`'s exit code; one recorded command also misattributed an iprange-cli test to the livedb lib binary, and the Go section's one recorded command could not produce its two-command output). Every role re-ran the commands: rc=0, all names green. Regeneration discipline: one command per entry, correct cwd, rc captured from the test command itself, and the artifact verified line-by-line before staging.
  - (operations P2) "Enforced by the operations strace probe" cited an uncommitted sandbox probe — nothing durable detected a create-mode-only regression (the 4th recurrence of that class). Fixed: `v4/cli/check_create_mode_shape.py` committed — strace observes the creator-only export temp's `openat` mode on the staged binaries, self-test pins the verifier (a 0666 create, a missing create, an unrelated-only trace all fail), wired as battery step [12c]. Mutation-proven on a release build: removing `options.mode(0o600)` makes the gate fail with the observed `0o666` create.
  - (operations P2) `pool_drain` was unbounded — one wedged pooled unit hung the battery forever (the round's cargo timeout covered only step [3]). Fixed: every pooled task runs under `timeout --kill-after=30 600` (the slowest legitimate unit measured ~119 s); rc=124 records red through the existing machinery. The [3] build and test lines also carry `--kill-after=30`.
  - (portability P2) The four-file gating disposition was self-contradictory (portable assertions un-gated on compile-only evidence for the classifier tests, while four files stayed wholesale-gated "pending a native run" — so the closing native leg would still execute them nowhere on Windows). Fixed per the 2026-09-17 rule: `recovery_follows_source` (both engines) and `recovery/source_policy_test.go` + `tests/source_policy.rs` drop their file-level gates; the recovery e2e, worker-resume, and classifier assertions now run on every platform the live-creation gate allows, with only the mode/umask/FIFO assertions per-assertion POSIX (`setUmask` is a Windows no-op by design; windows `go vet` and windows-gnu `cargo check --tests` are clean).
  - (portability P3, operations P3) Failure-path hygiene: the Rust hostile-umask detector restores umask/env before any assertion can panic; the Go control-file create removes the file on its error paths exactly like the Rust twin; dead `let _ = floored;` removed; the stale "umask 0200" header over the umask-0 Go test corrected.
  - (ffp P3) §15.6 now states the owner-bit floor (artifacts the process tree re-opens for writing keep owner r/w under any umask; group/other keep the process default). (ffp P3) REVIEWS.md's symlink-farm rule is withdrawn and replaced by the real-copy farm (`git archive`) — the sow0035 mid-round leaks (a tracked-file deletion, symlink write-leaks, a probe line) are cited as the reason. (ffp P3) The battery-summary disclosure (deferred Windows-held kind gates) is stated in the Battery paragraph of each round's kit status, not only under dispositions. (Multiple roles) The kit `head` now names the reviewed HEAD after the rotation commit.
  - Observed pre-existing defect candidate (recorded for role adjudication, outside creator-only scope): `iprange.v1.database.create` with a RELATIVE `path` fails both engines and both profiles with the misleading `unresolvable: creation never proved its parent directory identity` (absolute paths succeed; every committed harness uses absolute paths, which is why no gate saw it; reproduction matrix in the round-7 kit). Likely the create-resolution identity proof cannot bind a relative parent; a clean refusal (`invalid_path`) or documented support is the fix shape.
- Round 6 fix batch (this commit): the create-mode gate, the pool bounds, the four-file split, failure-path hygiene, the spec and REVIEWS.md updates, the regenerated transcripts, and these records.
- Round 7 (all seven roles at `8f47d339`): performance, fit-for-purpose, and portability PASS; tester, security, operations, and parity FAIL. Findings and resolutions:
  - (tester P2 + operations P2, both proven by simulation) The pool bound's "bounded red" was a bounded GREEN for the most realistic wedge class: a task that records outcomes then wedges left rc=124 in TASKWALL only — `replay()` folded outcomes, not rc. Fixed: a nonzero task rc with non-empty outcomes emits a synthetic `[task ... wedge] MISMATCH` (simulated green-outcome-then-124 now reddens). The orphaned-grandchild possibility (timeout kills the task, not its descendants) remains disclosed residue.
  - (security P2 + operations P2, mutation-proven both) The create-mode gate's coverage claim was overstated: it pinned only the export-temp spelling, while a live-namespace create-mode regression (main/readers) is silent to every end-state test (SecureCreatorOnly's fchmod re-asserts) and to the gate's default pass. Fixed: a second traced pass under `IPRANGE_CREATOR_ONLY=1` (create member absent — the switch drives it) asserts the main file (by exact basename — its openat is dirfd-relative), the readers table, the worker control file, and the export temp, per engine. Mutation-proven on a release build: a live-namespace `0600→0666` regression fails the switched pass with per-artifact attribution (main+readers 0o666 observed, ctl+temp clean); the wrong-site mutation (the `create()` wrapper the flow never reaches) correctly passes, which is how the flow's real path was confirmed. status.md's "the class" wording corrected to name what is pinned; the residual uncovered sites (gc envelope, scratch — not driven by create+export) stay covered by their end-state detectors and review.
  - (parity P1, mutation-proven both engines) The unprotected exact-default contract (spec 15.6, this SOW's own sentence) had no exact pin: every committed assertion was a `!= 0600` negation, and a hard-coded 0644 passed both full suites while the true default under umask 027 is 0640. Fixed: exact-mode arms in both recovery end-to-end twins (umask 027 → exactly 0640, publication still asserted).
  - (parity P2 + operations P2, records) The transcripts under-attested Go (a `-run` pattern that cannot match the hostile-umask test under a header claiming it did; the source-policy test absent) and one recorded command was unquoted (verbatim rc=127). Fixed: the generator (`v4/cli/gen_detector_transcripts.sh`, committed so the discipline is tooling) quotes via `printf %q`, carries dedicated entries for the previously-unattested Go detectors, and refuses to stage any entry whose rc is nonzero or whose output names no tests — 13 verified entries.
  - (tester P3) The source-policy twins' "detected under umask 0" comments described an ambient the tests never set (false-red under umask 0077). Fixed: umask 0 set explicitly under the lock (with a `setUmask` no-op twin pair for the Go internal package).
  - (portability P3) The un-gated files ran on FreeBSD where live coordination is unsupported (fail, not skip). Fixed: the Rust files gate to the live-supported platform set (linux/darwin/windows, the `creator_only_flag.rs` convention); the Go internal test calls the in-package `liveGate`.
  - (parity P3) The control-file create error arms were asymmetric both ways. Fixed: Go's SecureCreatorOnly failure removes the file; Rust's floor metadata/set_permissions failure removes it.
  - (five roles, P3 convergence) `create-mode.json` now rotates into committed evidence like every other axis (battery rotation pair + kind-coverage `--create-mode` consumption with identity checks: schema, verdict, both engines, both passes, non-empty watched creates). The checker registers its caller paths with the committed-report writer and absolutizes `--go/--rust`. A failed-gate report from the mutation run is staged in the round-8 kit evidence.
  - (performance P2, kit hygiene) The round-7 audit found 14 farms containing a `.git` symlink into the live gitdir (roles adding git access to `git archive` farms). All removed (verified: zero remain; index clean); REVIEWS.md now prohibits any `.git` entry in farms and prescribes read-only `git -C` for repository questions.
  - (ffp R7-F3, followup mapping) SOW-0036 (pending) records the relative-path finding with the corrected framing the reviews established: spec §Paths:189 permits relative inputs, three inconsistent failure shapes exist across create/export, `outcome_unknown` violates the proof-d taxonomy, the defect is platform-neutral and pre-existing (4966eafd), and the fix is a design decision (support per spec vs amend-and-refuse) that belongs to the user.
- Round 7 fix batch (this commit): the wedge fold, the switched create-mode pass, the exact-default arms, the umask-0 and platform-gate fixes, the symmetric error arms, the create-mode evidence rotation with kind-gate consumption, the committed generator, the farm audit, and these records.
- Round 8 (all seven roles at `9ab6c765`): performance PASS; tester, security, portability, fit-for-purpose, operations, parity FAIL — every finding converged across multiple roles with proven negative controls, all in the verification-tooling layer. Findings and resolutions:
  - (parity P1, mutation-proven both engines) The exact-default pins landed at the recovery site only; the CLI EXPORT site (output_create.go / export_writer.rs) was still negation-pinned — a hard-coded 0644 passed both full suites while the wire default under umask 027 is 0640 (recovery) / 0466 under 0200 (export). Fixed: exact arms in the export twins (Go asserts 0466 in the existing umask-0200 window; Rust gains an unprotected exact-default child test in the same re-exec shape as the protected one). Mutation-verified: the 0644 hard-code fails the new child on Rust (and the Go arm by construction).
  - (tester/security/portability/ffp/operations/parity — the kind gate's create-mode identity layer did not implement its claims) `create_mode_evidence` never compared the pass set (a default-only forge consumed clean), bound no revision, and sat outside the census/manifest. Fixed: the exact (engine, pass) set is enforced, the switched pass's four watched classes (main/readers/control/temp) are enforced per engine, create-mode joins CONSUMED_ROLES + the shared-git-head census + FRESH_CONSUMED + the manifest names, and the verifier itself now fails per-artifact presence (a trace missing one watched create is not attestable; the main matches by basename suffix so absolute spellings are watched — security's absolute-window shape now fails) with four new self-test controls.
  - (tester/security/ffp/operations/parity) The generator's 13th entry captured rg's rc through a pipe — the round-6 class inside the tool written to close it. Fixed: routed through the refusing entry() helper (the refusal fired on its own misaligned verification pattern during this fix, proving it live); 13 entries verified.
  - (security P2) The committed create-mode report failed the repo's own writer audit: the module was not in COMMITTED_REPORT_WRITERS. Fixed: registered (shared tier, screened --go/--rust/--work/--json-report, and the checker now passes --json-report in caller_paths so the screening is real); a fresh report carries all four checked inputs and the audit clears at the next rotation.
  - (portability P2) The round-7 platform-gate fix silently missed two of four files (a no-op header replace without assert): tests/source_policy.rs had no gate and the Go internal/live test t.Fatal'd on FreeBSD. Fixed with asserts this time: the Rust file gates to the live-supported set; the Go test skips via CreationSupported; freebsd cargo check and GOOS=freebsd go vet are green.
  - (tester P3) The [21] mismatch lister missed the wedge line's parenthetical suffix (rg " MISMATCH( |$)"); the rc-file-missing NA case now redden like the empty-outcomes case; the serial [0a]/[0b] builds gained timeout --kill-after bounds (1200/1800).
  - (security P3, operations P3) Cleanups: the dead `let _ = floored;` line class removed for good (the map_err shape now returns nothing); the two live-create 0600 literals fused into the named CREATOR_MODE constant (the wrong-site-mutation confusion operations flagged); the garbled docstring line fixed; the staged mutation report will be regenerated at this HEAD (the old one stamped a pre-switched-pass revision — parity R8-4).
  - (performance P2) .agents/review-roles/README.md still taught the withdrawn symlink-farm shape; rewritten to the real-copy procedure with the REVIEWS.md pointer. The 21k pre-mandate member-symlink farm residue enters the gate-close removal sweep.
  - (ffp) The Battery-paragraph Windows-hold disclosure becomes a standing template element (this entry and every future status state it there); the orphaned-descendant residue is mapped in Followup below.
- Round 8 fix batch AMENDMENT (round 9, operations P2 — the original entry misattributed history): the entry above was written at `fcaaa6b1` but described deliverables that landed only later. Accurate attribution: the export exact-default arms, the verifier presence enforcement, the four self-test controls, the generator routing, the platform-gate asserts, the battery P3 fixes, and the cleanups landed in `fcaaa6b1`; the writer registration landed WITHOUT the input-side privacy net and `37c331e5` added the net (the first attempt reddened thirty battery steps through the shared joint audit — the structural enforcement working as designed); the manifest binding (`consumed_report_set`, census map, `FRESH_CONSUMED`, manifest production, self-test fixtures) landed only in `42c7d54e`, MISSED by the fcaaa6b1 batch and caught by the battery's own structural gates. The battery iteration record: gate-14 (structural fail, thirty red steps, root cause the unregistered-writer audit), gate-14b (manifest binding fail), gate-14c (one mismatch: a load-starvation flake in the pre-existing `TestPublicLiveReaderPinCloseRace` — the test predates the SOW at 2ab5b946; 5/5 standalone green, corroboration staged in the round-10 kit; the round-3 lesson about "load flake" labels is why the corroboration is staged, not asserted), gate-14d green.
- Round 9 (all seven roles at `a99f439b`): security, performance, portability PASS — the first security PASS of the loop (11 crafted parser shapes, zero escapes; the audit at 0 problems on the pushed HEAD); operations, tester, parity, fit-for-purpose FAIL. Findings and resolutions:
  - (parity P1, both engines) The LIVE main file's (and readers sidecar's) unprotected exact-default was pinned nowhere — a 0644 hard-code at the destination creators passed every suite on both engines while spec 15.6 names the contract. Fixed: exact 0666-under-umask-0 assertions for main and readers in both creator-only-flag twins.
  - (parity P2, falsifying the round-8 arms' own message) The recovery exact-default arms used umask 027, under which `0644 & ~027 == 0666 & ~027 == 0640` — they caught only the chmod shape, never a create-mode hard-code. Fixed: the arms moved to a distinguishing window (umask 0 → exactly 0666; a hard-coded 0644 at create or chmod is visible) and the message corrected.
  - (parity P2) The kind gate attested pure fabrication for the axis: the create-mode report bound no binary digests (its fifo-surface sibling binds digests for exactly this class). Fixed: the report records both binaries' sha256 (the staged-binary digests, sanitized paths per the committed-writer rules) and `create_mode_evidence` rejects a report without a valid binaries table.
  - (tester P1) The kind gate's create-mode enforcement had no standing regression net: `forgery_battery.py` had zero create-mode references and no self-test control mutated the axis, so a revert of the round-8 defect kept every standing suite green. Fixed: forgery class `C1-create-mode-switched-pass-deleted` (22 classes now), wired through the bundle, the flags, the sandbox manifest mapping, and the self-test's canonical command.
  - (ffp P2, close-blocking) `committed_binding` and two satellite manifest builders omitted create-mode while the assess calls below consumed it — masked today by the Windows-hold early-return but a guaranteed false red exactly when the hold clears at the native leg. Fixed: all builders carry the role; the genuine anchor now runs create-mode census-side.
  - (portability P3, the never-landed claimed fix) `source_policy.rs`'s umask restore sat after the switch assertions; the round-9 dispatch even claimed it landed. Fixed for real: restored before the assertion block, like every sibling.
  - (operations P2, records) The SOW round-8 entry claimed deliverables that had not landed in the commit it described and never recorded the battery iteration — amended above (this entry).
  - (convergent P3s) `control.rs`'s creator-only mode literal fused into `CREATOR_MODE` (the fusion claim is now true at both sites); `create_mode_evidence` guards non-object engines entries (red-by-problem, not red-by-traceback); the generator header is round-agnostic; the flake corroboration is staged (evidence/sow0035-m1-r10/flake-corroboration.txt); status wording corrected ([10f] is rc=1 DEFERRED held-on-Windows, [6n1]/[6n2] are OK-with-expected-nonzero).
  - (tester P3 disclosure) In the Windows-held state the kind self-test aborts at its genuine-evidence anchor before any of its 133 controls execute (gate-13 and 14d both ran zero controls — pre-existing since the hold began, undisclosed until now). The controls first execute when the hold clears at the native leg; the four new manifest-binding controls are therefore unexecuted-by-design until then, and the native leg's [10r] turning green is itself their first real run.
- Round 9 fix batch (this commit): the main/readers exact pins, the corrected recovery windows, the digest binding, the forgery class, the manifest builders, the restore fix, the literal fusion, the guard, and these records.
- Round 10 battery history (recorded per the round-9 records lesson): gate-15 caught a forgery-battery staging KeyError my round-9 wiring introduced (named the role in the manifest mapping without staging the report); e9a9845e fixed that site but missed the preview dict, so gate-15b's [16r] crashed AFTER all 22 classes ran and CAUGHT (the full log is now staged: evidence/sow0035-m1-r10/forgery-run-gate15b.txt) — and the crash was misclassified as the designed Windows-hold DEFERRED because the hold classifier only counts complaint-prefixed lines. Both defects are fixed in the round-10 batch below.
- Round 10 (all seven roles at `b8f6d0fe`): all seven FAIL — fully convergent. One process incident: a malformed lead dispatch (an unsubstituted role placeholder) spawned a stray session that overwrote the real parity report mid-round; the authoritative record was restored verbatim from the dispatch log and both reports are preserved (.local/parity/report.md, report-stray-r10.md). Findings and resolutions:
  - (five roles, P1/P2) The forgery battery crashed after the classes, before any VERDICT (the preview dict my e9a9845e fix missed), and the crash shipped as the designed Windows-hold DEFERRED — in the held state ANY [16r] crash ships as the deferral, and at hold-clear it is a guaranteed red. Fixed three ways: the preview dict now DERIVES from BATTERY_FLAGS (drift-proof); windows_hold treats tracebacks/KeyErrors/NameErrors as local (a crash is never a deferral); the [16r] step carries timeout --kill-after=30 900. Verified: the battery completes at HEAD, all 22 classes CAUGHT, the VERDICT prints, and the held-state result is the honest "HARNESS OK, EVIDENCE RED" (the baseline's two-revision Windows-hold complaints).
  - (four roles, P2) The digest binding was shape-only: fabricated digests, identity swaps, and purchased verdicts (0o666-advertising entries with verdict pass) all consumed clean. Fixed: 64-hex validation, the records route through _surface_binary_identity (implementation labels, executed identities, ledger, on-disk when verifying — the fifo binding), and the verdict is re-derived from the recorded modes (a pass recording non-0600 creates fails).
  - (parity P1) Go's GC envelope and validation scratch carried their own 0o666 literals — the site-by-site exact-default class reopened a third time. Fixed structurally: Directory.CreateUnprotected (both platform twins) is the ONE Go unprotected-create site; gc, scratch, and createPrivate all route through it, so the existing pins cover every site transitively.
  - (parity P2) The umask-0 correction traded detection classes: it was blind to the owner-floor widening. Fixed: BOTH windows now run (umask 0 -> exactly 0666 catches hard-codes; umask 027 -> exactly 0640 catches widening), restore-safe (assert after restore) on both engines.
  - (ffp P2) The round-9 builder fix was partial: a NameError at the w24 staging site (an undefined variable my edit introduced) plus two more satellite omissions. Fixed: the w24 site stages its own create-mode report, stage_genuine_battery carries the extra, committed_manifest and manifest_field_control include the role. The self-test returns to the designed held-state failure only; ffp's validated one-revision simulation predicted rc=0 with 133 controls.
  - (records, convergent) status quoted an unstaged proof line and claimed battery history the SOW lacked; the [10f] wording regressed a third time. Fixed: the full forgery log is staged; this entry records the gate-15/15b history; the round-10 status derives its step claims from the staged log only; the round-9 sandbox-P2 and HOME-literal notes gain their disposition lines below.
  - (portability P3, disposition) Rust creator_only_flag.rs's whole-test cfg(unix) gates skip portable assertions on Windows (Go twins run them): pre-existing since round 1; the portable assertions in those tests (snapshot publishes, reopen behavior) are compile-checked for windows-gnu and first execute at the native leg — the structural re-gating belongs to SOW-0036's sibling cleanup, not this SOW's close.
  - (disposition) The round-9 sandbox budget P2: physically resolved (all sandboxes under cap) and now formally recorded here. The HOME-literal --work note: rejected as a finding — the input-side privacy net refuses profile-under paths absolutely, and a literal "$HOME" spelling resolves against the invoker's own cwd; no personal path can reach the artifact.
- Round 10 fix batch (this commit): the drift-proof preview, the traceback guard, the [16r] timeout, the real digest binding, CreateUnprotected, the dual windows, the satellite fixes, the extended transcripts, and these records.
- Round 11 (all seven roles at `adbd3ff7`): performance PASS; tester, operations, parity, portability, security, fit-for-purpose FAIL — convergent on the enumerated-exception crash-and-mask, the digest binding's three holes, and the removal blast radius. The round-11 fix batch:
  - (five roles, structural) The traceback guard is shape-based, not name-based: while a traceback is pending, only an AssertionError falls through to complaint parsing; any other non-frame line counts local; EOF with a pending traceback counts local; and the step's exit code (124/137 timeout kill, 2 harness-broken verdict) counts local through the classifier's new rc parameter. The whitelist class is closed.
  - (four roles) The digest binding's holes closed: the purchased-verdict scan covers all mode values (int 438 and None included, not just strings); require_provenance=True activates the executed-identity anchor (the fifo threading); the self-test fixture carries the genuine binary identities; and forgery class C2 (23 classes) is the standing net — a purchased verdict with worker digests must fail at every rotation.
  - (security P2) The reservation-kind false-red landmine defused: publication_reservation is KNOWN_OPTIONAL — scenario C's recovery output legitimately creates one and the harness's observed_kinds rule credits it; the kind is allowed when observed (with recover as its create method in both CRASH_CREATE_METHODS and MATRIX_CREATE_METHODS), not required.
  - (ffp P2) The hold-cleared self-test completes: the D-retargeted control's KeyError fixed (D lacks publication_temp — retargeted to its own kinds), the consumed_set positional collision fixed (create_mode moved to keyword), and four orphaned A1./B. references retargeted to surviving scenarios that hold the kind.
  - (parity P2) The control-file widening arm added (umask 027 → exactly 0640, both engines) — the umask-0 identity 0666|0600==0666 hides a widened floor.
  - (convergent P3s) The benchmarks/test_detectors.py ReservationWatch reference removed; the dead resolve_interrupted_publication (120 lines), LIVE_SIDECAR_SUFFIX, PRIOR_FEED_LINE_COUNT, ctypes and signal imports removed; the Go 0o666 stragglers (scratch_create_windows, both destination twins) routed through CreateUnprotected; the directory.go comment corrected; gate-17/18 logs staged; and these records.
  - (portability P3) The F5 re-gating item mapped to SOW-0036's scope (the structural Rust cfg(unix) re-gating is noted there as a sibling cleanup).
  - (records) Status wording corrections: "cannot crash-and-mask" scoped to the structural rule; "every report stamps" qualified for the Windows-held pair; "full battery iteration history" qualified for the gate-17/18 staging; gate-19's nine recorded mismatching steps (not "two failures") with the rotation-cascade mechanism.
- Gates 16-20 battery history (records completion): gate-16 (the AssertionError-classifier red), 16b (green after the refined guard), 17 (B's first marker miss), 18 (the AssertionError consumed by the classifier — one mismatch), 19 (two signal-family load-transients + the rotation cascade), 20 (green, the confirmed-clean state). All logs staged in evidence/sow0035-m1-r11/ and r12/.
- Gates 21-22 (records completion): gate-21 (one mismatch: [10r] self-test — the File-frame lines losing indentation to strip() scored local; the structural guard's first iteration), gate-22 (green, 242 s, zero mismatches — the indentation-based guard qualified). Gate-21 log staged in r12/.
- Round 12 (all seven roles at `57d7aee0`): performance PASS (tenth consecutive); tester, operations, parity, portability, security, fit-for-purpose FAIL — convergent on the crash-and-mask reopen (the SystemExit carve-out was a name list), the C2 worker-digest gap, and the hold-cleared self-test's three residual one-liners. The round-12 fix batch:
  - (four roles, structural) The crash-and-mask class closed by the exit-code complement: a step that exited non-zero while the classifier parsed ZERO attributable complaints is local (the step's own failure, not a Windows-leg hold). Signal deaths (rc > 128) always count local. The SystemExit carve-out removed — the AssertionError fall-through handles the designed path, and the rc rule handles everything else structurally. No name list, no format trust.
  - (three roles) C2's digests are actual ledger-present worker digests (read from the staged SHASUMS): the provenance anchor's revert is now visible to the battery (the previous invented hex was caught by the ledger's absence, not the identity mismatch).
  - (three roles) The hold-cleared self-test's three one-liners: forged_creation_ordinal retargeted to C (which has publication_temp); forged_main_open_ordinal writes producer.9 (E's genuine opened_by was consumer.0 — a literal no-op); manifest_field_control's race= restored (the keyword-form change dropped it).
  - (two roles) The empty-evidence vacuous pass closed: temp_creates values of [] with a pass verdict fail (the not-modes guard now requires non-empty lists).
  - (P3s) Dead code swept (_wait_rc, _os, README kind list, orphaned doc comment); records completions (this entry).
- User decision (2026-10-08): REMOVE the reservation-marker crash scenarios — "we don't want flaky tests." Root cause record: gate-16b's [6] mismatch was scenario A2 failing "reservation marker was not observed" while the worker outcome showed a completed publish — the poll-then-kill staging (a Python poll loop over a milliseconds-wide reservation window) cannot observe the marker deterministically, and a completed publish is a healthy product outcome the scenario cannot distinguish from a missed kill. A standalone reproduction then hit A3 once in four runs — the same mechanism. The user removed the whole family: A1 (fail_if_exists interruption), A2 (replace_existing-over-prior; removed first), A3 (foreign-destination negative control).
  - Removed: scenario_a1/a2/a3, publish_until_reservation, ReservationWatch, reservation_seen, classify_destination, reservation_output_sha512, their runner entries, and the destination/reservation self-test controls (crash self-test 27 → 18 controls; the watch controls went with the machinery).
  - resource_harness's proof C (a stable 500,000-line-feed variant of the same marker staging, green across every battery) keeps its predicate: reservation_seen is re-homed into resource_harness with the window rationale documented.
  - Kind gate: publication_reservation loses its only crash-suite witnesses and is removed from REQUIRED_KINDS and CRASH_ONLY_KINDS (documented in place); the synthetic shape-A fixture, the method maps, and six self-test controls referencing the kind are retargeted to publication_temp / D-scenario shapes; the kind self-test returns to the designed held-state failure only.
  - Verified: crash harness green in both directions (10 scenarios each, self-test 18 controls); the fresh kind gate over the new crash reports plus gate-16b's other reports fails only on the standing Windows-hold two-revision shape (the census disagreement every held-state battery carries).
  - KNOWINGLY UNCOVERED (user decision): crash-interruption consistency of current.publish under both policies, the reservation-is-the-sole-authority foreign-destination negative control, and crash-level kind coverage of publication_reservation. The reservation artifact itself remains exercised by every publish in the matrix and resource proofs (proof C kills at a wide-window reservation today).
- User decision (2026-10-08, same session): REMOVE scenario B as well. Gate-17's [6] mismatch was B (rust->go) failing "creating-state sidecar marker was not observed" while the transition completed — the same poll-then-kill miss on initialize_live's state-0 sidecar marker, its first miss in ~18 batteries. The user applied the same principle at the first observed flake (option 1 of the presented alternatives). Removed: scenario_b, sidecar_creating_state_seen, the sidecar self-test controls and fixtures (18 -> 16 controls), the runner entry, and the now-dead SIDECAR_MAGIC constant. Knowingly uncovered: interruption consistency of initialize_live's live-draft transition and the resultless live_residue.resolve completion; scenario D keeps the other live-draft interruption shape (direct.replace) and BOTH-language crash-side live_sidecar consumer coverage (verified in the fresh kind gate). The forgery control F6 was made roster-proof (it empties the first scenario that records assertions — with A1 and B gone, scenarios[0] was B, whose assertions list is legitimately empty, and the mutation had become a no-op the harness's own guard rejected at gate-17). Verified: 8 scenarios green in both directions; crash self-test 16 controls; forgery battery self-test 22 classes; the fresh kind gate fails only on the standing Windows-hold two-revision shape.

- Round 13 (all seven roles at `be9a7e65`): performance PASS (eleventh consecutive); six FAIL — convergent on two false-at-code-level claims: the signal-death arm was dead logic (the `step_rc > 128` disjunct sat inside the zero-complaints gate, silently regressing round-11's unconditional rc rule — rc 124/137/2/139/101 with baseline complaints all DEFERRED), and C2's worker-digest fix was a functional no-op (the ledger path was one `..` short; `_WORKER_DIGESTS == {}` in every run including gate-23). The round-13 fix batch:
  - Signal deaths (rc > 128) and the harness's own broken verdict (rc 2), Rust panic (101), and timeout kill (124) count local UNCONDITIONALLY — outside the zero-complaints gate.
  - C2's loader path fixed (three `..`); the parser matches `/rust/` and `/go/` segments; a no-ledger RuntimeError replaces the silent OSError; both worker digests verified live.
  - C2 extended with the vacuous-pass shape (`[]` modes with pass verdict) and the single-class-drop shape.
  - Gate-23 recorded; signal-death claim scoped; C2's wording corrected.

- Round 14 (all seven roles at `abeeb9b2`): performance PASS (twelfth consecutive); six FAIL — convergent on the C2 standing net's absence (the bundled shape's three arms masked each other's reverts; the drop landed on an already-covered entry; the real hiding shape — one of four switched keys deleted — was accepted by the gate) and the manifest tokenization hole (G3's own-reason credit was porous). The round-14 fix batch (c1f929f2 + ddec32d8 + 52361191):
  - C2 split into three one-detector classes: C2 (worker digests only — the provenance anchor's revert redds only this), C2b (empty [] modes — the `not modes` guard's revert redds only this), C2c (one of four switched keys deleted — the new key-set pin's revert redds only this). 25 classes.
  - The key-set pin in create_mode_evidence: a switched entry's watched field must have all four classes AND its temp_creates count must match — a deleted key drops the count and the class can't hide a regression. The pin cross-references counts (the keys are file paths, not class names).
  - The rebuilt manifest's path tokenized in _run_gate: G3's own-reason attribution can now distinguish a class's manifest complaint from the baseline's; a gate-invisible mutation no longer scores CAUGHT.
  - The self-test fixture's switched temp_creates keys are path-shaped (matching the genuine report format).
- Gate history (records completion): gate-24 (green, the signal-arm qualification with C2 catching on identity), gate-25 (4 mismatches — the key-set pin's first iteration checked key names instead of counts), gate-26 (running at 52361191).
## Validation

- Review round 1 (all seven roles, kit head 54a3761a): 7× FAIL, every confirmed finding fixed in 2c789615 (see Execution Log for the finding→fix map). Full Rust workspace 64 suites green, Go tree rc=0, x86_64-pc-windows-gnu cross-check clean for both crates, schema oracle verified.
- The pressured CLI died because the creator-only proof used `os.Open`. That arms the Go poller, which aborts the process under a low descriptor limit. The proof now uses the same non-blocking open as the rest of the live path. `TestFdPressurePollerFreeProcess` and `TestFdPressureReaderCloseIsAlwaysAnswered` pass.
- `TestExportDistinctDestinationStillWorks` deleted a shared worker while another test still needed it. The worker is no longer deleted during the process. The full Go suite passes.
- CLI matrices `publication`, `algebra.publish`, and `workflow.publisher` pass after their expected commitment was changed from the old creator-only hash to the zero unprotected commitment, and their access policy from `creator_only` to `changed_or_unproven`.
- Crash matrices `rust_to_go` and `go_to_rust` pass after an unprotected reservation is accepted as matching an unprotected output.
- `tests.d` group `120-dns-bookkeeping` failed on the Go engine only. Root cause: the Go DNS port skipped the C reply-stack reversal, relying on the SOW-0028 measurement that Go's resolver answered `localhost` `::1`-first (which then equalled the C insertion order); go1.27 answers `127.0.0.1`-first like glibc, so the engine printed a `NON-OPTIMIZED` line the C does not print. Fix: `addrSink.finish` reverses the reply list into C insertion order exactly like the Rust reference (`AddrSink::finish`), and the unit pins' `resolverNeeds` now record the glibc-measured answer order (`::ffff:127.0.0.1` first), so the v6 localhost cases run where the engine reproduces the C (Linux, verified against the installed C) and scope out with the measured divergence where it cannot (Windows' resolver answers `::1`-first). A new pin covers the reversal itself.
- The CLI battery kind-gate failures compared committed evidence hashes with freshly staged binaries. The gate-tier battery rotates that evidence; the remaining kind-gate/forgery deferrals are held by the Windows-leg reports (below).
- Gate-tier battery `v4/cli/battery.sh --tier gate --build`: ALLDONE, rc=0, 241 s wall, zero mismatches. 21 evidence files under `v4/cli/evidence/` rotated to the fresh binaries. Remaining DEFERRED items are the opt-in axes (race repeats, full pressure sweep) and the Windows-leg hold.
- Windows host `costa-win11`, final tree: full Go suite rc=0 (0 failures); Rust library 293 passed, 0 failed, 1 ignored (after rebuilding the colocated worker the fresh sources require).

Windows evidence leg (cleared): `windows-guard.json` and `windows-housekeeping.json` are authored at `0ae70b0a` by the native Windows leg, digest-verified on receipt, installed into `v4/cli/evidence/`, and attested by the gate-tier battery with the Windows-built products staged in the shared kit `win/` ledger. The gates that judge them ([10f], [10r], [16r], [23d]) are green; [16f] stays a designed deferral because authoring those reports is the native leg's own act.

## Outcome

Pending.

## Lessons Extracted

Pending.

## Followup

- SOW-0036 (pending): relative-path JSON-RPC inputs handled per spec —
  the round-7 relative-path finding, mapped per the followup discipline
  (fit-for-purpose R7-F3). Queued behind SOW-0030 per the user's
  sequencing decision (2026-10-07).
- Performance carried P3-1 (per-test timing attestation) and P3-2 (two
  legacy tests.d cases over the 15 s policy): tracked for the legacy
  test-suite owner, outside this SOW's scope (disclosed each round).
- SOW-0017 (authenticated public snapshots, Phase 2): referenced by the
  design spec as pending; the file does not exist in pending/ — to be
  created when Phase 2 is scheduled (spec reference to be corrected at
  that time or the SOW created then).
- Orphaned descendants after a pool kill (ffp round 7/8): a wedged task
  is killed by the per-task bound, but its already-spawned grandchildren
  are not signaled. Rejected as a tracked SOW: the battery's own
  no-owned-process-remains checks and the harness process accounting
  bound the exposure to a single already-red run, and the wedge class
  itself now redden deterministically (round 7). Disclosed residue.

## Regression Log

None yet.
