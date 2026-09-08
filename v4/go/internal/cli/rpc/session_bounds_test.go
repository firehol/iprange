// Transport-level regression tests (product findings 3 and 4): an
// arbitrary-precision integral request id survives the full session
// and is echoed losslessly; early schema errors are bounded (a
// 70,000-byte unknown member name can no longer exceed the 65,000-byte
// response-object bound or the 1,048,576-byte frame bound); unpaired
// surrogate escapes are refused with -32700 before any execution; a
// cancel notification with a 400-digit request_id is accepted and
// produces no response.

package rpc

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSessionEchoesBigIntegralID(t *testing.T) {
	big := strings.Repeat("9", 400)
	out, err := runService(t, `{"jsonrpc":"2.0","id":`+big+`,"method":"iprange.v1.system.describe","params":{}}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var resp map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("response %q: %v", out, err)
	}
	if _, hasError := resp["error"]; hasError {
		t.Fatalf("response carries an error: %s", out)
	}
	// The echoed id must be the bare integral literal, preserved
	// byte-for-byte (not a float64-rounded or quoted value).
	if got := string(resp["id"]); got != big {
		t.Fatalf("echoed id length %d, want %d", len(got), len(big))
	}
}

func TestSessionBoundedUnknownMemberError(t *testing.T) {
	name := strings.Repeat("Z", 70000)
	out, err := runService(t, `{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{},"`+name+`":1}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(out) > ResponseObjectLimit || len(out) > OutputFrameLimit {
		t.Fatalf("error response is %d bytes, over the 65,000-byte object bound", len(out))
	}
	if !strings.Contains(out, `"code":-32600`) {
		t.Fatalf("response %q lacks -32600", out)
	}
	if !strings.Contains(out, "...(truncated)") {
		t.Fatalf("response %q lacks the truncation marker", out)
	}
}

func TestSessionRefusesSurrogateBeforeExecution(t *testing.T) {
	out, err := runService(t, `{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{"x":"a\ud800b"}}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// The frame is refused with -32700 and id null; the method never
	// executes (no result member).
	if !strings.Contains(out, `"code":-32700`) || !strings.Contains(out, `"id":null`) {
		t.Fatalf("response %q, want -32700 with id null", out)
	}
	if strings.Contains(out, `"result"`) {
		t.Fatalf("response %q executed the method despite the surrogate", out)
	}
}

func TestSessionCancellationWithSurrogateIsRefused(t *testing.T) {
	out, err := runService(t, `{"jsonrpc":"2.0","method":"iprange.v1.cancel","params":{"request_id":"\ud800"}}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Before the fix Go silently accepted the notification with a
	// replacement character; serde_json refuses the frame (-32700).
	if !strings.Contains(out, `"code":-32700`) {
		t.Fatalf("response %q, want -32700 for the surrogate cancel", out)
	}
}

func TestSessionAcceptsCancelWithBigIntegralRequestID(t *testing.T) {
	big := strings.Repeat("9", 400)
	input := `{"jsonrpc":"2.0","method":"iprange.v1.cancel","params":{"request_id":` + big + `}}` + "\n" +
		`{"jsonrpc":"2.0","id":"2","method":"iprange.v1.system.describe","params":{}}`
	out, err := runService(t, input)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got := lines(out)
	if len(got) != 1 || !strings.Contains(got[0], `"id":"2"`) {
		t.Fatalf("output = %q, want only the describe response (cancel accepted silently)", out)
	}
}

func TestSessionAcceptsValidSurrogatePairInID(t *testing.T) {
	out, err := runService(t, `{"jsonrpc":"2.0","id":"\ud83d\ude00","method":"iprange.v1.system.describe","params":{}}`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var resp map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &resp); err != nil {
		t.Fatalf("response %q: %v", out, err)
	}
	if _, hasError := resp["error"]; hasError {
		t.Fatalf("response carries an error: %s", out)
	}
	var id string
	if err := json.Unmarshal(resp["id"], &id); err != nil {
		t.Fatalf("id %s: %v", resp["id"], err)
	}
	if id != "\U0001F600" {
		t.Fatalf("echoed id %q, want the emoji U+1F600", id)
	}
}
