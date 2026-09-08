# SOW-0028 delivery step 5 (milestone 4) — qualification evidence

The current evidence is regenerated after the wave-19.4 role-round
repairs (the file-identity guard, admission-error identity, and
canonicalization parity, SOW-0028 "Wave 19 round 19.4"): the record
HEAD is the wave's closing evidence commit after `73b5fcdd`; the
product revision is `8c0b3983` for Go (rebuilt with
`-buildvcs=false`) and `73b5fcdd` for Rust (the SDK-hosted identity
helper), with the Linux toolchain go1.27.0 / rustc 1.91.1 stable.

Re-qualification at the wave-19.4 Linux identities: matrices rust
38/38, go 38/38, rust_to_go 14 PASS + 24 legitimate skips,
go_to_rust 14 PASS + 24 skips; crash positive 16/16 both directions
and the /bin/false negative control fails as designed (rc 1);
resource proofs 8/8; kind-coverage gate PASS with fresh evidence and
all self-test controls; golden exchanges 55 / 38 case files;
sensitivity gate 14/14.  The operations wave-19 probe is 34/34 OK
against these binaries (renamed-source refusal, 70k-member error
identity with truncation marker, decorated-spelling parity,
oversized-frame close path).  Windows housekeeping re-qualified
natively on the authorized Windows validation host at `73b5fcdd`
(go1.26.5 windows/amd64, rustc 1.97.1, native Windows Python 3.14.6,
clean tree): 2/2 PASS (`windows-housekeeping.json`, schema v3).

Linux reports record the product identities `a320028a...` (go) and
`aff80842...` (rust), workers `4f2eb063...` (go) / `f80043e6...`
(rust), fixture `cd84271c...` (all staged in
`.local/shared/binaries/SHASUMS.txt`, sha256sum -c OK).  The Windows
housekeeping report records the Windows-host products `aec92202...`
(go) and `b595b97e...` (rust); the Windows Go worker is
`1ec4d089...` (build provenance `73b5fcdd`, tree_clean).

---

Historical wave record (superseded by the head block): the current evidence is regenerated at product revision `016010fc`
(the round-10 repair wave: destination name-rule gates and the
verbatim push fold).  The round-9 revision was re-anchored through
all seven role reviews; the portability role then FAILed with one
P1 and one P2, both independently verified and repaired:

- P1 — `rejectLiveSelf` dropped the `ValidDestinationName` gate in
  the round-9 wave (to probe the bound main-name spelling) and an
  overlong destination name surfaced the kernel error of the
  main-name Lstat as `io` (wire `code": "io"`) where Rust
  `Destination::bind` answers `name_invalid` via
  `require_name_lengths` before `open_regular`.  Repair: the
  preflight mirrors the Rust bind error order — the main-name
  component rule answers `name_invalid` before any parent access,
  the length rule answers `name_invalid` after the parent open and
  before the main-name open, and a missing parent still wins the
  class when both fail.  Pinned by `TestRejectLiveSelfNameRules`;
  live-probed: both products now answer `name_invalid`
  byte-identically for the 300-byte destination.
- P2 — `verbatimPushRebuild` appended the pushed name raw instead
  of folding its components like `PathBuf::_push`'s verbatim
  branch: a trailing separator stayed in the result, `"."` added a
  CurDir component, `".."` appended instead of popping the last
  Normal component, and `"a/b"` stayed one component.  Repair:
  CurDir vanishes, ParentDir pops the last Normal, RootDir
  truncates the buffer to its prefix, and the re-emission applies
  the disk-prefix need_sep rule; pinned by
  `TestVerbatimPushComponentRules` (37 native-rustc-derived rows,
  windows-gated) and the corrected wine oracle differential:
  WINDOWS 324/324 and POSIX 431/431, zero mismatches.

No current product call site passes a `.`/`..`/separator-carrying
name to the verbatim push, so the P2 class was latent; the P1 class
was reachable at the JSON-RPC snapshot surface and is now at parity.

Re-qualification at the new identities: Go suite 23/23 packages
PASS on Linux (go1.26.4 and host go1.27.0) and natively on the
Windows host (go1.26.5, 23/23 including the 201-row golden, the
push tests, and the new verbatim push fold test); Rust workspace
PASS on Linux (rustc 1.97.1, no Rust product source change).  The
full battery PASSes at the final staged identities: matrices 38/38
single and 14 PASS + 24 legitimate skips per mixed direction;
crash 16/16 both directions; the negative control 0/16; resource
proofs 8/8; kind-coverage gate PASS with all 46 self-test
controls; golden exchanges 55; sensitivity gate 14.  Windows
housekeeping re-qualified at `016010fc`: 2/2 PASS with native
Windows Python 3.14.6 (provenance: clean tree, go1.26.5
windows/amd64, rustc 1.97.1).

Linux reports record the product identities `07c4e314...` (rust,
unchanged since the round-4 qualified build) and `eab62a09...`
(go, rebuilt with `-buildvcs=false`), workers `77b6d086...`
(rust) / `2148bc0e...` (go), fixture `df3623a6...` (all staged in
`.local/shared/binaries/SHASUMS.txt` with sha256sum -c OK).  The
Windows housekeeping report records the Windows-host products
`c960a64f...` (rust) and `64854dfa...` (go); the Windows Go worker
is `06128e96...`.

---

Historical wave record (superseded by the head block): the current evidence is regenerated at product revision `01356600`
(the external-control repair wave; qualification HEAD `bfc60f96`
adds one test-only raw-parent helper repair).  The wave repairs
four Go/Rust divergence classes and two test-tripwire defects found
by the external whole-milestone control:

- Windows prefix parsing: `parsePrefix` normalizes the eight-byte
  prefix header (`/` -> `\`) before matching the verbatim UNC
  marker, so `\\?\\UNC/server/share` is VerbatimUNC with no name
  exactly like Rust `PrefixParser::get_prefix`; `parseUNC` no
  longer absorbs the share's trailing separator, so
  `\\server\share\\leaf` keeps one separator in derived parent
  and sidecar spellings.  `FileName` is rewritten as an
  allocation-free backward component walk with identical
  semantics (0 allocs/op on the probed corpus; the 8-byte
  verbatim-header normalization allocates once per call only
  for `\\?\\`-prefixed spellings).  The Windows golden corpus
  grows to 201 rows with five forward-slash UNC/verbatim-UNC
  shapes, every row confirmed against native windows-host rustc
  1.97.1.
- snapshot live-self rejection: `rejectLiveSelf` probes the bound
  destination spelling (main name + live parent), so valid
  `main/` and `main/.` destination spellings no longer fail
  ENOTDIR against the raw destination path; pinned by
  `reject_live_self_test.go`.
- Rust thread-creation tripwire: the scanner consumes its seeded
  brace once and now asserts the watchdog spawn marker
  (`iprange-signal-diag`) lies inside the checked region and
  reports checked/skipped line counts; the negative control
  (spawn injected at the watchdog) fails as designed.
- destination-preflight tests: accept shapes are delivered
  verbatim through raw (uncleaned) parents, so trailing-dot and
  mid-path `..` shapes reach the handlers; the raw-parent helper
  anchors at the volume/root and is Windows-correct.

Re-qualification at the new identities: Go suite 23/23 packages
PASS on Linux (go1.26.4 and host go1.27.0) and natively on the
Windows host (go1.26.5, 23/23 including the 201-row golden and the
push tests); Rust workspace PASS on Linux (rustc 1.97.1, no Rust
product source change).  The full battery PASSes at the final
staged identities: matrices 38/38 single and 14 PASS + 24
legitimate skips per mixed direction; crash 16/16 both directions;
the negative control 0/16; resource proofs 8/8; kind-coverage gate
PASS with all 46 self-test controls; golden exchanges 55;
sensitivity gate 14.  Windows housekeeping re-qualified at
`bfc60f96`: 2/2 PASS with native Windows Python 3.14.6
(provenance: clean tree, go1.26.5 windows/amd64, rustc 1.97.1).
The wine differential against the Rust-1.97.1-derived Python
oracle passes 296/296 Windows and 417/417 POSIX shapes with the
corrected prefix-header normalization.

Linux reports record the product identities `07c4e314...` (rust,
unchanged since the round-4 qualified build) and `23e4730a...`
(go, rebuilt with `-buildvcs=false`), workers `77b6d086...`
(rust) / `d83854dc...` (go), fixture `df3623a6...` (all staged in
`.local/shared/binaries/SHASUMS.txt` with sha256sum -c OK).  The
Windows housekeeping report records the Windows-host products
`c960a64f...` (rust) and `37a3e563...` (go); the Windows Go worker
is `c92b804b...`.

---

Historical wave record (superseded by the head block): the current evidence is regenerated at product revision `e54015d1`
(the round-6 Rust push join repair).  The re-anchored parity and
tester reviews closed the last two path-spelling divergences:
`expandPaths` and the export/metadata/removals temporary placement
built joins with an unconditional separator, while Rust
`PathBuf::push` inserts a separator only when the base does not
already end with one and never after a bare drive prefix, so
`"@<dir>/"` expanded to a doubled separator and a drive-relative
temporary (`C:` parent) was placed at the volume root instead of the
drive-relative name.  The repair at `e54015d1` adds one authoritative
`pathname.Push` mirror (bare-drive rule plus the verbatim `_push`
rebuild) and routes the four join sites through it; the separator
rule is pinned by `TestPushSeparatorRules` (cross-platform) and
`TestPushWindows` (native Windows), and the trailing-separator
`@`-spellings remain pinned by
`TestScratchAtExpansionTrailingSeparator`.  The round-5
physical-root repair (a69eb53d, forward slash after a verbatim
prefix) carries unchanged.

The Go suite is green on the qualified go1.26.4 and on the host
go1.27.0 (23/23 packages on each), and natively on the Windows host
(go1.26.5, 23/23 packages including the 196-row golden and the new
push tests); the Rust workspace suites are green on Linux (rustc
1.97.1) and natively on Windows (without source change).  The full
battery PASSes at the final staged identities: matrices 38/38 single
and 14 PASS + 24 legitimate skips per mixed direction; crash 16/16
both directions; the negative control 0/16 (8 real-producer
scenarios failing at the substituted-consumer stage and 8
substituted-producer scenarios failing during setup); resource
proofs 8/8; kind-coverage gate PASS with all 46 self-test controls;
golden exchanges 55; sensitivity gate 14.  Windows housekeeping
re-qualified at `e54015d1` on the authorized Windows validation
host: 2/2 PASS with native Windows Python 3.14.6 (provenance: clean
tree, go1.26.5 windows/amd64, rustc 1.97.1).  The `@`-expansion,
raw-path, and verbatim parity probes remain green on both products.

Linux reports at `e54015d1` record the product identities
`07c4e314…` (rust, unchanged since the round-4 qualified build) and
`b4aedb1c…` (go, rebuilt with `-buildvcs=false`), workers
`77b6d086…` (rust) / `11001b94…` (go), fixture `df3623a6…` (all
staged in `.local/shared/binaries/SHASUMS.txt` with sha256sum -c
OK).  The Windows housekeeping report records the Windows-host
products `c960a64f…` (rust) and `3ec21b97…` (go); the Windows Go
worker is `71229191…`.

---

Historical wave record (superseded by the head block): evidence was regenerated at product revision `8061d80d`
(the round-6 @-expansion separator repair).  The re-anchored parity
review found `expandPaths` concatenated the entry separator
unconditionally, so a trailing-separator referenced spelling
(`"@<dir>/"`) expanded to a doubled separator (`"<dir>//01.txt"`)
while Rust `read_dir entry.path()` (PathBuf push) inserts a separator
only when the base does not already end with one.  The repair at
`8061d80d` inserts the separator only when the last byte of the
referenced spelling is not a path separator and pins both spellings
with `TestScratchAtExpansionTrailingSeparator`; the round-5
physical-root repair (a69eb53d, forward slash after a verbatim
prefix) carries unchanged.

The Go suite is green on the qualified go1.26.4 and on the host
go1.27.0 (23/23 packages on each), and natively on the Windows host
(go1.26.5, 23/23 packages including the 196-row golden); the Rust
workspace suites are green on Linux (rustc 1.97.1) and natively on
Windows (without source change).  The full battery PASSes at the
final staged identities: matrices 38/38 single and 14 PASS + 24
legitimate skips per mixed direction; crash 16/16 both directions;
the negative control 0/16 (8 real-producer scenarios failing at the
substituted-consumer stage and 8 substituted-producer scenarios
failing during setup); resource proofs 8/8; kind-coverage gate PASS
with all 46 self-test controls; golden exchanges 55; sensitivity gate
14.  Windows housekeeping re-qualified at `8061d80d` on the
authorized Windows validation host: 2/2 PASS with native Windows
Python 3.14.6 (provenance: clean tree, go1.26.5 windows/amd64,
rustc 1.97.1).  The `@`-expansion, raw-path, and verbatim-UNC
parity probes remain green on both products.

Linux reports at `8061d80d` record the product identities
`07c4e314…` (rust, unchanged since the round-4 qualified build) and
`5383917e…` (go, rebuilt with `-buildvcs=false`), workers
`77b6d086…` (rust) / `3bb3180c…` (go), fixture `df3623a6…` (all
staged in `.local/shared/binaries/SHASUMS.txt` with sha256sum -c
OK).  The Windows housekeeping report records the Windows-host
products `c960a64f…` (rust) and `d013e0b5…` (go); the Windows Go
worker is `a91e548b…`.

---

Historical wave record (superseded by the head block): evidence was regenerated at product revision `a69eb53d`
(the round-6 forward-slash-after-verbatim-prefix repair).  The
re-anchored whole-milestone review found the Windows pathname port
checked the physical root with the verbatim-aware separator set: a
hand-built long-path spelling with a forward slash directly after a
verbatim prefix (`\\?\\C:/x`) was parsed with the slash inside the
first body component, so Go produced file names with a leading slash,
accepted `\\?\\C:/` with a name, and kept the slash in
`with_file_name` results, while Rust `has_physical_root` uses the
static separator set (body components still split on the verbatim
backslash only).  The repair at `a69eb53d` computes the physical root
with the static separator, consumes the root byte in `FileName` after
a verbatim prefix, keeps a trailing verbatim `"."` as a CurDir
component (no file name, like Rust), and mirrors the `PathBuf::_push`
verbatim rebuild in `WithFileName` (the root byte is re-emitted as the
main separator and the prefix raw bytes keep their parsed spelling),
replacing the share-less `\\?\\UNC\\` special case; the Windows golden
corpus grew to 196 rows with forward-slash-after-prefix and
trailing-dot verbatim shapes pinned against a native Windows rustc
1.97.1 probe (the previous 183-row corpus had no shape with a
separator after a verbatim prefix).

The Go suite is green on the qualified go1.26.4 and on the host
go1.27.0 (23/23 packages on each), and natively on the Windows host
(go1.26.5, 23/23 packages including the extended golden); the Rust
workspace suites are green on Linux (rustc 1.97.1) and natively on
Windows (without source change).  The full battery PASSes at the
final staged identities: matrices 38/38 single and 14 PASS + 24
legitimate skips per mixed direction; crash 16/16 both directions;
the negative control 0/16 (8 real-producer scenarios failing at the
substituted-consumer stage and 8 substituted-producer scenarios
failing during setup); resource proofs 8/8; kind-coverage gate PASS
with all 46 self-test controls; golden exchanges 55; sensitivity gate
14.  Windows housekeeping re-qualified at `a69eb53d` on the
authorized Windows validation host: 2/2 PASS with native Windows
Python 3.14.6 (provenance: clean tree, go1.26.5 windows/amd64,
rustc 1.97.1).  The `@`-expansion, raw-path, and verbatim-UNC
parity probes remain green on both products.

Linux reports at `a69eb53d` record the product identities
`07c4e314…` (rust, unchanged since the round-4 qualified build) and
`1ed287a8…` (go, rebuilt with `-buildvcs=false`), workers
`77b6d086…` (rust) / `3bb3180c…` (go), fixture `df3623a6…` (all
staged in `.local/shared/binaries/SHASUMS.txt` with sha256sum -c
OK).  The Windows housekeeping report records the Windows-host
products `c960a64f…` (rust) and `5018f974…` (go); the Windows Go
worker is `a91e548b…`.

---

Historical wave record (superseded by the head block): evidence was regenerated at product revision `2c5d668b`
(the round-6 @-directory expansion repair).  The re-anchored
whole-milestone review found the last remaining lexical
normalization of a caller-supplied path: `input.go` built each
`@`-directory entry path with `filepath.Join`, which lexically
cleaned a symlinked intermediate plus `".."` inside the caller's
referenced spelling, so Go refused the kernel-resolved directory (or
ingested a different one) while Rust's `read_dir entry.path()` keeps
the raw spelling.  The repair at `2c5d668b` builds the entry path by
raw concatenation and pins the class with a POSIX-gated `@`-expansion
regression test; a live dual-product probe proves both products
publish through `@<dir>/<symlink>/../<realdir>` with the same
`published` outcome.

The Go suite is green on the qualified go1.26.4 and on the host
go1.27.0 (23/23 packages on each), and natively on the Windows host
(go1.26.5, 23/23 packages); the Rust workspace suites are green on
Linux (rustc 1.97.1) and natively on Windows (without source
change).  The full battery PASSes at the final staged identities:
matrices 38/38 single and 14 PASS + 24 legitimate skips per mixed
direction; crash 16/16 both directions; the negative control 0/16
(8 real-producer scenarios failing at the substituted-consumer
stage and 8 substituted-producer scenarios failing during setup);
resource proofs 8/8; kind-coverage gate PASS with all 46 self-test
controls; golden corpus 55; sensitivity gate 14.  Windows
housekeeping re-qualified at `2c5d668b` on the authorized Windows
validation host: 2/2 PASS with native Windows Python 3.14.6
(provenance: clean tree, go1.26.5 windows/amd64, rustc 1.97.1).

Linux reports at `2c5d668b` record the product identities
`07c4e314…` (rust, unchanged since the round-4 qualified build) and
`78cbd4c3…` (go, rebuilt with `-buildvcs=false`), workers
`77b6d086…` (rust) / `114a7018…` (go), fixture `df3623a6…` (all
staged in `.local/shared/binaries/SHASUMS.txt` with sha256sum -c
OK).  The Windows housekeeping report records the Windows-host
products `c960a64f…` (rust) and `3b967437…` (go); the Windows Go
worker is `8338b58d…`.

---


Historical wave record (superseded by the head block): evidence was regenerated at product revision `03b7d4ab`
(the round-6 verbatim-UNC repair).  The re-anchored round-6
performance review found a P1 in the Windows pathname port: the
verbatim-UNC branch re-parsed the path as a plain UNC whenever the
share was the final component, so `FileName("\\?\\UNC\\srv\\sh")`
returned the share as a file name and `WithFileName` dropped the share
from derived sidecar paths, while Rust 1.97.1 keeps
`VerbatimUNC(server, share)` unconditionally and a share-terminal path
has no file name or parent.  The repair at `03b7d4ab` mirrors Rust
`Prefix::len` for VerbatimUNC (the share separator counts only with a
share) and the verbatim push rebuild (the share-less `\\?\\UNC\\`
prefix doubles the separator), and extends the Windows golden corpus
with the share-terminal, prefix-terminal, trailing-separator, and
plain-UNC share shapes (12 corpus rows) pinned against a native
Windows rustc 1.97.1 probe (the corpus previously contained no
share-terminal shape, so no committed gate could detect the class).

The Go suite is green on the qualified go1.26.4 and on the host
go1.27.0 (23/23 packages on each), and natively on the Windows host
(go1.26.5, 23/23 packages including the extended golden); the Rust
workspace suites are green on Linux (rustc 1.97.1) and natively on
Windows (without source change).  The full battery PASSes at the
final staged identities: matrices 38/38 single and 14 PASS + 24
legitimate skips per mixed direction; crash 16/16 both directions;
the negative control 0/16 (8 real-producer scenarios failing at the
substituted-consumer stage and 8 substituted-producer scenarios
failing during setup); resource proofs 8/8; kind-coverage gate PASS
with all 46 self-test controls; golden corpus 55; sensitivity gate
14.  Windows housekeeping re-qualified at `03b7d4ab` on the
authorized Windows validation host: 2/2 PASS with native Windows
Python 3.14.6 (provenance: clean tree, go1.26.5 windows/amd64,
rustc 1.97.1).  The symlink+`..` raw-path parity probe from the
prior repair remains green on both products.

Linux reports at `03b7d4ab` record the product identities
`07c4e314…` (rust, unchanged since the round-4 qualified build) and
`85b71310…` (go, rebuilt with `-buildvcs=false`), workers
`77b6d086…` (rust) / `114a7018…` (go), fixture `df3623a6…` (all
staged in `.local/shared/binaries/SHASUMS.txt` with sha256sum -c
OK).  The Windows housekeeping report records the Windows-host
products `c960a64f…` (rust) and `38417180…` (go); the Windows Go
worker is `8338b58d…`.

---


Historical wave record (superseded by the head block): evidence was regenerated at product revision `ae57845e`
(the round-6 raw-path repair).  The re-anchored round-6 tester review
at `5a008411` found a wire-reachable P1: the immutable reader open
still normalized the caller's raw path with `filepath.Clean`
(`v4/go/internal/mapping/mapping.go` `openMapping`), so a database
addressed as `<dir>/<symlink>/../<name>` was refused by Go while the
Rust twin serves it at the kernel-resolved location.  The repair at
`abfc2696` removes every lexical normalization of caller-supplied
paths on the open, verify, create, sidecar, snapshot preflight,
export, removal, and metadata publication paths: raw spellings flow
to the kernel exactly like the Rust `std::path` derivations, and only
os-resolved executable-discovery paths keep `filepath` helpers.  New
regression tests open a fixture through a symlinked intermediate plus
`".."` in the immutable reader and the validation source and assert
the cleaned spelling is refused (POSIX kernel-resolution class; the
tests skip on Windows, where reparse-point semantics apply to both
products equally because both pass the identical raw spelling).  A
live probe against the rebuilt products proves both products open,
describe, lookup, and close the same database through the raw path
and both refuse the cleaned spelling.

The Go suite is green on the qualified go1.26.4 and on the host
go1.27.0 (23/23 packages on each), and natively on the Windows host
(go1.26.5, 23/23 packages); the Rust workspace suites are green on
Linux (rustc 1.97.1) and natively on Windows (without source
change).  The full battery PASSes at the final staged identities:
matrices 38/38 single and 14 PASS + 24 legitimate skips per mixed
direction; crash 16/16 both directions; the negative control 0/16
(8 real-producer scenarios failing at the substituted-consumer
stage and 8 substituted-producer scenarios failing during setup);
resource proofs 8/8; kind-coverage gate PASS with all 46 self-test
controls; golden corpus 55; sensitivity gate 14.  Windows
housekeeping re-qualified at `ae57845e` on the authorized Windows
validation host: 2/2 PASS with native Windows Python 3.14.6
(provenance: clean tree, go1.26.5 windows/amd64, rustc 1.97.1).
The records commit `ae57845e` changes tests only; the product
binaries reproduce byte-identically from `abfc2696`.

Linux reports at `ae57845e` record the product identities
`07c4e314…` (rust, unchanged since the round-4 qualified build) and
`a00b1307…` (go, rebuilt with `-buildvcs=false`), workers
`77b6d086…` (rust) / `2d748be0…` (go), fixture `df3623a6…` (all
staged in `.local/shared/binaries/SHASUMS.txt` with sha256sum -c
OK).  The Windows housekeeping report records the Windows-host
products `c960a64f…` (rust) and `2f5b5fac…` (go); the Windows Go
worker is `b0bb2a2b…`.

---


Historical wave record (superseded by the head block): evidence was regenerated at product revision `06495eeb`
(the round-6 follow-up that charges the cross-toolchain deflate
workspace honestly).  The round-6 re-anchored portability review at
`5a008411` measured the Go `compress/flate` DefaultCompression
workspace at ~1.06 MiB on go1.27.0, above the declared 840 KiB
`deflateHeapOverhead` charged to metadata callers
(`v4/go/internal/writer/metadata.go`); the follow-up raises the
charge to 1150 KiB and pins the deflate branch of the small-payload
test with a budget that admits it under the larger charge.  The Go
suite is green on the qualified go1.26.4 and on the host go1.27.0
(23/23 packages on each), and natively on the Windows host
(go1.26.5, 23/23 packages); the Rust workspace suites are green on
Linux (rustc 1.97.1) and natively on Windows (without source
change).  The full battery PASSes at the final staged identities:
matrices 38/38 single and 14 PASS + 24 legitimate skips per mixed
direction; crash 16/16 both directions; the negative control 0/16
(8 real-producer scenarios failing at the substituted-consumer
stage and 8 substituted-producer scenarios failing during setup);
resource proofs 8/8; kind-coverage gate PASS with all 46 self-test
controls; golden corpus 55; sensitivity gate 14.  Windows
housekeeping re-qualified at `06495eeb` on the authorized Windows
validation host: 2/2 PASS with native Windows Python 3.14.6
(provenance: clean tree, go1.26.5 windows/amd64, rustc 1.97.1).

Linux reports at `06495eeb` record the product identities
`07c4e314…` (rust, unchanged since the round-4 qualified build) and
`7544ffc2…` (go, rebuilt with `-buildvcs=false`), workers
`77b6d086…` (rust) / `4c8f50fa…` (go), fixture `df3623a6…` (all
staged in `.local/shared/binaries/SHASUMS.txt` with sha256sum -c
OK).  The Windows housekeeping report records the Windows-host
products `c960a64f…` (rust) and `a20bcb2d…` (go); the Windows Go
worker is `d1273d04…`.

---


The round-6 evidence is regenerated at the round-6 final product
revision `ebbd0419`.  The external round-5 control review found the
Go publish destination preflight (`requirePublicationParent`) still
using raw `filepath.Base`: it rejected a trailing-dot destination
(`"<parent>/name/."`) that Rust `Path::file_name` accepts and
resolved a trailing `..` onto an ancestor that Rust refuses.  The
round-6 repair replaces every Go name/parent derivation that
mirrors a Rust twin with one shared implementation, the new
`internal/pathname` package that ports the Rust 1.97.1
`std::path` component state machine (`Path::file_name`,
`Path::parent`, `PathBuf::with_file_name`): raw paths are not
normalized, "." components are dropped, ".." is an ordinary
component that is never resolved, repeated and trailing separators
collapse, and the Windows volume prefix is not a name or a parent.
The port is pinned by golden differential tests against rustc
output (162 unix shapes generated natively on Linux, and 171
Windows shapes generated on the authorized Windows validation
host, which added the drive-relative, UNC, verbatim, and device
namespace classes).  Wired through every derivation site: the
publish/lifecycle destination preflights, the publication
destination binding, the live namespace bindPath/bindPair/
parent-identity/sync-parent and all of their callers, the
canonical sidecar and transition-temp derivation, the immutable
reader namespace checks and sidecar path, the recovery
basic/offline source openings, and the worker/system deps
discovery.  The native Windows suite then caught two remaining
parity gaps in the first port (Go filepath.VolumeName classified
`//a//b` as a UNC prefix although Rust requires a non-empty share,
and the CurDir yield after a drive prefix was permitted although
Rust's else-if chain never reaches it — `"C:."` has no file name
and no parent); classifyPrefix now ports
`sys/path/windows_prefix.rs parse_prefix` directly (Disk, UNC,
Verbatim, VerbatimDisk, VerbatimUNC, DeviceNS with their exact
byte lengths).  The live products now accept `"<parent>/name/."`
and refuse `".../.."` with identical error classes and messages
(probe-verified against the rebuilt Go and carried Rust binaries).

The round-6 repair wave was qualified at `ebbd0419` (product
source):

- the Go suite is green on Linux with the qualified go1.26.4
  (23/23 packages including the new pathname package) and natively
  on the Windows host (go1.26.5, 23/23 packages); the Rust
  workspace suites are green on Linux (rustc 1.97.1) and natively
  on Windows;
- the full battery PASSes at the final staged identities:
  matrices 38/38 single and 14 PASS + 24 legitimate skips per
  mixed direction; crash 16/16 both directions; the negative
  control 0/16 (8 real-producer scenarios failing at the
  substituted-consumer stage and 8 substituted-producer scenarios
  failing during setup); resource proofs 8/8; kind-coverage gate
  PASS with all 46 self-test controls; golden corpus 55;
  sensitivity gate 14;
- Windows housekeeping re-qualified at source revision
  `ebbd0419` on the authorized Windows validation host: 2/2 PASS
  with the native Windows Python 3.14.6 (Go `2156394a…`, Rust
  `c960a64f…` — the Rust product is unchanged since the round-4
  qualified build at `ed29e437`; provenance recorded: go1.26.5
  windows/amd64, rustc 1.97.1, clean tree), and the full Go suite
  passes natively there (23/23 packages) alongside the green
  native Rust suite.

Linux reports record the product identities `07c4e314…` (rust,
unchanged since the round-4 qualified build) and `34548919…` (go,
rebuilt with `-buildvcs=false` at the round-6 repair revision
`ebbd0419`), workers `77b6d086…` (rust) / `a7c9225a…` (go),
fixture `df3623a6…` (all staged in
`.local/shared/binaries/SHASUMS.txt` with sha256sum -c OK; the Go
worker was rebuilt with the qualified go1.26.4 because the worker
links the pathname helpers); the Windows housekeeping report
records the Windows-host products `c960a64f…` (rust) and
`2156394a…` (go) at source revision `ebbd0419`.

The fourteenth-wave (external whole-milestone control review
FAIL and repair) evidence is regenerated at the wave-14 revision
`e272c990` after the external control review of the wave-13
revision.  The reviewed findings and their repairs:

- held-open over-limit frames: a peer that wrote exactly LIMIT+1
  bytes whose last byte was not the CR of a CRLF terminator held
  both readers waiting for another byte forever (stdin open, no
  -32001, no exit).  The one-extra-byte allowance now applies only
  when the last byte is CR; any other LIMIT+1-th byte is already
  over the ceiling and is reported immediately (both products;
  held-open and CR-tail boundary tests in both languages);
- Windows basename round-trip: `LocalBasename` on Windows stores
  UTF-16LE units (encoding 2), but the Rust wire rendering passed
  the raw units through UTF-8 lossy and emitted NUL-interleaved
  mojibake that the resolvers reject.  `create_result`,
  `commit_cleanup_artifact`, and the live transition now render
  `LocalBasename` through an encoding-aware decoder (encoding 2 ->
  UTF-16LE text, else UTF-8 lossy); the Go product always stores
  encoding 1 and was unaffected.  The Windows report re-qualified
  at the new revision and now records a clean `main_basename` from
  both products;
- watchdog coverage: the full-stderr signal tests signalled an idle
  session and only exercised the graceful exit; they now wedge the
  session so the process-lifetime watchdog's detached-diagnostic
  path is genuinely exercised (Go helper mode + Rust wedged spawn);
  the Rust full-stderr fixture also wrote 4096 bytes from a
  one-byte buffer and treated any error as full-pipe proof and now
  fills from a real buffer requiring EAGAIN;
- proof and gate hardening: the resource proofs a/d drain stdout
  after the expected responses and require zero trailing bytes (a
  stray duplicate or malformed frame fails; new self-test control),
  and the kind gate rejects fixture identities recorded without a
  sha256 or contradicting an earlier identity for the same path;
- test-race repair: the Go signal tests no longer run `Wait`
  concurrently with `StdoutPipe` reads, and the full-stderr tests no
  longer close raw pipe fds behind an `*os.File` whose finalizer can
  then close a reused fd number (the pre-existing EBADF flakes
  across the suite under load).

Linux reports record the product identities `6ab63dfd…` (rust) and
`d228ebe5…` (go); the Windows housekeeping report is regenerated at
the same product source revision (recorded below).

Produced 2026-09-06 on the Linux workstation (x86_64) from staged
binary copies (no personal paths in the committed evidence) plus the
Windows-host housekeeping proof on the authorized Windows validation
host (access authorized for SOW-0028 qualification only).  The
tenth-wave evidence is regenerated at the wave-10 revision: the
termination-signal contract (a wedged transport can no longer ignore
SIGINT/SIGTERM; watchdog force-exit plus signal-wins-over-EOF),
spec amendments for the queue-bound distribution and the
id-less-invalid-notification response, kind-gate capability
enforcement for every kind (create and open, crash and matrix sides)
plus the per-case argv execution anchor, deadline-bounded client I/O
in the crash harness and conformance runner, and the golden corpus
now pins the invalid-notification error exchange.  The ninth-wave
repairs (broken-stdout shutdown deadlock, proof-b gaps, the shared
client deadline bypass, abbreviated-command and ordinal-attribution
gate bypasses, Windows `source_basename`) remain included; the full
finding lists are recorded in SOW-0028's ninth- and tenth-wave
sections.

- Rust product + worker + fixture tool: clean-release builds of
  `v4/rust/target/release/{iprange,iprange-v4-worker,examples/v4-fixture}`
  (rustc 1.97.1) staged as
  `/tmp/qualsvc/ev21/bin/rust/{iprange,iprange-v4-worker,v4-fixture}`:
  - product SHA-256
    `6ab63dfd72f498a4d2a41b856e2d94750adaa6598c16968c2489fcbe75ff8d93`
    (wave-14 build: the immediate held-over-limit report in
    `framing.rs`, the encoding-aware `LocalBasename` rendering in
    `handlers/lifecycle.rs` and `handlers/live.rs`, plus all earlier
    wave fixes);
  - worker SHA-256
    `cb9ad6cd82a03b7933d706de9e1b4e4c707836962b7f00e194c5d50cd4511e94`
    (unchanged; build-proven identity, not pinned by the committed
    reports);
  - fixture SHA-256
    `d615488f038fa59deea87e0ce3340b780380fe0f2122e8e1ad65edeb25d861f1`
    (source unchanged since the fixture-qualification wave; the
    identity is a fresh canonical release build at the wave-14
    revision with the recorded command - the earlier recorded
    identity predated the current release toolchain).
- Go product + worker: the documented `-buildvcs=false` qualification
  pair (go1.26 linux/amd64, no embedded vcs revision) staged as
  `/tmp/qualsvc/ev21/bin/go/{iprange,iprange-v4-worker}`:
  - product SHA-256
    `d228ebe5c024e9f8dc8cccfc31295f73f0ddf87a83fb2ee4c01a52336ef3467d`
    (wave-14 build: the immediate held-over-limit report in
    `framing.go`, plus all earlier wave fixes; the worker tree is
    unchanged since the eighth wave);
  - worker SHA-256
    `202a83ac92f5c8b85b44068a1553aef0dbf25a81fb2d888022592292d03b6141`
    (the worker source tree is unchanged since the eighth wave and
    the worker does not link `v4/go/internal/cli/rpc`; the identity
    is build-proven at this revision with the recorded command, not
    byte-reproducible across toolchains or build paths, and not
    pinned by the committed reports).
- Windows qualification binaries (built on the authorized Windows
  validation host at the wave-14 product-source revision
  `e272c990`, clean tracked tree, staged under
  `C:/Temp/qualsvc-win/ev20/`):
  - Go product SHA-256
    `fb6b503a46d151e0b52498e72041602782032b2a3e055f1284e8605a3b4318e5`
    (go1.26.5 windows/amd64, `-buildvcs=false`);
  - Rust product SHA-256
    `a6ec5b45427fe7e3af9cf27d567ad9aa7f09e65a45faa1cbb94985d915befc48`
    (rustc 1.97.1; carries the encoding-aware basename rendering and
    the immediate over-limit report, so the recorded Windows flow
    `main_basename` is now clean text from both products).
  Build commands, toolchain, and source revision are recorded in
  the report's `build_provenance` block; the harness runs under the
  native Windows Python 3.14.6 (embeddable distribution staged on
  the qualification host, no system install).

The external gap review of 2026-09-04 requested the D1-A crash scope,
the D2-A resource proofs (including the Windows-housekeeping kind),
the D3-B recovery wording, and the kind-gate provenance repair; this
evidence set is regenerated at that fix wave (external framework,
cases, records, and the product-source round-trip fix above).  The
whole-milestone gate review of 2026-09-05 returned FAIL and the user
approved amendment 1A for the crash interruption contract; this
evidence set is regenerated at the sixth fix wave: per-member queue
accounting in both session implementations, the hardened kind gate
with six negative controls, crash scenarios D/F per the amended
contract (successful control run + interrupted findings compared
against the reference), the oversized-frame sentinel proof, and
per-kind crash lineage records.  The whole-milestone gate review
of the sixth wave returned FAIL (two P1 session defects, one P1
kind-gate defect, and six P2 proof defects; the full finding list is
recorded in SOW-0028's seventh-wave section).  This evidence set is
regenerated at the seventh fix wave: per-member cancellation tokens
in both session implementations (cancelling a queued batch member no
longer touches unrelated active work), bounded Rust transport
channels with immediate all-rejected-batch answers, a non-zero exit
on the -32001 framing-failure close in both products, the hardened
kind gate (mixed matrices require both actors to execute every PASS
case, executed-operation records, effective/duplicate command option
validation with executable-to-binary-record binding, crash open
facts backed by recorded opens, fixture-created v4_main lineage,
required-opened kinds), truthful per-scenario sidecar and
adapter-output open lineage with per-actor executed operations, the
exactly-one export-temp orphan contract in crash scenario E, the
deadline-bounded resource harness, and the strict two-row / exact
50-record removal-log Windows checks (report schema v3).  The
five-reviewer round of the seventh wave found one Rust P1
(frame-over-limit deadlock with in-flight work), gate and resource
P2s, and record P1/P2s; the eighth fix wave (2026-09-05) repairs
them and regenerates this evidence: the Rust product above carries
the writer-guard scope, the kind gate now rejects matrix-side
fabricated cross-process opens, every harness deadline uses
`time.monotonic()`, resource proof a waits for the export's private
temp before pipelining the describes, and resource proof c kills
only after the reservation file reached its full 8,192-byte block
(2 x 4,096-byte v4 pages).  The ninth fix wave (2026-09-06)
repairs the broken-stdout shutdown deadlock in both session
implementations, the proof-b forced-kill acceptance (a harness kill
is never a product exit), the proof-b server-envelope and output-
ceiling validation (shared `frame.decode_response`), the missing
deadlines in the shared JSON-RPC client path (bounded read/write
with selectors when the resource harness configures bounds), the
kind-gate command-provenance bypass via argparse abbreviations
(recorded commands are replayed through the runner's own parser
with `allow_abbrev=False`; the fixture-tool argument is bound to the
battery's crash-recorded fixture), the crash operation-ordinal
attribution (opens and creations now reference the actual executed
operation, never a fabricated ordinal zero), and the Windows
cross-listing `source_basename` equality.  All Linux commands ran
under `nice` with work dirs under `/tmp/qualsvc/ev17/work2/`
(matrices, crash, resource); the mixed matrices
are invoked with `--allow-skips` (recorded truthfully in each
report command) so every battery command exits 0.  The Windows
housekeeping evidence is regenerated at the wave-10 product sources
(`e13be7ea`) on the authorized Windows validation host with the
deadline-bounded client running in its Windows thread mode; the
report schema is v3 with the exact 50-record removal log and
build provenance.

## Files

- `matrix-rust.json`, `matrix-go.json` — single-language matrices,
  38 case files: 38 passed, 0 failed in both languages; oracle
  checks 37.  Every PASS case entry carries the per-actor SHA-256,
  the product-declared `implementation` (rust|go from
  `system.describe`) and the executed-step count.
- `matrix-rust_to_go.json`, `matrix-go_to_rust.json` — two-binary
  cross-language matrices: 14 executed (both-actor cases), 24
  skipped (single-actor cases), 0 failed, oracle checks 22, in both
  directions.  The same per-actor identity is recorded for every
  PASS case; `check_kind_coverage.py` derives language attribution
  exclusively from those executed identities (the top-level `matrix`
  label is only cross-checked, never trusted).
- `crash.json` — `iprange-cli-crash-report-v1` in both directions
  (producer=rust with consumer=go, producer=go with consumer=rust):
  16/16 scenarios pass — A1/A2/A3 publication interruption and
  destination classification, B live-transition sidecar
  interruption, C authorized-scratch durability, D interruption
  during uncommitted live-draft construction with a recorded
  successful control run (range count and contents verified), E
  export interruption at the partial-output marker, F interruption
  during validation/findings delivery (interrupted findings output
  is a strict prefix of the successful reference; no destination
  replacement) — zero leftover processes, `failed: 0`, per-scenario
  per-kind actor lineage (`created_by`/`opened_by`).
- `crash-negative.json` — the negative control, 0/16 pass,
  `failed: 16`, zero leftover processes.  Each of the eight crash
  scenarios runs in both substitution directions: with the real
  producer and a substituted `/bin/false` consumer the scenario
  reaches the consumer stage and fails there (the fake consumer
  closes stdout: "service closed stdout"), so a substituted
  consumer can never be credited with executed work; with a
  substituted `/bin/false` producer the scenario fails during
  setup, because the fake producer never creates the artifact and
  no consumer operation runs.  Used as the harness sensitivity
  control, never as a kind-gate source.
- `resource.json` — `iprange-cli-resource-report-v1`: the four
  Linux product-interface proofs for both binaries — (a) the
  >16-in-flight `server_busy` pipelining proof (one slow export + 19
  `system.describe` frames; both binaries answer exactly 3
  `server_busy` (-32002) behind the single in-flight export, 16
  results in the 16-deep queue, the export -32010 `cancelled`, ids
  1..20 covered once, exit 0), (b) the -32001 over-limit frame
  close path (the oversized frame is followed by a valid
  `system.describe` sentinel in the same stdin stream; exactly one
  null-id -32001 response appears, the sentinel unanswered — trailing
  bytes are never parsed — stdout drains to EOF with zero further
  bytes, exit non-zero (startup/framing failure)), (c)
  `maintenance.remove` against a
  real reservation nonce (kill at the reservation marker, list,
  remove with the listed row passed unchanged and a
  proved-currently-absent result: `cleanup_state: clean`, no
  housekeeping artifacts, POSIX directory-synced unlink),
  (d) CLI cancellation (slow export id 1, `iprange.v1.cancel` naming
  it, `system.describe` id 2 in one stdin blob: the cancelled export
  never answers with a result — explicitly-cancelled ids are
  suppressed by the session — the describe answers with a result,
  exit 0).
- `windows-housekeeping.json` —
  `iprange-cli-windows-housekeeping-report-v3` produced on the
  authorized Windows validation host (Microsoft Windows 11, AMD64)
  at the same product-source revision: for each Windows-built
  product binary the report carries (1) the native refresh exercise —
  a real `retention.first_seen.refresh` with a `removals_output`
  behind a pinning live reader completes, publishes the exact
  removal log with the refresh value, leaves no private
  `.removals.tmp` residue, and the reader closes cleanly — (2) the
  two removal-collector abort/failure cleanup exercises (result-
  budget overflow and publish failure on an existing destination:
  no `.removals.tmp` residue, no destination replacement), (3) the
  deterministic GC pair proof — one format-valid 8,192-byte
  authenticated envelope (`.iprange-gcauth-<attempt>-<ordinal>.tmp`,
  artifact kind `private_output`, UTF-16LE source commitment,
  full-block CRC-32C, creator-only protected DACL) plus its inert
  payload twin, listed by `maintenance.list` as exactly two clean
  candidate rows (envelope and inert payload), cross-listed by the
  other product with an equal authenticated directory identity over
  every listing row, then removed with the listed envelope row
  passed unchanged, with a proved-currently-absent result
  (both products truthfully report
  crash_reappearance_possible; the spec's Clean contract makes
  no power-loss guarantee for the final unlink) and a zero-row
  after-listing — and (4) build provenance (source revision, clean
  tree, build commands, toolchain) with per-binary mtime/size.  The
  pair is built by `gc_envelope_windows.py` from the committed
  codec constants (`v4/go/internal/live/gc_codec.go`, `gc_name.go`,
  `identity_local_windows.go`, `v4/go/internal/security/
  security_windows.go`), not by any product test hook.  On
  non-Windows platforms the same script records the truthful
  `os_unsupported`/`read_only_failure` negative.

## Gate invocation

```bash
nice python3 v4/cli/check_kind_coverage.py \
  --matrix v4/cli/evidence/matrix-rust.json \
  --matrix v4/cli/evidence/matrix-go.json \
  --matrix v4/cli/evidence/matrix-rust_to_go.json \
  --matrix v4/cli/evidence/matrix-go_to_rust.json \
  --crash v4/cli/evidence/crash.json
```

The gate requires all four matrix reports and a positive crash report,
rejects failed/leftover reports and unknown kinds, enforces the
both-language creation (and, where any service opens the kind,
both-language consumption) contract per kind from the executed-actor
identities, and counts only PASS crash scenarios.  Its doctored-report
self-test runs before the CLI and covers the clone-and-relabel attack
(a `rust` report relabeled `go` fails), missing per-case actors, and
implementations outside rust/go.
