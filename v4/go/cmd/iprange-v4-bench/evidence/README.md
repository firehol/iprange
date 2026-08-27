# update-ipsets benchmark evidence (SOW-0027 milestone 4)

All runs: linux/amd64, release builds, Intel i9-12900K (12th gen),
under `nice`, single harness at a time. Go binary: cmd/iprange-v4-bench
at the recorded commit; Rust binary: iprange-livedb bench harness.

- `accepted-baseline.csv` (upstream, embedded into the bench binary):
  the accepted Go p50 medians and CI disaster limits (2x p50 + 100 ms
  runner noise, 500 ms for update-ipsets-workflow).
- `baseline-samples-final.csv`: the accepted Go run (18 CI cases, 1
  warmup + 5 samples), source of accepted-baseline.csv.
- `ci-go-v4-local-20260827.log`: the final CI gate run (1 warmup + 3
  samples, enforce mode), 18/18 within-limit, ratios 0.957-1.109.
- `smoke-go-final.csv`: the final Go smoke matrix (55 cases).
- `smoke-rust-reference.csv`: the matched Rust smoke matrix
  (same fixture identity iprange-v4-update-ipsets-v1), baseline
  rust-v4-local-20260811, from the pre-milestone-4 matched pair.
  Elapsed ratio range vs the final Go run: 0.42x-7.59x.
- `scale-validation-final.csv`: the scale validation rows at the
  fix HEAD (live-validation 1M 28.8 ms, live-membership-validation
  1M 29.8 ms, immutable-validation 1M 29.7 ms).
