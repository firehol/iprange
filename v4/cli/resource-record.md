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

Go matrix, fresh work dir, under `/usr/bin/time -v`: peak RSS **29,692 kB** (Python runner; the product child runs beside it), elapsed 0.19 s, exit 0. Per-request product memory is bounded by the 65,000-byte response object and 1 MiB frame ceilings.

## PROVEN vs deferred

PROVEN: advertised limits; `output_limit` on oversized inline results; reader/cursor 64+1 capacity (`server_busy`); frame-layer batch bound 1..16; bounded adapter memory by design and gates; `maintenance.list` reports the `scratch`/`reservation`/`publication_temp` kinds on Linux (case-backed); `maintenance.remove` against a real abandoned-scratch attempt ID (crash scenario C: list -> remove -> durable absent, both product languages).
NOT PROVEN here: the >16-in-flight `server_busy` race (needs a pipelining client, not expressible in the declarative suite); the -32001 over-limit-frame close path (the declarative runner refuses to send >1 MiB frames, so the claim rests on `system.describe` and the product constants); `maintenance.remove` against a real reservation nonce handle (nonce names are runtime-random; the suite proves the `invalid_argument` refusal only); the `windows_housekeeping` maintenance kind (platform-bound by design; probe-only observation of `os_unsupported`/`read_only_failure` on Linux in both products, not committed as a case step). These remain step-6 benchmark/qualification territory together with latency/throughput and engine RSS ceilings.
