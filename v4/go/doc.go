// Package iprangedb implements the exact unsigned Phase-1 iprange v4 database.
//
// The normative contract is ../../.agents/sow/specs/binary-format-v4.md.
// Physical pages, roots, membership IDs, bitmap words, allocator state, and
// publication machinery remain private; public APIs expose semantic operations.
//
// Platform support: validation, recovery-candidate inspection, and
// recovery run in the version-matched iprange-v4-worker process on
// linux, darwin, freebsd, and windows for amd64 and arm64 (the worker
// cross-build matrix; a missing or incompatible worker fails before
// source scanning). On any other platform there is no worker build, so
// the same operations refuse with ErrorOSUnsupported before any source
// scan or destination mutation (binary-format-v4.md section 19);
// in-process execution of faultable scans is never used.
package iprangedb
