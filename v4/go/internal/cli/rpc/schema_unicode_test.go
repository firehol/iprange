// Lossless number and escaped-Unicode frame semantics (product finding
// 3): frame validation must preserve integral numbers at arbitrary
// precision (a 400-digit request id is accepted and echoed) and must
// refuse unpaired surrogate escapes exactly like serde_json (-32700)
// before any decode or execution, so a refused frame can never execute
// with Go's U+FFFD replacement bytes.

package rpc

import (
	"strings"
	"testing"
)

func TestDecodeFrameLosslessIntegralID(t *testing.T) {
	big := strings.Repeat("9", 400)
	requests, serr := DecodeFrame([]byte(`{"jsonrpc":"2.0","id":` + big + `,"method":"iprange.v1.system.describe","params":{}}`))
	if serr != nil {
		t.Fatalf("400-digit integral id refused: %v", serr)
	}
	if len(requests) != 1 || requests[0].ID == nil {
		t.Fatalf("requests = %+v", requests)
	}
	id := requests[0].ID
	if id.IsString || id.Text != big {
		t.Fatalf("id text = %q (string=%v), want the exact 400-digit literal", id.Text, id.IsString)
	}
	if got := string(id.AsJSON()); got != big {
		t.Fatalf("id echo = %s, want the exact 400-digit literal", got)
	}
}

func TestDecodeFrameRefusesUnpairedSurrogateEscapes(t *testing.T) {
	cases := []struct {
		name  string
		frame string
	}{
		{"lone high surrogate in a value",
			`{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{"x":"a\ud800b"}}`},
		{"lone low surrogate in a value",
			`{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{"x":"a\udfff"}}`},
		{"high surrogate followed by a non-low escape",
			`{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{"x":"\ud800\u0041"}}`},
		{"high surrogate pair of high surrogates",
			`{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{"x":"\ud800\ud800"}}`},
		{"surrogate in a member name",
			`{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{"\ud800":1}}`},
		{"surrogate in a string request id",
			`{"jsonrpc":"2.0","id":"\udfff","method":"iprange.v1.system.describe","params":{}}`},
		{"surrogate inside a batch member",
			`[{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{}},{"jsonrpc":"2.0","id":"2","method":"iprange.v1.system.describe","params":{"x":"\udfff"}}]`},
		{"surrogate in a cancel request id",
			`{"jsonrpc":"2.0","method":"iprange.v1.cancel","params":{"request_id":"\ud800"}}`},
	}
	for _, c := range cases {
		if _, serr := DecodeFrame([]byte(c.frame)); serr == nil || serr.Code != StdParseError {
			t.Errorf("%s: serr = %v, want -32700", c.name, serr)
		}
	}
}

func TestDecodeFrameAcceptsValidSurrogatePair(t *testing.T) {
	requests, serr := DecodeFrame([]byte(`{"jsonrpc":"2.0","id":"\ud83d\ude00","method":"iprange.v1.system.describe","params":{}}`))
	if serr != nil {
		t.Fatalf("valid surrogate pair refused: %v", serr)
	}
	id := requests[0].ID
	if id == nil || !id.IsString || id.Text != "\U0001F600" {
		t.Fatalf("id = %+v, want the string id U+1F600", id)
	}
}

func TestDecodeFrameCancelAcceptsBigIntegralRequestID(t *testing.T) {
	big := strings.Repeat("9", 400)
	requests, serr := DecodeFrame([]byte(`{"jsonrpc":"2.0","method":"iprange.v1.cancel","params":{"request_id":` + big + `}}`))
	if serr != nil {
		t.Fatalf("cancel with a 400-digit request_id refused: %v", serr)
	}
	if len(requests) != 1 || requests[0].Method != CancelMethod || requests[0].ID != nil {
		t.Fatalf("requests = %+v, want one cancel notification", requests)
	}
}

func TestBoundedDiagnosticTextCapsRequestDerivedEcho(t *testing.T) {
	big := strings.Repeat("x", 20000)
	got := boundedDiagnosticText(big)
	if len(got) != maxRequestDiagnosticBytes+len("...(truncated)") {
		t.Fatalf("truncated length %d, want %d", len(got), maxRequestDiagnosticBytes+len("...(truncated)"))
	}
	if !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("truncated text %q lacks the explicit marker", got)
	}
	if small := boundedDiagnosticText("ok"); small != "ok" {
		t.Fatalf("small text changed: %q", small)
	}
}

func TestDecodeFrameBoundsUnknownMemberDiagnostic(t *testing.T) {
	name := strings.Repeat("Z", 70000)
	_, serr := DecodeFrame([]byte(`{"jsonrpc":"2.0","id":"1","method":"iprange.v1.system.describe","params":{},"` + name + `":1}`))
	if serr == nil {
		t.Fatal("expected an unknown-member error")
	}
	if len(serr.Message) > maxRequestDiagnosticBytes+len("...(truncated)")+64 {
		t.Fatalf("message length %d exceeds the diagnostic bound", len(serr.Message))
	}
	if !strings.Contains(serr.Message, "...(truncated)") {
		t.Fatalf("message %q lacks the truncation marker", serr.Message)
	}
}
