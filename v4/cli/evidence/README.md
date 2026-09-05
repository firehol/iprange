# SOW-0028 delivery step 5 (milestone 4) — qualification evidence

Produced 2026-09-05 on the Linux workstation (x86_64) with staged
binary copies (no personal paths in the committed evidence) plus the
Windows-host housekeeping proof on the authorized Windows validation
host (access authorized for SOW-0028 qualification only):

- Rust product + worker + fixture tool: copies of
  `v4/rust/target/release/{iprange,iprange-v4-worker,examples/v4-fixture}`
  staged as `/tmp/qualsvc/rust/{iprange,iprange-v4-worker,v4-fixture}`
  (Rust product SHA-256
  `389d01b90a93f8322a11590f6f46cb42509c4d2933f0c755710d9305328ee730`
  — changed from the previous revision by the sixth-wave per-member
  queue-accounting fix in `v4/rust/iprange-cli/src/rpc/session.rs`,
  worker
  `cb9ad6cd82a03b7933d706de9e1b4e4c707836962b7f00e194c5d50cd4511e94` —
  worker identities are build-proven from the staged version-matched
  pairs and are not pinned by the committed reports (which pin
  product actors and the fixture tool),
  fixture `322e8e69022aeeef01559ec3aeca95241cfd7983c2f3a39f4c0c7f2152f547e8`).
- Go product + worker: the documented `-buildvcs=false` qualification
  pair (go1.26 linux/amd64, no embedded vcs revision) staged as
  `/tmp/qualsvc/go/{iprange,iprange-v4-worker}`:
  - product SHA-256
    `8015bc3f9018648ef671aba1382083c2f06555d8146da9d99e2a5e7a9bf7a13c`
    (changed from the previous revision by the sixth-wave per-member
    queue-accounting fix in `v4/go/internal/cli/rpc/session.go`);
  - worker SHA-256
    `16236608325cb189e0fbe05603886bbe150fd1ae83e4a8b532bfb7dd07054b1e`
    (unchanged; build-proven identity, not pinned by the committed
    reports).
- Windows qualification binaries (built on the authorized Windows
  validation host at the same product-source revision, staged under
  `C:/Temp/qualsvc-win/`):
  - Go product SHA-256
    `854abf3a6df4940a2ac74f50e51d5bf2756d4d3bf568bf446b6e6f11d5547d39`;
  - Rust product SHA-256
    `927dbd47ee019fa4e279353dee2ee97504ad5ef11c75fc9cf4fdb0e469934a48`.

The external gap review of 2026-09-04 requested the D1-A crash scope,
the D2-A resource proofs (including the Windows-housekeeping kind),
the D3-B recovery wording, and the kind-gate provenance repair; this
evidence set is regenerated at that fix wave (external framework,
cases, records, and the product-source round-trip fix above).  The
whole-milestone gate review of 2026-09-05 returned FAIL and the user
approved amendment 1A for the crash interruption contract; this
evidence set is regenerated at the sixth fix wave: per-member queue
accounting in both session implementations, the hardened kind gate
with six negative controls, crash scenarios D/F per the amended
contract (successful control run + interrupted findings compared
against the reference), the oversized-frame sentinel proof, and
per-kind crash lineage records.  All
Linux commands ran under `nice` with work dirs under `/tmp/qualsvc/ev6/`;
the Windows evidence ran under the mingw64 Python of the authorized
Windows validation host.

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
  interruption, C authorized-scratch durability, D interruption
  during uncommitted live-draft construction with a recorded
  successful control run (range count and contents verified), E
  export interruption at the partial-output marker, F interruption
  during validation/findings delivery (interrupted findings output
  is a strict prefix of the successful reference; no destination
  replacement) — zero leftover processes, `failed: 0`, per-scenario
  per-kind actor lineage (`created_by`/`opened_by`).
- `crash-negative.json` — the `/bin/false` negative control: 0/16
  pass, `failed: 16`, zero leftover processes, empty per-scenario
  kind lineage; used as the harness sensitivity control, never as
  a kind-gate source.
- `resource.json` — `iprange-cli-resource-report-v1`: the four
  Linux product-interface proofs for both binaries — (a) the
  >16-in-flight `server_busy` pipelining proof (one slow export + 19
  `system.describe` frames; both binaries answer exactly 3
  `server_busy` (-32002) behind the single in-flight export, 16
  results in the 16-deep queue, the export -32010 `cancelled`, ids
  1..20 covered once, exit 0), (b) the -32001 over-limit frame
  close path (the oversized frame is followed by a valid
  `system.describe` sentinel in the same stdin stream; exactly one
  null-id -32001 response appears, the sentinel unanswered — trailing
  bytes are never parsed — stdout drains to EOF with zero further
  bytes, exit 0), (c) `maintenance.remove` against a
  real reservation nonce (kill at the reservation marker, list,
  remove with the listed row passed unchanged, durable absence),
  (d) CLI cancellation (slow export id 1, `iprange.v1.cancel` naming
  it, `system.describe` id 2 in one stdin blob: the cancelled export
  never answers with a result — explicitly-cancelled ids are
  suppressed by the session — the describe answers with a result,
  exit 0).
- `windows-housekeeping.json` — `iprange-cli-windows-housekeeping-report-v1`
  produced on the authorized Windows validation host (Microsoft
  Windows 11, AMD64): for each Windows-built product binary the
  report carries (1) the native refresh exercise — a real
  `retention.first_seen.refresh` with a `removals_output` behind a
  pinning live reader completes, publishes the exact removal log
  with the refresh value, leaves no private `.removals.tmp` residue,
  and the reader closes cleanly — and (2) the deterministic GC pair
  proof — one format-valid 8,192-byte authenticated envelope
  (`.iprange-gcauth-<attempt>-<ordinal>.tmp`, artifact kind
  `private_output`, UTF-16LE source commitment, full-block CRC-32C,
  creator-only protected DACL) plus its inert payload twin, listed
  by `maintenance.list` as exactly two clean candidate rows
  (envelope and inert payload), cross-listed by the other product
  with an equal authenticated directory identity, then removed with
  the listed envelope row passed unchanged, with durable absence
  and a zero-row after-listing.  The pair is built by
  `gc_envelope_windows.py` from the committed codec constants
  (`v4/go/internal/live/gc_codec.go`, `gc_name.go`,
  `identity_local_windows.go`, `v4/go/internal/security/
  security_windows.go`), not by any product test hook.  On
  non-Windows platforms the same script records the truthful
  `os_unsupported`/`read_only_failure` negative.

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
