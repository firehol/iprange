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
- queued requests per connection: 16 (`transport_server_busy`);
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
nice python3 v4/cli/run.py --matrix rust --rust v4/rust/target/release/iprange \
  --fixture-tool v4/rust/target/release/examples/v4-fixture --work-dir /tmp/w
nice python3 v4/cli/run.py --matrix go --go /tmp/iprange-go \
  --fixture-tool v4/rust/target/release/examples/v4-fixture --work-dir /tmp/w
nice python3 v4/cli/run.py --matrix rust_to_go --rust ... --go ... \
  --fixture-tool ... --work-dir /tmp/w
nice python3 v4/cli/run.py --matrix go_to_rust --rust ... --go ... \
  --fixture-tool ... --work-dir /tmp/w
```

`--allow-skips` is required for the C single-language surface that the
v4 product binaries do not implement. The `work-dir` must already
exist.

Other gates:

```bash
nice python3 v4/cli/check_golden.py        # 53 golden wire exchanges
nice python3 v4/cli/sensitivity_gate.py    # 14 broken-server modes
```

- `cases/` — the declarative method-family cases (31 files).
- `golden/` — complete request/response exchanges generated from the
  Rust binary and validated against the strict Python schemas.
- `schema/` — the machine authority: framing, methods, results, and
  the scalar interval oracle. It imports no SDK.
- `benchmarks/` — workload benchmarks for `bench.py` (SOW-0028
  step 5).

## Known limitations

- The Go binary currently reports `product_version "0.0.0"` to match
  the Rust build's unreleased package version.
- Windows: the Go product builds, and the SDK worker surface refuses
  live validation/recovery opens on platforms without proven
  coordination (the same honest stub the SDK documents).
- Error `message` text is a human diagnostic; the machine contract is
  the error `code` and `outcome` members.
