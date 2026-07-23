# iprange v4 conformance corpus

The normative contract is
[`../../.agents/sow/specs/binary-format-v4.md`](../../.agents/sow/specs/binary-format-v4.md).

The former fixtures in this directory encoded unreleased, incompatible v4
experiments and were removed. They are not compatibility inputs. The Phase-1
corpus is rebuilt only from the exact current v4 contract during
SOW-0016.

The completed corpus must contain independently produced Go and Rust files and
must prove actual bidirectional opens. Each reader verifies the other writer's
files semantically; mutable allocation order, tree shape, membership IDs, and
zlib bytes need not match.

At minimum, fixtures and behavioral cases cover:

- IPv4 and IPv6 direct ranges, including legal empty subtrees and both full
  address spaces;
- unordered, duplicate, adjacent, nested, and overlapping input across batch
  boundaries, with direct assignments applied exactly in arrival order and feed
  input reduced to coverage union;
- named-feed catalog lifecycle, feed-index reuse, memberships wider than 64
  bits, and lazy maximum-width access;
- `FeedRef`/`MembershipRef` ownership and invalidation, all five advanced
  membership operations, and atomic transactions that change several feeds;
- high-level named-feed and direct replacement state machines, including
  `Begin`, batched `AddRanges`, `FinishInput`, optional metadata, statistics,
  commit, abort, source/sink failure, and cancellation;
- exact ingestion-source composition: each range call drains one finite source,
  `End` ends only that call, repeated calls concatenate in record order, and a
  later source failure aborts every earlier accepted prefix;
- every fixed `FinishInput` field for feed, direct, retention, and import
  workflows, including checked record/feed/membership counter overflow;
- name-based multi-feed import, including same-name union, missing-feed creation,
  destination-only preservation, and internal source-index/membership-ID
  translation;
- exact `retention` refresh: old values survive continuous coverage, new
  addresses receive the refresh value, removed addresses disappear, and a later
  reappearance receives the later value;
- absent, empty, and maximum-size opaque metadata;
- metadata read-your-writes, exact two-call buffer behavior, equal-byte Set
  staging, already-absent Clear, and rejection of a second metadata stage;
- exact 129-bit cardinalities;
- explicit immutable/live reader and live writer opens, pending-writer Close as
  abort, empty advanced-operation termination, writer-child `HandleBusy`,
  dropped-unclosed-handle fail-closed behavior, commit nonce, and durability-
  outcome resolution;
- bounded `Reclaim`, including `WorkLimitTooSmall`, automatic publication, and
  no caller-pending maintenance draft;
- mixed Go/Rust subprocess coordination on the same live database in both
  directions, including reader slots, writer exclusion, process tokens,
  reclamation, sidecar replacement, reservation recovery, and SHA-512-bound
  publication/transition resolution;
- orthogonal resolver facts for destination content, later canonical owner,
  live lineage, access state, cleanup, and Windows housekeeping;
- cancellation before work, during bounded processing, immediately before an
  ambiguity boundary, and after that boundary with the factual durability or
  publication outcome preserved;
- compact unsigned snapshots with both destination policies, immutable
  same-path compaction, recovery `FailIfExists`, and no external sorting files
  for ordinary ingestion, transactions, abort, import, or snapshot construction;
- explicit validation modes and reason-coded recovery;
- CreatorOnly artifact creation and exact FreeBSD/Windows namespace behavior on
  their supported test hosts;
- generated ABI-1 C/C++ header and manifest checks plus native C compile/link and
  behavior coverage; and
- strict rejection of every obsolete or malformed format identity.

Golden generation and test commands will be recorded here with the final
implementation. A missing cross-language fixture is a test failure, never a
skip.

Snapshot signing is intentionally absent from the Phase-1 corpus. Pending
SOW-0017 adds authenticated-artifact cases only after the unsigned SDK is
reliable and measured.
