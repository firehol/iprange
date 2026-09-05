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
**26,044 kB** (Python runner; the product child runs beside it),
elapsed ~0.05 s, exit 0. Per-request product memory is bounded by
the 65,000-byte response object and 1 MiB frame ceilings.

## PROVEN vs deferred

PROVEN: advertised limits; `output_limit` on oversized inline results; reader/cursor 64+1 capacity (`server_busy`); frame-layer batch bound 1..16; bounded adapter memory by design and gates; `maintenance.list` reports the `scratch`/`reservation`/`publication_temp` kinds on Linux (case-backed); `maintenance.remove` against a real abandoned-scratch attempt ID (crash scenario C: list -> remove -> durable absent, both product languages).

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
   is durably absent in both products.
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
   unchanged removes the pair with durable absence and a zero-row
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
The Linux qualification binary hashes changed to `2f1d2bba…` (Go)
and `86056181…` (Rust) as a result; behavior on Linux is unchanged
outside the round-trip acceptance.

Deferred to delivery step 6: latency/throughput and engine RSS
ceilings only.
