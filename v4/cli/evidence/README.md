# SOW-0028 delivery step 5 (milestone 4) — qualification evidence

Produced 2026-09-04 on the Linux workstation (x86_64) with staged
binary copies (no personal paths in the committed evidence) plus the
Windows-host housekeeping proof on `costa-win11` (access authorized
for SOW-0028 qualification only):

- Rust product + worker + fixture tool: copies of
  `v4/rust/target/release/{iprange,iprange-v4-worker,examples/v4-fixture}`
  staged as `/tmp/qualsvc/rust/{iprange,iprange-v4-worker,v4-fixture}`
  (Rust product SHA-256
  `c13866378040e0524711b8c92b4acdb6ca7f89b3f4db375fd5419bccd7b71eb8`,
  product source unchanged in this milestone).
- Go product + worker: the documented `-buildvcs=false` qualification
  pair (go1.26.4 linux/amd64, no embedded vcs revision) staged as
  `/tmp/qualsvc/go/{iprange,iprange-v4-worker}`:
  - product SHA-256
    `cb0523cb4acc03d937e6ef97bf1b8c6aa5d1f7d9dd88f9bbb950012a9a1130ac`
    (changed from the previous revision by the SOW-0028 adapter fix in
    `v4/go/internal/cli/fileio/export_writer.go`: the export writer now
    closes its private temporary file before removing it, so
    Windows can remove the temporary — see `resource-record.md`);
  - worker SHA-256
    `16236608325cb189e0fbe05603886bbe150fd1ae83e4a8b532bfb7dd07054b1e`
    (unchanged).
- Windows qualification binaries (built on `costa-win11` at the same
  product-source revision, staged under `C:/Temp/qualsvc-win/`):
  - Go product SHA-256
    `252cd032c9ded4c9216a5680fd87099e37e7b5506eff32139c108b66f387696a`;
  - Rust product SHA-256
    `5e91d9048f210958d78d935f403cfd41ac6ad587c5b8af8c22c0ba2d352524e8`.

The external gap review of 2026-09-04 requested the D1-A crash scope,
the D2-A resource proofs (including the Windows-housekeeping kind),
the D3-B recovery wording, and the kind-gate provenance repair; this
evidence set is regenerated at that fix wave (external framework,
cases, and records only — the single Go adapter fix above is the only
product-source change).  All Linux commands ran under `nice` with work
dirs under `/tmp/qualsvc/ev3/`.

## Files

- `matrix-rust.json`, `matrix-go.json` — single-language matrices,
  38 case files: 38 passed, 0 failed in both languages; oracle
  checks 37.  Every PASS case entry carries the per-actor SHA-256,
  the product-declared `implementation` (rust|go from
  `system.describe`) and the executed-step count.
- `matrix-rust_to_go.json`, `matrix-go_to_rust.json` — two-binary
  cross-language matrices: 14 executed (both-actor cases), 24
  skipped (single-actor cases), 0 failed, oracle checks 22, in both
  directions.  The same per-actor identity is recorded for every
  PASS case; `check_kind_coverage.py` derives language attribution
  exclusively from those executed identities (the top-level `matrix`
  label is only cross-checked, never trusted).
- `crash.json` — `iprange-cli-crash-report-v1` in both directions
  (producer=rust with consumer=go, producer=go with consumer=rust):
  16/16 scenarios pass — A1/A2/A3 publication interruption and
  destination classification, B live-transition sidecar
  interruption, C authorized-scratch durability, D commit/finish
  interruption at the live replace resize marker, E export
  interruption at the partial-output marker, F validate
  interruption at the findings-output marker — zero leftover
  processes, `failed: 0`, per-scenario kind lists.
- `crash-negative.json` — the `/bin/false` negative control: 0/16
  pass, `failed: 16`, zero leftover processes, empty scenario kind
  lists; used as the harness sensitivity control, never as a
  kind-gate source.
- `resource.json` — `iprange-cli-resource-report-v1`: the three
  Linux product-interface proofs for both binaries — (a) the
  >16-in-flight `server_busy` pipelining proof (Rust: exactly 3
  pipelined describes answer -32002 behind one slow export; Go:
  serialized execution, all 20 answer, recorded truthfully), (b) the
  -32001 over-limit frame close path (one null-id -32001 response
  then EOF, exit 0), (c) `maintenance.remove` against a real
  reservation nonce (kill at the reservation marker, list, remove,
  durable absence).
- `windows-housekeeping.json` — `iprange-cli-windows-housekeeping-report-v1`
  produced on `costa-win11` (Microsoft Windows 11, AMD64): both
  Windows-built product binaries answer `maintenance.list` kind
  `windows_housekeeping` with 0 entries on an empty directory and
  exactly 1 listed GC-envelope candidate (kind,
  `candidate_kind: envelope`, UTF-16LE basename encoding, exact
  basename, authenticated directory identity) on the candidate
  directory.  On non-Windows platforms the same script records the
  truthful `os_unsupported`/`read_only_failure` negative.

## Gate invocation

```bash
nice python3 v4/cli/check_kind_coverage.py \
  --matrix v4/cli/evidence/matrix-rust.json \
  --matrix v4/cli/evidence/matrix-go.json \
  --matrix v4/cli/evidence/matrix-rust_to_go.json \
  --matrix v4/cli/evidence/matrix-go_to_rust.json \
  --crash v4/cli/evidence/crash.json
```

The gate requires all four matrix reports and a positive crash report,
rejects failed/leftover reports and unknown kinds, enforces the
both-language creation (and, where any service opens the kind,
both-language consumption) contract per kind from the executed-actor
identities, and counts only PASS crash scenarios.  Its doctored-report
self-test runs before the CLI and covers the clone-and-relabel attack
(a `rust` report relabeled `go` fails), missing per-case actors, and
implementations outside rust/go.
