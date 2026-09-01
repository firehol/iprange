# `iprange` JSON-RPC v1 Production API

**Status:** approved implementation contract; unsupported until SOW-0028 is
completed.

## Purpose and authority

This specification defines the production application interface exposed by
the Rust and pure-Go `iprange` executables. It is the implementation authority
for command selection, JSON-RPC framing, common data types, method parameters,
method results, error behavior, limits, and bulk-file schemas.

The executable is invoked as exactly:

```text
iprange --jsonrpc
```

SOW-0028 adds no other JSON-RPC transport option. Normal invocations continue
to use the released legacy `iprange` command-line contract. `--jsonrpc` cannot
be combined with a legacy option or input. `--v4`, `--daemon`, TCP, HTTP, and
WebSocket transports are unsupported.

The JSON-RPC methods are production operations. Conformance tests and
benchmarks are clients of these methods; production binaries contain no
test-only method, field, counter, fixture generator, or benchmark behavior.

The v4 database semantics and factual SDK outcomes remain governed by
[`binary-format-v4.md`](binary-format-v4.md). This specification never exposes
physical pages, roots, offsets, feed indexes, membership IDs, structure IDs,
bitmap words, allocator state, or file-backed mapping addresses.

## Transport

### Framing

- stdin and stdout are UTF-8 byte streams.
- Each physical input line contains exactly one complete JSON-RPC 2.0 request
  object or batch array.
- LF and CRLF terminate input frames. Output frames always end in LF.
- An unescaped CR or LF inside JSON is invalid.
- The hard frame limit is 1,048,576 bytes before the line terminator. There is
  no command-line override in API v1.
- The same 1,048,576-byte limit applies to every encoded response frame,
  including a batch response. Each encoded response object is also limited to
  65,000 bytes; with the 16-element batch bound, array punctuation cannot
  exceed the frame ceiling. A handler refuses an inline result with
  `output_limit` before stdout receives any part of that frame. Potentially
  larger results use bounded cursors or caller-selected output files.
- A mutating request whose worst-case complete inline response object
  (validated params, longest encodings of every counter and identity, real
  request-derived counts such as window or source counts, echoed request id)
  cannot fit the 65,000-byte object ceiling is refused with `output_limit` and
  outcome `not_started` BEFORE any writer is opened, destination published, or
  other mutation occurs. A committed workflow is never relabeled as a
  read-only failure by the post-hoc response bound. Current instances:
  `history.project` (window count) and `algebra.publish` (live-source count);
  read-only methods may keep the post-hoc conversion because no durable fact
  is at risk.
- A frame over the limit produces error `-32001` with `id: null` when stdout is
  writable, then the process closes. Bytes after the limit are discarded only
  as part of process shutdown; they are never parsed as another frame.
- stdout contains JSON-RPC frames only. Diagnostics use stderr.

### Requests, batches, and notifications

- Every request has `jsonrpc: "2.0"`, an `id`, a method beginning
  `iprange.v1.`, and object-valued `params`.
- `id` is a string or an integral JSON number. Null, fractional, and
  exponent-form numeric identifiers are invalid.
- Unknown request members and unknown parameter members are invalid.
- A batch contains 1 through 16 request/notification objects. An empty array is
  invalid.
- Batch elements execute in array order. Their responses are returned in one
  array in the same order, excluding valid notifications.
- `iprange.v1.cancel` is the only notification. Every other method requires an
  `id`; a request without one is not executed and produces no response, as
  required for an invalid notification by JSON-RPC 2.0.
- The service has one active ordinary request and at most 16 queued requests.
  A request exceeding the queue bound fails with `server_busy`.
- The read loop remains active while work executes so cancellation and EOF can
  be observed.
- Cancellation notifications are transport controls: the read loop applies
  them immediately after validating the enclosing frame instead of putting
  them behind ordinary work. Within a batch they may cancel an active request
  from an earlier frame or an ordinary element already queued from the same
  batch. This is the sole exception to ordinary array-order execution.

### Shutdown

- `iprange.v1.cancel` has params `{"request_id": ID}`. Unknown or already
  terminal IDs are ignored. It has no response.
- stdin EOF stops acceptance, cancels queued requests, requests cancellation of
  active work, waits for a factual terminal outcome, closes all cursors and
  readers, flushes a final response if stdout is writable, and exits zero unless
  transport shutdown itself fails.
- Broken stdout and termination signals use the same cancellation and handle
  cleanup path. They do not invent a response or convert an unknown durable
  outcome into success or failure.
- A protocol error does not change the process exit status while the service
  can continue. Startup/framing failure and unrecoverable stdout failure exit
  non-zero.

## JSON conventions

### Names, required fields, and null

- JSON member names use `snake_case`.
- Parameters and results reject unknown fields.
- A field is required unless explicitly described as optional.
- Optional fields are absent, not null. Null is accepted only where a schema
  explicitly lists it as a value.
- Enum values use the exact lowercase strings shown here.
- Arrays preserve caller order unless a method declares canonical order.

### Exact integers and identities

- Values whose domain is at most unsigned 32-bit use JSON integers.
- All unsigned 64-bit counters, page limits, byte limits, transaction IDs, and
  IPv4/IPv6 cardinalities use canonical unsigned decimal strings with no sign,
  separator, leading zero (except `"0"`), fraction, or exponent.
- A 129-bit cardinality uses the same decimal-string rule and can represent the
  complete IPv6 address space.
- Database, sidecar, attempt, and commit-nonce IDs are 16 bytes encoded as 32
  lowercase hexadecimal characters.
- Fixed byte digests and commitments are encoded as lowercase hexadecimal with
  exactly two digits per byte; no public byte array is serialized as a JSON
  integer array.
- A file identity is
  `{"volume":"DECIMAL","file":"DECIMAL"}`. POSIX device/inode and Windows
  volume/file identities map to these semantic members without exposing a
  platform-specific struct layout.
- IPv4 and IPv6 addresses use canonical text. IPv4-mapped IPv6 remains an IPv6
  address in IPv6 databases.
- An inclusive address range is `{"from":"IP","to":"IP"}`.

### Common enums

- `family`: `ipv4` or `ipv6`.
- `source_mode`: `immutable` or `live`.
- `value_kind`: `direct`, `membership`, or `structured`.
- `structure_kind`: `none` or `network_enrichment_v1`.
- `direction`: `forward` or `reverse`.
- `publication_policy`: `fail_if_exists`, `replace_existing`, or
  `replace_existing_no_rollback`.
- `publication_resolution_mode`: `complete` or `remove`.
- `live_transition_resolution_mode`: `complete` or `rollback`.
- `logical_change`: `changed` or `unchanged`.

### Paths

- A path is a non-empty platform-native string without NUL.
- `-` is invalid because stdin/stdout carry the protocol.
- Stdio methods run with the parent's operating-system filesystem authority.
- Inputs may be relative to the process working directory or absolute.
- Outputs are never silently treated as inputs and input files are never
  modified.

### Value tags and metadata

- A value-tag input is exactly one of `{"text":"UTF8"}` or
  `{"hex":"LOWERCASE_HEX"}`. `text` encodes its UTF-8 bytes; `hex` contains
  an even number of lowercase digits. Either form represents zero through 15
  bytes and may not contain a NUL byte. Results always use
  `{"hex":"LOWERCASE_HEX"}` so every valid v4 tag, including non-UTF-8
  application tags, remains representable. The exact hex for `first_seen` and
  `last_seen` has the corresponding SDK semantics.
- Base64 uses the RFC 4648 standard alphabet with required `=` padding and no
  whitespace. Decoders reject non-canonical encodings.
- A metadata input is exactly one of:
  - `{"mode":"keep"}`;
  - `{"mode":"clear"}`;
  - `{"mode":"replace_utf8","text":"EXACT_UTF8_TEXT"}`;
  - `{"mode":"replace_base64","base64":"RFC4648"}`; or
  - `{"mode":"replace_file","path":"PATH"}`.
- `replace_utf8` stores the decoded JSON string's exact UTF-8 bytes;
  `replace_base64` stores the decoded bytes; and `replace_file` stores the exact
  file bytes. The adapter does not parse, validate, normalize, or re-encode the
  metadata. Empty bytes, whitespace, malformed JSON, and arbitrary binary are
  distinct valid values under the v4 opaque-metadata contract.

### Budgets

Every listed budget field is required except the explicitly conditional
`scratch_directory`; API v1 has no magic zero/unlimited value.

- `writer_budget`:
  `{"max_heap_bytes":"U64","max_private_pages":"U64",`
  `"max_growth_pages":"U64","max_open_files":U32}`.
- `snapshot_budget`:
  `{"max_heap_bytes":"U64","max_output_pages":"U64",`
  `"max_open_files":U32}`.
- `validation_budget`:
  `{"max_heap_bytes":"U64","max_open_files":U32,`
  `"max_scratch_bytes":"U64","max_scratch_files":U32,`
  `"scratch_directory":"PATH"}`.
- `recovery_budget`:
  `{"max_heap_bytes":"U64","max_output_pages":"U64",`
  `"max_open_files":U32,"max_scratch_bytes":"U64",`
  `"max_scratch_files":U32,"scratch_directory":"PATH"}`.
- `membership_query_budget`:
  `{"max_heap_bytes":"U64"}`.
- `algebra_budget`:
  `{"max_heap_bytes":"U64","max_sources":U32}`.
- `algebra_output_budget`:
  `{"max_output_pages":"U64","max_open_files":U32}`.
- `result_budget`:
  `{"max_rows":"U64","max_output_bytes":"U64",`
  `"max_open_files":U32}`.
- `immutable_feed_budget`:
  `{"max_heap_bytes":"U64","max_output_pages":"U64",`
  `"max_workspace_pages":"U64","max_open_files":U32}`.

For validation/recovery, scratch is explicitly disabled by setting both
scratch limits to `"0"` and omitting `scratch_directory`. Enabling it requires
both nonzero limits and the directory; partial combinations are invalid. These
zero values mean disabled, never unlimited. SDK validation determines all
other legal minima. Values outside the target platform's addressable range are
invalid before work starts.

### File sources and selections

A feed name is 1 through 255 lowercase ASCII bytes. Its first and last bytes
are `a-z` or `0-9`; interior bytes additionally allow `_`, `-`, and `.`. This
is the exact v4 `FeedName` grammar.

A database source is:

```json
{"path":"PATH","mode":"immutable|live"}
```

A feed selection is exactly one of:

```json
{"mode":"all"}
{"mode":"named","feeds":["NAME", "..."]}
```

Named selections contain at least one unique valid feed name. Caller order is
preserved for reporting; algebra uses canonical name semantics.

A current coverage source is:

```json
{
  "source":{"path":"PATH","mode":"immutable|live"},
  "feed":"NAME"
}
```

The source must be a membership v4 file containing the named feed.

The semantic JSON value for `network_enrichment_v1` is:

```json
{
  "asn":64512,
  "country_id":1,
  "state_id":2,
  "city_id":3,
  "location":{"latitude_microdegrees":123,"longitude_microdegrees":-456},
  "threat_feeds":["feed-a","feed-b"]
}
```

The four scalar IDs are u32 JSON integers. `location` is either the shown
object with two signed 32-bit JSON integers or null. `threat_feeds` is always
present in feed-catalog order, including an empty array.

### Text input descriptor

`current.publish` accepts:

```json
{
  "paths":["PATH", "..."],
  "family":"ipv4|ipv6",
  "fix_network":true,
  "default_prefix":32,
  "dns":{"threads":8,"silent":false},
  "expand_at_paths":true,
  "max_line_bytes":1048576,
  "max_expanded_paths":100000
}
```

- `paths` contains at least one path. When `expand_at_paths` is true, a path
  beginning with `@` has the released legacy file-list/directory meaning.
- Text and released legacy binary inputs are autodetected exactly as legacy
  mode does.
- Family, mapped-IPv4, CIDR/netmask/range, comment, whitespace, hostname, DNS,
  network fixing, invalid-family, and warning behavior matches legacy mode.
- `default_prefix` is 0 through 32 for IPv4 and 0 through 128 for IPv6.
- DNS threads is 1 through the documented legacy maximum.
- `max_line_bytes` is 1 through 1,048,576 and bounds one physical text line
  before parsing or DNS work. `max_expanded_paths` is 1 through 1,000,000 and
  bounds the total paths after `@` file-list/directory expansion. Exceeding
  either is `input_format` before publication.
- Parsed ranges are normalized through the v4 immutable builder in bounded
  batches; no complete feed is retained by the adapter.

### Direct-value input file

`direct.replace` uses the descriptor
`{"path":"PATH","max_line_bytes":1048576}`. The bound is 1 through
1,048,576. The file is UTF-8 CSV with the mandatory header:

```text
from,to,value
```

- Blank lines are ignored. Comments and extra columns are invalid.
- `from` and `to` are canonical or parseable addresses of the database family.
- `value` is unsigned decimal 0 through 4294967295.
- Ranges are inclusive and may be unordered, duplicate, or overlapping.
- Later records overwrite earlier records, exactly matching direct-replacement
  workflow order.

## Responses and errors

### Standard success

```json
{"jsonrpc":"2.0","id":"REQUEST","result":{}}
```

Every result begins with `method`, whose value is the exact method name. This
prevents a client from decoding a valid response with the wrong result schema.

### Factual result conversion

Where a method returns a public SDK result/report, the JSON result contains the
complete semantic public value converted mechanically:

- public semantic field names become `snake_case`;
- enums become their documented lowercase semantic names;
- value tags use the canonical hex object defined above;
- all u64/cardinality/identity fields use the encodings above;
- optional SDK identities are absent when unavailable;
- an SDK `cause` becomes the JSON-RPC error's human `message`, never a success
  field;
- cleanup, residue, housekeeping, validation findings, recovery gaps, commit
  evidence, and publication evidence are not dropped;
- platform-only fields are present only on the platform that can produce them
  and are listed by `system.describe.platform_result_fields`.

The Rust public types and `binary-format-v4.md` are semantic authority. Go field
names may differ internally but must serialize identically.

A method that internally opens a live reader or writer must close it before
responding. Where the public SDK supplies a factual close result, success
contains it as `source_close`, `writer_close`, or the method-specific close
field. A method that opens several readers reports every live close result,
in reader close order, as `source_closes`. A close failure is a product
error whose `details` preserve both the completed logical report and the
close result; it is never silently converted to success.

### Error envelope

```json
{
  "jsonrpc":"2.0",
  "id":"REQUEST",
  "error":{
    "code":-32010,
    "message":"human diagnostic",
    "data":{
      "code":"invalid_argument",
      "outcome":"not_started",
      "details":{}
    }
  }
}
```

Standard JSON-RPC codes are `-32700` parse error, `-32600` invalid request,
`-32601` method not found, `-32602` invalid params, and `-32603` internal error.
Transport frame-too-large is `-32001`; bounded queue exhaustion is `-32002`.
All v4 domain failures use `-32010` and a stable `data.code` equal to the
canonical SDK error name converted to `snake_case`. Adapter codes not supplied
by the SDK are limited to:

- `frame_too_large`, `server_busy`, `invalid_path`, `input_format`,
  `output_limit`, `handle_not_found`, `handle_wrong_kind`, `handle_closed`,
  `cursor_not_found`, `cursor_closed`, `cancelled`, and `io`.

`data.outcome` is one of `not_started`, `not_committed`, `committed`,
`not_published`, `published`, `outcome_unknown`, or `read_only_failure`.
`not_committed` and `committed` come only from a factual `CommitResult`;
`not_published` and `published` come only from a factual `PublicationResult`;
an unknown commit or publication maps to `outcome_unknown`; a read-only
operation maps to `read_only_failure`; and a failure before any durable SDK
attempt maps to `not_started`. No generic retryable boolean is exposed because
the SDK supplies no universal retry policy. `details` carries the complete
factual result whenever an attempt began.

## Reader methods

The connection owns at most 64 readers and 64 cursors. Handles are opaque
lowercase strings of 32 random hexadecimal characters and are never reused in
one process.

### `iprange.v1.reader.open`

Params:

```json
{"source":{"path":"PATH","mode":"immutable|live"}}
```

Result: `method`, `reader`, and `info`. `info` is the complete `DatabaseInfo`
conversion. Live mode registers and pins one committed generation.

### `iprange.v1.reader.close`

Params: `{"reader":"HANDLE"}`.

Result: `method`, `closed:true`, plus the complete live close result for live
readers. Closing an already closed/unknown handle is `handle_not_found`.

### `iprange.v1.reader.info`

Params: `{"reader":"HANDLE"}`.

Result: `method` and complete `info`.

### `iprange.v1.reader.metadata`

Params:

```json
{"reader":"HANDLE","delivery":{"mode":"inline"}}
```

or `delivery` is:

```json
{"mode":"file","path":"PATH","publication_policy":"POLICY",
 "max_output_bytes":"U64","max_open_files":3}
```

Result always contains `method` and `present`. Inline delivery also contains
`base64` with the exact stored bytes in canonical padded RFC 4648 form when
present. File delivery atomically writes only those exact bytes and instead
returns `output` with path, lowercase SHA-256, and byte count. No file is
created when metadata is absent. Inline delivery fails with `output_limit`
before response serialization when the response frame would exceed its hard
limit.

### `iprange.v1.reader.lookup`

Params:

```json
{"reader":"HANDLE","addresses":["IP", "..."]}
```

The array has 1 through 4096 addresses of the reader family. Result contains
`method` and ordered `matches`. Each match has `address`, `present`, and exactly
the semantic value for the database kind:

- direct: `value` (u32);
- membership: `feeds` in catalog order;
- `network_enrichment_v1`: decoded scalar fields plus `threat_feeds` in catalog
  order.

Absent matches contain only `address` and `present:false`.

The complete ordered result must fit one response frame. The server counts
encoded bytes while building it and returns `output_limit` before writing any
response bytes if it cannot fit. Large membership/structured lookups use
`query.matching_feeds` or `export` to a file.

### `iprange.v1.reader.feeds.open`

Params: `{"reader":"HANDLE","batch_size":4096}`. The batch size is 1
through 4096. Result contains `method` and `cursor`. Direct databases return
`wrong_kind`.

### `iprange.v1.reader.feeds.next`

Params: `{"cursor":"HANDLE"}`. Result contains `method`, `feeds`, and `done`.
Each row has `name`; rows follow feed-catalog order. Cardinalities are obtained
through `query.cardinalities`, not recomputed by catalog enumeration. The server
returns at least one row when one remains, stops before the response-frame
limit or requested batch size, and closes the cursor automatically at
`done:true`. A single unencodable row is `output_limit`.

### `iprange.v1.reader.feeds.close`

Params: `{"cursor":"HANDLE"}`. Result contains `method` and `closed:true`.

### `iprange.v1.reader.matching_feeds`

Params: `{"reader":"HANDLE","address":"IP"}`.

Result: `method`, `address`, `feeds` in catalog order, and complete
`MatchingFeedsReport`. This interactive method refuses with `output_limit`
when the complete response would exceed the response-frame limit; use the
file-level `query.matching_feeds` method for an unbounded valid result.

### `iprange.v1.reader.ranges.open`

Params:

```json
{
  "reader":"HANDLE",
  "view":{"kind":"direct|structured|feed","feed":"NAME"},
  "direction":"forward|reverse",
  "start":"IP",
  "batch_size":4096
}
```

`feed` is required only for `kind:"feed"`. `batch_size` is 1 through 4096.
`start` is optional and valid only for direct/structured views. It applies the
SDK cursor seek: forward starts with the first range whose end is at or after
the address; reverse starts with the first range whose start is at or before
the address. Result: `method` and `cursor`.

### `iprange.v1.reader.ranges.next`

Params: `{"cursor":"HANDLE"}`.

Result: `method`, `records`, and `done`. Records are ordered ranges with
semantic values for direct/structured views and no value for a feed view. The
server returns at least one record when one remains and stops before the
response-frame limit or requested batch size. A single record whose semantic
value cannot fit is `output_limit`; `export` is the complete-result path. The
cursor closes automatically when `done:true` is returned.

### `iprange.v1.reader.ranges.close`

Params: `{"cursor":"HANDLE"}`.

Result: `method` and `closed:true`.

## Database and metadata methods

### `iprange.v1.database.create`

Params:

```json
{
  "path":"PATH",
  "family":"ipv4|ipv6",
  "value_kind":"direct|membership|structured",
  "structure_kind":"none|network_enrichment_v1",
  "value_tag":VALUE_TAG,
  "reader_capacity":256
}
```

`structure_kind` must be `network_enrichment_v1` only with structured values
and `none` otherwise. Creation uses the SDK's fixed creator-only security,
creates an empty database, and leaves metadata absent. A client that needs
initial metadata calls `database.metadata.replace` after successful creation.
Result is the complete `CreateResult` plus `method`.

### `iprange.v1.database.initialize_live`

Params: `path`, `reader_capacity`, and no budget. Result is the complete
`LiveTransitionResult`.

### `iprange.v1.database.reset_live`

Params: `path`, `reader_capacity`, and `policy` equal to `rollback_safe` or
`discard_previous`. Result is `LiveTransitionResult`.

### `iprange.v1.database.create.resolve`

Params: `path`, one complete `create_result` returned by `database.create`, and
`resolution_mode` (`complete` or `rollback`). Result is complete
`CreateResult`.

### `iprange.v1.database.live_transition.resolve`

Params: `path`, one complete `live_transition_result` returned by
`database.initialize_live` or `database.reset_live`, and `resolution_mode`
(`complete` or `rollback`). Result is complete `LiveTransitionResult`.

### `iprange.v1.database.live_residue.resolve`

Params: `path` and `resolution_mode` (`complete` or `rollback`). This is the
resultless interrupted-transition resolver and must not substitute for the two
evidence-carrying methods when the caller has their factual result. Result is
complete `LiveResidueResult`.

### `iprange.v1.database.reclaim`

Params: `path`, `max_transactions` (decimal string), `max_pages` (decimal
string), and `writer_budget`. Result is complete `ReclaimResult` and
`writer_close`.

### `iprange.v1.database.info`

Params: one `source`. Result is complete `DatabaseInfo`.

### `iprange.v1.database.metadata.get`

Params: one `source` and one `delivery` from `reader.metadata`. Result matches
`reader.metadata` without creating a connection handle.

### `iprange.v1.database.metadata.replace`

Params: `path`, `metadata` with any mode except `keep`, and `writer_budget`.
Result contains `logical_change` and complete `CommitResult` when changed, plus
`writer_close`. An unchanged clear has no commit attempt. Every replacement is
explicitly staged and committed even when its bytes equal the prior bytes,
because the SDK deliberately does not read/decompress/compare old metadata.

## Publisher mutation methods

Every live mutation opens one clean writer, performs one high-level SDK
workflow, applies the requested metadata within that draft, commits when
changed, closes the writer, and returns the complete workflow and commit/close
facts. Input failure aborts the draft and preserves the prior generation.

Metadata mode `keep` never stages metadata. When a high-level workflow changes
content, a replacement or clear is staged in that same draft before its commit.
When the workflow reports no content change, its draft is already clean; the
server opens one fresh typed transaction matching the database kind, stages
only the requested metadata operation, and commits every replacement. Clear
commits only when metadata was present.
The result reports the content `logical_change`, the metadata
`logical_change`, and a `commit` only when a commit was attempted. There is
never a fabricated no-op `CommitResult`.

### `iprange.v1.current.publish`

Params:

```json
{
  "input":TEXT_INPUT,
  "feed":"NAME",
  "value_tag":VALUE_TAG,
  "metadata":METADATA_INPUT,
  "destination":"PATH",
  "publication_policy":"POLICY",
  "immutable_feed_budget":IMMUTABLE_FEED_BUDGET
}
```

Because the destination is a new immutable file, metadata mode `keep` is
invalid; `clear` creates absent metadata. Result contains `method`, the complete
`ImmutableFeedReport`, and the complete `PublicationResult`. The output is one
immutable membership file with the named feed, including an empty cataloged
feed for empty input.

### `iprange.v1.direct.replace`

Params: `path`, `input` (the direct CSV descriptor), `metadata`, and
`writer_budget`.
Result contains the complete direct-replacement `WorkflowReport`, logical
change, and commit/close facts.

### `iprange.v1.retention.first_seen.refresh`

Params: `path`, `current` coverage source, `refresh_value` (u32), optional
`removals_output` containing `path`, `publication_policy`, and `result_budget`,
`metadata`, and `writer_budget`.

The target must be a direct database tagged exactly `first_seen`. Each removal
record is line-oriented JSON:

```json
{"from":"IP","to":"IP","first_seen":123,"removed_at":456,"addresses":"N"}
```

Records are address ordered. The private removal artifact is published only
after the commit is factually known to have committed. It is discarded when
the commit is known not to have committed; an outcome-unknown commit never
publishes it and reports its exact cleanup state. Result contains the complete
workflow/commit facts and removal publication outcome, path, digest, rows, and
address count when requested. The removals publication facts carry
`publication` and `destination_content` only: the artifact is adapter-owned,
so no SDK publication attempt exists for it.

### `iprange.v1.retention.last_seen.refresh`

Params: `path`, `current`, `refresh_value`, `cutoff`, `metadata`, and
`writer_budget`. The target tag must be exactly `last_seen`. Result contains the
complete workflow and commit/close facts.

### Named-feed lifecycle

- `iprange.v1.feeds.create`: params `path`, `feed`, `current`, `metadata`, and
  `writer_budget`.
- `iprange.v1.feeds.replace`: the same params.
- `iprange.v1.feeds.delete`: params `path`, `feed`, `metadata`, and
  `writer_budget`.
- `iprange.v1.feeds.rename`: params `path`, `old_feed`, `new_feed`, `metadata`,
  and `writer_budget`.
- `iprange.v1.feeds.import`: params `path`, `source` database source,
  `metadata`, and `writer_budget`.

Each of create, replace, and import returns the complete corresponding
`WorkflowReport` and commit/close facts. Delete and rename return the
commit, metadata, and writer-close facts only: the SDK exposes no workflow
report for them, and the catalog-changing outcome is carried by the commit
facts. Create preserves an empty feed. Replace requires an existing feed.
Import copies the complete source catalog/memberships by name and translates
all internal IDs.

### `iprange.v1.history.project`

Params:

```json
{
  "path":"MEMBERSHIP_LIVE_PATH",
  "last_seen":{"path":"PATH","mode":"immutable|live"},
  "windows":[{"feed":"NAME","cutoff":123}, "..."],
  "metadata":METADATA_INPUT,
  "writer_budget":WRITER_BUDGET
}
```

Windows contain 1 through 4096 unique names and retain addresses whose
last-seen value is greater than the window cutoff, following the SDK contract.
Result is complete `HistoryProjectionReport` and commit/close facts. The window
count is additionally bounded by the response-object ceiling: a request whose
worst-case complete inline report cannot fit a 65,000-byte response object is
refused with `output_limit` and `not_started` before any writer is opened or
mutation occurs (the worst-case report uses the longest encodings of every
counter and the request's real feed names).

## Query, join, and algebra methods

All potentially large tabular results use an output descriptor:

```json
{
  "path":"PATH",
  "format":"jsonl|csv",
  "publication_policy":"POLICY",
  "result_budget":RESULT_BUDGET
}
```

JSONL writes one compact JSON row per LF. CSV is UTF-8 RFC-4180-style records
with LF endings and an always-present header. Rows use the field order defined
below and are atomically published. Results contain output path, lowercase
SHA-256 digest, rows, bytes, and the complete SDK report.

### `iprange.v1.query.cardinalities`

Params: `source`, `selection`, `membership_query_budget`, and `output`. Rows are
`feed,addresses`, ordered by catalog order. Result includes complete
`MembershipAggregationReport`.

### `iprange.v1.query.overlaps`

Params: `source`, `selection`, `membership_query_budget`, `mode`, and `output`.
`mode` is exactly one of:

```json
{"kind":"all_pairs"}
{"kind":"target","target_feed":"NAME"}
{"kind":"selected_pairs","pairs":[{"left":"NAME","right":"NAME"}]}
```

Selected pairs are non-empty, unique after canonicalizing each unordered pair,
and contain no self-pair. Rows are `left,right,addresses`, with canonical
unordered catalog-pair order. Result includes `MembershipAggregationReport`.

### `iprange.v1.query.matching_feeds`

Params: `source`, 1 through 4096 `addresses`, and `output`. Output rows are
`address,feeds`, where JSONL uses a catalog-ordered string array and CSV uses a
semicolon-separated catalog-ordered string. The result contains an aggregate
matching count and the ordinary output facts. This file-level method opens one
reader once and does not return a handle.

### `iprange.v1.join.direct`

Params: `membership` source, `selection`, `membership_query_budget`, `direct`
source, `output`, and `max_result_cells` as a decimal string. Rows are
`feed,direct_value,addresses`; `direct_value` is null for uncovered cells.
Order follows selected feed catalog order then ascending direct value, with the
uncovered cell last. Result includes complete `DirectJoinReport`.

### `iprange.v1.join.membership`

Params: `left` and `right`, each containing `source`, `selection`, and
`membership_query_budget`, plus `output`. Cross rows are
`kind:"cross",left,right,addresses`; uncovered rows are
`kind:"uncovered",side,feed,addresses`. Cross rows use left catalog order then
right catalog order, followed by uncovered-left and uncovered-right catalog
order. Result includes complete `MembershipJoinReport`.

### Algebra sources

An algebra source list contains 1 through the algebra budget's `max_sources`
entries:

```json
[
  {"source":{"path":"PATH","mode":"immutable|live"},
   "scope":{"mode":"all"},
   "membership_query_budget":{"max_heap_bytes":"U64"}},
  "..."
]
```

Each `scope` is resolved independently before construction of the global
same-name algebra catalog. The per-source query budget bounds that resolution;
`algebra_budget` separately bounds the global algebra state.

### `iprange.v1.algebra.count`

Params: `sources`, `selection`, and `algebra_budget`. Result contains complete
`AlgebraCountReport` and exact union cardinality.

### `iprange.v1.algebra.compare`

Params: `sources`, `left` selection, `right` selection, and `algebra_budget`.
Result is complete `AlgebraComparisonReport`, including exact left-only,
right-only, and common cardinalities and equality.

### `iprange.v1.algebra.publish`

Params: `sources`, `operation`, `output_mode`, `value_tag`, `metadata`,
`destination`, `publication_policy`, `algebra_budget`, and
`algebra_output_budget`.

- operation is `{"kind":"union","selection":SELECTION}`,
  `{"kind":"intersection","selection":SELECTION}`, or
  `{"kind":"exclusion","included":SELECTION,"excluded":SELECTION}`.
- output mode is `{"kind":"preserve_feeds"}` or
  `{"kind":"flat","feed":"NAME"}`.

Because the destination is a new immutable file, metadata mode `keep` is
invalid; `clear` creates absent metadata. Result contains the complete
`AlgebraSetReport` and `PublicationResult`.

## Export

### `iprange.v1.export`

Params:

```json
{
  "source":{"path":"PATH","mode":"immutable|live"},
  "view":{"kind":"direct|structured|feed|selection","feed":"NAME",
          "selection":FEED_SELECTION},
  "format":"netset|ipset|ranges|csv|jsonl|legacy_binary",
  "destination":"PATH",
  "publication_policy":"POLICY",
  "min_prefix":0,
  "prefixes":[0],
  "result_budget":RESULT_BUDGET
}
```

`feed` is present only for a feed view; `selection` only for selection.
`min_prefix` and `prefixes` are mutually exclusive optional controls valid
only for netset. `min_prefix` enables every prefix from that value through the
family's host prefix. With neither field, every prefix from zero through the
host prefix is enabled. `prefixes` is non-empty, unique, in range, and includes
the host prefix.

- `netset`: canonical minimal CIDRs/singletons in address order.
- `ipset`: one address per line; expansion is refused before exceeding budgets.
- `ranges`: `from-to` in address order; singleton is one address.
- `csv`: always has `from,to,value`. Membership selection value is the
  semicolon-separated catalog-ordered feed names; direct is u32; structured is
  canonical compact JSON.
- `jsonl`: each row has `from`, `to`, and semantic `value`.
- `legacy_binary`: exact released binary bytes and only for a flat address set
  representable by that family format. Direct/structured values cannot be
  discarded implicitly.

Result contains path, format, SHA-256, rows/ranges, exact addresses, bytes, and
source database identity. Export publication is atomic and durable.

## Snapshot, validation, recovery, and resolution

### `iprange.v1.snapshot`

Params: `source`, `destination`, `publication_policy`, and `snapshot_budget`.
Result is complete `SnapshotResult`.

### `iprange.v1.validate`

Params: `path`, `mode`, `validation_budget`, and `findings_output` using the
tabular output descriptor. `mode` is exactly one of:

```json
{"kind":"immutable_current"}
{"kind":"live_current"}
{"kind":"offline_candidate","candidate":OPAQUE_CANDIDATE}
```

The offline candidate must be one exact value returned by
`recovery.inspect`; supplying it certifies exclusive source quiescence for the
complete call. This method requires JSONL and writes one
complete mechanically converted `ValidationFinding` per row; CSV is
unsupported because nested evidence must not be flattened. Every finding is
written; the result contains complete `ValidationResult`. Validation never
runs implicitly in another method.

### `iprange.v1.recovery.inspect`

Params: `path`, `mode` (`immutable`, `live`, or `caller_certified_offline`),
and `validation_budget`. Result contains complete
`RecoveryCandidateInspection` and opaque candidate values that can be supplied
unchanged to `recover`.

### `iprange.v1.recover`

Params: `source_path`, `source_mode` (`immutable`, `live`, or
`caller_certified_offline`), one exact `candidate` returned by
`recovery.inspect`, `destination`, `recovery_budget`, and `report_output`.
Recovery always requires a fresh absent destination and has no replacement
policy. `report_output` requires JSONL and writes one complete mechanically
converted `RecoveryUnknownEnvelope` per row; CSV is unsupported. Result is
complete `RecoveryResult` or
`RecoveryPreparationFailure`. It never replaces the source.

### Resolution attempts

Factual attempt values are ordinary JSON objects returned by the operation that
made the attempt. Clients must preserve the complete object without removing
unknown platform fields.

- `iprange.v1.commit.resolve`: params `path`, complete `commit_result`, and
  `mode` (`live` or `immutable`); result is `CommitResolutionResult`.
- `iprange.v1.publication.inspect`: params `path`; result is
  `PublicationResidueInspection` and any opaque authenticated handle.
- `iprange.v1.publication.resolve`: params `path`, optional complete
  `publication_result`, and `resolution_mode` (`complete` or `remove`); when
  the result is absent the retained reservation is the sole authority. Result
  is `PublicationResult`.
- `iprange.v1.publication.residue.remove`: params one complete opaque `handle`
  returned by inspect; result is `PublicationResidueRemoval`.

Result/handle schemas are the complete mechanically converted public SDK
types. The server rejects missing, extra, stale, foreign, or identity-mismatched
evidence before destructive action.

## Maintenance

### `iprange.v1.maintenance.list`

Params:

```json
{"directory":"PATH","kinds":["scratch","reservation","publication_temp",
"windows_housekeeping"],"max_entries":4096,"output":OUTPUT_DESCRIPTOR}
```

Kinds are unique and the bound is 1 through 65536. JSONL output writes one
complete mechanically converted public SDK entry per row; CSV is unsupported
for this method because nested authenticated identities must not be flattened.
Rows are ordered by kind then canonical basename. Every removable entry
contains its opaque authenticated removal identity. Result contains the SDK
list reports and ordinary output facts.

### `iprange.v1.maintenance.remove`

Params: `{"entry":OPAQUE_ENTRY}` using one unchanged list entry. Result is the
complete SDK removal result. The method has no arbitrary path-delete form and
does not accept an entry synthesized from only a basename.

## `system.describe`

### `iprange.v1.system.describe`

Params: `{}`.

Result contains:

```json
{
  "method":"iprange.v1.system.describe",
  "product":"iprange",
  "product_version":"VERSION",
  "implementation":"rust|go",
  "jsonrpc_version":"2.0",
  "api_version":"1",
  "format":"iprange-v4-phase1-unsigned",
  "platform":"linux|macos|windows|freebsd|other",
  "families":["ipv4","ipv6"],
  "methods":["SORTED_METHODS"],
  "export_formats":["netset","ipset","ranges","csv","jsonl",
                    "legacy_binary"],
  "limits":{
    "input_frame_bytes":"1048576",
    "output_frame_bytes":"1048576",
    "response_object_bytes":"65000",
    "batch_requests":16,
    "queued_requests":16,
    "reader_handles":64,
    "cursor_handles":64,
    "lookup_addresses":4096,
    "cursor_records":4096
  },
  "fault_worker":{"available":true,"protocol":"VERSION"},
  "platform_result_fields":["SORTED_FIELDS"]
}
```

Methods and platform fields are bytewise sorted. Production binaries never
advertise test-only fields or methods. Factual platform fields, version, and
fault-worker availability may differ by build target or installation.
`fault_worker.protocol` is the SDK worker control-protocol version of this
build; `available` is true only when a candidate worker executable exists
beside the running binary.

## Legacy coexistence

- Mode selection checks for exact argument `--jsonrpc` before legacy parsing.
- More than one argument, or any other argument beside `--jsonrpc`, is invalid
  JSON-RPC startup and cannot fall back to legacy parsing.
- Without exact `--jsonrpc`, the complete released legacy grammar, output,
  diagnostics, feature probes, binary formats, and exit codes apply.
- Neither Rust nor Go invokes or links the C executable at runtime. The C
  executable remains the qualification oracle.

## Compatibility and evolution

- API v1 is additive only after release: existing method schemas and meanings
  cannot change.
- An incompatible application contract uses `iprange.v2.*`; it does not infer
  version from the on-disk format.
- Unknown methods are `-32601`; unknown fields are `-32602`. Clients can use
  `system.describe` for optional future additive methods.
- The future WebSocket daemon in SOW-0029 must carry these exact JSON-RPC
  payloads and method semantics. It may add connection authentication and path
  authority outside method params, but cannot fork this registry.

## Unsupported surfaces

- Network listeners and remote path access.
- HTTP download/cache, archive extraction, scheduling, source policy, website
  generation, public routing, signing, and trust management.
- Raw logical writer transactions over multiple JSON-RPC messages.
- Physical storage inspection or control.
- Complete feed bodies in JSON-RPC request or response frames.
- Test, benchmark, fault injection, fixture generation, or internal work-counter
  methods.
