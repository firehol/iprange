# SOW-0025 — Milestone 1 Report: portable mapped immutable reader

Date: 2026-08-11 (updated 2026-08-13 after review rounds, external audits, and
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
wrappers, dot imports, build-tagged packages). The gate was fixed at
HEAD 4fdc671 with a whole-tree selector scan, dot-import and bufio
import bans, a durable --self-test mode, and the runtime strace
evidence below; decision 5A was recorded for the user's ratification
and remains open (a user decision, not a code fix).
The round-13 final review then found three remaining P2 (decision 5A
still unratified; the gate still accepted indirect content-transfer
forms, its line-level exemption, and a windows-tagged internal-package
import; the records contradicted the source) plus two P3 comments, all
fixed at HEAD dbdf2b7 (exact call-node blanking, extended selectors,
gzip/compress-zlib import bans, per-target boundary checks over eleven
GOOS/GOARCH pairs, an eighteen-form durable self-test, and the records
in this file). The six-reviewer re-review of that fix found the decoder/
encoder family and two write/reflection gaps still open; the gate now
also bans the reader-consumer packages, covers
WriteString/WriteRune/NewDecoder/Decode/Encode/Method, blanks only
paren-free tolerated call nodes, and its self-test durably rejects
twenty-two mutation forms. The re-review of that fix closed the
io.ReadFull/io.ReadAtLeast file-consumption gap, the log/template/
exec/http writer families, and the non-compiling self-test forms, and
the self-test now durably rejects twenty-six mutation forms
(bf33f2a). The fourth re-review closed the paren-crossing io.ReadFull
exemption shadow, the zr-name collision, the reflection Call
invocation, and the reader-constructor packages; the self-test now
durably rejects twenty-eight mutation forms (149a200). The fifth
re-review made the exemptions exact literals (c.r.Read(p),
c.r.ReadByte(), and the two io.ReadFull(zr, out[...int(meta.
MetadataUncompressed)]) inflater reads) so same-named file-backed
readers fail closed, added a self-test-residue sweep, and grew the
self-test to thirty mutation forms (c03e40c). The sixth final review
then failed with five P2 findings, all in the mmap gate and the records:
split-after-the-dot selectors; type-blind exact-literal exemptions; the
open-ended stdlib denylist (compress/gzip regex bug, log/slog,
runtime/trace, os.StartProcess ProcAttr files); the destructive
gatemut_* startup sweep; and completion claims ahead of the review
trail. The gate was rewritten at HEAD c42325a as an AST, type-light scanner
(v4/go-gate/main.go): it parses every production file, syntactically
taints *os.File values, bans 37 content-transfer imports and 56
selector families, constrains *os.File use to the mapping-lifecycle
methods and same-package/module-internal/x-sys consumers, and exempts
the three exact in-memory inflater nodes only with file-taint
verification; the self-test now copies the module to a private temp
directory and durably rejects forty mutation forms including all nine
independent reproducers of the sixth review, the startup sweep is
removed, and the records were corrected in the same pass. HEAD 81ca524
then pinned the aliased-os producer form (forty-first), HEAD 6b05801
tainted *os.File results of same-package accessor methods, and the
seventh sweep (HEAD e2dc7e0) closed the type-alias conversion/
parameter, separately built ProcAttr-container, and os.Pipe producer
classes; the eighth sweep (HEAD c4b1b52) closed the struct-field-
storage and channel-transport classes behind the inflater exemptions,
and the ninth sweep (HEAD ddc5f9c) closed the inline-FuncLit,
type-assertion, and nested/single-variable channel classes; the
tenth sweep (HEAD 5c88ba3) closed the parenthesized-producer,
parenthesized-closure, interface-typed-closure, alias-typed-function-variable,
and type-switch-bound classes (forms 54-59), plus the defined-func-type
family, the method-receiver boundary, the nested-callee double-call
family, the struct-field/chan-of-func/asserted-func/os-std-handle
family, the nested-field/named-helper/chan-pass family, the
named-method extension, the nested-method-receiver extension, the
method-value family, the generic pass-through family, the
generic-element family, the chan-result method-value class, and the
field-assignment class, the channel-consumer class, the
container-element class (forms 60-107), the anonymous-receiver
method class (forms 108-111), the alias-receiver method
class (forms 113-114), the receiver-resolution
class (forms 116-119), the pointer-defined-type
class (forms 121), the indexed-receiver
class (forms 123-125), the element-receiver
class (forms 127-132), the range-literal-receiver
class (forms 134-135), the bound-receiver
class (forms 137-138), the call-result-binding
class (forms 140-143), and the explicit-instantiation
and interface-binding class (forms 145-148), the
generic-receiver-binding class (forms 151-156), the
alias-spelled generic binding class (forms 159-164), and the
reader-shape binding class (forms 167-174), and the
renamed-qualified alias class (forms 179-182), and the
func-typed generic-method class (forms 185-189), the
mixed result and qualified-defined class (forms 191-196), the
interface-method and method-result class (forms 199-205), the
embedded-interface and cross-package chain class (forms 207-210), the
remote-interface and generic-instantiation class (forms 213-217), the
defined-hop instantiation class (forms 222-223), the nested generic-
instantiation class (forms 225-226), the cgo-import, raw-syscall,
linkname, no-error syscall, and preadv2/pwritev2 classes (forms
228-230 and 232-235) with the benign lifecycle control (form
231); the self-test now durably rejects two hundred forty
mutation forms (round-36 forms 236-237, follow-up forms 238-239,
round-38 form 240, round-39/40 forms 241-244, round-42 forms 245-247,
round-43 form 248, and round-45/46/47 forms 249-256 pin the dup/exec subprocess escape, the bodyless assembly-stub
class, the x/sys owner boundary, assembly objects, fcntl F_DUPFD
duplication, out-of-tree module-graph attach, x/sys source replacement,
hidden dot-directories, x/sys source-content spoofing (poisoned module
cache and file proxy with forged go.sum), case-variant assembly
objects, os.CopyFS directory copies, os.OpenInRoot/os.OpenRoot handles
reaching stream wrappers, the x/sys descriptor-transfer primitives
Tee/Vmsplice/IoctlFileClone*/Clonefile*, *os.Root laundering
through struct fields, file method values, initialized func-typed
variables with file-bearing declared results, stdlib producer
values bound without a declared type, and round-48 forms 257-260 pin
bound method expressions on file-bearing receiver types and
same-module cross-package producer vars; round-49 forms 261-266 pin
nested-paren, renamed-import, alias-over-renamed, wrapper-promoted
method expressions, value-bound cross-package producer vars, and
interface-conversion launders; round-50 forms 267-270 pin generic
interface erasure, composite-literal field launders,
instantiated-generic-wrapper method expressions, and deep
embedding chains (forms 267-268 were converted to exemption-shape
metadata.go appends in round 51 because their separate-file
launders were rejected unconditionally and never exercised the
taint; forms 269-270 were genuine); round-51 forms 271-277 pin the
renamed-qualifier and other-stdlib interface-erasure class, the
slice/array-of-type-parameter class, the positional
composite-literal field-launder class, the chan-send class, and
the positional os.Root opener class, and round-52 forms 278-282
pin the embedded file-handle literal-launder class, the var-bound
generic-instantiation class (container results and the
erase-to-interface twin), and the anonymous struct-literal
positional-element class (embedded fields named by type name; an
anonymous struct embedding a file handle escalates like a named
wrapper), and forms 283-288 pin the round-52 continuation class
(elided slice/map and nested container-element fields, pointer
composite literals, and func-valued arguments to explicitly
instantiated generics); details in the
close-out narrative). Decision 5A was ratified (option A, 2026-08-13):
value-plus-HasLocation is the zero-allocation equivalent of Rust's
Option<NetworkEnrichmentV1Location>, recorded in the parity matrix.
Milestone 2 must not start until a new independent final review passes.
Owning SOW: `.agents/sow/current/SOW-0025-20260811-pure-go-exact-v4-port.md`
(Status: in-progress).

2026-08-14: an independent external review PASS-failed the milestone-1
close with three findings (hot-path binary searches decode full records
per probe and re-decode the selected record; the mmap enforcement
machinery is 14,519 lines and misses the complete-page ownership rule;
the SOW Status grew unmaintainable). The rework is recorded in section 14
below: one authoritative key-only search primitive with test-only
necessary-work counters and benchmarks, a type-aware go/types gate
(11,197 lines) plus a trimmed shell harness (552 lines) whose 527-case
durable battery (456 rejections, 71 benign acceptances) includes the complete-page ownership forms and the
function-variable, closure-body, func-literal-variable, and
multi-hop-chain bypass pins, and compact
records with the history preserved in the SOW appendix. Independent
review outcome: section 14.

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
- Cross-compilation passes for linux amd64/386/arm/arm64/loong64, darwin
  amd64/arm64, freebsd amd64, windows amd64/arm64 (the gate's per-target
  listing matrix). Windows is an explicit honest
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
go test ./...                                  ok (5 packages: root, format, reader, mapping, work)
go test -race ./internal/format ./internal/reader ./internal/mapping .   ok
go vet ./...                                  clean
gofmt -l .                                    clean
GOOS/GOARCH builds (linux amd64/386/arm/arm64/loong64, darwin
amd64/arm64, freebsd amd64, netbsd amd64, windows amd64/arm64): all 11 ok
check-import-graph.sh --self-test (with per-target boundary checks across eleven
GOOS/GOARCH pairs): 527/527 mutation forms rejected (453 rejections, 71
benign acceptances)
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
doc.go, errors.go, reader_public.go, types.go): 5,182 raw lines, including
blanks; new-tree tests (`find . -name '*_test.go' | sort | xargs cat | wc -l` over the same
tree): 6,410 raw lines. The earlier
6,160 figure mixed production and test files and is superseded, as are the
~3,720/~1,700 snapshots from the first passes.

### Close-out fixes (external audit round, 2026-08-12)

- mmap-only gate hardened (round-12/13): the content-transfer scan is a
  whole-tree find over every production Go file (build-tagged files for
  every platform included), matching word-boundary selectors for calls,
  method values, function aliases, Seek, x/sys descriptor reads
  (Readv/Writev/Preadv/Pwritev), byte-oriented readers, and the indirect
  forms (fmt.Fscan/Fscanf/Fscanln, io.CopyN/CopyBuffer, reflection
  MethodByName, raw unix.Syscall(SYS_READ), unix.CopyFileRange, Sendfile,
  Splice); tolerated c.r.Read calls are blanked as exact call nodes so a
  forbidden transfer on the same line stays visible; dot imports and the
  bufio / io-ioutil / gzip / compress/zlib wrapper imports are banned
  outright; the internal-package boundary check runs per target over eleven
  GOOS/GOARCH pairs so a build-tagged package cannot import internal
  packages unseen; the Windows mapping stub no longer carries or exposes
  a raw `*os.File` (Mapping.File removed on every platform); `--self-test`
  durably rejects thirty mutation forms (direct call, alias, method
  value, Seek, new directory, unix.Readv in the mapping owner, bufio
  wrapper, dot import, windows-only package, single-line and aliased
  bufio escapes, fmt.Fscan, io.CopyN, reflection-invoked Read, raw
  SYS_READ, CopyFileRange, tolerated-call line sharing, windows-only
  internal import, json decoder over a file, os.File.WriteString,
  transfer nested inside the tolerated node, reflection Method(i),
  io.ReadFull over a file, io.ReadAtLeast over a file, log writing to
  a file, flate.NewWriter over a file, transfer nested inside the
  io.ReadFull exemption node, io.ReadFull over a file-backed flate
  reader, file-backed c.r receiver, file-backed zr/out reader with a
  different index shape).
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
  checked views; O(1) bootstrap maps exactly 2 meta pages, then
  `Remap(committedBytes)` grows to the committed extent (mremap on Linux,
  munmap+mmap on other POSIX); `PhysicalSize` returns the locked file
  extent for bootstrap validation; `!windows` POSIX implementation +
  honest Windows stub.
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

A second independent external review returned ten claims. Every claim was
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
pass with regression tests; an independent external reviewer inspected the pre-repair tree.

Review verdict on the third-pass repairs: same-failure searches re-run
(no other slotted access paths, no other handle-holding surfaces, no other
wrong-mode entry points), full suite green including race and vet.

## 10b. Review findings and repairs — fourth pass (2026-08-11, second independent review)

A second independent review returned nine numbered findings. All were verified
against the tree, the spec, and the Rust sources; all nine were real (one
also corrected a wrong factual claim in this report). Repairs:

1. **Handle registry replaced by borrow-count lifetime (API redesign).** The
   1024-slot token registry was unapproved, non-concurrent (the reviewer
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
  (11); second Close reports WrongState; numeric code 9 remains HandleClosed;
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
the durable gate self-test, and comment corrections, with the runtime
strace evidence recorded in section 2. The round-13 final review then
failed with three P2 (decision 5A unratified; gate bypasses through
fmt.Fscan, io.CopyN/CopyBuffer, reflection MethodByName, raw
unix.Syscall(SYS_READ), unix.CopyFileRange, Sendfile/Splice, a same-line
exemption shadow, and a windows-tagged package importing
internal/mapping unseen by the linux go list boundary check;
contradictory records and counts) and two P3 comments, all fixed at HEAD
dbdf2b7 and in the records of this file: exact call-node blanking,
extended selectors, gzip/compress-zlib import bans, ten-target
per-GOOS/GOARCH boundary verification, an eighteen-form durable
self-test, and the count refresh to production 4,772 / tests 4,832.
The six-reviewer re-review then found the decoder/encoder family
(json/xml/gob NewDecoder over a file, archive/image/bzip2 etc.),
os.File.WriteString, reflect.Value.Method(i), and a nested-paren
blanking shadow still open; fixed at HEAD f9c88b2 with the
reader-consumer import bans, the WriteString/WriteRune/NewDecoder/
Decode/Encode/Method selectors, paren-free-only tolerated-node
blanking, compiling self-test mutations, and four new mutation forms -
the durable self-test now rejects thirty mutation forms:
io.ReadFull/io.ReadAtLeast over a file join the selector set, the five
writer packages (log, text/template, html/template, os/exec, net/http)
and flate.NewWriter are covered, the four tolerated inflater nodes are
exempted as exact literals only (c.r.Read(p), c.r.ReadByte(), and the
two io.ReadFull(zr, out[...int(meta.MetadataUncompressed)]) reads) so
a same-named file-backed reader or a different index shape fails
closed, Call/CallSlice close the reflection invocation family, the
reader-constructor packages (debug/*, go/parser, go/scanner,
text/scanner) and writer families (text/tabwriter,
mime/quotedprintable) join the import ban, the method-value and
CopyFileRange forms compile, the nested-node probe is documented as an
intentional textual tripwire, and a startup sweep removes stale
gatemut_* artifacts from interrupted self-test runs.
Decision 5A was ratified (option A, 2026-08-13): value-plus-HasLocation
is the zero-allocation equivalent of Rust's Option<NetworkEnrichmentV1Location>,
recorded in the parity matrix.
Milestone 1 is reopened
and Milestone 2 is blocked pending the independent final review.
The worker boundary decision remains scheduled for its later milestone per 2A.

The sixth final review then failed with five P2 findings, all in the mmap
gate and the records: (1) selector splitting after the dot - `file.\n
Read(p)` and `io.\nReadAll(f)` compile and bypass a line-oriented scan;
(2) type-blind exact-literal exemptions - a struct whose `c.r` is
`*os.File` using exactly `c.r.Read(p)`, and a function whose `zr` is
`*os.File` using exactly `io.ReadFull(zr, out[:int(meta.
MetadataUncompressed)])`, both pass the name-keyed blanking; (3) the
open-ended stdlib denylist - the gzip regex never matches
`compress/gzip`, and log/slog.NewTextHandler, runtime/trace.Start, and
os.StartProcess with ProcAttr{Files: []*os.File} consume a file unseen;
(4) the startup sweep deletes every path named `gatemut_*` before
scanning, so a committed gatemut_hidden_linux.go violation is removed
and the gate reports PASS (and untracked user work can be destroyed);
(5) the records claim completion while the six-reviewer PASS at HEAD
360130c is not recorded and round-12 wording says decision 5A was
"fixed". The response (HEAD c42325a) replaces the line-oriented text scan with the
AST, type-light scanner at v4/go-gate/main.go (stdlib only): it parses
every production file - build tags, line wrapping, comments, aliases,
and file names are irrelevant to the token stream - syntactically taints
`*os.File` values (declarations, parameters, os.Open*/os.Create
producers, same-package constructors, struct fields), bans 37
content-transfer imports and 56 selector families, permits `*os.File`
values only into the mapping-lifecycle methods
(Fd/Close/Name/Stat/Sync/Truncate/Chmod/Chown) and
same-package/module-internal/x-sys consumers, and exempts the three
exact in-memory inflater nodes only when their receiver/arguments are
not file-tainted. The self-test now runs in a private temp copy (cp -a
into mktemp): forty mutation forms are rejected, including all nine
independent reproducers of this review; an innocent gatemut_-named file
is proven to survive; the reviewed tree is never modified; and the
startup sweep is removed. HEAD 81ca524 pinned the aliased-os producer
form (forty-first), HEAD 6b05801 tainted *os.File results of
same-package accessor methods, the seventh sweep closed the
alias/ProcAttr/os.Pipe classes, the eighth sweep added the
struct-field-storage and channel-transport forms behind the inflater
exemptions, the ninth sweep added the closure, type-assertion, and
nested/single-variable-channel forms; and the tenth sweep added the
parenthesized-producer, parenthesized-closure, interface-typed-closure,
alias-typed-function-variable, and type-switch-bound forms; and the
eleventh extension closed the defined-func-type family (defined func
types, func-valued returns through helpers, type-switch bound func
cases (forms 60-63), the method-receiver boundary, and the
nested-callee double-call shapes (forms 64-67), the round-5
struct-field/chan-of-func/asserted-func/os-std-handle family (forms
68-72), the round-6 nested-field/named-helper/chan-pass family
(forms 73-77), the named-method extension (forms 78-81), the
nested-method-receiver extension (forms 82-83), the method-value
family (forms 84-87), the generic pass-through family
(forms 88-89), the generic-element family, the chan-result
method-value class, the field-assignment class (forms 92-95), the channel-consumer
class (forms 98-100), the container-element class
(forms 103-106), the anonymous-receiver method class
(forms 108-111), the alias-receiver method class
(forms 113-114), the receiver-resolution class
(forms 116-119), the pointer-defined-type class
(forms 121), the indexed-receiver class
(forms 123-125), the element-receiver class
(forms 127-132), the range-literal-receiver class
(forms 134-135), the bound-receiver class
(forms 137-138), the call-result-binding class
(forms 140-143), and the explicit-instantiation
and interface-binding class (forms 145-148), the
generic-receiver-binding class (forms 151-156), the
alias-spelled generic binding class (forms 159-164), and the
reader-shape binding class (forms 167-174), and the
renamed-qualified alias class (forms 179-182), and the
func-typed generic-method class (forms 185-189), the
mixed result and qualified-defined class (forms 191-196), the
interface-method and method-result class (forms 199-205), and the
round-28 embedded-interface and cross-package chain class (forms
207-210: interface embedding promotion lost declared method results
in the promoted-method walk, cross-package defined structs were
invisible as generic type arguments, and nine-hop qualified defined
chains exceeded the single-pass fixpoint budget in map iteration
order), fixed in the round-28 gate pass recorded in the active SOW
exec log; and the round-29 remote-interface and generic-
instantiation class (forms 213-217: renamed-qualifier cross-package
interface embedding, generic-interface instantiation at the
embedding site with func-file and chan-of-func arguments, and the
adjacent renamed generic interface and cross-package generic struct
shapes), fixed in the round-29 gate pass recorded in the active SOW
exec log; and the round-30 defined-hop instantiation class (forms
222-223: a defined type over an instantiated generic interface,
reader-local and renamed-qualified), fixed in the round-30 gate pass
recorded in the active SOW exec log; and the round-31 nested
generic-instantiation class (forms 225-226: two-level and
three-level/chan generic-interface embedding chains), fixed in the
round-31 gate pass recorded in the active SOW exec log; and the
round-32 cgo-import, raw-syscall, and linkname gate class (forms
228-230: import "C" + C.pread, unix.RawSyscall on a file
capability, and //go:linkname aliasing), fixed in the round-32 gate
pass recorded in the active SOW exec log (rejects extended with the
no-error syscall and preadv2/pwritev2 classes, forms 232-235); the
self-test now durably rejects two hundred forty mutation forms (forms 236-248 pin the round-36/37/38/39/40/42/43 dup/exec subprocess escape, bodyless assembly-stub, x/sys-owner-boundary, assembly-object, fcntl F_DUPFD duplication, out-of-tree module-graph, x/sys source-replacement, hidden dot-directory, x/sys source-content spoofing, case-variant assembly-object, and unlistable-module rejections; forms 249-256 pin the round-45/46/47 os.CopyFS directory-copy, os.OpenInRoot/os.OpenRoot stream-wrapper, x/sys descriptor-transfer-primitive, *os.Root struct-field-laundering, file-method-value, initialized func-typed-variable, and stdlib-producer-value rejections; forms 257-260 pin the round-48 bound-method-expression and same-module cross-package producer-var rejections; forms 261-266 pin the round-49 nested-parenthesized, renamed-import, alias-over-renamed, wrapper-promoted method-expression, value-bound cross-package producer-var, and interface-conversion-launder rejections; forms 267-270 pin the round-50 generic-interface-erasure, composite-literal-field-launder, instantiated-generic-wrapper method-expression, and deep-embedding-chain rejections (267-268 converted to exemption-shape metadata.go appends in round-51 because their separate-file launders were rejected unconditionally; 269-270 were genuine); forms 271-277 pin the round-51 file-bound generic result-erasure and positional composite-literal field-launder rejections; forms 278-282 pin the round-52 embedded file-handle literal-launder, var-bound generic-instantiation (container results and erase-to-interface), and anonymous struct-literal positional-element rejections; forms 283-288 pin the round-52 continuation rejections for elided container-element fields, pointer composite literals, and explicit-instantiation callee closures, and forms 289-290 pin the round-53 embed-import and //go:embed-directive compile-time-copy rejections). The round-39 gate re-review found the module-graph escape: go.mod replace and go.work workspaces attach out-of-tree modules the scan never walks (reproduced with a wrapper calling unix.Pread, gate exit 0 on both vectors); fixed by validating the module graph to exactly this module plus golang.org/x/sys with no workspace active, pinned as self-test forms 241-242. The round-40 gate re-review then found the path-only allowlist gap: a replace of golang.org/x/sys to an evil directory keeps the allowed path in the graph while loading code the walk never scans (proven live with unix.Pread2 reading the database), and the walk skipped hidden dot-directories; fixed by banning all replace/exclude directives, verifying the resolved x/sys source is the module-cache checkout, and scanning hidden directories (only .git skipped), pinned as forms 243-244. The round-42 gate re-review then found the x/sys source-content gap: the path-only allowlist accepted a poisoned GOMODCACHE checkout and a file proxy serving an evil x/sys with a self-consistent forged go.sum (both proven live with a smuggled unix.Pread2, gate exit 0 on both vectors) because nothing pinned the module content; fixed by pinning the exact version, the module-cache path, the extracted-tree content hash, and the module zip/go.mod sums to the official v0.35.0 values, plus a case-insensitive assembly-object rejection, pinned as forms 245-247. The round-43 gate re-review then found the fail-open listing gap: the per-target go list ./... loop swallowed listing failures (2>/dev/null), so a module the go toolchain cannot list - symlinked package files or parse errors - passed with an empty package list and no import checks; go list failures now fail the gate per target and pkg_imports fails closed, pinned as form 248. The round-45 final review then found the mmap-gate denylist gaps: os.CopyFS was absent from the selector ban (a directory copy streams artifact bytes with no banned selector), os.OpenInRoot/os.OpenRoot were absent from the file-producer table (a Go 1.26 OpenInRoot *os.File, or an older-toolchain *os.Root handle, reached flate.NewReader untainted and streamed file bytes; Root.Open/Create/OpenFile also produce files), and the x/sys surface still carried descriptor-transfer primitives (unix.Tee, unix.Vmsplice, unix.IoctlFileClone/CloneRange/DedupeRange, darwin unix.Clonefile/Clonefileat); CopyFS and the x/sys primitives join the banned selector set, os.OpenInRoot/os.OpenRoot join the file-producer table so Root methods fail closed, pinned as self-test forms 249-251; the adversarial re-review then proved a P0 in the same class
(*os.Root stored in a struct field and opened through the field drops the taint, so the returned *os.File
reached flate.NewReader and the exempted inflater shape, gate exit 0), closed by resolving *os.Root as a
file-bearing type everywhere *os.File does, pinned as form 252; the producer-value re-review then
closed three more P0 escapes (file method values; initialized func-typed variables with file-bearing
declared results, Root and *os.File; stdlib producer values bound without a declared type), pinned as
forms 253-256. The round-48 re-review then closed two further gate classes (bound method expressions
on file-bearing receiver types, form-local and package-level, and same-module cross-package
package-level producer vars such as format.OpenRoot/format.Open), pinned as forms 257-260; the
round-48 exec-log entry in the owning SOW cites the exact round-45/46/47 chain HEADs (14c0698,
70dcc42, 262756c, e1410eb, 5ff9116, 8c6cc44). Decision 5A was ratified
(option A, 2026-08-13): value-plus-HasLocation is the zero-allocation
equivalent of Rust's Option<NetworkEnrichmentV1Location>, recorded in the
parity matrix. Milestone 1 is reopened and Milestone 2 is
blocked pending the independent final review.
The worker boundary decision remains scheduled for its later milestone
per 2A. The round-37 narrow re-review found and fixed one P2 (metadata
chunk tail-zero parity: ReadMetadataJSON accepted nonzero bytes after a
metadata chunk; Rust rejects the page as corrupt; fixed with an explicit
tail-zero check in internal/reader/metadata.go and the pre-fix-failing
regression pin TestMetadataChunkTailNonzeroRejected).

## 14. External-review rework (2026-08-14)

The independent external review at HEAD b230bd1 PASS-failed milestone
close with three findings, all reproduced by the lead before change:

1. P1 hot path: nine separately written binary-search loops decoded every
   probed record (including fields the comparison does not need) and
   decoded the selected record a second time; Rust probes key-only and
   decodes the selected record once. Go had no benchmarks, no
   necessary-work counters, and no profiles, so the timing impact was
   unmeasured while the wasted work was proven.
2. P1 enforcement: the gate totaled 14,519 lines (4,738-line type-light
   AST scanner + 9,781-line shell self-test) against a 4,789-line reader,
   and it missed the complete-page rule (binary-format-v4.md:108): a
   production function copying a mapped page into an owned [4096]byte
   compiled and passed the gate.
3. P2 records: the SOW Status had grown to 463 lines of round-by-round
   history.

Fixes (one commit; HEAD recorded in the review entry below):

- Search authority: internal/reader/search.go adds greatestLE, the single
  fixed-tree lower-bound primitive (key-only probes, last-probe reuse,
  one selected-record decode). Range branch/leaf, catalog feed,
  membership ID, and blob branch lookups all route through it; the
  format key readers (RangeEntryKeyV4/V6, RangeRecordKeyV4/V6) read only
  the key bytes per probe. The blob branch keeps its per-probe child
  validation (TestBlobBranchProbedChildValidation) and catalog name
  probes keep full-shape validation because the name is the record
  payload (decision 1A).
- Necessary work made visible: internal/work behind -tags v4work pins
  tree lookups, descents, page visits/parses, key probes, selected-leaf
  validations, word reads, and structure decodes; the production build
  compiles the counters to inlineable no-ops (Enabled const false,
  TestWorkCountersDisabled). Pins: TestWorkRangeLookupMultiLevel,
  TestWorkMembershipBlobWords, TestWorkStructureLookup.
- Benchmarks/profiles: bench_test.go on committed fixtures plus the
  synthetic multi-level databases. i9-12900K, 200k iterations:
  LookupDirect4MultiLevel 158.8 ns/op, LookupDirect4MissMultiLevel
  72.05 ns/op, LookupDirect6 68.56 ns/op, LookupFeed 227.7 ns/op,
  MembershipLookupWord 94.81 ns/op, StructuredLookup 120.1 ns/op,
  ScanDirect4 41.4 ns/op - all 0 B/op, 0 allocs/op. CPU profile:
  the dominant LookupFeed cost is the intentional catalog name
  validation (FeedNameValidString, 48% of that benchmark); the
  range/membership probe paths show only slot-offset checks and key
  reads - no full-record decode.
- Gate replacement: v4/go-gate is a type-aware scanner over go/types
  (stdlib-only, module-local source loader per OS config, pinned x/sys
  checkout), keeping the text bans, the *os.File/*os.Root capability
  surface, the interface-erasure rules (including named interfaces such
  as io.Reader), and adding the complete-page ownership rule with a
  symbolic interprocedural page flow (pageflow.go): constant slice
  spans carry their bound, so page[48:112] is a 64-byte view and
  page[0:4096] is a full page. Reviewer reproduction now fails the gate
  with rule-specific violations: copy of m.Page(0) into [4096]byte,
  append(page...), copy of m.View(0, format.PageSize), [4096]byte(page),
  string(page), copy of r.page(pgno); the bounded record copy and the
  decoded metadata-chunk append stay legal. The durable battery is table
  data inside the tool: 527 cases (453 rejections, 71 benign acceptances)
  covering source-transfer, complete-page, and file-capability forms. The shell
  harness shrank from 9,781 to 552 lines (import boundaries per target,
  module graph, x/sys checksum pins) plus 9 environment mutations
  (internal-import boundary, x/sys outside the mapping owner, assembly
  object, go.mod replace, go.work, poisoned x/sys cache/proxy,
  unlistable module). Gate totals: 11,197 (tool) + 552 (shell) = 11,749
  lines against module production 5,182 / tests 6,410.
- Battery repair during the replacement: the extractor had dropped
  multi-line inserts (forms 61/64/69/76), broken the form-107 escaping,
  and copied shell-only module-graph forms (18/238/243/248); benign
  forms 49/59/63/67/81/83/90 referenced undefined types or
  non-compiling assignments that only the old syntax-only scanner could
  analyze. Each was repaired to a compilable equivalent preserving the
  tested rule, the battery harness now restores multi-op case files in
  LIFO order (the metadata.go double-op restore bug), hidden
  dot-directories are scanned (go/build ignores dot-prefixed files;
  both scan paths now include them), and the four module-graph forms
  moved to the shell self-test.
- Validation at the rework commit: go test ./... (both tag sets),
  -race, checkptr, go vet, gofmt zero diffs, import-graph gate exit 0,
  gate --self-test 320/320 + 9 shell mutations exit 0, production scan
  across all five target configs exit 0, cross-compilation, Rust
  conformance corpus cross-open, SOW audit green.

- Swarm round 3 (2026-08-14): six residents PASS at the round-2 fix
  commit; K3's remaining theoretical bypass (two-hop function-variable
  chain: var a = func(p){ copy(out, p) }; var b = a; b(page) allowed the
  call while the literal body analysis stayed at the direct initializer)
  was closed: evalCall now follows the bounded initializer chain
  (funclitOf, max two hops, no reassigned hop) and binds call-site
  arguments to the literal parameters. Pinned as battery forms P19
  (reject) and P20 (benign bounded slice through the same chain).
  Reviewer P3 (stale production-LOC in the SOW Status) fixed: measured
  module production 5,049 / reader core 1,894 after the membership.go
  counter cleanup.

- Lead adversarial pass after round 2 (2026-08-14): one more hole in the
  same class was found and fixed before the round closed - a
  never-reassigned function-typed variable initialized with a func
  literal (var f = func(p []byte){ copy(out, p) }; f(page)) approved the
  call but analyzed the literal body at the declaration with the
  parameter unbound, so the complete-page sink inside stayed invisible.
  Calls through such variables now bind the call-site arguments and
  re-analyze the literal body at each call; package-level variables that
  are reassigned anywhere no longer count as approved (a var later bound
  to bytes.Clone is a transfer). Pinned as battery forms P16 (reject),
  P17 (benign bounded slice through the same literal) and P18 (reject,
  reassigned var).
- Validation at the final round-2 commit: production scan clean, gate
  --self-test 300/300 (243 rejections, 57 benign) + 9 shell mutations
  exit 0, go test ./... (both tag sets), -race, vet, gofmt zero diffs,
  SOW audit green.

- Follow-up swarm round (2026-08-14): five residents passed the rework
  commit; two P1 gate-bypass classes were then verified live and closed:
  (1) function-typed variables as call targets accepted an unscanable
  callee (var clone = bytes.Clone; clone(page) copied a full mapped page,
  gate exit 0) - approvedFuncVar now requires a package-level
  initializer that provably binds a scanned function, pinned as battery
  forms P8 (reject), P9/P15 (benign); (2) the page-taint flow skipped
  defer/go statements and function-literal bodies, so
  defer func(){ copy(out[:], page) }() and func(){ return
  append([]byte{}, page...) }() passed (gate exit 0) - pageflow now
  analyzes defer/go/select/labeled/block statements and closures in
  expression and callee position, pinned as forms P11-P13 (reject) and
  P14 (benign bounded closure copy). Functional housekeeping from the
  same round: the dead err re-check in lookupMembershipID was removed and
  work.LeafValidation moved to the record-decode point in
  membershipLeafFind (no counter on a clean miss), matching the
  range/catalog/blob helpers.
- Validation at the follow-up commit: production scan clean, gate
  --self-test 297/297 (241 rejections, 56 benign) + 9 shell mutations
  exit 0, go test ./... (both tag sets), -race, vet, gofmt zero diffs,
  SOW audit green.

- Swarm round at HEAD 8c8ca39 (2026-08-14): six residents reviewed the
  rework plus the follow-up hardening (function-variable callees,
  closures, func-literal variables, reassigned variables, bounded
  function-variable chains). GLM and MiMo PASSed; MiMo reported one P3
  class, verified live by the lead and upgraded: RangeStmt rebinding
  (for _, f = range fs over a slice holding bytes.Clone) and
  address-taken stores (p := &f; *p = bytes.Clone) both kept the gate at
  exit 0 while f(page) then copied a complete mapped page into owned
  memory; the pointer form and the range form were each reproduced
  before the change (exact mutations, gate 0).
- Fix (HEAD 93b0f07): the function-variable reassignment walk in
  v4/go-gate/rules.go now also marks package-level variables rebound by
  RangeStmt (Key and Value idents resolved through info.Uses) and
  variables whose address is taken anywhere (UnaryExpr AND on a
  package-level var), because either permits a runtime rebind to an
  unproven callee; both approvedFuncVar and funclitOf consult the same
  map, so both rule passes fail closed. Battery pins P21 (range
  rebinding, reject), P22 (address-taken store, reject), P23 (benign
  range rebinding without a page call, accept); battery is now 305
  cases (246 rejections, 59 benign acceptances). Production scan across
  all five targets stayed clean: the read-only tree takes the address of
  no package-level variable and range-rebinds none, so the conservative
  over-approximation produces zero false positives (verified by K3's
  tree-wide grep).
- Swarm round at HEAD 93b0f07 (2026-08-14): all six residents PASS on
  the delta (GLM, MiMo, K3, MiniMax, Qwen, Luna). Independent
  cross-checks confirmed RangeStmt Key/Value handling (define-form
  ranges stay untouched via the Defs/Uses split), the address-taken
  over-approximation is fail-closed and false-positive-free on the
  production tree, the remaining theoretical rebinding paths (closure
  stores through non-Ident LHS, reflection, unsafe) are already covered
  or blocked by the import/selector bans, and the battery totals match
  the tree. Luna additionally flagged stale current-state record counts
  (SOW still claimed 302 cases / 5,069 tooling lines at HEAD 93b0f07);
  the records were synced to 305 / 5,100 (4,548 go-gate + 552 shell) in
  a follow-up commit and re-audited.
- Validation at the round-close commit: gate --self-test 305/305 + 9
  shell mutations exit 0, production scan clean on all five targets,
  go test ./... (both tag sets), -race, vet, gofmt zero diffs, SOW
  audit green.

- Swarm round at HEAD 2ad4001 (2026-08-14): the independent round
  review returned nine gate findings (Luna resident), all reproduced by
  the lead against the tree (exact mutation probes, gate exit 0):
  1. helper summaries lost page taint through parameter flow (maxLen
     accumulated from 0, so maxUnknown -1 never registered);
  2. direct function aliases and local closures lost page taint at the
     call site;
  3. container element extraction (xs[0]) was unmodeled;
  4. pointer and generic byte parameters were not page-tainted, and
     expression-position pointer dereference was unmodeled;
  5. branch and loop states overwrote instead of joined, and append
     results dropped source taint (post-loop append of a
     branch-tainted accumulator passed);
  6. multi-result calls mapped one RHS to the first LHS slot only;
  7. named array/string conversion sinks ([4096]byte(page), string(page)
     through named types) were unrecognized;
  8. file-capability laundering through nested or package-level func
     literals and os.Stdin/Stdout/Stderr aliases passed outside the
     mapping owner;
  9. the Mapping.View mint expected three arguments while the method
     takes two.
  Fix (HEAD f96d13d, v4/go-gate): summary joins treat maxUnknown as at
  least the summary max; local funcs are bound and calleeTarget chains
  resolve local plus package-level aliases with call-site-bound literal
  analysis; IndexExpr/IndexListExpr derive element page values while
  preserving the parameter source (generic xs[0] stays caller-dependent);
  typeCanCarryPage recurses through pointers and accepts type
  parameters, and StarExpr is modeled; If/Switch/TypeSwitch/Select and
  For/Range clone-and-join states, and append results carry source
  taint; multi-result assignments consume fresh per-slot callResults;
  the array/string sink check unwraps Named/Alias types; os stream
  selectors and file-bearing call results fail outside the mapping
  owner; the View mint now takes the length from Args[1].
  Pinned as battery forms P24-P38 (eleven rejects: same-package helper,
  local closure, package func-alias var, element extraction, pointer
  deref, generic identity, post-loop append, multi-result slot, named
  array conversion, named string conversion, func-lit file return;
  four accepts: bounded View copy, bounded slice through local closure,
  bounded multi-result slot, bounded loop append); battery 305 -> 320
  (246 -> 257 rejections, 59 -> 63 benign).
  Validation at HEAD f96d13d: gate --self-test 320/320 + 9 shell
  mutations exit 0, production scan clean on all five targets, go test
  ./... (both tag sets), -race, vet, gofmt zero diffs,
  cross-compilation (windows/darwin/freebsd), SOW audit green.

- Swarm round 5 (2026-08-14, at HEAD 57522e8): the independent round
  review returned ten gate findings (Luna resident) plus one
  multi-result struct-field taint gap (MiniMax P2), every one
  reproduced by the lead against the tree (exact mutation probes, gate
  exit 0 on the misses) and closed in one commit at HEAD 65ca62a:
  1. helper summaries tested only the first source (choose(nil, page,
     true) lost page taint): fs.eval/evalResults taint when ANY
     recorded source is tainted, accumulating maxLen only over tainted
     sources;
  2. named/naked returns were not summarized: analyzeFunc records
     named-result slots and stores to them feed results and struct
     fields after body analysis;
  3. void unknown function-variable calls were not transfers: an
     unproven var func([]byte) receiving a page now transfers (calls
     with a scalar result stay reads);
  4. append(dest, src...) checked only the source: a complete mapped
     page view as destination (append(page[0:4096:4096], ...)) now
     fails (full page reallocated into owned memory);
  5. index and dereference stores were untracked: slots[0] = page and
     *h = page now bind the container/pointed-to variable;
  6. range variables were never bound: range over [][]byte{page}
     derives the loop value from the container taint;
  7. interface/map/channel carriers were missing: any(page) keeps the
     argument taint, direct interface parameters carry page values
     (variadic/stdlib-form interfaces stay exempt), channel
     send/receive round trips are modeled;
  8. switch fallthrough dropped the previous case state: each case
     body joins the falling-through case end state;
  9. nested func-literal return context leaked: every ReturnStmt is
     checked against its own enclosing function result types;
  10. unproven package func vars with interface results failed open: a
      var func() io.Reader outside the mapping owner is now a
      capability launder (restricted to never-reassigned package-level
      func vars so stdlib callbacks stay clean);
  11. multi-result struct-field taint (MiniMax P2): (chunk, err)
      helpers lost field taint on the struct slot; every returned
      expression now propagates composite-literal struct fields (first
      slot wins on name collisions), evalCall records struct fields
      even when the whole result slot is tainted, and
      parameter-sourced summary bounds resolve the caller's constant
      slice symbol (page[48:112] stays a 64-byte view) instead of
      reading the zero symbol as constant (the zero symbol's isConst
      returned 0,true and collapsed full pages to len 0).
  Pinned as battery forms P39-P55 (fourteen rejects, three benigns);
  battery 320 -> 337 (257 -> 271 rejections, 63 -> 66 benign). Gate
  tooling 5,104 -> 5,465 go-gate lines (+552 shell = 6,017 total).
  Validation at HEAD 65ca62a: gate --self-test 337/337 (271 rejections,
  66 benign) + 9 shell mutations exit 0, production scan clean on all
  five targets, go test ./... (both tag sets), -race, vet, gofmt zero
  diffs, cross-compilation (windows/darwin/freebsd/netbsd), SOW audit
  green.
  Round re-review at HEAD 2f5f71a: Qwen reported the cross-file package
  func-var scope hole - collectPkgFuncVars collected only the file
  under check, so var factory func() any declared in one file and
  called from another file of the same package skipped the
  interface-result fail-closed rule (reproduced by the lead: two-file
  probe, gate exit 0; same-file control rejects). Fixed at HEAD
  0d007a8: the map is built from every parsed file of the package.
  Pinned as battery form P56 (reject, two create ops); battery 337 ->
  338 (271 -> 272 rejections, 66 benign); gate tooling 5,465 -> 5,474
  go-gate lines (+552 shell = 6,026 total). Validation at HEAD
  0d007a8: gate --self-test 338/338 + 9 shell mutations exit 0,
  production scan clean on all five targets, go test ./... (both tag
  sets), -race, vet, gofmt zero diffs, cross-compilation, SOW audit
  green.

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
  gofmt zero diffs, cross-compilation, SOW audit green.
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
  gofmt zero diffs, cross-compilation, SOW audit - all green.

Review outcome (six-resident swarm, then sol, per the user's review
process): recorded after the entry below when the review completes.
