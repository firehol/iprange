// Package iprangedb implements the exact unsigned Phase-1 iprange v4 database.
//
// The normative contract is ../../.agents/sow/specs/binary-format-v4.md.
// Physical pages, roots, membership IDs, bitmap words, allocator state, and
// publication machinery remain private; public APIs expose semantic operations.
//
// Platform support: the worker-routed validation, recovery-candidate
// inspection, and recovery operations run in the version-matched
// iprange-v4-worker process on linux, darwin, freebsd, and windows for
// amd64 and arm64 (the worker cross-build matrix; a missing or
// incompatible worker fails before source scanning). On a platform
// without a worker build the same operations run on the in-process
// machines; those platforms are outside the tested worker matrix.
package iprangedb
