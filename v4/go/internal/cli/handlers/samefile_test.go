// Same-source output refusal and surrogate-refusal regression tests
// (product findings 1 and 3): a file destination that resolves to the
// source database path is refused with invalid_argument/not_started
// before any output publication (export, reader.metadata file
// delivery, and database.metadata.get), the source bytes stay intact
// and the database still opens, and ordinary distinct-destination
// exports still work. A database.create frame carrying an unpaired
// surrogate escape is refused with -32700 and leaves no files.

package handlers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	iprangedb "github.com/firehol/iprange/v4/go"
	"github.com/firehol/iprange/v4/go/internal/cli/fileio"
	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
	"github.com/firehol/iprange/v4/go/internal/worker"
)

// ---------------------------------------------------------------------------
// Real database fixture through the public SDK (no worker needed).
// ---------------------------------------------------------------------------

// newImmutableFeed creates one immutable membership database with the
// named metadata bytes and the single address 1.1.1.1 (Rust
// current.publish semantics through the SDK).
func newImmutableFeed(t *testing.T, dir, name string, metadata []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	input := filepath.Join(dir, name+".txt")
	if err := os.WriteFile(input, []byte("1.1.1.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	source, err := fileio.NewTextInputSource4([]string{input}, fileio.TextInputOptions{
		Family: fileio.AddressFamilyInputIPv4, FixNetwork: true, DefaultPrefix: 32,
		DNSThreads: 1, DNSSilent: true, MaxLineBytes: 1_048_576,
	}, true, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	tag, err := iprangedb.NewValueTag([]byte("v1"))
	if err != nil {
		t.Fatal(err)
	}
	feed, err := iprangedb.NewFeedName("testfeed")
	if err != nil {
		t.Fatal(err)
	}
	budget := &iprangedb.ImmutableFeedBudget{
		MaxHeapBytes: 10_485_760, MaxOutputPages: 256, MaxWorkspacePages: 256, MaxOpenFiles: 8,
	}
	result, err := iprangedb.CreateImmutableFeedV4(path, tag, feed, metadata,
		iprangedb.PolicyFailIfExists, source, budget, iprangedb.NewCancellationToken())
	if err != nil {
		t.Fatalf("create immutable feed: %v", err)
	}
	if result.Report.Addresses.String() != "1" {
		t.Fatalf("fixture addresses = %s, want 1", result.Report.Addresses.String())
	}
	return path
}

// assertSourceIntactAndOpen verifies the source bytes are unchanged
// and the database still opens and serves lookups after a refusal.
func assertSourceIntactAndOpen(t *testing.T, path string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read source after refusal: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("source bytes changed after refusal: %d -> %d bytes", len(before), len(after))
	}
	reader, err := iprangedb.OpenImmutable(path)
	if err != nil {
		t.Fatalf("database no longer opens after refusal: %v", err)
	}
	defer reader.Close()
	if _, err := reader.Info(); err != nil {
		t.Fatalf("database info after refusal: %v", err)
	}
	query, err := reader.MembershipQuery()
	if err != nil {
		t.Fatalf("membership query after refusal: %v", err)
	}
	token := iprangedb.NewCancellationToken()
	count := 0
	if _, err := query.MatchingFeedsV4(iprangedb.IPv4(0x01010101), func(name string) error {
		count++
		return nil
	}, token); err != nil {
		t.Fatalf("membership lookup after refusal: %v", err)
	}
	if count == 0 {
		t.Fatal("address 1.1.1.1 is no longer a member after refusal")
	}
}

// ---------------------------------------------------------------------------
// canonicalAbsolute / refuseOutputOverSource unit matrix
// ---------------------------------------------------------------------------

func TestCanonicalAbsoluteSameFileMatrix(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "db.bin")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "db-link.bin")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other.bin")
	if err := os.WriteFile(other, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Same-file spellings: the exact path, dot components, ".."
	// components, and a symlink to the source. The relative-spelling
	// arm is not available here because the scratch directory lives
	// outside the process working directory (filepath.Rel cannot
	// express it); canonicalAbsolute still joins relative inputs
	// against the process cwd exactly like Rust canonical_absolute.
	same := []string{
		source,
		filepath.Join(dir, ".", "db.bin"),
		filepath.Join(dir, "sub", "..", "db.bin"),
		link,
	}
	for i, left := range same {
		for j, right := range same {
			if canonicalAbsolute(left) != canonicalAbsolute(right) {
				t.Errorf("canonicalAbsolute(%q) != canonicalAbsolute(%q)", left, right)
			}
			_ = i
			_ = j
		}
	}
	if canonicalAbsolute(source) == canonicalAbsolute(other) {
		t.Fatal("distinct files compare equal")
	}
	// A not-yet-existing destination under a symlinked parent still
	// resolves through the link.
	linkDir := filepath.Join(dir, "dirlink")
	if err := os.Symlink(dir, linkDir); err != nil {
		t.Fatal(err)
	}
	fresh, err := filepath.Abs(filepath.Join(linkDir, "fresh.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if canonicalAbsolute(fresh) != filepath.Join(dir, "fresh.bin") {
		t.Fatalf("canonicalAbsolute(%q) = %q, want %q", fresh, canonicalAbsolute(fresh), filepath.Join(dir, "fresh.bin"))
	}
}

func TestRefuseOutputOverSourceErrorShape(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "db.bin")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	herr := refuseOutputOverSource(source, source, nil, nil)
	if herr == nil {
		t.Fatal("same path accepted")
	}
	if herr.Code != "invalid_argument" || herr.Outcome != "not_started" ||
		herr.Message != "destination must differ from the source database" {
		t.Fatalf("error = code=%q outcome=%q message=%q, want invalid_argument/not_started with the source-refusal message",
			herr.Code, herr.Outcome, herr.Message)
	}
	other := filepath.Join(dir, "other.bin")
	if herr := refuseOutputOverSource(other, source, nil, nil); herr != nil {
		t.Fatalf("distinct destination refused: %v", herr)
	}
}

// ---------------------------------------------------------------------------
// Export surface
// ---------------------------------------------------------------------------

func exportRequest(t *testing.T, source, destination, policy string) json.RawMessage {
	t.Helper()
	return json.RawMessage(fmt.Sprintf(
		`{"source":{"path":%q,"mode":"immutable"},"view":{"kind":"selection","selection":{"mode":"all"}},`+
			`"format":"ranges","destination":%q,"publication_policy":%q,`+
			`"result_budget":{"max_rows":"100","max_output_bytes":"1048576","max_open_files":8}}`,
		source, destination, policy))
}

func TestExportRefusesSourceDestination(t *testing.T) {
	dir := t.TempDir()
	source := newImmutableFeed(t, dir, "src.db", nil)
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	// Same-file destination spellings: the exact path, dot
	// components, and ".." components. A cwd-relative spelling cannot
	// express files outside the process working directory.
	for _, destination := range []string{
		source,
		filepath.Join(dir, ".", "src.db"),
		filepath.Join(dir, "sub", "..", "src.db"),
	} {
		st := rpc.NewSessionState()
		result, herr := Export(st, exportRequest(t, source, destination, "replace_existing"))
		if herr == nil {
			t.Fatalf("export to %q succeeded (%v), want refusal", destination, result)
		}
		if herr.Code != "invalid_argument" || herr.Message != "destination must differ from the source database" {
			t.Fatalf("export to %q: code=%q message=%q", destination, herr.Code, herr.Message)
		}
	}
	assertSourceIntactAndOpen(t, source, before)
}

// buildExportWorker compiles the real cmd/iprange-v4-worker once per
// test run so the export identity inspection routes through the
// isolated worker exactly like production (the root-package harness
// precedent).
var (
	exportWorkerOnce    sync.Once
	exportWorkerCleanup sync.Once
	exportWorkerPath    string
	exportWorkerErr     error
)

func buildExportWorker() string {
	exportWorkerOnce.Do(func() {
		dir, err := os.MkdirTemp("", "iprange-handlers-worker-")
		if err != nil {
			exportWorkerErr = err
			return
		}
		exportWorkerPath = filepath.Join(dir, "iprange-v4-worker")
		root, err := goModuleRoot()
		if err != nil {
			exportWorkerErr = err
			return
		}
		cmd := exec.Command("go", "-C", root, "build", "-o", exportWorkerPath, "./cmd/iprange-v4-worker")
		if output, err := cmd.CombinedOutput(); err != nil {
			exportWorkerErr = fmt.Errorf("build worker: %v\n%s", err, output)
		}
	})
	return exportWorkerPath
}

func goModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("v4/go module root not found from %s", dir)
		}
		dir = parent
	}
}

func installRealWorker(t *testing.T) {
	t.Helper()
	path := buildExportWorker()
	if exportWorkerErr != nil {
		t.Fatal(exportWorkerErr)
	}
	t.Cleanup(func() {
		worker.SetWorkerCandidatesForTest(nil)
		exportWorkerCleanup.Do(func() { _ = os.RemoveAll(filepath.Dir(exportWorkerPath)) })
	})
	worker.SetWorkerCandidatesForTest(func() ([]string, error) { return []string{path}, nil })
}

func TestExportDistinctDestinationStillWorks(t *testing.T) {
	installRealWorker(t)
	dir := t.TempDir()
	source := newImmutableFeed(t, dir, "src.db", nil)
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "export.txt")
	st := rpc.NewSessionState()
	result, herr := Export(st, exportRequest(t, source, destination, "fail_if_exists"))
	if herr != nil {
		t.Fatalf("distinct-destination export refused: %v", herr)
	}
	facts, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result = %T", result)
	}
	if facts["path"] != destination || facts["rows"] != "1" {
		t.Fatalf("facts = %v", facts)
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "1.1.1.1\n" {
		t.Fatalf("export content %q, want 1.1.1.1\n", content)
	}
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("source changed after a distinct-destination export")
	}
}

// ---------------------------------------------------------------------------
// Metadata file delivery surfaces
// ---------------------------------------------------------------------------

func metadataDeliveryObject(t *testing.T, path, policy string) rawObject {
	t.Helper()
	return rawObject{
		"mode":               json.RawMessage(`"file"`),
		"path":               json.RawMessage(mustJSONString(path)),
		"publication_policy": json.RawMessage(mustJSONString(policy)),
		"max_output_bytes":   json.RawMessage(`"1048576"`),
		"max_open_files":     json.RawMessage(`8`),
	}
}

func mustJSONString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func TestMetadataFileDeliveryRefusesSource(t *testing.T) {
	dir := t.TempDir()
	source := newImmutableFeed(t, dir, "src.db", []byte("mymetadata"))
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := iprangedb.OpenImmutable(source)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	rv := &rpc.ReaderValue{Immutable: reader, Path: source}

	// Handle-backed reader.metadata: same-path refusal, source intact.
	result, herr := deliverMetadata("iprange.v1.reader.metadata", rv, metadataDeliveryObject(t, source, "replace_existing"))
	if herr == nil {
		t.Fatalf("reader.metadata to the source path succeeded (%v), want refusal", result)
	}
	if herr.Code != "invalid_argument" || herr.Message != "destination must differ from the source database" {
		t.Fatalf("reader.metadata: code=%q message=%q", herr.Code, herr.Message)
	}
	assertSourceIntactAndOpen(t, source, before)

	// Distinct destination still publishes the metadata bytes.
	out := filepath.Join(dir, "meta.bin")
	result, herr = deliverMetadata("iprange.v1.reader.metadata", rv, metadataDeliveryObject(t, out, "fail_if_exists"))
	if herr != nil {
		t.Fatalf("distinct metadata destination refused: %v", herr)
	}
	facts, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result = %T", result)
	}
	output, ok := facts["output"].(map[string]any)
	if !ok || output["path"] != out {
		t.Fatalf("output facts = %v, want path %s", facts["output"], out)
	}
	content, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "mymetadata" {
		t.Fatalf("metadata output %q, want mymetadata", content)
	}
}

// ---------------------------------------------------------------------------
// Full-session surfaces (handlers registered; real worker not needed)
// ---------------------------------------------------------------------------

var registerAllOnce sync.Once

func registerHandlers() {
	registerAllOnce.Do(RegisterAll)
}

func runSession(t *testing.T, input string) string {
	t.Helper()
	registerHandlers()
	session := rpc.NewSession()
	var out bytes.Buffer
	if err := session.Run(strings.NewReader(input), &out); err != nil {
		t.Fatalf("session run: %v", err)
	}
	return out.String()
}

func TestSessionDatabaseMetadataGetRefusesSource(t *testing.T) {
	dir := t.TempDir()
	source := newImmutableFeed(t, dir, "src.db", []byte("mymetadata"))
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	frame := `{"jsonrpc":"2.0","id":"1","method":"iprange.v1.database.metadata.get","params":{"source":{"path":` +
		mustJSONString(source) + `,"mode":"immutable"},"delivery":{"mode":"file","path":` +
		mustJSONString(source) + `,"publication_policy":"replace_existing","max_output_bytes":"1048576","max_open_files":8}}}`
	out := runSession(t, frame)
	if !strings.Contains(out, `"code":"invalid_argument"`) ||
		!strings.Contains(out, "destination must differ from the source database") {
		t.Fatalf("output = %q, want the source-refusal error", out)
	}
	assertSourceIntactAndOpen(t, source, before)
}

func TestSessionReaderMetadataHandleRefusesSource(t *testing.T) {
	dir := t.TempDir()
	source := newImmutableFeed(t, dir, "src.db", []byte("mymetadata"))
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	registerHandlers()
	openFrame := `{"jsonrpc":"2.0","id":"1","method":"iprange.v1.reader.open","params":{"source":{"path":` +
		mustJSONString(source) + `,"mode":"immutable"}}}`
	metaFrame := `{"jsonrpc":"2.0","id":"2","method":"iprange.v1.reader.metadata","params":{"reader":` +
		mustJSONString("@HANDLE@") + `,"delivery":{"mode":"file","path":` +
		mustJSONString(source) + `,"publication_policy":"replace_existing","max_output_bytes":"1048576","max_open_files":8}}}`

	// The connection handle is random and session-local, so the two
	// frames share one live session: the open response is read, the
	// handle is substituted into the metadata frame, and the second
	// response is read before the transport closes.
	session := rpc.NewSession()
	pr, pw := io.Pipe()
	defer pr.Close()
	outR, outW := io.Pipe()
	defer outR.Close()
	done := make(chan error, 1)
	go func() { done <- session.Run(pr, outW) }()
	if _, err := fmt.Fprintf(pw, "%s\n", openFrame); err != nil {
		t.Fatalf("write open frame: %v", err)
	}
	first, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil {
		t.Fatalf("read open response: %v", err)
	}
	var openResponse struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(first)), &openResponse); err != nil {
		t.Fatalf("open response %q: %v", first, err)
	}
	var handle string
	if err := json.Unmarshal(openResponse.Result["reader"], &handle); err != nil || handle == "" {
		t.Fatalf("reader handle %s: %v", openResponse.Result["reader"], err)
	}
	metaFrame = strings.Replace(metaFrame, mustJSONString("@HANDLE@"), mustJSONString(handle), 1)
	if _, err := fmt.Fprintf(pw, "%s\n", metaFrame); err != nil {
		t.Fatalf("write metadata frame: %v", err)
	}
	second, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil && second == "" {
		t.Fatalf("read metadata response: %v", err)
	}
	_ = pw.Close()
	<-done
	if !strings.Contains(second, `"code":"invalid_argument"`) ||
		!strings.Contains(second, "destination must differ from the source database") {
		t.Fatalf("metadata response %q, want the source-refusal error", second)
	}
	assertSourceIntactAndOpen(t, source, before)
}

func TestSessionDatabaseCreateSurrogateRefusedLeavesNoFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "create.db")
	frame := `{"jsonrpc":"2.0","id":"1","method":"iprange.v1.database.create","params":{"path":` +
		mustJSONString(target) + `,"family":"ipv4","value_kind":"membership","structure_kind":"none",` +
		`"value_tag":{"text":"\ud800"},"reader_capacity":8}}`
	out := runSession(t, frame)
	if !strings.Contains(out, `"code":-32700`) {
		t.Fatalf("output = %q, want -32700 for the unpaired surrogate", out)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("database.create left a file at %s", target)
	}
	// No publication temporaries either.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			t.Fatalf("leftover artifact %s after refused create", entry.Name())
		}
	}
}

// TestCanonicalAbsoluteDecorations pins the wave-19.4 canonicalization
// parity: a trailing separator and a non-existent-ancestor ".."
// spelling must resolve to the same canonical identity as the plain
// source path (Go Clean at entry; Rust lexical_clean_path parity), so
// both engines refuse those destinations before any output temporary
// exists instead of failing late at the rename.
func TestCanonicalAbsoluteDecorations(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "db.bin")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []string{
		source + string(os.PathSeparator),
		filepath.Join(dir, "nosuch", "..", "db.bin"),
	} {
		if canonicalAbsolute(spelling) != canonicalAbsolute(source) {
			t.Errorf("canonicalAbsolute(%q) = %q, want %q",
				spelling, canonicalAbsolute(spelling), canonicalAbsolute(source))
		}
	}
	// Distinct target under the same decorations stays distinct.
	other := filepath.Join(dir, "other.bin")
	if canonicalAbsolute(filepath.Join(dir, "nosuch", "..", "other.bin")) !=
		canonicalAbsolute(other) {
		t.Fatal("distinct decorated target does not resolve to itself")
	}
}

// TestRefuseOutputOverSourceFileIdentity pins the wave-19.4 file
// identity arm of the guard: a destination that is the same FILE as
// the source (through a rename or a hard link) is refused even though
// its pathname differs, and a genuinely distinct file is accepted.
func TestRefuseOutputOverSourceFileIdentity(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "db.bin")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(dir, "db.bak")
	if err := os.Rename(source, renamed); err != nil {
		t.Fatal(err)
	}
	if herr := refuseOutputOverSource(renamed, source, sourceInfo, nil); herr == nil {
		t.Fatal("renamed same-file destination accepted")
	}
	// Restore the source name and try a hard-link alias.
	if err := os.Rename(renamed, source); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "db-alias.bin")
	if err := os.Link(source, link); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if herr := refuseOutputOverSource(link, source, sourceInfo, nil); herr == nil {
		t.Fatal("hard-link same-file destination accepted")
	}
	other := filepath.Join(dir, "other.bin")
	if err := os.WriteFile(other, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	if herr := refuseOutputOverSource(other, source, sourceInfo, nil); herr != nil {
		t.Fatalf("distinct destination refused: %v", herr)
	}
	// A nil source identity still refuses the same pathname (the
	// pathname arm is independent of the stat result).
	if herr := refuseOutputOverSource(source, source, nil, nil); herr == nil {
		t.Fatal("same pathname with nil identity accepted")
	}
}

// TestRefuseOutputOverSourceSidecar pins the wave-19.5 P1 repair: the
// live database's reader-coordination sidecar (main + ".readers") is a
// distinct file that records reader state; publishing output over it
// destroys the source's readability, so the guard refuses the sidecar
// pathname, its decorated spellings, and a hard-link alias of the
// sidecar FILE, while a genuinely distinct file is accepted.
func TestRefuseOutputOverSourceSidecar(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "db.bin")
	if err := os.WriteFile(source, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(dir, "db.bin.readers")
	if err := os.WriteFile(sidecar, []byte("readers"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	sidecarInfo, err := os.Stat(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{
		sidecar,
		filepath.Join(dir, "sub", "..", "db.bin.readers"),
	} {
		herr := refuseOutputOverSource(destination, source, sourceInfo, sidecarInfo)
		if herr == nil {
			t.Fatalf("sidecar destination %q accepted", destination)
		}
		if herr.Code != "invalid_argument" || herr.Outcome != "not_started" ||
			herr.Message != "destination must differ from the source database" {
			t.Fatalf("destination %q: code=%q outcome=%q message=%q",
				destination, herr.Code, herr.Outcome, herr.Message)
		}
	}
	// A hard-link alias of the sidecar FILE is refused through the
	// same-file arm even though its pathname differs.
	alias := filepath.Join(dir, "sidecar-alias.bin")
	if err := os.Link(sidecar, alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if herr := refuseOutputOverSource(alias, source, sourceInfo, sidecarInfo); herr == nil {
		t.Fatal("hard-link sidecar destination accepted")
	}
	// A distinct file stays accepted.  This assertion runs BEFORE the
	// sidecar is renamed and removed: a fresh file created after the
	// sidecar inode was freed can reuse that exact inode on common
	// filesystems, which would make the captured-identity comparison
	// refuse it and the test nondeterministic (tester role wave-19.6
	// determinism finding).
	other := filepath.Join(dir, "other.bin")
	if err := os.WriteFile(other, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	if herr := refuseOutputOverSource(other, source, sourceInfo, sidecarInfo); herr != nil {
		t.Fatalf("distinct destination refused: %v", herr)
	}
	// A RENAMED sidecar keeps its captured identity (tester role
	// wave-19.6): a destination at the renamed path is refused through
	// the same-file arm even though its pathname no longer matches.
	renamed := filepath.Join(dir, "db.bin.readers.old")
	if err := os.Rename(sidecar, renamed); err != nil {
		t.Fatal(err)
	}
	herr := refuseOutputOverSource(renamed, source, sourceInfo, sidecarInfo)
	if herr == nil {
		t.Fatal("renamed sidecar destination accepted")
	}
	if herr.Code != "invalid_argument" || herr.Outcome != "not_started" ||
		herr.Message != "destination must differ from the source database" {
		t.Fatalf("renamed sidecar: code=%q outcome=%q message=%q",
			herr.Code, herr.Outcome, herr.Message)
	}
	// An ephemeral guard without the captured identity accepts the
	// renamed pathname (a fresh stat no longer matches it); the
	// wave-19.6 record documents this handle-vs-preflight distinction.
	if herr := refuseOutputOverSource(renamed, source, sourceInfo, nil); herr != nil {
		t.Fatalf("ephemeral guard refused the renamed pathname: %v", herr)
	}
}

// TestRefuseOutputOverSourceSidecarReservedName pins the wave-19.6
// parity repair: the sidecar derivation is purely lexical (no
// main-name grammar), so a reserved-name source such as "x.readers"
// derives "x.readers.readers" and the guard refuses that destination
// preflight with the canonical shape instead of unarming the sidecar
// arm and failing later at the SDK open with a different outcome
// (Rust sidecar_path parity).
func TestRefuseOutputOverSourceSidecarReservedName(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "x.readers")
	if err := os.WriteFile(source, []byte("coordination"), 0o644); err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(dir, "x.readers.readers")
	herr := refuseOutputOverSource(sidecar, source, sourceInfo, nil)
	if herr == nil {
		t.Fatal("reserved-name sidecar destination accepted")
	}
	if herr.Code != "invalid_argument" || herr.Outcome != "not_started" ||
		herr.Message != "destination must differ from the source database" {
		t.Fatalf("code=%q outcome=%q message=%q", herr.Code, herr.Outcome, herr.Message)
	}
}

// newLiveFeed creates one live membership database with the single
// address 1.1.1.1 through the public SDK; the fixture carries the
// reader-coordination sidecar (main + ".readers") like a live product
// database.
func newLiveFeed(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	tag, err := iprangedb.NewValueTag([]byte("v1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iprangedb.CreateLive(path, iprangedb.AddressFamilyIPv4,
		iprangedb.ValueKindDirect, iprangedb.StructureKindNone, tag, 4, nil); err != nil {
		t.Fatal(err)
	}
	writer, err := iprangedb.OpenLiveWriter(path, iprangedb.DefaultBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := writer.BeginDirect(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.AssignV4(iprangedb.IPv4(0x01010101), iprangedb.IPv4(0x01010101), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSessionReaderMetadataRefusesLiveSidecar pins the wave-19.5 P1
// repair at the session level: a live reader's sidecar (main +
// ".readers") is a file-delivery destination that would destroy the
// database's readability, so it is refused with the canonical error
// shape and the sidecar file stays untouched.
func TestSessionReaderMetadataRefusesLiveSidecar(t *testing.T) {
	dir := t.TempDir()
	source := newLiveFeed(t, dir, "live.db")
	sidecar := source + ".readers"
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("live fixture is missing its sidecar: %v", err)
	}
	registerHandlers()
	openFrame := `{"jsonrpc":"2.0","id":"1","method":"iprange.v1.reader.open","params":{"source":{"path":` +
		mustJSONString(source) + `,"mode":"live"}}}`
	metaFrame := `{"jsonrpc":"2.0","id":"2","method":"iprange.v1.reader.metadata","params":{"reader":` +
		mustJSONString("@HANDLE@") + `,"delivery":{"mode":"file","path":` +
		mustJSONString(sidecar) + `,"publication_policy":"replace_existing","max_output_bytes":"1048576","max_open_files":8}}}`

	session := rpc.NewSession()
	pr, pw := io.Pipe()
	defer pr.Close()
	outR, outW := io.Pipe()
	defer outR.Close()
	done := make(chan error, 1)
	go func() { done <- session.Run(pr, outW) }()
	if _, err := fmt.Fprintf(pw, "%s\n", openFrame); err != nil {
		t.Fatalf("write open frame: %v", err)
	}
	first, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil {
		t.Fatalf("read open response: %v", err)
	}
	var openResponse struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(first)), &openResponse); err != nil {
		t.Fatalf("open response %q: %v", first, err)
	}
	var handle string
	if err := json.Unmarshal(openResponse.Result["reader"], &handle); err != nil || handle == "" {
		t.Fatalf("reader handle %s: %v", openResponse.Result["reader"], err)
	}
	metaFrame = strings.Replace(metaFrame, mustJSONString("@HANDLE@"), mustJSONString(handle), 1)
	if _, err := fmt.Fprintf(pw, "%s\n", metaFrame); err != nil {
		t.Fatalf("write metadata frame: %v", err)
	}
	second, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil && second == "" {
		t.Fatalf("read metadata response: %v", err)
	}
	_ = pw.Close()
	<-done
	if !strings.Contains(second, `"code":"invalid_argument"`) ||
		!strings.Contains(second, "destination must differ from the source database") {
		t.Fatalf("metadata response %q, want the source-refusal error", second)
	}
	// The sidecar is still the sidecar, not metadata text.
	bytes, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes) == 0 || bytes[0] == '{' {
		t.Fatalf("sidecar was modified: head %q", bytes[:min(len(bytes), 20)])
	}
}

// TestSessionReaderMetadataRefusesRenamedLiveSidecar pins the
// wave-19.6 P1 repair at the session level: a live reader captures
// the sidecar identity at open, so a file delivery whose destination
// is the sidecar RENAMED while the reader is open is refused through
// the same-file arm (mirroring the renamed-main case) instead of
// publishing metadata text over the displaced coordination file.
func TestSessionReaderMetadataRefusesRenamedLiveSidecar(t *testing.T) {
	dir := t.TempDir()
	source := newLiveFeed(t, dir, "live2.db")
	sidecar := source + ".readers"
	renamed := sidecar + ".old"
	registerHandlers()
	openFrame := `{"jsonrpc":"2.0","id":"1","method":"iprange.v1.reader.open","params":{"source":{"path":` +
		mustJSONString(source) + `,"mode":"live"}}}`
	metaFrame := `{"jsonrpc":"2.0","id":"2","method":"iprange.v1.reader.metadata","params":{"reader":` +
		mustJSONString("@HANDLE@") + `,"delivery":{"mode":"file","path":` +
		mustJSONString(renamed) + `,"publication_policy":"replace_existing","max_output_bytes":"1048576","max_open_files":8}}}`

	session := rpc.NewSession()
	pr, pw := io.Pipe()
	defer pr.Close()
	outR, outW := io.Pipe()
	defer outR.Close()
	done := make(chan error, 1)
	go func() { done <- session.Run(pr, outW) }()
	if _, err := fmt.Fprintf(pw, "%s\n", openFrame); err != nil {
		t.Fatalf("write open frame: %v", err)
	}
	first, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil {
		t.Fatalf("read open response: %v", err)
	}
	var openResponse struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(first)), &openResponse); err != nil {
		t.Fatalf("open response %q: %v", first, err)
	}
	var handle string
	if err := json.Unmarshal(openResponse.Result["reader"], &handle); err != nil || handle == "" {
		t.Fatalf("reader handle %s: %v", openResponse.Result["reader"], err)
	}
	// The reader is open: rename the sidecar now, then deliver to the
	// renamed path (the captured sidecar identity must refuse it).
	if err := os.Rename(sidecar, renamed); err != nil {
		t.Fatal(err)
	}
	metaFrame = strings.Replace(metaFrame, mustJSONString("@HANDLE@"), mustJSONString(handle), 1)
	if _, err := fmt.Fprintf(pw, "%s\n", metaFrame); err != nil {
		t.Fatalf("write metadata frame: %v", err)
	}
	second, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil && second == "" {
		t.Fatalf("read metadata response: %v", err)
	}
	_ = pw.Close()
	<-done
	if !strings.Contains(second, `"code":"invalid_argument"`) ||
		!strings.Contains(second, "destination must differ from the source database") {
		t.Fatalf("metadata response %q, want the source-refusal error", second)
	}
	// The displaced sidecar file is untouched, not metadata text.
	bytes, err := os.ReadFile(renamed)
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes) == 0 || bytes[0] == '{' {
		t.Fatalf("renamed sidecar was modified: head %q", bytes[:min(len(bytes), 20)])
	}
}

// TestSessionReaderMetadataRenamedSourceRefused pins the wave-19.4 P1
// repair at the session level: a reader whose source pathname was
// renamed while the handle is open must still refuse a file delivery
// to the renamed path (the destination is the same file), and the
// file itself must stay unmodified.
func TestSessionReaderMetadataRenamedSourceRefused(t *testing.T) {
	dir := t.TempDir()
	source := newImmutableFeed(t, dir, "src.db", []byte("mymetadata"))
	renamed := filepath.Join(dir, "src.bak")
	if err := os.Rename(source, renamed); err != nil {
		t.Fatal(err)
	}
	registerHandlers()
	openFrame := `{"jsonrpc":"2.0","id":"1","method":"iprange.v1.reader.open","params":{"source":{"path":` +
		mustJSONString(renamed) + `,"mode":"immutable"}}}`
	metaFrame := `{"jsonrpc":"2.0","id":"2","method":"iprange.v1.reader.metadata","params":{"reader":` +
		mustJSONString("@HANDLE@") + `,"delivery":{"mode":"file","path":` +
		mustJSONString(renamed) + `,"publication_policy":"replace_existing","max_output_bytes":"1048576","max_open_files":8}}}`

	session := rpc.NewSession()
	pr, pw := io.Pipe()
	defer pr.Close()
	outR, outW := io.Pipe()
	defer outR.Close()
	done := make(chan error, 1)
	go func() { done <- session.Run(pr, outW) }()
	if _, err := fmt.Fprintf(pw, "%s\n", openFrame); err != nil {
		t.Fatalf("write open frame: %v", err)
	}
	first, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil {
		t.Fatalf("read open response: %v", err)
	}
	var openResponse struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(first)), &openResponse); err != nil {
		t.Fatalf("open response %q: %v", first, err)
	}
	var handle string
	if err := json.Unmarshal(openResponse.Result["reader"], &handle); err != nil || handle == "" {
		t.Fatalf("reader handle %s: %v", openResponse.Result["reader"], err)
	}
	metaFrame = strings.Replace(metaFrame, mustJSONString("@HANDLE@"), mustJSONString(handle), 1)
	if _, err := fmt.Fprintf(pw, "%s\n", metaFrame); err != nil {
		t.Fatalf("write metadata frame: %v", err)
	}
	second, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil && second == "" {
		t.Fatalf("read metadata response: %v", err)
	}
	_ = pw.Close()
	<-done
	if !strings.Contains(second, `"code":"invalid_argument"`) ||
		!strings.Contains(second, "destination must differ from the source database") {
		t.Fatalf("metadata response %q, want the source-refusal error", second)
	}
	// The renamed file is still the database, not metadata text.
	bytes, err := os.ReadFile(renamed)
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes) == 0 || bytes[0] == '{' {
		t.Fatalf("renamed source was modified: head %q", bytes[:min(len(bytes), 20)])
	}
}

// TestExportRefusesDecoratedSourceSpelling pins the wave-19.4
// preflight claim for decorated spellings: exporting over
// "source.db/" or "nosuch/../source.db" is refused before any output
// temporary exists, in the same call as the plain-path refusal.
func TestExportRefusesDecoratedSourceSpelling(t *testing.T) {
	dir := t.TempDir()
	source := newImmutableFeed(t, dir, "src.db", []byte("mymetadata"))
	for _, spelling := range []string{
		source + string(os.PathSeparator),
		filepath.Join(dir, "nosuch", "..", "src.db"),
	} {
		st := rpc.NewSessionState()
		result, herr := Export(st, exportRequest(t, source, spelling, "replace_existing"))
		if herr == nil {
			t.Fatalf("export to %q succeeded (%v), want refusal", spelling, result)
		}
		if herr.Code != "invalid_argument" || herr.Message != "destination must differ from the source database" {
			t.Fatalf("export to %q: code=%q message=%q", spelling, herr.Code, herr.Message)
		}
	}
	// No output residue anywhere: the refusal fired before the
	// destination namespace was touched.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "src.db" && entry.Name() != "src.db.txt" {
			t.Fatalf("unexpected residue after refusal: %s", entry.Name())
		}
	}
}
