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

None yet.

## Regression Log

None yet.
