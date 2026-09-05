// Transport behavior tests (Rust rpc/session.rs test-suite parity):
// queue admission and busy rejection, in-position batch members,
// cancel-during-scan semantics, unanswerable ids, the 65,000-byte
// response-object ceiling, EOF shutdown with queued units, fatal
// stdout failure, and dispatcher responsiveness while a slow request
// occupies the worker (pipelined busy rejection, cancellation and EOF
// observability over real stream pipes). The system.describe stub
// registered here exists only in this test binary; production
// registration lives in the handlers package.

package rpc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	// A frame over the input ceiling is a startup/framing failure: the
	// session answers -32001 with id null, closes without parsing later
	// bytes, and exits non-zero (spec iprange-jsonrpc-v1.md shutdown
	// section).
	big := strings.Repeat("a", InputFrameLimit+10)
	out, err := runService(t, `{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{"x":"`+big+`"}}`)
	if err == nil {
		t.Fatal("expected a framing failure error")
	}
	if !strings.Contains(err.Error(), "frame over input limit") {
		t.Fatalf("error = %v", err)
	}
	lines := lines(out)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %q", len(lines), out)
	}
	if !strings.Contains(lines[0], `"code":-32001`) || !strings.Contains(lines[0], `"id":null`) {
		t.Fatalf("line = %q", lines[0])
	}
	// No further bytes after the single -32001 response.
	if out != lines[0]+"\n" {
		t.Fatalf("output has bytes beyond the -32001 line: %q", out)
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

func TestBatchMemberBusyResponseAtAdmission(t *testing.T) {
	// Transport round trip of one single-element batch, then the
	// busy-response machinery exercised directly at the admission
	// layer (the full-transport busy-array corner is pinned by
	// TestBatchBusyMembersAnswerInPosition).
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

// ---------------------------------------------------------------------------
// Dispatcher responsiveness while the worker is occupied (SOW-0028
// milestone-4 repair): the work channel is buffered to QueuedLimit, so
// the main loop keeps reading stdin while a slow request executes.
// These tests drive a real session over io.Pipe (input) and net.Pipe
// (output) and use a registered token-aware slow method; the tests run
// sequentially, so the package-level gate is safe.

// testSlowGate coordinates one slow-method test: the handler signals
// entered once it starts, waits for either release or cancellation of
// the active token, and signals observed the moment it sees the token
// cancelled.
type testSlowGate struct {
	entered  chan struct{}
	release  chan struct{}
	observed chan struct{}
}

func newTestSlowGate() *testSlowGate {
	return &testSlowGate{
		entered:  make(chan struct{}, 1),
		release:  make(chan struct{}, 1),
		observed: make(chan struct{}, 1),
	}
}

// slowGate is the coordination state of the currently running slow
// method test. Tests in this package run sequentially; each test
// stores its own gate before starting the session.
var slowGate atomic.Pointer[testSlowGate]

// registerSlowMethod installs a token-aware slow handler under one
// inventory name for the duration of one test.
func registerSlowMethod(t *testing.T) {
	t.Helper()
	registry["iprange.v1.database.info"] = registeredMethod{
		validate: func(params json.RawMessage) error { return nil },
		handle: func(st *SessionState, params json.RawMessage) (any, *HandlerError) {
			gate := slowGate.Load()
			if gate == nil {
				return nil, NewHandlerError("no_gate", "not_started", "test gate missing")
			}
			token := st.Token()
			select {
			case gate.entered <- struct{}{}:
			default:
			}
			for !token.IsCancelled() {
				select {
				case <-gate.release:
					return map[string]any{"slow": "released"}, nil
				case <-time.After(2 * time.Millisecond):
				}
			}
			select {
			case gate.observed <- struct{}{}:
			default:
			}
			return nil, NewHandlerError("cancelled", "cancelled", "operation was cancelled")
		},
	}
	t.Cleanup(func() { delete(registry, "iprange.v1.database.info") })
}

// startPipedSession runs one session over real stream pipes so the
// test can pipeline frames and observe responses while the handler is
// still executing. Closing in delivers stdin EOF; out supports read
// deadlines.
func startPipedSession(t *testing.T) (in io.WriteCloser, out net.Conn, done chan error) {
	t.Helper()
	inR, inW := io.Pipe()
	outL, outR := net.Pipe()
	done = make(chan error, 1)
	go func() {
		done <- NewSession().Run(inR, outL)
		outL.Close()
	}()
	t.Cleanup(func() {
		inW.Close()
		outR.Close()
	})
	return inW, outR, done
}

// waitRun waits for the session to terminate and returns its error.
func waitRun(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("session did not terminate in time")
		return nil
	}
}

// releaseSlow handler flips the slow method's release latch without
// blocking, so test failure cleanup never deadlocks on a full gate.
func releaseSlow(t *testing.T, gate *testSlowGate) {
	t.Helper()
	select {
	case gate.release <- struct{}{}:
	default:
	}
}

func writeFrame(t *testing.T, in io.Writer, frame string) {
	t.Helper()
	if _, err := io.WriteString(in, frame+"\n"); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// readResponseLines reads exactly n response lines, failing if the
// session produces fewer within the read deadline.
func readResponseLines(t *testing.T, r *bufio.Reader, out net.Conn, n int) []string {
	t.Helper()
	if err := out.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	defer out.SetReadDeadline(time.Time{})
	lines := make([]string, 0, n)
	for len(lines) < n {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("reading response %d of %d: %v", len(lines)+1, n, err)
		}
		lines = append(lines, strings.TrimSuffix(line, "\n"))
	}
	return lines
}

// drainOutput reads the session's output to EOF in a goroutine; the
// session's final writes block until the pipe is read, so tests drain
// concurrently with waiting for Run to return.
func drainOutput(t *testing.T, out net.Conn) <-chan []byte {
	t.Helper()
	if err := out.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	ch := make(chan []byte, 1)
	go func() {
		data, err := io.ReadAll(bufio.NewReader(out))
		if err != nil {
			t.Errorf("read output: %v", err)
		}
		ch <- data
	}()
	return ch
}

// assertResultID asserts one response line is a result for the id.
func assertResultID(t *testing.T, line, id string) {
	t.Helper()
	obj := decodeLine(t, line)
	if got, _ := obj["id"].(string); got != id {
		t.Fatalf("response id = %v, want %s", obj["id"], id)
	}
	if _, ok := obj["result"]; !ok {
		t.Fatalf("response is not a result: %v", obj)
	}
}

// assertFactualCancellation asserts one response line is the
// documented cancelled product outcome (-32010, data.code cancelled)
// for the id.
func assertFactualCancellation(t *testing.T, line, id string) {
	t.Helper()
	obj := decodeLine(t, line)
	if got, _ := obj["id"].(string); got != id {
		t.Fatalf("response id = %v, want %s", obj["id"], id)
	}
	if code, ok := errorCode(obj); !ok || code != float64(ProductError) {
		t.Fatalf("error code = %v, want -32010: %v", obj["error"], obj)
	}
	errObj, _ := obj["error"].(map[string]any)
	dataObj, _ := errObj["data"].(map[string]any)
	if dataObj == nil || dataObj["code"] != "cancelled" {
		t.Fatalf("response must report the factual cancellation outcome: %v", obj)
	}
}

func decodeLine(t *testing.T, line string) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatalf("unmarshal response %q: %v", line, err)
	}
	return obj
}

func errorCode(obj map[string]any) (float64, bool) {
	errObj, _ := obj["error"].(map[string]any)
	if errObj == nil {
		return 0, false
	}
	code, _ := errObj["code"].(float64)
	return code, true
}

func describeFrame(id string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"method":"iprange.v1.system.describe","params":{}}`, id)
}

func TestPipelinedBusyWhileWorkerOccupied(t *testing.T) {
	// With a slow request occupying the worker, 20 pipelined single
	// requests admit 17 (one active plus 16 queued) and answer the
	// remaining 3 with -32002 server_busy while the worker is still
	// busy. The three busy responses must arrive before the slow op is
	// released, proving the main loop never blocks behind the worker
	// (pre-fix the unbuffered work channel stalled the dispatcher on
	// the second frame).
	registerSlowMethod(t)
	gate := newTestSlowGate()
	slowGate.Store(gate)
	t.Cleanup(func() { releaseSlow(t, gate) })
	in, out, done := startPipedSession(t)
	reader := bufio.NewReader(out)

	writeFrame(t, in, `{"jsonrpc":"2.0","id":"1","method":"iprange.v1.database.info","params":{}}`)
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("slow handler did not start")
	}
	for id := 2; id <= 20; id++ {
		writeFrame(t, in, describeFrame(strconv.Itoa(id)))
	}

	busy := readResponseLines(t, reader, out, 3)
	for _, line := range busy {
		obj := decodeLine(t, line)
		if code, ok := errorCode(obj); !ok || code != float64(TransportServerBusy) {
			t.Fatalf("busy response = %v", obj)
		}
	}

	releaseSlow(t, gate)
	rest := readResponseLines(t, reader, out, 17)

	in.Close()
	if err := waitRun(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}

	all := append(busy, rest...)
	if len(all) != 20 {
		t.Fatalf("got %d responses, want 20", len(all))
	}
	results, busys := 0, 0
	seen := make(map[string]bool)
	for _, line := range all {
		obj := decodeLine(t, line)
		id, _ := obj["id"].(string)
		if id == "" {
			t.Fatalf("response without string id: %v", obj)
		}
		if seen[id] {
			t.Fatalf("id %s answered more than once", id)
		}
		seen[id] = true
		if _, ok := obj["result"]; ok {
			results++
			continue
		}
		if code, ok := errorCode(obj); ok && code == float64(TransportServerBusy) {
			busys++
			continue
		}
		t.Fatalf("unexpected response: %v", obj)
	}
	if results != 17 || busys != 3 {
		t.Fatalf("results=%d busy=%d, want 17 and 3", results, busys)
	}
	for id := 1; id <= 20; id++ {
		if !seen[strconv.Itoa(id)] {
			t.Fatalf("id %d never answered", id)
		}
	}
}

func TestBatchBusyMembersAnswerInPosition(t *testing.T) {
	// Full transport path under genuine queue pressure: one slow
	// execute occupies the worker and 16 more pipelined executes fill
	// the whole 16-entry buffer, so the admission counter sits at its
	// bound while the worker is still busy.  A following 16-request
	// batch can admit nothing and must answer all 16 members -32002
	// inside one response array in frame order, written by the
	// dispatcher without a channel send; with the buffer genuinely
	// full any queued unit would stall behind the occupied worker and
	// no busy response would ever arrive.  The busy array is read
	// before the gate release; after the release the slow execute
	// answers, the 16 queued executes answer normally, and the
	// session exits clean.
	registerSlowMethod(t)
	gate := newTestSlowGate()
	slowGate.Store(gate)
	t.Cleanup(func() { releaseSlow(t, gate) })
	in, out, done := startPipedSession(t)
	reader := bufio.NewReader(out)

	writeFrame(t, in, `{"jsonrpc":"2.0","id":"1","method":"iprange.v1.database.info","params":{}}`)
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("slow handler did not start")
	}
	for id := 2; id <= 17; id++ {
		writeFrame(t, in, describeFrame(strconv.Itoa(id)))
	}

	batch := make([]string, 0, 16)
	for id := 1; id <= 16; id++ {
		batch = append(batch, describeFrame("b"+strconv.Itoa(id)))
	}
	writeFrame(t, in, "["+strings.Join(batch, ",")+"]")

	busyLines := readResponseLines(t, reader, out, 1)
	var arr []map[string]any
	if err := json.Unmarshal([]byte(busyLines[0]), &arr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(arr) != 16 {
		t.Fatalf("batch response has %d members, want 16", len(arr))
	}
	for i, member := range arr {
		if id, _ := member["id"].(string); id != "b"+strconv.Itoa(i+1) {
			t.Fatalf("member %d id = %v, want b%d", i, member["id"], i+1)
		}
		if code, ok := errorCode(member); !ok || code != float64(TransportServerBusy) {
			t.Fatalf("member %d not busy: %v", i, member)
		}
	}

	releaseSlow(t, gate)
	rest := readResponseLines(t, reader, out, 17)
	in.Close()
	if err := waitRun(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
	assertResultID(t, rest[0], "1")
	for _, line := range rest[1:] {
		obj := decodeLine(t, line)
		if _, ok := obj["result"]; !ok {
			t.Fatalf("queued execute did not answer with a result: %v", obj)
		}
	}
}

func TestActiveBatchMemberFreesOneSlotAtATime(t *testing.T) {
	// A 16-member batch whose first member is slow occupies one
	// active slot plus 15 queued slots. While member 1 is blocked,
	// the 15 unexecuted batch members must still count against the
	// admission bound, so of 10 pipelined single requests exactly 9
	// answer -32002 and 1 is admitted. Pre-fix the worker subtracted
	// the whole batch from the admission counter when it picked the
	// unit up, leaving the 15 pending members uncounted and admitting
	// all 10.
	registerSlowMethod(t)
	gate := newTestSlowGate()
	slowGate.Store(gate)
	t.Cleanup(func() { releaseSlow(t, gate) })
	in, out, done := startPipedSession(t)
	reader := bufio.NewReader(out)

	batch := make([]string, 0, 16)
	batch = append(batch, `{"jsonrpc":"2.0","id":"b1","method":"iprange.v1.database.info","params":{}}`)
	for id := 2; id <= 16; id++ {
		batch = append(batch, describeFrame("b"+strconv.Itoa(id)))
	}
	writeFrame(t, in, "["+strings.Join(batch, ",")+"]")
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("slow handler did not start")
	}
	for id := 1; id <= 10; id++ {
		writeFrame(t, in, describeFrame("s"+strconv.Itoa(id)))
	}

	busy := readResponseLines(t, reader, out, 9)
	for _, line := range busy {
		obj := decodeLine(t, line)
		if code, ok := errorCode(obj); !ok || code != float64(TransportServerBusy) {
			t.Fatalf("busy response = %v", obj)
		}
	}

	releaseSlow(t, gate)
	rest := readResponseLines(t, reader, out, 2)
	in.Close()
	if err := waitRun(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}

	// First response after release: the batch array, 16 members in
	// frame order, the slow member plus the 15 ordinary results.
	var arr []map[string]any
	if err := json.Unmarshal([]byte(rest[0]), &arr); err != nil {
		t.Fatalf("unmarshal batch: %v", err)
	}
	if len(arr) != 16 {
		t.Fatalf("batch response has %d members, want 16", len(arr))
	}
	for i, member := range arr {
		if id, _ := member["id"].(string); id != "b"+strconv.Itoa(i+1) {
			t.Fatalf("member %d id = %v, want b%d", i, member["id"], i+1)
		}
		if _, ok := member["result"]; !ok {
			t.Fatalf("member %d not a result: %v", i, member)
		}
	}
	// Second response: the single request admitted while member 1 was
	// blocked, executed after the batch completed.
	assertResultID(t, rest[1], "s1")
}

func TestAdmissionPartialCapacityExecuteThenBusy(t *testing.T) {
	// Admission-layer parity with Rust
	// admission_preserves_batch_order_under_queue_pressure: with one
	// free slot a frame admits its first element and answers the rest
	// busy in order, and the counter never exceeds the bound.
	var inFlight atomic.Int64
	inFlight.Store(QueuedLimit - 1)
	for _, id := range []string{"a", "b", "c"} {
		request := &Request{
			ID:     idPtr(id),
			Method: "iprange.v1.system.describe",
			Params: json.RawMessage(`{}`),
		}
		entry := admitOne(request, &inFlight)
		switch id {
		case "a":
			if entry.kind != workExecute {
				t.Fatalf("first entry kind = %v, want execute", entry.kind)
			}
		default:
			if entry.kind != workBusy {
				t.Fatalf("entry %s kind = %v, want busy", id, entry.kind)
			}
		}
	}
	if inFlight.Load() != QueuedLimit {
		t.Fatalf("inFlight = %d, want %d", inFlight.Load(), QueuedLimit)
	}
}

func TestCancelReachesActiveSlowRequest(t *testing.T) {
	// Regression: a cancel notification sent while a slow request runs
	// must be applied before the slow op finishes, even when ordinary
	// requests already fill the queue. The slow handler observes the
	// cancelled token while it is still blocked and answers its
	// factual outcome (the cancelled error); the queued ordinary
	// request still answers.
	registerSlowMethod(t)
	gate := newTestSlowGate()
	slowGate.Store(gate)
	t.Cleanup(func() { releaseSlow(t, gate) })
	in, out, done := startPipedSession(t)

	writeFrame(t, in, `{"jsonrpc":"2.0","id":"slow","method":"iprange.v1.database.info","params":{}}`)
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("slow handler did not start")
	}
	// Fill the queue with an ordinary request; pre-fix the dispatcher
	// blocked on this send while the worker executed the slow request.
	writeFrame(t, in, describeFrame("fill"))
	// The cancel must now be applied while the slow op is still
	// blocked on its gate.
	writeFrame(t, in, `{"jsonrpc":"2.0","method":"iprange.v1.cancel","params":{"request_id":"slow"}}`)

	select {
	case <-gate.observed:
	case <-time.After(5 * time.Second):
		releaseSlow(t, gate)
		t.Fatal("cancel never reached the active slow request")
	}

	// Drain output concurrently: the worker's response writes block
	// until the test reads the pipe, so waiting for Run first would
	// deadlock.
	outData := drainOutput(t, out)
	in.Close()
	if err := waitRun(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
	data := <-outData
	got := lines(string(data))
	// The slow handler was already running when the cancel applied, so
	// it answers its factual outcome (the cancelled error), exactly
	// like EOF cancellation; the cancel-drives-omit rule covers
	// requests not yet started.
	if len(got) != 2 {
		t.Fatalf("got %d response lines, want 2: %q", len(got), data)
	}
	assertFactualCancellation(t, got[0], "slow")
	assertResultID(t, got[1], "fill")
}

func TestCancelQueuedBatchMemberDoesNotCancelActiveSibling(t *testing.T) {
	// Regression (reviewer P1-1): cancelling a queued batch member must
	// not cancel the active sibling. Pre-fix the worker installed every
	// batch member's id as active and cancelled one shared token, so
	// cancel(request_id=queued) made the running member answer -32010.
	// The handler checks its token only after the test releases it, so
	// any wrong cancellation is observed deterministically. The queued
	// member is skipped without a response (cancel-drives-omit); the
	// active member completes with its success result.
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	registry["iprange.v1.database.info"] = registeredMethod{
		validate: func(json.RawMessage) error { return nil },
		handle: func(st *SessionState, _ json.RawMessage) (any, *HandlerError) {
			token := st.Token()
			close(entered)
			<-release
			if token.IsCancelled() {
				return nil, NewHandlerError("cancelled", "cancelled", "wrong sibling cancelled active work")
			}
			return map[string]any{"completed": true}, nil
		},
	}
	t.Cleanup(func() { delete(registry, "iprange.v1.database.info") })
	in, out, done := startPipedSession(t)
	reader := bufio.NewReader(out)

	writeFrame(t, in, `[{"jsonrpc":"2.0","id":"active","method":"iprange.v1.database.info","params":{}},{"jsonrpc":"2.0","id":"queued","method":"iprange.v1.system.describe","params":{}}]`)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("active member did not start")
	}
	writeFrame(t, in, `{"jsonrpc":"2.0","method":"iprange.v1.cancel","params":{"request_id":"queued"}}`)
	// The schema-error response proves the dispatcher applied the
	// cancel frame before the test releases the active member.
	writeFrame(t, in, `{}`)
	marker := readResponseLines(t, reader, out, 1)
	if code, _ := errorCode(decodeLine(t, marker[0])); code != -32600 {
		t.Fatalf("bad dispatcher marker: %s", marker[0])
	}
	unblock()
	result := readResponseLines(t, reader, out, 1)
	in.Close()
	if err := waitRun(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(result[0]), &arr); err != nil {
		t.Fatalf("unmarshal batch: %v", err)
	}
	if len(arr) != 1 || arr[0]["id"] != "active" || arr[0]["result"] == nil {
		t.Fatalf("cancelling the queued sibling must preserve the active member's success; got %s", result[0])
	}
}

func TestCancelActiveMemberDoesNotPoisonLaterSibling(t *testing.T) {
	// Cancelling the running member of a batch cancels only that
	// member's own fresh token: the batch array carries the factual
	// -32010 for the cancelled member and the queued sibling still
	// completes with its success result. Pre-fix the shared token
	// poisoned every sibling behind the cancelled member.
	registerSlowMethod(t)
	gate := newTestSlowGate()
	slowGate.Store(gate)
	t.Cleanup(func() { releaseSlow(t, gate) })
	in, out, done := startPipedSession(t)
	reader := bufio.NewReader(out)

	writeFrame(t, in, `[{"jsonrpc":"2.0","id":"activeA","method":"iprange.v1.database.info","params":{}},{"jsonrpc":"2.0","id":"queuedB","method":"iprange.v1.database.info","params":{}}]`)
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("active member did not start")
	}
	writeFrame(t, in, `{"jsonrpc":"2.0","method":"iprange.v1.cancel","params":{"request_id":"activeA"}}`)
	select {
	case <-gate.observed:
	case <-time.After(5 * time.Second):
		releaseSlow(t, gate)
		t.Fatal("cancel never reached the active batch member")
	}
	// The cancelled member answered factually; release so the queued
	// sibling finishes under its own fresh token.
	releaseSlow(t, gate)
	result := readResponseLines(t, reader, out, 1)
	in.Close()
	if err := waitRun(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(result[0]), &arr); err != nil {
		t.Fatalf("unmarshal batch: %v", err)
	}
	if len(arr) != 2 {
		t.Fatalf("batch has %d members, want 2", len(arr))
	}
	cancelledLine, err := json.Marshal(arr[0])
	if err != nil {
		t.Fatalf("marshal cancelled member: %v", err)
	}
	assertFactualCancellation(t, string(cancelledLine), "activeA")
	if id, _ := arr[1]["id"].(string); id != "queuedB" {
		t.Fatalf("sibling id = %v, want queuedB", arr[1]["id"])
	}
	if _, ok := arr[1]["result"]; !ok {
		t.Fatalf("sibling did not complete with a result: %v", arr[1])
	}
}

func TestCancelUnknownOrCompletedIdWhileOtherMembersRun(t *testing.T) {
	// A cancel for an unknown id, or for an id already terminal from an
	// earlier frame, is ignored per spec and must never disturb a batch
	// whose members are still executing.
	in, out, done := startPipedSession(t)
	reader := bufio.NewReader(out)

	// The earlier frame completes id "done"; its terminal cleanup runs
	// before its response is written, so a later cancel of it is a
	// no-op.
	writeFrame(t, in, describeFrame("done"))
	if got := readResponseLines(t, reader, out, 1); !strings.Contains(got[0], `"id":"done"`) {
		t.Fatalf("earlier frame response = %q", got[0])
	}
	writeFrame(t, in, `[{"jsonrpc":"2.0","id":"a","method":"iprange.v1.system.describe","params":{}},{"jsonrpc":"2.0","id":"b","method":"iprange.v1.system.describe","params":{}}]`)
	writeFrame(t, in, `{"jsonrpc":"2.0","method":"iprange.v1.cancel","params":{"request_id":"unknown"}}`)
	writeFrame(t, in, `{"jsonrpc":"2.0","method":"iprange.v1.cancel","params":{"request_id":"done"}}`)
	in.Close()
	// The batch response is read before waitRun: the worker's write
	// blocks until the test drains the pipe.
	batch := readResponseLines(t, reader, out, 1)
	if err := waitRun(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(batch[0]), &arr); err != nil {
		t.Fatalf("unmarshal batch: %v", err)
	}
	if len(arr) != 2 {
		t.Fatalf("batch has %d members, want 2", len(arr))
	}
	for _, member := range arr {
		if id, _ := member["id"].(string); id != "a" && id != "b" {
			t.Fatalf("unexpected batch member id %v", member["id"])
		}
		if _, ok := member["result"]; !ok {
			t.Fatalf("member %v did not complete with a result", member["id"])
		}
	}
}

func TestEOFReachesActiveSlowRequest(t *testing.T) {
	// Regression: stdin EOF while a slow request runs must cancel the
	// active token, let the slow request answer its factual terminal
	// outcome (-32010 data.code cancelled), drain admitted queued
	// units, and return nil. Pre-fix the dispatcher blocked on a
	// queued ordinary request, so EOF was never processed until the
	// slow op ended.
	registerSlowMethod(t)
	gate := newTestSlowGate()
	slowGate.Store(gate)
	t.Cleanup(func() { releaseSlow(t, gate) })
	in, out, done := startPipedSession(t)

	writeFrame(t, in, `{"jsonrpc":"2.0","id":"slow","method":"iprange.v1.database.info","params":{}}`)
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		releaseSlow(t, gate)
		t.Fatal("slow handler did not start")
	}
	// Pre-fix the dispatcher blocked on this send while the worker
	// executed the slow request, so the EOF below stayed unprocessed.
	writeFrame(t, in, describeFrame("fill"))

	// Drain output concurrently: the worker's response writes block
	// until the test reads the pipe, so waiting for Run first would
	// deadlock.
	outData := drainOutput(t, out)
	in.Close()
	if err := waitRun(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
	data := <-outData
	got := lines(string(data))
	if len(got) != 2 {
		t.Fatalf("got %d response lines, want 2: %q", len(got), data)
	}
	assertFactualCancellation(t, got[0], "slow")
	assertResultID(t, got[1], "fill")
}
