# Resource proof at the product interface (SOW-0028 delivery step 5)

Evidence record for the bounded-resource contract of the iprange v1 JSON-RPC
products: documented ceilings, the new declarative case, response-object
enforcement, adapter memory bounds, and one measured memory number.
Latency/throughput and raw engine RSS ceilings are step-6 benchmark territory.

## Documented ceilings and where they live

- `v4/cli/README.md:32-40`: 1 MiB input/output frames; 65,000-byte response object (`output_limit`); batches 1..=16; queued requests 16 (`server_busy`); reader/cursor handles 64 each (1,024 tombstones per family); lookup batch 4,096; cursor page 4,096.
- Client authority: `schema/frame.py:24-28`; response-object ceiling `frame.py:275-286`. Products: Rust `iprange-cli/src/rpc/framing.rs:16-21`; Go `internal/cli/rpc/framing.go:24-34`; handle bounds `internal/cli/handlers/reader.go:21`, `cursors.go:21`.
- Capability object: `schema/results.py:1316-1327`, asserted at `results.py:1431-1445`.

## New case evidence: `v4/cli/cases/resource.limits.json`

Consumer-only, 134 steps, shared seed-0 direct fixture; passes in the `rust` and `go` matrices; mixed matrices skip it as "not cross-producer" (`run.py` run_one: capability split by actor set).

1. `system.describe` asserts the complete 9-member `limits` object.
2. `output_limit`: legal 4,096-address `reader.lookup` (lookup batch ceiling) whose inline result cannot fit 65,000 bytes; both products answer `output_limit` / `read_only_failure` (envelope -32010).
3. Reader capacity: 64 `reader.open` on one connection succeed; the 65th fails `server_busy` / `not_started` in both products.
4. Cursor capacity: 64 `reader.ranges.open` on one reader succeed; the 65th fails `server_busy` / `not_started` in both products.
5. Batch-of-17 `server_busy` is not asserted: the schema sends one object per step and `expect_error` covers product errors only; empirically both products reject a 17-element batch frame with transport -32600 before queue admission, and the runner serializes requests one at a time (`run.py` JsonRpcService.call requests under one lock). The queued bound is a product constant (Rust `framing.rs:21`; Go `framing.go:34`, `session.go:542`). PROVEN: capacity `server_busy`, frame-layer batch bound 1..16. NOT PROVEN here: the >16-in-flight `server_busy` race.
6. The declarative capture model aliases handles by name (schema `capture` items may be `{"name", "path"}`; `run.py` process_captures), so several handles on one result path coexist under distinct names; the case still does not close all 64 handles individually (the capacity boundary is per-connection and fully proven by the 64+1 opens). The runner terminates the per-case product process at case end (`run.py` close_services).

## Ceiling enforcement and adapter memory

- Oversized inline success is replaced by `output_limit`: Go `rpc/session.go:587-636`, Rust `rpc/session.rs:742-810`; over-limit frames fail -32001 and close the connection (`frame.py:95-97`).
- Adapters hold opaque handles and bounded response objects only; request sizes are bounded (lookup <=4,096 addresses, cursor pages <=4,096 records, `schema/methods.py:83-93`), and the database file is never materialized in adapter memory (mmap-only SDK; Go binary imports only the public SDK, `README.md:11-12`; entry `v4/go/cmd/iprange/main.go:16-31`).
- Existing gates: sensitivity gate 14 modes (`sensitivity_gate.py` MODES), mmap syscall trace gates `v4/rust/check-mmap-runtime.sh` and `check-mmap-storage.sh`, legacy C suite `tests.d/`.

## Measured memory (one representative matrix)

Go matrix, fresh work dir, under `/usr/bin/time -v` (final
qualification binaries, two runs): peak RSS **25,924 kB** and
**26,044 kB** (Python runner + product child measured together),
elapsed ~0.05 s, exit 0.  Milestone-5 methodology note (wave-10):
the step-6 CPU/peak-RSS 1.3x acceptance contract needs
product-child-only attribution; the recorded number measures the
runner and the product child together (`/usr/bin/time -v` around
the matrix run), so it cannot support the ceiling claim without
separating the child process. Per-request product memory is bounded
by the 65,000-byte response object and 1 MiB frame ceilings.

## PROVEN vs deferred

PROVEN: advertised limits; `output_limit` on oversized inline results; reader/cursor 64+1 capacity (`server_busy`); frame-layer batch bound 1..16; bounded adapter memory by design and gates; `maintenance.list` reports the `scratch`/`reservation`/`publication_temp` kinds on Linux (case-backed); `maintenance.remove` against a real abandoned-scratch attempt ID
(crash scenario C: list -> remove -> proved currently absent with
`cleanup_state: clean` and no housekeeping artifacts, both product
languages; on POSIX the unlink is directory-synced under the
standard filesystem contract).

The former NOT-PROVEN items are now proven at the product interface
by `resource_harness.py` (evidence `evidence/resource.json`) and
`windows_housekeeping_harness.py` (evidence
`evidence/windows-housekeeping.json`, executed on the authorized
Windows validation host):

1. The >16-in-flight `server_busy` race — one slow 500,000-range
   export pipelined with 19 `system.describe` frames on one stdin
   blob: both product binaries must answer exactly 3 describes with
   transport -32002 behind the single in-flight export and the
   remaining 16 with results (the 16-deep queue bound; which ids sit
   at the admission boundary races by a small timing window, so only
   the count and the id coverage are asserted), the in-flight export
   answers -32010 `cancelled` (EOF cancels the active unit), and the
   process exits 0 — the identical session contract for both
   products.  A 500,000-line feed is the verified minimum that keeps
   the queue occupied.
2. The -32001 over-limit-frame close path — one >1 MiB frame is
   followed in the same stdin stream by a valid `system.describe`
   sentinel; exactly one -32001 response appears with a null id
   (the sentinel is never parsed — trailing bytes are provably
   ignored), stdout drains to EOF with zero further bytes, and the
   process exits non-zero in both products (startup/framing failure,
   iprange-jsonrpc-v1.md shutdown section).
3. `maintenance.remove` against a real reservation nonce — a publish
   killed at the reservation marker, one `maintenance.list`
   reservation row, the row passed unchanged to
   `maintenance.remove` (the opaque-entry contract: the removal
   entry is exactly what `maintenance.list` emitted, never rebuilt
   or decoded), `maintenance.remove` returns ok and the reservation
   is proved currently absent in both products with
   `cleanup_state: clean` and no housekeeping artifacts (the POSIX
   unlink is directory-synced under the standard filesystem
   contract; the Windows-only housekeeping kind truthfully reports
   the documented `crash_reappearance_possible` state with no
   power-loss guarantee for the final unlink).
4. CLI cancellation — one stdin blob pipelines a slow export
   (id 1), the `iprange.v1.cancel` notification naming it, and one
   `system.describe` (id 2): the cancelled export never answers with
   a result (the session suppresses explicitly-cancelled ids;
   -32010 `cancelled` is the EOF-path answer from proof 1), the
   describe still answers with a result (the dispatcher stays
   responsive), and the process exits 0 — both products.
5. The `windows_housekeeping` maintenance kind — on the authorized
   Windows validation host (Microsoft Windows 11) each Windows-built
   product binary proves two things.  (1) The native refresh
   exercise: `retention.first_seen.refresh` with a `removals_output`
   behind a pinning live reader completes, publishes the exact
   removal log with the refresh value, leaves no private
   `.removals.tmp` residue (the Go removalCollector Windows fix is
   covered natively), and the pinning reader closes cleanly.  (2)
   The deterministic GC pair proof: product-written GC envelopes are
   timing-dependent (the retirement cleanup machine completes them
   best-effort), so the harness builds one format-valid 8,192-byte
   authenticated envelope plus its inert payload twin from the
   committed codec and creator-only DACL constants
   (`gc_envelope_windows.py`), then proves `maintenance.list`
   reports 0 entries on an empty directory and exactly the two clean
   candidate rows (envelope and inert payload) on the pair
   directory — entries == listed rows, UTF-16LE basename encoding,
   authenticated directory identity equal across both products —
   and `maintenance.remove` with the listed envelope row passed
   unchanged removes the pair with a proved-currently-absent
   result (crash_reappearance_possible) and a zero-row
   after-listing.  On non-Windows platforms both products truthfully
   refuse with `os_unsupported`/`read_only_failure` (Linux negative
   recorded by the same script over the same refresh-built
   directory).

The Windows qualification additionally found and fixed three product
adapter defects: two Go-only temporary-close defects
(`v4/go/internal/cli/fileio/export_writer.go` and
`v4/go/internal/cli/handlers/live.go`, the first-seen refresh
removal-output collector) removed their private temporaries while
the files were still open, which fails on Windows (Go files do not
share DELETE; both writers now close the temporary before
publication/removal), and one cross-language round-trip defect found
by the deterministic pair proof: both `maintenance.remove`
windows_housekeeping field decoders required the optional `artifact`
and `problem` members that `maintenance.list` omits on clean rows
(`v4/go/internal/cli/handlers/maintenance.go` and
`v4/rust/iprange-cli/src/rpc/handlers/maintenance.rs`); both now
treat those members as optional, so the unchanged list row
round-trips (spec `.agents/sow/specs/iprange-jsonrpc-v1.md:968`).
The round-6 std::path parity repair (external round-5 P1:
`requirePublicationParent` used raw `filepath.Base` and rejected
trailing-dot destinations Rust accepts) introduced the shared
`internal/pathname` port of the Rust 1.97.1 `std::path` component
machine and rewired every name/parent derivation site (handlers,
publication binding, live namespace, sidecar paths, reader,
recovery, worker/system deps discovery); the native Windows suite
then pinned and fixed the Windows prefix parser
(`sys/path/windows_prefix.rs parse_prefix` port; `//a//b` is not a
UNC prefix and `"C:."` has no file name or parent).  The current
canonical Linux identities are recorded in `evidence/README.md`
(Go product `5095c208…`, Go worker `795362f2…`, Rust product
`07c4e314…`, Rust worker `77b6d086…`, fixture `df3623a6…` at the
round-11 repair product revision `5dd8e010` — the Go
binary was rebuilt with `-buildvcs=false` after the parity and tester
reviews proved the temporary and `@`-expansion path joins used an
unconditional separator where Rust `PathBuf::push` inserts one only
when the base does not already end with it and never after a bare
drive prefix (drive-relative temporaries moved to the volume root);
the round-5 whole-milestone review proved the Windows pathname port
checked the physical root with the verbatim separator set, so a `/`
directly after a verbatim prefix (`\\?\\C:/x`) leaked into file names,
accept-gate results, and sidecar derivations that Rust never produces
(the repair also mirrors the Rust verbatim push rebuild for
`with_file_name`); the Rust binaries carry from the round-4 qualified
build at `ed29e437`); the Windows-host products are Go `02e7daa7…`
(worker `1dac468e…`) and Rust `c960a64f…` at the same
revision.  The wave-13 role-round delta found the Rust EOF arm
missing the ceiling check that Go has — a final unterminated frame
of LIMIT+1 bytes at EOF now exits non-zero in both products; the
wave-14 delta repaired held over-limit frame reporting, the Windows
basename round-trip, and the resource-proof residue drains.  The
signal-path forced-exit floor is ~1.05-1.07 s (1 s watchdog + 50 ms
diagnostic grace); the graceful fatal path exits within ~60 ms of
the session failure even with a full stderr pipe; an over-limit
input frame (with or without a terminator, including the
EOF-resolved LIMIT+1 shape) answers -32001 (id null) and exits
non-zero in both products.

Wave-14: proofs a and d additionally require the stdout stream
to drain to EOF with zero trailing bytes after the expected
responses (the harness drains the pipe and asserts no residue; a
self-test control pins the detection).

Deferred to delivery step 6: latency/throughput and engine RSS
ceilings only.  Wave-10 note: the D1-A signal contract adds a
deliberate ~25 ms grace wait to every clean-EOF exit in both
products (measured ~31 ms total per session); milestone 5 must treat
that as the per-session floor in latency benchmarks.

Wave-16, round 9 (external whole-milestone control repair): the
Go Windows prefix parser again matches Rust std byte-for-byte —
`parsePrefix` normalizes the eight-byte prefix header before the
verbatim-UNC match (a `/` inside `UNC\` keeps VerbatimUNC, exactly
like `PrefixParser::get_prefix`), `parseUNC` no longer absorbs the
share's trailing separator (doubled-separator share spellings no
longer double separators in derived parents), and the golden corpus
grows to 201 rows (five forward-slash UNC/verbatim-UNC shapes
probed natively on the Windows host).  `FileName` is an
allocation-free backward walk (one allocation only in the
8-byte verbatim-header normalization for `\\?\\`-prefixed
spellings); `rejectLiveSelf` probes the bound
main-name spelling; the Rust thread-creation tripwire asserts the
watchdog marker inside its checked region.  The destination
preflight tests deliver accept shapes via raw parents, and the raw
parent helper anchors at the volume root so the suite passes
natively on Windows.  Re-qualified at `bfc60f96`: Linux Go
`23e4730a…` / worker `d83854dc…`; Windows Go `37a3e563…` / worker
`c92b804b…`; Rust carried `07c4e314…` / `c960a64f…`; full battery,
Windows 23/23 suite, and Windows housekeeping 2/2 all PASS.

Wave-16, round 10 (role-round FAIL repair): the portability role
proved two classes at the round-9 revision and both are repaired at
`016010fc`.  (1) P1 — the round-9 `rejectLiveSelf` rewrite dropped
the destination name-rule gate and an overlong live-snapshot
destination answered `io` instead of `name_invalid`; the preflight
now mirrors the Rust `Destination::bind` error order (component rule
before the parent open, length rule after the parent open and before
the main-name open, parent error winning when both fail) and both
products answer `name_invalid` byte-identically on the live wire.
(2) P2 — `verbatimPushRebuild` now folds the pushed path's
components exactly like `PathBuf::_push`'s verbatim branch (CurDir
vanishes, ParentDir pops the last Normal, RootDir truncates to the
prefix) with the disk-prefix need_sep rule; 37 native-rustc-derived
rows pinned and the wine oracle differential passes 324/324 Windows
and 431/431 POSIX.  Re-qualified at `016010fc`: Linux Go
`eab62a09…` / worker `2148bc0e…`; Windows Go `64854dfa…` / worker
`06128e96…`; Rust carried `07c4e314…` / `c960a64f…`; full battery,
Windows 23/23 suite, and Windows housekeeping 2/2 all PASS.

Wave-16, round 11 (role-round FAIL repair): the round-10 revision
passed six of seven roles; portability FAILed again on the verbatim
fold's ParentDir rule (Go popped the last Normal anywhere, Rust only
when the last buffer element is Normal) and glm confirmed the same
cell.  The fold now pops exactly like Rust, `Push` implements the
complete `PathBuf::_push` contract (need_clear replacement for
absolute/prefix-carrying names, the verbatim component fold, the
rooted-name truncate to the base prefix, and the separator rules),
and `WithFileName` routes through `Push` like `set_file_name`.  The
wine oracle differential passes WINDOWS 591/591 and POSIX 437/437
(zero mismatches) with 69 committed pinned rows.  Re-qualified at
`5dd8e010`: Linux Go `5095c208…` / worker `795362f2…`; Windows Go
`02e7daa7…` / worker `1dac468e…`; Rust carried `07c4e314…` /
`c960a64f…`; full battery, Windows 23/23 suite, and Windows
housekeeping 2/2 all PASS.
