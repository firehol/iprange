// Physical line transport for the v1 JSON-RPC stdio contract
// (iprange-jsonrpc-v1.md): one physical line per request object or
// batch array; LF and CRLF terminate input frames, output frames
// always end in LF; an unescaped CR or LF inside JSON is invalid; the
// hard frame ceiling is 1,048,576 bytes before the line terminator.
// A frame over the limit fails with -32001 and the process closes.
// Only a zero-length read from the underlying reader means EOF; a
// real stdin error is a fatal input-transport event, never EOF. The
// encoded response frame ceiling is the same; each response object is
// capped at 65,000 bytes.

package rpc

import (
	"bufio"
	"errors"
	"io"
)

const (
	// InputFrameLimit is the hard ceiling of one request frame before
	// the line terminator; a frame over it fails with -32001 and the
	// process closes without parsing any later bytes.
	InputFrameLimit = 1_048_576
	// OutputFrameLimit is the ceiling of one encoded response frame.
	OutputFrameLimit = 1_048_576
	// ResponseObjectLimit caps one complete response object (envelope
	// plus id plus result or error) at 65,000 bytes.
	ResponseObjectLimit = 65_000
	// BatchLimit is the maximum number of members in one batch array.
	BatchLimit = 16
	// QueuedLimit is the maximum number of admitted requests waiting
	// behind the active request; more fail with -32002 server_busy.
	QueuedLimit = 16
)

// LineReadError is a line-read failure: FrameTooLarge (the service
// replies -32001 with id null and closes, and bytes after the limit
// are never parsed) or Io (a real reader failure; only a zero-length
// read means EOF, so an io error is fatal, never a clean end).
type LineReadError struct {
	FrameTooLarge bool
	Err           error
}

func (e *LineReadError) Error() string {
	if e.FrameTooLarge {
		return "frame over input limit"
	}
	return "stdin read failed: " + e.Err.Error()
}

// LineReader reads exactly one physical line, enforcing the input
// ceiling and stripping the LF (and one preceding CR for a CRLF
// terminator). ReadByte on a bufio.Reader is buffered, so the
// byte-wise scan is cheap for the small control frames of this API.
type LineReader struct {
	r   *bufio.Reader
	eof bool
}

func NewLineReader(r io.Reader) *LineReader {
	return &LineReader{r: bufio.NewReader(r)}
}

// ReadLine returns the payload of the next frame without its line
// terminator, or ok==false at EOF with no pending bytes. A final
// unterminated frame at EOF is accepted (the client closes the
// transport after its last complete interaction). A line longer than
// InputFrameLimit returns FrameTooLarge once and the caller must shut
// the service down. A real read error returns a LineReadError; only
// a zero-length read is EOF.
func (lr *LineReader) ReadLine() (line []byte, ok bool, err *LineReadError) {
	if lr.eof {
		return nil, false, nil
	}
	// The buffer may hold at most INPUT_FRAME_LIMIT+1 accumulated
	// bytes before a terminator: one byte above the payload ceiling
	// can still be the CR of a CRLF terminator (payload of exactly
	// LIMIT bytes plus CRLF), so the limit is only final on the
	// terminator after stripping one trailing CR.
	var buf []byte
	for {
		b, rerr := lr.r.ReadByte()
		if rerr == io.EOF {
			lr.eof = true
			if len(buf) == 0 {
				return nil, false, nil
			}
			if len(buf) > InputFrameLimit {
				return nil, false, &LineReadError{FrameTooLarge: true}
			}
			return buf, true, nil
		}
		if rerr != nil {
			lr.eof = true
			return nil, false, &LineReadError{Err: rerr}
		}
		if b == '\n' {
			lr.eof = false
			// Strip the CR of a CRLF terminator before the limit
			// check: the ceiling applies to the frame payload, not
			// the line terminator.
			if len(buf) > 0 && buf[len(buf)-1] == '\r' {
				buf = buf[:len(buf)-1]
			}
			if len(buf) > InputFrameLimit {
				return nil, false, &LineReadError{FrameTooLarge: true}
			}
			return buf, true, nil
		}
		if len(buf) < InputFrameLimit+1 {
			buf = append(buf, b)
			continue
		}
		// A (LIMIT+2)-th accumulated byte can never be part of a
		// legal frame; consume and discard the rest of the line so
		// shutdown stays deterministic, then report the failure once.
		discardRest(lr.r)
		return nil, false, &LineReadError{FrameTooLarge: true}
	}
}

// discardRest consumes the remainder of an oversized line.
func discardRest(r *bufio.Reader) {
	for {
		b, err := r.ReadByte()
		if err != nil || b == '\n' {
			return
		}
	}
}

// FrameWriter writes response frames as LF-terminated lines. The
// encoder caps text at OutputFrameLimit before this is called; every
// write flushes so a client sees each response frame promptly and a
// broken stdout is observed at the earliest possible point.
type FrameWriter struct {
	w *bufio.Writer
}

func NewFrameWriter(w io.Writer) *FrameWriter {
	return &FrameWriter{w: bufio.NewWriterSize(w, 64*1024)}
}

func (fw *FrameWriter) WriteLine(text string) error {
	if _, err := fw.w.WriteString(text); err != nil {
		return err
	}
	if err := fw.w.WriteByte('\n'); err != nil {
		return err
	}
	return fw.w.Flush()
}

// FatalWriteError marks a failed write so callers can distinguish a
// broken stdout from other transport states.
func FatalWriteError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New("stdout write failed: " + err.Error())
}
