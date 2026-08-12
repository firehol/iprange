# SOW-0025 — Milestone 1 Report: portable mapped immutable reader

Date: 2026-08-11 (updated 2026-08-12 after review rounds, external audits, and
the hot-path contract implementation). Status: Milestone 1 REOPENED pending
re-review. The round-10 PASS at HEAD 253f9d5 and closure commit at HEAD
1c71299 were invalidated by a fresh independent audit with five P2 findings:
unapproved structured public-API shape, implicit structured semantic
validation, repeated unnecessary hot-path work plus a false report claim,
contradictory closure records, and a raw mapping-file capability plus a
bypassable content-I/O gate. All five are fixed at HEAD ca30026 with
regression pins (NetworkEnrichmentV1Location + decision 5A; decode-only
structured lookup; one page-header decode per visited page and lookup-time
membership record decode; records corrected; Mapping.File removed and the
content-I/O gate extended to ReadAll/Copy forms). The round-11 final
review then failed with one P1 (pin variable reassignment retargets the
view guard; a word read segfaults on released memory) and three P2
(decision 5A unratified; mmap gate bypassable plus a Windows Mapping.File
escape; stale close-out records), fixed at HEAD 2fd6cae with the
cross-reader reassignment regression test; the records were corrected in
the following commit. The round-12 final review then confirmed the
lifetime fix but found two remaining P2: decision 5A still unratified,
and the mmap source gate still bypassable (x/sys descriptor reads, bufio
wrappers, dot imports, build-tagged packages). Both were fixed at HEAD
4fdc671 with a whole-tree selector scan, dot-import and bufio import
bans, a durable --self-test mode rejecting nine mutation forms, and the
runtime strace evidence below. Milestone 2 must not start until a new
independent final review passes.
Owning SOW: `.agents/sow/current/SOW-0025-20260811-pure-go-exact-v4-port.md`
(Status: in-progress).

## 1. TL;DR

- The pure-Go portable immutable reader is implemented, mmap-only, with the
  exact two-level design: `internal/format` (wire codecs, single error-code
  table), `internal/mapping` (the only mapping owner), `internal/reader` (the
  only healthy-generation reader core), and a public facade at the module
  root.
- The Go reader **opens and semantically verifies all six committed
  Rust-produced fixtures** (direct-v4, full-IPv6 first-seen, membership-v4
  with 70 feeds, membership-v6, structured-v4 with threat memberships, and
  structured-v4-no-threat) and
  rejects all three invalid mutations (wrong-magic, short, unaligned) with
  the exact `format-invalid` code required by the corpus. Multi-level range
  trees and multi-leaf blob bitmaps are not present in the committed corpus
  and are exercised by synthetic databases built in Go tests.
- Warm point lookups, membership word reads, feed lookup, direct scans, and
  cardinality allocate **zero Go heap bytes** (measured; the only public
  exception is the returned feed-name string copy).
- Cross-compilation passes for darwin/amd64+arm64, freebsd/amd64+arm64,
  windows/amd64+arm64+386, linux/arm64+386. Windows is an explicit honest
  stub (open refuses with `os-unsupported`) until the platform milestone.
- **Worker conclusion (corrected, spec-text authority, section 11):**
  `runtime/debug.SetPanicOnFault` recovers mapping faults on linux/amd64
  (empirically reproduced) but cannot satisfy the normative worker
  contract — SA_SIGINFO + alternate stack, kernel-generated si_code
  discrimination, in-region si_addr, exact previous-disposition chaining,
  and no unwinding are all unimplementable in pure Go. The worker
  milestone needs either a minimal project-owned assembly sigaction shim
  (spec-exact, Rust-mirror) or an explicit spec change; the user resolved
  this as decision 2A: the project-owned assembly sigaction shim (SOW
  resolved-decisions log, 2026-08-12).
- Commits: `913f4e6` (reader + tests), `9441f85` (independent-review
  repairs), `1df90fa` (milestone report), `4eec44e` (third-pass repairs,
  superseded worker conclusion), `03a910f` (fourth-pass repairs: lifetime
  redesign, view semantics, absence vs corruption, concurrency tests, report
  facts), `1e1ac4b` (fifth-pass repairs: meta tail invariant, mandatory aux,
  exact zlib stream, namespace under lock, structured decode at lookup,
  adversarial suite), `9a835e4` (six-agent gap analysis). No tracked file
  was deleted in this commit range (Decision 1 was decided after evidence;
  the 105-file 1A deletion landed later in the final milestone-1 commit).

## 2. Commands and factual results

```
go test ./...                                  ok (4 packages: root, format, reader, mapping)
go test -race ./internal/format ./internal/reader ./internal/mapping .   ok
go vet ./...                                  clean
gofmt -l .                                    clean
GOOS/GOARCH builds (darwin, freebsd, windows, linux arm/386): all ok
check-import-graph.sh --self-test: 9/9 mutation forms rejected
runtime mmap-only trace (strace -f, linux): openat -> F_OFD_SETLKW ->
  mmap(MAP_SHARED, db fd) -> munmap -> F_OFD_SETLK unlock -> close, with
  zero read/pread64/readv/preadv/lseek on the database descriptor
  (the only read() calls target /sys and /proc files of the Go runtime)
conformance test: 6/6 fixtures pass (incl. the no-threat structured fixture), 3/3 invalid mutations pass
zero-allocation: 12 public checks + 8 internal checks, all at exactly 0
allocations/run for every measured hot path (reader-level direct
v4/v6/scan/cardinality; pinned membership v4/v6-inline/contains/word/
readwords, structured v4/threat, feed-lookup-into); atomics exist only
at Pin/Close boundaries (reader-level LookupFeed keeps its spec-mandated
copied string: 1 alloc)
```

Production LOC measured at HEAD (recomputed after every repair pass;
`find . -name '*.go' ! -name '*_test.go' | sort | xargs cat | wc -l`
inside v4/go: internal/format + internal/mapping + internal/reader plus
doc.go, errors.go, reader_public.go, types.go): 4,771 raw lines, including
blanks; new-tree tests (`find . -name '*_test.go' | sort | xargs cat | wc -l` over the same
tree): 4,832 raw lines. The earlier
6,160 figure mixed production and test files and is superseded, as are the
~3,720/~1,700 snapshots from the first passes.

### Close-out fixes (external audit round, 2026-08-12)

- mmap-only gate hardened (round-12/13): the content-transfer scan is a
  whole-tree find over every production Go file (build-tagged files for
  every platform included), matching word-boundary selectors for calls,
  method values, function aliases, Seek, x/sys descriptor reads
  (Readv/Writev/Preadv/Pwritev), and byte-oriented readers; dot imports
  and the bufio / io-ioutil wrapper imports are banned outright; the
  Windows mapping stub no longer carries or exposes a raw `*os.File`
  (Mapping.File removed on every platform); `--self-test` durably
  rejects nine mutation forms (direct call, alias, method value, Seek,
  new directory, unix.Readv in the mapping owner, bufio wrapper, dot
  import, windows-only package).
- view-lifetime guard (round-12): public views retain the immutable
  `*pinState` captured at creation, so reassigning the Pin variable that
  created a view cannot retarget its close guard to another reader; the
  cross-reader reassignment regression test pins WrongState instead of a
  use-after-unmap crash.
- Obsolete `RetentionTag` removed (no compatibility alias, spec
  binary-format-v4.md:311; milestone-0 report had classified it for
  deletion).
- Pin values now share one private close state; value copies cannot
  double-decrement the reader pin count (value-copy tests pin it).
- Public `DirectSemantic` registry aligned with Rust: Generic=1,
  FirstSeen=2, LastSeen=3.
- `ThreatMembership` returns `(MembershipView, bool, error)` for the
  canonical no-threat absence result; corpus gained the Rust-produced
  `rust/structured-ipv4-nothreat.iprdb` fixture (6 fixtures total).
- Error codes and Cardinality129 are single-authority in
  `internal/format`; the public package re-exports aliases.

## 3. Structure and owners

- `internal/format`: constants, little-endian codecs, meta identity + kind
  invariants, page header + slotted pages, range/catalog/membership/
  structured/blob/metadata chunk codecs, 129-bit cardinality (verified
  transfer), and the single-authority error-code table 1–69 (code 46 named
  per the current Rust contract, 65–69 added).
- `internal/mapping`: the only owner that maps/unmaps persistent content;
  regular-file, symlink-free, page-aligned geometry; shared lifetime lock;
  checked views; `!windows` POSIX implementation + honest Windows stub.
- `internal/reader`: the only owner of healthy selected-generation reads;
  bootstrap selection (4.2), exact-size immutable check, range/catalog/
  membership/structure/metadata cores; no complete page ever exists in Go
  heap. Membership dictionary records are validated and decoded during the
  owning lookup and word reads slice the retained checked bitmap; structured
  payloads are decoded during lookup with no implicit semantic validation
  (normal operations never invoke Validate), then the scalar value is
  retained in the lightweight view while threat membership remains lazily
  resolved.
- Root facade `reader_public.go`: `OpenImmutable`, `Info`, `DirectSemantic`,
  `LookupDirectV4/V6`, `DirectRangesV4/V6`, `Cardinality`,
  `LookupFeed`, `LookupMembershipV4/V6` + `MembershipView`
  (`Word`/`ContainsIndex`), `LookupNetworkEnrichmentV1V4/V6` +
  `NetworkEnrichmentV1`/`NetworkEnrichmentV1Location` + view
  (`Value`/`ThreatMembership`), `MetadataJSON`, `Close`, plus the public
  `StructureKind`, `ValueKindStructured`, `MetaSelection`, `DirectSemantic`
  types and error codes 65–69. Cursors and queries remain Milestone 3 scope
  per the approved plan.

## 4. Conformance evidence

`v4/go/conformance_test.go` mirrors the Rust corpus verifier against
`cases.json`:

- all six fixtures open with `ProvenCurrent` meta selection and exact info
  (family, kind, structure kind, tag semantics: `first_seen` → FirstSeen);
- metadata states verified exactly: absent (nil), empty (0 bytes present),
  text (byte-exact JSON), repeat (1 MiB of byte 0x78);
- cardinality strings exact, including
  `340282366920938463463374607431768211456` (full IPv6, both fixtures);
- per-range probes at `from`, `to`, midpoint, and `from-1` boundary (no
  leakage beyond ranges);
- all 70 named feeds of the v4 membership fixture resolve with exact indices
  (including index-5 `feed-reused`);
- membership bitmaps verified at word level (inline words 0/1 across the
  64-bit boundary and ContainsIndex for 5-combination bitmaps);
- blob-backed membership verified at word level: the committed fixtures
  store every bitmap inline (v4 fixture: 2 words; v6 fixture: 1 word), so
  the official corpus cannot exercise multi-page blobs. A synthetic v4
  membership database with a 600-word (4,800-byte) bitmap in two blob leaves
  under one blob branch is hand-built in `internal/reader/blob_test.go` and
  read through the public path, including words crossing the leaf boundary
  and the binary-search branch descent;
- structured values verified field-exact (ASN, country/state/city, location
  flag + microdegrees, threat feeds per range);
- all three invalid mutations rejected with code 32 (format-invalid), with
  mutations byte-identical to the Rust generator (`X` in both meta pages,
  1-page truncation, single appended byte).

## 5. Transfer verification

- `key.go` (IPv4/IPv6 numeric hi/lo + LE u128 low-first): matches spec §2;
  used as the facade alias types.
- `cardinality.go`: verified 129-bit arithmetic (Add/Sub/String/inclusive)
  against §17 with literal vectors in `format/format_test.go` (0, 2^128,
  2^128−1, 2^129−1, full IPv4 space, overflow/underflow).
- `page.go`: offsets match §5 (magic@0, type@4, flags@5, header_size@6,
  born_txn@8, item_count@16, level@18, lower@20, upper@22, aux@24, crc@28);
  the new codec is an independent implementation pinned by the cross-open.
- `name_binding.go`: `IPR4NAME` domain and encoding kinds 1/2 verified
  against §3 (reused conceptually; no production path uses it yet in M1).
- `errors.go`: the full 1–69 table now exists in `format/codes.go` with the
  exact current names; code 46 is `LiveCoordinationMalformedRequiresReset`
  (the old Go name is obsolete); codes 65–69 added publicly.
- The obsolete `internal/exactv4` sources are deleted per Decision 1 in the
  final milestone-1 implementation commit (105 tracked files; see
  Deletion (1A)).

## 6. Zero-allocation evidence

`v4/go/zeroalloc_test.go` (public surface, warmed, `AllocsPerRun(400)`) +
`internal/reader/zeroalloc_test.go` (internal surface, warmed,
`AllocsPerRun(200)`):

| Operation | Allocations |
|---|---|
| direct lookup v4 (12 probes) | 0 |
| direct lookup v6 (4 probes) | 0 |
| membership lookup v4 (incl. view) | 0 |
| membership lookup v6 (inline bitmap) | 0 |
| ContainsIndex / Word / ReadWords (inline + blob, incl. synthetic blob DB) | 0 |
| structured lookup v4 | 0 |
| feed lookup (internal) | 0 |
| feed lookup (public) | 1 (returned name string copy; documented) |
| full direct scan v4 | 0 |
| cardinality scan | 0 |

Metadata decompression allocates the returned payload plus a measured
8 allocations/run and ~40-50 KiB/run of decompressor overhead (re-measured
at HEAD on the 1 MiB fixture and a small fixture; the bytes/value contract
itself is unchanged: caller value bounded by the 20 MiB limit).

## 7. Malformed/corruption evidence

- `internal/format/meta_test.go`: 10 identity-corruption vectors + kind
  invariant matrix (roots, counts, reserves, metadata lengths, txn).
- `internal/reader/reader_test.go`: bootstrap selection matrix — sole-meta
  selection, both-damaged rejection, conflicting identity, unsupported
  structure (code 67), txn gap, equal-txn-differing-images, symlink refusal,
  reserved basenames, external-sidecar refusal, exact-size rejection.
- Structural safety: every descent validates level decrease, page type, aux,
  slotted bounds, and page-number ranges before following pointers; level
  bounds cap recursion; no record can extend past its mapped page.

## 8. Portability

- Compiled: linux amd64/arm64/386, darwin amd64/arm64, freebsd amd64/arm64,
  windows amd64/arm64/386.
- Tested at runtime: linux amd64 (this host).
- Windows: `mapping_windows.go` stub refuses every open with code 58
  (os-unsupported) and is explicitly marked unreachable until the platform
  milestone. macOS/FreeBSD mapping shares the POSIX owner; runtime proof
  requires authorized native hosts (Milestone 4 per plan). Big-endian codec
  proof (s390x) is still required by later milestones.
- FreeBSD live coordination remains a later milestone; immutable opens on
  FreeBSD use the canonical whole-file shared flock lifetime lock (restored
  in the round-2 fix pass), Linux/macOS use the OFD byte-range lock, and the
  Windows stub refuses with a typed error.

## 9. Review findings and repairs (2026-08-11, second pass)

An independent first-principles review of the milestone diff found three
blocking issues, all verified and fixed, plus two nits and one report
correction:

1. Blob-branch descent passed `selectedTxn = 0` into `OpenSlotted`
   (`membership.go`), which rejected every multi-page blob (every committed
   page has born_txn >= 1); single-leaf blobs were unaffected, so the
   committed fixtures could not catch it. Fixed by threading the selected
   transaction; regression-tested by the new synthetic 2-leaf blob database
   (`blob_test.go`). The blob leaf item-count check was also corrected to
   apply to leaves only (branches legitimately hold many entries).
2. Crafted slotted pages with `upper` or slot offsets beyond the page size
   could panic the process with a slice-bounds fault instead of a typed
   error (the old overflow guard was dead code over a uint16). Fixed in
   `format/page.go` (lower/upper bounded to PageSize, slot offsets bounded);
   regression tests craft `upper = 60000` and `slot = 60000` pages and
   require typed rejection.
3. Metadata read pre-allocated from the wire `metadata_compressed_len`
   without a bound. Fixed twice: bootstrap now enforces the section-11
   compressed bound (`compressed <= bound(uncompressed)`) as part of meta
   invariants, and the reader caps the pre-allocation by the physical page
   count as a second defense. Regression tests cover both.
4. Catalog name records: the three reserved bytes after `name_len` are now
   checked (format/catalog.go) with unit vectors.
5. Dead `parseV6` helper removed from the conformance test.
6. Report correction: the committed v6 fixture's bitmap is inline (1 word);
   the 1 MiB object in that fixture is the uncompressed metadata, not a
   bitmap. The report no longer claims a corpus-exercised multi-page blob;
   that claim now rests on the synthetic blob test.

Review verdict on the repaired tree: codec layouts, bootstrap rules,
selection matrix, error table 1-69, and the zero-allocation claims for
lookups/scans were checked against the normative spec and Rust sources with
no remaining actionable finding in the milestone scope.

## 10. Review findings and repairs — third pass (2026-08-11)

A second external reviewer (codex) returned ten claims. Every claim was
verified against the current tree, the spec, and the Rust sources before any
action; five were real, two were already fixed, two were test/report gaps,
and one (SetPanicOnFault) was a correct refutation of this report's worker
conclusion (section 11).

Real issues found, fixed, and regression-tested in this pass:

1. **Missing wrong-mode pre-checks.** The public queries performed no value
   kind / address family validation, so a direct lookup on a membership file
   returned the raw internal membership ID, a membership lookup on a
   structured file returned page-derived data, and a v6 query on a v4 file
   surfaced mid-page decode corruption errors instead of the typed error.
   Rust performs these checks before touching any page
   (`reader_core/generation.rs:257-276` require_direct /
   require_membership_family, `membership_view.rs` require_kind,
   `structured_value/view.rs:155-170` require_kind, `feed_catalog.rs:214`
   require_membership). The Go facade now mirrors each rule exactly: wrong
   kind → WrongValueKind (13) except network enrichment → WrongStructureKind
   (67); wrong family → WrongAddressFamily (12); feed access requires kind
   Membership or Structured. An 18-probe wrong-mode matrix (12 error-asserting + 6 positive)
   pins all cases on the committed fixtures (conformance tests).
2. **Child-handle safety.** `Close` unmapped the file while public views
   still held page descriptors, and any later view operation could fault.
   The Rust contract (close with live children → ErrorHandleBusy; operations
   on a dropped borrow are impossible) has no Go runtime analog, so the
   public reader now owns a fixed 1024-slot handle registry (no heap state):
   every view lookup registers a handle, view operations verify liveness
   (ErrorHandleClosed after release or close), `Close` returns
   ErrorHandleBusy while views are alive, and `Release` is idempotent. A
   threat view derived from a structured view is an independent handle,
   exactly like Rust borrow semantics. Zero-allocation is preserved: the
   registry is a bitset in the reader value.
3. **Public membership-ID exposure.** `MembershipView.ID()` returned the
   internal membership ID, contrary to the agreed API ("never exposes
   membership IDs"). Removed from the public surface.
4. **Callback-error corruption.** `publicError` converted any non-format
   error into FormatInvalid, so an error returned by a scan callback was
   re-reported as database corruption. Non-format errors now pass through
   unchanged.
5. **Conformance gaps.** The IPv6 midpoint of direct and membership ranges
   was computed but discarded (now asserted); the "undeclared feeds must be
   absent" note was a comment (now asserted for every declared but unlisted
   feed of every membership range); exact range enumeration (count, every
   record, ascending order, total cardinality) is now compared against
   `cases.json`; declared-feed count is checked against the meta; and the
   corpus's single-level range trees are supplemented by a synthetic
   900-record / four-leaf / level-1-branch IPv4 database exercised through
   the public path (multi-level blob coverage had already been added in the
   second pass; the claim in that pass that the committed v6 fixture
   exercises blob trees was corrected).

Already fixed before this pass (stale claims): the slotted-page bounds
panic and the blob branch txn-zero bug were both repaired in the second
pass with regression tests; codex reviewed the pre-repair tree.

Review verdict on the third-pass repairs: same-failure searches re-run
(no other slotted access paths, no other handle-holding surfaces, no other
wrong-mode entry points), full suite green including race and vet.

## 10b. Review findings and repairs — fourth pass (2026-08-11, second codex review)

A second codex review returned nine numbered findings. All were verified
against the tree, the spec, and the Rust sources; all nine were real (one
also corrected a wrong factual claim in this report). Repairs:

1. **Handle registry replaced by borrow-count lifetime (API redesign).** The
   1024-slot token registry was unapproved, non-concurrent (codex
   reproduced 19 data races in register/alive/release), and had a free-slot
   exhaustion defect in its bit-scan. The spec requires concurrent lookups
   and scans without a per-call mutex, atomic, or active counter
   (design-iprange-engine.md §401) and requires Close → HandleBusy while
   children exist (binary-format-v4.md §2537). The registry is deleted. Views
   now carry a pointer to the reader's shared lifetime state; the data
   path (internal/reader) remains completely synchronization-free, and the
   facade's only shared state is an atomic close flag (one load per public
   entry) and an atomic live-view count (one add/sub at view
   creation/release) — the same guard layer the frozen Rust C ABI applies
   around the sync-free engine (iprange-capi view-handle registry). Close
   never unmaps while a view exists; every operation after a successful
   close returns ErrorHandleClosed instead of crashing (regression:
   TestOperationsAfterClose). Token reuse invalidation is impossible (no
   tokens). A concurrent lookups/scans/view-create-release test runs under
   -race on all six fixtures with zero reported races.
2. **Membership view API completed to the mandatory contract
   (binary-format-v4.md §2537):** caller-buffer `ReadWords(start, dst)`
   added (start above length → InvalidArgument); `WordCount` now performs
   the lifetime check; `ContainsIndex` returns InvalidArgument for indexes
   at/beyond the generation's feed-index limit (mirroring
   membership_view.rs, including check order); trailing-zero-word
   corruption check on reads reaching the canonical end (mirrors Rust).
3. **Missing referenced values are corruption.** A range value naming a
   membership ID absent from the ID tree now returns the typed corrupt error
   (mirroring membership_view.rs). The structure twin was claimed repaired
   here but was still a silent clean miss at HEAD; the round-2 review caught
   it and the fix pass landed it (structure.go now returns "range names an
   absent structure ID", pinned by TestMultiLevelStructureTree).
4. **Absence below the first branch key.** Range (v4+v6) and catalog name
   lookups with no qualifying branch entry now return absent instead of
   corruption (binary-format-v4.md §589: "If no key qualifies, the target
   is absent"); the synthetic multi-level database now probes addresses
   below the first branch key.
5. **Test hygiene:** superseded membership views and threat views are
   released; every conformance fixture close is asserted (a leaked view
   fails the cleanup); released-view and after-close contracts are pinned
   by dedicated tests.
8. **Hot-path atomic and wasted decodes.** The facade keeps exactly one
   atomic load per public entry as its binding guard, documented against
   design-iprange-engine.md:404: the sentence constrains the READER core
   (internal/reader remains import-free of sync/atomic, enforced by the
   import-graph gate); the binding guard mirrors the frozen Rust C ABI's
   per-call handle validation, and an atomic load is lock-free. The
   duplicated root-page decode in the tree descents (level discovery then
   re-read) and the structured record re-read are scheduled for removal
   together with the view-lifetime redesign; the structure re-read was
   already eliminated by the decode-at-lookup fix.
9. **Namespace safety.** The sidecar was stat'ed once before opening with
   non-existence errors silently ignored; symlink checks preceded a racing
   reopen; size was not validated against the host address space. Fixed:
   open uses O_NOFOLLOW plus a stat-after-open identity recheck
   (os.SameFile), the sidecar absence decision is re-made under the shared
   lifetime lock (only ENOENT counts as absent; any other stat failure is a
   refused open), the canonical sidecar must fit the filesystem component
   limit (regression with a 253-char basename), and file size is checked
   against the host address space before conversion.

6. **Worker conclusion corrections (this report):** SetPanicOnFault
   applies to the calling goroutine only (not process-global; better
   isolation), and the recovered fault address is documented best-effort
   and platform-dependent — both recorded with the remaining platform
   proofs. The empirical linux/amd64 probe stands.
7. **Report facts:** nonexistent commit `3bbbf4e` corrected to `4eec44e`;
   the SOW's reference to a nonexistent `check-go-architecture.sh` now
   names the real `v4/go/check-import-graph.sh` gate (created in this
   pass, passes); the summary's superseded blob/worker claims corrected.

Verdict after the fourth pass: full suite (incl. race) green, vet clean,
gofmt clean, import-graph gate green, cross-compilation matrix green.

## 11. Pure-Go fault-worker feasibility — corrected conclusion (2026-08-11)

This section supersedes the earlier provisional conclusion in the second
pass. The second pass's evidence was correct but incomplete: it established
that `os/signal.Notify` never receives mapping SIGBUS and that no pure-Go
API exposes `si_addr`, and concluded that pure Go cannot satisfy the
contract. The third-pass reviewer pointed out that
`runtime/debug.SetPanicOnFault` exists precisely for mapped-file faults and
exposes the fault address. Verified empirically on Linux/amd64 with a
standalone probe (`/tmp/panic_fault`, not committed):

```
mapping base=0x7f108aa90000 size=65536
RECOVERED type=runtime.errorAddressString addr=0x7f108aa91000 in-region=(true)
recovered ok, setpanicoff
```

Findings:

- `debug.SetPanicOnFault(true)` converts synchronous mapping faults into
  recoverable panics on the faulting goroutine; the recovered value is the
  unexported `runtime.errorAddressString`, whose `Addr()` (via reflection)
  is the exact faulting address — for this probe, the truncated page's line
  address inside the armed mapping. In-region claiming is therefore
  possible in pure Go.
- The worker design then mirrors the Rust worker exactly:
  arm (SetPanicOnFault) → operation → recover → if the address is inside
  the armed owned region and the control state is armed: record
  generation/role/relative offset and `unix.Exit(197)`; otherwise
  `debug.SetPanicOnFault(false)` and re-raise to reproduce the pre-worker
  crash behavior. The Rust claim condition (`posix.rs:173-196`:
  kernel_bus_code check, armed/generation/role/len checks, in-region
  si_addr, handling CAS) maps to: the region check covers in-region; a
  readable mapping can only fault with SIGBUS on reads (no writes, PROT
  READ), so an in-region fault is de-facto SIGBUS and the missing si_code
  discrimination is not observable; the CAS maps to the worker loop's
  armed/record-exit discipline.
- Observable contract preserved: owned faults exit with the fixed code 197
  with a fault record; unrelated faults re-raise and die like before (in
  Rust they chain into the previous handler — for a worker inside a Go
  process that previous handler is the Go runtime's own, so the re-raise
  lands on the same fatal path Go processes always had); no mislabeling:
  only in-region addresses produce the owned-fault exit.
- SetPanicOnFault applies only to the calling goroutine (per the official
  Go documentation), which is *better* isolation than Rust's process-wide
  sigaction: one dedicated worker goroutine arms the flag and regular
  process goroutines never see fault panics. The recovered fault address is
  documented as best-effort and platform-dependent, so the worker must
  validate the address (nonzero, inside an armed region) before claiming and
  must be proven on the remaining platforms (macOS, FreeBSD, Windows) in the
  worker milestone.
- Remaining proof (platform milestone): macOS and FreeBSD (same code path,
  needs real-systems verification), Windows via
  `AddVectoredExceptionHandler` + `syscall.NewCallback` (pure Go, already
  feasible by API surface; needs verification), runtime-internal faults
  during an armed operation (both designs crash; observable difference
  only in the message).

**Conclusion (corrected, spec-text authority): the normative worker
contract is not satisfiable in pure Go.** binary-format-v4.md lines
3080-3096 and the engine design spec require SA_SIGINFO with an alternate
stack, kernel-generated (SI_KERNEL) discrimination, in-region si_addr with
checked subtraction, exact previous-disposition chaining, and no unwinding
through SDK code. SetPanicOnFault provides none of these: it converts the
fault into a recoverable panic — an unwinding mechanism the contract
forbids — exposes no siginfo/si_code, is goroutine-scoped, and its
address is documented best-effort. The linux/amd64 probe remains evidence
for a panic-based subset, not for the contract.

Worker milestone decision 2A is resolved: use a minimal project-owned assembly
sigaction shim (SA_SIGINFO, kernel-bus
  check, si_addr interval, previous-disposition chaining, raw exit(197))
that is spec-exact and mirrors the Rust worker posix.rs. The rejected
alternatives were a spec change to panic-based semantics or dropping the fault
worker.

## 11b. Gap-analysis repair pass (2026-08-11, commit 58c4d8f)

The six-agent concurrent gap analysis (commit 9a835e4,
pure-go-m1-gap-analysis.md) produced one BLOCKER and ten MAJOR findings.
All were repaired in this pass, each with a committed regression test:

- B1 structure radix: the directory child index divided by R*512^(L-2)
  instead of R*512^(L-1) at levels >= 2; fixed (span = level), plus the
  synthetic level-2 structure-tree database (TestMultiLevelStructureTree)
  that fails on the old arithmetic. Child==0, id 0, id >= limit, and zero
  slot cells are now clean misses, mirroring table.rs.
- M1 blob walks: branch-first-offset continuity, child-level descent, leaf
  identity/geometry (lower==48+data_len, upper==4096, %8), nonfinal-full,
  end-vs-declared, and coverage of the requested span (blob_tree.rs
  find_leaf + leaf_geometry); exercised by the 2-leaf synthetic blob.
- M2 record limits: feed_index >= limit, membership id 0/>= limit, zero
  refcount, word_count beyond the limit-derived maximum, and out-of-range
  blob roots are corruption (decode_leaf + require_record_fields).
- M3 slotted exactness: lower == 32+2*item_count and upper < page size
  (slotted_page.rs shape_valid); structure pages enforce their fixed
  geometry (leaf_end 4032 / branch_end 2080, upper 4096, item-count
  bounds); all synthetic builders were made canonical (the blob builder
  previously wrote pages the Rust reader would reject).
- M4 lifetime lock: whole-file flock(2) replaced by the OFD byte-range
  lock at offset 1<<44 with len 1 (live_sidecar.rs MAIN_LIFETIME_LOCK);
  a held-lock exclusion test proves a concurrent writer is now excluded;
  freebsd/other-unix mirror the Rust platform table (typed unsupported).
- M5 binding: Info() is guarded and returns (DatabaseInfo, error);
  public code 46 renamed to LiveCoordinationMalformedRequiresReset; the
  feed name is validated before the kind check (feed_catalog.rs order).
- M6 conformance evidence: info assertions (value tag vs cases.json,
  MetaSelection==ProvenCurrent, page_count*4096==file size, range/feed
  counts); absence probes at family edges and inter-range gaps for
  direct/membership ranges; word-exact bitmap verification through
  ReadWords per range; feed-limit InvalidArgument probes.
- M7 reports: LOC recomputed honestly at HEAD (test files excluded; then
  4,255 raw production lines), 18 probes and 16 zero-allocation checks
  replace the stale 19/11,
  SOW worker sentence aligned with the corrected section 11.
- M8 gate: check-import-graph.sh dropped each package's first import
  (mapping->format was silently unchecked); it now checks every import,
  bans sync/sync-atomic/unsafe in format+reader, and encodes the
  mapping->format allowance.
- Bootstrap minima: range-record and retirement capacity bounds, metadata
  physical bound ((page_count-2)*4048), and membership entry_count <
  id_limit were added (bootstrap.rs mirrors), without changing fixture
  acceptance.
- Authority conflict recorded (no code change): unknown structure_kind on
  direct/membership files — the Go reader follows the spec text
  (UnsupportedStructure) where Rust reports NoBootstrapMeta; decision
  tracked for the conformance milestone.

Gates at commit: go test ./... (5 packages ok), -race, vet, gofmt clean,
check-import-graph.sh passed, cross-builds darwin/freebsd/windows/linux-386.


## 11c. Round-2 six-agent review and fix pass (2026-08-12)

Per the mandatory iterative review gate, six concurrent reviewers with
disjoint briefs (codecs / bootstrap+ranges / membership+structured+metadata /
mapping+lifetimes+platform / public API+errors+zero-alloc /
conformance+reports) reviewed the tree from an independent HEAD. Verdicts:
4 FAIL + 1 FAIL on paperwork + 1 PASS-with-2-P1 (both P1s are the already
recorded closed-state error-class user decision, precisely characterized).
No P0. Findings and fixes, each with a committed regression test:

- Catalog records with `feed_index >= feed_index_limit` were served instead
  of rejected as corruption (load-bearing Rust rule absent on the Go read
  path). Fixed in `nameLeafLookup`; pinned by `TestCatalogFeedIndexLimit`.
- Kind classification was wrong for registered-but-invalid combinations:
  structured kind 0 and direct/membership kind 1 reported
  UnsupportedStructure (67) where the spec/Rust classify FormatInvalid (32);
  only unknown kinds keep 67. Fixed in `meta.go`; pinned by
  `TestMetaKindClassification`.
- Dangling structure references (range names an absent structure ID) were a
  silent clean miss; the membership twin was already corruption. Fixed in
  `structure.go`; pinned by `TestMultiLevelStructureTree` (four ids now
  assert code 32).
- FreeBSD refused every open; the spec and Rust support immutable reading on
  FreeBSD with a whole-file flock lifetime lock. Implemented in
  `mapping_lifetime_freebsd.go` (LOCK_SH/LOCK_UN); live refusal remains for
  the coordination path.
- `Mapping.View`/`Page` after Close panicked on the nil slice; `Close` was
  not idempotent. Now typed WrongState + idempotent Close; pinned by
  `TestViewAfterClose`.
- Metadata: zlib FCHECK (RFC 1950 check bits) was never validated (probe:
  0x78 0x9b accepted); `deflateStreamLen` inflated each probe unboundedly
  (CPU amplification on a crafted stream). Fixed; pinned by
  `TestMetadataFCheckRejected`.
- Blob walk: leaf `data_len % 8` now explicit, extent arithmetic checked for
  overflow, and every probed branch entry's child page is validated (not
  only the selected one); pinned by `TestBlobBranchProbedChildValidation`.
- `publicError` lost the typed code through a wrapped `*format.Error`;
  rebuilt from the `errors.As` match. Error-code names 59/62/69 aligned to
  the Rust `Id` spelling.
- Gate: `check-import-graph.sh` now bans content-transfer I/O in production
  sources and the stdlib `syscall` package (the mmap-only contract has a
  mechanical guard).
- Evidence/hygiene: structured conformance absence probes (from-1/to+1/
  family min-max) added to the fixture loop; Info()-after-close pinned; the
  public zero-alloc suite releases its v6 view; literal byte vectors added
  for v6 range, membership leaf/branch, structure record, enrichment
  payload, and blob-branch codecs; report LOC/labels corrected (production
  4,492 raw / tests 3,794 raw; v6 bitmap is inline in the committed fixture);
  this report's false repaired-claim and the SOW log's stale worker and
  planning-era validation text repaired.

Gates after the pass: `go test ./...` (5 packages), `go test -race`,
`go vet`, `gofmt -l`, `check-import-graph.sh`, and the 9-target
cross-compile matrix (darwin/amd64+arm64, freebsd/amd64+arm64,
windows/amd64+arm64+386, linux/386+arm64) — all green.


## 11d. Round-3 verification pass (2026-08-12)

All six reviewers re-reviewed the round-2 tree; this pass records the
findings that landed between the round-2 record and HEAD, and the final
verification verdicts:

- `a5d7cf8` — import-graph gate: comment stripping for the content-transfer
  ban, later extended (`78cebc4`) to stateful whole-file stripping so
  multi-line `/* */` comments cannot false-positive (verified in both
  directions on planted files).
- `9203c28` — regression pins for the round-2 fixes: `TestSoleMetaGeometry`
  (sole-meta selection when one meta's page_count exceeds the physical
  length) and the membership-kind1/kind2 classification subtests; both
  provably fail on the pre-fix code.
- `e0a1687` — blob path zero-allocation: `blobRead`'s escaping
  expected-level pointer became a value pair; the internal zero-alloc suite
  now measures blob word and scan at 0 allocs.
- `a348c42` — conformance absence probes made real: the IPv4 family-min
  condition compared the always-zero IPv6 high word (dead code), and
  from-1 probes were missing; both are now live for membership and
  structured loops. The SOW log gained the missing `94723aa` line.
- `3b4f3d5` — P0 fix found by the round-3 codecs review: the round-2
  "checked arithmetic" coverage rewrite underflowed when a request offset
  fell past the selected leaf's end (`end-off` wrapped), turning a
  corrupt-file rejection into a silent out-of-leaf word read or a slice
  panic. The explicit `off > end` guard restores corruption semantics;
  `TestBlobGapRejectedCorruption` fails on the pre-fix code in both modes
  and passes now. Reviewer-1 re-ran both original crash reproductions
  against HEAD: both return typed code 32.
- Final round-3 verdicts: codecs PASS (0/0/0), bootstrap/ranges PASS
  (0/0/0), membership/structured/metadata PASS (0/0/0), mapping/lifetimes
  PASS after the gate fix (0/0/0), public API/errors/zero-alloc PASS
  (0 fixable outside the recorded closed-state decision), conformance/
  reports PASS after the round-3 record and count corrections (0/0/0).
  At this historical checkpoint the closed-state error-class choice
  (HandleClosed vs WrongState), deletion set, and worker boundary remained
  pending; they were subsequently resolved as decisions 3A, 1A, and 2A.


## 11e. External audit pass (2026-08-12)

An external audit reported six correctness/coordination failures and
performance waste; all claims were verified against code, spec, and the
Rust reference, all six reproduced, and all fixed with regression tests
(view-copy borrow double-release; sidecar Lstat + WrongState class;
blocking F_OFD_SETLKW lifetime lock; three-point path identity recheck;
structure_entry_count < structure_id_limit; catalog branch-key grammar).
Performance: single-inflation metadata validation (~1.4x on the
micro-benchmark), batched ReadWords (one decode/walk per batch), single
page-header decode per visited page (OpenSlottedHeader at every slotted
call site) with the root pre-read removed, inline membership word reads
served from the lookup-time record decode (no per-word record re-decode),
and a quote-aware gate stripper. Production LOC and test
LOC grew with the new regression tests; the public zero-alloc contract
documents exactly one guard allocation per created view (copy-safety).

CORRECTION (recorded 2026-08-12, after the round-4 closure): the earlier
claim that the public per-call atomic is "parity with the frozen C ABI
(handle.rs Gate::enter gates every C call)" is false in its mechanics.
Verified: Rust reader lookups go through ReaderHandle::get — a plain
Option check with no gate and no atomic (iprange-capi handle.rs:551); view
handles alone carry the AtomicBool fail-fast gate (handle.rs:69-74, caller-
serialized per c-abi-v4.md:178), and the C-ABI lookup additionally pays one
Arc::clone (atomic refcount) and one Box per view-handle. The binding Go
criteria are SOW-0025:175 ("warm successful point lookups and cursor steps
allocate zero Go heap bytes") and design-iprange-engine.md:373/:404; the
round-4 zero-alloc suite documents one guard allocation per view and one
string copy per feed lookup, so those criteria are NOT met by the round-4
  public facade. At this historical checkpoint this was the open hot-path API
  decision (section 11i); it was subsequently resolved as decision 4A, not by
  reinterpreting the round-4 facade as compliant.

## 11f. Round-4 reviewer follow-up (2026-08-12)

The round-4 external-audit reviewer for bootstrap/mapping asked for two
regression tests that were still missing: the blocking F_OFD_SETLKW wait
semantics and the post-lock/post-mmap path identity recheck. Writing the
identity test exposed that the shipped "three-point" recheck compared the
opened fd against the initial path stat — a comparison that can never fail
once the fd is open — so a replacement after open could still publish a
mapping of the old unlinked inode while the path named a new database.
The mapping owner was corrected to re-stat the path itself with
`os.Lstat` (symlink-aware, like Rust `fs::symlink_metadata`) at every
check point and require it to still name the opened inode; a mismatch or a
non-regular path entry under the lock is the WrongState class (code 11),
matching Rust `WrongMode` ("live path identity changed"). The initial
pre-open stat remains only as the early non-regular-file gate (FIFO and
directory refusal); it no longer vetoes the opened file, matching Rust's
open-what-the-path-names semantics. The darwin lifetime lock additionally
retries EINTR in the wait loop, matching the linux peer and the Rust
`live_lock` platform module (linux+apple use one shared loop).

Regression tests added at `v4/go/internal/mapping/mapping_test.go`:
`TestOpenImmutableWaitsForExclusiveLifetimeLock` (an exclusive contender
at the lifetime offset blocks the open until released; fails pre-fix on a
non-blocking lock) and `TestOpenImmutableRefusesPathReplacedDuringOpen`
(fails on the pre-fix tree, passes after the path-vs-inode correction).

Counts at HEAD 4950366: production 4,592 raw lines / tests 4,196 raw lines.

## 11g. Round-4 membership follow-up (2026-08-12)

The membership/structured/metadata reviewer found one remaining P0 in the
batched blob-membership read: the ba09f31 "one walk per batch" blob case
still issued a single span request, so any batched `ReadWords` crossing a
blob-leaf boundary failed as corruption even on a conforming file (the
synthetic two-leaf blob database reproduced it exactly: words 0..505 in
leaf 1, 506..599 in leaf 2). The Rust reference (`blob_tree.rs
read_words_from`) loops per leaf. The Go reader now mirrors it: the blob
traversal was split into `blobLeaf` (one descent to the covering leaf,
returning its mapped bytes and logical start) and the single-span
`blobRead` wrapper; the batched path loops per covering leaf, copying
`min(available, remaining)` words and advancing, with the no-advance
guard and the trailing-zero-word canonical check preserved. Regression
test `TestBlobReadWordsAcrossLeafBoundary` fails on the pre-fix tree with
the exact reported error ("blob leaf does not cover the requested bytes",
code 32) and passes at HEAD; `Word` per-word reads and the
zero-allocation blob subtests stay green.

Counts at HEAD: production 4,634 raw lines / tests 4,252 raw lines.

## 11h. Round-4 mapping P2 follow-up (2026-08-12)

The mapping/lifetime reviewer's re-verification found one remaining P2:
when the path is unlinked (not replaced) mid-open, the post-lock
`os.Lstat` failure mapped to `CodeIO` (31), while Rust
`verify_path_inner` refuses with `NameNotFound` (18). Fixed at
`v4/go/internal/mapping/mapping.go` by mapping `os.IsNotExist` to
`CodeNameNotFound` before the IO fallback; regression test
`TestOpenImmutableRefusesPathUnlinkedDuringOpen` (unlink inside the check
callback, expect code 18) added. The conformance/reports reviewer's P2 was
pure record lag: the round-3 "counts corrected at HEAD" sentence in the
SOW external-audit entry is now explicitly annotated as superseded by the
round-4 follow-up entries, section 13 now lists section 11e in the review
history, and each historical "Counts at HEAD" line carries its commit.

Counts at HEAD: production 4,639 raw lines / tests 4,308 raw lines.

## 11i. Reopened: public hot-path contract (2026-08-12, after round-4 closure)

An external re-review after the round-4 gate closure re-examined the
public facade against the frozen performance contract. Verified with the
report's own measurement method at HEAD:

- Membership lookup + Word + Release: 1 heap allocation (16-byte
  viewGuard) and two atomic operations per lookup (closed-state load +
  child-view add); every view operation adds one atomic load; Release adds
  a CAS plus a counter add.
- Feed lookup: 1 heap allocation (the returned name string) plus one
  atomic load.
- Direct lookup and direct scan: 0 allocations, but still one atomic load
  per call (ensureOpen).
- SOW-0025:175 requires warm successful point lookups and cursor steps to
  allocate zero Go heap bytes; design-iprange-engine.md:373 requires a
  warmed lookup to allocate nothing and return borrowed data or write into
  caller storage; design-iprange-engine.md:404 lets reader lookups and
  independent scans run concurrently without a per-call mutex, atomic, or
  active counter. The round-4 facade meets none of the three as written.
- The claimed Rust parity was wrong in mechanics (see the 11e
  correction): Rust's reader lookup is a plain Option check; the SDK core
  is atomic-free and allocation-free. The Go internal core already is too;
  only the public facade layer diverges.

Metadata staging was also avoidable: the 1 MiB fixture inflated with ~2.3
MiB of cumulative allocations (worst-case bound reserve + io.ReadAll
doubling growth) although the exact compressed and uncompressed lengths
are declared in the selected meta. Fixed at internal/reader/metadata.go:
the compressed chain allocates exactly its declared length (bootstrap
bounds make it a safe capacity) and decompression reads into one exact
output allocation with a one-byte overflow probe; truncation, trailing
bytes, and Adler checks are unchanged and pinned by the existing tests.
No API change.

The API shape that closes the gap is a user decision (section 13 pending
decision 4): caller-owned pinned lookup handles with token-style views
(zero allocations and zero atomics inside the loop, one lifetime
registration outside it) vs keeping the guard facade with a SOW/spec
amendment.

## 11j. Decisions 1A-4A implementation (2026-08-12)

All four decisions were recorded in the SOW and implemented:

- **Deletion (1A).** The verified scalar types were relocated in-line into
  `v4/go/types.go` (address/value families, IPv4/IPv6, ValueTag,
  Cardinality129 — byte-identical semantics, `TestPublicSemanticFoundation`
  unchanged and green); `v4/go/types.go` no longer imports the obsolete
  package. The obsolete tree was then removed in the final milestone-1
  implementation commit: 105 tracked files (47 production + 58 test — the
  previously recorded "100" count was stale) plus the untracked
  `v4/go/exactv4.test` binary and the empty `v4/go/exactv4/` directory.
  `.reasonix/` untouched; git history preserves the deleted sources; the
  import-graph gate no longer exempts the package.
- **Worker boundary (2A).** Recorded: a minimal project-owned assembly
  sigaction shim (SA_SIGINFO, kernel-bus check, si_addr interval,
  previous-disposition chaining, raw exit(197)), validation/recovery
  workers only, proven natively per POSIX platform in the worker milestone.
  No Go runtime code, cgo, or unwinding inside the handler.
- **Closed class (3A).** Closed readers and closed pins report WrongState
  (11); second Close reports WrongState; numeric code 9 stays reserved;
  `MembershipView.WordCount` is now `(uint32, error)`; per-view Release no
  longer exists; the HandleBusy contract moved to pins.
- **Hot-path facade (4A).** `ImmutableReader.Pin` registers one lifetime
  child outside the workload; one pin may be shared across concurrent
  immutable lookups. Lookups, scans, view operations, and the caller-buffer
  feed lookup (`LookupFeedInto`, BufferTooSmall + required size) are
  zero-allocation and zero-atomic: measured at 0.000000 heap bytes/run for
  pinned membership lookup+Word, feed-into-buffer, direct lookup, and
  pinned enrichment lookup+Value; atomics exist only at Pin/Close
  boundaries. Views are lightweight values valid while the pin is open.
- **Structure-kind authority conflict.** Resolved and pinned: direct and
  membership files reject ANY nonzero structure kind as the KindInvariant
  class (FormatInvalid); only a structured file with an unknown nonzero
  kind reports UnsupportedStructure (binary-format-v4.md:430 override;
  Rust bootstrap.rs validate_direct/validate_no_structures/finish_open).

## 12. Deviations and open items

- Deletion executed per decision 1A (see section 11j): the obsolete
  internal/exactv4 tree (105 tracked files + the untracked test binary and
  empty directory) is removed; git history preserves the sources.
- The verified scalar types (`IPv4`, `IPv6`, `Cardinality129`, `ValueTag`,
  address/value families) live in `v4/go/types.go`; `ErrorCode` lives in
  `errors.go`.
- `LookupFeed` returns a Go string (one copy); the internal path is
  zero-alloc. Cursors, namespaces beyond basename rules, windows mapping,
  darwin/freebsd runtime proof, big-endian vector execution, and the
  conformance mixed-process gates are later milestones per the approved plan.
- Metadata bytes and feed names are caller-visible values; their heap copies
  are the contract's "bounded encoded records", not pages.

## 13. Milestone 1 close-out

Acceptance criteria evidence: portable codecs (literal vectors), mapping
owner (geometry/lifetime/lock), public immutable reader, all six Rust
fixture cross-reads with `cases.json` semantics, malformed bootstrap
rejection, zero-allocation lookups/scans (incl. the blob path), first
platform/worker feasibility report — all executed and recorded above.
Review history: sections 9/10/10b record the independent second-fourth
passes; section 11c records the round-2 six-agent review and its repairs;
section 11d records the round-3 verification pass (including the blob-gap
underflow P0 and its regression test) and the six final PASS verdicts;
section 11e records the external audit pass and its repairs;
section 11f records the round-4 mapping follow-up (path identity recheck
correction, darwin EINTR parity, and the two requested regression tests);
section 11g records the round-4 membership follow-up (per-leaf batched
blob reads, failing pre-fix on the two-leaf fixture); section 11h records
the round-4 mapping P2 follow-up (deleted-mid-open NameNotFound parity)
and the report-record corrections. The round-4 external audit and its
three follow-up rounds ended with all six reviewers PASS at HEAD 2a03554.
The milestone was then REOPENED by an external re-review of the public
hot-path contract (sections 11e correction, 11i) and closed again
structurally by decisions 1A-4A (section 11j): the facade is rebuilt on
caller-owned pins with zero allocations and zero atomics in every hot
path, the closed class is WrongState, WordCount is error-capable, the
structure-kind conflict is resolved, metadata staging waste is fixed, and
the obsolete tree is deleted. The six-reviewer re-verification of the
rebuilt facade re-reviewed every brief at HEAD 2fdcce4: all six PASS
with no P0-P2 findings (the round's Pin value-copy P2 was subsequently
fixed in the external-audit round: every Pin references one shared
pinState, value copies are supported, and the TestPinValueCopy* and
TestPinValueCopyCannotReleaseSecondPin tests pin the behavior on the
pre-fix tree). The same-failure searches
(content-transfer, page arrays, stale constants, PID-slot model,
unsigned-subtraction-under-`||`) were re-run over the new tree: none
present. The later round-10 PASS at HEAD 253f9d5 and closure commit at HEAD
1c71299 were invalidated by the five-P2 external audit recorded in the report
header and active SOW. The round-11 final review then failed with one P1
(pin variable reassignment retargets the view guard; word read segfaults on
released memory) and three P2 (decision 5A unratified; mmap gate bypassable
plus a Windows Mapping.File escape; stale close-out records), fixed at HEAD
2fd6cae with the cross-reader reassignment regression test; the records were
corrected in the following commit. The round-12 final review then failed
with two P2 (decision 5A unratified; mmap gate still bypassable through
x/sys descriptor reads, bufio wrappers, dot imports, and build-tagged
packages) and one P3 (retained-slice lifetime comments); fixed at HEAD
4fdc671 with the whole-tree selector scan, the dot-import and bufio bans,
the durable nine-form gate self-test, and comment corrections, with the
runtime strace evidence recorded in section 2. Milestone 1 is reopened
and Milestone 2 is blocked pending the independent re-review and the
user's decision 5A. The worker boundary decision remains scheduled for
its later milestone per 2A.
