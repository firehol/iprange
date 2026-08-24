# SOW-0025 - Pure-Go Exact v4 Semantic Port

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

Recorded as Review Process below.


## Status
## Status

### Status (2026-08-23) - chunk 4-2 design recorded: worker SIGBUS spike, Linux amd64

Chunk 4-2 (worker SIGBUS spike) design, recorded before coding per the
pre-implementation gate. Rust authorities read in full: worker/posix.rs
(674 lines: Handler install/verify/Drop, signal_handler, owned_fault,
chain, call_action, apply_mask, redispatch_default, kernel_bus_code,
the 15-case signal_chain_subprocess_matrix), worker/control.rs fault
subset (Control::create_parent/open_worker/arm/disarm/fault_record/
alt_stack/base, CONTROL_LEN 1 MiB, ALT_STACK_LEN 64 KiB,
OWNED_FAULT_EXIT 197, FAULT_MARKER 0x42555346), worker/fault_memory.rs,
worker.rs (install point before Context::enter), fault.rs (crash points
are unrelated: the 86-code fault injection stays in internal/fault).

Design decisions (all Rust-parity, Decision 2A - no cgo, no runtime
linkname, no swallowed signal, no Rust-worker dependency):

- Package: v4/go/internal/worker (leaf; the 4-11 worker binary and the
  4-12 per-platform proofs consume it). Control-page layout subset at
  the exact Rust offsets: magic IPR4WRK\0 at 0, protocol 1 at 8, state
  at 12, probe armed at 116, handling at 120, generation/base/len at
  104/128/136, fault record at 144-176 (generation, role, code,
  relative, address, marker), state Request=1 / Fault=8, roles 1..=4.
- Go-side Control: createParent (os.TempDir() + .iprange-v4-worker-
  <16-byte hex nonce>.ctl, 0600 O_EXCL, truncate to 1 MiB,
  mapping.MapFile read-write MAP_SHARED, zero, magic+protocol+state),
  openWorker (exact 1 MiB extent), arm/disarm/faultRecord with the
  exact Rust validation (fault_record cross-checks generation, role,
  code>0, relative<len, base+relative==address), altStack (last
  64 KiB), base, removePath, close. The control file is a mapped
  coordination artifact exactly like the 4-1 sidecar: no read/write
  syscalls, only MapFile views; atomic fields (armed/handling/state)
  use sync/atomic over aligned mapped offsets, scalars use format.U64/
  PutU64, mirroring Rust's AtomicU32 + volatile mix.
- Signal machinery: sigbus_linux_amd64.s is the project-owned shim
  (rt_sigaction 13, rt_sigprocmask 14, rt_sigsuspend 130, sigaltstack
  131, kill 62, getpid 39, exit_group 231). The handler is a naked asm
  symbol with no Go declaration, entered by the kernel with the C ABI;
  it never calls Go. Go-callable asm install/verify/restore mirror
  posix.rs exactly (conflict check, sigaltstack capture, previous
  action capture and PREVIOUS_ACTION publish before install,
  SA_SIGINFO|SA_ONSTACK, empty mask, ACTIVE_CONTROL CAS, verify; Drop =
  disarm, CAS clear, restore action only if still ours, restore stack
  only if still ours).
- Owned-fault path: si_code kernel-bus codes 1..=5, armed==1,
  generation!=0, role 1..=4, len!=0, relative<len, handling CAS 0->1,
  fault record writes, state=Fault, exit_group(197).
- Chaining: restore the previous disposition, apply the exact
  kernel-equivalent mask (64-bit kernel sigset, sigsetsize 8; Rust
  loops glibc candidates 1..=1023 but only bits 0..=63 - signals
  1..=64 - are kernel-representable, and all of them propagate;
  SA_NODEFER/SA_RESETHAND honored),
  then TAIL-JUMP to the previous handler instead of calling it: the
  kernel frame stays intact, so the Go runtime's own sigtramp can be
  chained safely (returns through the frame's restorer) while the
  observable matrix outcomes stay identical to Rust (each previous
  test handler exits with its code). SIG_DFL: restore + return on
  synchronous faults (re-executed instruction dies), kill+sigsuspend
  loop on asynchronous; SIG_IGN: restore + return.
- Tests: in-package sigbus_linux_amd64_test.go, subprocess pattern of
  internal/writer/crash_v4work_test.go (env-stripped spawn, timeout,
  exit-code assertions). Matrix: owned 197, user-one 81, user-siginfo
  82, user-mask 88, user-nodefer 89, user-reset 90, captured-reset 92,
  unarmed 83, out-of-region 83, stale-region 83, nested 83, null-info
  86, replacement 91, default killed-by-SIGBUS, ignore 84,
  native-reset 90 (Rust allows 86|90; Linux is 90). Plus
  owned-fault-record-survives-worker-exit (parent creates the control,
  child opens/arms/faults, parent reads the exact record: role Scratch,
  relative == native page, mapping_len == 2 pages) and a Go-specific
  chaining proof (previous disposition is the Go runtime's own SIGBUS
  handler; an unarmed fault must chain into the runtime without a hang
  and never exit 197).
- Platforms: implemented for linux/amd64 (host proof); every other
  platform gets the typed refusal stub pattern of the repo
  (CodeOSUnsupported, "worker SIGBUS isolation is not implemented on
  this platform"), so the six cross-compiles stay green. Darwin/FreeBSD
  native proof lands in 4-12.

Validation plan (all under nice; expected cost: matrix children add
~1-2 s, full battery ~2-3 core-minutes - recorded per the resource
budget rule): gofmt -l, go vet, plain/v4work tests, race,
race+v4work, checkptr=2, mmap-trace, six cross-compiles
(linux/386, linux/arm, linux/arm64, windows/amd64, darwin/arm64,
freebsd/amd64). Then the five-aspect review gate (same five agents),
SOW close entry, signed commit + push.
### Status (2026-08-23) - chunk 4-2 design probe: kernel evidence isolates a missing SA_RESTORER

Before coding, the raw-syscall signal path was probed at kernel level
(plain-C matrix + a Go asm prototype in /tmp scratch) to validate the
design's kernel-behavior claims. The probe found the recorded design
would fail on this kernel: every owned-fault test would die 139 instead
of 197, before the handler's first instruction.

Evidence (kernel 7.1.6-1-MANJARO, x86-64; scratch C matrix /tmp/csig2,
Go prototype /tmp/sigproto):

- Raw rt_sigaction with SA_SIGINFO|SA_ONSTACK but WITHOUT SA_RESTORER:
  the handler is never entered; the kernel kills delivery with SIGSEGV
  si_code=SI_KERNEL si_addr=NULL (exit 139). Identical for the 1-arg ABI,
  with and without an alternate stack (C modes 1-3).
- libc sigaction always sets SA_RESTORER -> handler runs (exit 90). Raw
  rt_sigaction WITH SA_RESTORER set (project naked rt_sigreturn stub,
  libc's restorer, or even NULL) -> handler runs (exit 90; C modes 5-7).
  The flag is the trigger, not the restorer value.
- The Go asm prototype (same raw-syscall sequence inside a Go test
  binary) reproduces the identical kernel crash at the raise point (the
  Go runtime turns the SIGSEGV SI_KERNEL into "unexpected fault address
  0x0"); with SA_RESTORER + a naked rt_sigreturn stub (syscall 15) the
  child exits 90 (PASS).
- Rust never hits this: posix.rs installs through libc::sigaction, which
  sets SA_RESTORER implicitly. The Go shim has no libc, so the flag and
  a project-owned restorer are mandatory.

Design corrections recorded for chunk 4-2:

1. SA_RESTORER (0x04000000) is a required install flag; the restorer
   slot holds a project-owned naked rt_sigreturn stub (syscall 15) in
   sigbus_linux_amd64.s. The stub is also the return path for any chained
   handler that returns (the kernel frame's pretcode). verify_owned keeps
   exactly posix.rs's checks (handler identity, required flag subset,
   no NODEFER/RESETHAND, stack identity, ACTIVE_CONTROL identity);
   SA_RESTORER presence follows from the handler check.
2. Go worker threads are not single-threaded: Go migrates goroutines
   between OS threads and installs a per-thread 32 KiB sigaltstack on
   every M (observed in the prototype strace). The alternate stack is
   per-thread, so the thread that installs the handler must be pinned
   with runtime.LockOSThread() before install (spike test children and
   the 4-11 worker binary alike). This mirrors Rust's single-threaded
   worker and keeps verify's stack-identity check stable.
3. Mapped-control atomics (armed/handling/state and the scalars Rust
   reads atomically) are Go-callable asm LOCK-prefixed primitives
   (mapAtomicLoad32/Store32/CAS32 on base+offset) used by BOTH the Go
   control code and the naked handler: Go's sync/atomic is specified for
   Go-managed memory only, and the handler needs the same primitives
   anyway. This is a narrow deviation from the recorded "sync/atomic
   over aligned mapped offsets" wording, with the same semantics.
4. Test-only asm: the original probe premise ("_test.s files compile
   into production builds") was disproven by a counterexample on the
   repo toolchain (go1.26.4): a symbol defined only in *_test.s is
   undefined in `go build` - the file is fed to the symbol-ABI pass,
   never assembled into the package object. The conclusion stands on
   the verified half: //go:build constraints in assembly are honored,
   so the matrix's naked previous-handler symbols live in a
   //go:build v4work .s file exactly like crash_v4work_test.go, and
   the v4work tag also keeps -tags v4work cross-builds green (the
   matrix .s additionally carries linux && amd64). The signal matrix
   runs under -tags v4work; Control unit tests (no signals) run in
   plain builds. This also matches the Rust matrix's #[cfg(test)]
   placement: the Go equivalent of "test-only" is the v4work
   configuration.
5. Go-specific chaining proof semantics: with the Go runtime's own SIGBUS
   handler as the previous action (SA_RESTORER+restorer captured
   verbatim), an unarmed fault restores that exact action, applies the
   kernel-equivalent mask, and tail-jumps with the kernel frame intact;
   the runtime handler then fatals ("unexpected fault address", exit 2).
   The assertion is: exit != 197, exit != 0, no hang (under -race the
   race runtime may be the previous handler and prints its own fatal).



### Status (2026-08-23) - M4 defined: sidecar, lifecycle, validation, recovery, worker, platform

M3 closed at 8c53fce (signed, pushed). Milestone 4 (live/platform/
recovery completion) scoping completed by five parallel read-only
investigations over the Rust authorities and the Go tree; the M4 chunk
plan below is recorded for the gate rounds.

Rust authorities and measured sizes: live coordination surface
(live_sidecar 518 + header/slot 169 + live_lock 288 + live_namespace
312 + live_reader 222 + reader_core/live 233 + live_cleanup 289 +
live_lifecycle 2,709 across creation/create_resolution/transition/
resolution/residue/namespace + commit_resolution 412 + source_guard/live
396, ~5.7k), publication resolvers (publication/ scoped set ~5.4k +
required attempt/cleanup/output/main_file support ~2.1k), validation/
(~4.8k), recovery/ (~9.7k incl. tests; production ~4.3k), worker/
(worker.rs + control 859 + posix 674 + client + wire + fault_memory,
~3.4k + wire codecs), fault.rs, work.rs, live_crash_tests.rs,
mmap_runtime_tests.rs.

Go gap confirmed: no sidecar file, no byte-range lock primitive beyond
the main-file lifetime lock (internal/mapping), no gate/writer/slot
lock ranges, no reader registration, no commit barrier inside
Core.Publish, no CreateLive/InitializeLive/ResetLiveCoordination/
ResolveInterruptedLiveTransition, no LiveReader, no explicit Validate,
no recovery, no worker process or SIGBUS handling, one-shot in-memory
publication (no reservation files/resolver/residue/security). Go
current state that composes: internal/mapping (identity, no-follow
open, OFD lifetime lock linux/darwin, freebsd flock + live refusal,
windows honest stub, publish link machines linux/darwin/freebsd/netbsd,
remap, shrink, sync), internal/writer (publication.go CommitAttempt/
Prepare/Publish outcome classes, publication_staging.go one-shot
attempt, close.go tail cleanup, create.go main-only create, codecs,
OutputBuilder, heap), internal/reader (immutable reader, sidecar
absence refusal, metadata chain reader), internal/snapshot (To() with
SourceLive refused at snapshot.go:80, budget already reserves 3 live
files), internal/fault (crash/fail points, v4work env injection,
exit 86), internal/work (counters), subprocess test pattern
(subprocess_cross_open_test.go, crash_v4work_test.go).

M4 chunk plan (dependency-ordered; each chunk closes with the
five-aspect gate, committed signed and pushed):

- 4-1 sidecar core: internal/live/ lock.go + per-OS lock files,
  header.go, slot.go, sidecar.go (reserve/creating/ready/open/verify/
  gate/writer/slot ops/scan/oldest), cleanup.go, namespace helpers,
  fault points; ports live_sidecar_tests.rs + lock tests.
- 4-2 worker SIGBUS spike (Linux): minimal project-owned assembly
  sigaction shim per resolved Decision 2A (2026-08-12), armed-region
  ownership, unrelated-signal chaining matrix, alt-stack discipline;
  proves the fault-isolation contract before the worker slices.
- 4-3 create/initialize: CreateLive (sidecar-first ordering,
  CreateResult/CreationState), InitializeLive (offline conversion),
  creation security 0600 + IPR4PSEC commitment surface (POSIX).
- 4-4 live writer open + commit barrier: LiveWriter open
  (gate + claimWriter + tail cleanup), gate-around-Publish (identity
  recheck, unchanged base, slot scan, meta write/sync), close releases
  lease; OpenLiveWriter public; immutable-mode paths unchanged.
- 4-5 live reader: LiveReaderCore open/register/select/verify/close,
  ReaderCloseResult, ForkedHandle ownership (PID fallback per spec
  15.6; MADV_WIPEONFORK not available in Go), OpenLiveReader public.
- 4-6 transitions: ResetLiveCoordination (RollbackSafe exchange /
  DiscardPrevious), ResolveInterruptedLiveTransition,
  resolve_live_transition, resolve_create_live, .readers.reset
  deterministic private name.
- 4-7 live snapshot source: SnapshotTo(Live) + recovery source guard
  (register-like pin, release order).
- 4-8 publication resolvers: result/problem surface, reservation
  codec, reservation file lifecycle, reservation inspection,
  replacement evidence, resolver core + authority + verification,
  replacement resolver + Publish retrofit to the reservation path,
  residue + maintenance + security; Windows machinery stays honest
  refusal (recorded below).
- 4-9 validation: internal/validation, ImmutableCurrent first
  (context Claims bitmap, page/tree/range/catalog/bitmap/membership/
  structure/blob/metadata/retirement validators over the single
  authority codecs), then LiveCurrent/OfflineCandidate after 4-4/4-6.
- 4-10 recovery: internal/recovery, classify (recovery-readable meta,
  generation-order proof, candidate tokens), page set, tables,
  overlap components, direct build (ordered/in-memory), indirect build
  (catalog/membership/structure), outputs, source guard, terminal
  result/report, public RecoverImmutable/RecoverOffline +
  InspectRecoveryCandidates; external sort + authorized scratch
  tracked separately (below).
- 4-11 worker full: worker binary (cmd/iprange-v4-worker), control
  page + wire codecs, spawn/handshake/drive client, fault memory +
  unreadable-page retry, worker modes (validate/inspect/recover/
  cleanup), build-id matching.
- 4-12 platform completion + proof matrix: native proof on darwin and
  freebsd (offline validation/recovery not live-gated), mmap-runtime
  trace for worker+recovery, crash matrix extension (create/init/
  reset/metadata), budget/fault-injection resource tests, worker-build
  mismatch tests, code-size/duplication audit.

Recorded scope decisions for M4 (Rust is the baseline; no new user
decision was required to start):

- Worker boundary: resolved Decision 2A applies (minimal project-owned
  assembly sigaction shim; no cgo, no runtime linkname, no swallowed
  signal, no Rust-worker dependency). The 4-2 spike proves it on Linux
  first; native proof per POSIX platform lands in 4-12.
- Windows: Go keeps the honest-refusal stance established in M1-M3
  (mapping_windows.go refuses opens; windows publish stubs refuse).
  M4 implements the live surface on Linux first with darwin/freebsd
  gated by the existing OFD/flock machines; Windows live support is
  recorded as a tracked open item for the M5 platform acceptance
  review, not silently claimed. SnapshotTo, validation and recovery
  remain available on FreeBSD in their offline/immutable forms.
- External sort + authorized scratch (recovery slice F): tracked as a
  follow-up within M4 (4-10 scope: heap-only first, exact budget
  accounting; scratch/external-sort/mapped-window machinery added in a
  later 4-x slice when a Phase-1 consumer needs it), consistent with
  minimal-complete and the recorded spec allowance for authorized
  recovery scratch.
- M4 milestone gate: same five-aspect review process as M3; reviewers
  restarted at the M3/M4 boundary (completed) and reused across M4
  chunks.

### Status (2026-08-23) - chunk 4-1 implemented: sidecar core on the working tree, gate pending

Chunk 4-1 (sidecar core) implemented on the working tree over
8c53fce; five-aspect review dispatched, no commit yet.

Delivered (all under v4/go/):

- internal/live/ (new, 1,468 production lines + 418 test lines):
  lock.go + lock_linux.go + lock_darwin.go + lock_refuse.go
  (F_OFD_SETLK/LKW gate/writer/slot ranges, EINTR retry,
  EACCES/EAGAIN try semantics, freebsd+windows typed refusal),
  header.go (magic IPRDRS4\0, size 68, slot size 16, state 0/1,
  CRC-32C at offset 64 over the zeroed field, exact length
  4096+capacity*16 with overflow refusal), slot.go (16-byte
  txn+complement codec), sidecar.go (reserve/reserveAt with
  capacity>0, initializeCreating with truncate -> read-write
  mapping -> creating header -> flush -> sync -> crash point
  create.after_sidecar_sync, publishReady with crash point
  create.after_ready_write -> sync, open/openAt/openAny with
  exact-length and shape/checksum fail-closed, gate/writer/slot
  lock ops, claimReaderCancellable with per-slot cancellation,
  clearReader/unlockReader/verifyReader, scan/inspect/oldest
  with per-slot cancellation, stale-slot clear on lock success,
  close without unlock per spec 15.6), cleanup.go (POSIX
  remove-exact + require-available no-op), namespace.go (0600
  O_EXCL no-follow create, single-link identity, verify-path,
  sync-parent), path.go (canonical .readers naming), Windows
  honest-refusal stubs (namespace_windows.go, mapfile_windows.go)
  so the six cross-compiles stay green.
- internal/mapping: MapFile (descriptor-duplicated lock-free
  exact-extent mapping; kernel rounds the map up, Mapping bounds
  never reach padding; mirrors Rust memmap2 exact-length mapping),
  Mapping.locked flag so MapFile mappings close without unlocking,
  RegularLinkCount moved from writer (linkcount_unix.go /
  linkcount_windows.go).
- internal/writer: publication_staging.go now uses
  mapping.RegularLinkCount; deleted nlink_unix.go/nlink_windows.go.

Port parity: live_sidecar.rs, live_sidecar/header.rs,
live_sidecar/slot.rs, live_lock.rs (linux/darwin OFD, freebsd
refusal), live_namespace.rs POSIX subset, live_cleanup.rs POSIX
subset, live_sidecar_tests.rs (10 tests, plus header-codec and
length-geometry unit tests). Rust Corrupt -> CodeFormatInvalid,
Rust WrongMode -> CodeWrongState.

Validation on the working tree, all under nice: gofmt clean, vet
clean, plain/v4work tests, race, race+v4work, checkptr=2,
mmap-trace PASS (no read/write on any .iprdb descriptor), six
cross-compiles (linux/386, linux/arm, linux/arm64, windows/amd64,
darwin/arm64, freebsd/amd64). internal/live is mmap-only: header
and slot bytes are read and written only through Mapping views;
the only heap bytes are the decoded 32-byte ID pair plus capacity,
matching Rust's decoded Header struct.

### Status (2026-08-23) - chunk 4-1 five-aspect review round 1: FAILs fixed, re-review dispatched

Round-1 gate on the 4-1 working tree: Meitner-role (Rust parity) FAIL
1xP1 5xP2, Anscombe-role (Go idioms) FAIL 4xP2, Harvey-role
(performance) FAIL 2xP2 1xP3, Newton-role (wire/integrity) FAIL 1xP1
1xP2, Aristotle-role (APIs/docs/records) PASS 1xP2 2xP3. All findings
verified against the Rust authority and fixed on the working tree
(sidecar_length(0)=Ok(4096) geometry and MapFile exact-length mapping
were also recorded here; re-review dispatched):

- P1 (x2): mapping.MapFile now proves the file extent before mmap
  (fstat, refuse size > file size, CodeFormatInvalid "mapping exceeds
  the file extent"), mirroring Rust require_file_extent; a crash-left
  short sidecar fails typed instead of SIGBUS. Regression test added
  (truncate to 0 -> CodeFormatInvalid).
- Rust-parity P2s: double-fault sites (claim cancel + unlock, slot
  write + unlock, stale clear + unlock) report CodeCleanupInProgress
  via combineErrors, mirroring sdk_error::combine_errors /
  CleanupIncomplete; createPrivate identity-failure mirrors Rust's
  no-removal + Unresolvable cleanup; createPrivate missing parent
  reports CodeNameNotFound; removeCreated replaced by removeExact with
  Rust remove_exact semantics (missing -> NameNotFound, regular +
  identity + single-link proof, parent-dir sync, require_absent);
  createPrivate fchmods 0600 independent of umask (secure_creator_only
  core; ACL/commitment surface stays in 4-3); MapFile dup is
  close-on-exec (F_SETFD/FD_CLOEXEC, spec 15.6).
- Go-idiom P2s: local putU16/putU32/putU64/getU16/getU32/getU64
  helpers deleted, callers use format.PutU16/PutU32/PutU64 and
  format.U16/U32/U64 (single authoritative codec); dead
  uniqueAttemptID and cleanupRemove deleted; Sidecar drops its
  half-guarded mutex and documents exclusive ownership like the
  mapping owner (close must not race methods); test fake-import
  `_ = fmt.Sprint()` removed.
- Performance P2s: scan/claim paths return (uint64, bool) instead of
  heap-escaped *uint64 (readActiveSlot, scanSlot, inspectSlot,
  oldestReaderCancellable); scanAtMostCancellable tracks its min on
  the stack like Rust; claimReaderCancellable threads the precomputed
  slot offset into the slot write (no recompute). Escape analysis
  confirms zero hot-path heap escapes (remaining escapes are
  error-construction only).
- Records findings: publication_staging.go stale nlink_windows.go
  citation now cites mapping/linkcount_windows.go; SOW records the
  sidecar_length(0) geometry decision; mmap-trace claim is qualified
  to main-file .iprdb descriptors (the sidecar is .readers and the
  trace pattern covers only .iprdb; internal/live mmap-only rests on
  the code evidence verified by all five reviewers).
- Post-fix tree counts: internal/live is 1,451 production + 425 test
  lines; removeExact lives in namespace.go (the pre-fix "Delivered"
  entry above recorded the round-1 snapshot).

Validation after fixes, all under nice: gofmt clean, vet clean,
plain/v4work tests, race, race+v4work, checkptr=2, mmap-trace PASS,
six cross-compiles PASS.

### Status (2026-08-23) - chunk 4-1 CLOSED: five-aspect re-review PASS on the fixed working tree

Round-2 re-review on the fixed tree: Meitner-role (Rust parity) found
two new createPrivate error-class P2s (ELOOP -> CodeIO, fchmod failure
cause -> CodeIO) which were fixed and re-verified in round 3;
Anscombe-role (Go idioms) PASS, Harvey-role (performance) PASS with
escape-analysis proof of zero hot-path heap (mu removal verified
strictly cheaper than Rust's mapping-guard), Newton-role
(wire/integrity) PASS with byte-identical codec re-verification after
the format-helper consolidation, Aristotle-role (APIs/docs/records)
PASS with two P3 record nits fixed (post-fix line counts 1,451
production + 425 test; removeExact attribution moved to namespace.go;
"Records findings" heading). Final gate: all five aspects PASS, no
P0-P2 open.

Validation on the final tree, all under nice: gofmt clean, vet clean,
plain/v4work tests, race, race+v4work, checkptr=2, mmap-trace PASS
(main-file .iprdb descriptors only), six cross-compiles
(linux/386, linux/arm, linux/arm64, windows/amd64, darwin/arm64,
freebsd/amd64). Signed commit and push follow this entry.

- Next: commit + push chunk 4-1 (explicit file list, signed), then
  chunk 4-2 (worker SIGBUS spike, Linux) per Decision 2A.

### Status (2026-08-23) - M3 milestone CLOSED: five-aspect confirmation PASS on the final working tree

The final milestone-scope confirmation over the full M3 delta
(499a0e3..3b76f8f plus the working-tree close fixes) returned all five
aspects PASS with no P0-P2 findings: Meitner (Rust parity), Anscombe
(Go idioms), Harvey (performance), Newton (wire/integrity), Aristotle
(APIs/docs/records). Milestone 3 (complete logical SDK: advanced
transactions, typed workflows, metadata, queries, joins, algebra,
snapshots, reports, cancellation, cleanup, randomized public models
over the internal core) is CLOSED.

The last review round fixed or disposed every remaining item:

- F1 (Meitner/Aristotle, P2) - spent Commit/Abort error class on the
  workflow-terminal handles: FinishedWorkflow, FinishedHistoryProjection,
  PreparedFeedChange, and DirectTransaction Commit/Abort now report
  ErrorNoPendingTransaction on a spent handle, matching Rust
  commit_attempt/abort (writer_core/publication.rs:26-34,
  live_writer.rs:233-237, live_writer/commit.rs:37-43). The
  active/closed-writer WrongState classes and the no-change Abort class
  are unchanged and pinned. New/updated pins: TestDirectWorkflowCommitAfterAbort,
  TestPublicAbortDiscards, feed rename/delete prepared-change commit
  after abort, feed FinishedWorkflow commit after abort, history
  projection commit after abort.
- P2 (Harvey) - combinedWords heap escape on the membership-combine
  miss path: DraftStore now owns one combineScratch and
  combineMembership writes the operand into it (draft_store.go,
  membership_algebra.go), mirroring structureScratch; verified with
  -gcflags=-m=2 (no escape) and pinned at exactly 0 allocations/op by
  TestMembershipCombineZeroAlloc over 200 runs.
- P3 dispositions all recorded with evidence: general-gap path
  accepted-remain (measured 54 objects/run vs the 300 ceiling), tracked
  to the M4 union-input review; beginTimestampState closed-probe-first
  pattern accepted; ScannedComparison field stutter and noopCheck naming
  accepted (Rust field-name mirroring); leaf-re-inspect after COW
  first-touch tracked; Harvey unpinned probes measured 0 allocs/op;
  requireActive ordering unreachable through the typed surface.

Validation on the final working tree, all under nice: gofmt clean, vet
clean, plain/v4work tests, race, race+v4work, checkptr=2, mmap-trace
PASS (no read/write on any .iprdb descriptor), six cross-compiles
(linux/386, linux/arm, linux/arm64, windows/amd64, darwin/arm64,
freebsd/amd64), Rust source-graph complete, Rust cargo test and
--all-features, Rust conformance.

Follow-up map at M3 close: the recorded P3 dispositions carry to their
named M4 review points (union-input review, live sidecar work); no new
follow-up items remain. SOW-0017 (authenticated public snapshots,
Phase 2 signing) remains explicitly out of scope, as recorded.

Next: commit this close signed and push, then start Milestone 4
(live/platform/recovery completion): sidecar coordination, lifecycle
and publication resolvers, supported-platform boundaries, explicit
validation/recovery, worker, crash/fault/resource proof.

### Status (2026-08-23) - chunk 3b-6 slices A-D complete: five-aspect FAILs fixed, re-review pending

Chunk 3b-6 (randomized public models over the internal core) slices
A-D are complete on the working tree: slice A (internal/writer
timestamp_refresh.go with firstSeenPolicy/lastSeenPolicy over
mergeCoverage, DraftStore mergeFirstSeen/mergeLastSeen, WriterEdit
AssignInputV4/V6, AddPrivateConstantRangeV4/V6,
FinishPrivateConstantRanges, MergeFirstSeen/MergeLastSeen), slice B
(public direct_workflow_public.go with BeginDirectReplacement,
BeginFirstSeenRefresh, BeginLastSeenRefresh and the exact Rust
precondition/error classes), slice C (public
NetworkEnrichmentV1CursorV4/V6 wrappers), and slice D (six Go property
suites mirroring the Rust tests: workflow_properties.rs,
structured_value_properties.rs, membership_algebra_properties.rs,
membership_query_properties.rs, feed_workflow_properties.rs,
history_projection_properties.rs, plus the direct-workflow surface
tests in direct_workflow_public_test.go).

The five-aspect gate round returned FAIL with nine findings; every
finding is fixed and verified on the working tree, and the full battery
is green under nice again:

- P1 (Anscombe/Harvey) - per-record BindEdit heap allocation in the
  direct workflows: directWorkflowState now owns one bound edit,
  created once in beginExactDirectState, and all four input loops
  (addDirectV4/V6, addTimestampV4/V6) reuse it; the general timestamp
  branch routes through s.edit.AssignV4/V6 via the new
  WriterEdit.AssignV4/V6. Per-record DraftStore creation is gone
  (Rust allocate_nothing_per_record parity).
- P1 (Harvey) - v4work build broken: structured_cursor_work_test.go
  now passes the internal reader RangeForward/RangeBackward constants.
- P1 (Newton) - enrichment cursor lifetime escape: both internal
  enrichment cursors pin the reader at construction (r.sh.pins.Add(1)),
  expose Close() that marks the pin closed and releases it, checkOpen()
  on every operation, Seek on the internal cursors and both public
  wrappers, and the reader Close reports HandleBusy while a cursor is
  open (Rust borrow parity).
- P2 (Meitner/Aristotle) - raw finish errors: finishTimestamp is a
  closure and both Mutate errors abort through s.w.abortAfter(err);
  completeDirectWorkflow wraps its error path with
  w.abortAfter(err); the NoChange discard failure calls
  w.core.MarkUnresolved(err) before returning. The same NoChange
  discard gap was fixed in the pre-existing feed_workflow_public.go and
  history_projection_public.go paths.
- P2 (Meitner/Aristotle) - timestamp begin error order: beginTimestamp
  state checks ValueKind != Direct first and returns
  WrongValueKind("timestamp refresh requires a direct database"),
  matching Rust live_writer.rs.
- P2 (Aristotle) - missing finish_input_with_removals_v4/v6: ported as
  FirstSeenRefresh.FinishInputWithRemovalsV4/V6 over
  writer.FirstSeenRemoval4Sink/6Sink (batchedRemovals4/6, batch
  capacity 64 = Rust REMOVAL_BATCH_CAPACITY), DraftStore
  mergeFirstSeenWithRemovals4/6 with the removal-sink family gate,
  WriterEdit.MergeFirstSeenWithRemovals4/6, and the new
  FirstSeenRemovalSink6 logical type.
- P3 (Meitner) - overflow detail string: timestampCounters.observe
  returns (Cardinality129, error) and the input-address overflow uses
  "ordered merge address count" exactly like Rust range_merge.rs.
- P3 (Newton) - RemoveLeafRun unit tests: TestRemoveLeafRunMidRunRejection
  and TestRemoveLeafRunWholeLeafAndImmediateRejection added to
  internal/tree/tree_test.go covering the Rust remove_leaf_run
  scan-then-remove contract fixed in the slice-D progress entry.
- P3 (Aristotle) - cursor doc nit: the public enrichment cursor docs
  now say "in the requested direction".
- Race battery fix: the zero-allocation pin
  (TestZeroAllocationDirectWorkflowIngestion) uses MemStats windows,
  which race shadow memory inflates. The top-level package now has the
  same raceEnabled build-tag pair as internal/writer (race_enabled.go /
  race_disabled.go, //go:build race / !race) and the pin skips under
  -race exactly like TestMetadataDeflateHeapOverheadCoversWorkspace;
  the functional first-seen removal, family-gate, and cursor-lifetime
  tests keep running under race.

Full battery green under nice: gofmt clean, vet, plain/v4work tests,
race, race+v4work, checkptr=2, six cross-compiles (linux/386, linux/arm,
linux/arm64, windows/amd64, darwin/arm64, freebsd/amd64), mmap-trace
PASS (no read/write on any .iprdb descriptor), Rust source-graph
complete, Rust cargo test and --all-features, Rust conformance.

The five-aspect re-review gate closed with all five aspects PASS on
this working tree (same five reviewers, no respawn): Meitner (Rust
parity), Anscombe (Go idioms), Harvey (performance), Newton
(wire/integrity), Aristotle (APIs/docs/records). All previous FAIL
findings were verified fixed with file:line evidence, Harvey measured
0 allocs/op on all four direct-workflow input loops, and the battery is
green. Post-gate P3 dispositions:

- Fixed (Anscombe naming symmetry): the public removal-sink types are
  now FirstSeenRemoval4Sink/FirstSeenRemoval6Sink, matching the
  internal writer names and the 4/6 suffix convention (logical_types.go,
  direct_workflow_public.go method signatures).
- Fixed (Anscombe header + Newton doc): cursors_public.go header now
  names the enrichment cursor surface; both cursor type docs state
  Close must be called; both Close docs reworded to state the pin is
  the Go mapping of the Rust reader borrow scoped to the cursor
  lifetime.
- Fixed (Newton pin symmetry): both enrichment cursor constructors now
  run the same post-increment closed re-check as Pin(), so a Close
  racing construction returns WrongState instead of pinning a closed
  reader.
- Tracked (Newton, unreachable through the typed surface): requireActive
  runs before the workflow-kind gate in requireReplacement, where Rust
  checks the kind gate first; observable only when both preconditions
  fail, so no pinned test can reach it. Align if the path is ever
  touched.
- Tracked (Meitner, accepted closed-probe-first pattern): the nil-core
  probe precedes the ValueKind check in beginTimestampState, so a
  closed writer on a non-direct database reports WrongState where Rust
  reports WrongValueKind; consistent with the accepted slice-C
  closed-probe-first pattern and unreachable in the pinned suites.

Full battery green under nice at the gated working tree: gofmt clean,
vet, plain/v4work tests, race, race+v4work, checkptr=2, six
cross-compiles, mmap-trace PASS, Rust source-graph complete, Rust cargo
test and --all-features, Rust conformance.

### Status (2026-08-23) - chunk 3b-6 CLOSED and M3 complete: committed at 3b76f8f

Chunk 3b-6 closed at 3b76f8f (signed, pushed): the working-tree delta
above plus the post-gate P3 fixes landed as one commit, 26 files, +4343
lines. The five-aspect re-review (Meitner, Anscombe, Harvey, Newton,
Aristotle) closed PASS before the P3 dispositions; the P3 fixes were
re-verified by the full battery under nice after landing.

With chunk 3b-6 closed, Milestone 3 (complete logical SDK: advanced
transactions, typed workflows, metadata, queries, joins, algebra,
snapshots, reports, cancellation, cleanup, randomized public models
over the internal core) is functionally complete: every M3 chunk (1, 2,
3a, 3b-1, 3b-2 slices 1-3, 3b-3 slices A-D, 3b-4, 3b-5, 3b-6) closed
with the five-aspect gate PASS, each committed and pushed.

- Next: milestone-scope five-aspect confirmation over the full M3 delta
  (499a0e3..3b76f8f), then Milestone 4 (live/platform/recovery
  completion): sidecar coordination, lifecycle and publication
  resolvers, supported-platform boundaries, explicit
  validation/recovery, worker, crash/fault/resource proof.

### Status (2026-08-23) - M3 milestone confirmation: Aristotle FAIL on the empty follow-up claim, same-class commit/abort class fixed

The milestone-scope confirmation over 499a0e3..3b76f8f returned four
aspects PASS (Meitner, Anscombe, Harvey, Newton) and Aristotle FAIL
with one P2: the M3 entry claimed the follow-up map is empty, but the
feed-workflow Commit-after-abort error class was still open (recorded
twice as "to resolve at the next gate" at the 3b-5 and slice-C closes).
Rust commit_attempt reports NoPendingTransaction after the draft is
gone (writer_core/publication.rs:26-34, live_writer.rs commit_attempt);
the Go FinishedWorkflow/FinishedHistoryProjection/PreparedFeedChange/
DirectTransaction Commit and Abort paths reported WrongState("...
is no longer active") on spent handles, inconsistent with the
slice-C-fixed structured/membership transactions (spent Commit and
Abort -> NoPendingTransaction, pinned).

Fixed the whole class on the working tree, following the slice-C
pattern exactly (spent check first, then the active/closed-writer
gates):

- FinishedWorkflow.Commit and .Abort (feed_workflow_public.go) - spent
  handle reports ErrorNoPendingTransaction; the no-change Abort class
  and the closed-writer WrongState class are unchanged.
- FinishedHistoryProjection.Commit and .Abort
  (history_projection_public.go) - same spent-first class.
- PreparedFeedChange.Commit and .Abort (feed_workflow_public.go) - the
  rename/delete prepared-change handles now report
  ErrorNoPendingTransaction when spent.
- DirectTransaction.Commit and .Abort (writer_public.go) - the
  pre-existing direct transaction now matches the slice-C transaction
  class (spent Commit/Abort -> ErrorNoPendingTransaction) instead of
  the older WrongState mapping.
- Stale pins updated to the corrected class and new pins added:
  TestPublicAbortDiscards (direct transaction commit and abort after
  abort), feed rename/delete prepared-change commit after abort, feed
  FinishedWorkflow commit after abort, history projection commit after
  abort, and TestDirectWorkflowCommitAfterAbort covering the shared
  FinishedWorkflow terminal of the direct workflows.

Full battery green under nice at the fix: gofmt clean, vet,
plain/v4work tests, race, race+v4work, checkptr=2, mmap-trace PASS.

- Newton's 3b-6 P3 note (requireActive ordering before the workflow-kind
  gate in requireReplacement; Rust checks the kind gate first):
  unreachable through the typed surface, align if the path is ever
  touched; tracked with the other accepted-remain dispositions.
- Next: Aristotle re-review of the F1 fix on the working tree; when
  Aristotle reports PASS, record M3 closed with the corrected follow-up
  map: the recorded dispositions above carry to their named M4 review
  points, and no new follow-up items remain beyond SOW-0017 Phase-2
  signing (explicitly out of scope). Commit signed, push, and start
  Milestone 4 (live/platform/recovery completion): sidecar
  coordination, lifecycle and publication resolvers,
  supported-platform boundaries, explicit validation/recovery, worker,
  crash/fault/resource proof.

### Status (2026-08-23) - M3 milestone confirmation: Meitner/Anscombe/Harvey FAILs fixed on the working tree

The remaining three milestone-scope confirmations over 499a0e3..3b76f8f
returned FAIL with findings; all are fixed or disposed on the working
tree:

- Meitner F-1 (P2) - workflow-terminal Commit/Abort WrongState class:
  the identical finding Aristotle raised; the whole-class fix
  (FinishedWorkflow, FinishedHistoryProjection, PreparedFeedChange,
  DirectTransaction spent Commit/Abort -> ErrorNoPendingTransaction)
  is recorded in the entry above.
- Meitner F-2 + Anscombe P2-1 (P2, records) - the M3 close-out claimed
  an empty follow-up map while tracked items were still open. The
  corrected record is in the entries above and below; the remaining
  dispositions:
  - P3-B (optionalCell/rejectCell on the cold general gap protocol):
    the randomized-models chunk exercised the general path and the
    ceiling pin holds (measured 54 objects per run against the 300
    ceiling), so the substance is confirmed P3; disposition
    accepted-remain, tracked to the M4 union-input review.
  - Meitner P3 note (beginTimestampState closed-writer probe precedes
    the ValueKind check; Rust checks the kind gate first): accepted
    closed-probe-first pattern, consistent with the slice-C ruling,
    unreachable in the pinned suites; tracked.
  - ScannedComparison.Comparison field stutter (slice-B carried):
    accepted-remain, the field name mirrors the Rust struct field
    (workflow/compare.rs comparison: Comparison), consistent with the
    direct-mapping convention used across the port.
  - noopCheck vs noopCheckpoint naming (slice-B carried): accepted,
    the two nil-checkpoint stand-ins serve different prepare-stage and
    durability-checkpoint roles in their own packages.
  - leaf-re-inspect after COW first-touch (tree/path.go): documented
    retained cost, tracked.
  - Harvey P3-2 (unpinned performance probes, OutputBuilder intern and
    both-sides trim): test-only observability gaps, both measured 0
    allocs/op; tracked.
- Harvey P2-1 (P2, performance) - combinedWords heap-escapes per
  membership-combine call (membership_algebra.go): Rust builds the
  Combined operand on the stack; the Go shape-stenciled generic
  membershipWords dispatch leaked one &combinedWords (~48-56 B) per
  combine on the miss path (two distinct non-empty bitmaps), firing on
  live M3 per-record paths (feed merge transform, history projection
  per-record merge, membership apply, structure remove-feed). Fixed
  with the established scratch pattern: DraftStore owns one
  combineScratch combinedWords field (draft_store.go) and
  combineMembership writes the operand into it
  (membership_algebra.go), mirroring structureScratch. Verified:
  -gcflags=-m=2 shows no remaining combinedWords escape; new pin
  TestMembershipCombineZeroAlloc (internal/writer/
  membership_combine_alloc_test.go) measures exactly 0 allocations per
  miss-path combine over 200 runs.

Full battery green under nice at the fixes: gofmt clean, vet,
plain/v4work tests, race, race+v4work, checkptr=2, mmap-trace PASS.

- Next: Meitner/Anscombe/Harvey re-review of the fixes on the working
  tree; when all three report PASS together with Aristotle, record M3
  closed with the corrected follow-up map, commit signed, push, and
  start Milestone 4.

Status: in-progress

### Status (2026-08-23) - chunk 3b-6 defined: randomized public models over the internal core

The remaining M3 plan item is the randomized scalar-model property
suites. Go has no property tests today; the Rust authority is the six
suites in v4/rust/iprange-livedb/tests/: workflow_properties.rs,
structured_value_properties.rs, membership_algebra_properties.rs,
membership_query_properties.rs, feed_workflow_properties.rs, and
history_projection_properties.rs (membership_import_properties.rs stays
out: import workflows are the recorded M4 live-sidecar scope).

Scope investigation found three Rust-parity surfaces the property tests
need and Go does not have yet:

- The public direct workflows (Rust live_writer/direct_workflow.rs
  begin_direct_replacement / begin_first_seen_refresh /
  begin_last_seen_refresh with add_ranges_v4/v6_slice and
  finish_input) are missing entirely; the internal writer core has the
  range workflow draft, the assignment input
  (internal/writer/range_locator.go AssignmentInput), the union input
  and private constant ranges (range_coverage.go, range_draft.go), the
  ordered merge and its mergeCoverage driver (range_merge.go), and the
  FinishDirectWorkflow binding, but no timestamp merge policies and no
  public surface.
- The first-seen/last-seen timestamp merges (Rust
  draft_store/timestamp_refresh.rs FirstSeenPolicy/LastSeenPolicy over
  range_merge::merge_coverage) are missing.
- The public structured cursor (Rust database.rs
  network_enrichment_v1_cursor_v4/v6) is missing: the internal reader
  has NetworkEnrichmentV1Cursor4/6 (internal/reader/
  structured_cursor.go) but cursors_public.go exposes no wrapper, so
  the structured property suite cannot assert canonical enrichment
  ranges.
- The Go validation surface is Milestone 4 scope; the structured
  property suite mirrors the Rust validate_clean integrity check with
  the public reader cross-check instead, recorded here.

Slices (disjoint write scopes, exact Rust baseline):

- A (internal/writer): timestamp_refresh.go with the timestamp merge
  result, FirstSeenPolicy and LastSeenPolicy (transform/observe/
  finish over mergeCoverage, reusing the feed projection for the
  comparison counts plus the input-address counter), DraftStore
  mergeFirstSeen/mergeLastSeen, and the WriterEdit bindings the public
  workflows need: AssignInputV4/V6 (direct replacement through the
  assignment input), AddPrivateConstantRangeV4/V6,
  FinishPrivateConstantRanges, MergeFirstSeen, and MergeLastSeen.
- B (public v4/go): direct_workflow_public.go with
  BeginDirectReplacement, BeginFirstSeenRefresh(refreshValue), and
  BeginLastSeenRefresh(refreshValue, cutoff) on Writer, the
  DirectReplacement/FirstSeenRefresh/LastSeenRefresh input handles
  (AddRangesV4Slice/AddRangesV6Slice, FinishInput), the exact Rust
  precondition/error classes (WrongValueKind/WrongValueTag/WrongState
  on family change and workflow kind, abort-after on input errors),
  and the shared FinishedWorkflow terminal with the replacement
  report.
- C (public v4/go cursors): the NetworkEnrichmentV1CursorV4/V6 public
  wrappers over the internal structured cursor, mirroring the existing
  public cursor patterns and the Rust database.rs surface.
- D (public tests): the six Go property suites mirroring the Rust
  tests with the same deterministic xorshift generators, domains,
  round counts, and scalar models, asserting through the public reader
  (lookups, pins, feed-range and enrichment cursors, algebra,
  aggregation, reports) after every round.

Recorded decisions (no open user decision): the direct replacement
input type reuses the public DirectRangeV4/V6 value records (the exact
Rust DirectRange value mirror); the timestamp refresh inputs use the
value-free AddressRange4/6; the no-change terminal discards the draft
and the changed terminal retires the base through FinishDirectWorkflow,
exactly like the feed workflow completion.

- Next: slice A, then B, then C, then D, then the five-aspect close
  gate like every chunk.

Slice D progress (2026-08-23): workflow_properties.rs parity is
implemented (direct replacement, first-seen, last-seen randomized
suites, 100 rounds each on the 128-address domain) and green. The
last-seen suite exposed a real Go port bug in the shared tree layer:
v4/go/internal/tree/delete.go RemoveLeafRun returned before applying
the physical page edit when a rejected record stopped the run
mid-leaf, while Rust remove_leaf_run scans first and then removes
[index, end). The Go version reported the rejected prefix without
removing it, corrupting both the tree (overlapping records retained)
and the record count (count underflow on the next rewrite). Fixed to
the Rust shape: scan the leaf, record the first rejected record as the
following edge, then apply the fixed-range removal for the accepted
prefix. The fix is Rust-exact, covered by the new randomized last-seen
and first-seen suites, and the full Go suite stays green.

### Status (2026-08-23) - chunk 3b-5 complete: feed-workflow slices A/B/C all gated PASS

Chunk 3b-5 (complete feed workflows, M3 surface) is complete: slice A
(draft machinery), slice B (public feed workflows), and slice C
(structured transaction) each closed with all five aspects PASS. The
carried P3 dispositions for this phase:

- P3-A (newEncodedRange per-record escape) landed and was fixed in
  slice A (rangeCtx encodeScratch, 0 allocs on the ordered path).
- P3-B (rejectCell/optionalCell on the cold general gap protocol)
  remains tracked: the general fallback keeps its recorded ~1.98
  allocs/record with no production caller beyond the union inputs;
  disposition at the randomized-models chunk where the general path
  gets its strongest exercise.
- P3-C (segmentAt by value) landed with the rangeTransform work in
  slice A (rangeRecord, bool, error return).
- Harvey's slice-C P3 note on slicea_union_input_alloc_test.go:93
  comment arithmetic was verified against the file: the comment states
  the measured 54 objects and the 54 + 256 = 310 arithmetic is
  consistent, so no fix was needed.
- The unpinned performance probes (OutputBuilder intern path, both-
  sides trim path; both measured 0 allocs/op) stay tracked as test-
  only observability gaps, not production defects.
- The feed-workflow Commit-after-abort error class and the other
  carried P3s from the slice-C gate stay tracked as recorded.

Full battery green under nice at the gated HEAD 2dc13aa.

- Next: chunk 3b-6 (randomized public models over the internal core,
  the remaining M3 plan item): Go property-model tests mirroring the
  Rust property suites (workflow_properties.rs,
  structured_value_properties.rs, membership_algebra_properties.rs,
  membership_query_properties.rs, feed_workflow_properties.rs,
  history_projection_properties.rs), then the five-aspect close gate.

### Status (2026-08-23) - slice C gated PASS: all five aspects green on 929d4de

The slice-C close gate closed with all five aspects PASS on 929d4de
(gate delta 8bb1daf..929d4de): Meitner (Rust parity), Anscombe (Go
idioms), Harvey (performance), Newton (wire/integrity), Aristotle
(APIs/docs/records). Round 2 returned Harvey FAIL (two findings) and
Meitner FAIL (one P1); all three were fixed at 7004a84 and re-reviewed
PASS; Aristotle's two P3 nits were fixed at 929d4de:

- Harvey P1 - residual internStructurePayload leak through the
  shape-stenciled generic on the real draft/output intern paths
  (48 B/op, 1 alloc/op): the builders now own a structureScratch and
  the payload travels through it; the pin was rewritten to drive
  DraftStore.internNetworkEnrichmentV1 and measures exactly 0 allocs.
- Harvey P2 - trimPredecessor heap rewrite (24 B/1 alloc per clear):
  the rewrite struct now carries its sides by value with hasLeft/
  hasRight presence flags (Rust Option<Rewrite> value semantics); new
  pin TestSliceCStructuredClearZeroAlloc measures exactly 0 allocs.
- Meitner P1 - op errors bypassed Rust mutate's abort_after: every
  edit-touching op on both transactions now spends the transaction and
  aborts through the writer (TransactionAborted wrapping the cause);
  the metadata stage classes Rust raises before mutate stay raw;
  Commit on a spent transaction reports NoPendingTransaction (Rust
  commit_attempt). Pins: TestPublicStructuredTransactionOpErrorAborts,
  corrected TestPublicMembershipTransactionSurface.
- Aristotle F1/F2 - the round-2 SOW next-step line now names the
  committed delta, and both exported Commit doc comments state the
  spent-transaction NoPendingTransaction contract.

Carried P3s (tracked, non-blocking, all test-only or intentional):
- Harvey: OutputBuilder intern path fixed but unpinned; both-sides
  trim path unpinned; slicea_union_input_alloc_test.go:93 comment
  arithmetic (58 + 256 vs measured 54).
- Meitner: metadataStagePreCheck is class-based rather than
  check-identity-based (exact for the current error inventory; a
  future WrongState/InvalidArgument inside the metadata edit would
  stay raw); zero-MembershipRef intern is deliberately stricter than
  Rust on a dead transaction (documented, intentional).
- Anscombe: the unreachable metadataWriteChain InvalidArgument would
  be misclassified raw if it ever fired (cannot fire: the compressor
  output is always non-empty and within bound); the two abortEdit
  helpers duplicate per file, consistent with the existing per-file
  checkOrAbort/requireActive split.
- Feed workflow Commit-after-abort reports WrongState where Rust
  PreparedWorkflow::commit reports NoPendingTransaction; pre-existing
  slice-B surface outside this delta, to resolve at the next gate.

Full battery green under nice at the gated HEAD: build, gofmt clean,
vet, plain/v4work tests, race, race+v4work, checkptr=2, six
cross-compiles, Rust suite and --all-features, Rust conformance
(11 fixtures), Rust mixed-subprocess, Go subprocess cross-open.

- Next: chunk 3b-6 (randomized public models over the internal core,
  the remaining M3 plan item); the chunk-3b-5 slice plan defines
  slices A/B/C only.

### Status (2026-08-23) - slice C review round 2: Harvey FAIL fixed on the working tree, four aspects green

The round-2 re-review at ba446fa returned: Anscombe (Go idioms) PASS,
Newton (wire/integrity) PASS, Aristotle (APIs/docs/records) PASS with
one P3 record nit, Harvey (performance) FAIL with two findings, Meitner
(Rust parity) FAIL with one P1 (op errors bypassed Rust mutate's
abort_after). All three findings are fixed and verified on the working
tree (uncommitted at the time of writing); the full battery is green
again under nice:

- P1 (Harvey) - residual internStructurePayload leak: the shape-
  stenciled generic internStructure still escaped its payload argument
  (48 B/op, 1 alloc/op measured on the real draft and output intern
  paths; the round-1 pin tested internStructure directly and missed
  it). The draft and output builders now own a structureScratch
  (draft_store.go, output.go, same pattern as recordScratch and
  rangeScratch) and internStructurePayload copies the payload into that
  scratch before the generic call (structure_draft.go, structured.go).
  TestSliceCStructureInternZeroAlloc rewritten to exercise the real
  path (DraftStore.internNetworkEnrichmentV1 with an empty membership
  handle) and measures exactly 0 allocations.
- P2 (Harvey) - trimPredecessor heap rewrite: the function returned
  *rewrite, so every clear/delete allocated 24 B and one object per
  operation. rewrite now carries its left and right rangeRecord sides
  by value with hasLeft/hasRight presence flags and trimPredecessor
  returns rewrite by value (range_edit.go; call sites
  rangeReplaceWithHint, trimFollowing, writeReplacement updated). This
  matches the Rust Option<Rewrite> value semantics. New pin
  TestSliceCStructuredClearZeroAlloc runs store.ClearV4 on an empty
  structured tree and measures exactly 0 allocations.
- P1 (Meitner) - op errors bypassed Rust mutate's abort_after: every
  edit-touching transaction op returned the raw edit error, so the
  draft survived an op failure and Commit could publish whatever
  partial mutation the failed op left behind; Rust discards the draft
  and reports TransactionAborted wrapping the cause (live_writer.rs
  mutate -> abort_after, live_writer/commit.rs commit_attempt). The
  structured and membership transaction ops now spend the transaction
  and abort through the writer on any error raised inside the edit,
  while the metadata stage checks Rust raises before mutate
  (already-staged WrongState and the 20 MiB InvalidArgument cap,
  live_writer.rs stage_metadata_json) stay raw and keep the
  transaction alive. Commit on a spent transaction now reports
  NoPendingTransaction like Rust commit_attempt (previously the
  inactive WrongState). Membership DeleteFeed keeps its epoch
  overflow check before mutate: Rust membership delete_feed computes
  next_epoch with checked_add before the mutate (membership.rs:340),
  matching the existing Go order.
  Pins: TestPublicStructuredTransactionOpErrorAborts (intern with
  out-of-range coordinates and rename onto an existing name both abort
  with TransactionAborted wrapping the cause; the transaction is dead
  and the writer clean). TestPublicMembershipTransactionSurface was
  corrected to the Rust-exact rename-onto-existing abort: it
  previously pinned the raw NameExists with a surviving transaction,
  which diverges from Rust (rename_current_feed runs inside mutate).
  Meitner's example of the double SetMetadataJSON was verified against
  Rust and kept raw: require_metadata_stage_available runs before
  mutate (live_writer.rs:290), so the existing WrongState pin is
  unchanged.
- P3 (carried, tracked) - the slice-B feed workflow Commit-after-abort
  reports WrongState (feed_workflow_public.go requireChangedActive)
  where Rust PreparedWorkflow::commit reports NoPendingTransaction
  after an op-failure abort; unpinned and pre-existing on a gated
  surface outside this delta's blast radius, to resolve at the next
  gate.
- P3 (Aristotle) - SOW record phrasing: this round-1 entry was
  reworded to name the commit (ba446fa) instead of pre-commit working
  tree phrasing, and the "next: commit this round" line now names the
  re-review gate, so the record reads correctly at HEAD.
- Full battery green under nice: build, gofmt clean, vet, plain/v4work
  tests, race, race+v4work, checkptr=2, six cross-compiles, Rust
  conformance (11 fixtures), Rust mixed-subprocess, Go subprocess
  cross-open.
- Next: the five-aspect re-review gate on the committed delta
  7004a84, then the chunk 3b-6 close per the M3 plan.

### Status (2026-08-23) - slice C review round 1: five-aspect FAILs fixed at ba446fa

The five-aspect slice-C review round (Meitner Rust parity, Anscombe Go
idioms, Harvey performance, Newton wire/integrity, Aristotle APIs/docs)
returned: Newton PASS; Meitner, Anscombe, Harvey, Aristotle FAIL. Every
finding is fixed and verified at ba446fa (delta 8bb1daf..ba446fa); the
full battery is green again under nice:

- P1 (Harvey) - internStructure interface escape: the shape-stenciled
  decodeStructureRecord payload escaped through the codec validate
  dictionary call (measured 96 B/op, 2 allocs/op before the slice-C
  generics, 48 B/op 1 alloc/op after, Rust 0). The payload validate now
  takes the payload by value, so no decode local has its address taken:
  0 B/op, 0 allocs/op on the dedup path. Permanent pin added
  (TestSliceCStructureInternZeroAlloc, writer package).
- P2 (Harvey) - per-op Mutate allocations: the transaction now binds one
  WriterEdit at begin (t.edit) and every operation routes through it,
  including metadata staging (WriterEdit.SetMetadata/ClearMetadata
  passthroughs), so no operation allocates a DraftStore or closure.
- P2 (Anscombe + Harvey) - assignment input family: inputV4/inputV6 are
  built with their literal families (Rust typed assignment inputs), so
  an IPv4 database carries no dead 256 KiB IPv6 locator.
- P2 (Meitner F-1 + Aristotle) - begin guard order: now the Rust exact
  sequence (closed-writer probe, structure-kind outer guard,
  cancellation, healthy, value-kind inner guard); a fired token on a
  non-structured database reports WrongStructureKind, not Cancelled
  (pinned in TestPublicStructuredTransactionSurface).
- P2 (Meitner F-2) - zero-handle MembershipRef mapping: only the literal
  zero MembershipRef is None; EmptyMembership and every other
  transaction-produced reference validate as Some, so a stale or
  foreign empty-handle ref is refused (pinned in
  TestPublicStructuredTransactionStaleEmptyMembership).
- P2 (Meitner F-3) - inter-drain checkpoint: PrepareWithCheckpoint now
  checkpoints between the structure and membership drains (Rust
  draft_store.rs:317).
- P3 (Meitner F-4) - pre-mutate checkOrAbort removed from
  intern/assign; the post-mutate check stays (Rust parity).
- P3 (Meitner F-5) - the delete-feed transform closure no longer checks
  per cell; removeFeedFromStructure checks once per present cell (Rust
  draft_store/structured.rs parity).
- P3 (Meitner F-6) - DeleteFeed epoch overflow now checks after mutate
  and checkOrAbort (Rust invalidate_memberships order).
- P3 (Anscombe) - single exported StructureHandle type (internal
  structureHandle double type removed); all 25 structurePayloadCodec
  functions converted to generics.
- P3 (Aristotle) - conformance README: corpus count 9 -> 11, the two
  Go structured fixtures listed, mixed-subprocess wording updated; the
  slice-C-implemented status entry no longer uses hidden-context
  phrasing.
- P3 (Aristotle) - the surface-test doc comment now states the actual
  coverage: the value-kind inner guard is unreachable through open
  because bootstrap refuses every kind combination that could carry a
  structure kind without the structured value kind (Go
  ValidateKindInvariants, Rust bootstrap.rs KindInvariant), so the
  guard is Rust-parity defense and the wrong-kind open class is
  FormatInvalid, pinned by the open tests.
- Full battery green under nice: build, gofmt clean, vet, plain/v4work
  tests, race, race+v4work, checkptr=2, six cross-compiles, Rust
  conformance, Rust mixed-subprocess, Go subprocess cross-open.
- Next: the round-2 re-review gate on this delta (all five aspects at
  ba446fa), then the remaining chunk 3b-5 work per the M3 plan.

### Status (2026-08-23) - slice B gated PASS: all five aspects green on 948aa93

Re-review round 2 closed: Meitner (Rust parity), Anscombe (Go idioms),
Harvey (performance), Newton (wire/integrity), Aristotle (APIs/docs) all
PASS at HEAD 948aa93, after the residue fixes committed at f397ebe and
948aa93:

- f397ebe: applyStructureDelta retire-leak fix (fresh clearedRetired,
  single retirement), prepare cancellation threading (Rust
  draft_store.rs:318), AddRangesV4/V6 abort-contract docs,
  requireActive stale-first ordering, BindEdit CodeNoPendingTransaction,
  SlottedInsert/TryPush/Finish InvalidArgument classes, zeroalloc
  bounded runtime-metadata window.
- 948aa93: alloc-ceiling comment arithmetic (54 + 256 = 310) and the
  zeroalloc metadata window now falls through to the continuation pin so
  the exact 0-object/0-byte assertion runs on every invocation.
- Carried P3s (tracked, non-blocking): comparison.go
  ScannedComparison.Comparison field stutter; noopCheck vs
  noopCheckpoint package naming; leaf-re-inspect after COW first-touch
  (tree/path.go) documented retained cost.
- Full battery green under nice at the gated HEAD: build, gofmt clean,
  vet, plain/v4work tests, race, race+v4work, checkptr=2, six
  cross-compiles.
- Next: slice C (internal/writer structured apply + public
  StructuredTransaction).

### Status (2026-08-23) - slice C implemented: StructuredTransaction surface, structured writer apply, Go structured fixtures

Slice C (the internal/writer structured apply and the public
StructuredTransaction) is implemented and green; the close gate (five
aspects on this delta) is next. The implementation is committed at
8bb1daf; the review-fix round below is the current working tree on top
of it:

- New public surface (v4/go/structured_transaction_public.go): the
  advanced transaction over a clean writer (Rust live_writer/structured.rs
  parity) with StructureRef pinning (database id, operation nonce,
  reference epoch), feed/membership catalog reuse (EnsureFeed,
  EmptyMembership, AddFeed, LookupFeed, RenameFeed, DeleteFeed), typed
  interning (InternNetworkEnrichmentV1 with lazy threat membership,
  dedup by payload), assign/clear v4/v6 with family and ordered-range
  guards, metadata staging with the Rust wrong-state second-set
  contract, and Commit/Abort that spend the transaction and its
  references.
- New writer core (v4/go/internal/writer/structure_draft.go): the
  structure draft (intern with payload encode + structure refcounts +
  membership owner refcounts, assign/clear through the assignment
  input, delete_current_structured_feed with remove_feed_from_structure
  payload re-intern, finish_structure_deltas with structure_dictionary
  apply_delta + released-membership accounting), plus workflow bindings
  (WriterEdit) and the structure dictionary intern counter parity
  (work.StructureIntern).
- structure_codec.go now patches membership bytes in place (Rust
  with_membership parity) instead of decode/re-encode, so a corrupt
  flags byte errors instead of being silently masked.
- range_draft.go structured RangeRecordAdded/Removed now route through
  trackStructureRefcount (Rust parity); draft_prepare.go threads the
  caller's checkpoint through finishStructureDeltasWithCheckpoint
  (nil-safe, Rust draft_store.rs parity).
- Tests: v4/go/structured_transaction_public_test.go (4 tests mirroring
  Rust structured_values.rs: reference pinning, abort/dedup/release/
  reuse clean graph, metadata wrong-state, snapshot of the committed
  graph) and v4/go/structured_transaction_v4work_test.go (work-counter
  pins: intern attempts, dedup, assign/clear silence, delete-feed
  re-interns).
- Rust cross-open evidence: two Go-produced structured fixtures
  (structured-ipv4, structured-ipv4-nothreat) regenerated from the exact
  Rust generator op sequences (v4/go/conformance_generate_test.go),
  added to v4/conformance/cases.json, verified by the Go conformance
  suite, by the Rust conformance suite (all 11 manifest fixtures), and
  by the Rust mixed-subprocess smoke (typed lookups, feeds, cleared
  hole, no-threat absence).
- Full battery green under nice: build, gofmt clean, vet, plain/v4work
  tests, race, race+v4work, checkptr=2, six cross-compiles, Rust suite
  580 passed 0 failed.
- Next: the five-aspect close gate on this delta (Meitner, Anscombe,
  Harvey, Newton, Aristotle), then the remaining chunk 3b-5 work per
  the M3 plan.

### Status (2026-08-23) - slice B review-fix round 2: re-review verdicts and residue fixes

First fix round committed at c1fd96b; all five aspects re-reviewed. Harvey
(performance) PASSED; Meitner (Rust parity), Anscombe (Go idioms), Newton
(wire/integrity), Aristotle (APIs/docs) found residue, all fixed and
re-verified:

- P1 (Anscombe + Newton + Meitner, same finding): applyStructureDelta
  still double-retired the hash-delete list and discarded the
  ClearUsed accumulator. Now mirrors finishMembershipRemoval exactly:
  fresh `clearedRetired` accumulator, retired once; the second
  RetirePages(retired) is gone. The in-memory test store silently
  absorbed duplicates, masking the fault; the real DraftStore path
  deterministically failed with "page is already retired".
- P2 (Meitner + Anscombe): PrepareWithCheckpoint passed noopCheck into
  finishMembershipDeltasWithCheckpoint, dropping the caller's
  cancellation for the whole drain. Now threads the caller checkpoint
  (nil-safe), matching Rust draft_store.rs:318.
- P2 (Aristotle): CreateFeed/ReplaceFeed AddRangesV4/V6 doc contracts
  updated: reversed ranges and family mismatches both abort the
  workflow, observed as ErrorTransactionAborted wrapping the cause.
- P3 (Meitner): requireActive reports the stale transaction before the
  closed writer (nonce probe guarded by the core nil check), matching
  Rust require_transaction order; BindEdit now uses
  CodeNoPendingTransaction like every other no-draft site; SlottedInsert
  and TryPush empty-cell and Finish empty-page errors now use
  CodeInvalidArgument like Rust slotted_page.rs, not HeaderError.
- P3 (Aristotle): addRanges4/6 comments now include the trailing
  post-final-batch checkpoint.
- P3 (Harvey): alloc-ceiling test comment corrected to the measured 54
  objects.
- Test determinism: the fresh-workflow first-batch pin (TotalAlloc == 0)
  flaked once on a 16-byte Go runtime one-time metadata entry that
  Rust's thread-local counter structurally cannot see. The pin now
  accepts a bounded 64-byte runtime-metadata window with a size-class
  breakdown in the failure report; the 50 continuation batches remain
  pinned at exactly 0 objects and 0 bytes, so per-batch user-code leaks
  cannot hide.
- Full battery green under nice: build, gofmt clean, vet, plain/v4work
  tests, race, race+v4work, checkptr=2, six cross-compiles.
- Next: commit this round, send the residue delta to the four FAIL
  reviewers, close slice B only when all five aspects PASS, then
  slice C.

### Status (2026-08-23) - slice B review-fix round: five-aspect FAILs fixed

The five-aspect slice-B review round (Meitner Rust parity, Anscombe Go
idioms, Harvey performance, Newton wire/integrity, Aristotle APIs/docs)
returned FAIL on every aspect; all findings were fixed and the full
battery is green again:

- Retirement double-free (pre-existing, Newton-class integrity bug):
  finishMembershipRemoval (membership_dictionary.go) and
  applyStructureDelta (structure_dictionary.go) reused the hash-delete
  `retired` list for bitmap.ClearUsed, so RetirePages retired the same
  page twice ("page is already retired"). Rust clear_used_id builds a
  fresh RetiredPages; Go now uses a fresh `clearedRetired` list in both
  call sites.
- PrepareWithCheckpoint Rust parity (Meitner F-3): membership deltas
  are now drained at prepare (draft_prepare.go), matching Rust; this
  exposed the TestWorkflowStateMachine plain-transaction section, which
  assigned a raw value on a membership core (id never interned). The
  plain-transaction gates now run on a direct value core, preserving
  the membership workflow gates on the original core.
- Zero-alloc flake root cause (Harvey performance + reliability): Go
  1.26's probabilistic type-assert cache (48-byte allocation at
  ~1/1024 type asserts) fired on the per-record RangeStore -> tree.Store
  interface conversion in UnionInput.pushOrdered. rangeCtx now carries
  a concrete storeView set once in beginRangeEdit, removing the
  per-record conversion (60/60 flake attempts clean; also a real
  hot-path win matching Rust, which passes the concrete store).
- Entry-point hygiene (Anscombe P2-1): beginRangeEdit/commitRangeEdit
  helpers now own the range root/count/scratch/untracked reset at every
  entry point; missed-reset class bugs are impossible by construction.
- Test parity (Newton): TestPublicRenameDeleteReuseCommittedIndex must
  call del.ClearMetadataJSON() before commit; metadata is not
  auto-cleared by delete, matching the Rust vector.
- Harness cleanup: debug.PrintStack instrumentation removed from
  draft_store.go; range_bulk.go confirmed untouched (empty diff).
- Full battery green under nice: go build, gofmt clean, go vet,
  go test -count=1 ./..., go test -count=1 -tags v4work ./...,
  go test -count=1 -race ./..., go test -count=1 -race -tags v4work
  ./..., GODEBUG=checkptr=2 go test -count=1 ./..., and six
  cross-compiles (linux/386, linux/arm, linux/arm64, windows/amd64,
  darwin/arm64, freebsd/amd64).
- Next: commit this batch, send the delta brief to all five reviewers,
  close slice B only when all aspects PASS, then slice C.

### Status (2026-08-22) - slice B zero-alloc completion: feed slice ingestion reaches Rust parity (0 allocations per batch)

The slice-B public feed workflow batch (uncommitted work on top of
7a71f79) was completed and validated end to end:

- Zero-alloc refactor of the feed slice path (Rust
  slice_ingestion_and_feed_comparison_allocate_nothing_per_record parity):
  per-record allocations dropped from 3011 to 0 mallocs per 1000-record
  ordered batch, and the first batch on a fresh workflow now measures
  0 mallocs / 0 bytes (MemStats delta), matching the Rust thread-allocation
  pin exactly.
- Root causes eliminated:
  1. Per-record rangeCtx encode-scratch escape: the range context now
     lives on the DraftStore (rangeCtx + rangeScratch fields), reset at
     every entry point (range_draft.go x5, feed_merge.go x2,
     membership_draft.go x1).
  2. parse() pointer-to-local escape: expectedLevel is now passed by
     value with a checkLevel bool across internal/tree (page.go,
     delete.go, gap.go, insert.go, path.go, read.go, walk.go, cursor.go
     and the tree tests), removing the per-tree-descent allocation in
     privatePathSelect.
  3. Ordered builder heap allocation: the coverage ordered prefix now
     embeds rangeBulkBuilder by value (range_coverage.go orderedPrefix),
     exactly like the Rust OrderedPrefix variant holds Builder.
  4. Per-batch core-binding allocations (DraftStore, WriterEdit, and
     two operation closures per Mutate call): Core.BindEdit binds one
     draft-lifetime WriterEdit, exactFeedWorkflow holds it, and
     addRanges4/addRanges6 stream with plain loops (no closures),
     preserving the Rust drain_source accounting (one source pass, one
     input-source pass, cancellation checkpoints per 4096-record chunk,
     one consumed charge per record).
- Test corrected: TestZeroAllocationFeedSliceIngestion previously
  re-added the same ranges, wrapping the ascending stream, proving the
  input unordered and charging the general input's one-time locator
  (a legitimate design cost, not a leak). It now warms once and
  measures 50 strictly ascending continuation batches (the Rust test
  shape: the ordered prefix never wraps), asserting exactly 0 objects
  per batch.
- Also fixed: internal/tree/gap_work_test.go (v4work build) still used
  the pre-refactor generic acceptGap[wideLeaf]; updated to the
  non-generic acceptGap after the value-semantics refactor.
- Full battery green on the working tree (all under nice): go build,
  gofmt clean, go vet, go test -count=1 ./..., go test -count=1 -tags
  v4work ./..., go test -count=1 -race ./..., go test -count=1 -race
  -tags v4work ./..., GODEBUG=checkptr=2 go test -count=1 ./..., and
  six cross-compiles (linux/386, linux/arm, linux/arm64,
  windows/amd64, darwin/arm64, freebsd/amd64).
- Next: commit this batch, then run the five-aspect slice-B review
  round on the committed HEAD (reviewers: Meitner, Anscombe, Harvey,
  Newton, Aristotle), fix any P0-P2, then slice C.

### Status (2026-08-22) - chalk: slice B (public feed workflows) starts

Slice A (internal draft machinery) is gated PASS at HEAD b80295a /
da1e769 by all five aspects. Slice B implements the public v4/go
surface over the existing draft machinery, Rust baseline
live_writer/{feed_workflow,feed_lifecycle,membership,workflow}.rs,
feed.rs, feed_catalog.rs, workflow.rs, writer_core/{edit,core}.rs,
draft_store/{feed_merge,membership}.rs:

- Internal writer additions: base-meta feed lookup
  (Core.LookupBaseFeed), current-meta feed lookup and enumeration
  (Core.LookupCurrentFeed, feed cursor over the index tree),
  MembershipOperation apply (DraftStore.applyMembership over the
  existing rangeTransform + combineMemberships), feed membership
  delete (deleteCurrentFeedMembership), full-map comparison sweep
  (Core.CompareMaps, Rust workflow/compare.rs), the WriterEdit feed
  bindings, and a nonce-returning begin_transaction (Core.BeginTransaction).
- Public surface: FeedName validated binding, CreateFeed/ReplaceFeed
  (BeginCreateFeed/BeginReplaceFeed, AddRangesV4/V6(+Slice),
  FinishInput), FinishedWorkflow/PreparedWorkflow single Go handle
  (DirectTransaction precedent), PreparedFeedChange for
  RenameFeed/DeleteFeed, MembershipTransaction with
  FeedRef/MembershipRef/TransactionFeedCursor, and the public
  MembershipOperation enum.
- Tests: mirror Rust feed_workflow_tests.rs (three vectors with exact
  work counters and zero allocations), the membership transaction
  surface semantics (references, epochs, apply/ensure/rename/delete,
  cursor, metadata, commit/abort), work-counter and zeroalloc pins,
  and Rust cross-open at the close gate.

No open user decisions: the exact-feed workflows mirror the Rust public
API one-to-one (records: 2026-08-22 chunk-3b-5 entry), and the Go
handle-collapse precedent (DirectTransaction / FinishedHistoryProjection)
applies to FinishedWorkflow.

- Next: implement chunk B1 (internal writer), run the pinned battery,
  commit, then chunk B2 (public surface), then chunk B3 (tests) and the
  five-aspect close gate.

### Status (2026-08-22) - slice A round-6 cleanup: all five aspects PASS on b80295a

Round-5 residue landed on top of f1cedb6 and re-validated end-to-end
(HEAD b80295a):

- unionRun's duplicated 5-line insert_private_rejected comment was
  removed (range_coverage.go:677-679 retains the single copy).
- The F1 record now states the fits=false split path is not observed
  in the current test vectors (30/30 measured fits=true) instead of
  "currently unreachable", since growth splitting and rejected-gap
  slack are independent (see the F1 note for why no regression test
  pins the branch).
- Error-detail parity sweep completed: every Go Code*Exhausted
  production site now matches the Rust Display strings -
  PageSpaceExhausted "v4 page-number space is exhausted"
  (draft_store.go:468, output.go:686, range_bulk.go:356),
  MembershipIdExhausted "membership-ID space is exhausted"
  (membership_dictionary.go:140), StructureIdExhausted
  "structure-ID space is exhausted" (structure_dictionary.go:37),
  FeedIndexExhausted already exact (feed_catalog.go:103). Wire codes
  unchanged.

Full battery re-run on b80295a (all under nice): gofmt clean, go
vet, go test ./... and -tags v4work (both -count=1 and -race),
GODEBUG=checkptr=2, and the six GOOS/GOARCH cross-builds - all green.
Rust suite untouched.

All five aspects re-reviewed b80295a: PASS (Rust parity, Go idioms,
performance, wire format/integrity, APIs/docs). No open findings;
slice A remains gated PASS at this HEAD.

### Status (2026-08-22) - slice A parity-residue round: three unrecorded Rust-parity findings fixed (APIs/docs FAIL)

The APIs/docs re-review (HEAD b9ea00b) failed on records accuracy:
three Rust-parity findings from the first slice-A review were never
recorded in the SOW and remained open in code. All three were verified
against the code and the Rust baseline, and fixed:

- **F1 (unionRun position propagation).** unionRun ignored the fit
  result of insertPrivateRejected and returned hasPosition=true
  unconditionally; Rust union_run returns the insert_rejected_gap
  Option<PrivatePosition>, None when the rejected leaf must split
  (range_mutation.rs:131-143, coverage.rs:451-452). The Go split path
  returns a zero PrivatePosition, so the next edge-proven insert would
  raise a spurious "cached B+tree position is not its claimed edge".
  Fix: the fit flag now propagates (range_coverage.go). Reachability
  evidence: the fits=false split path is not observed in the current
  test vectors (30/30 probe rejects in a 4,000-record general tree
  measured fits=true), but it is not structurally unreachable: growth
  splitting and rejected-gap slack are independent. The fix guarantees
  the split path caches nothing, exactly like Rust, removing the
  latent landmine at zero cost. No unit test pins the fits=false
  branch: a fabricated full-header LocalReject would corrupt a real
  tree (EditLeaf writes the page), and Go and Rust share the same
  stale-edge corrupt guard, so the pin would only re-test shared code.
- **F2 (flushed edge lifetime).** finishPrivateUntracked cleared the
  cached edge after FlushEdge; Rust finish_private flushes via
  as_mut and keeps the edge (coverage.rs:233-242), so a later
  edge-proven insert reuses it instead of descending again. Fix: the
  flush keeps the edge (range_coverage.go). New v4work pin
  TestWorkUnionInputFlushKeepsEdge binds the invariant; proven
  binding by temporary bug reintroduction (pre-fix code fails it).
- **F3 (error-string parity).** pageSpaceExhausted detail was "range
  tree page space exhausted"; Rust displays "v4 page-number space is
  exhausted" (sdk_error.rs:356; wire code 25 identical). Fix:
  range_bulk.go matches the Rust Display string.

Validation (all under nice): gofmt clean, go vet, go test ./... and
-tags v4work (both -count=1 and -race), checkptr=2, and the
linux/386, linux/arm, linux/arm64, windows/amd64, darwin/arm64 and
freebsd/amd64 cross-builds - all green. Rust suite untouched.

### Status (2026-08-22) - slice A records-accuracy round: full battery on the committed HEAD, P3 hardening landed

The five-aspect re-review of HEAD c656b71 (the untracked-accounting
fix) returned four PASS and one FAIL:

- PASS: Rust parity, Go idioms, performance, wire format/integrity.
  The P1/P2 fixes were verified; the exact splice counters of the Rust
  vector hold in Go (coalesced 2000, tree lookups 8, cell probes
  < 2200).
- FAIL (APIs/docs, records accuracy only): the round entry claimed the
  full validation battery (go test ./... and -tags v4work with
  -count=1 and -race, checkptr=2, six cross-builds) without a recorded
  run on that HEAD. The full battery was re-run on this HEAD
  (b9ea00b) and the entry now matches what was executed; the P1 label now
  names the defect class (untracked accounting / Rust parity) with the
  reviewing aspect in parentheses, the coverage file is referenced by
  its full path, and the measured allocation baseline is stated as
  ~565.

Actionable non-blocking notes from the four PASS aspects were landed
in the same delta: rangeRecordAdded documents its end-to-end
accounting contract; the splice regression test also asserts that no
membership id other than the sealed feed is charged (pending slots
hold only member.id and the delta tree root stays 0); a new v4work
counter twin pins the Rust splice vector exactly
(TestWorkUnionInputSpliceLargeRun); and the rangeCtx.untracked field
comment warns that a marked context must never be reused for a
tracked operation.

### Status (2026-08-22) - slice A second reviewer round: one P1 and two P2 verified and fixed

The aspect-reviewer round on the encode-scratch fix (HEAD 4bd7f82)
returned three more findings, all verified against the tree:

- **P1 (defect class: untracked-accounting / Rust parity; found by
  the Performance-aspect reviewer): insert() bypassed the new
  untracked flag.** The wrapper removal converted every per-record
  accounting site except insert(), which still called
  ctx.store.RangeRecordAdded directly; on the marked-untracked
  coverage path the union-run splice (unionRun -> insert,
  v4/go/internal/writer/range_coverage.go:725) charged a spurious
  membership refcount delta into the draft (the old
  untrackedRangeStore wrapper made it a no-op). Verified end-to-end with the Rust
  private_constant_union_splices_a_large_run... vector through the
  empty-map feed: before the fix the member delta was +2 (splice +1,
  sealed count +1), after the fix exactly +1. Fix: insert() routes
  through rangeRecordAdded; replaceStrictlyInside charges through the
  new storeRecordAdded helper; regression test
  TestFeedMergeEmptyMapSpliceStaysUntracked binds the invariant.
- **P2: encodeRecord had no slot bounds check** (range_edit.go) - a
  derived slot would panic instead of returning the library error
  class. Fix: range-encode slot index validated against the scratch
  array, corrupt error on violation.
- **P2: the general-path alloc ceiling was loose enough to admit one
  new per-record allocation** (826 for 256 records allowed 3.0/record
  vs the 1.98 baseline). Fix: ceiling tightened to 800 (baseline ~565;
  a +1/record regression lands at ~820). The slope-based alternative
  was measured and rejected: the generic gap machinery allocates
  non-linearly (2.89/record at 512 records vs 1.98 at 256), so the pin
  uses one fixed measurement with a documented arithmetic margin.

Validation (all under nice): gofmt clean, go vet, go test ./... and
-tags v4work (both -count=1 and -race), checkptr=2, and the
linux/386, linux/arm, linux/arm64, windows/amd64, darwin/arm64 and
freebsd/amd64 cross-builds - all green. Rust suite untouched.

### Status (2026-08-22) - slice A five-aspect reviewer round: one P2 verified and fixed (per-record encode escape)

The first five-aspect review of the slice A delta (HEAD c2ccd50)
returned one verified blocker:

- **Aspect 2 (Go idioms) FAIL - P2: the carried P3-A (newEncodedRange
  heap-escape per range-record edit) became live in slice A.** The
  coverage-union and feed-merge machinery added live callers of
  `newEncodedRange` on the per-record path (range_coverage.go
  insertPrivateEdge/mergeRejected, range_locator.go probeCached,
  range_edit.go insert/insertPrivateGap/insertPrivateRejected/
  replaceStrictlyInside), while the milestone-1 close record required
  the writer-owned encode scratch to land exactly where callers become
  live. Measured before the fix (fresh-state AllocsPerRun, warmup
  cancelled): ordered-prefix path 1.00 alloc/record, general fallback
  6.97 allocs/record.

Fix applied on top of c2ccd50:

- `rangeCtx` owns a fixed-size encode scratch
  (`encodeScratch [3][format.RangeRecordV6Size]byte`) and the new
  `rangeCtx.encodeRecord(slot, r)` method renders one record into a
  slot, mirroring the Rust `EncodedRange::new` local `[u8; 36]`; the
  generic `rangeFamily` interface otherwise makes stack targets escape
  (range_edit.go). Slot 0 serves the one-record paths; slots 0..2
  serve the up-to-three-cell leaf replacement (Rust
  replace_strictly_inside). `newEncodedRange`/`encodedRange` removed.
- `rangeCtx` gained the `untracked` accounting flag replacing the
  per-record `untrackedCtx` wrapper context of the coverage input
  (`rangeCtx.markUntracked`, range_coverage.go): the same-slice hot
  path allocated one wrapper context per record (the largest remaining
  allocation in the post-fix profile).
- New permanent pins `TestSliceAOrderedUnionAllocCeiling` and
  `TestSliceAGeneralUnionAllocCeiling` (slicea_union_input_alloc_test.go)
  fail if a per-record allocation returns.

Measured after the fix (same harness): ordered-prefix path 0.00
alloc/record, general fallback 1.98 allocs/record. The remaining
general-path cost is the generic tree gap protocol (rejection,
probePredecessor, privatePathSelect escapes), which is the recorded
P3-B optionalCell follow-up of the milestone-1 close gate; it stays
tracked for its slice C slot rather than being redesigned inside this
fix.

Validation (all under nice): gofmt clean, go vet, go test ./...
and -tags v4work (both -count=1 and -race), checkptr=2, and the
linux/386, linux/arm, linux/arm64, windows/amd64, darwin/arm64 and
freebsd/amd64 cross-builds - all green. Rust suite untouched
(Go-only edits, no conformance corpus change; re-run at the close
gate).

### Status (2026-08-22) - slice A union-input tests: correctness and work pins, plus one merge-cardinality bug

The slice A feed-merge and coverage-union machinery is now pinned by
focused internal tests mirroring the Rust vectors, all running over the
real opened mapping (no owned page exists anywhere):

- `slicea_feed_merge_test.go` (fixed and extended): add-feed-to-membership
  interning and its corrupt classes (draft_store/membership.rs +
  membership_dictionary.rs), rename/remove current-feed lifecycle
  (feed_catalog.rs, hole reuse, NameExists class), the empty-map feed
  trio (begin/add/finish) including its non-empty-tree guard, the exact
  no-change result of a created feed with empty coverage, and the
  created-feed merge vector over the committed destination with bit
  probes on the interned union records.
- `slicea_union_input_test.go` (new): the Rust range_mutation_tests.rs
  correctness vectors - random-order buffered union matching a
  512-address scalar reference (2,000 LCG operations), the pending-gap
  rebridging vector [(35,45),(15,32),(30,38)] -> [(15,45)], the
  2,000-key queue-normalized single interval, the late-overlap general
  fallback in both address families, and the unordered empty-map feed
  over overlapping ranges.
- `slicea_union_input_work_test.go` (new, v4work build tag): the Rust
  work::measure vectors - queue normalize (emitted 1 / coalesced 1999 /
  lookups 0), ascending packed construction (lookups, splits and output
  passes all zero; ordered count 4000), monotonic edges (lookups equal
  splits, exactly one edge-path check, structural fence bound), random
  order lookup bound, the 20,000-input leaf-locator hint hit rate, and
  the 100,000-input IPv4/IPv6 assignment locator split (hits > N/3,
  no hint work for IPv6). Total v4work runtime of the new pins: ~0.6 s.

One real bug was found and fixed by these tests: `newFeedPolicy` built
the feed policy with its own family set but never initialized the
embedded `feedProjection.family`, so every created-feed merge over an
IPv4 base counted segments as IPv6 (the projection fell into the IPv6
cardinality branch). `newFeedPolicy` now fixes the projection family at
construction exactly like the history plan (history.go), mirroring the
Rust generic FeedPolicy<K> whose family is fixed at compile time. The
merge vector test's original expected values (added 11 / removed 23 /
four member-only records) were wrong for the real FeedPolicy semantics;
the corrected vector asserts added = covered cardinality (12), removed
0, the seven canonical segments, and the union bitmaps by feed-bit
probes.

Validation (all under nice): gofmt clean, go vet, go test ./... and
-tags v4work (both -count=1 and -race), checkptr=2, and the
linux/386, linux/arm, linux/arm64, windows/amd64, darwin/arm64 and
freebsd/amd64 cross-builds - all green. Rust suite untouched (Go-only
edits, no conformance corpus change; re-run at the close gate).

- Next: slice A open items before the feed workflow surface (slice B):
  confirm with the five-aspect reviewers on this delta, then continue
  the feed lifecycle and workflow-range draft fields.

### Status (2026-08-22) - five-aspect close gate: 2 FAIL verdicts, both fix batches applied

The five-aspect adversarial close gate (lead-model reviewers, final-review
skill, Rust baseline) ran over the chunk 3b-4 delta
`11003d8..925e114` (allocation pass 5,169 -> 39 allocs/run, slices A+B+C,
corpus extension). Verdicts and fixes:

- Aspect 5 (APIs/docs, Aristotle): PASS. 4 P3s, actionable ones fixed in
  `db1307a` (FeedName doc, fixture comment, SOW commit-state wording,
  commit-path alloc pin).
- Aspect 3 (performance, Harvey): PASS. 3 P3s; the commit-path allocation
  pin and AllocsPerRun(4) ceiling landed in `db1307a`; carrying a
  wall-time benchmark was recorded as a post-close bench item, not a
  blocker (test-only necessary-work counters already pin work).
- Aspect 4 (wire/integrity, Newton): PASS. P2.1 the gate commit moved
  while reviewing (re-affirmed below); P3.1 short membership blob reads
  now fail closed instead of trimming (`d9c00cc`).
- Aspect 2 (Go idioms, Anscombe): FAIL -> fixed in this batch.
  - P2-1 per-lookup heap escape in findMembership: membershipFound
    now stores tree.LeafLocation by value plus a located bool
    (membership_dictionary.go); `-gcflags=-m=2` no longer reports the
    location escape. (Same defect as Aspect-1 F1.)
  - P2-2 history prefix panic: historyPrefixWordCount now returns
    corrupt("nonempty history prefix has no feeds") like Rust
    Error::Corrupt, and prefixWords carries its word count so
    WordCount() does not re-scan (history.go).
  - P3-3 retire.go predecessor/atOrAfter returned pointers to locals;
    the retire package now returns (Extent, bool, error) through
    First/After/scanGroup/classify/applyPage (retire.go, draft_prepare).
  - P3-4 writer range_edit.go readPredecessor/readAtOrAfter returned
    pointers; now (rangeRecord, bool, error) through segmentAt,
    rangeReplaceWithHint, trimPredecessor, trimFollowing,
    mergePrevious/Next.
  - P3-5 RetiredPages API shape kept as-is: the tree returns the fixed
    array by value and the bitmap dictionaries accumulate in place
    because Rust passes RetiredPages by &mut and Go mirrors both
    shapes; both are allocation-free (verified with -gcflags=-m=2).
  - P3-6 all 23 `&4095` cancellation cadences in writer and reader are
    now one generic checkEvery[T integer] helper per package
    (membership_algebra.go, history.go, membership_query.go and every
    reader loop); the cadence and the nil-check are defined once,
    identical to Rust CancellationToken::check cadence.
  - P3-7 tree.LocalReject/LocalInsert/EdgeInsert became generic
    (rangeRecord, u32 codecs); the any-boxed neighbor cells are now
    typed values, removing the per-rejection interface boxing and the
    caller-side type assertion (gap.go, range_edit.go).
  - P3-8 feed-name validation moved to the plan boundary only, like
    Rust FeedName::new: collectHistoryWindows refuses invalid window
    names with CodeNameInvalid (pinned by sliceb_history_test.go) and
    the internal catalog operations document the pre-validated input
    contract with the encoder's corrupt guard as the caller-bug backstop
    (feed_catalog.go, catalog_codec.go); feed_catalog_test.go was
    updated to pin the new layering.
- Aspect 1 (Rust parity, Meitner): FAIL -> fixed in this batch.
  - F1 per-lookup allocation in findMembership: same fix as Anscombe
    P2-1.
  - F2 corrupt-path detail mismatch: findMembership now returns
    located=false and each caller attaches its Rust detail; the
    membership-algebra path emits corrupt("range membership ID is
    missing") exactly like Rust algebra.rs stored_word_count, the
    other paths keep "membership ID is missing"
    (membership_dictionary.go, membership_algebra.go).
  - F3 history prefix panic: same fix as Anscombe P2-2.
  - F4 empty-catalog TreeLookup counter: reader LookupFeed and
    LookupFeedIndex now charge work.TreeLookup before the absent-root
    shortcut, mirroring Rust fixed_tree::query (catalog.go, index.go).
  - F5 membership blob-root encode branch: newMembershipEncoded now
    returns corrupt("membership blob root is invalid") for blob roots
    below 2 before the invalid-argument limit check, exactly like Rust
    codec::Encoded::new (membership_codec.go).
- Recorded parity decision (counter granularity): Go charges
  work.BytesMoved at store-layer logical operations (tag, marker, seal
  stamps; draft_work_test pins 16/8/32) while Rust charges per mapped
  write; Rust asserts no bytes_moved totals anywhere, so no
  cross-language counter total is pinned or diverged. The counters are
  test-only no-ops; both sides pin the same per-operation invariants.
  Accepted as documented, no code change.

Validation of the fix batch (all under nice): go test ./... and -tags
v4work (both -count=1 and -race), go vet, gofmt clean, checkptr=2,
GOOS windows/amd64 + darwin/arm64 + freebsd/amd64 + linux/arm64 builds,
the two allocation ceilings (39 and 84/run, unchanged), the Rust suite
and --all-features - all green. Delta for the re-review pin:
`11003d8..<final commit of this batch>`.

- Next: re-review of the fix batch by Aspect 2 (Anscombe) and Aspect 1
  (Meitner) on the pinned commit; when all five report no P0-P2, close
  milestone 1 (push), then start the next M3 surface (feed workflows).

### Status (2026-08-22) - fd171b6 re-review residue fixed: parity and 32-bit cross-build batch

The Aspect-1 (Meitner) and Aspect-2 (Anscombe) re-reviews of
`11003d8..fd171b6` came back with verifiable residue; the Aspect-1 items
were each verified against the Rust source before editing. This batch
fixes all of them, applies the two remaining P3 dispositions, and pins
the new HEAD for the final re-review round:

- F-A: equalMembershipWords corrupt detail now reads
  "membership hash points to a missing ID", exactly like Rust
  membership_dictionary.rs stored_word_count
  (membership_dictionary.go).
- F-B: findMembershipBlobLeaf no longer allocates per tree level; the
  expected level is a value plus a set flag, mirroring Rust Option<u16>
  by value (membership_blob.go; verified no `new(uint16)` escape at
  -gcflags=-m=2).
- F-C: DraftStore.lookupFeed charges work.TreeLookup(1) before the
  absent-root shortcut, matching Rust fixed_tree::query cadence
  (feed_catalog.go).
- F-D1: the unreachable "membership inline record lost its leaf
  location" branch was deleted; the located flag is guaranteed by the
  found-record invariant, and Rust has no such branch
  (membership_dictionary.go).
- F-D2: pushMembershipBlobBranch propagates the raw b.Push error
  instead of wrapping it, matching Rust blob write branch propagation
  (membership_blob.go).
- P1-1 (Anscombe): prepareHistoryPlan compares uint64(windowCount)
  against math.MaxUint32 instead of the raw int, which did not compile
  on 32-bit targets (GOOS=linux GOARCH=386/arm now build)
  (history.go).
- P3-1: the writer's last pointer-Option residue, range_edit.go
  change/segment/writeReplacement and the transform operation, now
  carry optionalValue{value, present} by value like the ordered-merge
  hot path already did (range_edit.go, range_edit_test.go). This also
  removes the per-call &value escape when the compiler does not inline
  the assignment entry points.
- P3-4: tree/gap.go carried two byte-identical neighbor-cell structs;
  merged to one rejectCell type (Rust LocalReject stores a single
  Option<(usize, R)> shape), removing the needless duplicate.
- P3-2 kept by decision: rangeTransform has no production caller yet
  (Rust callers draft_store/membership.rs and structured.rs are the
  next milestone slice); it is the Rust-parity foundation for that
  slice, so it stays under test instead of churning the gate delta.
- P3-3 kept by decision: RetiredPages returns by value from the tree
  while the bitmap dictionaries accumulate in place, mirroring Rust
  &mut vs owned shapes; both forms are allocation-free (verified at
  -gcflags=-m=2).

Validation (all under nice): go test ./... and -tags v4work (both
-count=1 and -race), go vet, gofmt clean, checkptr=2, GOOS/GOARCH
linux/386, linux/arm, linux/arm64, windows/amd64, darwin/arm64 and
freebsd/amd64 builds, and the two allocation ceilings (39 and 84 per
run, unchanged) - all green. The Rust suite is untouched by this
batch (Go-only edits; no conformance corpus change).

- Next: re-review of this batch by Aspect 1 (Meitner) and Aspect 2
  (Anscombe) on the pinned commit (the commit this entry ships in);
  when all five report no P0-P2, close milestone 1 (push), then start
  the next M3 surface (feed workflows).

### Status (2026-08-22) - milestone 1 close gate: all five aspects PASS on 11003d8..499a0e3

Round-2 re-reviews of the residue batch `fd171b6..499a0e3` (pinned
milestone delta `11003d8..499a0e3`, HEAD `499a0e3`, signed, worktree
clean):

- Aspect 1 (Rust parity, Meitner): PASS. All five F-A..F-D2 fixes
  verified line-by-line against `membership_dictionary.rs`,
  `blob_tree.rs`, `fixed_tree/query.rs`, `membership_dictionary/blob.rs`;
  the optionalValue and rejectCell conversions were verified against
  `range_mutation/assign.rs` and `fixed_tree/gap.rs`; fresh
  -gcflags=-m=2 shows no remaining value escapes; full validation
  matrix green at the pinned HEAD. mmap-only/durability statement
  re-confirmed for the whole delta.
- Aspect 2 (Go idioms, Anscombe): PASS. P1-1 (uint64 boundary compare)
  verified against the previously failing linux/386 and linux/arm
  builds, now green; P3-1 (optionalValue in range_edit.go) and P3-4
  (single rejectCell) verified in source and in the escape report; the
  P3-2 and P3-3 dispositions are recorded here and accepted; full
  validation matrix green at the pinned HEAD.
- Aspects 3, 4 and 5 (Harvey, Newton, Aristotle) passed the milestone
  scope on `11003d8..fd171b6`; the residue batch touches no public
  API surface, no wire bytes and no conformance fixture (both
  re-reviewers re-ran the full cross-open/alloc/race battery at
  `499a0e3`), so their PASS stands for the pinned delta.

Carried P3s (all on the range-edit draft path, which has no production
caller until the next milestone ports draft_store membership/structured
applies; all tracked as requirements of that slice, see the next-milestone
entry):

- P3-A: newEncodedRange heap-escapes per range-record edit
  (range_edit.go:49-56, interface EncodeRecord call). Pre-existing at
  11003d8, not on the ProjectHistory path, not covered by the 39/84
  ceilings. Fix plan (validated by Aspect 1): rangeCtx carries a
  writer-owned [RangeRecordV6Size]byte encode scratch and
  EncodeRecord writes into it, mirroring Rust EncodedRange::new
  writing a local [u8; 36] by value.
- P3-B: the closed-gap rejection path allocates two rejectCell structs
  (tree/gap.go:458-467). Pre-existing, cold assign-with-hint retry
  path, returned-by-design pointers. Fix plan: an optionalCell
  value+present pair when the draft-edit path gets an alloc ceiling.
- P3-C: segmentAt returns *segment (one escape per transform segment)
  (range_edit.go:151-169). No production caller today. Fix plan:
  return segment by value when the transform walk becomes live.

No P0-P2 findings remain on the milestone delta. Milestone 1 (pure-Go
exact v4 reader core) CLOSED: pushed as master HEAD 499a0e3, and the
next milestone (M3 surface: feed workflows) starts from the pushed state.
Follow-up mapping: the three carried P3s above are the first recorded
requirements of the next milestone's range-edit slice; no other deferred
items exist in this SOW.

### Status (2026-08-22) - chunk 3b-5 defined: complete feed workflows (M3 surface)

Milestone 1 (reader core + history projection) is closed and pushed at
HEAD 19bf997. Next M3 surface: the complete named-feed workflows of the
Rust live writer. Go has the value-free report types, the ordered merge
base, the draft feed catalog (lookup/ensure/insert/allocate), the draft
membership dictionary (combine/contains-indexes/intern), the range edit
core, and the history workflow; it lacks the feed input machinery, the
feed merge policy, the feed lifecycle ops, the membership/structured
transaction surfaces, and the structured draft apply paths.

Rust authority: live_writer/{feed_workflow,membership,structured,
feed_lifecycle}.rs, live_writer/workflow.rs, draft_store/{feed_merge,
membership,structured,storage,timestamp_refresh,import_merge,
import_cache}.rs, range_mutation/{coverage.rs,coverage/input.rs,
assign.rs}, writer_core edit bindings, feed.rs (FeedName), and the
workflow report/comparison helpers; tests in feed_workflow_tests.rs,
live_writer workflows and draft_store tests.

Slices (disjoint write scopes, exact Rust baseline):

- A (internal/writer draft machinery): the range-mutation coverage and
  assignment inputs (UnionInput/UnionState/OrderedPrefix/
  UnionAssignmentInput, push/finish input untracked, private constant
  ranges, assign_private_input), the empty-map feed trio, the feed
  merge policy + projection + merge_coverage over the existing
  orderedMerge, the draft feed lifecycle (addFeedToMembership with
  intern_added_bit, rename/remove current feed), and the workflow-range
  draft fields. Go tree PrivateEdge/LocalInsert already exists.
- B (public v4/go): FeedName binding, CreateFeed/ReplaceFeed workflows
  (begin_create_feed/begin_replace_feed, add_ranges_v4/v6(+slice),
  finish_input -> FinishedWorkflow/PreparedFeedChange), the
  MembershipTransaction surface (FeedRef/MembershipRef/
  TransactionFeedCursor, apply_v4/v6, empty_membership, add_feed,
  lookup/ensure/rename/delete feed, metadata, commit/abort), the
  workflow completion/classification/report machinery shared with the
  history projection, and tests mirroring feed_workflow_tests.rs and
  the membership/structured transaction tests, plus work-counter and
  zeroalloc pins and Rust cross-open evidence at the close gate.
- C (internal/writer structured apply + public StructuredTransaction):
  draft intern_network_enrichment_v1 (payload encode + structure
  refcounts + membership owner refcounts), assign_structure_input_v4/v6
  with the assignment input, clear_v4/v6, delete_current_structured_feed
  (remove_feed_from_structure payload re-intern), finish_structure_deltas
  (structure_dictionary apply_delta + released-membership accounting),
  the public StructuredTransaction (StructureRef, assign/clear,
  metadata, commit/abort), and tests mirroring the Rust structured
  workflow tests.
- The carried P3s from the milestone-1 gate (newEncodedRange encode
  scratch, segmentAt by value, optionalCell for reject cells) land in
  slice A/C where their callers become live, as recorded.

Recorded decisions (no open user decision): live sidecar remains M4
(no import/lock/refresh workflows); the feed workflows are the exact
ranges-over-membership create/replace and the transaction surfaces,
mirroring the Rust public API one-to-one including reference epochs
(StaleReference/ForeignReference), empty-map fast path, and the
logical change classification.

- Next: slice A implementation on the pushed state, then B, then C,
  then the five-aspect close gate like every chunk.

### Status (2026-08-22) - allocation P1 on the warm ProjectHistory path: 5,169 to 39 allocations per run (committed at 925e114)

The warm ProjectHistory projection path (source 1000 last-seen points,
three destination windows, abort) allocated 5,169 heap objects per call
before this pass; it now allocates 39 - all per-projection or per-window
objects with Rust call-pattern parity, none proportional to the input
record count. The fixes, in order:

1. tree.Key inline hash keys: Key carries an optional 40-byte Raw value
   (tree.RawKey) so the hash-tree probe keys of the membership
   dictionary stop escaping through the generic Codec interface
   (tree/codec.go, writer/membership_dictionary.go). Removed ~700/run.
2. bitmap.AllocateLowestID exhaustion callback: the error value changed
   to a func() error built only on exhaustion (Rust parity), removing
   the per-allocation closure escape (bitmap/used.go, the two
   dictionaries). Removed ~300/run.
3. sha256 digests: hasher.Sum(digest[:0]) replaces Sum(nil)+copy at the
   three codec sites. Removed ~300/run.
4. membershipWords interface: ReadWords(start, output []uint64) became
   ReadChunk(start) ([64]uint64, count, error) (Rust HASH_WORDS), so
   the 64-word batch buffer no longer escapes through the generic
   interface (membership_dictionary.go, membership_blob.go). Removed
   ~300/run.
5. bitmap used-cursor: start/touchChild return a usedCursor value and
   every consumer takes it by value. Removed ~400/run.
6. historyPlan.begin moved to a pointer receiver. Removed ~200/run.
7. historyPolicy gained a scratchWords prefixWords field so the prefix
   decode reuses the bound instead of escaping a stack value through
   generic dispatch. Removed ~300/run.
8. Public FeedName became string ([]byte before): the Rust FeedName is
   a 256-byte value, and a Go string is the natural immutable value; it
   removes the per-window conversion and append-grow copies
   (logical_types.go, history_projection_public.go, public tests).
   Removed ~700/run.
9. MetaCRC32C padding: the [4]byte zero literal became a package-level
   var. Removed ~200/run.
10. Writer-owned encode scratches (the remaining per-record escapes):
    encodeMembershipRecord, encodeCatalogRecord, and writeMemberDelta
    now write into caller-held bounded arrays on DraftStore,
    OutputBuilder, or deltaPending (writer/membership_dictionary.go,
    writer/catalog_codec.go, writer/membership_delta.go,
    writer/draft_store.go, writer/output.go). The Go generic tree
    interface makes stack encodes escape; the Rust borrow checker keeps
    them on the stack, so the writer owns the bounded targets - a draft
    is single-threaded and every tree insert copies the record into the
    mapped page before the next encode reuses the buffer. Removed the
    last ~700/run. This also fixed a latent buffer-reuse bug: the
    membership record head only wrote the blobRoot slot for blob
    records, so an inline record encoded into a reused buffer kept the
    previous blob root and failed validation (masked while every encode
    used a fresh make() buffer).

The permanent gate is TestProjectHistoryAllocCeiling
(v4/go/history_projection_alloc_test.go): AllocsPerRun on the warm
path with a documented ceiling of 50 and a breakdown comment; the
temporary probe files were deleted. Remaining per-run costs are the
inherent ones: the report vectors (4 makes sized by window count, Rust
Vec parity), the plan/merge/draft-store/cursor objects, and the two
fstat sequences of the abort path (require unchanged base + trim +
verify, exactly the Rust discard_unpublished call pattern).

Validation (all under nice): go test ./... and -tags v4work (both
-count=1 and -race), go vet, gofmt clean, checkptr, GOOS
windows/darwin/freebsd and linux-arm64 builds, Rust full suite and
--all-features, Go conformance and mixed subprocess cross-open - all
green. The allocation test is deterministic at 39/run.

- Reviewed by the five-aspect close gate (see the entry below); the
  gate's fix batches are committed and the re-review proceeds on
  delta 11003d8..HEAD.

### 2026-08-21 - user decision: the gate scanner and its mutation corpora are deleted; the gate is full-codebase review

The pure-Go SDK is a small, simple file-format SDK. Reasonable tests are
CI-grade: well under a minute, at most a couple of minutes, single-core.
The mutation corpora and the whole-program scanner that re-analyzed the
entire module once per test case (~9 core-hours per run) are deleted with
this entry: the scanner tool directory and its shell harness
are removed, and every reference to them is purged from the live status
and current guidance; dated historical entries remain in the execution
log and the Status History appendix (preserved also in git history per
the user instruction to commit first, HEAD d16c88e).

The mmap-only and file-I/O policy gate is now a full-codebase adversarial
review: four concurrent fresh reviewers on the lead's own model read the
ENTIRE Go codebase, file by file - two hunt complete-page copies into or
out of the mmap, two hunt file I/O on persistent content outside the mmap.
Every .go file, every build-tagged variant, tests included.

### Status (2026-08-21) - chunk 3b-4 defined: history projection (draft_store/history.rs + live_writer/history_projection.rs)

- Next M3 chunk after the 3b-3 snapshot close: the last-seen history
  projection. Rust authority:
  v4/rust/iprange-livedb/src/draft_store/history.rs (one-pass
  last-seen map projection into named feeds: collect windows, unique
  names, ensure feeds, cutoff ranking, feed-index ordering, prefix
  interning, per-window before/after counts), its OrderedMerge base
  (draft_store/range_merge.rs), the committed-range cursor
  (range_store_cursor.rs), the draft membership algebra
  (membership_dictionary/algebra.rs contains_indexes + combine,
  draft_store/membership.rs ensure paths, draft_store/catalog.rs
  ensure_feed), and the live workflow surface
  (live_writer/history_projection.rs + workflow.rs prepared
  operation, metadata staging, commit/abort); tests in
  tests/history_projection.rs and
  live_writer/history_projection/tests.rs. Go has only the value-free
  report types (logical_types.go:82-119): no implementation calls
  them, and the writer lacks the ordered merge, the committed-range
  cursor, the draft feed catalog, the membership
  combine/contains_indexes algebra, and the prepared-workflow state.
- Slices (disjoint write scopes, exact Rust baseline):
  - A (internal/writer foundations): MembershipOperation enum plus
    dictionary contains_indexes and combine (membership_dictionary/
    algebra.rs; identity shortcuts, canonical word counts, chunked
    operand reads, HASH_WORDS-bounded scratch), the draft feed
    catalog (feed_catalog.rs lookup/insert/allocate feed index over
    the dual name/index trees and the feed used bitmap), the bounded
    heap charge helper (heap.rs parity with the "history projection
    heap" labels), range inclusive-cardinality helpers for both
    families (key.rs inclusive_cardinality), and the three new
    necessary-work counters source_passes, history_window_tests,
    membership_combinations (no-ops in production).
  - B (internal/writer merge + workflow state): the committed-range
    forward cursor over the base generation through the draft
    mapping (range_store_cursor.rs SelectedStore validation), the
    ordered old/input merge (range_merge.rs: Incoming, OrderedMerge,
    Policy, coalescing Output over the existing rangeBulkBuilder,
    RefcountRun over the membership dictionary), the history
    plan/merge/policy with prefix interning and per-window observe
    (draft_store/history.rs verbatim, including cancellation
    checkpoints and error classes), and the Draft/Core workflow
    state machine (begin_membership_workflow, workflow_input_open/
    active, abandon, draft finish, mutate semantics, no-change
    discard).
  - C (public v4/go): Writer.ProjectHistory with an immutable source
    and cancellation, FinishedHistoryProjection (Report/Changed/
    Commit/Abort, set/clear metadata on the changed handle),
    CommitResult/AbortResult mapping, plus tests mirroring
    tests/history_projection.rs and the module-work pins
    (zeroalloc/v4work), and the Rust cross-open evidence at the
    close gate.
- Recorded decisions (no open user decision):
  1. Live source refusal: the Go boundary takes the immutable reader
     only and refuses Live with the Rust Unsupported class
     (OsUnsupported 58) and detail "live history source requires the
     live sidecar coordination (Milestone 4)", exactly like the
     snapshot Live refusal; LiveReader is approved M4 scope.
  2. The Rust FinishedHistoryProjection enum collapses to one Go
     handle (DirectTransaction precedent): Report and Abort work on
     both variants (Abort on a no-change result reports
     NoPendingTransaction, Rust FinishedHistoryProjection::abort
     parity), Commit/SetMetadataJSON/ClearMetadataJSON require the
     changed variant and fail with the Rust class of the equivalent
     unreachable call. The PreparedState captures the cancellation
     token at prepare and checks it during commit exactly like Rust
     commit_operation. Go has no Drop hook: a changed prepared
     handle keeps the draft owned until Commit/Abort/Close, the
     DirectTransaction ownership pattern (documented divergence).
  3. Destination feeds for the Rust-parity multi-window scenario are
     built white-box through the newly ported draft feed catalog and
     membership interning (Go has no public create_feed workflow
     yet; feed workflows are the next M3 surface). Public tests
     cover the reachable surface: created feeds on empty
     destinations, the no-change rerun, invalid request classes, the
     full IPv6 space count, and the aborted-draft recovery.
  4. Heap accounting mirrors Rust heap.rs: every retained plan
     vector charges the draft budget MaxHeapBytes under the
     "history projection heap" labels (BudgetExceeded class), and
     the prefix bitmaps read through caller-owned word buffers only
     (no mapped views reach the dictionary read), the OutputWords
     contract.
  5. The old-range cursor reads the committed base generation with
     base-meta selection (Rust SelectedStore: selected txn and page
     count from base.meta, never the draft), so the merge compares
     the source scan against the committed destination exactly like
     Rust; release_private is false (history never consumes the
     base tree).
- Next: slice C (the public Writer.ProjectHistory facade), then the
  five-aspect review at the close gate like every slice.

### Status (2026-08-22) - chunk 3b-4 cross-open evidence committed: Go-produced history destination (HEAD 11003d8)

- The shared conformance corpus now carries a Go-produced membership
  destination built through the public Writer.ProjectHistory workflow:
  v4/conformance/go/history-membership-ipv4.iprdb (69 KB), source =
  1000 singleton last-seen points (last_seen 10+index%3), windows
  one/two/three with exclusive cutoffs 9/10/11 so the feeds keep
  1000/666/333 points (manifest breakdown 334 one-only, 333 one+two,
  333 one+two+three, 3 feeds, address_count 1000). cases.json now
  lists nine fixtures; the Rust conformance suite opens and verifies
  every range, feed projection, and cardinality of the new file, the
  Go conformance inventory includes it, and both mixed subprocess
  gates check the feed catalog and record count from a fresh process.
- The two older Go fixtures were regenerated with the current writer
  during the corpus extension. The regenerated bytes differ from the
  committed originals in the meta-page database_id (offset 32-47) and
  commit_nonce (offset 56-71) fields and in the page checksums that
  cover them; both readers verified the observable data (format, range
  records, feed catalog) is byte-identical between the regenerated and
  committed files, and the corpus contract allows non-identical bytes
  when observable data is equal.
- Go regeneration path: TestRegenerateGoFixtures gained
  regenHistoryMembershipIPv4 and republishes all three Go files after
  staging verification.
- Validation at HEAD: go test ./... and -tags v4work (both -race),
  checkptr (-gcflags=all=-d=checkptr=2), go vet, gofmt clean, GOOS
  windows/darwin/freebsd and linux/arm64 builds on both tag sets,
  Rust full suite and --all-features, Rust conformance and mixed
  subprocess, Go conformance and subprocess cross-open - all green.
  Tests finish in seconds; no scanner runs.
- Next: the five-aspect adversarial close gate for chunk 3b-4
  (delta 99166af..11003d8, slices A+B+C and the corpus extension),
  then the next M3 surface (feed workflows).

### Status (2026-08-22) - chunk 3b-4 slice C COMMITTED: public Writer.ProjectHistory facade (HEAD b9e942f)

- Slice C (the public feed-workflow facade) committed at b9e942f:
  - history_projection_public.go: Writer.ProjectHistory with the Rust
    request classification order (feed-workflow-ready, window count,
    source kind/reader, direct value kind, last_seen tag, family,
    same-file identity through device+inode, cancellation), the
    allocation-free drive through the reader direct cursors with the
    canonical-source checks and overflow classes, the finished handle
    with the no-change discard and the changed workflow finish;
  - FinishedHistoryProjection: report, abort (no-change
    ErrorNoPendingTransaction), SetMetadataJSON/ClearMetadataJSON with
    the cancellation checks and abort-on-error, Commit with the
    DirectTransaction sequence and the result status mapping;
  - internal/writer/history_workflow.go: BeginHistoryProjection /
    Push4/Push6/Finish binding the plan and merge inline per call (no
    closures on the per-record drive).
- Core fix found by slice-C validation: the two-commits-on-one-writer
  path failed at the second RequireUnchangedBase because the in-memory
  format.Meta struct carried the stored page checksum, which encode
  recomputes and never reconciles (Rust MetaV4 has no checksum field
  at all). The field is removed; ParseIdentity verifies the checksum
  locally. Pinned by writer_double_commit_test.go. This is a pre-fix
  P1-class parity bug that would have broken every second writer
  transaction.
- Work-counter parity fix: the public drive double-charged
  RangeConsumed (the reader direct cursor already charges it, Rust
  range_cursor::next parity). The drive now charges only the logical
  input-source and source passes.
- Tests: history_projection_public_test.go (multi-window vector with
  the exclusive-cutoff counts 1000/666/333, full IPv6 space, aborted
  draft recovery, metadata stage with the TransactionAborted class of
  the refused second stage, invalid requests, unrelated-feeds rerun,
  no-change abort), history_projection_v4work_test.go (Rust
  one_source_pass pins: 1 input pass, 3 source passes, 1000 consumed,
  1000 emitted, 1 output pass, 3000 window tests; 64 unused prefixes
  intern nothing), history_projection_zeroalloc_test.go (BySize >=
  4096 heap objects stay flat across 64 project/abort runs).
- Validation: go test and -tags v4work (both -race), checkptr, vet,
  gofmt, GOOS windows/darwin/freebsd and linux/arm64 builds on both
  tag sets, all green. No scanner runs; tests finish in seconds.
- Next: the five-aspect adversarial review at the close gate for the
  slice, then the milestone-1 close gate.

### Status (2026-08-22) - chunk 3b-4 slice B COMMITTED: ordered merge, history projection, workflow state (HEAD e1fae2c)

- Slice B (internal/writer merge + workflow state) committed at
  e1fae2c:
  - committed-range forward cursor over a base generation through the
    draft mapping (range_store_cursor.go SelectedStore; base txn and
    page count pinned, never consumes, recorded decision 5) on the
    shared tree.ForwardCursor (internal/tree/cursor.go);
  - ordered old/input merge (range_merge.go: incomingRange,
    orderedMerge, mergePolicy, coalescing mergeOutput over
    rangeBulkBuilder, refcountRun with the checked sign arithmetic of
    Rust range_merge.rs);
  - one-pass history plan/merge/policy (history.go: window collection,
    unique names, ensure feeds with the created count, cutoff ranking,
    feed-index ordering, cached prefix interning, per-window and
    aggregate observe with adjacency runs, balanced finish report, the
    "history projection heap" charges, cancellation checkpoints);
  - operation-private membership refcount delta state
    (membership_delta.go track_buffered/flush/track and the delta
    fixed tree; membership_draft.go
    finishMembershipDeltasWithCheckpoint; range edits route every
    membership record through trackMembershipRefcount and fail closed
    on structured kinds);
  - Draft/Core prepared-workflow state machine (workflow.go: HasDraft,
    DraftChanged, WorkflowInputOpen/Active, MetadataStaged,
    OperationAbandoned/OperationIs, AbandonOperation,
    BeginRangeWorkflow/BeginMembershipWorkflow, Mutate, WriterEdit
    PrepareHistoryFrom/BeginHistory/PushHistory/FinishHistory, the
    membership and direct workflow finishes with the no-change
    discard).
- Tests: sliceb_history_test.go mirrors the Rust
  tests/history_projection.rs vectors (exact multi-window counts and
  the no-change rerun, empty-source preservation, plan validation and
  heap budget, the canonical-input merge contract, the workflow gates,
  the direct-replacement publish), the tree cursor tests, the
  range-accounting fence updates, and v4work necessary-work pins (one
  source pass, four ranges consumed, 8 segments x 3 windows = 24
  window tests, six membership delta spills).
- Fixed during the slice: work.Reset() did not clear
  membershipDeltaSpills (the counter leaked across v4work pin tests),
  and the structured-kind fails-closed fixture wrote a
  bootstrap-invalid meta (missing structure kind and dictionary
  limits) instead of the valid empty structured meta.
- Validation: go test, -tags v4work, both -race, checkptr, vet,
  gofmt, and the GOOS windows/darwin/freebsd plus linux/arm64 builds
  and vet on both tag sets, all green.

### Status (2026-08-22) - chunk 3b-4 slice A COMMITTED: membership algebra, draft feed catalog, heap budget (HEAD 3495436)

- Slice A (internal/writer foundations) committed at 3495436: the
  membership algebra (contains_indexes over caller-owned selected-bit
  output, combine with identity shortcuts and canonical word counts),
  the draft feed catalog (ensure/insert over the dual name and index
  trees plus the feed used bitmap), the bounded heap charge helper
  under the Rust heap labels, the inclusive-cardinality helpers for
  both families, and the source_passes/history_window_tests/
  membership_combinations necessary-work counters (production no-ops).

### Status (2026-08-21) - chunk 3b-3 slice C+D CLOSED: five-aspect PASS at HEAD 861aef8

- M3 chunk-3b-3 slice C+D (snapshot writer surface) closes with all
  five aspect reviewers at PASS, no P0-P2 findings. Delta reviewed:
  b0f785a -> 200798c -> 6cb88df -> 8749f99 -> 4e077a6 -> e2858bb ->
  fcfc951 -> 861aef8. Aquinas (wire/integrity) PASSed at b0f785a
  including the Rust cross-open of six Go-produced snapshots; Ohm
  (APIs/docs) FAILed at b0f785a with three findings, all fixed at
  200798c and re-reviewed PASS; Hume (Go idioms) FAILed at b0f785a
  (the floor-of-zero heap-charge wrap and two P2s), fixed at 6cb88df
  and e2858bb and 861aef8, PASS; Aristotle (performance) FAILed at
  b0f785a (P2-1: the metadata deflate heap charge mirrored Rust's
  512 KiB miniz constant while the Go stdlib workspace measures
  ~821 KiB), fixed at 8749f99 with the honest 840 KiB charge and an
  enforcement test, PASS; Pauli (Rust parity) PASSed the full slice at
  8749f99 with the b0f785a P1 verified fixed in-range, then PASSed the
  P3 parity round at fcfc951.
- Fixed across the review rounds:
  - floorPowerOfTwo(0) returned 1 (Rust reference_batch.rs:113-118 has
    the zero guard), wrapping the snapshot heap charge for budgets
    under 32 bytes and bypassing the metadata heap gate; fixed and
    pinned by the tiny-heap tests (TestSnapshotTinyHeap*).
  - Real writer bug discovered in slice C+D validation: after a
    successful exchange the Go writer never removed the previous
    destination that RENAME_EXCHANGE swapped onto the private attempt
    name; Rust retire_steps unlink_previous does (main_file.rs:296-320).
    The writer now retires it identity-guarded with the Rust-verbatim
    classification surface.
  - The metadata deflate heap charge under-charged the Go stdlib
    workspace (measured peak 821,285 bytes with GC disabled vs the
    mirrored 512 KiB miniz constant); the honest 840 KiB constant is
    enforced by TestMetadataDeflateHeapOverheadCoversWorkspace
    (race-skipped; the detector shadow inflates HeapAlloc).
- P3 parity round before the close: retireExchangedPrevious and
  verifyCustody now mirror Rust verify_private_or_retired + unlink_exact
  (previousCustody binds identity + byte length + an open descriptor to
  the old main inode; zero-link already-retired vs multi-link conflict
  vs identity/byte-length proofs; post-unlink zero-link proof with
  CodeCleanupConflict residue "retired previous destination still has a
  link"; Rust-verbatim problem details). The three retirement crash
  points are injected (publication.after_main_rename,
  after_previous_unlink, after_retirement_sync) and pinned by
  TestCrashPublishReplacePreservesExactPreviousOrDesiredState
  (skip where the atomic exchange is unavailable, mirroring the Rust
  cfg guard). TestRetireExchangedPreviousBranchClassification pins
  every refusal branch deterministically; the post-unlink CleanupConflict
  proof stays a documented race-window guarantee (no deterministic
  construction without a blocking checkpoint; Rust pins it via
  checkpoint observers, and the project rules ban test-only production
  machinery). A pre-cancelled snapshot refuses before any destination
  artifact exists (Rust lock_file_cancellable order); the metadata copy
  charge includes the reader's length+1 overflow probe; the flate
  workspace test uses runtime.KeepAlive. Accepted non-blocking items
  recorded: probe-detail string divergence on unreachable EACCES
  corners, the split-capture TOCTOU (conservative residue direction),
  the fewer crash-checkpoint positions (the Go machine compresses the
  intermediate steps), and the standing performance P3s (feed-name
  string(), membership word passes, sha256 per intern) with their
  documented OutputWords contract.
- Cross-open evidence: all six Rust fixtures and the six Go-produced
  snapshots cross-open byte-exact (Aquinas, wire/integrity aspect).

### Status (2026-08-21) - chunk 3b-3 slices A+B COMMITTED: reader cursor surface and writer structure dictionary (HEAD 172c472)

- Slice A (internal/reader): exported NewMembershipRangeCursor4/6,
  NetworkEnrichmentV1Range4/6 + NewNetworkEnrichmentV1Cursor4/6
  (structured_value/cursor.rs), MetadataJSONLen, FileIdentity, and
  ConfirmUnchanged (source final-check: VerifyIdentity + meta re-read,
  mirroring BasicSource::final_check/bind_current, RecoveryCandidateChanged
  51 wrapping identical to Rust candidate_changed).
- Slice B (internal/writer): structure dictionary
  (structure_table/structure_codec/structure_dictionary.go),
  PushNetworkEnrichmentV1V4/V6 with structure-mode guards and membership
  interning (immutable_output/structured.rs), and the structure reference
  batch through NewStructuredOutputBuilder (immutable_output/
  reference_batch.rs; membership batch charged first, structure batch
  second from the remaining heap).
- Real bug fixed in slice B: the Go membership hash tree compared raw
  little-endian digest bytes while Rust Ord compares (digest, word_count,
  id) numerically, so dedup broke at membership id >= 256 (two records
  hashing to the same first-le-8 could share one ID). The hash probe now
  compares big-endian digests (hashProbe) while the wire cells stay
  little-endian; readers are unaffected (they use the ID tree). Verified
  against Rust structure_value/codec.rs and membership_tree.rs.
- All four test modes green (test, -tags v4work, both -race), vet,
  gofmt; all five platform builds compile.

### Status (2026-08-21) - chunk 3b-3 slices C+D COMMITTED: snapshot machine and public facade (HEAD 172c472+)

- Slice C (internal/snapshot): the machine mirroring api.rs + build.rs +
  terminal.rs - Live refusal at the API layer (OsUnsupported 58, detail
  per recorded decision), budget validation with Rust-verbatim details
  (pages >= 2; open files 3 for replace/Live else 2), source open BEFORE
  the destination create, identity compare (source vs private output,
  32-byte LE device+inode encode), heap-charged reference batches (slot
  16 bytes, ENTRY_LIMIT 1024, floor power of two), the six family copy
  loops with per-item cancellation checks, SnapshotWords exact-count
  verify ("membership length changed while copying"), metadata copy under
  the remaining heap budget, ConfirmUnchanged between finish and publish,
  source release before publish, cancellation gate, and the writer
  publish mapping to Cause+CleanupState. Reader gained the exported
  LookupMembershipID (Rust GenerationReader::membership).
- Slice D (public v4/go): SnapshotTo, SnapshotSourceMode,
  SnapshotPublicationPolicy alias, SnapshotBudget, SnapshotResult,
  SnapshotPreparationFailure (Cause+Cleanup collapse), plus tests
  mirroring snapshot_operations.rs over the committed Rust fixtures
  (direct/membership/structured/structured-nothreat), the
  zeroalloc/v4work pins, and the malformed-traversal vs CRC-damage pair.
- Real writer bug fixed in slice C validation: after a successful
  exchange (PolicyReplaceExisting) the Go writer never removed the
  previous destination that RENAME_EXCHANGE swapped onto the private
  attempt name; Rust retire_steps unlink_previous does
  (main_file.rs:296-320). The writer now captures the previous
  destination identity in verifyCustody and, after the destination proof,
  unlinks the private name identity-guarded and syncs the retained
  directory (retireExchangedPrevious); a failed retirement keeps the
  already-proven main published and reports ResiduePossible. Regression
  pinned in TestPublishExchangeReplaces and the snapshot replacement
  tests (assert_no_private_artifacts parity).
- Live-specific Rust tests (sidecar budgets, live generation pinning,
  live self replacement) are deliberately not ported: the Go boundary
  refuses the live source before any of them can run, per the recorded
  decision. The Go adaptation pins the refusal itself
  (TestSnapshotLiveRefusedAtBoundary) including the Rust api.rs ordering
  (refusal precedes budget validation).
- All four test modes green, vet, gofmt, all five platform builds; the
  v4work pins assert the one-pass copy (RangeConsumed == source range
  count) and copy determinism (identical generations, identical
  WordReads).
- API-docs review fixes (Ohm P2s, applied after the first review round):
  the nil-budget and invalid-mode boundary guards are now documented on
  SnapshotTo and pinned by TestSnapshotBoundaryGuards (both refusals are
  Go-boundary additions; Rust passes the budget by value and its mode
  enum is closed); the immutable-source-replacement-during-copy race
  (Rust snapshot_operations.rs:538) is now ported end-to-end
  (TestSnapshotImmutableSourceReplacementDuringCopyBlocksPublication
  over a 20k-range generated source, controller renames the source when
  the private output appears, final ConfirmUnchanged refuses with
  RecoveryCandidateChanged, no output, no private residue); the
  slice-A reader test used syscall.Stat_t untagged and broke GOOS=windows
  vet on both tag sets - the identity oracle is now the mapping owner's
  portable StatIdentity (mapping.StatIdentity exists on all five target
  platforms). The Rust Outcome collapse is named in the SnapshotTo doc.

- Performance review fix (Aristotle P2-1, applied after the second
  review round): the deflate heap charge under-charged the Go stdlib
  workspace. The gate mirrored Rust metadata.rs DEFLATE_HEAP_OVERHEAD
  (512 KiB, the miniz backend's pinned workspace), but compress/flate at
  DefaultCompression pins ~821 KiB live heap (measured with GC disabled
  across 1 KiB to 20 MiB payloads; stdlib layout: hashHead 512 KiB +
  hashPrev 128 KiB inline, 64 KiB window, 64 KiB token queue). An
  under-charge let the deflate attempt exceed the caller's declared heap
  budget. The Go charge is now its own honest constant (840 KiB) and is
  enforced by TestMetadataDeflateHeapOverheadCoversWorkspace, which
  re-measures the peak workspace and pins both a sanity floor and the
  upper bound (the Rust parity pattern: "allocation tests enforce it");
  the test skips under -race because the detector's shadow memory
  inflates HeapAlloc (race_disabled.go/race_enabled.go). The pre-existing
  storedZlib fallback remains the honest layout when the budget fits the
  bound but not the deflate workspace.

- Post-gate P3 parity round (all five reviewers PASS at HEAD 8749f99;
  applied before the slice close-out): the exchange retirement machine
  is now Rust-exact. verifyCustody binds the replacement destination
  (previousCustody: identity + byte length + an open descriptor to the
  old main inode, replacement.rs PreviousMain parity) and
  retireExchangedPrevious mirrors verify_private_or_retired and
  unlink_exact: the bound inode must keep its captured byte length, a
  zero-link inode is already retired and requires the private name
  absent, a multi-link inode is the link-count conflict, the private
  name must still name the captured inode with exactly one link, and
  the identity-guarded unlink must drop the last link (CleanupConflict
  "retired previous destination still has a link" residue). The two
  "retired ..." detail strings became the Rust-verbatim problem texts.
  The three retirement crash points (publication.after_main_rename /
  after_previous_unlink / after_retirement_sync) are injected with the
  existing fault machinery and pinned by
  TestCrashPublishReplacePreservesExactPreviousOrDesiredState (Rust
  replacement_crashes_preserve_exact_previous_or_desired_state): after
  the exchange the main holds the complete new generation and the
  private name holds the exchanged previous; after the unlink no
  private artifact survives. A pre-cancelled snapshot now refuses
  before any destination artifact exists (Rust lock_file_cancellable
  order); the metadata copy charge includes the reader's length+1
  overflow probe; the flate workspace test uses runtime.KeepAlive
  instead of the dead `_ = enc`.

- Retirement parity review round (Pauli PASS at 8749f99; Hume FAIL at
  4e077a6 with two P2s, both fixed at e2858bb; branch pins at the close):
  the replacement crash test skips where the atomic name exchange is
  unavailable (Rust cfg(linux, apple) guard parity), the zero-link
  retirement branch propagates a non-ENOENT probe error as CodeIO
  instead of collapsing it into NameExists, and a direct-call table
  pins every retirement refusal branch (TestRetireExchangedPrevious
  BranchClassification: already-retired Clean/NameExists, changed byte
  length, multi-link, vanished private name, symlink and directory at
  the private name, foreign inode, and the last-link unlink). The
  post-unlink CleanupConflict proof stays a race-window guarantee with
  no deterministic construction without a blocking checkpoint (Rust
  pins it via checkpoint observers; the Go fault machinery has no
  mutation hook and adding test-only production machinery is a defect
  per project philosophy). The branch table skips where the atomic
  name exchange is unavailable, like the crash suite (the exchange
  policy refuses to create an attempt there). The stale verifyCustody
  doc now describes the previousCustody binding.

### Status (2026-08-21) - chunk 3b-3 defined: snapshot writer surface (snapshot::snapshot_to)

- Next M3 chunk after the 3b-2 close: the compact-snapshot surface.
  Rust authority: v4/rust/iprange-livedb/src/snapshot/{api.rs,build.rs,
  terminal.rs,source.rs,snapshot.rs} and its dependencies
  immutable_output/structured.rs + structured_value/{table.rs,manager.rs,
  codec.rs,network_enrichment_v1.rs,cursor.rs}; tests in
  tests/snapshot_operations.rs. Go has no counterpart: the public
  facade, the machine, the reader structured cursor, and the writer
  structure dictionary are all missing.
- Slices (disjoint write scopes, exact Rust baseline):
  - A (internal/reader): exported membership-range cursor
    constructors, NetworkEnrichmentV1Cursor4/6 ordered structured
    cursor (structured_value/cursor.rs), MetadataJSONLen,
    FileIdentity, and ConfirmUnchanged (source final-check for the
    snapshot: VerifyIdentity + meta re-read compare, mirroring
    BasicSource::final_check/bind_current).
  - B (internal/writer): structure dictionary on the one-shot output
    builder - intern/apply_delta/flush over the structure-ID fixed
    tree (structured_value/table.rs+manager.rs+codec.rs geometry
    already in internal/format/structure.go), the structure reference
    batch (immutable_output/reference_batch.rs), and
    PushNetworkEnrichmentV1V4/V6 (immutable_output/structured.rs)
    including structure-mode family guards and membership interning.
  - C (internal/snapshot): the snapshot machine mirroring api.rs and
    build.rs: Live refusal, budget validation (max_output_pages>=2,
    open-files 3 for the replace policies / Live else 2, Rust-verbatim
    InsufficientResourceBudget details), source open through the
    reader, identity compare, heap-charged reference batches, the six
    family copy loops with per-item cancellation checks, metadata copy
    with the heap budget, source final-check + release before publish,
    and the publish mapping to the terminal shapes.
  - D (public v4/go): SnapshotTo, SnapshotSourceMode, SnapshotBudget,
    SnapshotResult, SnapshotPreparationFailure collapse (early/new/
    discarded/from_publication into Cause+Cleanup, live-only fields
    dropped), plus tests mirroring snapshot_operations.rs and the
    zeroalloc/v4work pins.
- Recorded decisions (no open user decision):
  1. Live source mode refuses honestly at the boundary with the Rust
     Unsupported class (OsUnsupported 58) and detail "live snapshot
     source requires the live sidecar coordination (Milestone 4)";
     live coordination is the approved M4 scope and the Rust
     non-unix cfg path refuses the whole surface the same way.
     reject_live_self is therefore unreachable and not ported.
  2. The output identity is preserved from the source meta verbatim
     (database id, transaction id, commit nonce - Rust
     GenerationReader::output_spec), unlike the fresh identity of
     algebra publish_set.
  3. The source is opened BEFORE the destination create and released
     (reader Close) BEFORE the publish rename, and the source
     final-check runs between builder finish and publish, exactly like
     Rust finish_current (changed source refuses publication).
  4. Reference-batch heap accounting mirrors Rust: membership batch
     charged for membership/structured kinds, then the structure
     batch charged from the remaining heap for structured; metadata
     charged under "snapshot metadata input heap" and written with
     the remaining budget.
  5. Failure shapes collapse to Cause+CleanupState on the Go boundary
     (AlgebraPreparationFailure precedent); source_cleanup/
     coordination_cleanup/housekeeping fields are always empty while
     Live is refused and are documented, not carried.
- Next: slices A and B run in parallel; then C, then D; then the
  five-aspect review at the close gate like every slice.

### Status (2026-08-21) - slice 3 CLOSED: five-aspect PASS at HEAD 8345891

- M3 chunk-3b-2 slice 3 (publish_set surface) closes with all five
  aspect reviewers at PASS, no P0-P2 findings. Delta reviewed:
  3897b4b (F-1 typing/probe/crash fixes) -> 45d453f (NetBSD cause
  translation) -> 8345891 (test portability). Pauli (Rust parity),
  Hume (Go idioms), Aristotle (performance), Aquinas (wire/integrity)
  PASSed at 3897b4b; Ohm (APIs/docs) FAILed at 3897b4b with three
  findings, all fixed and re-reviewed to PASS at 45d453f; the
  test-portability delta at 8345891 (test-only build tags and helper
  placement, no production semantics) was verified across the full
  matrix and Ohm re-confirmed PASS including the close records.
- Fixed with this entry:
  - The NetBSD no-replace refusal now surfaces the Rust-verbatim
    public cause: CodeDurabilityUnsupported (34) with the exact
    problem.rs detail "filesystem lacks required durable namespace
    operations" (problem.rs:62-64), instead of the Go-internal
    CodeOSUnsupported marker (58). The staging classifier still keys
    on the internal marker (publication_staging.go:410) and translates
    only at the public Cause; the marker never leaves the mapping
    owner. Ruling verified against Rust: re-keying the classifier on
    code 34 would misclassify the FreeBSD mid-machine SyncDirectory
    EINVAL (linkat machine, publish_link_noreplace.go) as NotPublished
    while Rust classifies the main-rename-stage Unsupported as
    outcome_unknown (attempt.rs from_armed) - the marker encodes the
    acquire position, which code 34 alone cannot.
  - Comments corrected: neither mapping_publish_netbsd.go nor
    mapping_publish_posix.go claims a "preparation failure" anymore;
    the writer returns the not-published result (attempt discarded).
    The publication_staging.go rename-refusal comment names netbsd as
    the only refusal target (Rust rename_noreplace Unsupported on
    non-linux/apple/freebsd; Windows implements the primitive through
    NtSetInformationFile, windows_mutation.rs:24-39). The Go Windows
    stubs stay documented Go-platform refusals.
  - Pre-existing test portability debt fixed (Ohm P3): makePagesFile
    lived in the linux-tagged mapping_test.go but is referenced by the
    untagged v4work pins, and the FreeBSD linkat-machine tests carried
    no !windows tag while testing the !windows machine - so
    GOOS!=linux cross-vet/cross-test builds of internal/mapping failed
    to compile. makePagesFile moved to an untagged portable helper
    file; both machine test files gained the !windows tag. Every 5-OS
    cross-vet and cross-build on both tag sets now compiles.
- Known edge recorded for the sidecar milestone, not a blocker: on a
  Linux filesystem without the RENAME_NOREPLACE primitive the staged
  Go flow classifies the main-rename EINVAL as OutcomeUnknown, while
  Rust refuses earlier at the coordination acquire (NotPublished). The
  Go staging deliberately has no coordination rename yet (the
  reservation-sidecar machinery lands with the M4 sidecar work per the
  approved scope), so the acquire-position refusal cannot be
  distinguished; the DurabilityUnsupported code is identical in both.
  Closure of this edge is tracked with the M4 sidecar coordination
  chunk, not deferred silently.
- Validation at 8345891, all niced: go test ./... and -tags v4work
  (9 packages ok each), -race both tag sets, vet and gofmt clean,
  5-OS (linux/darwin/freebsd/netbsd/windows) build and vet of ./... on
  both tag sets - all green, including GOOS=windows with v4work which
  previously failed to compile for testing. Rust tree unchanged.
- Next: chunk 3b-2 slice 3 done; the next M3 chunk is the snapshot
  writer surface - Rust snapshot::snapshot_to with SnapshotBudget,
  SnapshotSourceMode, SnapshotPublicationPolicy, SnapshotResult and
  SnapshotPreparationFailure (v4/rust/iprange-livedb/src/lib.rs
  snapshot export; no Go counterpart exists yet) - reusing the
  publication staging and output builder closed by this slice, per the
  Milestone-3 surface list (snapshots and reports). Slice 3 closes
  with no open user decision.

### Status (2026-08-21) - re-review round: FreeBSD no-replace machine, NetBSD classification, records corrections

- Re-review verdicts so far (same five reviewers, delta ae57dfa ->
  adcbd90 -> HEAD): Ohm (APIs/docs) returned one P2 - CleanupInProgress
  was recorded as code 77 but is 64 (codes.go line 77 vs sdk_error.rs:76
  CleanupInProgress = 64) - plus two P3; Aquinas (wire/integrity)
  returned one P2 - on the freebsd/netbsd targets the fail-if-exists
  rename refusal now retained one residue attempt file per attempt,
  while Rust refuses the unsupported primitive as the preparation
  failure with the attempt discarded and implements no-replace on
  FreeBSD with the crash-safe linkat machine - plus two P3. Pauli, Hume,
  and Aristotle re-reviews still running.
- Fixed with this entry:
  - FreeBSD no-replace publication implemented as the crash-safe linkat
    machine (Rust namespace_mutation.rs link_noreplace): identity-proved
    source probe, linkat, link-state classification, directory syncs,
    identity-proved alias unlink, and the destination-only proof; crash
    points publication.freebsd.after_noreplace_{link,link_sync,
    alias_unlink,alias_sync} at the exact Rust positions. The machine is
    compiled on every non-Windows target so the exact syscall sequence
    runs in build-host tests; the FreeBSD entry point
    (mapping_publish_freebsd.go) is its only production caller.
  - NetBSD keeps the no-replace refusal (mapping_publish_netbsd.go) and
    the writer now classifies the Unsupported refusal as the Rust
    acquire-failure result: NotPublished with both artifacts discarded
    and the content computed from the main slot (Rust attempt.rs
    from_private -> Ok(not_published)); no residue accumulates per
    attempt, and it is a result, never a preparation error.
  - Custody identity compare: verifyCustody compares the path probe to
    the creation-time identity captured from the builder descriptor
    instead of rebinding it (Rust verify_name); Discard stays
    identity-guarded on the creation capture. The fail-if-exists
    publish-time re-checks now cover the twin AND the main name (Rust
    reservation_file.rs acquire + arm_with require_absent):
    NotPublished with the foreign file preserved in both cases, and the
    remaining rename-race window is pinned by a test-only fault point to
    the outcome_unknown classification with the attempt retained (Rust
    from_armed).
  - Records: CleanupInProgress is code 64 everywhere; the direct-cursor
    gate detail strings now match what the Rust public path emits
    (require_direct: "direct lookup requires a direct-value database"
    and "lookup address family does not match the database"); the
    scanner-purge sentence is scoped to live guidance.
- Validation: go test both tag sets, -race (both), vet, gofmt, 5-OS
  cross-builds and cross-vet of the OS-tagged tests all green; ten new
  machine unit tests plus a four-point child-process crash suite (each
  FreeBSD crash point exits 86 in a spawned child, then the transition
  recovers in-process to the destination-only state).
- Re-review of this delta is with the same five reviewers; the slice
  closes only when all five report no P0-P2.

### Status (2026-08-21) - slice 3 close gate: five-aspect review executed, findings fixed, re-review pending

- M3 chunk-3b-2 slice 3 (publish_set surface) is at the close gate. The
  single-level five-aspect review above ran at HEAD 2a5e78e with the Rust
  implementation as the mandatory baseline; all five reviewers returned
  findings (none P0): Pauli (Rust parity) FAIL - three P1 publish
  classification gaps plus two P2; Hume (Go idioms) FAIL - fourteen stale
  gate-era comments plus three hand-rolled error-type chains; Aristotle
  (performance) FAIL - two P2 hot-path items (per-cell leaf re-validation,
  metadata two-pass decode); Aquinas (wire/integrity) PASS with one P2
  (Unicode-vs-ASCII fold in the reader reserved-name check); Ohm
  (APIs/docs) FAIL - two P1 cursor gate gaps plus one P2 (abortAfter code
  class, stale Validation records).
- All findings are fixed with this entry:
  - publish classification matches Rust: rename-refusal is
    PublicationOutcomeUnknown with the attempt retained (attempt.rs
    outcome_unknown); fail-if-exists twin checks and the replace-policy
    early custody checks (missing destination, foreign twin preserved,
    proven single-link attempt, identity-guarded discard) mirror
    publication/replacement.rs and reservation_file.rs; attempt names
    are hex-encoded;
  - file identity capture (FileIdentity via fstat) proves attempt
    custody before publish and discard;
  - error-type consolidation: join/merge error chains are single
    fmt.Errorf wraps with the close-failure cause appended;
  - abortAfter with unresolved state maps to CodeCleanupInProgress
    (64), nested under the abort error exactly like Rust CleanupIncomplete
    (sdk_error.rs CleanupInProgress = 64);
  - the stale gate-era comment vocabulary is purged from production
    sources (zero non-test hits);
  - reserved-name checks use ASCII-only folding with NUL rejection from
    the shared internal/format/name.go authority (Rust path.rs);
  - cursor constructors gate kind and family at open with Rust-exact
    error strings, pinned by cursors_gate_test.go;
  - metadata decoding is single-pass: the chain is validated on the same
    visit that feeds the inflater, capped at 5,182 pages (Rust MAX_PAGES);
  - treeCursor caches the validated leaf (Rust Cursor::leaf); the
    necessary-work pins moved with the measured reduction (catalog 71 ->
    2, direct 4 -> 2, projection 7 -> 5 page visits; LeafValidation
    counts unchanged).
- Validation after the fixes: go test ./... both tag sets, -race (both),
  vet, gofmt, 5-OS cross-builds (linux/darwin/freebsd/netbsd/windows)
  all green; full suite ~9 s, niced. Rust tree unchanged.
- Delta re-review is in progress with the same five reviewers; the slice
  closes only when all five report no P0-P2.
- M3 record (compact): chunk 1 - reader correctness fixes; chunk 2 - joins
  and aggregation with the zero-membership-ID fix; chunk 3a - read-side
  algebra; chunk 3b-1 - tree variable; chunk 3b-2 slices 1-3 - output
  machinery and the publish_set writer surface, slice 3 at close.
- Superseded pre-rules gates: at 6ded376 the previous level-1 five-aspect
  arrangement reported all PASS with a reduced level-2 quorum (minimax,
  mimo), and the four-reviewer full-codebase mmap/file-I/O gate PASSed at
  2a5e78e (record below); the single-level five-aspect rules at the top
  of this SOW supersede both, and the five-aspect re-review at 2a5e78e
  found the failures listed above.

### 2026-08-21 - close gate: four-reviewer full-codebase review (PASS, one P1 fixed)

- Four concurrent fresh reviewers on the lead's model read the ENTIRE
  pure-Go codebase file by file: 179 .go files each (production + tests +
  every build-tagged variant), zero files skipped, committed at HEAD
  d16c88e.
- Verdicts: 4/4 PASS on the mmap-only / file-I/O policy:
  - complete-page copies into or out of the mmap: none in production;
    view-holder whitelist clean (only internal/mapping, internal/format,
    internal/reader, internal/writer, and the public facade handle mapped
    page views; internal/tree, internal/bitmap, internal/retire,
    internal/bootstrap receive views only through the Store callbacks);
  - file I/O on persistent content outside the mmap: none; the descriptor
    surface is confined to internal/mapping (open, mmap, ftruncate, msync,
    fsync, close); zero unsafe/reflect/exec laundering.
- Unanimous P1, fixed with this entry: the metadata read path accumulated
  the whole compressed stream (up to the section-11 bound) in owned heap
  before inflating; the Rust authority streams mapped chunks into the
  inflater. The reader now validates the chain in pass 1 while capturing
  only the two header bytes and four trailer bytes, and inflates in pass 2
  straight from the mapped chunk views through metadataStream (implements
  ReadByte, so no bufio read-ahead and byte-exact stream-end detection).
  The finding reviewer re-reviewed the delta: PASS.
- The delta re-review caught one more P1 before commit: without ReadByte
  the flate wrapper would read ahead and trailing-junk rejection would be
  dead; now pinned by TestMetadataStreamFlateCounting (mechanism) and
  TestMetadataJunkBeforeTrailerRejected (real-fixture corruption: junk
  between the final DEFLATE block and the intact trailer).
- Rulings recorded (accepted, Rust parity): the publication digest copies
  1024-byte mapped spans into a sub-page stack buffer (exact output_
  digest.rs parity); bounded record-scale cells and feed-name conversions
  are the sanctioned logical boundaries; test-only page fixtures are not
  production. The in-memory flate metadata decode is the sanctioned
  logical-operation boundary for metadata (bounded by section 11).
- Validation after the fix: go test ./... both tag sets, -race, vet,
  gofmt, 5-OS cross-builds green; full suite ~8 s, niced. Rust unchanged.

## Review Process (user decisions 2026-08-12, 2026-08-17, 2026-08-21)

1. Five aspect reviewers on the lead's own model (copies), disjoint
   aspects, adversarial mode, running with the final-review skill;
   spawned once at first use, reused by message for every delta round,
   never respawned between rounds, restarted only between milestones.
   One level only - there is no level-2 gate and no other model is used.
2. The milestone closes only when all five reviewers report no P0-P2
   findings.
3. mmap-only / file-I/O policy gate (2026-08-21): enforced by the
   five-aspect reviewers above (aspect 2) plus periodic full-codebase
   sweeps when the surface under review requires them. No scanner or
   mutation-corpus enforcement exists.
4. All builds, tests, benchmarks, and scans run under nice; any step
   expected to exceed ~2 wall-minutes or ~10 core-minutes is named with
   its expected cost in the report and recorded in this SOW's validation
   plan before it runs.
## Requirements

### Purpose

Deliver the pure-Go peer of the accepted Rust v4 SDK so `update-ipsets` and Go
consumers can use the exact current unsigned Phase-1 database without cgo or a
Rust runtime dependency. The result must preserve the format's mmap-only,
bounded, durable, two-level architecture and its measured performance
discipline rather than mechanically translating Rust source.

Make the work safe to hand to an independently run implementer. Evidence at
each milestone must let the user judge whether the implementation is accurate,
focused, maintainable, and worth continuing before a large rewrite is accepted.

### User Request

- Commit and push the existing work so the repository starts clean.
- Create a self-contained SOW for porting the accepted Rust implementation to
  pure Go.
- Keep the implementation minimal-complete, thin, clear, and performance-first.
- Use the implementation and its evidence as a controlled evaluation of an
  independently run implementer; the user will decide whether and how to
  continue from the milestone results.

### Assistant Understanding

Facts:

- The Rust engine is complete for the current approved Phase-1 contract and has
  passed correctness, durability, resource, performance, conformance, C-boundary,
  and native supported-platform gates. Its final behavior and measurements are
  summarized in `v4/rust/README.md` and completed SOWs 0020-0024.
- The normative authorities are
  `.agents/sow/specs/binary-format-v4.md` and
  `.agents/sow/specs/design-iprange-engine.md`. Rust is implementation evidence;
  its physical page placement and internal types are not a second specification.
- The current Go module is an older unreleased experiment. It has 50 production
  Go files and 44,088 newline-counted production lines, but its public package
  exposes only basic types, cardinality, and error declarations.
- The old Go wire model permits only direct and membership values, keeps the old
  `retention` tag and 1 MiB metadata limit, lacks all structured-value meta state,
  and does not open the current shared corpus
  (`v4/go/internal/exactv4/contract.go:11-18,63-73,98-101,135-162` and
  `v4/go/types.go:13-34`).
- The old Go storage layer performs positional content reads and writes and owns
  complete 4 KiB page arrays outside file mappings
  (`v4/go/internal/exactv4/page_source_linux.go:13-49`,
  `v4/go/internal/exactv4/page_source_other.go:12-54`,
  `v4/go/internal/exactv4/os_linux.go:495-577`, and
  `v4/go/internal/exactv4/private_page_pool.go:92-103`). These are prohibited by
  the current format contract.
- The old Go tests, vet, and formatting pass. This proves those old internal
  fragments are internally consistent; it does not prove current format or SDK
  parity. No Go test references `v4/conformance/cases.json` or opens a current
  Rust-produced fixture.
- The Rust-provided C ABI remains the only C SDK. Porting or regenerating that ABI
  in Go is outside this SOW.
- Snapshot signing remains Phase 2 under pending SOW-0017 and is outside this
  SOW.

Inferences:

- Treating the current Go tree as a nearly finished implementation would preserve
  the architecture that Rust deliberately replaced. The safe approach is a
  semantic port from the current spec and accepted Rust behavior, reusing an old
  Go component only after current tests prove it conforms.
- A literal Rust-to-Go line translation would copy language-specific ownership
  machinery and the Rust implementation's size. The Go design should reproduce
  each invariant and observable operation once using idiomatic Go and a smaller
  module graph.
- The isolated physical-fault worker, Windows namespace/security behavior, and
  mixed-language live coordination are the highest feasibility and portability
  risks. They must be proved early, not left to final integration.

Unknowns:

- The exact subset of old Go tests or codecs worth retaining. Milestone 0 must
  classify each current production file as retain, rewrite, or delete before any
  destructive edit.
- The final Go/Rust timing delta. The accepted 5-10% band is a target where the
  runtimes permit it; only matched measurement can establish justified
  exceptions.
- Whether the pure-Go fault worker can satisfy every POSIX signal-chaining rule
  without project-owned assembly or another new native boundary. The implementer
  must prove the existing no-cgo contract. If a new boundary appears necessary,
  implementation stops for a user design decision before adding it.

### Acceptance Criteria

- The final module is a pure-Go implementation with no cgo, Rust library,
  external database engine, or alternate content path.
- It supports only the exact current v4 identity. No v3 code, old-v4 parser,
  compatibility mode, importer, exporter, or obsolete fixture remains.
- Public Go behavior covers the complete Phase-1 semantic surface listed in
  `v4/rust/iprange-livedb/src/lib.rs` and the design spec, except the explicitly
  Rust-provided C ABI. Idiomatic Go names are allowed; missing semantics are not.
- The public SDK exposes logical advanced direct, membership, and structured
  transactions plus the typed high-level workflows. It never exposes page
  numbers, roots, raw feed indexes as mutation authority, membership IDs,
  structure IDs, bitmap combinations, or allocator state.
- One internal reader core owns healthy selected-generation access. One internal
  writer core owns healthy mapped COW mutation, allocation, retirement, page
  sealing, and committed-generation publication. Higher-level APIs only sequence
  logical operations over those owners.
- Validation/recovery, external reader coordination, and filesystem
  namespace/publication retain their separate narrow owners while reusing the
  canonical codecs and mapped output builders. No second physical tree or
  publication implementation exists in a workflow.
- Every persistent artifact is content-accessed only through file-backed
  mappings. Static and runtime gates reject `Read`, `Write`, `ReadAt`, `WriteAt`,
  `Pread`, `Pwrite`, `ReadFile`, `WriteFile`, buffered/stream content transfer,
  and equivalent Windows calls against SDK artifacts.
- No complete database page exists in a Go heap/stack array, cache, anonymous
  mapping, or copied byte slice. Page creation and COW edits occur at final
  offsets in file-backed mappings; mapped-to-mapped cell copies are allowed.
- Open, lookup, scan, mutation, commit, query, and snapshot do not implicitly
  run full validation. Explicit validation and recovery alone perform the full
  graph and checksum work.
- Direct, membership, and structured files implement the exact current meta,
  page, dictionary, metadata, retirement, sidecar, reservation, and publication
  contracts. `NetworkEnrichmentV1` uses one common structured manager plus its
  independent codec, including lazy threat membership.
- The opaque compressed metadata payload preserves exact caller bytes, absent
  versus empty state, read-your-writes, and the 20 MiB uncompressed limit.
- Generic direct assignments and structured range assignments apply every input
  in arrival order. A later range overwrites only its covered addresses.
  Unordered ingestion normalizes directly in the destination and creates no
  sorting file or complete-feed heap image.
- Named feeds, feed-index reuse, membership interning, structured interning,
  typed references, stale/foreign reference rejection, and deletion/rename
  behavior match the current contract. Callers never construct internal ID
  combinations.
- Exact direct replacement, first-seen refresh, last-seen refresh, named-feed
  create/replace/rename/delete, membership import, one-inode immutable feed
  construction, history projection, queries, provider joins, global named-feed
  algebra, compact snapshot, commit resolution, and lifecycle/publication
  resolvers match Rust semantics and reports.
- Operation failure aborts the private draft. No later commit can publish partial
  work after a failed source or sink. Commit, abort, close, cleanup, and
  outcome-unknown results retain exact evidence and retry obligations.
- Live reader registration, writer exclusion, reader-safe transaction-grouped
  reclamation, lowest-free-page allocation, sidecar identity/replacement, and
  forked-handle rejection match the exact contract.
- Linux, macOS, and Windows implement the supported live contract. FreeBSD 14
  implements immutable reading, offline validation/recovery, and durable
  immutable publication while every live entry rejects before path access or
  mutation.
- The Go distribution supplies its own exact version-matched
  `iprange-v4-worker`. It claims only SDK-owned in-region physical mapping faults,
  chains every unrelated POSIX `SIGBUS` disposition exactly, uses the equivalent
  Windows exception rule, and never mislabels an unrelated worker crash as source
  unreadability.
- Go independently produces all required conformance fixtures. Both readers
  actually open and semantically verify every Go- and Rust-produced fixture,
  including structured values, full IPv6 cardinality, and exact metadata bytes.
- Mixed Rust/Go subprocess tests pass in both directions for reader slots, writer
  exclusion, reclamation, stale-slot cleanup, sidecar replacement, transition
  states, reservations, publication inspection, and resolution.
- Warm successful point lookups and cursor steps allocate zero Go heap bytes.
  Writer allocations and heap use are fixed or bounded by declared budgets, not
  proportional to input records or sparse page numbers.
- Test-only necessary-work counters pin tree descents, pages visited/copied,
  range passes/splits/merges, dictionary work, checksum sealing, synchronization,
  and artifact creation. They compile out of production binaries.
- Matched release benchmarks cover the accepted Rust matrix and representative
  update-ipsets data. Each retained dominant cost maps to required format work;
  unexplained wasted work is fixed and the audit repeated.
- Operation-by-operation Go performance targets the accepted Rust result within
  5-10% where runtime behavior permits. Every material exception has matched
  profiles, hardware/runtime evidence, and a documented cause. CI uses a loose
  disaster threshold only after local performance acceptance.
- Current Go tests, race/checkptr/fuzz/property tests, malformed corpus tests,
  crash/fault tests, resource tests, cross-compilation, and authorized native
  platform tests pass. Skips cannot hide a required platform or producer.
- The final production graph has no unreachable source, broad dead-code
  suppression, duplicate physical authority, or test-only production mechanism.
  Production LOC, file sizes, function sizes/complexity, and exact-clone evidence
  are reported honestly against the directional engineering philosophy.
- Every valid finding in the final first-principles audit is repaired and the
  identical audit is repeated. This SOW cannot complete while an actionable
  correctness, durability, coordination, bounded-resource, performance,
  layering, duplication, portability, documentation, or conformance issue
  remains.

## Analysis

Sources checked:

- `AGENTS.md`, especially the v4 engineering philosophy and Rust-first gate.
- `.agents/skills/project-v4-rust/SKILL.md` for the proven Rust verification and
  portability workflow that the Go peer must reproduce semantically.
- `.agents/sow/specs/design-iprange-engine.md:15-50,57-129,131-169,366-449,451-519`.
- `.agents/sow/specs/binary-format-v4.md`, complete current contract, especially
  sections 3-5, 8-15, 16, 18-21.
- `v4/rust/README.md` and `v4/rust/iprange-livedb/src/lib.rs` for the accepted
  public semantic inventory and measured baseline.
- Completed SOWs 0019-0024 for the mmap-only correction, final authority/hot-path
  audit, update-ipsets workflows, performance proof, structured values, and
  randomized structured correctness.
- `v4/conformance/README.md`, `v4/conformance/cases.json`, and all six current
  Rust-produced fixtures.
- Complete current Go production/test inventory, `v4/go/go.mod`, public files,
  current format constants, storage/page sources, OS code, and test references.
- Baseline commands: `go test ./...`, `go vet ./...`, and `gofmt -l .`; all pass
  before porting.
- Official Go `runtime`, `unsafe`, `golang.org/x/sys/unix`, and
  `golang.org/x/sys/windows` documentation; Microsoft file-mapping documentation;
  and the platform syscall references already normative in the format spec.
- `etcd-io/bbolt @ 01f7d9658a8a` as a limited Go platform-wrapper reference:
  `bolt_unix.go:55-83`, `bolt_windows.go:65-119`, and `db.go:454-570`.

Current state:

- Git started this planning slice clean and synchronized after commit `900b345`.
- The Rust peer and Rust-produced corpus exist. The Go peer, Go-produced corpus,
  cross-open proof, and mixed-language coordination proof do not.
- The current Go tree is large but not product-shaped: 44,088 production lines,
  1,609 production function declarations, several files above 1,000 lines, and a
  4,794-line largest file. Twenty-seven production files contain positional
  content access and/or complete page arrays.
- The current Go public package has no reader, writer, workflow, query,
  validation, recovery, snapshot, publication, or live lifecycle constructor.
- `v4/go/go.mod` currently declares Go 1.23 and `golang.org/x/sys v0.35.0`.
  Preserve that support floor unless a required API proves it impossible; a
  toolchain/dependency-floor change requires evidence and user approval.

Risks:

- Preserving the old Go storage architecture would reproduce the already-fixed
  positional-I/O/page-buffer failure and invalidate performance/resource claims.
- Translating Rust module-for-module would likely reproduce its language-specific
  size and obscure Go's simpler ownership model.
- Porting only the happy-path reader/writer would miss lifecycle, outcome,
  cleanup, recovery, and cross-process contracts that prevent data loss.
- A Go mmap slice remains valid only while its retained mapping/handle lifetime is
  valid. Escaping page slices or racing `Close` can create stale mapped access.
- Go runtime signal ownership makes the exact isolated-worker contract a
  high-risk area. No runtime-internal linkname, swallowed signal, cgo fallback,
  or Rust-worker dependency is acceptable.
- OS-specific namespace, security, locking, flush, shrink, and crash behavior
  cannot be established by cross-compilation alone.
- Tight timing gates before local optimization would create noise. Loose CI gates
  before local proof would hide waste. Local matched profiles come first.
- Deleting the old tracked Go tree without an exact inventory could remove useful
  tests or independently correct codecs. No tracked file is deleted until the
  user approves the exact proposed deletion set.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- The repository needs a pure-Go peer, but its current Go tree implements an
  obsolete, incomplete, positional-I/O architecture. The accepted Rust reset
  changed both the storage architecture and the public semantic surface. A port
  must therefore follow the current specs and observable Rust behavior, not
  continue the old Go experiment or transliterate Rust internals.

Evidence reviewed:

- The full normative architecture and binary-format specifications.
- The complete accepted Rust public export inventory, README evidence, current
  corpus, and SOW 0019-0024 outcomes.
- Every current Go production file name, production/test counts, LOC, public API,
  wire constants, platform build tags, content-transfer calls, complete-page
  arrays, conformance references, and baseline tests/vet/formatting.
- Official current Go/OS mapping and unsafe-memory references plus the limited
  bbolt platform-wrapper evidence listed above.

Affected contracts and surfaces:

- `v4/go/` public SDK, internal physical format implementation, OS platform
  layers, fault worker binary, tests, benchmarks, source/runtime architecture
  gates, module metadata, README, and package documentation.
- `v4/conformance/` manifest, Go-produced fixtures, cross-read tests, malformed
  transformations, and mixed Rust/Go subprocess harnesses.
- Rust test/harness code only where minimal cross-language orchestration is
  needed. Rust production behavior, bytes, public API, and C ABI are frozen.
- Project instructions and a new Go runtime project skill after the workflow is
  proven.

Existing patterns to reuse:

- Rust's authority boundaries, semantic tests/models, public workflow reports,
  conformance generator method, necessary-work accounting, update-ipsets
  benchmark scenarios, source/mmap gates, native platform matrix, and final
  audit structure.
- Shared language-neutral `cases.json` and the current Rust-produced files as the
  first immutable-reader target.
- Current Go scalar key/cardinality code and tests only if literal vectors prove
  exact current behavior.
- Small OS-specific mapping wrappers as a pattern. Do not copy bbolt's format,
  freelist, remapping, global locking, page objects, or durability model.

Risk and blast radius:

- This is a replacement of an unreleased Go experiment and eventually affects
  nearly all `v4/go/` production code and tests.
- Data-loss risk is concentrated in COW ownership, dirty-page sealing, durability
  ordering, abort, cleanup, and outcome resolution.
- Memory-safety/process-crash risk is concentrated in mapped lifetime, checked
  offsets, concurrent reuse, physical faults, and signal/exception chaining.
- Interoperability risk is concentrated in little-endian codecs, canonical
  validation, structure/membership dictionaries, metadata compression, sidecar
  locks, and publication/resolver records.
- Performance risk is hidden copying, validation, allocation, synchronization,
  hashing, lookup, and maintenance in hot paths.

Sensitive data handling plan:

- Use only synthetic addresses, feeds, metadata, paths, fault cases, and public
  aggregate benchmark facts in durable artifacts.
- Authorized update-ipsets replay may use operational files locally, but durable
  evidence records only sanitized aggregate sizes, counts, timings, and shapes.
- Do not record credentials, source filenames, literal operational ranges,
  personal/customer/community data, private endpoints, or host aliases.

Implementation plan:

1. Produce the Milestone 0 parity/gap map before production edits: every public
   Rust semantic export and every normative operation maps to its Go API/module,
   test, and status; every old Go production file is classified retain/rewrite/
   delete; the exact proposed deletion set is presented for user approval.
2. Prove the platform foundation in the smallest permanent form: retained
   file-backed mappings, checked offset/view lifetime, flush/sync, identity,
   locks, growth/shrink behavior, and isolated fault-worker feasibility. Delete
   any exploratory code not converted into the final owner or a permanent test.
3. Implement the current portable codecs and one mmap-only immutable reader that
   cross-opens and semantically verifies all Rust-produced fixtures, including
   structured data. This is the first implementation-quality checkpoint for the
   user.
4. Implement one mmap-only physical writer core: final-offset page construction,
   COW range/tree edits, free/used bitmaps, retirement, checksum sealing at
   commit, meta publication, abort, and reclaim. Generate independently produced
   Go fixtures through public APIs.
5. Build the public advanced logical transactions over that core: direct,
   membership, feed catalog/references, common structured manager, and modular
   `NetworkEnrichmentV1` codec. Add randomized scalar-model tests before high-level
   workflows.
6. Build the typed high-level workflows as thin sequencing layers: immutable
   creation, feed lifecycle, direct replacement, first-seen, last-seen,
   membership import, history, metadata, snapshots, queries, joins, and algebra.
7. Complete live coordination, lifecycle transitions, publication/reservation,
   cleanup/housekeeping, commit resolution, and supported-platform behavior over
   the same cores.
8. Complete explicit validation, candidate inspection, best-effort recovery, and
   the version-matched Go worker. Reuse canonical codecs and final mapped-output
   builders; do not add healthy-path validation or a second writer.
9. Add bidirectional producer/cross-open and mixed Rust/Go subprocess gates. Run
   complete correctness, crash, resource, race/checkptr, fuzz/property,
   architecture, mmap syscall, cross-target, and authorized native matrices.
10. Port the Rust benchmark scenarios, measure locally operation by operation,
    profile every material difference, remove wasted work, and repeat until the
    final audit finds no actionable issue. Add loose CI disaster limits only
    after local acceptance.
11. Update package docs, Go README, conformance docs, project instructions, and a
    proven `project-v4-go` runtime skill. Complete the SOW only with implementation,
    artifacts, final audit, SOW move, and one closure commit.

Validation plan:

- Baseline and every milestone: `go test ./...`, `go vet ./...`, `gofmt`, race
  where supported, checkptr, leak/descriptor/residue checks, and exact changed-
  surface tests.
- Literal portable vectors for every integer, meta field, page header, record,
  hash, checksum, structured payload, sidecar/control block, and publication
  record. Cross-architecture codec execution must include a big-endian target or
  emulator.
- Public black-box semantic/property models for arrival-order normalization,
  direct/membership/structured transactions, workflows, commit/abort, and
  reference invalidation.
- Explicit malformed/corrupt corpus, fault injection, truncation, crash-point,
  cleanup retry, outcome resolution, worker chaining, and worker-build mismatch
  tests.
- Static source gates plus Linux runtime tracing provide enforcement
  evidence for mmap-only persistent content and the absence of complete
  out-of-map page images; neither mechanism alone is claimed as proof for
  every possible Go program.
- Go-produced fixtures are built only through public Go operations, verified
  against `cases.json`, opened by Rust, and then committed. Rust fixtures are
  actually opened and checked by Go.
- Mixed subprocesses cover every section-21 direction and state; language-local
  unit tests are insufficient.
- Local benchmarks use the same fixture identities, work counts, timing
  boundaries, allocation/RSS/descriptor/file measurements, warm-up, isolated
  samples, and semantic-result checks as Rust. Profiles and necessary-work
  counters identify retained costs.
- Local cross-compilation covers supported target graphs. Runtime platform proof
  occurs only on user-authorized native Windows GNU, Apple ARM64, and FreeBSD 14
  environments; operational host details are never written to the repository.
- Before closure, run the exact final audit format below. If it reports a valid
  issue, fix it and repeat the entire audit rather than completing the SOW.

Artifact impact plan:

- AGENTS.md: add the proven Go workflow/guards and index the Go project skill at
  closure; do not weaken the current engineering philosophy.
- Runtime project skills: create `.agents/skills/project-v4-go/SKILL.md` only
  after commands, platform boundaries, and evidence are proven.
- Specs: change only if implementation exposes a genuine contradiction. Stop for
  a user design decision before changing behavior or bytes; otherwise record
  that the Go peer implements the existing specs unchanged.
- End-user/operator docs: add `v4/go/README.md` and package documentation for the
  completed SDK, worker installation, platform support, resource behavior, and
  reproducible verification/performance commands.
- End-user/operator skills: none currently exist; reassess at closure if a
  downstream/public workflow is introduced.
- SOW lifecycle: this remains the sole Go-port work item. Move it to `current/`
  only when implementation begins; close it only with all code/artifacts and the
  closure commit. SOW-0017 remains separate Phase 2.

Open-source reference evidence:

- `etcd-io/bbolt @ 01f7d9658a8a`
  - `bolt_unix.go:55-83`
  - `bolt_windows.go:65-119`
  - `db.go:454-570`
- This reference is evidence that Go can isolate basic Unix/Windows mapping
  mechanics behind small platform files. Its database architecture is not reused
  because it does not implement this format's mmap-only writer, external reader
  table, exact durability, recovery, or public semantics.

Open decisions:

- Decision 5A (NetworkEnrichmentV1 location representation) was
  ratified on 2026-08-13 (option A, long-term-best): the parity matrix
  now records `Location NetworkEnrichmentV1Location` plus
  `HasLocation` as the zero-allocation equivalent of Rust's
  `Option<NetworkEnrichmentV1Location>` (the decision-4A lookup
  contract wins over the earlier pointer spelling; a pointer field
  would force a heap allocation per lookup or unsafe pointers into
  the mapping). Every other product and
  format decision is closed; the current specifications and accepted
  Rust semantics are frozen for this port.
- The obsolete Go deletion set from Milestone 0 was resolved by decision 1A
  (user decision, 2026-08-12) and executed at HEAD e65e8b7 (105 tracked
  files removed with the compiling replacement); no approval remains
  outstanding for it.
- If pure Go cannot meet the exact fault-worker contract without a new assembly
  or native boundary, stop and present evidence and options. Do not silently add
  cgo, use the Rust worker, reach into Go runtime internals, or weaken fault
  ownership/chaining.

Resolved decisions (2026-08-11, Milestone 0 closure):

- Deletion set (Decision 1): user chose C - decide after Milestone 1 evidence.
  The exact proposed set (100 tracked files + 2 untracked leftovers) is
  recorded in `.agents/sow/pending/pure-go-v4-port-milestone-0-report.md`
  section 7. No tracked file is deleted and no untracked leftover is removed
  before that decision; deletion, when approved, executes atomically with the
  compiling, tested Milestone 1 replacement.
- Fault-worker native boundary (Decision 2): user chose A - wait for the
  Milestone 1 feasibility evidence, then decide. No new native boundary
  (assembly shim, cgo, runtime internals, Rust worker) may be added before
  that decision.

## Resolved Decisions (2026-08-12, hot-path re-review)

The user adopted the external re-review's decisions after the reopening:

- Decision 1 (deletion set) = A with correction: the obsolete Go tree
  (internal/exactv4: 105 tracked files - 47 production + 58 test, the
  recorded "100" was stale - + untracked exactv4.test + empty directory)
  is removed in the FINAL Milestone 1 commit, not the first
  Milestone 2 writer commit. Before removal the verified scalar types must
  be relocated out of internal/exactv4 (v4/go/types.go is the only new-tree
  importer). .reasonix/ is preserved. Git history already preserves the
  deleted sources.
- Decision 2 (worker boundary) = A: a minimal project-owned assembly
  sigaction shim preserving the exact fault-isolation contract
  (binary-format-v4.md:3084); it affects validation/recovery workers only,
  uses no Go runtime code, cgo, or unwinding inside the handler, and is
  proven natively on each supported POSIX platform in the worker milestone.
- Decision 3 (closed-state class) = A with corrections: closed readers and
  pinned handles report WrongState (11); numeric code 9 remains HandleClosed (no
  table renumbering); WordCount becomes error-capable "(uint32, error)";
  under decision 4 per-view Release disappears and second-close errors
  apply to the reader and the pinned handle.
- Decision 4 (hot-path API shape) = A: caller-owned pinned reader handles.
  Pin once outside the workload; one pin may be shared across concurrent
  immutable lookups. Lookups, scans, view operations, and cursor steps are
  zero-allocation and zero-atomic. Views are lightweight values valid while
  their pin remains open; reader Close returns HandleBusy while pins exist;
  Pin close must not race its operations; insufficient feed-name buffers
  report BufferTooSmall plus the required size.
- Structure-kind authority conflict resolved: direct/membership + nonzero
  structure kind -> FormatInvalid (32); structured + unknown nonzero
  structure kind -> UnsupportedStructure (67); follows the mode-specific
  override at binary-format-v4.md:430 and current Rust bootstrap behavior
  (validate_direct/validate_no_structures -> KindInvariant; finish_open ->
  UnsupportedStructure for unknown codes on an otherwise valid structured
  meta).
- Decision 4A amendment (pin pointer contract, recorded at the re-review):
  Every Pin value references one shared private close state: pointer
  aliases (p2 := p1) and value copies (p2 := *p1) close the same logical
  pin, the count is decremented exactly once, and a second Close through
  any alias or copy reports WrongState. Pinned by
  TestPinPointerAliasSharesClose and the added value-copy tests
  (TestPinValueCopySharesClose, TestPinValueCopyKeepsReaderBusy).
- External audit close-out (resolved): the duplicated Cardinality129
  arithmetic and the duplicated public/internal error-code tables were
  centralized - internal/format is the single authority and v4/go/types.go
  + v4/go/errors.go re-export aliases, so the copies can no longer drift.
- Decision 5A (NetworkEnrichmentV1 location representation) = A, recorded
  2026-08-13: the user delegated the choice to the implementing agent with
  the long-term-best and minimal-complete mandate and did not select an
  option; option A was adopted - `Location NetworkEnrichmentV1Location`
  plus `HasLocation` is the documented zero-allocation equivalent of
  Rust's `Option<NetworkEnrichmentV1Location>` (a pointer field would
  force a per-call heap allocation or unsafe pointers into the mapping).
  The parity matrix row 26 records the ratified spelling and the
  v4/go/reader_public.go doc comment aligns.

### 2026-08-12 - decisions 1A-4A implemented (hot-path contract)

- Deletion (1A): scalars relocated in-line into v4/go/types.go (no more
  internal/exactv4 import); the obsolete tree was then removed in the final
  milestone-1 implementation commit - 105 tracked files (47 production +
  58 test; the recorded "100" was stale) + untracked v4/go/exactv4.test and
  the empty v4/go/exactv4/ directory; .reasonix/ untouched; import-graph
  gate updated; git history preserves the sources.
- Worker boundary (2A): recorded for the worker milestone - minimal
  project-owned assembly sigaction shim, no Go runtime code/cgo/unwinding
  in the handler, proven natively per POSIX platform.
- Closed class (3A): closed readers/pins -> WrongState (11), second Close
  -> WrongState, code 9 = HandleClosed, WordCount -> (uint32, error), per-view
  Release removed (views are pin-valid values).
- Hot-path facade (4A): ImmutableReader.Pin / Pin.Close (one lifetime
  registration outside the workload; HandleBusy while pins exist);
  pin-level membership and enrichment lookups, reader-level direct
  lookups/scans/cardinality, and LookupFeedInto (caller buffer,
  BufferTooSmall + required size) are zero-allocation and zero-atomic
  (plain-state closed checks under the documented no-race-Close contract).
  Measured 0.000000 heap bytes/run for every hot path; atomics only at
  Pin/Close.
- Location shape (5A, recorded 2026-08-12 with the round-11 fix): the
  approved parity matrix writes `Location *NetworkEnrichmentV1Location`
  (milestone-0 report row 26). Decision 4A forbids per-lookup allocations,
  and a pointer inside a by-value result cannot reference stable storage
  without one; Rust itself models the field as
  `Option<NetworkEnrichmentV1Location>`. 5A: express the option as
  `Location NetworkEnrichmentV1Location` + `HasLocation bool`, with the type
  name and fields matching the matrix and the Rust authority exactly. The
  closure report surfaces this for user veto.
- Structure-kind rule: direct/membership + nonzero structure kind ->
  FormatInvalid; structured + unknown nonzero kind -> UnsupportedStructure;
  pinned by reader and format tests (fails on the pre-fix tree).
- Counts at reopening are recorded in the Status entry above (production 4,797
  raw / tests 4,676 raw), taken after the meta-precedence parity fix, the
  sole-meta kind regression test, the final-review regression guards, and
  the public tag/metadata-limit API completion.
  Gates: go test ./... (4 packages) incl -race, vet, gofmt, import graph,
  SOW audit - all green.

### 2026-08-12 - six-reviewer re-verification after decisions 1A-4A

- All six reviewers re-reviewed the rebuilt tree at their disjoint briefs
  after the decisions implementation. Findings during the round and their
  closures:
  - Pin value-copy P2 (mapping reviewer): a dereference copy of a Pin
    could double-decrement the pin count. Closed as a formal decision-4A
    amendment: Pin is a pointer type; aliasing the pointer shares the
    single close state; value copies are unsupported like C opaque
    handles, with typed-error-only consequences; pinned by
    TestPinPointerAliasSharesClose (fails on a double-decrement).
  - Record-drift findings (conformance/reports reviewer): report sections
    2 and 12 still described the pre-deletion 5-package tree and the
    view-guard zero-alloc contract; the SOW current-state entries said "5
    packages". All fixed at 2fdcce4; dated historical entries keep their
    then-accurate wording.
  - External audit close-out (resolved): Cardinality129 and error-code
    tables centralized on the single internal/format authorities; the
    public package re-exports aliases.
- Final verdicts at HEAD 2fdcce4: codecs PASS; bootstrap/meta/direct
  ranges PASS (kind matrix verified pre-fix-failing); membership/blob/
  structured/metadata PASS (metadata rewrite verified incl. empty-present
  and incomplete-final-block probes); mapping/lifetimes/platform PASS
  (pin contract amendment verified); public API/errors/zero-alloc PASS
  (12 public + 8 internal checks at exactly 0 allocs, zero atomics in hot
  paths, WrongState class, LookupFeedInto BufferTooSmall semantics);
  conformance/tests/reports PASS (counts verified with the documented
  method and recorded in the close-out entry; deletion of 105 files
  verified exactly; records internally consistent).
- Gates at HEAD 2fdcce4: go test ./... (4 packages) incl -race, go vet,
  gofmt, import graph, 9-target cross-compile matrix, SOW audit - all
  green. Milestone 1 (immutable reader) review gate: CLOSED.

### 2026-08-14 - external-review rework (milestone 1 re-open, verified findings)

- An independently run external review PASS-failed milestone 1 with three
  findings at HEAD b230bd1, all reproduced by the lead before any change:
  1. P1 - nine separately written binary-search loops decode full records per
     probe and re-decode the selected record; the Rust fixed-tree layer reads
     key-only during search and decodes the selected record once.
  2. P1 - the mmap enforcement machinery totals 14,519 lines (4,738-line
     reader, and the gate misses the complete-page rule: a production
     function copying a mapped page into an owned [4096]byte compiles and
     passes the gate (spec binary-format-v4.md:108).
  3. P2 - the SOW Status had grown to 463 lines of round-by-round scanner
     history, hurting auditability.
- Resolved decisions (direction 1-5 of the review, accepted):
  1A. Add one authoritative fixed-tree search primitive (key-only probes,
      single decode of the selected record) in internal/reader and refactor
      every range/catalog/membership/blob lookup onto it. The blob branch
      keeps per-probe child validation (pinned by
      TestBlobBranchProbedChildValidation; Rust blob select_branch does the
      same). Catalog name probes keep full record-shape validation because
      the name IS the record payload (Rust read_key decodes the entry);
      their feed-index-limit check moves to the selected record only,
      mirroring feed_catalog.rs decode_leaf.
  2A. Add test-only necessary-work counters in internal/reader (build-tag
      `v4work`; zero-cost no-op otherwise) pinning tree lookups, descents,
      page visits/parses, key probes, selected-leaf validations, membership
      word reads, and structure decodes, plus reader benchmarks on the
      committed conformance fixtures and profile evidence in the report.
  3A. Replace the type-light AST taint engine with a type-aware scanner
      built on go/types (stdlib-only, module-aware gc export importer),
      keeping the import/selector/.s/.syso/embed/linkname bans and the
      *os.File capability surface, and add the complete-page ownership rule
      (copy/append/array-conversion sinks at or above PageSize, and
      append/copy of mapped-page-sourced values), with the metadata
  4A. Compact the Status and move the round-by-round history into the
      verbatim appendix at the end of this file.
  5A. Repeat the independent review: six-resident swarm, then sol, before
      milestone 1 closes and Milestone 2 starts.

### 2026-08-14 - external-review rework implemented

Implementation record for the reopened milestone (all three findings
reproduced first at HEAD b230bd1; the rework landed as one commit at
HEAD recorded in the first review entry below):

- Finding 1 (hot path): internal/reader/search.go adds the single
  authoritative greatestLE primitive (key-only probes, last-probe reuse,
  one decode of the selected record), mirroring v4/rust/iprange-livedb/
  src/fixed_tree/page.rs lower_bound semantics. Range branch/leaf
  lookups (range.go), catalog feed lookups (catalog.go), membership ID
  lookups (membership.go) and the blob branch selection (blob.go) all
  route through it. The four format key readers (RangeEntryKeyV4/V6,
  RangeRecordKeyV4/V6) make the probe cost a 4-byte (or 16-byte) read plus
  the slot-offset check. The blob branch keeps per-probe child validation
  (pinned by TestBlobBranchProbedChildValidation); catalog name probes
  keep full-shape validation because the name is the record payload
  (decision 1A). Test-only necessary-work counters live in
  internal/work (build tag v4work; const false no-op stubs otherwise, so
  production binaries carry zero counter state - pinned
  by TestWorkCountersDisabled). Pins: TestWorkRangeLookupMultiLevel,
  TestWorkMembershipBlobWords, TestWorkStructureLookup, plus
  bench_test.go on the committed fixtures and the synthetic multi-level
  databases. Benchmarks (i9-12900K, 200k iterations, -benchtime=200000x):
  LookupDirect4MultiLevel 158.8 ns/op, LookupDirect4MissMultiLevel
  72.05 ns/op, LookupDirect6 68.56 ns/op, LookupFeed 227.7 ns/op,
  MembershipLookupWord 94.81 ns/op, StructuredLookup 120.1 ns/op - all
  0 B/op, 0 allocs/op. CPU profile of Lookup* (reader_cpu.prof in the
  session evidence): dominant costs are SlottedPage.Record slot checks
  (26%), the intentional catalog name validation FeedNameValidString
  (48% of LookupFeed), DecodeCatalogNameRecord, greatestLE dispatch; no
  full-record decode appears on the range/membership probe paths.
- Finding 2 (enforcement): v4/go-gate is now a type-aware scanner over
  go/types (stdlib-only, module-local loader with source type-checking
  per OS config and the pinned x/sys checkout), keeping the text bans
  (banned imports/selectors, .s/.syso, //go:embed, //go:linkname, dot
  imports), the *os.File/*os.Root capability surface (approved lifecycle
  methods, same-package/module-internal callees, x/sys owner), the
  interface-erasure and generic-result rules (including named interfaces
  such as io.Reader), and the complete-page ownership rule: copy/append/
  array-conversion/string sinks that move a mapped page view at or above
  PageSize into owned memory fail with a specific violation; bounded
  record decodes below PageSize and the metadata inflater nodes
  (exempted by exact shape) stay legal. The page-taint flow is
  interprocedural through per-package symbolic summaries (pageflow.go);
  bounded slices carry their constant span (page[48:112] is a 64-byte
  was replaced by 320 table cases inside the tool (257 rejections, 63
  benign acceptances) covering source-transfer, complete-page, and
  file-capability forms,
  target, module graph, x/sys checksum pins, and 9 environment mutations:
  internal-import boundary, x/sys outside the mapping owner, assembly
  object, go.mod replace, go.work, poisoned x/sys cache/proxy, unlistable
  module). Reviewer reproduction now fails the gate: copy of m.Page(0)
  into [4096]byte, append(page...), View(0, PageSize) copy,
  [4096]byte(page), string(page), and r.page(pgno) copy all produce
  rule-specific violations; the bounded copy and the decoded metadata
  chunk append stay accepted. Gate totals: 5,104 lines (go-gate) + 552
  lines (shell) = 5,656, against module production 5,049 and tests
  5,180.
- Swarm round 1 of the rework review (2026-08-14, at the rework
  commit) returned five passes plus two P1 gate-bypass classes, both
  verified and closed in a follow-up commit:
  - function-typed variables as call targets (var clone = bytes.Clone;
    clone(page)) were approved because the callee body is not scanned;
    approvedFuncVar now accepts such variables only when the package-level
    initializer provably binds a scanned function (a func literal, a
    forms P8 (reject) and P9/P15 (benign).
  - complete-page sinks inside closure bodies (defer func(){ copy(out[:],
    page) }(), go func(){...}(), and directly called func literals) were
    invisible to the page-taint flow: pageflow now analyzes defer/go
    statements, select comm clauses, labeled and bare blocks, and function
    literals in expression or callee position (parameters bound to
    call-site taints, return taint propagated, captured variables shared
    with the enclosing state). Pinned as forms P11-P13 (reject) and P14
    (benign bounded copy inside a defer closure).
  Reviewer P3s were fixed in the same commit: the dead err re-check after
  work.LeafValidation in lookupMembershipID was removed and the counter
  moved into membershipLeafFind at the actual record-decode point (it no
  longer fires on a clean miss), matching the range/catalog/blob leaf
  helpers.
- Swarm round 2 (2026-08-14, at the round-1 fix commit) re-verified the
  delta and the lead's own adversarial pass then closed one more hole in
  the same class, fixed in a follow-up commit:
  - a function-typed variable initialized with a func literal and never
    reassigned (var f = func(p []byte){ copy(out, p) }; f(page)) was
    approved, but the literal body was analyzed only at the declaration
    with the parameter unbound, so the complete-page sink stayed
    invisible. Calls through such variables now analyze the literal body
    with the call-site arguments bound (pageflow), and package-level
    variables that are reassigned anywhere lose their initializer proof
    (rules.go reassignedVars), so a var later bound to bytes.Clone is
    (benign bounded slice through the same literal) and P18 (reject,
    reassigned var).
- Swarm round 3 (2026-08-14, at the round-2 fix commit) re-verified the
  delta; one remaining theoretical bypass in the same class was closed
  in a follow-up commit:
  - a two-hop function-variable chain (var a = func(p){ copy(out, p) };
    var b = a; b(page)) was approved through the initializer chain but
    the literal body was re-analyzed only for direct initializers, so
    the sink inside stayed invisible at the call. evalCall now follows
    the same bounded chain (funclitOf, at most two hops, no reassigned
    hop) and binds call-site arguments to the literal's parameters.
    through the same chain).
  Also fixed from reviewer P3s: the SOW Status production-LOC figure was
  corrected to the measured 5,049 (reader core 1,894) after the
  membership.go counter cleanup.
- Swarm round 4 (2026-08-14, at HEAD 2ad4001): the independent round
  review returned nine gate findings; every one was reproduced by the
  lead against the tree (exact mutation probes, gate exit 0) and closed
  in one commit at HEAD f96d13d:
  - helper-summary parameter flow lost page taint (maxLen accumulated
    from 0, so maxUnknown -1 never registered): fs.eval and evalResults
    now treat maxUnknown as at least the summary max;
  - direct function aliases and local closures lost page taint: local
    funcs are bound (st.localFuncs) and calleeTarget resolves local and
    package-level chains so calls through them analyze the literal body
    with call-site arguments bound;
  - container element extraction was unmodeled: IndexExpr/IndexListExpr
    now derive element page values while preserving the parameter source
    (generic xs[0] stays caller-dependent);
  - pointer and type-parameter byte params were not page-tainted:
    typeCanCarryPage recurses through *types.Pointer and accepts
    *types.TypeParam, and the expression-position StarExpr case is now
    modeled;
  - branch/loop states overwrote instead of joining: If branches,
    Switch/TypeSwitch/Select, and For/Range (zero-iteration join) now
    clone-and-join, and append results carry source taint so
    post-loop append(owned, out...) is caught;
  - multi-result calls mapped one RHS to the first LHS only: fresh
    callResults per assignment slot, with the stale cache deleted before
    re-evaluation;
  - named array/string conversion sinks were unrecognized: the sink
    check unwraps Named/Alias to the underlying type for both
    [N]byte(page) and string(page);
  - file-capability laundering through nested or package-level func
    literals: os.Stdin/Stdout/Stderr access and any call whose result
    type is file-bearing now fail outside the mapping owner (the mapping
    owner keeps its legit os use);
  - Mapping.View was minted expecting three arguments while the method
    takes two: the mint now uses Args[1] for the view length.
  63 benign) + 9 shell mutations exit 0, production scan clean on all
  five targets, go test ./... (both tag sets), -race, vet, gofmt zero
  diffs, cross-compilation - all green.
- Swarm round 5 (2026-08-14, at HEAD 57522e8): the independent round
  review returned ten gate findings (Luna resident) plus one
  multi-result struct-field taint gap (MiniMax P2), every one
  reproduced by the lead against the tree (exact mutation probes, gate
  exit 0 on the misses) and closed in one commit at HEAD 65ca62a:
  - helper summaries tested only the first source: choose(nil, page,
    true) lost the page taint; fs.eval/evalResults now taint when ANY
    recorded source is tainted and accumulate maxLen only over tainted
    sources;
  - named/naked returns were not summarized: analyzeFunc records
    named-result slots and feeds stores to them back into results and
    struct fields after the body;
  - void unknown function-variable calls were not transfers: an
    unproven var func([]byte) called with a page was invisible; such
    calls now transfer (scalar-result calls stay reads);
  - append(dest, src...) checked only the source: a complete mapped
    page view as the destination (append(page[0:4096:4096], ...))
    reallocates the full page into owned memory and is now rejected;
  - index and dereference stores were untracked: slots[0] = page and
    *h = page now bind the container/pointed-to variable;
  - range variables were never bound: range over [][]byte{page} now
    derives the loop value from the container taint;
  - interface/map/channel carriers were missing: any(page) keeps the
    argument taint, direct interface parameters carry page values
    (variadic and stdlib-form interfaces stay exempt), and channel
    send/receive round trips are modeled;
  - switch fallthrough dropped the previous case state: each case body
    now joins the falling-through case end state;
  - nested func-literal return context leaked: every ReturnStmt is now
    checked against its own enclosing function (literal or
    declaration) result types;
  - unproven package func vars with interface results failed open: a
    var func() io.Reader outside the mapping owner is now a capability
    launder (restricted to never-reassigned package-level func vars so
    stdlib callbacks stay clean);
  - multi-result struct-field taint (MiniMax P2): (chunk, err) helpers
    lost field taint on the struct slot; every returned expression now
    propagates composite-literal struct fields (first slot wins on
    name collisions) and evalCall records struct fields even when the
    whole result slot is tainted; parameter-sourced summary bounds now
    resolve the caller's constant slice symbol (page[48:112] stays a
    64-byte view) instead of reading the zero symbol as constant.
  rejections, 63 -> 66 benign). Gate tooling 5,104 -> 5,465 go-gate
  lines (+552 shell = 6,017 total).
  66 benign) + 9 shell mutations exit 0, production scan clean on all
  five targets, go test ./... (both tag sets), -race, vet, gofmt zero
  diffs, cross-compilation (windows/darwin/freebsd/netbsd) - all green.
  Round re-review (HEAD 2f5f71a): GLM reported a 4-line gate LOC drift
  (records said 5,469, measured 5,465; the debug strip after measuring
  removed four lines) - corrected in the records at 361d4c1 (5,465 go-gate
  + 552 shell = 6,017). Qwen then reported the cross-file package
  func-var scope hole: collectPkgFuncVars collected only the file under
  check, so var factory func() any declared in a.go and called from
  b.go of the same package skipped the interface-result fail-closed
  rule; reproduced by the lead (two-file probe, gate exit 0; same-file
  control rejects). Fixed at HEAD 0d007a8: the map is now built from
  Gate tooling 5,465 -> 5,474 go-gate lines (+552 shell = 6,026 total).
  66 benign) + 9 shell mutations exit 0, production scan clean on all
  five targets, go test ./... (both tag sets), -race, vet, gofmt zero
  diffs, cross-compilation - all green.
- Swarm round 6 (2026-08-14, at HEAD 83ea65b): the independent round
  review returned nine further gate findings (Luna resident), every one
  reproduced by the lead against the tree (exact mutation probes, gate
  exit 0 on the misses) and fixed in one commit:
  - scalar-result function-variable calls with a page argument were not
    transfers: an unproven callback var func([]byte) int can copy a full
    mapped view inside its unscanned body; such calls now transfer
    (probe f1, pin P57);
  - named function types hid the signature from the package func-var
    collection: type factory func() any; var f factory with an unbound
    body called outside the mapping owner escaped the interface-result
    fail-closed rule; the collector now unwraps *types.Named to the
    underlying signature, and the rule stays silent only for variables
    bound to a func literal somewhere in the package (their bodies are
    benign) (probe f2, pin P58);
  - map and channel parameters were not page carriers: m["x"] and <-ch
    through map[string][]byte and chan []byte parameters lost the taint;
    paramCanCarryPage now unwraps map/chan element and key types (probe
    f3, pin P59);
  - same-named fields of different struct-result slots kept only the
    first source: split5(a,b) (S,S) returning S{Data:a}, S{Data:b}
    dropped the slot-1 page; propagateStructResult now unions the field
    sources (probe f5a, pin P60);
  - returned local struct variables were not summarized: box5(p)
    { s := S{Data:p}; return s } lost the field taint, and selecting
    .Data directly on a call result was not evaluated (a fixpoint-pass
    cache staleness also kept callFields stale: the selector-on-call
    path now drops the cached call result before re-evaluation); both
    shapes closed (probes f5b, pin P61);
  - string conversion of a definite full-page view was pinned only by
    maxLen: string(page[0:4096]) slipped through; the sink now uses
    definitePageSpan (constant maxLen or constant sym bound) (probe f6,
    pin P62);
  - append into a complete mapped view was checked only for maxLen ==
    pageSize: append(page[0:8192:8192], ...) slipped through; the
    destination check now uses pageFull (probe f7, pin P63);
  - the owned byte-builder family was not a copy sink: bytes.NewBuffer
    (page) and bytes.Buffer.Write* / strings.Builder.Write* own the
    bytes with a result type the transfer rule cannot see; a new
    ownedCopySink rule rejects them (probe f8, pin P64);
  - import "unsafe" was not banned: unsafe.Slice over a mapped
    descriptor would mint page views the type layer cannot trace; the
    import ban now includes unsafe (probe f9, pin P65).
  The format record decoders' byte fields (FeedEntry.Name and friends)
  are grammar-bounded below a complete page; once the return-local-
  struct fix made those bounds visible at the copy sinks, the field caps
  are minted by value (formatFieldCaps: catalog name records 255, blob
  leaf data 4048, inline membership bitmaps 4000, structure payloads 32)
  exactly like the existing DecodeMetadataChunk mint - the production
  LookupFeedInto copy stays legal while a full page passed through the
  same path still fails.
  (272 -> 281 rejections, 66 benign). Gate tooling 5,474 -> 5,691
  go-gate lines (+552 shell = 6,243 total). Round-6 hardening (a
  literal-bound package func var later rebound to a non-literal has an
  unknowable callee and must stay fail-closed, not exempt) pinned as
  tooling 5,691 -> 5,712 go-gate lines (+552 shell = 6,264 total).
  rejections, 66 benign) + 9 shell mutations exit 0, production scan
  clean on all five targets, go test ./... (both tag sets), -race, vet,
  gofmt zero diffs, cross-compilation - all green.
- Round 7 (independent full-pass re-review): five residents passed the
  round-6 delta; the sixth returned a fresh fail with nine static-gate
  bypass classes outside the round-5/6 delta. All nine were probe-
  verified and fixed: P67 opaque function-field callees (h.cb(page)),
  P68 slice-indexed callees (fs[0](page)), P69 func-literal named
  results with naked returns, P70 pointer struct literals
  (&B{Data: page}).Data, P71 page boxing through any containers plus
  type assertions, P72 collection literals dropping definite element
  bounds, P73 package-global stores invisible to cross-function
  summaries, P74 string conversion of page views with unknown bounds
  (the sink moved from definite-span to tainted-and-not-provably-
  sub-page; plain []byte parameters and minted record fields stay
  legal), P75 reflect byte extraction (reflect added to the banned
  import set). Model work: interface values are page carriers,
  derivedPageValue keeps definite bounds, slice expressions carry an
  honest bound, container literals aggregate element bounds, func-
  literal summaries get the named-result pass, &-wrapped composite
  literals carry field taints, and package-scope stores write through
  348 -> 357 (282 -> 291 rejections, 66 benign); gate tooling
  5,712 -> 5,906 go-gate lines (+552 shell = 6,458 total).
  rejections, 66 benign) + 9 shell mutations exit 0, production scan
  clean on all five targets, go test ./... (both tag sets), -race, vet,
  gofmt zero diffs, cross-compilation, audit - all green.
- Round 12 (delta re-review): luna failed the round-11 delta with
  fifteen findings (fourteen real, one rejected false positive), all
  probe-verified by the lead against a fresh HEAD build before any fix:
  P127 variadic parameter slots did not join trailing arguments
  (pick([]byte{1}, page) inside pick(xs ...[]byte) read only the first
  argument; trailing args now join into the variadic slot in evalCall,
  summary duplicates carry the variadic slot, and func-literal variadics
  bind every trailing argument); P128 var-decl struct initializers lost
  field taints (var b B = B{Data: page}; DeclStmt now records
  composite/call/index field taints); P129 nested selector stores were
  invisible (o.Inner.Data = page; assignTarget now records dotted paths
  through selectorChain); P130 indexed call-result struct fields lost
  page bounds (makeList(page)[0].Data; propagateStructResult records
  container-of-structs element fields and the indexed-call selector
  branch re-evaluates the call before reading its recorded fields);
  P131 opaque callbacks with field-only page values escaped (cb(b) with
  b.Data = page; whole-value field promotion now runs on every missed
  and resolved argument and on the interface-miss receiver); P132
  unknown-bound views collapsed to zero inside struct fields
  (B{Data: m.View(0, n)}; evalComposite now propagates maxUnknown field
  values); P133 string-param field conversion with a local struct var
  escaped (var b B; b.Data = page; sink(b) where sink stringifies the
  field; the local store now reaches the string-param call check
  through whole-value promotion); P134 interface methods with an
  empty-interface result escaped (m.Make() any on a parameter and
  mgrVar.Make() any on a package interface variable; the
  interface-result rule now also fires for any-typed results, and
  func() any parameters and locals fail closed like opaque package
  function variables); P135 structs holding a file into an interface
  formal escaped (sink(H{F: f}) with sink func(any);
  checkInterfaceErasure now holds struct-with-file arguments); P136
  structs holding a file into runtime map keys escaped (m[H{F: f}] = 1;
  checkAssign mirrors the collection-slot rule); P138 else-nested branch
  reassignment diverged (if c1 ... else { if c2 ... }; joinWith now
  forwards ambiguous-binding state across both directions); P139
  package-initializer struct fields were invisible to later stores
  (var g = B{Data: pageSrc}; the pkg-init loop records struct-literal
  field taints into the shared package state and replays them); P140
  package method values resolved to stale initializers (var get =
  holder.Get; pkgBindings seed statement state, calleeTarget hands
  method values to resolveMethodValue with the receiver, and reassigned
  package variables are excluded). F10 (a named function type
  var f Fn; f(file) already flagged by the file-valued argument rule)
  was rejected as a duplicate class, not a new finding. One
  whole-value field promotion graduated fail-closed opaque-call struct
  fields (clean stdlib chains such as io.NopCloser(x.Get()()) over a
  bytes.Reader-like result were flagged); synthetic fail-closed
  callFields are now marked and excluded from promotion while field
  benign). Gate tooling 7,704 -> 8,097 go-gate lines (+552 shell =
  8,649 total).
  rejections, 67 benign) + 9 shell mutations exit 0, production scan
  clean on every scanned target, all fifteen round-12 probes reject on
  every scanned target, go test ./... (both tag sets), -race, vet (go
  and go-gate), gofmt zero diffs, cross-builds for all shell-harness
  targets (incl. netbsd), the import-graph gate with the 422-case
- Round 13 (delta re-review): luna failed the round-12 delta with seven
  findings, all probe-verified by the lead against a fresh HEAD build
  before any fix (probes /tmp/probe-r12l2, all exit 0 pre-fix and all
  reject after): P141 variadic ...any interface erasure escaped
  (sink(nil, file) with sink(xs ...any) stopped at the first formal;
  checkInterfaceErasure now evaluates each trailing argument against the
  variadic element type); P142 variadic string-param conversion escaped
  (sink([]byte{1}, page) with sink(xs ...[]byte){ string(xs[1]) } read
  only the first trailing argument; checkStringParamCalls now enumerates
  every trailing argument of a variadic slot, and noteStringConvs
  records index reads of parameter-sourced slices); P143 range and
  pointer rebinding of function bindings escaped (for _, f = range fs
  and *p = page-returning literal through p := &f left the old binding
  proof live; RangeStmt and bindLocalFunc now invalidate callable
  bindings); P144 nested struct-parameter fields escaped (take(o)
  returning o.Inner.Data with a caller-supplied nested page; the read
  side resolves dotted param paths through leafPathType and the copy
  and argument-flow fallbacks materialize every leaf path through
  paramLeafPaths); P145 map-key struct fields escaped (m[box{Data:
  page}] = 1 then for k := range m { k.(box).Data }; indexed stores now
  record struct-literal key fields on the container and key-only ranges
  bind them to the key variable); P146 method-value receiver
  string-conversion escaped (get := b.String; get() with String()
  converting b.Data; evalCall records resolved method values in
  callMethodValues and checkStringParamCalls reads the captured receiver
  from it); P147 non-empty interface result type-assert recovered a file
  (factory().(*os.File) with factory func() io.Reader; the capability
  launder now fails any assertion that recovers a file-bearing type out
  of an interface that *os.File itself satisfies, while scanned
  io.ReadCloser-shaped results stay benign per the content-transfer
  design); P148 (found by the lead in the same-failure search) type-
  switch cases recovered the same descriptor with the same escape
  (switch v := factory().(type) { case *os.File: return v }; the case
  (364 rejections, 67 benign). Gate tooling 8,097 -> 8,475 go-gate
  lines (+552 shell = 9,027 total).
  rejections, 67 benign) + 9 shell mutations exit 0, production scan
  clean on every scanned target, all eight round-13 probe files plus the
  type-switch recovery probe reject on every scanned target, go test
  ./... (both tag sets), -race, vet (go and go-gate), gofmt zero diffs,
  cross-builds for every shell-harness target, the import-graph gate
- Round 14 (delta re-review): luna failed the round-13 delta with six
  findings, all probe-verified by the lead on a fresh HEAD build before
  any fix (probes /tmp/probe-luna2, /tmp/probe-p16, /tmp/probe-luna5):
  P150 one-sided branch joins trusted an unproven local callable
  (g := f with f an opaque parameter, then g = func() body on one
  branch only; the old merge re-seeded the literal binding after the
  join so a later g() resolved a callee the other path may not hold;
  joinWith now marks one-sided local callable bindings ambiguous and
  calls through them fail closed); P151 struct-field provenance lost
  for selector-valued arguments (take(h.Box) with h.Box.Data assigned a
  page resolved only the whole-value taint; argFlowOf now flattens a
  selector base's recorded field taints, dropping the base path prefix,
  and falls back to parameter-derived leaf sources); P152 promoted
  embedded struct fields bypassed parameter-field resolution (take(o)
  returning o.Data with Data embedded through an anonymous inner
  struct; leafPathType and paramFieldType now resolve through
  types.LookupFieldOrMethod exactly like go/types selection, and the
  lookup package is passed because go/types hides unexported fields
  from a nil-package lookup - holder.data reads regressed without it);
  P153 container field provenance only for direct struct literals
  (m[b] = 1 with b.Data a page, plus the whole-value m[box{Data: page}]
  form: indexed stores now derive the key's field taints through the
  argument flow instead of the literal-only path; luna's original
  map[lbox]int shape was invalid Go, so the valid map[any] variant is
  the pinned form); P154 bounded param-field flow collapsed to unknown
  (b := box{Data: page[48:112]}; take(b) over-flagged after the
  round-13 param-field source because the paramField symbol case had no
  length resolution; eval/evalResults now resolve the field length from
  the call-site argument flow, keeping bounded flows legal); P155
  (records) the Status metrics were stale and contradicted the source
  round-13 close in f478278 while the Status still carried the
  rejections, 68 benign) and go-gate 8,611 lines (+552 shell = 9,163
  total), with this entry completing the trail. The lead's first
  full page through a package func-literal variable, P19 the two-hop
  chain, P140 package method values): the merge treated identical
  package initializer seeds as divergent (func-literal bindings have no
  stable text for the equality check) and let branch-local invalidation
  erase package-scope proofs; the merge now keeps identical nodes and
  exempts package-scope callables (their binding is proven by the
  package initializer and reassignment is policed by reassignedVars).
  8,611 go-gate lines (+552 shell = 9,163 total).
  rejections, 68 benign) + 9 shell mutations exit 0, production scan
  clean on every scanned target, all round-14 probe files reject (or
  stay accepted for the bounded form) on every scanned target, go test
  ./... (both tag sets), -race, vet (go and go-gate), gofmt zero diffs,
  cross-builds for every shell-harness target, the import-graph gate
- Round 15 (delta re-review): luna failed the round-14 delta with six
  findings, all reproduced by the lead before any fix (probes
  /tmp/probe-luna14, /tmp/probe-luna15): P155 switch-without-default
  kept the pre-switch callable binding reachable after the join (g := f
  with f an unproven parameter, one case rebinds g to a closure, no
  default: the old switchJoin dropped the pre-switch state so a later
  g() resolved the closure on every path; switchJoin now tracks the
  default clause and joins the pre-switch state when none exists, the
  same path zero-iteration loops take); P156 dereferenced arguments
  lost struct-field provenance (take(*p) with p := &b and b.Data a
  page: argFlowOf handled only Ident/Selector/CompositeLit/CallExpr;
  the StarExpr case now resolves the pointed-to object through recorded
  pointer bindings and alias chains (derefTarget) and materializes its
  fields (fieldTaintsOf), with the declared leaves of a struct-pointer
  parameter as the fallback); P157 indexed arguments lost container
  element provenance (take(xs[0]) with xs := []box{{Data: page}} and
  container parameters: the IndexExpr case resolves the container
  object and reads its element fields, and container parameters
  ([]box, map[K]box, chan box) expose their element leaves through
  paramFieldFallback); P158 type-asserted arguments lost provenance
  (take(v.(box)) with v any holding box{Data: page}: the TypeAssertExpr
  case unwraps Ident/StarExpr/CallExpr bases and reuses the same field
  resolution); P159 a naked return of one multi-valued call forwarded
  only the first result slot (return source6(p) with (error, []byte)
  and _, b := wrap(page): the per-result loop evaluated the call once
  and bound only slot 0; the ReturnStmt case now re-evaluates the call
  in the current state, distributes every callResults slot and the
  callFields, and falls through to the per-result loop for calls with
  no per-result records (mints like r.m.Page(pgno), whose early-return
  specialization records neither)); P160 a concrete method on a type
  declared OUTSIDE the scanned module could mint a file descriptor
  (net.TCPListener.File() returns a real *os.File with a net-package
  receiver, invisible to the os-only selector check: the selector mint
  branch now fails closed for concrete external-package receiver
  methods; not probe-pinnable because the loader cannot resolve net
  and no stdlib external concrete method returns *os.File, so the fix
  is code-review-verified and the scanned-tree behavior is unchanged -
  external package bodies were already skipped). The lead disproved
  the channel finding (all eight f5 channel probe shapes were already
  rejected, so no gate change was needed for send/receive paths) and
  the map-literal shape (a []byte field makes the struct key
  non-comparable, so m[lbox]int is invalid Go; the valid map[any]
  -> 375 rejections, 68 benign). Gate tooling 8,611 -> 8,805 go-gate
  lines (+552 shell = 9,357 total).
  rejections, 68 benign) + 9 shell environment mutations exit 0,
  production scan clean on every scanned target, all round-15 probe
  files reject (or stay accepted for the bounded form) on every
  scanned target, the round-14 probe trees (/tmp/probe-luna2,
  /tmp/probe-p16, /tmp/probe-luna5) return the same verdicts as a
  clean HEAD build (luna2/p16 rejected, luna5 accepted), go test
  ./... (both tag sets), -race, vet (go and go-gate), gofmt zero
  diffs, cross-builds for every shell-harness target, the import-graph
- Round 26 (full re-review after restart, 3ca6218): the swarm
  re-reviewed at 3ca6218 after the transport restart. MiMo PASS, kimi
  PASS, minimax PASS, qwen confirmed no additional findings. Luna
  returned FAIL with two findings: (1) P1 — Go OpenImmutable maps the
  entire physical file before bootstrap, violating the O(1) bootstrap
  and committed-extent-only mapping rules (spec section 3); Rust
  map_reader maps 2 pages, bootstraps, then remaps to committed_bytes.
  (2) P2 — the content-transfer gate scans 5 OS configs while the
  records claim 11-target cross-compilation coverage. The P1 was
  verified against Rust database_file.rs:106-114 and mapping.rs:262-266.
  The fix: OpenImmutable now maps exactly 2 pages for bootstrap, the
  reader bootstraps from those pages, then calls the new
  Mapping.Remap(committedBytes) which grows the mapping in place via
  mremap(MREMAP_MAYMOVE) on Linux and munmap+mmap on other POSIX
  targets; the Windows stub gains Remap and PhysicalSize stubs. The
  reader bootstrap now uses PhysicalSize (the locked file extent) for
  meta validation instead of the mapped size. A new regression test
  verifies that a 1 GiB corrupt-tail file fails bootstrap with
  FormatInvalid; the O(1) property is structural (2-page mmap →
  bootstrap → Remap only on success). The P2 was analyzed: the 5 OS configs (linux, darwin,
  freebsd, netbsd-as-other-POSIX, windows) cover every distinct
  mapping-owner code path; the remaining 6 targets (linux/386, arm,
  arm64, loong64, darwin/arm64, windows/arm64) compile the same source
  files as the scanned configs, so the typed scanner sees identical
  targets. The records were updated to state this explicitly.
  audit all pass.
- Round 25 (delta re-review, 0018a41): the round-24 closing commit
  review FAILed with ten findings across two residents: luna reported
  six gate escapes and one records nit; glm reported two reader P2s and
  two records nits. The lead probe-verified every finding on the true
  round-24 build: all six luna shapes escape (rc 0) and the seven pin
  shapes reject (rc 1) after the fixes; a same-family sweep then found
  three further escape shapes (asserted field-map key ranges and
  returned/bound asserted selectors under interface-typed switch
  cases), pinned as P240-P242; glm-1 is a false positive with
  full evidence below; glm-2 is a real reader divergence. Real classes:
  P233 type-asserted INDEXED CHANNEL send and receive
  (v.(*H).Chs[0] <- lbox{Data: page}; y := <-v.(*H).Chs[0]; y.Data
  with v an any holding *H): recordChanSendFields and chanRecvFields
  resolved the index-chain root only through objOfDeref, so a
  TypeAssertExpr root bound nothing and the received element laundered
  the page; the new chainRootObject resolves the asserted base
  variable, and both sites now record and resolve under the "Chs."
  prefix;
  P234 type-asserted INDEXED STRUCT stores (v.(*H).Items[0] =
  lbox{Data: page} then v.(*H).Items[0].Data): the store recorded
  element fields only on plain and dereferenced roots; the store's
  selectorIndexChain root now resolves through chainRootObject, the
  same object the typeAssertBaseOf read path resolves;
  P235 type-asserted FIELD MAP key stores (v.(*H).M[&b] = 1 after
  b.Data = page, with for k := range v.(*H).M reading k.Data): the
  map-key store bound only selectorChain or dereference roots; the
  branch now also handles a typeAssertBaseOf root and records the key
  fields under the field prefix on the asserted base variable, the
  same path the asserted key-only range resolves;
  P236 address-of TYPE-ASSERTED INDEXED element mutations
  (set(&v.(*H).Items[0], page) with set(b *lbox, p []byte) { b.Data =
  p }): applySummaryMutations' IndexExpr branch resolved the
  selectorIndexChain root through objOfDeref only; it now binds the
  asserted base under the "Items." prefix;
  P237 returned TYPE-ASSERTED indexed elements of interface
  parameters (return v.(*H).Items[0] with v an any parameter):
  propagateStructResult's IndexExpr branch handled only plain and
  dereferenced roots; an interface-typed parameter asserted to a
  holder now projects the asserted type's "Items."-prefixed leaf paths
  with the parameter source, exactly like `return v.(T)`;
  P238 returned indexed elements of CALL-PRODUCED selected containers
  (return makeH(p).Items[0]): propagateStructResult only handled an
  indexChainRoot call; a selected call-produced root now re-evaluates
  the call and strips the "Items." prefix from the callee's flattened
  element fields;
  P239 type-switch `case any` variables (switch x := h.get().(type) {
  case any: b := x.(lbox); return b.Data }): an interface-typed case
  projected no concrete leaves and the implicit variable carried no
  whole-value taint, so the body-side assertion laundered the page;
  typeSwitchJoin now binds whole-value taint on the implicit variable
  of interface-typed cases, and argFlowOf projects the asserted
  type's leaves for bind-then-read forms of any whole-tainted local
  interface value.
  Same-family sweep (three further shapes, probes 8, 9 and 11,
  escaped the pre-fix build and reject now):
  P240 type-switch `case any` + asserted FIELD MAP key range
  (switch x := h.get().(type) { case any: for k := range
  x.(*H).M { return k.Data } } with get returning *H{M:
  map[*lbox]int{{Data: page}: 1}}): the same interface-typed-case
  join gap left the asserted map-key store with no bound leaf, so
  the range laundered the page; typeAssertBaseOf stores and the
  whole-tainted case variable close it;
  P241 returned type-asserted SELECTOR values of interface
  parameters (return v.(*H).Inner with v an any parameter and
  H.Inner holding the page): propagateStructResult's SelectorExpr
  branch never projected an interface-parameter asserted base, so
  the returned struct laundered the page; the new
  typeAssertBaseOf branch projects the asserted type's leaf paths
  with the parameter source and propagates whole-tainted locals;
  P242 type-switch `case any` binding an asserted SELECTOR
  (switch x := h.get().(type) { case any: b := x.(*H).Inner;
  return b.Data }): the case bind of an asserted selector lost
  every leaf (no typeAssertBaseOf projection on the implicit
  variable), so the bound struct laundered the page; the
  SelectorExpr argument flow now projects whole-tainted locals'
  asserted leaves.
  Reader: glm-1 (membership id zero treated as corruption) is a FALSE
  POSITIVE: spec binary-format-v4.md:562-567 forbids storing zero as a
  membership id, and both readers reject a stored zero at lookup (Go
  lookupMembershipID corrupts, Rust membership_tree find -> require_id
  corrupts), so no valid file diverges. glm-2 is REAL: ContainsIndex
  read its word through wordBytes and skipped the trailing-zero
  canonical check that Word, ReadWords and Rust contains_index all
  apply (spec section 9); ContainsIndex now reads through
  readWordsInner so a zero FINAL word is corrupt here too, with a
  runtime regression test that patches the conformance fixture bitmap
  (mirroring membership_view_tests.rs word(1) on an inline [1, 0]
  image).
  Records corrections: the round-24 validation sentence "119 reject"
  is corrected to 120 above (the 125 prior trees are 120 rejections
  plus the five recorded benigns l18m, r10-base, r11-base, luna1b,
  luna1c); the round-24 and round-23 "ten cross-builds" sentences are
  corrected to eleven (the harness lists 11 GOOS/GOARCH targets
  including netbsd/amd64); the milestone-1 report "4 packages" and
  "10 pairs" are corrected to 5 packages (root, format, reader,
  mapping, work) and eleven pairs.
  ten new fail pins P233-P242). Gate tooling 10,801 -> 11,016
  go-gate lines (+552 shell = 11,568 total).
  rejections, 71 benign) + 9 shell environment mutations exit 0,
  production scan of v4/go clean (rc 0), all eleven r25 probe shapes
  reject (rc 1) on the closing build (one probe per isolated tree),
  go test ./... (both tag sets) including -race -count=1, vet (go and
  go-gate), gofmt zero diffs, eleven cross-builds, the import-graph

- Round 24 (delta re-review, d482c1c): luna failed the round-23
  closing commit with seven findings; the lead probe-verified all six
  code findings on the true round-23 build (probes
  /tmp/probe-l23-l1..l6, with l2a/l2b as the literal-return control
  and the captured-assignment escape) and confirmed the seventh is a
  records contradiction. Real classes, all fixed in this round:
  l1 foreign EXPORTED struct fields in an unproven callee result were
  skipped wholesale by failClosedCallFields (the structDeclPkg !=
  pc.pkg early return): with a LOADABLE foreign package
  (encoding/pem), an unproven call result pem.Block{Bytes: page}
  laundered the page through the exported field read. The
  round-23 l23-1/1b/1c probes of this shape were dismissed there as
  unreachable because their imports are banned or fail to type-load;
  the pem variant proves the code gap is real whenever the foreign
  package loads. The guard now walks foreign EXPORTED fields and
  skips only foreign UNEXPORTED fields (the bytes.Reader.src shape
  the existing benign pins rely on);
  l2 a directly called func-literal COMPOSITE LITERAL argument
  (func(x B) { out = x.Data }(B{Data: page})) lost the field taint:
  materializeStructFields returned early on a nil ident, so the
  argFlowOf fallback was unreachable for composite literals; the
  ident-nil path now falls through to argFlowOf;
  l3 INDEXED SELECTOR-rooted channel sends and receives
  (h.Chs[0] <- B{Data: page}; x := <-h.Chs[0] with h.Chs []chan B)
  lost element provenance: the indexChainRoot-only recording missed
  selector roots, so recordChanSendFields and chanRecvFields now
  record and resolve the base object under the "Chs." prefix;
  l4 returned INDEXED SELECTOR values (return h.Items[0] with
  h.Items []lbox) lost the element records: propagateStructResult's
  IndexExpr branch only resolved the indexChainRoot; it now also
  strips the "Items."-prefixed records on the base object;
  l5 address-of INDEXED SELECTOR elements (&h.Items[0] passed to a
  pointer-mutation helper) never bound the container:
  applySummaryMutations' IndexExpr branch only handled the
  indexChainRoot; it now binds the base object under the "Items."
  prefix;
  l6 MULTI-TYPE type-switch cases (switch v := h.get().(type) {
  case B, *B: ... }) lost the case leaves: go/types types the
  implicit variable with the guard's interface type, so
  paramLeafPaths(cv.Type()) projected nothing; typeSwitchJoin now
  projects the page-carrying leaves of every listed case type.
  Records contradiction: the round-23 validation sentence said
  507/507 (437 rejections, 69 benign) while the count line correctly
  says 70 benign; the sentence is corrected above.
  acceptances; six new fail pins P226-P231 and one benign pin P232).
  Gate tooling 10,695 -> 10,801 go-gate lines (+552 shell = 11,353
  total).
  rejections, 71 benign) + 9 shell environment mutations exit 0,
  production scan of v4/go clean (rc 0), all eight l24 probe trees
  reject (rc 1) on the closing build (l1..l6 plus the l2a literal
  return and l2b captured-assignment forms), all 125 prior probe
  trees re-scanned with the closing build: 120 reject and the five
  recorded benigns stay accepted (probe-l18m, probe-r10-base,
  probe-r11-base, luna1b/luna1c), all twenty round-23 l23 probe
  trees keep their verdicts on the closing build (all reject except
  the self-pointer benign q2 rc 0), go test ./... (both tag sets)
  including -race -count=1, vet (go and go-gate), gofmt zero diffs,
  and the SOW audit all pass.

- Round 23 (delta re-review, 07081a2): luna failed the round-22
  delta with seven findings; the lead probe-verified all seven on the
  true round-22 build (probes /tmp/probe-l23-1..7 with lettered
  variants): five were real escape classes, two are false positives,
  and one reported variant is invalid Go. Real classes:
  P219 type assertions on func-field any call results
  (h.get().(B).Data with get func() any) projected no asserted leaves:
  the base's whole-value taint was only projected inside the objOf
  guard, so a DIRECT call base with no binding object never ran the
  projection; the projection now runs for every assertion base after
  the recorded-field and parameter paths;
  P220 type-switch variables from the same base (switch v :=
  h.get().(type) { case B: v.Data }) carried no asserted leaves:
  typeSwitchJoin mirrors the value-switch join and records the
  page-carrying leaves of every matched case type on the implicit
  per-case variable (info.Implicits) when the asserted base is
  whole-tainted;
  P221 pointer-mutation methods on INDEXED elements (xs[0].Set(page))
  never bound the container: applySummaryMutations now recognizes
  chainContainsIndex receivers and binds the root container's element
  records, under the selected field path when the receiver carries
  one;
  P222 indexed channel send/receive (cs[0] <- B{Data: page}; x :=
  <-cs[0]; x.Data with cs []chan B) lost element provenance:
  recordChanSendFields and chanRecvFields now record and resolve the
  indexChainRoot container;
  P223 pointer-wrapped map parameters (m *map[*B]int under a key-only
  range) lost the key leaves: mapUnderlying unwraps the pointer layer.
  False positives, probe-verified already rejected on the pre-fix
  build (no change): l23-1/1b/1c foreign exported struct fields
  skipped by failClosedCallFields are unreachable - image is a banned
  import and net/crypto/x509 fail to type-load (missing vendored
  x/net), so the gate fails closed on the type-check error; l23-4/4b/
  4c variadic direct func-literal argument flow is rejected in every
  probe shape. Invalid-Go variant: the l23-5 function-call form
  set(xs[0], page) does not type-check (an indexed element is not
  addressable without &), so only the method form is a real class.
  unchanged). Gate tooling 10,463 -> 10,648 go-gate lines (+552
  shell = 11,200 total). Five real escape classes fixed; two false
  positives documented with probe evidence; one invalid-Go variant
  recorded; none dismissed.
  The same round's full-scope pass then returned two further verified
  findings (Qwen), both probe-verified before any fix:
  P224 a two-value type assertion or map index (b, ok := v.([]byte), b,
  ok := m[k]) types the expression node as the (T, bool) tuple, and the
  whole-value carrier test (evalExpr TypeAssertExpr/IndexExpr/
  IndexListExpr) read typeCanCarryPage(tuple) as false, laundering the
  page taint of every comma-ok byte read into the bound variable
  (probe /tmp/probe-l23-q1 exited 0 pre-fix; valueCarrierType now
  unwraps the value slot of a 2-tuple before the carrier test);
  P225 a self-referential named pointer (type P *P) routed through the
  unproven-callee result walk hung the scanner: derefStruct's Named
  unwrap loop and mapUnderlying's pointer unwrap (added for the l23-7
  fix) had no seen guard, so failClosedCallFields(P) recursed forever
  (probe /tmp/probe-l23-q2 timed out at 90s pre-fix; both walkers now
  stop at the revisiting named type, mirroring containerElemTypeSeen).
  acceptances). Gate tooling 10,648 -> 10,695 go-gate lines (+552
  shell = 11,247 total).
  rejections, 70 benign) + 9 shell environment mutations exit 0,
  production scan of v4/go clean (rc 0), all sixteen l23 probe trees
  reject (rc 1) on the closing build, all 125 prior probe trees
  re-scanned with the closing build: 120 reject and the five recorded
  benigns stay accepted (probe-l18m, probe-r10-base, probe-r11-base,
  luna1b/luna1c), the q1/q2 verification probes reject or complete as
  pinned (q1 two-value assert rc 1, q1b two-value map index rc 1, q2
  self-pointer rc 0 without hang, q2b func-param form rc 0), go test
  ./... (both tag sets) including -race -count=1, vet (go and go-gate),
  gofmt zero diffs, eleven cross-builds, the import-graph gate, and the
  SOW audit all pass.
- Round 22 (delta re-review, 113867a): luna failed the round-21
  delta with nine findings; the lead probe-verified all nine on the
  true round-21 build (git archive HEAD, probes /tmp/probe-l22-1..9):
  eight were real gate escapes, one (l22-4/4b, func-literal closure
  materialization of a call argument) is a false positive: the probes
  carry the page view through the literal parameter to its result and
  then into append, a real complete-page copy the gate correctly
  rejects (rc 1 on both the pre-fix and closing builds), and one
  (l22-9) is a real false rejection fixed below. Fixed with durable
  pins P210-P218:
  P210 nested opaque carriers (x.Outer().Items[0].Data with Outer
  returning an unscanned interface result whose container FIELDS
  expose element leaves): containerElemTypeSeen and leafNameSet now
  unwrap *types.Pointer and recurse container fields at any depth;
  P211 two-variable map ranges over container VALUES and
  pointer-wrapped container KEYS keep their element leaves;
  P212/P213 address-of SELECTED FIELD and INDEXED ELEMENT arguments
  (&h.Inner, &xs[0]) bind mutation summaries to the base object and
  the container element fields; P214 directly called func-literal
  pointer-parameter mutations export their summary and re-bind at
  the call site, with the literal's recorded sources rebased to its
  own parameter slots; P215 struct-field channel send/receive and
  select-clause sends keep the "Ch."-prefixed provenance (selector
  and alias channel shapes); P216 indexed whole-struct stores through
  DEREFERENCED containers ((*q)[0] = B{Data: p}) record on the
  pointer and its alias target; P217 runtime map-key stores through
  field containers (h.M[&b] = 1) record key fields under the field
  prefix; P218 benign bounded struct-field helper copies stay legal:
  applySummaryMutations now skips writes whose recorded sources are
  all clean and reports tainted only when an argument is actually
  tainted (the previous over-taint rejected clean arg copies), and
  failClosedCallFields was rewritten as a package-aware recursive
  leaf walker that stops at foreign struct declarations so stdlib
  private fields (bytes.Reader.src) never taint benign copies.
  acceptances). Gate tooling 10,045 -> 10,463 go-gate lines (+552
  shell = 11,015 total). Eight real escapes fixed; the l22-4/4b
  false positive documented with probe evidence (no change needed);
  the l22-9 false rejection fixed; none dismissed.
  rejections, 69 benign) + 9 shell environment mutations exit 0,
  production scan of v4/go clean (rc 0), all nine l22 probes behave
  as pinned (rc 1 for l22-1/2/3/4/4b/5/6/7/8, rc 0 for l22-9 on the
  closing build; pre-fix the eight escapes exited 0 and l22-9 exited
  1), all 125 prior probe trees re-scanned with the closing build:
  119 reject (all round-19/20/21 shapes) and the five recorded
  benigns stay accepted (probe-l18m, probe-r10-base, probe-r11-base,
  luna1b/luna1c), go test ./... (both tag sets) including -race
  -count=1, vet (go and go-gate), gofmt zero diffs, the import-graph
  gate, and the SOW audit all pass.
- Round 21 (delta re-review, d5d5be9): luna failed the round-20
  delta with eight findings; the lead probe-verified all eight as real
  escapes on the round-20 build before any fix (probes /tmp/probe-l20-
  1/2/3/4/5/6/7/8) and fixed them with durable pins:
  P200 a TWO-variable map range bound the key fields to the VALUE
  variable (for k, v := range m with m map[*box]int lost k.Data); the
  range branch now detects map ranges and splits the recorded entry
  fields between the key and the value variable by the declared leaf
  names of the map key and element types;
  P201 a container literal element produced by a CALL lost its fields
  (xs := []box{makeBox(p)}; xs[0].Data) and P202 an element that is
  the ADDRESS of a variable lost them ([]*box{&b}); elementFieldsOf
  now falls back to argument-flow renaming, and argFlowOf resolves
  &b through the pointed-to variable's recorded fields;
  P203 an indexed whole-struct store into a FIELD container lost its
  element provenance (h.Items[0] = B{Data: p}): the store now records
  under the "Items.Data" flattened path on the base object, the same
  path the read resolves;
  P204 a dereference store through an ALIASED pointer lost the original
  variable (q := &b; *q = B{Data: page}; b.Data): the StarExpr store
  and the selector-field store now also record on the derefTarget
  alias, so reads under either name stay sourced;
  P205 pointer-receiver METHOD mutations never reached the caller
  (b.Set(p) writing b.Data = p; b.Data): funcSummary gained a
  mutFields map exported from the callee's final pointer-parameter
  field records (tainted stores only, value params excluded);
  applySummaryMutations re-binds the recorded sources to the actual
  argument values at every resolved call site, including receivers,
  address-of arguments, and field-chain receivers;
  P206 struct field provenance died at a channel send/receive (ch <-
  B{Data: page}; x := <-ch; x.Data): the send records the sent
  value's fields on the channel variable (tainted sends join; clean
  sends never erase), and argFlowOf's receive case binds them to the
  received variable;
  P207 an address-of argument lost the variable's fields (takePtr(&b)):
  argFlowOf resolves the & operand through the recorded fields;
  P208 an interface method call returning a CONTAINER failed open on
  element fields (x.Boxes()[0].Data): both unprovable-callee paths
  now fail closed on the page-carrying element and key leaves of
  container results (failClosedCallFields), matching the existing
  direct-struct-field policy;
  P209 a range over a TYPE-CONVERTED container lost the element leaves
  (for _, x := range []B(h.Items)): the range call branch detects
  conversions and resolves the operand's element fields.
  Gate tooling 9,648 -> 10,045 go-gate lines (+552 shell = 10,597
  total). All eight were real; no finding was dismissed.
  rejections, 68 benign) + 9 shell environment mutations exit 0
  probe trees reject (rc 1; the 30 round-19/20 shapes and the eight
  round-21 shapes), luna1b/luna1c stay accepted as the recorded known
  open question, go test ./... (both tag sets) including -race
  -count=1, vet (go and go-gate), gofmt zero diffs, the import-graph
  gate, and the SOW audit all pass.
- Round 20 (delta re-review, 64c8c13): luna failed the round-19
  delta with six findings; the lead probe-verified three real escape
  families on the round-19 build before any fix and pinned them:
  (a) selector-held container elements lost provenance in five shapes
  (for _, x := range h.Items, h.Items[0].Data, take(h.Items[0]),
  for _, x := range m[0].Items with m [1]holder, and a struct-field
  MAP key-only range for k := range h.Keyed): the range path now
  resolves selector/index/assert/star container roots through
  argFlowOf, compositeFields records container-field element fields
  under the "Field." prefix, paramLeafPaths emits the prefixed element
  leaves at every container depth plus map KEY leaves, and argFlowOf's
  index branch resolves selector/assert roots through the renamed
  fields - pinned P193-P197; (b) NAMED map types lost key provenance
  (type M map[*box]int; for k := range m with m M): types.Unalias
  does not unwrap named types, so the new mapUnderlying helper unwraps
  the named underlying chain and compositeFields/paramFieldFallback
  use it for the key-side leaves and literal key unions - pinned P198;
  (c) a dereference struct store lost field provenance (*p = B{Data:
  page} then p.Data and (*p).Data): the AssignStmt StarExpr branch
  now records the struct's field taints on the pointed-to object
  (including the package-level join) instead of only whole-value
  stores - pinned P199 with two create ops. The other three findings
  were probe-verified false positives: (1) unproven scalar indirect
  calls (func-field, interface-method, local func variable, and
  package-captured callable shapes) all reject on the round-19 build;
  (3) selector-valued arguments after a clean sibling store (clean
  sibling struct, clean container field, pointer parameter, and
  call-produced shapes) all reject; (5) append-built struct containers
  (literal plus variable append, indexed store, and parameter-sourced
  rejections, 68 benign acceptances). Gate tooling 9,497 -> 9,648
  go-gate lines (+552 shell = 10,200 total).
  rejections, 68 benign) + 9 shell environment mutations exit 0
  scanned target, every round-19 and round-20 probe shape escapes the
  fresh HEAD build (rc 0) and rejects the closing build (rc 1; 30/30
  probe trees), luna1b/luna1c stay accepted as the recorded known open
  question, go test ./... (both tag sets) including -race -count=1,
  vet (go and go-gate), gofmt zero diffs, the import-graph gate, and
  the SOW audit all pass.
- Round 19 (delta re-review): luna failed the round-18 delta with
  twelve findings; the lead reproduced eight real escape classes on a
  fresh HEAD build before any fix (probes /tmp/probe-luna2/4a/4b/5/6/7/
  8b/8c/9): P183 an EMPTY default (default:) discarded the pre-switch
  state (the old code joined only when no default existed, so a switch
  with a no-op default lost the page held before the statement);
  P184 a returned selected field of a call lost provenance (func
  retSel(p []byte) inner { return makeBox(p).Inner }: the return
  summary resolved only whole values, so the call-site read x :=
  retSel(page); x.Data was unsourced; propagateStructResult now strips
  the selection prefix from the base's recorded fields, parameter
  leaves, or callee flattening); P185 a returned element of an INLINE
  literal index lost provenance (return []box{{Data: p}}[0]: the
  index-return branch had no composite-literal root handling, now
  resolved through the literal's element-field union); P186 a
  container PARAMETER under range never bound its declared element
  leaves (for _, x := range xs with xs []box: the range path resolved
  only recorded local containers and inline literals, so the loop
  value carried no parameter-sourced fields; the range branch now
  applies paramFieldFallback to a parameter container); P187 a nested
  selector read after an interface assertion resolved only the
  asserted type's DIRECT leaf (v.(outer).Inner.Data leaked: the
  assertion branch read the recorded map by the single field name;
  typeAssertBaseOf now unwraps the full dotted chain and assertLeafType
  resolves the path against the asserted type's leaves, including
  container element leaves); P188 an asserted struct VALUE bound to a
  take argument lost its asserted leaves (take(v.(outer).Inner)):
  argFlowOf's selector branch had no assertion-root case; the new
  branch renames the source leaves to the selected value's direct
  field names exactly like recorded-base arguments); P189 a NAMED
  container type unwrapped no underlying chain (type matrix [1][1]box:
  containerElemType looked only at direct alias types, so the matrix
  parameter exposed no element leaves; the recursive
  containerElemTypeSeen unwraps named underlying chains with a
  self-reference guard and paramFieldFallback applies it at every
  level); P190 a map-key PARAMETER kept no key leaves for a key-only
  range (for k := range m with m map[*box]int: the key-side leaf
  fallback existed only for recorded stores; paramFieldFallback now
  adds the declared key leaves and key container chains). The lead's
  same-failure sweep over those fixes proved two further escapes on
  the same HEAD build and pinned them here: P191 a container literal
  with a VARIABLE element lost the element fields (b := box{Data:
  page}; xs := []box{b}: compositeFields resolved only literal
  elements; the new elementFieldsOf resolves variables and parameters
  through the recorded fields and nested containers through
  recursion); P192 a map composite-LITERAL key lost its struct fields
  through the key-only range (m := map[*box]int{{Data: page}: 1}:
  compositeFields now unions the KEY side exactly like the value
  acceptances). Gate tooling 9,234 -> 9,497 go-gate lines (+552 shell
  = 10,049 total). The remaining findings were verified not real:
  luna1b/luna1c (the two-value asserted-container bind and the
  interface-parameter container element) are intentionally accepted
  shapes and stay accepted as a known open question, three reports
  were false positives or dead code, and the stale-record report
  by this record sync.
  rejections, 68 benign) + 9 shell environment mutations exit 0,
  production scan clean on every scanned target, every round-19 probe
  shape escapes the fresh HEAD build (rc 0) and rejects the closing
  build (rc 1), the P172 two-value-assertion regression probe stays
  rejected, l19 and all prior probe trees keep their verdicts
  (luna1b/luna1c stay accepted), go test ./... (both tag sets), vet
  (go and go-gate), gofmt zero diffs, the import-graph gate with the
- Round 18 (lead same-failure sweep over the round-17 fixes): the
  interface-type-assertion family had three real escapes on a fresh
  HEAD build before any fix (probes /tmp/probe-l19): P171 a returned
  asserted interface-param struct lost caller provenance (func as(v
  any) box { return v.(box) } with x := as(any(box{Data: page})):
  propagateStructResult only handled literals, indexes, and
  parameters, so a returned type assertion over an interface
  parameter carried no leaves and the explicit any() conversion
  dropped the argument's fields; the return case now joins the
  asserted type's leaves with the parameter source, and single-arg
  type conversions keep the converted argument's field provenance);
  P172 a two-value asserted read inside a helper lost caller
  provenance (if b, ok := v.(box); ok { return b.Data } with v any:
  two-value assertions type the expression node as the (T, bool)
  tuple, so every assertion site must read the asserted type from the
  assertion's TYPE EXPRESSION, not from the expression node's type;
  the evalExpr direct-read, argFlowOf helper-binding, and
  propagateStructResult return paths now materialize the asserted
  type's leaves for interface-typed parameters, fixing both the
  helper-side binding and the direct v.(T).Data read). The same sweep
  then verified the IndexListExpr family on a clean self-contained
  tree after the earlier probe tree proved poisoned: P173 a field read
  through a multi-level index of a literal-bound matrix escaped (m :=
  [1][1]box{{{Data: page}}}; append(..., m[0][0].Data...): evalExpr's
  selector index branch and argFlowOf's IndexExpr binding resolved
  only ONE trailing index, so the root container's element-field
  taints were unreachable at depth two); P174 the same one-level gap
  for element bindings (x := m[0][0] then take(x)); P175 a forced
  element extraction from a literal escaped the bind path (x :=
  []box{{Data: page}}[0]: argFlowOf's IndexExpr case had no
  composite-literal root handling at all, unlike the read path);
  P176 a multi-level index of a call result stayed rejectable only
  through an unrelated path and is now resolved explicitly by the
  same root unwrap. A shared indexChainRoot walk now unwraps every
  trailing index level to the root expression, and the read, bind,
  and return sites dispatch the root through the same
  literal/ident/call resolution the one-level cases used. The same
  sweep then found the return-path half of the class: P177 a returned
  matrix element lost provenance (func retElem(m [1][1]box) box {
  return m[0][0] } with append(..., retElem([1][1]box{{{Data:
  page}}}).Data...): propagateStructResult's IndexExpr return branch
  resolved only ONE trailing index, so the root's recorded fields and
  parameter leaves were unreachable; the return branch now unwraps
  the full index chain) and P178 the bound form (x :=
  retElem([1][1]box{{{Data: page}}}); take(x)). Closing P177 required
  the declared-leaf fallback to unwrap every container depth:
  paramFieldFallback materialized element leaves for only ONE
  container level ([]box worked, [1][1]box exposed nothing), so the
  matrix parameter lost its caller fields even after the return
  branch could name the root; the fallback now loops over every
  392 rejections, 68 benign). Gate tooling 9,003 -> 9,136 go-gate
  lines (+552 shell = 9,688 total). The same sweep then closed the
  selector-over-index half of the family: P179 a nested field read
  through a multi-dim indexed local escaped (m := [1][1]outer{{{Inner:
  inner{Data: page}}}}; append(..., m[0][0].Inner.Data...): selectors
  above the index chain never reached the root record, because the
  indexed selector branch entered only when the base under the outer
  field name was directly an IndexExpr); P180 the bound form (x :=
  m[0][0].Inner then take(x)); P181 the inline literal form
  ([1][1]outer{{{Inner: inner{Data: page}}}}[0][0].Inner.Data); and
  P182 pins the nested-selection call-result form (makeMatrix(page)[0]
  [0].Inner.Data, already rejectable through the call-root branch).
  selectorIndexChain now collects every selector name above and
  between index levels, evalExpr reads the full dotted path on the
  root record (with the declared-leaf fallback for container
  parameters), and argFlowOf's selector case strips the collected
  wrapper path onto the selected value's direct field names for
  396 rejections, 68 benign). Gate tooling 9,136 -> 9,234 go-gate
  lines (+552 shell = 9,786 total).
  rejections, 68 benign) + 9 shell environment mutations exit 0,
  production scan clean on every scanned target, all round-18 probe
  shapes reject (l19 type-assertion trees, multi-dim index read/bind/
  return trees, forced literal extraction, and selector-over-index
  trees) and every prior probe tree keeps its verdict (l17b, l16,
  luna15, f2only2, luna2, p16 reject; luna5 stays accepted), go test
  ./... (both tag sets), -race, vet (go and go-gate), gofmt zero
  diffs, cross-builds for every shell-harness target, the import-graph
- Round 17 (lead same-failure search over the round-16 fixes): the
  round-16 call-base fallbacks covered direct argument positions only;
  the lead verified five escape families on a fresh HEAD build before
  any fix (probes /tmp/probe-l17b): P166 a struct value bound from a
  call-produced container element lost the element fields (x :=
  makeList(page)[0] then take(x): the AssignStmt and DeclStmt field
  switches resolved only recorded variable bases, while argFlowOf's
  round-16 fallback applied to direct arguments; both binding sites now
  resolve non-literal right sides through argFlowOf, the same
  resolution direct argument flow uses, and materializeStructFields
  applies the same fallback to closure parameters); P167 a bound
  selected field of a call-produced struct lost provenance (x :=
  makeBox(page).Inner: the argFlowOf selector call-branch built its
  chain from the base only, so the outermost selection was never
  stripped and the callee's flattened paths never matched; a new
  callRootChain walk now collects every selector name down to the
  producing call, and callProducedFields strips the full dotted prefix,
  shared by argument flow, binding sites, and closure-parameter
  materialization); P168 the same bound-selection gap for recorded
  local bases (x := b.Inner with b.Inner.Data a page: the binding sites
  had no SelectorExpr case at all; the selectorChain rename now
  applies, with the parameter fallback for parameter-held bases);
  P169 bound container-parameter elements lost the caller's leaves
  inside a helper (x := xs[0] with xs a container parameter:
  fieldTaintsOf's parameter fallback now feeds the binding resolution
  exactly like direct argument flow); P170 nested call-produced
  selector chains stayed reachable at every depth (makeBox(page)
  .Inner.Inner.Data reads, take(makeBox(page).Inner.Inner) arguments,
  and bound intermediates: propagateStructResult recorded only the
  top-level field name of a returned struct literal instead of the
  flattened dotted paths, so every nested selection missed; the
  returned-literal path now joins compositeFields' flattened records,
  and the evalExpr selector read resolves the full dotted chain through
  benign). Gate tooling 8,935 -> 9,003 go-gate lines (+552 shell =
  9,555 total).
  rejections, 68 benign) + 9 shell environment mutations exit 0,
  production scan clean on every scanned target, all round-17 probe
  shapes reject on every scanned target and every prior probe tree
  keeps its verdict (l16, luna15, f2only2, luna2, p16 reject; luna5
  stays accepted), go test ./... (both tag sets), -race, vet (go and
  go-gate), gofmt zero diffs, cross-builds for every shell-harness
  audit all pass.
- Round 16 (delta re-review): luna failed the round-15 delta at HEAD
  e03ecba with five findings; the lead reproduced four as real escapes on
  a fresh HEAD build before any fix (probes /tmp/probe-l16) and the fifth
  is code-review-verified (the loader cannot resolve net, no loadable
  external concrete method returns a page-bearing value, and scanned
  method values are approved callees): external method-value
  receivers bypassed the unprovable-receiver transfer check (get :=
  b.String; get() with String() converting a page-bearing b.Data: the
  checkCall selector branch only covers direct SelectorExpr calls, so a
  captured method value hid the full-page receiver; checkCall now
  resolves the method-value receiver through the flow pass and fails
  closed on a complete page); P162 partial local struct records
  suppressed parameter leaves (o.Other = 1 inside a helper erased the
  caller-supplied o.Data on the way out: fieldTaintsOf returned the
  partial local record for a struct parameter without joining the
  declared leaf sources, the same suppression materializeStructFields
  already avoided; fieldTaintsOf now copies local/package records and
  joins paramFieldFallback for every unrecorded path); P163
  call-produced containers and structs lost element/field provenance in
  argument flow (take(makeList(page)[0]) and take(makeBox(page).B)
  resolved only recorded variable bases: the IndexExpr and StarExpr
  cases now fall back to the callee's callFields, and selector bases
  build the dotted prefix across nested selector chains and rename the
  callee fields onto the selected argument); P164 returned struct
  parameters and container elements lost caller field provenance
  (func id(b box) box { return b } with x := id(box{Data: page}) read
  clean: propagateStructResult only propagated the returned object's
  local/package records, so an identity helper erased the caller's
  field taints; the return case now joins the package struct records,
  the paramFieldFallback leaves of the returned parameter, and the
  IndexExpr-returned local/parameter container element fields); P165
  promoted embedded leaves were absent from the parameter leaf fallback
  (wrap(louter{linner{Data: page}}) read clean because paramLeafPaths
  recorded only linner.Data, while the field read resolves the promoted
  alias o.Data; the anonymous-embedding walk now repeats with the
  (375 -> 379 rejections, 68 benign). Gate tooling 8,805 -> 8,935
  go-gate lines (+552 shell = 9,487 total).
  rejections, 68 benign) + 9 shell environment mutations exit 0,
  production scan clean on every scanned target, all round-16 probe
  files reject on every scanned target together with the round-15
  probe trees (/tmp/probe-luna15, /tmp/probe-luna14) and the previous
  round trees (/tmp/probe-f2only2, /tmp/probe-luna2, /tmp/probe-p16,
  /tmp/probe-luna5) returning the same verdicts as a clean HEAD build,
  go test ./... (both tag sets), -race, vet (go and go-gate), gofmt
  zero diffs, cross-builds for every shell-harness target, the
  pass.
- Round 11 (delta re-review): luna failed the round-10 delta with twelve
  further findings, all probe-verified by the lead (each reproduced as a
  real escape on a fresh HEAD build before any fix): P112 branch-local
  func-literal bindings diverged at joins (f reassigned on one path
  resolved only that branch's body; the join state now marks the binding
  ambiguous and calls through it fail closed); P113 partial struct-local
  stores suppressed caller field taints (writing b.Data clean locally
  erased the untouched b.Other's param-field source on a copy —
  materializeStructFields now fills unrecorded fields from the parameter
  fallback); P114 package-global whole-struct stores did not write
  pkgStructs (g = B{Data: page} in one helper was invisible to field
  reads in another); P115 indexed struct stores lost element field
  taints (xs[0] = B{Data: page} did not mark the container, so xs[0].Data
  read clean); P116 nested and promoted struct fields only resolved
  direct names (o.Inner.Data and embedded promotion now flatten through
  compositeFields and the selector chain); P117-P118 closure parameters
  with struct fields bound only whole-value taint (func-literal calls
  now materialize the closure parameters' fields from the argument flow;
  the refined shape b.Data = page; f(b) is pinned too); P119 opaque
  interface methods returning owned strings over a page-carrying
  receiver escaped (s.Text() with s holding B{Data: page}: the receiver
  of an interface-method miss is now evaluated and its whole-value plus
  struct-field taints are recorded at the transfer point, so the call
  site fails closed as a complete-page transfer); P120 dynamic
  interface-method struct results and type-asserted struct values lost
  field taints (callFields fail closed on byte-bearing fields;
  TypeAssertExpr reads the recorded element fields); P121-P122
  string-param conversion through helper chains lost the caller's
  field-sourced slot (propagateConvertParamSources narrows to plain
  parameter references and carries the converting marker through
  outer -> s chains); P123 runtime map-key taint was not evaluated
  (m[&page] = 1 now joins the container taint); P124 struct-with-file
  into conversion/send/range slots (any(H{F: f}), ch <- H{F: f}, and
  range over file structs now fail closed); P125 runtime map-key file
  393 -> 407 (326 -> 340 rejections; the 67 benign forms are
  unchanged). Gate tooling 7,282 -> 7,704 go-gate lines (+552 shell =
  8,256 total).
  rejections, 67 benign) + 9 shell mutations exit 0, production scan
  plus the 6b/9b shapes) reject on every scanned target, go test
  ./... (both tag sets), -race, vet (go and go-gate), gofmt zero
  diffs, cross-builds for all eleven shell-harness targets, and the
  SOW audit all pass.
- Round 10 (delta re-review): luna failed the round-9 delta with
  fourteen further findings, all probe-verified by the lead (each
  reproduced as a real escape on a fresh HEAD build before any fix):
  P97 a conditional clean store deleted the whole field taint at the
  join (if c { b.Data = []byte{1} }; return b.Data ran clean — forward
  and reverse join passes now delete only unasserted clean markers and
  keep any branch taint); P98-P100 local struct copies (c := b),
  pointer aliases (q := &b), and range over an inline struct literal
  (for _, x := range []box{{Data: p}}) dropped the argument field
  unions (materializeStructFields and the inline-composite range rule);
  P101 method-expression aliases misbound the receiver (get := box.Get;
  get(b) treated b as the receiver and the summary lookup missed — the
  alias now resolves to the bare function and the call binds the
  explicit first argument as slot 0); P102 closure struct results lost
  their fields (f := func() box { return box{Data: page} }; f().Data
  ran clean — func-literal struct results now record their field
  unions); P103 unprovable callees with page-carrying results failed
  open (interface methods, function-typed struct fields, and
  call-produced callees with an unknown body whose result shape can
  hold a full page now fail closed, while struct-wrapped reader results
  stay benign); P104 interface method-expression bindings (var put =
  sink.Put; put(s, page)) bypassed the approved-callee rules; P105
  method receiver string conversions missed the owned-string check
  (the receiver is argument slot 0 for method values and Args[0] for
  method expressions); P106 a user helper spreading a page collection
  into fmt.Sprintf laundered the page (param-sourced fmt spreads are
  now recorded in summaries and flagged at the call site); P107 a
  struct holding an *os.File placed into an interface slot laundered
  the descriptor; P108 file-bearing map keys; P109 append of a file
  into a non-file-bearing []any element slot; P110 range of file
  values into an any (new checkRange); P111 io.ReadFull(bytes.NewReader
  (page), out) slipped through the reader-arg exemption, which now
  applies only to variable-shaped arguments (bytes.NewReader of a
  378 -> 393 (311 -> 326 rejections; the 67 benign forms are
  unchanged). Gate tooling 6,684 -> 7,282 go-gate lines (+552 shell =
  7,834 total).
  rejections, 67 benign) + 9 shell mutations exit 0, production scan
  plus the 2a/2b/2c shapes) reject on every scanned target, go test
  ./... (both tag sets), -race, vet (go and go-gate), gofmt zero
  diffs, cross-builds for all eleven shell-harness targets, and the
  SOW audit all pass.
- Round 9 (delta re-review): luna failed the round-8 delta with thirteen
  further findings, all probe-verified by the lead (each reproduced as a
  real escape on a fresh HEAD build before any fix): P83 method values
  stored in locals (get := r.page; get(1)) lost the method summary and
  the minted page taint; P84 method receivers were absent from summaries
  (box{Data: page}.Get() returned clean, so the receiver is now
  parameter slot 0 of every method summary and method calls bind the
  receiver expression as the first argument); P85 the fmt variadic-spread
  exemption skipped the page check unconditionally (args := []any{page}
  spread into fmt.Sprintf is now flagged unless the spread is clean or
  param-sourced, which keeps the corrupt/headerErr helpers benign); P86
  defined string types (type S string) escaped the owned-string summary
  marker (noteStringConvs unwraps named types to the basic string
  underneath); P87 approved function-variable aliases (var a = f; a(page))
  bypassed the string-parameter call check (callCalleeFuncOrVar follows
  package-level var chains with the same proof rules as approvedFuncVar);
  P88 a clean store to one struct field shadowed the page provenance of
  other fields (sink6 wrote b.Other and returned b.Data; clean field
  stores now keep a marker and field reads fall through to the package
  and parameter sources); P89 a clean indexed store erased the container
  taint (slots[1] = []byte{0} after slots[0] = page; element stores now
  join, never delete); P90 local indexed containers of structs lost
  element field taint (xs := []box{{Data: page}}; xs[0].Data — slice
  literals now record element field unions on the bound container and
  indexed reads and range values consult them); P91 returning the address
  (or dereference) of a local tainted struct lost its fields (return &s
  propagates the recorded struct fields); P92 map composite keys were
  never evaluated (map[*[]byte]int{&page: 1} held the page only through
  its key); P93 file-bearing values hidden in interface-valued collection
  literals ([]any{f}) laundered the descriptor (the composite rule now
  requires the element slot type to bear files); P94 dynamic selector and
  indirect-calld file results missed the fail-closed capability rule
  (struct function fields h.get() and interface methods returning
  *os.File now fail outside the mapping owner); and P95 recursive carrier
  types (type R []R) recursed the type walk into a scanner stack overflow
  (typeCanCarryPage/paramCanCarryPage now carry a seen set; a recursive
  container cycle is the least fixed point, false, and the scan
  benign; the added benign form pins recursive carrier types with no page
  flow). Gate tooling 6,229 -> 6,684 go-gate lines (+552 shell = 7,236
  total).
  rejections, 67 benign) + 9 shell mutations exit 0, production scan
  p5c/p11b) and the recursive-type probe reject or terminate without a
  crash, go test ./... (both tag sets), -race, vet (go and go-gate),
  gofmt, cross-builds for windows/darwin/freebsd/netbsd, and the SOW
  audit all pass.
- Round 8 (delta re-review): luna failed the round-7 delta with seven
  further findings, all probe-verified by the lead: P76 func-literal
  multi-named results with naked returns (scanner panic, index out of
  range in analyzeFuncLitCall), P77 helper parameters converted to owned
  strings inside the callee (string(p) in the summary is caller-bound,
  so the call site fails closed), P78 stale call results across summary
  fixpoints (expression caches were per-package accumulations; every
  fixpoint pass now clears them and a final accumulation sweep replays
  all expressions against the stabilized summaries for the rule pass),
  P79 struct-field flow through dereferenced writes ((*b).Data = p) and
  indexed composite reads ([]B{{Data: p}}[0].Data), P80 package-global
  stores joining bounds instead of last-writer replace (setFull/setBound
  keep the conservative full bound), P81 []any and nested map/chan /
  slice-of-interface parameters as carriers (the fmt-spread exemption
  moved to the call-site rule, not the carrier type), and P82 interface
  method calls (s.Apply(page)) plus call-produced dynamic callees
  (factory()(page)) as unproven indirections. The round also surfaced a
  variantable regression in the lead's own wiring (rules-side lookups
  lost the per-pass values; fixed by the accumulation sweep) and a
  summaryDup gap that would have kept the fixpoint loop running forever;
  298 rejections, 66 benign); gate tooling 5,906 -> 6,229 go-gate lines
  (+552 shell = 6,781 total).
  rejections, 66 benign) + 9 shell mutations exit 0, production scan
  clean on all five targets, go test ./... (both tag sets), -race, vet,
  gofmt zero diffs, cross-compilation, audit - all green.
- Finding 3 (records): this Status is the compact record; the pre-rework
  history is preserved verbatim in ## Status History (appendix).
  string escaping (case 107), and copied shell-only module-graph cases
  (18, 238, 243, 248); benign cases 49/59/63/67/81/83/90 referenced
  undefined types or non-compiling assignments valid only for the old
  syntax-only scanner. All were repaired to compilable equivalents that
  preserve each tested rule (cleanup order of multi-op case files is now
  LIFO so metadata.go double ops restore the original), and the four
  already enforced them per target).
- Validation: go test ./... (both tag sets), -race, checkptr, go vet,
  + 9 shell mutations, exit 0), production scan across all 5 target
  configs, cross-compilation, Rust conformance corpus cross-open, and the
  SOW audit - all green at the closing commit.
- Review process: six-resident swarm (k3, glm, mimo, minimax, qwen,
  luna) then sol per the user's review decision; outcome recorded in the
  Execution Log below.
## Implications And Decisions

1. **Long-term-best: semantic peer, not incremental compatibility.**
   The current Go experiment is not a supported predecessor. Current specs and
   observable Rust semantics win every conflict. Reuse requires proof.
2. **Long-term-best: pure Go.**
   No cgo or Rust runtime dependency. The Rust-provided C ABI remains untouched.
3. **Long-term-best: one physical authority.**
   Internal reader/writer owners implement persistent operations once; public
   advanced and high-level APIs are logical wrappers.
4. **Long-term-best: mmap-only persistent content.**
   Lifecycle and durability syscalls remain necessary, but content-transfer I/O
   and complete out-of-map pages are prohibited.
5. **Long-term-best: semantic conformance.**
   Go and Rust files need not be byte-identical, but every observable value,
   error class, outcome, and cross-process coordination state must agree.
6. **Long-term-best: correctness before optimization, optimality before CI
   standardization.**
   Implement one authoritative path, measure its necessary work, remove waste,
   approve local performance, then add loose CI disaster gates.
7. **Minimal-complete scope.**
   All unsigned Phase-1 Go semantics and their consequences are in scope. C ABI,
   signing, v3, old-v4 compatibility, update-ipsets downloader/parser changes,
   and speculative structures are not.
8. **Controlled handoff.**
   Milestone 0 is an evidence checkpoint; the immutable cross-reader is the first
   code-quality checkpoint. No remote push, native-host access, tracked deletion,
   format change, or new native boundary is authorized merely by this SOW.

## Plan

1. **Milestone 0 - exact gap and replacement map.** Read-only inventory, parity
   matrix, proposed Go API/module graph, current-file classification, deletion
   approval list, risk register, and projected size. No production edit.
2. **Milestone 1 - portable mapped immutable reader.** Exact current codecs,
   mapping/lifetime owner, public immutable reader, all Rust fixture cross-reads,
   malformed bootstrap rejection, zero-allocation lookup/scan evidence, and the
   first platform/worker feasibility report.
3. **Milestone 2 - mapped COW writer and Go producer.** One physical writer,
   current allocation/retirement/commit rules, direct/membership/structured
   construction, public generation of the Go corpus, Rust cross-open.
4. **Milestone 3 - complete logical SDK.** Advanced transactions, typed
   workflows, metadata, queries, joins, algebra, snapshots, reports, cancellation,
   cleanup, and randomized public models over the internal core.
5. **Milestone 4 - live/platform/recovery completion.** Sidecar coordination,
   lifecycle and publication resolvers, supported-platform boundaries, explicit
   validation/recovery, worker, crash/fault/resource proof.
6. **Milestone 5 - cross-language and performance acceptance.** Mixed-process
   matrix, native proof, representative update-ipsets replay, matched Rust/Go
   benchmarks, profiles, necessary-work audit, code-size/duplication audit, docs,
   skill, and repeated clean final audit.

Every milestone report records:

- exact files changed and commits;
- behavior completed and behavior still missing;
- commands and factual results;
- production LOC, largest files/functions, allocations/resources, and benchmark
  deltas relevant to that milestone;
- same-failure and duplicate-authority searches;
- deviations or new decisions requiring the user; and
- whether the next milestone is safe to start.

## Required Final Audit Format

Use these sections in this order:

1. **Executive conclusion** - accepted or not accepted, with no hedging.
2. **Scope and source graph** - every production source classified and compiled.
3. **Single authority and two-level API** - physical owner map and bypass search.
4. **Hot-path necessary work** - counters, profiles, allocations, copies, syscalls,
   checksums, synchronization, and rejected optimizations.
5. **Correctness and durability** - public models, failures, commit/abort/crash,
   cleanup, reclamation, and outcome resolution.
6. **mmap-only and bounded resources** - static/runtime proof, heap/RSS/VM,
   descriptors, scratch, sparse scaling, and no complete page copies.
7. **Cross-language conformance** - both producer sets and both subprocess
   directions actually executed.
8. **Portability** - cross-target compilation versus separately identified native
   runtime evidence and exact unsupported boundaries.
9. **Maintainability** - production LOC, largest files/functions, complexity,
   clone/similarity results, dead code, layering, and justified exceptions.
10. **Findings and iteration record** - every finding, repair, repeat result, and
    remaining issue. Any remaining actionable issue means not accepted.

## Execution Log

### 2026-08-11 - planning and clean handoff

- Committed and pushed ignore rules for local generated build trees, stamp files,
  and Go test executables as commit `900b345`.
- Verified local `master` and `origin/master` at that commit with no visible
  uncommitted or untracked generated state.
- Updated the Rust and conformance README status text to record that the proven
  Rust result now authorizes this pending Go port; no runtime behavior changed.
- Read the complete current architecture/binary specifications, current/pending
  SOWs, Rust project skill, accepted Rust public/benchmark evidence, conformance
  corpus, and current Go production/test tree.
- Ran the current Go test, vet, and formatting baselines successfully.
- Confirmed no Go implementation edit was made. This file defines the future
  port; it does not start it.

### 2026-08-11 - Milestone 0 completed (read-only)

- Files changed: created
  `.agents/sow/milestones/SOW-0025-milestone-0-report.md` (full inventory,
  parity matrix, proposed module/API graph, per-file classification, exact
  deletion set, risk register, size projection); appended this execution-log
  entry. No production or test file in `v4/go/` was touched.
- Commits: `972960a` is the pre-milestone HEAD; the report and this entry are
  committed together as the Milestone 0 closure commit (local `master`, no
  remote push — per the controlled-handoff rule).
- Commands and results: `go test ./...` exit 0, `go vet ./...` exit 0,
  `gofmt -l .` empty, at HEAD `972960add710` with a clean tree. Measured:
  50 production files / 44,088 newline-counted lines, 59 test files / 37,403
  lines, 4 files with content-transfer calls, 24 files with complete page
  arrays, zero conformance references, 64 matched error codes (65-69 missing),
  sidecar stale magics/layouts, Rust production base 82,516 lines (livedb).
- Milestone 0 report outcome:
  - transfer files (6 production + 5 tests): `errors.go`, `key.go`,
    `cardinality.go`, `page.go`, `name_binding.go`, `process_identity.go`
    and their tests — each re-verified against literal vectors in Milestone 1
    before reuse;
  - proposed tracked deletions: 98 files (44 production + 54 test) — exact
    list in the milestone report, awaiting user approval (Decision 1);
  - fault-worker native-boundary policy: pure-Go feasibility proof is a
    Milestone 1 exit criterion; minimal project-owned assembly shim requires
    user decision (Decision 2);
  - projected Go production size: ~32-44k lines (midpoint ~37k), ~40-53% of
    the Rust base;
  - next milestone safe to start once Decisions 1 and 2 are answered.
- Same-failure and authority searches: content-transfer (4 files), complete
  page arrays (24 files), stale constants (retention 2 files, 1 MiB limit 1
  file, v3 zero), structured/threat/conformance references (zero) — each class
  is re-searched as a gate in later milestones; the old tree has one read and
  one write boundary internally, but that authority design is the rejected
  one, so the new tree mechanically enforces owner boundaries with
  set as the Rust `check-source-graph.sh`).
- No deviation from the approved SOW plan. Decisions requested from the user:
  (1) approve the 100-tracked-file + 2-untracked-leftover deletion set;
  (2) worker boundary: reopened by the milestone-1 evidence — SetPanicOnFault
  proves only a panic-based subset, not the spec-exact SA_SIGINFO contract
  (milestone-1 report §11); the user chooses between a minimal project-owned
  assembly sigaction shim and a spec change. Both are recorded in the
  milestone report with evidence and recommendations.

### 2026-08-11 - Milestone 0 corrected after external review (read-only)

- An external review of the milestone report found material errors. Every
  finding was verified against the sources before any change; all verified
  findings were corrected, one claim was not reproducible, and the two
  original recommendations were withdrawn as unsafe:
  - `process_identity.go` moved from transfer to delete: it implements a
    PID/process-start stale-slot recovery model (`process_identity.go:15`,
    `sidecar.go:394-396`) that the current spec explicitly excludes
    (`binary-format-v4.md:2130-2138`). Its test was moved to the deletion set
    too. Deletion set is now 100 tracked files (45 production + 55 test).
  - Error-code mapping corrected: names match Rust 1-64 in every position
    except code 46 (Go `ErrorLiveCoordinationDomainMismatchRequiresReset` vs
    Rust `LiveCoordinationMalformedRequiresReset`, `sdk_error.rs:58`); the
    report no longer claims an exact match and transfer requires resolving
    code 46 plus adding 65-69.
  - Prohibited-I/O classification corrected: content-transfer calls are
    `ReadAt`/`WriteAt`/`unix.Pread` in 3 files (`os_linux.go:503,565`,
    `page_source_linux.go:27`, `page_source_other.go:26`); `Truncate`/`Sync`
    are required lifecycle/durability syscalls and are not prohibited.
  - Decision recommendations changed to evidence-first for both decisions
    (deletion executes atomically with the compiling, tested Milestone 1
    replacement; fault-worker boundary is decided after the Milestone 1
    feasibility evidence, per the SOW's own stop-for-decision wording).
  - Design-spec line count corrected (519, not 530); exported-symbol report
    corrected to measured values (199 top-level exported declarations; 43 of
    50 files export nothing) and no longer labeled a "public SDK surface";
    size projection explicitly labeled estimate-only, not a target.
  - The report moved from an undocumented `.agents/sow/milestones/` directory
    to `.agents/sow/pending/pure-go-v4-port-milestone-0-report.md`, the best
    interpretation of the review's "wrong SOW path" finding. The reviewer
    later retracted that finding ("the original path was correct; moving the
    report was unnecessary, although harmless"). The report stays at the new
    location; both the SOW and this log reference the new path.
  - `.reasonix/` is harness session state, untracked by design; it is not repo
    content and was not committed.
- Files changed: `.agents/sow/pending/pure-go-v4-port-milestone-0-report.md`
  (moved and corrected, 496 lines), this execution-log entry. Still zero
  production/test edits in `v4/go/`.
- Commits: `579c1a3` (initial Milestone 0 report + log), `04032d6` (verified
  corrections, report moved to `pending/`, SOW correction log entry),
  `b91301d` (removal of the superseded old report path). All local, no remote
  push.
- Milestone 1 begins with moving SOW-0025 to `.agents/sow/current/` with
  `Status: in-progress`; it remains `open` in `pending/` while implementation
  has not started.

### 2026-08-11 - Milestone 1 completed (implementation checkpoint)

- Moved SOW-0025 to `.agents/sow/current/` with `Status: in-progress`
  (commit `0de8793`) and implemented the milestone:
  - New production packages: `internal/format` (wire codecs, meta identity
    and kind invariants, page/slotted, range, catalog, membership,
    structured, blob, metadata chunks, 129-bit cardinality, single 1-69
    error-code table with code 46 per the current Rust contract), `internal/
    mapping` (the only mapping owner; POSIX + honest Windows stub),
    `internal/reader` (the only healthy-generation reader core), and the
    public facade `reader_public.go` at the module root. ~3,720 new
    production lines, ~1,700 test lines.
  - Conformance: all six committed Rust fixtures open and verify with exact
    `cases.json` semantics (metadata states, full-IPv6 cardinality strings,
    boundary probes, 70 feed names, word-level bitmaps incl. blob-backed and
    1 MiB metadata, structured values + threat memberships); all three
    invalid mutations rejected with code 32.
  - Zero-allocation evidence: direct v4/v6, membership (inline + blob),
    structured, feed (internal), scans, cardinality = 0 allocs; public feed
    lookup = exactly the returned string copy. Recorded in both root and
    reader zero-allocation tests.
  - Portability: cross-compiles for darwin/freebsd/windows/linux-arm; Windows
    open refuses with os-unsupported stub; runtime proof on Linux amd64.
  - Worker feasibility: pure Go disproven for POSIX with an empirical probe
    (runtime-fatal `unexpected fault address`, os/signal never delivers
    mapping SIGBUS, no si_addr/sigaction surface in x/sys); Windows vectored
    exception path is pure-Go feasible. Fallback = minimal project-owned
    assembly sigaction shim; per recorded Decision 2 the user decides with
    this evidence.
  - Commits: `0de8793` (SOW move), `913f4e6` (reader + tests),
    `9441f85` (independent-review repairs: blob-branch txn threading,
    slotted-page bounds, metadata allocation bound, catalog reserved bytes,
    synthetic two-leaf blob regression database), `1df90fa` (milestone
    report), `4eec44e` (third-pass repairs), `03a910f` (fourth-pass
  repairs: borrow-count lifetime, view API, absence-vs-corruption,
  concurrency race tests, worker and report fact corrections,
  passthrough, conformance enumeration, multi-level range-tree test,
  corrected worker conclusion), `1e1ac4b` (fifth-pass repairs: meta tail
  zero invariant, mandatory aux checks, exact zlib stream verification,
  namespace safety under the lifetime lock, structure payload decode at
  lookup, public error typing, adversarial regression suite). No tracked
  file deleted (Decision 1 = C).
  - Full report: `.agents/sow/pending/pure-go-v4-port-milestone-1-report.md`.
    Baseline gates re-run: `go test ./...` (incl. race), `go vet`, `gofmt`,
    cross-compilation matrix — all green; SOW audit clean.
- At this historical checkpoint, both then-pending decisions had their evidence:
  the deletion set (then counted as 100 tracked files + 2 untracked leftovers;
  later corrected to 105 tracked files) and the worker
  boundary — the third-pass evidence demonstrates only a panic-based fault
  subset via `runtime/debug.SetPanicOnFault` (empirical recover with exact
  fault address); the spec-exact worker contract needs either a minimal
  project-owned assembly sigaction shim (spec-exact) or an explicit
  spec change. Both decisions were subsequently resolved as 1A and 2A. The
  contemporaneous statement that Milestone 2 would be safe after those answers
  is superseded by the later Milestone 1 reopenings.
- Commands and results: `wc -l` design spec 519; `grep` evidence for error
  code 46 in `errors.go:54` and `sdk_error.rs:58`; sidecar/spec magic and slot
  layout at `sidecar.go:11,15,394-396` vs `binary-format-v4.md:2104,2130-2138`;
  I/O call sites listed above; baseline gates unchanged (already green).

### 2026-08-11 - gap analysis and repair pass (six-agent)

- `94723aa` records the fifth-pass repairs in this log and the M1 report.
- `9a835e4` adds `.agents/sow/pending/pure-go-m1-gap-analysis.md`: six
  concurrent read-only subagents, each with a disjoint brief (codecs, reader
  semantics, architecture, public API/errors, test evidence,
  worker/mapping/report facts) at HEAD `94723aa`. Result: one BLOCKER (B1,
  structure radix divisor one level too deep) and ten MAJOR findings,
  plus three MINOR and one refuted claim.
- `58c4d8f` repairs every verified finding with a regression test: B1 span
  fix, blob-walk record/geometry validation, slotted exactness, OFD lifetime
  lock, API binding guards, absence/word-exact conformance evidence,
  honest LOC, import-graph gate strengthening (every import checked +
  sync/sync-atomic/unsafe ban), and bootstrap minima. No tracked file
  deleted.
- `bb7f485` corrects the SOW worker-feasibility sentences and the report's
  stale claims to match the corrected section 11.
- Gates re-run green at `bb7f485`: `go test ./...` (5 packages, incl.
  cross-compile matrix.

### 2026-08-12 - mandatory six-agent review round 2 and fix pass

- Per the mandatory iterative review gate (one review round per milestone
  before it can close), six new concurrent reviewers with disjoint briefs
  (codecs / bootstrap+ranges / membership+structured+metadata / mapping+
  lifetimes+platform / public API+errors+zero-alloc / conformance+reports)
  reviewed HEAD `bb7f485`. Verdicts: 4 FAIL (codecs 1 P1 + 4 P2; bootstrap
  1 P1 + 1 P2; membership 1 P1 + 3 P2; mapping 1 P1 + 3 P2), 1 FAIL on
  paperwork (reports 5 P1 + 5 P2), 1 PASS-with-2-P1 that are the already
  recorded closed-state error-class user decision. No P0.
- Fixed in this pass (all regression-pinned):
  - catalog `feed_index >= feed_index_limit` now corruption
    (`internal/reader/catalog.go`, `guard_regression_test.go`).
  - kind-classification: registered-but-invalid combinations (structured
    kind 0, direct/membership kind 1) report FormatInvalid (32); only
    unknown kinds report UnsupportedStructure (67)
    (`internal/format/meta.go`).
  - dangling structure reference now the typed corrupt error (structure
    range names an absent structure ID), matching the membership twin
    (`internal/reader/structure.go`).
  - FreeBSD immutable opens now use the canonical whole-file shared flock
    lifetime lock instead of refusing every open (`mapping_lifetime_freebsd.go`).
  - `Mapping.View`/`Page` after Close report the typed wrong-state error;
    `Mapping.Close` is idempotent (`internal/mapping/mapping.go`).
  - metadata zlib FCHECK validated; `deflateStreamLen` probes bounded at
    the declared length (CPU-amplification fix) (`internal/reader/metadata.go`).
  - blob leaf `%8` alignment explicit; checked blob extent arithmetic;
    blob branch validates every probed entry's child page
    (`internal/reader/membership.go`).
  - `publicError` preserves the typed code through wraps; error-code names
    59/62/69 aligned to the Rust `Id` spelling.
    sources and the stdlib `syscall` package.
  - test hygiene: public zero-alloc suite releases its v6 view; structured
    conformance absence probes added; Info()-after-close pinned; literal
    byte vectors added for the v6 range, membership leaf/branch, structure
    record, enrichment payload, and blob-branch codecs.
  - report facts corrected (honest production LOC 4,492 raw / tests 3,794
    raw, zero-alloc table labels, OFD lock wording, metadata allocation
    overhead 0-88 bytes) and this SOW log repaired.
- Commits: see the round-2 fix commit. Gates green after the pass; the six
  reviewers re-review the repaired tree before Milestone 1 closes.

### 2026-08-12 - six-agent review round 3 (final verification)

- All six reviewers re-reviewed the round-2 tree at HEAD with their same
  disjoint briefs. Verdicts: codecs PASS, bootstrap/ranges PASS,
  membership/structured/metadata PASS, public API/errors/zero-alloc PASS
  (fixable issues outside the recorded closed-state decision: none),
  mapping/lifetimes PASS after the gate fix, conformance/reports PASS after
  this record.
- Commits and fixes between the round-2 record and HEAD `3b4f3d5`:
  - `a5d7cf8` + `78cebc4` — import-graph gate comment stripping (line and
    stateful multi-line block), verified both directions.
  - `9203c28` — regression pins: `TestSoleMetaGeometry`,
    membership-kind1/kind2 subtests (both fail on the pre-fix code).
  - `e0a1687` — blob path zero-allocation: value-pair expected-level state;
    internal zero-alloc suite now measures blob word/scan at 0.
  - `a348c42` — real family-min and from-1 conformance absence probes;
    explicit `94723aa` log line.
  - `3b4f3d5` — P0 fix: blob coverage check underflowed when the request
    fell past the selected leaf's end (`end-off` wrapped), allowing a
    silent out-of-leaf word read or a slice panic on crafted files; the
    explicit `off > end` guard restores corruption semantics and
    `TestBlobGapRejectedCorruption` pins it (fails pre-fix in both modes).
- Report counts corrected at HEAD: production 4,500 raw lines (tests
  excluded), tests 3,922 raw, zero-allocation checks 18 (8 internal + 10
  public). (Superseded: the round-4 follow-up entries below carry the
  verified counts at each later HEAD.) Milestone report sections 11c-11d record both passes; the
  reviewers' final verdicts are all PASS with no P0-P2 remaining.
- Gates at HEAD: `go test ./...` (5 packages, incl. -race),
  matrix — all green; SOW audit clean.
- Pending user decisions unchanged: (1) deletion set — 100 tracked files +
  2 untracked leftovers, atomic with the Milestone 2 writer commit;
  (2) worker boundary — assembly sigaction shim vs spec change vs drop;
  (3) closed-state error class — HandleClosed (9) vs WrongState (11),
  Release idempotency, WordCount released-view silent-0; plus the recorded
  authority conflict (unknown nonzero structure_kind on direct/membership:
  spec text 67 vs Rust 32).

### 2026-08-12 - external audit pass (verified, all six findings real)

- An externally run audit of the round-3 tree reported six correctness and
  coordination failures plus performance waste. Every claim was verified
  against code, the spec, and the Rust reference before any change; all six
  correctness findings reproduced and were fixed with regression tests:
  1. View copies could double-release one borrow: two copies of one public
     view each decremented the reader's child count, so a later Close could
     succeed while a live child existed (use of the closed mapping). The
     released flag moved from the view value into a shared viewGuard with an
     atomic CAS; copies of a view now share one borrow, second Release is a
     no-op, and every copy reports HandleClosed after release. Public
     lookups that return a view now pin exactly one small guard allocation
     (the copy-safety cost; mapped traversal stays zero-alloc).
  2. Immutable sidecar handling: a present canonical `.readers` sidecar
     returned LiveCoordinationUnsupported (44) instead of the Rust WrongMode
     class (11), and a dangling `.readers` symlink was accepted as absent.
     Uses os.Lstat (symlink-aware, mirroring fs::symlink_metadata) and code
     11; pinned by TestSidecarPresence.
  3. Immutable lifetime lock was non-blocking F_OFD_SETLK; Rust blocks
     (F_OFD_SETLKW) while a writer holds the exclusive lock. Both Linux and
     macOS now use the blocking form (darwin F_OFD_SETLKW = 91).
  4. Path identity was verified only before the lock; Rust rechecks after
     locking and after mapping. mapping.OpenImmutable now re-verifies
     identity and re-runs the namespace check at all three points.
  5. validateStructuredMeta omitted structure_entry_count <
     structure_id_limit (Rust CountInvariant); both metas with
     entry_count == id_limit opened. Added the check; pinned by
     TestStructureEntryCountBound.
  6. Catalog name-branch keys were not grammar-validated (Rust decode_entry
     validates leaf and branch names through one decoder). DecodeCatalogName
     Branch now rejects invalid names; pinned by TestCatalogBranchNameGrammar.
- Performance waste, all fixed:
  - metadata stream validation re-inflated the payload O(log n) times (the
    1 MiB fixture twelve times); replaced with one single-pass inflation
    whose consumed-byte position (flate reads byte-at-a-time from a
    bytes.Reader) proves the exact stream end. ~1.4x measured on the
    micro-benchmark and far fewer allocations; trailing-byte, truncation,
    and Adler checks preserved (TestMetadataTrailingBytesRejected passes).
  - ReadWords decoded the whole record or walked the blob tree once per
    word; now one record decode (inline) or one blob walk (blob) per batch.
  - range/catalog/membership/structured descents pre-read the root page and
    re-decoded every page header inside OpenSlotted; the root pre-read is
    gone (first iteration captures the level) and OpenSlottedHeader reuses
    the already-decoded header (page.go).
    literals as a comment (a real call after a string containing `//` could
    bypass the gate); the stripper is now a quote-aware awk state machine,
    and in-memory decompression reads (consumedReader) are the documented
    exemption.
- CORRECTED non-finding (2026-08-12 re-review): the earlier claim that the
  per-call atomic in the public facade is parity with the frozen C ABI
  (handle.rs Gate::enter gates every C call) was wrong in mechanics. Rust
  reader lookups are a plain Option check (ReaderHandle::get, no gate, no
  atomic); the C facade's real costs are one Arc::clone (atomic refcount)
  and one Box per view-handle lookup, and view ops carry the caller-
  serialized AtomicBool fail-fast gate. The binding Go criteria are
  SOW-0025:175 (zero Go heap bytes for warm point lookups/cursor steps) and
  design-iprange-engine.md:373/:404; the round-4 facade (1 guard alloc per
  view lookup, 1 string alloc per feed lookup, per-call atomic load) does
  not meet them. Open as the hot-path API decision, not a resolved
  interpretation.
- Gates at HEAD: go test ./... (5 packages, incl. -race), go vet, gofmt,
  import-graph (with quote-aware stripper), 9-target cross-compile matrix —
  all green; zero-alloc suite updated for the one-guard-per-view contract;
  report counts and this record updated in the same commit.

### 2026-08-12 - hot-path contract re-review (milestone 1 reopened)

- An external re-review after the round-4 closure re-examined the public
  facade against the frozen performance contract. Verified (measurements
  reproduced at HEAD): membership lookup+word+release = 1 allocation (16B
  guard) and 2 atomic ops; feed lookup = 1 allocation (string); direct
  lookup/scan = 0 allocations but 1 atomic load each; every view op adds 1
  atomic load. SOW-0025:175, design-iprange-engine.md:373/:404 and the
  binary-format-v4.md:2537 checked-word_count requirement are not met by
  the round-4 facade. The earlier "Gate::enter parity" justification was
  wrong in mechanics (Rust reader lookups are a plain Option check; SDK
  core is atomic- and allocation-free; the C facade pays one Arc clone +
  one Box per view-handle lookup and gates view ops).
- Fixed now (no API change): metadata staging at internal/reader/metadata.go
  allocated the worst-case bound and then grew an io.ReadAll output (~2.3
  MiB / dozens of allocations for 1 MiB although exact lengths are declared
  in the selected meta). The chain now allocates exactly its declared
  compressed length (bootstrap bounds make it a safe capacity) and
  decompression reads into one exact output allocation with a one-byte
  overflow probe; truncation/trailing/Adler checks unchanged, pinned by the
  existing tests.
- HISTORICAL OPEN ITEM (subsequently resolved as decision 4A): the public facade
  API shape that closes the
  gap — caller-owned pinned lookup handles with token-style views and zero
  allocations/atomics inside the hot loop (long-term-best, recommended) vs
  keeping the guard facade with a SOW/spec amendment. Also unresolved at that
  checkpoint: the
  facade moves to the WrongState closed class, WordCount becomes
  error-capable, and Release/second-close semantics are decided explicitly
  (decision 3 corrections from the re-review). Reopened milestone 1 does
  not close before the hot-path contract is met.

### 2026-08-12 - external audit round-4 reviewer follow-up (mapping)

- The bootstrap/mapping reviewer's round-4 P2 was that the mapping owner
  still lacked regression tests for (a) the blocking F_OFD_SETLKW wait
  semantics and (b) the post-lock/post-mmap path identity recheck.
- Writing test (b) exposed that the shipped three-point recheck compared
  the opened fd against the initial path stat — a comparison that can never
  fail once the fd is open — so a replacement after open could still publish
  a mapping of the old unlinked inode while the path named a new database.
  Corrected at v4/go/internal/mapping/mapping.go: every recheck now
  re-stats the path itself with os.Lstat (symlink-aware, like Rust
  fs::symlink_metadata) and requires it to still name the opened inode;
  a mismatch or non-regular path entry under the lock is the WrongState
  class (code 11), matching Rust WrongMode ("live path identity changed").
  The initial pre-open stat remains only as the early non-regular-file gate
  and no longer vetoes the opened file, matching Rust's
  open-what-the-path-names semantics.
- TestOpenImmutableRefusesPathReplacedDuringOpen fails on the pre-fix tree
  and passes after the correction; TestOpenImmutableWaitsForExclusive-
  LifetimeLock pins the blocking wait (fails on a non-blocking lock).
- The darwin lifetime lock now retries EINTR in the wait loop, matching the
  linux peer and the Rust live_lock platform module (one loop for
  linux+apple), at v4/go/internal/mapping/mapping_lifetime_darwin.go.
- Counts at HEAD refreshed (4950366): production 4,592 raw lines / tests
  4,196 raw lines (report sections 2 and 11f). Gates at HEAD: go test ./... (5
  packages, -race, mapping -count=3), go vet, gofmt, import-graph, five
  cross-compiles (darwin/amd64, darwin/arm64, freebsd/amd64, windows/amd64,
  linux/386) — all green.

### 2026-08-12 - external audit round-4 follow-up 2 (membership P0)

- The membership/structured/metadata reviewer's round-4 verdict found one
  remaining P0 in batched blob-membership reads: the earlier
  single-descent blob case still issued one span request, so ReadWords
  crossing a blob-leaf boundary failed as corruption on a conforming file
  (reproduced on the committed synthetic two-leaf blob database with
  `ReadWords(505, 4)`: "blob leaf does not cover the requested bytes"),
  while per-word Word() succeeded and the Rust reference loops per leaf
  (blob_tree.rs read_words_from).
- Fixed in v4/go/internal/reader/membership.go: the traversal split into
  blobLeaf (one descent to the covering leaf, returning its mapped bytes
  and logical start) plus the single-span blobRead wrapper; the batched
  path now loops per covering leaf, copies min(available, remaining)
  words, advances, and keeps the no-advance guard and the trailing-zero
  word canonical check.
- TestBlobReadWordsAcrossLeafBoundary (blob_test.go) fails on the pre-fix
  tree with the exact reported error and passes at HEAD; blob per-word
  reads and all 8 internal zero-alloc subtests remain green.
- Counts at HEAD (ac6bef1): production 4,634 raw lines / tests 4,252 raw
  lines (report sections 2, 11f, 11g).

### 2026-08-12 - external audit round-4 follow-up 3 (mapping P2 + records)

- The mapping/lifetime reviewer's re-verification found one remaining P2:
  an unlinked (not replaced) path mid-open mapped the Lstat failure to
  CodeIO (31), while Rust verify_path_inner refuses with NameNotFound (18).
  Fixed at v4/go/internal/mapping/mapping.go (os.IsNotExist ->
  CodeNameNotFound before the IO fallback); pinned by
  TestOpenImmutableRefusesPathUnlinkedDuringOpen.
- The conformance/reports reviewer's P2 was record lag only: the round-3
  "counts corrected at HEAD" sentence in the external-audit entry is now
  explicitly annotated as superseded by the round-4 follow-up entries;
  report section 13 now lists section 11e; every historical "Counts at
  HEAD" line carries its commit.
- Counts at HEAD (this commit): production 4,639 raw lines / tests 4,308
  raw lines. Gates: go test ./... incl -race (mapping -count=3), go vet,
  gofmt, import graph — all green. Gates at HEAD: go test ./... incl -race,
  go vet, gofmt, import graph — all green.

### 2026-08-13 - round-45 final review mmap-gate denylist gaps closed (HEAD 14c0698)

- The round-44 re-verification completed with all six narrow reviewers at
  PASS (HEAD e5fea20). The round-45 full-scope final review then failed
  with three P2 findings, all in the mmap source gate: os.CopyFS was
  absent from the selector ban (a directory copy streams artifact bytes
  with no banned selector; the live reproducer exited 0);
  os.OpenInRoot/os.OpenRoot were absent from the file-producer table (a
  Go 1.26 OpenInRoot *os.File, or an older-toolchain *os.Root handle,
  reached flate.NewReader untainted and streamed file bytes, and
  Root.Open/Create/OpenFile also produce files); and the blanket-approved
  x/sys surface still carried descriptor-transfer primitives
  (unix.Tee, unix.Vmsplice, unix.IoctlFileClone/CloneRange/DedupeRange,
  darwin unix.Clonefile/Clonefileat).
- Fixed at HEAD 14c0698: CopyFS, Tee, Vmsplice, IoctlFileClone* and
  Clonefile* join the banned selector set (CopyFileRange/Sendfile/Splice
  were already banned); os.OpenInRoot and os.OpenRoot join the file
  producer table as position-0 file taints, so every Root method outside
  the approved lifecycle surface fails closed; all three live reproducers
  plus the OpenRoot/ReadAll and darwin Clonefile variants are rejected.
  then proved a P0 in the same class: a *os.Root stored in a struct
  field (h := gateRootField{r: root}; h.r.Open(name)) dropped the
  file taint, so the returned *os.File reached flate.NewReader
  untainted and the stream was consumed through the exact inflater
  exemption shape (gate exit 0, /tmp reproducer); the type model now
  resolves *os.Root as a file-bearing type everywhere *os.File does
  (struct fields, parameters, helper returns, type assertions,
- The P0 closure landed at HEAD 262756c on top of the round-45 gate fix
  (14c0698, records 70dcc42); the record trail and counts above reflect
  the full chain 14c0698 -> 70dcc42 -> 262756c -> e1410eb.
- The following adversarial re-review then found three producer-value
  P0 escapes, all proven live with the metadata-exemption consumption
  chain at gate exit 0: file method values (open := root.Open;
  open(name)), initialized func-typed variables with file-bearing
  declared results (var newRoot func(string) (*os.Root, error) =
  os.OpenRoot and the pre-existing *os.File form var openPath
  func(string) (*os.File, error) = os.Open), and stdlib producer
  values bound without a declared type (openPath := os.Open). Fixed
  in v4/go-gate/main.go: the file method in value position is checked
  against the approved capability surface, the declared result type of
  an initialized func-typed variable registers as a func-file, and
  stdlib producer values register as func-files wherever bound;
- The producer-value closure landed at HEAD 5ff9116 on top of the
  Root-taint fix (262756c); the exact round-45/46/47 chain is 14c0698
  (gate gaps), 70dcc42 (its records), 262756c (Root laundering), e1410eb
  (its records), 5ff9116 (producer values), 8c6cc44 (its records). Gates:
  go test ./... incl -race, go vet,
  CGO_ENABLED=0
  build and test, four cross-compiles, SOW audit — all green.
- Counts unchanged at this commit: production 4,792 raw lines / tests
  4,877 raw lines (the gate scanner lives outside the module). Decision
  5A remains open for user ratification; Milestone 2 remains blocked
  until the re-review passes.

### 2026-08-13 - round-48 gate re-review bound method expressions and cross-package producer vars closed (HEAD aec609c)

- The round-48 adversarial re-review (six narrow reviewers: codecs,
  membership/zero-alloc, mapping/pin/gate, metadata/bootstrap, records,
  gate hunting) passed codecs, membership/zero-alloc, mapping/pin/gate,
  and metadata, and failed with two gate findings plus one records
  finding.
- P0 - bound method expressions: `open := (*os.Root).Open` followed by
  `open(root, name)` binds the Open method with the receiver as an
  explicit first argument. The receiver node is a type expression and
  never carries value taint, so the value-position selector check could
  not see it; the same held for the package-level initializer
  `var openRootPkg = (*os.Root).Open`. Both reproduced live at gate
  exit 0 with the exempted inflater chain consuming the file.
- P2 - same-module cross-package producer vars: `internal/format`
  declares `var OpenRoot = os.OpenRoot` (and the *os.File sibling
  `var Open = os.Open`); a caller in `internal/mapping` invoking
  `format.OpenRoot(dir)` cannot see the declaring directory's taint
  registry, so the returned *os.Root/*os.File reached a flate reader
  untainted, reproduced live at gate exit 0.
- P2 (records) - the round-47 exec-log bullet never cited the terminal
  records HEAD by hash; the corrected chain 14c0698 -> 70dcc42 ->
  262756c -> e1410eb -> 5ff9116 -> 8c6cc44 is now named in the
  round-45/46/47 bullets above and in this entry.
- Fixed at HEAD aec609c: the selector rules now recognize method
  expressions whose receiver type resolves to a file-bearing handle
  (methodExprFileType, rejecting every method outside the approved
  lifecycle surface in both function and package-level scans), and a
  process-wide package-level producer-var registry
  (qualifiedProducerVars plus the per-directory pkgProducerVarsByDir)
  resolves same-module producer vars through the call-site import path,
  with the clause-name fallback for plain (non-renamed) imports;
- Replayed at the new gate: all round-47 replays (R4-R14) and the new
  probes (P10 method expression, P12 exempted-inflater chain, P14
  package-level method expression, P15 cross-package producer var)
  are rejected; the benign close-value/Fd-value/Chdir controls still
  pass. Gates: go test ./... incl -race, go vet, gofmt, import graph
  four cross-compiles, SOW audit — all green. Counts unchanged at
  this commit. Decision 5A remains open for user ratification;
  Milestone 2 remains blocked until the final review pass.

### 2026-08-13 - round-49 gate re-review closed method-expression spelling variants, value-bound cross-package producers, and interface-conversion laundering (HEAD 041cf6098ee6)

- The round-49 adversarial re-review (six narrow reviewers: codecs,
  membership/zero-alloc, mapping/pin/gate, metadata/bootstrap, records,
  gate hunting) passed membership/zero-alloc and metadata, and failed
  codecs (P1 value-bound cross-package producer vars), mapping/pin/gate
  (P1 renamed-import and alias-over-renamed method expressions, P2
  wrapper-promoted methods), records (P1 stale count prose), and gate
  hunting (P0 nested-parenthesized method expressions, P0
  interface-conversion laundering).
- P0 - nested-parenthesized method expressions: `open := ((*os.Root)).Open`
  (and deeper paren nesting) wrapped the receiver so the single paren
  strip left a hidden type spelling while Go still compiled and invoked
  the bound open; the same gap applied to package-level initializers.
- P1 - renamed-import and alias spellings: `import o "os"; (*o.Root).Open`
  escaped because only the canonical os.Root spelling resolved, and the
  alias hop `type RR = o.Root` over a renamed import compounded it. A
  wrapper struct `type WE struct{ *os.Root }` promoted Open/OpenFile/
  Create into the method set, so `(*WE).Open` escaped with no Root
  spelling in the receiver at all. All four were reproduced live at gate
  exit 0.
- P1 - value-bound cross-package producer vars: form 260 pinned the
  direct call `format.Open(name)`; binding the producer into a local
  (`open := format.Open`), a package-level var (`var bound = fm.Open`),
  or a renamed qualifier (`fm.Open`) dropped the func-file taint and the
  invoked binding returned an untainted *os.File (live, gate exit 0).
- P0 - interface-conversion laundering: `zr := gateZR(f)` converts a
  live *os.File into a fresh named interface; the conversion resolved as
  a function call, zr carried no taint, and the exact metadata inflater
  exemption shape consumed file bytes (live, gate exit 0).
- P1 (records) - the SOW `## Status` close-out and the reviewer-findings
  close-out still reported the round-47 count (two hundred six forms)
  instead of the round-48 count and closure; corrected here.
- Fixed at HEAD 041cf6098ee6: method-expression receivers now strip parens
  to a fixpoint, translate renamed stdlib imports, chase alias chains,
  and walk defined-struct embedding chains for promoted file-handle
  methods (fileCapableReceiverType); same-module package-level producer
  vars resolve in value position through the process-wide registry
  (producerVarSelector in classify, function and package-level
  bindings); type conversions of file-tainted values keep the file
  taint in every binding and argument context, so the inflater
  exemption no longer shields a laundered reader
- Replayed at the new gate: all round-47 replays (R1-R14), round-48
  probes (P1-P15), the new nested-paren/renamed/alias/wrapper
  method-expression probes, the cross-package value-bound producer
  probes (function-level, package-level, renamed-import), and the
  interface-conversion launder probe are rejected; the benign controls
  still pass. Gates: go test ./... incl -race, go vet, gofmt, import
  test, four cross-compiles, SOW audit — all green. Counts unchanged
  at this commit: production 4,792 raw lines / tests 4,877 raw lines.
  Decision 5A remains open for user ratification; Milestone 2 remains
  blocked until the final review passes.

### 2026-08-13 - round-50 gate re-review closed generic interface erasure, composite-literal and generic-wrapper launders, and deep embedding chains (HEAD 04f4271e3c5f)

- The round-50 adversarial re-review (six narrow reviewers: codecs,
  membership/zero-alloc, mapping/pin/gate, metadata/bootstrap, records,
  gate hunting) passed membership/zero-alloc and metadata, and failed
  with four live gate escapes: codecs (P0 generic identity erasing
  file taint into an interface result), mapping/pin/gate (P1
  composite-literal field launder, P1 method expression on an
  instantiated generic wrapper, P2 embedding chain deeper than the
  bounded walk), and records (stale round-48 count prose in the
  Status opening summary, fixed in this pass).
- P2 (records) - the Status opening summary still carried pre-round-48
  count prose (two hundred six forms) and the exec-log label for it
  misattributed the staleness to round-48; the current-state prose is
  unified on the round-50 count and this entry drops the stale label.
- P0 - generic interface erasure: `func probeWrapR[T io.Reader](v T)
  io.Reader { return v }` binds a file argument to a type parameter
  and returns an interface-typed result; the generic result
  propagation only tracked exact type-parameter results, so
  `zr := probeWrapR(f)` erased the taint and the exempted inflater
  shape consumed file bytes (live, gate exit 0).
- P1 - composite-literal field launder: `s := gateLaunderS{r: f}`
  followed by `zr := s.r` dropped the taint because field taint was
  registered only for selector writes (live, gate exit 0).
- P1 - instantiated generic wrapper: `type gW[T any] struct{ *os.Root }`
  with `open := (*gW[byte]).Open` hid the promoted method behind the
  generic instantiation spelling (live, gate exit 0).
- P2 - deep embedding chain: a five-level wrapper chain
  (gDE5 -> ... -> *os.Root) exceeded the four-hop embedding walk
  budget; `(*gDE5).Open` bound the method untainted (live, gate exit 0).
- Fixed at HEAD 04f4271e3c5f: generic results that can only compile as
  interface-erased carriers (any, error, anonymous interface, io.*
  interfaces, bare same-package declared results) keep the file taint
  when a type-parameter-bound argument is file-tainted
  (interfaceErasedResult); composite-literal bindings register
  named-element field taint, including nested literals and selector
  field assignments (registerCompositeFieldTaints); method-expression
  receivers strip generic instantiation suffixes before the struct and
  embedding registry lookups (genericBase); the embedding walk tracks
  visited types and runs to a fixpoint instead of a fixed four-hop
- Replayed at the new gate: all round-47 (R1-R14) and round-48 (P1-P15)
  probes, the round-49 probe families (nested-paren, renamed-import,
  alias-over-renamed, wrapper, value-bound producer, interface
  conversion), and the four new probes are rejected; the benign
  controls still pass. Gates: go test ./... incl -race, go vet,
  CGO_ENABLED=0 build and test, ten cross-compiles across the
  per-target listing matrix, SOW audit — all green. Counts unchanged
  at this commit: production 4,792 raw lines / tests 4,877 raw lines.
  Decision 5A remains open for user ratification; Milestone 2 remains
  blocked until the final review passes.

### 2026-08-13 - round-51 gate re-review closed file-bound generic result erasure and positional composite-literal field launders (HEAD 875b19205fb3)

- The round-51 adversarial gate hunt (six narrow reviewers:
  codecs, membership/zero-alloc, mapping/pin/gate, metadata/bootstrap,
  records, gate hunting) failed with seven live escapes across three
  root causes in the content-transfer scanner:
- P0 - spelling-based interface-erased results: generic interface
  results were recognized only under the literal `io` qualifier, so a
  renamed import (`import r "io"`; `func gateHuntWrapA[T r.Reader](v T)
  r.Reader { return v }`), another stdlib interface (`fs.File`), and a
  chan send of the erased call value all flowed a file into the
  exempted inflater shape (live, gate exit 0; probes A/B/F).
- P0 - container-of-type-parameter results: `[]T` and `[2]T` results
  never resolved to file taint, so `files[0]`/`arr[0]` read a clean
  value (live, gate exit 0; probes C/E). Maps and func wrappers share
  the miss.
- P0 - positional (unkeyed) composite-literal elements: the registry
  skipped non-keyed elements, hiding a file field (`s := gateHuntS{f}`;
  `zr := s.r`) and an os.Root opener
  (`gateHuntRootIface{root}`; `zr0.Open(name)`) (live, gate exit 0;
  probes D/G). Keyed twins were already pinned.
- Vacuous-form discovery: forms 267-268 as separate files were
  rejected by the unconditional `.ReadFull` selector ban regardless of
  taint, so they never exercised the exemption-shape escape; the
  zero MISSes. Forms 269-270 (method-expression classes) were genuine.
- Fixed at HEAD 875b19205fb3:
  `genericParamFilePositions` now taints every declared result
  position once a file-typed argument binds a type parameter (the
  generic body is opaque; exact parameter, interface spellings of any
  qualifier, containers, channels, and func wrappers all keep the
  taint); `registerCompositeFieldTaints` resolves unkeyed elements
  through the struct's declared field order (embedded fields by type
  text), mirrors the order cross-package like the struct registry, and
  metadata.go save/restore appends (`append_mut`) and import-block
  injection (`inject_import`).
- Pinned as durable exemption-shape appends: forms 267-268 converted
  and 271-277 added (renamed-io interface result, io/fs interface
  result, slice-of-T, positional field, array-of-T, chan send of an
  erased value, positional os.Root opener), raising the rejection set
  round-50 scanner misses exactly the seven new forms (and the
  converted 267-268 against the pre-round-50 scanner), the fixed
  scanner rejects all 227. Gates: go test ./... incl -race, go vet,
  tree gate, CGO_ENABLED=0 build and test, ten cross-compiles across
  the per-target listing matrix, SOW audit — all green. Counts
  unchanged at this commit: production 4,792 raw lines / tests 4,877
  raw lines. Decision 5A remains open for user ratification; Milestone
  2 remains blocked until the final review passes.

### 2026-08-13 - round-52 gate re-review closed embedded, var-bound, anonymous struct-literal, container-element, pointer-literal, and explicit-instantiation launders (HEAD 5acd2a6; records 6470f21)

- The round-52 adversarial gate hunt (six narrow reviewers:
  scanner diff review, gate hunting) failed with four live escapes
  across three root causes in the content-transfer scanner plus one
  further live escapes across three more classes, all confirmed
  against the final scanner:
- P0 - embedded-type composite-literal field laundering: type gateEmb
  struct{ io.Reader } with s := gateEmb{f} (positional) and s :=
  gateEmbR{root} / gateEmbR{Root: root} over type gateEmbR struct{
  *os.Root } left the binding container-tainted while the promoted
  Reader/Open methods stayed live, so io.ReadFull(s.Reader) and
  s.Open(name) streamed file bytes into the exempted shape (live,
  gate exit 0; probes embed/embedR/embedRk; the constructor twins
  embedC/embedAR were already rejected). Root cause: embedded fields
  were registered by type TEXT (io.Reader, *os.Root) while Go names
  embedded fields by type NAME (Reader, Root), so positional taint
  landed on a dead key; the keyed *os.Root twin escaped because the
  binding stayed kindContainer and promoted s.Open needed kindFile to
  trip the approved capability surface.
- P0 - var-bound generic instantiation: var gateHuntAliasXP =
  gateHuntWrapXP[*os.File] registered nothing (the variable has no
  generic parameters of its own), so files := gateHuntAliasXP(f) read
  a clean []*os.File result and files[0] fed the exempted inflater
  (live, gate exit 0; probe varinst). The direct explicit
  instantiation wrap[*os.File](f) was already rejected by the
  argument-approval rule; only the variable route slipped past.
- P0 - anonymous struct-literal positional elements: x := struct{ r
  io.Reader }{f} has no declared struct name, so the field-order
  registry had no entry and x.r stayed clean (live, gate exit 0;
  probe anons); the anonymous twin s := struct{ *os.Root }{root}
  combined the missing positional order with a missing embedded-
  handle escalation and promoted s.Open(name) flowed an untainted
  file into the exempted inflater (live, gate exit 0; probe
  anonroot).
- P0 - elided inner composite literals in containers: the element
  type name is omitted inside the container literal (s := []S1{{fn}},
  m := map[string]S1{"a": {fn}}, s := S6{in: []S1{{fn}}}, and a chan
  *os.File element []S5{{ch}}), so the inner composite literal carried
  no explicit type text and its fields were never registered; calling
  s[0].fn(name) or receiving from s[0].ch returned a clean io.Reader
  into the exempted inflater (live, gate exit 0; probes e1a, e10,
  e1c, e20, e36).
- P0 - pointer composite literals: s := &S1{fn} and s := &S2{r: f}
  addressed the struct before use; the unary & was never unwrapped,
  so neither positional func-file nor keyed any-field binding was
  registered and the file reached the exempted inflater untainted
  (live, gate exit 0; probes e8a, e8b, e34, e35).
- P0 - func-valued arguments to explicitly instantiated generics:
  gateH2E7[io.Reader](name, closure) and the variadic
  gateH2E31[io.Reader](name, closure) use an index-expression callee,
  which never reached the Ident-callee type-parameter mapping; the
  closure's *os.File surfaced through the erased body as a clean
  io.Reader (live, gate exit 0; probes e7, e31, e33 with e33 closed
  by the opaque-body result umbrella).
  run_mut expanded the GOMODCACHE/GOPROXY environment prefix as a
  command name and both x/sys-poisoning mutations "rejected" with
  exit 127 before the gate executed (silent vacuity since c86e162);
  run_mut now prefixes env. Running them for real exposed a second
  dormant defect in the same forms: the forged go.sum was written
  with a double h1: prefix (--dirhash already emits h1:), so the
  module could never resolve and the forms rejected via the
  fail-closed listing rule instead of the content pin; the /go.mod
  sum was also computed with the wrong format. The evil tree's
  go.mod is byte-identical to the official v0.35.0 go.mod, so the
  correct self-consistent forge pins the official /go.mod sum and
  the evil module/tree hash. The continuation harness review then
  proved the fix still incomplete on go1.26: Go only treats a module
  as downloaded (go list -m resolves a Dir) after the zip is
  verified and the .ziphash marker written, so a manually-seeded
  cache rejected via the fail-closed listing fallback and the
  content pin still never ran. Both forms now pre-verify the seeded
  cache/proxy with go mod download under the poisoned environment
  (the forged go.sum makes the evil zip self-consistent, so the
  toolchain writes the marker), after which the gate's own checkout
  content-hash boundary violation fires for real in both forms.
- Fixed at HEAD: embedded struct fields register by type name
  (embeddedFieldName strips pointer, package qualifier, and generic
  instantiation), so positional and keyed embedded elements share
  the key a selector read uses; registerGenericInstantiationVar
  records var-bound explicit instantiations and
  genericParamFilePositions resolves their calls through the base
  generic's fixed type arguments (parameter text substituted before
  the file-binding check, so the opaque-body rule covers both
  inference and fixed-argument calls); registerCompositeFieldTaints
  resolves anonymous literal order from the literal's own fields
  (named by name, embedded by type name);
  compositeLitEmbedsFileHandle escalates named and anonymous structs
  whose embedding chain reaches *os.File/*os.Root.
- Fixed at 5acd2a6: the explicit-instantiation callee route
  mounts genericParamFilePositions on index-expression callees whose
  base is an identifier (g[io.Reader](...));
  registerContainerElementFields records elided slice, map,
  nested-container, and channel elements by the element struct's
  declaration through a new element-field taint registry, and the
  read side resolves container element selects (s[0].fn, m["a"].fn,
  c[0].ch) against it; pointer composite literals unwrap their
  leading & before field registration and LHS taint application;
  genericMethodResults claims every declared result position when
  any call argument bears a file, covering closures surfaced through
  erased generic bodies.
- Pinned as durable exemption-shape appends: forms 278-282 (embedded
  *os.Root wrapper literal, var-bound generic instantiation with
  fixed file arguments and the erase-to-interface twin, anonymous
  positional element, anonymous embedded handle) and forms 283-288
  (explicit single and variadic instantiation with a func-file
  argument, elided slice element, map elided element, pointer
  composite literal, nested and channel elided container elements),
  raising the rejection set to two hundred thirty-eight
  the eleven new forms (and forms 245-246 were vacuous before the
### 2026-08-13 - round-53 final review reopened the gate: embed and //go:embed compile-time database copies closed (HEAD 2e0e3667db3c)

- The round-53 final review failed with two P2 findings: (1) the mmap-only
  gate accepted //go:embed database content - a production source embedding
  because the embed package was absent from the banned import set and the
  directive scan rejected only //go:linkname; (2) the
  NetworkEnrichmentV1Location pointer-vs-value parity deviation, which is
  the already-open decision 5A awaiting user ratification, not a new code
  defect.
- P2-1 closed at 2e0e3667db3c: the embed import is banned (a blank
  `_ "embed"` import alone would bypass it, so the directive is also
  rejected), and every production //go:embed directive now fails the AST
  scan as a compile-time content copy. Pinned as pre-fix-failing forms
  289 (non-blank embed import) and 290 (blank embed import with an
  embedded probe.db under a //go:embed directive). The pre-fix scanner
  misses exactly the two new forms; the fixed scanner rejects all two
- Vacuity against the round-51 scanner: exactly thirteen MISSes, forms
  278-290 (the eleven round-52 forms plus the two round-53 forms).
- P2-2 resolved by decision 5A (ratified 2026-08-13, option A): the
  value-plus-HasLocation representation is the approved zero-allocation
  equivalent of Rust's Option<NetworkEnrichmentV1Location>; the parity
  matrix now records the ratification, so the milestone gate no longer
  depends on this item.
- Gates at this commit: go test ./... incl -race, go vet, gofmt,
  CGO_ENABLED=0 build and test, ten cross-compiles, SOW audit - all green.
### 2026-08-13 - round-54 cosmetic alignment: dead word-read state removed, zero Pin reports WrongState, inflater wording aligned (HEAD 72e4d89d75fb)

- Removed dead state in internal/reader/membership.go readWordsInner
  (var data []byte, var err error, and the never-true error check after
  the storage switch); behavior identical.
- The zero-value Pin now reports WrongState on Close and checkOpen,
  matching the inert zero-view contract instead of panicking; pinned by
  TestPinZeroValueClose.
- Wording: "the three in-memory inflater nodes" became "the in-memory
  findExemptions comment, Validation section).
- Blank-import decision recorded: blank imports of banned packages remain
  intentionally uncaught by the import ban - blank imports expose no
  names and cannot transfer bytes, and the only blank-import-sensitive
  mechanism, //go:embed, is separately rejected as a directive.
- Gates at this commit: go test ./... incl -race, go vet, gofmt,
  ten cross-compiles, SOW audit - all green. Counts: production 4,789 raw lines / tests
  4,887 raw lines (the dead-state removal and zero-Pin test account for
  the delta; the gate scanner lives outside the module).

## Validation

Acceptance criteria evidence:

- Milestone 1 (immutable reader): CLOSED (final check 2026-08-17, HEAD
  4f11e3d; re-closed after the 2026-08-14 external-review rework whose
  three verified findings - duplicated search loops, overgrown gate
  machinery, stale records - were all fixed and pinned). Milestone 2
  (writer core) is implemented; Milestone 3 is in progress: chunks
  1-3b-2 are implemented; chunk-3b-2 slice 3 (publish_set surface)
  CLOSED at HEAD 8345891 with the five-aspect review at PASS, no
  P0-P2 (Status entry above).
- Slice-3 evidence: single-level five-aspect adversarial review at HEAD
  2a5e78e with the Rust implementation as the mandatory baseline; all
  findings fixed and re-reviewed to no P0-P2 by the same five
  reviewers (Pauli/Hume/Aristotle/Aquinas PASS at 3897b4b; Ohm FAIL at
  3897b4b, PASS at 45d453f; test-portability delta 8345891 verified
  across the full matrix).

Tests or equivalent validation:

- `go test ./...` (all packages, default tag set) - green.
- `go test -tags v4work ./...` (necessary-work counters and fault hooks
  compiled in) - green.
- `go test -race ./...` and `go test -race -tags v4work ./...` - green.
- `go vet ./...` - clean; `gofmt -l .` - empty.
- Cross-compilation: linux, darwin, freebsd, netbsd, windows (amd64) -
  all build.
- Conformance: 6/6 Rust fixtures cross-open with exact semantics; 3/3
  invalid mutations rejected with the typed error; structured absence
  probes included.
- The mmap-only / file-I/O policy gate: the full-codebase four-reviewer
  review PASSed at 2a5e78e with one P1 fixed (metadata streaming, record
  in Status above); enforcement now lives in review aspect 2 plus
  periodic full-codebase sweeps (Review Process, step 3). No scanner or
  mutation corpus exists.
- `.agents/sow/audit.sh` - clean.
- Necessary-work counters: pinned in query_work_test.go against the
  frozen fixtures; the pins moved with the leaf-cache reduction (Status
  entry above).

Real-use evidence:

- The Rust peer has accepted representative update-ipsets replay evidence.
  Equivalent Go evidence is an implementation acceptance requirement, not
  a planning claim.

Reviewer findings:

- Single-level five-aspect review, 2026-08-21 (HEAD 2a5e78e): five
  reviewers, disjoint aspects, Rust authority baseline; verdicts FAIL x4
  plus PASS-with-P2 x1; all findings fixed and validated (Status entry
  above).
- Re-review round 1 (delta ae57dfa): Ohm FAILed one P2 (code 77 vs 64
  records) plus two P3; Aquinas FAILed one P2 (freebsd/netbsd residue
  regression and the missing FreeBSD no-replace capability) plus two P3;
  Pauli, Hume, and Aristotle re-reviews were still running when this
  entry was written. All round-1 findings are fixed and validated
  (Status entry above, including the FreeBSD linkat machine, the NetBSD
  preparation classification, and the custody identity compare).
- The slice closes only when all five reviewers report no P0-P2 on the
  final delta.
- The earlier milestone-1 gate rounds and the retired scanner-era form
  narratives are preserved in git history and are not reproduced here;
  their dated execution-log entries remain below.

Same-failure scan:

- The publish-classification fixes cover every publication outcome path
  (rename refusal, fail-if-exists, replace, discard, cleanup) and every
  deliverable that reports a publication outcome; the error-type
  consolidation covered all hand-rolled join chains in the writer; the
  cursor gates cover both direct and feed-range constructors for both
  address families; the ASCII fold is one shared authority used by
  reader and writer; stale gate-era comment vocabulary has zero non-test
  hits (grep-verified).

Sensitive data gate:

- This SOW contains repository-relative paths, public upstream identity,
  generic platform names, code metrics, and synthetic/public benchmark
  descriptions only. It contains no raw secret, credential, operational
  host alias, personal or customer/community data, private endpoint, or
  proprietary incident detail.

Artifact maintenance gate:

- AGENTS.md: unchanged; this SOW's review rules stand at the top of this
  file per the user decision (SWARM.md is intentionally untouched).
- Runtime project skills: project-final-review and project-v4-rust
  unchanged; no new skill was needed for this round.
- Specs: no format or behavior change was made; the v4 contract is
  unchanged.
- End-user/operator docs: v4/conformance/README.md corrected (compressed
  metadata-chain size example); no other public document changed.
- End-user/operator skills: none exist and no public workflow changed.
- SOW lifecycle: this file remains `in-progress` under `current/`;
  Milestone 1 is closed, Milestone 2 is implemented, Milestone 3 is in
  progress; SOW-0017 remains the separate Phase-2 item.

Specs update:

- None: the fixes align the Go implementation with already-normative
  Rust behavior; nothing in the wire contract changed.

Project skills update:

- None for this round; the review process itself is recorded in this
  SOW's top rules per the user decision.

End-user/operator docs update:

- The Go SDK documentation is an implementation deliverable; the only
  document corrected this round is v4/conformance/README.md (chain-size
  example).

End-user/operator skills update:

- None exist; reassess at closure.

Lessons:

- Passing tests around private fragments do not establish a usable SDK or
  current wire compatibility.
- A semantic port needs an explicit parity matrix and cross-language
  execution; source similarity is neither required nor sufficient.
- The most dangerous way to port this engine is to preserve the obsolete
  Go storage architecture because it already looks substantial.
- Single-reviewer passes converge; only adversarial multi-agent passes
  with disjoint briefs found the catalog-limit, kind-class,
  dangling-reference, FreeBSD, and paperwork defects.
- Necessary-work pins are part of the code under review: when a
  performance fix legitimately reduces page visits, the pins move with
  the measured reduction, never the other way around.

Follow-up mapping:

- Snapshot signing remains tracked by pending SOW-0017.
- The Linux no-replace-primitive acquire/main classification edge
  recorded in the slice-3 close entry is tracked to the M4 sidecar
  coordination chunk (the staged Go flow has no coordination rename
  until then); it is inside this SOW, so no separate SOW file is
  warranted.
- No other deferred item is created by this milestone.

## Outcome

Milestone 1 (immutable reader) is closed. Milestone 2 (writer core) is
implemented. Milestone 3 is in progress: chunks 1-3b-2 are implemented
and closed at the five-aspect review gate (slice 3, the publish_set
writer surface, closed at HEAD 8345891 with all five aspect reviewers
at PASS). The next M3 chunk is the snapshot writer surface
(snapshot::snapshot_to parity). SOW completion remains pending final
validation and user acceptance.

## Lessons Extracted

Pending implementation.

### 2026-08-12 - external audit close-out round (milestone-1 gate re-verification)

An external full-scope review found five real P0/P1 contract defects after
the six-reviewer pass declared PASS. All were verified against code and
specs, fixed, and pinned by pre-fix-failing tests:

1. Obsolete retention semantics had been reintroduced: `RetentionTag()` and its
   "retention" test survived the deletion (binary-format-v4.md:311 forbids
   the compatibility alias; milestone-0 report classified them for
   deletion). Removed from v4/go/types.go and types_test.go.
2. Pin value copies double-decremented the reader pin count: `p2 := *p1`
   carried its own closed flag. Every Pin now references one shared
   private pinState; value copies and pointer aliases close the same
   logical pin. Pinned by TestPinValueCopySharesClose,
   TestPinValueCopyKeepsReaderBusy, and
   TestPinValueCopyCannotReleaseSecondPin (all fail on the pre-fix code).
3. DirectSemantic registry drift: public Go values were 0/1/2 while the
   Rust engine-defined registry is Generic=1, FirstSeen=2, LastSeen=3.
   Go now exports 1/2/3; TestPublicSemanticFoundation pins them.
4. No-threat structured values had no clean absence result: the Go
   ThreatMembership returned a zero view with nil error. It now returns
   (MembershipView, bool, error) mirroring the Rust Option; a new
   Rust-produced corpus fixture (rust/structured-ipv4-nothreat.iprdb, six
   fixture manifest) with membership-id-zero values pins the absent path
   in both readers. Rust verify asserts Option None for the empty-feeds
   ranges; Go asserts present=false.
5. Duplicated authorities removed: error-code tables (public errors.go vs
   internal/format/codes.go) and Cardinality129 arithmetic (public types.go
   vs internal/format/cardinality.go) were independent copies. The
   internal/format package is now the single authority; the public package
   re-exports typed aliases (ErrorCode = format.ErrorCode and per-name
   constants; Cardinality129 = format.Cardinality129 plus wrappers).
   Uint64/Uint128 moved into the format authority to preserve the public
   method set.
6. Final-review regression guards: compile-time alias assertions pin the
   public ErrorCode and Cardinality129 as the internal/format types, and a
   negative source guard forbids the reintroduction of the obsolete
   retention symbol in any non-test production source; all three guards
   fail on the pre-fix tree.

Gates re-run at HEAD: go test ./... (4 packages), -race, vet, gofmt,
import graph, 9 cross-compiles, SOW audit, Rust conformance (6 fixtures),
and the regenerated no-threat fixture all pass. ZERO allocations and zero
atomics on every measured public hot path are unchanged (12 public + 8
internal checks at exactly 0).

## Followup

- SOW-0017 remains the separate Phase-2 signing item.

## Regression Log

### 2026-08-12 - final-review process regression

The session's round-6 final review reported PASS at HEAD 29e1dde after verifying
the supplied defect list and all mechanical gates. A fresh independent review
then found material issues outside that checklist:

- exported writable canonical ValueTag variables drive DirectSemantic and can
  be reassigned process-wide or raced with readers;
- Go exposes ImmutableInfo while the approved parity matrix, normative spec,
  and Rust authority use DatabaseInfo, with no recorded deviation;
- the milestone report retained stale statements about view allocations, the
  worker decision, and structured-view revalidation.

Root cause: the final review behaved as a closed checklist verifier, inherited
the prior repair narrative, sampled corrected record summaries, and allowed
green automation to end the review before an open-world public-contract and
record audit. The external review's claim that Milestone 2 lacked a
Pre-Implementation Gate was not reproduced: this SOW already has a ready gate.

Process repair: created and registered the generic `project-final-review`
runtime skill. Future final reviews start from zero trust, reconstruct authority
and complete scope, audit public interfaces and full records before running
gates, verify regression evidence and same-failure classes, and perform a
separate disproof pass before PASS. The skill is intentionally generic so the
same failure mode is prevented across all project work.

Validation for the process repair uses HEAD 29e1dde as the historical benchmark:
the workflow must identify the mutable tag authority, API-name deviation, and
three stale report claims even though every mechanical gate passes. Resolution:
the product fixes landed at HEAD 73bba50 (immutable tag accessors, DatabaseInfo
rename, corrected report statements) with the metadata figure re-measured at
2d2197a; the round-7 through round-10 re-review results are recorded in the
Gate execution record, with the closing round-10 PASS at HEAD 253f9d5.

Append regression entries here only after this SOW was completed or closed and
later testing or use found broken behavior. Use a dated
`## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend
regression content above the original SOW narrative.

## Regression - 2026-08-12 - round-10 false PASS and evidence-protocol hardening

A fresh independent review of closure HEAD 1c71299 invalidated the round-10 PASS
at 253f9d5. Four implementation/contract defects already existed at the reviewed
revision, and the closure commit introduced a fifth records contradiction:

- the approved public API requires `NetworkEnrichmentV1Location` with
  `Location *NetworkEnrichmentV1Location`, but the Go facade flattened the
  coordinates and added `HasLocation` without an approved deviation;
- structured lookup performs semantic flags/coordinate validation that the Rust
  hot path and the normal-operation contract intentionally omit, and a test
  incorrectly labels this extra work as Rust parity;
- direct scans, blob branches, and inline membership word reads repeat decodes or
  record reconstruction, while the milestone report claims one page-header
  decode per visited page;
- the closure header said Milestone 1 was closed and Milestone 2 could start while
  Validation still said reopened/blocked and contradicted the skill update;
- `Mapping.File()` exposes an unused raw file capability, and the source gate's
  method-call regex does not catch content-transfer helpers such as `io.ReadAll`
  or `io.Copy`.

Root cause: the generic final-review skill described the right review lanes but
did not make adversarial disproof the reviewer's explicit success condition. The
round-10 execution therefore sampled exported names, equated zero
allocations/atomics with necessary work, trusted the source gate, ignored an
unused boundary escape hatch, and attached PASS to a revision followed by an
unreviewed closure commit.

The first repair draft overcorrected with six mandatory evidence artifacts and a
formal closure protocol. The user rejected that as compliance-heavy and likely
to narrow thinking. Final process repair: `project-final-review` now gives the
reviewer one explicit mission - prove with concrete evidence that the work is
faulty, incomplete, harmful, or unsafe to merge. It requires understanding the
objective and blast radius, grants authority to inspect any relevant surface and
create `/tmp` tests or mutations, treats green evidence as something to attack,
continues after the first finding, and permits PASS only when the strongest
plausible disproof attempts fail. It also explicitly forbids modifying the
reviewed repository, interfering with running processes, or installing/removing
software. This framing is generic and applies to every project final review.

Resolution: the five external-audit findings were fixed at HEAD ca30026 with
pre-fix-failing regression pins, and the round-11 final-review findings
(view-lifetime guard retargeting, mmap-gate escapes, stale records) were fixed
at HEAD 2fd6cae; the round-12 gate findings were fixed at HEAD 4fdc671
(whole-tree selector scan, dot-import and bufio bans, durable gate
decision log for the user's ratification; it was ratified on
2026-08-13 (option A) after the user delegated the choice to the
implementing agent. The complete re-review trail is recorded in the Gate
execution record; the closing result is appended there when it completes.

### 2026-08-13 - sixth-sweep gate rewrite (AST scanner) (HEAD c42325a)

- The sixth final review failed with five P2 findings, all in the mmap
  gate and the records: split-after-the-dot selectors; type-blind
  exact-literal exemptions; the open-ended stdlib denylist
  (compress/gzip regex bug, log/slog, runtime/trace,
  sweep; and completion claims ahead of the review trail.
- The line-oriented text scan is replaced by the AST gate scanner at
  v4/go-gate/main.go (stdlib only): it parses every production file
  regardless of build tags, line wrapping, comments, aliases, or file
  names; syntactically taints *os.File values; bans 37 content-transfer
  imports and 56 selector families; constrains *os.File use to the
  mapping-lifecycle methods and same-package/module-internal/x-sys
  consumers; and exempts the three exact in-memory inflater nodes only
  with file-taint verification (c.r.Read(p)/c.r.ReadByte() over a
  *bytes.Reader field, and the two exact io.ReadFull(zr, out[...int(meta.
  MetadataUncompressed)]) shapes with a non-file zr).
  independent reproducers of the sixth review; an innocent
  modified; the startup sweep is removed. HEAD 81ca524 then pinned the
  aliased-os producer form as the forty-first; HEAD 6b05801 tainted
  *os.File results returned by same-package accessor methods.
- Gates at those HEADs: go test ./... incl -race, go vet, gofmt,
  all green. Counts: production 4,772 raw lines / tests 4,832 raw
  lines.

### 2026-08-13 - seventh-sweep gate hardening (alias/container/pipe classes)

- The narrow re-review of the sixth-sweep records found two P2 items:
  the accessor-method taint (HEAD 6b05801) had no mutation pin and no
  record entry, and the records cited HEAD c42325a for the 41-form
  count although that commit pinned forty forms (the aliased-os form
  arrived with HEAD 81ca524).
- Ampere additionally proved two untainted file-escape classes in the
  scanner: a separately built `&os.ProcAttr{Files: []*os.File{f}}`
  lost the container taint (both as a composite literal and as a
  field assignment), and a type alias `type zrAlias = *os.File`
  defeated the exemption predicate and the taint resolution.
- Fixed (HEAD e2dc7e0): file producers now carry result positions
  (`os.Pipe` = both results; error results never tainted); type
  aliases are collected and resolved in classifyType, signatures, and
  producer conversions; composite literals propagate container taint
  from any file/container element; `os.StartProcess` joins the banned
  selectors (closing the field-assign variant); `os.Pipe` joins the
  producers; multi-result assignment taints only the file positions.
  41 accessor-method, 42 alias-conversion, 43 alias-parameter,
  44 separately built ProcAttr, 45 os.Pipe producer, 46 innocent
- Gates at HEAD e2dc7e0: go test ./... incl -race, go vet, gofmt,
  audit - all green. Counts: production 4,772 raw lines / tests 4,832
  raw lines (gate scanner lives outside the module).

### 2026-08-13 - eighth-sweep gate hardening (field storage and channel transport)

- The seventh-sweep re-review (Ampere round 2) found one P1 and one P2
  still open in the scanner: a *os.File parked in a struct field
  (`var zb box; zb.r, _, _ = os.Pipe()`), then assigned to the exact
  exempted inflater reader (`zr = zb.r`) in metadata.go, passed the gate
  (compiling code, exemption granted); the same class over a
  `chan *os.File` (`zr = <-ch`) also passed.
- Root causes: package-level var state was per-file in the scanner, so a
  type-only struct var declared in one file was invisible to functions
  in another; struct-field writes and type-only/new(T) instances were
  not registered; channel element taint did not exist (the make() chan
  type text was never printed), and field-type aliases were resolved in
  classifyType but not in field reads.
- Fixed (HEAD c4b1b52): shared per-package taint state (all package vars
  collected before any file runs); struct-field write taint overlay;
  type-only var and new(T) struct registration; chan *os.File taint for
  declarations, make(), parameters, sends (ch <- f), receives
  (x := <-ch), and range loops; container index reads; alias-resolved
  field types in isFileExpr/isContainerExpr; SelectStmt traversal.
  exact inflater exemption in metadata.go with a struct-field stored
  file and a channel-transported file; form 49 pins the benign
  same-shaped control (int field) that must pass, proving the taint is
  not a false positive. Durable rejection set: forty-seven mutation
  forms; the interplay between the exemption guard and the file taint
  is now mutation-tested.
- Gates at HEAD c4b1b52: go test ./... incl -race, go vet, gofmt,
  audit - all green. Counts: production 4,772 raw lines / tests 4,832
  raw lines (gate scanner lives outside the module).

### 2026-08-13 - ninth-sweep gate hardening (closures, assertions, nested channels)

- The eighth-sweep re-review (Ampere round 3) found three more escape
  classes in the scanner: an inline FuncLit returning *os.File
  (`zr = func() *os.File { ... return f }()`) escaped the taint because
  closure bodies were not taint-propagated and closure calls were not
  producers; a type assertion `zb.r.(*os.File)` erased the taint of a
  file hidden in an interface field (no TypeAssertExpr
  classification); and the channel family had two gaps - chan chan
  *os.File (and chan C with C = chan *os.File) did not resolve
  iteratively, and the single-variable range form `for z := range ch`
  put the element in the Key slot, which was not tainted.
- Fixed (HEAD ddc5f9c): closure bodies are walked for taint propagation
  (they capture the outer state); closure calls and func()-typed
  identifiers are producers when they return *os.File; TypeAssertExpr
  taints when the asserted type is *os.File and otherwise delegates to
  the asserted value; chanElemFile resolves nested channels and alias
  chains iteratively; the single-variable channel range taints the Key
  slot; IndexExpr reads of containers are file-tainted in isFileExpr.
  assertion, two-hop channel transport, and single-variable channel
  range, all shadowing or exercising the inflater exemption; the benign
  control (form 49) and the innocent-file survival check (form 46)
- Gates at HEAD ddc5f9c: go test ./... incl -race, go vet, gofmt,
  audit - all green. Counts: production 4,772 raw lines / tests 4,832
  raw lines (gate scanner lives outside the module).


### 2026-08-13 - tenth-sweep gate hardening (parenthesized producers, alias funcs, type-switch binds)

- The ninth-sweep re-review (Ampere round 4) found five more scanner gaps:
  a parenthesized producer call `zr = (getFile)()` and a parenthesized
  closure `zr = (func() *os.File { ... return f })()` both hid the callee
  shape behind a ParenExpr node; a closure declared as
  `func() io.ReadCloser { ... return f }` hid the returned *os.File behind
  an interface result type; a type-only alias
  `type fileFn = func() *os.File; var getFat fileFn` registered nothing in
  the funcs table; and a type-switch guard `switch zv := x.(type) { case
  *os.File: ... }` never tainted the bound identifier.
- Fixed (HEAD 7caf351): unwrapParen strips parentheses in producerCall and
  the rules walk so selector and argument checks see the same call;
  closures whose body returns a tainted value are marked retFile at their
  FuncLit node and every declared result position is treated as
  file-tainted; funcTypeResultsFile resolves alias text through the
  package alias map before falling back to AST result checks; the
  type-switch prepass binds the guarded identifier when a case type
  resolves to *os.File.
  parenthesized benign control (HEAD 5c88ba3). Durable rejection set:
  scanner.
- Extended during the round-5 gate re-review (HEAD 3952097): defined
  func types (type F func() *os.File) now register in the alias map,
  func() *os.File values returned through same-package helpers keep
  the producer taint (callResultsFuncFile), and type-switch cases
  binding defined func types enter funcFile. Forms 60-63 pin the
  three classes plus the benign bytes.Reader control.
- Extended again during the round-5 re-review (HEAD b168aba): method
  receivers now resolve through the struct instance (not the
  receiver variable name) in callResultsFuncFile, and a callee that
  is itself a call returning func() *os.File is a file producer
  (zb.mk()(), useDef2(getDef3)()). Forms 64-67 pin the method
  boundary, both double-call shapes, and the benign int control.
  Documented residual: a *os.File exported by a third-party package
  (other than os.Stdin/Stdout/Stderr) is invisible to the syntactic
  taint unless the code names *os.File textually or moves it through
  an already-tainted route.
- Ampere round 5 found four more producer routes (HEAD 5f97f94):
  struct-field func values, chan of func() *os.File, any-erased func
  returns recovered by type assertion, and os.Stdin/Stdout/Stderr
  through interface closures. Fixed with the kindFuncFile/
  kindChanFuncFile split, struct-field func resolution in
  producerCall/classify, func-file type assertions, and the std
  handle file-expression set. Forms 68-72 pin the four classes plus
  the benign chan-of-func() int control.
- Ampere round 6 found three more producer routes (HEAD 36fe279):
  nested struct-field func chains (nh.inner.fn()), named
  interface-typed helpers whose bodies return tainted files or
  os.Stdout (getNamed()/getStd()), and chan-of-func values passed
  through same-package helpers. Fixed with a resolveStruct field-
  chain walk, a per-directory pre-scan that marks named producers
  (retFuncs) before any runFile, and callResultsChanFuncFile.
  Forms 73-77 pin the three classes plus the benign named io.Reader
  helper control.
- The named-producer pre-scan was extended to methods with a
  fixpoint (HEAD 5a4f8dc): mb.named() returning os.Pipe or
  os.Stdout through io.ReadCloser, and deep() -> mid() chains,
  are now producers (retMethods + prescanFileProducers). Forms
  78-81 pin the three classes plus the benign method control;
  form 77 was reworked to a compiling bytes.Reader wrapper shape.
- Ampere round 7 found a fourth producer route (HEAD 1a54443 -> 8696af3):
  a nested method-receiver chain (mhv.inner.mk()(), where mk is a
  method on minner reached through the mholder.inner field, returning
  a defined func type producing *os.File). Fixed by resolving the
  receiver expression through resolveStruct instead of requiring a
  plain identifier; the same fix applies to the chan-of-func caller
  (callResultsChanFuncFile). Form 82 pins the escape; form 83 pins
  the benign nested-method control.
- Ampere round 8 found two more producer families (HEAD 96f0515):
  method values are never classified (a method value bound to a
  variable, returned through an interface-typed helper, bound from a
  nested receiver chain, or sent through a package-level channel), and
  generic type-parameter pass-through erases the file taint
  (idf[T any](f T) T instantiated with *os.File). Fixed with method
  resolution in classify (declared *os.File results, retMethods
  taint, defined func-type results), a retFuncFiles/retMethodFiles
  body-scan registry, double-call resolution on func-file variables,
  package-level channel taint propagation, and type-parameter result
  mapping for generic calls. Forms 84-89 pin the six escapes; forms
  90-91 pin the benign method-value and benign generic controls.
  Residual documented: third-party exported *os.File and cross-file
  local-channel flows remain visible only through declarative types.
- Ampere round 9 found three consumer-path gaps (HEAD aa019c8):
  generic container element shapes ([]T) never bound the type
  parameter; method values returning chan-of-func-file never tainted;
  and struct fields assigned file-producing closures/chans lost their
  taint because consumers read only the declared field text. Fixed
  with token-boundary element matching in the generic rules,
  chan-result resolution for method values and chan-marked variables,
  full-kind fieldTaint writes, fieldTaint consumers in classify and
  producerCall, and package-var fieldTaint propagation from the
  prescan. Forms 92-95 pin the four escapes; forms 96-97 pin the
  benign generic-container and benign func-field controls.
- Self-audit before Ampere round 10 closed the remaining channel
  consumers: receive classification (ARROW) distinguished chan-file
  element kind, RangeStmt classified the ranged expression whole
  (struct-field channels and method values), and SendStmt recorded
  selector-typed channel fields. Forms 98-100 pin the three escapes;
  forms 101-102 pin the benign range and benign receive controls.
- Ampere round 10 found the container-element route (HEAD ed0a0f9):
  map/slice fields holding file-producing funcs (fm.m["k"]()) were
  invisible because producerCall had no IndexExpr callee case,
  applyLHS did not record element writes, and classify read only
  file-container elements. Fixed with an elementTaint registry
  (element reads/writes, composite element kinds, declared element
  shapes for fields/params/vars), an IndexExpr callee case in
  producerCall, and exprText coverage for map/ellipsis/index types.
  Forms 103-106 pin the four escapes; form 107 pins the benign map
  field control.
- Ampere round 11 found the anonymous-receiver method route (HEAD
  63d665d): func (T) m() with no receiver variable name was invisible
  because receiverOf required a receiver name and never resolved the
  receiver type for method-value keys. Fixed by deriving the receiver
  struct from the receiver type expression, trimming pointer/generic
  spellings, keying alias-resolved method signatures, and never
  misregistering methods as package funcs. Forms 108-111 pin the four
  escapes (direct file, interface-hidden, pointer receiver, map-field
  method value); form 112 pins the benign anonymous-receiver control.
- Ampere round 12 found the alias-receiver route (HEAD 5fe4b4f):
  receiver aliases (type a = s) keyed retMethods and instance lookups
  inconsistently -- receiverOf returned the raw alias text while call
  sites resolved it through structBase, so interface-hidden results
  were invisible; and resolveStruct/classifyStruct/classify accepted
  only registered struct names for composite literals, so alias-named
  instances (rF{}.m()) never resolved. Fixed by resolving receiver
  aliases inside receiverOf and resolving alias names in every
  composite-literal struct lookup. Forms 113-114 pin the two escapes
  (alias-variable interface-hidden call, alias-literal direct file);
  form 115 pins the benign alias-receiver control.
- Ampere round 13 found the receiver-resolution class (HEAD
  85db9dc): defined-type receivers (type b a) were never registered,
  generic instantiations (gsG[int]) never stripped to the base name,
  embedded-field promotion (hE{gsE}) was dropped at collection, and
  pointer aliases (type p = *s) keyed methods under the pointer
  spelling. Fixed with a defined-type chain map, a resolveStructName
  helper (aliases + defined types + pointer + generic suffixes) used
  by structBase/receiverOf/every composite-literal lookup, and an
  embedded-method walk (methodMeta) for method-value and call
  resolution. Forms 116-119 pin the four escapes; form 120 pins the
  benign embedded-promotion control.
- Ampere round 14 found the pointer-defined-type route (HEAD
  36f6e82): var p *d with type d gs (defined type) and no initializer
  registered neither the pointer nor the defined chain because
  resolveStructName applied the defined-type lookup before the
  pointer trim. Fixed by running the alias/defined/pointer/generic
  reductions to a fixpoint in resolveStructName. Form 121 pins the
  escape; form 122 pins the benign pointer-defined-type control.
- Ampere round 15 found the indexed-receiver class (HEAD 99b211a):
  new(d) with d a defined type never resolved through the defined
  chain, and array/map-index receivers (arr[1].get(), mm["k"].get())
  had no IndexExpr resolution at all. Fixed with a varTypes registry
  (package vars, local typed vars, and short-decl composite
  literals), element-type stripping to the base struct in resolveStruct
  and classifyStruct, and defined-chain resolution in both new()
  cases. Forms 123-125 pin the three escapes; form 126 pins the
  benign array-index receiver control.
- Ampere round 16 found the element-receiver class (HEAD 7f72ca3):
  indexed bases beyond bare variables (struct fields, call results,
  dereferenced pointer-to-containers), make() short declarations,
  range-variable element receivers, and chan-receive receivers were
  all invisible. Fixed with a typeOfBase resolver (variables, struct
  fields, call results, deref/paren wrappers), a stripElemType
  helper (container and channel wrappers), exprElemStruct for range
  and receive element binding, make() type registration for
  short-declared containers, and ARROW receive resolution in
  resolveStruct/classifyStruct. Forms 127-132 pin the six escapes;
  form 133 pins the benign make() map receiver control.
- Ampere round 17 found the range-literal-receiver class (HEAD
  255f34c): range variables never recorded their element type in
  varTypes, so container/chan-typed bindings were blind as indexed or
  receive bases, and composite-literal index bases (map[string]*gs{...}
  ["a"]) had no typeOfBase case. Fixed by recording one wrapper-stripped
  element type for range bindings and adding the CompositeLit case to
  typeOfBase. Forms 134-135 pin the two escapes; form 136 pins the
  benign composite-literal indexed receiver control.
- Ampere round 18 found the bound-receiver class (HEAD 3cfe554):
  type-switch bound variables (case *gs: v.get()) never registered as
  struct instances, and multi-assignment call results (a, _ := f())
  never recorded their declared result type, so both were blind as
  receivers or index bases. Fixed by registering case structs and case
  type texts for type-switch bindings and recording the declared
  result type per index in applyLHSMulti. Forms 137-138 pin the two
  escapes; form 139 pins the benign type-switch bound receiver
  control.
- Ampere round 19 found the call-result-binding class (HEAD
  90ea53c): single-value call results (a := mkArr()), method-call
  results (a := box.mkArr()), type-switch default-clause bindings
  (switch v := iv.(type) { default: v.get() }), and multi-assign
  element reads (a, _ := mm["k"], 0) all failed to record the binding's
  type or struct instance, so each stayed blind as an indexed or
  receiver base. Fixed by recording result-0 declared types and
  non-call element types in applyLHS/applyLHSMulti (one wrapper
  stripped, struct instances only when the binding type itself names a
  struct), a typeOfBase IndexExpr case, and default-clause
  registration from the switched expression in the type-switch
  handler. Forms 140-143 pin the four escapes; form 144 pins the
  benign single-LHS call-result index control.
- Ampere round 20 found the interface-and-channel binding class
  (HEAD 2b3006a): explicit generic instantiations (a := mkGen[*gsG]())
  never substituted the call's type arguments into the result type,
  type-switch default clauses over interface-valued expressions
  (mkIf().(type), sd.f.(type)) never resolved the interface to its
  signature-identical implementations, and multi-assign from a channel
  receive ((<-chS), 0) recorded no element type. Fixed by substituting
  explicit type arguments in applyLHS/applyLHSMulti via
  genericSubstitutedResults, registering interface method signatures as
  pseudo-structs (with methodFull text matching), an ifaceImplProducer
  union over signature-identical implementations, and a typeOfBase
  UnaryExpr ARROW case. Forms 145-148 pin the four escapes; forms
  149-150 pin the benign generic and interface-default controls.
- Ampere round 21 found the generic receiver-binding class (HEAD
  5f18d4f): generic-receiver method results never substituted
  receiver type arguments, explicit-instantiation calls
  (mkT[*os.File]()) and their direct method flows were invisible,
  and argument-inferred generic struct bindings
  (mkT2(&gsG{}).get()) bound no file taint. Fixed with a
  recvTypeParams registry feeding genericMethodResults, a unified
  genericCallResults helper (explicit and inferred via
  inferTypeArgs), a typeOfBase unary-& case for generic literal
  receivers, and generic-first wiring in producerCall/classify/
  resolveStruct/classifyStruct/applyLHS/applyLHSMulti. Forms
  151-156 pin the six escapes; forms 157-158 pin the benign
  bytes-only controls.
- Ampere round 22 found the alias-spelled generic binding class
  (HEAD 82b96bf): a generic type argument written through a type
  alias (type zfA = *os.File; gRA[zfA]{}) was substituted as
  literal text and never alias-resolved, so the taint checks
  compared container results like []zfA instead of []*os.File and
  the io.ReadFull exemption could grant a file-backed zr. Fixed in
  substituteTypeParams: every type argument is reduced through
  resolveTaintType (alias and defined-type chains to a fixpoint)
  before substitution, and the substituted result is reduced
  again. This covers receiver instantiations, explicit and
  inferred generic calls, method values, and embedded promotion.
  Forms 159-164 pin the six alias escapes; forms 165-166 pin the
  benign bytes-backed alias controls.
- Ampere round 23 found four reader-shape escape classes (HEAD
  85c6f2c), all reaching the io.ReadFull exemption with gate exit 0:
  (1) container element reads from call results (a[0] of a
  []*os.File generic or plain result, m["k"], *p on *zfA) never
  classified the element; (2) chan-of-file carriers were blind in
  return positions (return <-c) and method/generic-call results
  (h.ch() chan *os.File, mkC[*os.File](), rr.mc()) never registered
  as chan taint; (3) cross-package aliases as generic type arguments
  (mapping.MappingFile) could not resolve because each directory is
  an independent pkgInfo; (4) generic method results naming a struct
  (r := rr.mk() with T bound to wS) never registered as struct
  instances, losing field taint on r.f. Fixed with elementReadKind
  (declared-element shape of index/deref bases) in classify,
  isFileExpr and isFileOrContainer; chanCarrier (identifier
  registries plus call classification) for receive positions, and a
  declared-results chan loop for method and function calls; a
  process-wide qualifiedAliases registry keyed package-clause name;
  and genericMethodResults/methodMeta consultation in resolveStruct
  and classifyStruct. Forms 167-174 pin the eight escapes; forms
  175-178 pin the benign bytes-backed controls.
- Ampere round 24 found the import-renamed qualified alias class
  (HEAD 932a0e8): a locally renamed import qualifier (import mm
  ".../internal/mapping") was never translated back to a package
  path, so mm.MappingFile generic type arguments, local alias chains
  of renamed imports (type MChainRen = mm.MappingFile), container
  element spellings ([]mm.MappingFile), declared variables
  (var z mm.MappingFile; z.Chdir()), and type assertions
  (v.(mm.MappingFile).Chdir()) all bypassed the gate with exit 0.
  Fixed with a per-directory alias registry (pkgAliasesByDir keyed by
  relative package dir), a per-file import snapshot (currentImports),
  and aliasLookup qualifier translation through the import map before
  matching the scanned directory; resolveTypeText, resolveDefinedType
  and resolveStructName all route through aliasLookup. Forms 179-182
  pin the four rejects; forms 183-184 pin the benign renamed-qualified
  bytes controls.
- Ampere round 25 found the func-typed generic-method class
  (HEAD 21b5742): producerCall's generic-method branch claimed a
  func-typed result (mresG of func() *os.File after the direct
  *os.File position check missed) as a direct file position, so
  applyLHS recorded the binding via st.file instead of st.funcFile
  and calling the bound func lost the taint - gRZ[func() *os.File]
  {}.mk()() reached the io.ReadFull exemption with gate exit 0.
  Fixed by removing the funcTextFile producer claim so classify's
  existing generic-method loop yields kindFuncFile and applyKind
  registers the func-file; the non-generic method control was
  already caught, proving the shape is right and only the generic
  registration was blind. Forms 185-189 pin the five rejects
  (direct, embedded promotion, local alias, renamed-import func
  alias, unapproved method on the invoked result); form 190 pins
  the benign bytes-backed func control.
- Ampere round 26 found two adjacent escape classes at HEAD
  e1b1229: (1) mixed multi-result non-generic functions
  (getFn() (func() *os.File, error) bound as f, _ := getFn();
  f()) lost the func-file at the exact func-typed position
  because callResultsFuncFile required every declared result to be
  a func-file, with no per-position fallback for plain functions;
  (2) a defined type over a qualified or complex underlying
  (type x mm.A, type x []*os.File) registered nothing in
  definedTo, and cross-package defined func types
  (type F func() *os.File in mapping) were invisible to
  qualified references, so both reached the io.ReadFull exemption
  with gate exit 0. Fixed with callResults/callResultKinds/
  callResultKindAt (per-position result kinds through generic and
  declared signatures), per-position carrier registration in
  applyLHSMulti for call RHS, definedTo registration for every
  non-func non-ident underlying type text, and qualified
  registration of defined func types. Forms 191-196 pin the six
  rejects (mixed pos 0, mixed pos 1, defined over renamed alias,
  local defined func type, cross-package defined func type, defined
  over defined); forms 197-198 pin the benign bytes controls.
- Ampere round 27 found three adjacent escape classes at HEAD
  180024b: (1) a generic receiver bound to an interface whose method
  declares mixed results (Get() (func() *os.File, error)) was claimed
  by producerCall as a raw file position because interface method
  signatures are stored as pseudo-fields, so applyLHSMulti recorded
  st.file instead of st.funcFile and invoking the bound func lost the
  taint (both mixed positions and the chan-of-func variant); (2) all
  non-generic methods lost their declared results in callResults
  because methodMeta's ok flag reports body-marked producers
  (retMethods), not method existence - mixed method results
  (mk() (func() *os.File, error)) bound with no position taint; (3)
  a defined type over an alias (type D A; A = func() *os.File) and an
  alias over a defined func type (type E = D2) were invisible to
  cross-package spellings: only aliases and defined func types
  entered the qualified registries, and a first-hop bare name from
  another directory could not resolve further. Fixed with declared-
  result precedence in classify plus a per-position kind preference
  in applyLHSMulti (func-file/chan carriers keep their invoke-able
  kind when a producer claim overlaps), method-existence detection in
  producerCall's func-field claim (a declared method is not a func
  field), non-nil-results acceptance in callResults for ordinary
  methods, and defined-type registration plus a per-directory
  fixpoint (finalizeDirAliases) closing alias/defined chains in the
  qualified registries. Forms 199-205 pin the seven rejects
  (interface mixed pos 0, interface mixed pos 1, interface
  chan-of-func, defined over alias, alias over defined func, method
  mixed pos 0, method mixed pos 1); form 206 pins the benign
  interface-typed bytes control.
- Ampere round 28 found three adjacent escape classes at HEAD
  a8097a1: (1) an interface embedding a file-producing interface
  (type IEmb interface{ IBase }, IBase.Get() func() *os.File)
  resolved no promoted method because methodMeta's embedded walk
  propagated only body-marked producers (ok flag) and dropped
  declared results, so x.Get()() on an interface-typed generic
  result reached the io.ReadFull exemption with gate exit 0, in
  both the single-result and the mixed multi-result shapes; (2) a
  defined struct in another package used as a generic type argument
  (gRZ28[mm.S28]{}, s.Get()) never resolved because each directory
  keeps an independent package info and only aliases/func types
  were mirrored across directories; (3) a qualified defined chain
  of nine named hops (mm.J28 behind alias A28 and hops B28..I28)
  exceeded the single-pass fixpoint budget and was map-order
  dependent (passing only when the map iteration chanced to make
  the final hop resolvable early). Fixed by propagating promoted
  declared results in methodMeta's embedding walk, process-wide
  mirrors of remote structs/methods/method-full/embedded chains
  with a parse-time seed-merge (local wins on collision),
  self-entries for struct spellings in the qualified alias
  registries, result-type resolution through resolveStructName in
  classifyStruct/resolveStruct, and a full fixpoint loop for the
  per-directory alias/defined closure with self-hop guards
  (resolveDirText budget 8 -> 64). Forms 207-210 pin the four
  rejects (embedded interface single, embedded interface mixed,
  cross-package struct method, nine-hop qualified chain); forms
  211-212 pin the benign bytes controls.
- Ampere round 29 found two adjacent escape classes at HEAD
  8385134/fffc4dc: (1) a renamed import qualifier on a cross-package
  interface embedded in a reader interface or struct (type IEmb
  interface{ mm.IMapBase }) never reduced to a registered key: the
  interface branch mirrored structs/methods/embedded chains but - 
  unlike the struct branch - registered no qualifier self-entry, so
  the promoted file method resolved nothing and x.Get()() reached the
  io.ReadFull exemption with gate exit 0; the clause-name spelling
  passed because the clause-qualified mirror key happened to match;
  (2) a generic interface instantiated at the embedding site
  (type IEmbGN interface{ IBaseGN[func() *os.File] }) promoted the
  declared results with the raw type parameter unsubstituted, so
  Get() T never matched the file shapes; the same gap covered the
  chan-of-func variant, a renamed generic interface instantiation,
  and a cross-package generic struct receiver. Fixed by registering
  the qualified self-entry for interface names exactly like structs
  (pkgAliasesByDir + qualifiedAliases), recording generic interface
  type parameters in recvTypeParams (with a process-wide mirror
  seeded per parseDir, closing the remote generic-receiver gap), and
  substituting the embedding's type arguments in methodMeta's
  promoted-method walk (args from parseBracketArgs, substitution via
  did not compile (unused "bytes" import in the reader file) - was
  the missing check is documented in the form comment. Forms 213-217
  pin the five rejects (renamed-qualifier interface embedding,
  generic-interface instantiation with func-file argument, same with
  chan-of-func argument, renamed-qualified generic interface
  instantiation, cross-package generic struct method); forms 218-221
  pin the benign bytes controls.
- Ampere round 30 found the defined-hop instantiation class at HEAD
  b53652d/9be245e: a defined type over an instantiated generic
  interface embedded in an interface (type D IBaseG[func() *os.File];
  type IEmb interface{ D }) lost the type arguments at the embedding
  walk because methodMeta's promoted-walk extracted brackets from the
  raw embedded spelling ("D") while the instantiation lives in the
  defined chain's target text ("IBaseG[func() *os.File]"), so the
  promoted results propagated the raw type parameter and Get() T
  reached the io.ReadFull exemption with gate exit 0; the renamed-
  qualified cross-package twin had the same shape. The same gap
  existed in genericMethodResults' embedded loop. Fixed by extracting
  the embedding's type arguments from the resolved text
  (parseBracketArgs(resolveTaintType(emb, info))) in both walk sites,
  so alias and defined chains carrying the instantiation substitute
  exactly like a direct spelling. Forms 222-223 pin the two rejects
  (reader-local and renamed-qualified defined-over-instantiated
  embedding); form 224 pins the benign bytes control.
- Ampere round 31 found the nested generic-instantiation class at
  HEAD f2d40d4: a multi-level generic-interface embedding (type
  InnerL[T] interface{ Get() T }; type IBaseGL[T] interface{
  InnerL[T] }; type IEmb interface{ IBaseGL[func() *os.File] })
  substituted type arguments only at the frame owning the brackets,
  so the frame declaring Get returned the raw type parameter "T"
  and x.Get()() reached the io.ReadFull exemption with gate exit 0
  (three-level and chan-of-func variants included); the intermediate
  frame's identity substitution (InnerL[T] with its own argument T)
  masked the leak during the audit's control probes. Fixed by
  threading type parameters and arguments down the embedding chain
  in methodMeta: every frame substitutes its own instantiation into
  the next embedded type text before recursing, the declaring frame
  applies the accumulated arguments to its declared results, and a
  frame-level interface-parameter registry (ifaceParams, mirrored
  process-wide like the receiver-parameter registry) carries generic
  interface parameters across packages, with the genericMethodResults
  receiver-substitution walk given the same threading and the
  embedded-entry argument list. Forms 225-226 pin the two rejects
  (two-level and three-level/chan variants); form 227 pins the
  benign bytes control.
- Round-41 narrow re-review found a sidecar error-class
  divergence at HEAD 550d107: Go's immutable open stat'ed the main
  file before checking the canonical .readers sidecar, so a live
  database whose main file was missing/renamed but whose sidecar
  remained returned Io (31) while Rust's open_immutable refuses
  with WrongMode (11) because require_sidecar_absent runs before
  open_read_only. Fixed at the reader level: OpenImmutable now
  applies the same sidecarAbsentUnderLock check before the main
  file is touched (the under-lock re-check inside the mapping open
  stays authoritative), pinned by the pre-fix-failing test
  TestSidecarPresence/missing-main-sidecar-present.
- Round-42 gate re-review found the x/sys source-content gap at HEAD
  6733d1c: the path-only allowlist accepted a poisoned GOMODCACHE
  checkout (evil extracted dir plus download cache at the allowed path)
  and a file proxy serving an evil x/sys with a self-consistent forged
  go.sum (both proven live with a smuggled unix.Pread2, gate exit 0 on
  both vectors), because nothing pinned the module content; the gate now
  pins the exact version, the module-cache path, the extracted-tree
  content hash, and the module zip/go.mod sums to the official v0.35.0
  values, and the assembly-object rejection is case-insensitive, pinned
  the fail-open listing gap: the per-target go list ./... loop swallowed
  listing failures, so a module the go toolchain cannot list (symlinked
  package files, parse errors) passed with an empty package list and no
  import checks; go list failures now fail the gate per target and the
  per-package import listing fails closed too, pinned as form 248,
  gate re-review then found the mmap-gate denylist gaps: os.CopyFS
  directory copies, os.OpenInRoot/os.OpenRoot handles reaching stream
  wrappers, and the x/sys descriptor-transfer primitives (unix.Tee,
  unix.Vmsplice, unix.IoctlFileClone/CloneRange/DedupeRange, darwin
  unix.Clonefile/Clonefileat) bypassed the scan (all proven live, gate
  exit 0); CopyFS and the x/sys primitives join the banned selector set,
  os.OpenInRoot/os.OpenRoot join the file-producer table so Root methods
  through a struct field: h.r.Open(name) after h := struct{r
  *os.Root}, gate exit 0) was then closed by resolving *os.Root as a
  file-bearing type everywhere *os.File does, pinned as form 252,
  producer-value re-review then closed the file-method-value,
  initialized func-typed-variable (Root and *os.File), and plain
  stdlib-producer-value escapes (forms 253-256); the round-48
  adversarial re-review then closed bound method expressions on
  file-bearing receiver types (form-local and package-level) and
  same-module cross-package producer vars (forms 257-260), raising
- Gates at current HEAD: go test ./... incl -race, go vet, gofmt,
  rejects 249-256 cover os.CopyFS directory copies, os.OpenInRoot/
  os.OpenRoot handles reaching stream wrappers, the x/sys
  descriptor-transfer primitives, *os.Root laundering through struct
  fields, file method values, initialized func-typed variables with
  file-bearing declared results, and stdlib producer values bound
  without a declared type, and round-48 rejects 257-260 cover bound
  method expressions on file-bearing receiver types and same-module
  cross-package producer vars, and round-49 rejects 261-266 cover
  doubly-parenthesized, renamed-import, alias-over-renamed, and
  wrapper-promoted method expressions, value-bound cross-package
  producer vars, and interface conversions laundering file values,
  and round-50 rejects 267-270 cover generic identity functions
  erasing a file taint into an interface result, composite-literal
  field laundering, method expressions on instantiated generic
  wrappers, and embedding chains deeper than the original walk
  budget, and round-51 rejects 271-277 cover file-bound generic
  result erasure and positional composite-literal field launders
  (with 267-268 converted to exemption-shape appends), and round-52
  rejects 278-288 cover embedded file-handle literal launders,
  var-bound generic-instantiation results, anonymous struct-literal
  positional elements, elided container-element fields, pointer
  composite literals, and explicit-instantiation callee closures),
  and round-53 rejects 289-290 cover the embed import and the
  //go:embed directive (compile-time database copies),
  ten cross-compiles,
  SOW audit - all green. Counts: production 4,789 raw lines / tests
  4,887 raw lines (gate scanner lives outside the module).


## Status History (appendix, 2026-08-11 .. 2026-08-13)

This appendix preserves the full pre-2026-08-14 Status narrative, moved
verbatim when the Status was compacted on 2026-08-14. It is historical
review detail: rounds 1-54 of the gate hardening, the milestone-1 review
history, and the close-out attempts that were later invalidated. Current
truth lives in ## Status; this appendix is context, not authority.


Sub-state: milestone 1 REOPENED pending re-review. The round-10 PASS at HEAD
253f9d5 and the closure commit at HEAD 1c71299 were invalidated by a fresh
independent audit; all five P2 classes were fixed at HEAD ca30026: the
implemented NetworkEnrichmentV1Location surface (value + HasLocation,
recorded as open decision 5A awaiting user ratification, ratified later on 2026-08-13), implicit semantic validation removed from
structured lookup, hot-path decodes cut to one page-header decode per
visited page with membership word reads served from the lookup-time record
decode, the contradictory closure records corrected, and Mapping.File
removed with the content-I/O source gate extended to io.ReadAll/io.Copy.
Regression pins: plausible-corruption decode acceptance, record-geometry
rejection at lookup, and the vector codec. The round-11 final review then
found one P1 (pin variable reassignment retargets the view guard and a
word read segfaults on released memory) and three P2 (decision 5A
unratified; mmap gate bypassable plus a Windows Mapping.File escape; stale
close-out records), all fixed at HEAD 2fd6cae with the cross-reader
reassignment regression test, or recorded for the user's decision (5A).
The round-12 final review then confirmed the lifetime fix but found two
remaining P2: decision 5A still unratified, and the mmap source gate still
bypassable (x/sys descriptor reads, bufio wrappers, dot imports, and
build-tagged packages). Fixed at HEAD 4fdc671: the gate is now a
whole-tree selector scan (find across all build tags) with dot-import and
mmap-only evidence (strace of an open/read/close session: openat, OFD
lock, mmap, munmap, unlock, close with no read/pread/readv/lseek on the
database descriptor) recorded in the report. The round-13 final review
then failed with three P2: decision 5A still unratified; the gate still
accepted indirect content-transfer forms (fmt.Fscan/Fscanf, io.CopyN/
CopyBuffer, reflection MethodByName, raw unix.Syscall(SYS_READ),
unix.CopyFileRange, Sendfile/Splice), its line-level tolerated-call
exemption could hide a forbidden transfer on the same line, and a
windows-tagged package could still import internal packages unseen by a
linux-only go list boundary check; plus two P3 comment corrections.
All fixed at HEAD dbdf2b7: tolerated calls are blanked as exact call
nodes instead of whole lines, the selector set covers every indirect
form, gzip and compress/zlib wrapper imports are banned, the boundary
fix then found the decoder/encoder family still open (encoding/json,
xml, gob NewDecoder(f).Decode, image/archive wrappers), os.File.WriteString,
a nested-paren blanking shadow, and reflect.Value.Method(i);
the gate now also bans the reader-consumer packages, covers
WriteString/WriteRune/NewDecoder/Decode/Encode/Method selectors, blanks
io.ReadAtLeast over a file still open, the writer-consumer packages
(log, text/template, html/template, os/exec, net/http, flate.NewWriter)
at HEAD bf33f2a (selectors ReadFull/ReadAtLeast/Print/Printf/Println/
Scan/Scanln/Scanf/NewWriter; the five writer packages join the import
ban; the two in-memory inflater io.ReadFull(zr, ...) nodes are exempted
exactly; the method-value and CopyFileRange forms compile, and the
nested-node probe stays an intentional textual tripwire), and the
of that fix found the io.ReadFull exemption itself paren-crossing (a
nested transfer could still be swallowed; a file-backed flate reader
named zr was exempted by name), the reflection Call invocation
unguarded (FieldByName("Read").Call), and the reader-constructor
packages (debug/elf and the debug/* family, go/parser, go/scanner,
text/scanner, text/tabwriter, mime/quotedprintable) unlocked; all fixed
at HEAD 149a200 (the io.ReadFull exemption is shape-bounded to the two
real nodes io.ReadFull(zr, out[...]), Call/CallSlice join the
selectors, the constructor packages join the import ban), and the
of that fix found the exemptions still name-keyed (a file-backed flate
reader named zr with a buffer named out, and a receiver field r, could
reproduce the tolerated shapes), so at HEAD c03e40c the exemptions are
exact literals and nothing else: c.r.Read(p), c.r.ReadByte(), and the
two io.ReadFull(zr, out[...int(meta.MetadataUncompressed)]) inflater
reads; same-named file-backed readers and other index shapes now fail
closed, two pin forms were added, and a startup sweep removes stale
the same pass; decision 5A remains the single open user decision and
blocks milestone close. Repository counts: production 4,792 raw lines /
tests 4,877 raw lines. Milestone 2 must not start until a new
independent final review passes.

The sixth final review then failed with five P2 findings, all in the mmap
gate and the records: selector splitting after the dot (`file.\nRead(p)`
and `io.\nReadAll(f)` compile and bypass a line scan); type-blind
exact-literal exemptions (a struct whose `c.r` is `*os.File` using exactly
`c.r.Read(p)`, and a function whose `zr` is `*os.File` using exactly
`io.ReadFull(zr, out[:int(meta.MetadataUncompressed)])`, both pass); an
open-ended stdlib denylist (the gzip regex never matches `compress/gzip`;
`log/slog.NewTextHandler`, `runtime/trace.Start`, and `os.StartProcess`
with `ProcAttr{Files: []*os.File}` consume a file unseen); a destructive
gate reports PASS, and untracked user work can be destroyed); and
acceptance records claiming completion while the six-reviewer PASS at
HEAD 360130c was not recorded and round-12 wording said decision 5A was
"fixed". Fixed at HEAD c42325a: the line-oriented text scan is replaced by
an AST, type-light scanner (v4/go-gate/main.go, stdlib only) that parses
every production file - build tags, line wrapping, comments, aliases,
and file names are irrelevant to the token stream - syntactically taints
`*os.File` values (declarations, parameters, os.Open*/os.Create
producers, same-package constructors, struct fields), bans 37
content-transfer imports and 56 selector families, permits `*os.File`
values only into the mapping-lifecycle methods
(Fd/Close/Name/Stat/Sync/Truncate/Chmod/Chown) and
same-package/module-internal/x-sys consumers, and exempts the three
exact in-memory inflater nodes only when their receiver/arguments are
temporary directory: it never touches the reviewed tree, reserves no
reproducers of the sixth review; the startup sweep is gone. HEAD
81ca524 then pinned the aliased-os producer form as the forty-first;
HEAD 6b05801 tainted `*os.File` results returned by same-package
accessor methods. The seventh-sweep hardening (HEAD e2dc7e0) closed
the type-alias conversion and parameter classes, separately built
`os.ProcAttr{Files}` containers, and the `os.Pipe` producer class,
struct-field-storage and channel-transport classes behind the inflater
exemptions (shared per-package taint state, struct-field write taint,
chan *os.File taint including send/recv/range, new(T) instances,
ninth sweep (HEAD ddc5f9c) closed the inline-FuncLit, type-assertion,
two-hop-channel, and single-variable-channel-range escape classes
(forms 50-53, with the benign control at form 49); the durable
(HEAD 5c88ba3) closed the parenthesized-producer,
parenthesized-closure, interface-typed-closure,
alias-typed-function-variable, and type-switch-bound escape classes
(forms 54-58, with the parenthesized benign control at form 59); the
stress-testing the round-4 fixes during the round-5 gate re-review,
the defined-func-type family and its method/nested-callee variants
chan-of-func/asserted-func/os-std-handle family (forms 68-72), and
the round-6 nested-field/named-helper/chan-pass family, the
named-method extension, the nested-method-receiver extension, the
method-value family, the generic pass-through family, the
generic-element family, the chan-result method-value class, and the
field-assignment class, the channel-consumer class, the
container-element class (forms 73-107), the anonymous-receiver
method class (forms 108-111), the alias-receiver method
class (forms 113-114), the receiver-resolution
class (forms 116-119), the pointer-defined-type
class (forms 121), the indexed-receiver
class (forms 123-125), the element-receiver
class (forms 127-132), the range-literal-receiver
class (forms 134-135), the bound-receiver
class (forms 137-138), the call-result-binding
class (forms 140-143), the explicit-instantiation and
interface-binding class (forms 145-148), the
generic-receiver-binding class (forms 151-156), the
alias-spelled generic binding class (forms 159-164), and the
reader-shape binding class (forms 167-174), and the renamed-qualified alias
  class (forms 179-182), and the func-typed
generic-method class (forms 185-189), the mixed result and
qualified-defined class (forms 191-196), the
interface-method and method-result class (forms 199-205), the
embedded-interface and cross-package chain class (forms 207-210), the
remote-interface and generic-instantiation class (forms 213-217), the
defined-hop instantiation class (forms 222-223), the nested generic-
instantiation class (forms 225-226), and the cgo-import,
raw-syscall, linkname, no-error syscall and preadv2/pwritev2 classes
(forms 228-230, 232-235) with the benign lifecycle control (form
231); the durable rejection set is now two hundred thirty-eight
same-module cross-package producer-var class, forms 257-260;
round-49 closed the nested-parenthesized, renamed-import,
alias-over-renamed, wrapper-promoted method-expression,
value-bound cross-package producer-var, and
interface-conversion-launder class, forms 261-266;
round-50 closed the generic interface-erasure, composite-literal
field-launder, generic-wrapper method-expression, and deep
embedding-chain class, forms 267-270, note that forms 267-268 were
initially vacuous (separate-file launders rejected by the
unconditional selector ban) and were converted to exemption-shape
appends in round 51);
round-51 closed the file-bound generic result-erasure class
(all declared result positions of a generic call with a file-typed
argument binding a type parameter stay tainted: renamed-qualifier
interfaces, other-stdlib interfaces, slice/array/map/chan/func
wrappers) and the positional composite-literal field-launder class
(unkeyed elements resolve through the struct's declared field
order, including an os.Root opener hidden behind a positional
field), forms 271-277 plus the converted 267-268, raising the set
round-52 closed the embedded and anonymous struct-literal launder
classes (embedded fields are named by type name, not qualifier
spelling, so positional io.Reader and positional/keyed *os.Root
wrapper literals no longer leave a live promoted handle; variables
bound to explicit generic instantiations keep the base generic's
substituted results; anonymous struct literals resolve positional
order from their own fields, and anonymous structs embedding
*os.File/*os.Root escalate like named wrappers), forms 278-282, and
the continuation class of the same round (elided inner composite
literals in slice, map, nested-container, and channel elements,
pointer composite literals, and func-valued arguments to explicitly
instantiated generics), forms 283-288, raising the set to two
import and //go:embed directive classes (compile-time database
copies), forms 289-290, raising the set to two hundred forty
round-45 closed the mmap-gate denylist gaps (os.CopyFS directory copies, os.OpenInRoot/os.OpenRoot handles reaching stream wrappers, the x/sys descriptor-transfer primitives Tee/Vmsplice/IoctlFileClone*/Clonefile*, *os.Root laundering through fields/params/helpers, file-method values, and func-typed variables with file-bearing declared results or stdlib producer initializers, forms 249-256; round-36 closed the dup/exec subprocess escape and the
bodyless assembly-stub class, forms 236-237; its follow-up closed the
x/sys-owner boundary for every package plus assembly-object files, forms
238-239; round-38 closed the fcntl F_DUPFD descriptor duplication
primitive, form 240; round-39 closed the out-of-tree module-graph
escape (go.mod replace and go.work can attach code the walk never
scans; the graph is validated to exactly this module plus x/sys with
no workspace, forms 241-242; round-40 closed the x/sys source
replacement and hidden dot-directory vectors, forms 243-244; round-42 closed the x/sys source-content gap, forms 245-247; round-43 closed the fail-open listing gap, form 248). The round-24 gate re-review then found the import-renamed qualified
alias class: an import mm ".../internal/mapping" local qualifier was
never translated back to a package path, so mm.MappingFile generic type
arguments, local alias chains of renamed imports, element spellings,
declared variables, and type assertions all escaped with gate exit 0.
Fixed in the go-gate scanner with per-directory alias registration
(pkgAliasesByDir), a per-file import snapshot (currentImports), and
179-182 (rejects) and 183-184 (benign controls). The durable rejection
of this pass complete the trail up to this re-review. The round-25
gate re-review then found the func-typed generic-method class: a
generic method whose type argument binds a func type producing
*os.File (gRZ[func() *os.File]{}.mk() bound to f, then f()) was
claimed by producerCall as a direct file result, so the binding was
recorded as a file instead of a func-file and invoking it lost the
taint - a file-backed zr again slipped through the io.ReadFull
exemption with gate exit 0. Fixed by removing the funcTextFile claim
from producerCall's generic-method branch so classify's own generic
method loop yields kindFuncFile and applyKind records the func-file
bytes control). The durable rejection
of this pass complete the trail up to this re-review. The round-26
gate re-review then found the mixed-result and qualified-defined
class: (1) callResultsFuncFile required every declared result of a
non-generic function to be a func-file, so mixed multi-result calls
(getFn() (func() *os.File, error)) lost the func-file taint at the
exact func-typed position and f() reached the io.ReadFull exemption
with gate exit 0; (2) a defined type over a qualified or complex
underlying (type x mm.A, type x []*os.File) registered nothing, so
the chain never expanded, and cross-package defined func types
(type F func() *os.File in mapping) were invisible to qualified
references. Fixed with per-position result-kind resolution
(callResults/callResultKinds/callResultKindAt routed through
generic and declared signatures), per-position carrier registration
in applyLHSMulti for call RHS, definedTo registration for every
non-func non-ident underlying, and qualified registration of defined
(benign bytes controls). The durable rejection
of this pass complete the trail up to this re-review. The round-27
gate re-review then found the interface-method and method-result
class: (1) a generic receiver bound to an interface whose method
declares mixed results (Get() (func() *os.File, error)) lost the
func-file position because producerCall claimed the interface method
signature (stored as a pseudo-field) as a raw file position, so the
binding was recorded as a file and invoking it lost the taint;
(2) callResults dropped every declared result of a non-generic method
because methodMeta's ok flag reports body-marked producers, not
whether the method exists - mixed method results (mk() (func() *os.File,
error)) bound with gate exit 0; (3) defined types over aliases and
aliases over defined func types (type D A with A = func() *os.File;
type E = D2) were invisible to cross-package spellings because only
aliases and defined func types entered the qualified registries, and a
first-hop name from another directory could not resolve further.
Fixed with declared-result precedence in classify and per-position
kind preference in applyLHSMulti (func-file/chan carriers keep their
invoke-able kind), method-existence detection in producerCall's
field-type claim (a declared method is not a func field),
non-nil-results acceptance in callResults for ordinary methods, and
defined-type registration plus a per-directory fixpoint in the
199-205 (rejects covering both mixed positions, the chan-of-func
variant, the defined-over-alias and alias-over-defined hops, and both
method-result positions) and 206 (benign interface-typed bytes
control). The durable rejection
of this pass complete the trail up to this re-review. The round-28
gate re-review then found the embedded-interface and cross-package
chain class: (1) an interface embedding a file-producing interface
(type IEmb interface{ IBase } with IBase.Get() func() *os.File)
resolved no promoted method because methodMeta's embedded walk
propagated only body-marked producers and dropped declared results,
so x.Get()() on an interface-typed generic result reached the
io.ReadFull exemption with gate exit 0 (both the single-result and
the mixed multi-result shapes); (2) a defined struct in another
package used as a generic type argument (gRZ[mm.S28]{}, s.Get())
was invisible to the reader's local package info; (3) a qualified
defined chain of nine named hops (mm.J28 after alias A and hops
B..I) exceeded the single-pass fixpoint budget and was map-order
dependent. Fixed by propagating promoted declared results in
methodMeta's embedding walk, process-wide mirrors of remote
structs/methods/embedded chains with a parse-time seed-merge,
self-entries for struct spellings in the qualified registries, and
a full fixpoint loop for the per-directory alias/defined closure
and 211-212 (benign bytes controls). The durable rejection
of this pass complete the trail up to this re-review. The round-29
gate re-review then found the remote-interface and generic-
instantiation class: (1) a renamed import qualifier on a cross-
package interface embedded in a reader interface or struct (type
IEmb interface{ mm.IMapBase }) reduced to no registered key because
only structs (not interfaces) registered the qualifier self-entry,
so the promoted file method lost its taint; (2) a generic interface
instantiated at an embedding site (type IEmbGN interface{
IBaseGN[func() *os.File] }) promoted the raw type parameter without
substitution, so Get() T never matched the file shapes (both the
func-file and chan-of-func variants); the adjacent remote shapes (a
renamed generic interface instantiation and a cross-package generic
struct receiver) carried the same gap. Fixed by registering the
qualified self-entry for interface names exactly like structs,
recording generic interface type parameters in the receiver-parameter
registry with process-wide mirrors, and substituting the embedding's
type arguments in the promoted-method walk; the round's P2 (a
non-compiling benign form-212 twin) was fixed by removing its unused
(benign bytes controls). The durable rejection
of this pass complete the trail up to this re-review. The round-30
gate re-review then found the defined-hop instantiation class: a
defined type over an instantiated generic interface embedded in an
interface (type D IBaseG[func() *os.File]; type IEmb interface{ D })
lost the instantiation at the embedding walk because the brackets
live in the defined chain's target text, not in the raw embedded
spelling, so the promoted method results propagated the raw type
parameter and Get() T never matched the file shapes (both the
reader-local and the renamed-qualified cross-package shapes). Fixed
by extracting the embedding's type arguments from the resolved
defined/alias text (resolveTaintType then parseBracketArgs) in both
the promoted-method walk and the generic receiver-substitution walk;
control). The durable rejection
of this pass complete the trail up to this re-review. The round-31
gate re-review then found the nested generic-instantiation class:
a multi-level generic-interface embedding (type InnerL[T]
interface{ Get() T }; type IBaseGL[T] interface{ InnerL[T] };
type IEmb interface{ IBaseGL[func() *os.File] }) substituted only
at the frame owning the brackets, so the frame declaring the method
returned the raw type parameter and Get() T reached the io.ReadFull
exemption (three-level and chan-of-func variants included). Fixed by
threading type parameters and arguments down the embedding chain:
each frame substitutes its own instantiation into the next embedded
type text, the declaring frame applies the accumulated arguments,
and a frame-level interface-parameter registry (ifaceParams, mirrored
process-wide) carries generic interface parameters across packages;
the receiver-substitution walk gained the same threading and the
(rejects) and 227 (benign bytes control); forms 228-230 and 232-235 pin the
round-32 cgo-import, raw-syscall, linkname, no-error syscall, and
preadv2/pwritev2 rejects with form 231 the benign lifecycle
control. The durable rejection set is now one hundred eighty-five
subprocess-escape class: dup'ing the database descriptor onto stdin and
exec'ing a reader (unix.Dup2 + unix.Exec, /bin/cat) streams file content
out with no banned read call, and a bodyless Go declaration attaches an
assembly syscall body the AST scan cannot see. Fixed by banning the
Dup/Dup2/Dup3/Exec/ForkExec selectors and rejecting bodyless
follow-up re-review then closed the remaining owner-boundary gaps: a new
package could import golang.org/x/sys unseen by the per-target loop
(only the four known packages were checked), and a .s/.syso assembly
object was never scanned (the bodyless-declaration ban was the only link
guard). Fixed by moving the x/sys owner rule into the per-target loop
for every package except internal/mapping and rejecting assembly objects
round-37 narrow re-review then found a metadata parity gap: ReadMetadataJSON
accepted a metadata chunk page whose post-data tail bytes were nonzero,
while Rust rejects it as corrupt (metadata.rs:274) and the spec requires
zero tails (binary-format-v4.md:1051). Fixed with an explicit tail-zero
check in internal/reader/metadata.go and the pre-fix-failing regression
pin TestMetadataChunkTailNonzeroRejected. The round-38 re-review then
closed the fcntl F_DUPFD duplication primitive: unix.FcntlInt(fd,
F_DUPFD, 0) can duplicate the descriptor onto stdin like dup, unseen by
the dup-name bans; FcntlInt joins the banned selector set (FcntlFlock,
the mapping owner's lock path, is a different function and stays
module-graph escape: a go.mod replace directive or a go.work workspace
can attach an out-of-tree module whose files the scanner walk never
visits, letting a wrapper call unix.Pread on the database descriptor
unseen (both vectors exited 0). Fixed by validating the module graph
itself - go list -m all must be exactly this module plus
forms 241-242, raising the set to one hundred ninety-two mutation
forms. The round-40 re-review then found the path-only allowlist gap:
replace golang.org/x/sys => <evil dir> keeps the allowed path in the
graph while loading attacker-controlled code the walk never scans
(proven live with unix.Pread2 reading the database), and the walk
skipped every hidden dot-directory, hiding in-tree replacements. Fixed
by banning all replace/exclude directives, verifying the resolved
x/sys source is the module-cache checkout, and scanning hidden
gate re-review then closed the x/sys source-content gap: the path-only
allowlist accepted a poisoned GOMODCACHE checkout or a file proxy
serving an evil x/sys with a self-consistent forged go.sum (both proven
live with a smuggled unix.Pread2 the ban list cannot know, because
nothing pinned the module content); the gate now pins the exact version,
the module-cache path, the extracted-tree content hash, and the module
zip/go.mod sums to the official v0.35.0 values, and the assembly-object
gate re-review then found the fail-open listing gap: the per-target go
list ./... loop swallowed listing failures (2>/dev/null), so a module
the go toolchain cannot list - symlinked package files or parse errors -
passed with an empty package list and no import checks (reproduced with
a symlinked smuggled file in internal/mapping); the package checks now
fail closed on every listing error and the per-package import listing
pass for that fix completed with all six narrow reviewers at PASS (HEAD e5fea20).
The round-45 final review then failed with three P2 findings, all in the mmap source gate: os.CopyFS was absent
from the selector ban (a directory copy streams artifact bytes with no banned selector; the live reproducer
exited 0), os.OpenInRoot/os.OpenRoot were absent from the file-producer table (a Go 1.26 OpenInRoot *os.File,
or an older-toolchain *os.Root handle, reached flate.NewReader untainted and streamed file bytes, and
Root.Open/Create/OpenFile also produce files), and the blanket-approved x/sys surface still carried
descriptor-transfer primitives unseen by the denylist (unix.Tee, unix.Vmsplice, unix.IoctlFileClone/CloneRange/
DedupeRange, darwin unix.Clonefile/Clonefileat). Fixed at HEAD 14c0698: CopyFS, Tee, Vmsplice,
IoctlFileClone* and Clonefile* join the banned selector set (CopyFileRange/Sendfile/Splice were already
banned); os.OpenInRoot and os.OpenRoot join the file-producer table as position-0 file taints, so every Root
method outside the approved lifecycle surface fails closed; all three live reproducers plus the
OpenRoot/ReadAll and darwin Clonefile variants are rejected by the hardened gate. The adversarial re-review
of that fix then found a P0 in the same class: a *os.Root handle stored in a struct field (h :=
gateRootField{r: root}; h.r.Open(name)) dropped the file taint, so the returned *os.File reached
flate.NewReader untainted and the stream was consumed through the exact inflater exemption shape with gate
exit 0; *os.Root now resolves as a file-bearing type everywhere *os.File does (fields, parameters,
helper returns, type assertions, func/chan elements, results), so every laundering route fails closed.
adversarial re-review of that closure then found three P0 escapes in the same producer-value class, all
proven live with full metadata-exemption chains at gate exit 0: (1) a file method value (open := root.Open;
open(name)) escaped the call-receiver ban and the bound method produced an untainted *os.File; (2) a
func-typed variable with an initializer (var newRoot func(string) (*os.Root, error) = os.OpenRoot) lost
its declared file-bearing result type because the type was only consulted for type-only vars; (3) the same
declared-type gap predated the Root work for *os.File (var openPath func(string) (*os.File, error) =
os.Open). Fixed by checking the file method in value position against the approved surface, registering the
declared result type of initialized func-typed variables, and registering stdlib producer values (os.Open
closed bound method expressions on file-bearing receiver types and
same-module cross-package producer vars (forms 257-260, two hundred
parenthesized, renamed-import, alias-over-renamed, and
wrapper-promoted method expressions, value-bound cross-package
producer vars, and interface-conversion laundering of a file into
the metadata inflater exemption (forms 261-266, two hundred sixteen
identity-with-interface-result erasure, the composite-literal field
launder, the instantiated-generic-wrapper method expression, and
the deep embedding-chain method expression (forms 267-270, two
of this pass complete the trail up to this re-review. Repository counts:
production 4,789 raw lines / tests 4,887 raw lines (the round-54
dead-state removal and zero-Pin test account for the latest delta; the gate
scanner lives outside the module). Milestone 2 must not start until a
new independent final review passes; decision 5A was ratified (option A,
2026-08-13); no open user decision remains.
The approved later scope remains unchanged: Milestone 2 is the writer;
sidecars, live coordination, and publication remain Milestone 4.


### Status (2026-08-23) - chunk 4-2 implemented: worker SIGBUS spike, linux/amd64

Chunk 4-2 delivered on the working tree (design + probe entries above).
New package v4/go/internal/worker (leaf; 4-11 worker binary and 4-12
platform proofs consume it):

- control_common.go: portable fault-subset constants and types
  (controlLen 1 MiB, altStackLen 64 KiB, ownedFaultExit 197, protocol,
  states, faultMarker, exact Rust offsets, MappingRole 1..=4,
  roleFromWire, FaultRecord).
- control.go (linux && amd64): Control with CreateParent (random
  16-byte nonce, os.TempDir() .iprange-v4-worker-<hex>.ctl, 0600
  O_EXCL, truncate to 1 MiB, mapping.MapFile read-write, clear +
  magic + protocol + state=Request), OpenWorker (exact 1 MiB extent,
  CodeFormatInvalid otherwise), Arm (handling=0, generation, role,
  base, len, armed=1; empty probe -> CodeInvalidArgument;
  wire-invalid role outside 1..=4 -> CodeInvalidArgument, restoring
  Rust's MappingRole enum invariant the enum makes unrepresentable), Disarm,
  FaultRecord (every Rust cross-check incl. base.checked_add overflow
  parity), RemovePath, Close, base/altStack/state. The control file is
  a mapped coordination artifact exactly like the 4-1 sidecar: no
  read/write syscalls, only mapping views; scalar fields via
  format.U64/PutU64, atomic fields via mapped LOCK-prefixed asm
  primitives (design probe item 3).
- control_other.go (!linux || !amd64): typed-refusal stub (mapping
  owner pattern; CodeOSUnsupported "worker SIGBUS isolation is not
  implemented on this platform") so the six cross-compiles stay green;
  Darwin/FreeBSD native proof lands in 4-12.
- sigbus_linux_amd64.go + .s: project-owned naked SIGBUS handler and
  raw-syscall shim (rt_sigaction 13, rt_sigprocmask 14, rt_sigreturn
  15, kill 62, getpid 39, rt_sigsuspend 130, sigaltstack 131,
  exit_group 231). Handler mirrors posix.rs signal_handler +
  owned_fault + chain exactly: owned-fault gate (signal 7, info
  non-null, si_code 1..=5, armed 1, generation non-zero, role 1..=4,
  len non-zero, relative<len, handling CAS 0->1), fault-record writes,
  state=Fault, exit 197; chain restores the previous disposition
  (SA_RESETHAND arm restores a zeroed SIG_DFL like Rust), applies the
  kernel-equivalent mask (current OR previous bits 0..=63 (signals 1..=64), SIGBUS
  re-added unless SA_NODEFER, chain_mask), then TAIL-JUMPS to the
  previous handler with the original C ABI registers (DI/SI/DX
  restored from R12/R13/R14) so the kernel frame stays intact;
  SIG_DFL synchronous faults return to re-execute under the default
  disposition, asynchronous redispatch via kill + sigsuspend;
  SIG_IGN restore + return. Install/VerifyOwned/Close mirror
  posix.rs Handler::install/verify_owned/Drop (conflict check,
  sigaltstack capture + install, previous-action capture published
  before install, SA_SIGINFO|SA_ONSTACK|SA_RESTORER with the project
  rt_sigreturn stub, ACTIVE_CONTROL CAS, verify; Close disarms,
  restores the action only if still ours, restores the stack only if
  still ours). runtime.LockOSThread is required of callers because Go
  migrates goroutines and the alternate stack is per-thread (design
  probe item 2).
- Mapped-control atomics (design probe item 3): Go-callable asm
  LOCK-prefixed mapAtomicLoad32/Store32 over base+offset, used by
  both the Go control code and the naked handler (Go sync/atomic is
  specified for Go-managed memory only).
- Tests: control_test.go (plain build; 13 unit tests: header, unique
  paths, umask-independent 0600 mode, Close/RemovePath (2),
  exact-extent open, wrong-extent/missing refusals (2), arm/disarm
  field verification, empty-probe rejection, invalid-role rejection,
  fault-record pre-fault conflict, parent/worker shared armed state),
  sigbus_linux_amd64_test.go (plain build; owned-fault record
  survives worker exit with the exact Rust record values, and the
  Go-specific chaining proof: an unarmed fault chains into the Go
  runtime's own SIGBUS handler with the kernel frame intact - child
  exits via the runtime fatal, asserted != 0/87/197, no hang),
  sigbus_matrix_v4work.s + sigbus_matrix_v4work_test.go (v4work
  build; the 15-case previous-disposition matrix with naked matrix
  handlers: owned 197, user-one 81, user-siginfo 82, user-mask 88,
  user-nodefer 89, user-reset 90, captured-reset 92, unarmed 83,
  out-of-region 83, stale-region 83, nested 83, null-info 86,
  replacement 91, default killed-by-SIGBUS, ignore 84, native-reset
  90; plus matrixCallChainNullInfo entering the handler with the
  null-info shape). The matrix .s carries the v4work AND linux/amd64
  constraints so -tags v4work cross-builds stay green.

Coding-time corrections (all Rust-parity, recorded against the
recorded design):
- Go asm operand order: CMP takes register/memory first and the
  immediate second (runtime convention verified; the probe file in
  /tmp/asmtag), and ANDQ rejects unencodable 64-bit immediates (mask
  loaded via a register).
- Go ABI0 places results at the 8-byte boundary after the last
  argument (mapAtomic* FP offsets fixed after go vet).
- The faulting read must feed an unfoldable branch (if view[0] != 0):
  a dead `_ = view[0]` load is eliminated by the compiler and the
  SIGBUS never fires.
- Tail-jump argument restoration (DI/SI/DX from R12/R13/R14) is
  required before every chain jump: the chained handler must receive
  the original signal/info/context (posix.rs call_action).
- FaultRecord mirrors Rust base.checked_add(relative): an overflow of
  base+relative is a rejection, not a wrapped comparison.

Validation on the working tree, all under nice (expected cost
~2-3 core-minutes, recorded in the design entry): gofmt clean, vet
clean (plain + v4work), plain/v4work module tests, race, race+v4work,
checkptr=2 (plain + v4work), mmap-trace PASS (no read/write on any
.iprdb descriptor; worker control files are mapping-only by
construction and the reviewer gate re-verifies the code evidence),
six cross-compiles (linux/386, linux/arm, linux/arm64, windows/amd64,
darwin/arm64, freebsd/amd64) plain AND -tags v4work. Full battery
wall time ~25 s.

- Next: chunk 4-2 five-aspect review (same five agents, adversarial,
  one level, disjoint aspects, Rust authority baseline), fix
  findings, re-review, then signed commit + push and 4-3.

### Status (2026-08-23) - chunk 4-2 five-aspect review round 1: 1xP2 + P3s fixed, re-review dispatched

Round-1 gate on the 4-2 working tree (five adversarial reviewers, one
level, disjoint aspects, Rust authority baseline): Rust parity PASS,
Go idioms PASS (7xP3), performance FAIL 1xP2 3xP3, wire/integrity PASS
1xP3, APIs/docs/records PASS 1xP2 2xP3. All findings verified against
the Rust authority and fixed on the working tree:

- P2 (performance, real): chain_mask dropped kernel bit 63 (signal 64,
  SIGRTMAX). Rust's apply_mask loops candidates 1..=1023 over the glibc
  sigset and the kernel mask is 64 bits (sigsetsize 8), so signals
  1..=64 (bits 0..=63) all propagate; the Go shortcut constant
  0x7fffffffffffffff cleared bit 63. Fixed: MOVQ $-1 keeps every
  kernel bit; asm comment now cites the loop range and the 64-bit
  kernel mask.
- P2 (records, real): the design-probe claim "_test.s files compile
  into production builds (verified)" was disproven by a counterexample
  (symbol defined only in *_test.s is undefined in go build; the file
  feeds only the symbol-ABI pass). The v4work-tag conclusion was
  correct on the verified half (build constraints in assembly are
  honored) and stands; the record now states the verified premise.
- P3 (performance): fault-record write re-loaded generation/role after
  the gate (Rust keeps locals and reuses the gate registers); the
  handler now holds them in R9/R11 from the gate and writes the record
  with zero reloads. ACTIVE_CONTROL unpublish is now a single store
  like Rust's codegen. Dead mapAtomicCAS32 wrapper deleted (the
  handler's inline LOCK CMPXCHG is the only CAS; Rust's fault subset
  has no such primitive either).
- P3 (wire): Arm accepted any MappingRole value (Rust's enum makes
  invalid roles unrepresentable); Arm now rejects wire-invalid roles
  with CodeInvalidArgument and a regression test covers roles 0 and 5.
- P3 (Go idioms): wantCode now uses errors.As like the repo's
  canonical expectCode; FaultRecord reads state through the
  mapAtomicLoad32 primitive (state is an atomic_u32 field in Rust,
  written by the handler); Arm documents its TSO ordering on the Go
  side; the mapAtomic comment now states the real drivers (the naked
  handler cannot call Go code; sync/atomic is not specified for mapped
  memory); package doc names the future cmd/iprange-v4-worker binary
  instead of SOW chunk labels.
- P3 (records): control_test.go count corrected to 13 (the umask-mode test and
  the invalid-role test enumerated); the design record's mask wording now
  says 64-bit kernel sigset / signals 1..=64 instead of "1024-bit".

Also fixed during the gate (found by the lead while verifying parity
evidence): CreateParent now fchmods 0600 independent of umask
(secure_creator_only core, Rust control.rs create_file + security), so
a restrictive umask can never make the control file unopenable by the
worker; regression test TestCreateParentModeIndependentOfUmask.

Validation after fixes, all under nice: gofmt clean, vet clean
(plain + v4work), plain/v4work tests (matrix 16/16), race,
race+v4work, checkptr=2 (plain + v4work), mmap-trace PASS, six
cross-compiles plain AND -tags v4work. Re-review of the fixed tree
dispatched to the same five reviewers.

### Status (2026-08-23) - chunk 4-2 CLOSED: five-aspect re-review PASS on the final working tree

Round-2 re-review on the fixed tree, same five reviewers (one level,
disjoint aspects, Rust authority baseline): Rust parity PASS (two P3
nits - stale 1..=63 asm header comment and the Arm role guard not yet
recorded in the design record, both fixed here), Go idioms PASS,
performance PASS with three P3s (all fixed here), wire/integrity PASS,
APIs/docs/records PASS with three P3 record nits (all fixed here).

Round-2 fixes on the final tree:

- P3 (performance, real): chain_mask's AND with -1 was a dead
  identity after the round-1 bit-63 fix (the previous mask loaded from
  previousAction+24 already holds the full 64-bit kernel sigset); the
  two-instruction MOVQ $-1 + ANDQ were deleted, and the stale "signals
  1..=63" doc comments in the .s header and chain_mask body now say
  signals 1..=64 (bits 0..=63).
- P3 (performance): the owned-fault record write no longer re-reads
  si_code from the siginfo mapping; the gate keeps it in SI from entry
  to the record write exactly like Rust holds it in ESI (posix.rs
  release codegen), and len is loaded once into CX at the gate and
  compared register-register like Rust's R10. Two redundant mapped
  loads removed from the owned path; the handler is now at instruction
  parity with the Rust release codegen on every path.
- P3 (records): implemented-entry test count corrected to 13
  (TestArmRejectsInvalidRole belongs to the round-1 fix batch);
  implemented-entry mask phrase corrected to bits 0..=63 (signals
  1..=64); stale mapAtomicLoad32/Store32/CAS32 record corrected to
  Load32/Store32 (the CAS wrapper was deleted in round 1); the Arm
  role guard is now recorded in the design record as a deliberate
  restoration of Rust's MappingRole enum invariant.

Validation on the final tree, all under nice: gofmt clean, vet clean
(plain + v4work), plain/v4work tests (matrix 16/16), race,
race+v4work, checkptr=2 (plain + v4work), mmap-trace PASS, six
cross-compiles plain AND -tags v4work. Signed commit and push follow
this entry.

- Next: commit + push chunk 4-2 (explicit file list, signed), then
  chunk 4-3 (create/initialize: CreateLive, InitializeLive, creation
  security 0600 + IPR4PSEC commitment surface) per the M4 chunk plan.

### Status (2026-08-23) - chunk 4-3 design recorded: create/initialize + creation security

Chunk 4-3 design, recorded before coding per the pre-implementation
gate. Rust authorities read in full: live_lifecycle/creation.rs (398
lines: create_live, CreateResult/CreationState, Attempt, validate_
destination, validate_kinds, prepare_sidecar, initialize_pair, cleanup,
require_absent, the seven create.* crash points), live_lifecycle/
transition.rs Initialize subset (initialize_live, LockedMain::open/
verify, Attempt, initialize_sidecar, cleanup_created, reservation_
failure, require_capacity, the four live_initialize.* crash points),
publication/security.rs + security/posix.rs + security/linux.rs +
apple.rs + freebsd.rs (CREATOR_MODE 0600, Profile, commitment =
SHA256("IPR4PSEC" || uid_le32 || 0600_le32), secure_creator_only,
creator_only_commitment, Linux xattr ACL removal/proof, darwin/freebsd
libc ACL machines), live_cleanup.rs POSIX subset (Outcome, TerminalFacts,
Authority, unique_attempt_id, remove, require_available), live_namespace.rs
(parent_identity, path_identity, public_identity, create_private with the
full secure_creator_only), live_writer.rs LocalBasename + result.rs,
database_file.rs (EmptySpec::live, empty_meta, write_empty,
bootstrap_file, require_sidecar_absent, require_regular_file),
random.rs (nonzero_128), live_writer/create.rs public adapter,
iprange-capi lifecycle_ops.rs + abi_extra.rs (CreateReport,
LiveTransitionReport wire shapes), tests/live_transitions.rs and
tests/live_roundtrip.rs create/init cases.

Design decisions (all Rust-parity; the M4 chunk plan already scoped
this slice):

- internal/live gains two exported lifecycle operations mirroring the
  Rust library API: CreateLive (creation.rs create_live exact:
  capacity>0, destination + canonical-sidecar absence, kinds
  validation, three nonzero_128 draws re-bound through
  unique_attempt_id, parent identity, sidecar reserve -> initialize
  creating -> parent sync -> require-absent, create_private main ->
  write_empty -> parent sync -> publish_ready -> parent sync, with
  the seven create.* crash points and the exact cleanup cascade
  ordered by ordinal) and InitializeLive (transition.rs initialize_live
  exact: LockedMain open under the exclusive lifetime lock with
  Writer-mode bootstrap and the exact-committed-length rule, sidecar
  absence, reserve -> initialize creating -> parent sync -> main
  verify -> publish_ready -> parent sync, with the four
  live_initialize.* crash points and cleanup_created). Result types
  carry the full Rust field surface (CreateResult/CreationState,
  LiveTransitionResult/LiveTransitionOperation/LiveTransitionStatus/
  LiveCoordinationLocation/LiveResetPolicy, Housekeeping, LocalBasename,
  identities). Reset (4-6) is out of this slice; the LiveResetPolicy
  enum is defined now only because LiveTransitionResult carries it.
- Creation security surface (POSIX): new internal/security package
  (the single authority for secure_creator_only, mirroring Rust
  publication/security.rs): Profile capture (geteuid), SHA-256
  commitment over the IPR4PSEC domain + uid + 0600, fchmod 0600, Linux
  inherited-ACL removal/proof via system.posix_acl_access xattr
  (Fremovexattr/Fgetxattr, ENODATA/EOPNOTSUPP tolerated), the
  single-link + mode + uid commitment proof. Error classes follow
  Rust problem.rs: AccessPolicy -> CodeAccessPolicyUnsupported,
  Unsupported -> CodeDurabilityUnsupported, IoAt -> CodeIO.
  internal/live.createPrivate and internal/worker.CreateParent (the
  4-2 fchmod-only core, recorded as deferred) both switch to the
  shared surface; the worker maps security failures to CodeConflict
  "worker control access policy could not be established" exactly
  like Rust worker/control.rs namespace_error. Worker CreateParent
  also switches its nonce draw to random.Nonzero128 (Rust create_
  parent parity; the 4-2 draw lacked the all-zero check).
- Darwin/freebsd ACL machines: pure Go has no filesec/acl_* libc
  bindings in x/sys (verified v0.35.0: no darwin filesec, no freebsd
  acl_*), and Decision 2A forbids cgo. The Go security surface gives
  darwin (where live locks work) an honest typed refusal
  (CodeOSUnsupported "creator-only access policy requires libc ACL
  APIs unavailable to pure Go"), so darwin CreateLive/InitializeLive
  fail cleanly instead of silently weakening the creator-only proof;
  freebsd is already refused earlier by lock_refuse (live locks), and
  the other targets keep the same honest refusal for the unreachable
  path. Tracked for the 4-12 platform proof (recorded follow-up).
- Shared random: new internal/random.Nonzero128 (Rust random.rs exact:
  one CSPRNG fill, all-zero -> CodeFormatInvalid "operating-system
  randomness returned an all-zero identity"); writer randomNonce and
  worker control nonce delegate to it (one authority).
- Public facade: CreateLive and InitializeLive (with the existing
  public CancellationToken checkpoint), the public CreateResult
  extended to the full Rust field set, and new public
  CreationState/Housekeeping/LocalBasename/FileIdentity/
  LiveTransitionResult + transition enums. The M3 CreateResult
  TransactionID field is removed: Rust CreateResult has no such field
  (the M3 type was explicitly documented as a milestone-4 gap; the
  transaction is fixed 1 and observable via Writer.Info), one public
  test assertion updates. Create (immutable main-only convenience)
  keeps its behavior and fills the new fields truthfully
  (state=Created, no identities/basename/sidecar, capacity 0).
- Empty-image codec: internal/live mirrors database_file.rs
  write_empty over the createPrivate descriptor (set_len, read-write
  mapping, both meta pages, flush, sync_all) composing format.Meta
  EncodeMapped; writer/create.go keeps its mapping.Create-based driver
  (different creation flow; consolidation noted for 4-4 when the live
  writer open path lands).
- Validation: live lifecycle unit tests (create success incl. sidecar
  readiness + main bootstrap, capacity 0, kind mismatch, destination
  exists, sidecar exists, missing parent, cleanup after late failure,
  initialize success after sidecar removal, initialize refuses
  existing sidecar, capacity 0, missing main, uncommitted tail, pre-
  cancelled token both ops), security tests (commitment vector, mode
  proof, ACL xattr removal + trivial proof, worker control file
  proof), crash-point state tests via v4work for the create/init
  points (the full crash matrix stays 4-12 per the chunk plan), plus
  the public facade round-trip. All builds/tests under nice.

### Status (2026-08-23) - chunk 4-3 implemented: live create/initialize + creator-only security

Chunk 4-3 delivered on the working tree (design entry above).
New packages:

- v4/go/internal/random (leaf): random.go Nonzero128 - one CSPRNG
  fill, all-zero -> CodeFormatInvalid "operating-system randomness
  returned an all-zero identity" (Rust random.rs exact); writer
  randomNonce (writer/reclaim.go) and worker control nonce
  (internal/worker/control.go CreateParent) now delegate to it, one
  authority.
- v4/go/internal/security (leaf; the single authority for Rust
  publication/security.rs): Profile capture (geteuid), SHA-256
  commitment over "IPR4PSEC" || uid_le32 || 0600_le32,
  SecureCreatorOnly (fchmod 0600; Linux system.posix_acl_access
  xattr removal/proof via Fremovexattr/Fgetxattr with ENODATA/
  EOPNOTSUPP tolerated; single-link + mode + uid proof),
  CreatorOnlyCommitment. Error classes follow Rust problem.rs:
  AccessPolicy -> CodeAccessPolicyUnsupported, Unsupported ->
  CodeDurabilityUnsupported, IoAt -> CodeIO. acl_linux.go keeps the
  xattr machine; acl_darwin.go/acl_other.go give an honest typed
  refusal (CodeOSUnsupported, no libc ACL in pure Go, Decision 2A
  forbids cgo); security_windows.go stub keeps the six cross-
  compiles green.
- v4/go/internal/live lifecycle surface: lifecycle_create.go
  CreateLive (creation.rs create_live exact flow: capacity > 0,
  destination + canonical-sidecar absence, kinds validation, three
  nonzero_128 draws re-bound through unique_attempt_id, parent
  identity, sidecar reserve -> initialize creating -> parent sync ->
  require-absent, create_private main -> write_empty -> parent sync
  -> publish_ready -> parent sync, the seven create.* crash points,
  and the cleanup cascade ordered by ordinal), lifecycle_initialize.go
  InitializeLive (transition.rs initialize_live exact: LockedMain
  open under the exclusive lifetime lock with Writer-mode bootstrap
  and the exact-committed-length rule, sidecar absence, reserve ->
  initialize creating -> parent sync -> main verify -> publish_ready
  -> parent sync, the four live_initialize.* crash points,
  cleanup_created). Full Rust result surfaces: CreateResult/
  CreationState, LiveTransitionResult/LiveTransitionOperation/
  LiveTransitionStatus/LiveCoordinationLocation/LiveResetPolicy
  (reset itself stays 4-6), Housekeeping (housekeeping.go),
  LocalBasename (basename.go; max 512, encoding 1), FileIdentity
  (namespace identity encode: dev le64, inode le64, zeros, kind 1),
  main_empty.go (emptySpec/emptyMeta/writeEmpty over the
  createPrivate descriptor: set_len, read-write mapping, both meta
  pages, flush, sync_all, composing format.Meta EncodeMapped).
  cleanup.go extended (TerminalFacts, absorb, uniqueAttemptID,
  require_available); namespace.go restructured with
  namespace_types.go shared result types and namespace_windows.go
  stubs; createPrivate now returns Rust-exact (createdPrivate,
  *privateCreationFailure) with cause+cleanup+identity facts (main
  identity preserved through private failure, sidecar removal only
  when main cleanup clean); reserve/reserveAt carry
  *privateCreationFailure; internal/worker.CreateParent switches to
  random.Nonzero128 and the shared security surface, mapping
  security failures to CodeConflict "worker control access policy
  could not be established" exactly like Rust worker/control.rs
  namespace_error.
- Public facade v4/go/lifecycle_public.go: CreateLive and
  InitializeLive with the CancellationToken checkpoint; the public
  CreateResult extended to the full Rust field set (M3
  TransactionID removed - Rust CreateResult has no such field; the
  transaction is fixed 1 via Writer.Info), public CreationState/
  Housekeeping/LocalBasename/FileIdentity/LiveTransitionResult +
  transition enums. Create (immutable convenience) fills the new
  fields truthfully (state=Created, capacity 0, no live surface).

Coding-time corrections (all Rust-parity, recorded against the
recorded design):
- publicIdentity used *unix.Stat_t while os.FileInfo.Sys() returns
  *syscall.Stat_t: the identity bytes were all zeros on Linux.
  Fixed to syscall.Stat_t (matches mapping.RegularLinkCount
  pattern); lifecycle_test.go asserts the identity is non-zero and
  stable across the two meta pages.
- parentIdentity reported CodeNameNotFound for a missing parent;
  Rust parent_identity maps NamespaceError::Missing ->
  Error::Io(NotFound) -> CodeIO. Fixed; TestCreateLiveMissingParentIs
  NotCreatedWithoutResidue pins CodeIO with no residue.

Validation on the working tree, all under nice: gofmt clean, vet
clean (plain + v4work), plain/v4work tests, race, race+v4work,
checkptr=2, mmap-trace PASS (no read/write on any .iprdb or sidecar
descriptor), five cross-compiles (linux/386, linux/arm64,
windows/amd64, darwin/arm64, freebsd/amd64) plain AND -tags v4work.
Lifecycle coverage: create success (bootstrap txn=1 + identical meta
pages + sidecar ready), hard errors, missing parent, initialize
success, existing-sidecar refusal, uncommitted-tail refusal, v4work
crash matrix (7 create points + 4 initialize points, child exit 86,
artifact state verified), public round trip + cancellation, ACL
xattr removal + trivial proof, commitment vector, worker control
proof.

- Next: chunk 4-3 five-aspect review (same five agents, adversarial,
  one level, disjoint aspects, Rust authority baseline), fix
  findings, re-review, then signed commit + push and 4-4 (live
  writer open + commit barrier).

### Status (2026-08-23) - chunk 4-3 five-aspect review round 1: fixes applied

Round-1 review of the chunk 4-3 working tree, same five reviewers
(one level, disjoint aspects, Rust authority baseline). Verdicts:
Rust parity FAIL (2 P1, 3 P2, 5 P3), Go idioms FAIL (1 P2, 10 P3),
performance PASS (2 P3), wire/integrity PASS (1 P3), APIs/docs/records
PASS (5 P3). All findings fixed on the working tree:

P1-1 (parity) - the Go live namespace now runs on the retained
directory machine (new internal/live/directory.go + ns_error.go,
mirroring Rust publication/namespace/unix.rs Directory and
live_namespace::namespace_error): parent opens use O_DIRECTORY|
O_NOFOLLOW, prove the directory, require a durability-approved local
filesystem (Linux f_type whitelist EXT/XFS/BTRFS/F2FS/ZFS/BCACHEFS;
darwin/freebsd MNT_LOCAL; other targets refuse) and a proven name_max
(Linux statfs f_namelen, bsd fpathconf _PC_NAME_MAX), and every name
operation executes against the retained directory descriptor
(fstatat/openat/unlinkat, at_nofollow per target). bindPath mirrors
Rust Path::parent/file_name semantics (single-component paths have
the empty parent whose open reports Missing; "." and ".." report
InvalidArgument). New focused tests: overlong basename (300 bytes) ->
CodeNameInvalid before any syscall; symlinked parent -> CodeIO;
relative single-component create -> CodeIO with no residue.
ForkedHandle (check_creator) is unreachable in Go: the process cannot
fork; recorded here so the parity gap is explicit. filepath.Clean
normalization remains the Go-side path convention (Rust operates on
raw paths); noted as a deliberate Go idiom.

P1-2 / wire P3-1 (parity) - security failures in the live path now
fold through the live namespace_error classes exactly like Rust
create_private (new liveSecurityError in ns_error.go): AccessPolicy
-> CodeWrongState "live file ownership changed", Unsupported ->
CodeDurabilityUnsupported; the CodeAccessPolicyUnsupported problem
class stays with the chunk 4-8 publication resolver surface. Pinned
by TestLiveSecurityErrorMapsToLiveClasses. The security package doc
now records the caller-contextual mapping.

P2-1 (parity) - directory sync EINVAL is the Unsupported class
(DurabilityUnsupported "live file namespace lacks required local
operations") in syncParent and in every removeExact cleanup outcome
(Rust Directory::sync + namespace_error).

P2-2 (parity) - createPrivate enforces the directory name_max before
openat (Directory::create require_name_lengths): an overlong
basename is CodeNameInvalid, reachable through the canonical sidecar
name even when the main basename passes the entry probe (Rust
reachability is identical).

P2-3 (parity, tracked) - darwin creator-only refusal (no pure-Go
filesec ACL machine, Decision 2A) and the Windows live stubs stay as
recorded in the design entry (4-12 / M5); no change in this round.

P3 (parity) - message strings aligned to the Rust authorities:
WrongMode classes all report "live file ownership changed" (identity,
link-count, not-regular, not-directory, access-policy); NameInvalid/
NameExists/NameNotFound report "feed name is invalid"/"feed name
already exists"/"feed name does not exist" (Rust Display payloads,
matching the existing feed_workflow convention); reader-capacity
exhaustion reports "the live reader table has no free slot"; path.go
reports the distinct Rust reserved-prefix and reserved-suffix
details. createPrivate no longer runs the extra post-security
verifyPath (Rust has no such re-check); openRw no longer verifies the
path internally (Rust open_rw binds, opens, and proves the identity
only; the Rust-exact post-open verify stays at LockedMain::open and
the sidecar verify surfaces). Edge classes now match Rust: an ENOENT
after bind is Io (CodeIO), "." is InvalidArgument, a regular-file
parent is Io via the O_DIRECTORY open.

P2 (idioms) - dead test var errNever deleted; type assertions
replaced with errors.As/errors.Is in the crash test, security tests,
and the ACL install helper; nil checked before use in publicIdentity;
the dead basename empty-name branch deleted; commitmentDomain is a
const string; commitment tests gained a hardcoded known-answer
vector for uid 0 (dbf2a75f...); exported security surfaces without
current production callers (CreatorOnlyCommitment) are documented as
consumed by the chunk 4-8 resolver slice.

P3 (performance) - slotOffset and sidecarLength dropped the per-call
64-bit division; the overflow guards stay as provably-dead compare
forms (Rust's checked_mul is LLVM-eliminated for the same uint32
range). openRw's redundant pre-lock lstat is gone with the
verify-path restructure (single post-lock verify, Rust-exact).

P3 (records) - implemented entry cross-compile count corrected to
five; public facade cites live_lifecycle::initialize_live (not
live_writer); the reset note says chunk 4-6; LiveTransitionResult
identity pointers documented as always present; the design entry's
"cleanup after late failure" validation item is now covered
functionally by TestCreateLiveMidFlowCancellationCleansTheSidecar
and TestInitializeLiveMidFlowCancellationCleansTheSidecar (mid-flow
cancellation after sidecar reservation removes the sidecar exactly,
main byte-identical, status Unchanged, no residue), in addition to
the crash-matrix artifact states.

Validation on the working tree, all under nice: gofmt clean, vet
clean (plain + v4work), plain/v4work tests, race, race+v4work,
checkptr=2 (plain + v4work), mmap-trace PASS, five cross-compiles
(linux/386, linux/arm64, windows/amd64, darwin/arm64, freebsd/amd64)
plain AND -tags v4work.

- Next: chunk 4-3 round-2 re-review (same five agents, adversarial,
  one level, disjoint aspects), then signed commit + push and 4-4.

### Status (2026-08-23) - chunk 4-3 five-aspect review round 2: fixes applied

Round-2 re-review of the same five reviewers (one level, disjoint
aspects, adversarial, read-only, Rust authority baseline; focus on the
round-1 diff). Verdicts: Rust parity PASS (6 P3), Go idioms FAIL (1 P2,
4 P3), performance PASS (3 P3), wire/integrity PASS (1 P3),
APIs/docs/records FAIL (1 P2, 2 P3). All P2s and the cheap P3s fixed;
the remaining P3s are deliberate conventions recorded below.

P2 (idioms) - dead Rust-mirror placeholders with zero consumers
deleted: nsAccessPolicyError() and the nsAccessPolicy discriminant
(internal/live/ns_error.go; the wrong-mode kinds still fold to
WrongState via the nsMap/Error defaults) and residueFacts()
(internal/live/cleanup.go; Rust uses it only in reset_live_coordination,
out of this slice - re-added with chunk 4-6).

P2 (records) - the SOW prose said "the five create.* crash points" in
three places; the create crash matrix has seven points (five call
sites in Rust creation.rs plus create.after_sidecar_sync and
create.after_ready_write in live_sidecar.rs), all armed in Go.
Corrected to "the seven create.* crash points" at the implemented
entry, the design entry, and the round-1 summary.

P3 (parity) - freebsd final-symlink classification aligned: openat
ELOOP and (freebsd only) EMLINK now both report the not-regular class
(Rust namespace::is_nofollow_symlink), via the new
nofollow_freebsd.go / nofollow_other.go helpers. combineErrors detail
aligned to Rust Display: "; cleanup also failed: " (sdk_error.rs).
random.go CodeIO detail aligned to Rust Error::Random Display:
"operating-system randomness failed: ".

P3 (idioms) - the hand-rolled hex decoder in the commitment
known-answer test replaced with encoding/hex; the provably-dead
`name == ""` arms (filepath.Base never returns "") dropped from
basename.go, path.go, and namespace.go.

P3 (performance) - dirEntry returned by value from Directory::entry
(the bool reports absence), removing the per-probe heap allocation on
every namespace probe (verifyName, requireAbsent, unlinkExact,
verifyPath, pathIdentity); security.Capture returns Profile by value.
openDirectory returning *directory and pathIdentity returning
*FileIdentity stay: pointer returns are the Go idiom for a machine
holding an *os.File and for the optional-identity surface (Rust
Option<LocalFileIdentity>); both are once-per-operation cold paths,
recorded here as deliberate.

P3 (records) - the public transition-result doc qualified: the
directory and main identities are always present; the new-sidecar
identity is absent when the sidecar was never created; the
previous-sidecar identity is absent on initialize and present on
reset (lifecycle_public.go; Rust carries
Option<LocalFileIdentity>). The internal LiveTransitionResult doc and
the reset slice note now say "out of the current 4-3 slice, scheduled
for chunk 4-6".

Recorded message-parity decisions (class parity is exact; text differs
by design): CodeIO details carry the Go operation prefix (Go
error-wrapping convention; Rust discards the operation in
namespace_error); parentIdentity keeps the stable detail "live parent
directory does not exist" (Rust renders the underlying io::NotFound
message); bindPath edge shapes ("."/".."/"/" and "foo/.." with a
missing parent) are class-identical InvalidArgument and unreachable in
normal use; the worker claimWriter detail "live database writer lease
is held" is pre-existing and off the 4-3 path. sidecar.go at 598 lines
stays as one cohesive file (4-3 delta is small), noted in the ledger.

Pre-existing flake fixed in the same close-out: the direct-workflow
zero-allocation window (assertZeroAllocWindow) measured process-wide
TotalAlloc deltas against a 64-byte bound that documented the
one-time 48-byte and 16-byte runtime cache entries; measurement
windows occasionally caught two 48-byte entries (96 bytes). Proven
pre-existing by reproduction on the committed HEAD tree (3/30
isolated runs, always exactly +2 mallocs of 48 bytes; the writer
path is untouched by this chunk). The bound is now 160 bytes with
the comment recording the observed entry shape; sensitivity is
preserved because a per-record regression allocates ~1.15 KB per
record, far above the window (30/30 isolated runs pass after the
fix).

Validation on the working tree, all under nice with -count=1: gofmt
clean, vet clean (plain + v4work), plain/v4work tests, race,
race+v4work, checkptr=2 (plain + v4work), mmap-trace PASS, five
cross-compiles (linux/386, linux/arm64, windows/amd64, darwin/arm64,
freebsd/amd64) plain AND -tags v4work. All round-2 fixes land before
the final signed commit; chunk 4-3 closes with this entry.

- Next: signed commit + push of chunk 4-3, then chunk 4-4 (live
  writer open + commit barrier).
### Status (2026-08-23) - chunk 4-4 design recorded: live writer open + commit barrier

Chunk 4-4 design, recorded before coding per the pre-implementation
gate. Rust authorities read in full: live_writer.rs (LiveWriter
open/open_main/open_locked, State machine, require_healthy/require_
owner, discard_draft/discard_draft_inner, abort_after/abort_after_
source, unpublished_tail_cleanup, mutate), live_writer/commit.rs
(commit_with, commit_attempt, prepare_and_lock, finish_commit_locked_
with, commit_locked, prepublication_checks, failed_result,
apply_commit_unlock), live_writer/close.rs (close, close_locked,
finish_close, closing_failure, close_failure), live_writer/result.rs
(CommitResult/AbortResult/CloseResult, CommitCleanupArtifact(s),
AbortOutcome/CloseOutcome), publication/types.rs (CoordinationCleanup,
CleanupState), writer_core/publication.rs (publish, outcome_unknown),
writer_core.rs (tail_cleanup_state), and the live crash matrix in
live_crash_tests.rs + tests/live_roundtrip.rs writer cases.

Key decisions:

- The live writer opens the main under the SHARED lifetime lock (Rust
  open_main lock_file(MAIN_LIFETIME_LOCK, Shared)); writer exclusivity
  comes from the sidecar writer claim. The chunk-1 exclusive-lock
  substitution remains only on the immutable Writer/OpenWriter path.
- Open order: requireLiveSupported, checkpoint, OpenWriterLive
  (shared-lock map + bootstrap + remap + terminal identity), identity
  capture (FileIdentity, parentIdentity, publicIdentity, basename),
  verifyPath, Sidecar::open (ready), gate exclusive, verifyPair,
  SelectCommitted, database-id match, scanAtMost, checkpoint,
  claimWriter, checkpoint, TrimCommittedTail, checkpoint, verifyPair,
  unlock gate. Every failure releases the gate, the sidecar, and the
  mapping; require_main_available is a POSIX no-op (live_cleanup.rs)
  and is not called.
- Commit runs the gate-around-Publish barrier: commitAttempt (unchanged
  draft discarded -> NoPendingTransaction), prepareAndLock (Prepare +
  gate exclusive), commitLocked (checkpoint, verifyPair,
  RequireUnchangedBase, scanAtMost, RequireDraftLength, verifyPair,
  checkpoint, Publish), applyCommitUnlock. Fatal classes (IO,
  FormatInvalid - the Go mapping of Rust Io|Format|Corrupt) fail the
  writer closed even when the discard succeeds.
- Close: owner check, idempotent Closed, retryable Closing* states,
  gate exclusive, closeLocked (verifyPair, PrepareClose, non-
  cancellable scanAtMost(plan txn), FinishClose, verifyPair), then
  ClosingWriter -> releaseWriter -> unlockGate -> core.Close (Go
  mapping Close bundles unmap + lifetime unlock, preserving the Rust
  lock release order; unmap lands at the same final step) -> Closed.
  closeFailure matches Rust close_failure exactly (Unusable state,
  abort outcome only when a draft was pending, cleanup ledger from
  TailCleanupState).
- Result surfaces mirror live_writer/result.rs: CommitDurability maps
  to the existing public CommitStatus; public LiveCommitResult/
  LiveAbortResult/LiveCloseResult carry public identities, basenames,
  cleanup ledgers, CoordinationCleanup, and Cause; each result has a
  CleanupState() method (Rust cleanup_state). Public Close keeps the
  writer usable after an incomplete close (retryable, Rust parity).
- Abort uses the Rust discard_draft wrapper: a failed discard fails the
  writer closed and reports AbortIncomplete with the tail ledger.
- Fault points: Rust arms only the four writer-core commit points
  (commit.before_private_sync, commit.after_private_sync,
  commit.after_meta_write, commit.after_meta_sync); all four are
  already armed in Go Core.Publish (fault.Crash + the single
  fault.Fail at after_meta_write). No live_writer-specific points.
- Windows gets the honest refusal stub for OpenMutableShared (the
  mapping owner refuses every open in milestone 1), so the five
  cross-compiles stay green.

- Next: implement + tests (internal live_writer_test.go, public
  live_writer_public_test.go, v4work crash matrix), full battery,
  five-aspect review, signed commit + push.

### Status (2026-08-23) - chunk 4-4 implemented: live writer open + commit barrier

Chunk 4-4 delivered on the working tree (design entry above).

Implementation:

- internal/mapping: openMapping refactored to take the lifetime-lock
  mode as a parameter; OpenMutableShared added (shared lifetime lock
  for the live writer); OpenImmutable/OpenMutable behavior unchanged;
  mapping_windows.go refuses OpenMutableShared like every other open.
- internal/writer: OpenWriterLive (shared-lock variant of the open
  sequence, via the common openWriter helper; OpenWriter behavior
  unchanged); TailCleanupState + Core.TailCleanupState() mirroring Rust
  tail_cleanup_state (greatest of unprovedTailEnd and current file
  length beyond the committed bytes).
- internal/live: sidecar.scanAtMost (non-cancellable close-path
  variant, Rust Sidecar::scan_at_most); live_writer_result.go (the
  durability/outcome/cleanup/coordination facts); live_writer.go (the
  LiveWriter state machine: open, commit barrier, abort, close with
  retryable Closing* states, owner check, discard_draft wrapper).
- Public facade: live_writer_public.go (OpenLiveWriter,
  LiveWriter.Info/BeginDirect/Close, LiveDirectTransaction with
  AssignV4/V6 + ClearV4/V6 + SetMetadataJSON/ClearMetadataJSON,
  Commit/Abort; public result types and mapping helpers;
  LiveCommitCleanupArtifact(s), AbortOutcome, CloseOutcome,
  CoordinationCleanup, CleanupState methods). Immutable-mode Writer/
  OpenWriter/DirectTransaction paths are unchanged.
- Design parity fixes found while wiring: LiveWriter.BeginDirect gained
  the Rust value-kind gate (WrongValueKind for non-direct databases);
  Commit/close/abort state transitions match Rust close.rs exactly
  (finish_close refuses non-closing states; close_failure and
  closing_failure set the abort outcome only when a draft was pending;
  discard_draft fails the writer closed on a failed discard).

Tests:

- internal/live/live_writer_test.go: open/close round trip, second
  open WriterBusy, direct commit advances the generation (verified
  through the immutable reader on a sidecar-free copy, since
  OpenImmutable refuses live pairs by design), noop commit
  NoPendingTransaction, abort discards the draft, the commit barrier
  rejects a newer reader slot (claimed through a separate sidecar
  descriptor so the slot lock is a separate open-file description,
  exactly like Rust), open cancellation releases locks, commit
  cancellation aborts the draft while the writer stays healthy.
- internal/live/lifecycle_crash_test.go (v4work): the four commit crash
  points through the live writer (before/after_private_sync keep txn 1
  and the value absent; after_meta_write/after_meta_sync expose the
  complete txn 2), and the OutcomeUnknown fail-closed gate
  (commit.after_meta_write via fault.Fail: operations fail WrongState,
  Close completes cleanly with no abort payload because the failed
  publish abandoned the draft - Rust outcome_unknown parity - and the
  complete new generation is left behind).
- live_writer_public_test.go: public round trip with result facts,
  noop+abort, WriterBusy, non-live refusal, cancelled open leaves no
  lock residue.

Validation on the working tree, all under nice with -count=1: gofmt
clean, vet clean (plain + v4work), plain/v4work tests, race,
race+v4work, checkptr=2 (plain + v4work), mmap-trace PASS, five
cross-compiles (linux/386, linux/arm64, windows/amd64, darwin/arm64,
freebsd/amd64) plain AND -tags v4work.

- Next: chunk 4-4 five-aspect review (same five agents, adversarial,
  read-only, Rust authority baseline), fixes, round-2 re-review,
  signed commit + push.

### Status (2026-08-23) - chunk 4-4 five-aspect review round 1: three FAILs fixed, two PASSes

Round-1 review of the chunk 4-4 working tree, same five reviewers
(adversarial, read-only, Rust authority baseline). Verdicts: Helmholtz
(Rust parity) FAIL, Sartre (Go idioms) FAIL, Socrates (performance)
FAIL, Parfit (wire/integrity) PASS with one P2, Franklin
(APIs/docs/records) FAIL. All findings fixed on the working tree:

- P1 (Helmholtz) - the six direct ops (AssignV4/V6, ClearV4/V6,
  SetMetadata/ClearMetadata) returned raw store errors, so a failed op
  left its partial mutation in the draft and a later Commit could
  publish it; Rust routes every store error through mutate ->
  abort_after (live_writer.rs). Every op now wraps core errors in
  abortAfter (draft discarded, TransactionAborted class, Unusable on
  fatal IO/Format). SetMetadata gained the pre-mutate 20 MiB check
  (Rust stage_metadata_json position): oversized input refuses with
  InvalidArgument and the draft survives. Pin:
  TestLiveDirectOpFailureAbortsDraft (zero heap budget: assign
  succeeds, metadata store fails mid-edit, Commit -> NoPendingTransaction,
  writer healthy, the immutable copy proves the partial mutation never
  published).
- P2 (Helmholtz) - abortAfterSource nesting: the outer class is now
  always TransactionAborted and a failed discard nests the
  CleanupInProgress class inside (Rust
  TransactionAborted(CleanupIncomplete)); outer detail "the pending
  transaction was aborted" (sdk_error.rs Display prefix).
- P2 (Helmholtz) - PageBudget.MaxOpenFiles added (public + internal),
  DefaultBudget() = 2, OpenLiveWriter refuses < 2 with
  CodeInsufficientResourceBudget and the Rust-verbatim detail "a live
  writer requires two open files" before any path access (Rust
  TransactionBudget::validate at LiveWriter::open). Pin:
  TestPublicLiveWriterBudgetValidation (bounds 1 and 0).
- P2 (Socrates) - os.Getpid() per operation replaced by a package-init
  cached process identity: zero syscalls on the hot path.
- P3 (Socrates) - Core.Publish reuses the tracked physical extent set
  by Shrink instead of a post-sync FileSize(): one fstat saved per
  commit, byte-identical to Rust shrink_or_retain reuse.
- P2 (Parfit) - Core.rebootstrap re-stats via FileSize() exactly like
  Rust select_committed (fresh stat on the shared-lock live-open path);
  the old "under the exclusive lock" comment was false on that path.
- P2 (Sartre) - mainPublicIdentity dropped; results carry mainIdentity
  (publicIdentity is identity-preserving in Go).
- P3 (Franklin) - SOW "six cross-compiles" corrected to five.
- P3s (Helmholtz/Parfit/Sartre) - closeFailure/closingFailure factored
  through abortOutcomeFor(hadPending, hasDraft); public LiveWriter
  field renamed lw (shadowing); sidecar scanAtMost/scanAtMostCancellable
  doc comments re-attached; raw type assertions replaced with expectCode
  (live_writer_test.go, lifecycle_crash_test.go); errorCodeOfPublic
  deleted in favor of lifecycleCode; crashWriterBudget delegates to
  liveWriterTestBudget; the floating "LiveCommitStatus is not a new
  type" comment removed; claimWriter detail aligned to the Rust Display
  "another live writer owns this database" (sidecar.go:303 =
  sdk_error.rs:375); outcome-unknown test doc reworded to the
  clean-close outcome; membership property tests carry MaxOpenFiles: 2
  and the "no open-files bound" comment is gone.

Recorded conventions from round 1 (all structurally closed or
accepted, see the complete list in the round-2 entry): per-transaction
CancellationToken absent, Rust metadata accessors deferred, workflow
input gates structurally closed, lifetime-lock wait not
cancellation-polled, verifyPath after mapping open, live_writer.go at
661 lines accepted, classedError internal mirror kept.

### Status (2026-08-23) - chunk 4-4 five-aspect review round 2: FAILs fixed and re-verified, SOW entry

Round-2 re-review of the fixed tree returned: Socrates (performance)
PASS, Sartre (Go idioms) PASS, Parfit (wire/integrity) PASS, Helmholtz
(Rust parity) FAIL with one new P2, Franklin (APIs/docs/records) FAIL
with one new P2 and one P3. All fixed or recorded:

- P2 (Helmholtz) - public LiveDirectTransaction.Commit and Abort after
  a failed op reported WrongState ("direct transaction is no longer
  active") where Rust reports NoPendingTransaction: Rust commit and
  abort have no transaction-nonce check (live_writer/commit.rs
  commit_attempt, live_writer.rs abort has_draft gate) and a draft-less
  core reports NoPendingTransaction (writer_core/publication.rs). The
  public terminal gates no longer call requireActive; they keep only
  the spent-nonce check (!t.active -> NoPendingTransaction) and a
  closed-writer nil guard (WrongState, a Go-only lifetime case Rust
  cannot express), then delegate to the internal ops, which already
  produce the Rust-exact classes. The mutation ops keep the draft
  presence gate: Rust require_transaction fails WrongState on a
  draft-less transaction (the nonce left with the discarded draft).
  Pin: TestPublicLiveDirectCommitAbortAfterOpFailure (public path:
  failed op -> TransactionAborted, Commit and Abort ->
  NoPendingTransaction, spent Commit -> NoPendingTransaction, fresh
  transaction commits, partial mutation never published).
- Same-failure search (SOW validation gate) - the identical defect
  class existed on the pre-existing immutable-mode DirectTransaction
  (writer_public.go) and was worse: a failed op left the draft alive
  and a later Commit PUBLISHED the partial mutation (confirmed by a
  scratch test). The immutable ops now wrap store errors in
  Writer.abortAfter and spend the transaction; SetMetadataJSON gained
  the pre-mutate 20 MiB cap (InvalidArgument, draft survives, Rust
  stage_metadata_json position); public Commit and Abort report
  NoPendingTransaction on the draft-less state and WrongState only for
  the closed-writer nil case. Pins: TestPublicDirectOpFailureAbortsDraft,
  TestPublicDirectOversizedMetadataKeepsDraft. The carried slice-B P3
  "feed workflow Commit-after-abort reports WrongState" does NOT
  reproduce (verified by scratch test: the ops already spend the
  handle on failure and Commit reports NoPendingTransaction); the
  record was stale and is closed here.
- P2 (Franklin) - the round-1 require_main_available comment claimed
  Rust draws a discarded uniqueness nonce that cannot fail; both halves
  are false (require_main_available is a pure POSIX no-op in
  live_cleanup.rs require_available, and random::nonzero_128 returns
  Result). The comment now states the no-op truth and points the
  4-3 cleanup-attempt nonce draw at its documented home.
- P3 (Franklin) - the deferred-convention list below now names all
  three structural closures (require_owner/ForkedHandle,
  require_operation_owned/workflow_input_open, the DirectTransaction
  metadata accessors in addition to the LiveWriter accessors).
- P3s (Parfit) - the SOW record for this review pass is this entry;
  the lifetime-lock non-cancellable wait is recorded below as an
  accepted convention.

Deferred conventions (complete list, all structurally closed or
accepted with evidence):
- Per-transaction CancellationToken absent: Go's live writer owns
  checkpoint injection and the public facade takes no token; Rust
  DirectTransaction carries one, but every observable class has a Go
  mapping and no concurrent cancellation source exists.
- Rust metadata accessors absent: LiveWriter and DirectTransaction
  metadata_json_len/read_metadata_json/metadata_json
  (live_writer/direct.rs) are not ported; Go exposes only the staging
  ops. Deferred surface, tracked here.
- require_operation_owned / workflow_input_open structurally closed:
  Rust requires the operation handle not abandoned (Drop ->
  abandon_operation) and the workflow input closed before direct
  metadata staging; Go has no operation handles and direct
  transactions are the only workflow kind reachable, so only the
  metadata-staged check is observable.
- require_owner/ForkedHandle structurally closed on the 4-4 writer
  path: Go cannot fork, so the process identity cached at package init
  is used; the Rust ProcessIdentity comparison cannot fail in Go.
- Lifetime-lock acquisition is not cancellation-polled
  (F_OFD_SETLKW blocks; Rust polls live_writer.rs): live writers, live
  readers, and immutable readers all take the lifetime lock SHARED and
  never contend; only the legacy immutable OpenWriter takes it
  exclusive, so a cancelled live open waits at most on that legacy
  path and returns Cancelled once the lock is free. Latency-of-
  cancellation only; no correctness or wire impact.
- verifyPath runs after the mapping is open (Go bundles
  open -> lock -> map); Rust verifies the path first. Same observable
  classes; ordering difference only.
- Message parity: claimWriter "another live writer owns this database"
  (sdk_error.rs:375) and abort outer "the pending transaction was
  aborted" (sdk_error.rs:380-382) are Rust-verbatim; the inner
  "commit discard failed" prefix on the CleanupInProgress nesting is a
  documented rendering-only difference (class parity exact).
- classedError internal mirror kept: the internal live package returns
  internal classes; the public facade maps them to public codes.
- live_writer.go at 661 lines accepted: one cohesive state machine;
  splitting would reduce clarity.

Round-3 re-verify (Helmholtz, Rust parity) of the round-2 fix tree
found one same-class gap the same-failure search missed and closed:
- P2 (Helmholtz) - the six public live ops left the handle active
  after a failed op; once a newer transaction began, the stale handle
  passed requireActive (the new draft is non-nil) and could mutate and
  even commit the newer transaction's draft, where Rust refuses
  WrongState (require_transaction -> operation_is; the nonce lives in
  the discarded draft). Every live op now spends the handle whenever
  the failure left no draft (t.active = false when Draft() == nil):
  pre-mutate refusals (the 20 MiB cap) keep the draft and the handle,
  store failures spend it. Pin: TestPublicLiveStaleHandleAfterOpFailure
  (stale op -> WrongState, stale commit -> NoPendingTransaction, the
  newer draft commits its own value untouched).
- P3 (Helmholtz, accepted) - the terminal class after a FATAL op error
  (Io/Format) is NoPendingTransaction on the spent handle where Rust
  reports WrongMode("writer is unusable") via require_healthy: not
  reachable through public input today (no store op raises Io/Format;
  the fault points are armed in commit only), and identical to the
  immutable path's recorded spent-transaction convention.

Validation on the final fix tree, all under nice with -count=1: gofmt
clean, vet clean (plain + v4work), plain/v4work tests ./..., race,
race+v4work, checkptr=2 (plain + v4work), mmap-trace PASS, five
cross-compiles (linux/386, linux/arm64, windows/amd64, darwin/arm64,
freebsd/amd64) plain AND -tags v4work. Rust fixtures cross-open
unchanged.

- Next: signed commit + push of chunk 4-4, then chunk 4-5 (live
  reader) per the M4 chunk plan.

### Status (2026-08-23) - chunk 4-5 design recorded: live reader open/register/close

Chunk 4-5 design, recorded before coding per the pre-implementation
gate. Rust authorities read in full: live_reader.rs (LiveReader,
ReaderCloseResult, From<LiveReaderClose>), reader_core/live.rs
(LiveReaderCore state machine: open, register,
select_registered_generation, verify_registration,
release_gate_after_failure, close with the eight states, require_open/
require_owner, reader_closed/reader_close_incomplete),
reader_core.rs (ReaderCore, DatabaseInfo), bootstrap.rs (OpenMode::
LiveReader finish rules), process_identity.rs (fork-safe ownership;
Go uses the spec-15.6 PID fallback because MADV_WIPEONFORK is not
available to Go).

Key decisions:

- Open order (Rust LiveReaderCore::open): requireLiveSupported,
  checkpoint, read-only open + SHARED lifetime lock (new
  mapping.OpenLiveReader; the mapping owner stays the only creator of
  mappings), identity capture, verifyPath, select_registered_generation
  (verifyPath, sidecar.verifyPath, FRESH physical stat via FileSize(),
  bootstrap ModeLiveReader, database-id match against the sidecar
  header -> WrongState "reader table belongs to a different database",
  scanAtMostCancellable(committed txn)), remap to committed bytes,
  claimReaderCancellable, checkpoint, verifyPath, sidecar.verifyPath.
  The gate is held EXCLUSIVE during registration and released on every
  failure path (Rust finish_with_cleanup), so registration serializes
  with commit and with other registrations.
- Bootstrap ModeLiveReader joins the Go bootstrap authority with the
  Rust finish_open rules: a live reader requires a PROVEN CURRENT
  generation (sole meta refused, like the writer) and tolerates an
  unpublished tail (the reader remaps to committed bytes only, never
  the writer's in-flight growth). The fresh stat mirrors the 4-4
  select_committed lesson: the writer may have grown the file between
  the mapping-open stat and registration.
- Close order (Rust LiveReaderCore::close state machine): owner check,
  idempotent Closed; Open|CloseOnly -> gate SHARED (failure:
  CloseOnly + incomplete); GateHeldSlotActive -> verifyRegistration
  (failure: release_gate_after_failure, which on a gate-unlock failure
  nests the CleanupIncomplete class) then Unmap; GateHeldSlotClearing
  -> clearReader; GateHeldSlotCleared -> unlockReader;
  GateHeldSlotReleased -> unlockGate; MainLockOnly -> mapping.Close
  (Go mapping bundles unmap + lifetime unlock; the reader unmaps
  BEFORE clearing the slot exactly like Rust, so a new
  mapping.Unmap() separates the two). Every failure returns the
  factual incomplete result with RetainedReaderCloseRequired and the
  cause; the reader stays retryable.
- ForkedHandle ownership: the PID fallback of spec 15.6. The package-
  init cached PID (already used by the live writer) is compared on
  every operation; a forked child reports the ForkedHandle class.
- Public surface: OpenLiveReader + LiveReader mirroring the public
  ImmutableReader surface over the same logical core (Info,
  FileIdentity, LookupDirectV4/V6, DirectRangesV4/V6, Cardinality,
  LookupFeed, MetadataJSON, Pin-protected membership/enrichment
  lookups) and Close returning ReaderCloseResult (CloseOutcome +
  CoordinationCleanup + Cause, retryable, reusing the 4-4 public
  outcome types). The Pin machinery generalizes pinState.r to an
  interface so both readers share one Pin type.
- Windows/FreeBSD: the live reader refuses through the same honest
  stubs as the live writer (mapping_windows.go refuses every open;
  the live-support gate refuses FreeBSD before path access); the six
  cross-compiles stay green.

- Next: implement + tests (internal live_reader_test.go, public
  live_reader_public_test.go, v4work close/registration crash matrix),
  full battery, five-aspect review, signed commit + push.

### Status (2026-08-23) - chunk 4-5 implemented and validated locally

Implementation complete (the design above, plus two parity corrections
the implementation exposed):

- v4/go/internal/live/live_reader.go: the LiveReaderCore mirror with
  the eight-state close machine, verifyRegistration,
  releaseGateAfterFailure (CleanupInProgress nesting on gate-unlock
  failure, the Go mirror of Rust Error::CleanupIncomplete via the
  writer's combineErrors), requireOpen/requireOwner, and the factual
  LiveReaderClose result. OpenLiveReader runs the exact Rust open
  order: checkpoint, mapping.OpenLiveReader (shared lifetime lock,
  requireLiveCoordination refused before path access), identity
  capture, verifyPath, reader.OpenLiveMapped (map_reader), documented
  require_main_available POSIX no-op, sidecar open by database id,
  exclusive cancellable gate, register (verifyPath,
  sidecar.verifyPath, fresh FileSize stat, SelectRegisteredGeneration,
  database-id match -> WrongState, scanAtMostCancellable, checkpoint,
  Remap to committed, claimReaderCancellable, checkpoint, verifyPath,
  sidecar.verifyPath), gate release via combineErrors on every failure.
- v4/go/internal/reader: bootstrapMode refactored to
  bootstrapModeWith(mode, physical); SelectRegisteredGeneration(physical)
  re-selects ModeLiveReader under the gate with the fresh stat;
  OpenLiveMapped mirrors Rust map_reader (open-stat bootstrap + remap,
  giving the database id before the sidecar open); Unmap added.
- v4/go/internal/mapping: OpenLiveReader (read-only, shared lifetime
  lock) added with an explicit requireLiveCoordination gate;
  requireLiveWriter renamed to requireLiveCoordination; Mapping.Unmap
  added (munmap only, keeps descriptor + lock, Rust Mapping::unmap);
  Remap no longer bounds the target by the open-time physical extent
  (Rust require_file_extent re-stats only; the live reader may remap
  to a generation the writer grew after the open stat). CRITICAL
  regression caught and fixed during implementation: openMapping's
  live gate must apply to rdwr opens only, so FreeBSD immutable opens
  (whole-file flock) keep working; OpenLiveReader refuses explicitly
  (verified by GOOS=freebsd builds and the pre-existing FreeBSD
  live-refusal tests).
- v4/go/internal/bootstrap: ModeLiveReader with the ModeWriter finish
  rules (sole meta refused, tail tolerated), covered by new
  bootstrap_test.go cases.
- v4/go/reader_public.go: pinState.r generalized to the pinHost
  interface (require* + core + addPin/dropPin) so the immutable and
  live facades share one Pin implementation; the require* meta checks
  moved to shared package-level helpers used by both facades.
- v4/go/live_reader_public.go: public OpenLiveReader + LiveReader with
  the full immutable-reader surface over the pinned generation
  (Info, FileIdentity, LookupDirectV4/V6, DirectRangesV4/V6,
  Cardinality, LookupFeed, MetadataJSON, the eight cursor/query
  constructors added by review round 1, and Pin-protected membership
  and enrichment lookups) and Close returning ReaderCloseResult
  (CloseOutcome + CoordinationCleanup + Cause + CleanupState). Every
  public operation pays exactly one open-state check (Rust
  LiveReader::core -> require_open parity); pins refuse on a
  close-only reader and Close reports HandleBusy while pins are live.

Validation (all under nice, -count=1): gofmt clean; vet plain and
v4work; full test suite plain and v4work (all packages); race plain
and v4work; checkptr=2 on live/reader/root plain and v4work;
check-mmap-trace PASS (no read/pread64/readv/write/writev/pwrite64/
lseek on any .iprdb descriptor); five cross-compiles (linux/386,
linux/arm64, windows/amd64, darwin/arm64, freebsd/amd64) plain and
v4work; cross-vet of the test trees (freebsd/windows/darwin) shows
only the pre-existing sidecar-test and security-test tag gaps, present
at the chunk 4-4 base too.

Tests added: internal live_reader_test.go (open/close round trip with
slot-state proof, the Rust pinned-generation parity scenario
including ReaderCapacityExhausted and slot reuse, close retry after
main-path replacement with CloseOnly op refusal, cancellation leaving
no slot or lock residue, missing-sidecar and different-database
refusals, ForkedHandle structural check via the cached-PID override),
public live_reader_public_test.go (round trip with identity parity
against the creation facts, range split parity, pin read of a live
membership pair converted by InitializeLive, HandleBusy close with a
live pin, retry close result facts, cancelled open residue-free),
bootstrap ModeLiveReader finish-rule cases.

- Next: five-aspect adversarial review of the 4-5 delta (the five
  kept reviewers), fix findings, append review rounds, signed commit
  + push, then chunk 4-6 (transitions).

### Status (2026-08-23) - chunk 4-5 review round 1: five findings fixed

The five kept reviewers ran the adversarial five-aspect review of the
4-5 delta (Rust parity, Go idioms, performance, wire format/integrity,
APIs/docs/records). Verdicts: 3 FAIL, 1 PASS (performance, one P3),
1 FAIL (Rust parity). All blocking findings were fixed before the
battery re-run.

Findings and fixes:

- P1-1 (Go idioms + APIs agree): the live facade's Pin/Close
  arbitration was broken. Pin touched the internal core before
  registering its pin and Close read r.lr without atomics, so Pin
  racing Close could panic on a nil core and data-race the internal
  state machine (proven by the race detector). Fix: the live facade
  now ports the immutable arbitration exactly - Close CASes the
  shared closed flag before touching the internal reader and reverts
  on HandleBusy, error, or incomplete close; Pin checks closed
  before and after pins.Add(1) and only then runs the owner/open
  check, dropping the pin on failure; the internal close can never
  overlap a registered pin or Pin's check (the CAS blocks new pins
  once close starts). New TestPublicLiveReaderPinCloseRace hammers
  the arbitration under -race (both HandleBusy and WrongState
  outcomes asserted).
- P1-2 (APIs/docs/records): LiveReader was not the full
  immutable-reader surface: the eight cursor/query constructors
  (DirectCursorV4/V6, FeedCursor, FeedRangeCursorV4/V6,
  NetworkEnrichmentV1CursorV4/V6, MembershipQuery) were missing
  although the facade docs and the SOW claimed the full surface.
  Fix (long-term-best): the cursor types now hold one shared
  cursorHost interface (checkOpen + core + addPin + dropPin) so the
  same cursor implementations serve both facades; LiveReader gained
  the eight constructors, each running the owner/open check exactly
  once (Rust LiveReader::core -> require_open). The membership
  scope, query, join, aggregation, and algebra hosts were
  generalized the same way. New public tests cover the direct
  cursor round trip, the membership cursors and query on a live
  pair, and the enrichment cursor holding its reader pin
  (HandleBusy while open, clean close after).
- P2-1 (Rust parity + wire/integrity agree): the live reader close
  never released the sidecar. Go has no drop, so every open/close
  cycle leaked the sidecar read-write mapping (4096 + capacity*16
  bytes) and its descriptor until GC; the writer closes its sidecar
  at the terminal close step and Rust drops it with the core. Fix:
  the terminal MainLockOnly -> Closed transition now calls
  sidecar.close() after the lifetime unlock, mirroring Rust's drop
  order; retryable failure paths keep the sidecar open because the
  close machine still needs the reader table.
- P3s: Remap extent-refusal detail aligned with Rust require_file_extent
  ("mapping exceeds the file extent"); post-Unmap View reports
  "mapping unavailable" instead of "mapping closed"; public Close
  wraps internal errors (ForkedHandle) and ReaderCloseResult.Cause
  through publicError; pin lookups capture the internal core at Pin
  creation (pinState.core) and skip the per-call host dispatch and
  open check (sound: a reader with live pins cannot close, Rust
  borrow parity); pinHost trimmed to dropPin-only; bootstrap test
  reuses mustFormatInvalid; the public identity test decodes with
  binary.LittleEndian.Uint64; FileIdentity doc reworded; the
  enrichment cursors register their pin through the shared addPin
  arbitration on both facades.
- Dropped promise (APIs/docs/records): the design entry promised a
  v4work close/registration crash matrix. Rejected with evidence: the
  close machine has no injectable fault points, the unmap-before-clear
  crash property is guaranteed by ordering (a crash between them
  leaves the slot naming a still-valid generation), and the Rust
  corpus has no reader-close crash matrix either (confirmed by the
  wire/integrity review of tests/live_roundtrip.rs).

Validation re-run (all under nice, -count=1): gofmt clean; vet plain
and v4work; full test suite plain and v4work (all packages); race
plain and v4work; checkptr=2; check-mmap-trace PASS (fixtures mapped,
never streamed); six cross-compiles (linux/386, linux/arm64,
darwin/amd64, darwin/arm64, freebsd/amd64, windows/amd64); cross-vet
shows only the pre-existing test-tree tag gaps.

- Next: five-aspect re-review of the fixed delta, then signed commit
  + push, then chunk 4-6 (transitions).

### Status (2026-08-23) - chunk 4-5 review round 2: all five aspects PASS

Round-2 re-review of the fixed delta, same five reviewers, same
disjoint aspects:

- Rust parity (Helmholtz): PASS. P2-1 verified (sidecar released only
  on the terminal transition; retryable paths keep it; crash wire
  state unchanged); P3-2 verified (Remap detail verbatim Rust); the
  Pin/Close arbitration, the eight cursor constructors (one
  require_open each, Rust live_reader.rs:73-181), the pin-captured
  core, and the cursorHost sharing were walked against the Rust
  authorities with no divergences.
- Go idioms (Sartre): FAIL in round 2 on one P2 - the five dead
  LiveReader.require* methods left over from the pinHost trim, plus a
  stale comment. Deleted; the core() accessor comment now describes
  the reader-level operations. P3s also fixed: pinState doc records
  the nil-core cursor-pin invariant, and the dead startClose channel
  was removed from the race test. Re-verified: PASS. (Round-2 note:
  the first deletion attempt was a local script bug - the block was
  replaced in memory but not written; caught by the reviewer's tree
  check and re-applied.)
- Performance (Socrates): PASS. The pinState core cache is confirmed
  (zero itab, zero Core() per pin lookup; stable because pins block
  Close); cursor ops pay one itab dispatch per operation and exactly
  one open-state check per reader (Rust require_open parity); the
  direct lookup hot paths are unchanged.
- Wire format and integrity (Parfit): PASS. P2-1 resolved; slot,
  gate, and lifetime locks are all down before the sidecar release;
  the pin-core cache reads the same pinned generation with no wire or
  locking interaction; the mapping delta matches Rust detail strings.
- APIs, docs, records (Franklin): PASS. Both round-1 P1s verified
  fixed (the race probe is clean under -race; all eight cursor
  constructors present with one check each); four doc P3s fixed
  (FileIdentity wording, cursors/membership file headers now name
  both facades, double-Close concurrency note in the LiveReader doc,
  SOW enumeration includes the cursor constructors).

All five aspects report no P0-P2 findings. Validation battery re-run
after the last fix (all under nice, -count=1): gofmt clean; vet plain
and v4work; full test suite plain and v4work; race plain and v4work;
checkptr=2; check-mmap-trace PASS; six cross-compiles; ZeroAlloc
passes. TestPublicLiveReaderPinCloseRace runs under -race on every
battery pass.

- Next: signed commit + push of chunk 4-5, then chunk 4-6
  (transitions: ResetLiveCoordination, ResolveInterruptedLiveTransition,
  resolve_live_transition, resolve_create_live, .readers.reset).

### Status (2026-08-23) - chunk 4-6 design recorded: transitions (reset + resolvers)

Chunk 4-6 design, recorded before coding per the pre-implementation
gate. Rust authorities read in full: live_lifecycle.rs (types:
LiveTransitionOperation, LiveResetPolicy, LiveTransitionStatus,
LiveCoordinationLocation, LiveTransitionResult), transition.rs
(reset_live_coordination, finish_reset, prepare_reset_sidecar,
existing_identity, verify_previous, remove_exact, require_capacity),
resolution.rs (LiveTransitionResolutionMode, resolve_live_transition,
resolve_initialize, resolve_reset, observe, observe_reset_canonical,
observe_reset_private, require_supplied, require_main, require_attempt,
is_attempt, require_previous_identity, resolved,
resolved_after_cleanup, cleanup_attempt, remove_previous),
create_resolution.rs (resolve_create_live, Main/Coordination
observation, definitive, complete, rollback, verify_created,
require_supplied, expected_spec, result folding), residue.rs
(LiveResidueKind/Status/Result, resolve_interrupted_live_transition,
resolve_without_main, resolve_with_main, complete_canonical,
complete_private_reset, remove_valid_private, remove_private_residue,
retire_observed, facts, after_removal, with_cleanup),
namespace.rs (install_noreplace / install_replace_discarding /
install_exchange over publication namespace_mutation.rs
rename_noreplace/exchange/replace_discarding_destination), path.rs
(live_transition_temp = main name + ".readers.reset"), plus the ported
tests: resolution_tests.rs (4), create_resolution_tests.rs (3),
tests/live_transitions.rs (5 public), live_crash_tests.rs
(reset_crashes_leave_a_retryable_or_ready_database +
creation/initialization recoverable arms). The Go infrastructure they
compose is already ported: lockedMain + verify + attempt
(lifecycle_initialize.go), Sidecar reserve/reserveAt/openAt/openAny/
initializeCreating/publishReady/verifyPath/verifyHeader/currentHeader/
lockGate* (sidecar.go), namespace openRw/createPrivate/removeExact/
syncParent/pathIdentity/parentIdentity/verifyPath (namespace.go), the
retained-directory machine (directory.go), the mapping publication
primitives (mapping_publish_linux.go / darwin / posix / freebsd:
RenameNoReplace, RenameExchange, RenamePlain, ExchangeAvailable),
cleanup facts (cleanup.go), fault.Crash points (fault.go), and the
v4work crash-child harness (lifecycle_crash_test.go).

Key decisions (Rust is the baseline; every message and error class
mirrors the Rust Display/class exactly):

- ResetLiveCoordination (internal ResetLiveCoordination, public
  ResetLiveCoordination): requireLiveSupported, requireCapacity,
  LockedMain::open, canonical + private (.readers.reset via the new
  liveTransitionTemp path helper), existing_identity at canonical,
  RollbackSafe + existing coordination requires the atomic name
  exchange (mapping.ExchangeAvailable; Rust
  require_exchange_available -> DurabilityUnsupported detail
  "rollback-safe live reset requires atomic name exchange"), Attempt
  with previous_sidecar_identity (the 4-3 Go attempt gains the field),
  reserveAt(private), initializeCreating, publishReady, syncParent,
  verify_previous (verify_path or absent-check; a fresh canonical is
  CleanupConflict "canonical sidecar appeared during reset"),
  namespace::install dispatcher, crash points live_reset.before_replace
  / after_replace, finish_reset (syncParent, main.verify,
  verify_path(canonical), sidecar.verify_header, RollbackSafe
  remove_exact(previous) -> initialized_with_residue on failure,
  DiscardPrevious requires private absent -> CleanupConflict
  "discarding reset retained an unexpected private sidecar"), and the
  factual result folding (reservation_failure / initialized /
  initialized_with_residue / unknown / cleanup_created) exactly as the
  4-3 attempt methods.
- namespace install machinery: bindPair (one retained directory, two
  names), installNoreplace (verify private, rename_noreplace, sync,
  require_absent(private) + verify canonical), installReplaceDiscarding
  (verify both, replace_discarding_destination = rename(2), sync,
  require_absent private + verify canonical), installExchange
  (verify both, atomic exchange, verify swapped names, restore on
  verification failure -> CleanupConflict, double-failure ->
  CleanupIncomplete via combineErrors), with the freebsd/no-exchange
  build returning DurabilityUnsupported for exchange exactly like the
  Rust non-linux/apple arm. The Go primitives compose the existing
  mapping.RenameNoReplace/RenameExchange/RenamePlain with the
  retained-directory sync (directory.sync, not mapping.SyncDirectory,
  so the fd-relative machine stays the authority).
- ResolveLiveTransition (public ResolveLiveTransition): require
  live-supported, require_supplied (complete-result validation:
  nonzero database_id/transaction_id/commit_nonce/reader_capacity/
  sidecar_id, operation/reset_policy consistency, Windows RollbackSafe
  refusal kept as a compile-time-free parity comment, OutcomeUnknown
  requires new_sidecar_identity), LockedMain::open, require_main
  (basename, directory identity, main identity + generation), observe
  canonical and private (path_identity -> Sidecar::open_at with
  database_id -> public identity), initialize arm (private present ->
  Conflict "initialize transition has an unexpected private sidecar";
  canonical absent -> Unchanged/Absent; Ready -> Initialized/Canonical;
  Creating+Complete -> verify, sync_all, sync parent, publish_ready,
  sync parent, verify -> Initialized; Creating+Rollback -> verify +
  cleanup_attempt identity-guarded, absorbed verify failure),
  reset arm (canonical Previous/Attempt classification with
  require_attempt and ready-state checks, DiscardPrevious+Rollback ->
  Unresolvable "discarding reset cannot restore the previous sidecar",
  RollbackSafe previous cleanup at the private name, both-names
  conflicts, private Attempt + Rollback -> cleanup, private Attempt +
  Complete -> require_previous_identity, install, sync, verify, header
  check, remove_exact previous or discard-conflict), result folding
  with resolved / resolved_after_cleanup (status Unchanged +
  residue_possible + merged housekeeping + cause).
- ResolveCreateLive (public ResolveCreateLive): require_live_supported,
  require_supplied (nonzero ids/capacity, basename match, directory
  identity proven -> Unresolvable "creation never proved its parent
  directory identity" when absent, DirectoryIdentityMismatch),
  observe_main (absent / exact-empty under the lifetime lock /
  malformed attributed by main_identity), observe_coordination
  (absent / exact sidecar by sidecar_id + capacity / malformed
  attributed by sidecar_identity), definitive (ready pair -> Created;
  Created without ready pair -> Conflict "a completed creation result
  no longer names a ready pair"; clean NotCreated with both absent ->
  NotCreated; NotCreated with artifacts -> Conflict "a clean
  not-created result has unexpected artifacts"; otherwise proceed),
  Complete (malformed -> Unresolvable; reserve missing sidecar with
  private-failure folding; write_empty main with the expected EmptySpec
  when absent; syncs + publish_ready + verify_created; failures ->
  OutcomeUnknown with retained identities), Rollback (remove main then
  sidecar identity-guarded by ordinal, cleanup_result folding). The Go
  emptySpec type and databaseFile::write_empty/requireSidecarAbsent
  equivalents are the existing create.go initializePair machinery
  (writeEmptyMain refactor if needed; the existing write-empty path is
  reused, NOT reimplemented).
- ResolveInterruptedLiveTransition (public
  ResolveInterruptedLiveTransition + LiveResidueResult): the
  resultless residue machine over the same observe primitives (open_any
  for the Valid observation; Format/Corrupt/WrongState classes fold to
  Malformed like Rust Sidecar::open_any error arms), the full
  without-main / with-main matrix ported exactly (absent pair,
  canonical Ready, canonical Ready + private residue cleanup,
  canonical Creating + Complete -> complete_canonical under the
  exclusive gate with finishWithCleanup, private Ready + Complete ->
  complete_private_reset with require_absent + install, private +
  Rollback -> remove_valid_private, canonical Malformed -> Unresolvable
  "canonical live coordination is malformed; explicit reset is
  required", both-present without main -> Conflict, rollback-without-
  main retirement), facts/status folding (Absent/Ready/Completed/
  Removed/OutcomeUnknown, residue_possible, merged housekeeping).
- finishWithCleanup helper added to cleanup.go (Rust
  sdk_error::finish_with_cleanup: operation error and unlock error
  combine through combineErrors).
- Public surface: ResetLiveCoordination, ResolveLiveTransition,
  ResolveCreateLive, ResolveInterruptedLiveTransition +
  LiveTransitionResolutionMode (Complete/Rollback), LiveResidueKind
  (Canonical/PrivateReset), LiveResidueStatus (Absent/Ready/Completed/
  Removed/OutcomeUnknown), LiveResidueResult (all facts as pointers
  where Rust carries Option), reusing publicTransitionResult and a new
  publicResidueResult mapper; the public CreateResult gains nothing
  (resolve_create_live returns the existing public CreateResult via
  publicCreateResult).
- Tests: internal lifecycle_resolution_test.go (port of
  resolution_tests.rs: initialize complete/rollback, reset complete
  over corrupt coordination, exchanged reset cleans the exact previous
  sidecar, linux/apple-gated), lifecycle_create_resolution_test.go
  (port of create_resolution_tests.rs: sidecar-only complete/rollback,
  ready pair never removed), crash-test extension (reset crash points
  live_reset.after_creating_sync / after_ready_sync /
  after_private_parent_sync / before_replace -> Rollback residue
  recovery then re-reset; after_replace / after_directory_sync ->
  Complete residue recovery; creation/initialize recoverable arms via
  the residue resolver, porting Rust live_crash_tests.rs),
  lifecycle_public_test.go extension (live_transitions.rs port:
  immutable main initialized explicitly + resolve complete, existing
  coordination never repaired, rollback-safe reset replaces corrupt
  coordination with the main unchanged + capacity proof, discarding
  reset reports policy and cannot roll back after installation,
  cancelled transition leaves the main unchanged).
- Windows/FreeBSD: the same honest stubs as 4-3/4-5: requireLiveSupported
  refuses before path access (lock_refuse.go), installExchange refuses
  DurabilityUnsupported on non-linux/darwin builds, and the six
  cross-compiles stay green.

- Implemented and validated 2026-08-23 (see the chunk 4-6 validation
  record below).

### Status (2026-08-23) - chunk 4-6 implemented and validated: transitions

Implemented exactly per the design entry above: ResetLiveCoordination
with the full prepare/install/finish cascade, ResolveLiveTransition
(initialize/reset matrices), ResolveCreateLive (main/coordination
observation matrix), ResolveInterruptedLiveTransition (resultless
residue matrix), the fd-relative rename machines
(namespace_install_linux.go renameat2 RENAME_NOREPLACE/RENAME_EXCHANGE,
namespace_install_darwin.go renameatx_np RENAME_EXCL/RENAME_SWAP,
namespace_install_other.go honest nsUnsupportedError, Windows
install refusal), finishWithCleanup, and the public facade with the
internal<->public identity/basename/housekeeping round trips.

Three defects found and fixed during implementation validation:

1. Descriptor/lock leak across the lifecycle surface: CreateLive,
   InitializeLive, ResetLiveCoordination, ResolveLiveTransition,
   ResolveCreateLive, and ResolveInterruptedLiveTransition never
   closed their owned sidecar/main descriptors. Rust relies on RAII
   drops; Go needs explicit closes. The leak retained open fds,
   mappings, exclusive lifetime locks, and gate locks after every
   operation - proven by TestCreateResolutionReadyPairIsNeverRemoved
   hanging forever in OpenLiveReader (blocked on the lifetime shared
   lock held by the leaked exclusive descriptor from
   resolve_create_live). Fix: one defer per owned descriptor,
   registered immediately after each observation/reservation so later
   observation failures cannot leak earlier ones (double close is
   safe: Sidecar.close nil-checks, os.File.Close returns ErrClosed).
2. ResetLiveCoordination never reported its reset policy: the Go
   newTransitionAttempt lacked the Rust Attempt::new
   reset_policy parameter, so result.ResetPolicy was always nil.
   Fix: parameter added; reset passes &policy, initialize passes nil.
3. Cross-v4work vet gaps in the live package: expectCode lived in the
   linux/darwin-tagged sidecar_test.go while the all-OS lifecycle and
   crash tests used it. Fix: expectCode moved to the shared
   expect_test.go; lifecycle_crash_test.go tagged
   "v4work && (linux || darwin)" because its child actions exercise
   real paths that honestly refuse on freebsd/windows (same surface
   class as the 4-5 lock_refuse boundary).

Validation battery (all under nice, -count=1): gofmt clean; vet clean
plain + v4work; full test suite plain + v4work (all packages); race
plain + v4work; checkptr=2 plain + v4work; check-mmap-trace PASS
(fixtures mapped, never streamed); six cross-compiles (linux/386,
linux/arm64, darwin/amd64, darwin/arm64, freebsd/amd64, windows/amd64)
green; ZeroAlloc passes. Live-package cross-vet is now clean on
freebsd/windows/darwin plain + v4work.

Pre-existing gaps verified unrelated to this chunk (recorded, not
fixed here): GOARCH=386 v4work vet reports 1<<32-overflows-int in
test-only mocks of internal/writer/range_edit_test.go,
internal/retire/retire_test.go, internal/bitmap/free_test.go, and
internal/tree/tree_test.go; GOOS=windows v4work vet reports
unix.Geteuid undefined in internal/security/security_test.go. These
packages are untouched by chunk 4-6 and the standard battery does not
cross-vet.

### Review round (2026-08-23) - five-aspect adversarial review, all PASS

Reviewers (same model, level-1, adversarial, disjoint scopes, kept
open across chunks; scopes recorded at the top of this SOW):

1. Helmholtz (Rust parity): FAIL -> PASS. Round-1 findings:
   - P2-3 installReplaceDiscarding private verify returned the raw
     namespace class instead of CleanupConflict; both verifies now
     fold to CodeCleanupConflict with the Rust detail string
     (namespace_install.go installReplaceDiscarding).
   - P2-1 residuePossible over-included CodeCleanupConflict; now only
     CodeCleanupInProgress, exactly the projection of Rust
     sdk_error.rs:299-301 matches!(self, CleanupIncomplete{..})
     (lifecycle_reset.go residuePossible).
   - P2-2 requireSupplied rejected nil DirectoryIdentity/MainIdentity
     with InvalidArgument; the nil draws were removed from the
     InvalidArgument condition (only the five nonzero draws remain,
     Rust resolution.rs:320-329) and requireMain now classifies a nil
     draw like a mismatch at the same step: directory nil ->
     CodeDirectoryIdentityMismatch, main nil -> CodeConflict "live
     transition main identity or generation changed" (resolution.rs:
     359-376). Files: lifecycle_resolution.go requireSupplied +
     requireMain.
   - P2-4 completeCreate main-absent branch passed nil main identity
     to unknownAfterPrivateFailure; it now threads failure.identity
     when non-nil, matching Rust create_resolution.rs:180
     failure.identity.map(public_identity)
     (lifecycle_create_resolution.go completeCreate).
2. Parfit (wire/integrity): FAIL -> PASS. P2-1 was the same
   installReplaceDiscarding class fix as Helmholtz P2-3; verified all
   three discarding-reset error sites against Rust (CleanupConflict in
   install + finish_reset, Conflict in resolve_reset) with exact
   detail strings.
3. Sartre (Go idioms): FAIL -> PASS. Round-1 findings fixed:
   hand-rolled asFormat replaced with errors.As in isFormatClass /
   isMalformedClass; map-literal ternary in resolvedAfterCleanup
   replaced with plain if/else; stale public doc header now lists
   reset + the three resolvers; `_ = file` dead captures, stale chunk
   labels, `_ = privateFile` suppressions, and the publicHousekeeping
   no-op loop removed.
4. Socrates (performance): PASS in round 1; one stale doc comment
   fixed; no hot-path findings (the fixes are cold-path
   error-classification changes; no new syscalls or allocations).
5. Franklin (APIs/docs): FAIL -> PASS. Round-1 findings fixed: SOW
   design entries now state six cross-compiles (4-5 and 4-6 entries;
   the remaining "five" occurrences are dated historical records of
   five-target runs); requireSupplied nil-identity guard documented.
   A P3 wording note (comment attributed an absent Rust Option to
   transition identities that Rust models as values) was fixed:
   lifecycle_resolution.go now states Go models these identities as
   pointers and nil draws are classified like mismatches.

Final validation battery (all under nice, -count=1): gofmt clean;
vet clean plain + v4work (full tree); full test suite plain + v4work;
race plain + v4work; checkptr=2 plain + v4work; check-mmap-trace PASS
(fixtures mapped, never streamed); ZeroAlloc PASS; six cross-compiles
(linux/386, linux/arm64, darwin/amd64, darwin/arm64, freebsd/amd64,
windows/amd64) green; live-package cross-vet clean on all six targets
plain + v4work.

Chunk 4-6 is ready to close.

### Status (2026-08-23) - chunk 4-7 design recorded: live snapshot source + recovery source guard

Chunk 4-7 design, recorded before coding per the pre-implementation
gate. Rust authorities read in full: snapshot/api.rs (snapshot_to:
require_live_supported, open_source, reject_live_self, construct,
finish, fail_attempt, fail_source), snapshot.rs (SnapshotSourceMode,
SnapshotBudget::validate with the live third-file draw),
snapshot/terminal.rs (SnapshotPreparationFailure surface: output,
cleanup, coordination_cleanup, housekeeping, source_cleanup guard,
cause; cleanup_state = Clean iff cleanup empty AND coordination_cleanup
None), snapshot/build.rs + snapshot/source.rs (GenerationReader over
source.mapping + source.meta), recovery/source_guard.rs (Source
enum, SourceEnd, SourceOpenFailure, terminal folding,
cleanup_for_cause, open_problem, live_coordination wrapper),
recovery/source_guard/live.rs (LiveSource::open_current full machine:
open_file, bind_current, open_sidecar_locked, prepare_claim,
claim_prepared, verify_live_claim, verify_live_paths; final_check;
release in the slot -> gate -> lifetime order; RegistrationState
Active/Clearing/Cleared/Released), plus the ported tests:
tests/snapshot_operations.rs live arms (live_membership_snapshot_...
live_snapshot_requires_the_sidecar_descriptor_budget,
live_snapshot_cannot_replace_its_own_source_path,
live_snapshot_race_with_writer_commit_is_refused,
live_snapshot_pins_its_generation_while_a_writer_advances),
mmap_runtime_tests.rs (SnapshotSourceMode::Live in the full live
round trip), freebsd_boundary.rs (Live snapshot -> ErrorCode::
LiveCoordinationUnsupported before any path access; no output),
history_projection.rs / mapped_chaining.rs / metadata_roundtrip.rs /
structured_values.rs (SnapshotSourceMode::Live arms).

The Go infrastructure they compose is already ported: mapping
(OpenLiveReader: open_read_only + shared lifetime lock + two-page
bootstrap map; MapFile for the bare descriptor read; StatIdentity),
reader (OpenLiveMapped: ModeLiveReader bootstrap + committed remap;
SelectRegisteredGeneration; reselectMeta; ImmutableReader cursor
surface + snapshot_source.go: MetadataJSONLen / FileIdentity /
ConfirmUnchanged), live (Sidecar open/lockGateCancellable/
scanAtMostCancellable/claimReaderCancellable/verifyPath/verifyHeader/
verifyReader/clearReader/unlockReader/unlockGate/close, checkpoint,
combineErrors, directory + openRegular + FileIdentity,
canonicalSidecarPath, format.Error classes), writer (CreateAttempt /
OutputAttempt / Discard / Publish, PublicationPolicy,
mapping.StatIdentity), and the public snapshot facade
(snapshot_public.go).

Key decisions (Rust is the baseline; every class and message mirrors
Rust exactly):

- New internal liveSource type (v4/go/internal/live/live_source.go)
  porting Rust recovery/source_guard/live.rs LiveSource for the
  current-selection open (open_current); the recovery-candidate
  variant (bind_candidate, candidate label proof) is chunk 4-10
  scope. The type is distinct from LiveReaderCore because Rust keeps
  them distinct: the source guard owns one mapping + one claimed slot
  with a simpler registration state machine (Active/Clearing/Cleared/
  Released) and its final check re-locks the gate EXCLUSIVE
  (ensure_gate_cancellable), unlike the reader-close shared-gate
  machine. It lives in internal/live (it composes the sidecar, lock,
  and namespace primitives directly) and is exported for the snapshot
  package; chunk 4-10 recovery composes the same guard.
- OpenLiveSourceCurrent(path, check): requireLiveSupported before any
  path access (mapping.OpenLiveReader already refuses; the explicit
  API-layer call mirrors api.rs require_live_supported, which runs
  before budget validation), then open_file (mapping.OpenLiveReader ->
  FileIdentity -> verifyPath), bind_current (verifyPath, bootstrap
  ModeLiveReader via reader.OpenLiveMapped, verifyPath again; the
  require_main_available POSIX no-op is omitted like every other Go
  live open), Sidecar::open, gate exclusive cancellable, prepare_claim
  (verify_live_paths = main verifyPath + sidecar verifyPath +
  verifyHeader, checkpoint, bind_current re-run = verifyPath +
  reselectMeta + verifyPath, database-id equality with the sidecar
  header -> CodeRecoveryCandidateChanged, scanAtMostCancellable),
  claim_prepared (claimReaderCancellable under the held gate, then
  verify_live_claim = verify_live_paths + verifyReader(slot, txn)),
  then gate unlock before return; registration Active, lifetime locked,
  owner PID retained. Every failure closes the sidecar and mapping in
  the Rust Unclaimed/Claimed order (a claimed source abandons through
  the terminal fold, producing the retryable guard state).
- Final check (FinishCurrent): requireOwner, checkpoint,
  ensureGateCancellable (re-lock the gate exclusive), meta unchanged
  (self.meta == used; the snapshot passes the claimed meta, so the
  check is the Rust structural equality), verify_live_paths,
  verifyReader(slot, used txn). Release follows in the Rust order:
  releaseSlot (ensure gate, clearReader, unlockReader), releaseGate,
  releaseLifetime (mapping Close = unmap + lifetime unlock + fd
  close, the Go equivalent of the Rust drop after unlock_file).
- Terminal fold (liveSourceEnd{Cause, Residue}): release failure with
  a clean check folds Cause = CleanupConflict "source recovery
  protection was not released" (ForkedHandle keeps its class) and
  Residue = true (Rust guard present -> coordination_cleanup
  CleanupGuard -> cleanup_state ResiduePossible); a failed check with
  a clean release folds the check cause with Residue false; both
  failing combine (combineErrors) with Residue true. The Go
  SnapshotPreparationFailure collapse keeps the established
  Cause + CleanupState shape; the source-guard presence maps to
  CleanupStateResiduePossible (the Rust cleanup_state projection),
  and the retryable guard itself is not carried (the Go precedent
  documented in the current Failure type; recovery chunk 4-10 ports
  the guard when the recovery surface needs it).
- SnapshotTo(Live) wiring (internal/snapshot): the live refusal
  replaced by the real source. Source open before the destination
  create (api.rs order). reject_live_self after the source open and
  before the attempt: Live + ReplaceExisting/ReplaceExistingNoRollback
  only, bind the destination parent + main name (missing parent ->
  NameNotFound "publication name is missing"; non-directory ->
  Conflict; overlong/invalid name -> NameInvalid), open the main name
  O_NOFOLLOW read-only (absent -> no rejection; symlink/non-regular ->
  Conflict with the writer's replacement messages), compare
  device+inode with the source identity; equal -> InvalidArgument
  "a live snapshot cannot replace its own source path" (Rust
  reject_live_self + problem()). The existing identity-compare after
  the attempt creation stays for both modes (source vs private output).
- The copy consumes the same reader core for both modes
  (ImmutableReader cursor surface); the live source exposes Core()
  like LiveReader, so build/copy.go is untouched. FinishCurrent
  replaces ConfirmUnchanged for the live mode and runs between builder
  finish and publish (api.rs finish_current position); failure paths
  call ReleaseOnly (fail_source) and fold the residue into
  CleanupState.
- Public surface (v4/go/snapshot_public.go): SnapshotSourceLive is
  no longer refused; the boundary-refusal test is replaced by the
  ported live tests. Budget validation already draws the live
  third file (internal Budget.Validate). The SnapshotPreparationFailure
  doc comment updates: the source-guard state now maps to
  CleanupStateResiduePossible instead of "always empty".
- Tests (public level): port live_membership_snapshot_... (live
  membership pair, snapshot Live, verify identity/feeds/bitmaps/
  metadata through the immutable output), live_snapshot_requires_the_
  sidecar_descriptor_budget (budget 2 + Live -> InsufficientResource
  Budget, no output), live_snapshot_cannot_replace_its_own_source_path
  (Live + supported replacement over its own path -> InvalidArgument,
  source bytes unchanged, no private artifacts; replaces the old
  boundary-refusal self arm), and on linux the two concurrency tests:
  live_snapshot_pins_its_generation_while_a_writer_advances (snapshot
  thread, wait for the reader claim bytes in the sidecar, writer
  commit during the copy, snapshot publishes the OLD txn, live reader
  sees the NEW txn) and live_snapshot_race_with_writer_commit_is_
  refused (controller moves the source under the live claim, snapshot
  fails RecoveryCandidateChanged | LiveRecoveryCoordinationUnavailable,
  no output, no private artifacts). freebsd/windows keep the
  LiveCoordinationUnsupported refusal (mapping refusal), pinned by the
  existing TestSnapshotLiveRefusedAtBoundary adapted to the honest
  platform class.

### Status (2026-08-23) - chunk 4-7 implemented: live snapshot source + recovery source guard

Implemented and validated on linux; branch pushed after review (chunk 4-6).

Implementation:
- `v4/go/internal/live/live_source.go` (new): `LiveSource` with
  `OpenLiveSourceCurrent` (Rust `open_current` through `claim_prepared`
  and `verify_live_claim`), `FinishCurrent` (final_check then release),
  `ReleaseOnly`, the slot/gate/lifetime release order with the exact
  Rust registration state machine, `terminal`/`cleanupForCause`
  (ForkedHandle keeps its class, everything else
  CleanupConflict "source recovery protection was not released"), and
  the `live_coordination` mapping applied at exactly the Rust call
  sites (`verify_live_paths`, slot scan, reader claim/proofs, gate
  ops; the database-identity mismatch stays the raw
  RecoveryCandidateChanged, mirroring Rust prepare_claim).
- `v4/go/internal/snapshot/snapshot.go` (rewritten): `To()` now follows
  the Rust api.rs order exactly: live supported-refusal (api layer)
  before budget validation, budget validation before the source open,
  source open before the destination create, `reject_live_self`
  before the create, the source `finishCurrent` (Rust
  `Source::finish_current`: final check plus the unconditional single
  release, folded through the terminal) between builder finish and
  publish, and the publish-gate cancellation discard. The immutable
  finishCurrent closes the reader on the success path (the previous
  version leaked the mapping on success), and a live final-check
  failure no longer double-releases (Rust finish carries the guard
  only when the release itself failed; Go folds that to the cleanup
  classification).
- `v4/go/internal/live/public.go`: exported `CheckSupported`
  (Rust require_live_supported at the api.rs refusal position).
- `v4/go/internal/snapshot/snapshot_unix.go` / `snapshot_windows.go`
  (new): platform split of the nofollow open + device/inode probe used
  by `reject_live_self`; windows stubs follow the mapping-owner
  precedent (live mode is refused before the probe on windows).
- `v4/go/snapshot_operations_test.go`: `TestSnapshotLiveMembership
  PreservesNamesIndexesBitmapsAndMetadata`,
  `TestSnapshotLiveRequiresSidecarDescriptorBudget`,
  `TestSnapshotLiveCannotReplaceItsOwnSourcePath`; boundary test
  renamed to `TestSnapshotLiveRefusedOnUnsupportedPlatforms`.
- `v4/go/snapshot_live_race_linux_test.go` (new, linux tag):
  `TestSnapshotLivePinsItsGenerationWhileWriterAdvances`,
  `TestSnapshotLiveSourceReplacementAfterReaderClaimBlocksPublication`
  (controller polls the claimed slot bytes, takes the gate OFD lock,
  renames the source; snapshot refuses with
  RecoveryCandidateChanged | LiveRecoveryCoordinationUnavailable).
- Findings fixed during implementation: (1) Go `release()` was missing
  the Rust `require_owner` first step; (2) budget validation ran after
  the source open, mis-ordering the Rust api.rs (and claiming a live
  slot before the budget refusal); (3) immutable success path leaked
  the reader mapping (FinishCurrent now closes); (4) live
  final-check failure double-released through failSource; (5) the
  pre-publish cancellation path leaked the builder mapping
  (now closed before the discard); (6) LiveSource path/sidecar proof
  errors surfaced raw (NameNotFound) instead of the Rust
  LiveRecoveryCoordinationUnavailable class.

Validation (all under nice): full `go test ./...` green, race +
`-tags v4work` green for live/snapshot/public, `go vet ./...` and
`-tags v4work` green (incl. freebsd/darwin cross-vet of the live and
snapshot packages), all six cross-builds (linux/386, linux/arm64,
darwin/amd64, darwin/arm64, freebsd/amd64, windows/amd64) green,
`check-mmap-trace.sh` PASS.

### Chunk 4-7 round-1 review (2026-08-23): findings and fixes

The five-reviewer swarm (Helmholtz Rust parity, Sartre Go idioms,
Socrates performance, Parfit wire/integrity, Franklin API/docs)
reviewed the chunk 4-7 delta adversarially; all five returned with
verified P0-P2 findings, all fixed and re-validated:

- P1 (four reviewers, independently): the live gate-time re-bind used
  the immutable selection mode with the open-time extent
  (`ReselectMeta` -> `ModeImmutableReader` over `PhysicalSize`), so a
  live database with an in-progress writer tail or a commit between
  the snapshot open and the exclusive gate refused (FormatInvalid) or
  silently pinned the open-time generation where Rust re-binds under
  the gate with a fresh stat (`OpenMode::LiveReader`) and maps the
  selected extent. Fixed: `prepare_claim` now mirrors the live-reader
  register sequence under the gate - fresh `m.FileSize()` ->
  `core.SelectRegisteredGeneration` -> database-id proof -> second
  path proof -> slot scan -> `m.Remap(selected committed bytes)` ->
  claim. `ReselectMeta` is the immutable-only helper again and its doc
  no longer claims live coverage. New race test
  `TestSnapshotLiveSourceReselectsGenerationCommittedWhileOpen` proves
  the snapshot publishes the commitment staged between open and gate.
- P1/P2 (three reviewers): `rejectLiveSelf` omitted Rust's
  `regular_identity` rules. Fixed: the destination main name is
  validated before any path access (`writer.ValidDestinationName`,
  exported and shared with `CreateAttempt`; empty/./.. and overlong
  names refuse `NameInvalid` exactly like `Destination::bind`), and
  the open destination is proved same-filesystem as its parent
  (`PublicationUnsupported`) with exactly one link (`Conflict`
  "publication inode link count changed"), with the Rust-verbatim
  detail strings. Regression tests:
  `TestSnapshotLiveRejectsInvalidDestinationNames`,
  `TestSnapshotLiveRejectsHardLinkedDestination`.
- P2 (two reviewers): the claimed-open unwind dropped the release
  residue. Fixed: `live.OpenFailure` carries the fold residue out of
  `releaseUnclaimed`; the snapshot machine maps it to
  `CleanupStateResiduePossible` (Rust `SourceOpenFailure.guard`).
- P2: `LiveSource.FileIdentity()` re-statted the descriptor although
  the open identity is immutable; it now returns the retained
  identity (Rust `Source::identity`, zero extra syscalls).
- P2: `Sidecar::open` leaked the mapped sidecar on the not-ready
  state; it now closes it before refusing (Rust drops it), so a
  crash-left sidecar no longer grows RSS per retry.
- P3 fixes: `failSource` keeps the primary cause pure (Rust
  `fail_source`); publish-gate cancellation uses `checkCancellation`;
  the duplicated post-finish fold became `abortAfterFinish`; the dead
  attachClose conditional was removed; the race-test `bytes` shadow
  renamed; the replacement-race controller re-checks the claim while
  holding the gate before renaming (starve guard); the snapshot
  budget doc names the live sidecar as the third file.
- Test fidelity: the live membership snapshot test now ports the Rust
  `b"{}"` metadata assertion (the fixture stages the metadata and the
  output must preserve it).
- Tracked follow-up (outside this delta, pre-existing in the
  immutable snapshot path, candidate for chunk 4-8+):
  `internal/snapshot/copy.go` `membershipWords` materializes each
  bitmap into a heap slice and the writer re-copies it; Rust interns
  membership words from the mapped view in bounded chunks. Needs a
  view-based writer interning entry point before it can be removed.

Re-validation after the fixes (all under nice): full `go test ./...`
green, race + `-tags v4work` green, `go vet` (both) green
(freebsd/darwin cross-vet green), all six cross-builds green,
`check-mmap-trace.sh` PASS, gofmt clean. Delta round 2 dispatched to
all five reviewers.

### Chunk 4-7 round-2 review (2026-08-23): two FAILs verified, fixed, verdicts

Round-2 delta review by the same five reviewers. Socrates (performance),
Parfit (wire/integrity), and Sartre (Go idioms) PASSed. Franklin
(API/docs) and Helmholtz (Rust parity) returned findings; both resolved:

- Helmholtz P1 (verified, fixed): `BasicSource::final_check` ->
  `bind_current` runs `verify_path` (inode plus
  `require_sidecar_absent`) before and after the bootstrap, plus a
  cancellation checkpoint inside; Go `ConfirmUnchanged` only re-checked
  the inode, so a `.readers` sidecar appearing mid-build could be
  ignored. Fixed:
  `v4/go/internal/reader/snapshot_source.go` `ConfirmUnchanged(path,
  check)` now proves sidecar absence under the lock on both sides of
  the re-selection with the checkpoint between the first proof and the
  re-selection, folding every path/sidecar failure to
  `RecoveryCandidateChanged` (Rust `candidate_changed`); the caller
  passes the snapshot checkpoint through. Regression tests: the unit
  "sidecar appeared" sub-test in `snapshot_source_test.go` and
  `TestSnapshotImmutableSidecarAppearingDuringBuildBlocksPublication`
  (sidecar injected at the first checkpoint; class 51, no output, no
  artifacts).
- Helmholtz P2 (verified, fixed): the open-time live bootstrap reused
  the open stat while Rust `map_reader`/`bootstrap_file` re-stats
  `file.metadata().len()` at the bootstrap moment; two writer commits
  inside the window could fail the Go open with FormatInvalid where
  Rust succeeds. Fixed at the single shared authority:
  `OpenLiveMapped` now samples `m.FileSize()` before the live-mode
  selection (covers the live reader and the live snapshot source
  together; the gate-side `SelectRegisteredGeneration` remains
  authoritative).
- Helmholtz P3-1 (verified, fixed): the `rejectLiveSelf` parent probe
  classed a symlink parent as Conflict; Rust `O_DIRECTORY|O_NOFOLLOW`
  folds ELOOP (and ENOTDIR for any other non-directory) into
  `NamespaceError::Io` -> CodeIO.
- Helmholtz P3-2 (verified, fixed): immutable release errors now route
  through `live.CleanupForCause` (Rust `terminal`/`cleanup_for_cause`:
  ForkedHandle keeps its class, everything else CleanupConflict) in
  both `finishCurrent` and the `openSource` release closure.
- Franklin FAIL -> PASS by rebuttal: its `out/.` NameInvalid claim was
  disproven with the active toolchain's std source
  (`Path::new("out/.").file_name() == Some("out")`, Go `Clean` equals
  Rust on the whole edge set), and its symlink-detail claim was
  corrected against `problem.rs:72-73` (the IoAt symlink branch exists
  but is unreachable from the `reject_live_self` probe on unix, so the
  Go verbatim NotRegular detail stands).

### Chunk 4-7 round-3 review (2026-08-23): all five PASS, residuals fixed

Round-3 delta review after the Helmholtz fixes: Helmholtz, Sartre,
Socrates, Parfit, and Franklin all PASSed. Residual P3 items were fixed
on the same working tree before close-out:

- Non-directory parent collapse: `rejectLiveSelf` (and later the
  writer's `CreateAttempt`) classed every non-directory parent CodeIO,
  matching the POSIX ELOOP/ENOTDIR fold; the Rust NotDirectory ->
  Conflict arm is unreachable from a path open on POSIX.
- `ReselectMeta` fresh extent: the immutable final-check re-selection
  now samples `m.FileSize()` and bootstraps ModeImmutableReader over
  the fresh extent (Rust `bootstrap_file` parity), so an externally
  grown or truncated main file refuses with the bootstrap classes
  (ImmutableLengthMismatch when committed != physical) instead of
  re-confirming the pinned open-time generation.
- Barrier select: the gate-barrier race test now closes a done channel
  after the snapshot goroutine returns and selects on it, reporting
  "the reselect race never ran" with the actual cause instead of a
  misleading failure message on successful early completion.
- `ConfirmUnchanged` doc now names the sidecar-absence proofs and the
  checkpoint.

### Chunk 4-7 final confirmation (2026-08-23): Windows parent split, all PASS

Parfit's final re-review found one Windows parity defect in the
parent-collapse change: Rust Windows `Directory::open` keeps the
NotDirectory -> Conflict arm (`FILE_ATTRIBUTE_DIRECTORY` clear or
`FILE_ATTRIBUTE_REPARSE_POINT` set, probed through CreateFileW with
`FILE_FLAG_OPEN_REPARSE_POINT`), so a junction, directory symlink, or
non-directory destination parent must be Conflict on Windows while
POSIX folds ELOOP/ENOTDIR to CodeIO. Fixed with one shared
platform-split authority, `writer.CheckPublicationParent`
(`v4/go/internal/writer/publication_parent_unix.go` +
`publication_parent_windows.go`), used by both `writer.CreateAttempt`
and `snapshot.rejectLiveSelf`; the Windows variant mirrors the Rust
CreateFileW probe and attribute check exactly (missing parent ->
NameNotFound, open/attribute failure -> CodeIO, non-directory or
reparse-point parent -> Conflict). All five reviewers confirmed PASS on
the final tree.

Tracked deferred items (recorded for the next chunks):

- `v4/go/internal/snapshot/copy.go` `membershipWords`
  heap-materializes each membership bitmap (pre-existing immutable
  snapshot path; needs a view-based writer interning entry point before
  it can be removed; candidate for chunk 4-8).
- UNIX-socket destination main name (live + replace policy) classes
  CodeConflict "publication name is not a regular file" in Go vs the
  Rust openat ENXIO Io class; refuse-only on an exotic input, no test
  or contract depends on it; carry into the chunk 4-8
  publication-resolver work.
- Rust Windows-only `require_main_available` (publication-GC
  availability under `#[cfg(windows)]`) has no Go Windows equivalent;
  it is a pure no-op on the validated POSIX platforms and the live
  machine is refused on Windows this milestone; track with the Phase-2
  GC surface.

Final battery on the working tree (all under nice): full `go test
./...` green, race + `-tags v4work` green for live/snapshot/public,
`go vet` both green, freebsd/darwin/windows cross-builds green (6
configs), GOOS=windows vet green for writer/snapshot/reader/mapping/
live, `check-mmap-trace.sh` PASS, gofmt clean. Chunk 4-7 committed at
f2c7ee7 and pushed to origin/master; next chunk 4-8 publication
resolvers.

### Status (2026-08-23) - chunk 4-8 design recorded: publication resolvers (result/problem surface, reservation machine, resolver, residue, maintenance)

Chunk 4-8 design, recorded before coding per the pre-implementation
gate. Rust authorities read in full: publication/resolver.rs (435:
inspect authority, dispatch desired/other/absent, complete_absent, arm,
abandon), resolver_authority.rs (106: choose_authority matrix),
resolver_verification.rs (88: verify_destination/no_later/final_later/
synchronize), resolver_result.rs (189: desired_result/problem,
published_output_result, record_cancellation, coordination_access),
replacement_resolver.rs (372: dispatch unlock/relock + pair inspection,
resolve_previous/desired/other/not_desired, removable_output,
desired_cleanup), reservation.rs (542: dual-block codec, select/
contains_selectable_header, Header/State/Policy/Previous),
reservation_file.rs (547: draft -> private -> canonical -> armed
lifecycle), reservation_verify.rs (113: exact custody checks),
reservation_inspection.rs (451: discover/canonical/exact_private/
scan_private, require_bound), file_inspection.rs (309: main/private/
private_owned classification), cleanup.rs (499: discard/discard_recovered,
link-count removal machine, Summary), attempt.rs (776: publish state
machine, not_published/outcome_unknown/finish_published, observed
checkpoints used by recovery/api.rs), main_file.rs (510: rename_main
per policy, synchronize/prove, retire_steps), output.rs (574:
CreatedOutput/OutputAttempt/PreparedOutput, resume_secured_output),
replacement.rs (228: PreviousMain bind/bind_no_rollback),
replacement_inspection.rs (340: two-inode Pair inspection, desired/
previous/other content), residue/ (linux.rs 457 + main.rs 137 +
retirement.rs 158: inspect/remove canonical residue), maintenance.rs
(396) + maintenance/{common,output,reservation}.rs (820: abandoned
temp/reservation list+remove), types.rs (342: full public result
surface), problem.rs (199: class mapping), namespace.rs (254) +
namespace_mutation.rs (329: rename_noreplace/exchange/
replace_discarding_destination/unlink_exact/sync) + namespace_scan.rs
(constant-memory readdir) + namespace_identity.rs (identity encoding).
Windows-only machines (gc.rs, gc_codec.rs, gc_maintenance.rs, gc_name.rs,
gc_barrier.rs require_source_available) verified as no-ops or refusal
arms for this port: gc_barrier is #[cfg(windows)] and compiles to
nothing on POSIX.

Go current state that composes: internal/writer/publication_staging.go
(730: one-shot CreateAttempt/Publish/Discard with a simplified
PublicationResult; callers internal/snapshot/snapshot.go:154,263 and
membership_publish_set.go:212,280), internal/live/directory.go
(retained-dir machine: entry/create/openRegular/requireAbsent/
verifyName/unlinkExact/sync/requireNameLengths), namespace_install_*.go
(dirfd renameNoReplace/renameExchange/renamePlain linux+darwin; other
arm refuses freebsd because the live sidecar refuses), internal/live/
lock.go (byte-range OFD locks linux/darwin; freebsd/windows refusal),
internal/mapping (MapFile, publish_link_noreplace.go path-based
FreeBSD link machine + mapping_publish_*.go path-based renames),
internal/security (CreatorMode, commitmentDomain IPR4PSEC,
SecureCreatorOnly, CreatorOnlyCommitment), internal/bootstrap
(open meta pages), internal/format (CRC32CWithZeroed, put/le helpers,
codes.go full ErrorCode table), internal/fault (Crash/Fail + v4work),
internal/random (Nonzero128), internal/work (test-only counters).

Problem / root-cause model: the Go immutable publish is one-shot
(path-based rename with a simplified result) while the Rust v4 format
owns an exact dual-block reservation inode, a state1/state2 arm
machine, a resolver that reconstructs the attempt from a supplied
result or a retained reservation, offline residue removal, and
abandoned-artifact maintenance. The one-shot path cannot resume after
a crash, exposes no reservation evidence, and classifies outcomes with
a reduced surface - a wire-format and crash-durability gap, not just a
missing API.

Affected contracts and surfaces:
- internal/writer public-to-internal surfaces consumed by snapshot and
  membership publish-set (CreateAttempt, Publish, OutputAttempt,
  PublicationResult, PublicationPreparationFailure, CleanupState,
  PublicationPolicy, DestinationContent, PublicationStatus).
- The on-disk reservation record (IPR4RSV1, 8192-byte file, 512-byte
  dual blocks, CRC32C at 508, section binary-format-v4.md) - Rust
  cross-open authority; Go must read and write the exact bytes.
- The destination namespace twin name (`<main>.readers`) and the
  private attempt/reservation names (.iprange-publish-*.tmp,
  .iprange-reservation-*.tmp).
- New public SDK surface (Rust publication.rs pub use + resolve_
  publication): PublicationResult full shape, PublicationProblem,
  resolve_publication + PublicationResolutionMode, residue inspect/
  remove, abandoned temp/reservation list+remove, Windows housekeeping
  refusal.
- Platform table: linux/darwin/freebsd supported (lock and rename
  machines), windows/non-POSIX typed refusal, netbsd follows the Rust
  no-primitive classes.

Existing patterns to reuse:
- internal/live retained-directory machine and ns_error classes
  (directory.go, ns_error.go); extend, do not fork, the machine with
  the freebsd arms (namespace_install_freebsd.go, openRegularAnyLink,
  finishNoreplaceTransition, scan) that Rust's publication/namespace
  provides even though the live sidecar refuses freebsd.
- internal/live lock.go for the byte-range operation lock; add the
  Rust live_lock lock_file surface (whole-artifact flock on freebsd,
  offset OFD on linux/darwin, refusal elsewhere) used on reservation
  and output files (MAIN_LIFETIME_LOCK 1<<44, OPERATION_LOCK 0).
- internal/security for creator-only commitments; internal/fault for
  the publication.* crash points; internal/random for attempt ids;
  internal/format CRC/put/decode helpers; internal/bootstrap for the
  finished-output meta proof; internal/mapping.MapFile for mapped
  reservation/output views (mmap-only, no read/write syscalls).
- Rust problem.rs class mapping verbatim through format.Error codes and
  detail strings (the Go port carries no os_code field, matching every
  earlier chunk).

Risk and blast radius:
- The retrofit touches the two production publish call sites (snapshot
  To, membership PublishSet) and their public results; keep the public
  call signatures and observable classifications, expand the result
  shape.
- FreeBSD no-replace moves from the path-based mapping machine to the
  dirfd machine; the mapping path-based publish machines
  (mapping_publish_*.go, publish_link_noreplace.go) become dead and are
  removed with their tests after the retrofit (single authoritative
  implementation).
- Crash-matrix and race windows widen: the 13 publication.* crash
  points need the subprocess crash harness (crash_v4work_test.go
  pattern) plus resolver resume tests; race tests must cover the
  lock/discover windows.
- Windows: all new surfaces are typed refusals (CodeOSUnsupported /
  CodePublicationUnsupported), no behavior change to the honest-refusal
  stance.
- Worker enter_output probes (Rust worker::enter_output before every
  mapped output access) have no Go counterpart yet: they are pure
  fault-containment no-ops on healthy files; the Go worker wiring lands
  with 4-11 and is recorded (no observable semantics change, matching
  chunks 4-1..4-7).
- The attempt observed/checkpoint variants (PublicationCheckpoint) are
  consumed only by recovery/api.rs (4-10) and the worker drive (4-11);
  Go 4-8 ports the plain variants and records the observed variants
  with those chunks.
- Resolver resume of `later` canonical reservations requires the
  live-sidecar classification in residue; internal/live already owns
  hasSelectableHeader/readSourceHeader (header.go:106).

Sensitive data handling plan: none; the chunk operates on file
identities, hashes, and paths only. No secrets, credentials, or
customer data enter durable artifacts.

Implementation plan (dependency-ordered slices; each slice keeps the
tree green under `nice go test ./...`):
- A types+problems: internal/publication types package
  (PublicationResult full shape, Attempt facts, fixed cleanup ledger,
  AccessPolicy/ArtifactKind/DestinationContent/LaterCanonical/
  Housekeeping/etc.) + problem class mapping (problem.rs).
- B reservation codec: internal/publication/reservation.go (offsets,
  decode order, select/contains_selectable_header, state2, attempt_eq,
  identity encoding) over mapped views only.
- C namespace machine: extend internal/live with the freebsd dirfd
  arms (renameNoReplace linkat machine, openRegularAnyLink,
  finishNoreplaceTransition) and the retained-dir scan; internal/
  publication destination.go (bind, commitment, names,
  require_fail_if_exists_available).
- D artifact locks: internal/live lockFile/unlockFile/lockFile
  Cancellable surface (freebsd flock, linux/darwin OFD, refusal
  elsewhere) used at OPERATION_LOCK and MAIN_LIFETIME_LOCK.
- E output+replacement evidence: created/secured/prepared output,
  digest cancellable, resumed output, PreviousMain bind/bind_no_
  rollback (replacement.rs).
- F reservation lifecycle: reservation_file.go draft/private/canonical/
  armed + reservation_verify.go custody checks + the 13 crash points.
- G reservation inspection: discover/canonical/exact_private/
  scan_private, require_bound, unlock_operation/relock_operation.
- H cleanup: discard_created/discard_attempt/discard_recovered with
  the exact link-count removal machine + Summary.
- I attempt+main_file: the publish state machine (fail_if_exists and
  replace_existing over the reservation), not_published/
  outcome_unknown/finish_published, rename per policy, prove, retire
  steps.
- J resolver core: resolver.go + authority.go + verification.go +
  result_builder.go (choose_authority, dispatch, complete_absent, arm,
  abandon, final_later, record_cancellation).
- K replacement resolver: unlock/relock, pair inspection,
  resolve_previous/desired/other.
- L residue: inspect/remove canonical residue with the retained handle
  and the final coordination-reuse proof.
- M maintenance: list/remove abandoned publication temps and
  reservation artifacts (constant-memory scan, exact evidence),
  Windows housekeeping typed refusals.
- N Publish retrofit + public surface: publication.Publish over the
  reservation path; snapshot.To and PublishSet moved to it; the
  one-shot machine and the mapping path-based publish machines removed;
  public ResolvePublication/Residue/Abandoned surfaces in iprangedb.
- O validation + gate: ported Rust tests, crash matrix, work counters
  where Rust exposes them, docs.

Validation plan: unit tests per slice (codec known-answer vectors with
hardcoded expected CRCs; select matrix; lifecycle states; inspection
conflicts; resolver authority matrix incl. supplied-vs-reservation
cases; replacement evidence; residue classes; maintenance evidence
guards). Crash matrix via the subprocess harness at every
publication.* point with resolver resume (Rust crash_tests.rs port).
Cross-open: Rust-written reservation fixtures selected and verified by
the Go codec (six fixture cross-opens extend to reservation files).
Full battery under nice: plain tests, race, vet, checkptr, gofmt,
six cross-compiles, mmap-trace, zeroalloc. Five-aspect adversarial gate
per the SOW review rules at chunk close.

Artifact impact plan: SOW records this design and the close-out;
AGENTS.md unchanged (retained-directory and lock authority notes live
in package docs); spec binary-format-v4.md gains the reservation record
only if the current spec lacks it (checked during slice B); docs and
skills unchanged; the tracked UNIX-socket destination class note is
resolved by the Rust-exact open_regular errno mapping (ENXIO -> Io,
ELOOP -> NotRegular -> Conflict), verified in slice C.

Recorded scope decisions (Rust is the baseline; no new user decision
was required to start, consistent with the M4 plan):
1. New internal/publication package (mirrors the Rust publication/
   module boundary) instead of growing internal/writer: writer keeps
   the OutputBuilder/Core, publication owns namespace publication
   facts; call sites compose (snapshot, publish-set, the future 4-10
   recovery api).
2. FreeBSD publication support is in scope on the reservation path
   (dirfd linkat machine + flock artifact locks), matching the
   existing offline/immutable freebsd support; the live sidecar
   refusal on freebsd is unchanged.
3. The public SDK surface (resolve_publication, residue, abandoned
   maintenance) ships in this chunk with the full result shape.
4. The mapping path-based publish machines become dead after the
   retrofit and are removed with their tests.
5. Worker enter_output probes and the observed checkpoint variants are
   recorded with 4-10/4-11, not stubbed here.
6. os_code is not carried in Go publication problems (format.Error
   parity with every earlier chunk); codes and detail strings are
   Rust-verbatim.

### Status (2026-08-23) - chunk 4-8 slice A implemented: publication types + problem mapping

Slice A delivered on the working tree:

- internal/publication/types.go: full Rust types.rs fact surface
  (PublicationPolicy/Status, DestinationContent (full 5-class),
  LaterCanonical, LiveLineage, AccessPolicy, CleanupState,
  ArtifactKind, DirectoryRole, CoordinationCleanup, Housekeeping +
  merge, HousekeepingState, ArtifactPresence, CreationSecurity,
  UnpublishedTailFacts, CleanupArtifact, fixed CleanupArtifacts ledger
  (capacity 4, overflow panics like the Rust assert), PreviousDestination,
  PublicationAttempt, PrivateOutputAttempt, HousekeepingArtifact,
  AbandonedArtifactRemoval, PublicationResult + cleanup_state,
  PublicationPreparationFailure + cleanup_state, AsProblem).
- internal/publication/identity.go: portable LocalFileIdentity
  (kind 1 + 32-byte device/inode encoding; decode rejects zero payload,
  nonzero tail, foreign kind; Rust namespace_identity.rs).
- internal/publication/problem.go: problem mapping (problem.rs): every
  NamespaceError arm Rust-verbatim (incl. LinkCount(0) "has no links",
  IoAt symlink fold, plain-Io fixed detail), output/reservation/
  replacement/main/sdk folds; os_code dropped (design decision 6);
  Windows gc and 4-10/4-11 checkpoint arms recorded, not stubbed.
- internal/live: nsError exported as live.NamespaceError (Rust
  publication::namespace NamespaceError peer; os.PathError-style
  exported facts Kind/Op/Links/Err) with the full 13-kind table;
  plain-Io vs IoAt split at the machine sites (directory.go
  "open directory"/"inspect directory"/"inspect retained file" are
  plain Io; all mutation/scan ops are IoAt); LinkCount now carries the
  observed count; IsNofollowSymlink exported for the problem surface.
  The live nsMap fold keeps its existing classes (WrongState ownership
  fold, DurabilityUnsupported, ForkedHandle passthrough).
- Tests: enum ordinal pins, housekeeping merge lattice, ledger
  capacity contract, result cleanup-state methods, identity codec
  known-answer vector + rejections, and every Rust problem arm.
- Validation: go build ./..., go vet ./..., go test ./... and
  -tags v4work live/writer/publication all PASS under nice.

Next slice: B reservation codec (reservation.go offsets, decode order,
select, contains_selectable_header).

### Status (2026-08-23) - chunk 4-8 slice B implemented: reservation codec

Slice B delivered on the working tree:

- internal/publication/reservation.go: the exact dual-block
  reservation codec (Rust reservation.rs, 542 lines read in full):
  fixed offsets, magic IPR4RSV1, 512-byte record in each 4096 page,
  dual-block select (single-valid with sequence==block+1, equal
  sequence requires byte-identical pages, adjacent 1->2 transition
  with attempt_eq, torn-newer fallback), contains_selectable_header,
  decode order fixed->reserved->checksum->state->policy->attempt->
  identity->output->previous->basename->security->sequence, state2
  derivation, encode with whole-page zeroing and CRC-32C seal over
  the page with the CRC field treated as zero. All reads run directly
  over mapped views; the decode path is allocation-free.
- Spec: binary-format-v4.md gained section 15A "Publication
  reservation file" (the spec lacked the reservation record; checked
  during this slice per the design). Values are Rust-verbatim; the
  table also pins the reserved-zero ranges, the absent/present
  previous layouts, and the selection rules.
- Tests: full port of Rust reservation_tests.rs (surviving-block
  authority, adjacent state2 + torn fallback, disagreement/gap/
  attempt-mismatch/invalid-transition rejections, policy-exact
  previous fields, reserved/kind/output fail-closed table, wrong-size
  vs CRC corruption, empty security commitment) plus independent
  CRC-32C known-answer vectors computed with a separate Python
  implementation (fail-if-exists 7bf19b18, replace a3026650,
  replace-no-rollback ad1e394f, state2 955492de) pinning the encode
  byte-exactness.
- Validation: go build ./..., go vet ./..., go test ./..., -tags
  v4work publication/live/writer, gofmt, -race publication/live all
  PASS under nice. Tree committed with this entry.

Next slice: C namespace machine (freebsd dirfd arms + retained-dir
scan) and destination.go (bind, commitment, names,
require_fail_if_exists_available).

### Status (2026-08-23) - chunk 4-8 slice C implemented: namespace machine (exported directory authority, freebsd linkat transition, retained-dir scan) + destination binding

Slice C delivered on the working tree:

- internal/live directory authority exported (Rust publication/
  namespace Directory is pub(crate); Go exports the live machine and
  internal/publication composes it): Directory with Entry/Create/
  OpenRegular/RequireAbsent/VerifyName/UnlinkExact/Sync/
  RequireNameLengths/Verify/Scan, RegularFile, Entry, FileIdentity
  stays opaque with the existing public.go conversions. All live
  call sites updated; Sidecar.close exported as Close (single naming
  convention for owned handles).
- New Verify + Scan (Rust Directory::verify + namespace_scan.rs):
  constant-memory readdir over ".", pre/post identity+filesystem
  verify, "." / ".." skipped, stream failures IoAt "open/read
  retained directory stream", post-verify takes precedence over a
  visitor error exactly like Rust.
- FreeBSD no-replace linkat transition machine (Rust
  namespace_mutation.rs freebsd arms): link_machine.go builds for
  freebsd OR the v4work test tag (Rust `test` cfg analog), carrying
  linkNoReplace/finishNoreplaceTransition/linkState/unlinkLinkAlias/
  proveLinkComplete/requireSource/requireLinkEntry/
  regularIdentityAnyLink/OpenRegularAnyLink and the four
  publication.freebsd.* crash points; namespace_install_freebsd.go
  dispatches RenameNoReplace to the machine, refuses RenameExchange,
  keeps renameat RenamePlain; namespace_install_other.go now
  excludes freebsd. The linked-pair fresh-link refusal and the
  EEXIST-resume semantics are pinned by v4work tests.
- Parity fix in renameNamespaceResult (pre-existing live bug): the
  conflict errno now maps to the caller's class, so exchange and
  plain renames report Missing on ENOENT instead of the Exists class
  (Rust rename_result passes the conflict error per operation).
- nofollow_windows.go added (Rust non-unix is_nofollow_symlink
  false arm): the exported IsNofollowSymlink surface now compiles on
  windows.
- internal/publication destination.go: Destination::bind port with
  raw-Path semantics (no Clean; Rust Path::file_name rules incl.
  the ".."-terminated None), validate_main_name (Rust path.rs:
  reserved .iprange- prefix and .readers suffix ASCII-case-
  insensitive), destination_names (<main>.readers twin), parent
  open with the live directory authority, require_name_lengths,
  basename commitment (SHA-256 over IPR4NAME + encoding u16le +
  length u32le + bytes; KAT 581c...3c20 pinned), creator security
  profile capture, output/reservation private names
  (.iprange-publish-/.iprange-reservation- + 32 lowercase hex +
  .tmp; zero attempt InvalidName), privateAttempt decode (lowercase
  hex only, zero rejected), require_fail_if_exists_available, and
  create/secureCreated/verifyCreated with the security-failure fold
  to the namespace classes (security_namespace.go).
- Tests: live directory scan facts, the freebsd transition matrix,
  destination name-rules table, raw-bytes binding + exact attempt
  name KATs, normative commitment vector, fail-if-exists-available
  twin refusals, private name round trips, and the linux-only
  exclusive nofollow creator-only creation test (Rust
  namespace_tests.rs port). Hygiene fix carried in this slice: the
  slice-A problem_test.go used unix.EIO/unix.ELOOP constants that do
  not exist on windows, breaking the windows test compile; the cases
  now use errors.New erps and a platform probe pair
  (problem_nofollow_unix_test.go / _other_test.go) so every target
  compiles and runs its own no-follow expectation.
- Recorded divergences (no behavior change): the Go security owner
  has no pure-Go ACL machine on darwin/freebsd (no cgo, Decision
  2A), so secure_created on those platforms refuses with the
  Unsupported class where the Rust libc ACL machine proceeds;
  tracked with the 4-12/M5 platform acceptance. The openbsd
  cross-compile failure in internal/mapping (publish_link_noreplace
  references linux/darwin-only Unlink/SyncDirectory helpers) is
  pre-existing at HEAD and disappears with the slice-N removal of
  the path-based publish machines.
- Validation: go build, go vet, go test ./..., -tags v4work ./...
  (14/14 packages ok each), gofmt clean, -race live+publication,
  checkptr=2, and the six cross-compiles (linux arm64/386, darwin
  amd64/arm64, freebsd amd64, windows amd64) all PASS under nice.
  Rust tree untouched (no re-run needed).

Next slice: D artifact locks (freebsd whole-file flock vs
linux/darwin offset OFD at OPERATION_LOCK and MAIN_LIFETIME_LOCK,
reusing internal/live/lock.go).

Slice D (2026-08-23) — artifact locks (Rust live_lock.rs port):

- internal/live/lock.go now exports the artifact lock surface that the
  publication owner will use: LockMode (LockShared/LockExclusive, the
  renamed Rust Mode), LockFile, TryLockFile, UnlockFile, and
  LockFileCancellable, dispatched through the new per-platform
  fileLockSet/fileLockUnlock vars; the sidecar byte-range surface
  (lock/tryLock/unlock/lockCancellable) stays package-private. The
  rename from lockMode/lockShared/lockExclusive is mechanical and
  touches only internal/live.
- Linux and macOS delegate the artifact surface to the existing OFD
  byte-range machine at the caller offset (Rust non-freebsd arm):
  lock_linux.go and lock_darwin.go inits now also assign
  fileLockSet/fileLockUnlock.
- lock_file_freebsd.go (//go:build freebsd): whole-file flock arm with
  LOCK_SH/LOCK_EX/LOCK_NB/LOCK_UN, EINTR retry, EWOULDBLOCK -> false on
  non-wait, other errors folded to CodeIO; the offset is ignored exactly
  like the Rust freebsd arm. lock_file_refuse.go
  (//go:build !linux && !darwin && !freebsd) refuses the artifact
  surface with the same CodeLiveCoordinationUnsupported refusal as the
  byte-range surface, so Windows publication refuses before path access
  (M5 tracked).
- lock_file_test.go (//go:build !windows, white-box): exclusive
  contention, release, shared coexistence, cancellable acquisition after
  release, and check-cancellation, at mainLifetimeOffset like the Rust
  main_file_tests/output_tests. The freebsd flock arm has identical
  observable semantics and is covered natively at the 4-12 platform
  acceptance; there is no v4work host emulation for it (flock is a
  trivial syscall and the refuse-file tag makes host emulation awkward).
- Validation: go build, go vet, go test ./..., go test -tags v4work
  ./... (14/14 packages ok each), gofmt clean, -race + checkptr=2 on
  internal/live, the six cross-compiles (linux arm64/386, darwin
  amd64/arm64, freebsd amd64, windows amd64), and per-OS test-compiles
  of internal/live and internal/publication for linux, darwin, freebsd,
  windows all PASS under nice. Rust tree untouched.

Next slice: E output and replacement evidence (open output for the
main file, replace-and-discard evidence, main_file_tests.rs ports).

Slice E (2026-08-23) — output and replacement evidence (Rust
output.rs + output_digest.rs + output_resume.rs + replacement.rs):

- internal/publication/output_digest.go: fixed-memory SHA-512 pass
  (digest/digestCancellable/digestWith) over mapped views only, 1KiB
  stack buffer, per-chunk + final checkpoints; the out-of-range and
  length arms carry the Rust-verbatim Conflict/FormatInvalid classes
  that problem.go maps (FinishedLengthChanged/FinishedMetaChanged/
  Bootstrap).
- internal/publication/finished.go: FinishedOutput (Rust
  immutable_output::Finished subset consumed by output.rs).
- internal/publication/output_created.go: CreatedOutput create/
  create_absent (random nonzero attempt id, .iprange-publish- private
  name, exclusive creator-only create), facts, secure
  (secureCreated: identity -> name proof -> creator-only policy ->
  re-identity -> verify_created). The Rust windows collision-retry
  loop is unreachable in Go (windows refuses at bind; M5 recorded).
- internal/publication/output_attempt.go + output_prepared.go:
  SecuredOutput/intoParts, OutputAttempt facts, prepare_cancellable
  (custody -> lifetime lock -> inspect_finished -> digest ->
  finish re-proof), PreparedOutput verify_private/verify_main/
  verify_destination_before_main, inspect_exact (two meta pages via
  bootstrap ModeImmutableReader), verify_custody (gc_barrier is a
  POSIX no-op per Rust cfg(windows); windows refuses earlier).
- internal/publication/output_resume.go: resume_secured_output /
  resume_secured_output_for_cleanup / bind_secured_output with the
  exact fact checks (encoding kind, identity decode, directory
  identity, basename, security commitment) and Rust-verbatim
  InvalidArgument details.
- internal/publication/replacement.go: PreviousMain bind /
  bind_no_rollback (missing main -> Missing, same inode ->
  SameIdentity, lifetime lock, sync, read-only view, cancellable
  digest, pre/post canonical proofs) and the three retention proofs
  (canonical_namespace/private_or_retired/retired/content).
- internal/live exports for the machine: RegularIdentity /
  RegularIdentityAnyLink / RegularLinkCount (unix arms in
  identity_helpers_unix.go, refusal arms elsewhere), SyncFile
  (F_FULLFSYNC darwin arm), MainLifetimeOffset, Checkpoint. The
  identity helpers moved out of link_machine.go (freebsd||v4work
  tag) so the machine compiles on every target; link_machine_test.go
  references updated.
- Recorded deferrals with their later chunks: worker enter_output
  probes stay with the 4-10/4-11 observed checkpoints (no observable
  semantics; problem.go already maps the classes), the gc barrier is
  the Rust #[cfg(windows)] no-op, and the secure-created darwin/
  freebsd ACL refusal stays the recorded 4-12/M5 item.
- Tests (linux-tagged where the pure-Go creator-only ACL runs; digest
  tests !windows): byte-visit order/KAT/cancellation, created facts +
  name shape, absent main refusal, hard-link refusals at secure and
  prepare, meta-change refusal, exact digest + retained lifetime
  lock + release on close, resume + cleanup-evidence round trips,
  missing/same-identity/content-changed/cancelled replacement binds.
- Validation: go build, go vet, go test ./..., go test -tags v4work
  ./... (14/14 packages ok each), gofmt clean, -race + checkptr=2 on
  internal/live + internal/publication, the six cross-compiles
  (linux arm64/386, darwin amd64/arm64, freebsd amd64, windows
  amd64), and per-OS test-compiles of the touched packages for
  linux, darwin, freebsd, windows all PASS under nice. Rust tree
  untouched.

Slice F (2026-08-23) — reservation lifecycle (Rust
reservation_file.rs 547 + reservation_verify.rs 113, read in full):

- internal/publication/reservation_verify.go: the three-part custody
  proof (verify_inode / verify_location / verify_contents) over the
  mapped reservation view only, with the canonical private-name
  absence rule and the reservationExpected/select_exact header-changed
  and codec classes. The Rust gc_barrier availability call is the
  #[cfg(windows)] no-op on POSIX (recorded with the Phase-2 GC
  surface, matching verify_custody in slice E).
- internal/publication/reservation_file.go: the full lifecycle owners
  with the exact Rust failure owners: reservationDraft (create),
  privateReservation (initialize: prepare_header -> write_state1 ->
  lock_state1_with with the operation lock), canonicalReservation
  (acquire: verify -> no-replace rename -> directory sync -> canonical
  re-proof; resume_armed), armedReservation (arm: state2 derivation,
  page-1 encode/flush, sync, select, canonical re-proof) with
  verify_before_main/verify_after_main, and the acquiring/arming
  failure-owner structs preserving namespace_call_started and
  state2_selected for the slice-H cleanup machine. Header builder
  carries the exact evidence (database/txn/nonce, attempt, identities,
  policy, byte length, sha512, previous, basename/security
  commitments, sequence 1); the Rust basename_len try_from overflow
  arm is unreachable after the bind name-max proof and recorded so.
  The observed checkpoint variants and worker enter_output probes
  stay recorded with 4-10/4-11; plain variants only, like slice E.
- The six reservation crash points are live:
  publication.after_reservation_state1_sync / after_reservation_
  rename / after_reservation_directory_sync / after_reservation_
  state2_write / after_reservation_state2_sync / after_reservation_
  state2_selection (the plan's "13 publication crash points" wording
  spans the whole machine: the 4 freebsd.* points shipped with slice
  C and the 9 main_file points land with slice I).
- Tests: port of the plain Rust reservation_file_tests.rs tests
  (7/9; the two after-selection checkpoint-injection tests are the
  observed variants recorded with 4-10/4-11) - exact header / 0600
  mode / operation-lock contention, acquire+arm inode and state-2
  selection, canonical Exists conflict with the namespace_call_started
  owner, AccessPolicy initialization refusal with the never-truncated
  owner, hard-link LinkCount(2) refusals at prepare and arm,
  existing-main Exists refusal before state 2 - plus a Go-added
  resume_armed header-invariant gate with the on-disk-reconstructed
  canonical resume path, and the Rust crash matrix
  (reservation_crashes_leave_one_complete_output_and_selectable_
  authority): each of the six points exits the child with Rust's
  code 86 and the parent proves the private/canonical placement, the
  selectable state (either at state2_write), the output-identity
  binding, and the complete fixture output via bootstrap.
- Validation: go build, go vet, go test ./..., go test -tags v4work
  ./... (14 packages ok each; internal/fault, internal/snapshot, and
  internal/work report no test files), gofmt clean, -race +
  -gcflags=all=-d=checkptr=2 on internal/publication + internal/live,
  the six cross-compiles (linux arm64/386, darwin amd64/arm64,
  freebsd amd64, windows amd64), and per-OS test-compiles of the
  touched packages for linux, darwin, freebsd, windows all PASS under
  nice. Rust tree untouched.

- Five-aspect adversarial review at slice close (HEAD b542d48):
  parity PASS (Dewey), idioms PASS with 3 P3 (Peirce; the test
  double-close and the helper fd-leak fixed, the verifyCanonicalAt
  reference alias kept for Rust-name traceability), performance PASS
  (Einstein), wire/integrity PASS with byte-exact CRC re-derivation
  (McClintock), records FAIL then fixed (Pasteur): the validation
  package count was corrected to 14 ok + 3 no-test-file packages,
  the reservationExpected name replaced the phantom ExactExpected,
  and the test port claim was narrowed to 7/9 plain tests with the
  two after-selection checkpoint-injection tests recorded with
  4-10/4-11. Production code unchanged by the fixes; tree re-tested
  green after them.

Slice G (2026-08-23) — reservation inspection (Rust
reservation_inspection.rs 451 lines, read in full; Rust
gc_barrier.rs require_source_available verified as the #[cfg(windows)]
no-op on POSIX, so the Go port omits it exactly like every earlier
slice). IMPLEMENTED on the working tree; review not yet dispatched.

Production code:

- internal/publication/reservation_inspection.go: the exact discovery
  machine. inspectedReservation (name/file/mapping/identity/header/
  location/access + Close/verify/unlockOperation/relockOperation),
  discoverReservation (canonical first, then scan_private, then the
  coordination-absent re-proof with the found owner closed on the
  conflict), inspectedCanonical, exactPrivateReservation,
  inspectCanonicalReservation, scanPrivateReservations,
  inspectPrivateReservation, requireBound, mapReservation (exact
  8192-size proof -> Invalid marker), readSelected (select refusal ->
  Invalid marker), inspectedReservationOf (creator-only classification
  via security.CreatorOnlyCommitment), lockOperation/lockOperationFile,
  invalidPrivateEntry (NotRegular / LinkCount / CrossFilesystem /
  nofollow-symlink IoAt -> skip), strictRecord (Invalid -> the fixed
  Unresolvable problem, SDK errors fold through sdkProblem to the fixed
  "publication SDK operation failed" detail - the Rust
  ReadError::Sdk arm), conflictProblem / destinationNameMismatchProblem
  with the Rust-verbatim detail strings.
- Resource ownership: every error and skip path closes the opened
  regular descriptor and the mapped view exactly like the Rust drop of
  Regular and Mapping (Go has no drop; explicit closeReservationOwner
  on each arm). The skip arms of inspectPrivateReservation and the
  multiple-bound and coordination-changed conflicts close the owned
  candidates; the ownership audit in the chunk plan was completed
  before the tests (no fd/mapping leaks on any refusal arm).
- internal/publication/reservation_inspection_freebsd.go + _other.go:
  the per-OS joints: freebsd OpenRegularAnyLink + FinishNoreplace
  Transition, POSIX OpenRegular + no-op finish (the atomic rename
  leaves nothing to finish).
- internal/live/link_machine.go: finishNoreplaceTransition exported as
  FinishNoreplaceTransition (the publication freebsd arm is another
  package and must call the exported name); link_machine_test.go
  updated to the exported name.

Correctness choices applied during the port (vs Rust):

- exact_private locks the operation lock BEFORE verify_name and
  require_bound, in the Rust order.
- inspect_private recheck after the lock is strict (changed ->
  Conflict "private reservation changed during inspection"), never a
  skip; the require_bound and record-read skip arms are the only
  Ok(None) paths, exactly like Rust.
- inspect_canonical re-checks after the per-OS finish; canonical name
  verified after the transition.
- gc_barrier availability calls omitted on POSIX (verified no-ops in
  Rust gc_barrier.rs).

Rust-verbatim problem details carried: "publication reservation changed
after inspection", "publication reservation changed while acquiring its
lock", "publication reservation changed during inspection", "private
reservation changed during inspection", "reservation self identity does
not match its inode", "private reservation name has another attempt
id", "multiple bound private publication reservations exist",
"coordination changed during reservation scan", "caller result and
private reservation disagree", the Unresolvable "publication
reservation record is not selectable", the DestinationNameMismatch
"reservation belongs to another destination name", and the fixed
sdkProblem detail.

Tests (new files):

- reservation_inspection_test.go (linux): canonical discover on an
  acquired reservation and on an armed state-2 reservation (location,
  private name derived from the attempt, identity, header, creator-only
  access, held operation lock, verify), exact_private on an
  initialized private reservation (lock held, exact header,
  creator-only), exact_private caller-result disagreement -> Conflict,
  the private-scan skip matrix with one inode per malformed class
  (wrong size, unselectable, extra hard link, foreign attempt name)
  proving each skip arm on its own, multiple-bound private
  reservations -> Conflict, coordination-appears-during-scan ->
  Conflict (via the checkpoint injection), the require_bound mismatch
  classes (self-identity / filename-attempt / basename length /
  basename commitment) plus the accept arm, unlock/relock round trip
  (contender can lock between, not after relock), relock after an
  external lock steal with a selectable record rewrite -> the
  changed-after-inspection Conflict, creator-only vs chmod 0644 ->
  ChangedOrUnproven classification, and the malformed-canonical zone
  (zeros at the coordination twin -> Unresolvable, never a private-scan
  fallback, private reservation bytes untouched). A leak probe runs
  the wrong-size skip arm 64 times.
- reservation_inspection_v4work_test.go (v4work && linux): the Rust
  crash discover matrix after all six reservation crash points
  (placement, state, output-identity binding, creator-only access,
  held lock, verify), the state2_write either-state selection, two
  state1 crash artifacts -> the multiple-bound Conflict (Rust
  private_scan_requires_one_unique_bound_reservation), the malformed
  canonical zeros -> Unresolvable with the file byte-identical (Rust
  malformed_canonical_reservation_is_not_private_scan_authority), and
  the cancellable foreign-entry scan.
- freebsd-only transition tests deferred to 4-12 platform acceptance
  (the v4work tag cannot emulate freebsd), recorded with the slice.

Validation (all under nice): go build ./..., go vet ./..., go test
./... (14/14 packages ok), go test -tags v4work ./... (14/14 ok;
internal/fault, internal/snapshot, internal/work report no test
files), gofmt clean, -race + -gcflags=all=-d=checkptr=2 on
internal/publication + internal/live, the six cross-compiles (linux
arm64/386, darwin amd64/arm64, freebsd amd64, windows amd64), and
per-OS test-compiles of the touched packages for linux, darwin,
freebsd, windows all PASS. Rust tree untouched. FreeBSD arm verified
with GOOS=freebsd build + vet after the FinishNoreplaceTransition
export.

Slice G CLOSED at HEAD 6f3ad8c (2026-08-23): first review round at
7fe04f8 (parity PASS, idioms FAIL, performance FAIL, wire PASS,
records PASS) and the fixes commit 6f3ad8c, re-reviewed at 6f3ad8c:
all five aspects PASS (Dewey parity PASS with no regressions in the
delta; Peirce idioms PASS after the ownership unification and test
fixes with two optional P3s; Einstein performance PASS after the
value-read and zero-alloc-scan fixes, F3 accepted as recorded;
McClintock wire PASS with no wire-format change and the GOROOT-
mirrored dirent parser verified per OS; Pasteur records PASS with
one P3 wording nit on the mapping.rs citation). Residual P3s are
optional and recorded with the slices. Slice G is complete; next is
slice H (cleanup).

Five-aspect adversarial review at HEAD 7fe04f8 (first round;
(2026-08-23): parity PASS (Dewey; P3: sentinel comparisons hardened
with errors.Is), idioms FAIL then fixed (Peirce): the orphaned
inspectPrivateReservation test call that leaked an owned inspected
reservation was removed, exactPrivateReservation was unified to the
same named-return + defer ownership pattern as its two siblings (the
explicit per-arm closes and the dead named return are gone), the
hand-rolled test itoa was replaced with strconv.Itoa, the
operation-lock assertion now fails instead of passing vacuously, and
the cancellable-scan test now exercises the per-entry scan
checkpoints. Performance FAIL then fixed (Einstein): readSelected now
returns selectedReservation by value (zero allocation, Rust parity)
instead of a ~370-byte heap object per probe, and the retained
directory scan was rewritten from os.File.Readdirnames(1) to one
reused 32 KiB getdents/getdirentries buffer with a zero-alloc dirent
parser (constant memory, no per-entry allocations or syscalls, Rust
namespace_scan.rs parity; the live scan test suite still passes
unchanged). The performance finding that mapReservation "double
stats" was re-examined and rejected: Rust map_reservation performs
two metadata calls itself (the exact-size check at
reservation_inspection.rs:394-399 and require_file_extent inside
Mapping::view at mapping.rs:326-333), so Go's stat inside
mapReservation plus the fstat inside MapFile is parity; the only
syscall delta is the dup + cloexec fcntl (+1 close at drop) of the
pre-existing accepted Go mapping-owner lifetime design, unchanged
since the mapping slice. Wire/integrity PASS (McClintock; note for
slice H: machine-produced problems must pass through the composition
folds unchanged; raw namespace/SDK errors fold once at the
boundary). Records PASS (Pasteur; the close-out entry will record
this fix commit identity). All fixes re-validated under nice: build,
vet, plain + v4work tests (17 packages: 14 ok + 3 no-test-file -
fault/snapshot/work), gofmt clean, race + checkptr=2 on
publication + live, six cross-compiles, per-OS test-compiles for
linux/darwin/freebsd/windows all PASS. Rust tree untouched.

Slice plan after G: H cleanup (discard_created/discard_attempt/
discard_recovered + link-count removal machine + Summary; Rust
cleanup.rs 499), I attempt+main_file publish state machine
(attempt.rs 776 + main_file.rs 510), J resolver core (resolver.rs 435
+ resolver_authority 106 + resolver_verification 88 + resolver_result
189), K replacement resolver (replacement_resolver.rs 372), L residue
(residue/linux.rs 457 + main.rs 137 + retirement.rs 158), M
maintenance (maintenance.rs 396 + common/output/reservation 820), N
Publish retrofit + public surface + delete dead mapping publish
machines (one-shot writer/publication_staging.go + snapshot.go:154,263
+ membership_publish_set.go:61,212,280; explicit user approval needed
before deleting files), O validation + gate + push. Do not push
until chunk 4-8 completes and the five-aspect gate passes.

### Status (2026-08-23) - chunk 4-8 slice H implemented: publication cleanup machine (discard + seed)

Slice H ports the exact-discard machine (Rust publication/cleanup.rs,
499 lines) and the publication result seed (Rust result.rs Seed,
result.rs:71-140; NameSlot at result.rs:21, take_name at
result.rs:292-301) to Go. The machine discards
created, attempted, prepared, and recovered direct-publication
artifacts before main publication: one removal proves the retained
name went away, the directory sync + verify run behind the
DirectorySync checkpoint, and any removal that cannot be proved pushes
one exact cleanup artifact into the result ledger (Rust
CleanupArtifacts fixed capacity 4).

Go files (all !windows; Windows publication opens refuse at destination
bind per M5, so the Rust cleanup/windows.rs gc-transition arm is
intentionally absent):

- internal/publication/cleanup.go (508 lines): ownerLocation (Rust
  ReservationLocation Private/Canonical/Either), reservationOwner
  (identity Option arm), outputOwner, cleanupSummary (artifacts +
  main/coordination absence flags), earlyDiscard, cleanupPoint (Rust
  Point), discardCreated/discardAttempt/failedAttempt/confirmedAbsent,
  discardWith/discardRecovered/discardOwnersWith, the remove machine
  (removeFile/removeOutput/removeReservation/unlinkNames/
  requireUnlinked/links), finishOne/finishRemoval, removal +
  removalState, defaultSlot, earlyArtifact. Every Rust detail string is
  verbatim: "private output identity was not established", "owned
  publication artifact has unexpected links", "unlinked publication
  artifact still has links", "owned publication artifact has no exact
  retained name", "private output removal was not proved",
  "publication artifact removal was not proved", plus the panic texts
  "cleanup requires one exact name" and "each artifact name is consumed
  once".
- internal/publication/seed.go (154 lines): nameSlot (Rust NameSlot),
  seed with the full capture field set (database/transaction/nonce/
  attempt/directory identity/destination basename/output identity and
  digest/policy/previous/creation security/private basename + one-shot
  name inventory), captureSeed (Rust Seed::capture), seed.artifact
  (Rust Seed::artifact), takeName (one-shot slot consumption), and
  publicPolicy.

Behavioral parity notes (Rust baseline):

- remove() contract: links 0 -> awaiting sync; exactly 1 -> unlink the
  candidate names in Rust order; any other count -> the fixed
  unexpected-links conflict, namespace untouched.
- unlink_names: Ok(true) proves the slot; Missing/IdentityChanged/
  NotRegular candidates are skipped; LinkCount is a hard namespace
  error; any other failure is kept as the first problem and reported
  only when no name could be unlinked (Rust first_problem).
- discard_owners_with: each owner runs behind its checkpoint; a
  checkpoint or removal failure becomes Removal::failed with the
  default slot of the owner; one shared directory sync proves every
  success (needs_sync = output or reservation), and the finish order is
  output first, reservation second, exactly like Rust.
- remove_reservation infers a missing owner identity from the open file
  (Rust regular_identity arm); the canonical name set order is
  canonical-first for Canonical and private-first for Either.
- main_absent/coordination_absent report the destination require-absent
  proofs (is_ok == nil), computed after the removals, in Rust order.
- machine-produced problems pass through composition folds unchanged;
  raw namespace and SDK errors fold once at the boundary (no double
  wrapping).
- discard_recovered passes the always-ok checkpoint closure, exactly
  like Rust: recovery paths never re-sync through this machine.

No crash matrix for slice H: Rust cleanup.rs contains no fault::crash
points; reservation retirement crash points belong to slice L (residue),
where the v4work matrix lands. The EarlyDiscard identity-not-established
arm and the require_unlinked link-proof arm are covered by direct unit
probes because they cannot be reached through real unix namespace state
(identity decode is best-effort over a regular same-filesystem inode;
a third name appearing between unlink and re-prove is a race).

Tests (internal/publication/cleanup_test.go, linux, 794 lines, plus the
allocation pin in cleanup_alloc_test.go, 41 lines):

- discardCreated on a real created output (name gone, links 0, facts
  carried), discardAttempt on a secured attempt, failedAttempt
  (artifact facts exact, no namespace work), confirmedAbsent (no
  namespace work), the identity-not-established conflict.
- discardWith output-only and with private/canonical/either
  reservation owners: checkpoint order [OutputRemoval,
  ReservationRemoval, DirectorySync], both names removed, ledger empty,
  absence flags; the already-absent output arm (links 0 -> awaiting
  sync); canonical location removing the private name when the
  canonical twin never appeared; canonical location removing the
  canonical name after rename; either location preferring the private
  name.
- Conflict arms: unexpected links (two names on the reservation inode)
  is read-only (both names remain, links stay 2) and pushes the exact
  artifact; no exact retained name after the retained name is renamed
  away; require_unlinked with links 2; finishOne with the name still
  present; finishRemoval with links 1 (publication artifact removal was
  not proved) and with an injected sync problem (sync wins, Rust
  or_else order).
- Checkpoint arms: failure at OutputRemoval (no namespace work, artifact
  carries the injected problem), failure at ReservationRemoval (output
  still removed, sync still runs, artifact slot defaults to the
  coordination name for Canonical), both failures consuming the two
  distinct name slots; the seed name-slot double consumption panics.
- discardRecovered removes both owners with no checkpoint; identity
  inference for a reservation owner without identity; absence flags
  with retained main/coordination names.

Validation (all under nice): go build ./..., go vet ./..., go test
./... (17 packages: 14 ok + 3 no-test-file - fault/snapshot/work), go
test -tags v4work ./... (14 ok + 3 no-test-file - fault/snapshot/work),
fresh -count=1 publication tests, gofmt
clean, -race + -gcflags=all=-d=checkptr=2 on internal/publication +
internal/live, the six cross-compiles (linux arm64/386, darwin
amd64/arm64, freebsd amd64, windows amd64), and per-OS test-compiles of
publication + live for linux/darwin/freebsd/windows all PASS. Rust tree
untouched. No deferrals. The slice-G review rounds also noted optional
P3s (scan buffer pooling for Directory.Scan, a big-endian portability
note, a test itoa alias, Directory.Scan doc lifetime wording); they are
not cleanup-machine work, they stay optional, and this entry does not
claim they are recorded elsewhere.

Slice H review round 1 at HEAD 8911450 (2026-08-23): parity PASS
(Dewey, no findings), idioms PASS (Peirce, one P3 removed), performance
FAIL (Einstein), wire PASS (McClintock, three P3s), records FAIL
(Pasteur, two P2 citation defects). The reviews also listed optional
P3s (dead test bindings, missing direct LinkCount-arm test, wording).

Performance FAIL findings (review F1/F2): the success path of one
discard heap-allocated 3-4 objects while Rust allocates zero. Go
emulated Rust Option<Identity> with pointers into local values
(awaitingSyncRemoval identity escape, discardOwnersWith reservation
identity copy, the discardWith outputOwner literal, and the
PrivateOutputAttempt identity pointer built by outputFacts). Fixed by
introducing the value identityOptional (present flag + value, Rust
Option<Identity> Copy semantics) for the machine structs and a flat
Identity + IdentityPresent pair in the shared PrivateOutputAttempt
fact shape: the machine success path is now allocation-free, and the
new TestDiscardWithZeroAllocations pin proves it. The pin asserts
exactly two allocations per discard, both the x/sys
ByteSliceFromString NUL-termination copies of the two require-absent
name probes: Go's runtime copies every name per syscall while Rust
amortizes it in the Name CString at construction. This is a
runtime-syscall-boundary trait (the Go standard library pays it too),
not machine logic; the machine itself is at zero allocations. Review
F3 (seed.artifact portable-identity pointer, failure path only) stays
as the accepted optional P3.

Records FAIL fixes: corrected the NameSlot citation (result.rs:21 and
take_name at 292-301, not 241-262) and reworded the phantom slice-G
residual-P3 cross-reference so it no longer claims the optional P3s
were recorded with slice G; the v4work validation phrasing now matches
the plain-test clause (14 ok + 3 no-test-file). The counts in the
slice-H entry were refreshed to the fix commit (cleanup.go 508,
seed.go 154, cleanup_test.go 794) and the 41-line cleanup_alloc_test.go
is named in the test list.

Idioms and wire P3s fixed: the dead test bindings were removed, the
test fixture wording reads "fully prepared" now, and the missing
direct arm test was added (TestUnlinkNamesLinkCountHardError pins the
hard LinkCount namespace fold, which a race can otherwise reach only
between the pre-unlink link probe and UnlinkExact). All fixes
revalidated under nice: build, vet, fresh publication tests, v4work,
race + checkptr=2, gofmt, six cross-compiles, per-OS test-compiles
all PASS. Re-review dispatched at the fix commit; verdicts recorded
here when the rounds complete.

Slice H CLOSED at HEAD 441a40d + the records fix on the working tree
(2026-08-23): round 2 re-review at 441a40d - parity PASS (Dewey,
machine zero-alloc verification and full regression sweep clean; two
cosmetic P3s: a bare checkptr-only run would skew the pin, and the
identityOptional field shadowing reads slightly awkward), idioms PASS
(Peirce, value-option shapes and flat facts pair natural; optional:
could add a someIdentity helper, pin count is Go-version sensitive by
nature), performance PASS (Einstein, F1/F2 resolved with escape
analysis on HEAD clean - the only remaining success-path allocation is
the Rust-parity basename copy; the exactly-two pin is honest and
stable; F3 stays an accepted failure-path P3), wire PASS (McClintock,
no wire/locking/durability change, P3-2 test added and read-only
proved, P3-1/P3-3 accepted as recorded), records FAIL then PASS
(Pasteur: all round-1 citation fixes verified, one new P2 - stale line
counts in the H entry - fixed on the working tree and re-verified
exact: cleanup.go 508, seed.go 154, cleanup_test.go 794,
cleanup_alloc_test.go 41). Slice H is complete; next is slice I.

Chunk-level P3 sweep item (recorded here, not slice H work): Einstein
noted the same identity-pointer escape class at
v4/go/internal/publication/reservation_file.go:109 (prepareHeader
stores d.identity = &identity once per reservation draft; slice F
territory, publish-attempt frequency). The identityOptional value type
introduced here is the fix; apply it during the O validation sweep or
when the attempt/main_file slice touches that owner next, and verify
with the same -m=2 evidence.

### Status (2026-08-24) - chunk 4-8 slice I implemented: attempt + main_file publish state machine

Slice I ports the one-shot publication machine (Rust publication/attempt.rs
776 lines + main_file.rs 510 lines) to Go: the fail-if-exists and
replacement flows composed from the explicit ownership states, the
atomic main-name publication per policy, the exact retirement of the
reservation and any replaced previous, the checkpoint/observer surface
(the slice-F deferral for the observed checkpoint variants and the
two after-selection checkpoint-injection tests is closed here; the
worker enter_output probes stay recorded with the 4-10/4-11 worker
slices and remain mapped there, absent by design at their Go sites),
and every failure class with the exact cleanup ledger. Rust is the
mandatory baseline; the Go machines mirror the Rust owners and outcome
classes one to one.

Go files (all !windows; the Rust windows gc-transition arms of
main_file.rs stay intentionally absent: Go publication refuses Windows
opens at destination bind per M5):

- internal/publication/attempt.go (450 lines): failIfExistsCancellable,
  failIfExistsCancellableObserved, replaceExistingCancellable,
  resumeArmed, publishWithObserver, fromPrivate/fromCanonical/fromArmed,
  the four observe* checkpoint builders (observePreparation/
  observeNotPublished/observeOutcomeUnknown/observePublished with the
  interruptedProblem detail "mapped output fault interrupted
  publication"), preparation/notPublished/outcomeUnknown/finishPublished
  (the Rust finish_published_observed surface is the windows-only
  housekeeping-observed retire, absent per M5), the owner builders
  (draft/acquiring/canonical/arming), optionalFrom, cleanupPointOf,
  cleanupIgnoresCancellation. Every terminal path closes the
  machine-owned reservation (file, mapping, operation flock) exactly
  where Rust drops the moved owner; the Go two-value return
  (PublicationResult, *PublicationPreparationFailure) is the peer of
  Rust Result<_, Box<PreparationFailure>>.
- internal/publication/main_file.go (361 lines): publishProved/
  publishObserved/publishWith/publishSteps/verifyBeforeMain/renameMain/
  synchronizeMain/proveMain, retire/retireMain/retireSteps/
  verifyPublished/unlinkPrevious/unlinkReservation/syncRetirement/
  verifyRetired, the mainAttempt/publishedMain/retiringMain/
  publishedOutput owners, and all seven main-file crash points at the
  exact Rust steps (publication.after_main_rename/sync/directory_sync/
  proof, after_previous_unlink, after_reservation_unlink,
  after_retirement_sync; the six reservation-file crash points keep
  their F/G records and the 13-point matrix below). The post-unlink link-count conflict arms
  return cleanupConflictProblem with the Rust-verbatim details
  ("retired previous destination still has a link" / "retired
  reservation still has a link").
- internal/publication/seed.go (+76): finalState, result(),
  resultWithHousekeeping(), preparationWithHousekeeping() (Rust
  result.rs 155-266).
- internal/publication/problem.go (+27): checkpointProblem wrapper and
  asCheckpointProblem (Rust Error::Checkpoint clone-through);
  reservationProblem/mainProblem unwrap it first.
- internal/publication/reservation_file.go (modified): plain
  initialize/acquire/arm are now thin wrappers over the observed
  machines (nil checkpoint); the two after-selection
  checkpoint-injection tests of the Rust suite are ported here too;
  this slice flattens every machine owner to a value and every
  success return to a value so the success path stays on the stack
  (Rust moves values; the failures copy the owner value into the heap
  failure). Go signature note: the machine takes *preparedOutput and
  value reservations (caller ownership of the file descriptors; Rust
  moves the PreparedOutput into the machine).

Go tests:

- main_file_test.go (Rust main_file_tests.rs arms): the clean
  publish+retire facts, the post-rename checkpoint failure retaining
  the ambiguous complete main, the verify-stage single-link custody
  refusals when a hard alias pre-exists retirement (Rust classifies
  them at verify_published/verify_private_or_retired with
  NamespaceError::LinkCount, verified against the running Rust suite),
  and the exchange/retire flow. The post-unlink PreviousLinkCount/
  ReservationLinkCount classes are race arms in both implementations;
  their exact problem surface is pinned via TestCleanupConflictProblem
  Mapping.
- attempt_test.go (Rust attempt_tests.rs arms): success facts + no
  residue, pre-boundary preparation failure with exact cleanup, the
  state1/acquire/state2 failure classifications, the main-race after
  state2 (outcome unknown, never overwrites), shared and individual
  cleanup-failure ledgers, the foreign-coordination and existing-main
  refusals, the published reservation-link conflict residue, the
  post-proof published retention, the replacement publish/retire and
  the replacement path race before state 2, and the two resumeArmed
  outcome classes (before rename publishes; after rename holds the
  sure-to-be untouchable outcome-unknown); the outcome-unknown flock
  release, which pins the Rust drop of the failure owner (an abandoned
  reservation flock would stall the slice-J resolver's lockOperation
  forever), and the pre-lock state-1 failure descriptor release
  (TestAttemptState1FailureReleasesTheDraftFiles; /proc/self/fd
  accounting - Go has no drop, the machine closes the draft).
- attempt_alloc_test.go: the fromPrivate success-path allocation pin
  (Rust post_boundary_success_allocates_no_heap), measured exactly
  once with MemStats like the Rust count_thread_allocations (the
  machine consumes the reservation, so AllocsPerRun's warmup-repeat
  loop would measure a changed state). The pinned budget is 58 objects,
  all accounted: every one is an accepted x/sys boundary conversion
  (the name and attribute NUL-copies of the Entry/VerifyName/
  RequireAbsent/UnlinkExact/RenameNoReplace probes and the Fgetxattr
  attribute-name copies, about 20 in-window), a rename boundary class,
  or the portable result plumbing (attempt_alloc_test.go:63-72);
  escape analysis shows zero machine-logic escapes on the success
  path. To get there the slice also flattened the machine owners to
  values, added the value-returning bootstrap.OpenMeta core (Rust
  open_meta_pages; Open keeps its pointer surface for the reader),
  replaced the machine-path os.File.Stat calls with raw unix.Fstat
  (fstatSize), made the security commitment use sha256.Sum256 on the
  stack (Rust commitment is register-only), and switched
  live.Directory.Sync to raw Fsync (Rust libc fsync). The pin excludes
  race and v4work builds exactly like the discard pin: race/checkptr
  and the crash harness allocate inside the measured path themselves.
- attempt_crash_v4work_test.go (Rust crash_tests.rs): the 4
  after_main_* points leave the complete desired main with the
  selectable canonical reservation (state MainMayHaveBeenAttempted,
  output identity bound) and no private artifacts; the 2 retirement
  points leave the complete main with no coordination; the 13-point
  replacement matrix preserves the previous bytes before the rename,
  the previous at the private name after it, and the retired state at
  the retirement points - all with child exit 86 at the exact step.
  The Rust ImmutableReader::open refusal while the reservation is
  armed is wired with the reader authority slices (J resolver core, N
  publish retrofit), recorded here, not stubbed.

Design notes:

- Gofmt clean; all Rust-verbatim problem details in place; the Go
  machines hold no dead code and the checkpointFailure wrapper is
  shared by the reservation and main-file machines (same package).
- Chunk-level P3 sweep item (reservation_file.go:109 identity-pointer
  escape) is still recorded for the O validation sweep, untouched this
  slice (the draft identity owner is outside the measured success
  path).
- Validation (all under nice): go build ./..., go vet ./..., plain and
  v4work test batteries (14 packages ok each + the 3 no-test-file
  packages), gofmt clean, -race + -gcflags=all=-d=checkptr=2 on
  publication/live/bootstrap/security, the six cross-compiles (linux
  arm64/386, darwin amd64/arm64, freebsd amd64, windows amd64), and
  per-OS test-compiles of internal/publication all PASS. Rust tree
  untouched.

- Review round 1: five-aspect adversarial review at HEAD 74cfc7d:
  parity PASS (Goodall; the armObserved header-invariant target
  pointer fixed; the P3 crash-count wording was recorded - the slice
  record edit it described landed in round 2 below), wire/integrity PASS
  (Herschel; P3 flaky pin routed to performance), idioms FAIL then
  fixed (Gauss: P2-A the MemStats window witnessed background
  allocations in the full battery - the pin now takes the minimum of
  5 single-run windows; P2-B the unix-dead housekeeping-observer
  ceremony of the Rust cfg-shared retire surface removed (M5 note in
  code); P3-A the triplicated cancellation-checkpoint closure hoisted
  to cancellationCheckpoint), performance FAIL then fixed
  (Chandrasekhar: F1 the same pin flake; F2 the pin's per-class
  enumeration corrected to the escape-analysis-proven claim - zero
  machine-logic escapes, every one of the pinned 58 objects is an
  x/sys boundary conversion, a rename class, or result plumbing),
  records FAIL then fixed (Noether: the 58-budget flake, the
  deferral-mapping overclaim corrected above, the stale
  checkpoint-arm comments in problem.go/problem_test.go refreshed,
  the reservation_file.go header refreshed, line-count wording
  corrections). Round-2 outcomes at HEAD c036f2e: performance PASS
  (Chandrasekhar: min-of-5 single-run MemStats windows with fresh
  fixtures is sound - background allocations can only inflate a
  window, and the single-window diagnostic still measures 58), and
  wire/integrity PASS (Herschel: the fix commit touches no wire
  codec/state-2 arm/selection/locks/crash matrix; both after-selection
  injection tests re-assert record selectability; the retire-observed
  removal is behaviorally exact on POSIX; one P3 comment-precision
  nit). Three FAILs fixed: parity (Goodall: the round-1 crash-count
  "fixed" claim was stale - the record still said "all nine crash
  points" and still listed the deleted retireObserved; corrected
  above), idioms (Gauss: P1 - the machine never closed its own
  reservation owners on any terminal path, so the fd, the 8 KiB
  mapping, and the operation flock were abandoned on every attempt
  (the same-process resolver would stall on the flock after an
  outcome-unknown attempt); every terminal path now closes the owner
  exactly where Rust drops it, pinned by
  TestAttemptOutcomeUnknownReleasesTheOperationFlock, which fails
  without the close. P2 - finishPublishedObserved's observe/observer
  parameters were dead after the M5 ceremony removal; the variant is
  folded into finishPublished, matching Rust finish_published with
  observe=false), records (Noether: P2-A stale retireObserved in the
  main_file.go inventory, P2-B the false "all nine" count, P3 the
  observe*-builder count - all corrected above). Round-3 outcomes at
  HEAD 329de57: wire/integrity PASS (Herschel: the retire_observed cfg
  attribution comment fixed precisely against Rust main_file.rs
  retire/retire_observed/unix-arm wiring; every owner close runs
  after the last mapping/file use, so the closes are wire-neutral),
  performance PASS (Chandrasekhar: pin 58 stable x5 plus the
  single-window diagnostic; -m=2 shows zero new escapes; the closes
  are pure munmap/close syscalls at the Rust drop point and add no
  allocations in the measured window; the finishPublishedObserved
  fold is allocation-neutral). Three FAILs fixed after 329de57:
  parity (Goodall: the "every terminal path closes" claim was false -
  the two initializeObserved failure arms of publishWithObserver
  returned without closing the draft, leaking one fd and the 8 KiB
  mapped view per failed attempt where Rust drops the moved
  ReservationDraft; both arms now close, f87f1e1, and
  TestAttemptState1FailureReleasesTheDraftFiles pins the descriptor
  release, failing without the closes), idioms (Gauss: same two arms
  plus a new P2 - the two resume fixtures double-closed the armed
  reservation after resumeArmed took ownership; the transfer contract
  is now documented on resumeArmed (Rust moves the ArmedReservation
  in) and the fixtures no longer close their copy, 7bf1e87; the
  canonical-reservation resume contract - the caller closes the
  returned armed reservation or hands it to resumeArmed, and closes
  the refusal failure owner - is recorded for the slice-J resolver,
  which introduces the production callers), records (Noether: same P1
  plus the phantom attempt_alloc_test.go:80-95 citation, corrected to
  :63-72). Round-4 outcomes at HEAD e7b1d7e: all five aspects PASS
  (parity Goodall, idioms Gauss, performance Chandrasekhar, wire/
  integrity Herschel, records Noether) with no P0-P2 findings; the
  terminal-close invariant is verified arm-by-arm (14 owner-carrying
  terminal paths, each closing exactly once, no use-after-close, no
  double-close), the flock and fd-count pins both fail without their
  fixes, and the only remaining item was the attempt.go inventory
  count (444 -> 450, corrected here). Slice I is complete; next is
  slice J (resolver core).

### Status (2026-08-24) - chunk 4-8 slice J implemented: resolver core

Slice J ports the restart resolution machine (Rust resolver.rs 435 +
resolver_authority 106 + resolver_verification 88 + resolver_result
189, plus the file_inspection 309 dependency) to Go: the authority
reconciliation (caller result header_for + binding, choose_authority
arms), the main-output inspection with the exclusive/shared lifetime
locks, the desired/other/absent classification, complete (private
state-2 restore, arm, resume_armed) and remove (abandon with the
exact post-cleanup proofs), the later-canonical retention and
reclassification, the cancellation fold, and the reconstructed seed.
The replacement-policy branch of the Rust resolve entry is recorded
with slice K (replacement_resolver.go): the Go resolve() documents it
at the branch point, and no caller can pass a replacement header
before slice K lands (the public ResolvePublication surface is slice
N). Every functionally-owned inspected value is closed exactly where
Rust drops it.

Go files (all !windows):

- internal/publication/file_inspection.go (+314 at 12d936e, 317 lines at
  59fc0dd): inspectedOutput
  owner with Close/verify, inspectMainOutput/inspectPrivateOutput
  (require_exact_private)/inspectPrivateOutputExact (creator-only
  access), inspectOutput (lifetime lock, meta pair, digest, double
  proof), mapBootstrap/readBootstrap (bootstrap.OpenMeta
  ImmutableReader), classify/classifyOutputAccess; the freebsd arms
  (open_regular_any_link + finish_noreplace_transition) live in
  file_inspection_freebsd.go and the POSIX arms in
  file_inspection_other.go.
- internal/publication/resolver.go (+510 at 12d936e, 536 lines at
  59fc0dd, 538 at 981f8fe): resolve()/resolution (type)/
  dispatch/resolveOther/resolveAbsent/completeAbsent/arm (acquire -
  arm - resume_armed with the unknown/problemed arm classes)/arm
  Failure/resolveDesired (four-value close at scope end like Rust
  drops)/abandon with the destination- and coordination-post proofs,
  owner builders, reservationIdentityOf, requireNoLater,
  conflictProblem/unresolvable, resolverProblem folding.
- internal/publication/resolver_authority.go (+111 at 12d936e, 114 lines
  at 59fc0dd):
  inspectResolution (bind + header_for + reconstruction),
  inspectAuthority, chooseAuthority (all five arms with their exact
  conflict classes).
- internal/publication/resolver_verification.go (+93):
  verifyDestination/verifyNoLater/finalLater/synchronize/
  checkCancellation.
- internal/publication/resolver_result.go (+153):
  recordCancellation (cleanup-cause equality), desiredResult/
  desiredProblem/publishedOutputResult/desiredState/coordination
  Access/withLater/firstProblem.
- internal/publication/result_header.go (+86): resultHeaderFor with
  requireResultBinding (directory identity, basename encoding,
  identity kinds).
- internal/publication/output_resume.go (+29 at 12d936e; 200 lines at
  59fc0dd after the +35 slice-K resumePreparedOutputReplacement):
  resumePreparedOutput
  (PreparedOutput::resume consumes the inspected output).
- internal/publication/seed.go (+45): reconstructSeed (Seed::
  reconstruct from the header payloads).
- internal/publication/resolver_test.go (+1002 at 12d936e; 1,162 lines,
  26 tests at 59fc0dd, of which 22 port resolver_tests.rs and 4 are the
  slice-K replacement tests in the same file): porting resolver_tests.rs - every pre-main and post-main
  crash state through Complete and Remove, the private state-2
  restore, the unresolvable/conflict/cancelled classes, the supplied
  result binding refusals, the malformed exact reservation, the
  later-canonical retention, the canonical-reuse reclassification
  (verify_no_later/final_later), the equivalent desired inode, the
  unremovable foreign private output residue, the never-overwrite
  foreign main, the contended-lock cancellation, the no-implicit-
  validation postcondition, the missing/access-changed private
  output, the private-output symlink conflict, the no-authority
  refusal, and the descriptor-leak pin (complete + remove cycles keep
  /proc/self/fd stable).

Validation (all under nice): go build ./..., go vet ./..., plain and
v4work full trees (14 packages ok each), gofmt clean, -race +
-gcflags=all=-d=checkptr=2 on publication/live/bootstrap/security,
the six cross-compiles (linux arm64/386, darwin amd64/arm64, freebsd
amd64, windows amd64), and the 22 resolver tests (v4work) all PASS.
Rust tree untouched. The fd/mapping leak class of slice I is pinned
for the resolver by TestResolverDoesNotLeakDescriptors; every
inspected value close is placed exactly where Rust drops the value
(resolveDesired/abandon scope-end defers, arm consumption into the
machine owner, completeAbsent output close at every terminal path).

### Status (2026-08-24) - chunk 4-8 slice K implemented: replacement resolver

Slice K ports the replacement restart resolution (Rust
replacement_resolver.rs 372 + replacement_inspection.rs 340) to Go
and lands the replacement-policy branch of resolve(): the base
reservation operation locks unlock for the two-inode pair inspection
and relock after it, the main class selects the arm (previous ->
complete/remove, desired -> published with the private-output or
foreign-residue cleanup, other -> restore-and-discard), the previous
main and prepared output move into the resumed attempt, and the
no-rollback remove mode is refused with the unresolvable class.
Every inspected pair entry and base reservation closes exactly where
Rust drops it.

Go files:

- internal/publication/replacement_inspection.go (+364):
  replacementContent/inspectedReplacement (Close, verify with the
  rehash double-check)/replacementPair, inspectReplacementPair (the
  role-ordered exclusive lifetime locks: recorded output identity,
  recorded previous identity, remaining entries),
  openReplacementEntry (single-link regular rule), lock roles,
  inspectOneReplacement (name proof, read-only mapping, digest,
  desiredReplacementMeta via bootstrap.OpenMeta, classify,
  classifyAccess, verifyStableReplacement).
- internal/publication/replacement_resolver.go (+427):
  replacementDispatch (unlock/relock pair inspection + the four
  classification arms), completePreviousReplacement (requires the
  exact previous main, prepared output, and reservation; the pair
  entries move into previousMain and the resumed prepared output;
  arm + resumeArmed + recordCancellation),
  removePreviousReplacement/resolveNotDesiredReplacement (restore
  the classified main with the exact ledger),
  resolveDesiredReplacement (no-rollback remove refusal,
  desiredCleanupReplacement, verification, withLater),
  removableReplacementOutput/desiredCleanupReplacement/
  requireOutputReplacement/requirePreviousReplacement/
  synchronizeReplacement/unlock/relock/attempted.
- internal/publication/resolver.go (+8/-4 at 3d787b9): the
  replacement-policy branch of resolve() now routes to
  replacementDispatch.
- internal/publication/output_resume.go (+35):
  resumePreparedOutputReplacement (PreparedOutput::resume_replacement
  with the previous main).
- internal/publication/resolver_test.go (+138 at 3d787b9): the four
  replacement tests of resolver_tests.rs - complete/remove over every
  pre-main crash state, both modes over the five post-exchange states,
  and the supplied replacement result after retirement.

Validation (all under nice): go build ./..., go vet ./..., plain and
v4work full trees (14 packages ok each), gofmt clean, -race +
-gcflags=all=-d=checkptr=2 on publication/live/bootstrap/security,
and the six cross-compiles all PASS; the 4 replacement tests plus the
22 resolver tests pass under v4work.

### Status (2026-08-24) - chunk 4-8 slice K fix round (round-1 review)

The slice-K round-1 review returned four PASS-pending and one FAIL
(idioms, Gauss): P1-1 completePreviousReplacement never closed the
machine-created previous main (deterministic fd + mapping leak on
every post-resume terminal; preparedOutput.Close did not release the
previous and the comment claimed the opposite), P2-1 double close of
the private entry when resumePreparedOutputReplacement errors, P3-1
the contradictory ownership comment, P3-2 the finalState shadow, and
P3-3 the double-pointer inspection helpers. Fixes in this round:

- P1-1/P3-1: preparedOutput.Close now releases the previous main
  together with the output (Rust drop of PreparedOutput owns
  PreviousMain), and the resume-construction error arm closes the
  still-owned previous (the constructor already closed the inspected
  private artifact on error). The contradictory comments in
  output_resume.go and output_prepared.go state the true contract.
  TestResolverReplacementCompleteResumesEveryPreMainCrashState now
  pins descriptor stability per crash point (fails without the fix:
  2 leaked descriptors per iteration).
- P2-1: the completePreviousReplacement resume-error arm no longer
  re-closes the private entry after the constructor closed it.
- P3-2: the two finalState shadow variables are renamed state.
- P3-3: lockReplacementRole/replacementEntryWithIdentity/
  lockReplacementRemaining/lockReplacementEntry take single
  inspectedReplacement pointers (nil-checked) instead of
  double pointers that were only dereferenced.

The performance review (round-2 carry-over from round-1) added three
P2s, all fixed in this round:

- P2: the replacement arms copied base.seed to the heap on every
  call. The arms now borrow &base.seed (the slice-J convention), and
  the seed artifact builder copies the creation security out of the
  seed instead of borrowing it, so no replacement arm moves the seed
  or the base resolution to the heap (verified with -gcflags=-m=2:
  the only remaining seed-adjacent move is the 48-byte security copy
  on artifact paths, the accepted pre-existing class; the
  TestAttemptPostBoundarySuccessAllocatesNoHeap pin dropped 58 to 57
  objects, causal link proven by reverting the builder).
- P2: removableReplacementOutput/desiredCleanupReplacement returned
  *outputOwner (heap escape). They now return the owner by value with
  a presence flag; the callers take the address inside their own
  frame (the slice-H value-owner pattern).
- P2: desiredReplacementMeta returned *format.Meta (heap escape).
  inspectedReplacement now stores the meta by value with a
  replacement-specific presence flag (inspectedOutput already stores
  the meta by value without a flag).

The parity review (round-4 carry-over P2-2) added one more fix:
zero-length pair entries now inspect like Rust Mapping::view maps
nothing. inspectOneReplacement classifies a zero-length entry with
the empty SHA-512 digest and no mapping (never the zero-size mapping
refusal), and inspectedReplacement.verify proves the empty digest
with the cancellation probe in place of the digest pass, so a
zero-byte main or private leftover resolves through the foreign-main
arm exactly like Rust instead of failing the whole pair inspection
with CodeFormatInvalid. TestResolverZeroLengthReplacementEntry
ClassifiesInsteadOfFailing pins the behavior (fails without the fix).

The wire/integrity review (round-3 carry-over P1) added one more fix:
the replacement arms returned an outcome-unknown result on
non-cancelled synchronize failures, where Rust propagates every
synchronize failure with ? on both replacement arms (the
outcome-unknown conversion exists on the Rust fail-if-exists
synchronize arms and on the common completion/arm-failure paths, not
on the replacement synchronize arms). Both replacement arms now close
the pair and base reservations and return the synchronize error
directly.

Validation (all under nice): go build ./..., go vet ./..., plain and
v4work full trees (14 packages ok each), gofmt clean, -race +
-gcflags=all=-d=checkptr=2 on publication/mapping/live/format, the
linux/freebsd/darwin publication cross-builds, and the 27 resolver +
replacement tests (v4work) all PASS; the new descriptor pin fails
without the previous-close fix and passes with it; the allocation pin
fails without the artifact security-copy fix (58 objects) and passes
with it (57); the zero-length test fails without the empty-entry
classification fix. Present-state counts at d7cb015:
replacement_resolver.go 435, replacement_inspection.go 398,
output_resume.go 202, seed.go 276, attempt_alloc_test.go 80,
resolver_test.go 1,198 (27 tests).


### Status (2026-08-24) - chunk 4-8 slice J fix round (round-1 FAILs)

The slice-J review round at 12d936e returned three FAILs and two
pending verdicts: performance (Chandrasekhar P2), idioms (Gauss
P2-1/P2-2/P3), and records (Noether P2/P3); the parity and wire
verdicts (Goodall, Herschel) were still running when this fix round
started. All three FAILs are fixed in this round:

- Performance P2 (unpinned heap on the resolver success path): the
  reconstructed seed now travels by pointer through resolution/
  dispatch/resolveDesired/resolveOther/resolveAbsent/completeAbsent/
  abandon instead of being copied to the heap at each arm. The
  borrow-lifetime convention is documented on the resolution type
  (base.seed owns the value; the arms borrow it). The remaining
  owner-pointer allocations follow the pointer-owner convention
  accepted since slice G: Go pointers emulate the Rust move-Option
  ownership at the machine boundary, not heap-backed handles (Rust
  File/Mapping are stack values); the cost is a constant 2-4 small
  objects per cold resolve, closed exactly once by the arms.
- Idioms P2-1 (mixed close contract): resolve()'s error path now
  closes only the destination directory. Each arm owns the
  exact/later/main values and closes them on every terminal path,
  matching the Rust drop points exactly: resolveDesired and abandon
  keep their scope-end defers; completeAbsent's reservation defer
  reads the variable at return time so the post-arm nil transfer
  prevents a second close after arm's failure owners or
  resumeArmed/finishPublished already closed the moved fields;
  resolveOther's requireNoLater and cancelled-synchronize error paths
  close exact+main where Rust drops them; resolveAbsent's
  requireNoLater error closes exact+later; and resolve() itself
  closes exact/later only when inspectMainOutput fails before any
  arm can run.
- Idioms P2-2 (authority leak): inspectAuthority closes auth.later
  when the exactPrivateReservation fallback errors, exactly where
  Rust drops the Authority value.
- Idioms P3 (seed reconstruction leak): inspectResolution closes
  auth.exact/auth.later when Seed::reconstruct fails, exactly where
  Rust drops the Authority value.
- Records P2/P3: resolver.go +510 (not +503), result_header.go +86
  (not +80), resolver_test.go +1002 (not +969), output_resume.go +29
  (not +38), classifyOutputAccess (not classifyAccess), and
  resolution is a type, not a function.
- TestResolverMalformedMainAndCancelledResolutionChangeNothing now
  pins descriptor stability on both its error paths (fd count before
  and after each resolve), covering the exact/later closes that the
  new contract requires of resolve() when no arm runs.

Validation (all under nice): go build ./..., go vet ./..., plain and
v4work full trees (14 packages ok each), gofmt clean, -race +
-gcflags=all=-d=checkptr=2 on publication/mapping/live/format, the
linux/freebsd/darwin publication cross-builds and the freebsd/darwin
v4work vet, and the 26 resolver + replacement tests (v4work) all
PASS. GOOS=windows remains out of scope for publication this
milestone (resolver.go is !windows; the v4work freebsd link machine
does not build on windows - pre-existing).
The first-round parity (Goodall) and wire (Herschel) verdicts on
12d936e arrived during this fix round. Herschel PASSed with two P3s;
Goodall FAILed with one P1 and two P2s. The P1 (authority error
paths leak the owned later/exact reservation) is the same leak class
fixed above at 12d936e. The remaining Goodall findings are fixed in
this round:

- Goodall P2 (desiredResult cause not folded): inspectedOutput.verify
  and readBootstrap now fold every failure into the fixed publication
  problem surface exactly where Rust does (verify: directory proofs
  via Problem::namespace; readBootstrap: fstat/page failures via
  Problem::sdk, not-complete via conflict). Every other verify caller
  (synchronize, verifyDestination, verifyLater) keeps its idempotent
  resolverProblem fold; the desiredResult and publishedOutputResult
  causes are no longer raw namespace or mapping errors.
- Goodall P2 (panic vs Identity::decode None): the private-output
  cleanup artifact now decodes the header output identity with
  optionalEncodedIdentity (Rust Identity::decode option - an invalid
  payload yields no artifact identity instead of a process panic);
  the reservationIdentityOf expect panic is unchanged (Rust expect on
  the selected record).
- Goodall P3 (extra recordCancellation fold): resolveDesired's
  synchronize-failure arm now returns plain outcomeUnknown exactly
  like Rust; the fold was provably inert (non-nil cause, empty
  cleanup) but added a redundant cancellation probe.
- Herschel P3 (main-reopen parity): the Complete pre-main matrix and
  both-modes post-main matrix reopen the resolved main with the
  immutable reader and pin the fixture transaction, exactly like the
  Rust matrix tests.

Validation (all under nice): go build ./..., go vet ./..., plain and
v4work full trees (14 packages ok each), gofmt clean, -race +
-gcflags=all=-d=checkptr=2 on publication/mapping/live/format/reader,
the linux/freebsd/darwin publication cross-builds and the freebsd/
darwin v4work vet, and the 26 resolver + replacement tests (v4work)
all PASS. GOOS=windows remains out of scope for publication this
milestone (resolver.go is !windows; the v4work freebsd link machine
does not build on windows - pre-existing).
### Status (2026-08-24) - chunk 4-8 slice J complete: round-2 and round-3 review PASS

Round-2 re-review at 59fc0dd: idioms PASS (Gauss), performance PASS
(Chandrasekhar), wire/integrity PASS (Herschel), parity FAIL items
verified fixed (Goodall PASS after re-check of the delta). Round-3 at
981f8fe confirmed all five aspects PASS (records FAIL items P2-1 stale
counts and P2-2 missing seed-borrow convention fixed; both re-verified
with the dated-count phrasing). Every owner-carrying terminal path of
the resolver now closes exactly once, resolve() closes only the
destination directory on the dispatch error path, the authority-entry
leaks stay closed, verification errors reach the result surface
folded like Rust, and the descriptor pins cover the error paths.
Slice J is complete; next is the slice-K review round.

### Status (2026-08-24) - chunk 4-8 slice K complete: review rounds 1-5 PASS

Slice K closed after five review rounds. Round-1 FAILs (idioms
P1/P2/P3, records P2/P3) were fixed in the fix round (previous-main
close with the descriptor pin, resume-error single close, double
pointer removal, record deltas +138 and +8/-4). Round-2/3 carry-over
findings fixed: the replacement-arm heap escapes (seed borrow with
the artifact security copy and the 58-to-57 allocation-pin step,
outputOwner value returns, meta value with presence flag) and the
wire P1 (synchronize failures propagate like Rust on both
replacement arms). Round-4/5 carry-over findings fixed: zero-length
pair entries classify like Rust Mapping::view maps nothing (new
regression test) and the live record counts. All five aspects PASS
at 9872815.

### Status (2026-08-24) - chunk 4-8 slice L implemented: residue inspection and removal

Slice L ports the coordination residue machine (Rust residue.rs 145 +
residue/linux.rs 457 + residue/main.rs 137 + residue/retirement.rs
158) to Go at commit 22ffd1f: the canonical coordination inode is
classified (absent, bound selectable reservation, live reader-table
sidecar, unselectable residue), an unselectable coordination is
removed only after the operation lock, the selectable refusals, and
the retained-handle proofs, the destination main is hashed but never
changed (zero-length mains carry the empty digest), and every
descriptor owned by an inspection or removal terminal is released
exactly when the Rust handle would drop. The residue helpers map
reservation files read-only like Rust (reservationMappingResidue),
the main guard owns its file and mapping with an explicit Close, and
closeResidueAuthority releases coordination + destination on every
error terminal; only the retained-handle incomplete results keep the
authority open for the caller's retry.

Go files:

- internal/publication/residue.go (+582): the four coordination
  classes, the inspection/removal results, inspectResidue (lifetime
  checkpoint, bind, exact-name proof, classify, destination close on
  every no-handle terminal), removeResidue (authority close on every
  error terminal; main guard close on the post-inspect checkpoints),
  finishRetiredResidue (retry proof, main proof, final
  coordination-reuse class; closes the guard and the authority on
  success), classifyCoordinationResidue, selectedBoundResidueHeader,
  reservationMappingResidue (read-only), reconstructResidue,
  reservationAccessResidue, verifyCoordinationResidue,
  rejectSelectableResidue, finishRemovalResidue,
  finalCoordinationResidue, incompleteResidue.
- internal/publication/residue_main.go (+155): residueMainGuard
  (inspectMainResidue with the lifetime lock, name proof, optional
  read-only mapping + SHA-512 digest, v4 tuple via bootstrap.OpenMeta;
  Close; verify).
- internal/publication/residue_retirement.go (+64): unix retirement
  (UnlinkExact + regular-link-count proof, retry arm).
- internal/publication/residue_test.go (+504): 9 tests porting the
  Rust residue suites, each pinned across the whole inspect/remove
  cycle with process-fd counters (the pins are proven effective: a
  temporarily injected authority leak fails the malformed-removal
  test with +2 descriptors).
- internal/live/header.go (+4/-3): export HasSelectableHeader for the
  residue machine.

Validation (all under nice): go build ./..., go vet ./..., plain and
v4work full trees (14 packages ok; 857 Test functions runnable under
v4work at 22ffd1f via go test -tags v4work -list '^Test' ./..., 172
in publication of which 9 are the new residue tests), gofmt clean,
-race + -gcflags=all=-d=checkptr=2 on internal/publication, and the
freebsd/darwin cross-builds all PASS at 22ffd1f. (The tree total is
restated with its counting method here because an earlier draft
omitted the root package and internal/bitmap.)

### Status (2026-08-24) - chunk 4-8 slice L fix round (round-1 review)

Round-1 review: parity (Goodall) PASS, performance (Chandrasekhar)
PASS, wire/integrity (Herschel) PASS, idioms (Gauss) FAIL (one P1,
four P3), records (Noether) FAIL (one P2, two P3). Fixes at b0be0df:

- P1 (idioms): a cancelled removal retry consumed the coordination
  descriptor and the destination directory but leaked the retired
  main guard (fd + mapping); the first-checkpoint arm of removeResidue
  now closes handle.retired.main exactly when the Rust handle would
  drop it, and the new TestResidueRetryCancellationReleasesRetainedMain
  pins the whole cycle with a process-fd counter (empirically proven:
  an injected leak fails with +2 descriptors).
- P3 (idioms): the unix-dead retirementPending field and the
  destination/identity/flag parameters of the retirement retry arm
  are removed (retryResidueRetirement takes only the file, mirroring
  the unix arm of Rust retirement::retry); the one-use
  liveLockOperationResidue wrapper is replaced by lockOperationFile;
  the retry arm and its handle-level wrapper no longer collide in
  name; residueHandle documents its consume/move contract.
- P3 (parity): readResidueTuple propagates mapped page faults as sdk
  problems like Rust read_tuple (the K-port desiredReplacementMeta
  convention), keeping only the unreadable-meta-pair arm silent.
- P2 (records): the slice-L tree total is restated with its method:
  858 Test functions runnable under v4work at b0be0df (go test -tags
  v4work -list '^Test' ./...; 725 plain) - the earlier 647 number
  omitted the root package (198) and internal/bitmap (12).
- P3 (records): the slice-I deferral cross-reference closes here -
  "reservation retirement crash points belong to slice L" is realized
  by the residue tests driving the runAttemptCrashChild fixtures
  (residue_test.go via resolverPreMainPoints) inside the v4work
  matrix; the Rust residue test authority is residue_tests.rs (8
  tests) + residue/linux_tests.rs (1 test), and the Go residue suite
  now has 10 tests (the retry-cancellation test added in this round).

Validation (all under nice): plain and v4work full trees (14
packages ok each), vet, -race + -gcflags=all=-d=checkptr=2 on
internal/publication, freebsd/darwin cross-builds, gofmt clean, all
PASS at b0be0df.

### Status (2026-08-24) - chunk 4-8 slice L round-3: all five aspects PASS

Round-2 re-review: parity (Goodall) PASS, performance
(Chandrasekhar) PASS, wire/integrity (Herschel) PASS, idioms (Gauss)
PASS with two P3 residuals, records (Noether) FAIL (P2: the old 647
statement survived in the implemented record). Fixes at f4b7bc3 and
the in-place record correction above:

- P3 (idioms): the handle-level retry wrapper is renamed
  retryRetiredResidueHandle so the two retry names diverge in
  content, and the retry-cancellation test asserts the main guard
  exists before the cancelled retry, keeping its fd pin non-vacuous.
- P2 (records): the implemented record now states the method-stated
  count (857 under v4work at 22ffd1f) and is consistent with the
  fix-round record (858 at b0be0df, 10 residue tests).

Round-3 delta re-verified by verbose re-runs: plain and v4work trees
(14 packages ok each), vet, -race + -gcflags=all=-d=checkptr=2 on
internal/publication, freebsd/darwin cross-builds, gofmt clean, all
PASS at f4b7bc3. All five aspects PASS; slice L is complete.


### Status (2026-08-24) - chunk 4-8 slice M implemented: abandoned-publication maintenance

Slice M ports the abandoned-artifact maintenance machine (Rust
maintenance.rs 396 + maintenance/common.rs 456 + maintenance/output.rs
181 + maintenance/reservation.rs 183 + maintenance_tests.rs 488) to
Go at commit 2c7ed4d: constant-memory listing of stable exact-pattern
private publication temps and reservation artifacts with exact
evidence, and exact removal of one retained artifact after the caller
certified its quiescence (directory identity proof, single-link owned
open under the artifact lock, content verification, unlink-exact with
the retained link-count proof, and the durable absence proof).
Windows housekeeping is the typed OS-unsupported refusal of the Rust
non-windows arm.

Go files:

- internal/publication/maintenance.go (+230): the abandoned temp/
  reservation entry and list types, the evidence types (reusing
  residueTuple/residueDigest for the portable facts), the sink
  control surface (errMaintenanceSinkStop -> StoppedBySink, sink
  errors -> SinkFailed), the four list/remove dispatchers with the
  exact evidence both-or-none argument class, and the two refused
  Windows housekeeping arms.
- internal/publication/maintenance_common.go (+349): maintenanceArtifact
  (prefix + family problem details), the constant-memory scan over
  live.Directory.Scan with the checked-add overflow class, the
  stable single-link entry proof before and after inspection,
  encode/decode via the private-name codec, the portable identity
  conversions, the owned open with the artifact lock and the
  post-lock verify, the durable absence proof, the unix retirement
  (unlink exact + retained link-count cause + sdk-folded absence
  failure), and the artifact-specific namespace/cleanup problem
  mappers (invalid-name InvalidArgument, IO raw, unsupported and
  cross-filesystem OS-unsupported, ownership-changed
  CleanupConflict).
- internal/publication/maintenance_output.go (+125): publication-temp
  listing with the optional tuple+digest content evidence of a
  readable v4 main (geometry gate, read-only mapping, OpenMeta with
  the Format class as the no-evidence marker, single-pass digest),
  and exact removal under the main lifetime lock with the
  changed-evidence refusal.
- internal/publication/maintenance_reservation.go (+147): reservation
  listing with the authenticated policy/phase/output/previous
  evidence of selectable bound records, and exact removal under the
  operation lock with the readable-binding refusal.
- internal/publication/maintenance_test.go (+486): the nine Rust
  maintenance tests (stable listing, complete/partial/absent
  removal, changed identity/content/directory refusals, reservation
  policy/phase/previous evidence, malformed names, bound/malformed/
  absent reservation removal, wrong directory identity and copied
  header binding, cancellation/stop/sink for both families) plus the
  Windows refusal test, each removal cycle pinned with process-fd
  counters (empirically proven: an injected directory leak fails +1
  descriptor).

Validation (all under nice): go build ./..., go vet ./..., plain and
v4work full trees (14 packages ok each; 868 Test functions runnable
under v4work at 2c7ed4d via go test -tags v4work -list '^Test'
./..., 183 in publication of which 10 are the maintenance tests),
gofmt clean, -race + -gcflags=all=-d=checkptr=2 on
internal/publication, and the freebsd/darwin cross-builds all PASS
at 2c7ed4d.

### Status (2026-08-24) - chunk 4-8 slice M fix round (round-1 review)

Round-1 review: performance (Chandrasekhar) PASS, records (Noether)
PASS, idioms (Gauss) PASS with two P3s, parity (Goodall) FAIL (two
P2, three P3), wire/integrity (Herschel) FAIL (one P2, two P3).
Fixes at d21c4d5:

- P2 (parity): the Windows housekeeping refusals now carry the exact
  Rust message ("Windows housekeeping is unavailable on this
  platform", capital W), pinned by the test; the maintenance-specific
  namespace mappers collapse the IoAt arm into the Io arm exactly
  like Rust's NamespaceError::Io|IoAt -> Error::Io (the operation
  label is discarded).
- P2 (wire) + P3 (parity): both remove dispatchers run the leading
  cancellation checkpoint before the evidence-pair argument check
  and before any identity or namespace work, exactly like the Rust
  remove arms; the new
  TestMaintenanceRemoveHonorsLeadingCancellation pins the cancelled
  class for an absent artifact and for an invalid evidence pair.
- P3 (parity): the raw fstat failure of contentEvidence folds through
  the sdk class; the written-record identity decodes panic with the
  Rust-verbatim expect texts ("selected output identity is valid" /
  "selected previous identity is valid").
- P3 (idioms): the new identity conversions reuse residueLocalIdentity
  instead of duplicating the device+inode fold; maintenanceTestName
  follows the suite's t-based helper convention.
- P3 (records): the slice-M implemented record moved after the slice-L
  round-3 record so the stack is chronological, the superseded Next
  line of the L round-3 record is deleted, and the Next line lives
  only at the tail of the newest M record like the L convention.

Validation (all under nice): plain and v4work full trees (14
packages ok each; 869 Test functions runnable under v4work at
d21c4d5, 184 in publication of which 11 are the maintenance tests),
vet, -race + -gcflags=all=-d=checkptr=2 on internal/publication,
freebsd/darwin cross-builds, gofmt clean, all PASS at d21c4d5.

### Status (2026-08-24) - chunk 4-8 slice M complete: review rounds 1-3 PASS

Round-2 re-review: parity (Goodall) PASS, performance
(Chandrasekhar) PASS, wire/integrity (Herschel) PASS, idioms (Gauss)
PASS with one residual P3, records (Noether) PASS with one wording
note. Round-3 at 86b8716: the two written-identity decodes
consolidate into one parameterized selectedIdentity with the
Rust-verbatim expect texts preserved, and the record wording matches
the Next-line structure; all five aspects PASS. Slice M is complete.

### Status (2026-08-24) - chunk 4-8 slice N implemented: reservation-path publish composition

Slice N step-1 replaces the one-shot staging facade with the Rust
workflow::create + publish composition at commit ab130c7:
CreatePublishAttempt creates and secures the private output (the
exchange-availability probe precedes the rollback-safe creation, and
the fail-if-exists policy proves the main and the coordination twin
absent), the caller builds the finished content into the attempt
file, and Finish prepares it (custody, lifetime lock, finished
proof, digest, finish sync), binds it to the replaced main under the
replacement policies, and publishes it through the attempt machine.
Every pre-machine failure carries the folded problem class and the
discard-evidence ledger exactly like the Rust Failure::Early arms;
every terminal closes the descriptors it opened, including the bound
destination directory the machine keeps open for its caller (Rust
drops the consumed owners). finished is consumed on every terminal.

Go files:

- internal/publication/publish.go (+166): the PublishAttempt owner
  and the composition; earlyPreparationFailure folds the full
  cleanup ledger (attempt id, directory identity, basename, output
  identity, creation security, cleanup artifact) from one early
  discard; closeCreatedOwner/closeFinishedOwner/closeDestination
  Directory mirror the Rust drops.
- internal/publication/publish_test.go (+245): the five composition
  ports (fail-if-exists success with the exact published facts and
  no residue, existing-main and existing-coordination create
  refusals with the fd pin, replacement over a previous main with
  the PreviousDestination evidence, cancelled preparation discard
  with the cleanup-state pin, missing-previous bind refusal), each
  cycle pinned with the process-fd counter.
- internal/publication/output_created.go: the bound destination
  directory closes on the requireFailIfExistsAvailable error and on
  the createAttempt error, exactly where the Rust bind would drop on
  the refusal.

One real defect surfaced by the new tests before commit: bindPrevious
returns a concrete *replacementFailure, and the composition stored it
in an error interface, so a successful bind became a typed-nil
interface and replacementProblem panicked on the replacement success
path. The composition keeps the concrete pointer type, and
TestPublishReplacementOverPreviousMain pins the path.

Validation (all under nice): plain and v4work full trees (14 packages
ok each; 874 Test functions runnable under v4work at ab130c7, 189 in
publication of which 5 are the publish composition tests), vet,
-race + -gcflags=all=-d=checkptr=2 on internal/publication,
freebsd/darwin cross-builds, gofmt clean, all PASS at ab130c7.

### Status (2026-08-24) - chunk 4-8 slice N implemented: snapshot and publish_set on the composition

Slice N step-2 at commit a78eced moves both publishing callers onto
the reservation-path composition and removes the one-shot writer
staging machine. The public result shapes become the exact
Rust-parity publication facts (publication.PublicationResult: the
Publication status field, the destination content with the
Previous/Other classes, the artifact ledger, and CleanupState
projections), the snapshot and publish_set preparation failures keep
the collapsed Cause + CleanupState terminal, and every build failure
before Finish discards the created attempt through the composition
(Rust fail_attempt / discard_attempt parity, identity-guarded unlink
plus the retained-directory sync).

Go changes:

- internal/writer/output.go (+66/-1): NewOutputBuilderOverFile and
  NewStructuredOutputBuilderOverFile (Rust new_owned_with_extent over
  the workflow::create file): the file must be empty, extends to the
  budget extent, maps read-write through a duplicated descriptor, and
  takes no lifetime lock (the composition prepare step takes it, Rust
  prepare_cancellable). The shared assemble helper keeps both
  constructors byte-identical.
- internal/publication/publish.go: CreatePublishAttempt now takes the
  exported PublicationPolicy (reservationPolicyOf maps the wire peer;
  an invalid policy is refused with the verbatim class), and the
  attempt exposes FileIdentity (the identity probe) and Discard (the
  identity-guarded cleanup of an unfinished attempt, consumed).
- internal/publication/name.go: ValidDestinationName (moved from the
  writer staging) available on all platforms (pure string rules).
- internal/publication/publication_parent_{unix,windows}.go: moved
  from the writer package (CheckPublicationParent is a publication
  concern).
- internal/publication/publish_windows.go + finished_windows.go
  (new): typed Windows stubs for the new surface (M5 honest
  refusal at create, same class as the POSIX destination bind).
- internal/snapshot/snapshot.go: To() creates the attempt through the
  composition, builds into its file, compares the secured attempt
  identity, and publishes through Finish; the builder-construction and
  build failures discard the attempt, the source release order is
  unchanged, and the machine refusals stay results with their Cause.
- v4/go/membership_publish_set.go: PublishSet uses the same
  composition; the public aliases retarget to publication.Publication
  Policy/Result/Status/DestinationContent/CleanupState (the rich Rust
  shapes), and the preparation failure collapses to Cause + Cleanup
  state.
- v4/go/snapshot_public.go + root snapshot/publish-set tests: the
  result field rename (Publication.status -> Publication.Publication)
  and the CleanupState projection.
- Removed: internal/writer/publication_staging.go (730) and its
  superseded tests (publication_staging_test.go 817, publish_race_
  v4work_test.go 48, retire_branches_test.go, and the staging arms of
  crash_v4work_test.go) - the internal/publication machine tests and
  the five composition tests own that coverage (including the
  fail-if-exists rename race: TestAttemptMainRaceAfterState2 and the
  replacement crash family in attempt_crash_v4work_test.go).
- Removed (commit 355e894): the mapping path-based publish machine
  bodies and their tests (1,269 deletions incl. the 22 deleted
  mapping test functions); internal/live owns the namespace
  mutations. Only the ExchangeAvailable capability stubs survive
  (mapping_publish_linux.go, mapping_publish_darwin.go,
  mapping_publish_posix.go, mapping_publish_windows.go);
  mapping_publish_freebsd.go, mapping_publish_netbsd.go,
  publish_link_noreplace*.go and linkcount_*.go were fully deleted.
  StatIdentity lives in mapping_identity_probe{,_bsd,_windows}.go.
  The deleted tests are owned by the publication machine and
  composition suites.

Behavior notes:

- FreeBSD immutable snapshots and publish_set now progress to the
  reservation machine instead of refusing at the builder creation
  gate; the machine's operation-lock refusal keeps the exact
  LiveCoordinationUnsupported class and detail ("live coordination is
  not implemented on this platform") at the SOW-recorded platform
  limitation position (4-12/M5), and the cross-compiles stay green.
- The composition closes every descriptor on every terminal (Rust
  drop parity), so the previous builder-Close-after-Publish pattern is
  gone: the finished output (attempt file + builder mapping) is
  consumed by Finish.

Validation (all under nice): plain and v4work full trees (14 packages
ok each; 848 Test functions runnable under v4work at a78eced, 189 in
publication of which 5 are the publish composition tests), vet,
-race + -gcflags=all=-d=checkptr=2 on internal/publication,
internal/writer, and the root package (plain and v4work),
linux/arm64 + freebsd/amd64 + darwin/arm64 + windows/amd64
cross-builds, gofmt clean, all PASS at a78eced.

### Status (2026-08-24) - chunk 4-8 slice N implemented: public publication surfaces

Slice N step-3 at commit 7b0f1a0 adds the SDK-facing publication
surfaces (Rust publication.rs re-exports): interrupted-publication
resolution, canonical residue inspection/removal, and
abandoned-artifact maintenance. The publication package exports the
boundary types and entry points; the root iprangedb facade aliases
the Rust-parity shapes and folds the cancellation token.

Go changes:

- internal/publication/public.go (+474): PublicationResolutionMode
  and ResolvePublication (Complete/Remove over one supplied result or
  the retained reservation), PublicationTuple/PublicationDigest,
  PublicationResidueCoordination/MainContent/Main/Handle/Inspection/
  Removal with the exact conversions (mapResidueRemoval keeps the
  residual authority of an incomplete removal), the consumed-handle
  rule enforced by nil-ing the wrapper after Remove/Close (Rust move
  semantics), the abandoned reservation policy/phase/evidence/entry/
  list and publication-temp entry/list shapes with the exact
  evidence mappings, the exported ErrMaintenanceSinkStop control, and
  the four list/remove entry points.
- internal/publication/public_windows.go (+147): typed Windows stubs
  for every new symbol (M5 honest refusal, same class as the POSIX
  destination bind); the type names exist only so the SDK facade
  compiles on Windows.
- internal/publication/public_test.go (+276): boundary ports of the
  malformed-residue removal (with the consumed-handle refusal and fd
  pin), the resolver Complete/Remove modes over real crash states,
  the abandoned-temp listing/removal with the exact tuple/digest
  facts and the sink-stop/sink-failure classes, and the reservation
  listing evidence mapping (policy/phase/output/previous).
- v4/go/publication_public.go (+244): the iprangedb entry points
  (ResolvePublication, InspectPublicationResidue,
  RemovePublicationResidue, ListAbandonedPublicationTemps,
  RemoveAbandonedPublicationTemp, ListAbandonedReservationArtifacts,
  RemoveAbandonedReservationArtifact, ErrMaintenanceSinkStop) as type
  aliases and wrappers; publicationCheck converts the SDK token's
  cancelled state to the exact machine Cancelled problem so the class
  survives the machines that fold unknown check errors to the SDK IO
  class (the internal tests use the format surface directly).
- v4/go/publication_public_test.go (+173): end-to-end residue and
  maintenance through the SDK boundary over hand-built namespace
  state, the unresolvable refusal, and the leading-cancellation
  refusals of every entry point.
- v4/go/lifecycle_public.go: the root FileIdentity becomes an alias
  of the publication LocalFileIdentity (identical byte shape), so
  listed entries pass back into the removal entry points without
  conversion.

Validation (all under nice): plain and v4work full trees (14 packages
ok each; 854 Test functions runnable under v4work at 7b0f1a0, 193 in
publication of which 4 are the boundary tests plus 2 root-surface
tests), vet, -race +
-gcflags=all=-d=checkptr=2 on internal/publication and the root
package (v4work), linux/arm64 + freebsd/amd64 + darwin/arm64 +
windows/amd64 cross-builds, gofmt clean, all PASS at 7b0f1a0. (The
GOOS=windows vet of internal/security's tests fails on the
pre-existing unix.Geteuid use in security_test.go, added at slice
4-3; it is outside this slice's blast radius and tracked for the
slice-O sweep.)


### Status (2026-08-24) - chunk 4-8 slice N complete: review rounds 1-2 PASS

Round-1 review (five level-1 reviewers, adversarial): performance
(Chandrasekhar) and wire/integrity (Herschel) PASS; parity (Goodall)
one P3 (symlink detail); idioms (Gauss) six P3s; records (Noether)
P2 line-count drift plus wording. Round-1 fixes at d5d4459 (record
line counts) and 3b68ff3 (snapshot identity compare moved before
builder construction so a refusal discards the empty attempt file
directly, resolution mode closed enum, PublishAttempt consume guards
on File/Close/Discard/Finish with the file nil-ed on every terminal
and a second Finish refused, dead mapPublicationResidueCoordination
removed, policy/phase evidence mapped via explicit switches, root
test errors.As plus capacity 53).

Round-2 re-review: parity (Goodall) PASS, idioms (Gauss) PASS,
performance (Chandrasekhar) PASS, records (Noether) PASS with two P3
notes fixed at 4ac17b7 (the mapping-machine removal now recorded in
the step-2 Removed bullet, doubled parenthesis fixed). Wire/integrity
(Herschel) round-2 found P2-1: the round-1 symlink detail in
rejectLiveSelf diverged from the Rust fold - Rust open_regular with
O_NOFOLLOW folds a symlinked destination ELOOP into NotRegular, so
the probe reports Conflict "publication name is not a regular file"
(namespace/unix.rs:192-195, problem.rs:35-37), while the Go branch
reported "publication name is a symlink", a string Rust keeps only on
the IoAt arm (problem.rs:72-73) this probe never reaches. Fixed at
7d95412 by deleting the symlink branch so both cases share the single
not-a-regular-file arm; the IoAt mirror in internal/publication/
problem.go:96 is untouched. Re-validated under nice: plain and v4work
full trees (14 packages ok each; 833 Test functions runnable under
v4work, 194 in publication), TestSnapshotLive, GOOS darwin/freebsd/
windows/linux amd64 builds, gofmt clean. Herschel re-check PASS (full
rejectLiveSelf parity re-verified arm by arm) and Goodall confirmed
the placement and withdrew the round-1 P3-1. All five aspects PASS at
HEAD 7d95412. Slice N is complete.


### Status (2026-08-24) - chunk 4-8 slice O: full validation and the two pre-existing windows gaps closed

Slice O at commit 6d9ab2f closes the chunk with the full milestone
battery under nice and removes the two pre-existing cross-build gaps
recorded at chunk 4-6, chunk-4-8 slice C, and slice N:

- internal/security/security_test.go now carries the !windows build
  tag (matching the production split: security.go is !windows; the
  Windows stub has no creator identity), fixing the pre-existing
  unix.Geteuid vet failure of the windows test tree.
- internal/live/link_machine.go and link_machine_test.go now carry
  the (freebsd || v4work) && !windows guard: the machine needs the
  POSIX Linkat and directory-identity primitives, which have no
  Windows counterpart; it stays fully built on freebsd production and
  on the linux/darwin v4work test hosts (the only hosts where the
  v4work suite runs), and the windows v4work build/vet of the whole
  tree is green for the first time.

Validation (all under nice; full battery recorded per the resource
budget rule, ~2-3 core-minutes): gofmt clean; vet clean plain +
v4work on linux; GOOS=windows vet clean plain + v4work (whole tree);
GOOS=freebsd v4work vet clean; plain and v4work full trees 14
packages ok each (833 Test functions runnable under v4work, 194 in
publication, unchanged); -race + -gcflags=all=-d=checkptr=2 on
publication, writer, mapping, snapshot, live, and the root package
(plain and v4work); seven cross-builds (linux/386, linux/arm64,
darwin/amd64, darwin/arm64, freebsd/amd64, netbsd/amd64,
windows/amd64) plain and v4work; zero-alloc gates
(TestNoPageSizedHeapAllocations, TestNoPageSizedHeapAllocations
PublishSet, TestSnapshotOutputWarmLookupsZeroAllocation) PASS; the
Go conformance corpus cross-open and invalid-mutation gates PASS; the
Rust suite (411 tests plus the fixture suites) unchanged and green.
All validation PASS at 6d9ab2f.

Next: five-aspect review of the slice-O delta (same five reviewers;
the review-round-2 verdicts on slice N are recorded above), then the
milestone-1 close push per the user decision.

### Status (2026-08-24) - milestone 1 close: all five aspects PASS on the chunk 4-8 delta

The chunk 4-8 delta (slices A-O; this entry records the slice-N and
slice-O close verdicts) passes the five-aspect review gate. Heads are
7d95412 for the slice-N round-2 fixes and 426d597 for the slice-O
close; both were reviewed by the same five level-1 reviewers on the
lead's model, adversarial mode, scopes per the top-of-SOW split:

- Rust parity (Goodall): PASS. The rejectLiveSelf symlink fold at
  7d95412 matches the Rust open_regular O_NOFOLLOW -> NotRegular
  path; the slice-O build-tag guards are parity-neutral on every host
  where the suite runs.
- Go idioms (Gauss): PASS. Build-tag placement and comments follow
  the repo's platform-split conventions; one records nit fixed at
  6400b93.
- Absolute performance (Chandrasekhar): PASS. No host's compiled
  surface changed in the slice-O delta; the zero-alloc gates re-ran
  green at HEAD.
- Wire format and integrity (Herschel): PASS. Zero diff across the
  whole slice range in the codecs, lock offsets, reservation
  machines, and the conformance corpus; Go cross-open and
  invalid-mutation gates green; the Rust suite unchanged.
- APIs, docs, records (Noether): PASS after the records fixes at
  6400b93 and 426d597 (cross-build count, Removed-bullet accuracy,
  superseded Next lines, commit anchors, provenance wording).

The full validation battery (recorded in the slice-O entry) ran under
nice on the real tree: vet plain + v4work (linux and windows, the
latter green for the first time with the two pre-existing gaps
closed at 6d9ab2f), plain and v4work full trees 14 packages ok each
(833 v4work test functions, 194 in publication), race + checkptr on
the core packages, seven cross-builds both tag sets, zero-alloc and
conformance gates, and the unchanged Rust suite. The branch pushes to
origin/master with this entry (61 commits ahead at the close, all
signed); milestone 1 (the pure-Go exact v4 port with the singular
fixed-tree authority, the mmap-only ownership split, and the
reviewer-enforced gates) is closed per the user decision.

Next: milestone 2 - the next M3 surface (feed workflows,
draft_store membership and structured applies, and the range-edit
callers), following the same review process.
