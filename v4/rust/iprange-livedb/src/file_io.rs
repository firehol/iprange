//! Checked positional page I/O.

use std::fs::File;

use crate::contract::{PAGE_SHIFT, PAGE_SIZE};
use crate::error::{Error, Result};

#[cfg(unix)]
use std::os::unix::fs::FileExt;
#[cfg(windows)]
use std::os::windows::fs::FileExt;

pub(crate) fn read_page(
    file: &File,
    page_number: u32,
    page_count: u64,
    page: &mut [u8; PAGE_SIZE],
) -> Result<()> {
    if page_number < 2 || u64::from(page_number) >= page_count {
        return Err(Error::Corrupt("page number is outside committed bounds"));
    }
    let offset = u64::from(page_number)
        .checked_shl(u32::from(PAGE_SHIFT))
        .ok_or(Error::Corrupt("page offset overflow"))?;
    read_exact_at(file, page, offset)
}

pub(crate) fn read_exact_at(file: &File, mut output: &mut [u8], mut offset: u64) -> Result<()> {
    while !output.is_empty() {
        match read_at(file, output, offset) {
            Ok(0) => return Err(Error::Corrupt("page is physically truncated")),
            Ok(read) => {
                output = &mut output[read..];
                offset = offset
                    .checked_add(read as u64)
                    .ok_or(Error::Corrupt("page offset overflow"))?;
            }
            Err(error) if error.kind() == std::io::ErrorKind::Interrupted => {}
            Err(error) => return Err(error.into()),
        }
    }
    Ok(())
}

#[cfg(unix)]
fn read_at(file: &File, output: &mut [u8], offset: u64) -> std::io::Result<usize> {
    file.read_at(output, offset)
}

#[cfg(windows)]
fn read_at(file: &File, output: &mut [u8], offset: u64) -> std::io::Result<usize> {
    file.seek_read(output, offset)
}

#[cfg(not(any(unix, windows)))]
fn read_at(_file: &File, _output: &mut [u8], _offset: u64) -> std::io::Result<usize> {
    Err(std::io::Error::new(
        std::io::ErrorKind::Unsupported,
        "safe positional file reads are not implemented on this platform",
    ))
}

pub(crate) fn write_exact_at(file: &File, mut input: &[u8], mut offset: u64) -> Result<()> {
    while !input.is_empty() {
        match write_at(file, input, offset) {
            Ok(0) => {
                return Err(std::io::Error::new(
                    std::io::ErrorKind::WriteZero,
                    "positional page write made no progress",
                )
                .into())
            }
            Ok(written) => {
                input = &input[written..];
                offset = offset
                    .checked_add(written as u64)
                    .ok_or(Error::ArithmeticOverflow("page write offset"))?;
            }
            Err(error) if error.kind() == std::io::ErrorKind::Interrupted => {}
            Err(error) => return Err(error.into()),
        }
    }
    Ok(())
}

#[cfg(unix)]
fn write_at(file: &File, input: &[u8], offset: u64) -> std::io::Result<usize> {
    file.write_at(input, offset)
}

#[cfg(windows)]
fn write_at(file: &File, input: &[u8], offset: u64) -> std::io::Result<usize> {
    file.seek_write(input, offset)
}

#[cfg(not(any(unix, windows)))]
fn write_at(_file: &File, _input: &[u8], _offset: u64) -> std::io::Result<usize> {
    Err(std::io::Error::new(
        std::io::ErrorKind::Unsupported,
        "safe positional file writes are not implemented on this platform",
    ))
}

#[cfg(all(test, any(unix, windows)))]
mod tests {
    use super::*;
    use std::io::{Seek, SeekFrom, Write};
    use std::time::{SystemTime, UNIX_EPOCH};

    #[test]
    fn positional_read_keeps_the_file_cursor_unchanged() {
        let mut file = tempfile();
        file.write_all(&vec![7u8; 3 * PAGE_SIZE]).unwrap();
        file.seek(SeekFrom::Start(123)).unwrap();

        let mut page = [0; PAGE_SIZE];
        read_page(&file, 2, 3, &mut page).unwrap();
        assert!(page.iter().all(|&byte| byte == 7));
        assert_eq!(file.stream_position().unwrap(), 123);
    }

    fn tempfile() -> File {
        let path = std::env::temp_dir().join(format!(
            "iprange-v4-io-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        let file = File::options()
            .read(true)
            .write(true)
            .create_new(true)
            .open(&path)
            .unwrap();
        std::fs::remove_file(path).unwrap();
        file
    }
}
