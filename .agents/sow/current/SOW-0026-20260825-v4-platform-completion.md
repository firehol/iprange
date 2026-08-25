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

- Work package A (layout-inspection allocation fix) delivered and committed at `ff3435d`: `format.InspectLayout` returns the layout by value; the validation and recovery sweeps allocate nothing per page (alloc pins tree 2138->2057, blob 927->924, exactly the walk baselines). Full battery green.
- Work package B (FreeBSD durable immutable publication, pure-Go ACL machine) DELIVERED, committed `b91f3a7` + host-proof fixes `9415a80`/`42bd7c4`, host-proven on the freebsd VM (FreeBSD 14.1-RELEASE amd64, ZFS acltype=nfsv4): implemented in `internal/security` on top of `ff3435d`:
  - `acl_freebsd_algo.go`: ABI-exact kernel `struct acl`/`struct acl_entry` plus exact translations of libc `acl_strip_np`, `acl_is_trivial_np`, `acl_calc_mask`, and kernel `acl_nfs4_sync_mode_from_acl` / `acl_nfs4_trivial_from_mode_libc` (PSARC/2010/029 and canonical-six draft forms).
  - `acl_freebsd_sys.go` (freebsd build tag): raw `__acl_get_fd`/`__acl_set_fd` syscalls (349/350), the libc `fpathconf(_PC_ACL_NFS4)` brand probe (ZFS NFSv4 vs POSIX.1e), libc `_posix1e_acl_sort` presort for the POSIX set arm, and the Rust error classes (get EOPNOTSUPP -> CodeDurabilityUnsupported, other get/set failures -> CodeIO with the Rust operation labels).
  - `acl_freebsd_algo_test.go`: host-measured vectors (FreeBSD 14.1-RELEASE, ZFS acltype=nfsv4) for the PSARC forms of eight modes, the draft form of 0600, sync-mode round trips, triviality decisions, POSIX strip/mask-recalc/trivial, and the POSIX sort.
  - `acl_freebsd_sys_test.go` (freebsd build tag): live kernel round trip - fresh 0600 file proves; a named-entry ACL fails the proof, strips to trivial, and proves again (NFSv4 arm); the POSIX arm pins the masked-ACL behavior; devfs reports the EOPNOTSUPP class.
  - `CreatorOnlySupported()` flips true on freebsd; `acl_other.go` drops the freebsd stub; the platform gate skip texts and "linux-only" comments updated across security/live/publication/recovery; `destination_create_linux_test.go` became `destination_create_posix_test.go` (linux || freebsd); `securityNamespaceError` recognizes the freebsd CodeIO operation labels as IoAt.
  - The freebsd live/writer package gates stay skipped by design: live creation needs the sidecar byte-range lock machine (`lock_refuse.go`, Rust authority has no FreeBSD lock machine) and writer fixtures create database files through `mapping.Create` (`mapping.CoordinationSupported` stays false) - the publication/recovery/validation suites are the newly green freebsd surface.
  - Host proof (freebsd VM, `~/src/iprange` at HEAD): plain `go test ./...` GREEN (root suite 151 s, recovery 31 s, security, publication, live/writer skip honestly), `-tags v4work` GREEN, race+checkptr on security/live/publication/recovery GREEN, vet clean. The zero-alloc publish-set pin passes on freebsd: the ACL struct stays on the stack (escape-verified), and the only prior 4096-bucket growth (694 -> 2265) is gone.
  - Host-proof fixes along the way: the freebsd ACL get now fills a caller-provided buffer (no page-sized heap object on the proof paths); recovery/snapshot fixtures build over-file like production so the recovery, publication, and snapshot suites run on freebsd; the live-pair algebra property test, the replacement-policy test, and the structured transaction round trip gained the exact platform gates (live creation, atomic name exchange).
  - Local battery green at every commit: plain, v4work, race+checkptr, vet, cross-builds linux/darwin/freebsd/windows amd64+arm64 and freebsd riscv64.
Sub-state: milestone-5 transition started (user decision 2026-08-25). Work package A (layout-inspection allocation fix) and work package B (FreeBSD durable immutable publication, pure-Go ACL machine) are being implemented now; the remaining items (Windows live/publication surface, authorized recovery scratch + external sort, Rust XNU-16K flush-shape pin) stay tracked below and become work packages in later rounds of this SOW.

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

Status: ready (work packages A and B); the Windows surface (final item) keeps a separate user decision.

Problem / root-cause model:

- A. The offline validation/recovery sweeps allocate one heap object per visited page: format.InspectLayout returns *LayoutInspection while Rust returns the layout by value (slotted_page.rs), so a 100k-page sweep pays ~3-6 ms of avoidable allocation (M4 close review P3-3; no SOW record existed for it - recorded into SOW-0026 at the close).
- B. The Go SDK refuses durable immutable publication on FreeBSD because security.CreatorOnlySupported is linux-only (pure Go has no libc ACL surface; Decision 2A), while the Rust authority ships it via publication/security/freebsd.rs (acl_get_fd/acl_set_fd/acl_strip_np/acl_is_trivial_np). FreeBSD's ACL primitives are raw syscalls (no libc), so a pure-Go machine is feasible within Decision 2A and closes the acceptance-criteria gap recorded at the M4 close (user decision option A: implement here).

Evidence reviewed:

- SOW-0025 close record (FreeBSD scope amendment, P3-3 disposition); Rust authority publication/security/posix.rs + freebsd.rs; Go internal/security package (linux xattr machine, refusal stubs); binary-format-v4.md section 20 (durable publication) and the platform contract (FreeBSD durable immutable publication supported).

Affected contracts and surfaces:

- A. internal/format.InspectLayout callers: validation/page.go, recovery/range_scan.go, recovery/tree_scan.go, recovery/membership_blob.go; no public surface change (return type is internal).
- B. internal/security (new freebsd machine + CreatorOnlySupported), live.CreationSupported, mapping.CoordinationSupported stays, internal/live + internal/writer platform gates (freebsd stops skipping), publication durable arms on freebsd; public capability checks; acceptance criteria wording.

Existing patterns to reuse:

- A. The value-return shape of Rust slotted_page layout decode; the existing alloc pins (recovery output push path, live_alloc_test.go).
- B. security_linux.go xattr machine structure; the platform predicate pattern (aclSupported/coordinationSupported); Rust freebsd.rs as the behavioral authority; live/writer TestMain gates.

Risk and blast radius:

- A. Low: internal type change; compile-checked; sweeps already tested; alloc pin added.
- B. Medium: delicate platform ABI (struct acl layout, entry walk, mask recalculation); incorrect ACL manipulation could corrupt file ACLs - mitigated by proving against the host (freebsd VM) with the full publication suite, and by mirroring the Rust implementation exactly.

Sensitive data handling plan:

- None of the items handle credentials or personal data; host proofs run on the user-authorized freebsd VM clone.

Implementation plan:

- A. Change InspectLayout to return the layout by value; adopt the value at the four call sites; add an offline-sweep allocation pin; rerun battery. (small, first)
- B. FreeBSD machine: freeze the ACL struct layout and syscall numbers from the host header; port remove_inherited + require_trivial from Rust freebsd.rs; wire security_freebsd.go via build tags; flip aclSupported for freebsd; remove the freebsd skips from the live/writer gates (keep the lifetime-lock freebsd absence check - the writer gate keys on mapping.CoordinationSupported, which stays freebsd-absent: the writer package skip must be re-evaluated because writer tests create database files through mapping.Create, which needs the exclusive lifetime-lock machine - verify whether freebsd durable publication needs the lifetime lock for the WRITER, or only the security machine for publication).
- Remaining items stay tracked: Windows surface (user decision when started), authorized scratch + external sort, Rust XNU-16K flush pin, Store split-window seam.

Validation plan:

- A. Full battery + the new alloc pin; confirm the reader hot paths unchanged.
- B. Full battery on linux (freebsd not skipped anymore adds the live/writer suites on the workstation cross-compile - cannot run freebsd binaries locally except via the VM); on-host proof on freebsd (plain + v4work both suites must now run the previously skipped live/writer packages); darwin unchanged.

Artifact impact plan:

- Specs: the platform contract already states FreeBSD durable publication; no spec text changes expected (Go implementation catches up to the contract). AGENTS.md unchanged. This SOW's record carries the evidence.

Open decisions:

- 1. FreeBSD durable publication: resolved (option A) - implement the pure-Go ACL machine here; on-host proof required.
- 2. Windows live/publication surface: deferred to the final work package of this SOW; separate user decision on scope (cgo vs pure-Go syscall surface) when started.
