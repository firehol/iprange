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
	herr := refuseOutputOverSource(source, source)
	if herr == nil {
		t.Fatal("same path accepted")
	}
	if herr.Code != "invalid_argument" || herr.Outcome != "not_started" ||
		herr.Message != "destination must differ from the source database" {
		t.Fatalf("error = code=%q outcome=%q message=%q, want invalid_argument/not_started with the source-refusal message",
			herr.Code, herr.Outcome, herr.Message)
	}
	other := filepath.Join(dir, "other.bin")
	if herr := refuseOutputOverSource(other, source); herr != nil {
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
