# SOW-0015 - v4.3 Audit Round 3: 7 Hardening Fixes (Go + Rust)

## Status

Status: in-progress

Sub-state: Round 3 implementation landed; Round 4 test-only baseline is complete
and implementation repair is pending

## Requirements

### Purpose

Harden the v4.3 streaming-mmap COW engine against 7 remaining correctness,
data-loss, and performance defects discovered in audit round 3. Every fix must
land in BOTH the Go and Rust implementations (where the affected code path
exists), with a failing test written and verified before each fix.

### User Request

For each of 7 issues: write a failing test, verify it fails, fix the code,
verify it passes, run the full suite for regressions. Work in both Rust and Go.

On 2026-07-14, the user approved adding every confirmed Round 4 defect directly
to the normal Go and Rust test suites before any implementation fix. The tests
must remain as permanent behavioral and corruption regressions. A separate
assistant will repair the implementation against those tests.

### Assistant Understanding

Facts at the Round 3 starting baseline:

- Both suites passed before the Round 3 changes (Go `go test ./...`; Rust
  `cargo test`). Round 4 intentionally makes the suites red until the newly
  recorded contracts are implemented.
- The 7 issues are genuinely untested/ unfixed as of the starting tree.
- The codebase uses per-page CRC32C (`verifyPage` / `crc32c::verify_page`), a
  persistent tombstone free-list chain, a reader companion file with flock'd
  registration, and an mmap'd COW B+tree.

### Acceptance Criteria

- A failing test exists for each of the 7 issues and now passes after the fix.
- The opposite language has a matching test+fix where the code path exists.
- Full Go and Rust suites pass with no regressions.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

1. **MVCC race (commit vs reader register):** `FileWriter.Commit` queries
   `oldest_reader_txn_id()` with NO lock; reader `Register()` takes flock. A
   reader registering between the query and the meta-flip/LoadFreeList is
   invisible to the writer, whose stale `oldest=MaxUint64` free-list then
   recycles pages the reader's pinned txn still references. Evidence:
   `v4/go/os.go:237-241`, `v4/go/readers.go:186-198` (OldestReaderTxnID no lock),
   `v4/go/readers.go:124` (Register takes flock); Rust mirror `os.rs:179-183`,
   `readers.rs:151-161`.
2. **OpenFile truncates corrupt file before rejecting:** `OpenFile` falls
   through to `metaB.totalPages` when BOTH meta CRCs fail, then calls
   `newMmapStore` which `file.Truncate`s to that (garbage) size BEFORE
   `openWriter` rejects. Evidence: `v4/go/os.go:186-202`,
   `v4/go/page_store.go:93-101` (Truncate at line 99).
3. **Validate() skips scope-table CRC:** `validateScopeTable` calls
   `readAllScopes`/`read_all` which never calls `verifyPage`. Evidence:
   `v4/go/reader.go:397-405`, `v4/go/scope_table.go:330-360`; Rust
   `reader.rs:138-153`, `scope_table.rs:294+`.
4. **Writer trusts corrupt scope/free-list pages:** `ReadChain`/`read_chain`
   and `readAllScopes`/`read_all` never verify CRC; corrupt chain or scope
   pages are silently used. Evidence: `v4/go/free_list.go:44-77`,
   `v4/go/writer.go:197,715`; Rust `free_list.rs:33-64`, `scope_table.rs:294+`.
5. **Truncated spill files silently lose IPs:** `read_record`/`runReader.advance`
   treat partial-read `UnexpectedEof` as clean EOF. Evidence:
   `v4/rust/.../extsort.rs:485-502`; `v4/go/extsort.go:367-379`.
6. **Go Set allocates on the hot path:** `scanFirstOverlap` returns a heap
   pointer; `compactIfNeeded` runs+allocates on every delete.
7. **Compaction check runs on every Set/Delete:** `deleteRange` calls
   `compactIfNeeded` (full `countTreePages` walk) per delete → O(n²).

Evidence reviewed:

- All `v4/go` and `v4/rust/iprange-livedb/src` files named above (read in full).
- Existing audit tests: `reaudit2_test.go` (C1-C4, A1), `reaudit_test.go`,
  `audit_regressions_test.go`, `robustness_test.go`, `v43_fixes_test.go`.
- SOW-0014 (streaming-mmap-cow-engine) for the design contract.

Affected contracts and surfaces:

- Go: `os.go` (OpenFile, FileWriter.Commit), `readers.go` (lock helper),
  `reader.go` (validateScopeTable), `scope_table.go` (CRC-validating walk),
  `free_list.go` (ReadChain CRC), `writer.go` (openWriter scope CRC,
  compactIfNeeded move, zero-alloc overlap), `extsort.go` (truncation).
- Rust: `os.rs`, `readers.rs`, `reader.rs`, `scope_table.rs`, `free_list.rs`,
  `writer.rs`, `extsort.rs` (mirror changes).
- Public API unchanged; only error-rejection and internal hot-path changes.

Existing patterns to reuse:

- `verifyPage` / `crc32c::verify_page` for per-page CRC.
- `readScopeNode` walk structure for scope validation.
- Existing flock-on-companion-file pattern in `Register`/`UpdateTxnID`.
- `overlapInfo` value-return pattern (see `overlap.go` callback style).

Risk and blast radius:

- Issue 1 (MVCC lock): holding a lock during commit adds brief reader-open
  latency. Acceptable (LMDB model). Risk: deadlock if writer also needs the
  reader-table mmap lock — mitigated by using a SEPARATE flock fd.
- Issue 2: rejecting both-CRC-fail in OpenFile changes an edge error path only.
- Issues 3-4: stricter CRC may reject previously-accepted corrupt files —
  desired (fail closed).
- Issue 5: spill truncation now errors instead of silently losing records —
  may surface previously-hidden corruption. Desired.
- Issue 6: zero-alloc refactor must not change tree semantics.
- Issue 7: moving compaction to commit changes when it runs, not whether.

Sensitive data handling plan:

- No secrets/PII in scope. Tests use synthetic IP ranges only.

Implementation plan:

1. Issue 5 (extsort truncation) — both languages, independent.
2. Issue 2 (Go OpenFile corrupt-file guard) — Go only.
3. Issue 3 (Validate scope CRC) — both languages.
4. Issue 4 (writer CRC on scope + free-list) — both languages.
5. Issue 7 (move compactIfNeeded to commit) — both languages.
6. Issue 1 (MVCC commit-reader lock) — both languages.
7. Issue 6 (Go zero-alloc Set) — Go only; depends on 5/7 being clean.

Validation plan:

- Per-issue: write test, `go test -run` / `cargo test` to confirm FAIL, fix,
  confirm PASS.
- After all: `go test ./... --count=1` and
  `cargo test --manifest-path iprange-livedb/Cargo.toml --features slow-tests -- --test-threads 1`.

Artifact impact plan:

- AGENTS.md: no change (workflow unchanged).
- Runtime project skills: none yet exist; not applicable.
- Specs: `design-iprange-engine.md` is target-direction; these are bugfixes,
  no spec change.
- End-user/operator docs: wiki/ unchanged (internal fixes).
- SOW lifecycle: this SOW tracks the batch.

Open decisions:

- None blocking. The user specified all 7 fixes in detail.

## Audit Round 4 - 2026-07-14

### Requirements

Purpose:

- Make the v4 implementation failures reproducible in the repository itself,
  in both languages, so fixes cannot silently regress.
- Test public behavior and durable-file behavior rather than private
  implementation details wherever the current API permits it.
- Keep tests deterministic. Resource-bound failures use process isolation and
  fixed operating-system limits; algorithmic speed uses benchmarks rather than
  wall-clock pass/fail thresholds.

User decision:

- Add all confirmed tests now. Do not fix implementation code in this step.
- Put the tests in the normal Go and Rust suites. The implementation work will
  follow against these failing contracts.

Acceptance criteria for the test-only step:

- Every confirmed defect has a permanent Go and Rust regression where the
  affected public surface exists.
- Corruption fixtures are independently forged and do not rely on another
  known implementation defect to create malformed state.
- Tests compile, static checks pass, and every red result is traced to its
  named implementation contract rather than a race, timeout, or harness flaw.
- No v4 implementation code is changed in the test-only step.

### Round 4 Pre-Implementation Gate

Status: ready

Problem / root-cause model:

1. The indirect-scope file record has a fixed 256-byte bitmap payload while the
   API and design describe unlimited feed bits. Feed bit 2048 grows an in-memory
   bitmap to 257 bytes and is then truncated on persistence.
2. Inclusive overlap cardinality uses `uint64` / `u64`; an IPv6 range containing
   exactly 2^64 addresses wraps to zero instead of reporting that the exact
   count cannot fit the current API.
3. Writable file open trusts checksum-valid metadata geometry before complete
   structural validation. It can accept `total_pages=2` for a non-empty file and
   truncate the original before rejecting it.
4. Multi-level scope-tree construction uses incorrect parent separators. Point
   lookup crosses the first multi-level boundary incorrectly at about 7,635
   scopes even though a full scan still sees the entries.
5. Feed-bit range updates at the address-family maximum duplicate the terminal
   address and commit a non-disjoint tree.
6. Free-list validation verifies page checksums but does not prove that every
   listed page is allocatable and unreachable from metadata, data, scope, and
   free-list roots. Cycles are also not rejected with a bounded traversal.
7. Migration mutates the transaction before it knows that its source completed
   successfully. A late source error or malformed desired stream leaves partial
   state that can still be committed.
8. Foreign-vs-all assumes sorted, disjoint, valid foreign input but does not
   validate the precondition, allowing double counting or under-counting.
9. Mode-2 mutation accepts dangling scope IDs, and bitmap mutation silently
   treats unknown scope IDs as empty scopes.
10. Bitmap canonicalization retains trailing zero bytes, creating different
    scope IDs for identical memberships.
11. Overlap APIs silently return empty success for scalar mode, where feed-bit
    interpretation is undefined.
12. Scope validation checks local ordering but misses cross-leaf ordering and
    separator invariants.
13. `normalize_overlapping` drops a range whose tail reaches the maximum family
    address.
14. External merge opens all spill runs concurrently, so a valid workload fails
    under an ordinary bounded file-descriptor limit.
15. Sorter lifecycle is incomplete: caller-owned config is retained, Go allows
    use after finish, and abandoned streams/sorters cannot always release spill
    files deterministically.
16. All-to-all documentation promises total overlap per feed pair while the
    implementation emits one callback per fragment.
17. Commit writes the new active metadata before its only `sync`, so data pages
    and the commit marker do not have an enforced durability barrier between
    them. A crash can expose metadata that references data not yet durable.
18. Store sync/allocation failures return an error without poisoning the writer.
    The caller can continue mutating or commit again after a partially completed
    transaction whose in-memory and durable state are no longer trustworthy.
19. Trusted reader lookup/scan paths use checksum-valid page entry counts without
    complete bounds protection. A hostile leaf can make a public read operation
    panic instead of returning a bounded failure.

Evidence reviewed:

- Go probes executed against commit `5b61681e2d95ebee9571670305e4634bff80c6c2`.
- Rust probes executed against the same commit.
- Format and API implementations in `v4/go/` and
  `v4/rust/iprange-livedb/src/`, including scope tables, overlap, migration,
  external sort, file open, free lists, and interval normalization.
- Existing tests were searched for each defect. Existing scan-only scope tests,
  clean-EOF spill tests, and local page-order validation do not cover these
  contracts.

Affected contracts and surfaces:

- Public writer, migration, overlap, interval, scope, and external-sort APIs.
- Persistent meta, B+tree, scope-tree, and free-list validation.
- Unix file open and non-destructive corruption rejection.
- Go and Rust parity for every shared behavior.

Existing patterns to reuse:

- Existing synthetic in-memory images and checksum-restamping helpers.
- Existing file-writer tests using temporary directories.
- Existing cross-language mirrored test scenarios and exact boundary fixtures.
- Subprocess isolation already used by the test suite for process-level
  behavior.

Risk and blast radius:

- The new tests intentionally make both suites red until implementation fixes
  land. Each failure must be a contract failure, not a timing or environment
  accident.
- Corruption tests must never operate on real data. All files and images are
  synthetic and temporary.
- The bounded-FD test changes limits only in a child process.
- No production file-format migration or destructive operation is performed.

Sensitive data handling plan:

- Tests use synthetic addresses, temporary paths, and generated files only.
- SOW and test diagnostics contain no secrets, customer data, private
  endpoints, or production artifacts.

Implementation plan:

1. Add Go behavioral regressions for boundaries, scope identity, migration,
   overlap aggregation, and malformed input.
2. Add Go corruption and Unix resource regressions for writable open, free-list
   reachability, sorter descriptors, and deterministic cleanup.
3. Add equivalent Rust regressions with the same fixtures and expectations.
4. Add fault-injected page-store tests for commit ordering and permanent
   transaction poisoning after storage failures.
5. Format and compile the suites, then run targeted tests to prove each failure
   is caused by the intended defect.
6. Leave implementation changes and final green-suite closure to the follow-on
   repair step in this same SOW.

Validation plan:

- `go test ./... --count=1`
- `go test -race ./... --count=1`
- `cargo test --workspace --all-features`
- Targeted Go and Rust runs for each new regression, recording intentional
  failures separately from compilation errors or test defects.
- `go vet ./...`, `cargo fmt --all -- --check`, and
  `cargo clippy --workspace --all-targets --all-features -- -D warnings` after
  implementation repairs make the behavior suites green.

Artifact impact plan:

- `AGENTS.md`: no workflow or project-wide guardrail change.
- Runtime project skills: none exist in this repository.
- Specs: no format promise is changed by test-only work; tests expose conflicts
  that implementation must resolve against the locked specs.
- End-user/operator docs and skills: unaffected by test-only work.
- SOW lifecycle: SOW-0015 remains `in-progress` until fixes pass both full
  suites and all Round 3/4 acceptance criteria.

Open decisions:

- None for the test-only step. Where an API can either reject malformed input
  or normalize it without losing accuracy, the regression accepts either
  outcome and rejects only silent wrong results.
- With the current `uint64` / `u64` overlap callback, an exact 2^64 count must
  return an explicit overflow error. Returning any representable but inaccurate
  value is not accepted.

## Plan

1-7. As listed in Implementation plan (issue-by-issue TDD, both languages).

## Execution Log

### 2026-07-13

- SOW created; all 7 issues investigated, evidence gathered (file:line above).

### 2026-07-14

- Added 31 Go Round 4 tests across behavioral, corruption, Unix resource,
  lifecycle, and transaction-fault suites, plus one nested-normalization
  benchmark.
- Added 29 equivalent Rust tests across four normal integration-test binaries,
  plus the matching Criterion nested-normalization benchmark.
- Corrected two pre-existing test-harness defects found by validation: the Go
  multi-process test now synchronizes its child-output buffer, and the Rust
  process-global allocation counter serializes test bodies in its integration
  binary.
- Made corruption fixtures independent: dangling record scopes are forged into
  otherwise-valid committed images, so fixing mutation validation does not
  break corruption-test setup.
- Made storage fault injection one-shot and made the IPv6 2^64 case require an
  explicit overflow error, preventing false-positive implementations.

## Validation

Acceptance criteria evidence:

- A failing test exists for each of the 7 issues and now passes after the fix.
  Each test was verified to FAIL before its fix during TDD (I5 runReader,
  I2 OpenFile, I3 Validate, I4 scope+freelist, I6 allocs, I7 page-walk, I1
  lock-blocks).
- The opposite language has a matching test+fix where the code path exists.
- Full Go and Rust suites pass with no regressions.

Tests or equivalent validation:

- Go: `go test ./... --count=1` → **213 tests, all pass** (was 203 baseline;
  +10 new tests in `v43_audit3_test.go`).
- Rust: `cargo test --features slow-tests --all-features -- --test-threads 1`
  → **219 tests, all pass** (+6 new: 5 in `tests/audit3.rs`, 1 unit test in
  `extsort.rs`).

Same-failure scan:

- The CRC-validating free-list chain (issue 4) exposed a pre-existing
  dangling-freeListHead bug: when a file-backed writer with an active reader
  commits, the free-list chain page can land beyond the committed region and
  get overwritten by the next transaction's COW growth, orphaning the chain.
  ReadChain is kept lenient at commit time (stops at non-TXN_FREE pages, as
  before) to avoid crashing on this known case; strict CRC validation runs
  only at open time (ValidateChainCRC). Tracked as a follow-up.

Sensitive data gate:

- No secrets/PII in any durable artifact. Tests use synthetic IP ranges.

Artifact maintenance gate:

- AGENTS.md: no change (workflow unchanged).
- Runtime project skills: none exist yet; not applicable.
- Specs: no change (these are bugfixes to current behavior, not spec changes).
- End-user/operator docs: wiki/ unchanged (internal fixes).
- SOW lifecycle: this SOW tracks the batch; status in-progress pending commit.

Follow-up mapping:

- Dangling freeListHead under MVCC churn (chain page beyond committed region):
  tracked here; needs a dedicated SOW. The chain is orphaned silently
  (free-list lost for that segment), causing potential space growth under
  long-lived readers.

## Round 3 Outcome

All 7 issues fixed in both Go and Rust with TDD. Full suites green.

## Round 3 Followup

- Dangling freeListHead: freeListHead can point beyond the committed region
  after a commit with an active reader + COW growth. The chain page gets
  overwritten by the next transaction. Needs a dedicated SOW.

## Round 4 Test-Only Validation

Compilation and static checks:

- Go `go test ./... -run '^$'`: passes.
- Go `go vet ./...`: passes.
- Rust `cargo test -p iprange-livedb --all-features --no-run`: passes.
- Rust `cargo fmt --all -- --check`: passes.
- Rust `cargo clippy -p iprange-livedb --all-targets --all-features -- -D warnings`:
  passes.
- Rust `cargo check -p iprange-livedb --all-features --benches`: passes.
- Repository `git diff --check`: passes.

Intentional-red behavior baseline at commit
`5b61681e2d95ebee9571670305e4634bff80c6c2`:

- Go `go test ./... -count=1 -timeout=3m`: 30 intended top-level failures.
  The 31st new test, malformed leaf entry-count safety, already passes in Go.
- Go `go test -race ./... -count=1 -timeout=5m`: the same 30 intended
  failures and zero data-race reports.
- Rust `round4_corruption`: 7 intended failures.
- Rust `round4_os_regressions`: 4 intended failures.
- Rust `round4_regressions`: 15 intended failures.
- Rust `round4_transaction`: 3 intended failures.
- Rust pre-Round-4 unit and integration binaries pass before Cargo reaches the
  first intentionally failing Round 4 binary.
- The Rust allocation suite passes five consecutive runs after harness
  serialization. The Go multi-process stability test passes under `-race`.

## Round 4 Test-Only Outcome

- Test-only scope is complete. The normal Go and Rust suites now preserve all
  confirmed Round 4 edge cases and failure modes.
- No v4 implementation source was changed.
- SOW-0015 remains `in-progress`; completion requires implementation repairs,
  both full suites green, benchmark review, and final artifact maintenance.
