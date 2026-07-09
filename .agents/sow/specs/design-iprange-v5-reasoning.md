# v5 Format Reasoning — Superseded by v4.3

**Status**: SUPERSEDED. The v4.3 rebuild (SOW-0014) implemented the structural
changes this document originally discussed. v5 is not needed.

## History

This document originally recommended three format changes for a hypothetical v5:
1. Persisted free-list
2. Optional CRC
3. Append optimization

All three were implemented in v4.2. Then the user gave four new rules (SOW-0014)
that required a deeper rebuild:

- Rule 1: zero heap in the writer hot path → writable MAP_SHARED mmap
- Rule 2: concurrent N readers + 1 writer → no flock LOCK_EX blocking readers
- Rule 3: migration support → external sort + streaming merge
- Rule 4: fixed scope_id record → `[from, to, scope_id:u32]` + scope_mode

These rules invalidated the v4.0–v4.2 format (scope_width, heap dirty pages,
flock mutual exclusion). The result is v4.3 — a breaking format change that
absorbs all the structural improvements.

## v4.3 Performance Characteristics

(To be benchmarked once Phase 3 concurrency is complete.)

The writable-mmap model eliminates:
- Heap dirty-page allocation (4KB per COW page)
- pwrite-at-commit I/O (pages are already in the mmap)
- Buffer pool management

The fixed-record model gives:
- Maximum B+tree density (340 records/leaf for IPv4, constant)
- Single-u32 coalescing comparison (was N-byte comparison)
- No file recreation when feeds grow

## Conclusion

v5 is not needed. The v4.3 format satisfies all four rules. Future work focuses
on the reader-registration companion file (Phase 3) and migration API refinements.
