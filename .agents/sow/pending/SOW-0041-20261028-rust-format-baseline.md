# SOW-0041 - Restore Rust workspace formatting, lint, and documentation baseline

## Status

Status: open

Implementation is not authorized; discovered during SOW-0040 validation.

## Requirements

Restore clean workspace-wide rustfmt, strict Clippy, and warning-denied rustdoc checks without changing behavior or including
unrelated user work. This is independent repository hygiene, not an immutable SDK repair.

## Analysis

`cargo fmt --manifest-path v4/rust/Cargo.toml --all -- --check` on2026-10-28 reports
102 source files, none changed by SOW-0040. Example: iprange-capi/src/tests.rs:203 has
misindented create_live arguments already present in HEAD. SOW-0040 changed Rust files
are formatted explicitly with skip_children=true, preserving unrelated source.

Strict `cargo clippy --workspace --all-features --all-targets -- -D warnings` also
fails on pre-existing build.rs:35 manual_pattern_char_comparison. A complete warning
inventory without -D warnings completes and reports43unmodified source paths; none of
the changed/new immutable construction files has a diagnostic. Reassess these findings
under the supported Rust/MSRV policy before any broad automatic rewrite.

Workspace warning-denied rustdoc also fails on pre-existing CLI legacy/options.rs:100–101:
`[33]` and `[129]` are parsed as unresolved links. SDK-specific documentation is checked
separately by SOW-0040. Preserve the distinction between unrelated baseline failures and
new constructor documentation defects.

## Pre-Implementation Gate

Status: not-ready

Before implementation, inventory formatting, lint, and rustdoc findings, confirm installed tool
versions, the supported MSRV, and baseline, preserve concurrent edits, and obtain scope approval.

## Plan

1. Reproduce and inventory formatting, Clippy, and rustdoc baseline deviations.
2. Propose minimal corrections and obtain approval; preserve behavior and MSRV.
3. Apply approved corrections and review formatting token-equivalence separately from code/doc changes.
4. Verify workspace fmt, strict Clippy, warning-denied rustdoc, and relevant compile/tests; commit explicit approved paths only.

## Validation

Pending implementation approval. No source files changed for this item.

## Artifact impact

This tracking file only. No product contract, format, runtime, or deployment change.

## Outcome

Tracked independently; no implementation or closure claim.
