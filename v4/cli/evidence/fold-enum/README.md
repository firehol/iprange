# Windows fold parity — differential enumerations

Wave 19 round 19.13 (fold parity): the same-source guard's Windows
pathname fold must be byte-identical between the Go and Rust
products on the shipped Windows toolchains.

Two differentials prove it, both run against the actual product
toolchains (go1.26.5, rustc 1.97.1) on the Windows validation host:

1. **Scalar differential** (`fold_enum.go` + `fold_enum.rs`): every
   one of the 1,112,064 valid scalar values folded per rune.
   rustc 1.97.1 applies Unicode-16 lowercase mappings that the
   go1.26.5 tables predate (added in go1.27): U+1C89, U+A7CB,
   U+A7CC, U+A7CE, U+A7D2, U+A7D4, U+A7DA, U+A7DC, Garay
   U+10D50..U+10D65, and Kirat Rai U+16EA0..U+16EB8 — 55 runes
   total.  The Go product maps them explicitly in
   `v4/go/internal/cli/handlers/export.go` (`windowsFoldPath`);
   result: 0 mismatches.
2. **String-context differential** (`fold_string_enum.rs` and the
   committed corpus tests): every scalar in both sigma-neighbor
   contexts around U+03A3 plus fixed boundary spellings
   (7,784,473 chars), folded whole-string.  rustc 1.97.1 applies
   the contextual Final_Sigma rule (`Σ` -> `ς` word-finally, else
   `σ`, skipping Case_Ignorable neighbors and testing the Cased
   property), which a per-rune simple mapping cannot express; the
   Go fold implements the same rule using the Cased and
   Case_Ignorable property tables in
   `v4/go/internal/cli/handlers/unicode_fold_tables.go`.  Result:
   byte-identical output, sha256
   `3cdf661f6772e0ec6875a315d1662232cc1f80f11ab44edc65435f3b9992e4d4`,
   pinned by `TestWindowsFoldCorpusPin` (every platform, real
   production function), `TestWindowsFoldStringContextDifferential`
   (Windows Go test) and
   `windows_fold_string_context_differential` (Windows Rust test).

Property table generation: `unicode_fold_tables.go` is generated
from DerivedCoreProperties.txt **Unicode 17.0.0**
(unicode.org/Public/17.0.0/ucd/DerivedCoreProperties.txt) —
rustc 1.97.1's Cased/Case_Ignorable tables match Unicode 17 (for
example U+0295 lost the Cased property when the pharyngeal
fricative letters moved to U+A7CE/U+A7CF), while its lowercase
tables match Unicode 16.  Re-generate with:

```text
python3 - <<'PY'
def parse(path, prop):
    out = []
    for line in open(path, encoding="utf-8"):
        line = line.split("#")[0].strip()
        if not line: continue
        p = [x.strip() for x in line.split(";")]
        rng = p[0]
        if p[1] != prop: continue
        lo, hi = [int(x, 16) for x in rng.split("..")] if ".." in rng else (int(rng, 16), int(rng, 16))
        out.append((lo, hi))
    return out
# emit sorted []struct{lo,hi rune} tables for Cased and
# Case_Ignorable with the isCased/isCaseIgnorable helpers
PY
```

To re-run the scalar differential on the Windows validation host
(go1.26.5 + rustc 1.97.1 are the product toolchains):

```text
go run fold_enum.go > fold_go.txt
rustc fold_enum.rs -O -o fold_rust.exe && fold_rust.exe > fold_rust.txt
diff <(sed 's/\r$//' fold_go.txt) fold_rust.txt
```

To re-run the string-context differential:

```text
rustc fold_string_enum.rs -O -o fold_string_enum.exe
fold_string_enum.exe        # writes corpus_lower.rs.txt
# fold the same corpus through the real Go production fold (the
# committed corpus tests do this on Windows) and compare sha256
```

History: the pre-fix fold had 55 scalar mismatches (3 pinned in
the first release, 52 remaining); the release that mapped only
U+A7CE/U+A7D2/U+A7D4 still had 52; the complete scalar map reached
0; the Final_Sigma string-context differential closed the last
divergent class.
