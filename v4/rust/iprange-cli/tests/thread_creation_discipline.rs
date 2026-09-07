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

/// Returns (checked_lines, skipped_lines, problems).
fn scan_file(path: &Path, problems: &mut Vec<String>) -> (usize, usize) {
    let text = std::fs::read_to_string(path).expect("read source file");
    let lines: Vec<&str> = text.lines().collect();
    let mut index = 0usize;
    let mut checked = 0usize;
    let mut skipped = 0usize;
    while index < lines.len() {
        let trimmed = lines[index].trim();
        if trimmed.starts_with("#[cfg(test)]") {
            // Region starts at this attribute and covers the item it
            // decorates.  If the attribute is on its own line the
            // item opens on the next line; the region ends when the
            // item's brace depth returns to zero.
            let mut depth = braces(lines[index]);
            if depth == 0 && index + 1 < lines.len() {
                depth = braces(lines[index + 1]);
            }
            skipped += 1;
            index += 1;
            while index < lines.len() && depth > 0 {
                depth += braces(lines[index]);
                skipped += 1;
                index += 1;
            }
            continue;
        }
        checked += 1;
        if trimmed.contains("std::thread::spawn(") {
            problems.push(format!(
                "{}:{}: panicking std::thread::spawn in product code",
                path.display(),
                index + 1
            ));
        }
        index += 1;
    }
    (checked, skipped)
}

#[test]
fn no_panicking_thread_spawn_in_product_code() {
    let manifest = std::env::var("CARGO_MANIFEST_DIR").expect("manifest dir");
    let src = PathBuf::from(manifest).join("src");
    let mut problems = Vec::new();
    let mut checked = 0usize;
    let mut skipped = 0usize;
    fn walk(dir: &Path, problems: &mut Vec<String>, checked: &mut usize,
            skipped: &mut usize) {
        for entry in std::fs::read_dir(dir).expect("read src dir") {
            let entry = entry.expect("dir entry");
            let path = entry.path();
            if path.is_dir() {
                walk(&path, problems, checked, skipped);
            } else if path.extension().map(|e| e == "rs").unwrap_or(false) {
                let (c, s) = scan_file(&path, problems);
                *checked += c;
                *skipped += s;
            }
        }
    }
    walk(&src, &mut problems, &mut checked, &mut skipped);
    // The scan must actually observe product code; a parser regression
    // that skips whole files would make the tripwire vacuous.
    assert!(
        checked > 1000,
        "thread-discipline scan checked only {checked} lines (skipped {skipped})"
    );
    assert!(
        problems.is_empty(),
        "product thread spawns that panic on creation failure:\n{}",
        problems.join("\n")
    );
}
