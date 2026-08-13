# SOW-0025 - Pure-Go Exact v4 Semantic Port

## Status

Status: in-progress

Sub-state: milestone 1 REOPENED pending re-review. The round-10 PASS at HEAD
253f9d5 and the closure commit at HEAD 1c71299 were invalidated by a fresh
independent audit; all five P2 classes were fixed at HEAD ca30026: the
implemented NetworkEnrichmentV1Location surface (value + HasLocation,
recorded as open decision 5A awaiting user ratification), implicit semantic validation removed from
structured lookup, hot-path decodes cut to one page-header decode per
visited page with membership word reads served from the lookup-time record
decode, the contradictory closure records corrected, and Mapping.File
removed with the content-I/O source gate extended to io.ReadAll/io.Copy.
Regression pins: plausible-corruption decode acceptance, record-geometry
rejection at lookup, and the vector codec. The round-11 final review then
found one P1 (pin variable reassignment retargets the view guard and a
word read segfaults on released memory) and three P2 (decision 5A
unratified; mmap gate bypassable plus a Windows Mapping.File escape; stale
close-out records), all fixed at HEAD 2fd6cae with the cross-reader
reassignment regression test, or recorded for the user's decision (5A).
The round-12 final review then confirmed the lifetime fix but found two
remaining P2: decision 5A still unratified, and the mmap source gate still
bypassable (x/sys descriptor reads, bufio wrappers, dot imports, and
build-tagged packages). Fixed at HEAD 4fdc671: the gate is now a
whole-tree selector scan (find across all build tags) with dot-import and
bufio import bans, a self-test mode, and the runtime half of the
mmap-only evidence (strace of an open/read/close session: openat, OFD
lock, mmap, munmap, unlock, close with no read/pread/readv/lseek on the
database descriptor) recorded in the report. The round-13 final review
then failed with three P2: decision 5A still unratified; the gate still
accepted indirect content-transfer forms (fmt.Fscan/Fscanf, io.CopyN/
CopyBuffer, reflection MethodByName, raw unix.Syscall(SYS_READ),
unix.CopyFileRange, Sendfile/Splice), its line-level tolerated-call
exemption could hide a forbidden transfer on the same line, and a
windows-tagged package could still import internal packages unseen by a
linux-only go list boundary check; plus two P3 comment corrections.
All fixed at HEAD dbdf2b7: tolerated calls are blanked as exact call
nodes instead of whole lines, the selector set covers every indirect
form, gzip and compress/zlib wrapper imports are banned, the boundary
check runs per target over ten GOOS/GOARCH pairs, and the self-test
durably rejects eighteen mutation forms. The narrow re-review of that
fix then found the decoder/encoder family still open (encoding/json,
xml, gob NewDecoder(f).Decode, image/archive wrappers), os.File.WriteString,
a nested-paren blanking shadow, and reflect.Value.Method(i);
the gate now also bans the reader-consumer packages, covers
WriteString/WriteRune/NewDecoder/Decode/Encode/Method selectors, blanks
only paren-free tolerated call nodes, and its self-test durably rejects
twenty-two mutation forms. The re-review of that fix found io.ReadFull/
io.ReadAtLeast over a file still open, the writer-consumer packages
(log, text/template, html/template, os/exec, net/http, flate.NewWriter)
uncovered, and three self-test mutations that did not compile; all fixed
at HEAD bf33f2a (selectors ReadFull/ReadAtLeast/Print/Printf/Println/
Scan/Scanln/Scanf/NewWriter; the five writer packages join the import
ban; the two in-memory inflater io.ReadFull(zr, ...) nodes are exempted
exactly; the method-value and CopyFileRange forms compile, and the
nested-node probe stays an intentional textual tripwire), and the
self-test now durably rejects twenty-six mutation forms. The re-review
of that fix found the io.ReadFull exemption itself paren-crossing (a
nested transfer could still be swallowed; a file-backed flate reader
named zr was exempted by name), the reflection Call invocation
unguarded (FieldByName("Read").Call), and the reader-constructor
packages (debug/elf and the debug/* family, go/parser, go/scanner,
text/scanner, text/tabwriter, mime/quotedprintable) unlocked; all fixed
at HEAD 149a200 (the io.ReadFull exemption is shape-bounded to the two
real nodes io.ReadFull(zr, out[...]), Call/CallSlice join the
selectors, the constructor packages join the import ban), and the
self-test now durably rejects twenty-eight mutation forms. The re-review
of that fix found the exemptions still name-keyed (a file-backed flate
reader named zr with a buffer named out, and a receiver field r, could
reproduce the tolerated shapes), so at HEAD c03e40c the exemptions are
exact literals and nothing else: c.r.Read(p), c.r.ReadByte(), and the
two io.ReadFull(zr, out[...int(meta.MetadataUncompressed)]) inflater
reads; same-named file-backed readers and other index shapes now fail
closed, two pin forms were added, and a startup sweep removes stale
gatemut_* artifacts from interrupted self-test runs. The self-test now
durably rejects thirty mutation forms. The records were completed in
the same pass; decision 5A remains the single open user decision and
blocks milestone close. Repository counts: production 4,792 raw lines /
tests 4,877 raw lines. Milestone 2 must not start until a new
independent final review passes.

The sixth final review then failed with five P2 findings, all in the mmap
gate and the records: selector splitting after the dot (`file.\nRead(p)`
and `io.\nReadAll(f)` compile and bypass a line scan); type-blind
exact-literal exemptions (a struct whose `c.r` is `*os.File` using exactly
`c.r.Read(p)`, and a function whose `zr` is `*os.File` using exactly
`io.ReadFull(zr, out[:int(meta.MetadataUncompressed)])`, both pass); an
open-ended stdlib denylist (the gzip regex never matches `compress/gzip`;
`log/slog.NewTextHandler`, `runtime/trace.Start`, and `os.StartProcess`
with `ProcAttr{Files: []*os.File}` consume a file unseen); a destructive
startup sweep (every path named `gatemut_*` is deleted before scanning,
so a committed `gatemut_hidden_linux.go` violation is removed and the
gate reports PASS, and untracked user work can be destroyed); and
acceptance records claiming completion while the six-reviewer PASS at
HEAD 360130c was not recorded and round-12 wording said decision 5A was
"fixed". Fixed at HEAD c42325a: the line-oriented text scan is replaced by
an AST, type-light scanner (v4/go-gate/main.go, stdlib only) that parses
every production file - build tags, line wrapping, comments, aliases,
and file names are irrelevant to the token stream - syntactically taints
`*os.File` values (declarations, parameters, os.Open*/os.Create
producers, same-package constructors, struct fields), bans 37
content-transfer imports and 56 selector families, permits `*os.File`
values only into the mapping-lifecycle methods
(Fd/Close/Name/Stat/Sync/Truncate/Chmod/Chown) and
same-package/module-internal/x-sys consumers, and exempts the three
exact in-memory inflater nodes only when their receiver/arguments are
not file-tainted. The self-test now copies the module into a private
temporary directory: it never touches the reviewed tree, reserves no
file name, proves an innocent `gatemut_`-named file is not deleted, and
durably rejects forty mutation forms including all nine independent
reproducers of the sixth review; the startup sweep is gone. HEAD
81ca524 then pinned the aliased-os producer form as the forty-first;
HEAD 6b05801 tainted `*os.File` results returned by same-package
accessor methods. The seventh-sweep hardening (HEAD e2dc7e0) closed
the type-alias conversion and parameter classes, separately built
`os.ProcAttr{Files}` containers, and the `os.Pipe` producer class,
renumbered the self-test forms, and raised the durable rejection set to
forty-five mutation forms. The eighth sweep (HEAD c4b1b52) closed the
struct-field-storage and channel-transport classes behind the inflater
exemptions (shared per-package taint state, struct-field write taint,
chan *os.File taint including send/recv/range, new(T) instances,
container index reads) and pinned them as self-test forms 47-48. The
ninth sweep (HEAD ddc5f9c) closed the inline-FuncLit, type-assertion,
two-hop-channel, and single-variable-channel-range escape classes
(forms 50-53, with the benign control at form 49); the durable
rejection set is now fifty-one mutation forms. The tenth sweep
(HEAD 5c88ba3) closed the parenthesized-producer,
parenthesized-closure, interface-typed-closure,
alias-typed-function-variable, and type-switch-bound escape classes
(forms 54-58, with the parenthesized benign control at form 59); the
durable rejection set is now fifty-six mutation forms. While
stress-testing the round-4 fixes during the round-5 gate re-review,
the defined-func-type family and its method/nested-callee variants
were closed (self-test forms 60-67), the round-5 struct-field/
chan-of-func/asserted-func/os-std-handle family (forms 68-72), and
the round-6 nested-field/named-helper/chan-pass family, the
named-method extension, the nested-method-receiver extension, the
method-value family, the generic pass-through family, the
generic-element family, the chan-result method-value class, and the
field-assignment class, the channel-consumer class, the
container-element class (forms 73-107), the anonymous-receiver
method class (forms 108-111), the alias-receiver method
class (forms 113-114), the receiver-resolution
class (forms 116-119), the pointer-defined-type
class (forms 121), the indexed-receiver
class (forms 123-125), the element-receiver
class (forms 127-132), the range-literal-receiver
class (forms 134-135), the bound-receiver
class (forms 137-138), the call-result-binding
class (forms 140-143), the explicit-instantiation and
interface-binding class (forms 145-148), the
generic-receiver-binding class (forms 151-156), the
alias-spelled generic binding class (forms 159-164), and the
reader-shape binding class (forms 167-174), and the renamed-qualified alias
  class (forms 179-182), and the func-typed
generic-method class (forms 185-189), the mixed result and
qualified-defined class (forms 191-196), the
interface-method and method-result class (forms 199-205), the
embedded-interface and cross-package chain class (forms 207-210), the
remote-interface and generic-instantiation class (forms 213-217), the
defined-hop instantiation class (forms 222-223), the nested generic-
instantiation class (forms 225-226), and the cgo-import,
raw-syscall, linkname, no-error syscall and preadv2/pwritev2 classes
(forms 228-230, 232-235) with the benign lifecycle control (form
231); the durable rejection set is now two hundred six
mutation forms; round-45 closed the mmap-gate denylist gaps (os.CopyFS directory copies, os.OpenInRoot/os.OpenRoot handles reaching stream wrappers, the x/sys descriptor-transfer primitives Tee/Vmsplice/IoctlFileClone*/Clonefile*, *os.Root laundering through fields/params/helpers, file-method values, and func-typed variables with file-bearing declared results or stdlib producer initializers, forms 249-256; round-36 closed the dup/exec subprocess escape and the
bodyless assembly-stub class, forms 236-237; its follow-up closed the
x/sys-owner boundary for every package plus assembly-object files, forms
238-239; round-38 closed the fcntl F_DUPFD descriptor duplication
primitive, form 240; round-39 closed the out-of-tree module-graph
escape (go.mod replace and go.work can attach code the walk never
scans; the graph is validated to exactly this module plus x/sys with
no workspace, forms 241-242; round-40 closed the x/sys source
replacement and hidden dot-directory vectors, forms 243-244; round-42 closed the x/sys source-content gap, forms 245-247; round-43 closed the fail-open listing gap, form 248). The round-24 gate re-review then found the import-renamed qualified
alias class: an import mm ".../internal/mapping" local qualifier was
never translated back to a package path, so mm.MappingFile generic type
arguments, local alias chains of renamed imports, element spellings,
declared variables, and type assertions all escaped with gate exit 0.
Fixed in the go-gate scanner with per-directory alias registration
(pkgAliasesByDir), a per-file import snapshot (currentImports), and
qualifier translation in aliasLookup; pinned as self-test forms
179-182 (rejects) and 183-184 (benign controls). The durable rejection
set is now one hundred forty-seven mutation forms. The records
of this pass complete the trail up to this re-review. The round-25
gate re-review then found the func-typed generic-method class: a
generic method whose type argument binds a func type producing
*os.File (gRZ[func() *os.File]{}.mk() bound to f, then f()) was
claimed by producerCall as a direct file result, so the binding was
recorded as a file instead of a func-file and invoking it lost the
taint - a file-backed zr again slipped through the io.ReadFull
exemption with gate exit 0. Fixed by removing the funcTextFile claim
from producerCall's generic-method branch so classify's own generic
method loop yields kindFuncFile and applyKind records the func-file
binding; pinned as self-test forms 185-189 (rejects) and 190 (benign
bytes control). The durable rejection
set is now one hundred fifty-two mutation forms. The records
of this pass complete the trail up to this re-review. The round-26
gate re-review then found the mixed-result and qualified-defined
class: (1) callResultsFuncFile required every declared result of a
non-generic function to be a func-file, so mixed multi-result calls
(getFn() (func() *os.File, error)) lost the func-file taint at the
exact func-typed position and f() reached the io.ReadFull exemption
with gate exit 0; (2) a defined type over a qualified or complex
underlying (type x mm.A, type x []*os.File) registered nothing, so
the chain never expanded, and cross-package defined func types
(type F func() *os.File in mapping) were invisible to qualified
references. Fixed with per-position result-kind resolution
(callResults/callResultKinds/callResultKindAt routed through
generic and declared signatures), per-position carrier registration
in applyLHSMulti for call RHS, definedTo registration for every
non-func non-ident underlying, and qualified registration of defined
func types; pinned as self-test forms 191-196 (rejects) and 197-198
(benign bytes controls). The durable rejection
set is now one hundred fifty-eight mutation forms. The records
of this pass complete the trail up to this re-review. The round-27
gate re-review then found the interface-method and method-result
class: (1) a generic receiver bound to an interface whose method
declares mixed results (Get() (func() *os.File, error)) lost the
func-file position because producerCall claimed the interface method
signature (stored as a pseudo-field) as a raw file position, so the
binding was recorded as a file and invoking it lost the taint;
(2) callResults dropped every declared result of a non-generic method
because methodMeta's ok flag reports body-marked producers, not
whether the method exists - mixed method results (mk() (func() *os.File,
error)) bound with gate exit 0; (3) defined types over aliases and
aliases over defined func types (type D A with A = func() *os.File;
type E = D2) were invisible to cross-package spellings because only
aliases and defined func types entered the qualified registries, and a
first-hop name from another directory could not resolve further.
Fixed with declared-result precedence in classify and per-position
kind preference in applyLHSMulti (func-file/chan carriers keep their
invoke-able kind), method-existence detection in producerCall's
field-type claim (a declared method is not a func field),
non-nil-results acceptance in callResults for ordinary methods, and
defined-type registration plus a per-directory fixpoint in the
qualified registries (finalizeDirAliases); pinned as self-test forms
199-205 (rejects covering both mixed positions, the chan-of-func
variant, the defined-over-alias and alias-over-defined hops, and both
method-result positions) and 206 (benign interface-typed bytes
control). The durable rejection
set is now one hundred sixty-five mutation forms. The records
of this pass complete the trail up to this re-review. The round-28
gate re-review then found the embedded-interface and cross-package
chain class: (1) an interface embedding a file-producing interface
(type IEmb interface{ IBase } with IBase.Get() func() *os.File)
resolved no promoted method because methodMeta's embedded walk
propagated only body-marked producers and dropped declared results,
so x.Get()() on an interface-typed generic result reached the
io.ReadFull exemption with gate exit 0 (both the single-result and
the mixed multi-result shapes); (2) a defined struct in another
package used as a generic type argument (gRZ[mm.S28]{}, s.Get())
was invisible to the reader's local package info; (3) a qualified
defined chain of nine named hops (mm.J28 after alias A and hops
B..I) exceeded the single-pass fixpoint budget and was map-order
dependent. Fixed by propagating promoted declared results in
methodMeta's embedding walk, process-wide mirrors of remote
structs/methods/embedded chains with a parse-time seed-merge,
self-entries for struct spellings in the qualified registries, and
a full fixpoint loop for the per-directory alias/defined closure
with self-hop guards; pinned as self-test forms 207-210 (rejects)
and 211-212 (benign bytes controls). The durable rejection
set is now one hundred sixty-nine mutation forms. The records
of this pass complete the trail up to this re-review. The round-29
gate re-review then found the remote-interface and generic-
instantiation class: (1) a renamed import qualifier on a cross-
package interface embedded in a reader interface or struct (type
IEmb interface{ mm.IMapBase }) reduced to no registered key because
only structs (not interfaces) registered the qualifier self-entry,
so the promoted file method lost its taint; (2) a generic interface
instantiated at an embedding site (type IEmbGN interface{
IBaseGN[func() *os.File] }) promoted the raw type parameter without
substitution, so Get() T never matched the file shapes (both the
func-file and chan-of-func variants); the adjacent remote shapes (a
renamed generic interface instantiation and a cross-package generic
struct receiver) carried the same gap. Fixed by registering the
qualified self-entry for interface names exactly like structs,
recording generic interface type parameters in the receiver-parameter
registry with process-wide mirrors, and substituting the embedding's
type arguments in the promoted-method walk; the round's P2 (a
non-compiling benign form-212 twin) was fixed by removing its unused
import. Pinned as self-test forms 213-217 (rejects) and 218-221
(benign bytes controls). The durable rejection
set is now one hundred seventy-four mutation forms. The records
of this pass complete the trail up to this re-review. The round-30
gate re-review then found the defined-hop instantiation class: a
defined type over an instantiated generic interface embedded in an
interface (type D IBaseG[func() *os.File]; type IEmb interface{ D })
lost the instantiation at the embedding walk because the brackets
live in the defined chain's target text, not in the raw embedded
spelling, so the promoted method results propagated the raw type
parameter and Get() T never matched the file shapes (both the
reader-local and the renamed-qualified cross-package shapes). Fixed
by extracting the embedding's type arguments from the resolved
defined/alias text (resolveTaintType then parseBracketArgs) in both
the promoted-method walk and the generic receiver-substitution walk;
pinned as self-test forms 222-223 (rejects) and 224 (benign bytes
control). The durable rejection
set is now one hundred seventy-six mutation forms. The records
of this pass complete the trail up to this re-review. The round-31
gate re-review then found the nested generic-instantiation class:
a multi-level generic-interface embedding (type InnerL[T]
interface{ Get() T }; type IBaseGL[T] interface{ InnerL[T] };
type IEmb interface{ IBaseGL[func() *os.File] }) substituted only
at the frame owning the brackets, so the frame declaring the method
returned the raw type parameter and Get() T reached the io.ReadFull
exemption (three-level and chan-of-func variants included). Fixed by
threading type parameters and arguments down the embedding chain:
each frame substitutes its own instantiation into the next embedded
type text, the declaring frame applies the accumulated arguments,
and a frame-level interface-parameter registry (ifaceParams, mirrored
process-wide) carries generic interface parameters across packages;
the receiver-substitution walk gained the same threading and the
embedded-entry argument list. Pinned as self-test forms 225-226
(rejects) and 227 (benign bytes control); forms 228-230 and 232-235 pin the
round-32 cgo-import, raw-syscall, linkname, no-error syscall, and
preadv2/pwritev2 rejects with form 231 the benign lifecycle
control. The durable rejection set is now one hundred eighty-five
mutation forms. The round-36 gate re-review then found the
subprocess-escape class: dup'ing the database descriptor onto stdin and
exec'ing a reader (unix.Dup2 + unix.Exec, /bin/cat) streams file content
out with no banned read call, and a bodyless Go declaration attaches an
assembly syscall body the AST scan cannot see. Fixed by banning the
Dup/Dup2/Dup3/Exec/ForkExec selectors and rejecting bodyless
declarations outright; pinned as self-test forms 236-237. The durable
rejection set is now one hundred eighty-seven mutation forms. The round-36
follow-up re-review then closed the remaining owner-boundary gaps: a new
package could import golang.org/x/sys unseen by the per-target loop
(only the four known packages were checked), and a .s/.syso assembly
object was never scanned (the bodyless-declaration ban was the only link
guard). Fixed by moving the x/sys owner rule into the per-target loop
for every package except internal/mapping and rejecting assembly objects
outright in the scanner walk; pinned as self-test forms 238-239. The
round-37 narrow re-review then found a metadata parity gap: ReadMetadataJSON
accepted a metadata chunk page whose post-data tail bytes were nonzero,
while Rust rejects it as corrupt (metadata.rs:274) and the spec requires
zero tails (binary-format-v4.md:1051). Fixed with an explicit tail-zero
check in internal/reader/metadata.go and the pre-fix-failing regression
pin TestMetadataChunkTailNonzeroRejected. The round-38 re-review then
closed the fcntl F_DUPFD duplication primitive: unix.FcntlInt(fd,
F_DUPFD, 0) can duplicate the descriptor onto stdin like dup, unseen by
the dup-name bans; FcntlInt joins the banned selector set (FcntlFlock,
the mapping owner's lock path, is a different function and stays
allowed), pinned as self-test form 240, raising the set to one hundred
ninety mutation forms. The round-39 re-review then found the
module-graph escape: a go.mod replace directive or a go.work workspace
can attach an out-of-tree module whose files the scanner walk never
visits, letting a wrapper call unix.Pread on the database descriptor
unseen (both vectors exited 0). Fixed by validating the module graph
itself - go list -m all must be exactly this module plus
golang.org/x/sys, and no workspace may be active - pinned as self-test
forms 241-242, raising the set to one hundred ninety-two mutation
forms. The round-40 re-review then found the path-only allowlist gap:
replace golang.org/x/sys => <evil dir> keeps the allowed path in the
graph while loading attacker-controlled code the walk never scans
(proven live with unix.Pread2 reading the database), and the walk
skipped every hidden dot-directory, hiding in-tree replacements. Fixed
by banning all replace/exclude directives, verifying the resolved
x/sys source is the module-cache checkout, and scanning hidden
directories (only .git is skipped), pinned as self-test forms 243-244,
raising the set to one hundred ninety-four mutation forms. The round-42
gate re-review then closed the x/sys source-content gap: the path-only
allowlist accepted a poisoned GOMODCACHE checkout or a file proxy
serving an evil x/sys with a self-consistent forged go.sum (both proven
live with a smuggled unix.Pread2 the ban list cannot know, because
nothing pinned the module content); the gate now pins the exact version,
the module-cache path, the extracted-tree content hash, and the module
zip/go.mod sums to the official v0.35.0 values, and the assembly-object
rejection is case-insensitive, pinned as self-test forms 245-247,
raising the set to one hundred ninety-seven mutation forms. The round-43
gate re-review then found the fail-open listing gap: the per-target go
list ./... loop swallowed listing failures (2>/dev/null), so a module
the go toolchain cannot list - symlinked package files or parse errors -
passed with an empty package list and no import checks (reproduced with
a symlinked smuggled file in internal/mapping); the package checks now
fail closed on every listing error and the per-package import listing
fails closed too, pinned as self-test form 248, raising the set to one
hundred ninety-eight mutation forms. The six-reviewer
pass for that fix completed with all six narrow reviewers at PASS (HEAD e5fea20).
The round-45 final review then failed with three P2 findings, all in the mmap source gate: os.CopyFS was absent
from the selector ban (a directory copy streams artifact bytes with no banned selector; the live reproducer
exited 0), os.OpenInRoot/os.OpenRoot were absent from the file-producer table (a Go 1.26 OpenInRoot *os.File,
or an older-toolchain *os.Root handle, reached flate.NewReader untainted and streamed file bytes, and
Root.Open/Create/OpenFile also produce files), and the blanket-approved x/sys surface still carried
descriptor-transfer primitives unseen by the denylist (unix.Tee, unix.Vmsplice, unix.IoctlFileClone/CloneRange/
DedupeRange, darwin unix.Clonefile/Clonefileat). Fixed at HEAD 14c0698: CopyFS, Tee, Vmsplice,
IoctlFileClone* and Clonefile* join the banned selector set (CopyFileRange/Sendfile/Splice were already
banned); os.OpenInRoot and os.OpenRoot join the file-producer table as position-0 file taints, so every Root
method outside the approved lifecycle surface fails closed; all three live reproducers plus the
OpenRoot/ReadAll and darwin Clonefile variants are rejected by the hardened gate. The adversarial re-review
of that fix then found a P0 in the same class: a *os.Root handle stored in a struct field (h :=
gateRootField{r: root}; h.r.Open(name)) dropped the file taint, so the returned *os.File reached
flate.NewReader untainted and the stream was consumed through the exact inflater exemption shape with gate
exit 0; *os.Root now resolves as a file-bearing type everywhere *os.File does (fields, parameters,
helper returns, type assertions, func/chan elements, results), so every laundering route fails closed.
Pinned as self-test forms 249-252, raising the durable rejection set to two hundred two mutation forms. The
adversarial re-review of that closure then found three P0 escapes in the same producer-value class, all
proven live with full metadata-exemption chains at gate exit 0: (1) a file method value (open := root.Open;
open(name)) escaped the call-receiver ban and the bound method produced an untainted *os.File; (2) a
func-typed variable with an initializer (var newRoot func(string) (*os.Root, error) = os.OpenRoot) lost
its declared file-bearing result type because the type was only consulted for type-only vars; (3) the same
declared-type gap predated the Root work for *os.File (var openPath func(string) (*os.File, error) =
os.Open). Fixed by checking the file method in value position against the approved surface, registering the
declared result type of initialized func-typed variables, and registering stdlib producer values (os.Open
and friends) as func-files wherever bound; pinned as self-test forms 253-256, raising the durable rejection
set to two hundred six mutation forms. The records
of this pass complete the trail up to this re-review. Repository counts:
production 4,792 raw lines / tests 4,877 raw lines (the metadata fix
accounts for the delta; the gate scanner lives outside the module). Milestone 2 must not start until a
new independent final review passes; decision 5A remains the single open
user decision.
The approved later scope remains unchanged: Milestone 2 is the writer;
sidecars, live coordination, and publication remain Milestone 4.

## Review Process (user decision, 2026-08-12)

1. Implement the milestone work, always long-term-best and minimal-complete.
2. Iteratively run 5-7 narrow-scope subagents on the session's own model (no
   model override). Each focuses on a disjoint aspect of the changes. Fix all
   P0 (critical), P1 (high), and P2 (medium) findings; only P3 (cosmetic)
   issues may be ignored. Repeat until all reviewers PASS.
3. After the iterative pass, run the full-scope final reviewer(s) over the
   entire milestone scope: sol (fixed at xhigh reasoning). The milestone is
   finished only when the final reviewer reports no P0-P2 findings.
4. If a final reviewer finds any P0-P2 issue, restart at step 1: rework,
   re-run the iterative reviewers, then re-run the final reviewer.

### Gate execution record (2026-08-12)

- Iterative pass: six narrow reviewers all PASS at HEAD 52f7a39/e02dee9
  (Peirce, Gauss, Faraday, Ampere, Kant, Bernoulli; only P3 cosmetics,
  fixed in 8e0f413).
- Final reviewer execution: k3 was attempted twice (model group k3) and
  failed in the harness with a proxy tool-call continuation error; the
  user directed that the session rely on sol only.
- sol full-scope rounds at c65b2b9 -> 52f7a39 -> f6007c7 -> 6140a80:
  round 1: 2 P2 (missing pre-fix-failing guards; missing two-pin refcount
  test) + 3 P3 -> fixed in 52f7a39/8e0f413;
  round 2: 2 P2 (retention check wording; report header contradiction) +
  1 P3 -> fixed in f6007c7;
  round 3: 1 P2 (test count 4,648 not recorded) -> fixed in 6140a80;
  round 4: PASS, no P0-P2-P3 at HEAD 6140a80 (this gate was subsequently
  reopened by an external audit, below).
- External audit reopening (2026-08-12): four findings - close-out records
  inconsistent (pending worker decision, no-deletion claim, 5/5 fixtures,
  pin copies described as unsupported), Milestone 2 scope drift, an
  incomplete public scalar API (raw [16]byte ValueTag, missing predefined
  tags and the 20 MiB metadata bound), and false zero-allocation source
  documentation. Fixed in 228be36, plus reviewer P3s in 78373e5
  (DirectSemantic per-fixture coverage, terminal error code 69 pin,
  decision-log 105-file count, qualified package comment).
- sol round 5: PASS, no P0-P2-P3 at HEAD 78373e5 (record gap: the
  reopening itself was not yet recorded);
- sol round 6: reported PASS at HEAD 29e1dde, confirming the previously named
  checks and records. An independent adversarial re-review then invalidated
  that verdict: it found the mutable exported semantic-tag authority, the
  unapproved DatabaseInfo/ImmutableInfo API deviation, and stale contradictory
  milestone-report claims. Milestone 1 is reopened; Milestone 2 is blocked.
- Review-process repair: added the generic runtime skill
  `.agents/skills/project-final-review/SKILL.md`. It requires zero-trust
  authority reconstruction, open-world public-contract and record audits,
  mechanical gates only after semantic review, and a final disproof pass before
  PASS.
- sol round 7: FAIL at HEAD 2d2197a with four P2 record/truth defects: the
  milestone report and SOW still presented the reopened findings as
  unresolved, the import-graph gate still exempted the deleted
  internal/exactv4 package, the reader documented a re-derive-every-access
  claim contradicted by cached structured values, and the zero-allocation
  evidence misstated the pin/reader measurement grain and iteration counts.
  All fixed in a64a495 (gate exemption removed; report header, section 5,
  section 6 and close-out corrected; reader.go comment qualified; zero-alloc
  comments/messages corrected), and the report LOC counts were refreshed
  for that commit's own delta in 12b2e7f (P1 found by the narrow records
  reviewer; production 4,812 / tests 4,685 verified).
- sol round 8: FAIL at HEAD 12b2e7f with one P2 (this gate record omitted
  round 7 and its repairs; the repair note above contradicted the Status)
  and one P3 (duplicate package documentation: go doc rendered both
  doc.go and types.go package comments). Fixed in 1af6135: the trail was
  completed, types.go became a file-level comment, and the report LOC
  counts were refreshed (production 4,807 / tests 4,685).
- sol round 9: FAIL at HEAD 1af6135 with one P2 - the round-8 entry had
  omitted the P3 and its repair, the "follows below" reference dangled,
  and the regression resolution claimed a closing result that did not yet
  exist. This entry records the complete round-8 result.
- sol round 10: PASS at HEAD 253f9d5, zero P0-P2-P3, full-scope zero-trust
  re-review (records, pin lifetime, mapping/locking, format/API, resources,
  regression proof, gates). This verdict was later invalidated.
- External audit after closure: FAIL at HEAD 1c71299 with five P2 findings:
  the public NetworkEnrichmentV1 location shape deviates from the approved API
  matrix; structured lookup performs full semantic validation that Rust and the
  normal-operation contract omit; range/blob/membership hot paths repeat
  decodes and the report falsely claims one page-header decode per visited
  page; the closure header contradicts Validation and skill-maintenance text;
  Mapping.File exposes an unused raw descriptor capability while the
  content-transfer source gate misses helper forms such as io.ReadAll and
  io.Copy. Milestone 1 is reopened and Milestone 2 is blocked.
- Review-process repair: `project-final-review` now defines the reviewer's
  mission as proving the work faulty or incomplete with concrete evidence. It
  authorizes unrestricted relevant investigation and `/tmp` tests, requires an
  objective/blast-radius model, treats every green claim as something to attack,
  continues beyond the first finding, and permits PASS only when the strongest
  plausible disproof attempts fail. The repository remains read-only; reviewers
  may not interfere with processes or install/uninstall software.
- Fix round (external-audit findings): implemented at HEAD ca30026 with
  pre-fix-failing pins - NetworkEnrichmentV1Location restored (decision 5A),
  structured lookup decode-only (plausible-corruption acceptance test fails
  pre-fix; record-geometry rejection keeps the memory-safety bound),
  OpenSlottedHeader at every slotted call site, membership word reads from
  the lookup-time record decode, Mapping.File removed, content-I/O gate
  extended to ReadAll/Copy forms. All gates green.
- Iterative pass (round-11 fixes): six narrow reviewers all PASS at HEAD
  431e7d7 (Peirce, Gauss, Faraday, Ampere, Kant, Bernoulli; one records P1
  - stale test LOC - fixed in 73c358b; Kant flags that decision 5A needs
  user ratification before milestone close).
- sol round 11: FAIL at HEAD 73c358b with one P1 and three P2: a Pin
  variable reassigned after creating a view (pinCopy = *otherPin)
  retargets the view's close guard to another reader and a word read then
  hits the first reader's released mapping (SIGSEGV at
  internal/reader/membership.go:106 through reader_public.go:485);
  decision 5A is recorded but not user-ratified; the content-I/O gate
  misses method values (m := f.Read), function aliases (rd := io.ReadAll),
  Seek, and new package directories, and the Windows mapping stub still
  exposed Mapping.File; the close-out records dangled ("re-run below")
  and the regression tail still said product repair is pending. Fixed at
  HEAD 2fd6cae (views retain the immutable *pinState captured at creation;
  the Windows descriptor and accessor are gone; the gate scans every go
  list package with word-boundary selector matching, mutation-tested
  against all five bypass forms) and in the records commit that follows.
- Iterative pass (round-12 fixes): six narrow reviewers all PASS at HEAD
  002505b (Peirce, Gauss, Faraday, Ampere, Kant, Bernoulli; one records P1
  - report production LOC - and one records P2 - missing round-12
  narrative - fixed in 002505b).
- sol round 12: FAIL at HEAD 002505b with two P2 and one P3: decision 5A
  remains unratified (user-decision gate); the mmap source gate is still
  bypassable - unix.Readv descriptor reads in the mapping owner, bufio
  wrappers (bufio.NewReader(file).ReadByte), dot-imported os.ReadFile,
  and build-tagged packages invisible to a linux go list all compile and
  pass the gate; the records claim gate coverage beyond what it provides
  and the report lacks the runtime-trace evidence the SOW records
  require. Fixed at HEAD 4fdc671: whole-tree selector scan (find covers
  every build-tagged file), dot-import and bufio/io-ioutil import bans,
  extended selector set (Readv/Writev/Preadv/Pwritev/ReadByte/...), a
  durable --self-test mode that at that commit rejected nine mutation forms
  (the two bufio escape forms followed in 9567067), runtime
  strace evidence recorded in the report, and P3 lifetime-comment
  corrections.
- Iterative pass (round-13 fixes): six narrow reviewers all PASS at HEAD
  a1f846f (Peirce, Gauss, Faraday, Ampere, Kant, Bernoulli; no P0-P2
  findings; only the records forward-pointer remained to be written).
- sol round 13: FAIL at HEAD a1f846f with three P2 and two P3: decision
  5A remains unratified (user-decision gate); the gate still accepts
  indirect content-transfer forms (fmt.Fscan/Fscanf/Fscanln,
  io.CopyN/CopyBuffer, reflection MethodByName("Read"), raw
  unix.Syscall(SYS_READ), unix.CopyFileRange, Sendfile, Splice), its
  line-level exemption lets a forbidden transfer share a line with a
  tolerated c.r.Read call, and a windows-tagged package can import
  internal packages unseen by a linux-only go list boundary check; the
  records contradicted the source (open-decisions prose said no product
  decision is open while 5A is unratified, the report header claimed the
  round-12 gate P2 was fully fixed although the gate remained bypassable,
  and the reported production count missed two comment lines). P3s:
  mapping View retained-slice comment wording and
  NetworkEnrichmentV1View.Value comment wording. All fixed at HEAD
  dbdf2b7 and in this record: exact call-node blanking replaces the
  line-level exemption, the selector set covers every indirect form, gzip
  and compress/zlib wrapper imports are banned, the boundary check runs
  per target over ten GOOS/GOARCH pairs, the self-test durably rejects
  all eighteen mutation forms, the P3 comments were corrected, and the
  records in this entry complete the trail.
- Iterative pass (round-13 fixes, second sweep): five narrow reviewers
  PASS at HEAD 26f0527 (Peirce, Gauss, Faraday, Kant, Bernoulli); Ampere
  found the gate still open in four classes - P1: stdlib decoder/
  encoder families consume the file directly
  (json/xml/gob NewDecoder(f).Decode, plus archive/image/bzip2/etc.
  reader packages); P2: os.File.WriteString, reflect.Value.Method(i),
  and the exact-node blanking swallowed a forbidden transfer nested
  inside the tolerated call's parentheses; P3: two self-test mutations
  did not compile. All fixed at HEAD f9c88b2: the reader-consumer
  packages join the import ban, the selector set gains
  WriteString/WriteRune/NewDecoder/Decode/Encode/Method, the blanking
  matches only paren-free tolerated arguments (c.r.Read(p) /
  c.r.ReadByte()), the two previously non-compiling sweep mutations
  compile, and four new mutation forms pin every escape; the self-test now durably rejects all
  twenty-two mutation forms. Decision 5A remains open for user
  ratification and is the only remaining P2 class.
- Iterative pass (round-13 fixes, third sweep): five narrow reviewers
  PASS at HEAD 2b30b29 (Peirce, Gauss, Faraday, Kant, Bernoulli);
  Ampere found the gate still open in three classes - P1: io.ReadFull
  and io.ReadAtLeast consume an *os.File directly (the word boundary
  after Read kept them out of the selector set); P2: the writer-consumer
  families curry a file unseen (log.New(f).Println / log.SetOutput(f),
  text/template Execute, html/template ExecuteTemplate, os/exec
  Stdout+Run, flate.NewWriter(f), http ServeContent/ServeFile; none
  active in production, but the gate must cover the Milestone 2
  writer); P3: three self-test mutations did not compile (method value
  assignment arity, CopyFileRange int width, and the nested-node probe).
  Fixed at HEAD bf33f2a: the selector set gains ReadFull/ReadAtLeast/
  Print/Printf/Println/Scan/Scanln/Scanf/NewWriter, the five writer
  packages join the import ban, the two in-memory inflater calls
  io.ReadFull(zr, ...) are exempted as exact call nodes (compress/flate
  stays importable), the method-value and CopyFileRange forms now
  compile, and the nested-node probe is retained and documented as an
  intentional textual tripwire (no []byte-typed file-read expression
  exists to embed before the first closing paren); the self-test now
  durably rejects all twenty-six mutation forms. Decision 5A remains
  open for user ratification and is the only remaining P2 class.
- Iterative pass (round-13 fixes, fourth sweep): five narrow reviewers
  PASS at HEAD 35a4182 (Peirce, Gauss, Faraday, Kant, Bernoulli);
  Ampere found the gate still open in three classes - P1: the new
  io.ReadFull blanking was paren-crossing and name-keyed (a transfer
  nested inside the tolerated node's arguments was swallowed again; a
  file-backed `zr := flate.NewReader(f)` was exempted by the variable
  name alone); P2: the reflection invocation primitive `.Call` was
  unguarded (reflect.ValueOf(f).FieldByName("Read").Call(nil) slipped);
  P2: the reader-constructor packages (debug/elf/macho/pe/plan9obj,
  go/parser, go/scanner, text/scanner) and writer families
  (text/tabwriter, mime/quotedprintable) consumed a file unseen.
  Fixed at HEAD 149a200: the io.ReadFull exemption is shape-bounded to
  the two real in-memory nodes (io.ReadFull(zr, out[...])) so neither a
  nested transfer nor a zr-named file reader is hidden; Call/CallSlice
  join the selector set; the constructor packages join the import ban;
  two new mutation forms pin the shadow and the zr-name collision. The
  self-test now durably rejects all twenty-eight mutation forms.
  Decision 5A remains open for user ratification and is the only
  remaining P2 class.
- Iterative pass (round-13 fixes, fifth sweep): five narrow reviewers
  PASS at HEAD 6a25450 (Peirce, Gauss, Faraday, Kant, Bernoulli; Kant
  adds a P3 hygiene finding - stale gatemut_* artifacts from an
  interrupted self-test can wedge the tree); Ampere found the
  exemptions still name-keyed: P1 - `zr := flate.NewReader(f)` with a
  buffer literally named `out` reproduces the tolerated
  io.ReadFull(zr, out[...]) shape (the project's own inflater naming),
  and P2 - a receiver field `r *os.File` reproduces the c.r.Read shape.
  Fixed at HEAD c03e40c: all four tolerated nodes are blanked as exact
  literals (c.r.Read(p), c.r.ReadByte(), and the two
  io.ReadFull(zr, out[:int(meta.MetadataUncompressed)]) /
  io.ReadFull(zr, out[int(meta.MetadataUncompressed):]) inflater
  reads) and nothing else, so same-named file-backed readers and other
  index shapes fail closed; two new mutation forms pin the file-backed
  c.r and the zr/out name collision; the startup sweep removes stale
  gatemut_* artifacts so an interrupted self-test cannot wedge the
  tree. The self-test now durably rejects all thirty mutation forms.
  Decision 5A remains open for user ratification and is the only
  remaining P2 class.
- Iterative pass (fifth-sweep completion): all six narrow reviewers PASS
  at HEAD 360130c (Peirce, Gauss, Faraday, Ampere, Kant, Bernoulli); the
  fifth-sweep records were committed in 360130c.
- sol round 14: FAIL at HEAD 360130c with five P2 findings, all in the
  mmap gate and the records: split-after-the-dot selectors
  (file.\nRead(p), io.\nReadAll(f)); type-blind exact-literal
  exemptions (a struct whose c.r is *os.File using exactly c.r.Read(p),
  and a function whose zr is *os.File using exactly io.ReadFull(zr,
  out[:int(meta.MetadataUncompressed)]), both reproduce the tolerated
  shapes); the open-ended stdlib denylist (the gzip regex never matches
  compress/gzip; log/slog.NewTextHandler, runtime/trace.Start, and
  os.StartProcess with ProcAttr{Files: []*os.File} consume a file
  unseen); the destructive startup sweep (any path named gatemut_* is
  deleted before scanning, so a committed gatemut_hidden_linux.go
  violation is removed and the gate reports PASS); and acceptance
  records claiming completion while the six-reviewer PASS at 360130c
  was not recorded and round-12 wording said decision 5A was "fixed".
  Fixed at HEAD c42325a: the line-oriented text scan is replaced by the
  AST, type-light scanner (v4/go-gate) described in Status; the
  self-test copies the module to a private temp directory (forty
  mutation forms rejected, including all nine independent reproducers;
  the reviewed tree is never modified, no file name is reserved, and an
  innocent gatemut_-named file is proven to survive); the startup sweep
  is removed. HEAD 81ca524 pinned the aliased-os producer-taint form as
  the forty-first; HEAD 6b05801 tainted *os.File results of
  same-package accessor methods; the seventh sweep (HEAD e2dc7e0)
  closed the alias conversion/parameter, ProcAttr-container, and
  os.Pipe classes, releasing the 45-form self-test; the eighth sweep
  (HEAD c4b1b52) closed the struct-field-storage and channel-transport
  classes behind the inflater exemptions (self-test forms 47-48). The
  ninth sweep (HEAD ddc5f9c) closed the inline-FuncLit, type-assertion,
  two-hop-channel, and single-variable-range classes (forms 50-53);
  the tenth sweep (HEAD 5c88ba3) closed the parenthesized-producer,
  parenthesized-closure, interface-typed-closure, alias-typed-function-variable,
  and type-switch-bound classes (forms 54-59), and the defined-func-type
  family (defined func types, func-valued returns through same-package
  helpers, type-switch bound func cases (forms 60-63), and the
  method-receiver/nested-callee double-call family (forms 64-67),
  the struct-field-func/chan-of-func/asserted-func/os-std-handle
  family (forms 68-72), the nested-field/named-helper/chan-pass
  family (forms 73-77), the named-method extension (forms
  78-81), the nested-method-receiver extension (forms 82-83),
  the method-value family (forms 84-87), the generic
  pass-through family (forms 88-89), the generic-element
  family, the chan-result method-value class, the
  field-assignment class (forms 92-95), the channel-consumer
  class (forms 98-100), the container-element
  class (forms 103-106), the anonymous-receiver
  method class (forms 108-111), the alias-receiver
  method class (forms 113-114), the receiver-resolution
  class (forms 116-119), the pointer-defined-type
  class (forms 121), the indexed-receiver
  class (forms 123-125), the element-receiver
  class (forms 127-132), the range-literal-receiver
  class (forms 134-135), the bound-receiver
  class (forms 137-138), the call-result-binding
  class (forms 140-143), the explicit-instantiation and
  interface-binding class (forms 145-148), the
  generic-receiver-binding class (forms 151-156), the
  alias-spelled generic binding class (forms 159-164), and the
  reader-shape binding class (forms 167-174), and the
  renamed-qualified alias class (forms 179-182), and the
  func-typed generic-method class
  (forms 185-189), the mixed result and
  qualified-defined class (forms 191-196), the
  interface-method and method-result class (forms 199-205), and the
  embedded-interface and cross-package chain class (forms 207-210),
  the remote-interface and generic-instantiation class (forms
  213-217), the defined-hop instantiation class (forms 222-223), and
  the nested generic-instantiation class (forms 225-226);
  the self-test now durably rejects one hundred eighty-five mutation forms (round-32 rejects 228-235, benign control 231); the round-36 re-review then closed the dup/exec subprocess-escape and bodyless assembly-stub classes as forms 236-237, bringing the set to one hundred eighty-seven mutation forms; the follow-up then closed the x/sys-owner boundary for new packages and rejected assembly-object files (forms 238-239), raising the set to one hundred eighty-nine mutation forms. The round-37
  re-review then found one P2 in the metadata/bootstrap aspect: nonzero
  bytes after a metadata chunk were accepted by ReadMetadataJSON (Rust
  rejects them as corrupt; spec binary-format-v4.md:1051 requires zero
  tails). Fixed with the tail-zero check and the pre-fix-failing pin
  TestMetadataChunkTailNonzeroRejected. The round-38
  re-review then closed the fcntl F_DUPFD duplication primitive
  (unix.FcntlInt; form 240), raising the set to one hundred ninety
  mutation forms. The round-39
  re-review then closed the out-of-tree module-graph escape: go.mod
  replace and go.work workspaces attach modules the scan never walks
  (reproduced with a wrapper calling unix.Pread, gate exit 0 on both
  vectors); the graph is now validated to exactly this module plus
  golang.org/x/sys with no workspace active, pinned as forms 241-242,
  raising the set to one hundred ninety-two mutation forms. The round-40
  re-review then closed the x/sys source-replacement gap: a replace of
  golang.org/x/sys to an evil dir keeps the allowed path in the graph
  while loading code the walk never scans (live pread2 reproducer), and
  hidden dot-directories were skipped by the walk; replace/exclude
  directives are now banned, the resolved x/sys source is verified to be
  the module-cache checkout, and the walk skips only .git, pinned as
  forms 243-244, raising the set to one hundred ninety-four mutation
  forms. The round-42
  re-review then closed the x/sys source-content gap: a poisoned module
  cache or a file proxy with a forged go.sum keeps the allowed path and
  version while loading an evil x/sys (live Pread2 reproducers on both
  vectors); replace/exclude were already banned, so the gate now pins
  the exact version, the module-cache path, the extracted-tree content
  hash, and the module zip/go.mod sums to the official v0.35.0 values,
  and rejects assembly objects case-insensitively, pinned as forms
  245-247, raising the set to one hundred ninety-seven mutation forms. The
  round-43 re-review then closed the fail-open listing gap: the target
  loop ran go list ./... with 2>/dev/null, so a module the toolchain
  cannot list (symlinked package files, parse errors) passed with no
  package checks at all; go list failures now set fail=1 per target and
  pkg_imports fails closed, pinned as form 248, raising the set to one
  hundred ninety-eight mutation forms. The round-45
  re-review then found the mmap-gate denylist gaps: os.CopyFS directory
  copies, os.OpenInRoot/os.OpenRoot handles reaching stream wrappers, and
  the x/sys descriptor-transfer primitives (unix.Tee/Vmsplice/
  IoctlFileClone*, darwin Clonefile*) all bypassed the scan (proven live,
  gate exit 0 on every vector); CopyFS and the x/sys primitives join the
  banned selector set, os.OpenInRoot/os.OpenRoot join the file-producer
  table so Root methods fail closed, pinned as self-test forms 249-251,
  raising the set to two hundred one mutation forms; the
  same-class P0 (Root laundered through a struct field, gate exit
  0) was then closed by resolving *os.Root as a file-bearing type
  everywhere *os.File does, pinned as form 252, raising the set to
  two hundred two mutation forms; the following re-review then found
  three producer-value P0 escapes (file method values; func-typed
  vars with file-bearing declared results and an initializer, Root
  and *os.File; stdlib producer values bound without a declared
  type), all closed by the value-position capability check, the
  declared-type func-file registration, and stdlib producer-value
  registration, pinned as forms 253-256, raising the set to two
  hundred six mutation forms. The
  records
  of this entry complete the trail up to this re-review. Decision 5A
  remains open for user ratification and is the only remaining P2
  class.

## Requirements

### Purpose

Deliver the pure-Go peer of the accepted Rust v4 SDK so `update-ipsets` and Go
consumers can use the exact current unsigned Phase-1 database without cgo or a
Rust runtime dependency. The result must preserve the format's mmap-only,
bounded, durable, two-level architecture and its measured performance
discipline rather than mechanically translating Rust source.

Make the work safe to hand to an independently run implementer. Evidence at
each milestone must let the user judge whether the implementation is accurate,
focused, maintainable, and worth continuing before a large rewrite is accepted.

### User Request

- Commit and push the existing work so the repository starts clean.
- Create a self-contained SOW for porting the accepted Rust implementation to
  pure Go.
- Keep the implementation minimal-complete, thin, clear, and performance-first.
- Use the implementation and its evidence as a controlled evaluation of an
  independently run implementer; the user will decide whether and how to
  continue from the milestone results.

### Assistant Understanding

Facts:

- The Rust engine is complete for the current approved Phase-1 contract and has
  passed correctness, durability, resource, performance, conformance, C-boundary,
  and native supported-platform gates. Its final behavior and measurements are
  summarized in `v4/rust/README.md` and completed SOWs 0020-0024.
- The normative authorities are
  `.agents/sow/specs/binary-format-v4.md` and
  `.agents/sow/specs/design-iprange-engine.md`. Rust is implementation evidence;
  its physical page placement and internal types are not a second specification.
- The current Go module is an older unreleased experiment. It has 50 production
  Go files and 44,088 newline-counted production lines, but its public package
  exposes only basic types, cardinality, and error declarations.
- The old Go wire model permits only direct and membership values, keeps the old
  `retention` tag and 1 MiB metadata limit, lacks all structured-value meta state,
  and does not open the current shared corpus
  (`v4/go/internal/exactv4/contract.go:11-18,63-73,98-101,135-162` and
  `v4/go/types.go:13-34`).
- The old Go storage layer performs positional content reads and writes and owns
  complete 4 KiB page arrays outside file mappings
  (`v4/go/internal/exactv4/page_source_linux.go:13-49`,
  `v4/go/internal/exactv4/page_source_other.go:12-54`,
  `v4/go/internal/exactv4/os_linux.go:495-577`, and
  `v4/go/internal/exactv4/private_page_pool.go:92-103`). These are prohibited by
  the current format contract.
- The old Go tests, vet, and formatting pass. This proves those old internal
  fragments are internally consistent; it does not prove current format or SDK
  parity. No Go test references `v4/conformance/cases.json` or opens a current
  Rust-produced fixture.
- The Rust-provided C ABI remains the only C SDK. Porting or regenerating that ABI
  in Go is outside this SOW.
- Snapshot signing remains Phase 2 under pending SOW-0017 and is outside this
  SOW.

Inferences:

- Treating the current Go tree as a nearly finished implementation would preserve
  the architecture that Rust deliberately replaced. The safe approach is a
  semantic port from the current spec and accepted Rust behavior, reusing an old
  Go component only after current tests prove it conforms.
- A literal Rust-to-Go line translation would copy language-specific ownership
  machinery and the Rust implementation's size. The Go design should reproduce
  each invariant and observable operation once using idiomatic Go and a smaller
  module graph.
- The isolated physical-fault worker, Windows namespace/security behavior, and
  mixed-language live coordination are the highest feasibility and portability
  risks. They must be proved early, not left to final integration.

Unknowns:

- The exact subset of old Go tests or codecs worth retaining. Milestone 0 must
  classify each current production file as retain, rewrite, or delete before any
  destructive edit.
- The final Go/Rust timing delta. The accepted 5-10% band is a target where the
  runtimes permit it; only matched measurement can establish justified
  exceptions.
- Whether the pure-Go fault worker can satisfy every POSIX signal-chaining rule
  without project-owned assembly or another new native boundary. The implementer
  must prove the existing no-cgo contract. If a new boundary appears necessary,
  implementation stops for a user design decision before adding it.

### Acceptance Criteria

- The final module is a pure-Go implementation with no cgo, Rust library,
  external database engine, or alternate content path.
- It supports only the exact current v4 identity. No v3 code, old-v4 parser,
  compatibility mode, importer, exporter, or obsolete fixture remains.
- Public Go behavior covers the complete Phase-1 semantic surface listed in
  `v4/rust/iprange-livedb/src/lib.rs` and the design spec, except the explicitly
  Rust-provided C ABI. Idiomatic Go names are allowed; missing semantics are not.
- The public SDK exposes logical advanced direct, membership, and structured
  transactions plus the typed high-level workflows. It never exposes page
  numbers, roots, raw feed indexes as mutation authority, membership IDs,
  structure IDs, bitmap combinations, or allocator state.
- One internal reader core owns healthy selected-generation access. One internal
  writer core owns healthy mapped COW mutation, allocation, retirement, page
  sealing, and committed-generation publication. Higher-level APIs only sequence
  logical operations over those owners.
- Validation/recovery, external reader coordination, and filesystem
  namespace/publication retain their separate narrow owners while reusing the
  canonical codecs and mapped output builders. No second physical tree or
  publication implementation exists in a workflow.
- Every persistent artifact is content-accessed only through file-backed
  mappings. Static and runtime gates reject `Read`, `Write`, `ReadAt`, `WriteAt`,
  `Pread`, `Pwrite`, `ReadFile`, `WriteFile`, buffered/stream content transfer,
  and equivalent Windows calls against SDK artifacts.
- No complete database page exists in a Go heap/stack array, cache, anonymous
  mapping, or copied byte slice. Page creation and COW edits occur at final
  offsets in file-backed mappings; mapped-to-mapped cell copies are allowed.
- Open, lookup, scan, mutation, commit, query, and snapshot do not implicitly
  run full validation. Explicit validation and recovery alone perform the full
  graph and checksum work.
- Direct, membership, and structured files implement the exact current meta,
  page, dictionary, metadata, retirement, sidecar, reservation, and publication
  contracts. `NetworkEnrichmentV1` uses one common structured manager plus its
  independent codec, including lazy threat membership.
- The opaque compressed metadata payload preserves exact caller bytes, absent
  versus empty state, read-your-writes, and the 20 MiB uncompressed limit.
- Generic direct assignments and structured range assignments apply every input
  in arrival order. A later range overwrites only its covered addresses.
  Unordered ingestion normalizes directly in the destination and creates no
  sorting file or complete-feed heap image.
- Named feeds, feed-index reuse, membership interning, structured interning,
  typed references, stale/foreign reference rejection, and deletion/rename
  behavior match the current contract. Callers never construct internal ID
  combinations.
- Exact direct replacement, first-seen refresh, last-seen refresh, named-feed
  create/replace/rename/delete, membership import, one-inode immutable feed
  construction, history projection, queries, provider joins, global named-feed
  algebra, compact snapshot, commit resolution, and lifecycle/publication
  resolvers match Rust semantics and reports.
- Operation failure aborts the private draft. No later commit can publish partial
  work after a failed source or sink. Commit, abort, close, cleanup, and
  outcome-unknown results retain exact evidence and retry obligations.
- Live reader registration, writer exclusion, reader-safe transaction-grouped
  reclamation, lowest-free-page allocation, sidecar identity/replacement, and
  forked-handle rejection match the exact contract.
- Linux, macOS, and Windows implement the supported live contract. FreeBSD 14
  implements immutable reading, offline validation/recovery, and durable
  immutable publication while every live entry rejects before path access or
  mutation.
- The Go distribution supplies its own exact version-matched
  `iprange-v4-worker`. It claims only SDK-owned in-region physical mapping faults,
  chains every unrelated POSIX `SIGBUS` disposition exactly, uses the equivalent
  Windows exception rule, and never mislabels an unrelated worker crash as source
  unreadability.
- Go independently produces all required conformance fixtures. Both readers
  actually open and semantically verify every Go- and Rust-produced fixture,
  including structured values, full IPv6 cardinality, and exact metadata bytes.
- Mixed Rust/Go subprocess tests pass in both directions for reader slots, writer
  exclusion, reclamation, stale-slot cleanup, sidecar replacement, transition
  states, reservations, publication inspection, and resolution.
- Warm successful point lookups and cursor steps allocate zero Go heap bytes.
  Writer allocations and heap use are fixed or bounded by declared budgets, not
  proportional to input records or sparse page numbers.
- Test-only necessary-work counters pin tree descents, pages visited/copied,
  range passes/splits/merges, dictionary work, checksum sealing, synchronization,
  and artifact creation. They compile out of production binaries.
- Matched release benchmarks cover the accepted Rust matrix and representative
  update-ipsets data. Each retained dominant cost maps to required format work;
  unexplained wasted work is fixed and the audit repeated.
- Operation-by-operation Go performance targets the accepted Rust result within
  5-10% where runtime behavior permits. Every material exception has matched
  profiles, hardware/runtime evidence, and a documented cause. CI uses a loose
  disaster threshold only after local performance acceptance.
- Current Go tests, race/checkptr/fuzz/property tests, malformed corpus tests,
  crash/fault tests, resource tests, cross-compilation, and authorized native
  platform tests pass. Skips cannot hide a required platform or producer.
- The final production graph has no unreachable source, broad dead-code
  suppression, duplicate physical authority, or test-only production mechanism.
  Production LOC, file sizes, function sizes/complexity, and exact-clone evidence
  are reported honestly against the directional engineering philosophy.
- Every valid finding in the final first-principles audit is repaired and the
  identical audit is repeated. This SOW cannot complete while an actionable
  correctness, durability, coordination, bounded-resource, performance,
  layering, duplication, portability, documentation, or conformance issue
  remains.

## Analysis

Sources checked:

- `AGENTS.md`, especially the v4 engineering philosophy and Rust-first gate.
- `.agents/skills/project-v4-rust/SKILL.md` for the proven Rust verification and
  portability workflow that the Go peer must reproduce semantically.
- `.agents/sow/specs/design-iprange-engine.md:15-50,57-129,131-169,366-449,451-519`.
- `.agents/sow/specs/binary-format-v4.md`, complete current contract, especially
  sections 3-5, 8-15, 16, 18-21.
- `v4/rust/README.md` and `v4/rust/iprange-livedb/src/lib.rs` for the accepted
  public semantic inventory and measured baseline.
- Completed SOWs 0019-0024 for the mmap-only correction, final authority/hot-path
  audit, update-ipsets workflows, performance proof, structured values, and
  randomized structured correctness.
- `v4/conformance/README.md`, `v4/conformance/cases.json`, and all six current
  Rust-produced fixtures.
- Complete current Go production/test inventory, `v4/go/go.mod`, public files,
  current format constants, storage/page sources, OS code, and test references.
- Baseline commands: `go test ./...`, `go vet ./...`, and `gofmt -l .`; all pass
  before porting.
- Official Go `runtime`, `unsafe`, `golang.org/x/sys/unix`, and
  `golang.org/x/sys/windows` documentation; Microsoft file-mapping documentation;
  and the platform syscall references already normative in the format spec.
- `etcd-io/bbolt @ 01f7d9658a8a` as a limited Go platform-wrapper reference:
  `bolt_unix.go:55-83`, `bolt_windows.go:65-119`, and `db.go:454-570`.

Current state:

- Git started this planning slice clean and synchronized after commit `900b345`.
- The Rust peer and Rust-produced corpus exist. The Go peer, Go-produced corpus,
  cross-open proof, and mixed-language coordination proof do not.
- The current Go tree is large but not product-shaped: 44,088 production lines,
  1,609 production function declarations, several files above 1,000 lines, and a
  4,794-line largest file. Twenty-seven production files contain positional
  content access and/or complete page arrays.
- The current Go public package has no reader, writer, workflow, query,
  validation, recovery, snapshot, publication, or live lifecycle constructor.
- `v4/go/go.mod` currently declares Go 1.23 and `golang.org/x/sys v0.35.0`.
  Preserve that support floor unless a required API proves it impossible; a
  toolchain/dependency-floor change requires evidence and user approval.

Risks:

- Preserving the old Go storage architecture would reproduce the already-fixed
  positional-I/O/page-buffer failure and invalidate performance/resource claims.
- Translating Rust module-for-module would likely reproduce its language-specific
  size and obscure Go's simpler ownership model.
- Porting only the happy-path reader/writer would miss lifecycle, outcome,
  cleanup, recovery, and cross-process contracts that prevent data loss.
- A Go mmap slice remains valid only while its retained mapping/handle lifetime is
  valid. Escaping page slices or racing `Close` can create stale mapped access.
- Go runtime signal ownership makes the exact isolated-worker contract a
  high-risk area. No runtime-internal linkname, swallowed signal, cgo fallback,
  or Rust-worker dependency is acceptable.
- OS-specific namespace, security, locking, flush, shrink, and crash behavior
  cannot be established by cross-compilation alone.
- Tight timing gates before local optimization would create noise. Loose CI gates
  before local proof would hide waste. Local matched profiles come first.
- Deleting the old tracked Go tree without an exact inventory could remove useful
  tests or independently correct codecs. No tracked file is deleted until the
  user approves the exact proposed deletion set.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- The repository needs a pure-Go peer, but its current Go tree implements an
  obsolete, incomplete, positional-I/O architecture. The accepted Rust reset
  changed both the storage architecture and the public semantic surface. A port
  must therefore follow the current specs and observable Rust behavior, not
  continue the old Go experiment or transliterate Rust internals.

Evidence reviewed:

- The full normative architecture and binary-format specifications.
- The complete accepted Rust public export inventory, README evidence, current
  corpus, and SOW 0019-0024 outcomes.
- Every current Go production file name, production/test counts, LOC, public API,
  wire constants, platform build tags, content-transfer calls, complete-page
  arrays, conformance references, and baseline tests/vet/formatting.
- Official current Go/OS mapping and unsafe-memory references plus the limited
  bbolt platform-wrapper evidence listed above.

Affected contracts and surfaces:

- `v4/go/` public SDK, internal physical format implementation, OS platform
  layers, fault worker binary, tests, benchmarks, source/runtime architecture
  gates, module metadata, README, and package documentation.
- `v4/conformance/` manifest, Go-produced fixtures, cross-read tests, malformed
  transformations, and mixed Rust/Go subprocess harnesses.
- Rust test/harness code only where minimal cross-language orchestration is
  needed. Rust production behavior, bytes, public API, and C ABI are frozen.
- Project instructions and a new Go runtime project skill after the workflow is
  proven.

Existing patterns to reuse:

- Rust's authority boundaries, semantic tests/models, public workflow reports,
  conformance generator method, necessary-work accounting, update-ipsets
  benchmark scenarios, source/mmap gates, native platform matrix, and final
  audit structure.
- Shared language-neutral `cases.json` and the current Rust-produced files as the
  first immutable-reader target.
- Current Go scalar key/cardinality code and tests only if literal vectors prove
  exact current behavior.
- Small OS-specific mapping wrappers as a pattern. Do not copy bbolt's format,
  freelist, remapping, global locking, page objects, or durability model.

Risk and blast radius:

- This is a replacement of an unreleased Go experiment and eventually affects
  nearly all `v4/go/` production code and tests.
- Data-loss risk is concentrated in COW ownership, dirty-page sealing, durability
  ordering, abort, cleanup, and outcome resolution.
- Memory-safety/process-crash risk is concentrated in mapped lifetime, checked
  offsets, concurrent reuse, physical faults, and signal/exception chaining.
- Interoperability risk is concentrated in little-endian codecs, canonical
  validation, structure/membership dictionaries, metadata compression, sidecar
  locks, and publication/resolver records.
- Performance risk is hidden copying, validation, allocation, synchronization,
  hashing, lookup, and maintenance in hot paths.

Sensitive data handling plan:

- Use only synthetic addresses, feeds, metadata, paths, fault cases, and public
  aggregate benchmark facts in durable artifacts.
- Authorized update-ipsets replay may use operational files locally, but durable
  evidence records only sanitized aggregate sizes, counts, timings, and shapes.
- Do not record credentials, source filenames, literal operational ranges,
  personal/customer/community data, private endpoints, or host aliases.

Implementation plan:

1. Produce the Milestone 0 parity/gap map before production edits: every public
   Rust semantic export and every normative operation maps to its Go API/module,
   test, and status; every old Go production file is classified retain/rewrite/
   delete; the exact proposed deletion set is presented for user approval.
2. Prove the platform foundation in the smallest permanent form: retained
   file-backed mappings, checked offset/view lifetime, flush/sync, identity,
   locks, growth/shrink behavior, and isolated fault-worker feasibility. Delete
   any exploratory code not converted into the final owner or a permanent test.
3. Implement the current portable codecs and one mmap-only immutable reader that
   cross-opens and semantically verifies all Rust-produced fixtures, including
   structured data. This is the first implementation-quality checkpoint for the
   user.
4. Implement one mmap-only physical writer core: final-offset page construction,
   COW range/tree edits, free/used bitmaps, retirement, checksum sealing at
   commit, meta publication, abort, and reclaim. Generate independently produced
   Go fixtures through public APIs.
5. Build the public advanced logical transactions over that core: direct,
   membership, feed catalog/references, common structured manager, and modular
   `NetworkEnrichmentV1` codec. Add randomized scalar-model tests before high-level
   workflows.
6. Build the typed high-level workflows as thin sequencing layers: immutable
   creation, feed lifecycle, direct replacement, first-seen, last-seen,
   membership import, history, metadata, snapshots, queries, joins, and algebra.
7. Complete live coordination, lifecycle transitions, publication/reservation,
   cleanup/housekeeping, commit resolution, and supported-platform behavior over
   the same cores.
8. Complete explicit validation, candidate inspection, best-effort recovery, and
   the version-matched Go worker. Reuse canonical codecs and final mapped-output
   builders; do not add healthy-path validation or a second writer.
9. Add bidirectional producer/cross-open and mixed Rust/Go subprocess gates. Run
   complete correctness, crash, resource, race/checkptr, fuzz/property,
   architecture, mmap syscall, cross-target, and authorized native matrices.
10. Port the Rust benchmark scenarios, measure locally operation by operation,
    profile every material difference, remove wasted work, and repeat until the
    final audit finds no actionable issue. Add loose CI disaster limits only
    after local acceptance.
11. Update package docs, Go README, conformance docs, project instructions, and a
    proven `project-v4-go` runtime skill. Complete the SOW only with implementation,
    artifacts, final audit, SOW move, and one closure commit.

Validation plan:

- Baseline and every milestone: `go test ./...`, `go vet ./...`, `gofmt`, race
  where supported, checkptr, leak/descriptor/residue checks, and exact changed-
  surface tests.
- Literal portable vectors for every integer, meta field, page header, record,
  hash, checksum, structured payload, sidecar/control block, and publication
  record. Cross-architecture codec execution must include a big-endian target or
  emulator.
- Public black-box semantic/property models for arrival-order normalization,
  direct/membership/structured transactions, workflows, commit/abort, and
  reference invalidation.
- Explicit malformed/corrupt corpus, fault injection, truncation, crash-point,
  cleanup retry, outcome resolution, worker chaining, and worker-build mismatch
  tests.
- Static source gates plus Linux runtime tracing prove mmap-only persistent
  content and the absence of complete out-of-map page images.
- Go-produced fixtures are built only through public Go operations, verified
  against `cases.json`, opened by Rust, and then committed. Rust fixtures are
  actually opened and checked by Go.
- Mixed subprocesses cover every section-21 direction and state; language-local
  unit tests are insufficient.
- Local benchmarks use the same fixture identities, work counts, timing
  boundaries, allocation/RSS/descriptor/file measurements, warm-up, isolated
  samples, and semantic-result checks as Rust. Profiles and necessary-work
  counters identify retained costs.
- Local cross-compilation covers supported target graphs. Runtime platform proof
  occurs only on user-authorized native Windows GNU, Apple ARM64, and FreeBSD 14
  environments; operational host details are never written to the repository.
- Before closure, run the exact final audit format below. If it reports a valid
  issue, fix it and repeat the entire audit rather than completing the SOW.

Artifact impact plan:

- AGENTS.md: add the proven Go workflow/guards and index the Go project skill at
  closure; do not weaken the current engineering philosophy.
- Runtime project skills: create `.agents/skills/project-v4-go/SKILL.md` only
  after commands, platform boundaries, and evidence are proven.
- Specs: change only if implementation exposes a genuine contradiction. Stop for
  a user design decision before changing behavior or bytes; otherwise record
  that the Go peer implements the existing specs unchanged.
- End-user/operator docs: add `v4/go/README.md` and package documentation for the
  completed SDK, worker installation, platform support, resource behavior, and
  reproducible verification/performance commands.
- End-user/operator skills: none currently exist; reassess at closure if a
  downstream/public workflow is introduced.
- SOW lifecycle: this remains the sole Go-port work item. Move it to `current/`
  only when implementation begins; close it only with all code/artifacts and the
  closure commit. SOW-0017 remains separate Phase 2.

Open-source reference evidence:

- `etcd-io/bbolt @ 01f7d9658a8a`
  - `bolt_unix.go:55-83`
  - `bolt_windows.go:65-119`
  - `db.go:454-570`
- This reference is evidence that Go can isolate basic Unix/Windows mapping
  mechanics behind small platform files. Its database architecture is not reused
  because it does not implement this format's mmap-only writer, external reader
  table, exact durability, recovery, or public semantics.

Open decisions:

- Decision 5A (NetworkEnrichmentV1 location representation) is open and
  awaits user ratification: the approved parity matrix specifies
  `Location *NetworkEnrichmentV1Location`, while the zero-allocation
  lookup contract (decision 4A) is implemented as `Location
  NetworkEnrichmentV1Location` plus `HasLocation`. The Go implementation
  stays as recorded until the user decides. Every other product and
  format decision is closed; the current specifications and accepted
  Rust semantics are frozen for this port.
- The obsolete Go deletion set from Milestone 0 was resolved by decision 1A
  (user decision, 2026-08-12) and executed at HEAD e65e8b7 (105 tracked
  files removed with the compiling replacement); no approval remains
  outstanding for it.
- If pure Go cannot meet the exact fault-worker contract without a new assembly
  or native boundary, stop and present evidence and options. Do not silently add
  cgo, use the Rust worker, reach into Go runtime internals, or weaken fault
  ownership/chaining.

Resolved decisions (2026-08-11, Milestone 0 closure):

- Deletion set (Decision 1): user chose C - decide after Milestone 1 evidence.
  The exact proposed set (100 tracked files + 2 untracked leftovers) is
  recorded in `.agents/sow/pending/pure-go-v4-port-milestone-0-report.md`
  section 7. No tracked file is deleted and no untracked leftover is removed
  before that decision; deletion, when approved, executes atomically with the
  compiling, tested Milestone 1 replacement.
- Fault-worker native boundary (Decision 2): user chose A - wait for the
  Milestone 1 feasibility evidence, then decide. No new native boundary
  (assembly shim, cgo, runtime internals, Rust worker) may be added before
  that decision.

## Resolved Decisions (2026-08-12, hot-path re-review)

The user adopted the external re-review's decisions after the reopening:

- Decision 1 (deletion set) = A with correction: the obsolete Go tree
  (internal/exactv4: 105 tracked files - 47 production + 58 test, the
  recorded "100" was stale - + untracked exactv4.test + empty directory)
  is removed in the FINAL Milestone 1 commit, not the first
  Milestone 2 writer commit. Before removal the verified scalar types must
  be relocated out of internal/exactv4 (v4/go/types.go is the only new-tree
  importer). .reasonix/ is preserved. Git history already preserves the
  deleted sources.
- Decision 2 (worker boundary) = A: a minimal project-owned assembly
  sigaction shim preserving the exact fault-isolation contract
  (binary-format-v4.md:3084); it affects validation/recovery workers only,
  uses no Go runtime code, cgo, or unwinding inside the handler, and is
  proven natively on each supported POSIX platform in the worker milestone.
- Decision 3 (closed-state class) = A with corrections: closed readers and
  pinned handles report WrongState (11); numeric code 9 remains HandleClosed (no
  table renumbering); WordCount becomes error-capable "(uint32, error)";
  under decision 4 per-view Release disappears and second-close errors
  apply to the reader and the pinned handle.
- Decision 4 (hot-path API shape) = A: caller-owned pinned reader handles.
  Pin once outside the workload; one pin may be shared across concurrent
  immutable lookups. Lookups, scans, view operations, and cursor steps are
  zero-allocation and zero-atomic. Views are lightweight values valid while
  their pin remains open; reader Close returns HandleBusy while pins exist;
  Pin close must not race its operations; insufficient feed-name buffers
  report BufferTooSmall plus the required size.
- Structure-kind authority conflict resolved: direct/membership + nonzero
  structure kind -> FormatInvalid (32); structured + unknown nonzero
  structure kind -> UnsupportedStructure (67); follows the mode-specific
  override at binary-format-v4.md:430 and current Rust bootstrap behavior
  (validate_direct/validate_no_structures -> KindInvariant; finish_open ->
  UnsupportedStructure for unknown codes on an otherwise valid structured
  meta).
- Decision 4A amendment (pin pointer contract, recorded at the re-review):
  Every Pin value references one shared private close state: pointer
  aliases (p2 := p1) and value copies (p2 := *p1) close the same logical
  pin, the count is decremented exactly once, and a second Close through
  any alias or copy reports WrongState. Pinned by
  TestPinPointerAliasSharesClose and the added value-copy tests
  (TestPinValueCopySharesClose, TestPinValueCopyKeepsReaderBusy).
- External audit close-out (resolved): the duplicated Cardinality129
  arithmetic and the duplicated public/internal error-code tables were
  centralized - internal/format is the single authority and v4/go/types.go
  + v4/go/errors.go re-export aliases, so the copies can no longer drift.

### 2026-08-12 - decisions 1A-4A implemented (hot-path contract)

- Deletion (1A): scalars relocated in-line into v4/go/types.go (no more
  internal/exactv4 import); the obsolete tree was then removed in the final
  milestone-1 implementation commit - 105 tracked files (47 production +
  58 test; the recorded "100" was stale) + untracked v4/go/exactv4.test and
  the empty v4/go/exactv4/ directory; .reasonix/ untouched; import-graph
  gate updated; git history preserves the sources.
- Worker boundary (2A): recorded for the worker milestone - minimal
  project-owned assembly sigaction shim, no Go runtime code/cgo/unwinding
  in the handler, proven natively per POSIX platform.
- Closed class (3A): closed readers/pins -> WrongState (11), second Close
  -> WrongState, code 9 = HandleClosed, WordCount -> (uint32, error), per-view
  Release removed (views are pin-valid values).
- Hot-path facade (4A): ImmutableReader.Pin / Pin.Close (one lifetime
  registration outside the workload; HandleBusy while pins exist);
  pin-level membership and enrichment lookups, reader-level direct
  lookups/scans/cardinality, and LookupFeedInto (caller buffer,
  BufferTooSmall + required size) are zero-allocation and zero-atomic
  (plain-state closed checks under the documented no-race-Close contract).
  Measured 0.000000 heap bytes/run for every hot path; atomics only at
  Pin/Close.
- Location shape (5A, recorded 2026-08-12 with the round-11 fix): the
  approved parity matrix writes `Location *NetworkEnrichmentV1Location`
  (milestone-0 report row 26). Decision 4A forbids per-lookup allocations,
  and a pointer inside a by-value result cannot reference stable storage
  without one; Rust itself models the field as
  `Option<NetworkEnrichmentV1Location>`. 5A: express the option as
  `Location NetworkEnrichmentV1Location` + `HasLocation bool`, with the type
  name and fields matching the matrix and the Rust authority exactly. The
  closure report surfaces this for user veto.
- Structure-kind rule: direct/membership + nonzero structure kind ->
  FormatInvalid; structured + unknown nonzero kind -> UnsupportedStructure;
  pinned by reader and format tests (fails on the pre-fix tree).
- Counts at reopening are recorded in the Status entry above (production 4,797
  raw / tests 4,676 raw), taken after the meta-precedence parity fix, the
  sole-meta kind regression test, the final-review regression guards, and
  the public tag/metadata-limit API completion.
  Gates: go test ./... (4 packages) incl -race, vet, gofmt, import graph,
  SOW audit - all green.

### 2026-08-12 - six-reviewer re-verification after decisions 1A-4A

- All six reviewers re-reviewed the rebuilt tree at their disjoint briefs
  after the decisions implementation. Findings during the round and their
  closures:
  - Pin value-copy P2 (mapping reviewer): a dereference copy of a Pin
    could double-decrement the pin count. Closed as a formal decision-4A
    amendment: Pin is a pointer type; aliasing the pointer shares the
    single close state; value copies are unsupported like C opaque
    handles, with typed-error-only consequences; pinned by
    TestPinPointerAliasSharesClose (fails on a double-decrement).
  - Record-drift findings (conformance/reports reviewer): report sections
    2 and 12 still described the pre-deletion 5-package tree and the
    view-guard zero-alloc contract; the SOW current-state entries said "5
    packages". All fixed at 2fdcce4; dated historical entries keep their
    then-accurate wording.
  - External audit close-out (resolved): Cardinality129 and error-code
    tables centralized on the single internal/format authorities; the
    public package re-exports aliases.
- Final verdicts at HEAD 2fdcce4: codecs PASS; bootstrap/meta/direct
  ranges PASS (kind matrix verified pre-fix-failing); membership/blob/
  structured/metadata PASS (metadata rewrite verified incl. empty-present
  and incomplete-final-block probes); mapping/lifetimes/platform PASS
  (pin contract amendment verified); public API/errors/zero-alloc PASS
  (12 public + 8 internal checks at exactly 0 allocs, zero atomics in hot
  paths, WrongState class, LookupFeedInto BufferTooSmall semantics);
  conformance/tests/reports PASS (counts verified with the documented
  method and recorded in the close-out entry; deletion of 105 files
  verified exactly; records internally consistent).
- Gates at HEAD 2fdcce4: go test ./... (4 packages) incl -race, go vet,
  gofmt, import graph, 9-target cross-compile matrix, SOW audit - all
  green. Milestone 1 (immutable reader) review gate: CLOSED.

## Implications And Decisions

1. **Long-term-best: semantic peer, not incremental compatibility.**
   The current Go experiment is not a supported predecessor. Current specs and
   observable Rust semantics win every conflict. Reuse requires proof.
2. **Long-term-best: pure Go.**
   No cgo or Rust runtime dependency. The Rust-provided C ABI remains untouched.
3. **Long-term-best: one physical authority.**
   Internal reader/writer owners implement persistent operations once; public
   advanced and high-level APIs are logical wrappers.
4. **Long-term-best: mmap-only persistent content.**
   Lifecycle and durability syscalls remain necessary, but content-transfer I/O
   and complete out-of-map pages are prohibited.
5. **Long-term-best: semantic conformance.**
   Go and Rust files need not be byte-identical, but every observable value,
   error class, outcome, and cross-process coordination state must agree.
6. **Long-term-best: correctness before optimization, optimality before CI
   standardization.**
   Implement one authoritative path, measure its necessary work, remove waste,
   approve local performance, then add loose CI disaster gates.
7. **Minimal-complete scope.**
   All unsigned Phase-1 Go semantics and their consequences are in scope. C ABI,
   signing, v3, old-v4 compatibility, update-ipsets downloader/parser changes,
   and speculative structures are not.
8. **Controlled handoff.**
   Milestone 0 is an evidence checkpoint; the immutable cross-reader is the first
   code-quality checkpoint. No remote push, native-host access, tracked deletion,
   format change, or new native boundary is authorized merely by this SOW.

## Plan

1. **Milestone 0 - exact gap and replacement map.** Read-only inventory, parity
   matrix, proposed Go API/module graph, current-file classification, deletion
   approval list, risk register, and projected size. No production edit.
2. **Milestone 1 - portable mapped immutable reader.** Exact current codecs,
   mapping/lifetime owner, public immutable reader, all Rust fixture cross-reads,
   malformed bootstrap rejection, zero-allocation lookup/scan evidence, and the
   first platform/worker feasibility report.
3. **Milestone 2 - mapped COW writer and Go producer.** One physical writer,
   current allocation/retirement/commit rules, direct/membership/structured
   construction, public generation of the Go corpus, Rust cross-open.
4. **Milestone 3 - complete logical SDK.** Advanced transactions, typed
   workflows, metadata, queries, joins, algebra, snapshots, reports, cancellation,
   cleanup, and randomized public models over the internal core.
5. **Milestone 4 - live/platform/recovery completion.** Sidecar coordination,
   lifecycle and publication resolvers, supported-platform boundaries, explicit
   validation/recovery, worker, crash/fault/resource proof.
6. **Milestone 5 - cross-language and performance acceptance.** Mixed-process
   matrix, native proof, representative update-ipsets replay, matched Rust/Go
   benchmarks, profiles, necessary-work audit, code-size/duplication audit, docs,
   skill, and repeated clean final audit.

Every milestone report records:

- exact files changed and commits;
- behavior completed and behavior still missing;
- commands and factual results;
- production LOC, largest files/functions, allocations/resources, and benchmark
  deltas relevant to that milestone;
- same-failure and duplicate-authority searches;
- deviations or new decisions requiring the user; and
- whether the next milestone is safe to start.

## Required Final Audit Format

Use these sections in this order:

1. **Executive conclusion** - accepted or not accepted, with no hedging.
2. **Scope and source graph** - every production source classified and compiled.
3. **Single authority and two-level API** - physical owner map and bypass search.
4. **Hot-path necessary work** - counters, profiles, allocations, copies, syscalls,
   checksums, synchronization, and rejected optimizations.
5. **Correctness and durability** - public models, failures, commit/abort/crash,
   cleanup, reclamation, and outcome resolution.
6. **mmap-only and bounded resources** - static/runtime proof, heap/RSS/VM,
   descriptors, scratch, sparse scaling, and no complete page copies.
7. **Cross-language conformance** - both producer sets and both subprocess
   directions actually executed.
8. **Portability** - cross-target compilation versus separately identified native
   runtime evidence and exact unsupported boundaries.
9. **Maintainability** - production LOC, largest files/functions, complexity,
   clone/similarity results, dead code, layering, and justified exceptions.
10. **Findings and iteration record** - every finding, repair, repeat result, and
    remaining issue. Any remaining actionable issue means not accepted.

## Execution Log

### 2026-08-11 - planning and clean handoff

- Committed and pushed ignore rules for local generated build trees, stamp files,
  and Go test executables as commit `900b345`.
- Verified local `master` and `origin/master` at that commit with no visible
  uncommitted or untracked generated state.
- Updated the Rust and conformance README status text to record that the proven
  Rust result now authorizes this pending Go port; no runtime behavior changed.
- Read the complete current architecture/binary specifications, current/pending
  SOWs, Rust project skill, accepted Rust public/benchmark evidence, conformance
  corpus, and current Go production/test tree.
- Ran the current Go test, vet, and formatting baselines successfully.
- Confirmed no Go implementation edit was made. This file defines the future
  port; it does not start it.

### 2026-08-11 - Milestone 0 completed (read-only)

- Files changed: created
  `.agents/sow/milestones/SOW-0025-milestone-0-report.md` (full inventory,
  parity matrix, proposed module/API graph, per-file classification, exact
  deletion set, risk register, size projection); appended this execution-log
  entry. No production or test file in `v4/go/` was touched.
- Commits: `972960a` is the pre-milestone HEAD; the report and this entry are
  committed together as the Milestone 0 closure commit (local `master`, no
  remote push — per the controlled-handoff rule).
- Commands and results: `go test ./...` exit 0, `go vet ./...` exit 0,
  `gofmt -l .` empty, at HEAD `972960add710` with a clean tree. Measured:
  50 production files / 44,088 newline-counted lines, 59 test files / 37,403
  lines, 4 files with content-transfer calls, 24 files with complete page
  arrays, zero conformance references, 64 matched error codes (65-69 missing),
  sidecar stale magics/layouts, Rust production base 82,516 lines (livedb).
- Milestone 0 report outcome:
  - transfer files (6 production + 5 tests): `errors.go`, `key.go`,
    `cardinality.go`, `page.go`, `name_binding.go`, `process_identity.go`
    and their tests — each re-verified against literal vectors in Milestone 1
    before reuse;
  - proposed tracked deletions: 98 files (44 production + 54 test) — exact
    list in the milestone report, awaiting user approval (Decision 1);
  - fault-worker native-boundary policy: pure-Go feasibility proof is a
    Milestone 1 exit criterion; minimal project-owned assembly shim requires
    user decision (Decision 2);
  - projected Go production size: ~32-44k lines (midpoint ~37k), ~40-53% of
    the Rust base;
  - next milestone safe to start once Decisions 1 and 2 are answered.
- Same-failure and authority searches: content-transfer (4 files), complete
  page arrays (24 files), stale constants (retention 2 files, 1 MiB limit 1
  file, v3 zero), structured/threat/conformance references (zero) — each class
  is re-searched as a gate in later milestones; the old tree has one read and
  one write boundary internally, but that authority design is the rejected
  one, so the new tree mechanically enforces owner boundaries with
  `v4/go/check-import-graph.sh` (import boundary gates, run in the same gate
  set as the Rust `check-source-graph.sh`).
- No deviation from the approved SOW plan. Decisions requested from the user:
  (1) approve the 100-tracked-file + 2-untracked-leftover deletion set;
  (2) worker boundary: reopened by the milestone-1 evidence — SetPanicOnFault
  proves only a panic-based subset, not the spec-exact SA_SIGINFO contract
  (milestone-1 report §11); the user chooses between a minimal project-owned
  assembly sigaction shim and a spec change. Both are recorded in the
  milestone report with evidence and recommendations.

### 2026-08-11 - Milestone 0 corrected after external review (read-only)

- An external review of the milestone report found material errors. Every
  finding was verified against the sources before any change; all verified
  findings were corrected, one claim was not reproducible, and the two
  original recommendations were withdrawn as unsafe:
  - `process_identity.go` moved from transfer to delete: it implements a
    PID/process-start stale-slot recovery model (`process_identity.go:15`,
    `sidecar.go:394-396`) that the current spec explicitly excludes
    (`binary-format-v4.md:2130-2138`). Its test was moved to the deletion set
    too. Deletion set is now 100 tracked files (45 production + 55 test).
  - Error-code mapping corrected: names match Rust 1-64 in every position
    except code 46 (Go `ErrorLiveCoordinationDomainMismatchRequiresReset` vs
    Rust `LiveCoordinationMalformedRequiresReset`, `sdk_error.rs:58`); the
    report no longer claims an exact match and transfer requires resolving
    code 46 plus adding 65-69.
  - Prohibited-I/O classification corrected: content-transfer calls are
    `ReadAt`/`WriteAt`/`unix.Pread` in 3 files (`os_linux.go:503,565`,
    `page_source_linux.go:27`, `page_source_other.go:26`); `Truncate`/`Sync`
    are required lifecycle/durability syscalls and are not prohibited.
  - Decision recommendations changed to evidence-first for both decisions
    (deletion executes atomically with the compiling, tested Milestone 1
    replacement; fault-worker boundary is decided after the Milestone 1
    feasibility evidence, per the SOW's own stop-for-decision wording).
  - Design-spec line count corrected (519, not 530); exported-symbol report
    corrected to measured values (199 top-level exported declarations; 43 of
    50 files export nothing) and no longer labeled a "public SDK surface";
    size projection explicitly labeled estimate-only, not a target.
  - The report moved from an undocumented `.agents/sow/milestones/` directory
    to `.agents/sow/pending/pure-go-v4-port-milestone-0-report.md`, the best
    interpretation of the review's "wrong SOW path" finding. The reviewer
    later retracted that finding ("the original path was correct; moving the
    report was unnecessary, although harmless"). The report stays at the new
    location; both the SOW and this log reference the new path.
  - `.reasonix/` is harness session state, untracked by design; it is not repo
    content and was not committed.
- Files changed: `.agents/sow/pending/pure-go-v4-port-milestone-0-report.md`
  (moved and corrected, 496 lines), this execution-log entry. Still zero
  production/test edits in `v4/go/`.
- Commits: `579c1a3` (initial Milestone 0 report + log), `04032d6` (verified
  corrections, report moved to `pending/`, SOW correction log entry),
  `b91301d` (removal of the superseded old report path). All local, no remote
  push.
- Milestone 1 begins with moving SOW-0025 to `.agents/sow/current/` with
  `Status: in-progress`; it remains `open` in `pending/` while implementation
  has not started.

### 2026-08-11 - Milestone 1 completed (implementation checkpoint)

- Moved SOW-0025 to `.agents/sow/current/` with `Status: in-progress`
  (commit `0de8793`) and implemented the milestone:
  - New production packages: `internal/format` (wire codecs, meta identity
    and kind invariants, page/slotted, range, catalog, membership,
    structured, blob, metadata chunks, 129-bit cardinality, single 1-69
    error-code table with code 46 per the current Rust contract), `internal/
    mapping` (the only mapping owner; POSIX + honest Windows stub),
    `internal/reader` (the only healthy-generation reader core), and the
    public facade `reader_public.go` at the module root. ~3,720 new
    production lines, ~1,700 test lines.
  - Conformance: all six committed Rust fixtures open and verify with exact
    `cases.json` semantics (metadata states, full-IPv6 cardinality strings,
    boundary probes, 70 feed names, word-level bitmaps incl. blob-backed and
    1 MiB metadata, structured values + threat memberships); all three
    invalid mutations rejected with code 32.
  - Zero-allocation evidence: direct v4/v6, membership (inline + blob),
    structured, feed (internal), scans, cardinality = 0 allocs; public feed
    lookup = exactly the returned string copy. Recorded in both root and
    reader zero-allocation tests.
  - Portability: cross-compiles for darwin/freebsd/windows/linux-arm; Windows
    open refuses with os-unsupported stub; runtime proof on Linux amd64.
  - Worker feasibility: pure Go disproven for POSIX with an empirical probe
    (runtime-fatal `unexpected fault address`, os/signal never delivers
    mapping SIGBUS, no si_addr/sigaction surface in x/sys); Windows vectored
    exception path is pure-Go feasible. Fallback = minimal project-owned
    assembly sigaction shim; per recorded Decision 2 the user decides with
    this evidence.
  - Commits: `0de8793` (SOW move), `913f4e6` (reader + tests),
    `9441f85` (independent-review repairs: blob-branch txn threading,
    slotted-page bounds, metadata allocation bound, catalog reserved bytes,
    synthetic two-leaf blob regression database), `1df90fa` (milestone
    report), `4eec44e` (third-pass repairs), `03a910f` (fourth-pass
  repairs: borrow-count lifetime, view API, absence-vs-corruption,
  concurrency race tests, worker and report fact corrections,
  check-import-graph.sh gate, view-API ID removal, callback-error
  passthrough, conformance enumeration, multi-level range-tree test,
  corrected worker conclusion), `1e1ac4b` (fifth-pass repairs: meta tail
  zero invariant, mandatory aux checks, exact zlib stream verification,
  namespace safety under the lifetime lock, structure payload decode at
  lookup, public error typing, adversarial regression suite). No tracked
  file deleted (Decision 1 = C).
  - Full report: `.agents/sow/pending/pure-go-v4-port-milestone-1-report.md`.
    Baseline gates re-run: `go test ./...` (incl. race), `go vet`, `gofmt`,
    cross-compilation matrix — all green; SOW audit clean.
- At this historical checkpoint, both then-pending decisions had their evidence:
  the deletion set (then counted as 100 tracked files + 2 untracked leftovers;
  later corrected to 105 tracked files) and the worker
  boundary — the third-pass evidence demonstrates only a panic-based fault
  subset via `runtime/debug.SetPanicOnFault` (empirical recover with exact
  fault address); the spec-exact worker contract needs either a minimal
  project-owned assembly sigaction shim (spec-exact) or an explicit
  spec change. Both decisions were subsequently resolved as 1A and 2A. The
  contemporaneous statement that Milestone 2 would be safe after those answers
  is superseded by the later Milestone 1 reopenings.
- Commands and results: `wc -l` design spec 519; `grep` evidence for error
  code 46 in `errors.go:54` and `sdk_error.rs:58`; sidecar/spec magic and slot
  layout at `sidecar.go:11,15,394-396` vs `binary-format-v4.md:2104,2130-2138`;
  I/O call sites listed above; baseline gates unchanged (already green).

### 2026-08-11 - gap analysis and repair pass (six-agent)

- `94723aa` records the fifth-pass repairs in this log and the M1 report.
- `9a835e4` adds `.agents/sow/pending/pure-go-m1-gap-analysis.md`: six
  concurrent read-only subagents, each with a disjoint brief (codecs, reader
  semantics, architecture, public API/errors, test evidence,
  worker/mapping/report facts) at HEAD `94723aa`. Result: one BLOCKER (B1,
  structure radix divisor one level too deep) and ten MAJOR findings,
  plus three MINOR and one refuted claim.
- `58c4d8f` repairs every verified finding with a regression test: B1 span
  fix, blob-walk record/geometry validation, slotted exactness, OFD lifetime
  lock, API binding guards, absence/word-exact conformance evidence,
  honest LOC, import-graph gate strengthening (every import checked +
  sync/sync-atomic/unsafe ban), and bootstrap minima. No tracked file
  deleted.
- `bb7f485` corrects the SOW worker-feasibility sentences and the report's
  stale claims to match the corrected section 11.
- Gates re-run green at `bb7f485`: `go test ./...` (5 packages, incl.
  -race), `go vet`, `gofmt -l`, `check-import-graph.sh`, 9-target
  cross-compile matrix.

### 2026-08-12 - mandatory six-agent review round 2 and fix pass

- Per the mandatory iterative review gate (one review round per milestone
  before it can close), six new concurrent reviewers with disjoint briefs
  (codecs / bootstrap+ranges / membership+structured+metadata / mapping+
  lifetimes+platform / public API+errors+zero-alloc / conformance+reports)
  reviewed HEAD `bb7f485`. Verdicts: 4 FAIL (codecs 1 P1 + 4 P2; bootstrap
  1 P1 + 1 P2; membership 1 P1 + 3 P2; mapping 1 P1 + 3 P2), 1 FAIL on
  paperwork (reports 5 P1 + 5 P2), 1 PASS-with-2-P1 that are the already
  recorded closed-state error-class user decision. No P0.
- Fixed in this pass (all regression-pinned):
  - catalog `feed_index >= feed_index_limit` now corruption
    (`internal/reader/catalog.go`, `guard_regression_test.go`).
  - kind-classification: registered-but-invalid combinations (structured
    kind 0, direct/membership kind 1) report FormatInvalid (32); only
    unknown kinds report UnsupportedStructure (67)
    (`internal/format/meta.go`).
  - dangling structure reference now the typed corrupt error (structure
    range names an absent structure ID), matching the membership twin
    (`internal/reader/structure.go`).
  - FreeBSD immutable opens now use the canonical whole-file shared flock
    lifetime lock instead of refusing every open (`mapping_lifetime_freebsd.go`).
  - `Mapping.View`/`Page` after Close report the typed wrong-state error;
    `Mapping.Close` is idempotent (`internal/mapping/mapping.go`).
  - metadata zlib FCHECK validated; `deflateStreamLen` probes bounded at
    the declared length (CPU-amplification fix) (`internal/reader/metadata.go`).
  - blob leaf `%8` alignment explicit; checked blob extent arithmetic;
    blob branch validates every probed entry's child page
    (`internal/reader/membership.go`).
  - `publicError` preserves the typed code through wraps; error-code names
    59/62/69 aligned to the Rust `Id` spelling.
  - `check-import-graph.sh` now bans content-transfer I/O in production
    sources and the stdlib `syscall` package.
  - test hygiene: public zero-alloc suite releases its v6 view; structured
    conformance absence probes added; Info()-after-close pinned; literal
    byte vectors added for the v6 range, membership leaf/branch, structure
    record, enrichment payload, and blob-branch codecs.
  - report facts corrected (honest production LOC 4,492 raw / tests 3,794
    raw, zero-alloc table labels, OFD lock wording, metadata allocation
    overhead 0-88 bytes) and this SOW log repaired.
- Commits: see the round-2 fix commit. Gates green after the pass; the six
  reviewers re-review the repaired tree before Milestone 1 closes.

### 2026-08-12 - six-agent review round 3 (final verification)

- All six reviewers re-reviewed the round-2 tree at HEAD with their same
  disjoint briefs. Verdicts: codecs PASS, bootstrap/ranges PASS,
  membership/structured/metadata PASS, public API/errors/zero-alloc PASS
  (fixable issues outside the recorded closed-state decision: none),
  mapping/lifetimes PASS after the gate fix, conformance/reports PASS after
  this record.
- Commits and fixes between the round-2 record and HEAD `3b4f3d5`:
  - `a5d7cf8` + `78cebc4` — import-graph gate comment stripping (line and
    stateful multi-line block), verified both directions.
  - `9203c28` — regression pins: `TestSoleMetaGeometry`,
    membership-kind1/kind2 subtests (both fail on the pre-fix code).
  - `e0a1687` — blob path zero-allocation: value-pair expected-level state;
    internal zero-alloc suite now measures blob word/scan at 0.
  - `a348c42` — real family-min and from-1 conformance absence probes;
    explicit `94723aa` log line.
  - `3b4f3d5` — P0 fix: blob coverage check underflowed when the request
    fell past the selected leaf's end (`end-off` wrapped), allowing a
    silent out-of-leaf word read or a slice panic on crafted files; the
    explicit `off > end` guard restores corruption semantics and
    `TestBlobGapRejectedCorruption` pins it (fails pre-fix in both modes).
- Report counts corrected at HEAD: production 4,500 raw lines (tests
  excluded), tests 3,922 raw, zero-allocation checks 18 (8 internal + 10
  public). (Superseded: the round-4 follow-up entries below carry the
  verified counts at each later HEAD.) Milestone report sections 11c-11d record both passes; the
  reviewers' final verdicts are all PASS with no P0-P2 remaining.
- Gates at HEAD: `go test ./...` (5 packages, incl. -race),
  `go vet`, `gofmt -l`, `check-import-graph.sh`, 9-target cross-compile
  matrix — all green; SOW audit clean.
- Pending user decisions unchanged: (1) deletion set — 100 tracked files +
  2 untracked leftovers, atomic with the Milestone 2 writer commit;
  (2) worker boundary — assembly sigaction shim vs spec change vs drop;
  (3) closed-state error class — HandleClosed (9) vs WrongState (11),
  Release idempotency, WordCount released-view silent-0; plus the recorded
  authority conflict (unknown nonzero structure_kind on direct/membership:
  spec text 67 vs Rust 32).

### 2026-08-12 - external audit pass (verified, all six findings real)

- An externally run audit of the round-3 tree reported six correctness and
  coordination failures plus performance waste. Every claim was verified
  against code, the spec, and the Rust reference before any change; all six
  correctness findings reproduced and were fixed with regression tests:
  1. View copies could double-release one borrow: two copies of one public
     view each decremented the reader's child count, so a later Close could
     succeed while a live child existed (use of the closed mapping). The
     released flag moved from the view value into a shared viewGuard with an
     atomic CAS; copies of a view now share one borrow, second Release is a
     no-op, and every copy reports HandleClosed after release. Public
     lookups that return a view now pin exactly one small guard allocation
     (the copy-safety cost; mapped traversal stays zero-alloc).
  2. Immutable sidecar handling: a present canonical `.readers` sidecar
     returned LiveCoordinationUnsupported (44) instead of the Rust WrongMode
     class (11), and a dangling `.readers` symlink was accepted as absent.
     Uses os.Lstat (symlink-aware, mirroring fs::symlink_metadata) and code
     11; pinned by TestSidecarPresence.
  3. Immutable lifetime lock was non-blocking F_OFD_SETLK; Rust blocks
     (F_OFD_SETLKW) while a writer holds the exclusive lock. Both Linux and
     macOS now use the blocking form (darwin F_OFD_SETLKW = 91).
  4. Path identity was verified only before the lock; Rust rechecks after
     locking and after mapping. mapping.OpenImmutable now re-verifies
     identity and re-runs the namespace check at all three points.
  5. validateStructuredMeta omitted structure_entry_count <
     structure_id_limit (Rust CountInvariant); both metas with
     entry_count == id_limit opened. Added the check; pinned by
     TestStructureEntryCountBound.
  6. Catalog name-branch keys were not grammar-validated (Rust decode_entry
     validates leaf and branch names through one decoder). DecodeCatalogName
     Branch now rejects invalid names; pinned by TestCatalogBranchNameGrammar.
- Performance waste, all fixed:
  - metadata stream validation re-inflated the payload O(log n) times (the
    1 MiB fixture twelve times); replaced with one single-pass inflation
    whose consumed-byte position (flate reads byte-at-a-time from a
    bytes.Reader) proves the exact stream end. ~1.4x measured on the
    micro-benchmark and far fewer allocations; trailing-byte, truncation,
    and Adler checks preserved (TestMetadataTrailingBytesRejected passes).
  - ReadWords decoded the whole record or walked the blob tree once per
    word; now one record decode (inline) or one blob walk (blob) per batch.
  - range/catalog/membership/structured descents pre-read the root page and
    re-decoded every page header inside OpenSlotted; the root pre-read is
    gone (first iteration captures the level) and OpenSlottedHeader reuses
    the already-decoded header (page.go).
  - check-import-graph.sh comment stripper treated `//` inside string
    literals as a comment (a real call after a string containing `//` could
    bypass the gate); the stripper is now a quote-aware awk state machine,
    and in-memory decompression reads (consumedReader) are the documented
    exemption.
- CORRECTED non-finding (2026-08-12 re-review): the earlier claim that the
  per-call atomic in the public facade is parity with the frozen C ABI
  (handle.rs Gate::enter gates every C call) was wrong in mechanics. Rust
  reader lookups are a plain Option check (ReaderHandle::get, no gate, no
  atomic); the C facade's real costs are one Arc::clone (atomic refcount)
  and one Box per view-handle lookup, and view ops carry the caller-
  serialized AtomicBool fail-fast gate. The binding Go criteria are
  SOW-0025:175 (zero Go heap bytes for warm point lookups/cursor steps) and
  design-iprange-engine.md:373/:404; the round-4 facade (1 guard alloc per
  view lookup, 1 string alloc per feed lookup, per-call atomic load) does
  not meet them. Open as the hot-path API decision, not a resolved
  interpretation.
- Gates at HEAD: go test ./... (5 packages, incl. -race), go vet, gofmt,
  import-graph (with quote-aware stripper), 9-target cross-compile matrix —
  all green; zero-alloc suite updated for the one-guard-per-view contract;
  report counts and this record updated in the same commit.

### 2026-08-12 - hot-path contract re-review (milestone 1 reopened)

- An external re-review after the round-4 closure re-examined the public
  facade against the frozen performance contract. Verified (measurements
  reproduced at HEAD): membership lookup+word+release = 1 allocation (16B
  guard) and 2 atomic ops; feed lookup = 1 allocation (string); direct
  lookup/scan = 0 allocations but 1 atomic load each; every view op adds 1
  atomic load. SOW-0025:175, design-iprange-engine.md:373/:404 and the
  binary-format-v4.md:2537 checked-word_count requirement are not met by
  the round-4 facade. The earlier "Gate::enter parity" justification was
  wrong in mechanics (Rust reader lookups are a plain Option check; SDK
  core is atomic- and allocation-free; the C facade pays one Arc clone +
  one Box per view-handle lookup and gates view ops).
- Fixed now (no API change): metadata staging at internal/reader/metadata.go
  allocated the worst-case bound and then grew an io.ReadAll output (~2.3
  MiB / dozens of allocations for 1 MiB although exact lengths are declared
  in the selected meta). The chain now allocates exactly its declared
  compressed length (bootstrap bounds make it a safe capacity) and
  decompression reads into one exact output allocation with a one-byte
  overflow probe; truncation/trailing/Adler checks unchanged, pinned by the
  existing tests.
- HISTORICAL OPEN ITEM (subsequently resolved as decision 4A): the public facade
  API shape that closes the
  gap — caller-owned pinned lookup handles with token-style views and zero
  allocations/atomics inside the hot loop (long-term-best, recommended) vs
  keeping the guard facade with a SOW/spec amendment. Also unresolved at that
  checkpoint: the
  facade moves to the WrongState closed class, WordCount becomes
  error-capable, and Release/second-close semantics are decided explicitly
  (decision 3 corrections from the re-review). Reopened milestone 1 does
  not close before the hot-path contract is met.

### 2026-08-12 - external audit round-4 reviewer follow-up (mapping)

- The bootstrap/mapping reviewer's round-4 P2 was that the mapping owner
  still lacked regression tests for (a) the blocking F_OFD_SETLKW wait
  semantics and (b) the post-lock/post-mmap path identity recheck.
- Writing test (b) exposed that the shipped three-point recheck compared
  the opened fd against the initial path stat — a comparison that can never
  fail once the fd is open — so a replacement after open could still publish
  a mapping of the old unlinked inode while the path named a new database.
  Corrected at v4/go/internal/mapping/mapping.go: every recheck now
  re-stats the path itself with os.Lstat (symlink-aware, like Rust
  fs::symlink_metadata) and requires it to still name the opened inode;
  a mismatch or non-regular path entry under the lock is the WrongState
  class (code 11), matching Rust WrongMode ("live path identity changed").
  The initial pre-open stat remains only as the early non-regular-file gate
  and no longer vetoes the opened file, matching Rust's
  open-what-the-path-names semantics.
- TestOpenImmutableRefusesPathReplacedDuringOpen fails on the pre-fix tree
  and passes after the correction; TestOpenImmutableWaitsForExclusive-
  LifetimeLock pins the blocking wait (fails on a non-blocking lock).
- The darwin lifetime lock now retries EINTR in the wait loop, matching the
  linux peer and the Rust live_lock platform module (one loop for
  linux+apple), at v4/go/internal/mapping/mapping_lifetime_darwin.go.
- Counts at HEAD refreshed (4950366): production 4,592 raw lines / tests
  4,196 raw lines (report sections 2 and 11f). Gates at HEAD: go test ./... (5
  packages, -race, mapping -count=3), go vet, gofmt, import-graph, five
  cross-compiles (darwin/amd64, darwin/arm64, freebsd/amd64, windows/amd64,
  linux/386) — all green.

### 2026-08-12 - external audit round-4 follow-up 2 (membership P0)

- The membership/structured/metadata reviewer's round-4 verdict found one
  remaining P0 in batched blob-membership reads: the earlier
  single-descent blob case still issued one span request, so ReadWords
  crossing a blob-leaf boundary failed as corruption on a conforming file
  (reproduced on the committed synthetic two-leaf blob database with
  `ReadWords(505, 4)`: "blob leaf does not cover the requested bytes"),
  while per-word Word() succeeded and the Rust reference loops per leaf
  (blob_tree.rs read_words_from).
- Fixed in v4/go/internal/reader/membership.go: the traversal split into
  blobLeaf (one descent to the covering leaf, returning its mapped bytes
  and logical start) plus the single-span blobRead wrapper; the batched
  path now loops per covering leaf, copies min(available, remaining)
  words, advances, and keeps the no-advance guard and the trailing-zero
  word canonical check.
- TestBlobReadWordsAcrossLeafBoundary (blob_test.go) fails on the pre-fix
  tree with the exact reported error and passes at HEAD; blob per-word
  reads and all 8 internal zero-alloc subtests remain green.
- Counts at HEAD (ac6bef1): production 4,634 raw lines / tests 4,252 raw
  lines (report sections 2, 11f, 11g).

### 2026-08-12 - external audit round-4 follow-up 3 (mapping P2 + records)

- The mapping/lifetime reviewer's re-verification found one remaining P2:
  an unlinked (not replaced) path mid-open mapped the Lstat failure to
  CodeIO (31), while Rust verify_path_inner refuses with NameNotFound (18).
  Fixed at v4/go/internal/mapping/mapping.go (os.IsNotExist ->
  CodeNameNotFound before the IO fallback); pinned by
  TestOpenImmutableRefusesPathUnlinkedDuringOpen.
- The conformance/reports reviewer's P2 was record lag only: the round-3
  "counts corrected at HEAD" sentence in the external-audit entry is now
  explicitly annotated as superseded by the round-4 follow-up entries;
  report section 13 now lists section 11e; every historical "Counts at
  HEAD" line carries its commit.
- Counts at HEAD (this commit): production 4,639 raw lines / tests 4,308
  raw lines. Gates: go test ./... incl -race (mapping -count=3), go vet,
  gofmt, import graph — all green. Gates at HEAD: go test ./... incl -race,
  go vet, gofmt, import graph — all green.

### 2026-08-13 - round-45 final review mmap-gate denylist gaps closed (HEAD 14c0698)

- The round-44 re-verification completed with all six narrow reviewers at
  PASS (HEAD e5fea20). The round-45 full-scope final review then failed
  with three P2 findings, all in the mmap source gate: os.CopyFS was
  absent from the selector ban (a directory copy streams artifact bytes
  with no banned selector; the live reproducer exited 0);
  os.OpenInRoot/os.OpenRoot were absent from the file-producer table (a
  Go 1.26 OpenInRoot *os.File, or an older-toolchain *os.Root handle,
  reached flate.NewReader untainted and streamed file bytes, and
  Root.Open/Create/OpenFile also produce files); and the blanket-approved
  x/sys surface still carried descriptor-transfer primitives
  (unix.Tee, unix.Vmsplice, unix.IoctlFileClone/CloneRange/DedupeRange,
  darwin unix.Clonefile/Clonefileat).
- Fixed at HEAD 14c0698: CopyFS, Tee, Vmsplice, IoctlFileClone* and
  Clonefile* join the banned selector set (CopyFileRange/Sendfile/Splice
  were already banned); os.OpenInRoot and os.OpenRoot join the file
  producer table as position-0 file taints, so every Root method outside
  the approved lifecycle surface fails closed; all three live reproducers
  plus the OpenRoot/ReadAll and darwin Clonefile variants are rejected.
- Pinned as self-test forms 249-251; the durable rejection set is now two
  hundred one mutation forms. The adversarial re-review of this fix
  then proved a P0 in the same class: a *os.Root stored in a struct
  field (h := gateRootField{r: root}; h.r.Open(name)) dropped the
  file taint, so the returned *os.File reached flate.NewReader
  untainted and the stream was consumed through the exact inflater
  exemption shape (gate exit 0, /tmp reproducer); the type model now
  resolves *os.Root as a file-bearing type everywhere *os.File does
  (struct fields, parameters, helper returns, type assertions,
  func/chan elements, method results), pinned as self-test form 252,
  raising the durable rejection set to two hundred two mutation forms.
- The P0 closure landed at HEAD 262756c on top of the round-45 gate fix
  (14c0698, records 70dcc42); the record trail and counts above reflect
  the full chain 14c0698 -> 70dcc42 -> 262756c -> e1410eb.
- The following adversarial re-review then found three producer-value
  P0 escapes, all proven live with the metadata-exemption consumption
  chain at gate exit 0: file method values (open := root.Open;
  open(name)), initialized func-typed variables with file-bearing
  declared results (var newRoot func(string) (*os.Root, error) =
  os.OpenRoot and the pre-existing *os.File form var openPath
  func(string) (*os.File, error) = os.Open), and stdlib producer
  values bound without a declared type (openPath := os.Open). Fixed
  in v4/go-gate/main.go: the file method in value position is checked
  against the approved capability surface, the declared result type of
  an initialized func-typed variable registers as a func-file, and
  stdlib producer values register as func-files wherever bound;
  pinned as self-test forms 253-256, raising the durable rejection
  set to two hundred six mutation forms.
- The producer-value closure landed at HEAD 5ff9116 on top of the
  Root-taint fix (262756c); the exact round-45/46/47 chain is 14c0698
  (gate gaps), 70dcc42 (its records), 262756c (Root laundering), e1410eb
  (its records), 5ff9116 (producer values), 8c6cc44 (its records). Gates:
  go test ./... incl -race, go vet,
  gofmt, import graph (self-test, all 206 forms rejected at that commit),
  CGO_ENABLED=0
  build and test, four cross-compiles, SOW audit — all green.
- Counts unchanged at this commit: production 4,792 raw lines / tests
  4,877 raw lines (the gate scanner lives outside the module). Decision
  5A remains open for user ratification; Milestone 2 remains blocked
  until the re-review passes.

### 2026-08-13 - round-48 gate re-review bound method expressions and cross-package producer vars closed (HEAD aec609c)

- The round-48 adversarial re-review (six narrow reviewers: codecs,
  membership/zero-alloc, mapping/pin/gate, metadata/bootstrap, records,
  gate hunting) passed codecs, membership/zero-alloc, mapping/pin/gate,
  and metadata, and failed with two gate findings plus one records
  finding.
- P0 - bound method expressions: `open := (*os.Root).Open` followed by
  `open(root, name)` binds the Open method with the receiver as an
  explicit first argument. The receiver node is a type expression and
  never carries value taint, so the value-position selector check could
  not see it; the same held for the package-level initializer
  `var openRootPkg = (*os.Root).Open`. Both reproduced live at gate
  exit 0 with the exempted inflater chain consuming the file.
- P2 - same-module cross-package producer vars: `internal/format`
  declares `var OpenRoot = os.OpenRoot` (and the *os.File sibling
  `var Open = os.Open`); a caller in `internal/mapping` invoking
  `format.OpenRoot(dir)` cannot see the declaring directory's taint
  registry, so the returned *os.Root/*os.File reached a flate reader
  untainted, reproduced live at gate exit 0.
- P2 (records) - the round-47 exec-log bullet never cited the terminal
  records HEAD by hash; the corrected chain 14c0698 -> 70dcc42 ->
  262756c -> e1410eb -> 5ff9116 -> 8c6cc44 is now named in the
  round-45/46/47 bullets above and in this entry.
- Fixed at HEAD aec609c: the selector rules now recognize method
  expressions whose receiver type resolves to a file-bearing handle
  (methodExprFileType, rejecting every method outside the approved
  lifecycle surface in both function and package-level scans), and a
  process-wide package-level producer-var registry
  (qualifiedProducerVars plus the per-directory pkgProducerVarsByDir)
  resolves same-module producer vars through the call-site import path,
  with the clause-name fallback for plain (non-renamed) imports;
  pinned as self-test forms 257-260, raising the durable rejection set
  to two hundred ten mutation forms.
- Replayed at the new gate: all round-47 replays (R4-R14) and the new
  probes (P10 method expression, P12 exempted-inflater chain, P14
  package-level method expression, P15 cross-package producer var)
  are rejected; the benign close-value/Fd-value/Chdir controls still
  pass. Gates: go test ./... incl -race, go vet, gofmt, import graph
  (self-test, all 210 forms rejected), CGO_ENABLED=0 build and test,
  four cross-compiles, SOW audit — all green. Counts unchanged at
  this commit. Decision 5A remains open for user ratification;
  Milestone 2 remains blocked until the round-49 re-review and the
  final review pass.

## Validation

Acceptance criteria evidence:

- Milestone 1 evidence: milestone report
  `.agents/sow/pending/pure-go-v4-port-milestone-1-report.md` (fixture
  cross-open with exact cases.json semantics, malformed rejection, literal
  codec vectors, zero-allocation measurements incl. the blob path, review
  rounds 1-3 with all P0-P2 findings fixed and regression-pinned).
  Milestone 2 not started.

Tests or equivalent validation:

- `go test ./...` (4 packages) — green at the last commit.
- `go test -race ./internal/format ./internal/reader ./internal/mapping .` — green.
- `go vet ./...` — clean; `gofmt -l .` — empty.
- `./check-import-graph.sh` — passes; the content-transfer scan is the AST
  gate (v4/go-gate, stdlib only): banned imports/selectors and the
  `*os.File` capability surface, with the three in-memory inflater nodes
  exempted as exact, file-taint-verified shapes; the 210-form `--self-test`
  runs in a private temp copy and never modifies the reviewed tree.
- Cross-compilation: darwin/amd64+arm64, freebsd/amd64+arm64,
  windows/amd64+arm64+386, linux/386+arm64 — all build.
- Conformance: 6/6 Rust fixtures cross-open with exact semantics; 3/3 invalid
  mutations rejected with code 32; structured absence probes added.
- `.agents/sow/audit.sh` — clean.

Real-use evidence:

- The Rust peer has accepted representative update-ipsets replay evidence.
  Equivalent Go evidence is an implementation acceptance requirement, not a
  planning claim.

Reviewer findings:

- Six-agent adversarial review rounds 1-5 (2026-08-11/12, see execution
  log): the pre-session gap-analysis pass found one real BLOCKER (structure
  radix) and ten MAJOR findings, all repaired (58c4d8f); this session's
  round 1 found no P0 but P1/P2 findings across all six aspects, all
  repaired with regression tests; round 2 re-review caught a shipped P0
  (blob coverage underflow, 3b4f3d5) and further P2s, all repaired and
  pinned; rounds 3-5 closed with all six reviewers at PASS (0 P0-P2).
  The review-process sweeps through the sixth round are recorded in the
  gate execution record: the fifth sweep completed with all six narrow
  reviewers at PASS (360130c); the sixth final review (sol round 14)
  failed with five P2 findings in the mmap gate and the records, all
  fixed in this pass with the AST gate rewrite (v4/go-gate), the
  temp-copy self-test, and the completed records; decision 5A remains an
  open user decision.
  The round-44 re-verification then completed with all six narrow
  reviewers at PASS (e5fea20); the round-45 final review failed with
  three P2 mmap-gate findings (os.CopyFS, os.OpenInRoot/os.OpenRoot
  handles, x/sys descriptor-transfer primitives), all fixed at the next
  HEAD with self-test forms 249-251, the same-class Root-laundering P0 closed
  with form 252, and the producer-value P0s closed with forms 253-256;
  the rejection set is two hundred six mutation forms;
  re-verification is pending.
  The closed-state error class was resolved by decision 3 (WrongState
  class, error-capable WordCount) and was never an open defect.

Same-failure scan:

- Round-2 searches re-ran the full classes: content-transfer I/O (now also a
  mechanical gate), complete page arrays, stale wire constants, missing
  record-limit validation (catalog feed index), kind-classification error
  classes, blob/probe validation gaps, metadata stream validation, report and
  SOW factual drift. Each class is fixed completely, not only the cited
  examples.

Sensitive data gate:

- This SOW contains repository-relative paths, public upstream identity,
  generic platform names, code metrics, and synthetic/public benchmark
  descriptions only. It contains no raw secret, credential, operational host
  alias, personal or customer/community data, private endpoint, or proprietary
  incident detail.

Artifact maintenance gate:

- AGENTS.md: updated to register the generic final-review runtime skill.
- Runtime project skills: added `project-final-review` after the first repeated
  false PASS, then reframed it after the round-10 false PASS around one explicit
  adversarial objective: prove the work should not merge. It now grants broad
  investigative authority, requires concrete evidence, and defines PASS as a
  failed full-scope disproof attempt. The Rust skill remains unchanged. A Go
  implementation skill is still not invented here.
- Specs: reviewed completely; no format or behavior change was made.
- End-user/operator docs: unaffected; the milestone report is corrected as a
  project record, not published product documentation.
- End-user/operator skills: none exist and no public workflow changed.
- SOW lifecycle: this file remains `in-progress` under `current/`; Milestone 1
  is reopened and Milestone 2 is blocked; SOW-0017 remains the separate Phase-2
  item.

Specs update:

- None for Milestone 1: the Go reader implements the current normative
  contract unchanged.

Project skills update:

- Updated `project-final-review` after the round-10 false PASS. The generic
  workflow now makes fault discovery its explicit mission, requires reviewers
  to understand the objective and blast radius, grants authority to examine any
  relevant surface and build `/tmp` reproducers, requires proven findings, and
  defines PASS as failure to prove a blocking defect after the strongest
  plausible attacks. Repository modification, process interference, and
  software installation/removal are forbidden. A Go implementation skill
  remains deferred until proven commands and hazards exist later in this SOW.

End-user/operator docs update:

- The round-2 repairs are recorded in the milestone report and this SOW log;
  the Go SDK documentation itself is an implementation deliverable.

End-user/operator skills update:

- None exist; reassess at closure.

Lessons:

- Passing tests around private fragments do not establish a usable SDK or
  current wire compatibility.
- A semantic port needs an explicit parity matrix and cross-language
  execution; source similarity is neither required nor sufficient.
- The most dangerous way to port this engine is to preserve the obsolete Go
  storage architecture because it already looks substantial.
- Single-reviewer passes converge; only adversarial multi-agent passes with
  disjoint briefs found the catalog-limit, kind-class, dangling-reference,
  FreeBSD, and paperwork defects.

Follow-up mapping:

- Snapshot signing remains tracked by pending SOW-0017.
- No other deferred item is created by this milestone.

## Outcome

Pending implementation and user acceptance.

## Lessons Extracted

Pending implementation.

### 2026-08-12 - external audit close-out round (milestone-1 gate re-verification)

An external full-scope review found five real P0/P1 contract defects after
the six-reviewer pass declared PASS. All were verified against code and
specs, fixed, and pinned by pre-fix-failing tests:

1. Obsolete retention semantics had been reintroduced: `RetentionTag()` and its
   "retention" test survived the deletion (binary-format-v4.md:311 forbids
   the compatibility alias; milestone-0 report classified them for
   deletion). Removed from v4/go/types.go and types_test.go.
2. Pin value copies double-decremented the reader pin count: `p2 := *p1`
   carried its own closed flag. Every Pin now references one shared
   private pinState; value copies and pointer aliases close the same
   logical pin. Pinned by TestPinValueCopySharesClose,
   TestPinValueCopyKeepsReaderBusy, and
   TestPinValueCopyCannotReleaseSecondPin (all fail on the pre-fix code).
3. DirectSemantic registry drift: public Go values were 0/1/2 while the
   Rust engine-defined registry is Generic=1, FirstSeen=2, LastSeen=3.
   Go now exports 1/2/3; TestPublicSemanticFoundation pins them.
4. No-threat structured values had no clean absence result: the Go
   ThreatMembership returned a zero view with nil error. It now returns
   (MembershipView, bool, error) mirroring the Rust Option; a new
   Rust-produced corpus fixture (rust/structured-ipv4-nothreat.iprdb, six
   fixture manifest) with membership-id-zero values pins the absent path
   in both readers. Rust verify asserts Option None for the empty-feeds
   ranges; Go asserts present=false.
5. Duplicated authorities removed: error-code tables (public errors.go vs
   internal/format/codes.go) and Cardinality129 arithmetic (public types.go
   vs internal/format/cardinality.go) were independent copies. The
   internal/format package is now the single authority; the public package
   re-exports typed aliases (ErrorCode = format.ErrorCode and per-name
   constants; Cardinality129 = format.Cardinality129 plus wrappers).
   Uint64/Uint128 moved into the format authority to preserve the public
   method set.
6. Final-review regression guards: compile-time alias assertions pin the
   public ErrorCode and Cardinality129 as the internal/format types, and a
   negative source guard forbids the reintroduction of the obsolete
   retention symbol in any non-test production source; all three guards
   fail on the pre-fix tree.

Gates re-run at HEAD: go test ./... (4 packages), -race, vet, gofmt,
import graph, 9 cross-compiles, SOW audit, Rust conformance (6 fixtures),
and the regenerated no-threat fixture all pass. ZERO allocations and zero
atomics on every measured public hot path are unchanged (12 public + 8
internal checks at exactly 0).

## Followup

- SOW-0017 remains the separate Phase-2 signing item.

## Regression Log

### 2026-08-12 - final-review process regression

The session's round-6 final review reported PASS at HEAD 29e1dde after verifying
the supplied defect list and all mechanical gates. A fresh independent review
then found material issues outside that checklist:

- exported writable canonical ValueTag variables drive DirectSemantic and can
  be reassigned process-wide or raced with readers;
- Go exposes ImmutableInfo while the approved parity matrix, normative spec,
  and Rust authority use DatabaseInfo, with no recorded deviation;
- the milestone report retained stale statements about view allocations, the
  worker decision, and structured-view revalidation.

Root cause: the final review behaved as a closed checklist verifier, inherited
the prior repair narrative, sampled corrected record summaries, and allowed
green automation to end the review before an open-world public-contract and
record audit. The external review's claim that Milestone 2 lacked a
Pre-Implementation Gate was not reproduced: this SOW already has a ready gate.

Process repair: created and registered the generic `project-final-review`
runtime skill. Future final reviews start from zero trust, reconstruct authority
and complete scope, audit public interfaces and full records before running
gates, verify regression evidence and same-failure classes, and perform a
separate disproof pass before PASS. The skill is intentionally generic so the
same failure mode is prevented across all project work.

Validation for the process repair uses HEAD 29e1dde as the historical benchmark:
the workflow must identify the mutable tag authority, API-name deviation, and
three stale report claims even though every mechanical gate passes. Resolution:
the product fixes landed at HEAD 73bba50 (immutable tag accessors, DatabaseInfo
rename, corrected report statements) with the metadata figure re-measured at
2d2197a; the round-7 through round-10 re-review results are recorded in the
Gate execution record, with the closing round-10 PASS at HEAD 253f9d5.

Append regression entries here only after this SOW was completed or closed and
later testing or use found broken behavior. Use a dated
`## Regression - YYYY-MM-DD` heading at the end of the file. Never prepend
regression content above the original SOW narrative.

## Regression - 2026-08-12 - round-10 false PASS and evidence-protocol hardening

A fresh independent review of closure HEAD 1c71299 invalidated the round-10 PASS
at 253f9d5. Four implementation/contract defects already existed at the reviewed
revision, and the closure commit introduced a fifth records contradiction:

- the approved public API requires `NetworkEnrichmentV1Location` with
  `Location *NetworkEnrichmentV1Location`, but the Go facade flattened the
  coordinates and added `HasLocation` without an approved deviation;
- structured lookup performs semantic flags/coordinate validation that the Rust
  hot path and the normal-operation contract intentionally omit, and a test
  incorrectly labels this extra work as Rust parity;
- direct scans, blob branches, and inline membership word reads repeat decodes or
  record reconstruction, while the milestone report claims one page-header
  decode per visited page;
- the closure header said Milestone 1 was closed and Milestone 2 could start while
  Validation still said reopened/blocked and contradicted the skill update;
- `Mapping.File()` exposes an unused raw file capability, and the source gate's
  method-call regex does not catch content-transfer helpers such as `io.ReadAll`
  or `io.Copy`.

Root cause: the generic final-review skill described the right review lanes but
did not make adversarial disproof the reviewer's explicit success condition. The
round-10 execution therefore sampled exported names, equated zero
allocations/atomics with necessary work, trusted the source gate, ignored an
unused boundary escape hatch, and attached PASS to a revision followed by an
unreviewed closure commit.

The first repair draft overcorrected with six mandatory evidence artifacts and a
formal closure protocol. The user rejected that as compliance-heavy and likely
to narrow thinking. Final process repair: `project-final-review` now gives the
reviewer one explicit mission - prove with concrete evidence that the work is
faulty, incomplete, harmful, or unsafe to merge. It requires understanding the
objective and blast radius, grants authority to inspect any relevant surface and
create `/tmp` tests or mutations, treats green evidence as something to attack,
continues after the first finding, and permits PASS only when the strongest
plausible disproof attempts fail. It also explicitly forbids modifying the
reviewed repository, interfering with running processes, or installing/removing
software. This framing is generic and applies to every project final review.

Resolution: the five external-audit findings were fixed at HEAD ca30026 with
pre-fix-failing regression pins, and the round-11 final-review findings
(view-lifetime guard retargeting, mmap-gate escapes, stale records) were fixed
at HEAD 2fd6cae; the round-12 gate findings were fixed at HEAD 4fdc671
(whole-tree selector scan, dot-import and bufio bans, durable gate
self-test, runtime strace evidence). Decision 5A was entered in the
decision log for the user's ratification and remains the only open
product decision. The complete re-review trail is recorded in the Gate
execution record; the closing result is appended there when it completes.

### 2026-08-13 - sixth-sweep gate rewrite (AST scanner) (HEAD c42325a)

- The sixth final review failed with five P2 findings, all in the mmap
  gate and the records: split-after-the-dot selectors; type-blind
  exact-literal exemptions; the open-ended stdlib denylist
  (compress/gzip regex bug, log/slog, runtime/trace,
  os.StartProcess ProcAttr files); the destructive gatemut_* startup
  sweep; and completion claims ahead of the review trail.
- The line-oriented text scan is replaced by the AST gate scanner at
  v4/go-gate/main.go (stdlib only): it parses every production file
  regardless of build tags, line wrapping, comments, aliases, or file
  names; syntactically taints *os.File values; bans 37 content-transfer
  imports and 56 selector families; constrains *os.File use to the
  mapping-lifecycle methods and same-package/module-internal/x-sys
  consumers; and exempts the three exact in-memory inflater nodes only
  with file-taint verification (c.r.Read(p)/c.r.ReadByte() over a
  *bytes.Reader field, and the two exact io.ReadFull(zr, out[...int(meta.
  MetadataUncompressed)]) shapes with a non-file zr).
- The self-test now runs in a private temp copy of the module (cp -a
  into mktemp): forty mutation forms rejected, including the nine
  independent reproducers of the sixth review; an innocent
  gatemut_-named file is proven to survive; the reviewed tree is never
  modified; the startup sweep is removed. HEAD 81ca524 then pinned the
  aliased-os producer form as the forty-first; HEAD 6b05801 tainted
  *os.File results returned by same-package accessor methods.
- Gates at those HEADs: go test ./... incl -race, go vet, gofmt,
  import graph with the 41-form self-test, cross-compiles, SOW audit -
  all green. Counts: production 4,772 raw lines / tests 4,832 raw
  lines.

### 2026-08-13 - seventh-sweep gate hardening (alias/container/pipe classes)

- The narrow re-review of the sixth-sweep records found two P2 items:
  the accessor-method taint (HEAD 6b05801) had no mutation pin and no
  record entry, and the records cited HEAD c42325a for the 41-form
  count although that commit pinned forty forms (the aliased-os form
  arrived with HEAD 81ca524).
- Ampere additionally proved two untainted file-escape classes in the
  scanner: a separately built `&os.ProcAttr{Files: []*os.File{f}}`
  lost the container taint (both as a composite literal and as a
  field assignment), and a type alias `type zrAlias = *os.File`
  defeated the exemption predicate and the taint resolution.
- Fixed (HEAD e2dc7e0): file producers now carry result positions
  (`os.Pipe` = both results; error results never tainted); type
  aliases are collected and resolved in classifyType, signatures, and
  producer conversions; composite literals propagate container taint
  from any file/container element; `os.StartProcess` joins the banned
  selectors (closing the field-assign variant); `os.Pipe` joins the
  producers; multi-result assignment taints only the file positions.
- The self-test forms were renumbered and extended: 40 aliased-os,
  41 accessor-method, 42 alias-conversion, 43 alias-parameter,
  44 separately built ProcAttr, 45 os.Pipe producer, 46 innocent
  gatemut_-named survival (positive). Durable rejection set: forty-five
  mutation forms.
- Gates at HEAD e2dc7e0: go test ./... incl -race, go vet, gofmt,
  import graph with the 45-form self-test, nine cross-compiles, SOW
  audit - all green. Counts: production 4,772 raw lines / tests 4,832
  raw lines (gate scanner lives outside the module).

### 2026-08-13 - eighth-sweep gate hardening (field storage and channel transport)

- The seventh-sweep re-review (Ampere round 2) found one P1 and one P2
  still open in the scanner: a *os.File parked in a struct field
  (`var zb box; zb.r, _, _ = os.Pipe()`), then assigned to the exact
  exempted inflater reader (`zr = zb.r`) in metadata.go, passed the gate
  (compiling code, exemption granted); the same class over a
  `chan *os.File` (`zr = <-ch`) also passed.
- Root causes: package-level var state was per-file in the scanner, so a
  type-only struct var declared in one file was invisible to functions
  in another; struct-field writes and type-only/new(T) instances were
  not registered; channel element taint did not exist (the make() chan
  type text was never printed), and field-type aliases were resolved in
  classifyType but not in field reads.
- Fixed (HEAD c4b1b52): shared per-package taint state (all package vars
  collected before any file runs); struct-field write taint overlay;
  type-only var and new(T) struct registration; chan *os.File taint for
  declarations, make(), parameters, sends (ch <- f), receives
  (x := <-ch), and range loops; container index reads; alias-resolved
  field types in isFileExpr/isContainerExpr; SelectStmt traversal.
- Self-test forms 47 and 48 pin the two proven classes by shadowing the
  exact inflater exemption in metadata.go with a struct-field stored
  file and a channel-transported file; form 49 pins the benign
  same-shaped control (int field) that must pass, proving the taint is
  not a false positive. Durable rejection set: forty-seven mutation
  forms; the interplay between the exemption guard and the file taint
  is now mutation-tested.
- Gates at HEAD c4b1b52: go test ./... incl -race, go vet, gofmt,
  import graph with the 47-form self-test, nine cross-compiles, SOW
  audit - all green. Counts: production 4,772 raw lines / tests 4,832
  raw lines (gate scanner lives outside the module).

### 2026-08-13 - ninth-sweep gate hardening (closures, assertions, nested channels)

- The eighth-sweep re-review (Ampere round 3) found three more escape
  classes in the scanner: an inline FuncLit returning *os.File
  (`zr = func() *os.File { ... return f }()`) escaped the taint because
  closure bodies were not taint-propagated and closure calls were not
  producers; a type assertion `zb.r.(*os.File)` erased the taint of a
  file hidden in an interface field (no TypeAssertExpr
  classification); and the channel family had two gaps - chan chan
  *os.File (and chan C with C = chan *os.File) did not resolve
  iteratively, and the single-variable range form `for z := range ch`
  put the element in the Key slot, which was not tainted.
- Fixed (HEAD ddc5f9c): closure bodies are walked for taint propagation
  (they capture the outer state); closure calls and func()-typed
  identifiers are producers when they return *os.File; TypeAssertExpr
  taints when the asserted type is *os.File and otherwise delegates to
  the asserted value; chanElemFile resolves nested channels and alias
  chains iteratively; the single-variable channel range taints the Key
  slot; IndexExpr reads of containers are file-tainted in isFileExpr.
- Self-test forms 50-53 pin the four classes: inline FuncLit, type
  assertion, two-hop channel transport, and single-variable channel
  range, all shadowing or exercising the inflater exemption; the benign
  control (form 49) and the innocent-file survival check (form 46)
  still pass. Durable rejection set: fifty-one mutation forms.
- Gates at HEAD ddc5f9c: go test ./... incl -race, go vet, gofmt,
  import graph with the 51-form self-test, nine cross-compiles, SOW
  audit - all green. Counts: production 4,772 raw lines / tests 4,832
  raw lines (gate scanner lives outside the module).


### 2026-08-13 - tenth-sweep gate hardening (parenthesized producers, alias funcs, type-switch binds)

- The ninth-sweep re-review (Ampere round 4) found five more scanner gaps:
  a parenthesized producer call `zr = (getFile)()` and a parenthesized
  closure `zr = (func() *os.File { ... return f })()` both hid the callee
  shape behind a ParenExpr node; a closure declared as
  `func() io.ReadCloser { ... return f }` hid the returned *os.File behind
  an interface result type; a type-only alias
  `type fileFn = func() *os.File; var getFat fileFn` registered nothing in
  the funcs table; and a type-switch guard `switch zv := x.(type) { case
  *os.File: ... }` never tainted the bound identifier.
- Fixed (HEAD 7caf351): unwrapParen strips parentheses in producerCall and
  the rules walk so selector and argument checks see the same call;
  closures whose body returns a tainted value are marked retFile at their
  FuncLit node and every declared result position is treated as
  file-tainted; funcTypeResultsFile resolves alias text through the
  package alias map before falling back to AST result checks; the
  type-switch prepass binds the guarded identifier when a case type
  resolves to *os.File.
- Self-test forms 54-58 pin the five closed classes and form 59 pins the
  parenthesized benign control (HEAD 5c88ba3). Durable rejection set:
  fifty-six mutation forms; the real tree stays green under the hardened
  scanner.
- Extended during the round-5 gate re-review (HEAD 3952097): defined
  func types (type F func() *os.File) now register in the alias map,
  func() *os.File values returned through same-package helpers keep
  the producer taint (callResultsFuncFile), and type-switch cases
  binding defined func types enter funcFile. Forms 60-63 pin the
  three classes plus the benign bytes.Reader control.
- Extended again during the round-5 re-review (HEAD b168aba): method
  receivers now resolve through the struct instance (not the
  receiver variable name) in callResultsFuncFile, and a callee that
  is itself a call returning func() *os.File is a file producer
  (zb.mk()(), useDef2(getDef3)()). Forms 64-67 pin the method
  boundary, both double-call shapes, and the benign int control.
  Documented residual: a *os.File exported by a third-party package
  (other than os.Stdin/Stdout/Stderr) is invisible to the syntactic
  taint unless the code names *os.File textually or moves it through
  an already-tainted route.
- Ampere round 5 found four more producer routes (HEAD 5f97f94):
  struct-field func values, chan of func() *os.File, any-erased func
  returns recovered by type assertion, and os.Stdin/Stdout/Stderr
  through interface closures. Fixed with the kindFuncFile/
  kindChanFuncFile split, struct-field func resolution in
  producerCall/classify, func-file type assertions, and the std
  handle file-expression set. Forms 68-72 pin the four classes plus
  the benign chan-of-func() int control.
- Ampere round 6 found three more producer routes (HEAD 36fe279):
  nested struct-field func chains (nh.inner.fn()), named
  interface-typed helpers whose bodies return tainted files or
  os.Stdout (getNamed()/getStd()), and chan-of-func values passed
  through same-package helpers. Fixed with a resolveStruct field-
  chain walk, a per-directory pre-scan that marks named producers
  (retFuncs) before any runFile, and callResultsChanFuncFile.
  Forms 73-77 pin the three classes plus the benign named io.Reader
  helper control.
- The named-producer pre-scan was extended to methods with a
  fixpoint (HEAD 5a4f8dc): mb.named() returning os.Pipe or
  os.Stdout through io.ReadCloser, and deep() -> mid() chains,
  are now producers (retMethods + prescanFileProducers). Forms
  78-81 pin the three classes plus the benign method control;
  form 77 was reworked to a compiling bytes.Reader wrapper shape.
- Ampere round 7 found a fourth producer route (HEAD 1a54443 -> 8696af3):
  a nested method-receiver chain (mhv.inner.mk()(), where mk is a
  method on minner reached through the mholder.inner field, returning
  a defined func type producing *os.File). Fixed by resolving the
  receiver expression through resolveStruct instead of requiring a
  plain identifier; the same fix applies to the chan-of-func caller
  (callResultsChanFuncFile). Form 82 pins the escape; form 83 pins
  the benign nested-method control.
- Ampere round 8 found two more producer families (HEAD 96f0515):
  method values are never classified (a method value bound to a
  variable, returned through an interface-typed helper, bound from a
  nested receiver chain, or sent through a package-level channel), and
  generic type-parameter pass-through erases the file taint
  (idf[T any](f T) T instantiated with *os.File). Fixed with method
  resolution in classify (declared *os.File results, retMethods
  taint, defined func-type results), a retFuncFiles/retMethodFiles
  body-scan registry, double-call resolution on func-file variables,
  package-level channel taint propagation, and type-parameter result
  mapping for generic calls. Forms 84-89 pin the six escapes; forms
  90-91 pin the benign method-value and benign generic controls.
  Residual documented: third-party exported *os.File and cross-file
  local-channel flows remain visible only through declarative types.
- Ampere round 9 found three consumer-path gaps (HEAD aa019c8):
  generic container element shapes ([]T) never bound the type
  parameter; method values returning chan-of-func-file never tainted;
  and struct fields assigned file-producing closures/chans lost their
  taint because consumers read only the declared field text. Fixed
  with token-boundary element matching in the generic rules,
  chan-result resolution for method values and chan-marked variables,
  full-kind fieldTaint writes, fieldTaint consumers in classify and
  producerCall, and package-var fieldTaint propagation from the
  prescan. Forms 92-95 pin the four escapes; forms 96-97 pin the
  benign generic-container and benign func-field controls.
- Self-audit before Ampere round 10 closed the remaining channel
  consumers: receive classification (ARROW) distinguished chan-file
  element kind, RangeStmt classified the ranged expression whole
  (struct-field channels and method values), and SendStmt recorded
  selector-typed channel fields. Forms 98-100 pin the three escapes;
  forms 101-102 pin the benign range and benign receive controls.
- Ampere round 10 found the container-element route (HEAD ed0a0f9):
  map/slice fields holding file-producing funcs (fm.m["k"]()) were
  invisible because producerCall had no IndexExpr callee case,
  applyLHS did not record element writes, and classify read only
  file-container elements. Fixed with an elementTaint registry
  (element reads/writes, composite element kinds, declared element
  shapes for fields/params/vars), an IndexExpr callee case in
  producerCall, and exprText coverage for map/ellipsis/index types.
  Forms 103-106 pin the four escapes; form 107 pins the benign map
  field control.
- Ampere round 11 found the anonymous-receiver method route (HEAD
  63d665d): func (T) m() with no receiver variable name was invisible
  because receiverOf required a receiver name and never resolved the
  receiver type for method-value keys. Fixed by deriving the receiver
  struct from the receiver type expression, trimming pointer/generic
  spellings, keying alias-resolved method signatures, and never
  misregistering methods as package funcs. Forms 108-111 pin the four
  escapes (direct file, interface-hidden, pointer receiver, map-field
  method value); form 112 pins the benign anonymous-receiver control.
- Ampere round 12 found the alias-receiver route (HEAD 5fe4b4f):
  receiver aliases (type a = s) keyed retMethods and instance lookups
  inconsistently -- receiverOf returned the raw alias text while call
  sites resolved it through structBase, so interface-hidden results
  were invisible; and resolveStruct/classifyStruct/classify accepted
  only registered struct names for composite literals, so alias-named
  instances (rF{}.m()) never resolved. Fixed by resolving receiver
  aliases inside receiverOf and resolving alias names in every
  composite-literal struct lookup. Forms 113-114 pin the two escapes
  (alias-variable interface-hidden call, alias-literal direct file);
  form 115 pins the benign alias-receiver control.
- Ampere round 13 found the receiver-resolution class (HEAD
  85db9dc): defined-type receivers (type b a) were never registered,
  generic instantiations (gsG[int]) never stripped to the base name,
  embedded-field promotion (hE{gsE}) was dropped at collection, and
  pointer aliases (type p = *s) keyed methods under the pointer
  spelling. Fixed with a defined-type chain map, a resolveStructName
  helper (aliases + defined types + pointer + generic suffixes) used
  by structBase/receiverOf/every composite-literal lookup, and an
  embedded-method walk (methodMeta) for method-value and call
  resolution. Forms 116-119 pin the four escapes; form 120 pins the
  benign embedded-promotion control.
- Ampere round 14 found the pointer-defined-type route (HEAD
  36f6e82): var p *d with type d gs (defined type) and no initializer
  registered neither the pointer nor the defined chain because
  resolveStructName applied the defined-type lookup before the
  pointer trim. Fixed by running the alias/defined/pointer/generic
  reductions to a fixpoint in resolveStructName. Form 121 pins the
  escape; form 122 pins the benign pointer-defined-type control.
- Ampere round 15 found the indexed-receiver class (HEAD 99b211a):
  new(d) with d a defined type never resolved through the defined
  chain, and array/map-index receivers (arr[1].get(), mm["k"].get())
  had no IndexExpr resolution at all. Fixed with a varTypes registry
  (package vars, local typed vars, and short-decl composite
  literals), element-type stripping to the base struct in resolveStruct
  and classifyStruct, and defined-chain resolution in both new()
  cases. Forms 123-125 pin the three escapes; form 126 pins the
  benign array-index receiver control.
- Ampere round 16 found the element-receiver class (HEAD 7f72ca3):
  indexed bases beyond bare variables (struct fields, call results,
  dereferenced pointer-to-containers), make() short declarations,
  range-variable element receivers, and chan-receive receivers were
  all invisible. Fixed with a typeOfBase resolver (variables, struct
  fields, call results, deref/paren wrappers), a stripElemType
  helper (container and channel wrappers), exprElemStruct for range
  and receive element binding, make() type registration for
  short-declared containers, and ARROW receive resolution in
  resolveStruct/classifyStruct. Forms 127-132 pin the six escapes;
  form 133 pins the benign make() map receiver control.
- Ampere round 17 found the range-literal-receiver class (HEAD
  255f34c): range variables never recorded their element type in
  varTypes, so container/chan-typed bindings were blind as indexed or
  receive bases, and composite-literal index bases (map[string]*gs{...}
  ["a"]) had no typeOfBase case. Fixed by recording one wrapper-stripped
  element type for range bindings and adding the CompositeLit case to
  typeOfBase. Forms 134-135 pin the two escapes; form 136 pins the
  benign composite-literal indexed receiver control.
- Ampere round 18 found the bound-receiver class (HEAD 3cfe554):
  type-switch bound variables (case *gs: v.get()) never registered as
  struct instances, and multi-assignment call results (a, _ := f())
  never recorded their declared result type, so both were blind as
  receivers or index bases. Fixed by registering case structs and case
  type texts for type-switch bindings and recording the declared
  result type per index in applyLHSMulti. Forms 137-138 pin the two
  escapes; form 139 pins the benign type-switch bound receiver
  control.
- Ampere round 19 found the call-result-binding class (HEAD
  90ea53c): single-value call results (a := mkArr()), method-call
  results (a := box.mkArr()), type-switch default-clause bindings
  (switch v := iv.(type) { default: v.get() }), and multi-assign
  element reads (a, _ := mm["k"], 0) all failed to record the binding's
  type or struct instance, so each stayed blind as an indexed or
  receiver base. Fixed by recording result-0 declared types and
  non-call element types in applyLHS/applyLHSMulti (one wrapper
  stripped, struct instances only when the binding type itself names a
  struct), a typeOfBase IndexExpr case, and default-clause
  registration from the switched expression in the type-switch
  handler. Forms 140-143 pin the four escapes; form 144 pins the
  benign single-LHS call-result index control.
- Ampere round 20 found the interface-and-channel binding class
  (HEAD 2b3006a): explicit generic instantiations (a := mkGen[*gsG]())
  never substituted the call's type arguments into the result type,
  type-switch default clauses over interface-valued expressions
  (mkIf().(type), sd.f.(type)) never resolved the interface to its
  signature-identical implementations, and multi-assign from a channel
  receive ((<-chS), 0) recorded no element type. Fixed by substituting
  explicit type arguments in applyLHS/applyLHSMulti via
  genericSubstitutedResults, registering interface method signatures as
  pseudo-structs (with methodFull text matching), an ifaceImplProducer
  union over signature-identical implementations, and a typeOfBase
  UnaryExpr ARROW case. Forms 145-148 pin the four escapes; forms
  149-150 pin the benign generic and interface-default controls.
- Ampere round 21 found the generic receiver-binding class (HEAD
  5f18d4f): generic-receiver method results never substituted
  receiver type arguments, explicit-instantiation calls
  (mkT[*os.File]()) and their direct method flows were invisible,
  and argument-inferred generic struct bindings
  (mkT2(&gsG{}).get()) bound no file taint. Fixed with a
  recvTypeParams registry feeding genericMethodResults, a unified
  genericCallResults helper (explicit and inferred via
  inferTypeArgs), a typeOfBase unary-& case for generic literal
  receivers, and generic-first wiring in producerCall/classify/
  resolveStruct/classifyStruct/applyLHS/applyLHSMulti. Forms
  151-156 pin the six escapes; forms 157-158 pin the benign
  bytes-only controls.
- Ampere round 22 found the alias-spelled generic binding class
  (HEAD 82b96bf): a generic type argument written through a type
  alias (type zfA = *os.File; gRA[zfA]{}) was substituted as
  literal text and never alias-resolved, so the taint checks
  compared container results like []zfA instead of []*os.File and
  the io.ReadFull exemption could grant a file-backed zr. Fixed in
  substituteTypeParams: every type argument is reduced through
  resolveTaintType (alias and defined-type chains to a fixpoint)
  before substitution, and the substituted result is reduced
  again. This covers receiver instantiations, explicit and
  inferred generic calls, method values, and embedded promotion.
  Forms 159-164 pin the six alias escapes; forms 165-166 pin the
  benign bytes-backed alias controls.
- Ampere round 23 found four reader-shape escape classes (HEAD
  85c6f2c), all reaching the io.ReadFull exemption with gate exit 0:
  (1) container element reads from call results (a[0] of a
  []*os.File generic or plain result, m["k"], *p on *zfA) never
  classified the element; (2) chan-of-file carriers were blind in
  return positions (return <-c) and method/generic-call results
  (h.ch() chan *os.File, mkC[*os.File](), rr.mc()) never registered
  as chan taint; (3) cross-package aliases as generic type arguments
  (mapping.MappingFile) could not resolve because each directory is
  an independent pkgInfo; (4) generic method results naming a struct
  (r := rr.mk() with T bound to wS) never registered as struct
  instances, losing field taint on r.f. Fixed with elementReadKind
  (declared-element shape of index/deref bases) in classify,
  isFileExpr and isFileOrContainer; chanCarrier (identifier
  registries plus call classification) for receive positions, and a
  declared-results chan loop for method and function calls; a
  process-wide qualifiedAliases registry keyed package-clause name;
  and genericMethodResults/methodMeta consultation in resolveStruct
  and classifyStruct. Forms 167-174 pin the eight escapes; forms
  175-178 pin the benign bytes-backed controls.
- Ampere round 24 found the import-renamed qualified alias class
  (HEAD 932a0e8): a locally renamed import qualifier (import mm
  ".../internal/mapping") was never translated back to a package
  path, so mm.MappingFile generic type arguments, local alias chains
  of renamed imports (type MChainRen = mm.MappingFile), container
  element spellings ([]mm.MappingFile), declared variables
  (var z mm.MappingFile; z.Chdir()), and type assertions
  (v.(mm.MappingFile).Chdir()) all bypassed the gate with exit 0.
  Fixed with a per-directory alias registry (pkgAliasesByDir keyed by
  relative package dir), a per-file import snapshot (currentImports),
  and aliasLookup qualifier translation through the import map before
  matching the scanned directory; resolveTypeText, resolveDefinedType
  and resolveStructName all route through aliasLookup. Forms 179-182
  pin the four rejects; forms 183-184 pin the benign renamed-qualified
  bytes controls.
- Ampere round 25 found the func-typed generic-method class
  (HEAD 21b5742): producerCall's generic-method branch claimed a
  func-typed result (mresG of func() *os.File after the direct
  *os.File position check missed) as a direct file position, so
  applyLHS recorded the binding via st.file instead of st.funcFile
  and calling the bound func lost the taint - gRZ[func() *os.File]
  {}.mk()() reached the io.ReadFull exemption with gate exit 0.
  Fixed by removing the funcTextFile producer claim so classify's
  existing generic-method loop yields kindFuncFile and applyKind
  registers the func-file; the non-generic method control was
  already caught, proving the shape is right and only the generic
  registration was blind. Forms 185-189 pin the five rejects
  (direct, embedded promotion, local alias, renamed-import func
  alias, unapproved method on the invoked result); form 190 pins
  the benign bytes-backed func control.
- Ampere round 26 found two adjacent escape classes at HEAD
  e1b1229: (1) mixed multi-result non-generic functions
  (getFn() (func() *os.File, error) bound as f, _ := getFn();
  f()) lost the func-file at the exact func-typed position
  because callResultsFuncFile required every declared result to be
  a func-file, with no per-position fallback for plain functions;
  (2) a defined type over a qualified or complex underlying
  (type x mm.A, type x []*os.File) registered nothing in
  definedTo, and cross-package defined func types
  (type F func() *os.File in mapping) were invisible to
  qualified references, so both reached the io.ReadFull exemption
  with gate exit 0. Fixed with callResults/callResultKinds/
  callResultKindAt (per-position result kinds through generic and
  declared signatures), per-position carrier registration in
  applyLHSMulti for call RHS, definedTo registration for every
  non-func non-ident underlying type text, and qualified
  registration of defined func types. Forms 191-196 pin the six
  rejects (mixed pos 0, mixed pos 1, defined over renamed alias,
  local defined func type, cross-package defined func type, defined
  over defined); forms 197-198 pin the benign bytes controls.
- Ampere round 27 found three adjacent escape classes at HEAD
  180024b: (1) a generic receiver bound to an interface whose method
  declares mixed results (Get() (func() *os.File, error)) was claimed
  by producerCall as a raw file position because interface method
  signatures are stored as pseudo-fields, so applyLHSMulti recorded
  st.file instead of st.funcFile and invoking the bound func lost the
  taint (both mixed positions and the chan-of-func variant); (2) all
  non-generic methods lost their declared results in callResults
  because methodMeta's ok flag reports body-marked producers
  (retMethods), not method existence - mixed method results
  (mk() (func() *os.File, error)) bound with no position taint; (3)
  a defined type over an alias (type D A; A = func() *os.File) and an
  alias over a defined func type (type E = D2) were invisible to
  cross-package spellings: only aliases and defined func types
  entered the qualified registries, and a first-hop bare name from
  another directory could not resolve further. Fixed with declared-
  result precedence in classify plus a per-position kind preference
  in applyLHSMulti (func-file/chan carriers keep their invoke-able
  kind when a producer claim overlaps), method-existence detection in
  producerCall's func-field claim (a declared method is not a func
  field), non-nil-results acceptance in callResults for ordinary
  methods, and defined-type registration plus a per-directory
  fixpoint (finalizeDirAliases) closing alias/defined chains in the
  qualified registries. Forms 199-205 pin the seven rejects
  (interface mixed pos 0, interface mixed pos 1, interface
  chan-of-func, defined over alias, alias over defined func, method
  mixed pos 0, method mixed pos 1); form 206 pins the benign
  interface-typed bytes control.
- Ampere round 28 found three adjacent escape classes at HEAD
  a8097a1: (1) an interface embedding a file-producing interface
  (type IEmb interface{ IBase }, IBase.Get() func() *os.File)
  resolved no promoted method because methodMeta's embedded walk
  propagated only body-marked producers (ok flag) and dropped
  declared results, so x.Get()() on an interface-typed generic
  result reached the io.ReadFull exemption with gate exit 0, in
  both the single-result and the mixed multi-result shapes; (2) a
  defined struct in another package used as a generic type argument
  (gRZ28[mm.S28]{}, s.Get()) never resolved because each directory
  keeps an independent package info and only aliases/func types
  were mirrored across directories; (3) a qualified defined chain
  of nine named hops (mm.J28 behind alias A28 and hops B28..I28)
  exceeded the single-pass fixpoint budget and was map-order
  dependent (passing only when the map iteration chanced to make
  the final hop resolvable early). Fixed by propagating promoted
  declared results in methodMeta's embedding walk, process-wide
  mirrors of remote structs/methods/method-full/embedded chains
  with a parse-time seed-merge (local wins on collision),
  self-entries for struct spellings in the qualified alias
  registries, result-type resolution through resolveStructName in
  classifyStruct/resolveStruct, and a full fixpoint loop for the
  per-directory alias/defined closure with self-hop guards
  (resolveDirText budget 8 -> 64). Forms 207-210 pin the four
  rejects (embedded interface single, embedded interface mixed,
  cross-package struct method, nine-hop qualified chain); forms
  211-212 pin the benign bytes controls.
- Ampere round 29 found two adjacent escape classes at HEAD
  8385134/fffc4dc: (1) a renamed import qualifier on a cross-package
  interface embedded in a reader interface or struct (type IEmb
  interface{ mm.IMapBase }) never reduced to a registered key: the
  interface branch mirrored structs/methods/embedded chains but - 
  unlike the struct branch - registered no qualifier self-entry, so
  the promoted file method resolved nothing and x.Get()() reached the
  io.ReadFull exemption with gate exit 0; the clause-name spelling
  passed because the clause-qualified mirror key happened to match;
  (2) a generic interface instantiated at the embedding site
  (type IEmbGN interface{ IBaseGN[func() *os.File] }) promoted the
  declared results with the raw type parameter unsubstituted, so
  Get() T never matched the file shapes; the same gap covered the
  chan-of-func variant, a renamed generic interface instantiation,
  and a cross-package generic struct receiver. Fixed by registering
  the qualified self-entry for interface names exactly like structs
  (pkgAliasesByDir + qualifiedAliases), recording generic interface
  type parameters in recvTypeParams (with a process-wide mirror
  seeded per parseDir, closing the remote generic-receiver gap), and
  substituting the embedding's type arguments in methodMeta's
  promoted-method walk (args from parseBracketArgs, substitution via
  substituteTypeParams). The round's P2 - benign self-test form 212
  did not compile (unused "bytes" import in the reader file) - was
  fixed by removing the import; the self-test never type-checks, so
  the missing check is documented in the form comment. Forms 213-217
  pin the five rejects (renamed-qualifier interface embedding,
  generic-interface instantiation with func-file argument, same with
  chan-of-func argument, renamed-qualified generic interface
  instantiation, cross-package generic struct method); forms 218-221
  pin the benign bytes controls.
- Ampere round 30 found the defined-hop instantiation class at HEAD
  b53652d/9be245e: a defined type over an instantiated generic
  interface embedded in an interface (type D IBaseG[func() *os.File];
  type IEmb interface{ D }) lost the type arguments at the embedding
  walk because methodMeta's promoted-walk extracted brackets from the
  raw embedded spelling ("D") while the instantiation lives in the
  defined chain's target text ("IBaseG[func() *os.File]"), so the
  promoted results propagated the raw type parameter and Get() T
  reached the io.ReadFull exemption with gate exit 0; the renamed-
  qualified cross-package twin had the same shape. The same gap
  existed in genericMethodResults' embedded loop. Fixed by extracting
  the embedding's type arguments from the resolved text
  (parseBracketArgs(resolveTaintType(emb, info))) in both walk sites,
  so alias and defined chains carrying the instantiation substitute
  exactly like a direct spelling. Forms 222-223 pin the two rejects
  (reader-local and renamed-qualified defined-over-instantiated
  embedding); form 224 pins the benign bytes control.
- Ampere round 31 found the nested generic-instantiation class at
  HEAD f2d40d4: a multi-level generic-interface embedding (type
  InnerL[T] interface{ Get() T }; type IBaseGL[T] interface{
  InnerL[T] }; type IEmb interface{ IBaseGL[func() *os.File] })
  substituted type arguments only at the frame owning the brackets,
  so the frame declaring Get returned the raw type parameter "T"
  and x.Get()() reached the io.ReadFull exemption with gate exit 0
  (three-level and chan-of-func variants included); the intermediate
  frame's identity substitution (InnerL[T] with its own argument T)
  masked the leak during the audit's control probes. Fixed by
  threading type parameters and arguments down the embedding chain
  in methodMeta: every frame substitutes its own instantiation into
  the next embedded type text before recursing, the declaring frame
  applies the accumulated arguments to its declared results, and a
  frame-level interface-parameter registry (ifaceParams, mirrored
  process-wide like the receiver-parameter registry) carries generic
  interface parameters across packages, with the genericMethodResults
  receiver-substitution walk given the same threading and the
  embedded-entry argument list. Forms 225-226 pin the two rejects
  (two-level and three-level/chan variants); form 227 pins the
  benign bytes control.
- Round-41 narrow re-review found a sidecar error-class
  divergence at HEAD 550d107: Go's immutable open stat'ed the main
  file before checking the canonical .readers sidecar, so a live
  database whose main file was missing/renamed but whose sidecar
  remained returned Io (31) while Rust's open_immutable refuses
  with WrongMode (11) because require_sidecar_absent runs before
  open_read_only. Fixed at the reader level: OpenImmutable now
  applies the same sidecarAbsentUnderLock check before the main
  file is touched (the under-lock re-check inside the mapping open
  stays authoritative), pinned by the pre-fix-failing test
  TestSidecarPresence/missing-main-sidecar-present.
- Round-42 gate re-review found the x/sys source-content gap at HEAD
  6733d1c: the path-only allowlist accepted a poisoned GOMODCACHE
  checkout (evil extracted dir plus download cache at the allowed path)
  and a file proxy serving an evil x/sys with a self-consistent forged
  go.sum (both proven live with a smuggled unix.Pread2, gate exit 0 on
  both vectors), because nothing pinned the module content; the gate now
  pins the exact version, the module-cache path, the extracted-tree
  content hash, and the module zip/go.mod sums to the official v0.35.0
  values, and the assembly-object rejection is case-insensitive, pinned
  as self-test forms 245-247, raising the set to one hundred
  ninety-seven mutation forms. The round-43 gate re-review then found
  the fail-open listing gap: the per-target go list ./... loop swallowed
  listing failures, so a module the go toolchain cannot list (symlinked
  package files, parse errors) passed with an empty package list and no
  import checks; go list failures now fail the gate per target and the
  per-package import listing fails closed too, pinned as form 248,
  raising the set to one hundred ninety-eight mutation forms. The round-45
  gate re-review then found the mmap-gate denylist gaps: os.CopyFS
  directory copies, os.OpenInRoot/os.OpenRoot handles reaching stream
  wrappers, and the x/sys descriptor-transfer primitives (unix.Tee,
  unix.Vmsplice, unix.IoctlFileClone/CloneRange/DedupeRange, darwin
  unix.Clonefile/Clonefileat) bypassed the scan (all proven live, gate
  exit 0); CopyFS and the x/sys primitives join the banned selector set,
  os.OpenInRoot/os.OpenRoot join the file-producer table so Root methods
  fail closed, pinned as self-test forms 249-251, raising the set to two
  hundred one mutation forms; the same-class P0 (Root laundered
  through a struct field: h.r.Open(name) after h := struct{r
  *os.Root}, gate exit 0) was then closed by resolving *os.Root as a
  file-bearing type everywhere *os.File does, pinned as form 252,
  raising the set to two hundred two mutation forms; the
  producer-value re-review then closed the file-method-value,
  initialized func-typed-variable (Root and *os.File), and plain
  stdlib-producer-value escapes (forms 253-256); the round-48
  adversarial re-review then closed bound method expressions on
  file-bearing receiver types (form-local and package-level) and
  same-module cross-package producer vars (forms 257-260), raising
  the set to two hundred ten mutation forms.
- Gates at current HEAD: go test ./... incl -race, go vet, gofmt,
  import graph with the 210-form self-test (round-32 rejects cover cgo, raw and no-error syscalls, linkname, preadv2/pwritev2; round-36 rejects 236-237, follow-up rejects 238-239, round-38 reject 240, round-39/40 rejects 241-244, round-42 rejects 245-247, and round-43 reject 248 cover the dup/exec subprocess escape, bodyless assembly stubs, the x/sys owner boundary, assembly objects, fcntl F_DUPFD duplication, out-of-tree module-graph attach, x/sys source replacement, hidden dot-directories, x/sys source-content spoofing (poisoned cache and file proxy with forged go.sum), case-variant assembly objects, and unlistable modules, and round-45
  rejects 249-256 cover os.CopyFS directory copies, os.OpenInRoot/
  os.OpenRoot handles reaching stream wrappers, the x/sys
  descriptor-transfer primitives, *os.Root laundering through struct
  fields, file method values, initialized func-typed variables with
  file-bearing declared results, and stdlib producer values bound
  without a declared type, and round-48 rejects 257-260 cover bound
  method expressions on file-bearing receiver types and same-module
  cross-package producer vars), ten cross-compiles,
  SOW audit - all green. Counts: production 4,792 raw lines / tests
  4,877 raw lines (gate scanner lives outside the module).
