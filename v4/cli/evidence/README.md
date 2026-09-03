# SOW-0028 delivery step 5 (milestone 4) — qualification evidence

Produced 2026-09-04 on the Linux workstation (x86_64) with:

- Rust product: `v4/rust/target/release/iprange` (product source unchanged
  in this milestone; same binary as the milestone-3 close-out).
- Go product: qualification build `nice go -C v4/go build -buildvcs=false -o ... ./cmd/iprange`
  (go1.26.4 linux/amd64, no embedded vcs revision; byte-stable for the
  identical v4/go source since `d956d8f2`):
  - product SHA-256 `4f8fb7b82fe4bcba7c7d039e77be1672c28c89cc110d641e3bffc76e799c86fa`
  - worker SHA-256 `16236608325cb189e0fbe05603886bbe150fd1ae83e4a8b532bfb7dd07054b1e`
- Fixture tool: `v4/rust/target/release/examples/v4-fixture`.

All commands ran under `nice`.

## Files

- `matrix-rust.json`, `matrix-go.json` — single-language matrices,
  37 case files (34 milestone-3 cases plus `resource.limits`,
  `workflow.publisher`, `mixed.live-coordination`): 37 passed,
  0 failed in both languages; oracle checks 37.
- `matrix-rust_to_go.json`, `matrix-go_to_rust.json` — two-binary
  cross-language matrices: 13 executed (both-actor cases), 24 skipped
  (single-actor cases), 0 failed, oracle checks 22, in both directions.
  Every report carries the per-actor binary SHA-256 and executed-step
  counts, the mechanical `file_kinds` ledger and the per-method
  `frame_sizes` maxima.
- `crash.json` — `iprange-cli-crash-report-v1` from
  `v4/cli/crash_harness.py` in both directions (producer=rust with
  consumer=go, producer=go with consumer=rust): 6/6 scenarios pass
  (mid-`current.publish` and mid-`database.initialize_live` SIGKILL,
  truthful resolution, bounded residue, clean reopen), zero leftover
  processes.

## Other gates at the same revision (evidence in the SOW record)

- `v4/cli/check_golden.py`: 53 golden exchanges PASS.
- `v4/cli/sensitivity_gate.py`: 14 modes PASS.
- `nice go -C v4/go test ./...` and `-tags v4work`: PASS.
- `env IPRANGE_BIN=<go product> nice ./run-tests.sh`: 100/100 PASS.
- `nice ./v4/go/check-mmap-trace.sh`: PASS (mapped, never streamed).
- `nice ./v4/rust/check-mmap-storage.sh`: PASS (343 production sources).
- `nice ./v4/rust/check-mmap-runtime.sh`: PASS (no persistent-content
  transfer syscalls).
