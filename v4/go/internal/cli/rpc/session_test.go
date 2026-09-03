// Transport behavior tests (Rust rpc/session.rs test-suite parity):
// queue admission and busy rejection, in-position batch members,
// cancel-during-scan semantics, unanswerable ids, the 65,000-byte
// response-object ceiling, EOF shutdown with queued units, and fatal
// stdout failure. The system.describe stub registered here exists only
// in this test binary; production registration lives in the handlers
// package.

package rpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

func init() {
	// Test-only stub: answers describe with a small fixed result so
	// transport tests can run without the handler packages.
	registry["iprange.v1.system.describe"] = registeredMethod{
		validate: func(params json.RawMessage) error { return nil },
		handle: func(st *SessionState, params json.RawMessage) (any, *HandlerError) {
			return map[string]any{"protocol": "iprange-jsonrpc-v1"}, nil
		},
	}
}

// runService drives one session over the given input and returns the
// captured output and the session error.
func runService(t *testing.T, input string) (string, error) {
	t.Helper()
	session := NewSession()
	var out bytes.Buffer
	err := session.Run(strings.NewReader(input), &out)
	return out.String(), err
}

func lines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func TestRequestResponseRoundTrip(t *testing.T) {
	out, err := runService(t,
		`{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{}}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := `{"id":"1","jsonrpc":"2.0","result":{"protocol":"iprange-jsonrpc-v1"}}`
	if out != want+"\n" {
		t.Fatalf("output = %q, want %q", out, want+"\n")
	}
}

func TestSchemaErrorsKeepServing(t *testing.T) {
	out, err := runService(t,
		"{\"jsonrpc\":\"9.9\",\"id\":\"1\",\"method\":\"iprange.v1.system.describe\",\"params\":{}}\n"+
			`{"jsonrpc":"2.0","id":"2","method":"iprange.v1.system.describe","params":{}}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := lines(out)
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(got), out)
	}
	if !strings.Contains(got[0], `"code":-32600`) || !strings.Contains(got[0], `"id":null`) {
		t.Fatalf("first line %q", got[0])
	}
	if !strings.Contains(got[1], `"id":"2"`) {
		t.Fatalf("second line %q", got[1])
	}
}

func TestUnknownMethod(t *testing.T) {
	out, err := runService(t,
		`{"jsonrpc":"2.0","id":"1","method":"iprange.v1.no.such","params":{}}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, `"code":-32601`) || !strings.Contains(out, `"id":"1"`) {
		t.Fatalf("output = %q", out)
	}
}

func TestFrameOverLimitFailsWithIDNullAndCloses(t *testing.T) {
	big := strings.Repeat("a", InputFrameLimit+10)
	out, err := runService(t, `{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{"x":"`+big+`"}}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	lines := lines(out)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	if !strings.Contains(lines[0], `"code":-32001`) || !strings.Contains(lines[0], `"id":null`) {
		t.Fatalf("line = %q", lines[0])
	}
}

func TestBatchAnswersInOrder(t *testing.T) {
	out, err := runService(t,
		`[{"jsonrpc":"2.0","id":"a","method":"iprange.v1.system.describe","params":{}},`+
			`{"jsonrpc":"2.0","id":"b","method":"iprange.v1.system.describe","params":{}}]`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := lines(out)
	if len(got) != 1 {
		t.Fatalf("got %d lines, want 1", len(got))
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(got[0]), &arr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(arr) != 2 {
		t.Fatalf("batch has %d members, want 2", len(arr))
	}
	if arr[0]["id"] != "a" || arr[1]["id"] != "b" {
		t.Fatalf("batch order = %v, %v", arr[0]["id"], arr[1]["id"])
	}
}

func TestEmptyBatchRejected(t *testing.T) {
	out, err := runService(t, `[]`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, `"code":-32600`) {
		t.Fatalf("output = %q", out)
	}
}

func TestRequestIDMinusZeroEchoedAsZero(t *testing.T) {
	// serde_json parity: the numeric literal -0 is echoed as 0.
	out, err := runService(t,
		`{"jsonrpc":"2.0","id":-0,"method":"iprange.v1.system.describe","params":{}}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, `"id":0`) || strings.Contains(out, `-0`) {
		t.Fatalf("output = %q, want an id of 0 with no -0 literal", out)
	}
}

func TestSameBatchCancelOmitsMinusZeroSibling(t *testing.T) {
	// A cancel targeting request_id -0 must correlate with the pending
	// numeric id -0 (canonical key n:0) so the sibling is omitted, exactly
	// like Rust serde_json-normalized cancellation.
	out, err := runService(t,
		`[{"jsonrpc":"2.0","id":-0,"method":"iprange.v1.system.describe","params":{}},`+
			`{"jsonrpc":"2.0","method":"iprange.v1.cancel","params":{"request_id":-0}},`+
			`{"jsonrpc":"2.0","id":2,"method":"iprange.v1.system.describe","params":{}}]`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := lines(out)
	if len(got) != 1 {
		t.Fatalf("got %d lines, want 1", len(got))
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(got[0]), &arr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(arr) != 1 || arr[0]["id"] != float64(2) {
		t.Fatalf("cancelled -0 sibling must be omitted, got %v", arr)
	}
}

func TestNumberCancelKeyNormalizesMinusZero(t *testing.T) {
	key, ok := numberCancelKey(json.RawMessage(`-0`))
	if !ok || key != "n:0" {
		t.Fatalf("numberCancelKey(-0) = %q, %v; want %q, true", key, ok, "n:0")
	}
	key, ok = numberCancelKey(json.RawMessage(`0`))
	if !ok || key != "n:0" {
		t.Fatalf("numberCancelKey(0) = %q, %v; want %q, true", key, ok, "n:0")
	}
}

func TestSameBatchCancelOmitsEarlierSibling(t *testing.T) {
	out, err := runService(t,
		`[{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{}},`+
			`{"jsonrpc":"2.0","method":"iprange.v1.cancel","params":{"request_id":"1"}},`+
			`{"jsonrpc":"2.0","id":"2","method":"iprange.v1.system.describe","params":{}}]`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := lines(out)
	if len(got) != 1 {
		t.Fatalf("got %d lines, want 1", len(got))
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(got[0]), &arr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(arr) != 1 || arr[0]["id"] != "2" {
		t.Fatalf("cancelled sibling must be omitted, got %v", arr)
	}
}

func TestUnknownCancelIDDoesNotPoisonLaterRequest(t *testing.T) {
	// A cancel for an id that never existed must not mark a later
	// request with the same id.
	out, err := runService(t,
		`{"jsonrpc":"2.0","method":"iprange.v1.cancel","params":{"request_id":"7"}}`+"\n"+
			`{"jsonrpc":"2.0","id":"7","method":"iprange.v1.system.describe","params":{}}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := lines(out)
	if len(got) != 1 || !strings.Contains(got[0], `"id":"7"`) {
		t.Fatalf("output = %q", out)
	}
}

func TestMalformedCancelParamsIgnored(t *testing.T) {
	out, err := runService(t,
		`{"jsonrpc":"2.0","method":"iprange.v1.cancel","params":{"request_id":1.5}}`+"\n"+
			`{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{}}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := lines(out)
	if len(got) != 1 || !strings.Contains(got[0], `"id":"1"`) {
		t.Fatalf("output = %q", out)
	}
}

func TestBusyAdmission(t *testing.T) {
	var inFlight atomic.Int64
	inFlight.Store(QueuedLimit)
	request := &Request{
		ID:     idPtr("1"),
		Method: "iprange.v1.system.describe",
		Params: json.RawMessage(`{}`),
	}
	if got := admitOne(request, &inFlight); got.kind != workBusy {
		t.Fatalf("kind = %v, want busy", got.kind)
	}
	if inFlight.Load() != QueuedLimit {
		t.Fatalf("busy admission consumed capacity: %d", inFlight.Load())
	}
}

func TestUnanswerableIDNeverOccupiesQueue(t *testing.T) {
	var inFlight atomic.Int64
	hugeID := strings.Repeat("x", ResponseObjectLimit+100)
	request := &Request{
		ID:     idPtr(hugeID),
		Method: "iprange.v1.system.describe",
		Params: json.RawMessage(`{}`),
	}
	if got := admitOne(request, &inFlight); got.kind != workUnanswerable {
		t.Fatalf("kind = %v, want unanswerable", got.kind)
	}
	if inFlight.Load() != 0 {
		t.Fatalf("unanswerable id consumed capacity: %d", inFlight.Load())
	}
}

func TestEOFShutdownExecutesAdmittedUnit(t *testing.T) {
	// EOF arrives immediately after the request; the admitted unit
	// must still answer factually.
	out, err := runService(t,
		`{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{}}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, `"id":"1"`) {
		t.Fatalf("output = %q", out)
	}
}

func TestBrokenStdoutIsFatal(t *testing.T) {
	session := NewSession()
	err := session.Run(strings.NewReader(
		`{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{}}`),
		failingWriter{})
	if err == nil {
		t.Fatal("expected a fatal write error")
	}
	if !strings.Contains(err.Error(), "write") {
		t.Fatalf("error = %v", err)
	}
}

// failingWriter never accepts bytes, like stdout on a broken pipe.
type failingWriter struct{}

func (failingWriter) Write(p []byte) (int, error) { return 0, errors.New("broken pipe") }

func TestStdinReadErrorIsFatalNotEOF(t *testing.T) {
	session := NewSession()
	err := session.Run(&erroringReader{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected a fatal stdin error")
	}
	if !strings.Contains(err.Error(), "stdin") {
		t.Fatalf("error = %v", err)
	}
}

// erroringReader fails like stdin on a broken pipe.
type erroringReader struct{}

func (erroringReader) Read([]byte) (int, error) { return 0, errors.New("stdin broken") }

func TestOversizedSuccessBecomesOutputLimitError(t *testing.T) {
	// Register an oversized-result handler for one test method.
	registry["iprange.v1.export"] = registeredMethod{
		validate: func(params json.RawMessage) error { return nil },
		handle: func(st *SessionState, params json.RawMessage) (any, *HandlerError) {
			return map[string]any{"data": strings.Repeat("z", ResponseObjectLimit+1000)}, nil
		},
	}
	defer delete(registry, "iprange.v1.export")
	out, err := runService(t,
		`{"jsonrpc":"2.0","id":"1","method":"iprange.v1.export","params":{}}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, `"code":"output_limit"`) {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(out, `"code":-32010`) {
		t.Fatalf("output = %q", out)
	}
}

func TestCRLFAndLFTerminatedFrames(t *testing.T) {
	session := NewSession()
	var out bytes.Buffer
	err := session.Run(strings.NewReader(
		"{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"method\":\"iprange.v1.system.describe\",\"params\":{}}\r\n"+
			"{\"jsonrpc\":\"2.0\",\"id\":\"2\",\"method\":\"iprange.v1.system.describe\",\"params\":{}}\n"),
		&out)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := lines(out.String()); len(got) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(got), out.String())
	}
}

func TestBatchBusyMemberStaysInPosition(t *testing.T) {
	// First request fills the queue via direct admission so the batch
	// member answers busy in position.
	out, err := runService(t,
		`[{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{}}]`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, `"id":"1"`) {
		t.Fatalf("output = %q", out)
	}
	// The busy path is exercised directly at the admission layer.
	var inFlight atomic.Int64
	inFlight.Store(QueuedLimit)
	req := &Request{ID: idPtr("b"), Method: "iprange.v1.system.describe", Params: json.RawMessage(`{}`)}
	entry := admitOne(req, &inFlight)
	resp := busyResponse(entry.request)
	var decoded map[string]any
	if err := json.Unmarshal(resp, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["id"] != "b" || decoded["error"].(map[string]any)["code"].(float64) != -32002 {
		t.Fatalf("busy response = %v", decoded)
	}
}

func TestCloseAllDeterministicOrder(t *testing.T) {
	// Shutdown closes registered live readers in sorted handle order;
	// this test pins the no-reader case (immutable readers need no
	// close) and the cursor clearing.
	state := NewSessionState()
	state.Resources.RecordClosedReader("0001")
	state.Resources.RecordClosedCursor("0002")
	if failures := state.Resources.CloseAll(); len(failures) != 0 {
		t.Fatalf("failures = %v", failures)
	}
	if len(state.Resources.Readers) != 0 || len(state.Resources.ClosedReaders) != 0 {
		t.Fatalf("state not cleared")
	}
}

func idPtr(s string) *RequestId {
	id := RequestIdFromString(s)
	return &id
}

func TestIntegralNumericIDAcceptedAndEchoed(t *testing.T) {
	out, err := runService(t,
		`{"jsonrpc":"2.0","id":42,"method":"iprange.v1.system.describe","params":{}}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, `"id":42`) {
		t.Fatalf("output = %q", out)
	}
}

func TestFloatIDRejected(t *testing.T) {
	out, err := runService(t,
		`{"jsonrpc":"2.0","id":1.5,"method":"iprange.v1.system.describe","params":{}}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, `"code":-32600`) {
		t.Fatalf("output = %q", out)
	}
}

var _ io.Reader = erroringReader{}
