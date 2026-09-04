# SOW-0028 delivery step 5 (milestone 4) — qualification evidence

Produced 2026-09-04 on the Linux workstation (x86_64) with:

- Rust product: `v4/rust/target/release/iprange` (product source unchanged
  in this milestone; same binary as the milestone-3 close-out).
- Go product: qualification build `nice go -C v4/go build -buildvcs=false -o ... ./cmd/iprange`
  (go1.26.4 linux/amd64, no embedded vcs revision) at product-source
  revision 3408c64c (v4/go last changed at 3408c64c: the recover
  error-details member-set parity fix; identical v4/go source before
  that since `d956d8f2`):
  - product SHA-256 `1612646fdbfc54e4c9fe99378806dcc271a2f852c634d4f149d6220bf63b07b9`
  - worker SHA-256 `16236608325cb189e0fbe05603886bbe150fd1ae83e4a8b532bfb7dd07054b1e`
- Fixture tool: `v4/rust/target/release/examples/v4-fixture`.

The external gap review reopened the milestone-4 close on 2026-09-04;
this evidence set is regenerated at the fix-wave revision (external
framework, cases, and records only - no product source changed).

All commands ran under `nice`.

## Files

- `matrix-rust.json`, `matrix-go.json` — single-language matrices,
  38 case files (the milestone-3 surface plus `resource.limits`,
  `workflow.publisher` with its downstream steps connected to the
  workflow-built live feed DB, `mixed.live-coordination` with two
  simultaneously pinned readers, and `recover.successful`): 38
  passed, 0 failed in both languages; oracle checks 37.
- `matrix-rust_to_go.json`, `matrix-go_to_rust.json` — two-binary
  cross-language matrices: 14 executed (both-actor cases), 24
  skipped (single-actor cases), 0 failed, oracle checks 22, in both
  directions.  Every report carries the per-actor binary SHA-256 and
  executed-step counts, the mechanical `file_kinds` ledger (root
  aggregate), the per-case `file_kinds` lineage (relative path ->
  kind with `actor.method` created_by/opened_by lists) and the
  per-method `frame_sizes` maxima.
- `crash.json` — `iprange-cli-crash-report-v1` from
  `v4/cli/crash_harness.py` in both directions (producer=rust with
  consumer=go, producer=go with consumer=rust): 10/10 scenarios pass
  (A1/A2 publication interruption, A3 foreign-destination negative
  control, B live-transition interruption, C recovery interruption
  at the CRC-valid authorized-scratch marker; truthful resolution,
  bounded residue, clean reopen, zero leftover processes).  Each
  scenario records its producer/consumer binary paths and the
  artifact kinds it observed.
- `crash-negative.json` — the same harness with the consumer slot
  replaced by `/bin/false`: 10/10 scenarios fail in both directions,
  proving a broken product binary is never masked.
- The artifact-kind gate re-reads all of the above:
  `nice python3 v4/cli/check_kind_coverage.py --matrix matrix-rust.json
  --matrix matrix-go.json --matrix matrix-rust_to_go.json --matrix
  matrix-go_to_rust.json --crash crash.json` → PASS (every required
  kind - `v4_main`, `live_sidecar`, `publication_reservation`,
  `authorized_scratch`, `adapter_output`, `metadata_delivery` - has
  observed evidence).

## Other gates at the same revision (evidence in the SOW record)

- `v4/cli/check_golden.py`: 53 golden exchanges PASS.
- `v4/cli/sensitivity_gate.py`: 14 modes PASS.
- `nice go -C v4/go test ./...` and `-tags v4work`: PASS.
- `env IPRANGE_BIN=<go product> nice ./run-tests.sh`: 100/100 PASS.
- `nice ./v4/go/check-mmap-trace.sh`: PASS (mapped, never streamed).
- `nice ./v4/rust/check-mmap-storage.sh`: PASS (343 production sources).
- `nice ./v4/rust/check-mmap-runtime.sh`: PASS (no persistent-content
  transfer syscalls).
