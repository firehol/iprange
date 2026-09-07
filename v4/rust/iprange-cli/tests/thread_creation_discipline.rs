//! Product-code thread-creation discipline tripwire (astra round-4
//! P2): a best-effort diagnostic thread must never be created with
//! `std::thread::spawn`, because a failed thread creation panics and
//! can defeat the termination bound it was meant to serve (the
//! forced-exit and graceful-fatal diagnostics).  Every product-code
//! spawn must go through `std::thread::Builder`, whose spawn returns
//! a `Result` the caller can ignore.
//!
//! The scan skips `#[cfg(test)]` items: the attribute opens a region
//! whose brace depth starts at the item that follows it, and the
//! region ends when that item's own depth returns to zero.  This
//! handles both a `#[cfg(test)]` method inside an impl and a
//! `#[cfg(test)] mod tests` block.

use std::path::{Path, PathBuf};

fn braces(line: &str) -> i64 {
    line.bytes().filter(|b| *b == b'{').count() as i64
        - line.bytes().filter(|b| *b == b'}').count() as i64
}

/// Returns (checked_lines, skipped_lines, checked_line_numbers,
/// problems).  checked_line_numbers and skipped_line_numbers are
/// 1-based; the test uses them to pin specific production sites as
/// checked so a region-skip regression cannot silently widen a
/// `#[cfg(test)]` region over product code.
#[allow(clippy::type_complexity)]
fn scan_file(
    path: &Path,
    problems: &mut Vec<String>,
) -> (usize, usize, Vec<usize>, Vec<usize>) {
    let text = std::fs::read_to_string(path).expect("read source file");
    let lines: Vec<&str> = text.lines().collect();
    let mut index = 0usize;
    let mut checked = 0usize;
    let mut skipped = 0usize;
    let mut checked_lines = Vec::new();
    let mut skipped_lines = Vec::new();
    while index < lines.len() {
        let trimmed = lines[index].trim();
        if trimmed.starts_with("#[cfg(test)]") {
            // Region starts at this attribute and covers the item it
            // decorates.  If the attribute is on its own line the
            // item opens on the next line; the region ends when the
            // item's brace depth returns to zero.
            //
            // The decorated item's own first line must seed the depth
            // and then NOT be counted again by the loop: counting it
            // twice would leave the depth one higher than the real
            // nesting and would swallow product code after the item
            // until the phantom brace closed (the whole remainder of
            // session.rs after a standalone test-only fn, including
            // the signal-watchdog diagnostic spawn).
            let mut depth = braces(lines[index]);
            let mut region_start = index;
            if depth == 0 {
                if index + 1 < lines.len() {
                    depth = braces(lines[index + 1]);
                    region_start = index + 1;
                } else {
                    skipped += 1;
                    index += 1;
                }
            }
            if depth == 0 {
                skipped += 1;
                index += 1;
                continue;
            }
            // Seed consumed the item's opening line; skip from the
            // line after it without recounting the seed.
            skipped += region_start - index + 1;
            index = region_start + 1;
            while index < lines.len() && depth > 0 {
                depth += braces(lines[index]);
                skipped += 1;
                skipped_lines.push(index + 1);
                index += 1;
            }
            for line in region_start + 1..index {
                skipped_lines.push(line);
            }
            continue;
        }
        checked += 1;
        checked_lines.push(index + 1);
        if trimmed.contains("std::thread::spawn(") {
            problems.push(format!(
                "{}:{}: panicking std::thread::spawn in product code",
                path.display(),
                index + 1
            ));
        }
        index += 1;
    }
    (checked, skipped, checked_lines, skipped_lines)
}

#[test]
fn no_panicking_thread_spawn_in_product_code() {
    let manifest = std::env::var("CARGO_MANIFEST_DIR").expect("manifest dir");
    let src = PathBuf::from(manifest).join("src");
    let mut problems = Vec::new();
    let mut checked = 0usize;
    let mut skipped = 0usize;
    fn walk(
        dir: &Path,
        problems: &mut Vec<String>,
        checked: &mut usize,
        skipped: &mut usize,
        checked_lines: &mut Vec<usize>,
        skipped_lines: &mut Vec<usize>,
    ) {
        for entry in std::fs::read_dir(dir).expect("read src dir") {
            let entry = entry.expect("dir entry");
            let path = entry.path();
            if path.is_dir() {
                walk(&path, problems, checked, skipped, checked_lines, skipped_lines);
            } else if path.extension().map(|e| e == "rs").unwrap_or(false) {
                let (c, s, cl, sl) = scan_file(&path, problems);
                *checked += c;
                *skipped += s;
                checked_lines.extend(cl);
                skipped_lines.extend(sl);
            }
        }
    }
    let mut checked_lines = Vec::new();
    let mut skipped_lines = Vec::new();
    walk(&src, &mut problems, &mut checked, &mut skipped,
         &mut checked_lines, &mut skipped_lines);
    // The scan must actually observe product code; a parser regression
    // that skips whole files would make the tripwire vacuous.
    assert!(
        checked > 1000,
        "thread-discipline scan checked only {checked} lines (skipped {skipped})"
    );
    // Pin the signal-watchdog diagnostic spawn site as checked: a
    // brace-counting regression that widens a `#[cfg(test)]` skip
    // region over the watchdog (the previous scanner bug swallowed
    // the whole remainder of session.rs) would silently disable the
    // gate for the exact site that must stay fallible.
    let session = src.join("rpc").join("session.rs");
    let session_lines = std::fs::read_to_string(&session).expect("session.rs");
    let watchdog: Vec<usize> = session_lines
        .lines()
        .enumerate()
        .filter(|(_, l)| l.contains("iprange-signal-diag"))
        .map(|(i, _)| i + 1)
        .collect();
    assert!(
        !watchdog.is_empty(),
        "session.rs signal watchdog marker not found; the pin is stale"
    );
    for line in &watchdog {
        assert!(
            checked_lines.contains(line),
            "session.rs:{line} (signal watchdog) is inside a skipped region; \
             the thread-discipline tripwire would not detect a panicking \
             spawn reintroduced there"
        );
    }
    assert!(
        problems.is_empty(),
        "product thread spawns that panic on creation failure:\n{}",
        problems.join("\n")
    );
}
