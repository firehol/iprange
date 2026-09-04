# SOW-0028 delivery step 5 (milestone 4) — qualification evidence

Produced 2026-09-04 on the Linux workstation (x86_64) with staged
binary copies (no personal paths in the committed evidence):

- Rust product + worker + fixture tool: copies of
  `v4/rust/target/release/{iprange,iprange-v4-worker,examples/v4-fixture}`
  staged as `/tmp/qualsvc/rust/{iprange,iprange-v4-worker,v4-fixture}`
  (Rust product SHA-256
  `c13866378040e0524711b8c92b4acdb6ca7f89b3f4db375fd5419bccd7b71eb8`,
  product source unchanged in this milestone).
- Go product + worker: the documented `-buildvcs=false` qualification
  pair (go1.26.4 linux/amd64, no embedded vcs revision) at
  product-source revision 3408c64c, staged as
  `/tmp/qualsvc/go/{iprange,iprange-v4-worker}`:
  - product SHA-256
    `1612646fdbfc54e4c9fe99378806dcc271a2f852c634d4f149d6220bf63b07b9`
  - worker SHA-256
    `16236608325cb189e0fbe05603886bbe150fd1ae83e4a8b532bfb7dd07054b1e`

The external gap reviews reopened the milestone-4 close on 2026-09-04;
this evidence set is regenerated at the closing fix-wave revision
(external framework, cases, and records only — no product source
changed).  All commands ran under `nice` with work dirs under
`/tmp/qualsvc/ev2/`.

## Files

- `matrix-rust.json`, `matrix-go.json` — single-language matrices,
  38 case files (the milestone-3 surface plus `resource.limits`,
  `workflow.publisher` with its downstream steps connected to the
  workflow-built live feed DB, `mixed.live-coordination` with two
  simultaneously pinned readers, and `recover.successful` including
  a validate-of-the-recovered-file step): 38 passed, 0 failed in
  both languages; oracle checks 37.  Each report records its matrix
  identity in the top-level `matrix` field.
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
  (A1/A2/A3 publication interruption and destination classification,
  B live-transition sidecar interruption, C authorized-scratch
  durability), zero leftover processes, `failed: 0`, per-scenario
  kind lists.  The report records producer/consumer SHA-256 values
  and the scenario pass flags.
- `crash-negative.json` — the `/bin/false` negative control: 0/10
  pass, `failed: 10`, zero leftover processes, empty scenario kind
  lists (the inventory never runs after a failed scenario launch);
  used as the harness sensitivity control, never as a kind-gate
  source.

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
both-language consumption) contract per kind, and counts only PASS
crash scenarios.  Its doctored-report self-test runs before the CLI.
