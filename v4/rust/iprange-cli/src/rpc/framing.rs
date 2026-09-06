//! Physical line transport for the v1 JSON-RPC stdio contract.
//!
//! Rules implemented (iprange-jsonrpc-v1.md):
//! - one physical line per request object or batch array;
//! - LF and CRLF terminate input frames; output frames always end in LF;
//! - an unescaped CR or LF inside JSON is invalid;
//! - hard frame ceiling 1,048,576 bytes before the line terminator;
//! - a frame over the limit fails with -32001 and the process closes;
//! - only `Ok([])` from the underlying reader means EOF; a real stdin
//!   io error is a fatal input-transport event, never EOF;
//! - the encoded response frame ceiling is the same; each response
//!   object is capped at 65,000 bytes.

use std::io::{self, BufRead, Write};

pub const INPUT_FRAME_LIMIT: usize = 1_048_576;
pub const OUTPUT_FRAME_LIMIT: usize = 1_048_576;
pub const RESPONSE_OBJECT_LIMIT: usize = 65_000;

pub const BATCH_LIMIT: usize = 16;
pub const QUEUED_LIMIT: usize = 16;

/// A line-read failure.
///
/// `FrameTooLarge`: a frame exceeded the input ceiling; the service
/// replies -32001 with id null and closes, and bytes after the limit
/// are never parsed. `Io`: the underlying reader reported a real
/// stdin failure; only `Ok([])` means EOF, so an io error is a fatal
/// input-transport event, never a clean end of input.
#[derive(Debug)]
pub enum LineReadError {
    FrameTooLarge,
    Io(io::Error),
}

/// Reads exactly one physical line, enforcing the input ceiling and
/// stripping the LF (and one preceding CR for a CRLF terminator).
pub struct LineReader<R> {
    inner: R,
    buf: Vec<u8>,
    eof: bool,
}

impl<R: BufRead> LineReader<R> {
    pub fn new(inner: R) -> Self {
        Self {
            inner,
            buf: Vec::with_capacity(1024),
            eof: false,
        }
    }

    /// Returns Ok(None) at EOF with no pending bytes.
    ///
    /// A final unterminated frame at EOF is accepted (the transport is
    /// closed by the client after its last complete interaction); a
    /// line longer than INPUT_FRAME_LIMIT returns
    /// Err(LineReadError::FrameTooLarge) once, and the caller must shut
    /// the service down. A real io::Error from the underlying reader
    /// returns Err(LineReadError::Io); only Ok([]) is EOF.
    pub fn read_line(&mut self) -> Result<Option<Vec<u8>>, LineReadError> {
        if self.eof {
            return Ok(None);
        }
        self.buf.clear();
        loop {
            match self.inner.fill_buf() {
                Ok([]) => {
                    self.eof = true;
                    if self.buf.is_empty() {
                        return Ok(None);
                    }
                    // A final unterminated frame at EOF is accepted
                    // only up to the ceiling.  At EOF there is no
                    // terminator to strip a CR for, so any accumulated
                    // payload above the limit is a framing failure
                    // with a non-zero exit (Go parity; role-round
                    // finding: the EOF-resolved LIMIT+1 shape used to
                    // answer -32001 at the schema layer and then exit
                    // 0 through the clean-EOF path).
                    if self.buf.len() > INPUT_FRAME_LIMIT {
                        return Err(LineReadError::FrameTooLarge);
                    }
                    return Ok(Some(std::mem::take(&mut self.buf)));
                }
                Ok(b) => {
                    if let Some(pos) = b.iter().position(|&ch| ch == b'\n') {
                        self.buf.extend_from_slice(&b[..pos]);
                        self.inner.consume(pos + 1);
                        // Strip the CR of a CRLF terminator before the
                        // limit check: the ceiling applies to the frame
                        // payload, not the line terminator.
                        if self.buf.last() == Some(&b'\r') {
                            self.buf.pop();
                        }
                        if self.buf.len() > INPUT_FRAME_LIMIT {
                            return Err(LineReadError::FrameTooLarge);
                        }
                        return Ok(Some(std::mem::take(&mut self.buf)));
                    }
                    // One accumulated byte may still be the CR of a
                    // CRLF terminator, so only a payload that exceeds
                    // the limit even after stripping one byte is
                    // definitely over the ceiling here.
                    if self.buf.len() + b.len() > INPUT_FRAME_LIMIT + 1 {
                        // The accumulated line is over the ceiling even
                        // after stripping one CR of a CRLF terminator.
                        // Report the failure immediately without waiting
                        // for the terminator or EOF: the peer may hold
                        // stdin open forever, and the -32001 + close
                        // (spec iprange-jsonrpc-v1.md) must not depend
                        // on the frame being terminated (role-round
                        // finding).  Bytes after the limit are never
                        // parsed; the caller shuts the session down.
                        return Err(LineReadError::FrameTooLarge);
                    }
                    self.buf.extend_from_slice(b);
                    // A payload of exactly LIMIT+1 bytes is final only
                    // when its last byte can still be the CR of a CRLF
                    // terminator (payload of LIMIT bytes plus CRLF).
                    // When the last byte is any other byte, no
                    // continuation can make the frame legal -- even a
                    // following LF leaves the payload over the ceiling
                    // -- so report the failure immediately without
                    // waiting for the terminator or EOF (Go parity;
                    // external review finding).
                    if self.buf.len() == INPUT_FRAME_LIMIT + 1
                        && self.buf.last() != Some(&b'\r')
                    {
                        return Err(LineReadError::FrameTooLarge);
                    }
                    let n = b.len();
                    self.inner.consume(n);
                }
                Err(error) => {
                    self.eof = true;
                    return Err(LineReadError::Io(error));
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
        assert!(matches!(
            reader.read_line(),
            Err(LineReadError::FrameTooLarge)
        ));
        assert!(reader.read_line().unwrap().is_none());
    }

    #[test]
    fn exact_limit_frames_are_accepted_with_lf_and_crlf() {
        // The ceiling applies before the line terminator, so a payload
        // of exactly INPUT_FRAME_LIMIT bytes is a legal frame under
        // both LF and CRLF terminators.
        let body = vec![b'x'; INPUT_FRAME_LIMIT];
        let mut lf = body.clone();
        lf.push(b'\n');
        let mut reader = LineReader::new(Cursor::new(lf));
        assert_eq!(reader.read_line().unwrap().unwrap(), body);
        assert!(reader.read_line().unwrap().is_none());

        let mut crlf = body.clone();
        crlf.extend_from_slice(b"\r\n");
        let mut reader = LineReader::new(Cursor::new(crlf));
        assert_eq!(reader.read_line().unwrap().unwrap(), body);
        assert!(reader.read_line().unwrap().is_none());
    }

    /// One-shot reader that delivers exactly one payload and panics if
    /// it is queried again: the held-open shape where no terminator or
    /// EOF ever arrives cannot block in a unit test, so the decisive
    /// property is that the reader must not ask for more input after a
    /// LIMIT+1 non-CR payload (external review finding: the pre-fix
    /// reader awaited another byte forever and never reported).
    struct HeldNonCR {
        data: Vec<u8>,
        done: bool,
    }

    impl std::io::Read for HeldNonCR {
        fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
            assert!(
                !self.done,
                "reader queried again: a LIMIT+1 non-CR frame must be                  final without more input"
            );
            self.done = true;
            let n = buf.len().min(self.data.len());
            buf[..n].copy_from_slice(&self.data[..n]);
            Ok(n)
        }
    }

    #[test]
    fn held_limit_plus_one_non_cr_is_immediate() {
        // A held-open frame of exactly LIMIT+1 bytes whose last byte
        // is not the CR of a CRLF terminator can never become legal,
        // so the reader must report FrameTooLarge immediately instead
        // of awaiting the terminator or EOF (the peer may hold stdin
        // open forever).
        let reader = std::io::BufReader::with_capacity(
            INPUT_FRAME_LIMIT + 2,
            HeldNonCR {
                data: vec![b'x'; INPUT_FRAME_LIMIT + 1],
                done: false,
            },
        );
        let mut line_reader = LineReader::new(reader);
        assert!(matches!(
            line_reader.read_line(),
            Err(LineReadError::FrameTooLarge)
        ));
    }

    #[test]
    fn held_limit_plus_one_cr_tail_resolves_on_lf() {
        // A held LIMIT+1 payload whose last byte is the CR of a CRLF
        // terminator is still potentially legal: a following LF leaves
        // exactly LIMIT payload bytes, so the reader must keep waiting
        // (not report early) and then accept the CRLF frame.
        let mut data = vec![b'x'; INPUT_FRAME_LIMIT + 1];
        data[INPUT_FRAME_LIMIT] = b'\r';
        data.extend_from_slice(b"\n");
        let mut reader = LineReader::new(Cursor::new(data));
        let line = reader.read_line().unwrap().unwrap();
        assert_eq!(line.len(), INPUT_FRAME_LIMIT);
        assert!(reader.read_line().unwrap().is_none());
    }

    #[test]
    fn one_byte_over_limit_is_rejected() {
        let mut data = vec![b'x'; INPUT_FRAME_LIMIT + 1];
        data.push(b'\n');
        let mut reader = LineReader::new(Cursor::new(data));
        assert!(matches!(
            reader.read_line(),
            Err(LineReadError::FrameTooLarge)
        ));
        assert!(reader.read_line().unwrap().is_none());
    }

    /// Reader whose fill_buf fails like stdin on a broken pipe.
    struct FailingReader;

    impl std::io::Read for FailingReader {
        fn read(&mut self, _buf: &mut [u8]) -> io::Result<usize> {
            Err(io::Error::new(io::ErrorKind::BrokenPipe, "stdin broken"))
        }
    }

    impl std::io::BufRead for FailingReader {
        fn fill_buf(&mut self) -> io::Result<&[u8]> {
            Err(io::Error::new(io::ErrorKind::BrokenPipe, "stdin broken"))
        }
        fn consume(&mut self, _amt: usize) {}
    }

    #[test]
    fn io_error_is_fatal_not_eof() {
        // Only Ok([]) means EOF; a real io error must surface as an
        // input-transport failure instead of a clean end of input.
        let mut reader = LineReader::new(FailingReader);
        match reader.read_line() {
            Err(LineReadError::Io(error)) => {
                assert_eq!(error.kind(), io::ErrorKind::BrokenPipe);
            }
            other => panic!("expected a fatal io error, got {other:?}"),
        }
    }
}
