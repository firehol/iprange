# SOW-0026 - v4 platform completion: Windows surface, authorized scratch, FreeBSD durable publication, Rust durability-shape pins

## Lead review rules (user decision, 2026-08-21; overrides SWARM.md)

These rules govern this SOW's review process and override the shared
swarm guide. They are placed at the top so they are re-read after every
compaction. SWARM.md is intentionally not updated: this SOW is the
authority for its own review process.

1. ONE review level only. No level-2 gate, no other models: only five
   reviewers, all on the lead's own model, each holding one disjoint
   aspect of the milestone scope, adversarial mode, running with the
   final-review skill.
2. Spawn all five once at first use; every later round is a message to
   the same agents. Never respawn between rounds; restart them only
   between milestones.
3. The milestone closes only when all five reviewers report no P0-P2
   findings. No reviewer is ever replaced by another model.
4. The Rust implementation is the mandatory baseline for ALL five
   aspects. The two implementations must be identical in logic,
   operation order, system calls, errors, and philosophy; the smallest
   divergence in how a thing is done is a performance and correctness
   red flag. Go must follow exactly what Rust does, while expressing
   that logic in natural, maintainer-grade Go.
5. Aspect split (all aspects are judged against the Rust baseline):
   (1) Rust parity - exact logic, operation order, syscalls, errors,
   mmap-only, memory safety, lifetime, durability/crash semantics;
   (2) Go idioms - identical logic and structure, but written as
   natural Go, never foreign or translated-looking;
   (3) absolute performance - the most performant form possible; even
   a single unnecessary branch, copy, or allocation in the hot path is
   a defect;
   (4) wire format and integrity - on-disk bytes, locking, Go<->Rust
   cross-open interoperability;
   (5) APIs, docs, records - public API, errors, documentation, and
   SOW records in sync with Rust.

## Status

Status: in-progress

### Status (2026-08-25) - milestone-5 work packages A and B

- Work package A (sweep allocation fix) delivered at `ff3435d` and completed in the gate fix round below: `format.InspectLayout`, the page header, and the cell iterator travel by value; the recovery tree scan keys and cursors are concrete generic values (no interface boxing, no per-record allocation); and the refusal envelopes copy the page number only inside the cold arms. The recovery tree scan allocates 0 objects per run (pin 2138 -> 0), the membership blob scan allocates exactly one fixed scanner object per run (pin 927 -> 1), and the live validation sweep costs only the fixed one-time open machinery (~103 per Validate call; pin bound tightened 1024 -> 200). Full battery green.
- Work package B (FreeBSD durable immutable publication, pure-Go ACL machine) DELIVERED, committed `b91f3a7` + host-proof fixes `9415a80`/`42bd7c4`, host-proven on the freebsd VM (FreeBSD 14.1-RELEASE amd64, ZFS acltype=nfsv4): implemented in `internal/security` on top of `ff3435d`:
  - `acl_freebsd_algo.go`: ABI-exact kernel `struct acl`/`struct acl_entry` plus exact translations of libc `acl_strip_np`, `acl_is_trivial_np`, `acl_calc_mask`, and kernel `acl_nfs4_sync_mode_from_acl` / `acl_nfs4_trivial_from_mode_libc` (PSARC/2010/029 and canonical-six draft forms).
  - `acl_freebsd_sys.go` (freebsd build tag): raw `__acl_get_fd`/`__acl_set_fd` syscalls (349/350), the libc `fpathconf(_PC_ACL_NFS4)` brand probe (ZFS NFSv4 vs POSIX.1e), libc `_posix1e_acl_sort` presort for the POSIX set arm, and the Rust error classes (get EOPNOTSUPP -> CodeDurabilityUnsupported, other get/set failures -> CodeIO with the Rust operation labels).
  - `acl_freebsd_algo_test.go`: host-measured vectors (FreeBSD 14.1-RELEASE, ZFS acltype=nfsv4) for the PSARC forms of eight modes, the draft form of 0600, sync-mode round trips, triviality decisions, POSIX strip/mask-recalc/trivial, and the POSIX sort.
  - `acl_freebsd_sys_test.go` (freebsd build tag): live kernel round trip - fresh 0600 file proves; a named-entry ACL fails the proof, strips to trivial, and proves again (NFSv4 arm); the POSIX arm pins the masked-ACL behavior; devfs reports the EOPNOTSUPP class.
  - `CreatorOnlySupported()` flips true on freebsd; `acl_other.go` drops the freebsd stub; the platform gate skip texts and "linux-only" comments updated across security/live/publication/recovery; `destination_create_linux_test.go` became `destination_create_posix_test.go` (linux || freebsd); `securityNamespaceError` recognizes the freebsd CodeIO operation labels as IoAt.
  - The freebsd live/writer package gates stay skipped by design: live creation needs the sidecar byte-range lock machine (`lock_refuse.go`; the Rust authority on FreeBSD ships only the whole-file flock lifetime lock in `live_lock.rs freebsd_file_lock`, no OFD byte-range machine) and writer fixtures create database files through `mapping.Create` (`mapping.CoordinationSupported` stays false) - the publication/recovery/validation suites are the newly green freebsd surface.
  - Host proof (freebsd VM, `~/src/iprange` at HEAD): plain `go test ./...` GREEN (root suite 151 s, recovery 31 s, security, publication, live/writer skip honestly), `-tags v4work` GREEN, race+checkptr on security/live/publication/recovery GREEN, vet clean. The zero-alloc publish-set pin passes on freebsd: the ACL struct stays on the stack (escape-verified), and the only prior 4096-bucket growth (694 -> 2265) is gone.
  - Host-proof fixes along the way: the freebsd ACL get now fills a caller-provided buffer (no page-sized heap object on the proof paths); recovery/snapshot fixtures build over-file like production so the recovery, publication, and snapshot suites run on freebsd; the live-pair algebra property test, the replacement-policy test, and the structured transaction round trip gained the exact platform gates (live creation, atomic name exchange).
  - Local battery green at every commit: plain, v4work, race+checkptr, vet, cross-builds linux/darwin/freebsd/windows amd64+arm64 and freebsd riscv64.
Sub-state: milestone-5 gate in progress (user decision 2026-08-25). Work packages A (sweep allocation fix) and B (FreeBSD durable immutable publication, pure-Go ACL machine) are delivered and committed; the five-reviewer gate on the slice found three P0-P2 defects (POSIX strip mask recalculation, per-page and per-record sweep allocations, stale gate records), all fixed and re-verified in the gate fix round below. The remaining items (Windows live/publication surface, authorized recovery scratch + external sort, Rust XNU-16K flush-shape pin) stay tracked below and become work packages in later rounds of this SOW.

### Status (2026-08-25, milestone-5 gate fix round)

- The five-reviewer gate on the A+B slice (HEAD `a07df5f`) returned three FAIL verdicts: Hubble (Rust parity) P1 - the POSIX strip mask recalculation diverged from libc `acl_calc_mask`; Hypatia (performance) P2-1 - one per-page `PageHeader` heap object in the recovery/validation sweeps, P2-2 - per-record `treeKey any` interface boxing in the recovery tree scan, P2-3 - 4088-byte by-value returns in the FreeBSD ACL algorithms; Faraday (records) P2-1 - stale Pre-Implementation-Gate text and an unresolved decision that the user had already resolved. Einstein (Go idioms) and Averroes (wire format) passed with P3s only.
- P1 fixed: `fbsdPOSIXStrip` recalculates the mask over the surviving base entries of the already-stripped ACL (libc `acl_calc_mask` runs on the stripped ACL), not over the original named entries; the two host-measured vector tests now pin the libc-verified `r` mask.
- P2-1 fixed for the whole class, not only the four named sites: the tree and range scans, the validation tree/range/blob/structure/bitmap/membership/structure-dictionary/catalog/retirement walkers, and the graph-read helpers all return the header by value and copy the page number only inside cold refusal arms; `format.LayoutInspection.Cells` returns the iterator by value. Pins: recovery tree scan 0 objects/run, membership blob scan 1 fixed object/run (the scanner), live validation sweep ~103 fixed one-time machinery objects (bound 200).
- P2-2 fixed: the recovery tree scan is generic over the key type (`treeCodec[K]`, `treeKeyOption[K]`), so the membership-ID and catalog-index keys are concrete `uint32` values and the catalog-name keys are `[]byte` slices into the page; no interface boxing exists anywhere in the walk.
- P2-3 fixed: `fbsdStrip`, `fbsdPOSIXStrip`, and `fbsdNFS4TrivialFromMode` write into caller-provided buffers and `fbsdNFS4Trivial` compares two stack candidates (the measured 1181 ns/op trivial test drops to the 76 ns/op caller-buffer form on the freebsd VM).
- P2 (Faraday) fixed: this SOW's Pre-Implementation-Gate, status text, and decision list now describe the delivered state; decision 2 (Windows scope) is resolved (option A) below.
- P3 sweep in the same round: unified the `freebsdACL*` identifiers on the `fbsd*` prefix (mechanical, security package only); removed the dead `inspection == nil` refusal arms; removed the milestone-lifecycle labels from the alloc-pin headers; deduplicated the over-file fixture open dance into one `buildFixtureWriter` helper per package; renamed the terse mode-bit constants in `fbsdNFS4SyncMode`; fixed the `acl_darwin.go` comment (linux+freebsd machine, SOW-0026 tracking label); added the Windows `CoordinationSupported` surface and tagged the POSIX-internal unreadable-pages test `!windows` so `GOOS=windows go vet ./...` is green; corrected the FreeBSD lock-machine wording (whole-file flock exists in Rust, no OFD byte-range machine).
- Averroes P3-2 recorded: the POSIX.1e live arm stays unit-tested only - the freebsd host's root filesystem is ZFS with acltype=nfsv4, so an on-host POSIX-brand live proof is not possible on the current host (a UFS/`zfs set acltype=posix` filesystem would be needed). The POSIX strip vectors are additionally libc-verified in-memory (`acl_from_text` + `acl_strip_np`) on the host, closing most of the residual risk.
- Second re-review pass on the fix round: the five reviewers re-reviewed HEAD `7a8cabc`. Verdicts: Hubble (parity) PASS, Einstein (idioms) PASS, Averroes (wire) PASS, Faraday (records) PASS, Hypatia (performance) FAIL with one remaining P2 - the bitmap validation walker still allocated one heap object per visited node and per absent child (`bitmapLeafTotals.result`/`bitmapBranchTotals.result`/`absentBitmapResult` returned `*bitmapNodeResult`; the Rust authority returns `NodeResult` by value). Fixed in the same pass: the bitmap walk now carries `bitmapNodeOption{result, ok}` by value like the other sweep cursors, and the live-validation pin bound (200) continues to pass at ~103 measured. Hypatia re-verified the fix (see the close record).
- Host proof re-run at the fix-round HEAD `7a8cabc` (freebsd VM, `~/src/iprange`): plain `go test ./...` GREEN, `-tags v4work` GREEN, `go vet ./...` clean, `-race` on security/live/publication/recovery GREEN, all under `nice`. This anchors the host battery to the fix-round HEAD (the earlier host-proof bullet above records the first proof at the slice HEAD `a07df5f`).
- Decided and recorded (Averroes P3-1): Go's `fbsdSetACL` reuses the get-time brand probe instead of re-running `fpathconf(_PC_ACL_NFS4)` like libc `acl_set_fd`. The brand of one open fd cannot change between the get and the set on the same flow, so this is one fewer syscall with identical behavior; kept as-is by decision.

### Resolved user decisions (2026-08-25)

- Windows surface scope: option A - pure-Go syscall surface (mirror the FreeBSD ACL-machine approach via x/sys/windows), stays within Decision 2A (no cgo). Recorded before the Windows work package starts.
- Swarm gate timing: run the five-reviewer gate on the milestone-5 slice delivered so far (work packages A + B) NOW, before stacking the next packages on top; the same five reviewers continue for the SOW-0026 close.

## Requirements

### Purpose

Close the platform, durability, and scratch gaps that SOW-0025 (pure-Go exact v4 port, milestone 4) recorded as deferred, so the Go and Rust engines satisfy the full v4 acceptance contract.

### User Request

Follow-up discipline at milestone close: every deferred item must be carried by a real pending SOW (SOW-0025 close record; project AGENTS.md "Followup Discipline"). The user accepted the milestone-4 close with these items tracked.

### Assistant Understanding

Facts:

- SOW-0025 milestone-4 close records these open items (SOW-0025 close record, 2026-08-25):
  - Windows live/publication surface: Go keeps typed refusals; Rust namespace/windows.rs is the authority. Carried from the M4 scope as a tracked open item for the M5 platform acceptance review.
  - External sort + authorized scratch (recovery slice F): heap-only first with exact budget accounting; scratch/external-sort/mapped-window machinery deferred.
  - FreeBSD durable immutable publication in pure Go: refused today (no libc ACL in pure Go; Decision 2A); Rust implements it (publication/security/freebsd.rs). Scope decision requested from the user at the M4 close (reader-only amendment vs machine delivery).
  - Rust-side durability-shape pins on Apple Silicon: memmap2 floor-align extends flush ranges (16 KiB hardware pages); raw msync callers on XNU-16K need a pinned native flush shape.
  - Naked-SIGBUS asm role gate (sigbus_linux_amd64.s): roles 1..4 hardcoded; the new tripwire test (asm_role_gate_test.go) fails when a fifth MappingRole is added until the gate is extended.
  - Per-page heap allocation in the offline validation/recovery sweeps: format.InspectLayout returns *LayoutInspection (one heap alloc per visited page; Rust returns the layout by value). Tracked as a performance follow-up with a value-return refactor plus an offline-sweep allocation pin (M4 close review P3-3).
- SOW-0017 (snapshot signing, phase 2) already tracks the authenticated-snapshot work and is independent of these items.

Inferences:

- The M5 transition (SOW-0025 "Next" block) is the natural slot to expand this SOW into the acceptance-review work package.

Unknowns:

- None: the M4 close decision (2026-08-25, option A) resolved the FreeBSD scope; the remaining unknowns are implementation details discovered when this SOW starts.

### Acceptance Criteria

- Every item above is implemented, explicitly rejected with evidence, or re-mapped into a newer pending/current SOW.
- Complete SOW-0025's accepted M4 scope without carrying unexplained debt.

## Analysis

Sources checked:

- SOW-0025 close record (2026-08-25), `.agents/sow/current/SOW-0025-20260811-pure-go-exact-v4-port.md`
- Rust authority `v4/rust/iprange-livedb/src/publication/security/freebsd.rs`
- Go pure-Go stance records (Decision 2A in SOW-0025)

Current state:

- All items are recorded with evidence in SOW-0025's follow-up map.

Risks:

- FreeBSD durable publication and Windows live are the two acceptance-criteria gaps; both ship in Rust today.
- Pure-Go constraint (Decision 2A) blocks the macOS filesec machine; macOS live/publication refusals stay recorded.

## Pre-Implementation Gate

Status: resolved for work packages A and B (delivered, gate-fixed, and re-verified in this SOW's status record); the Windows surface (final item) carries its resolved scope decision below.

Problem / root-cause model:

- A. The offline validation/recovery sweeps allocate one heap object per visited page: format.InspectLayout returns *LayoutInspection while Rust returns the layout by value (slotted_page.rs), so a 100k-page sweep pays ~3-6 ms of avoidable allocation (M4 close review P3-3; no SOW record existed for it - recorded into SOW-0026 at the close).
- B. The Go SDK refuses durable immutable publication on FreeBSD because security.CreatorOnlySupported is linux-only (pure Go has no libc ACL surface; Decision 2A), while the Rust authority ships it via publication/security/freebsd.rs (acl_get_fd/acl_set_fd/acl_strip_np/acl_is_trivial_np). FreeBSD's ACL primitives are raw syscalls (no libc), so a pure-Go machine is feasible within Decision 2A and closes the acceptance-criteria gap recorded at the M4 close (user decision option A: implement here).

Evidence reviewed:

- SOW-0025 close record (FreeBSD scope amendment, P3-3 disposition); Rust authority publication/security/posix.rs + freebsd.rs; Go internal/security package (linux xattr machine, refusal stubs); binary-format-v4.md section 20 (durable publication) and the platform contract (FreeBSD durable immutable publication supported).

Affected contracts and surfaces:

- A. internal/format.InspectLayout callers: validation/page.go, recovery/range_scan.go, recovery/tree_scan.go, recovery/membership_blob.go; no public surface change (return type is internal).
- B. internal/security (new freebsd machine + CreatorOnlySupported), live.CreationSupported, mapping.CoordinationSupported stays, internal/live + internal/writer platform gates (freebsd keeps skipping by design: no sidecar byte-range lock machine and no mapping.Create on freebsd), publication durable arms on freebsd; public capability checks; acceptance criteria wording.

Existing patterns to reuse:

- A. The value-return shape of Rust slotted_page layout decode; the existing alloc pins (recovery output push path, live_alloc_test.go).
- B. security_linux.go xattr machine structure; the platform predicate pattern (aclSupported/coordinationSupported); Rust freebsd.rs as the behavioral authority; live/writer TestMain gates.

Risk and blast radius:

- A. Low: internal type change; compile-checked; sweeps already tested; alloc pin added.
- B. Medium: delicate platform ABI (struct acl layout, entry walk, mask recalculation); incorrect ACL manipulation could corrupt file ACLs - mitigated by proving against the host (freebsd VM) with the full publication suite, and by mirroring the Rust implementation exactly.

Sensitive data handling plan:

- None of the items handle credentials or personal data; host proofs run on the user-authorized freebsd VM clone.

Implementation plan (delivered; the gate fix round completed both items):

- A. InspectLayout returns the layout by value; the page header and the cell iterator join the value-return shape; the recovery tree scan is generic over the key type; the sweep refusal envelopes copy the page number only inside cold arms; the offline-sweep allocation pins now pin 0 (tree) and 1 fixed scanner object (blob) per run, and the live validation pin bound tightened to 200. (committed `ff3435d` + gate fix round)
- B. FreeBSD machine: the ACL struct layout and syscall numbers were frozen from the host headers; remove_inherited + require_trivial ported from Rust freebsd.rs; security_freebsd.go wired via build tags; aclSupported flipped for freebsd. The live/writer gates were NOT removed: the writer gate keys on mapping.CoordinationSupported (freebsd-absent because writer tests create database files through mapping.Create, which needs the exclusive lifetime-lock machine), and the live gate keys on the sidecar byte-range lock machine (also absent on freebsd); durable publication itself needs only the security machine, so the publication/recovery/validation suites run on freebsd while live/writer skip honestly. (committed `b91f3a7` + host-proof fixes `9415a80`/`42bd7c4`)
- Remaining items stay tracked: Windows surface (decision resolved below), authorized scratch + external sort, Rust XNU-16K flush pin, Store split-window seam.

Validation plan (delivered; the on-host proof ran the real freebsd surface):

- A. Full battery + the alloc pins (tree 0, blob 1, live validation bound 200); reader hot paths unchanged.
- B. Full battery on linux at every commit; on-host proof on freebsd (plain + v4work): the publication/recovery/validation suites run and pass on the VM; the live/writer suites skip by design (absent lock machines on freebsd); darwin unchanged.

Artifact impact plan:

- Specs: the platform contract already states FreeBSD durable publication; no spec text changes expected (Go implementation catches up to the contract). AGENTS.md unchanged. This SOW's record carries the evidence.

Open decisions:

- 1. FreeBSD durable publication: resolved (option A) - implement the pure-Go ACL machine here; on-host proof required. Delivered.
- 2. Windows live/publication surface: resolved (option A, 2026-08-25) - pure-Go syscall surface mirroring the FreeBSD ACL-machine approach via x/sys/windows, staying within Decision 2A (no cgo). Remains the final work package of this SOW.
