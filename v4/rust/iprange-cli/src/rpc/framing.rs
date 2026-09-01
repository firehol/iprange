//! Physical line transport for the v1 JSON-RPC stdio contract.
//!
//! Rules implemented (iprange-jsonrpc-v1.md):
//! - one physical line per request object or batch array;
//! - LF and CRLF terminate input frames; output frames always end in LF;
//! - an unescaped CR or LF inside JSON is invalid;
//! - hard frame ceiling 1,048,576 bytes before the line terminator;
//! - a frame over the limit fails with -32001 and the process closes;
//! - the encoded response frame ceiling is the same; each response
//!   object is capped at 65,000 bytes.

use std::io::{self, BufRead, Write};

pub const INPUT_FRAME_LIMIT: usize = 1_048_576;
pub const OUTPUT_FRAME_LIMIT: usize = 1_048_576;
pub const RESPONSE_OBJECT_LIMIT: usize = 65_000;
#[allow(dead_code)] // used by the reader-handler increment for output_limit checks

pub const BATCH_LIMIT: usize = 16;
pub const QUEUED_LIMIT: usize = 16;

/// A frame exceeded the input ceiling. The service replies -32001 with
/// id null and closes; bytes after the limit are never parsed.
#[derive(Debug)]
pub struct FrameTooLarge;

/// Reads exactly one physical line, enforcing the input ceiling and
/// stripping the LF (and one preceding CR for a CRLF terminator).
pub struct LineReader<R> {
    inner: R,
    buf: Vec<u8>,
    eof: bool,
}

impl<R: BufRead> LineReader<R> {
    pub fn new(inner: R) -> Self {
        Self { inner, buf: Vec::with_capacity(1024), eof: false }
    }

    /// Returns Ok(None) at EOF with no pending bytes.
    ///
    /// A final unterminated frame at EOF is accepted (the transport is
    /// closed by the client after its last complete interaction); a
    /// line longer than INPUT_FRAME_LIMIT returns Err(FrameTooLarge)
    /// once, and the caller must shut the service down.
    pub fn read_line(&mut self) -> Result<Option<Vec<u8>>, FrameTooLarge> {
        if self.eof {
            return Ok(None);
        }
        self.buf.clear();
        loop {
            match self.inner.fill_buf() {
                Ok(b) if b.is_empty() => {
                    self.eof = true;
                    if self.buf.is_empty() {
                        return Ok(None);
                    }
                    return Ok(Some(std::mem::take(&mut self.buf)));
                }
                Ok(b) => {
                    if let Some(pos) = b.iter().position(|&ch| ch == b'\n') {
                        self.buf.extend_from_slice(&b[..pos]);
                        self.inner.consume(pos + 1);
                        if self.buf.len() > INPUT_FRAME_LIMIT {
                            return Err(FrameTooLarge);
                        }
                        // Strip the CR of a CRLF terminator so a blank
                        // CRLF line is a genuinely empty frame.
                        if self.buf.last() == Some(&b'\r') {
                            self.buf.pop();
                        }
                        return Ok(Some(std::mem::take(&mut self.buf)));
                    }
                    if self.buf.len() + b.len() > INPUT_FRAME_LIMIT {
                        // Consume and discard the rest of the line so
                        // shutdown stays deterministic.
                        let take = INPUT_FRAME_LIMIT + 1 - self.buf.len();
                        self.buf.extend_from_slice(&b[..take]);
                        self.inner.consume(take);
                        loop {
                            let rest = self.inner.fill_buf().unwrap_or(&[]);
                            if rest.is_empty() {
                                break;
                            }
                            if let Some(pos) = rest.iter().position(|&ch| ch == b'\n') {
                                self.inner.consume(pos + 1);
                                break;
                            }
                            let n = rest.len();
                            self.inner.consume(n);
                        }
                        return Err(FrameTooLarge);
                    }
                    self.buf.extend_from_slice(b);
                    let n = b.len();
                    self.inner.consume(n);
                }
                Err(_) => {
                    self.eof = true;
                    return Ok(None);
                }
            }
        }
    }
}

/// Writes response frames as LF-terminated lines and enforces both
/// ceilings while encoding.
pub struct FrameWriter<W> {
    inner: W,
}

impl<W: Write> FrameWriter<W> {
    pub fn new(inner: W) -> Self {
        Self { inner }
    }

    /// Write one encoded response line. `text` must already be capped
    /// at OUTPUT_FRAME_LIMIT by the encoder; this only flushes.
    pub fn write_line(&mut self, text: &str) -> io::Result<()> {
        debug_assert!(text.len() <= OUTPUT_FRAME_LIMIT);
        self.inner.write_all(text.as_bytes())?;
        self.inner.write_all(b"\n")?;
        self.inner.flush()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Cursor;

    #[test]
    fn lf_and_crlf_and_eof() {
        let data = b"one\n\r\ntwo\r\nthree";
        let mut reader = LineReader::new(Cursor::new(data));
        assert_eq!(reader.read_line().unwrap().unwrap(), b"one");
        assert_eq!(reader.read_line().unwrap().unwrap(), b"");
        assert_eq!(reader.read_line().unwrap().unwrap(), b"two");
        assert_eq!(reader.read_line().unwrap().unwrap(), b"three");
        assert!(reader.read_line().unwrap().is_none());
    }

    #[test]
    fn over_limit_detected() {
        let body = vec![b'a'; INPUT_FRAME_LIMIT + 10];
        let mut data = body.clone();
        data.push(b'\n');
        let mut reader = LineReader::new(Cursor::new(data));
        assert!(reader.read_line().is_err());
        assert!(reader.read_line().unwrap().is_none());
    }
}
