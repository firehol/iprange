package handlers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/cli/rpc"
)

// Exact copy of the wave-19.6 session test plus cleanup diagnostics
// (wave-19.13 Windows qualification).
func TestTmpProbeGCOnly(t *testing.T) {

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
	// The read re-opens with a share-delete handle on Windows (Go's
	// os.ReadFile does not share delete, and the live reader's sidecar
	// gate retains a DELETE-access handle; readSidecar_windows_test.go).
	bytes, err := readFileShareDelete(renamed)
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes) == 0 || bytes[0] == '{' {
		t.Fatalf("renamed sidecar was modified: head %q", bytes[:min(len(bytes), 20)])
	}
	runtime.GC()
}
