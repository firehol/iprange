# iprange v3 — conformance corpus

Language-neutral fixtures that pin the **byte-identical** contract of the v3 binary
format (`.agents/sow/specs/binary-format-v3.md`). Every conforming writer
(Rust and Go) given a case's logical input MUST produce the exact golden
bytes; every conforming reader MUST accept a `bytes` golden and reject every
`reject` case.

## Layout

```
conformance/
  cases/<name>.json     # one test case (the logical input + expectation)
  golden/<name>.iprbin  # for "expect": "bytes" cases — the canonical output
```

## Case schema (JSON)

```json
{
  "name": "v4-two-ranges-one-value",
  "description": "human note (optional)",
  "ip_version": "v4",                       // "v4" | "v6"
  "feed_meta": {                            // all six fields; omitted => empty string
    "name": "", "category": "", "maintainer": "",
    "maintainer_url": "", "source_url": "", "license": ""
  },
  "license_flags": 0,                        // u32 (bit0 = dont_redistribute)
  "generation_unixtime": 1700000000,         // u64, POSIX seconds
  "ranges": [
    { "start": "10.0.0.0", "end": "10.0.0.255", "value": null },
    { "start": "11.0.0.0", "end": "11.0.0.15",
      "value": { "type_id": 2, "bytes_hex": "07" } }
  ],
  "expect": "bytes",                         // "bytes" | "reject"
  "reject_class": "InvalidInput"             // only for "reject"; the Error variant name
}
```

- **IPs** are canonical strings parsed by each language's stdlib
  (`Ipv4Addr`/`Ipv6Addr` in Rust, `net.ParseIP` in Go), then converted to the
  numeric key. Ranges are inclusive `[start, end]`.
- **value** is `null` (the sentinel "present, no value") or `{type_id, bytes_hex}`.
  `bytes_hex` is the opaque value bytes, lower-case hex, no separators.
- **license_flags** / **generation_unixtime** map straight to the header fields.

### Multi-feed merge cases (v3.1)

A case with a top-level `feeds` array is built via the **merge constructor**
(`MergeWriter`, §13.3) instead of the single-feed writer, producing a v3.1 interleaved
merged file (catalog + membership index). Provide `feeds` in place of `ranges`:

```json
{
  "name": "merge-v4-partial-overlap",
  "ip_version": "v4",
  "feed_meta": { "name": "merged" },          // the merged artifact's own identity
  "license_flags": 0,
  "generation_unixtime": 1700000000,
  "feeds": [
    { "feed_id": 1,                            // stable u32 id (catalog key, §13.2)
      "feed_meta": { "name": "alpha" },        // this feed's identity
      "ranges": [ { "start": "10.0.0.0", "end": "10.0.0.10" } ] }  // value-less
  ],
  "expect": "bytes"
}
```

Feeds may be listed in any order (the writer sorts by `feed_id`); ranges of different
feeds may overlap (that overlap becomes membership). `feed_id`s must be unique and
ranges within one feed must be disjoint, else the case is a `reject`.

## Running (Rust)

```
cd rust && cargo test -p iprange-format --test conformance        # verify
REGENERATE_GOLDENS=1 cargo test -p iprange-format --test conformance  # (re)write goldens
```

Goldens are regenerated only on purpose (the env var) and committed. A normal run
fails if any rebuilt case diverges from its committed golden — that is the
cross-language byte-identity guard.

## Adding a case

Add `cases/<name>.json`, run the regenerate command once to mint
`golden/<name>.iprbin` (for `bytes` cases), eyeball the result, then commit both.
The Go harness reads the same files.

## Legacy fixtures (`legacy/`)

`legacy/<name>.bin` are **real** `iprange --print-binary` outputs (legacy v1.0 IPv4 /
v2.0 IPv6), each with a `legacy/<name>.json` manifest listing the expected decoded
ranges and header metadata. Both the Rust (`tests/legacy.rs`) and Go
(`legacy_test.go`) legacy readers parse these and must agree, then migrate them to v3
and read the result back. Format: `.agents/sow/specs/legacy-binary-format.md`.
