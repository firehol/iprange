# SOW-0025 - Pure-Go Exact v4 Semantic Port

## Lead swarm operating rules (user decision, 2026-08-17)

These rules govern this SOW's review process and override the generic
shared swarm guide for this work (the guide's "residents"/"sol" roles
map to level-1/level-2 below). They are placed at the top so they are
re-read after every compaction.

1. Level-1 reviewers: five aspect reviewers on the lead's own model
   (copies), each holding one disjoint aspect of the milestone scope,
   adversarial mode. Spawn all five once at first use; every later
   round is a message to the same agents. Never respawn between rounds.
2. Level-2 final gate (in place of sol): glm, kimi, minimax, mimo,
   full-scope review after every available level-1 reviewer has
   returned PASS. Spawn once at first use, then reuse.
3. The milestone closes only when every available level-2 reviewer
   reports no P0-P2 findings. Unavailable reviewers (technical/quota)
   are skipped, do not count in the PASS denominator, and are reported
   to the user as reduced coverage.
4. Aspect split for milestone 2 (writer): (1) writer-core semantics vs
   the Rust authority; (2) mmap-only/memory-safety/lifetime; (3) wire
   format and integrity incl. Go<->Rust cross-open; (4) public API,
   docs and records; (5) durability and crash/resource semantics.

Recorded as Review Process step 6 below.

## Status

Status: in-progress

### 2026-08-18 - user decision: all builds, tests, and benchmarks run under nice

The user requires every test, benchmark, and heavy build invocation to run
under `nice` so toolchain work never competes with the interactive desktop.
Applied repo-wide: run-tests.sh, run-unit-tests.sh, run-build-tests.sh,
run-sanitizer-tests.sh, the Makefile check target (Makefile.am; Makefile.in is generated and gitignored),
v4/go/check-import-graph.sh, v4/rust/check-source-graph.sh,
v4/rust/check-mmap-runtime.sh, and the tests.build.d harnesses now prefix
`nice` internally; every documented command in AGENTS.md, README.md,
v4/rust/README.md, v4/conformance/README.md, tests.d/README.md, and the
project-v4-rust skill references `nice` explicitly. SOW historical records
are dated narrative and remain as written.


Sub-state: milestone 1 external-review rework IMPLEMENTED and validated
(2026-08-14); the six-resident swarm PASSes at HEAD 93b0f07; the swarm
round at HEAD 2ad4001 returned nine gate findings, all reproduced by
the lead and fixed at HEAD f96d13d with battery pins; the round at HEAD
57522e8 returned ten further gate findings (Luna) plus a multi-result
struct-field taint gap (MiniMax P2), all reproduced by the lead and
fixed at HEAD 65ca62a with battery pins; the round re-review at HEAD 2f5f71a added Qwen's cross-file
package func-var gap (var factory func() any declared in one file,
called from another, escaped the interface-result rule), reproduced by
the lead (gate exit 0) and fixed at HEAD 0d007a8 with battery pin; the round re-review at HEAD 83ea65b
returned nine further gate findings (Luna), all reproduced by the lead
and fixed with durable battery pins (round-6 entry below); the round-8
re-review returned thirteen further gate findings (Luna), all
probe-verified and fixed with durable battery pins (round-9 entry
below); later delta reviews continued the same verify-and-pin cycle
through round 36 (round-by-round history is preserved in the appendix).
The current gate review state is closed at the resident level and awaits
the next independent final gate after the latest verified fixes. All
three original review
findings are fixed: the hot path has one authoritative key-only search
primitive with test-only necessary-work counters and benchmarks; the
mmap gate is an 13,300-line typed toolchain (12,748-line go/types module
plus the 552-line shell boundary/self-test harness, down from 14,519)
that detects complete-page ownership; follow-up swarm rounds closed
nineteen bypass classes (function-variable callees, closure/defer/go
bodies, func-literal variables, reassigned function variables,
multi-hop function-variable chains, range-rebound variables,
address-taken variables, helper-summary parameter flow, local closures
and function aliases, container element extraction, pointer and
type-parameter page taint, branch/loop state joins, multi-result
assignment slots and struct-result field taint, named array/string
conversion sinks, file-capability laundering, interface-typed
parameter assertions, multi-dim index chains, and selector chains
over indexed bases) plus the round-5,
round-6, round-7, round-8, round-9, round-10, round-11, round-12, round-13, round-14, round-15, round-16, round-17, round-18, round-19, round-20, round-21, round-22, round-23, round-24, and round-25
classes recorded in the entries below, all with durable battery pins; the Status is compact with the
full history in the appendix. The round at HEAD f478278 returned six
further gate findings (Luna): one-sided branch joins trusted an
unproven local callable, selector-valued arguments lost struct-field
provenance, promoted embedded struct fields bypassed parameter-field
resolution, variable-held map keys lost container field provenance,
bounded param-field flow collapsed to unknown, and stale Status
metrics; all probe-verified and fixed with durable battery pins in the
round-14 entry below. The durable battery is 436 cases (368
rejections, 68 benign acceptances) plus 9 shell environment mutations
and passes end to end. The round at HEAD 0808c1e returned six further
gate findings (Luna): a switch without default kept the pre-switch
callable binding reachable after the join, dereferenced/indexed/
type-asserted arguments lost struct-field provenance, a naked
multi-result return forwarded only the first result slot, and a
concrete method on an externally declared receiver type could mint a
file descriptor outside the mapping owner; the channel finding was a
false positive (every channel probe shape was already rejected) and
the map-literal variant is invalid Go (a []byte field makes the key
non-comparable); all verified real findings were probe-verified and
fixed with durable battery pins in the round-15 entry below. The round
at HEAD e03ecba returned five further gate findings (Luna): external
method-value receivers hid a page-bearing receiver from the transfer
check, partial local struct records suppressed parameter leaf taints,
call-produced containers and struct values lost element/field
provenance in argument flow, returned struct parameters and container
elements lost caller field provenance through identity helpers, and
promoted embedded leaves were absent from the parameter leaf
fallback; four were reproduced by the lead as real escapes before any
fix (durable pins P162-P165) and the external method-value case is
code-review-verified because the loader cannot resolve net and no
loadable external concrete method returns a page-bearing value; all
five are fixed with durable battery pins in the round-16 entry below.
The lead's same-failure search over the round-16 fixes found five
further escapes in the same family (struct-valued BINDINGS from
call-produced and selector sources lost field provenance, and nested
call-produced selector chains never resolved), all probe-verified
before any fix and pinned P166-P170 in the round-17 entry below. The
round-18 same-failure sweep covered the interface-type-assertion
family (returned asserted interface-param structs, two-value asserted
reads, and explicit conversion argument flow) and the multi-dim index
family (field reads and element bindings through multi-level index
chains and forced literal element extraction), all probe-verified
before any fix and pinned P171-P182 in the round-18 entry below. The round-19 delta
returned twelve further gate findings (Luna) on the round-18 fixes; the
lead reproduced eight real escape classes on a fresh HEAD build before
any fix: an EMPTY default (default:) discarded the pre-switch state
(P183), returned selected fields of call results (P184) and returned
elements of inline literal indexes (P185) lost provenance in the return
summary, a container PARAMETER under range never bound its declared
element leaves (P186), a nested selector read after an interface
assertion resolved only the asserted type's direct leaf (P187), an
asserted struct VALUE bound to an argument lost its asserted leaves
(P188), a NAMED container type (type matrix [1][1]box) unwrapped no
underlying chain (P189), and a map-key parameter kept no key leaves for
a key-only range (P190); the lead's same-failure sweep over those fixes
proved two further escapes on the same HEAD build, the variable-element
container literal (P191) and the map composite-literal key (P192), and
pinned all ten. The remaining findings were verified not real: one pair
of intentionally accepted shapes stays accepted (recorded as a known
open question) and three reports were false positives or dead code; the
stale-record report is closed by this record sync. The round-20
delta re-review returned six further gate findings (Luna): unproven
scalar indirect calls hiding page copies, selector-valued containers
losing element provenance under range, selector-valued arguments losing
partial field provenance after a clean sibling store, named map types
losing key provenance, append-built struct containers losing element
fields, and dereferenced struct stores losing field provenance. The
lead probe-verified three real escape families on the round-19 build
before any fix (selector-held container elements under range/indexed
read/take argument, named map key-only range, dereference struct
stores) and fixed them with durable battery pins P193-P199 in the
round-20 entry below; the other three findings were probe-verified
false positives (every scalar indirect-call shape, every clean
sibling-store shape, and every append-built container shape already
rejects). The round-21 delta
re-review returned eight further gate findings (Luna); the lead
probe-verified all eight as real escapes on the round-20 build and
fixed them with durable battery pins P200-P209 in the round-21 entry
below: two-variable map ranges bound the key fields to the value
variable, container literal elements produced by calls or addresses
lost their fields, indexed whole-struct stores into field containers
and dereference stores through aliased pointers missed the record,
pointer-receiver method mutations never reached the caller's variable,
struct field provenance died at channel sends, address-of arguments
lost the variable's fields, interface calls returning containers
failed open on element fields, and ranges over type-converted
containers lost the element leaves. The round-22 delta
re-review returned nine further gate findings (Luna); the lead
probe-verified all nine on the true round-21 build: eight were real
escapes (nested opaque interface-result container fields, two-variable
map ranges over nested container values and pointer-wrapped keys,
address-of selected-field and indexed-element mutation arguments,
directly called func-literal pointer-parameter mutations, struct-field
channel send/receive and select-send provenance, dereferenced indexed
whole-struct stores, and runtime map-key stores through field
containers) and one (l22-4/4b, closure materialization) is a false positive:
the probe shape carries the page view into append and the gate
already rejected it; the eight real escapes and the l22-9 false
rejection are fixed with durable battery pins P210-P218 in the
round-22 entry below. The round-23 delta
re-review returned seven further gate findings (Luna); the lead
probe-verified all seven on the true round-22 build: five were real
escape classes (type assertions on func-field any call results and
type-switch variables from the same base projected no asserted leaves,
pointer-mutation methods on INDEXED elements never bound the container,
indexed channel send/receive lost element provenance, and
pointer-wrapped map parameters lost key-only range leaves), two are
false positives (foreign exported struct fields skipped by
failClosedCallFields are unreachable because banned imports and
unloadable stdlib packages fail the gate closed, and variadic direct
func-literal argument flow is already rejected in every probe shape),
and the l23-5 function-call variant (set(xs[0], page)) is invalid Go:
an indexed element is not addressable without an ampersand, so only the
method form is a real class; the five real classes are fixed with
durable battery pins P219-P223 in the round-23 entry below. The
same re-review then returned two further verified findings (Qwen): a
two-value type assertion or map index (b, ok := v.([]byte), b, ok :=
m[k]) types the expression node as the (T, bool) tuple, so the
whole-value carrier test dropped the page taint of every comma-ok
byte read (probe-verified escape, pinned P224), and the unguarded
named-pointer recursion (type P *P) through derefStruct/mapUnderlying
hung the scanner on an unproven callee returning the self-pointer type
(probe-verified 90s timeout, pinned P225); both fixed in the round-23
entry below. The
round-25 delta review fixed nine further gate escape classes (type-asserted
indexed channels, stores, map keys, mutation arguments and returns,
interface-typed type-switch cases, and the same-family sweep of asserted
field-map key ranges and returned/bound asserted selectors under
interface-typed switch cases) and one reader divergence (membership
ContainsIndex skipped the trailing-word canonical check); details in the
round-25 entry below. The durable battery is 578 cases (495 rejections,
83 benign acceptances) plus 9 shell environment mutations and passes end
to end.
Milestone 2 (writer) is authorized to start: the final milestone-1 check
passed at HEAD 4f11e3d (2026-08-17, entry below) and the user authorized
proceeding to the writer once milestone 1 closes.

Rework outcome per finding:

- P1 hot path - fixed: one authoritative search primitive
  (internal/reader/search.go greatestLE) with key-only probes and a single
  decode of the selected record now drives every range/catalog/membership/
  blob lookup (decisions 1A-5A below; implementation record 2026-08-14).
  Test-only necessary-work counters (internal/work behind -tags v4work,
  const false no-op in production) pin probes/visits/descends/decodes;
  reader benchmarks on committed fixtures and a CPU profile were added and
  are recorded in the implementation record.
- P1 enforcement - fixed: the type-light 4,738-line AST taint engine and
  the 9,781-line shell battery were replaced by a type-aware go/types
  scanner (v4/go-gate) with the complete-page ownership rule
  (copy/append/array-conversion sinks at or above PageSize, spec
  binary-format-v4.md:108) plus the file-capability and text-ban families.
  The durable mutation battery moved into the tool as table data
  (578 cases: 495 rejections, 83 benign); the shell harness keeps only the import-boundary, module-graph,
  x/sys-ownership and environment checks (552 lines) and the self-test
  invocation. A production function that copies a mapped page into an
  owned [4096]byte now fails the gate with a specific rule violation
  (probe matrix recorded below).
- P2 records - fixed: this Status is compact; the round-by-round history
  is preserved verbatim in "## Status History (appendix)" at the end.

Current state: the pure-Go immutable reader implements the exact Phase-1
wire contract, opens all six Rust-produced conformance fixtures with exact
semantics, rejects the three invalid corpus mutations with the typed
FormatInvalid class, keeps warm lookups and scans at zero heap allocation,
holds the mapping owner in internal/mapping (mmap-only access), and passes
go test (both tag sets), -race/checkptr, vet, gofmt, cross-compilation,
the import-graph gate with its 578-case battery, and the SOW audit.
Module production 5,182 raw lines (reader core 1,871 across the 7
production files in internal/reader; the 5k directional goal is met),
module tests 6,410 raw
lines; gate tooling 13,300 raw lines total (12,748 go-gate + 552 shell). Hot-path benchmarks on the
synthetic multi-level tree: LookupDirect4 159 ns/op, direct-6 69 ns/op,
membership word 95 ns/op, all 0 allocs/op (full table in the
implementation record below).


Final check (2026-08-17, HEAD 4f11e3d): the five-resident full-scope swarm
(glm, kimi, qwen, minimax, mimo) reviewed milestone 1 under the
user-approved gate-review boundary (Review Process step 5). glm, kimi,
minimax, and mimo returned PASS with independent zero-trust evidence
(wire parity of codecs vs the Rust authority and binary-format-v4.md,
mapping owner and remap fail-closed states, hot-path probes and pin
lifetime, gate families and the 578-case battery, import graph, records).
qwen was unavailable (no response; technical skip); sol and luna were
unavailable (weekly quota exhausted), so the final gate closed on the
available-resident quorum per the user decision; reduced review coverage
is reported to the user. Lead re-verification at close: production gate
scan rc=0, battery 578/578 (495 rejections, 83 benign acceptances), go
test ./... (both tag sets), -race/checkptr, vet, gofmt, import graph,
SOW audit - all green. Milestone 1: CLOSED.



Milestone 2 kickoff (2026-08-17, HEAD 81c9443): the mapped COW writer and
Go producer starts now per the user decision (proceed to the next
milestone after milestone 1 closes). Authority: the Rust writer
(writer_core open/edit/publication/close/reclaim, used_bitmap/free_bitmap
mutation, retirement, commit_resolution, private_page_pool, page_io,
page_checksum) and binary-format-v4.md; the Go writer mirrors its
semantics with one authoritative physical implementation, mmap-only
(full pages constructed only at final offsets in the file-backed
mapping; no complete page in owned memory), matching the project's
zero-copy, single-authority, and clean-code rules. Planned chunks
(recorded in the Pre-Implementation Gate implementation plan):
1. M2 gap analysis - Rust writer public/internal export inventory
   mapped to Go packages, writer risk register (COW ownership,
   dirty-page sealing, durability, abort, reclaim), same-authority
   search.
2. Writer foundation in v4/go: mutable mmap owner mode (write/
   MAP_SHARED, growth/remap), page checksum, meta publication with
   commit nonce/generation rules, free/used bitmap mutation,
   retirement, commit resolution, abort and reclaim, mirroring the
   Rust core semantics exactly.
3. COW page-edit layer: final-offset construction for direct/
   membership/structured trees, draft store with declared page
   budget, typed high-level construction entry points (public Go
   generation API).
4. Go-produced corpus: fixtures built only through public Go
   operations, verified against cases.json, opened by Rust; mixed
   Rust/Go subprocess gates in both directions; literal writer
   vectors pinned from the Rust writer tests.
Validation per chunk: go test (both tag sets), -race/checkptr, vet,
gofmt, gate scan + battery on the new production surface, SOW audit;
cross-open is the milestone gate.

Milestone 2 chunk 1 - writer foundation (2026-08-17, HEAD bf49779):
this entry supersedes the 578-case gate battery counts in the M1
close-out paragraphs: the gate battery is now 601 cases (515
rejections, 86 benign acceptances) plus 9 shell environment mutations.
mapping write mode and the page checksum authority are implemented and
pinned. internal/mapping now opens mutable mappings (O_RDWR + exclusive
lifetime lock + PROT_READ|PROT_WRITE MAP_SHARED of the two-page
bootstrap extent, then Remap(committed) after the meta pair is proven,
mirroring Rust database_file.rs map_writer and live_lock exclusive),
grows them (ftruncate + remap, mirroring resize), and flushes them
(msync / fsync, mirroring flush_range / sync_file); the open path is one shared
implementation (openMapping) for readers and the writer, and the Windows
stub refuses the new methods like every other owner method. The CRC
authority in internal/format gained the non-meta page seal exactly as
Rust page_checksum.rs defines it (offset 28, length 4, zeroed-field
CRC-32C): CRC32CWithZeroed, PageChecksumValid, SealPageChecksum. Gap
analysis: .agents/sow/pending/pure-go-v4-port-milestone-2-gap-analysis.md.
Validation: go test ./... (both tag sets) + -race + vet + gofmt +
production gate scan rc=0 + import-graph check + 11/11 cross-compiles;
new tests pin write visibility through MAP_SHARED, Grow, flush/sync
round-trip, exclusive-lock exclusion of readers, grow refusals, and the
checksum seal lifecycle with the Castagnoli check vector.

Milestone 2 chunk 1 - level-1 review round 1 and fix round (2026-08-17,
HEAD ff27d73 -> uncommitted fix tree): the five aspect reviewers
(Jason, Linnaeus, Peirce, Sartre, Leibniz) ran the first level-1 round
over chunk 1. Four returned FAIL with P1/P2 findings; one returned PASS.
All findings were reproduced by the lead and fixed in the working tree
(commit pending delta re-review):

- P1/P2 FreeBSD writer compliance (Jason, Leibniz, Sartre): the spec
  (binary-format-v4.md:2403-2411) and the Rust authority
  (live_lock.rs require_live_supported) refuse a live writer on FreeBSD;
  the Go writer previously substituted whole-file flock LOCK_EX.
  Fixed: requireLiveWriter() now returns CodeLiveCoordinationUnsupported
  (44, the pinned SDK class) before any path access on FreeBSD and the
  fallback-OS set, nil on Linux/darwin, and lockLifetimeExclusive stays
  a typed CodeOSUnsupported refusal for defense in depth (mirroring the
  Rust lock-primitive class).
- P1/P2 macOS durability (Jason, Leibniz): plain fsync on macOS can
  return before the drive's volatile write cache is flushed. Fixed:
  syncFile now uses fcntl(F_FULLFSYNC) on darwin (mapping_sync_darwin.go)
  and fsync elsewhere (mapping_sync_posix.go); the gate gained a narrow
  per-file exemption for the exact unix.FcntlInt call in
  mapping_sync_darwin.go only, with the existing FcntlInt battery pin
  (case 240) still rejecting every other location.
- P2 writer open bootstrap (Jason, Leibniz): the writer previously
  mapped the full physical extent at open, making an unpublished
  corrupt tail writable and costing VA; Rust map_writer bootstraps
  2 pages then remaps to the committed extent. Fixed: openMapping now
  maps exactly 2*PageSize for both modes and OpenMutable documents the
  Remap(committed) step before editing.
- P2 Grow below physical extent (Linnaeus): Grow(newSize) with
  newSize below the opened physical extent previously fell through to
  ftruncate and could truncate the file. Fixed: Grow refuses with
  FormatInvalid ("grow below the opened physical extent"); pinned by
  TestGrowBelowPhysicalRefused.
- P2 fail-closed Remap/Grow (Linnaeus; Jason P3): Linux mremap failure
  previously restored the old slice and leaked the mapping on the
  fallback path. Fixed: both failure paths munmap any returned slice
  and set size=0, matching Rust replace_map (map=None, len=0 -> WrongState);
  Close guards m.data != nil; pinned by TestCloseAfterRemapFailure.
- P2 writer view lifetime (Linnaeus): writer views alias the mapping
  with no pin guard; Grow/Remap (mremap may move) invalidate them.
  Fixed: package doc states the discipline, and
  TestViewRefetchAfterGrow pins refetch-after-grow behavior.
- P1 gate socket-direction x/sys variants + big.Int.SetBytes (qwen,
  late M1-level gate findings ported to the M2 gate): unix.Recvfrom/
  Recvmsg/Recvmmsg and Sendto/Sendmsg/Sendmmsg joined the banned
  selector set, and math/big (*Int).SetBytes joined ownedCopySink
  (a full mapped page must not land in owned big.Int limbs). Battery
  grew 578 -> 586 cases (291-298; 7 rejections, 1 benign); the
  pre-existing FcntlInt dup pin (240) still rejects every FcntlInt
  outside mapping_sync_darwin.go.
Milestone 2 chunk 1 - level-1 review round 2 and close (2026-08-17,
HEAD 7c08889/745da34): all five aspect reviewers passed their delta
re-review after two fix waves.

- Jason (writer-core semantics): PASS. Round-1 P2 - the FreeBSD/
  fallback live refusal used CodeOSUnsupported (58) instead of the
  pinned LiveCoordinationUnsupported (44, Rust live_lock.rs
  require_live_supported; spec binary-format-v4.md:2406-2409). Fixed
  in d46f4aa (requireLiveWriter returns 44; lockLifetimeExclusive
  keeps 58 as the lock-primitive class). Jason also caught that the
  SOW record still named the old class; corrected in 7c08889.
- Linnaeus (mmap/lifetime): PASS. All four round-1 blockers verified
  fixed (FreeBSD gate, Grow below-physical guard, fail-closed
  munmap/Close, view refetch discipline).
- Peirce (wire format/integrity): PASS. Zero wire-format or checksum
  delta vs the round-1 reviewed state; bootstrap/remap mirrors Rust
  map_writer; conformance corpus untouched.
- Sartre (public API/docs/records): PASS. P3 cleanups applied:
  PhysicalSize and Remap extent wording, gap-analysis status header,
  case-240 wording in the fix-round entry.
- Leibniz (durability/crash): PASS after three waves. Round-2 P2: the
  lifecycle-owner rule covered only Fsync/Ftruncate/Msync while the
  risk register declared it a hard single-authority rule; the gate now
  name-bans the full same-class family - Fdatasync, Syncfs, Sync,
  path-based Truncate (unix/os) in d91fe3a, then Fallocate and
  SyncFileRange (the spec's sparse-extension and range-durability
  primitives) in 745da34, all allowed only inside internal/mapping.
  Leibniz mechanistically enumerated all 369 x/sys unix exports and
  confirmed no remaining gate-silent durability/flush/geometry
  primitive. Battery grew 578 -> 586 (round 1) -> 597 (round 2) -> 599
  (round 3, 514 rejections, 85 benign), every new reject pin
  vacuity-proven against the pre-fix rules; production gate scan rc=0
  across the 5 OS configs after every wave; go test (both tag sets),
  -race, vet, gofmt, import-graph, and 11/11 cross-compiles stay
  green.

Level-1 result: 5/5 PASS at HEAD 745da34. Level-2 final gate follows.

Milestone 2 chunk 1 - level-2 gate round 1 and fix round (2026-08-17,
HEAD ebe8e46 -> d83fa28): kimi, minimax, and mimo returned PASS with
independent full-scope evidence (Rust-authority parity for
mapping/locks/checksum, gate family completeness, records truth,
attempted bypasses). glm returned FAIL with three findings, all fixed
in d83fa28:
- P0 CRC32CWithZeroed overflow: zeroAt+zeroLen could wrap past MaxInt
  and panic on slicing instead of reporting an invalid range; le.go
  now uses overflow-safe comparisons (Rust checksum.rs checked
  arithmetic) and TestCRC32CWithZeroedOverflow pins MaxInt offsets.
- P1 FcntlInt exemption scope: the darwin exemption was per-file and
  inherited every unix.FcntlInt command in mapping_sync_darwin.go
  (F_DUPFD/F_DUPFD_CLOEXEC/F_PREALLOCATE would pass the gate); it is
  now per-call, requiring the exact unix.F_FULLFSYNC command argument.
  Battery pins 312 (appended F_DUPFD_CLOEXEC in the darwin file
  rejects) and 313 (appended exact F_FULLFSYNC call stays legal).
- P2 records: the chunk-1 entry still claimed the writer maps the
  full extent at open; corrected to the two-page bootstrap followed by
  Remap(committed).
Battery 599 -> 601 (515 rejections, 86 benign); production gate scan
rc=0 (5 OS configs); go test (both tag sets), -race, vet, gofmt,
import-graph, 11/11 cross-compiles all green. GLM re-review returned
one records-only P2 (stale battery counts and missing level-2 entry),
fixed in de338fa (601/515/86, pin range 299-313, round-1 entry added);
GLM PASS at HEAD de338fa.

Milestone 2 chunk 1 - level-2 gate CLOSED (2026-08-17, HEAD de338fa):
all four available level-2 reviewers (glm, kimi, minimax, mimo)
report no P0-P2 findings; reduced coverage: none (qwen was not part of
the level-2 set for this chunk). The chunk-1 foundation is complete:
mutable mapping owner with per-platform live coordination, two-page
bootstrap + Remap(committed) mirroring map_writer, fail-closed
Remap/Grow matching replace_map, darwin F_FULLFSYNC durability, the
page checksum authority with the Castagnoli vector, and the gate at
601 cases covering content transfer, complete-page ownership, and the
full lifecycle-owner syscall family with vacuity-proven pins.

Milestone 2 chunk 2 - writer open (2026-08-17; design record at HEAD
7a90fb4, implementation committed at 56e8516, level-1 round-1 fix
round committed at 35a096b): the
writer open surface: map_writer / select_committed /
trim_committed_tail (Rust authority writer_core/open.rs +
database_file.rs map_writer), the shared bootstrap authority with the
writer finish rules, and the mapping Shrink primitive. Chunk-2
implementation decisions (lead, under the long-term-best /
minimal-complete rules; the user delegated technical design choices to
the swarm guidelines):
- One bootstrap authority: reader and writer share a new pure
  internal/bootstrap package mirroring Rust bootstrap.rs
  (open_meta_pages + finish_open incl. the Writer rule: selection must
  be ProvenCurrent; the immutable length check applies only to
  ImmutableReader). The reader is refactored to call it, so a
  selection-rule change cannot diverge between open modes. Only the
  two modes the milestone actually uses exist (ImmutableReader,
  Writer); the Rust LiveReader mode joins when the live-reader
  milestone needs it.
- Mapping Shrink mirrors Rust shrink_or_retain (unmap first, stat,
  ftruncate, remap, fail-closed on remap failure; physical < committed
  is the FormatInvalid Corrupt class; no-op when already trimmed;
  Windows stub refuses like every other owner method).
- Test-only mapping work counters (MappingRemap, MappingGrowth,
  MappingFlush, FileSync) mirror Rust work.rs; the import-graph gate
  extends the mapping owner's allowed set with internal/work and
  admits internal/bootstrap + internal/writer as internal importers.
- internal/writer exposes the three Rust mirrors plus an Open entry
  (OpenMutable -> bootstrap Writer mode -> Remap(committed) ->
  SelectCommitted -> TrimCommittedTail); the sidecar coordination of
  Rust live_writer.open_locked arrives with the M4 sidecar milestone.

Milestone 2 chunk 2 - writer open IMPLEMENTED (2026-08-17, committed at
56e8516, review-fix round committed at 35a096b):
the chunk-2 design decisions above are implemented and locally validated:
- internal/bootstrap: pure shared selection authority (Open with
  ImmutableReader/Writer modes, Result with Meta/Selection/SelectedMetaPage/
  CommittedBytes/PhysicalBytes), mirroring Rust bootstrap.rs
  open_meta_pages + finish_open (identity + per-meta validation + pair
  selection + mode rules: writer requires ProvenCurrent, immutable
  requires committed == physical, unknown structured kinds report
  UnsupportedStructure after selection). The reader was refactored onto
  it (MetaSelection is now an alias of bootstrap.Selection); the reader
  module lost its private validateMeta/selectBetween/sameIdentity, so
  one selection authority remains.
- internal/mapping.Shrink: mirrors Rust shrink_or_retain (unmap first,
  stat, ftruncate, remap; fail-closed on remap failure; physical <
  committed is the FormatInvalid Corrupt class; same-extent no-op;
  read-only/closed refusal WrongState; Windows stub refuses). The
  mapping owner now increments test-only work counters (MappingRemap/
  MappingGrowth/MappingFlush/FileSync, no-ops in production) mirroring
  Rust work.rs.
- internal/writer: Core with OpenWriter (map_writer mirror: OpenMutable
  -> Writer bootstrap -> Remap(committed)), SelectCommitted (Writer-mode
  re-derivation), TrimCommittedTail (shrink + SyncFile when physical !=
  committed), Open (the composed open_locked-minus-sidecar entry),
  PageBudget and WriterInfo mirrors. The sidecar coordination of Rust
  live_writer.open_locked arrives with the M4 sidecar milestone.
- Gate: import-graph allowlist admits internal/bootstrap + internal/
  writer, extends the mapping owner's allowed set with internal/work,
  and adds bootstrap to the sync-free zone; gatescan topoRank places
  bootstrap at leaf rank and writer with the reader.
- Tests: bootstrap selection matrix (equal/adjacent/gap/sole/identity/
  corrupt-CRC identity refusals/geometry/unknown-kind/tail rules), shrink contract (truncate+data
  survival+regrow, no-op, above-physical/unaligned/read-only/closed
  refusals), writer open (tail trim, no-tail no-op, empty 2-page db,
  primitive sequence, sole-meta/no-meta/bad-checksum/short-file
  refusals, budget+info round-trip), and v4work necessary-work pins
  (tailed open = 2 remaps + 1 file sync; no-tail = 1 remap; 2-page db =
  0; same-size no-ops count 0).
Validation so far: go test ./... (both tag sets), -race, vet, gofmt,
import-graph scan, 11/11 cross-compiles, and the 601-case gate battery
+ 9 shell mutations - all green at 56e8516. Level-1 round 1: 4/5
PASS; Leibniz FAILed on one P2 (writer open lacked the terminal
path-identity re-verification) plus Peirce's same-path P3. The round-1
fixes (VerifyIdentity in Open/OpenWriter after remap/trim, deterministic
replacement-race test, failed-open lock-release probes; plus the P3
cleanups incl. the doc-only disposition of the Shrink both-failure
error-reporting finding) were committed at 35a096b. The delta re-review
at 0c9dd95 reworked the lock-release probe to a same-inode pin
(TestRefusedOpenPathMovedReleasesLock renames the opened file itself out
of the way, so a leaked lock on the original inode genuinely blocks the
probe; trigger-verified by dropping the failure-path Close) and widened
VerifyIdentity's doc comment to name both Rust mirrors (mapping.go).
Accepted reduced coverage, recorded not waived: mapping_shrink_test.go is
linux-tagged, so the Shrink unmap-first contract is compiled (not run) on
darwin/freebsd cross-builds - no darwin host is available in the
validation harness. Level-2 final gate (full scope, chunk-2 surface):
kimi PASS, minimax PASS, mimo PASS; glm was unavailable (quota/technical,
recorded as reduced coverage per the review-organization decision) - no
P0-P2 findings were reported by any available level-2 reviewer. Chunk 2
gate: CLOSED.

Milestone 2 chunk 2 - level-1 review rounds (2026-08-17): five aspect
reviewers (writer-core parity, mmap/lifetime, wire/error classes,
records/gate, durability/lock) run over the chunk-2 surface.
- Round 1 at 56e8516: 4/5 PASS. FAIL: durability (1x P2 - writer open
  lacked the terminal path-identity re-verification: Rust open_locked
  ends with verify_pair after trim_committed_tail; a rename during open
  could publish a writer bound to a detached inode). P3s recorded:
  missing linux build tag on the shrink test (windows vet), stale
  remap/shrink comments, mapping doc missing Shrink in the
  view-invalidation wording, Shrink both-failure error reporting
  simplification vs Rust combine_errors, stale SOW headers and
  overclaiming test-record wording, gate sync-free-zone comment.
- Round 2 delta at 35a096b: Open/OpenWriter gained the check hook and
  terminal m.VerifyIdentity after remap/trim; deterministic
  replacement-race test (WrongState); assertReopen lock-release probes
  on every refusal test; the P3 list above fixed. Re-review: 4/5 PASS;
  FAIL: records (1x P2 - the committed records still claimed the fix
  round was an uncommitted working tree; plus 2 P3s: gap-analysis
  header lag, missing both-failure P3 disposition). Linnaeus P3: the
  promised Shrink lower-bound note was absent from the tree.
- Round 3 delta at 996cc9c: records corrected (fix commit named,
  gap-analysis header advanced, both-failure disposition recorded),
  Shrink no-lower-bound boundary documented (Rust parity - no floor
  check in shrink_or_retain; committed is never below two pages).
  Re-review: 4/5 PASS; FAIL: durability (1x P2 - the new lock-release
  probe in the replacement-race test was vacuous: after the rename the
  path named a different inode and OFD locks are per-inode, so a
  leaked lock was unobservable; trigger experiment: removing the
  failure-path Close did not fail the probe).
- Round 4 delta at 3987142: the vacuous probe was removed (scoped
  comment) and TestRefusedOpenPathMovedReleasesLock added - the open
  hook renames the opened file itself out of the way, VerifyIdentity
  refuses with NameNotFound on the same inode, and the reopen probe
  genuinely contends with a leaked lock; trigger-verified (dropping
  the failure-path Close fails the probe at the 5s timeout). The
  darwin/freebsd Shrink coverage boundary (compiled, not run) is
  recorded as accepted reduced coverage. Re-review: 5/5 PASS, no
  P0-P2. Level-1 gate for chunk 2: PASS.

Milestone 2 chunk 2 - level-2 final gate (2026-08-17): full-scope
adversarial review of the chunk-2 surface at HEAD 3987142. kimi: PASS
(P3 only - Remap lacks Grow's explicit oversize check; on 32-bit a
>maxInt extent fails fail-closed, never OOB; cosmetic asymmetry). mimo:
PASS (exhaustive per-file audit; zero findings). minimax: PASS
(independent adversarial attempts incl. FIFO refusal, tailed trim,
detached-inode probes, freebsd typed refusal, txn-gap, structured-kind
validation, page-count-beyond-physical, shrink-below-physical - all
blocked at the correct classes; P3s informational). glm: unavailable
(no response across two 10-minute waits; quota/technical per the
round-1 note) - recorded as reduced coverage, never respawned. Chunk 2
(writer open surface) gate: CLOSED.

Milestone 2 chunk 3a - COW page-edit storage surface + gate extensions
(2026-08-17; committed at d9990b9, HEAD before commit 066fc6a): the
storage surface of chunk 3 (COW page-edit layer), implemented and
locally validated:
- internal/tree: the generic COW fixed-tree core mirroring Rust
  fixed_tree (page.rs lower_bound/lower_bound_by key-only probes,
  branch/leaf descent, cell codec surface; insert.rs leaf targets and
  split propagation; delete.rs path collapse; read.rs Predecessor/
  AtOrAfter/adjacent-leaf cursor; walk.rs release/visit). One
  authoritative search primitive serves both read and edit paths, with
  test-only necessary-work counters (KeyProbe/CellProbe/SlotRead/
  EditFitProbe/FirstFenceUpdate).
- internal/bitmap: free-page bitmap COW mutation (free.rs) and the
  used-bitmap authority (used.rs) mirroring Rust free_bitmap +
  used_bitmap: hierarchical word/leaf geometry, lowest-zero selection,
  membership-zero exclusion, committed-path privatization,
  allocation-protection over the live meta roots, and COW descent via
  the tree core.
- internal/retire: the ordered retirement-extent B+tree mirroring the
  Rust retirement module: (transaction, first, count) extents,
  neighbor coalescing, oldest-safe-transaction reclamation selection.
- internal/format put.go: the write-side page authority mirroring Rust
  page_header.rs initialize + slotted_page.rs mutation; writes only
  into caller-supplied mapped views, checksum seal counted by work.
- internal/writer Draft + DraftStore (draft.go, draft_store.go): one
  mapped COW draft storage surface mirroring Rust draft_store.rs +
  storage.rs - private-page stack (dirty-chain tags in the checksum
  slot), allocation from private stack / committed free bitmap /
  allocator reserve / file tail, CopyPage with mapped src/output
  callback, DiscardPrivate, retire backlog, seal/charge accounting,
  and test-only BytesMoved/BytesZeroed/PageCreated/page counters. The
  editor entry points and workflow state machines (range/membership/
  structure deltas) arrive in chunk 3b.
- Gate: dispatch approval and the store-callback closure are scoped
  by NAME, not by declaration site. The only approved module-internal
  interface dispatches are tree.Store, tree.RetiringStore,
  tree.Codec, and bitmap.BitmapStore (approvedModuleInterfaces):
  their method sets have no satisfier outside the scanned source
  (Codec references internal tree.Key; the Store-family method names
  plus exact signatures exist nowhere in the stdlib or x/sys). Every
  other interface - including module-declared ones whose method set a
  stdlib type satisfies (interface UnmarshalText([]byte) error on
  *big.Int) - is an unproven indirection that fails closed on
  page-bearing arguments (new battery pin P242). The two fail-closed
  checks that keep concrete page carriers visible through the erased
  receiver still apply to every module-declared interface:
  interface receivers that concretely carry a full page fail at the
  dispatch (P119), and field-hidden full-page arguments to unproven
  callback callees fail at the call. The store-callback seeding (only
  on the four approved names) is counter-checked at the
  IMPLEMENTATION site: Inspect/Update/CopyPage implementations on an
  approved store interface must invoke their callback formal only
  with mapped views (P243 dishonest-store pin); an owned callback
  buffer would make the seeding bless copies of complete mapped pages
  into owned memory. The scanned-callback fence (checkFuncTypedArgs)
  requires func-typed arguments of approved module callees to bottom
  out in scanned bodies; func-typed FORMAL PARAMETERS are the approved
  exception (call sites are scanned). Field-full-page promotion
  covers expressions whose whole value is already full-page taint
  (P119/P130/P131-style launders fail closed while interface-PARAMETER
  fallbacks stay benign). Param copy pairs (copy(paramD, paramS))
  recorded in callee summaries are enforced at the binding call site
  (checkParamCopyCalls). Battery: 608 cases (519 rejections, 89
  benign acceptances); P57 reclassified to benign (scanned literal
  callback; intent pinned by P136 + P42/P67/P68), P130/P131/P132
  field-promotion pins, P119 module-internal interface receiver pin,
  P137/P138 store-callback pins rewritten to dispatch through the
  real tree.Store (the approved surface they model), P242
  stdlib-satisfiable-interface pin, P243 dishonest-store pin.
Validation so far: go test ./... (both tag sets), -race on
tree/bitmap/retire/writer/format, vet, gofmt, import-graph scan with
the new tree/bitmap/retire boundaries, 11/11 cross-builds, and the
608-case gate battery - all green, battery with zero misses.
Work-counter pins cover one-shot reads visiting each path page once,
lower-bound reuse of its final probe, fixed replacement single
capacity probe, deletion single tree lookup, same-path set-free
exactly two bitmap probes, draft PagesCreated/BytesZeroed/BytesMoved/
MappingGrowths.

Review round 1 (level-1 swarm, 2026-08-17): five aspect reviewers at
d9990b9/eaa4d26; findings verified and fixed in this round:
- P2 (gate): module-internal dispatch approval accepted ANY
  module-declared interface; a stdlib-satisfiable method set
  (UnmarshalText on *big.Int) passed a full mapped page with zero
  diagnostics. Fixed by named-set approval + P242 pin; the SOW claim
  now matches the implementation (Store/RetiringStore/BitmapStore/
  Codec only).
- P1->P2 (gate): store-callback seeding was trust-based; an owned
  buffer implementation made mapped->owned copies silent. Fixed by
  the implementation-site mapped-view requirement + P243 pin.
- P2 (wire parity): Go bitmap headerProblem omitted the Rust
  common_valid magic/flags/header-size checks, so a torn page could
  be COW-copied and re-sealed with a valid CRC. Fixed in
  bitmap_page.go with a bounded magic compare + parity tests.
- P3 (parity) fixed: splitDifference >= -> > (Rust >), retirement
  After u32 wrap to the next transaction, error-class alignment
  (Unsupported->58 for requireCodec/newBranchCell/run-removal,
  ArithmeticOverflow->60 for retire grow/insert/remove and
  childBaseAt, Corrupt->32 for the fixedPositions gap), findLowest
  no-summary now reports no candidate (Rust Found(None)) instead of
  bit 0, mapped provenance preserved through function summaries so
  the store-callback rule sees mapping views across helper calls.
- Duplicate battery case numbers P136/P137/P138 (two cases each)
  remain as-is (reporting cosmetics only; both run); not renumbered
  to keep the shell battery history stable.
- Accepted-with-record P3s: retire encode/decode boxing per extent
  and put.go fixed-position bookkeeping allocations are bounded and
  commit-path only; revisit under chunk 3b with commit benchmarks.

Review round 2 (level-1 swarm, 2026-08-17): aspect reviewers at
579f8a1. Linnaeus reported one P2: the implementation-site
store-callback mapped-view counter-check was bypassed when the
callback formal was FORWARDED through a scanned helper (runCbR2(fn,
s.buf, s.buf) with a helper invoking its own func formal with its own
byte parameters); the direct, local-alias, and struct-field forms
were already rejected. Probe-verified as a real escape on the round-1
build (zero diagnostics for the helper form), then fixed:
funcSummary now records callbackInvokes (func-typed formal slot ->
byte-parameter slots it is invoked with) and callbackInvokesInternal
(untraceable byte invocations) at the definition site
(noteCallbackInvokes), composes them through call chains like
copyParams (recordCallbackInvokeComposition), and
checkCallbackInvokeCalls enforces the store contract at the
store-implementation call site: the forwarded callback formal must
receive only mapped views through the helper. Linnaeus's round-3
re-check then found the first cut false-positived on an honest store
whose internal helper MINTS the mapped views itself (fetch-inside-
helper: fn(a, b) with a, b from s.r.page); probe-verified, then
fixed: the definition-site record evaluates untraceable byte
arguments for mapped provenance (mints stay honest, owned/unknown
buffers still fail closed) and the records are rebuilt every fixpoint
pass instead of accumulating an early unstable pass's verdict. New
battery pins P297 (fail: helper-forwarded owned buffers) and P298
(benign: helper-forwarded mapped views); the two-hop chain, the
untraceable-invocation, closure-capture, wrap-then-invoke, and
global-registration shapes are probe-verified as well. Battery now
608 cases (519 rejections, 89 benign acceptances), zero misses.
Round-2 verdicts for the other four reviewers were not capturable
(interrupted sessions / no output); their round-3 re-review is
required at the new HEAD.

Review round 3 (level-1 swarm, 2026-08-17): Sartre reported one P2 at
1cd3975: the implementation-site store-callback fence only followed
the callback formal DIRECTLY at the call site, so a store
implementation hiding the formal behind a LOCAL (cb := fn, or cb :=
func(a, b []byte) error { return fn(a, b) }) and handing the local to
a scanned helper bypassed the mapped-view requirement. Probe
verification on the round-3 build found the family in three states:
the direct- and helper-closure liar forms were caught only by
accident (the body-level counter-check over unbound closure params),
an honest closure wrapper with mapped views FALSE-POSITIVED the same
way, and a closure routing the formal through a struct field escaped
with zero diagnostics. Fixed:
- funcSummary now records local callback aliases (noteCallbackAliases):
  cb := fn and cb := func(a, b []byte) error { return fn(a, b) } map
  the local to the wrapped formal slot plus the closure parameter
  positions actually forwarded to it (position-precise, so dropped
  positions are not constrained); chains (cb2 := cb) copy the record;
  reassigned or address-taken locals are not aliases.
- recordCallbackInvokeComposition and checkCallbackInvokeCalls follow
  the alias: a callee that invokes its func-typed formal must still
  receive MAPPED views for every forwarded position when the argument
  is the local wrapper, and identity aliases forward every position.
- Func-literal arguments of approved module callees are now analyzed
  with their parameters bound to the call-site views the callee's
  invocation record reaches the callback: the body-level
  counter-check and the fail-closed unproven-callee checks evaluate
  the true views instead of unbound parameters (honest wrappers stay
  legal; the struct-field routing closure now fails the same way).
- The scanned-callback fence admits never-reassigned identity aliases
  of func-typed formals (paramAliasedFuncVar): the formal's call
  sites are policed, so the alias is a scanned callback; the
  call-site callback fence then provides the mapped-view check.
New battery pins P299 (fail: closure wrapper + identity alias with
owned buffers through a helper) and P300 (benign: both honest forms
with mapped views); probe evidence kept for the direct-closure,
helper-closure, identity-alias, and field-routing shapes plus the
one-param closure bisect. Battery now 610 cases (520 rejections, 90
benign acceptances), zero misses; the real-tree scan stays at zero
violations and v4/go test ./... stays green.

Round-3.5 follow-up (2026-08-17): the round-4 re-review reported a
P2-1 for the DIRECT invocation of the callback formal through a local
identity alias (cb := fn; cb(s.buf, s.buf)), a declared-parameter
literal wrapper (wrap := func(cb, a, b []byte) error { return cb(a,
b) }; wrap(fn, s.buf, s.buf)), and a struct field
(s.cb = fn; s.cb(s.buf, s.buf)). Probe verification (probe_r8) shows
all three liar sub-shapes were ALREADY rejected at the previous HEAD
by the existing fail-closed transfer for calls through unproven
callees (rules.go varIndirect on the full-page arguments; the
identity-alias and field forms are the existing R2-4/R2-6 pins), with
byte-identical diagnostics at 1cd3975 and 9d7ffa4; the honest twins
fail identically at both heads by the same fail-closed rule and are
recorded as accepted conservatism (the same class as interface
dispatch with mapped pages), not as a bypass. The three liar
sub-shapes are pinned durably as P301 (fail). Battery: 611 cases
(521 rejections, 90 benign acceptances).

Round review 2026-08-17 (chunk 3a, level-1 re-review on the alias
machinery; HEAD 1e90c67): the round-4 delta probes (probe_r9) applied
the alias attack surface to the store-callback fence. R9-1 (identity
alias chain whose source is reassigned after the chain binds;
cb := fn; cb2 := cb; cb = nil; runCb9(cb2, owned, owned)) was SILENT
at 1cd3975 - the alias follower dropped the record when the source was
reassigned and the unproven-callee transfer never fired; Jason's
P2-1 reproduced. Fixed at 9d7ffa4 (noteCallbackAliases records chains
with reassigned/address-taken guards; the store-callback fence follows
the alias at call sites; the scanned-callback fence admits identity
aliases of func-typed formals whose call sites are policed) and the
direct alias/literal-wrapper/field sub-shapes are pinned as P301.
Probe verification at 1e90c67: R9-1, R9-2 (address-taken), R9-3
(array element), R9-5 (map value), and R9-7 (helper-forwarded capture)
all fail closed; R9-6/R9-9 honest twins stay clean; R9-8 (captured
local re-minted to a mapped view before the invocation) stays benign
by reference semantics. Same-failure sweep over the round-4 fixes then
found two further escapes, probe-verified in isolation before any fix:
- P302 (pointer-deref callee): (*p)(owned, owned) with p := &cb was
  completely silent because the syntactic isTypeExpr StarExpr case
  misread a dereferenced FUNCTION POINTER call as a type conversion,
  returning before the unproven-callee page-argument fence ran; the
  pointee must itself name a type (rules.go isTypeExpr now recurses
  into the StarExpr operand). The same sweep proved B1-B4 (pointer to
  package func var, func literal var, the formal, double deref) were
  equally silent and now all fail closed; the honest mapped twin also
  fails by the same unproven-indirection conservatism as the array/
  map/interface classes and is pinned as accepted P304.
- P303 (branch-joined capture): a helper capturing v := s.capture,
  re-minting v to a mapped view on ONE branch, then invoking the
  callback formal with v, was silent because joinPageValue/
  joinFieldTaint OR-ed the mapped flag across paths; the value is
  provably mapped only when EVERY path is mapped, so both joins now
  AND the flag and the store-callback fence fails closed on the
  owned path.
Deep-chain stress (4/8/16-hop identity aliases with source or
mid-chain reassignment) all fail closed, the unmolested 16-hop honest
chain with mapped views passes, and the fixpoint converges with no
hang and no growth on the real-tree scan (still zero violations).
Battery: 614 cases (524 rejections, 90 benign acceptances) with P302/
P303/P304 pinned durably; full-battery re-run at the fixed HEAD was
clean. The real module scan stays at zero violations and v4/go test
./... (both tag sets) stays green.

Level-1 callback-alias round, fix wave (2026-08-17): the five
aspect reviewers reviewed the alias/pointer/join delta at HEAD 08db7d7.
Sartre and Leibniz PASSed (Leibniz added a P3-strength preference for
pinning the pointer-dereference alias forms). Jason FAILed with one
reproducible P2: the store-callback alias fence read only the top-level
statement list of a store implementation body in noteCallbackAliases
(pageflow.go), while paramAliasedFuncVar admitted block-scoped and
var-declared identity aliases for direct-invocation approval; a
block-scoped alias forwarded through a scanned helper therefore escaped
the helper-forwarding fence. Reproduced by the lead on a probe module:
if-block alias to helper = gate-silent; switch-case alias, var-declared
identity aliases (top-level and nested), and block-scoped literal
wrappers are the same class. Fixed by recording callback aliases from
every block of the enclosing function (if/switch/loop bodies and var
declarations) while never descending into nested func literal bodies
(those are analyzed as their own summaries); the assignCount/
addressTaken guards already covered the whole body. All five liar
variants now fail closed; the honest twin (block-scoped aliases
forwarded with mapped views) stays legal. Pinned durably as P305
(four liar variants, one file) and P306 (honest twins). Battery: 616
cases (525 rejections, 91 benign acceptances), zero misses under
--self-test-jobs 24 (1:46 wall, ~8x faster than the sequential
self-test); real module scan rc=0 across the 5 OS configs; go test
./... both tag sets, -race, vet (module and gate), gofmt, and the
import-graph boundary check over the 11 GOOS/GOARCH pairs all green.
Also in this period: the battery self-test gained a parallel worker
mode (--self-test-jobs N / --self-test-chunk K/N, each worker in its
own private module copy), cutting the dominant validation cost from
~14 minutes sequential to ~1.5-2 minutes.

Level-1 round continuation (2026-08-17, Peirce P2 on clean-buffer
callback indirections): Peirce returned FAIL at HEAD c597eba with one
P2: a store implementation forwarding its callback formal into a
helper that stores the formal in a local struct field and invokes it
there (h.f = fn; h.f(a, a)) escapes the callback-invocation records,
which resolve only Ident callees. Reproduced by the lead with probe
modules: the EXACT Peirce shape is caught by the generic
unproven-callee fence because the store field buffers carry page
taint, but the same class with CLEAN owned buffers (out := make([]byte,
4096)) is gate-silent through four indirections - struct field in the
store impl (s.cb = fn; s.cb(out, out)), local identity alias
(cb := fn; cb(out, out)), forwarder literal
(wrap := func(cb, a, b []byte) error { return cb(a, b) };
wrap(fn, out, out)), and helper-local struct field
(runCb(fn) { var h struct{ f ... }; h.f = fn; h.f(out, out) }). Fixed:
the flow pass now records func-formal value flow into struct fields
(fs.fieldAliases, populated in noteCallbackInvokes from h.f = fn
assignments and resolved for field-selection callees), and the
store-implementation counter-check (checkStoreCallbackViews) now
requires byte-slice arguments to be provably mapped whenever the
callee is the formal, a local identity alias
(paramAliasedFuncVar), a recorded wrapper alias, a field that holds
the formal, or a forwarder call passing the formal as a func-typed
argument. Direct field/alias/wrapper invocation with MAPPED views
stays reject-by-design (the pre-existing unproven-callee conservatism,
P304-class, verified unchanged against the pre-fix binary); the legal
production patterns (formal invocation, scanned-helper forwarding with
mapped views) stay green on the real module. Battery: 618 cases (526
rejections, 92 benign acceptances) with P305 de-vacuated (constructors
no longer embed the page, so the pin fails only through the
store-implementation fence), P307 (four clean-buffer liar variants)
and P308 (block-scoped wrapper honest twin) pinned durably; zero
misses under --self-test-jobs 24 (1:46 wall). Real module scan rc=0
across the 5 OS configs; go test ./... both tag sets, -race, vet
(module and gate), gofmt, and the import-graph boundary check over
the 11 GOOS/GOARCH pairs all green.

Level-1 round continuation (2026-08-17, Linnaeus P2 on callback
invocation end-state and composition fixpoint): Linnaeus returned FAIL
at HEAD 7b379bf with two P2 gate defects, both reproduced by the lead
with probe modules:

1. P2-1 trailing-mint end-state divergence: a store helper invokes the
callback formal with a locally-sourced owned variable and then re-mints
from r.page AFTER the call. noteCallbackInvokes runs after the body
walk and read end-state stmtVars, so the mapped exemption exempts the
invocation even though the value at call time was owned. The probe
(helper-local `v` loaded earlier, trailing `r.page` mint after the
call) was gate-silent before the fix; it now fails.
2. P2-2 cross-helper alias chain: h1(fn, s) does `cb := fn; return
h2(cb, s.buf, s.buf)`; h2 invokes fn(a,b). The composition record
(recordCallbackInvokeComposition) could not resolve `cb` because
noteCallbackAliases ran AFTER analyzeStmts, and summaryEqual did not
compare the callback maps, so the fixpoint terminated before a second
pass could stabilize the alias. The probe (identity alias in one
helper, invocation in a second helper with clean owned buffers) was
gate-silent before the fix; it now fails.

Fixed in v4/go-gate/pageflow.go with three coordinated changes:
(a) noteCallbackAliases now runs BEFORE analyzeStmts in analyzeFunc -
it derives purely from the AST and the signature, so earlier recording
cannot be stale, and the composition records see the aliases on the
same pass; (b) summaryDup/summaryEqual gained callbackInvokes,
callbackInvokesInternal, callbackAliases, and fieldAliases via a new
callbackRecordsEqual helper, without which the fixpoint terminated
early; (c) a new invokeCensus snapshots the body's assignment
structure (identifier assignment count/position, selector-path writes,
index writes, deref writes, address-taken) and the mapped exemption in
noteCallbackInvokes now requires pv.mapped AND census.stable(arg,
callPos): body-declared locals must be single-assigned, not
address-taken, and assigned before the call; params stay stable unless
reassigned/address-taken; selector paths allow at most one write
before the call; constants/builtins/type/pkg names are always stable;
everything else fails closed (package vars, captured values,
multi-assigned locals). The census is deliberately conservative - the
end-state of a variable is not its call-time value, so any storage the
argument reads that can be written after the call rejects.

Pinned durably in the battery as P309 (four liar shapes: direct
trailing mint by the callee-owned prover, helper-local owned `v` with
trailing mint, and the two-helper identity-alias chain) and P310
(honest twins: mint-before-call helper, and the two-helper chain
forwarding mapped views). Battery: 620 cases (527 rejections, 93
benign acceptances), zero misses under --self-test-jobs 24 (1:45
wall). Real module scan rc=0 across the 5 OS configs; go test ./...
both tag sets, -race, vet (module and gate), gofmt, and the
import-graph boundary check over the 11 GOOS/GOARCH pairs all green.

Level-1 round continuation (2026-08-17, Jason P2 on type-assertion
and indexed-slot callee escapes): Jason returned FAIL at HEAD 6bf4467
with three P2 gate escapes, all probe-verified silent at HEAD and all
reproduced by the lead:

1. P2-1 type-assertion callee defeats the holder fence: s.cb = fn with
cb any, then s.cb.(T)(out, out) with owned buffers - the counter-check
was wired only for Ident/Selector callees, so the assertion callee
matched neither the flow records nor the rules counter-check; the
identity-func roundtrip cast(s.cb.(T))(out, out) (cast := func(f F) F
{ return f }) was equally silent.
2. P2-2 type-assertion-to-local breaks the alias chain: f := s.cb.(T);
f(out, out) - noteCallbackInvokes accepted RHS recordings only as
*ast.Ident and paramAliasedFuncVar required an Ident initializer, so
the assertion result was neither a scanned callback nor a policed
holding.
3. P2-3 indexed slot holders are not recorded: arr[0] = fn, map
m["cb"] = fn, slice literals hs := []func{fn}, and slice fields
s.hs = []func{fn} invoked as arr[0](out, out) etc. - slot recording
handled only Ident/Selector LHS and callee resolution only
Ident/Selector functions.

Fixed with one shared callee-resolution authority (slotOfExpr in
pageflow.go, with its rules-side counterpart callbackSlotOf in
rules.go): a func-typed expression resolves to the callback formal
slot through the formal itself, identity/assertion aliases
(f := fn.(T), f := s.cb.(T), var box any = fn; f := box.(T)),
struct-field holders, indexed container slots (keyed by root object,
selector path, and constant index; non-constant indices fail closed
with a catch-all key), and type assertions of any of these. The
flow pass records type-assert RHSs in the identity and param-alias
passes, seeds indexAliases from indexed assignments AND array/slice/
map composite literals ([]func{fn} seeds hs[0]), records return
wrappers (id := func(f F) F { return f }) as returnAliases so
id(x)(out, out) resolves the callee to the callback bound to x, and
the composition and rootSlot resolve through the same authority.
The rules counter-check now fires for TypeAssertExpr, IndexExpr/
IndexListExpr, and call-typed callees, and forwardsCallbackFormal and
paramAliasedFuncVar resolve the new shapes (paramAliasedFuncVar also
accepts flow-recorded any-holder aliases, so a box-asserted callback
forwarded to a helper is scanned instead of over-rejected).

All nine Jason liar shapes now fail (type-assert callee, array slot,
map slot, assert-to-local, slice literal, slice field, identity-
return roundtrip, box-assert forwarded, field-assert forwarded),
per-OS as expected, while the honest twin (assertion alias forwarded
through a helper with mapped views, box- or field-derived) stays
clean; helper-internal direct holder invocations with traced params
stay over-rejected exactly like the pre-existing alias/field classes
(verified identical to the 6bf4467 binary on the mapped honest
controls). Pinned durably as P311 (nine liar shapes) and P312
(honest assertion-alias forwarding twins). Battery: 620 -> 622 cases
(528 rejections, 94 benign acceptances), zero misses under
--self-test-jobs 24 (1:47 wall). Real module scan rc=0 across the 5
OS configs; go test ./... both tag sets, -race, vet (module and
gate), gofmt, and the import-graph boundary check over the 11
GOOS/GOARCH pairs all green.

### 2026-08-17 - level-1 re-review round: trailing-mint cache poisoning and cross-function carrier escapes closed (HEAD 149492f, committed with this record)

Two level-1 reviewer classes (Linnaeus/Sartre P2, silent at 149492f)
were probe-verified and fixed, plus the earlier Jason class closed at
149492f:

- Jason escape (fixed at 149492f): type-assertion callees, assertion
  aliases, indexed container slots, and identity-return wrappers
  reached the storage callback with clean owned buffers; slotOfExpr
  authority, fieldAliases/indexAliases/returnAliases, composite-literal
  seeding, and the rules-side counterparts close it. Battery P311
  (nine liar shapes) / P312 (honest assertion-alias twins).
- Linnaeus/Sartre P2-1 (fixed in this round): the definition-site
  mapped exemption read the END-STATE value of a local that a TRAILING
  mint (v := s.buf; fn(v, v); v = s.r.page(0)) re-blessed after the
  invocation; the callback-invocation walk now snapshot-evaluates the
  exemption path (snapshotEvalExpr) so the call-time owned buffer is
  not poisoned by the later mapped value.
- Linnaeus/Sartre P2-2 (fixed in this round): the callback formal
  carried inside a STRUCT FIELD (h := car{cb: fn}; runCar(h, out, out),
  h.run(out, out), composite-literal carriers, two-helper chains)
  laundered clean owned buffers; field records are now keyed by
  canonical struct type + field name (canonFieldType structural
  identity, named and anonymous structs share keys), composite
  literals are seeded, fieldInvokes compose through call chains
  (recordFieldAliasComposition forwards caller records and re-records
  callee invocations), and the rules side enforces mapped views at
  every carrier call site (checkCallbackInvokeCalls field fence,
  moduleFieldCarrier, storeCarrierTracedFieldCall suppressing the
  generic unprovable-callee fence only for parameter-sourced carrier
  calls).

Evidence: probe module shapes R-A..R-I (liars flagged: direct
trailing mint, helper trailing mint, named/anonymous carrier helpers,
carrier method receiver, composite-literal carrier, two-helper
chains; honest mint-before and mapped-view carrier twins clean).
Pinned durably as P313 (direct + helper trailing mint liars), P314
(mint-before honest twins), P315 (five carrier liars), P316 (four
mapped-carrier honest twins). Battery: 622 -> 626 cases (530
rejections, 96 benign acceptances), zero misses under
--self-test-jobs 24 (1:55 wall). Real module scan rc=0; go test
./..., -race, vet, gofmt, import-graph boundary check, and GOOS=windows
cross build all green.

### Round 3 (delta re-review of the round-2 fixes, working-tree build): the five resident
level-1 reviewers re-audited the round-2 delta and returned five further callback-escape
classes, all probe-verified by the lead on the round-2 build (dev9): S2-1a/S2-1b
(package-global and pointer-setter carrier binding: setGlobal(fn); g.cb(out, out) and
var h car; setCb(&h, fn); h.cb(out, out) escaped because the setter helper's DIRECT field
record never reached the store-implementation summary), D (identity-return passthrough:
cb := passthrough(fn); cb(out, out) lost the formal binding through the helper return), L2-1
(positional struct composites h := car{fn} of a NAMED carrier type never seeded the
canonical field alias), L2-2 (wrapper literals carrying a byte pair composite:
wrap(fn, pair{a: buf, b: buf}) invoked the formal with fields of an owned pair), L2-3
(cross-method storage s.prep(fn); s.cb(out(), out())), and the bound-carrier launch
(launchS2c(car{cb: arbitrary}, page) feeding a mapped page into a field callee with an
unscannable body). Fixed at the working tree: seedStructComposite resolves NAMED carrier
types to their underlying struct so positional elements seed field aliases; the
store-implementation call site pulls the callee's DIRECT field aliases into its own record
(recordFieldAliasComposition leg c), so global/pointer setters and prep methods bind the
canonical key for moduleFieldCarrier and the counter-check; rootSlot traces selector
chains (v.a, s.buf) to the root parameter so pair-carrying wrapper literals are
parameter-traced instead of marked internal; evalComposite computes a struct composite's
mappedness as the conjunction of its byte-carrying element expressions; and the rules-side
compositeCarrierMapped re-derives the same conjunction at the store-callback fence, because
promoteFullPageFields rewrites a composite's cached whole-value taint without the mapped
flag after evalComposite wrote it (probe-verified with per-pass cache traces: the element
expressions x and y keep mapped=true in the flow cache, so honest pair{a: x, b: y} wrapper
arguments pass while owned pair{a: buf, b: buf} stays fail-closed). The Sartre P2-2
carrier-fence class (checkCarrierViewCallSites) shipped with round 2 as part of the same
working tree. Pinned durably as P321 (passthrough liar), P322 (positional-composite liar),
P323 (cross-method liar), P324 (wrapper-literal pair liar with owned elements),
P325 (global + pointer setter liars), P326 (benign helper-forwarding honest twins of all
seven classes with mapped views; direct field-call forms remain P304-class fail-closed).
Battery: 630 -> 636 cases (538 rejections, 98 benign acceptances), zero misses under
--self-test-jobs 24; probe modules: eight round-2 liar lines and the s2c liar rejected,
zero honest/control over-rejections; real module scan rc=0; go test ./..., -race, vet,
gofmt, import-graph boundary check, and GOOS=windows cross build all green.

### Round 3 second delta (2026-08-17, level-1 re-review of the round-2 fixes; working tree at ef10446)

The five level-1 aspect reviewers (Jason, Linnaeus, Peirce, Sartre,
Leibniz) re-audited the round-2 delta on the working tree and returned
five further callback-escape classes, all probe-verified by the lead:

- Peirce: local func-typed callees. g := passthrough; cb := g(fn) with
  cb(out, out) and mv := s.getCB; cb := mv() lost the formal binding
  because callResultAlias and the rules pass only resolved the callee
  when the call expression was a selector or free function, not a local
  func-typed variable. Fixed: both passes resolve a local func-typed
  callee through its single never-address-taken definition
  (varDefOf/calleeExprFunc) before applying the scanned summary.
- Jason: nested carriers. o := outerJL{in: carJ1{cb: fn}} followed by
  runOuterJL(o, out, out), the three-level form
  runTopJM(topJM{midJM{outerJL{...}}}, out, out), the inner-field
  setter var o outer; o.in = car{cb: fn}, and the positional
  local-bound form o := outer{car{fn}} all escaped: the leaf field
  record had no path, so the helper fence never matched the callee
  parameter declared as the OUTER carrier type. Fixed: fieldSlotAlias
  and returnCarrierField carry a carrier path (outer-to-inner field
  steps); seedStructCompositeAt recurses into nested literals and
  local-held composites; setFieldAlias keeps the longest path per
  field key (the nested record matches the outer-typed parameter, the
  flat record matches the leaf-typed one); the rules pass matches the
  callee parameter through the path's first step.
- Linnaeus: address-taken alias chains and method-value carriers.
  cb := fn; p := &cb; cb2 := cb with a later launch lost the formal via
  the address-taken paramAliases guard, and runCb(h.run, ...) was not
  recognized as forwarding the bound carrier field. Fixed: record()
  keeps paramAliases across address-taken/reassignment (only
  callbackAliases stay guarded); methodValueCarriesCallback resolves
  method values bound to locals (mv := h.run) and marks internal
  method bodies fail-closed; checkStoreCallbackViews runs the carrier
  invocation loop for method values.
- Sartre: module-wide carrier suppression was over-broad. A concrete
  mapped page entering a field callee whose carrier key the enclosing
  store implementation never bound (runCar(car{cb: nop}, v))
  previously fell into the suppression path and was accepted. Fixed:
  when the current implementation has no direct record for the
  carrier key, a concrete mapped page entering the field callee FAILS
  (parameter-sourced views stay legal - the decision belongs to the
  store call sites upward).
- Honest-twin over-rejection (JP3 class): cb(x, y) with mapped views
  after a passthrough was double-flagged by the generic unproven-callee
  transfer fence. Fixed: storeCallbackCallee sanctions invocations of
  the store callback itself (formal, alias, carrier field, indexed
  slot, asserted holder, call-result wrapper) inside store callback
  implementations; the owned-view direction stays policed by
  checkStoreCallbackViews.

Pinned durably as P327 (local func-var passthrough liar), P328
(local-bound method-value getter liar), P329 (keyed nested carrier
liar), P330 (three-level nested carrier liar), P331 (inner-field setter
liar), P332 (local-bound nested carrier liar), P333 (method-value launch
liar), P334 (delegated address-taken chain liar), P335 (local method-
value launch liar), P336 (mapped mint into an unrelated carrier field
callee), P337-P339 (benign honest twins of the same classes with
mapped views). All seven liar snippets without the full Store method
set were completed with the standard stubs so the probe type-checks
(P327/P329-P333/P335 - the earlier two MISSes were snippet type errors,
not scanner gaps).

Battery: 636 -> 649 cases (548 rejections, 101 benign acceptances),
zero misses under --self-test-jobs 24 (~2:10 wall). Probe modules:
every expected round-3 liar flags (probe_jm.go:16/31/49/67,
probe_jp.go:20/39, probe_jn.go:17/33, probe_jh.go carrier helpers,
probe_r14.go delegated/method-value/address-taken forms, probe_ji.go
nested carriers) and every honest twin passes (probe_jm.go:89,
probe_jp.go:62, r14e/r14d, probe_jh.go:73, probe_jl.go:174,
probe_jn.go:55 over-rejections from HEAD are gone). Real module scan
rc=0; go test ./..., -race, vet, gofmt, import-graph boundary check,
and GOOS=windows/darwin cross builds all green. The round-3 code
fixes were committed and pushed as HEAD e7bc666 (this SOW record was
written against the pre-commit tree).

### Round 3 third delta (2026-08-18, level-1 re-review of the round-3
fixes; working tree at e7bc666)

The level-1 reviewers (Jason, Linnaeus, Peirce, Sartre; the Leibniz
session returned incoherent output twice and was not reused, so its
aspect coverage is reduced) re-audited the round-3 delta and returned
eight further callback-escape classes, all probe-verified by the lead:

- Linnaeus (nested carriers): a pointer-element composite carrier
  w := pw{p: &pn{cb: fn}} and the nested inner-field setter
  var t T; t.m.cb = fn silently lost the formal binding. Fixed:
  fieldSlotAlias keeps multiple path records per field key (deduped,
  no longest-path-wins), recordSlot records the nested destination
  path, and seedStructCompositeAt unwraps &CompositeLit so the leaf
  record carries the outer-to-inner path.
- Peirce (local callees): passthrough behind an address-taken local,
  a struct field, and an indexed slot silently escaped. Fixed:
  calleeExprFunc resolves address-taken single-definition locals, and
  exprCompositeLit/fieldBoundValue/indexBoundValue/constIndex resolve
  func-typed fields and index slots bound through composite literals.
- Sartre (store-callback fences): intra-package method expressions
  (*T).m as callback carriers were fully silent, including mixed
  mapped+owned laundering. Fixed: methodValueCarriesCallback accepts
  types.MethodExpr and method-expression launches enforce mapped
  views at the call site.
- Jason (cross-cutting soundness): storeCallbackCallee exempted any
  func-typed struct field, so a mapped mint through an unrelated
  field was silent; a flat+nested same-key collision dropped the flat
  record; and multi-hop return h1(g) call-return resolution was
  missing. Fixed: the SelectorExpr sanction was narrowed to
  fieldHoldsCallbackFormal only, the field-alias enforcement loop now
  iterates every direct record, and ReturnStmt resolves call returns
  eagerly through callResultAlias so helper summaries settle over the
  fixpoint passes.

Pinned durably as P340-P356 (17 pins): P340/P341 pointer-element
carrier liar/honest, P342/P343 nested inner-field setter liar/honest,
P344/P345 address-taken local passthrough liar/honest, P346 struct-
field-held passthrough liar, P347 indexed-slot passthrough liar,
P348/P349 multi-hop return-call liar/honest, P350/P351/P352 method-
expression owned/mixed/honest, P353 unrelated store-struct field
direct mapped mint, P354 unrelated local carrier direct mapped mint,
P355/P356 flat+nested same-key liar/honest.

Battery: 649 -> 666 cases (559 rejections, 107 benign acceptances),
zero misses under --self-test-jobs 24 (~2:10 wall). Probe modules:
every expected round-3.5 liar flags (probe_ln15.go:29/61,
probe_jl_e1.go:21/41/57/101, probe_q1.go:25, probe_q2.go:30,
probe_q3.go:27, probe_q9.go:25, probe_r17.go:27/48/66,
probe_qa.go:50) and every honest twin passes, including the two
full-module honest twins re-checked after the isolation delta
(probe_jl_e1.go:83 mapped-view twin and probe_qa.go:25 mapped-view
multi-hop twin are both clean in the full probe module with the final
scanner, matching their battery-isolated P345/P349 pins).
probe_ln15.go:82 (interface type-erasure note) is pre-existing at
HEAD e7bc666 and unchanged - a probe-harness artifact, not a scanner
regression. Real module scan rc=0; go test ./..., -race, vet, gofmt,
import-graph boundary check, and GOOS=windows/darwin cross builds all
green. Committed with this SOW record.

### 2026-08-18 - level-2 gate round 1: open-ended slice false positive and fix (HEAD f615d52 -> working tree)

The user directed a level-2 final-gate review of the milestone-1 rework
with glm, kimi, minimax, and qwen at HEAD f615d52 (all four kept open as
the level-2 set). kimi returned FAIL with one P2: the gate falsely
rejected the honest split of ONE mapped page into TWO separate owned
buffers (a = append(a, page[:2048]...); b = append(b, page[2048:]...),
the copy() twins, and the helper-mediated forms). The lead reproduced
all three shapes in probe modules (/tmp/jas5d, /tmp/jas5e) and
instrumented the gate to print the accumulation keys: the reviewer's
causal theory (bounded-span accumulation collapsing distinct buffers
onto one key) is FALSE - accumulation keys stayed per-object with
correct totals (obj=a, obj=b, obj=c). The real cause is the flow pass's
sliceLenSym: an OPEN-ENDED slice (x[lo:] with no high bound) of a
mapped page view returned maxUnknown, so ANY sub-page open-ended view
was treated as a complete page, rejecting even a 2048-byte copy into a
2048-cap destination. Fixed in pageflow.go: sliceLenSym now bounds
x[a:] by base.maxLen - constLow when the base view has a definite
maximum length; bases with unknown length stay unknown (fail-closed).
Same-failure search: the copy-parameter helper rule
(rules.go checkParamCopyCalls) had been catching one-buffer two-half
assembly through a helper (fr(dst, src) { copy(dst, src) } twice into
the same caller buffer) ONLY through the same open-ended confusion;
after the slice fix it went silent, so checkParamCopyCalls now
accumulates bounded spans onto the CALLER's destination key (mirroring
checkCopy/checkAppend), rejecting genuine one-buffer assemblies through
helpers while accepting distinct-buffer splits. Probes: every honest
split shape (append/copy/helper, two and three buffers, open-ended and
explicit bounds) passes; every one-buffer assembly liar fails with the
semantic accumulation messages. Battery: 666 -> 670 cases (561
rejections, 109 benign acceptances), zero misses under
--self-test-jobs 24 (~1:50 wall). New durable pins: P357 (open-ended
two-half append assembly into one buffer, reject), P358 (honest split
copy + append into separate buffers, benign), P359 (helper-mediated
one-buffer assembly, reject with expectRule on the call-site span
message), P360 (honest helper split, benign). Real module scan rc=0
(5 OS configs); go test (both tag sets), -race, vet, gofmt,
import-graph boundary check, linux/windows/darwin builds all green.
Level-2 verdict at f615d52: FAIL (one P2, now fixed); re-review of the
fixed tree is the next level-2 round.

### 2026-08-18 - level-2 gate round 2: cyclic pointer carrier crash and fix (HEAD 427c3c7 -> working tree)

GLM (level-2) returned FAIL at 427c3c7 with a P1: the scanner crashed
with a stack overflow (rc=2, no verdict) on a self-referential pointer
carrier used as a container element: type chainRec struct { inner
*chainRec; leaf func(src, output []byte) error } with a []chainRec or
map[string]chainRec parameter. Reproduced independently with probes:
the container-element expansion in paramLeafPaths
(v4/go-gate/pageflow.go:10568-10571) calls paramLeafPaths(et) with a
FRESH walkSeen per call, and containerElemType unwraps pointers
(pageflow.go:9145), so the element walk of the pointer field re-enters
the same struct through a new call forever; the direct-field guard
(pageflow.go:10528) only protects one call's path. Fixed by threading
one seen set through nested element calls
(paramLeafPaths -> paramLeafPathsSeen, pageflow.go:10524-10533): the
container and map-key element walks stop at the revisiting type with
the same documented semantics as the direct-field guard. The
call-flow matcher resolves r.inner.leaf independently of the leaf-path
walk, so cyclic depth-two callback coverage is preserved (verified by
probe: full page to it.inner.leaf still rejected, bounded span to the
same leaf accepted). Battery: 670 -> 673 cases (563 rejections, 110
benign acceptances), zero misses under --self-test-jobs 24 (~2:30
wall). New durable pins: P361 (cyclic container element passes full
page to element leaf, reject), P362 (same carrier, bounded page half
to the leaf, benign), P363 (depth-two cyclic branch leaf receives full
page, reject with expectRule on it.inner.leaf). Real module scan rc=0
(5 OS configs); go test (both tag sets), -race, vet (module and gate),
gofmt, import-graph boundary check all green. Remaining level-2
verdicts outstanding: glm re-review of this fix, minimax, qwen; kimi
PASS at 427c3c7.

### 2026-08-18 - level-1 final-gate round at 6041aff: two verified callback-composition misses fixed (HEAD 6041aff -> working tree)

The four level-1 reviewers returned FAIL at 6041aff with seven distinct
claims about the store-callback composition fences. Each claim was
reprobed against the real store shape (tree.Store implementations with
CopyPage, exactly like battery cases P297-P307). Verified results:

- Sartre P1 (field-held callback as helper ARGUMENT, runCb(s.cb, s.buf,
  s.buf), silent): CONFIRMED MISS. moduleFieldCarrier records s.cb = fn,
  so checkFuncTypedArgs whitelists the argument and checkCallbackInvokeCalls
  could not follow a FieldVal fnArg (forwardedCallback/aliasCallback
  accept only Ident). FIXED: checkCallbackInvokeCalls now resolves a
  FieldVal argument through the module carrier record and checks its
  byte views (rules.go).
- Peirce P2 (paramAliases -1 fail-closed marker dropped): CONFIRMED
  MISS. A callee that invokes an un-attributable callback value
  (method value through an identity passthrough) records
  callbackInvokes[-1]; the store-site fence dropped the record because
  argAt(-1) can never resolve. FIXED: negative fnSlot records now fail
  closed on their byte slots: the views handed to the un-attributable
  value must be mapped at the call site (rules.go).
- Linnaeus P2-2/P2-3/P2-4 (setter-then-return constructor, two-hop
  carrier delegation, struct field assigned from a call result): ALL
  REFUTED by probes - every liar shape is already rejected with the
  store-callback fence diagnostics.
- Jason P2-1 (variable-index container dispatch with owned buffers
  silent): REFUTED - the liar flags (generic unproven-callee fence,
  callee "?"), the const-index control flags, and the map variant
  flags. Jason P2-2 (honest twins over-rejected: variable-index and
  passthrough mapped dispatch): CONFIRMED but ACCEPTED DOCTRINE - the
  unprovable-callee over-rejection for mapped views is explicitly
  accepted conservatism, pinned by battery P304 ("pointer-dereferenced
  callback calls fail closed even with mapped views"); honest code must
  use the scanned forms (direct formal, identity alias, scanned helper).
- Sartre P3 (local field carrier honest over-rejected): not reproduced
  for the store shapes; those honest twins pass.

Battery: 673 -> 676 cases (565 rejections, 111 benign acceptances),
zero misses under --self-test-jobs 24 (~2:30 wall). New durable pins:
P364 (field-argument forwarding with owned buffers, reject), P365
(honest field-argument twin with mapped views, benign), P366
(un-attributable method-value passthrough with owned buffers, reject).
Real module scan rc=0 (5 OS); go test (both tag sets), -race, vet
(module and gate), gofmt, import-graph boundary check all green.

### 2026-08-18 - level-2 gate round 3 (39a1956 -> working tree): negative-slot internal marker and generic type-parameter callback gaps closed (HEAD 39a1956 -> working tree)

Round-6 level-2 verdicts at 39a1956: kimi PASS, minimax PASS, glm FAIL
(P1), qwen FAIL (P2). Both FAILs were probe-reproduced against store
shapes, then fixed in the working tree:

- glm P1 (negative-slot internal marker dropped): a CopyPage
  implementation that binds fn into a carrier, passes the carrier's
  method value through an identity passthrough, and invokes the result
  with LOCALLY MINTED owned buffers recorded only
  callbackInvokesInternal[-1]; the store-site fence dropped the record
  because argAt(-1) can never resolve, so the all-owned launder was
  gate-silent (probe rc=0). FIXED: checkCallbackInvokeCalls now fails
  closed on callbackInvokesInternal[fnSlot] in the negative-slot branch
  AND on the negative-only internal record (rules.go).
- qwen P2 (generic type-param callback escape): gApply[T any](f func(T)
  T, v T) with f a copying closure and v a complete mapped page was
  gate-silent for the inferred call; noteCallbackInvokes only recorded
  *types.Slice parameters, so the TypeParam-typed byte view never
  reached the bound-literal analysis. FIXED: type-parameter arguments
  now flow into the same trace/honest/internal logic as slice
  parameters (pageflow.go). Lead probing of the same class found the
  EXPLICIT instantiation spelling (gApply[[]byte](...)) still silent:
  calleeObject did not resolve generic instantiation index callees, so
  the call never reached the generic summary. FIXED: calleeObject and
  calleeExprFunc resolve IndexExpr/IndexListExpr bases (identifier and
  selector forms) to the underlying scanned function (pageflow.go).
  Generic methods (receiver type parameters, wrapRM[[]byte]{}.apply)
  resolve through the existing selector path and are probe-verified.

Probe expectations verified with the fixed scanner: inferred generic
full-page liar flags, explicit generic full-page liar flags, generic
method full-page liar flags, non-generic control flags, generic bounded
page[:16] honest forms pass (function and method), all-owned
negative-internal store launder flags, negative-slot honest
method-expression carrier twin with mapped views passes.

Battery: 676 -> 683 cases (569 rejections, 114 benign acceptances),
zero misses under --self-test-jobs 24. New durable pins: P367 (generic
inferred full-page reject), P368 (generic explicit-instantiation
full-page reject), P369 (generic method full-page reject), P370
(generic bounded span benign), P371 (generic method bounded span
benign), P372 (negative-internal all-owned store launder reject), P373
(honest method-expression carrier twin benign). Real module scan rc=0
(5 OS); go test (both tag sets), -race, vet (module and gate), gofmt,
import-graph boundary check all green.


### 2026-08-18 - level-2 gate round 3 re-review at f86f8f3: glm FAIL claims refuted, one real over-rejection found and fixed (battery 683 -> 686)

The round-7 code-only review at f86f8f3 returned glm FAIL with three
claims; every claim was probe-verified by the lead:

- glm P1 (TypeParam recording mis-slots non-byte arguments, corrupting
  liar and honest enforcement): REFUTED. The captured-carrier launder
  sketch (pass[T](b.n, closure) with the closure laundering through a
  captured box.cb) is ALREADY rejected at both f86f8f3 and 39a1956
  (field-call fence at the captured b.cb call plus the store fence),
  and the honest int-through-generic-wrapper probe
  (wrap(len(p), func(n int) int { return n + 1 })) does not flag. The
  store-callback composition cannot instantiate T non-byte anyway: the
  fence only applies when the forwarded callback is the store formal,
  which forces T to the callback's byte types.
- glm P2-1 (indexed container calls misclassified as generic
  instantiations): REFUTED. Package-level func-array slot calls
  (arr[0](page)) still resolve nil through funcVarCallee and stay in
  the unproven-callee path (probe flags "mapped page view passed to ?"
  exactly like pre-fix).
- glm P2-2 (generic method summary-key mismatch / wrong body): REFUTED.
  Ordinary generic-method calls (wrapRM[[]byte].apply) resolve the
  correct summary (append fence fires), method expressions on
  instantiated generic receivers are rejected, and the summary key is
  receiver-type-name scoped per package, so same-name collisions are
  impossible.

Lead probing of the method-expression variants surfaced one REAL
over-rejection, pre-existing at 39a1956 and still present at f86f8f3:
a DIRECT method-expression call of a scanned method with a func-typed
formal ((*app).apply(&a, closure, page[:16]) or
wrapRM[[]byte].apply(w, closure, page[:16])) was falsely rejected with
"func-typed argument to ....apply is not a scanned callback" because
callFormals omits the receiver type that method-expression calls carry
as an explicit first argument, shifting every formal by one. The
receiver value was checked against the func-typed formal and every
func-typed argument against the next formal. FIXED: callFormals now
prepends sig.Recv().Type() when the selector selection is
types.MethodExpr (rules.go), which also aligns checkInterfaceErasure.

Battery: 683 -> 686 cases (570 rejections, 116 benign acceptances),
zero misses under --self-test-jobs 24. New durable pins: P374 (direct
method-expression call with a func-typed formal and a bounded span,
benign), P375 (direct method-expression on an instantiated generic
receiver with a bounded span, benign), P376 (same spelling with a full
mapped page and a copying closure, reject). Real module scan rc=0 (5
OS); go test (both tag sets), -race, vet (module and gate), gofmt,
import-graph boundary check all green.


### 2026-08-18 - round-7 final: three confirmed gate gaps fixed; the container admission scoped to provable store-callback carriers; GLM claims refuted (battery 686 -> 694)

The round-7 swarm delivered nine reviews: PASS kimi, mimo, minimax,
qwen, Linnaeus; FAIL Sartre (P1: getter call-result carrier), Jason
(P2-1: variable-index composite dispatch), Peirce (P2: variadic/slice
element-wise callback invocation). GLM FAIL was probe-refuted on every
claim: P1 (TypeParam recording mis-slots) and P2a (indexed container
calls misclassified as generic instantiations) reproduce nothing at the
reviewed binary, and P2b (type-param receiver `T.Close(v)`) is a
pre-existing doctrine-accepted conservatism that fails for every
receiver shape, not a round-7 delta. Jason P3 (func-typed receiver
method-expression) and Linnaeus P3 (interface-erasure on the receiver
slot) were recorded as accepted-conservatism without pins: honest
isolated runs fail identically to the plain func-typed-formal control.

Lead probing during the fixes found that the first admission attempt
(admitting ANY func-container formal as a scanned callback container)
introduced a REGRESSION: `fs[0](page)` with `fs []func([]byte) int` a
non-store formal stopped failing (battery P68 MISS at the intermediate
build; the committed HEAD binary passes 686/686). Root cause: the
element admission must apply only to containers that PROVABLY carry
the store callback, never to an arbitrary func-container formal whose
call sites are not policed.

FIXED (rules.go, pageflow.go, main.go):

1. `storeCbSlots`: a module-wide fixpoint (`computeStoreCbSlots`) marks
   every function parameter slot that receives the store callback
   formal from a store implementation, directly or through forwarding
   chains (aliases, carrier fields, indexed holders, container
   elements, and composite-literal containers). `rangeVarHoldsCallback`
   and `indexCalleeOverFuncFormal` now admit only marked containers:
   the P68 class is fail-closed again, while the honest
   store-sanctioned helpers (`fireCbs(x, fn)` and alike) stay admitted.
2. Composite-literal container forwards at store call sites
   (`fireCbsIdx(out, []func(page []byte) error{fn})`) were STILL silent
   after the first fixes (probe var.go:44). `slotOfExpr` and
   `callbackSlotOf` now resolve func-container composite literals
   through their elements, and `forwardedCallback` accepts them, so the
   owned buffer fails at the store site exactly like a direct forward.
3. Sartre P1 (getter call-result carrier:
   `runCbT1(st.cbGet(), x, st.buf)`): `callbackArgAlias` resolves call
   results through the callee summary's `returnFieldKeys` /
   `returnSlotAliases` and fails closed on un-attributable results, so
   the owned `st.buf` handed to the getter-produced callback is
   rejected at the store site.

Honest twins for all four shapes verified clean in isolation (8-file
honest module scans rc=0); the four liar shapes flag exactly at the
store sites with the store-fence rules.

Battery: 686 -> 694 cases (574 rejections, 120 benign acceptances),
zero misses under --self-test-jobs 24, full shell-side mutation suite
green. New durable pins P377-P384 (the four round-7 store-callback
container shapes, each with its honest twin). Real module scan rc=0
(5 OS); go test (both tag sets), -race, vet (module and gate), gofmt,
import-graph boundary check all green.

### 2026-08-20 - M3 chunk-3b-2 slice 3 gatescan round: four gate false positives fixed in the gate tool itself

The milestone-3 external gatescan (every committed and new production
file under v4/go, all 5 OS configs) flagged four sites in the new
publish_set surface. All four were proven gate-analysis false positives;
the real code never copies a complete page (mmap-only holds). The gate
tool (v4/go-gate) was fixed so the scans express the true contract, with
three durable benign battery pins (322-324):

- internal/reader/algebra_output.go:398 string(entry.Name): the decoded
  catalog name is bounded to <=255 bytes by the v4 name grammar, but the
  gate lost the bound when the analysis read a.state.names directly. The
  production code decodes through the State().Names() accessor, whose
  summary preserves the bounded provenance (pin 324).
- membership_publish_set.go:216 NewOutputBuilder(..., nil): nil has no
  body and cannot launder a mapped page; the scanned-callback fence
  gained the nil-literal exemption (pin 322).
- membership_publish_set.go:240/243 discarded(err): the local
  `discarded := func(cause error)...` closure is a never-reassigned local
  bound to a func literal - its body is part of the scanned file, so a
  call through the variable is a proven callee, not an unproven
  indirection. approvedCallee, the varIndirect exemption, and
  unprovenVarCallee now treat approvedLocalFuncVar bindings as scanned
  (pin 323); the summary-tainted error argument that triggered the flags
  is an error value, not a page, so the call-site fence no longer misreads
  it as a complete-page copy.

Gate evidence for this round: real-module gatescan rc=0 on the linux
config and on the full 5-config set (16 bytes of log = nothing but the
exit marker); boundary corpus 55/55 (44 rejections, 11 benign
acceptances); the three new pins accepted standalone; go test ./... both
tag sets, -race, checkptr, vet, gofmt, 5-OS cross-builds plus darwin
v4work, and the Rust suite all green. No production mmap-only behavior
changed; the full 718-case battery runs as the milestone-close gate.

## Review Process (user decision, 2026-08-12)

1. Implement the milestone work, always long-term-best and minimal-complete.
2. Iteratively run 5-7 narrow-scope subagents on the session's own model (no
   model override). Each focuses on a disjoint aspect of the changes. Fix all
   P0 (critical), P1 (high), and P2 (medium) findings; only P3 (cosmetic)
   issues may be ignored. Repeat until all reviewers PASS.
3. After the iterative pass, run the full-scope final reviewer(s) over the
   entire milestone scope: sol (fixed at xhigh reasoning). The milestone is
   finished only when the final reviewer reports no P0-P2 findings.
4. If a final reviewer finds any P0-P2 issue, restart at step 1: rework,
   re-run the iterative reviewers, then re-run the final reviewer.
5. Gate-review threat model and blocker boundary (user decision,
   2026-08-16): the mmap source gates are mechanical tripwire evidence
   for trusted maintainers and accidental regressions, not proof against
   hostile or deliberately obfuscated Go. A gate finding is blocking only
   when it is a current production violation, a concise/idiomatic bypass
   of a declared gate family, a runtime mmap-trace violation, or a
   regression in an existing battery case. Enormous unrolled programs,
   absurd constants with artificial breaks, and equivalent deliberately
   adversarial constructions are recorded as accepted limitations unless
   they appear in production. Static source gates plus Linux runtime
   tracing provide enforcement evidence; static analysis alone does not
   prove the invariant for every possible Go program.

6. Review organization (user decision, 2026-08-17): level-1 reviewers
   are five aspect reviewers on the lead's own model (copies), each
   holding one disjoint aspect of the milestone scope, spawned once at
   first use and reused by message for every delta round. Level-2 is
   the full-scope final gate in place of step 3's sol: glm, kimi,
   minimax, mimo, spawned once and reused; the milestone is finished
   only when every available level-2 reviewer reports no P0-P2
   findings. Unavailable reviewers (technical/quota) are skipped and
   reported as reduced coverage; swarm agents are never respawned
   between rounds.

Milestone 2 chunk 3b - range gap edit core (2026-08-18; committed
at 7a1b4f8): the range editor entry points over the chunk 3a COW
storage surface, implemented and locally validated:
- internal/tree: one authoritative selector-based private-path descent
  (privatePathSelect, Rust private_path_select port) now shared by
  PrivatePath/PrivateLeafSelect; the LocalGap probe surface (gap.go,
  Rust fixed_tree/gap.rs port: Edge, LocalPrevious/LocalNext,
  LocalInsert, CachedInsert, EdgeInsert, PrivatePosition, PrivateEdge,
  RootEdge, FlushEdge, InsertIfLocalGap, InsertIfCachedInteriorGap,
  InsertRejectedGap, InsertIfEdgeGap, ReplaceLocalPredecessorWith,
  ReplaceLocalRun, LocalReject, LocalRun); SplitLeafAtEdge +
  locatePrivatePosition in insert.rs parity.
- internal/writer: range_codec.go (rangeFamily/rangeRecord,
  rangeCodec4/rangeCodec6 with 32-bit and 128-bit checked next/
  previous arithmetic); range_edit.go (Rust range_mutation.rs +
  assign.rs port: assign/assignPrivate/assignWithHint, clear,
  retireTree, transform, replace/insert/remove with predecessor/
  successor trimming and split/coalesce coalescing, per-record value
  accounting through RangeStore); range_draft.go (DraftStore
  AssignV4/V6 and ClearV4/V6 entry points; RangeRecordAdded/Removed
  fail closed with unsupported for membership/structured kinds until
  those accounting cores arrive).
- work counters: EdgePathCheck, RangeEmitted, RangeSplit,
  RangeCoalesced in both prod no-op and v4work atomic builds.
- gate: approvedModuleInterfaces extended with tree.LocalGap,
  writer.rangeFamily, writer.RangeStore under the same satisfier
  argument as the existing store family (every method set references a
  module-internal type; no stdlib or x/sys satisfier). The range codec
  method is named EncodeRecord: Encode is a banned content-transfer
  selector name (the gate keeps that ban unconditional), with the Rust
  RangeCodec::encode parity recorded in the comment.
- Tests: Rust-port vectors (portable record literal bytes, arrival-
  order overlap, clear split/coalesce, endpoint arithmetic on both
  full address spaces, transforms vs scalar reference after every
  non-idempotent op, randomized sequences vs scalar reference map,
  many disjoint ranges split leaves and COW once per path), work pins
  (clear split/coalesce, nested-assignment linearity 1023 tree
  lookups, disjoint split leaves), gap semantics (inspect/update
  without copying the leaf, cached-leaf interior-gap acceptance,
  rejected-gap local insertion completion, edge split), DraftStore
  v4/v6 family dispatch, membership-kind accounting fail-closed.
Validation: go test ./... (both tag sets), -race on
tree/bitmap/retire/writer/format, vet, gofmt, import-graph check,
11/11 cross-builds, and the 694-case gate battery (574 fail forms,
120 benign forms) - all green, battery with zero misses.

Level-1 review round (2026-08-18, chunk 3b; reviewed at HEAD
f85574d): five aspect reviewers; four PASS, one FAIL with one P2:
- Leibniz F-1 (P2, durability): the Go range entry points mutated
  draft meta in place through pointers (rangeCtx root/count into
  s.draft.meta), while Rust draft_store.rs assign/clear snapshot
  range_root/range_record_count into locals and commit them to meta
  only after the state machine returns. A mid-edit failure (e.g. the
  membership/structured accounting fence) therefore left Go meta
  partially mutated, so a retry of the same edit would observe the
  record already present instead of replaying cleanly. Fixed: assign/
  clear now snapshot root/count into locals and commit on success only
  (range_draft.go); the regression is pinned in
  TestDraftStoreRangeAccountingFailsClosedOnMembershipKinds (meta
  unchanged after failed assign, retry stays clean).
- F-2 (P3) folded into F-1: multi-step rewrites abandon partial
  mutations on mid-sequence errors; byte-for-byte parity with Rust
  sequencing; with the local-snapshot fix the draft meta stays at the
  pre-call state, matching Rust.
- P3 dispositions: wire decode guards tightened to exact record size
  (Rust decode_fields parity) with an IPv6 literal vector pin added;
  the BigEndian test name is inherited from the Rust test name
  (big_endian_portable_range_record_matches_literal_bytes) and the
  comment states little-endian - accepted with record; no battery
  pins yet for the three new interface approvals (P242-style control)
  - tracked for the gate follow-up; unguarded internal type
  assertions (pred.(rangeRecord), leaf.Selection.(gapDecision)) are
  internal-consistency style and fail closed by panic - accepted with
  record; LocalGap bounded-by-construction depends on codec LeafSize
  staying small - recorded in the gate comment.
- Re-review: Leibniz PASS at the fixed working tree (no P0-P2). The
  regression pin fails on the pre-fix build (local-ctx proof in the
  test record). Residual P3 accepted: a failed membership-kind assign
  still strands one private page on the draft dirty chain (meta never
  names it); same accepted abort-on-error class; the draft-level abort
  surface arrives at chunk 4 close/reclaim.
Validation: go test ./... (both tag sets), -race, vet, gofmt,
import-graph, 11/11 cross-builds, 694-case gate battery - all green.

Milestone 2 chunk 4 - publication, commit, abort, close, and bounded
reclamation (2026-08-18; committed at e1c8aeb, corrected from the
initial working-tree heading below): the
physical publication path over the chunk 2/3 COW storage surface,
implemented and locally validated:
- internal/mapping: FlushRange (msync subrange, Rust flush_range),
  FlushPage (one page), FileSize (re-stat of the locked file).
- internal/format: Meta.EncodeMapped writes the meta image directly
  into the alternate mapped page at its final offset (Rust
  contract.rs MetaV4::encode_mapped): every field at its wire offset,
  the reserved bytes zeroed in place (clear(), the Go form of Rust
  page.fill(0)), then the exact meta CRC. No owned page buffer exists.
- internal/fault (new): test-only crash-point injection; the v4work
  build exits 86 at IPRANGE_V4_TEST_CRASH_AT, production builds
  compile the Crash call to a no-op (Rust live_crash_tests.rs
  commit.before_private_sync / after_private_sync / after_meta_write
  / after_meta_sync parity).
- internal/writer: StartDraft/Draft/commitAttempt/Prepare (Rust
  prepare_with_checkpoint: releasePrivatePages/replenishReserve/
  drainPrivateStack/finishBitmapShape/sealPrivatePages),
  RequireDraftLength, Publish (the exact Rust shrink-or-retain,
  flush-data-range, sync, alternate-meta encode+flush, sync again,
  then adopt sequence; OutcomeUnknown abandons the draft),
  ClosePlan/PrepareClose/FinishClose/DiscardUnpublished (close.rs:
  trim any unpublished tail, verify discard geometry),
  PrepareReclamation/select/apply (reclaim.rs: select the oldest
  reader-safe retired transactions within the work limits, replay
  and verify, reclaimExtent frees every page and removes the extent,
  re-retiring removal COW victims). DraftStore.SelectReclamation and
  ApplyReclamation compose retire.SelectReclamation with a nil-
  checkpoint-safe hook (a nil checkpoint is a no-op, the offline
  reclamation shape).
- Tests: commit alternates the meta page across reader re-opens
  (fresh txn-1 DB opens on meta page 1, Rust
  identical_creation_metas_are_proven_current parity; each commit
  toggles to 1-base), abort discards unpublished tail bytes, close
  trims the unpublished tail and re-opens clean, bounded reclamation
  frees the oldest retired generation (Rust
  reclamation_counts_each_released_page_once: the reclaimed-page
  work counter pins the freed pages; the retirement extent count
  does not shrink for a single-record tree because removal COW
  victims are re-retired under the new generation - Rust parity,
  recorded in the test), publish state gates (no draft /
  double-draft).
- Gate: new content-transfer false-positive dispositions - the
  Windows stub used a nonexistent CodeUnsupported class (fixed to
  CodeOSUnsupported); EncodeMapped zeroes the mapped page with
  clear() instead of an element-wise loop the source tripwire treats
  as a complete-page transfer; crypto/rand.Read for the commit nonce
  (Rust random::nonzero_128 over getrandom) is exempted ONLY inside
  internal/writer/reclaim.go and only when the receiver resolves to
  crypto/rand (battery 314 pins an aliased non-crypto rand.Read,
  315 pins the benign exact call); internal/fault joined the
  writer-approved import boundary. Battery 694 -> 696 cases (575
  fail, 121 benign).
Validation: go test ./... (both tag sets), -race (writer/mapping/
reader), vet, gofmt, import-graph check incl. the 696-case battery
with zero misses, 11/11 cross-builds - all green. Level-2 gate for
chunk 3b (glm, kimi, minimax, mimo) closed at 818691f: all four
reviewers PASS with no P0-P2 (kimi verified assign/clear state
machine, gap machinery, wire parity; mimo verified local-commit,
exact-size decode, gate approvals; glm verified gap/assign/wire
parity end to end; minimax verified the full edit core, durability
fix, fail-closed accounting, and record accuracy).

Chunk 4 level-1 review round (2026-08-18; reviewed at HEAD e1c8aeb):
five aspect reviewers; four PASS (Linnaeus, Peirce, Jason, Leibniz),
one FAIL with two P2 (Sartre). Fix wave committed at 818691f:
- Sartre P2-1 (records): the chunk-4 record named 0963218 -> working
  tree; the code is committed at e1c8aeb. Corrected in this record.
- Sartre P2-2 (gate): the reclaim.go rand.Read exemption let a
  receiver that FAILED the *types.Func resolution (a local struct
  field named Read) fall through to the exemption, and never checked
  the argument. The exemption now requires resolution to
  crypto/rand's Read (receiver) AND an owned, non-tainted,
  non-file-bearing argument; battery pin 316 (struct-field shadow,
  expectFail) added. Battery 696 -> 697 cases.
- Sartre P3-3 (API): StartDraft accepted an all-zero commit nonce;
  Rust nonzero_128 hard-fails an all-zero draw. StartDraft now
  refuses the zero nonce (CodeFormatInvalid) with the caller
  contract recorded; the public workflows draw from randomNonce
  when they arrive.
- Peirce P3-1 / Jason P3-1 / Leibniz P3-3 / Sartre P3-1 (parity):
  reclaimExtent freed u32-truncated page numbers where Rust fails
  closed Corrupt("reclaimed page exceeds page-number space"); the
  guard now matches. Jason P3-2 / Sartre P3-2: retire appendGroup/
  scanGroup totals now checked (Rust checked_add
  ArithmeticOverflow); the check-add rewrite initially introduced a
  loop-scope shadowing regression the existing retire pin caught
  immediately, fixed with plain assignment; the appendGroup
  overflow/limit branches were then separated to exact Rust
  error propagation ((bool, error), ArithmeticOverflow) per the
  re-review residuals, verified by Leibniz at the final tree.
- P3 accepted with record: crash-consistency child tests for the
  four commit crash points are tracked to the crash/recovery chunk
  (the points and the fault machinery are parity-exact; no Go test
  yet spawns IPRANGE_V4_TEST_CRASH_AT children); FlushRange
  alignment strictness (stricter than Rust checked_range, all call
  sites aligned) accepted conservatism; unprovedTailEnd as *uint64
  is the idiomatic Go Option<u64> (Rust Option<u64> parity); the
  post-sync FileSize in Publish equals Rust's shrink-time
  physical_bytes under the exclusive lifetime lock.
- Level-1 re-review at the fix-wave tree: 5/5 PASS (Linnaeus, Jason,
  Peirce, Sartre, Leibniz). The regression pin for the scanGroup
  shadowing fails on the pre-fix build (local proof).
Validation (fix wave): go test ./... (both tag sets), -race, vet,
gofmt, import-graph, 11/11 cross-builds, 697-case gate battery
(576 fail, 121 benign) - all green, zero misses.

Level-2 gate for chunk 4 (2026-08-18, HEAD d26662f/code 818691f):
kimi PASS, mimo PASS, minimax PASS, glm FAIL with one P1 and two P2.
Dispositions:
- glm P1 (Publish committed_bytes unchecked): REFUTED with direct
  evidence. Rust writer_core/publication.rs publish computes
  committed_bytes with an unchecked `page_count * PAGE_SIZE as u64`
  (same as Go publication.go:130); the checked_mul lives in
  require_draft_length on both sides (Go checkedMul at
  publication.go:100, Rust publication.rs:50-55), which the workflow
  runs before publish. Go matches the authority exactly; a
  malformed PageCount cannot reach Publish without failing the
  length gate first.
- glm P2a (StartDraft zero-nonce class): the all-zero nonce refusal
  uses CodeFormatInvalid, the module-wide mapping of Rust
  Error::Corrupt (the same class Rust nonzero_128 returns for an
  all-zero draw); the caller-supplied-nonce API shape is recorded
  in the code comment and the SOW; kept as the uniform module
  mapping, documented, accepted.
- glm P2b (randomNonce IO mapping): Rust Error::Random(getrandom)
  maps to ErrorCode::Io; Go returns CodeIO - numeric parity. The
  mapping is now recorded here; no code change needed.
- glm P3 (post-sync FileSize, crash tests pending, previousTxn=0,
  unprovedTailEnd) - accepted as already recorded; the stale
  chunk-4 header GLM flagged is corrected in this record.
Re-review at the corrected record: glm PASS, confirming the P1
refutation (Rust publication.rs publish unchecked multiply) and the
corrected header; chunk 4 level-2 gate closes 4/4 PASS.


Milestone 2 chunk 5 - crash/recovery child-process tests (2026-08-19;
committed at a9a67e5): the crash-consistency suite tracked from the
chunk-4 close. The four commit crash points and the fault machinery
were parity-exact since chunk 4; this chunk proves each crash state on
the real mapped publication path in a spawned child of the test binary
(Rust live_crash_tests.rs, every child dies with Rust's code 86 at the
exact physical step named by IPRANGE_V4_TEST_CRASH_AT):
- v4/go/internal/writer/crash_v4work_test.go (v4work-only, the build
  the fault machinery lives in): TestCrashChild is the subprocess entry
  point (skips in a normal suite run; the Rust #[ignore] shape), and
  runCrashChild spawns os.Executable() with the action and crash-point
  environment and requires exit code 86.
- TestCrashCommitSelectsCompleteGeneration (Rust
  commit_crashes_select_only_a_complete_generation): one [10,20] -> 123
  commit crashing at each of the four points. before/after_private_sync:
  the unpublished tail the draft grew is refused by the immutable reader
  (Rust ImmutableReader::open ImmutableLengthMismatch parity), the
  writer open is the recovery surface (committed bootstrap + tail trim,
  Rust live_writer open_locked) and re-selects txn 1, then the reader
  sees txn 1 with the value absent. after_meta_write/after_meta_sync:
  shrink ran before the meta write, the alternate meta page is complete
  in the page cache (or durable after the sync); the reader opens
  directly on txn 2 with the value present.
- TestCrashReclamationPreservesCompleteGeneration (Rust
  reclamation_crashes_preserve_a_complete_readable_generation): two
  commits ([10,20] -> 1, then [12,18] -> 2, txn 3), then a reclamation
  publish crashing at the four points; the reader selects txn 3 (meta
  untouched) or txn 4 (meta written) and both committed ranges stay
  readable at 11 -> 1 and 15 -> 2.
- TestProcessDeathReleasesLocks (Rust
  process_death_releases_reader_and_writer_locks): a child holding the
  reader or writer lifetime claim exits; the parent re-opens the same
  shape immediately.
- Not ported with reason: Rust create/initialize/reset crash groups and
  metadata_crashes_select_absence_or_the_complete_value exercise live
  surfaces Go does not have yet (create_live/initialize_live/
  reset_live_coordination and the metadata-JSON writer API); the Go
  metadata JSON read surface exists but nothing can write metadata yet.
  They are tracked to the live-lifecycle/outer-workflow chunks that add
  those surfaces.
Validation: go test ./... (both tag sets), -race (both tag sets on
internal/writer), vet (both tag sets), gofmt, import-graph check, direct
scanner run, 697-case gate battery, 11/11 cross-builds - all green.

Chunk 5 level-1 review round (2026-08-19; reviewed at HEAD 6be81c2):
five aspect reviewers; four PASS (Jason, Sartre, Peirce, Leibniz), one
FAIL with two P2 (Linnaeus, test mechanics). Fix wave committed at
2869a68 (+ timeout diagnostics at 18da163):
- Linnaeus P2-1 (hermeticity): TestCrashChild skipped on the ambient
  action environment, so a stray developer IPRANGE_V4_TEST_ACTION could
  execute a child action in-process and kill the whole test binary
  (os.Exit 86) or red the package. runCrashChild now sets a spawn-only
  marker (IPRANGE_V4_TEST_SPAWNED=1) and TestCrashChild skips unless it
  is present; the child env also strips every inherited
  IPRANGE_V4_TEST_* variable so an ambient crash point cannot redirect
  the child (verified locally: ambient variables in a normal suite run
  produce the skip and nothing is touched).
- Linnaeus P2-2 (hang/orphan): a lock regression would stall cmd.Run
  until the go-test timeout and orphan the child with the lifetime lock
  held. The child is now spawned via exec.CommandContext with a 60s
  context (the parent kills a hung child and reports the timeout class,
  18da163) and the child carries a portable self-deadline
  (time.AfterFunc -> os.Exit(1)) so it cannot linger past 60s even when
  the parent already died.
- Linnaeus P3-1 / Peirce P3-1 (allocator dynamics): the reclamation
  txn-3 cases now run the writer-open recovery shape first (committed
  bootstrap + tail trim) instead of relying on a no-growth invariant;
  the commit-test fixture dependency (FreeBitmapRoot=0 forces every
  draft allocation to the file tail) is pinned in a comment at the
  assertion site.
- Jason P3-1 (drift hazard): the canonical commit orchestration moved
  to an untagged commitRange helper in publication_test.go; commitOne
  and every crash child delegate to it, so a future commit-flow change
  cannot silently diverge the crash suite from the publication tests.
- Linnaeus P3-2 / Jason P3-2: crashBudget removed; the child actions
  reuse the existing testBudget helper.
- Linnaeus P3-3 / Sartre P3-1: the tail-refusal assertion pins the
  exact CodeFormatInvalid class (errCode) and closes the reader on the
  unexpected-accept path.
- Jason P3-3: inherited IPRANGE_V4_TEST_* variables are stripped from
  the child environment (folded into the P2-1 fix).
Level-1 re-review at the fix-wave tree (HEAD 2869a68/18da163): 5/5 PASS
(Linnaeus, Jason, Peirce, Sartre, Leibniz).
Validation (fix wave): go test ./... (both tag sets), -race, vet (both
tag sets), gofmt, import-graph, 11/11 cross-builds, 697-case gate
battery (576 fail, 121 benign) - all green at HEAD 18da163.

Level-2 gate for chunk 5 (2026-08-19, HEAD 18da163): kimi PASS, minimax
PASS, mimo PASS. glm unavailable for this gate: its long-lived session
hit the provider context-length limit on both attempts (technical, not
a review finding) and a fresh-session respawn was blocked by the agent
thread limit; per the swarm rules it was skipped and does not count in
the denominator (coverage 3/4, reported to the user). All three
available reviewers ran full-scope adversarial review of the crash
suite, the shared-helper refactor, and the SOW record: kimi verified
mechanically by driving the child binary (exit 86 at the armed point,
exit 1 with none armed - the suite is non-vacuous); minimax verified
every fix-wave item, both crash-state bootstrap selections, the
lock-release semantics (OFD/flock kernel release on death), and the
record; mimo verified the 1:1 fault-point parity, spawn-marker
hermeticity, exit-code dispatch, and the not-ported list. No P0-P2;
only the recorded P3 notes (v4work-only placement by design, the
FreeBitmapRoot=0 fixture dependency pinned in a comment, os.Exit(86)
bypassing Go-style cleanup exactly like Rust _exit(86)). Chunk 5
level-2 gate closes 3/3 available PASS.

Chunk 6 design record - public Go generation API + Go-produced corpus +
Rust cross-open + mixed subprocess gates (2026-08-19; decisions recorded
before implementation, per the SOW process):
- D1 (facade boundary): the module-root import boundary extends to
  stdlib + internal/format + internal/reader + internal/writer
  (check-import-graph.sh root rule and header comment). The public SDK
  stays one `iprangedb` package mirroring the single Rust lib; the
  writer facade is a thin composition over the internal owner. Every
  other package still may not import internal/*; the per-target loop
  package list is unchanged (internal/writer already listed).
- D2 (create without sidecar): Go has no sidecar until milestone 4, so
  the public Create mirrors only the main-file half of Rust
  create_live: O_CREATE|O_EXCL destination refusal (Rust require_absent
  parity, fail closed on existing files), exclusive lifetime lock, a
  2-page extent with the identical txn-1 meta on both pages (Rust
  EmptySpec::live + write_empty: page_count 2, all roots 0,
  feed_index_limit 0, membership_id_limit 1 for membership/structured,
  structure_id_limit 1 for structured), flush + sync, then close.
  reader_capacity, the sidecar, cleanup IDs, and the private-then-
  publish namespace stage are milestone-4 gaps recorded here, not
  defects of this chunk.
- D3 (public workflow shape): Create(path, family, kind, structure,
  tag) -> CreateResult{DatabaseID, CommitNonce, TxnID=1}; OpenWriter
  (Go writer.Open; default budget = the fixture-proved values
  32 MiB heap / 200k private / 200k growth pages, exported
  DefaultBudget); Writer.Info (DatabaseInfo over the selected
  committed generation); Writer.BeginDirect (direct-only ValueKind
  gate, Rust begin_direct_state parity; the commit nonce is drawn
  inside the writer core via Core.BeginDraft -> randomNonce, keeping
  the crypto/rand exemption path in internal/writer/reclaim.go);
  DirectTransaction.AssignV4/AssignV6/ClearV4/ClearV6/
  SetMetadataJSON/ClearMetadataJSON/Commit/Abort; Writer.Close
  (PrepareClose + FinishClose + mapping close; an open draft is
  discarded like Rust close with had_pending). No CancellationToken:
  the Go writer has no cancellation machinery (milestone-4 workflow
  concern; recorded gap). CommitResult mirrors the Rust facts minus
  the sidecar/coordination/cleanup surface (recorded gap): Status
  (Committed / BeforePublication / OutcomeUnknown), DatabaseID,
  TransactionID, CommitNonce, Err; an unchanged draft commits to
  CodeNoPendingTransaction and is discarded (Rust commit_attempt
  parity).
- D4 (metadata edit core): internal/writer gains the draft metadata
  surface mirroring Rust draft_store/metadata.rs exactly: 20 MiB
  uncompressed cap, compressed_bound + heap-budget guard, deflate into
  the bound with a stored-zlib fallback when deflate cannot finish
  inside it (Rust compress), chunk-chain write with the exact chain
  geometry (each page: header type 13 born=target txn item_count 1
  level 0 lower 48+len upper 4096 aux 0, next at 32, length at 36,
  reserved zero at 38, logical offset at 40, data at 48; stale chain
  pages retired), metadata_staged state gate (one metadata stage per
  transaction, Rust require_metadata_available/finish_metadata_stage
  parity), and clear_metadata root-0 no-op. Compressed bytes may
  differ from Rust (corpus rule); every reader contract (chain
  geometry, offset continuity, full-chunk nonfinal pages, zlib stream
  exactness) is pinned by the existing readers on both sides.
- D5 (Go-produced corpus): two direct fixtures committed under
  v4/conformance/go/: direct-ipv4.iprdb (the direct-ipv4 op sequence
  with Go producer metadata text) and first-seen-ipv6.iprdb (whole-
  space v6 assign value 1_700_000_000, empty metadata payload via
  SetMetadataJSON([]) - the exact Rust commit_changed shape; the
  existing rust/first-seen-ipv6 fixture carries present-empty
  metadata, probe-verified). Membership/structured Go fixtures are
  blocked on their edit cores (later milestones) and are recorded as
  such, not as a gap in this chunk's gate. Manifest entries are added;
  both reader inventory tests are updated (Rust keeps its 6-fixture
  rust-count assertion, add the go set; Go stops asserting all-rust
  and asserts the exact combined inventory).
- D6 (mixed subprocess gates, both directions, no sidecar): Go suite
  spawns fresh Go processes (the test binary, chunk-5 pattern) that
  (a) open and verify a Rust-produced fixture, (b) open and verify a
  Go-produced fixture, (c) create + commit a database through the
  public API and verify it read-back; the Rust conformance suite
  spawns a fresh Rust process (current_exe child, fork_ownership
  pattern, no external toolchain dependency) that opens a Go-produced
  fixture in the child. Both sides prove fresh-process opens over both
  producer sets. The sidecar/reader-table/reclamation mixed directions
  of section 21 are milestone-4 gates (unchanged).
- D7 (work counters): no new counters; Rust metadata.rs and the Go
  work package add none for metadata (mirror authority), and the
  existing v4work counters already cover the range/alloc paths the
  fixtures exercise.

Chunk 6 close-out (2026-08-19; implementation + validation):
- Public Go writer facade shipped: Create (O_EXCL + lifetime lock +
  txn-1 2-page meta pair), OpenWriter, Writer.Info, BeginDirect,
  DirectTransaction.AssignV4/AssignV6/ClearV4/ClearV6/
  SetMetadataJSON/ClearMetadataJSON/Commit/Abort, Writer.Close; the
  internal writer core owns all persistent bytes (mmap-only, no
  complete page in owned memory), metadata core mirrors Rust
  metadata.rs (20 MiB cap, bounded zlib-deflate with stored fallback,
  chunk chain, one stage/txn).
- Go-produced corpus: v4/conformance/go/direct-ipv4.iprdb and
  first-seen-ipv6.iprdb committed; cases.json records producer=go for
  both; Go conformance inventory covers all 8 fixtures with
  rust|go producer; regeneration is env-gated
  (IPRANGE_V4_GO_REGENERATE_FIXTURES=1) and verifies staged output
  against cases.json before replacing files.
- Cross-open proven both directions: Rust conformance opens the Go
  fixtures (passed); Go conformance opens the Rust fixtures (passed);
  mixed subprocess gates added on both sides
  (v4/go/subprocess_cross_open_test.go - child opens rust fixture +
  go fixture + create-commit-read-back roundtrip; Rust
  tests/mixed_subprocess.rs - child opens both direct-ipv4 fixtures
  via ImmutableReader) and pass.
- Gate tooling: rules.go gained the file-scoped metadata.go
  exemption (flate/bytes.Buffer shapes with taint + static-type
  guards; copy(page[48:], chunk) only for Update-callback page
  formals); battery 317-321 pin the file-scope edges; battery
  self-test 702/702 OK (580 fail forms, 122 benign forms), 0 MISS;
  module scan on the reviewed tree: SCAN EXIT 0, 0 violations.
- Full validation at the closing commit: go test ./... including
  -race and -tags v4work, go vet, gofmt -l, check-import-graph.sh,
  11-target cross-compile matrix (linux amd64/386/arm/arm64/loong64,
  darwin amd64/arm64, freebsd amd64, netbsd amd64, windows
  amd64/arm64), SOW audit, Rust all-features cargo test, Rust
  check-source-graph.sh - all green. README (v4/conformance/README.md)
  documents both producer sets and the regeneration/subprocess
  commands. Milestone 2 (public writer + corpus + cross-open) is
  complete; a swarm final gate closes it in the next record.

Chunk 6 level-1 gate round (2026-08-19, five adversarial reviewers on
the uncommitted chunk-6 diff; every finding reproduced by the lead
before any change; fixes implemented and validated in this session):
- Peirce (corpus/conformance): P2-1 regeneration was one-shot (Create's
  O_EXCL refused the committed fixtures, so the documented regeneration
  command could never run post-commit) and P2-2 it neither staged nor
  verified (two spot lookups only). Fixed with the Rust two-phase
  contract: generate both fixtures into a staging corpus, verify the
  staging corpus with the exact same conformance suite in a subprocess
  (IPRANGE_V4_GO_CORPUS override), and only then publish each file next
  to its target and rename over it. P3s: subprocess diagnostics said
  "want (2, ...)" for the fixture's actual value 3 (fixed); the child
  now strips the IPRANGE_V4_TEST_* crash controls alongside the GO_
  controls; Rust mixed_subprocess gained a parent try_wait deadline and
  a child self-deadline (90 s, Go parity); README wording now states the
  subprocess gates are smoke gates and the full manifest verdicts are
  proven in-process.
- Linnaeus (metadata core + gate shaping): P2 - the metadata.go copy
  exemption was name-keyed (any same-file local named like the
  Update-callback formal inherited the exemption). Probe-verified with a
  name-shadowed owned local: the scan returned EXIT 0 on a complete-page
  copy. Fixed two layers: (a) the exemption is now binding-keyed on the
  types.Var identity of the actual callback formals; (b) checkCopy now
  accumulates destination-bounded spans (ownedCap < pageSize) of full
  mapped sources into the assembly accumulator, closing the
  page[48:]+page[:48] complete-page assembly shape (also probe-verified:
  an array-based assembly that previously passed is now rejected).
  Battery pin 321 pins the name-shadow shape (its first version used an
  Update-formal source the scanner does not taint; the pinned mutation
  now copies a real mapping.Page view). P3 notes: the deflate bound is
  enforced post-hoc rather than mid-stream like Rust (Go pre-Grows to
  the bound plus a dedicated 512 KiB headroom, so the physical budget
  holds; accepted as recorded); retirement-reuse and
  MaxMetadataChainPages boundary tests are not yet present on the Go
  side (recorded, not milestone-blocking).
- Jason (public facade): P1 - after CommitOutcomeUnknown the Go writer
  stayed fully usable while Rust brands the writer unusable until
  close. Fixed with Rust require_healthy parity: outcomeUnknown now
  records an unresolved marker on the Core and every mutating entry
  point (BeginDraft/StartDraft/CommitAttempt/Prepare/Publish/
  DiscardUnpublished/AssignV4/V6/ClearV4/V6/SetMetadata/ClearMetadata/
  PrepareReclamation) fails closed with WrongState until Close; a new
  fault.Fail point (v4work-only, one-shot, compiled out of production)
  past the alternate meta write drives the new v4work subprocess test
  TestWriterOutcomeUnknownFailClosed (outcome_unknown + abort_class
  children; the abort_class child also proves a preparation failure
  keeps the writer healthy). P2 - a failed Create left the partial
  destination in place and poisoned retries; mapping.Create now removes
  the exclusively-created file on every error path (Rust
  live_cleanup::remove parity) and create_test.go pins the retry.
  P3a - second Close was CodeHandleClosed; now idempotent success and
  post-close calls report WrongState (Rust State::Closed parity; public
  pins updated). P3b - facade Commit now runs RequireDraftLength and
  RequireUnchangedBase (exported from the core) immediately before
  Publish, mirroring Rust prepublication_checks. P3c - preparation
  failures now surface the TransactionAborted class (code 22) with the
  cause chain preserved (chainError), and a failed abandon discard
  nests the CleanupInProgress class (code 64), mirroring Rust
  abort_after_source.
- Sartre (mmap/taint integrity) FAIL and Leibniz (records/hygiene)
  FAIL, both fixed and pinned this session:
  - Sartre P2-1/P3-1 (concrete-receiver store callbacks never
    seeded): only interface-dispatch call sites seeded the mapped
    callback formals; s.Inspect/Update on the CONCRETE *DraftStore
    (the writer's whole store surface) analyzed callback literals
    with no mapped provenance, so copy(owned, page) inside a concrete
    callback exited the scan silently (probe-verified before any
    fix). Two root layers: (a) evalCall approved store-callback calls
    only when the static receiver type was one of the approved
    interfaces; (b) even with the widening, the args loop deleted the
    seeds BEFORE argFlowOf re-evaluated the literal, so the LAST
    cached body values were the unseeded ones and the rule pass saw a
    clean source. FIXED: the seed now applies to any receiver that
    provably implements one of the approved store interfaces
    (types.Implements over approvedStoreInterfaces), and the deletion
    moved after argFlowOf so the final body analysis carries the
    mapped taint. Probes: copy(owned[:], p) in a concrete Inspect
    callback now fails with "copy of a mapped page view into an owned
    buffer"; the honest twin (copy INTO the mapped param) stays
    clean; the whole module scan stays rc=0 (5 OS). Pins P385 (fail)
    and P386 (benign) pin both shapes.
  - Sartre P3-3 (flate.NewWriterDict): the dictionary compressor
    constructor was neither banned nor exempted, and its
    *flate.Writer Write calls would match the metadata.go exemption
    once a mapped argument is involved. FIXED: NewWriterDict added to
    bannedSelectors; pin P387 pins the rejection.
  - Leibniz P2-1 (create.go dropped closeErr): a writeEmptyMeta
    failure discarded the mapping close error. FIXED with joinError
    in internal/writer (cause chain preserved through Unwrap, no
    interface-typed fmt formatting); create_work_test.go pins the
    write_empty fault path (fault.Fail("create.write_empty"),
    v4work-only: partial file removed, retry succeeds) and
    create_test.go pins the joinError surface. Both partial-file
    removals (the mapping-create deferred cleanup and the
    post-writeEmptyMeta removal) are identity-guarded like Rust
    remove_exact: a path that no longer names the created file is
    never removed (SameFile / Mapping.VerifyIdentity).
  - Leibniz P3-1 (stale battery counts): the chunk-6 close-out record
    above now carries the real numbers (702 pre-round-8, 705
    post-round-8).
  - Leibniz P3-2 (.gitignore): v4/go-gate/.gitignore now covers
    go-gate-local and go-gate.exe (the locally built gate binaries).
  - Leibniz P3-3 (Windows regeneration rename): the Go fixture
    regeneration removes an existing target on windows before the
    rename, mirroring the Rust cfg(windows) publish branch.
  Final validation on the reviewed tree: battery 705/705 (582 fail
  forms, 123 benign forms), 0 MISS under --self-test-jobs 24; module
  scan rc=0 (5 OS); go test ./... both tag sets, -race, vet, gofmt,
  check-import-graph.sh, 11-target cross-compile matrix, Rust
  all-features cargo test (incl. mixed_subprocess), Rust
  check-source-graph.sh - all green. Chunk 6 records are now
  complete; the level-2 gate verdicts land in the next record.

Chunk 6 level-2 gate round (2026-08-19, the open level-2 set minus
glm: kimi, minimax, mimo, qwen):
- kimi PASS, minimax PASS, mimo PASS on the round-8 tree (705/705).
  The only notes were non-blocking P3 records-hygiene items: the
  chunk-6 close-out still carried the historical 702 battery figure;
  corrected inline in the level-1 record below (702 pre-round-8, 705
  post-round-8).
- qwen FAIL with one real P2: string(p) inside a seeded store-callback
  formal silently escaped the gate. Root cause: checkArrayConversionSink
  only rejected string(page) when the value had no caller-supplied
  bound (hasSrc false); seeded callback formals carry hasSrc=true
  because the store call site seeds their taint, so the provably mapped
  formal converted the full page into an owned string unchecked
  (probe gateStr exited 0 before the fix). FIXED on two layers in
  rules.go: (a) the string sink now fails whenever the value is a
  provably mapped parameter (mapped flag set) or has no caller bound,
  unless the conversion is a definite sub-page slice
  (string(p[:16])); (b) checkAssign now falls back to the conversion
  expression's own type when the short-var LHS ident has no type
  record, so the sink receives a non-nil destination for the common
  q := string(p) shape. Probe-verified: the failing twin
  (string(p) in a concrete *DraftStore Inspect callback) is now
  flagged on all 5 OS with "string conversion of a full-page view",
  the honest twin (string(p[:16]) same shape) stays clean, and the
  module scan stays rc=0. Battery pins P388 (fail) and P389 (benign)
  pin both shapes in internal/writer/.
- Level-2 final validation on the fixed tree: battery 707/707 (583
  fail forms, 124 benign forms), 0 MISS under --self-test-jobs 24;
  module scan rc=0 (5 OS); go test ./... both tag sets, -race both
  tag sets, vet, gofmt, check-import-graph.sh, 11-target cross-compile
  matrix, Rust all-features cargo test (incl. mixed_subprocess), Rust
  check-source-graph.sh, SOW audit - all green.
- qwen re-review of the fix: ACCEPT (verified against the rebuilt
  gate: the escape is closed in every shape probed - interface and
  concrete receivers, short-var and assign-to-existing LHS, named
  string types, CopyPage src param - the honest twin string(p[:16])
  stays clean, the caller-side string-params fence (strHelper4
  shape) still fires, and the mapped-flag logic is confirmed
  false-positive-free because mapped=true is set only by Mapping
  page mints and seedMappedCallbackParams). One non-blocking P3 note
  (pre-existing): a helper returning string(formal) may report two
  diagnostics at the same site (caller-side fence + local sink);
  redundant but never fired on honest code, recorded not changed.
- Chunk 6 level-2 gate verdict: 4/4 PASS (kimi, minimax, mimo,
  qwen). The chunk-6 milestone-2 gate is CLOSED and the chunk is
  committed at eb8c128 (signed); the SOW stays in-progress because
  milestone 3 (complete logical SDK) is the next unit; its chunk
  plan is recorded below (Milestone 3 plan record, 2026-08-19).

Milestone 3 plan record (2026-08-19, grounded in the Rust authority at
v4/rust/iprange-livedb/src: workflow.rs, cancellation.rs, snapshot/*,
membership_query/* (aggregation, algebra, join, decode),
feed_catalog.rs, feed_range_cursor.rs, range_cursor.rs, history.rs,
cardinality.rs; Go baseline = committed milestone-2 tree). Scope per
the approved Plan line: advanced transactions, typed workflows,
metadata, queries, joins, algebra, snapshots, reports, cancellation,
cleanup, randomized public models over the internal core. All chunks
stay mmap-only, one-authority, counter-instrumented in test builds
only; the five level-1 reviewers are re-aimed per chunk with new
aspects (1: reader logical surface vs Rust; 2: joins/algebra/snapshot
output semantics; 3: writer workflows/reports/cancellation/cleanup;
4: mmap-only/lifetime/zero-alloc; 5: cross-language parity and
records), and the level-2 gate stays kimi/minimax/mimo/qwen (glm
unavailable). Chunks:
- M3 chunk 1 - logical read SDK foundation: CancellationToken,
  FeedCursor (feed iteration), range cursors (DirectCursorV4/V6,
  DirectRange, RangeDirection, FeedRangeCursorV4/V6), MembershipQuery
  (all_feeds/named_feeds/matching_feeds_v4/v6/feed_count/feeds),
  MembershipScope, budgets, MatchingFeedsReport/Sink, FeedPair, plus
  the report data types (AddressRange, WorkflowKind, LogicalChange,
  WorkflowReport, FirstSeenRemoval, FirstSeenRemovalSink,
  HistoryWindow/HistoryWindowReport/HistoryProjectionReport) and full
  Cardinality129 parity checks - all read-only over the existing
  reader core, with zero-alloc and work-counter evidence.
- M3 chunk 2 - joins and aggregation: MembershipAggregation (modes,
  FeedCardinality, FeedOverlap, sink, report), DirectJoin (source,
  budget, cells, sink, report), MembershipCross (cells, UncoveredSide,
  report) - read-only, allocation-free sinks, budgets enforced.
- M3 chunk 3 - algebra set operations and output: FeedSelection,
  AlgebraSetOperation (Union/Intersection/Exclusion),
  AlgebraOutputMode (PreserveFeeds/Flat), AlgebraCountReport,
  AlgebraComparisonReport, AlgebraOutputBudget/MembershipAlgebraBudget,
  AlgebraSetOutcome, and the materialized-result output machinery
  shared with snapshot publication; snapshot_to Immutable mode
  (pinned generation to compact unsigned snapshot),
  SnapshotBudget/SourceMode/PublicationPolicy/Result/PreparationFailure.
- M3 chunk 4 - typed writer workflows and live snapshots:
  CreateFeed/ReplaceFeed/MembershipImport workflows on the writer
  core with pre-publication WorkflowReport statistics (input counts,
  before/after range records, Cardinality129 comparisons),
  DirectReplacement parity, FirstSeenRefresh/LastSeenRefresh with
  FirstSeenRemovalSink batching, history-window creation, cancellation
  checkpoints in every bounded mutation op, cleanup paths, and
  snapshot_to Live mode.
- M3 chunk 5 - randomized public models and final gate: randomized
  cross-language conformance (Go vs Rust random databases and random
  logical operations, budget and cancellation injection), v4work
  fault probes through the workflows, necessary-work audit and
  zero-alloc proof for all new surfaces, five-reviewer level-1 round,
  level-2 gate, records, close-out.
Each chunk closes with its own gate evidence like milestone 2; the
chunk order is authoritative and a later chunk never reopens an
earlier one without a regression record.

Decision (2026-08-19, resolved with the user, replacing the earlier
battery policy): boundary enforcement replaces the heavy battery as
the routine gate. The view-holder whitelist is now the architectural
contract: only internal/mapping (descriptor + mmap owner), internal/
format (wire codec), internal/reader, internal/writer, and the public
facade (module root) may handle mapped page views - enforced by the
import graph plus a new gate rule that fails any mapped-view export
from a non-holder package (unit-pinned in v4/go-gate/viewholder_test.go;
end-to-end battery forms B1-B4 stay in the archived battery table).
Measured gate reality (2026-08-19): a single-config module scan costs
~6-12 min on the grown tree (the analyzer is GC-bound, profiled:
~55% runtime marking; the loader is now per-config instead of per-
directory, and the stdlib cache is shared, but the page-flow
allocation volume dominates), so per-case battery runs are unusable at
any routine frequency. The routine gate check for every chunk is
therefore: one real-module linux scan (rc=0) plus the dynamic mmap-only
evidence - strace (v4/go/check-mmap-trace.sh: no read/pread64/readv/
write/lseek on any database descriptor) and the MemStats size-class
assertion (mmap_page_alloc_test.go: no heap allocation >= 4096 bytes
during lookups) - all run at nice. The durable battery (battery.go +
battery_page.go; 711 forms at the 2026-08-19 decision - the chunk-6
"707/707" figure was the inventory of that earlier run - and 715 forms
after the M3 chunk-1 review added B5-B8 for the generic-erasure and
root-boundary shapes) stays in the tool as the archived regression
net, run only on request or at the M3 close if explicitly scheduled;
all recorded historical runs stay valid. The gate's allocation/GC
cost (per-config loader, shared FileSet, page-flow allocation volume;
measured ~6-12 min per linux scan) is tracked by pending SOW-0026
(typed ownership analyzer / allocation reduction), not M3 scope. Performance is a hard constraint of this decision:
every enforcement mechanism is static analysis or test-only;
production code paths are unchanged, so the SDK cannot become slower.

Milestone 3 chunk 1 - logical read SDK foundation (2026-08-19, HEAD
pending-commit): the read-only logical surface over the committed
milestone-2 core. New public types and APIs: CancellationToken,
DirectCursorV4/V6 with RangeDirection and DirectRange, FeedRangeCursor
V4/V6, MembershipQuery (all_feeds/named_feeds/matching_feeds_v4/v6)
with MembershipScope, budgets, MatchingFeedsReport/MatchingFeedsSink,
FeedPair, and the workflow/report value types (AddressRange,
WorkflowKind, LogicalChange, WorkflowReport, FirstSeenRemoval,
FirstSeenRemovalSink, HistoryWindow/HistoryWindowReport/
HistoryProjectionReport), plus full Cardinality129 parity checks - all
read-only over the existing reader core, zero-alloc on warm paths
(5 new cursor/query zero-alloc sub-tests in zeroalloc_test.go all
PASS), and necessary-work counted under the v4work tag only
(internal/work + work_v4work pins). The DirectCursor4/6 seek state is
dispatched through explicit struct fields (branchChild, leafSeekPolicy,
branchSelectPolicy); MembershipQuery resolves named feeds through
ResolveNamedFeeds; cancellation.check is nil-receiver-safe and passed
through without a checkpoint indirection.
The 2026-08-19 boundary decision (above) is implemented in this chunk:
the view-holder whitelist rule checkViewHolderExports fails closed on
mapped-view exports from non-holder packages (unit-pinned in
v4/go-gate/viewholder_test.go; end-to-end forms B1-B4 join the archived
battery table), the gate's loader is now per-OS-config instead of
per-directory with first-object-wins cache seeding (type identity
across the module preserved; measured linux scan rc=0 in ~12 min,
GC-bound, allocation reduction tracked as follow-up), the routine
per-chunk gate evidence is the module scan + dynamic mmap-only proof
(v4/go/check-mmap-trace.sh strace: no read/pread64/readv/write/lseek on
any database descriptor; TestNoPageSizedHeapAllocations: no heap size
class >= 4096 allocates during lookups), and check-import-graph.sh
documents the holder whitelist. Validation: go test ./... both tag
sets + -race both + vet + gofmt clean, import-graph check clean,
production linux gate scan rc=0, Rust check-source-graph.sh (459
sources) + cargo test --all-features clean, 11/11 cross-compiles, SOW
audit clean. Level-1/level-2 review rounds for this chunk are recorded
below (or in the next records) and committed together with the chunk.

### 2026-08-19 - M3 chunk-1 level-1 adversarial round: reader fixes and boundary-level gate fixes (working tree, pending commit)

The five level-1 reviewers (Jason: public logical facade; Linnaeus:
internal reader core and lifetimes; Peirce: cross-language parity and
work counters; Sartre: mmap-only / ownership / zero-alloc + holder
boundary; Leibniz: records hygiene) reviewed the chunk-1 delta. Two P1
and eleven P2/P3 were confirmed; all reader findings are fixed in the
working tree:

- P1 empty-db cursors: newTreeCursor refused root==0 as corruption;
  Rust treats a zero root as a finished cursor
  (fixed_tree/cursor.rs::unpositioned). Fixed in
  v4/go/internal/reader/cursor.go (finished cursor for root==0 +
  seekPosition empty-tree return); the dead empty-root guard in
  index.go NewFeedCursor was removed. Pinned by
  TestEmptyDatabaseCursors (v4/go/logical_query_test.go).
- P1 feed-range warm-path allocations: nextInner/merge/contains
  returned heap pointers (pending, merged, membership cache);
  the zeroalloc pin was vacuous (AllocsPerRun's warm-up exhausted the
  shared cursor, so measured runs hit the finished path only). The
  projection is now all value-typed (pending+hasPending,
  membershipCache fields, merge returning (value, bool)); the pins
  measure an open-vs-open+scan delta so per-interval work is really
  measured (zeroalloc_test.go feedrange-cursor-v4/v6-delta).
- P2 seek-below-minimum: a forward seek below the tree's first key
  returned finished instead of the first range on multi-level trees
  (rangeBranchSelect returned (0,0,nil); seekPosition mapped next==0
  to finished). Rewritten as rangeBranchSeek4/6 mirroring Rust
  seek_inner: lower-bound, exact/sub-1 selection, (None, Forward) => 0,
  (None, Backward) => Finished, and require_child validation
  (2 <= child < pageLimit) at the branch step. Pinned by
  TestMultiLevelSeekBelowMinimum.
- P2 corrupt child: a branch child outside [2, pageLimit) now reports
  corruption at the branch selection (Rust require_child parity),
  never a silent empty result. Pinned by
  TestMultiLevelSeekCorruptChild (the child==0 shape was already a
  format header error via DecodeRangeEntry*; the seek-empty collapse
  was the real silent path).
- P2 counter mislabel: reader cursors counted work.RangeEmitted
  (writer semantics); Rust readers count range_consumed. Added
  RangeConsumed/RangesConsumed (work.go + work_v4work.go), writer
  RangeEmitted unchanged, pins renamed in query_work_test.go.
- P2 MembershipQuery gate: the query surface admitted Structured
  databases; Rust membership_query::Query::new requires Membership
  only. requireMembershipQuery (strict) added in reader_public.go.
- P2 HistoryProjectionReport shape: the Go type carried per-window
  fields (minus Created); Rust history.rs reports aggregates plus
  windows. Replaced to field-for-field parity (logical_change,
  source_range_count, source_addresses, created_feed_count, before/
  after aggregates, windows).
- P2 sorted scope: sortEntries was insertion sort (O(n^2) on
  caller-chosen order); replaced with sort.Slice (Rust
  sort_unstable_by_key parity).
- P2 scope budget parity: Go accounted 32+nameLen per entry while Rust
  pre-accounts count * size_of::<FeedEntry>() (24) plus the feed-index
  map (dense u32 vs sparse slots, smaller wins); identical inputs now
  admit identically (membership_query.go chargeEntries/chargeIndexMap/
  chargeHeap, checked arithmetic, checkpoint every 4096 steps, empty
  scope argument error precedes budget refusal like Rust).
- P2/P3 merge MAX guard: mergeRange6's adjacency condition was
  inverted (MAX endpoint merged instead of refusing); now mirrors
  checked_next (mergeRange4 + mergeRange6). emitWord uses checked
  u32/u64 arithmetic (Rust ArithmeticOverflow parity) for the feed
  index and the match count.
- P2 docs: name-yielding surfaces (NextFeed, MatchingFeeds yields,
  Feeds) copy names to owned strings by design at the root boundary
  (Go cannot borrow); the API docs now state the per-name allocation,
  and the MembershipScope "names alias the mapping" doc contradiction
  is fixed (they are copies).
- Verified-not-changed: the membership_word_read counter concern was a
  false positive - Go counts WordRead per word inside
  MembershipView.readWordsInner, which totals identically to Rust's
  per-batch membership_word_read; the v4work pin (WordReads: 2) holds.
- Boundary-level gate fixes (Sartre Part 2 + Leibniz):
  - The public facade (last holder) may no longer EXPORT mapped views:
    checkViewHolderExports now checks exported symbols of the module
    root (unit-pinned in viewholder_test.go; B6 fail form + B7 benign
    twin in the battery).
  - Generic type-parameter erasure: an unproven callee (interface
    method / type-param receiver) now inherits the receiver's and
    arguments' provenance, so a generic helper forwarding a minted
    page summaries mapped and the holder rule fails closed
    (pageflow.go indirect-callee branch; B5 fail form added).
  - Battery copyTree now re-creates symlinks (CLAUDE.md -> AGENTS.md)
    instead of failing the copy; the 707/711/714 inventory and the
    measured gate cost are recorded honestly (SOW-0026 pending
    tracks the typed ownership analyzer / allocation reduction).
  - check-mmap-trace.sh: writev/pwrite64 were grepped but never
    traced (dead check); the trace set now matches the violation
    pattern, and the test log uses a private mktemp path.
Validation at fix time: go test ./... (all packages), -race both tag
sets, v4work counter pins, gofmt/vet clean, multilevel and empty-db
pins PASS. Full-module production gate scan (check-import-graph.sh
non-self-test, 5 configs) PASSED at nice on the final tree
(import-graph check passed, 2026-08-19 22:28); check-mmap-trace.sh
PASS (no read/pread64/readv/write/writev/pwrite64/lseek on any .iprdb
descriptor; openat=5438 mmap=524). Battery verification of the new
B-forms at nice:

- B4 (holder export benign) PASS, B7 (public-facade benign twin) PASS,
  B6 (public-facade mapped export) PASS after the case was made
  self-contained: the first B6 form referenced HolderPage, a method
  only B4's mutation file creates, so B6's private module copy did not
  compile and the harness reported "missing expected rule" instead of
  the whitelist diagnostic. B6 now creates its own helper
  (internal/reader/gatemut_b6helper.go) in the same case.
- B5 first form (generic type-parameter helper) DOES NOT TERMINATE and
  was rewritten to the interface-receiver form (PeekC(m pager5) ->
  LeakC(m *mapping.Mapping)); the type-parameter termination defect is
  tracked in SOW-0026. See the SOW-0026 record for the diagnosis
  (fresh instantiated type identities defeat the walk seen-set;
  depth > 120 with 10.7M diagnostic hits in 40 s, never terminating).
  The interface form TERMINATES and the launder tree is rejected by
  the view-holder whitelist export rule itself: the first verification
  run showed only the call-site fail-closed rules firing ("mapped page
  view passed to m.Page on an unprovable receiver", "file-bearing
  argument laundered into an interface parameter (type erasure)")
  because the module-interface miss path summarized the unproven call
  result as tainted without mapped provenance. The pageflow.go
  unproven-callee handling now carries the mapped flag when the erased
  receiver could be the mapping owner itself (couldBeMappingOwner:
  types.Implements(*mapping.Mapping, receiver-interface)), so
  PeekC/LeakC summarize mapped and the whitelist fires; interfaces the
  mapping cannot implement (tree.Codec.ReadKey, external error/
  Stringer/io.Reader) keep tainted-only results, so bounded record
  copies stay legal. Verified on a mutation copy: "mapped page view
  exported from .../internal/tree by LeakC (result 0); view-holder
  whitelist" plus the two call-site rules, all at once.
- The type-parameter (generic) form of the helper still DOES NOT
  TERMINATE (defect tracked in SOW-0026) and is not a usable battery
  case until the analyzer caches instantiated summaries or keys the
  walk cycle-set on type origins.
- Harness invocation note: --self-test* battery workers must run with
  root = the Go module directory (v4/go), exactly like
  check-import-graph.sh does (cd "$(dirname "$0")"; scanner "."). A
  repo-root copy carries v4/rust/target release .s objects, and
  scanRoot rejects ANY assembly object (assembly body invisible to the
  source scan), which poisons every case with a false rejection.

Full go test ./... (all packages, both tag sets), go vet, go test
-race ./..., and the go-gate unit tests all pass at nice on the final
tree.

### 2026-08-20 - M3 chunk-1 level-2 round (open L2 set; glm unavailable, k3 removed)

- mimo: PASS with one P2 - captureDiagnostics saves/restores the
  global diagnosticCapture slice header without gateOutMu while
  reporter.fail appends under it (structurally unsafe; today the
  battery runs capture single-threaded). Fixed: the swap, the restore,
  and the read all take gateOutMu; fn() itself runs unlocked because
  fail re-enters the lock from the scanning goroutines. go-gate unit
  tests pass with the fix.
- minimax: FAIL with one P0 - the unproven-callee provenance branch
  marked results tainted but never mapped for interface receivers, so
  the view-holder whitelist could not fire on the B5 launder shape
  (the tree was still rejected, but by the pre-existing call-site
  rules). Fixed: the module-interface summary-miss path now carries
  the mapped flag when the erased receiver could be the mapping owner
  (couldBeMappingOwner = types.Implements(*mapping.Mapping,
  receiver-interface)); the indirect-callee branch applies the same
  check. Interfaces the mapping cannot implement (tree.Codec.ReadKey,
  external error/Stringer/io.Reader) keep tainted-only results, so
  bounded record copies stay legal. Verified on a mutation copy: the
  whitelist violation fires for PeekC and LeakC, and the full
  production scan (check-import-graph.sh, 5 configs) still passes
  rc=0, so no false positive landed in the real tree.
- kimi: FAIL with one P1 - a scalar-result interface method on an
  unapproved interface param (lenIface.Len) is flagged by the
  unprovable-receiver rule. Baseline proof (gatescan built from HEAD
  2f2a975): the identical violation fires on the pre-chunk analyzer,
  so this is pre-existing fail-closed conservatism (unapproved
  interface parameters are treated as possibly page-carrying; only
  approved store/codec interfaces and concrete flows stay benign),
  not a chunk regression. The production tree contains no such shape
  (scan rc=0). Recorded here as a known conservative behavior of the
  summary-based analyzer; the typed ownership analyzer (SOW-0026) is
  the planned cure. kimi's P3 (stale 355e log) is superseded by the
  record above: 355e ran the pre-correction binary, and the final
  verification (below) supersedes it.
- qwen: FAIL with one P2 - asserted the whitelist can never fire for
  the B5 shape; superseded by the miss-path fix above (whitelist now
  fires; verified on the mutation copy). qwen's P3 stands and is
  fixed: GATESCAN_CONFIGS could silently narrow the authoritative
  production scan from a polluted environment; check-import-graph.sh
  now unsets it before the scan.
- Remaining tracked unknowns from this round: shared *token.FileSet
  across parallel per-config scans (go/token does not document AddFile
  as thread-safe; not reproduced under -race; recorded in SOW-0026),
  and the type-parameter termination defect (SOW-0026).

Boundary corpus run (2026-08-20, final binary, v4/go root, 44 workers
at nice): 53 of 54 routine cases passed, including all seven B-forms
(B1-B3 direct minted-page exports rejected with the whitelist rule, B4
holder export accepted, B5 interface launder rejected with the whitelist
rule, B6 root export rejected, B7 root bounded export accepted) and the
launder/ownership P-form representatives. The one failure is the P49
channel round-trip case: it drives the PRE-EXISTING paramLeafPathsSeen
walk into a non-terminating recursion (see SOW-0026; the walk is
byte-identical to HEAD 2f2a975, so this is not a chunk regression). P49
is excluded from the routine boundary corpus with an explicit comment
and stays in the archived full battery as the regression pin for the
SOW-0026 fix; the corpus re-run after the exclusion is the final
evidence for this chunk.


### 2026-08-20 - M3 chunk-1 final re-run: walk termination fix, P49 restored, B8 added

- The final boundary re-run (54-case corpus, 44 workers at nice) hung
  twice on the SAME pre-existing paramLeafPathsSeen non-terminating
  walk (see SOW-0026): once on P49 (worker 26, 73+ min; SIGQUIT
  evidence in battery-chunk-355c.log) and once on routine cases 5 and 6
  in chunk 2 (os.ReadFile in a new package directory; unix.Readv
  descriptor read in the mapping owner; 37+ min; SIGQUIT in
  battery-boundary-final.log and -final2.log). All three dumps share
  one stack: pageflow.go paramLeafPathsSeen recursion over two struct
  classes with fresh object identities per revolution, so map-order
  randomness made the hang nondeterministic across runs.
- Fixed: paramLeafPathsSeen is now a per-struct memoized leaf walk
  (keyed on the dereferenced *types.Struct; an in-progress nil marker
  stops revisiting structs on cycles; a fail-closed leafWalkBudget of
  1<<20 panics leafWalkDivergence on exhaustion). scanRoot recovers
  the panic per OS config and marks that config failed closed.
  Previously-hanging cases now finish in seconds; P49 passes in ~20 s.
- P49 (previously excluded after the first hang) is restored to the
  routine boundary corpus; battery B8 was added (the type-parameter
  helper laundering a minted page - the generic form that originally
  diverged; it now terminates and fires the m.Page-on-unprovable-
  receiver rule). Boundary corpus: 55 cases. Full battery: 715 cases
  (589 fail forms, 126 benign forms).
- Final chunk-gate evidence (all at nice): full battery passed with the
  final binary; production import-graph scan passed rc=0 with
  GATESCAN_CONFIGS unset; go test ./..., go test -race ./..., go vet
  ./..., go-gate test, and go-gate vet all pass.

## Milestone 3 chunk 2 - joins and aggregation (design record, 2026-08-20)

Scope (from the M3 plan record): MembershipAggregation (modes,
FeedCardinality, FeedOverlap, sink, report), DirectJoin (source, budget,
cells, sink, report), MembershipCross (cells, UncoveredSide, report) -
read-only, allocation-free sinks, budgets enforced. The Rust authority
is v4/rust/iprange-livedb/src/membership_query/ (aggregation.rs,
selected.rs, decode.rs, cache.rs, scope.rs, join.rs, join/direct.rs,
join/membership.rs) and reader_core/cursor.rs (membership range
cursor). The Go chunk-1 reader (membership_query.go, direct_cursor.go,
cursor.go, membership.go) already provides the tree cursor, the
membership dictionary by ID (lookupMembershipID), MembershipView word
reads, and the bounded scope charges; chunk 2 adds the remaining
authoritative primitives.

Design decisions (2026-08-20, long-term-best per the working
principles):

1. Public API mirrors the Rust types with Go idioms: aggregation mode
   is one value (MembershipAggregationMode with unexported tag plus
   constructors Cardinalities/AllPairs/TargetAgainstScope/SelectedPairs);
   sinks are per-channel batch funcs (feedYield/overlapYield,
   cellYield/uncoveredYield) matching the ABI's separate per-channel
   sink callbacks; nil yields discard. Batched delivery uses one
   reusable fixed-size (32) batch buffer per operation, so sink
   delivery is allocation-free per result and the per-operation
   allocation count is constant in database size (Rust pins the same
   property by asserting equal allocation counts for small and large
   range counts).
2. Budget model stays the chunk-1 modeled-heap accounting: operation
   heap = scope maxHeap - heapUsed (Rust operation_heap); every Rust
   heap charge (entries, index map, totals, pair cells/offsets,
   scratch present/flags, sequence cache slots/positions, join result
   cells/slots and cross arrays) is charged with the exact Rust
   size_of values (Cardinality129=24, PairCell=40, Cell=40, Slot=24,
   cache Slot=24, usize=8, u32=4, u8=1), pinned by a test comparing
   the constants against Go unsafe.Sizeof. Budget failure reports
   CodeInsufficientResourceBudget with the exact Rust detail strings;
   result-cell overruns during a direct join report the same code
   (Rust BudgetExceeded) with detail "direct join result cells".
3. One authoritative low-level owner per primitive: the new
   internal/reader membership range cursors (MembershipRangeCursor4/6)
   reuse the existing treeCursor; the selected-ranges decoder
   (scratch + SequenceCache keyed by membership ID) and the
   SelectedRanges lookahead merger mirror Rust exactly, including the
   all-catalog lookahead disable. The direct join open-addressing
   Table and the membership join cross arrays are implemented once in
   internal/reader; the public facade only composes.
4. Error and corruption parity: WrongValueKind/WrongAddressFamily on
   source mismatch, NameNotFound for a target name absent from the
   catalog, InvalidArgument for a target outside the scope, empty or
   non-unique selected pairs, self-pairs; Corrupt for range-count
   disagreements and absent membership IDs (exact Rust messages).
5. Test-only necessary-work counters added to internal/work mirroring
   Rust work.rs for these paths: input_source_pass,
   membership_decode_cache_hit, membership_word_read,
   aggregation_contribution, aggregation_result, join_advance (all
   no-ops unless -tags v4work).
6. Validation strategy for cross-language parity without a Go catalog
   writer (chunk 4 owns CreateFeed/MembershipImport): semantic tests
   derive exact expected aggregation/join results from the
   language-neutral conformance manifest (cases.json membership_ranges
   + feeds arrays and direct_ranges) for the committed
   rust/membership-ipv4, rust/membership-ipv6 and rust/direct-ipv4
   fixtures; error/budget/cancellation/zero-alloc/work-counter tests
   pin operation behavior on the same fixtures. A Go catalog writer is
   explicitly out of scope for this chunk (tracked to chunk 4).

Chunk order and gate: M3 chunk-1 stays closed; this chunk builds on
it. Gate evidence at close: go test ./... (both tag sets), vet, race,
mmap-trace, gatescan linux scan rc=0, zero-alloc and work-counter pins,
level-2 review round (kimi/minimax/mimo/qwen).

### 2026-08-20 - M3 chunk-2 implementation record: joins and aggregation

- Implemented the chunk-2 surface under the Rust authority
  (v4/rust/iprange-livedb/src/membership_query/ - aggregation.rs,
  selected.rs, decode.rs, cache.rs, scope.rs, join.rs and
  reader_core/cursor.rs):
  - internal/reader: family range primitives and concrete iterators
    (range_ops.go), the selected-ranges lookahead merger
    (selected_ranges.go), membership scratch decoder and sequence cache
    (membership_scratch.go), one-pass aggregation with all four modes
    (aggregation.go), the direct join table and sweep (join_direct.go),
    and the membership cross/uncovered join (join_membership.go, with
    the membership_ranges.go sweep);
  - module root: MembershipAggregation* constructors, Aggregate,
    JoinDirect, JoinMembership with bounded batch sinks, budgets,
    reports, and boundary-owned string conversion
    (aggregation_public.go, join_public.go).
- P1 semantic fix found by the new chunk-2 model tests: the selected
  bitmap decoder extracted set bits with `word >>= 1` inside the bit
  loop, shifting every bit above position 5 down (bit 63 surfaced as
  58) and mis-mapping feeds. Fixed to exact Rust parity:
  bits.TrailingZeros64(word) plus word &= word - 1, with the u32
  feed-index overflow guard (membership_scratch.go decodeAll). The
  semantic and v4work pins now hold on rust/membership-ipv4 (70 feeds,
  2415 pairs, exact 384/256 centroids, 46 contributions, 2485 results).
- Gate-compliance refactor: the reader is the sync-free owner and may
  not import internal/tree, so the chunk-2 streaming code uses concrete
  iterators over the existing treeCursor (family switch on record
  decode) instead of interface streams; checkpoint state is threaded as
  a parameter (the s.check field is gone); feed/pair names travel as
  mapped []byte views and are converted to owned strings only at the
  module root, once per delivered record (the internal reader stays
  zero-allocation per result).
- Test-only observability: internal/work counters input_source_pass,
  membership_decode_cache_hit, membership_word_read,
  aggregation_contribution, aggregation_result, join_advance were added
  (no-ops without -tags v4work) and pinned by
  aggregation_join_v4work_test.go against the Rust work.rs model
  (passes 1/2/2, decodes 3/3/6, word reads 6/6/12, contributions
  46/44/14, results 2485/22/141, join advances 0/9/3).
- Validation: aggregation_join_test.go derives exact expected
  aggregation and join results from the language-neutral conformance
  manifest (cases.json membership_ranges/feeds/direct_ranges) for the
  committed rust fixtures; error/budget/cancellation pins cover the
  WrongValueKind/WrongAddressFamily/NameNotFound/InvalidArgument/corrupt
  and budget-exceeded paths; aggregation_join_zeroalloc_test.go pins no
  heap object >= 4096 bytes across 256 warmed aggregate/join runs.
  go test ./... and go test -tags v4work ./... and go vet ./... and
  go test -race ./... all pass at nice.
- Gate record: check-import-graph.sh passes rc=0 with GATESCAN_CONFIGS
  unset (boundary checks plus the full typed production scan). One
  analyzer tripwire artifact had to be worked around in production
  shape, not by weakening the gate: two sequential loops appending
  result structs carrying mapped name views into one shared batch
  variable made the flow pass aggregate-taint the destination after the
  first loop, so the second append reported "append into a complete
  mapped page view" (a false positive: the owned batch contains mapped
  views; no mapped page is copied). The uncovered emitter now uses one
  batch per side (join_membership.go emitUncoveredFeeds), the same
  one-batch-per-loop shape every other emitter uses. The analyzer
  conflation (aggregated-element taint vs. mapped destination) is
  tracked to the SOW-0026 type-aware ownership-analyzer rework.

### 2026-08-20 - M3 chunk-2 level-2 review round and zero-membership-ID fix

- Chunk-2 level-2 round over the working tree (adversarial, real-issues
  only): kimi PASS, minimax PASS (one P1-candidate / unchecked AllPairs
  index, resolved not-blocking), qwen PASS after one finding, glm and
  mimo unavailable (glm-5.3 backend rejects its grown context with
  "Prompt exceeds max length"; mimo's stream drops with transport
  errors, twice on short requests). Findings disposition:
  1. qwen P2 - "membership ID 0 short-circuits in Go, fails Corrupt in
     Rust": the Go scratch's fresh state uses membershipID 0 as its
     never-loaded marker, so the identity short-circuit returned nil for
     a corrupt range record naming membership 0 without consulting the
     dictionary; Rust's Scratch keeps Option<MembershipToken> (None
     until a real load) and membership_view.rs by_id refuses id 0 with
     Corrupt("range names the empty membership ID"). Fixed: the scratch
     now carries a loaded flag (clear resets it, both cache-hit and
     decode paths set it), so id 0 always reaches lookupMembershipID;
     the Go zero-id detail string was aligned to the exact Rust message.
     Regression test zero_membership_test.go patches the leftmost range
     record's value to 0 in a fixture copy and pins CodeFormatInvalid +
     the Rust message through AggregateScope (the same scratch serves
     the direct and membership joins). Verified the test fails without
     the fix.
  2. kimi P3 - pairCount's hand-rolled 64x64->128 multiply deserves a
     comment documenting the Rust u128 equivalence: added.
  3. minimax P1-candidate - AllPairs index arithmetic
     offsets[left] + right - left - 1 was unchecked while Rust uses
     checked_add and returns ArithmeticOverflow("membership pair
     index"): added the same bounds/overflow guard (code and detail
     string match Rust; unreachable before, now parity-exact).
- Final chunk-2 battery at nice on the reviewed tree: go test ./...,
  go test -tags v4work ./..., go vet ./..., go test -race ./... all
  pass; import-graph gate passes rc=0 (see the gate line below).

## Milestone 3 chunk 3 - algebra set operations and output (design record, 2026-08-20)

Scope (from the M3 plan record): FeedSelection, AlgebraSetOperation
(Union/Intersection/Exclusion), AlgebraOutputMode (PreserveFeeds/Flat),
AlgebraCountReport, AlgebraComparisonReport, AlgebraSetReport,
AlgebraSetResult, MembershipAlgebraBudget/AlgebraOutputBudget,
AlgebraSetOutcome/PreparationFailure, the materialized-result output
machinery shared with snapshot publication, and snapshot_to Immutable
(SnapshotBudget/SourceMode/PublicationPolicy/PreparationFailure). Live
snapshot mode stays in chunk 4.

The Rust authority: v4/rust/iprange-livedb/src/membership_query/algebra/
(selection.rs, analysis.rs, scan.rs, output.rs), membership_query/
algebra.rs, immutable_output.rs (ranges.rs, membership.rs, setup.rs),
membership_dictionary.rs, feed_catalog.rs, range_bulk.rs,
snapshot/{api.rs,build.rs,source.rs,terminal.rs} and
publication/workflow.rs. Four level-1 exploration passes (Peirce,
Sartre, Jason, Linnaeus, 2026-08-20) produced the capability inventory
and the exact semantics/error-string references this record summarizes.

Design decisions (2026-08-20, long-term-best):

1. Two implementation sub-rounds inside the chunk, each with its own
   gate evidence (one chunk, one close): 3a the read-side algebra
   (MembershipAlgebra construction, Selection, count, compare, the N-way
   event scan, work counters, semantic + v4work tests - all over the
   existing Rust fixtures, no writer dependency); 3b the output side
   (the Go one-shot immutable output builder with catalog name/index
   trees, membership dictionary with SHA-256 dedup and refcounts, used
   bitmap, range bulk builder, metadata chain, dual-meta finish; the
   publication attempt staging (identity, fail-if-exists vs
   ReplaceExisting/NoRollback policies); publish_set and snapshot_to
   Immutable; reopen-and-verify round-trip tests). 3b has no precedent
   fixture path in Go (membership-valued files cannot be produced yet),
   so 3b tests build, reopen, and re-scan their own outputs and
   cross-check semantic results against the Rust authority on the same
   inputs.
2. Public API mirrors the Rust shapes with Go idioms: MembershipAlgebra
   constructed from []*MembershipScope + MembershipAlgebraBudget
   (max_heap_bytes, max_sources); unexported-tag value types
   FeedSelection (All/Named constructors), AlgebraSetOperation
   (Union/Intersection/Exclusion), AlgebraOutputMode
   (PreserveFeeds/Flat); Count/Compare return the exact reports;
   PublishSet(destination, valueTag, operation, mode, metadataJSON,
   publicationPolicy, budget, cancellation) returns
   (AlgebraSetResult, error) with the PreparationFailure-typed error
   surface; SnapshotTo(sourcePath, mode, destinationPath, policy,
   budget, cancellation) mirrors snapshot/api.rs. Errors keep the
   Rust detail strings verbatim (the exploration record below lists
   them) and map to the Go published error classes.
3. The read-side scan is the Rust N-way event sweep, NOT the chunk-2
   two-source joins: per-source SelectedRanges (the chunk-2 Go mirror)
   feed Start/End events into one ordered queue (small linear for
   <= 4 sources, heap for larger), a GlobalState counts/present/slots
   triple with the exact Rust corrupt classes ("global feed source
   count underflow", "global feed presence slot is absent", "global
   feed presence slot disagrees"), emit_before/emit_terminal/
   finish_sources with the "membership algebra ... disagrees"/"range
   count disagrees"/"event queue ended early" checks, and the same
   4096-unit checkpoint cadence. Work counters: input_source_pass
   (== source count) and join_advance per event, matching Rust.
4. The output builder is a new internal/reader-adjacent owner: the
   one-shot append-only builder lives in internal/writer (it owns page
   allocation, checksums, meta publication; the reader stays read-only)
   following the immutable_output.rs state machine (require_active
   latch, reserve_page at the single authority, retire/discard
   refusals, page bounds and ownership checks after every update,
   seal-then-resize-then-dual-meta-then-flush-sync finish order).
   Catalog name/index codecs, membership id/hash codecs, and the used
   bitmap codec are implemented once over the existing tree core
   (tree.Codec), mirroring feed_catalog.rs and membership_dictionary.rs
   exactly (SHA-256 over little-endian words, hash-tree dedup,
   refcount deltas with the batch rule). The Go writer substrate
   (create, draft store, range edit, metadata chain, publication
   basics) is present and tested from milestone 2; the new code
   composes it, never reimplements it.
5. Publication staging: a minimal attempt layer in internal/writer
   matching publication/workflow.rs create/publish for the policies in
   scope (FailIfExists, ReplaceExisting, ReplaceExistingNoRollback):
   per-attempt private file, attempt identity (digest + dir identity),
   fail-if-exists rename and rollback-safe/plain replacement, discard
   cleanup with the ResiduePossible semantics carried by
   PreparationFailure. The dynamic mmap-only evidence (no
   read/write/seek on the database descriptor) and zero-alloc pins
   apply to the builder as they do to the reader; the builder itself
   deliberately performs file I/O only through the mapping
   (resize/ftruncate/msync/fsync), matching the v4 contract.
6. Cancellation lives at the caller layers exactly like Rust: every
   scan/selection/copy loop checks every 4096 work units; the builder
   has no token and fails closed on any prior error (WrongState
   "immutable output construction failed").
7. Validation: 3a semantic tests derive expected union/intersection/
   exclusion counts and comparisons from the language-neutral
   conformance manifest (cases.json coverage model, the same tables the
   chunk-2 tests use) across the committed Rust fixtures (multi-source
   combinations incl. duplicate global names and disjoint families);
   error/budget/cancellation pins cover every Rust string; v4work pins
   input_source_pass and join_advance. 3b round-trip tests build from
   algebra publish_set (PreserveFeeds and Flat) and snapshot_to
   Immutable from the Rust fixtures, reopen the outputs, and verify
   catalog, ranges, membership semantics, identity (fresh vs preserved
   per mode), metadata, and CRC/seal validity; budget refusal and
   policy behavior per Rust message; cross-language parity by scanning
   the same operation through the Rust authority outputs on the shared
   corpus.

Gate evidence at chunk close (both sub-rounds): go test ./... (both
tag sets), vet, race, import-graph gate rc=0, zero-alloc and v4work
pins for the new surfaces, level-2 review round (kimi/minimax/mimo/
qwen), records.

### 2026-08-20 - M3 chunk-3 level-1 exploration record

Four parallel read-only passes over the Rust authority and the Go
surface (level-1 agents, working tree 4389451) established the exact
port contract:

- Jason - algebra analysis/scan/selection: count/compare are one
  ordered pass per selection; the N-way event sweep (Start/End events,
  small linear for <= 4 sources else heap) consumes one
  SelectedRanges per source with expected-ranges agreement; GlobalState
  add/remove carry the exact Corrupt classes; checkpoint cadence 4096
  everywhere; work counters input_source_pass(source_count) and
  join_advance(1) per event; aggregation counters are NOT used on this
  path. Error strings verbatim: "membership algebra feed selection is
  empty"/"is not unique", "membership algebra has no sources",
  "membership algebra sources", "membership algebra source families
  differ", "membership algebra source heap"/"catalog heap"/"scan
  heap"/"event heap"/"selection heap", "membership algebra feeds",
  "membership algebra heap", "global feed source count" (overflow) /
  "global feed source count underflow", "global feed presence slot is
  absent"/"disagrees", "membership algebra boundary", "membership
  algebra segments", "membership algebra event source is invalid",
  "membership algebra event has no range", "membership algebra start
  event disagrees", "membership algebra end event disagrees",
  "membership algebra range count disagrees", "membership algebra has
  no terminal range", "membership algebra event queue ended early",
  "active membership algebra source has no range", "membership algebra
  source remained active", "membership algebra source range count",
  "global feed name disappeared", "membership algebra source
  disappeared"/"input disappeared", "membership algebra addresses".
- Linnaeus - output machinery and snapshot: output.rs publish order
  (validate budget, Prepared::new, workflow::create, build,
  workflow::publish, failure mapping), the immutable_output::Builder
  state machine (append-only pages, reserve_page single authority,
  feed catalog name+index trees, used bitmap Kind::Feed, membership
  dictionary SHA-256 dedup + refcount batches, range_bulk 6-level
  builder, metadata chain, finish = seal -> resize -> dual meta ->
  flush_range -> sync), the 13-item writer capability checklist, and
  the snapshot_to Immutable flow (identity PRESERVED from the source
  vs fresh identity for algebra output - a behavioral delta the Go
  port must mirror), reject_live_self, identity-mismatch refusal,
  per-iteration copy checkpoints, and the terminal failure shapes.
  Error strings verbatim: "membership algebra output pages"/"output
  files", "membership algebra output feeds", "membership algebra
  intersection is empty", "membership algebra output feed
  disappeared", "membership algebra selected empty output
  membership", "membership algebra output addresses"/"output range
  count", "membership algebra output heap", "membership algebra word
  range"/"bit range", "membership word count exceeds the feed-index
  limit", "membership bitmap is not canonical", "membership
  references an inactive feed", "snapshot output pages"/"snapshot
  open files", "snapshot metadata input heap", "metadata length
  changed while copying", "membership length changed while copying",
  "a live snapshot cannot replace its own source path", "source and
  snapshot output identities match", "immutable output file is not
  empty", "immutable output identity is invalid", "immutable output
  value and structure kinds do not match", "immutable output pages",
  "immutable output feed-index limit is invalid", "immutable output
  capacity"/"immutable construction extent"/"immutable construction
  extent is invalid", "immutable output construction failed",
  "immutable output attempted to retire an existing page"/"to
  discard an append-only page", "immutable output page is outside
  bounds", "immutable output page ownership is invalid", "feed index
  exceeds the preserved limit", "feed catalog exceeds its index
  limit", "immutable feed output requires a membership-capable value
  kind", "immutable output metadata is already set", "metadata
  exceeds 20 MiB", "metadata compression heap", "immutable output
  pages", "ordered output ranges are not canonical", "range start is
  after its end", "indirect range value is zero", "immutable range
  page is outside bounds", "empty membership reference batch stayed
  full", "rollback-safe replacement requires atomic name exchange",
  "snapshot publication is not implemented on this platform",
  "operating-system randomness returned an all-zero identity".
- Peirce - Go writer capability inventory: create/draft/range-edit/
  metadata/commit/reclaim complete and tested (direct databases);
  missing for chunk 3: catalog name/index write codecs, membership
  id/hash/used write codecs, the one-shot immutable output builder,
  the publication attempt staging, publish_set, snapshot_to. The Go
  tree core (tree.Codec + tree.Insert) is the substrate the missing
  codecs plug into; budget/error classes already align numerically
  with Rust.
- Sartre - Go reader composition map: the chunk-2 primitives
  (selectedRanges, scratch, operationHeap, membershipIterator,
  rangeOps, MembershipView word reads), ScopeData accessors, the
  JoinMembership multi-owner validation template, and the root-package
  facade pattern the algebra API will follow (membership_algebra_
  public.go), with the exact Rust public signatures to mirror.


### 2026-08-20 - M3 chunk-3a implementation record: read-side algebra

- Implemented the chunk-3a surface under the Rust authority
  (v4/rust/iprange-livedb/src/membership_query/algebra.rs and
  algebra/{selection,analysis,scan}.rs):
  - internal/reader/algebra.go: MembershipAlgebra construction with the
    exact Rust ordering and labels (require_source_count, source-heap
    admission charge, inspect_sources family agreement with the 4096
    checkpoint cadence, collect_names sort/dedup under the catalog
    charge, build_inputs local-to-global maps under the input-state
    charges); FeedSelection All/Named constructors;
    resolveAlgebraSelection (empty/unique/NameNotFound rules, sorted
    positions, presence flags, selection-heap labels);
    AlgebraCountReport/AlgebraComparisonReport; the one ordered N-way
    event sweep (algebra/scan.rs parity: per-source SelectedRanges over
    the chunk-2 membership cursors, Start/End event queue with the
    linear <= 4 / heap ordering and the End-before-Start tie-break,
    GlobalState counts/slots/present with swap-remove slot repair,
    emit_before with the "membership algebra boundary" overflow label,
    apply_boundary same-at grouping, emit_terminal with the "event
    queue ended early" check, finish_sources with the range-count
    agreement); the modeled-heap charges mirror the Rust size_of labels
    ("membership algebra source heap"/"catalog heap"/"scan heap"/
    "event heap"/"selection heap"/"membership algebra feeds"/"heap").
  - module root membership_algebra_public.go: MembershipAlgebraBudget,
    FeedSelection with AlgebraFeedSelectionAll/Named constructors,
    NewMembershipAlgebra (per-scope open check, reader conversion),
    Count, Compare, FeedCount, Feeds (owned strings with the global
    catalog position), AddressFamily, and the boundary name validation
    (Rust FeedName::new parity: invalid spellings fail NameInvalid
    before resolution).
- Work counters: input_source_pass(source_count) and join_advance(1)
  per event, matching Rust work.rs; the aggregation counters are not
  touched on this path (Rust parity). Pins:
  - membership_algebra_v4work_test.go: two all-catalog sources over
    rust/membership-ipv4 - passes 2, join advances 12 (three fixture
    rows x two sources x Start+End, the rows do not reach the family
    maximum), decodes 6, decodes+hits == source_range_count (6),
    segments 3; the named feed-001 selection merges the two adjacent
    rows into one run per source (4 join advances) while still reading
    every physical range (6 decodes, 6 source ranges).
- Semantic tests (membership_algebra_test.go) derive every expected
  cardinality from the language-neutral conformance manifest
  (cases.json membership_ranges through the chunk-2 interval model):
  v4 two-source compounds (All = 512 with Exact segments/ranges,
  per-feed unions 384/256/256/384/0 and set unions, compare buckets
  all/all, all/named, named/named including the shared-middle-row
  overlap of 128 and empty-side cases) and v6 two-source compounds
  (All = 2^128, global vs special with the exact 65536-only row).
  Error pins cover every Rust string: no sources, max sources, family
  disagreement, empty/duplicate selection, NameNotFound, invalid
  spelling, source-heap exhaustion, scan-heap exhaustion after a
  tight-but-buildable construction, cancellation, and the closed-reader
  guard.
- Zero-alloc evidence: membership_algebra_zeroalloc_test.go pins no
  heap object >= 4096 bytes across 256 warmed Count/Compare runs
  (All and Named selections).
- Known working-theory item (budget calibration): the modeled Rust
  struct sizes (Source 224, SourceState 1664/1720, Event 16/32,
  FeedName 256, InputState 32) are provisional estimates and are NOT
  exercised by any admission-equality test in this sub-round; the
  budget tests use wide margins (1 << 20 and the 19000-byte
  tight-but-buildable construction). Calibrating every constant with a
  Rust size_of probe (tests.rs small/large allocation-equality shape)
  is tracked to chunk 3b, where the output builder must admit
  identically to Rust.
- Gate note: the import-graph gate passes rc=0 on the reviewed tree
  (full record below). Battery at nice: go test ./...,
  go test -tags v4work ./..., go vet ./..., go test -race ./... all
  pass.
- Gate fix round (2026-08-20, same sub-round): the first chunk-3a
  implementation failed the content-transfer gate on five shape
  families. The code was restructured to pass the ownership gate
  honestly (no gate exemptions were added):
  1. the algebraSegmentSink interface dispatch was replaced by a single
     concrete algebraSink struct (Go has no traits; nil right selects
     the count mode, a set right the comparison mode) so every segment
     call is a provably scanned concrete receiver;
  2. the internal FeedSelection.names changed [][]byte -> []string
     (Rust &str parity) and resolveAlgebraSelection resolves each name
     through sort.Search over the struct-carrier catalog, removing the
     byte-container formal loop that the gate treats as a full-page
     source;
  3. the catalog sort/dedup moved into dedupAlgebraNames(names
     []FeedEntry) - a struct-element container parameter, which the
     gate keeps bounded - so the gathered local never appends
     page-tainted elements into an owned slice;
  4. the public Feeds() facade reads the catalog through the one-hop
     pointer accessor State().Names() and converts string(entry.Name)
     field-by-field, exactly the gate-accepted MembershipScope.Feeds
     shape (the previous two-hop method result was unprovable).
  Evidence: the full ./check-import-graph.sh (five OS configs) passes
  rc=0 with "import-graph check passed", and a final
  GATESCAN_CONFIGS=linux gatescan-fix . on the exact committed tree
  returns rc=0 with zero violations (clean runs print no violation
  lines, exactly like the earlier clean linux-only runs).
- Dead-code clean-up (same sub-round): the Rust-parity
  algebraSelection.allPresent helper had no caller in this sub-round
  (the output round needs it in chunk 3b); per the no-dead-code rule it
  was removed and will return with its callers in 3b.
- Level-2 verdicts on the first tree version (before the gate fix
  round): kimi FAIL (the gate violations above, now fixed), minimax
  PASS (P2-1 gate false positives, now restructured away without
  exemptions; P2-2 gate-claim record, corrected above), mimo PASS (P3
  dead code, removed). kimi's delta re-review of the fixed tree was
  requested and is pending model availability; qwen and glm did not
  respond to the delta (known-flaky models).


### 2026-08-20 - M3 chunk-3b exploration record: output machinery (five level-1 passes, working tree 49948e0)

Five read-only passes (Jason, Linnaeus, Peirce, Sartre, Leibniz) over
the Rust authority and the Go writer substrate established the exact
chunk-3b contract. Consolidated facts:

- OUTPUT BUILDER (Jason): the one-shot append-only Builder state
  machine (immutable_output.rs) with OutputSpec{family, value_kind,
  structure_kind, value_tag, database_id, commit_nonce, txn_id=1,
  feed_index_limit}, fresh() random identity, NewFailure/FinishFailure
  wrappers, the require_active latch ("immutable output construction
  failed"), reserve_page single authority (page_count == 2^32 ->
  PageSpaceExhausted, >= max_output_pages -> BudgetExceeded "immutable
  output pages", page 0/1 are the meta pair, pages 2.. are data), the
  append-only Store (discard_private -> Corrupt "immutable output
  attempted to discard an append-only page", retire non-empty ->
  Corrupt "immutable output attempted to retire an existing page"),
  post-update ownership check ("immutable output page ownership is
  invalid"), seal_pages (ownership + CRC32C per data page), exact
  finish order (flush structure refs -> flush membership refs ->
  ranges.finish -> seal -> resize to page_count*PAGE_SIZE -> dual meta
  encode inside one probe -> flush_range(0, bytes) -> sync_file), and
  the full verbatim error-string table with classes and locations.
- RANGE BULK BUILDER (Jason/Leibniz): range_bulk.rs 6-level bottom-up
  builder (BRANCH_LEVELS=6, leaf level 0 aux=family, branch level
  level_index+1, only_child collapse, finish -> (root, record_count),
  work counters page_created/range_emitted/output_pass, can_append
  rules "ordered output ranges are not canonical"/"range start is
  after its end"/"indirect range value is zero", merge adjacent
  same-value, "range branch cell does not fit", "range record does not
  fit an empty leaf", depth overflow PageSpaceExhausted).
- CATALOG CODECS (Linnaeus/Peirce): name tree (types 3/4) and index
  tree (5/6) share one 12+name_len record layout (u16 len @0, reserved
  @2, u32 feed index @4, u8 name len @8, 3 reserved @9, name @12);
  name branches are full records with the child in the index slot
  (write_branch/read_branch_child overrides); index branches fixed 8B
  (u32 first_index + u32 child); KeySize 0/4 respectively; insert
  into name then index tree with "feed name already exists"/"feed
  index already exists" Corrupt classes and work::catalog_intern.
- MEMBERSHIP DICTIONARY (Linnaeus/Leibniz): State{id_root, hash_root,
  used_root, entry_count, id_limit}; ID tree (7/8): key u32, leaf
  record fixed head 64B (len u16, storage u8 inline/blob, id u32,
  refcount u64, word_count u32, bitmap_len u32, blob_root u32, 4
  reserved, SHA-256[32]) + inline words to 512B (56 words) or blob
  tree; HASH tree (9/10): 40B key (digest[32], wc u32, id u32), 40B
  leaf, 44B branch; SHA-256 over LE-u64 words in <=64-word chunks;
  intern = require_word_count -> digest -> hash-tree at_or_after +
  equal_words -> insert_new (allocate lowest free id via used bitmap
  Kind::Membership, encode inline/blob, insert id then hash tree);
  refcount deltas applied in place at REFCOUNT_OFFSET with the
  ReferenceBatch (entries<=1024, add/Full flush/retry, Direct apply,
  "empty membership reference batch stayed full"); error strings
  verbatim (membership dictionary record is malformed, membership
  refcount, membership entry count, ID namespace limit is zero,
  membership used bit is missing, membership word count is outside
  the v4 limit, hash/ID record decode classes).
- ALGEBRA OUTPUT (Linnaeus): publish pipeline validate_budget ->
  Prepared::new -> workflow::create -> build -> workflow::publish;
  AlgebraOutputBudget (max_output_pages >= 2, required files 2/3 ->
  "membership algebra output pages"/"membership algebra output
  files"); build_mapped PreserveFeeds (push_feed per catalog global,
  output index == position) vs Flat (one feed index 0, bitmaps
  single-bit); global_to_output map; OutputSink::segment qualify /
  collect positions (u32::MAX -> "membership algebra output feed
  disappeared") / coalesce / flush with PositionWords -> intern ->
  push_interned_membership; SequenceCache dedup with
  membership_intern_cache_hit work counter; output feed count gates
  ("membership algebra output feeds"); per-4096 cancellation
  checkpoints.
- PUBLICATION (Sartre/Linnaeus): workflow::create (ReplaceExisting
  requires atomic name exchange -> DurabilityUnsupported "rollback-safe
  replacement requires atomic name exchange"; FailIfExists ->
  create_absent), secure() binds private file identity (dev+inode) to
  the destination directory identity, prepare_cancellable (custody
  verify, sha512 digest, finish sync), publish per policy
  (fail_if_exists_cancellable / replacement bind / bind_no_rollback),
  main-file rename_noreplace/exchange/plain + dir sync + proof +
  retirement, discard_attempt cleanup with Point checkpoints, and the
  snapshot-specific identity rule "source and snapshot output
  identities match" (not applied by algebra publish). Go gate note:
  x/sys is mapping-owned only, so renameat2/exchange/dir-sync live in
  internal/mapping like every other syscall surface.
- SNAPSHOT_TO (Sartre): snapshot/api.rs entry (Live gate -> chunk 4),
  budget.validate ("snapshot output pages"/"snapshot open files"),
  open_source + reject_live_self (Live only) + identity rule, copy
  loop build.rs copy_logical (feeds, per-family range cursors incl.
  structured threat_membership, metadata last with
  "snapshot metadata input heap"/"metadata length changed while
  copying"/"membership length changed while copying"), per-iteration
  cancellation, finish order identical to the output builder;
  SnapshotPreparationFailure shapes (early/new/discarded/
  from_publication) with Clean vs ResiduePossible cleanup_state.
- GO SUBSTRATE GAPS (Peirce): missing = catalog name/index write
  codecs, membership id/hash/used write codecs, the one-shot output
  builder, publication attempt staging, publish_set, snapshot_to;
  present = draft store + tree.Insert (fixed-range only), metadata
  chain writer, dual-meta encode, bitmap SetUsed/AllocateLowestID/
  ShrinkMembership, publication.go in-place commit, range codecs,
  reader side of catalog/membership (bespoke walkers).
- CRITICAL DESIGN FINDING (Peirce, verified by the lead): the Go tree
  core (tree.Codec) requires fixed KeySize/LeafSize and a two-limb
  Key{Hi,Lo}; catalog name keys (up to 255B), membership hash keys
  (40B), and all variable-length leaf records (name records 12+len,
  ID records 64+bitmap) CANNOT be inserted through tree.Insert today.
  Rust mirrors this with KEY_SIZE=0/LEAF_SIZE=0 codecs + overridable
  leaf_cell/branch_cell/write_branch/read_branch_child + a generic Key
  type; Go must gain the same variable-record/variable-key capability
  in the tree core (the page machinery - splitBySize, lowerBoundBy,
  variable truncate, codecCell - already exists and is byte-identical
  to Rust; only requireCodec, the cell readers, and the key
  abstraction need the variable branch). The Go reader never uses the
  tree core for these trees (bespoke walkers), so the extension is
  writer-side only and cannot regress reader hot paths.

Design decisions (2026-08-20, long-term-best; extends the chunk-3
design record decisions with the exploration finding):

A. Extend the Go tree core Rust-shaped instead of building dedicated
   page assemblers: one authoritative tree machinery for every v4
   tree, exactly like fixed_tree. The Codec gains an optional
   variable mode (KeySize()==0/LeafSize()==0 allowed when the codec
   provides explicit max sizes and cell/branch accessors); searches
   route through lowerBoundBy/codecCell (already present) with
   codec-provided cell readers and byte-key comparison (bytes.Compare
   for names; digest->word_count->id for hash keys, mirroring Rust
   derived Ord). The fixed-size path (range/retirement trees) stays
   byte-identical. Member: range_bulk is a separate bottom-up builder
   exactly as in Rust (it is not tree-core insert).
B. The output builder is one Go owner in internal/writer mirroring
   immutable_output.rs: append-only Store over a fresh mapping
   (allocate=reserve_page, update=page_mut+ownership, copy for
   splits, discard/retire refusals), reserve_page at meta.page_count,
   the mutate latch, and the exact finish order. It reuses
   writer/metadata.go (write chain) and format.Meta.EncodeMapped for
   the dual meta; page sealing reuses format.SealPageChecksum.
C. Publication staging is minimal-complete per the chunk-3 design
   record: attempt file naming (.iprange-publish-<hex>.tmp), attempt
   identity (dir identity device+inode via stat + basename), sha512
   digest over the finished mapping bytes read through mapped views,
   fail-if-exists rename, exchange/plain replacement, discard cleanup
   with ResiduePossible carried by the PreparationFailure error type.
   All new syscalls (renameat2 RENAME_NOREPLACE/RENAME_EXCHANGE,
   dir sync, stat identity) are added to internal/mapping - the gate's
   syscall owner - as small exported primitives; no gate exemption.
D. publish_set mirrors algebra/output.rs: validate budget -> prepare
   -> create attempt -> build_mapped (feeds, global map, the N-way
   scan with OutputSink segment walk, intern + push_interned, cache)
   -> finish -> publish; error mapping to the Go PreparationFailure
   surface with Rust detail strings verbatim.
E. snapshot_to Immutable mirrors snapshot/{api,build,source,terminal}:
   source guard over an existing immutable path, identity rules,
   per-iteration copy loop with per-4096 cancellation, metadata last,
   publication policies; Live mode stays chunk 4.
F. Budget calibration moves into 3b: the Rust size_of probes for the
   modeled heap charges (Source/SourceState/Event/FeedName/InputState/
   output vectors and the reference-batch slots) are pinned by a Rust
   test and mirrored by Go admission-equality tests, closing the 3a
   working-theory item.
G. Sub-rounds inside chunk 3b, each closing its own gate evidence:
   3b-1 tree-core variable codecs + catalog/membership write codecs +
   range_bulk + the output builder with reopen-verify round trips;
   3b-2 publication staging + publish_set + snapshot_to + public
   surface + budget calibration + final gates. Level-1 reviewers are
   re-aimed: Jason output-builder vs Rust, Linnaeus codecs/wire
   parity, Peirce publication/identity, Sartre mmap-only/lifetimes,
   Leibniz records; level-2 stays kimi/minimax/mimo/qwen.

### 2026-08-20 - M3 chunk-3b-1 implementation record: tree variable
codecs + catalog/membership write codecs + range_bulk + output builder

Implemented the complete 3b-1 surface under the Rust authority, with
reopen-verify round trips through the public immutable reader:

- Tree-core variable extension (internal/tree, decisions A + G):
  Key gains byte-key support (VarKey constructor; Less/Equal compare
  the byte keys when either side is variable); the optional
  VariableCodec interface (MaxBranchCell/MaxLeafCell,
  LeafRecordBounds/BranchRecordBounds, WriteBranch, ReadBranchChild)
  routes the cell readers, the branch-cell writer, and the max-size
  checks when KeySize()==0/LeafSize()==0, mirroring the Rust
  fixed_tree.rs overridables; leaf and branch record reads go through
  the concrete format.SlottedRecord helper with the codec's integer
  length bounds (no full-page interface dispatch in the tree core); MutateLeafU64 (fixed_tree::mutate_leaf_u64) with the
  private-path select, exact lowerBound, in-place u64 field write at
  the exact cell offset, or record delete; format.SlottedRecord
  (slotted_page::record with the length envelope check) and
  format.SlottedAppender (slotted_page::Appender try_push/finish).
  The fixed-size path is byte-identical; the reader never uses these
  trees through the tree core (bespoke walkers), so reader hot paths
  are untouched.
- Work counters: CatalogIntern, OutputPass, MembershipInternCacheHit,
  MembershipLookup, MembershipIntern, MembershipRefcountBatch in both
  build variants, exactly matching Rust work.rs names.
- Catalog write codecs (internal/writer/catalog_codec.go): the shared
  12+name_len record wire (u16 len @0, u16 zero @2, u32 index @4, u8
  name len @8, three zero bytes @9, name @12); nameCodec (types 3/4,
  KeySize 0, full-record branch cells with the child in the index
  slot, WriteBranch pads the full record) and indexCodec (types 5/6,
  KeySize 4, fixed 8-byte branches); insertCatalogEntry inserts name
  then index, retires through the store, counts CatalogIntern, and
  reports the exact Rust strings "feed name already exists"/"feed
  index already exists".
- Membership dictionary write codecs (membership_codec.go): the ID
  tree (types 7/8, fixed u32 keys, variable leaf records: 64-byte
  head + inline words to the 512-byte record limit, or blob head) and
  the hash tree (types 9/10, fixed 40-byte keys/leaves, 44-byte
  branches); the hash key is the raw record bytes (digest, word
  count, id) mirroring the Rust derived Ord; digestWords runs SHA-256
  over LE-u64 words in <=64-word chunks (Rust hash_words).
- Membership dictionary (membership_dictionary.go): intern ->
  find_equal (hash-tree AtOrAfter + located word-for-word compare,
  counting lookups exactly as Rust: find_equal does NOT count,
  record::find and apply_delta do) -> insert_new (bitmap
  AllocateLowestID KindMembership, encode inline/blob, insert ID then
  hash tree with their own codecs, entry_count++);
  applyMembershipDelta with in-place refcount mutation and
  deletion-on-zero; finishMembershipRemoval (hash delete, blob
  release, ClearUsed, ShrinkMembership); membershipReferenceBatch
  (power-of-two <=1024 slots, linear probing, Added/Direct/Full,
  flush with the exact "empty membership reference batch stayed full"
  string); readMembershipWords through the located record (Rust
  record::Found), one counted lookup per read not per chunk.
- Blob tree (membership_blob.go): bottom-up 5-level builder,
  4048-byte payload leaves (aux 1, leaf data at 48, start u64 @32,
  length u16 @40), 16-byte branch records (offset u64 @0, child u32
  @8, reserved @12), only-child collapse, BRANCH_ITEMS=226,
  writeMembershipBlobLeaf with 64-word chunks, release walk with
  geometry/coverage checks, readMembershipBlobWords /
  findMembershipBlobLeaf with the exact expected-level plumbing.
  Initialization bug fixed during testing: initializeMembershipBlob-
  Leaf wrote the tail fields but not the page magic (Rust
  blob_tree::initialize_leaf calls page_header::initialize first);
  now uses format.InitializePageHeader.
- Range bulk builder (range_bulk.go): the exact range_bulk.rs
  6-level bottom-up builder with only-child collapse, PackedPage
  (no heap pointers), tryPush/canAppend, pushNode, finish/finishLevel
  and the verbatim error strings ("ordered output ranges are not
  canonical", "range start is after its end", "indirect range value
  is zero", "range record does not fit an empty leaf", "range branch
  cell does not fit", PageSpaceExhausted); v4/v6 record and branch
  cell encoders over format.SlottedAppender.
- Output builder (output.go): OutputSpec/FreshOutputSpec, OutputBudget
  (extent = budget x 4096 via mapping.Create O_EXCL), the mutable
  latch ("immutable output construction failed"), PushFeed /
  PushDirectV4/V6 / PushMembershipV4/V6 / PushInternedMembershipV4/V6
  / InternMembership with checkedOutputWords (every supplied word vs
  the feed used bitmap: "membership references an inactive feed") and
  requireOutputShape (word count vs the feed-index limit, canonical
  final word: "membership bitmap is not canonical"); the append-only
  Store (reserve_page PageSpaceExhausted/BudgetExceeded
  "immutable output pages", post-update page ownership check
  "immutable output page ownership is invalid" against the 4-byte page
  magic + born txn, RetirePages/DiscardPrivate refusals); Finish in
  the exact Rust order (membership refs flush, ranges.finish, seal
  pages with ownership + CRC32C, shrink to page_count*4096, dual meta,
  FlushRange, SyncFile). Ownership-magic bug fixed during testing:
  outputPageOwned compared 8 bytes against MainMagic; the page header
  magic is the 4-byte PageMagic (Rust page_header::owned_by).
- Reopen-verify round trips (output_test.go): direct v4 (identity,
  dual meta pair, CRC seals on every data page, range scans, lookups),
  full-space v6, empty direct/membership state, 2000-record
  multi-level direct, the v6 branch-overflow one-child right edge
  (leafCapacity*branchCapacity+1 = 19505 records), sparse 501-word
  membership with the three-feed catalog (active feeds, entry count 2,
  id limit 3, dedup bitmap reuse), 512-interned-reference run, budget
  refusal, malformed-order/permanent-poison, inactive-bit and
  trailing-zero-word refusals, leaf rollover, store guardrails, and
  existing-path refusal. Work pins (output_work_test.go, v4work):
  the reference run counts exactly 1 refcount batch, 1 membership
  lookup, 512 emitted ranges, 1 output pass, 1 catalog intern; the
  membership run counts 3 interns and 3 lookups (one refcount apply
  per range).
- Gate status (routine gate per the 2026-08-19 user decision: the
  heavy mutation battery is archived and runs only on request; the
  routine chunk gate is the boundary scan plus the dynamic mmap-only
  evidence): go test ./... and go test -tags v4work ./... pass, go
  vet clean, go test -race clean on writer+tree, gofmt clean, the
  typed gatescan passes on all five OS configs, the full
  check-import-graph.sh boundary gate passes (per-package import
  boundaries, content-transfer scan, module-graph pins, per-target
  import boundaries, the reader sync-free zone), the mmap-trace
  evidence shows no read/pread64/readv/write/lseek on any database
  descriptor, and the MemStats size-class assertion holds (no heap
  allocation >= 4096 bytes during lookups). Gate tooling grew with
  the codec surface: VariableCodec joined approvedModuleInterfaces
  (its dispatched methods carry only partial records/keys; leaf/
  branch reads go through the concrete format.SlottedRecord with the
  codec's integer bounds), and the codec key copies plus the sha256
  digester .Write are exempted as exact file-scoped shapes (a
  tripwire for the writer's bounded key cells; the exemption is
  shape- and binding-keyed and stays fail-closed for any other
  copy).

### 2026-08-20 - M3 chunk-3b-2 slice 1 implementation record:
publication namespace primitives

First 3b-2 slice (decision C foundation): the atomic name-exchange,
directory-sync, and identity primitives the publication staging layer
composes. All new syscalls live in internal/mapping (the gate's
syscall owner; no exemption): RenameNoReplace (linux renameat2
RENAME_NOREPLACE, darwin renameatx_np RENAME_EXCL), RenameExchange
(linux RENAME_EXCHANGE, darwin RENAME_SWAP), SyncDirectory (fsync on
the directory descriptor, EINVAL -> CodeOSUnsupported mirroring the
Rust sync_all mapping), StatIdentity (device+inode, the Rust
Identity), and the exchangeAvailable() probe (linux+apple true,
elsewhere false, mirroring Rust require_exchange_available). FreeBSD/
NetBSD/Windows refuse the atomic renames with CodeOSUnsupported (no
primitive in the pinned x/sys v0.35.0 surface); their dir sync and
identity stay real on the POSIX pair. Tests (mapping_publish_test.go)
pin no-replace refusal over an existing destination (destination
untouched, source preserved), the atomic exchange, directory sync,
and identity stability. Gate: go test ./... both tag sets, vet,
gofmt, per-OS cross-builds (linux/darwin/freebsd/netbsd/windows), and
the linux gatescan all pass.

Follow-up slice-1 addition (2026-08-20): RenamePlain (rename(2), the
Rust bind_no_rollback path) and Unlink (attempt-cleanup path) added to
the same four platform files. FreeBSD/NetBSD keep real rename(2)+unlink;
Windows refuses both with CodeOSUnsupported to keep the writer's
no-rollback path explicit. Same gate.

### 2026-08-20 - M3 chunk-3b-2 slice 2 implementation record:
publication staging layer

Second 3b-2 slice (decision C): the staging layer a publish_set /
snapshot_to compose. Two parts:

- mapping primitives upgraded to the Rust errno classification
  (namespace_mutation.rs rename_result + problem.rs, verbatim detail):
  rename_noreplace EEXIST -> NameExists "publication name already
  exists"; exchange/plain ENOENT -> NameNotFound "publication name is
  missing"; EINVAL/ENOSYS/EOPNOTSUPP -> DurabilityUnsupported
  "filesystem lacks required durable namespace operations"; every other
  errno -> CodeIo with the Rust operation detail ("publish name without
  replacement", "atomically exchange publication names", "atomically
  replace and discard publication destination", "unlink exact file",
  "synchronize retained directory", "publication filesystem operation
  failed"). SyncDirectory EINVAL re-mapped to DurabilityUnsupported
  (was CodeOSUnsupported) per problem.rs; exchangeAvailable() exported
  as ExchangeAvailable().
- internal/writer/publication_staging.go (411 lines): PublicationPolicy
  (FailIfExists/ReplaceExisting/ReplaceExistingNoRollback), CleanupState
  (Clean/ResiduePossible), PublicationStatus, DestinationContent,
  PublicationPreparationFailure (Cause + Cleanup, Error/Unwrap),
  PublicationResult, and OutputAttempt with the decision-C attempt
  identity: destination path, captured directory device+inode, nonzero
  128-bit attempt id hex-encoded lowercase (artifact_name.rs
  write_attempt), name .iprange-publish-<32hex>.tmp. CreateAttempt
  mirrors workflow::create: policy validation, ReplaceExisting requires
  ExchangeAvailable else DurabilityUnsupported "rollback-safe
  replacement requires atomic name exchange", invalid destination name
  "invalid destination name", missing parent "publication name is
  missing", non-directory parent "destination parent is not a
  directory", FailIfExists refuses an existing slot "publication name
  already exists". Discard mirrors cleanup::discard_attempt: exact-name
  Lstat + Unlink + retained-dir sync; Clean only when the name provably
  no longer exists, else ResiduePossible. Publish mirrors
  workflow::publish: unfinished-builder guard, custody verify (dir
  identity "publication inode identity changed", missing attempt
  "publication name is missing", symlink "publication name is a
  symlink", non-regular "publication name is not a regular file",
  cross-filesystem "publication inode is on another filesystem",
  replacement same-identity "replacement source and destination
  identities match", replacement non-regular destination), SHA-512
  digest over the finished mapping through constant 1024-byte mapped
  views only (output_digest.rs DIGEST_BUFFER_SIZE, page-aligned files
  make the constant chunk exact; no read()/copy of full pages), finish
  sync + length recheck "finished output length changed", the policy
  rename, retained-dir sync, and the post-rename identity proof.
  Failure classification mirrors attempt.rs: pre-rename failures return
  *PublicationPreparationFailure with the discard cleanup state;
  rename refusals (EEXIST/ENOENT) and unprovable post-rename outcomes
  return Ok(PublicationResult) with Cause, NotPublished /
  OutcomeUnknown, and the discard cleanup state. The attempt file is
  created by the output builder (mapping.Create O_EXCL over the random
  name), so every create failure is early with nothing to clean; the
  writer calls no syscalls (all primitives in internal/mapping).
- Gate: findExemptions digest-branch generalized to cover
  publication_staging.go with the stdlib sha512 hasher (same
  binding-keyed exact shape as the sha256 digestWords exemption); the
  attempt-hex encoder writes only into a local fixed array (the
  element-wise page-sourcing rule treats byte-array params as
  potentially page-carrying, so a range-over-param + caller-slice
  destination shape fails closed - the used shape is the codes.go
  indexed-local-array pattern).
- Tests: mapping_publish_test.go pins the errno classification and
  RenamePlain/Unlink; publication_staging_test.go (538 lines) pins
  naming/validation, FailIfExists publish + create-time and rename-time
  refusal (destination untouched), exchange replacement and
  missing-destination, plain replacement, same-identity refusal,
  non-regular replacement destination, missing attempt, unfinished
  builder, discard Clean/ResiduePossible states (read-only directory),
  residue carried by refused publish, and the Rust-verbatim error
  surface. Gate: go test ./... both tag sets, vet, race, gofmt,
  cross-builds (linux/darwin/freebsd/netbsd/windows, darwin v4work),
  and the full check-import-graph.sh all pass.

### 2026-08-20 - M3 chunk-3b-2 slice 3 implementation record:
publish_set

Third 3b-2 slice (decision D): publish_set, the materialized algebra
set output published as its own immutable v4 file. Architecture (the
reader/writer import boundary forces the shape): the output sink is
reader-owned machinery (selection, scan, cache, positions, address
accounting) reached through the existing concrete algebraSink - the
single-sink pattern from 3a, extended with one output mode whose
writer calls travel as function values (the rangeOps adapter
pattern); the one-shot builder stays writer-owned and is published
through the slice-2 staging layer; the module root composes the
pipeline exactly like Rust algebra/output.rs publish (validate
budget -> prepare -> workflow::create -> build -> workflow::publish)
with the AlgebraPreparationFailure error surface (early / discarded /
from_publication shapes collapsed onto Cause + Cleanup at the Go
boundary).

- reader/algebra_output.go: AlgebraSetOperation (Union/Intersection/
  Exclusion with the "membership algebra intersection is empty" rule),
  AlgebraOutputMode (PreserveFeeds/Flat), AlgebraOutputPrepared
  (operation heap, resolved plan, catalog positions, output feed
  count with the "membership algebra output feeds" gate), the
  algebraOutputPlan (union-any / intersection-all / exclusion
  qualify, fill_output with the u32::MAX "membership algebra output
  feed disappeared" corrupt class and the intersection-ordered /
  present-sorted current vector), the concrete function-value writer
  bridge (Feed/Intern/PushV4/PushV6 as separate func-typed formals of
  BuildAlgebraOutput: the gate callback fence requires the writer
  method values at the call site rather than a hooks struct),
  BuildAlgebraOutput (feed pushes with per-4096 cancellation, the
  global-to-output map, output-heap vectors, the family-dispatched
  sweep into the output sink, sink finish), and the output sink
  itself: per-segment qualify / coalesce / flush with PositionWords
  (hasPending consumed at flush like the Rust pending.take(), so no
  non-qualifying segment can flush an empty pending),
  the sequence-keyed cache (reader cache.rs parity) with
  membership_intern_cache_hit work counting, "membership algebra
  selected empty output membership" and "algebra output membership
  cache is empty" corrupt classes, and the "membership algebra output
  addresses"/"output range count" overflow labels. Three selection
  methods return with their 3b callers: all (the 3a-recorded
  allPresent), forEachPosition, forEachPresent.
- reader/membership_scratch.go: sequence-keyed cache surfaces
  (sequenceValue/insertSequence with the Rust hash_sequence and
  sequences_equal 4096-unit checkpoints) joined to the existing
  keyed surface.
- writer: OutputBuilder.WriteMetadata (metadata.go, under the
  existing metadata-file exemption: metadataCompress with the caller
  heap budget, the forward chain over tree.Store, "immutable output
  metadata is already set"), metadataWriteChain/metadataCollectPages
  widened from *DraftStore to tree.Store (approved dispatch; the
  DraftStore call sites are unchanged), the builder metadataStaged
  latch, and CreateAttempt gained the Rust path::validate_main_name
  reserved-name rule (the .iprange- prefix and the .readers suffix
  refuse with "invalid destination name"; the publish_set boundary
  pins it). No new exemption: the hasher/buffer shapes
  are the already-exempted metadata.go ones.
- module root: public AlgebraSetOperation/AlgebraOutputMode
  constructors, AlgebraOutputBudget, AlgebraSetReport/AlgebraSetResult
  (with the writer PublicationResult aliased), PublicationPolicy
  aliases, AlgebraPreparationFailure (Cause + Cleanup, Error/Unwrap),
  and MembershipAlgebra.PublishSet mirroring algebra/output.rs publish
  with the Rust failure mapping: budget/cancellation/prepare/create
  errors are early (Clean), build/finish errors discard the attempt
  and carry its cleanup state, publish refusals carry the result
  cleanup state, and every detail string is Rust-verbatim through the
  public Error classes. The builder mapping closes in every path
  after creation: Rust drops the mapped writer after publish, and Go
  must release the exclusive lifetime lock (Mapping.Close is
  idempotent) before the caller can reopen the destination; the
  success path reports a close failure, the failure paths prefer the
  original cause.
- Tests (membership_publish_set_test.go plus the v4work and zeroalloc
  slices): publish -> reopen -> verify round trips over the committed
  Rust fixtures (catalog feeds, membership ranges, CRC/seal, fresh
  identity, metadata). The committed membership-ipv4 fixture carries
  70 catalog feeds (feed-000..feed-069 with feed-005 published as
  feed-reused), so preserve-mode reports 70 output feeds with only the
  seven present feeds carrying ranges; the per-feed projection
  coalesces adjacent same-feed intervals across membership rows, and
  Flat union coalesces the three segments into one full range. The
  Rust intern-pinning shape is pinned over a generated 1024-segment
  corpus (2 interns, 1022 cache hits, 1 refcount batch), FailIfExists
  budget files=2 and replacement files=3 admission, budget refusal
  pins, empty intersection, empty selection, missing names,
  destination-exists early refusal, replacement policies, closed
  source (the SDK WrongState class), and zero-alloc (no >= 4096 heap
  object) across the build path.
- Gate: go test ./... both tag sets, vet, race, gofmt, cross-builds,
  and the full check-import-graph.sh all pass (record at the slice
  close, below).


  copy).



- Iterative pass: six narrow reviewers all PASS at HEAD 52f7a39/e02dee9
  (Peirce, Gauss, Faraday, Ampere, Kant, Bernoulli; only P3 cosmetics,
  fixed in 8e0f413).
- Final reviewer execution: k3 was attempted twice (model group k3) and
  failed in the harness with a proxy tool-call continuation error; the
  user directed that the session rely on sol only.
- sol full-scope rounds at c65b2b9 -> 52f7a39 -> f6007c7 -> 6140a80:
  round 1: 2 P2 (missing pre-fix-failing guards; missing two-pin refcount
  test) + 3 P3 -> fixed in 52f7a39/8e0f413;
  round 2: 2 P2 (retention check wording; report header contradiction) +
  1 P3 -> fixed in f6007c7;
  round 3: 1 P2 (test count 4,648 not recorded) -> fixed in 6140a80;
  round 4: PASS, no P0-P2-P3 at HEAD 6140a80 (this gate was subsequently
  reopened by an external audit, below).
- External audit reopening (2026-08-12): four findings - close-out records
  inconsistent (pending worker decision, no-deletion claim, 5/5 fixtures,
  pin copies described as unsupported), Milestone 2 scope drift, an
  incomplete public scalar API (raw [16]byte ValueTag, missing predefined
  tags and the 20 MiB metadata bound), and false zero-allocation source
  documentation. Fixed in 228be36, plus reviewer P3s in 78373e5
  (DirectSemantic per-fixture coverage, terminal error code 69 pin,
  decision-log 105-file count, qualified package comment).
- sol round 5: PASS, no P0-P2-P3 at HEAD 78373e5 (record gap: the
  reopening itself was not yet recorded);
- sol round 6: reported PASS at HEAD 29e1dde, confirming the previously named
  checks and records. An independent adversarial re-review then invalidated
  that verdict: it found the mutable exported semantic-tag authority, the
  unapproved DatabaseInfo/ImmutableInfo API deviation, and stale contradictory
  milestone-report claims. Milestone 1 is reopened; Milestone 2 is blocked.
- Review-process repair: added the generic runtime skill
  `.agents/skills/project-final-review/SKILL.md`. It requires zero-trust
  authority reconstruction, open-world public-contract and record audits,
  mechanical gates only after semantic review, and a final disproof pass before
  PASS.
- sol round 7: FAIL at HEAD 2d2197a with four P2 record/truth defects: the
  milestone report and SOW still presented the reopened findings as
  unresolved, the import-graph gate still exempted the deleted
  internal/exactv4 package, the reader documented a re-derive-every-access
  claim contradicted by cached structured values, and the zero-allocation
  evidence misstated the pin/reader measurement grain and iteration counts.
  All fixed in a64a495 (gate exemption removed; report header, section 5,
  section 6 and close-out corrected; reader.go comment qualified; zero-alloc
  comments/messages corrected), and the report LOC counts were refreshed
  for that commit's own delta in 12b2e7f (P1 found by the narrow records
  reviewer; production 4,812 / tests 4,685 verified).
- sol round 8: FAIL at HEAD 12b2e7f with one P2 (this gate record omitted
  round 7 and its repairs; the repair note above contradicted the Status)
  and one P3 (duplicate package documentation: go doc rendered both
  doc.go and types.go package comments). Fixed in 1af6135: the trail was
  completed, types.go became a file-level comment, and the report LOC
  counts were refreshed (production 4,807 / tests 4,685).
- sol round 9: FAIL at HEAD 1af6135 with one P2 - the round-8 entry had
  omitted the P3 and its repair, the "follows below" reference dangled,
  and the regression resolution claimed a closing result that did not yet
  exist. This entry records the complete round-8 result.
- sol round 10: PASS at HEAD 253f9d5, zero P0-P2-P3, full-scope zero-trust
  re-review (records, pin lifetime, mapping/locking, format/API, resources,
  regression proof, gates). This verdict was later invalidated.
- External audit after closure: FAIL at HEAD 1c71299 with five P2 findings:
  the public NetworkEnrichmentV1 location shape deviates from the approved API
  matrix; structured lookup performs full semantic validation that Rust and the
  normal-operation contract omit; range/blob/membership hot paths repeat
  decodes and the report falsely claims one page-header decode per visited
  page; the closure header contradicts Validation and skill-maintenance text;
  Mapping.File exposes an unused raw descriptor capability while the
  content-transfer source gate misses helper forms such as io.ReadAll and
  io.Copy. Milestone 1 is reopened and Milestone 2 is blocked.
- Review-process repair: `project-final-review` now defines the reviewer's
  mission as proving the work faulty or incomplete with concrete evidence. It
  authorizes unrestricted relevant investigation and `/tmp` tests, requires an
  objective/blast-radius model, treats every green claim as something to attack,
  continues beyond the first finding, and permits PASS only when the strongest
  plausible disproof attempts fail. The repository remains read-only; reviewers
  may not interfere with processes or install/uninstall software.
- Fix round (external-audit findings): implemented at HEAD ca30026 with
  pre-fix-failing pins - NetworkEnrichmentV1Location restored (decision 5A),
  structured lookup decode-only (plausible-corruption acceptance test fails
  pre-fix; record-geometry rejection keeps the memory-safety bound),
  OpenSlottedHeader at every slotted call site, membership word reads from
  the lookup-time record decode, Mapping.File removed, content-I/O gate
  extended to ReadAll/Copy forms. All gates green.
- Iterative pass (round-11 fixes): six narrow reviewers all PASS at HEAD
  431e7d7 (Peirce, Gauss, Faraday, Ampere, Kant, Bernoulli; one records P1
  - stale test LOC - fixed in 73c358b; Kant flags that decision 5A needs
  user ratification before milestone close).
- sol round 11: FAIL at HEAD 73c358b with one P1 and three P2: a Pin
  variable reassigned after creating a view (pinCopy = *otherPin)
  retargets the view's close guard to another reader and a word read then
  hits the first reader's released mapping (SIGSEGV at
  internal/reader/membership.go:106 through reader_public.go:485);
  decision 5A is recorded but not user-ratified; the content-I/O gate
  misses method values (m := f.Read), function aliases (rd := io.ReadAll),
  Seek, and new package directories, and the Windows mapping stub still
  exposed Mapping.File; the close-out records dangled ("re-run below")
  and the regression tail still said product repair is pending. Fixed at
  HEAD 2fd6cae (views retain the immutable *pinState captured at creation;
  the Windows descriptor and accessor are gone; the gate scans every go
  list package with word-boundary selector matching, mutation-tested
  against all five bypass forms) and in the records commit that follows.
- Iterative pass (round-12 fixes): six narrow reviewers all PASS at HEAD
  002505b (Peirce, Gauss, Faraday, Ampere, Kant, Bernoulli; one records P1
  - report production LOC - and one records P2 - missing round-12
  narrative - fixed in 002505b).
- sol round 12: FAIL at HEAD 002505b with two P2 and one P3: decision 5A
  remains unratified (user-decision gate); the mmap source gate is still
  bypassable - unix.Readv descriptor reads in the mapping owner, bufio
  wrappers (bufio.NewReader(file).ReadByte), dot-imported os.ReadFile,
  and build-tagged packages invisible to a linux go list all compile and
  pass the gate; the records claim gate coverage beyond what it provides
  and the report lacks the runtime-trace evidence the SOW records
  require. Fixed at HEAD 4fdc671: whole-tree selector scan (find covers
  every build-tagged file), dot-import and bufio/io-ioutil import bans,
  extended selector set (Readv/Writev/Preadv/Pwritev/ReadByte/...), a
  durable --self-test mode that at that commit rejected nine mutation forms
  (the two bufio escape forms followed in 9567067), runtime
  strace evidence recorded in the report, and P3 lifetime-comment
  corrections.
- Iterative pass (round-13 fixes): six narrow reviewers all PASS at HEAD
  a1f846f (Peirce, Gauss, Faraday, Ampere, Kant, Bernoulli; no P0-P2
  findings; only the records forward-pointer remained to be written).
- sol round 13: FAIL at HEAD a1f846f with three P2 and two P3: decision
  5A remains unratified (user-decision gate); the gate still accepts
  indirect content-transfer forms (fmt.Fscan/Fscanf/Fscanln,
  io.CopyN/CopyBuffer, reflection MethodByName("Read"), raw
  unix.Syscall(SYS_READ), unix.CopyFileRange, Sendfile, Splice), its
  line-level exemption lets a forbidden transfer share a line with a
  tolerated c.r.Read call, and a windows-tagged package can import
  internal packages unseen by a linux-only go list boundary check; the
  records contradicted the source (open-decisions prose said no product
  decision is open while 5A is unratified, the report header claimed the
  round-12 gate P2 was fully fixed although the gate remained bypassable,
  and the reported production count missed two comment lines). P3s:
  mapping View retained-slice comment wording and
  NetworkEnrichmentV1View.Value comment wording. All fixed at HEAD
  dbdf2b7 and in this record: exact call-node blanking replaces the
  line-level exemption, the selector set covers every indirect form, gzip
  and compress/zlib wrapper imports are banned, the boundary check runs
  per target over ten GOOS/GOARCH pairs, the self-test durably rejects
  all eighteen mutation forms, the P3 comments were corrected, and the
  records in this entry complete the trail.
- Iterative pass (round-13 fixes, second sweep): five narrow reviewers
  PASS at HEAD 26f0527 (Peirce, Gauss, Faraday, Kant, Bernoulli); Ampere
  found the gate still open in four classes - P1: stdlib decoder/
  encoder families consume the file directly
  (json/xml/gob NewDecoder(f).Decode, plus archive/image/bzip2/etc.
  reader packages); P2: os.File.WriteString, reflect.Value.Method(i),
  and the exact-node blanking swallowed a forbidden transfer nested
  inside the tolerated call's parentheses; P3: two self-test mutations
  did not compile. All fixed at HEAD f9c88b2: the reader-consumer
  packages join the import ban, the selector set gains
  WriteString/WriteRune/NewDecoder/Decode/Encode/Method, the blanking
  matches only paren-free tolerated arguments (c.r.Read(p) /
  c.r.ReadByte()), the two previously non-compiling sweep mutations
  compile, and four new mutation forms pin every escape; the self-test now durably rejects all
  twenty-two mutation forms. Decision 5A remains open for user
  ratification and is the only remaining P2 class.
- Iterative pass (round-13 fixes, third sweep): five narrow reviewers
  PASS at HEAD 2b30b29 (Peirce, Gauss, Faraday, Kant, Bernoulli);
  Ampere found the gate still open in three classes - P1: io.ReadFull
  and io.ReadAtLeast consume an *os.File directly (the word boundary
  after Read kept them out of the selector set); P2: the writer-consumer
  families curry a file unseen (log.New(f).Println / log.SetOutput(f),
  text/template Execute, html/template ExecuteTemplate, os/exec
  Stdout+Run, flate.NewWriter(f), http ServeContent/ServeFile; none
  active in production, but the gate must cover the Milestone 2
  writer); P3: three self-test mutations did not compile (method value
  assignment arity, CopyFileRange int width, and the nested-node probe).
  Fixed at HEAD bf33f2a: the selector set gains ReadFull/ReadAtLeast/
  Print/Printf/Println/Scan/Scanln/Scanf/NewWriter, the five writer
  packages join the import ban, the two in-memory inflater calls
  io.ReadFull(zr, ...) are exempted as exact call nodes (compress/flate
  stays importable), the method-value and CopyFileRange forms now
  compile, and the nested-node probe is retained and documented as an
  intentional textual tripwire (no []byte-typed file-read expression
  exists to embed before the first closing paren); the self-test now
  durably rejects all twenty-six mutation forms. Decision 5A remains
  open for user ratification and is the only remaining P2 class.
- Iterative pass (round-13 fixes, fourth sweep): five narrow reviewers
  PASS at HEAD 35a4182 (Peirce, Gauss, Faraday, Kant, Bernoulli);
  Ampere found the gate still open in three classes - P1: the new
  io.ReadFull blanking was paren-crossing and name-keyed (a transfer
  nested inside the tolerated node's arguments was swallowed again; a
  file-backed `zr := flate.NewReader(f)` was exempted by the variable
  name alone); P2: the reflection invocation primitive `.Call` was
  unguarded (reflect.ValueOf(f).FieldByName("Read").Call(nil) slipped);
  P2: the reader-constructor packages (debug/elf/macho/pe/plan9obj,
  go/parser, go/scanner, text/scanner) and writer families
  (text/tabwriter, mime/quotedprintable) consumed a file unseen.
  Fixed at HEAD 149a200: the io.ReadFull exemption is shape-bounded to
  the two real in-memory nodes (io.ReadFull(zr, out[...])) so neither a
  nested transfer nor a zr-named file reader is hidden; Call/CallSlice
  join the selector set; the constructor packages join the import ban;
  two new mutation forms pin the shadow and the zr-name collision. The
  self-test now durably rejects all twenty-eight mutation forms.
  Decision 5A remains open for user ratification and is the only
  remaining P2 class.
- Iterative pass (round-13 fixes, fifth sweep): five narrow reviewers
  PASS at HEAD 6a25450 (Peirce, Gauss, Faraday, Kant, Bernoulli; Kant
  adds a P3 hygiene finding - stale gatemut_* artifacts from an
  interrupted self-test can wedge the tree); Ampere found the
  exemptions still name-keyed: P1 - `zr := flate.NewReader(f)` with a
  buffer literally named `out` reproduces the tolerated
  io.ReadFull(zr, out[...]) shape (the project's own inflater naming),
  and P2 - a receiver field `r *os.File` reproduces the c.r.Read shape.
  Fixed at HEAD c03e40c: all four tolerated nodes are blanked as exact
  literals (c.r.Read(p), c.r.ReadByte(), and the two
  io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]) /
  io.ReadFull(zr, out[int(meta.MetadataUncompressed):]) inflater
  reads) and nothing else, so same-named file-backed readers and other
  index shapes fail closed; two new mutation forms pin the file-backed
  c.r and the zr/out name collision; the startup sweep removes stale
  gatemut_* artifacts so an interrupted self-test cannot wedge the
  tree. The self-test now durably rejects all thirty mutation forms.
  Decision 5A remains open for user ratification and is the only
  remaining P2 class.
- Iterative pass (fifth-sweep completion): all six narrow reviewers PASS
  at HEAD 360130c (Peirce, Gauss, Faraday, Ampere, Kant, Bernoulli); the
  fifth-sweep records were committed in 360130c.
- sol round 14: FAIL at HEAD 360130c with five P2 findings, all in the
  mmap gate and the records: split-after-the-dot selectors
  (file.\nRead(p), io.\nReadAll(f)); type-blind exact-literal
  exemptions (a struct whose c.r is *os.File using exactly c.r.Read(p),
  and a function whose zr is *os.File using exactly io.ReadFull(zr,
  out[:int(meta.MetadataUncompressed)]), both reproduce the tolerated
  shapes); the open-ended stdlib denylist (the gzip regex never matches
  compress/gzip; log/slog.NewTextHandler, runtime/trace.Start, and
  os.StartProcess with ProcAttr{Files: []*os.File} consume a file
  unseen); the destructive startup sweep (any path named gatemut_* is
  deleted before scanning, so a committed gatemut_hidden_linux.go
  violation is removed and the gate reports PASS); and acceptance
  records claiming completion while the six-reviewer PASS at 360130c
  was not recorded and round-12 wording said decision 5A was "fixed".
  Fixed at HEAD c42325a: the line-oriented text scan is replaced by the
  AST, type-light scanner (v4/go-gate) described in Status; the
  self-test copies the module to a private temp directory (forty
  mutation forms rejected, including all nine independent reproducers;
  the reviewed tree is never modified, no file name is reserved, and an
  innocent gatemut_-named file is proven to survive); the startup sweep
  is removed. HEAD 81ca524 pinned the aliased-os producer-taint form as
  the forty-first; HEAD 6b05801 tainted *os.File results of
  same-package accessor methods; the seventh sweep (HEAD e2dc7e0)
  closed the alias conversion/parameter, ProcAttr-container, and
  os.Pipe classes, releasing the 45-form self-test; the eighth sweep
  (HEAD c4b1b52) closed the struct-field-storage and channel-transport
  classes behind the inflater exemptions (self-test forms 47-48). The
  ninth sweep (HEAD ddc5f9c) closed the inline-FuncLit, type-assertion,
  two-hop-channel, and single-variable-range classes (forms 50-53);
  the tenth sweep (HEAD 5c88ba3) closed the parenthesized-producer,
  parenthesized-closure, interface-typed-closure, alias-typed-function-variable,
  and type-switch-bound classes (forms 54-59), and the defined-func-type
  family (defined func types, func-valued returns through same-package
  helpers, type-switch bound func cases (forms 60-63), and the
  method-receiver/nested-callee double-call family (forms 64-67),
  the struct-field-func/chan-of-func/asserted-func/os-std-handle
  family (forms 68-72), the nested-field/named-helper/chan-pass
  family (forms 73-77), the named-method extension (forms
  78-81), the nested-method-receiver extension (forms 82-83),
  the method-value family (forms 84-87), the generic
  pass-through family (forms 88-89), the generic-element
  family, the chan-result method-value class, the
  field-assignment class (forms 92-95), the channel-consumer
  class (forms 98-100), the container-element
  class (forms 103-106), the anonymous-receiver
  method class (forms 108-111), the alias-receiver
  method class (forms 113-114), the receiver-resolution
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
  reader-shape binding class (forms 167-174), and the
  renamed-qualified alias class (forms 179-182), and the
  func-typed generic-method class
  (forms 185-189), the mixed result and
  qualified-defined class (forms 191-196), the
  interface-method and method-result class (forms 199-205), and the
  embedded-interface and cross-package chain class (forms 207-210),
  the remote-interface and generic-instantiation class (forms
  213-217), the defined-hop instantiation class (forms 222-223), and
  the nested generic-instantiation class (forms 225-226);
  the self-test now durably rejects one hundred eighty-five mutation forms (round-32 rejects 228-235, benign control 231); the round-36 re-review then closed the dup/exec subprocess-escape and bodyless assembly-stub classes as forms 236-237, bringing the set to one hundred eighty-seven mutation forms; the follow-up then closed the x/sys-owner boundary for new packages and rejected assembly-object files (forms 238-239), raising the set to one hundred eighty-nine mutation forms. The round-37
  re-review then found one P2 in the metadata/bootstrap aspect: nonzero
  bytes after a metadata chunk were accepted by ReadMetadataJSON (Rust
  rejects them as corrupt; spec binary-format-v4.md:1051 requires zero
  tails). Fixed with the tail-zero check and the pre-fix-failing pin
  TestMetadataChunkTailNonzeroRejected. The round-38
  re-review then closed the fcntl F_DUPFD duplication primitive
  (unix.FcntlInt; form 240), raising the set to one hundred ninety
  mutation forms. The round-39
  re-review then closed the out-of-tree module-graph escape: go.mod
  replace and go.work workspaces attach modules the scan never walks
  (reproduced with a wrapper calling unix.Pread, gate exit 0 on both
  vectors); the graph is now validated to exactly this module plus
  golang.org/x/sys with no workspace active, pinned as forms 241-242,
  raising the set to one hundred ninety-two mutation forms. The round-40
  re-review then closed the x/sys source-replacement gap: a replace of
  golang.org/x/sys to an evil dir keeps the allowed path in the graph
  while loading code the walk never scans (live pread2 reproducer), and
  hidden dot-directories were skipped by the walk; replace/exclude
  directives are now banned, the resolved x/sys source is verified to be
  the module-cache checkout, and the walk skips only .git, pinned as
  forms 243-244, raising the set to one hundred ninety-four mutation
  forms. The round-42
  re-review then closed the x/sys source-content gap: a poisoned module
  cache or a file proxy with a forged go.sum keeps the allowed path and
  version while loading an evil x/sys (live Pread2 reproducers on both
  vectors); replace/exclude were already banned, so the gate now pins
  the exact version, the module-cache path, the extracted-tree content
  hash, and the module zip/go.mod sums to the official v0.35.0 values,
  and rejects assembly objects case-insensitively, pinned as forms
  245-247, raising the set to one hundred ninety-seven mutation forms. The
  round-43 re-review then closed the fail-open listing gap: the target
  loop ran go list ./... with 2>/dev/null, so a module the toolchain
  cannot list (symlinked package files, parse errors) passed with no
  package checks at all; go list failures now set fail=1 per target and
  pkg_imports fails closed, pinned as form 248, raising the set to one
  hundred ninety-eight mutation forms. The round-45
  re-review then found the mmap-gate denylist gaps: os.CopyFS directory
  copies, os.OpenInRoot/os.OpenRoot handles reaching stream wrappers, and
  the x/sys descriptor-transfer primitives (unix.Tee/Vmsplice/
  IoctlFileClone*, darwin Clonefile*) all bypassed the scan (proven live,
  gate exit 0 on every vector); CopyFS and the x/sys primitives join the
  banned selector set, os.OpenInRoot/os.OpenRoot join the file-producer
  table so Root methods fail closed, pinned as self-test forms 249-251,
  raising the set to two hundred one mutation forms; the
  same-class P0 (Root laundered through a struct field, gate exit
  0) was then closed by resolving *os.Root as a file-bearing type
  everywhere *os.File does, pinned as form 252, raising the set to
  two hundred two mutation forms; the following re-review then found
  three producer-value P0 escapes (file method values; func-typed
  vars with file-bearing declared results and an initializer, Root
  and *os.File; stdlib producer values bound without a declared
  type), all closed by the value-position capability check, the
  declared-type func-file registration, and stdlib producer-value
  registration, pinned as forms 253-256, raising the set to two
  hundred six mutation forms. The
  records
  of this entry complete the trail up to this re-review. Decision 5A
  remains open for user ratification and is the only remaining P2
  class.

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
     type-light scanner + 9,781-line shell self-test) versus a 4,789-line
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
      inflation nodes exempted by exact shape. The 240-form durable battery
      moves into the Go tool as table data; the shell harness shrinks to the
      boundary/module-graph/x-sys checks and the self-test invocation.
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
  view, page[0:4096] is a complete page). The 9,781-line shell battery
  was replaced by 320 table cases inside the tool (257 rejections, 63
  benign acceptances) covering source-transfer, complete-page, and
  file-capability forms,
  and the shell harness shrank to 552 lines (import boundaries per
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
    direct approved function, or a bounded var chain). Pinned as battery
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
    treated as a transfer. Pinned as battery forms P16 (reject), P17
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
    Pinned as battery forms P19 (reject) and P20 (benign bounded slice
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
  Pinned as battery forms P24-P38 (eleven rejects, four accepts);
  battery 305 -> 320 (246 -> 257 rejections, 59 -> 63 benign).
  Validation at HEAD f96d13d: gate --self-test 320/320 (257 rejections,
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
  Pinned as battery forms P39-P55 (fourteen rejects, three benigns:
  P39-P52 reject, P53-P55 benign); battery 320 -> 337 (257 -> 271
  rejections, 63 -> 66 benign). Gate tooling 5,104 -> 5,465 go-gate
  lines (+552 shell = 6,017 total).
  Validation at HEAD 65ca62a: gate --self-test 337/337 (271 rejections,
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
  every parsed file of the package. Pinned as battery form P56 (reject,
  two create ops); battery 337 -> 338 (271 -> 272 rejections, 66 benign).
  Gate tooling 5,465 -> 5,474 go-gate lines (+552 shell = 6,026 total).
  Validation at HEAD 0d007a8: gate --self-test 338/338 (272 rejections,
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
    scanned and policed at their own sites - battery case 63 stays
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
  Pinned as battery forms P57-P65 (nine rejects); battery 338 -> 347
  (272 -> 281 rejections, 66 benign). Gate tooling 5,474 -> 5,691
  go-gate lines (+552 shell = 6,243 total). Round-6 hardening (a
  literal-bound package func var later rebound to a non-literal has an
  unknowable callee and must stay fail-closed, not exempt) pinned as
  P66; battery 347 -> 348 (281 -> 282 rejections, 66 benign), gate
  tooling 5,691 -> 5,712 go-gate lines (+552 shell = 6,264 total).
  Validation at the closing commit: gate --self-test 348/348 (282
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
  to the shared global state (set-only, monotone fixpoint). Battery
  348 -> 357 (282 -> 291 rejections, 66 benign); gate tooling
  5,712 -> 5,906 go-gate lines (+552 shell = 6,458 total).
  Validation at the closing commit: gate --self-test 357/357 (291
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
  false-positive class found and fixed while validating the battery:
  whole-value field promotion graduated fail-closed opaque-call struct
  fields (clean stdlib chains such as io.NopCloser(x.Get()()) over a
  bytes.Reader-like result were flagged); synthetic fail-closed
  callFields are now marked and excluded from promotion while field
  reads keep failing closed. Battery 407 -> 422 (355 rejections, 67
  benign). Gate tooling 7,704 -> 8,097 go-gate lines (+552 shell =
  8,649 total).
  Validation at the closing commit: gate --self-test 422/422 (355
  rejections, 67 benign) + 9 shell mutations exit 0, production scan
  clean on every scanned target, all fifteen round-12 probes reject on
  every scanned target, go test ./... (both tag sets), -race, vet (go
  and go-gate), gofmt zero diffs, cross-builds for all shell-harness
  targets (incl. netbsd), the import-graph gate with the 422-case
  battery, and the SOW audit all pass.
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
  list now fails closed like the assertion form). Battery 422 -> 431
  (364 rejections, 67 benign). Gate tooling 8,097 -> 8,475 go-gate
  lines (+552 shell = 9,027 total).
  Validation at the closing commit: gate --self-test 431/431 (364
  rejections, 67 benign) + 9 shell mutations exit 0, production scan
  clean on every scanned target, all eight round-13 probe files plus the
  type-switch recovery probe reject on every scanned target, go test
  ./... (both tag sets), -race, vet (go and go-gate), gofmt zero diffs,
  cross-builds for every shell-harness target, the import-graph gate
  with the 431-case battery, and the SOW audit all pass.
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
  (battery 431 and go-gate 8,475 were already committed at the
  round-13 close in f478278 while the Status still carried the
  round-12 values); the Status now reports battery 436 (368
  rejections, 68 benign) and go-gate 8,611 lines (+552 shell = 9,163
  total), with this entry completing the trail. The lead's first
  joinWith rewrite (the P150 fix) regressed three battery cases (P16
  full page through a package func-literal variable, P19 the two-hop
  chain, P140 package method values): the merge treated identical
  package initializer seeds as divergent (func-literal bindings have no
  stable text for the equality check) and let branch-local invalidation
  erase package-scope proofs; the merge now keeps identical nodes and
  exempts package-scope callables (their binding is proven by the
  package initializer and reassignment is policed by reassignedVars).
  Battery 431 -> 436 (368 rejections, 68 benign). Gate tooling 8,475 ->
  8,611 go-gate lines (+552 shell = 9,163 total).
  Validation at the closing commit: gate --self-test 436/436 (368
  rejections, 68 benign) + 9 shell mutations exit 0, production scan
  clean on every scanned target, all round-14 probe files reject (or
  stay accepted for the bounded form) on every scanned target, go test
  ./... (both tag sets), -race, vet (go and go-gate), gofmt zero diffs,
  cross-builds for every shell-harness target, the import-graph gate
  with the 436-case battery, and the SOW audit all pass.
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
  variants were already pinned as P146/P153). Battery 436 -> 443 (368
  -> 375 rejections, 68 benign). Gate tooling 8,611 -> 8,805 go-gate
  lines (+552 shell = 9,357 total).
  Validation at the closing commit: gate --self-test 443/443 (375
  rejections, 68 benign) + 9 shell environment mutations exit 0,
  production scan clean on every scanned target, all round-15 probe
  files reject (or stay accepted for the bounded form) on every
  scanned target, the round-14 probe trees (/tmp/probe-luna2,
  /tmp/probe-p16, /tmp/probe-luna5) return the same verdicts as a
  clean HEAD build (luna2/p16 rejected, luna5 accepted), go test
  ./... (both tag sets), -race, vet (go and go-gate), gofmt zero
  diffs, cross-builds for every shell-harness target, the import-graph
  gate with the 443-case battery, and the SOW audit all pass.
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
  ASTs. The shell harness runs import-boundary checks on all 11
  targets. The records were updated to state this explicitly.
  Cross-builds pass on all 11 targets. Battery 527/527, import-graph
  self-test, prod scan, go test both tag sets, race, vet, gofmt, SOW
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
  Battery 514 -> 524 (443 -> 453 rejections, 71 benign unchanged;
  ten new fail pins P233-P242). Gate tooling 10,801 -> 11,016
  go-gate lines (+552 shell = 11,568 total).
  Validation at the closing commit: gate --self-test 524/524 (453
  rejections, 71 benign) + 9 shell environment mutations exit 0,
  production scan of v4/go clean (rc 0), all eleven r25 probe shapes
  reject (rc 1) on the closing build (one probe per isolated tree),
  go test ./... (both tag sets) including -race -count=1, vet (go and
  go-gate), gofmt zero diffs, eleven cross-builds, the import-graph
  gate with the 524-case battery, and the SOW audit all pass.

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
  Battery 507 -> 514 (437 -> 443 rejections, 70 -> 71 benign
  acceptances; six new fail pins P226-P231 and one benign pin P232).
  Gate tooling 10,695 -> 10,801 go-gate lines (+552 shell = 11,353
  total).
  Validation at the closing commit: gate --self-test 514/514 (443
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
  eleven cross-builds, the import-graph gate with the 514-case battery,
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
  battery 500 -> 505 (431 -> 436 rejections, 69 benign acceptances
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
  battery 505 -> 507 (436 -> 437 rejections, 69 -> 70 benign
  acceptances). Gate tooling 10,648 -> 10,695 go-gate lines (+552
  shell = 11,247 total).
  Validation at the closing commit: gate --self-test 507/507 (437
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
  battery 491 -> 500 (423 -> 431 rejections, 68 -> 69 benign
  acceptances). Gate tooling 10,045 -> 10,463 go-gate lines (+552
  shell = 11,015 total). Eight real escapes fixed; the l22-4/4b
  false positive documented with probe evidence (no change needed);
  the l22-9 false rejection fixed; none dismissed.
  Validation at the closing commit: gate --self-test 500/500 (431
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
  battery 481 -> 491 (413 -> 423 rejections, 68 benign acceptances).
  Gate tooling 9,648 -> 10,045 go-gate lines (+552 shell = 10,597
  total). All eight were real; no finding was dismissed.
  Validation at the closing commit: gate --self-test 491/491 (423
  rejections, 68 benign) + 9 shell environment mutations exit 0
  (battery log /tmp/battery-r21b.log), production scan clean, all 38
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
  append shapes) all reject. battery 474 -> 481 (406 -> 413
  rejections, 68 benign acceptances). Gate tooling 9,497 -> 9,648
  go-gate lines (+552 shell = 10,200 total).
  Validation at the closing commit: gate --self-test 481/481 (413
  rejections, 68 benign) + 9 shell environment mutations exit 0
  (battery log /tmp/battery-r20c.log), production scan clean on every
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
  side). battery 464 -> 474 (396 -> 406 rejections, 68 benign
  acceptances). Gate tooling 9,234 -> 9,497 go-gate lines (+552 shell
  = 10,049 total). The remaining findings were verified not real:
  luna1b/luna1c (the two-value asserted-container bind and the
  interface-parameter container element) are intentionally accepted
  shapes and stay accepted as a known open question, three reports
  were false positives or dead code, and the stale-record report
  (pending-report battery/LOC snapshot from mid-work state) is closed
  by this record sync.
  Validation at the closing commit: gate --self-test 474/474 (406
  rejections, 68 benign) + 9 shell environment mutations exit 0,
  production scan clean on every scanned target, every round-19 probe
  shape escapes the fresh HEAD build (rc 0) and rejects the closing
  build (rc 1), the P172 two-value-assertion regression probe stays
  rejected, l19 and all prior probe trees keep their verdicts
  (luna1b/luna1c stay accepted), go test ./... (both tag sets), vet
  (go and go-gate), gofmt zero diffs, the import-graph gate with the
  474-case battery, and the SOW audit all pass.
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
  container level of the parameter type. Battery 452 -> 460 (384 ->
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
  recorded, literal, and parameter roots. Battery 460 -> 464 (392 ->
  396 rejections, 68 benign). Gate tooling 9,136 -> 9,234 go-gate
  lines (+552 shell = 9,786 total).
  Validation at the closing commit: gate --self-test 464/464 (396
  rejections, 68 benign) + 9 shell environment mutations exit 0,
  production scan clean on every scanned target, all round-18 probe
  shapes reject (l19 type-assertion trees, multi-dim index read/bind/
  return trees, forced literal extraction, and selector-over-index
  trees) and every prior probe tree keeps its verdict (l17b, l16,
  luna15, f2only2, luna2, p16 reject; luna5 stays accepted), go test
  ./... (both tag sets), -race, vet (go and go-gate), gofmt zero
  diffs, cross-builds for every shell-harness target, the import-graph
  gate with the 464-case battery, and the SOW audit all pass.
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
  callRootChain). Battery 447 -> 452 (379 -> 384 rejections, 68
  benign). Gate tooling 8,935 -> 9,003 go-gate lines (+552 shell =
  9,555 total).
  Validation at the closing commit: gate --self-test 452/452 (384
  rejections, 68 benign) + 9 shell environment mutations exit 0,
  production scan clean on every scanned target, all round-17 probe
  shapes reject on every scanned target and every prior probe tree
  keeps its verdict (l16, luna15, f2only2, luna2, p16 reject; luna5
  stays accepted), go test ./... (both tag sets), -race, vet (go and
  go-gate), gofmt zero diffs, cross-builds for every shell-harness
  target, the import-graph gate with the 452-case battery, and the SOW
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
  parent's prefix so every promoted path binds). Battery 443 -> 447
  (375 -> 379 rejections, 68 benign). Gate tooling 8,805 -> 8,935
  go-gate lines (+552 shell = 9,487 total).
  Validation at the closing commit: gate --self-test 447/447 (379
  rejections, 68 benign) + 9 shell environment mutations exit 0,
  production scan clean on every scanned target, all round-16 probe
  files reject on every scanned target together with the round-15
  probe trees (/tmp/probe-luna15, /tmp/probe-luna14) and the previous
  round trees (/tmp/probe-f2only2, /tmp/probe-luna2, /tmp/probe-p16,
  /tmp/probe-luna5) returning the same verdicts as a clean HEAD build,
  go test ./... (both tag sets), -race, vet (go and go-gate), gofmt
  zero diffs, cross-builds for every shell-harness target, the
  import-graph gate with the 447-case battery, and the SOW audit all
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
  stores (m[fileKey] = 1 flagged like the literal key form). Battery
  393 -> 407 (326 -> 340 rejections; the 67 benign forms are
  unchanged). Gate tooling 7,282 -> 7,704 go-gate lines (+552 shell =
  8,256 total).
  Validation at the closing commit: gate --self-test 407/407 (340
  rejections, 67 benign) + 9 shell mutations exit 0, production scan
  clean, all fourteen luna probes (gatemut_r11_1 through gatemut_r11_12
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
  mapped view is a complete-page copy); and the shell harness never
  listed netbsd/amd64 (the targets matrix now includes it). Battery
  378 -> 393 (311 -> 326 rejections; the 67 benign forms are
  unchanged). Gate tooling 6,684 -> 7,282 go-gate lines (+552 shell =
  7,834 total).
  Validation at the closing commit: gate --self-test 393/393 (326
  rejections, 67 benign) + 9 shell mutations exit 0, production scan
  clean, all fifteen luna probes (gatemut_r10_1 through gatemut_r10_13
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
  terminates). Battery 364 -> 378 (298 -> 311 rejections, 66 -> 67
  benign; the added benign form pins recursive carrier types with no page
  flow). Gate tooling 6,229 -> 6,684 go-gate lines (+552 shell = 7,236
  total).
  Validation at the closing commit: gate --self-test 378/378 (311
  rejections, 67 benign) + 9 shell mutations exit 0, production scan
  clean on all five targets, all twelve luna probes (gatemut_p1-p12 plus
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
  both are covered by the final sweep design. Battery 357 -> 364 (291 ->
  298 rejections, 66 benign); gate tooling 5,906 -> 6,229 go-gate lines
  (+552 shell = 6,781 total).
  Validation at the closing commit: gate --self-test 364/364 (298
  rejections, 66 benign) + 9 shell mutations exit 0, production scan
  clean on all five targets, go test ./... (both tag sets), -race, vet,
  gofmt zero diffs, cross-compilation, audit - all green.
- Finding 3 (records): this Status is the compact record; the pre-rework
  history is preserved verbatim in ## Status History (appendix).
- Battery repair during replacement (recorded for the record): the
  extractor of the shell battery had lost multi-line inserts, broken
  string escaping (case 107), and copied shell-only module-graph cases
  (18, 238, 243, 248); benign cases 49/59/63/67/81/83/90 referenced
  undefined types or non-compiling assignments valid only for the old
  syntax-only scanner. All were repaired to compilable equivalents that
  preserve each tested rule (cleanup order of multi-op case files is now
  LIFO so metadata.go double ops restore the original), and the four
  module-graph cases moved to the shell self-test (the boundary loop
  already enforced them per target).
- Validation: go test ./... (both tag sets), -race, checkptr, go vet,
  gofmt zero diffs, the full import-graph gate and its --self-test (320
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
  `v4/go/check-import-graph.sh` (import boundary gates, run in the same gate
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
  check-import-graph.sh gate, view-API ID removal, callback-error
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
  -race), `go vet`, `gofmt -l`, `check-import-graph.sh`, 9-target
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
  - `check-import-graph.sh` now bans content-transfer I/O in production
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
  `go vet`, `gofmt -l`, `check-import-graph.sh`, 9-target cross-compile
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
  - check-import-graph.sh comment stripper treated `//` inside string
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
- Pinned as self-test forms 249-251; the durable rejection set is now two
  hundred one mutation forms. The adversarial re-review of this fix
  then proved a P0 in the same class: a *os.Root stored in a struct
  field (h := gateRootField{r: root}; h.r.Open(name)) dropped the
  file taint, so the returned *os.File reached flate.NewReader
  untainted and the stream was consumed through the exact inflater
  exemption shape (gate exit 0, /tmp reproducer); the type model now
  resolves *os.Root as a file-bearing type everywhere *os.File does
  (struct fields, parameters, helper returns, type assertions,
  func/chan elements, method results), pinned as self-test form 252,
  raising the durable rejection set to two hundred two mutation forms.
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
  pinned as self-test forms 253-256, raising the durable rejection
  set to two hundred six mutation forms.
- The producer-value closure landed at HEAD 5ff9116 on top of the
  Root-taint fix (262756c); the exact round-45/46/47 chain is 14c0698
  (gate gaps), 70dcc42 (its records), 262756c (Root laundering), e1410eb
  (its records), 5ff9116 (producer values), 8c6cc44 (its records). Gates:
  go test ./... incl -race, go vet,
  gofmt, import graph (self-test, all 206 forms rejected at that commit),
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
  pinned as self-test forms 257-260, raising the durable rejection set
  to two hundred ten mutation forms.
- Replayed at the new gate: all round-47 replays (R4-R14) and the new
  probes (P10 method expression, P12 exempted-inflater chain, P14
  package-level method expression, P15 cross-package producer var)
  are rejected; the benign close-value/Fd-value/Chdir controls still
  pass. Gates: go test ./... incl -race, go vet, gofmt, import graph
  (self-test, all 210 forms rejected), CGO_ENABLED=0 build and test,
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
  (typeConversionFile); pinned as self-test forms 261-266, raising the
  durable rejection set to two hundred sixteen mutation forms.
- Replayed at the new gate: all round-47 replays (R1-R14), round-48
  probes (P1-P15), the new nested-paren/renamed/alias/wrapper
  method-expression probes, the cross-package value-bound producer
  probes (function-level, package-level, renamed-import), and the
  interface-conversion launder probe are rejected; the benign controls
  still pass. Gates: go test ./... incl -race, go vet, gofmt, import
  graph (self-test, all 216 forms rejected), CGO_ENABLED=0 build and
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
  budget; pinned as self-test forms 267-270, raising the durable
  rejection set to two hundred twenty mutation forms.
- Replayed at the new gate: all round-47 (R1-R14) and round-48 (P1-P15)
  probes, the round-49 probe families (nested-paren, renamed-import,
  alias-over-renamed, wrapper, value-bound producer, interface
  conversion), and the four new probes are rejected; the benign
  controls still pass. Gates: go test ./... incl -race, go vet,
  gofmt, import graph (self-test, all 220 forms rejected),
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
  pre-fix scanner passes the full 220-form self-test at
  zero MISSes. Forms 269-270 (method-expression classes) were genuine.
- Fixed at HEAD 875b19205fb3:
  `genericParamFilePositions` now taints every declared result
  position once a file-typed argument binds a type parameter (the
  generic body is opaque; exact parameter, interface spellings of any
  qualifier, containers, channels, and func wrappers all keep the
  taint); `registerCompositeFieldTaints` resolves unkeyed elements
  through the struct's declared field order (embedded fields by type
  text), mirrors the order cross-package like the struct registry, and
  records the element's classified kind; self-test machinery gained
  metadata.go save/restore appends (`append_mut`) and import-block
  injection (`inject_import`).
- Pinned as durable exemption-shape appends: forms 267-268 converted
  and 271-277 added (renamed-io interface result, io/fs interface
  result, slice-of-T, positional field, array-of-T, chan send of an
  erased value, positional os.Root opener), raising the rejection set
  to two hundred twenty-seven mutation forms. Vacuity proof: the
  round-50 scanner misses exactly the seven new forms (and the
  converted 267-268 against the pre-round-50 scanner), the fixed
  scanner rejects all 227. Gates: go test ./... incl -race, go vet,
  gofmt, import graph (self-test, all 227 forms rejected), the real
  tree gate, CGO_ENABLED=0 build and test, ten cross-compiles across
  the per-target listing matrix, SOW audit — all green. Counts
  unchanged at this commit: production 4,792 raw lines / tests 4,877
  raw lines. Decision 5A remains open for user ratification; Milestone
  2 remains blocked until the final review passes.

### 2026-08-13 - round-52 gate re-review closed embedded, var-bound, anonymous struct-literal, container-element, pointer-literal, and explicit-instantiation launders (HEAD 5acd2a6; records 6470f21)

- The round-52 adversarial gate hunt (six narrow reviewers:
  validation runner, reader endpoint, records, self-test integrity,
  scanner diff review, gate hunting) failed with four live escapes
  across three root causes in the content-transfer scanner plus one
  vacuous self-test pair; the continuation hunt then found twelve
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
- P1 (self-test integrity) - forms 245-246 never ran the gate:
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
  mutation forms. Vacuity proof: the round-51 scanner misses exactly
  the eleven new forms (and forms 245-246 were vacuous before the
### 2026-08-13 - round-53 final review reopened the gate: embed and //go:embed compile-time database copies closed (HEAD 2e0e3667db3c)

- The round-53 final review failed with two P2 findings: (1) the mmap-only
  gate accepted //go:embed database content - a production source embedding
  an 8,192-byte database into a []byte passed check-import-graph.sh,
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
  hundred forty mutation forms (round-52: two hundred thirty-eight).
- Vacuity against the round-51 scanner: exactly thirteen MISSes, forms
  278-290 (the eleven round-52 forms plus the two round-53 forms).
- P2-2 resolved by decision 5A (ratified 2026-08-13, option A): the
  value-plus-HasLocation representation is the approved zero-allocation
  equivalent of Rust's Option<NetworkEnrichmentV1Location>; the parity
  matrix now records the ratification, so the milestone gate no longer
  depends on this item.
- Gates at this commit: go test ./... incl -race, go vet, gofmt,
  import graph (self-test, all 240 forms rejected), the real tree gate,
  CGO_ENABLED=0 build and test, ten cross-compiles, SOW audit - all green.
### 2026-08-13 - round-54 cosmetic alignment: dead word-read state removed, zero Pin reports WrongState, inflater wording aligned (HEAD 72e4d89d75fb)

- Removed dead state in internal/reader/membership.go readWordsInner
  (var data []byte, var err error, and the never-true error check after
  the storage switch); behavior identical.
- The zero-value Pin now reports WrongState on Close and checkOpen,
  matching the inert zero-view contract instead of panicking; pinned by
  TestPinZeroValueClose.
- Wording: "the three in-memory inflater nodes" became "the in-memory
  inflater call sites" (check-import-graph.sh header, v4/go-gate/main.go
  findExemptions comment, Validation section).
- Blank-import decision recorded: blank imports of banned packages remain
  intentionally uncaught by the import ban - blank imports expose no
  names and cannot transfer bytes, and the only blank-import-sensitive
  mechanism, //go:embed, is separately rejected as a directive.
- Gates at this commit: go test ./... incl -race, go vet, gofmt,
  import graph (self-test, all 240 forms rejected), the real tree gate,
  ten cross-compiles, SOW audit - all green. Counts: production 4,789 raw lines / tests
  4,887 raw lines (the dead-state removal and zero-Pin test account for
  the delta; the gate scanner lives outside the module).

## Validation

Acceptance criteria evidence:

- Milestone 1 evidence: milestone report
  `.agents/sow/pending/pure-go-v4-port-milestone-1-report.md` (fixture
  cross-open with exact cases.json semantics, malformed rejection, literal
  codec vectors, zero-allocation measurements incl. the blob path, review
  rounds 1-3 with all P0-P2 findings fixed and regression-pinned).
  Milestone 2 not started.

Tests or equivalent validation:

- `go test ./...` (5 packages: root, format, reader, mapping, work) — green at the last commit.
- `go test -race ./internal/format ./internal/reader ./internal/mapping .` — green.
- `go vet ./...` — clean; `gofmt -l .` — empty.
- `./check-import-graph.sh` — passes; the content-transfer scan is the typed
  gate (v4/go-gate, stdlib only): banned imports/selectors and the
  `*os.File` capability surface, with the in-memory inflater call sites
  exempted as exact, file-taint-verified shapes; the 578-form `--self-test`
  runs in a private temp copy and never modifies the reviewed tree.
- Cross-compilation: linux amd64/386/arm/arm64/loong64, darwin
  amd64/arm64, freebsd amd64, netbsd amd64, windows amd64/arm64 (the
  gate's per-target listing matrix) — all build.
- Conformance: 6/6 Rust fixtures cross-open with exact semantics; 3/3 invalid
  mutations rejected with code 32; structured absence probes added.
- `.agents/sow/audit.sh` — clean.

Real-use evidence:

- The Rust peer has accepted representative update-ipsets replay evidence.
  Equivalent Go evidence is an implementation acceptance requirement, not a
  planning claim.

Reviewer findings:

- Six-agent adversarial review rounds 1-5 (2026-08-11/12, see execution
  log): the pre-session gap-analysis pass found one real BLOCKER (structure
  radix) and ten MAJOR findings, all repaired (58c4d8f); this session's
  round 1 found no P0 but P1/P2 findings across all six aspects, all
  repaired with regression tests; round 2 re-review caught a shipped P0
  (blob coverage underflow, 3b4f3d5) and further P2s, all repaired and
  pinned; rounds 3-5 closed with all six reviewers at PASS (0 P0-P2).
  The review-process sweeps through the sixth round are recorded in the
  gate execution record: the fifth sweep completed with all six narrow
  reviewers at PASS (360130c); the sixth final review (sol round 14)
  failed with five P2 findings in the mmap gate and the records, all
  fixed in this pass with the AST gate rewrite (v4/go-gate), the
  temp-copy self-test, and the completed records; decision 5A was later
  ratified (option A, 2026-08-13) after the user delegated the choice to
  the implementing agent.
  The round-44 re-verification then completed with all six narrow
  reviewers at PASS (e5fea20); the round-45 final review failed with
  three P2 mmap-gate findings (os.CopyFS, os.OpenInRoot/os.OpenRoot
  handles, x/sys descriptor-transfer primitives), all fixed at the next
  HEAD with self-test forms 249-251, the same-class Root-laundering P0 closed
  with form 252, and the producer-value P0s closed with forms 253-256;
  the rejection set is two hundred six mutation forms; the round-48
  gate re-review then closed bound method expressions and
  same-module cross-package producer vars (forms 257-260, two
  hundred ten forms); the round-49 gate re-review closed nested-
  paren, renamed-import, alias-over-renamed, and wrapper-promoted
  method expressions, value-bound cross-package producer vars, and
  interface-conversion laundering of a file into the metadata
  inflater exemption (forms 261-266, two hundred sixteen forms);
  the round-50 gate re-review then closed the generic
  identity-with-interface-result erasure, the composite-literal
  field launder, the instantiated-generic-wrapper method
  expression, and the deep embedding-chain method expression
  (forms 267-270, two hundred twenty forms); re-verification at the
  round-50 HEAD passed the full replay and gate suites.
  The round-51 gate re-review then failed with seven live escapes
  across three root causes: interface-erased generic results were
  recognized only under the literal io qualifier (renamed imports,
  io/fs interfaces, and chan sends of the erased value escaped),
  container-of-type-parameter results ([]T, [2]T) never resolved to
  file taint, and positional (unkeyed) composite-literal elements
  registered no field taint (a plain file field and an os.Root
  opener both laundered); closed at 875b192 by tainting every
  declared result position of a file-bound generic call and by
  resolving unkeyed literal elements through the struct's declared
  field order, pinned as forms 271-277 and by converting the vacuous
  separate-file forms 267-268 to exemption-shape metadata.go appends
  (forms 269-270 were already genuine); the pre-fix scanner misses
  exactly the seven new forms, the fixed scanner rejects all two
  hundred twenty-seven.
  The round-52 gate re-review then failed with four live escapes
  across three root causes: embedded fields were registered by type
  text while Go names them by type name, so positional io.Reader and
  positional/keyed *os.Root wrapper literals left promoted handles
  live; a variable bound to an explicit generic instantiation
  (var a = wrap[*os.File]) erased container-result taint; anonymous
  struct literals registered no positional order (x := struct{ r
  io.Reader }{f}, and an anonymous struct embedding *os.Root with a
  positional element); the continuation hunt then added twelve live
  escapes across three more classes (elided inner composite
  literals in slice, map, nested-container, and channel elements;
  pointer composite literals; func-valued arguments to explicitly
  instantiated generics); closed at 5acd2a6 by naming embedded
  fields by type name, resolving var-bound instantiations through
  the base generic's fixed arguments, resolving anonymous literal
  order from the literal's own fields, escalating anonymous
  embedded-handle wrappers, registering elided container element
  fields by the element struct's declaration, unwrapping pointer
  literals, and mounting the explicit-instantiation callee route,
  pinned as forms 278-288; the self-test
  integrity review also exposed forms 245-246 as never running the
  gate (the env prefix expanded to a command name), fixed by
  prefixing env in run_mut; the pre-fix scanner misses exactly the
  eleven new forms, the fixed scanner rejects all two hundred
  thirty-eight.
  The round-53 final review then failed with two P2 findings: the
  embed package and the //go:embed directive copy database bytes
  into the binary at compile time, bypassing the mmap-only contract
  (the AST gate banned neither the embed import - blank `_ "embed"`
  imports are skipped as name-less - nor the directive, which was
  scanned only for //go:linkname); closed at 2e0e3667db3c by banning the
  embed import and rejecting every //go:embed directive in the
  production scan, pinned as pre-fix-failing forms 289 (non-blank
  embed import) and 290 (blank import with an embedded probe.db);
  the pre-fix scanner misses exactly the two new forms, the fixed
  scanner rejects all two hundred forty; the
  NetworkEnrichmentV1Location pointer-vs-value deviation was ratified
  as decision 5A (option A): value-plus-HasLocation is the documented
  zero-allocation equivalent of Rust's Option<NetworkEnrichmentV1Location>.
  The closed-state error class was resolved by decision 3 (WrongState
  class, error-capable WordCount) and was never an open defect.

  Final check (2026-08-17, HEAD 4f11e3d): full-scope five-resident swarm
  under the Review Process step-5 boundary - glm, kimi, minimax, and mimo
  PASS with no P0-P2 (independent adversarial sweeps of codec parity vs
  the Rust authority, mapping/remap fail-closed states, pin lifetime,
  zero-allocation hot paths, gate families and the 578-case battery,
  per-target import boundaries, records); qwen skipped as unavailable;
  sol and luna skipped (weekly quota); the final gate closed on the
  available-resident quorum per user decision; reduced coverage reported
  to the user. No findings to fix; no round restart.

Same-failure scan:

- Round-2 searches re-ran the full classes: content-transfer I/O (now also a
  mechanical gate), complete page arrays, stale wire constants, missing
  record-limit validation (catalog feed index), kind-classification error
  classes, blob/probe validation gaps, metadata stream validation, report and
  SOW factual drift. Each class is fixed completely, not only the cited
  examples.

Sensitive data gate:

- This SOW contains repository-relative paths, public upstream identity,
  generic platform names, code metrics, and synthetic/public benchmark
  descriptions only. It contains no raw secret, credential, operational host
  alias, personal or customer/community data, private endpoint, or proprietary
  incident detail.

Artifact maintenance gate:

- AGENTS.md: updated to register the generic final-review runtime skill.
- Runtime project skills: added `project-final-review` after the first repeated
  false PASS, then reframed it after the round-10 false PASS around one explicit
  adversarial objective: prove the work should not merge. It now grants broad
  investigative authority, requires concrete evidence, and defines PASS as a
  failed full-scope disproof attempt. The Rust skill remains unchanged. A Go
  implementation skill is still not invented here.
- Specs: reviewed completely; no format or behavior change was made.
- End-user/operator docs: unaffected; the milestone report is corrected as a
  project record, not published product documentation.
- End-user/operator skills: none exist and no public workflow changed.
- SOW lifecycle: this file remains `in-progress` under `current/`; Milestone 1
  is CLOSED after the final check (2026-08-17, HEAD 4f11e3d) and re-closed
  after the round-7 final re-verification (2026-08-18, 694-case battery,
  entry in Status above); Milestone 2 (writer) is authorized and in
  progress; SOW-0017 remains the separate Phase-2 item.

Specs update:

- None for Milestone 1: the Go reader implements the current normative
  contract unchanged.

Project skills update:

- Updated `project-final-review` after the round-10 false PASS. The generic
  workflow now makes fault discovery its explicit mission, requires reviewers
  to understand the objective and blast radius, grants authority to examine any
  relevant surface and build `/tmp` reproducers, requires proven findings, and
  defines PASS as failure to prove a blocking defect after the strongest
  plausible attacks. Repository modification, process interference, and
  software installation/removal are forbidden. A Go implementation skill
  remains deferred until proven commands and hazards exist later in this SOW.

End-user/operator docs update:

- The round-2 repairs are recorded in the milestone report and this SOW log;
  the Go SDK documentation itself is an implementation deliverable.

End-user/operator skills update:

- None exist; reassess at closure.

Lessons:

- Passing tests around private fragments do not establish a usable SDK or
  current wire compatibility.
- A semantic port needs an explicit parity matrix and cross-language
  execution; source similarity is neither required nor sufficient.
- The most dangerous way to port this engine is to preserve the obsolete Go
  storage architecture because it already looks substantial.
- Single-reviewer passes converge; only adversarial multi-agent passes with
  disjoint briefs found the catalog-limit, kind-class, dangling-reference,
  FreeBSD, and paperwork defects.

Follow-up mapping:

- Snapshot signing remains tracked by pending SOW-0017.
- No other deferred item is created by this milestone.

## Outcome

Pending implementation and user acceptance.

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
self-test, runtime strace evidence). Decision 5A was entered in the
decision log for the user's ratification; it was ratified on
2026-08-13 (option A) after the user delegated the choice to the
implementing agent. The complete re-review trail is recorded in the Gate
execution record; the closing result is appended there when it completes.

### 2026-08-13 - sixth-sweep gate rewrite (AST scanner) (HEAD c42325a)

- The sixth final review failed with five P2 findings, all in the mmap
  gate and the records: split-after-the-dot selectors; type-blind
  exact-literal exemptions; the open-ended stdlib denylist
  (compress/gzip regex bug, log/slog, runtime/trace,
  os.StartProcess ProcAttr files); the destructive gatemut_* startup
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
- The self-test now runs in a private temp copy of the module (cp -a
  into mktemp): forty mutation forms rejected, including the nine
  independent reproducers of the sixth review; an innocent
  gatemut_-named file is proven to survive; the reviewed tree is never
  modified; the startup sweep is removed. HEAD 81ca524 then pinned the
  aliased-os producer form as the forty-first; HEAD 6b05801 tainted
  *os.File results returned by same-package accessor methods.
- Gates at those HEADs: go test ./... incl -race, go vet, gofmt,
  import graph with the 41-form self-test, cross-compiles, SOW audit -
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
- The self-test forms were renumbered and extended: 40 aliased-os,
  41 accessor-method, 42 alias-conversion, 43 alias-parameter,
  44 separately built ProcAttr, 45 os.Pipe producer, 46 innocent
  gatemut_-named survival (positive). Durable rejection set: forty-five
  mutation forms.
- Gates at HEAD e2dc7e0: go test ./... incl -race, go vet, gofmt,
  import graph with the 45-form self-test, nine cross-compiles, SOW
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
- Self-test forms 47 and 48 pin the two proven classes by shadowing the
  exact inflater exemption in metadata.go with a struct-field stored
  file and a channel-transported file; form 49 pins the benign
  same-shaped control (int field) that must pass, proving the taint is
  not a false positive. Durable rejection set: forty-seven mutation
  forms; the interplay between the exemption guard and the file taint
  is now mutation-tested.
- Gates at HEAD c4b1b52: go test ./... incl -race, go vet, gofmt,
  import graph with the 47-form self-test, nine cross-compiles, SOW
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
- Self-test forms 50-53 pin the four classes: inline FuncLit, type
  assertion, two-hop channel transport, and single-variable channel
  range, all shadowing or exercising the inflater exemption; the benign
  control (form 49) and the innocent-file survival check (form 46)
  still pass. Durable rejection set: fifty-one mutation forms.
- Gates at HEAD ddc5f9c: go test ./... incl -race, go vet, gofmt,
  import graph with the 51-form self-test, nine cross-compiles, SOW
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
- Self-test forms 54-58 pin the five closed classes and form 59 pins the
  parenthesized benign control (HEAD 5c88ba3). Durable rejection set:
  fifty-six mutation forms; the real tree stays green under the hardened
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
  substituteTypeParams). The round's P2 - benign self-test form 212
  did not compile (unused "bytes" import in the reader file) - was
  fixed by removing the import; the self-test never type-checks, so
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
  as self-test forms 245-247, raising the set to one hundred
  ninety-seven mutation forms. The round-43 gate re-review then found
  the fail-open listing gap: the per-target go list ./... loop swallowed
  listing failures, so a module the go toolchain cannot list (symlinked
  package files, parse errors) passed with an empty package list and no
  import checks; go list failures now fail the gate per target and the
  per-package import listing fails closed too, pinned as form 248,
  raising the set to one hundred ninety-eight mutation forms. The round-45
  gate re-review then found the mmap-gate denylist gaps: os.CopyFS
  directory copies, os.OpenInRoot/os.OpenRoot handles reaching stream
  wrappers, and the x/sys descriptor-transfer primitives (unix.Tee,
  unix.Vmsplice, unix.IoctlFileClone/CloneRange/DedupeRange, darwin
  unix.Clonefile/Clonefileat) bypassed the scan (all proven live, gate
  exit 0); CopyFS and the x/sys primitives join the banned selector set,
  os.OpenInRoot/os.OpenRoot join the file-producer table so Root methods
  fail closed, pinned as self-test forms 249-251, raising the set to two
  hundred one mutation forms; the same-class P0 (Root laundered
  through a struct field: h.r.Open(name) after h := struct{r
  *os.Root}, gate exit 0) was then closed by resolving *os.Root as a
  file-bearing type everywhere *os.File does, pinned as form 252,
  raising the set to two hundred two mutation forms; the
  producer-value re-review then closed the file-method-value,
  initialized func-typed-variable (Root and *os.File), and plain
  stdlib-producer-value escapes (forms 253-256); the round-48
  adversarial re-review then closed bound method expressions on
  file-bearing receiver types (form-local and package-level) and
  same-module cross-package producer vars (forms 257-260), raising
  the set to two hundred ten mutation forms.
- Gates at current HEAD: go test ./... incl -race, go vet, gofmt,
  import graph with the 240-form self-test (round-32 rejects cover cgo, raw and no-error syscalls, linkname, preadv2/pwritev2; round-36 rejects 236-237, follow-up rejects 238-239, round-38 reject 240, round-39/40 rejects 241-244, round-42 rejects 245-247, and round-43 reject 248 cover the dup/exec subprocess escape, bodyless assembly stubs, the x/sys owner boundary, assembly objects, fcntl F_DUPFD duplication, out-of-tree module-graph attach, x/sys source replacement, hidden dot-directories, x/sys source-content spoofing (poisoned cache and file proxy with forged go.sum), case-variant assembly objects, and unlistable modules, and round-45
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
bufio import bans, a self-test mode, and the runtime half of the
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
check runs per target over ten GOOS/GOARCH pairs, and the self-test
durably rejects eighteen mutation forms. The narrow re-review of that
fix then found the decoder/encoder family still open (encoding/json,
xml, gob NewDecoder(f).Decode, image/archive wrappers), os.File.WriteString,
a nested-paren blanking shadow, and reflect.Value.Method(i);
the gate now also bans the reader-consumer packages, covers
WriteString/WriteRune/NewDecoder/Decode/Encode/Method selectors, blanks
only paren-free tolerated call nodes, and its self-test durably rejects
twenty-two mutation forms. The re-review of that fix found io.ReadFull/
io.ReadAtLeast over a file still open, the writer-consumer packages
(log, text/template, html/template, os/exec, net/http, flate.NewWriter)
uncovered, and three self-test mutations that did not compile; all fixed
at HEAD bf33f2a (selectors ReadFull/ReadAtLeast/Print/Printf/Println/
Scan/Scanln/Scanf/NewWriter; the five writer packages join the import
ban; the two in-memory inflater io.ReadFull(zr, ...) nodes are exempted
exactly; the method-value and CopyFileRange forms compile, and the
nested-node probe stays an intentional textual tripwire), and the
self-test now durably rejects twenty-six mutation forms. The re-review
of that fix found the io.ReadFull exemption itself paren-crossing (a
nested transfer could still be swallowed; a file-backed flate reader
named zr was exempted by name), the reflection Call invocation
unguarded (FieldByName("Read").Call), and the reader-constructor
packages (debug/elf and the debug/* family, go/parser, go/scanner,
text/scanner, text/tabwriter, mime/quotedprintable) unlocked; all fixed
at HEAD 149a200 (the io.ReadFull exemption is shape-bounded to the two
real nodes io.ReadFull(zr, out[...]), Call/CallSlice join the
selectors, the constructor packages join the import ban), and the
self-test now durably rejects twenty-eight mutation forms. The re-review
of that fix found the exemptions still name-keyed (a file-backed flate
reader named zr with a buffer named out, and a receiver field r, could
reproduce the tolerated shapes), so at HEAD c03e40c the exemptions are
exact literals and nothing else: c.r.Read(p), c.r.ReadByte(), and the
two io.ReadFull(zr, out[...int(meta.MetadataUncompressed)]) inflater
reads; same-named file-backed readers and other index shapes now fail
closed, two pin forms were added, and a startup sweep removes stale
gatemut_* artifacts from interrupted self-test runs. The self-test now
durably rejects thirty mutation forms. The records were completed in
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
startup sweep (every path named `gatemut_*` is deleted before scanning,
so a committed `gatemut_hidden_linux.go` violation is removed and the
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
not file-tainted. The self-test now copies the module into a private
temporary directory: it never touches the reviewed tree, reserves no
file name, proves an innocent `gatemut_`-named file is not deleted, and
durably rejects forty mutation forms including all nine independent
reproducers of the sixth review; the startup sweep is gone. HEAD
81ca524 then pinned the aliased-os producer form as the forty-first;
HEAD 6b05801 tainted `*os.File` results returned by same-package
accessor methods. The seventh-sweep hardening (HEAD e2dc7e0) closed
the type-alias conversion and parameter classes, separately built
`os.ProcAttr{Files}` containers, and the `os.Pipe` producer class,
renumbered the self-test forms, and raised the durable rejection set to
forty-five mutation forms. The eighth sweep (HEAD c4b1b52) closed the
struct-field-storage and channel-transport classes behind the inflater
exemptions (shared per-package taint state, struct-field write taint,
chan *os.File taint including send/recv/range, new(T) instances,
container index reads) and pinned them as self-test forms 47-48. The
ninth sweep (HEAD ddc5f9c) closed the inline-FuncLit, type-assertion,
two-hop-channel, and single-variable-channel-range escape classes
(forms 50-53, with the benign control at form 49); the durable
rejection set is now fifty-one mutation forms. The tenth sweep
(HEAD 5c88ba3) closed the parenthesized-producer,
parenthesized-closure, interface-typed-closure,
alias-typed-function-variable, and type-switch-bound escape classes
(forms 54-58, with the parenthesized benign control at form 59); the
durable rejection set is now fifty-six mutation forms. While
stress-testing the round-4 fixes during the round-5 gate re-review,
the defined-func-type family and its method/nested-callee variants
were closed (self-test forms 60-67), the round-5 struct-field/
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
mutation forms (round-48 closed the bound method-expression and
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
to two hundred twenty-seven mutation forms);
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
hundred thirty-eight mutation forms; round-53 closed the embed
import and //go:embed directive classes (compile-time database
copies), forms 289-290, raising the set to two hundred forty
mutation forms);
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
qualifier translation in aliasLookup; pinned as self-test forms
179-182 (rejects) and 183-184 (benign controls). The durable rejection
set is now one hundred forty-seven mutation forms. The records
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
binding; pinned as self-test forms 185-189 (rejects) and 190 (benign
bytes control). The durable rejection
set is now one hundred fifty-two mutation forms. The records
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
func types; pinned as self-test forms 191-196 (rejects) and 197-198
(benign bytes controls). The durable rejection
set is now one hundred fifty-eight mutation forms. The records
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
qualified registries (finalizeDirAliases); pinned as self-test forms
199-205 (rejects covering both mixed positions, the chan-of-func
variant, the defined-over-alias and alias-over-defined hops, and both
method-result positions) and 206 (benign interface-typed bytes
control). The durable rejection
set is now one hundred sixty-five mutation forms. The records
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
with self-hop guards; pinned as self-test forms 207-210 (rejects)
and 211-212 (benign bytes controls). The durable rejection
set is now one hundred sixty-nine mutation forms. The records
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
import. Pinned as self-test forms 213-217 (rejects) and 218-221
(benign bytes controls). The durable rejection
set is now one hundred seventy-four mutation forms. The records
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
pinned as self-test forms 222-223 (rejects) and 224 (benign bytes
control). The durable rejection
set is now one hundred seventy-six mutation forms. The records
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
embedded-entry argument list. Pinned as self-test forms 225-226
(rejects) and 227 (benign bytes control); forms 228-230 and 232-235 pin the
round-32 cgo-import, raw-syscall, linkname, no-error syscall, and
preadv2/pwritev2 rejects with form 231 the benign lifecycle
control. The durable rejection set is now one hundred eighty-five
mutation forms. The round-36 gate re-review then found the
subprocess-escape class: dup'ing the database descriptor onto stdin and
exec'ing a reader (unix.Dup2 + unix.Exec, /bin/cat) streams file content
out with no banned read call, and a bodyless Go declaration attaches an
assembly syscall body the AST scan cannot see. Fixed by banning the
Dup/Dup2/Dup3/Exec/ForkExec selectors and rejecting bodyless
declarations outright; pinned as self-test forms 236-237. The durable
rejection set is now one hundred eighty-seven mutation forms. The round-36
follow-up re-review then closed the remaining owner-boundary gaps: a new
package could import golang.org/x/sys unseen by the per-target loop
(only the four known packages were checked), and a .s/.syso assembly
object was never scanned (the bodyless-declaration ban was the only link
guard). Fixed by moving the x/sys owner rule into the per-target loop
for every package except internal/mapping and rejecting assembly objects
outright in the scanner walk; pinned as self-test forms 238-239. The
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
allowed), pinned as self-test form 240, raising the set to one hundred
ninety mutation forms. The round-39 re-review then found the
module-graph escape: a go.mod replace directive or a go.work workspace
can attach an out-of-tree module whose files the scanner walk never
visits, letting a wrapper call unix.Pread on the database descriptor
unseen (both vectors exited 0). Fixed by validating the module graph
itself - go list -m all must be exactly this module plus
golang.org/x/sys, and no workspace may be active - pinned as self-test
forms 241-242, raising the set to one hundred ninety-two mutation
forms. The round-40 re-review then found the path-only allowlist gap:
replace golang.org/x/sys => <evil dir> keeps the allowed path in the
graph while loading attacker-controlled code the walk never scans
(proven live with unix.Pread2 reading the database), and the walk
skipped every hidden dot-directory, hiding in-tree replacements. Fixed
by banning all replace/exclude directives, verifying the resolved
x/sys source is the module-cache checkout, and scanning hidden
directories (only .git is skipped), pinned as self-test forms 243-244,
raising the set to one hundred ninety-four mutation forms. The round-42
gate re-review then closed the x/sys source-content gap: the path-only
allowlist accepted a poisoned GOMODCACHE checkout or a file proxy
serving an evil x/sys with a self-consistent forged go.sum (both proven
live with a smuggled unix.Pread2 the ban list cannot know, because
nothing pinned the module content); the gate now pins the exact version,
the module-cache path, the extracted-tree content hash, and the module
zip/go.mod sums to the official v0.35.0 values, and the assembly-object
rejection is case-insensitive, pinned as self-test forms 245-247,
raising the set to one hundred ninety-seven mutation forms. The round-43
gate re-review then found the fail-open listing gap: the per-target go
list ./... loop swallowed listing failures (2>/dev/null), so a module
the go toolchain cannot list - symlinked package files or parse errors -
passed with an empty package list and no import checks (reproduced with
a symlinked smuggled file in internal/mapping); the package checks now
fail closed on every listing error and the per-package import listing
fails closed too, pinned as self-test form 248, raising the set to one
hundred ninety-eight mutation forms. The six-reviewer
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
Pinned as self-test forms 249-252, raising the durable rejection set to two hundred two mutation forms. The
adversarial re-review of that closure then found three P0 escapes in the same producer-value class, all
proven live with full metadata-exemption chains at gate exit 0: (1) a file method value (open := root.Open;
open(name)) escaped the call-receiver ban and the bound method produced an untainted *os.File; (2) a
func-typed variable with an initializer (var newRoot func(string) (*os.Root, error) = os.OpenRoot) lost
its declared file-bearing result type because the type was only consulted for type-only vars; (3) the same
declared-type gap predated the Root work for *os.File (var openPath func(string) (*os.File, error) =
os.Open). Fixed by checking the file method in value position against the approved surface, registering the
declared result type of initialized func-typed variables, and registering stdlib producer values (os.Open
and friends) as func-files wherever bound; pinned as self-test forms 253-256, raising the durable rejection
set to two hundred six mutation forms. The round-48 re-review then
closed bound method expressions on file-bearing receiver types and
same-module cross-package producer vars (forms 257-260, two hundred
ten mutation forms); the round-49 re-review closed nested-
parenthesized, renamed-import, alias-over-renamed, and
wrapper-promoted method expressions, value-bound cross-package
producer vars, and interface-conversion laundering of a file into
the metadata inflater exemption (forms 261-266, two hundred sixteen
mutation forms); the round-50 re-review closed the generic
identity-with-interface-result erasure, the composite-literal field
launder, the instantiated-generic-wrapper method expression, and
the deep embedding-chain method expression (forms 267-270, two
hundred twenty mutation forms). The records
of this pass complete the trail up to this re-review. Repository counts:
production 4,789 raw lines / tests 4,887 raw lines (the round-54
dead-state removal and zero-Pin test account for the latest delta; the gate
scanner lives outside the module). Milestone 2 must not start until a
new independent final review passes; decision 5A was ratified (option A,
2026-08-13); no open user decision remains.
The approved later scope remains unchanged: Milestone 2 is the writer;
sidecars, live coordination, and publication remain Milestone 4.

