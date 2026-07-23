// Package iprangedb implements the exact unsigned Phase-1 iprange v4 database.
//
// The normative contract is ../../.agents/sow/specs/binary-format-v4.md.
// Physical pages, roots, membership IDs, bitmap words, allocator state, and
// publication machinery remain private; public APIs expose semantic operations.
package iprangedb
