# Windows fold parity — full-rune differential enumeration

Wave 19 round 19.13 (fold parity): the same-source guard's Windows
pathname fold must be byte-identical between the Go and Rust
products on the shipped Windows toolchains.  rustc 1.97.1 (the
Windows Rust product toolchain) applies Unicode-16 lowercase
mappings that the go1.26.5 (Windows Go product toolchain) tables
predate (added in go1.27): U+1C89, U+A7CB, U+A7CC, U+A7CE, U+A7D2,
U+A7D4, U+A7DA, U+A7DC, Garay U+10D50..U+10D65, and Kirat Rai
U+16EA0..U+16EB8.

The Go product maps those runes explicitly in
`v4/go/internal/cli/handlers/export.go` (`windowsFoldPath`); the
three-run release pins are in
`v4/go/internal/cli/handlers/samefile_windows_test.go`
(`TestSameCanonicalWindowsFoldUnicode16`) and
`v4/rust/iprange-cli/src/rpc/handlers/output.rs`
(`same_canonical_folds_windows_unicode16`).

`fold_enum.go` replicates `windowsFoldPath`'s per-rune mapping;
`fold_enum.rs` replicates the Rust fold
(`str::to_lowercase`).  To re-run the differential on the Windows
validation host (go1.26.5 + rustc 1.97.1 are the product
toolchains):

```text
go run fold_enum.go > fold_go.txt
rustc fold_enum.rs -O -o fold_rust.exe && fold_rust.exe > fold_rust.txt
diff <(sed 's/\r$//' fold_go.txt) fold_rust.txt
```

Result at the final fold (2026-09-11, product source revision of
the wave): 0 mismatches across all 1,112,064 valid scalar values.
The pre-fix fold had 55 mismatches (3 pinned in the first release,
52 remaining); the intermediate release that mapped only U+A7CE,
U+A7D2, U+A7D4 still had 52 mismatches.
