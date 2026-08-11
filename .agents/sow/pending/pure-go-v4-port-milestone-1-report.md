# SOW-0025 — Milestone 1 Report: portable mapped immutable reader

Date: 2026-08-11. Status: implementation checkpoint complete; two decisions
still pending (both evidence-first, per the recorded Milestone 0 closure).
Owning SOW: `.agents/sow/current/SOW-0025-20260811-pure-go-exact-v4-port.md`
(Status: in-progress).

## 1. TL;DR

- The pure-Go portable immutable reader is implemented, mmap-only, with the
  exact two-level design: `internal/format` (wire codecs, single error-code
  table), `internal/mapping` (the only mapping owner), `internal/reader` (the
  only healthy-generation reader core), and a public facade at the module
  root.
- The Go reader **opens and semantically verifies all five committed
  Rust-produced fixtures** (direct-v4, full-IPv6 first-seen, membership-v4
  with 70 feeds, membership-v6 with a 1 MiB blob-backed bitmap and 1 MiB
  repeated-byte metadata, structured-v4 with threat memberships) and rejects
  all three invalid mutations (wrong-magic, short, unaligned) with the exact
  `format-invalid` code required by the corpus.
- Warm point lookups, membership word reads, feed lookup, direct scans, and
  cardinality allocate **zero Go heap bytes** (measured; the only public
  exception is the returned feed-name string copy).
- Cross-compilation passes for darwin/amd64+arm64, freebsd/amd64+arm64,
  windows/amd64+arm64+386, linux/arm64+386. Windows is an explicit honest
  stub (open refuses with `os-unsupported`) until the platform milestone.
- **Pure-Go worker feasibility: disproven for POSIX with empirical evidence**
  (Go's runtime turns a file-mapping SIGBUS into a fatal
  `unexpected fault address` crash; `os/signal` never delivers it and no pure
  Go API exposes `si_addr` or a custom handler/chaining surface). The Windows
  vectored-exception path is pure-Go feasible. Per Decision 2 (recorded
  Milestone 0: wait for evidence), the fallback is a minimal project-owned
  assembly sigaction shim; the user decision is requested at the end of this
  report.
- Commit: `913f4e6` ("Milestone 1: mmap-only Go immutable reader with corpus
  cross-open"). No tracked file was deleted (Decision 1 = decide after
  evidence; the old Go tree remains green and untouched).

## 2. Commands and factual results

```
go test ./...                    ok (root 0.013s, format, reader; old exactv4 cached)
go test -race ./internal/format ./internal/reader .   ok
go vet ./internal/format ./internal/reader .          clean
gofmt -l .                                           clean
GOOS/GOARCH builds (darwin, freebsd, windows, linux arm/386): all ok
conformance test: 5/5 fixtures pass, 3/3 invalid mutations pass
zero-allocation: 11 operation groups, all 0 allocs (feed-lookup: 1 copy/lookup)
```

Production LOC added: `internal/format` 1,497; `internal/mapping` 161;
`internal/reader` 1,615; root facade ~450 — total ~3,720 new production lines
(+~1,700 test lines). Largest new file: `internal/reader/range.go` 557 lines
(within the 500-line preference; splitting it would reduce clarity).

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
  heap; views are re-derived and re-validated on every access.
- Root facade `reader_public.go`: `OpenImmutable`, `Info`, `DirectSemantic`,
  `LookupDirectV4/V6`, `DirectRangesV4/V6`, `Cardinality`,
  `LookupFeed`, `LookupMembershipV4/V6` + `MembershipView`
  (`Word`/`ContainsIndex`), `LookupNetworkEnrichmentV1V4/V6` + view
  (`Value`/`ThreatMembership`), `MetadataJSON`, `Close`, plus the public
  `StructureKind`, `ValueKindStructured`, `MetaSelection`, `DirectSemantic`
  types and error codes 65–69. Cursors and queries remain Milestone 3 scope
  per the approved plan.

## 4. Conformance evidence

`v4/go/conformance_test.go` mirrors the Rust corpus verifier against
`cases.json`:

- all five fixtures open with `ProvenCurrent` meta selection and exact info
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
- The old files remain untouched and will be deleted only per Decision 1.

## 6. Zero-allocation evidence

`v4/go/zeroalloc_test.go` (public surface) + `internal/reader/zeroalloc_test.go`
(internal surface), warmed, `AllocsPerRun(200)`:

| Operation | Allocations |
|---|---|
| direct lookup v4 (11 probes) | 0 |
| direct lookup v6 (4 probes) | 0 |
| membership lookup v4 (incl. view) | 0 |
| membership lookup v6 (blob bitmap) | 0 |
| ContainsIndex / Word (inline + blob) | 0 |
| structured lookup v4 | 0 |
| feed lookup (internal) | 0 |
| feed lookup (public) | 1 (returned name string copy; documented) |
| full direct scan v4 | 0 |
| cardinality scan | 0 |

Metadata decompression allocates only the returned payload (a caller value,
bounded by the 20 MiB limit), matching the contract.

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
- FreeBSD live coordination (error 44 before path access) is a later
  milestone; the immutable reader itself is platform-neutral (mmap + flock).

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

## 10. Pure-Go fault-worker feasibility — evidence and stop point

Per Decision 2 (recorded Milestone 0), feasibility evidence was collected
before any boundary decision. Empirical probe (`/tmp/wprobe`, not committed):
a file-backed mapping is truncated under the worker and page 1 is touched
while `signal.Notify(SIGBUS)` is registered. Result:

- The Go runtime turns the mapping fault into
  `unexpected fault address ... fatal error: fault` and terminates the
  process (exit code 2). The registered `os/signal` handler NEVER runs for
  file-mapping SIGBUS.
- No pure-Go API exposes `si_addr`/siginfo: `os/signal` drops it,
  `golang.org/x/sys/unix@v0.35.0` exposes no `sigaction`/`Sigaction_t`
  surface at all (verified in the module cache), and the runtime's own
  handler cannot be chained to without runtime internals (prohibited).
- Consequences for the exact contract: (1) owned faults cannot be claimed,
   recorded, and exited with the fixed `OWNED_FAULT_EXIT=197`; (2) unrelated
  SIGBUS crashes would terminate the worker with a Go-style fatal trace, and
  the parent could not distinguish them from source unreadability — exactly
  the mislabeling the contract forbids; (3) prior-disposition chaining is
  impossible.

Conclusion: **the exact POSIX fault-worker contract is not satisfiable in
pure Go.** The fallback is a minimal project-owned assembly sigaction shim
(SA_SIGINFO|SA_ONSTACK, si_addr checking against armed regions in the mapped
control state, raw `exit(197)` on owned faults, chaining to the saved previous
disposition otherwise) — a new native boundary (not cgo, no runtime
linkname), matching how `v4/rust/.../worker/posix.rs` implements the same
contract (SA_SIGINFO @53, si_addr @183, chaining @257, exit 197 @control.rs:16).

Windows is different: `AddVectoredExceptionHandler` with a
`syscall.NewCallback` handler is pure Go (no cgo, no assembly); the vectored
model's `EXCEPTION_CONTINUE_SEARCH` return provides exact chaining and
`EXCEPTION_RECORD.ExceptionAddress` provides the fault address. Still to be
proven on real Windows in the platform milestone.

**User decision requested (Decision 2, evidence now in hand):**
- A. approve a minimal project-owned assembly sigaction shim for POSIX
  (documented, reviewed, no other boundary);
- B. pure-Go only — implementation stops at the worker milestone;
- C. adjust/qualify the contract instead (requires a spec change + user
  design decision; not recommended).

## 11. Deviations and open items

- No tracked deletion executed (Decision 1 = C: decide after this
  evidence). The old Go tree is untouched and green.
- The public facade temporarily reuses the verified scalar aliases
  (`IPv4`, `IPv6`, `Cardinality129`, `ErrorCode`) from the old root package;
  the reset relocates them into the final public package.
- `LookupFeed` returns a Go string (one copy); the internal path is
  zero-alloc. Cursors, namespaces beyond basename rules, windows mapping,
  darwin/freebsd runtime proof, big-endian vector execution, and the
  conformance mixed-process gates are later milestones per the approved plan.
- Metadata bytes and feed names are caller-visible values; their heap copies
  are the contract's "bounded encoded records", not pages.

## 12. Milestone 1 close-out

Acceptance criteria evidence: portable codecs (literal vectors), mapping
owner (geometry/lifetime/lock), public immutable reader, all five Rust
fixture cross-reads with `cases.json` semantics, malformed bootstrap
rejection, zero-allocation lookups/scans, first platform/worker feasibility
report — all executed and recorded above. The independent review found three
blocking issues and two nits; all were verified, fixed, and regression-tested
(section 9), and the reviewer's verdict on the repaired tree was that no
actionable finding remains in the milestone scope. The same-failure searches
(content-transfer, page arrays, stale constants, PID-slot model) were re-run
over the new tree: none present (the new tree contains no read/write/seek
content calls, no `[PageSize]byte` arrays, no stale tags, no sidecar code).
Next milestone is safe to start once Decisions 1 (deletion set, evidence now
available) and 2 (worker boundary) are answered.
