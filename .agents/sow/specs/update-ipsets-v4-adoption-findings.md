# Findings: update-ipsets workflows ↔ v4 scope-aware engine

Investigation 2026-06-23 — four parallel read-only agents over
`github.com/firehol/update-ipsets` (525 Go files / 124k LoC). Companion to
`design-iprange-v4-scope-api.md`; input to the eventual implementation SOW. All
`file:line` refer to the update-ipsets repo unless noted.

## Scale (measured/documented)

- ~423 sources + ~13 merges ≈ **436 feeds**; ~342 source YAMLs (history/window variants
  expand at runtime); 5 geo + 4 ASN + 6 bogon + 21 critical providers.
- ~436 feeds × ~10 published artifacts ≈ **3,500–4,400 output files** + per-feed `latest`
  binary + retention cohort dirs.
- Full run ≈ **206 s**, incremental 13–46 s, peak RSS ~1.5 GB, **single-threaded today**
  (`max_ingest_workers: 1`).
- Geo dimensions today: **country (ISO, ~250)** and **ASN (~70k routed; exact count
  data-dependent)**. **No city dimension exists** (`pkg/engine/format_handlers.go:80-97`).

## Convergent findings (all four agents)

1. **update-ipsets is a per-feed-FILE engine.** Each feed → own `latest` binary set,
   `new/<ts>.set` cohort dir, append CSVs, ~10 JSON/CSV artifacts. No global cross-feed
   index.
2. **The dominant cost is the O(N²) pairwise comparison** — `436·435/2 ≈ 95k` pairs
   (`pkg/engine/output_comparison.go:198`), with **>1,000 lines of machinery built solely
   to make separate-file N×N tractable**: disjointness prefilter
   (`pkg/iprange/range_overlap_filter.go`, ~440 LoC), content-hash ledger
   (`output_comparison_pair_ledger.go`, ~521 LoC), one-to-many heap (`compare_pairs.go`).
   **The data model, not the algorithm, is the bottleneck.**
3. **v4 reframes set algebra**: union/intersect/exclude/compare stop being *materializing
   ops that emit files* and become **scope-predicate over one `scan`**. The N×N matrix →
   one `O(records · popcount)` scan reading per-record membership; the prefilter+ledger
   apparatus retires. Coalesce/`Optimize` becomes free (maintained per `set`).
4. **Retention is the heaviest per-file I/O**: every run re-`ReadDir`s `lib/<feed>/new/`
   and opens+intersects **every cohort file** (`retention_update.go:270-531`); history
   `_30d`-style variants re-union N daily snapshots (`feed_body_stage.go:400-452`).
   Exactly v4's `O(log n)` per-IP delete target — and the **biggest, most self-contained**
   simplification.
5. **ASN is already an interval map** (`pkg/asnloc/backend_rangetable.go`: sorted,
   same-ASN-merged ranges + binary search) — v4 categorical generalizes an existing
   pattern. The clearest single deletion target is the nested country×ASN cross-walk
   (`pkg/engine/home_entity_precompute.go:234-297`).

## What v4 does NOT touch (honest scope boundary)

- **Ingest** (download/HTTP-cache/parse/DNS/`.new`→`.processing`) — upstream-bound,
  dominates wall-clock, unchanged.
- **Text `.ipset`/`.netset` outputs** — git-committed, consumer-facing hard contract
  (`finalize.go:54-68`); v4 supplies ranges, the CIDR `Reduce`/print layer
  (`pkg/iprange/print.go:131`) stays.
- **~3,500 published JSON/CSV artifacts** — must still be materialized; v4 speeds
  *computing* the numbers, not serving.
- **Append-only `history.csv`/`changesets.csv`** — already one row/run, leave as-is.
- **`max_ingest_workers: 1`** is the current clamp — raising it is the cheapest near-term
  win and is partly orthogonal to v4; don't measure v4 against the clamped baseline.

## P1–P4 resolved (recommendations, pending user confirmation)

- **P1 — feed-set encoding.** A raw 436-bit membership bitmap = **55 bytes/record → ~71-B
  IPv4 records vs 8 B today (~9× inflation)**. Recommend **interned set-id + canonical
  dictionary** (compact 8-B records, dictionary maps id→membership; canonical interning so
  coalescing stays id-equality). Preserves the comparison win (deref id→bitmap, popcount)
  without the inflation.
- **P2 — `rangeMove`.** **No current use case** — geo is rebuilt from providers, not
  incrementally reassigned. Recommend **defer** (don't build speculative API); keep
  `range-replace` for categorical.
- **P3 — file granularity.** One global tuple file is blocked by **multi-provider
  comparison** (4 ASN + N country providers shown side-by-side; one value/range = one
  provider) and **overlapping country attribution** across providers. Recommend
  **per-(dimension, provider) categorical files** (each a clean categorical interval map);
  feeds are a **separate** set-membership file. Cross-file joins (country×ASN) are rare /
  precompute-able.
- **P4 — counters at scale.** ~70k ASNs × 16 B ≈ 1.1 MB; ~250 countries trivial; ~436
  feeds trivial. **Maintained per-scope counters are affordable** at current scale; keep
  on-demand fallback only if a dimension exceeds ~1M distinct values (e.g. future cities).

## New decisions the investigation forced

- **Architecture — live DB vs derived index.** The shared-file write-amplification /
  single-failure-domain / phase-recoupling risk (flagged by 3 agents) is real. Recommended
  resolution: the multi-scope v4 file is the **live mutable DB** (so incremental retention
  is `O(log n)`), while **per-feed files are retained** as ingest staging + text-output
  rendering + **rebuild source** (disaster recovery). Write-amplification is the residual
  risk → **measure** (SOW-0005) before committing.
- **Retention timestamp model.** The biggest win requires a **per-interval first-seen
  timestamp** dimension in v4 to replace cohort files; the "hours-alive" histogram +
  incomplete-flag semantics (`retention.go:122`, `retention_update.go:533`) are subtle and
  must be preserved. This is the next design sub-problem.
- **Phased adoption (surgical → long-term):** (1) **retention** (most per-file I/O,
  contained blast radius) → (2) **pairwise comparison** → (3) **query/compose read path**
  → (4) **merge/history composition** → (5) **geo as categorical files**. Items 4–5 are
  larger/riskier; defer until the core proves parity.

## Risks to carry into the SOW

- Retention "hours-alive cohort" exactness + incomplete-flag parity.
- Per-IP/first-seen timestamp storage cost.
- Crash-safety/atomicity must match current temp-write+rename guarantees
  (`binary_write.go`, `writeFileAtomic`).
- Single shared file = contention/locking point the per-feed model avoids.
- **IPv6 parity**: current `pkg/iprange` has full dual-stack (`fileset6*`,
  `range_source6_indexed*`, `iter6_ops*`); v4 must match. Catalog/comparison pipeline is
  IPv4-only today — v4 dual-stack is net-new surface.
- Record-size inflation / scan cost vs scope encoding (P1).

## Key files for the implementation SOW

`pkg/engine/`: `retention_update.go`, `retention.go`, `output_comparison.go`,
`output_comparison_pair_ledger.go`, `query.go`, `feed_body_stage.go`, `merge_inputs.go`,
`run_pipeline.go`, `finalize.go`, `latest_set_cache.go`, `unique_share.go`,
`home_entity_precompute.go`, `geo_provider_cache.go`, `asn.go`, `geoloc.go`.
`pkg/iprange/`: `range_overlap_filter.go`, `compare_pairs.go`, `set_ops.go`, `iter_ops.go`,
`fileset*.go`, `binary.go`. `pkg/asnloc/`, `pkg/geoloc/`.
