# Pure-Go v4 Milestone 1 — Six-Agent Concurrent Gap Analysis

Date: 2026-08-11 · Head analyzed: `94723aa` · Method: six concurrent read-only
subagents, each with one narrow brief (codecs, reader semantics, architecture,
public API/errors, test evidence, worker/mapping/report facts), followed by
master triangulation of every finding against the tree, the specs, and the
Rust reference. All agents worked from the filenames and command receipts
recorded below; nothing in the repository was modified by the analysis.

## Executive summary

- **One BLOCKER**: the structured-ID radix descent computes the child index
  with a divisor one level too deep (factor 512) for directory levels ≥ 2 —
  any conforming structured file with `structure_id_limit > 25,600` is
  misread. Untested (fixtures only reach root level 0).
- **Ten MAJOR** findings span the reader (blob-walk validation, record
  limit checks, slotted-shape exactness), the binding (unguarded `Info()`,
  closed-state error class, code-46 public name), the mapping contract
  (flock-vs-OFD lifetime lock), the test evidence (absent-path, word-exact
  bitmap, info assertions), and the reports (all stale numbers).
- **Zero findings** in the meta bootstrap, range descent discipline,
  metadata decompression, CRC32C/Castagnoli coverage, error-table numbering
  1–69, and fixture integrity (byte-clean vs HEAD, no test writes the
  corpus); those areas are now cross-checked by two independent probes
  (Python byte-level) plus code reading.
- **One refuted claim**: the aux-sentinel issue (aux checks bypassed for
  aux 0) is already fixed at `v4/go/internal/format/page.go:111`
  (unconditional equality); the agent that re-flagged it examined the
  pre-fifth-pass state.
- **Three user decisions** are now concretely informed by this analysis
  (worker contract, view-lifetime + closed-state error class, deletion set).

## BLOCKER

### B1 — Structure-ID radix divides by one level too deep (structured files with `structure_id_limit > 25,600` misread)
- Spec: binary-format-v4.md §9A.1 (lines 883–889): "A level `L > 0`
  directory child covers `R * 512^(L-1)` IDs. The child index … follow[s]
  directly from the unsigned ID and those spans."
- Rust: `v4/rust/iprange-livedb/src/structured_value/table.rs`
  `child_index` (629–635) divides by `coverage(level-1)` where
  `coverage(k) = R*512^k` (636–646).
- Go: `v4/go/internal/reader/structure.go:138` divides by
  `StructureSpanOfLevel(level-1)`; `v4/go/internal/format/structure.go:121-133`
  defines `SpanOfLevel(L) = R*512^(L-1)` — so at directory level L the Go
  divisor is `R*512^(L-2)` instead of `R*512^(L-1)`: **exact for L=1,
  wrong by 512 for every L ≥ 2**.
- Consequence: `LookupNetworkEnrichmentV1*` descends into an unrelated
  subtree → corruption/absence errors on healthy Rust-written files. No Go
  test exercises `StructureIDLimit > 25,600`. Root level 0 fixtures mask it.
- Verified by master: both span conventions re-derived from code; the Go
  fix is `StructureSpanOfLevel(level)` at `structure.go:138` plus a
  synthetic multi-level structured-file test with level ≥ 2 descent.
- Severity: BLOCKER (cross-language readability of exactly the large
  structured databases the format targets).

## MAJOR

### M1 — Blob-tree walks skip validation Rust enforces (offset chains, `%8`, leaf geometry, nonfinal-full, end-vs-declared)
- Spec: binary-format-v4.md §10 (lines 991–1004): every nonfinal leaf
  `data_len == 4048`; leaves cover the declared length exactly; every
  branch-entry offset equals its child's first logical offset; a request
  beyond the declared length is corruption; `lower == 48 + data_len`,
  `upper == 4096` for blob leaves.
- Rust: `blob_tree.rs` `find_leaf` (84–148), `leaf_geometry` (258–290),
  `read_words` (86–88) — all return `Corrupt`.
- Go: `internal/reader/membership.go` `blobRead` (324–381) +
  `internal/format/blob.go` `DecodeBlobLeaf` (41–56) check only aux, level,
  item_count==1, data_len range, and single-read fit.
- Consequence: offset-disagreeing or short/nonfinal blob chains are silently
  read by Go where Rust reports corruption.

### M2 — Catalog/membership records accept values Rust classifies corrupt (index/id/word_count limits)
- Spec: §8.3 (684–696), §9.1 (739–749), §4.3 (390–396).
- Rust: `feed_catalog.rs` `decode_leaf` (196–207: `index >= feed_index_limit`
  → Corrupt); `membership_tree.rs` `require_record_fields` (138–147: `id >=
  membership_id_limit` → Corrupt, `word_count > max_words(limit)` →
  Corrupt).
- Go: `catalog.go` `nameLeafLookup` (150–175) and `membership.go`
  `membershipLeafFind`/`lookupMembershipID` never bound these against meta
  limits.

### M3 — Slotted-page shape checks looser than spec/Rust (and synthetic builders exploit it)
- Spec: §7 (603–607): `lower == 32 + 2*item_count`, records wholly within
  `[upper, 4096)`, `upper < 4096`.
- Rust: `slotted_page.rs` `shape_valid` (272–280) enforces equality +
  `upper < PAGE_SIZE`; `structured_value/table.rs` `inspect_header`
  (310–327) also checks structure pages.
- Go: `format/page.go` `OpenSlotted` (127–138) accepts `lower ≥` and
  `upper ≤ 4096`; structure directory/record pages get no lower/upper check
  at all.
- Consequence (twofold): (a) moderately nonconformant pages accepted where
  Rust corrupts; (b) the synthetic DB builders (`blob_test.go`: lower
  46/96 for slotted pages) are themselves non-canonical, so the tests
  prove the laxer reader, not spec conformance — a real overclaim.

### M4 — Mapping lifetime lock is flock(2); the writer contract is an OFD byte-range lock — cross-language exclusion is broken
- Go: `internal/mapping/mapping.go:83,129` — whole-file `unix.Flock(LOCK_SH)`.
- Rust/spec: `live_lock.rs` `set/unlock` (35–61) — `fcntl(F_OFD_SETLK[W])`
  byte-range locks at an explicit offset.
- flock(2) and OFD fcntl locks do NOT exclude each other: a Rust writer can
  hold the write lock while the Go reader maps and reads → live truncation
  SIGBUS is not prevented by the current shared lock. The reader's sidecar
  checks happen under the lock, but the lock itself is the wrong mechanism.

### M5 — Production API binding gaps (public surface)
- `Info()` is the only public method without the `ensureOpen` guard
  (`reader_public.go:164-188`); after `Close` it returns stale metadata
  with nil error — silent wrong data, untested.
- Closed-state error class: Go returns `ErrorHandleClosed` (9) for
  ops-on-closed / double-close / ops-on-released-view situations; the C ABI
  registry (the parity reference) produces `WrongState` (11) for these
  (`v4/rust/iprange-capi/src/handle.rs:551-563`, `error.rs:107`);
  `HandleClosed` is a real sdk_error code (sdk_error.rs:21) that Rust never
  emits. The Go `Release()` is idempotent where the C ABI reports
  `WrongState` on double-close; `WordCount()` on a released view returns 0
  silently (no error channel).
- Code 46's public Go name is still divergent: `errors.go:54` exports
  `ErrorLiveCoordinationDomainMismatchRequiresReset` while Rust
  (sdk_error.rs:58) and the internal table (codes.go:59) use
  `LiveCoordinationMalformedRequiresReset`; parity-matrix row 4 requires
  the rename and no test pins it.

### M6 — Test evidence gaps that would let a hallucinating reader pass
- Absent path (`found=false`) is never asserted on membership/structured
  fixtures (no probes at the fixture gaps, `to+1`, or family min/max).
- Word-exact bitmap verification (Rust `word_count` + `read_words`) has no
  Go counterpart (only partial spot-bit checks).
- `value_tag`, `MetaSelection == ProvenCurrent`, `page_count*4096 == len`,
  and `range_record_count` are never asserted, despite report claims of
  "exact info (… tag semantics)".
- Feed projections / catalog-order / manifest canonicality have no Go
  counterpart (partly scheduled, partly missing assertions).

### M7 — Report and SOW factual drift (all numbers recomputed)
- Production LOC: 5,688 raw / 5,312 non-empty lines
  (`cat internal/format/*.go internal/mapping/*.go internal/reader/*.go
  reader_public.go | grep -v '^$' | wc -l`); tests 2,730. The report's
  "~3,720 / ~1,700" is stale and was never recomputed (report §2 also
  describes the composition wrongly).
- Wrong-mode probes: 18 (not 19); zero-allocation groups: 16 checks
  (6 internal + 10 public), not "11"; the report's "11 v4 probes" is
  unsupported (all 18 probes use v4/first-seen fixtures).
- The report §10b still carries the old lock/LOC language; the fifth-pass
  repairs are not recorded in the report (SOW log has them); the SOW
  status/validation sentences contradict the current worker conclusion
  ("no assembly shim needed … proceeds pure-Go" vs B2 below); one SOW log
  entry is mangled.

### M8 — Cost-visibility and no-per-call-sync claims are partly unbacked
- The import-graph gate (`check-import-graph.sh`) enforces import
  boundaries but NOT a sync/atomic ban; the report claims the reader core
  is "sync/atomic-free, enforced by the import-graph gate" — the factory
  part is true today, the enforcement part is not. Add the rule or drop
  the claim.
- No cost-visibility machinery exists in Go (test-only counters for
  page visits / range passes / durability work).
- Hot paths re-derive on a per-word basis in membership reads (Rust
  streams per leaf); duplicate root-page fetch exists in all six tree
  descents (level probe then re-read).

## MINOR

- Bootstrap leniency vs Rust: Go `ValidateKindInvariants` omits
  `entry_count < id_limit`, the range-record capacity bound, the retirement
  bound, and the metadata geometric bound (Rust `bootstrap.rs` 327–392,
  506–517).
- **Authority conflict (needs a decision)**: for an unknown `structure_kind`
  on a direct/membership file, Rust fails the candidate as plain invalid
  (`NoBootstrapMeta`), while Go follows the spec text §4 (257–265) and
  returns `UnsupportedStructure`. Specs are authoritative over
  implementation per AGENTS.md; the Rust behavior deviates from the spec
  text. Record and decide (spec-first vs Rust-first).
- Structure-directory/record pages get no `lower/upper/item_count`
  geometry check (only type/aux/level).
- `LookupFeed` checks kind before name; Rust validates the name first —
  divergent error code for direct-DB + invalid/non-UTF-8 name.
- `MetadataJSON` allocates 0–88 bytes/run (not "only the returned
  payload"); honestly relabel it.

## INFO

- Missing format-package codecs: membership-hash (40/44 B), structure-hash
  (36/40 B), bitmap pages, structure-directory pages (reader hand-decodes
  types 18/19). Writer-milestone concern, not reader-blocking.
- `FeedCursor`, index-tree lookup, hash/reverse-index lookups, membership
  range cursors: absent (scheduled, no misbehavior).
- `SetPanicOnFault` probe: mechanism reproduced on this host
  (linux/amd64) with exact in-region fault addresses; the panic path exists
  and is goroutine-local; the SPEC contract needs SA_SIGINFO + alt stack +
  previous-disposition chaining + kernel-vs-user si_code discrimination +
  no-unwind (binary-format-v4.md §worker, lines 3080–3096; design
  engine §12) — none implementable in pure Go; "re-raise" has no Go
  primitive. The pure-Go "solution" claim in report §11 is not
  spec-text-compatible and must be rewritten.
- Fixture integrity: byte-clean vs HEAD; no test writes into the corpus
  (the earlier pollution class is closed — verified by agent 5).
- Legacy `internal/exactv4` page arrays remain until the approved deletion.

## Refuted / corrected by master triangulation

- Aux-sentinel bypass (agent 2 M3 in review text): **refuted** — fixed in
  the fifth pass; `page.go:111` enforces unconditional aux equality and all
  catalog/membership-ID call sites pass 0 with fixture aux == 0 verified.
- "HandleClosed never produced anywhere in Rust": false — `sdk_error.rs:21`
  defines it; the C ABI simply chooses WrongState. The divergence is real
  but must be framed as an error-class choice (see M5).
- Zeroalloc "11 groups" and "19 probes": recomputed to 16 checks and 18
  probes.
- Agent arithmetic conflicts on per-file LOC were resolved by direct
  recomputation (M7).

## Findings mapped to the pending user decisions

1. **Worker contract (Decision 2 re-visit)**: B2 gives the evidence —
   spec requires SA_SIGINFO/alt-stack/chaining/si_code/no-unwind; pure Go
   cannot provide them. Options: (A) minimal project-owned assembly
   sigaction shim (spec-exact, Rust mirror) — recommended; (B) spec change
   to panic-based semantics; (C) no worker.
2. **View-lifetime API + closed-state error class**: M5 defines the
   contract deltas (WrongState vs HandleClosed, release idempotency,
   WordCount signature, Info guard). Options: pointer views with shared
   state (recommended), value views with a spec change, full token
   registry.
3. **Deletion set**: unchanged position (100 tracked + 2 untracked
   leftovers), still atomic with the Milestone 2 writer commit.

## Per-agent inventories

- Agent 1 (codecs): 0 BLOCKER/MAJOR; 3 MINOR (slotted geometry; blob-leaf
  geometry unverified on read path; missing hash/bitmap/directory codecs);
  4 INFO (CRC32C + meta CRC byte-exact via independent Python probe
  `OK metas=10 pages=30 slotted_records=179 violations=0`; offsets match
  spec+Rust+fixtures; codes.go 1:1 with sdk_error.rs; decode-only).
- Agent 2 (reader semantics): B1 + M1 + M2 + M3(minor) + M5(minor) +
  INFO 7/8; confirmed matching: bootstrap matrix, range descent, absence
  vs corruption for range/membership, metadata stream, txn binding.
- Agent 3 (architecture): M8 + M7(LOC) + MINOR (MetadataJSON allocs,
  Info guard) + INFO (exactv4 arrays, SetPanicOnFault reproduction);
  confirmed: import boundaries, mapping checks present, 16 zeroalloc
  groups pass, no hidden page arrays in new tree.
- Agent 4 (public API): M5 (all four parts) + MINORs (LookupFeed
  precedence, probes/report counts) + INFO (65–69 present, enum numbers
  differ); confirmed matching: wrong-kind/family/structure codes and
  messages, typed error conversion, callback passthrough, no ID/pointer
  exposure.
- Agent 5 (test evidence): M3(builders) + M6 + MINORs (boundary probes,
  report drift) + INFO (fixture cleanliness, gates green, invalid-mutation
  parity exact).
- Agent 6 (worker/mapping/facts): M4 + B2-worker + M7 (report facts:
  4 mechanical errors, stale placeholder, missing fifth-pass section, SOW
  self-contradiction) + INFO (control fields 197 verified;
  macOS/FreeBSD/Windows unproven).

Master verification commands: `sed -n` on `structure.go:125-150`,
`table.rs:625-646`, `page.go:100-145`, `check-import-graph.sh` (read),
`live_lock.rs:35-61`, `mapping.go:83,129`, `sdk_error.rs:21-23`;
`awk`/`grep` recounts (18 probes, 16 zeroalloc checks); `wc`/`cat` LOC;
`go test -count=1 ./...`, `go vet ./...`, `gofmt -l .`,
`go test -race ./internal/format ./internal/reader .`, cross-builds
(darwin/arm64, windows/amd64, freebsd/amd64, linux/386) all green at HEAD.
