# iprange — Multi-Language Engine, Binary Format & Threat-Intel SDK

**Status:** Design — decisions captured, pre-implementation
**Last updated:** 2026-06-21
**Scope:** The future of `iprange` as a multi-language engine + a portable binary
threat-intel format + ready-to-use SDKs, with `update-ipsets` and Netdata as the
first consumers.

> This is a living design document. Decisions are marked **DECIDED** or **OPEN**.
> Nothing here is implemented yet. No code has been changed.

---

## 0. Architecture update (2026-06-21) — SUPERSEDES affected sections below

After grounding in the C oracle + prior art, the language architecture changed.
Where this section conflicts with §2/§7/§8/§11 below, **this section wins**; those
sections are pending a full refresh. Authoritative decision record:
`.agents/sow/pending/SOW-0001-20260621-iprange-engine-and-binary-format.md`.

- **Two full native libraries — Rust and Go — with 100% feature parity.** Both
  implement the complete engine (format + set algebra + lookup). Rust also produces
  a **C library** (`cdylib`/`staticlib` + cbindgen) so C consumers use Rust. The Go
  library is **pure Go, no cgo** (hard constraint). **Native C is dropped**; legacy
  C survives only as the **behavioral oracle** for the conformance corpus, then
  retires. Rust is the reference; Go must match it (and the oracle).
- **Consumers:** `netflow.plugin` (Rust) → Rust library; `network-viewer.plugin`
  (C) → Rust's C library; `go.d.plugin` (Go) + `update-ipsets` (Go) → the Go
  library. Go consumers need the **full** operations, not just lookups — hence a
  full Go library.
- **Parity risk (real):** Go (garbage collector, less low-level control) may
  struggle to hit the 5–10% band against Rust on the heaviest set algebra; the
  shared test corpus + benchmark harness surfaces this per operation.
- **v4/v6 are unified** via generics over an integer width (`u32`/`u128`) in both
  languages — written once per library, not duplicated.
- **Format:** little-endian; IP keys are integer pairs compared numerically (v6 =
  two `u64` **hi-then-lo**, so a v6 key is **NOT** castable to a native `u128`; v4
  `u32[]` may be cast only on an endianness-matching, suitably-aligned host —
  otherwise parse field-by-field); typed-section **directory** `{kind, offset:u64,
  length, sha256}`; **Ed25519** signature over header+directory; **bounds-check
  structure → verify signature → read** ("signed ≠ safe"). Lookup = sorted-range
  binary search baseline (optional DIR-24-8 v4 / Poptrie v6 accel are additive —
  deferred). Authoritative byte-level contract: `binary-format-v3.md`.
- **Build order = three steps:** (1) format library, (2) processing engine,
  (3) backwards-compatible CLI. Step 1 = `SOW-0002`.

The interval-map core (§3), format layout (§4), metadata tiers (§5), and multi-feed
design (§6) below remain valid as written.

---

## 1. Purpose (fit-for-purpose)

Add **threat intelligence to Netdata**. Netdata's NetFlow, L2/L3/L7 topologies,
and network-viewer (service map) must **annotate IPs with threat-intel data in
real time** — for every flow/connection/edge, answer: *"which threat-intel feeds
and categories does this IP belong to?"*

To serve this, `update-ipsets` becomes a **public, open-source, professional,
high-performance threat-intel source** that publishes feeds in a portable binary
format, with **ready-to-use SDKs** for custom applications. Netdata is the **first
consumer**, not the only one.

The SDKs need a **very fast `iprange`** at their core for real-time IP
comparisons (membership lookups).

### Two halves with opposite characteristics

| | **Producer** (update-ipsets) | **Consumer** (SDK in Netdata C/Go/Rust) |
|---|---|---|
| Job | fetch, parse, dedup, merge, compute metrics, build + sign artifacts | **lookup**: "which feeds/categories is this IP in?" |
| Pattern | batch, periodic | real-time, per-flow/per-packet |
| Bottleneck | throughput | **latency** (millions of lookups/sec/core) |
| Allocation | may allocate | **zero/minimal allocation** (lookups produce no output) |
| Set algebra | full (union/intersect/exclude) | none — membership / range-containment only |

The consumer hot path is read-only longest-prefix-match — a small, well-defined
kernel. This is what makes zero-copy / mmap / zero-alloc natural there.

---

## 2. Architecture (DECIDED)

### 2.1 Three full native libraries — C, Rust, Go

Full `iprange` libraries in **C, Rust, Go** — not thin readers. They map exactly
onto Netdata's stack (C core, Go topology, Rust netflow) and serve arbitrary
third-party SDK consumers who want to *process* feeds, not just look them up.

**No FFI on the hot path.** Three native implementations mean the Go consumer is
**pure Go (no cgo)** — required for Netdata's static, cross-platform, IoT-grade
distribution. cgo per-call overhead (tens of ns) would dominate a single-digit-ns
lookup and break cross-compilation.

### 2.2 Three CLI binaries — as a shared conformance + benchmark harness

The three CLIs exist **not to be published**, but as the vehicle for **one**
black-box conformance corpus and **one** benchmark harness that drive all three
implementations (the model used by the protobuf conformance suite and the
WebAssembly spec tests). Tests and benchmarks are written **once**, not 3×.
Per-language unit tests cannot prove cross-language equivalence; a shared
black-box corpus can.

### 2.3 Decision table

| # | Decision | Choice | Status |
|---|----------|--------|:------:|
| 1 | Library scope | Full native libraries in C, Rust, Go | **DECIDED** |
| 2 | Three CLIs | Shared conformance + benchmark harness (not products) | **DECIDED** |
| 3 | Performance parity tolerance | **5–10%** delta between implementations; beyond requires strong written justification | **DECIDED** |
| 4 | Benchmark metrics | wall-time **+ allocation count + RSS**, exposed via CLI benchmark modes | **DECIDED** |
| 5 | Format as conformance check | All three read/write **byte-identical** binary files | **DECIDED** |
| 6 | Behavioral oracle | **C** (incumbent defines correctness); freeze format + corpus against it; Rust then Go chase it; C/Rust set the perf ceiling | **DECIDED** |
| 7 | Dossier storage | **Single self-contained, signed, sectioned file** per feed (computed metrics recompute every update → same cadence as index) | **DECIDED** |
| 8 | Multi-feed model (phase 2) | **Merged index + catalog + per-feed dossiers** (one lookup = all memberships), not a concatenated bundle | **DECIDED** |
| 9 | IPv6 | **Dual-stack from day one** — key type generalizes to 128-bit, not just the value | **DECIDED** |
| 10 | Public artifact licensing | **Include** `dont_redistribute` feeds, **marked** via the header license/redistribute flag (handling deferred; may become a no-op later) | **DECIDED** |
| 11 | Delivery to Netdata | **Indirect**: iprange → replaces update-ipsets `pkg/iprange` → update-ipsets SDK (Rust/Go) embeds iprange → Netdata consumes the SDK | **DECIDED** |

### 2.4 Engine unification & consumption layering

`update-ipsets` already depends on an `iprange` engine for all set operations,
binary persistence, and merge-joins. Therefore the engine we design **is**
update-ipsets' engine. There are **two SDK layers**, and Netdata consumes the
upper one — it does **not** depend on `iprange` directly:

```
   ┌──────────────────────── iprange (C / Rust / Go) ────────────────────────┐
   │  interval-map engine + portable binary format (reader/writer, lookups)   │
   │  generic, domain-agnostic                                                 │
   └──────────────────────────────────────────────────────────────────────────┘
                 ▲                                   ▲
                 │ replaces pkg/iprange              │ embedded in
                 │                                   │
   ┌─────────────┴───────────┐        ┌─────────────┴────────────────────────┐
   │  update-ipsets (Go)      │        │  update-ipsets SDK (Rust / Go)        │
   │  the producer/service    │ feeds  │  threat-intel client (fetch/sync      │
   │  builds + signs artifacts│ ─────► │  artifacts, dossier schema, feed-id   │
   │                          │artifacts│  registry) on top of iprange         │
   └──────────────────────────┘        └─────────────┬─────────────────────────┘
                                                      │ consumed by
                                          ┌───────────┴───────────┐
                                          │  Netdata               │
                                          │  netflow (Rust),       │
                                          │  topology/viewer (Go)  │
                                          └────────────────────────┘
```

Sequence (DECIDED — Decision 11):

1. **Finish `iprange`** (C/Rust/Go engine + portable binary format).
2. **Replace update-ipsets' own `pkg/iprange`** with it (update-ipsets adopts the
   new engine + format). *(The current `pkg/iprange` is being rewritten separately
   and was intentionally **not examined** for this design to avoid bias.)*
3. **Build the update-ipsets SDK** (Rust + Go) — the threat-intel layer
   (artifact fetch/sync, dossier schema, feed-id registry) embedding `iprange`.
4. **Netdata consumes the update-ipsets SDK** (Rust for netflow, Go for
   topology/network-viewer) → it gets `iprange` **indirectly**.

So `iprange` stays the generic engine (Rule B); the **update-ipsets SDK owns the
threat-intel schema and the artifact-fetch client**. C `iprange` remains a
first-class library and the behavioral **oracle** (Decision 6) even though the
Netdata consumption path is Rust/Go.

This is not "add an engine" — it is "finish and generalize the engine that
update-ipsets is already built on, then wrap it in a threat-intel SDK."

---

## 3. The interval-map core primitive (DECIDED)

The central data structure generalizes `iprange`'s from-to model by attaching an
**interned attribute value** to each range, splitting a range wherever the value
changes.

```
IntervalMap<V> =
    sorted disjoint intervals { start, end, value_id }   // the index
  + interned table of distinct V                          // dedup of values
```

- A new boundary is introduced wherever the attribute changes; each elementary
  interval has one constant value.
- **V is opaque to the core** — an interned blob with equality (dedup by hash).
  The consumer assigns meaning. This keeps the core domain-agnostic.
- Value types in practice: `feed-id set` (multi-feed membership), `ASN`,
  `country`, `first_seen timestamp`, severity, count, …

### Operations

| Operation | Description |
|---|---|
| point lookup | `ip → V` via binary search (or trie) over the index |
| merge-join | walk two sorted maps once, apply a **combine/reduce** function on overlaps (e.g. set-union, `min(timestamp)`) → new map |
| aggregate | fold over `(range, V)` → per-value counts (geo/ASN composition, retention histograms) |
| build | sweep-line over `+1/−1` boundary events; emit an interval when the active value changes; intern the value |

Today's set operations (union/intersect/exclude) become the special case
`V = present/absent`.

### Worked example (membership)

Three feeds → disjoint elementary intervals, each with a constant membership set:

| start | end | members | set-id |
|------|------|---------|:------:|
| 10.0.0.0 | 10.0.0.49 | {A} | 1 |
| 10.0.0.50 | 10.0.0.100 | {A, B} | 2 |
| 10.0.0.101 | 10.0.0.150 | {B} | 3 |
| 10.0.0.200 | 10.0.0.255 | {C} | 4 |

Lookup `10.0.0.70` → interval `{50..100, set_id=2}` → `set_table[2] = {A,B}` →
**one probe returns all memberships.**

### Representation choices (OPEN — recommendation noted)

- **Interval array (recommended):** `{start, end, value_id}` — closest to the
  existing from-to model; gaps cost nothing; 12 B/entry IPv4 (wider for IPv6).
- Breakpoint array: `{start, value_id}`, end implied by next start − 1; ~33%
  smaller but needs a sentinel and tiles the whole space.

### Value (set) encoding (OPEN — recommendation noted)

- **Interned sorted feed-id lists (recommended):** `{count, id₀, id₁, …}` —
  compact for sparse membership (the common case: an IP is in 1–3 feeds).
- Bitset (`⌈N_feeds/8⌉` bytes): fixed-width, fast for set algebra, wasteful when
  membership is sparse.

### Scale note (estimate)

Merging all ~430 feeds may yield millions of elementary intervals → tens of MB
for the index. That is precisely why it is **mmap'd**: the consumer faults in only
the pages its binary searches touch. Distinct membership sets stay modest
(bounded by combinations that actually occur, not 2^N) and are interned.

---

## 4. Binary file format — phase 1 (single feed) (DECIDED)

A single self-contained, **streamed**, **signed**, **sectioned** file per feed.

### 4.1 Layout

```
┌──────────────────────────────────────────────┐
│ FRONT HEADER (fixed, front)                    │  magic, format version, flags,
│   identity + ALL operational data              │  feed-id, ip-version, counts,
│                                                │  generation ts, source version,
│                                                │  category, license/redistribute,
│                                                │  health, cadence summary,
│                                                │  → directory offset/len
├──────────────────────────────────────────────┤
│ DIRECTORY (front)                              │  per section: {type-id, offset,
│   + SIGNATURE (covers header+directory)        │  length, SECTION-HASH}; key-id; algo-id
├──────────────────────────────────────────────┤
│ INDEX SECTION   ◄── HOT PATH (mmap, zero-copy) │  the interval-map index
├──────────────────────────────────────────────┤
│ METRIC SECTIONS (Tier 3a, per-update, struct.) │  asn[], geo[], overlap[], retention[],
│                                                │  behavior[], crit_infra[], bogons[] …
├──────────────────────────────────────────────┤
│ DESCRIPTIVE SECTIONS (Tier 3b, rare)           │  maintainer, about, policies, sources[]
├──────────────────────────────────────────────┤
│ STRING TABLE                                   │  ASN names, prose — referenced by offset
└──────────────────────────────────────────────┘
```

### 4.2 Streaming producer + front-loaded metadata (DECIDED)

These do not conflict:

- The producer **streams the body forward** (index → metrics → prose), hashing
  each section incrementally. Bounded memory — never holds a whole feed (e.g.
  600M-IP firehol_level1) in RAM.
- Then **one seek back** to write the fixed-size front header + directory +
  signature ("backpatch"). The output is a regular seekable file.

Result: bounded producer memory **and** front-loaded operational data + selective
verification for consumers.

### 4.3 Integrity: signed directory + per-section hashes (DECIDED)

- The directory carries a **hash per section**.
- The **signature covers only header + directory** (small, instant to verify).
- Because the directory lists each section's hash, signing it **transitively
  authenticates the whole file**, yet a consumer can:
  1. read front header + directory (a few pages),
  2. verify the directory signature (cheap),
  3. mmap **only the index**, hash that one section, compare to its directory entry,
  4. never fault metric/prose pages unless needed.
- **Crypto agility:** `algorithm-id` + `key-id` in the header. Reserve header room
  for a Merkle root if finer-grained lazy verification is ever required (not now —
  per-section granularity matches the access pattern; no over-engineering).
- A consumer can render a full operational summary (name, category, counts,
  health, license, generation time) from the **header alone, zero body I/O**.

### 4.4 Extensibility (DECIDED)

- **Typed section registry + skip-unknown:** each section has a `type-id`;
  consumers skip types they don't know → forward/backward compatibility.
- **Core stays domain-agnostic (Rule B):** core `iprange` understands exactly one
  section type (the index) and treats all others as **opaque typed blobs it
  carries and signs but never interprets**. `update-ipsets` owns the threat-intel
  metric/prose schema.

---

## 5. Metadata tiers (DECIDED)

`update-ipsets` produces a rich per-feed research dossier (identity, insights,
prose, ASN/geo/overlap/critical-infra/bogon tables, behavior/retention curves,
sources). It is stored as **structured fields, not markdown** (markdown is a
*rendering*; structured records are queryable and can be made byte-identical
across implementations — which serves the conformance harness).

| Tier | Content | Size | Cadence | Where |
|---|---|---|---|---|
| **0 — Identity** | name, category, maintainer, counts, health, generation ts, source version, **license/redistribute**, integrity+signature | tiny | per update | header |
| **1 — Index** | the interval-map ranges | bulk | per update | hot-path section (mmap) |
| **2 — Summary stats** | cadence, churn/rotation medians, IPs/entries range, tracked-since | small | per update | header / section |
| **3a — Computed metrics** | ASN, geo, overlap matrix, critical-infra, bogons, behavior, retention, age, churn | large | **per update** | metric sections |
| **3b — Descriptive** | maintainer, about, how-built, detection method, listing/removal policy, sources | small | rare | descriptive sections |

> Tier 3a recomputes **every update** (same cadence as the index) — which is why a
> single self-contained file (Decision 7) is correct: splitting it out would save
> no bandwidth, since it re-ships every update regardless. A consumer that wants
> only the index can still issue an **HTTP range request** for the index byte
> range (the directory gives offsets), so single-file does not penalize
> index-only consumers.

The hot-path lookup returns the **annotation record** per matching feed: Tier-0
identity (`feed-id, name, category, severity/health`). The dossier (Tier 3) is
fetched on demand (local section or remote API/MCP).

---

## 6. Multi-feed format — phase 2 (DECIDED: merged index)

The reason multi-feed exists is **one lookup returning all memberships** for
real-time annotation. A concatenated bundle of independent feeds would **not**
deliver this (it stays N searches). The merged index does:

```
FILE HEADER                                          ◄── front (streamed + backpatched)
FEED CATALOG  {feed-id → name, category, → dossier offset}
MERGED INDEX  ◄── HOT PATH: elementary ranges → membership-set id   (ONE lookup)
MEMBERSHIP-SET TABLE  (interned distinct feed-id sets)
PER-FEED DOSSIERS  (metric + descriptive sections — same structure as single-feed, minus the index)
SIGNATURE (over header + catalog + per-section hashes)
```

- One search → membership-set id → feed-ids → join catalog for names/categories.
- Per-feed dossiers reuse the single-feed metadata structure (the "same
  structure" elegance survives for dossiers; only the index is merged, stored once).
- A single feed's IP list is reconstructable (filter elementary ranges where its
  bit is set, coalesce) — no redundant per-feed indexes.
- **Requires a stable global feed-id registry** (feed name → fixed numeric id),
  owned by `update-ipsets`, so membership sets stay consistent across files and
  versions.

Direction: one merged index is far cheaper than N separate per-feed searches, so
the merged approach is right. **CORRECTION (design review):** the earlier absolute
figures here were wrong. A plain binary search over a merged index of *millions to
tens of millions* of ranges is **~20+ cache misses (µs-scale when cold)**, not
"1–2 / ~100–200 ns", and the merged file may be **~1–2 GB**, not "tens of MB". So
a lookup **accelerator** (e.g. a direct-indexed first stage) is likely **required**
for per-flow real-time speed, not optional. **Measure at full (~430-feed) scale
before committing.** *(Estimates — must be measured.)*

A concatenated **bundle** may still be kept as a separate *distribution* artifact
(download many named feeds at once), but it is not what the annotator queries.

---

## 7. IPv6 / dual-stack (DECIDED)

The current IPv4-only model is a structural hole for the purpose: NetFlow
annotation is IPv6-heavy. The engine must be **dual-stack from day one** — the
**key type** generalizes to 128-bit (not just the value type). This is the single
largest design + effort item the format forces. *(Note: this repo's C `iprange`
already has some IPv6 handling — see `wiki/ipv6.md` — to be reconciled with the
new model.)*

---

## 8. Performance baselines (DECIDED philosophy)

"Optimal / nothing faster" is reframed as **defined, benchmarked baselines**,
regression-gated in CI, measured per operation and split into:

- **CPU-bound ops** (in-memory set algebra, merged-index build): C/Rust set the
  ceiling; Go within the 5–10% tolerance (Decision 3) or justified.
- **I/O-bound ops** (parse, load): language differences wash out.
- **Lookup hot path:** memory-latency-bound → all three can reach parity (the
  cache miss dominates Go's bounds-check overhead).

Consumer hot path = **zero/minimal allocation** (no output to allocate). Producer
may allocate (batch). Harness reports allocations + RSS (Decision 4).

---

## 9. `update-ipsets` fit — evidence & adoption roadmap

Investigation of `update-ipsets` (excluding the off-limits Go `pkg/iprange`)
confirms the format is a **generalization of what it already does**, not a graft.

### 9.1 Already ~80% there (for IPv4)

- Core set = **sorted disjoint `{Lo,Hi uint32}` ranges**, coalesced — not a hash
  set. (`pkg/engine/bogons_rfc.go:144`, `geo_provider_cache.go:245`)
- **Binary mmap range files already exist** (`WriteBinary`/`FileSet`) for
  `latest`, history snapshots, retention cohorts.
  (`pkg/engine/binary_write.go:11-52`, `finalize.go:44`, `fileset_helpers.go:43-89`)
- **Geo is already this exact interval map**: sweep-line over `+1/−1` events →
  disjoint segments carrying interned membership sets (`codes []uint16`),
  coalesced on change, merge-joined to feeds.
  (`pkg/engine/geo_provider_cache.go:141-259`, `:17-20`, `:187-191`, `:248-254`,
  `:269-277`) ASN is a sorted range table with skip-the-span lookup
  (`pkg/asnloc/backend_rangetable.go:11-16`, `pkg/asnloc/asnloc.go:268-292`).
- Set ops are already streaming merge-joins
  (`iprange.UnionSourcesContext`/`IntersectSourcesContext`/`Exclude`/
  `OverlapCountIterContext`).

### 9.2 The gap

The whole range/binary pipeline is **IPv4-`uint32`-only**; IPv6 is carried as
opaque text outside the model (`pkg/processor/primitives.go:366`,
`stream_filters.go:87`). The new format must close this.

### 9.3 Where the format/engine actually helps (measured)

Measured on the live server (`iplists`, **423 sources + 13 merges = 436 ipsets**),
average **full run ≈ 206 s**:

| Phase | Per run | % | Interval-map impact |
|---|---:|---:|---|
| **metadata** | 63.0s | 30.6% | **Partly** — O(N²) overlap matrix (~89K pairs) ✅ + reflection JSON ❌ |
| **publish** | 56.8s | 27.6% | **No** — pure I/O (byte-compare, fsync, atomic rename) |
| **sources** | 42.3s | 20.5% | **Partly** — retention/cohort reconcile ✅ + text parse ❌ |
| **asn** | 24.9s | 12.1% | Partly (MMDB + overlaps) |
| bogons | 5.4s | 2.6% | ✅ pure set-ops |
| entities | 4.8s | 2.3% | mostly no |
| critical_infra | 3.5s | 1.7% | partly |
| insights | 2.7s | 1.3% | no |
| **geoip** | 2.5s | 1.2% | **No — already an interval-map** |

- Incremental runs (1–6 changed feeds): **13–46 s**. Peak RSS **1.5 GB**.
  Startup full-rebuild is **CPU-limited** (~1 core; ~12 MB/s disk = not disk-bound).
- **`max_ingest_workers: 1` is active** (`configs .../runtime.yaml:78`,
  `pkg/engine/runtime.go:253-262`) → pipeline effectively single-threaded.
  **Raising it is the cheapest win and is orthogonal to the format.**
- Honest ceiling: the format can attack ~**40–55%** of a full run (overlap matrix
  + retention + bogon/ASN overlaps). It **cannot** touch `publish` (I/O, 27.6%),
  JSON serialization, text parsing, or downloads.

### 9.4 Standout win: age/retention

Today age is a **fan-out of per-cohort binary snapshots** (`new/<unix>.set`),
reconciled with **O(#cohorts) compare+intersect+rewrite on every tick with
removals** (`pkg/engine/retention_update.go:257-459`), and `_1d/_7d/_30d` are
**fully duplicated retention pipelines** (4× machinery) fed by a parallel snapshot
store (`pkg/config/expand.go:170-235`, `pkg/engine/feed_body_stage.go:400-446`).

An `IntervalMap<first_seen>` collapses reconciliation to **one merge-join per
update**, and windows become a **filter predicate** (`first_seen ≥ now−window`).
The unbounded `new/*.set` corpus and duplicated pipelines disappear.

> Caveat: the removed-IP "age at removal" distribution (`retention.csv` `past[]`)
> is not captured by a live `first_seen` map alone — it needs a small
> **removal-event emission** at merge-join time (bounded, not free).

### 9.5 Adoption sequence (recommended)

1. Build the `iprange` interval-map engine + format (needed for the SDK anyway).
2. Adopt in `update-ipsets` **incrementally, highest-ROI first**:
   **(a) overlap matrix** → merged-membership sweep; **(b) age/retention** →
   `first_seen` interval map.
3. Independently: **raise `max_ingest_workers`** (config-only quick win).

---

## 10. Conformance & benchmark harness (DECIDED)

- One black-box corpus: `(args, stdin) → expected (stdout, exit)`, run against all
  three CLIs. Divergence = bug.
- Includes **byte-identical binary read/write** cases (cross-validates the format).
- Library-only behaviors (mmap, zero-alloc) exposed through CLI modes/flags so the
  same harness measures them (e.g. `--mmap`, a benchmark mode reporting
  allocation counts + RSS).
- One benchmark harness: same inputs, compare wall-time / allocations / RSS across
  the three; gate on the 5–10% tolerance.
- C is the oracle; Rust then Go are brought to green against the same corpus.

---

## 11. Phasing

Critical path to Netdata threat-intel annotation (Decision 11):
**iprange → update-ipsets adoption → update-ipsets SDK → Netdata.**

| Phase | Deliverable |
|---|---|
| **1 — Engine + format** | iprange interval-map engine (**dual-stack**), C as oracle; single-feed format (sectioned, signed, streamed); conformance + benchmark harness; Rust + Go to green within 5–10%. |
| **2 — Merged multi-feed** | Catalog + merged index + per-feed dossiers + feed-id registry → one-lookup all-memberships (the real-time-annotation enabler). |
| **3 — update-ipsets adoption** | Replace `pkg/iprange` with iprange; migrate **overlap matrix**, then **age/retention**, onto the engine. *(Independent quick win: raise `max_ingest_workers`.)* |
| **4 — update-ipsets SDK** | Rust + Go SDK: artifact fetch/sync + dossier schema + feed-id registry, embedding iprange. |
| **5 — Netdata** | netflow (Rust) + topology/network-viewer (Go) consume the SDK → real-time IP annotation. |

> **Sequencing (DECIDED — Decision 11):** real-time Netdata annotation lands at
> **phase 5**, gated on **phase 2** (merged format) + **phase 4** (the SDK).
> Phase 1 is the foundation (single-feed + cached/non-real-time use). Netdata
> never depends on iprange directly — only via the update-ipsets SDK.

---

## 12. Open decisions

| # | Open item | Options | Status |
|---|-----------|---------|--------|
| O1 | License/redistribution policy | include + mark | **RESOLVED → Decision 10** |
| O2 | Consumption / sequencing | indirect via update-ipsets SDK | **RESOLVED → Decision 11, §2.4, §11** |
| O3 | Index representation | explicit `{start,end,value_id}` vs breakpoints | **OPEN** — explicit recommended (closest to current model) |
| O4 | Membership-set encoding | interned sorted id-lists vs bitset | **OPEN** — interned sorted id-lists recommended (sparse) |

---

## Appendix A — Evidence sources

- **update-ipsets code** (`firehol/update-ipsets`, the Go repo, excluding
  `pkg/iprange`): data model, analytics, retention, pipeline — file:line refs in §9.
- **Live server** (the new update-ipsets daemon, internal host — see the parent
  FireHOL operations notes for host/endpoint details):
  - `/api/v1/status` → 423 sources + 13 merges.
  - admin `/metrics` (Prometheus) → `engine_phase_duration_ms` per-phase split (§9.3).
  - `/proc/<pid>/io` + `ps` → CPU-bound, ~1 core, 7 GB reads / 2.8 GB writes in 9.5 min.
  - systemd journal → incremental runs 13–46 s; service instances burning 40 min–
    hours CPU per restart; peak RSS 1.5 GB.
  - `runtime.yaml` → `max_ingest_workers: 1` active.

## Appendix B — Naming & compatibility rules

- Keep pre-existing `update-ipsets` ipset **names** stable (URLs, history,
  retention, comparison data all key off them).
- Public PRs/commits describe code only — no infrastructure/server details.
