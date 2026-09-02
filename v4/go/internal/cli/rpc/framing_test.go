package rpc

import (
	"strings"
	"testing"
)

func TestExactLimitFramesAcceptedWithLFAndCRLF(t *testing.T) {
	payload := strings.Repeat("x", InputFrameLimit)
	// LF
	lr := NewLineReader(strings.NewReader(payload + "\n"))
	line, ok, err := lr.ReadLine()
	if err != nil || !ok || len(line) != InputFrameLimit {
		t.Fatalf("LF: ok=%v err=%v len=%d", ok, err, len(line))
	}
	// CRLF: the CR is part of the terminator, not the payload.
	lr = NewLineReader(strings.NewReader(payload + "\r\n"))
	line, ok, err = lr.ReadLine()
	if err != nil || !ok || len(line) != InputFrameLimit {
		t.Fatalf("CRLF: ok=%v err=%v len=%d", ok, err, len(line))
	}
}

func TestOneByteOverLimitRejected(t *testing.T) {
	payload := strings.Repeat("x", InputFrameLimit+1)
	lr := NewLineReader(strings.NewReader(payload + "\n"))
	_, _, err := lr.ReadLine()
	if err == nil || !err.FrameTooLarge {
		t.Fatalf("err = %v, want FrameTooLarge", err)
	}
}

func TestCRLFPayloadOverLimitRejected(t *testing.T) {
	// Payload of LIMIT+1 plus CRLF: even after stripping the CR the
	// payload is over the ceiling.
	payload := strings.Repeat("x", InputFrameLimit+1)
	lr := NewLineReader(strings.NewReader(payload + "\r\n"))
	_, _, err := lr.ReadLine()
	if err == nil || !err.FrameTooLarge {
		t.Fatalf("err = %v, want FrameTooLarge", err)
	}
}

func TestReaderIoErrorIsFatal(t *testing.T) {
	lr := NewLineReader(erroringReader{})
	_, _, err := lr.ReadLine()
	if err == nil || err.FrameTooLarge || err.Err == nil {
		t.Fatalf("err = %v, want an Io error", err)
	}
}
