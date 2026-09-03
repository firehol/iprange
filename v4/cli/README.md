# iprange v4 CLI and JSON-RPC qualification suite

This directory contains the external qualification suite for the
`iprange` v1 production API and the JSON-RPC product services that
implement it.

Two product executables implement the same wire contract:

- `v4/rust/iprange-cli` — the Rust product binary (`iprange --jsonrpc`).
- `v4/go/cmd/iprange` — the pure-Go product binary (`iprange --jsonrpc`).

Both implement the same 52 callable JSON-RPC methods plus the
`iprange.v1.cancel` notification, and both answer `system.describe`
with the same capability object. The Go binary imports only the public
Go SDK; it never touches the engine internals.

## The JSON-RPC stdio service

Run either binary and drive it over stdin/stdout:

```bash
cargo build --release --manifest-path v4/rust/Cargo.toml
v4/rust/target/release/iprange --jsonrpc

go build -C v4/go -o /tmp/iprange-go ./cmd/iprange
/tmp/iprange-go --jsonrpc
```

The transport is one JSON-RPC 2.0 object per physical line (LF or
CRLF), with these limits:

- input and output frame ceiling: 1,048,576 bytes;
- response object ceiling: 65,000 bytes (`output_limit` product error);
- batches: 1..=16 requests, executed in array order;
- queued requests per connection: 16 (`server_busy`);
- ids: string or integral JSON number; only `iprange.v1.cancel` may be
  a notification without an id;
- reader handles and cursor handles: 64 each, bounded closed-handle
  tombstones (1,024 per family);
- lookup batch: 4,096 addresses; cursor page: 4,096 records.

`system.describe` reports the complete capability object, including
the limits above, the export formats, and the local
`iprange-v4-worker` availability probe (validation and recovery run in
the worker process when one is installed beside the product binary).

## The qualification suite

`run.py` drives a real executable through declarative cases and an
independent scalar-interval oracle:

```bash
RUST_IPRANGE=$PWD/v4/rust/target/release/iprange
GO_IPRANGE=/tmp/iprange-go
FIXTURE_TOOL=$PWD/v4/rust/target/release/examples/v4-fixture
nice python3 v4/cli/run.py --matrix rust --rust "$RUST_IPRANGE" \
  --fixture-tool "$FIXTURE_TOOL" --work-dir /tmp/w
nice python3 v4/cli/run.py --matrix go --go "$GO_IPRANGE" \
  --fixture-tool "$FIXTURE_TOOL" --work-dir /tmp/w
nice python3 v4/cli/run.py --matrix rust_to_go --rust "$RUST_IPRANGE" --go "$GO_IPRANGE" \
  --fixture-tool "$FIXTURE_TOOL" --work-dir /tmp/w
nice python3 v4/cli/run.py --matrix go_to_rust --rust "$RUST_IPRANGE" --go "$GO_IPRANGE" \
  --fixture-tool "$FIXTURE_TOOL" --work-dir /tmp/w
```

The mixed matrices (`rust_to_go`, `go_to_rust`) are two-binary
cross-language proofs: every rpc step declares the service role that
runs it (`actor: producer` for artifact creation/mutation,
`actor: consumer` for observation and transformation), and a mixed
case executes its producer steps on the producer binary and its
consumer steps on the consumer binary, in separate `--jsonrpc`
processes sharing only the per-case work directory. A case that
cannot exercise both actors is skipped with its reason, so a mixed
PASS means both binaries genuinely served; the JSON report records
the SHA-256 and executed-step count of each actor per case. `--allow-skips` is
required for skipped cases (fixture-only and single-actor cases in the
mixed matrices, and the whole C single-language surface, which the v4
product binaries do not implement). The `work-dir` must already exist.

Other gates:

```bash
nice python3 v4/cli/check_golden.py        # 53 golden wire exchanges
nice python3 v4/cli/sensitivity_gate.py    # 14 broken-server modes
```

- `cases/` — the declarative method-family cases (38 files). Every
  rpc step declares its service role explicitly (`actor: producer` for
  artifact creation/mutation, `actor: consumer` for observation and
  transformation), so a transformation can run on either binary in a
  mixed matrix. Producer-created cross-language cases:
  `mixed.direct-created`, `mixed.membership-created`,
  `mixed.transform-created` (the consumer binary snapshots a database
  the producer built and reads it back),
  `mixed.live-coordination` (a live reader in one binary stays pinned
  on its generation while the other binary commits, then reclamation
  is proven after close), and `workflow.publisher` (the full
  update-ipsets six-step production sequence with interleaved
  cross-binary verification and per-feed failure isolation).
  `resource.limits` proves the product-boundary ceilings (response
  object limit, reader/cursor capacity, system limits report).
- `golden/` — complete request/response exchanges generated from the
  Rust binary and validated against the strict Python schemas.
- `schema/` — the machine authority: framing, methods, results, and
  the scalar interval oracle. It imports no SDK.
- `benchmarks/` — reserved for the consolidated workload manifests
  and `bench.py` harness of SOW-0028 delivery step 6 (currently
  empty; also update the `cases/` bullet above and the matrix counts
  when a new case is added).

### Mechanical file-kind ledger and frame sizes

Every run report carries two additive evidence fields derived
mechanically from the executed steps — never a manually maintained
table:

- `file_kinds`: after each executed rpc step (success and
  expected-error) and each executed legacy step, the runner
  inventories the per-case work directory (fixture inputs excluded)
  and classifies every file it finds: v4 database main files plus
  snapshot/recovery destinations (`v4_main`); `.readers` live
  sidecars; `.iprange-reservation-*.tmp` publication reservations;
  `.iprange-scratch-*.tmp` authorized scratch; declared adapter
  outputs (csv/jsonl/netset/ipset/ranges/legacy_binary via `output`,
  `findings_output`, `report_output`, `removals_output`, and
  `export.destination`); metadata delivery files (`delivery.path`);
  anything else as `unknown`. A file that first appears by the end of
  a step is `created_by` that step's method; a file that already
  existed and whose path the step params reference is `opened_by`
  that method. Files that appear and disappear inside one step are
  transient and are not counted.
- `frame_sizes`: the JSON-RPC client measures the raw wire bytes of
  every request and response frame (LF terminator included, one
  physical line per frame; the same unit for both directions) and
  reports the per-method maximum request and response size.

### Step-5 product-interface gates

- `resource-record.md` — the delivery-step-5 resource record: the
  documented frame/response-object/batch/reader/cursor ceilings, the
  `resource.limits` case evidence, and the adapter memory evidence.
- `crash_harness.py` — process-level interruption proof at the product
  interface: drives the normal JSON-RPC client (reusing `run.py`'s
  service and fixture code — no production test hook), kills the
  producer at a durable engine marker mid-`current.publish` and
  mid-`database.initialize_live`, then proves with both consumer
  binaries in both directions that resolution is truthful, residue is
  bounded, and reopen succeeds:

```bash
RUST_IPRANGE=$PWD/v4/rust/target/release/iprange
GO_IPRANGE=/tmp/iprange-go
FIXTURE_TOOL=$PWD/v4/rust/target/release/examples/v4-fixture
nice python3 v4/cli/crash_harness.py --producer "$RUST_IPRANGE" --consumer "$GO_IPRANGE" \
  --fixture-tool "$FIXTURE_TOOL" --work-dir /tmp/w-crash --json-report /tmp/crash-report.json
nice python3 v4/cli/crash_harness.py --producer "$GO_IPRANGE" --consumer "$RUST_IPRANGE" \
  --fixture-tool "$FIXTURE_TOOL" --work-dir /tmp/w-crash --json-report /tmp/crash-report.json
```

  The first command runs the harness in both directions
  (producer=rust then producer=go) in one invocation; the report
  schema is `iprange-cli-crash-report-v1`.

## Known limitations

- The Go binary currently reports `product_version "0.0.0"` to match
  the Rust build's unreleased package version.
- Windows: the Go product builds, and the SDK worker surface refuses
  live validation/recovery opens on platforms without proven
  coordination (the same honest stub the SDK documents).
- Error `message` text is a human diagnostic; the machine contract is
  the error `code` and `outcome` members.
