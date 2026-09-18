//! Module-level test helpers shared by the legacy submodule test blocks.

/// Report one test as unscored because its fixture cannot exist on this
/// host (running as root, a missing device, ...), naming the condition.
///
/// This is the legacy-surface twin of the established Windows-pin policy
/// (`io::caller_open`'s `record_unsupported`): the status is accepted only
/// when the caller asked for a tally through `IPRANGE_V4_WIN_PIN_TALLY`
/// (the battery's shared tally file, named by its own pin), and the run
/// fails when the fixture is unavailable and nobody asked to record it.
/// A silent `return` would let a root or host-limited run report a pin as
/// passed without judging anything, which is the unavailable-vs-skipped
/// defect class user decision 2A polices.
///
/// Only the unix root branch of the denied-open pin calls it, so the
/// helper is compiled on unix; the shape checks below read this file as
/// text and therefore run on every host.
#[cfg(unix)]
pub(crate) fn record_unscored_pin(pin: &str, reason: &str) {
    match std::env::var_os("IPRANGE_V4_WIN_PIN_TALLY") {
        Some(tally) => {
            use std::io::Write;
            let mut sink = std::fs::OpenOptions::new()
                .create(true)
                .append(true)
                .open(&tally)
                .unwrap_or_else(|error| {
                    panic!("{pin}: cannot record the unscored status in {tally:?}: {error}")
                });
            writeln!(sink, "UNSCORED {pin}: {reason}")
                .unwrap_or_else(|error| panic!("{pin}: cannot record the status: {error}"));
        }
        None => panic!(
            "{pin}: {reason}; the fixture is unavailable and no tally destination is \
             configured, so the pin fails rather than passing unverified (set \
             IPRANGE_V4_WIN_PIN_TALLY=<file> to record it as UNSCORED)"
        ),
    }
}

#[cfg(test)]
mod root_pin_fail_closed_tests {
    //! Fail-closed shape check for the root-scoped denied-open pin.
    //!
    //! The condition is compiled on unix hosts, so a Linux battery cannot
    //! observe whether a root run records or merely returns. Reading this
    //! file is the check available to any host — the same technique as
    //! `caller_open.rs`'s `windows_pin_fail_closed_tests`. Reverting the
    //! denied-open pin to a quiet `return`, or muting the tally
    //! requirement inside `record_unscored_pin`, fails here.
    const SOURCE: &str = include_str!("parse.rs");

    /// The text of one function, from its opening brace to the matching
    /// closing brace (balanced-brace depth counter; an unbalanced body
    /// panics rather than guesses).
    fn body_of(name: &str) -> &str {
        let head = format!("fn {name}(");
        let start = SOURCE
            .find(&head)
            .unwrap_or_else(|| panic!("expected fn {name} in the pinned source"));
        let relative = SOURCE[start..]
            .find('{')
            .unwrap_or_else(|| panic!("expected a body for fn {name}"));
        let open = start + relative;
        let mut depth = 0i32;
        for (offset, byte) in SOURCE[open..].bytes().enumerate() {
            match byte {
                b'{' => depth += 1,
                b'}' => {
                    depth -= 1;
                    if depth == 0 {
                        return &SOURCE[open..open + offset + 1];
                    }
                }
                _ => {}
            }
        }
        panic!("unbalanced body for fn {name}");
    }

    /// `body` with each sanctioned exit removed: a live
    /// `record_unscored_pin(..)` call and the `return` that may follow it.
    /// Any `return` still in the text is an exit that reports nothing —
    /// this is what closes the comment escape hatch a bare `contains`
    /// would leave (a commented-out call keeps its `return`). Ported from
    /// `caller_open.rs`'s twin of the same name.
    fn without_recorded_exits(body: &str) -> String {
        const RECORD: &str = "record_unscored_pin(";
        // Comments first: prose about "a silent return" must not be
        // mistaken for a live exit, and a commented-out record call must
        // not be mistaken for a live one (it disappears here, leaving its
        // `return` behind for the assertion to catch).
        let code: String = body
            .lines()
            .map(|line| match line.find("//") {
                Some(at) => &line[..at],
                None => line,
            })
            .collect::<Vec<_>>()
            .join("\n");
        let mut out = String::new();
        let mut rest = code.as_str();
        while let Some(at) = rest.find(RECORD) {
            out.push_str(&rest[..at]);
            let mut tail = &rest[at + RECORD.len()..];
            let mut depth = 1i32;
            let mut closed = None;
            for (offset, byte) in tail.bytes().enumerate() {
                match byte {
                    b'(' => depth += 1,
                    b')' => {
                        depth -= 1;
                        if depth == 0 {
                            closed = Some(offset + 1);
                            break;
                        }
                    }
                    _ => {}
                }
            }
            let closed = closed.unwrap_or_else(|| panic!("unterminated call to {RECORD}"));
            tail = tail[closed..].trim_start();
            if let Some(after) = tail.strip_prefix(';') {
                tail = after.trim_start();
            }
            if let Some(after) = tail.strip_prefix("return") {
                tail = after.trim_start();
                if let Some(after) = tail.strip_prefix(';') {
                    tail = after.trim_start();
                }
            }
            rest = tail;
        }
        out.push_str(rest);
        out
    }

    #[test]
    fn the_root_branch_of_the_denied_open_pin_records_rather_than_returns() {
        let body = body_of("a_directory_whose_open_fails_is_not_made_empty");
        // Start after the condition's own opening brace; the first `}`
        // then closes the root branch.
        let branch = body
            .find("geteuid() } == 0 {")
            .map(|i| &body[i + "geteuid() } == 0 {".len()..])
            .and_then(|tail| tail.find('}').map(|e| &tail[..e]))
            .expect("the denied-open pin keeps its root branch");
        assert!(
            branch.contains("record_unscored_pin("),
            "the root branch must record the unscored status; a quiet return \
             lets a root run pass the 3A pin unverified"
        );
        // The strict half: with every LIVE record call and its optional
        // `return` stripped, no `return` may remain. A commented-out call
        // therefore leaves a bare return and fails here.
        let stripped = without_recorded_exits(branch);
        assert!(
            !stripped.contains("return"),
            "the root branch exits without recording: {}",
            stripped.trim()
        );
        assert!(
            !branch.contains("eprintln!"),
            "printing a scope note is not a substitute for the tally: the \
             battery judges the tally file, not stdout"
        );
    }

    /// The helper must fail closed when no tally is configured. This
    /// mirrors `caller_open.rs`'s `windows_pin_fail_closed_tests`: the
    /// environment variable is process-wide, so a live panic test would
    /// depend on the runner's environment; the shape check is the
    /// runner-independent instrument, and muting the `None => panic!`
    /// arm (or renaming the tally variable) fails here.
    #[test]
    fn record_unscored_pin_fails_closed_without_a_tally() {
        let source = include_str!("tests.rs");
        let fn_start = source
            .find("pub(crate) fn record_unscored_pin(")
            .expect("the helper exists");
        let body = &source[fn_start..];
        assert!(
            body.contains("None => panic!("),
            "without IPRANGE_V4_WIN_PIN_TALLY the helper must fail, not shrug"
        );
        assert!(
            body.contains("IPRANGE_V4_WIN_PIN_TALLY"),
            "the helper must read the shared tally variable by name"
        );
    }
}
