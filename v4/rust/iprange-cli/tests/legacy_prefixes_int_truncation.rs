//! `--prefixes` tests its bound on the `int` the value was cast to.
//!
//! The C tokenizer is a `strtol` loop, and the result is cast before it
//! is compared with the family bound:
//!
//! ```c
//! j = (int)strtol(s, &e, 10);
//! if(j <= 0 || j > 32) { /* report %d, which is the cast j */ }
//! ```
//!
//! (`src/iprange.c:534-559`; the IPv6 twin at `src/iprange6_main.c:127-146`
//! uses the same cast with the bound 128.) The released binary is a
//! 64-bit build, so `long` is 64 bits and `int` is 32: a token above
//! `INT_MAX` wraps, and that wrapped value is both what the bound test
//! sees and what the diagnostic prints. `ERANGE` is not checked in this
//! branch, so it shows up only through the wrap. The observable
//! consequences, each pinned below against the C reference:
//!
//! - `2147483648` is reported as `-2147483648`, not as itself;
//! - `4294967299` passes the bound as `3`, so it is accepted;
//! - `LONG_MAX` is reported as `-1` and `LONG_MIN` as `0`;
//! - `2147483647` (`INT_MAX`) is *not* wrapped, so it is reported as
//!   itself -- the case that separates cast-then-bound from bound-then-cast.
//!
//! The sibling prefix options are a different parser and must not share
//! this behaviour: `--min-prefix` and `--default-prefix`/`-p` go through
//! `parse_long_option_or_die()` (`src/iprange.c:452-463`), which bounds
//! the `long` itself, rejects `ERANGE`, and requires `strtol` to consume
//! the whole value. That parser does accept the leading whitespace and
//! the sign `strtol` consumes, which is why `--min-prefix +5` is valid
//! where an all-digits parser would reject it.

#![cfg(unix)]

use std::ffi::OsStr;
use std::path::Path;

mod parity_support;
use parity_support::{assert_parity, Scratch};

/// The IPv4 prefix diagnostic C prints for a value outside 1..=32.
fn v4_invalid(value: &str) -> Vec<u8> {
    format!("iprange: Only prefixes from 1 to 32 can be set (32 is always enabled). {value} is invalid.\n").into_bytes()
}

/// The IPv6 prefix diagnostic C prints for a value outside 1..=128.
fn v6_invalid(value: &str) -> Vec<u8> {
    format!("iprange: Only prefixes from 1 to 128 can be set. {value} is invalid.\n").into_bytes()
}

/// The four single addresses of the /30 the IPv4 cases read, which is
/// what a run with no usable prefix wider than /32 prints.
const V4_ADDRESSES: &[u8] = b"10.0.0.0\n10.0.0.1\n10.0.0.2\n10.0.0.3\n";

/// The sixteen single addresses of the /124 the IPv6 cases read. The
/// first one renders as `2001:db8::`, not `2001:db8::0`.
fn v6_addresses() -> Vec<u8> {
    let mut out = String::from("2001:db8::\n");
    for i in 1..16u32 {
        out.push_str(&format!("2001:db8::{i:x}\n"));
    }
    out.into_bytes()
}

/// Write the two input fixtures both families use.
fn inputs(dir: &Scratch) -> (std::path::PathBuf, std::path::PathBuf) {
    (
        dir.file(b"in4", b"10.0.0.0/30\n"),
        dir.file(b"in6", b"2001:db8::/124\n"),
    )
}

/// Run `iprange <args> <file>` with an empty stdin.
fn check(label: &str, file: &Path, args: &[&OsStr], expect: (i32, &[u8], &[u8])) {
    let mut argv: Vec<&OsStr> = args.to_vec();
    argv.push(file.as_os_str());
    assert_parity(label, &argv, b"", expect);
}

#[test]
fn ipv4_prefixes_are_bounded_after_the_cast_to_int() {
    let dir = Scratch::new("prefixes-v4");
    let (in4, _) = inputs(&dir);

    // 0x1_0000_0003 truncates to 3, so the list is accepted as /3. The
    // /30 has no /3 to print, so it falls back to single addresses.
    check(
        "--prefixes 4294967299 accepted as 3",
        &in4,
        &[OsStr::new("--prefixes"), OsStr::new("4294967299")],
        (0, V4_ADDRESSES, b"".as_slice()),
    );
    check(
        "--prefixes 4294967297 accepted as 1",
        &in4,
        &[OsStr::new("--prefixes"), OsStr::new("4294967297")],
        (0, V4_ADDRESSES, b"".as_slice()),
    );
    check(
        "--prefixes 4294967298 accepted as 2",
        &in4,
        &[OsStr::new("--prefixes"), OsStr::new("4294967298")],
        (0, V4_ADDRESSES, b"".as_slice()),
    );
    // A truncation that lands inside the range is accepted in a list too.
    check(
        "--prefixes 1,4294967299 accepted",
        &in4,
        &[OsStr::new("--prefixes"), OsStr::new("1,4294967299")],
        (0, V4_ADDRESSES, b"".as_slice()),
    );

    // A truncation that lands outside it reports the truncated number.
    for (token, reported) in [
        ("2147483648", "-2147483648"),
        ("2147483649", "-2147483647"),
        ("4294967295", "-1"),
        ("4294967296", "0"),
        ("9223372036854775807", "-1"),
        ("-9223372036854775808", "0"),
        ("99999999999999999999", "-1"),
        ("-99999999999999999999", "0"),
    ] {
        check(
            &format!("--prefixes {token} reports {reported}"),
            &in4,
            &[OsStr::new("--prefixes"), OsStr::new(token)],
            (1, b"".as_slice(), v4_invalid(reported).as_slice()),
        );
    }

    // INT_MAX is inside `int`, so it is reported unwrapped: the bound is
    // applied to the cast value, and the cast is not the whole story.
    check(
        "--prefixes 2147483647 reports itself",
        &in4,
        &[OsStr::new("--prefixes"), OsStr::new("2147483647")],
        (1, b"".as_slice(), v4_invalid("2147483647").as_slice()),
    );
}

#[test]
fn ipv6_prefixes_use_the_same_cast_with_the_128_bound() {
    let dir = Scratch::new("prefixes-v6");
    let (_, in6) = inputs(&dir);

    check(
        "-6 --prefixes 4294967299 accepted as 3",
        &in6,
        &[
            OsStr::new("-6"),
            OsStr::new("--prefixes"),
            OsStr::new("4294967299"),
        ],
        (0, &v6_addresses(), b"".as_slice()),
    );
    check(
        "-6 --prefixes 2147483648 reports -2147483648",
        &in6,
        &[
            OsStr::new("-6"),
            OsStr::new("--prefixes"),
            OsStr::new("2147483648"),
        ],
        (1, b"".as_slice(), v6_invalid("-2147483648").as_slice()),
    );
    // 2^128-1 saturates strtol at LONG_MAX, which truncates to -1.
    check(
        "-6 --prefixes 2^128-1 reports -1",
        &in6,
        &[
            OsStr::new("-6"),
            OsStr::new("--prefixes"),
            OsStr::new("340282366920938463463374607431768211455"),
        ],
        (1, b"".as_slice(), v6_invalid("-1").as_slice()),
    );
    check(
        "-6 --prefixes 129 rejected",
        &in6,
        &[
            OsStr::new("-6"),
            OsStr::new("--prefixes"),
            OsStr::new("129"),
        ],
        (1, b"".as_slice(), v6_invalid("129").as_slice()),
    );
    // The bound is the family active at that point in argv: with
    // --prefixes before -6 the IPv4 range and message still apply.
    check(
        "--prefixes 200 -6 uses the IPv4 bound",
        &in6,
        &[
            OsStr::new("--prefixes"),
            OsStr::new("200"),
            OsStr::new("-6"),
        ],
        (1, b"".as_slice(), v4_invalid("200").as_slice()),
    );
}

#[test]
fn ipv4_prefix_list_tokenizer_keeps_the_strtol_forms() {
    let dir = Scratch::new("prefixes-tokens");
    let (in4, _) = inputs(&dir);

    // strtol skips leading whitespace and takes a sign, so both of these
    // are the value 5.
    for token in ["+5", " 5"] {
        check(
            &format!("--prefixes {token:?} is prefix 5"),
            &in4,
            &[OsStr::new("--prefixes"), OsStr::new(token)],
            (0, V4_ADDRESSES, b"".as_slice()),
        );
    }
    // A token with no digits is strtol 0, and an empty value is an empty
    // list, which leaves only /32 enabled.
    for (token, reported) in [
        ("abc", "0"),
        ("3x", "0"),
        ("0x10", "0"),
        (",", "0"),
        ("1,,2", "0"),
        ("0", "0"),
        ("-1", "-1"),
        ("33", "33"),
    ] {
        check(
            &format!("--prefixes {token:?} rejected as {reported}"),
            &in4,
            &[OsStr::new("--prefixes"), OsStr::new(token)],
            (1, b"".as_slice(), v4_invalid(reported).as_slice()),
        );
    }
    check(
        "--prefixes empty leaves /32",
        &in4,
        &[OsStr::new("--prefixes"), OsStr::new("")],
        (0, V4_ADDRESSES, b"".as_slice()),
    );
    check(
        "--prefixes 3, keeps its trailing comma",
        &in4,
        &[OsStr::new("--prefixes"), OsStr::new("3,")],
        (0, V4_ADDRESSES, b"".as_slice()),
    );
}

#[test]
fn sibling_prefix_options_bound_the_long_and_accept_the_strtol_forms() {
    let dir = Scratch::new("prefix-siblings");
    let (in4, in6) = inputs(&dir);

    // parse_long_option_or_die(): strtol must consume the whole value,
    // so a sign and leading whitespace are accepted. An int truncation
    // would change these outcomes, which is why they are pinned here.
    for (option, value) in [
        ("--min-prefix", "+5"),
        ("--min-prefix", " 5"),
        ("--default-prefix", "+5"),
        ("--default-prefix", " 0"),
        ("-p", "+5"),
    ] {
        check(
            &format!("{option} {value:?} accepted"),
            &in4,
            &[OsStr::new(option), OsStr::new(value)],
            (0, b"10.0.0.0/30\n".as_slice(), b"".as_slice()),
        );
    }
    // Out-of-`long` is ERANGE, and an in-`long` value above the bound is
    // rejected on its own value: neither is wrapped.
    for (option, value, expected) in [
        ("--min-prefix", "2147483647", "It must be between 1 and 32."),
        (
            "--min-prefix",
            "9223372036854775808",
            "It must be between 1 and 32.",
        ),
        (
            "--default-prefix",
            "2147483647",
            "It must be between 0 and 32.",
        ),
    ] {
        let message =
            format!("iprange: Invalid value '{value}' for {option}. {expected}\n").into_bytes();
        check(
            &format!("{option} {value} rejected unwrapped"),
            &in4,
            &[OsStr::new(option), OsStr::new(value)],
            (1, b"".as_slice(), message.as_slice()),
        );
    }
    // The IPv6 --min-prefix branch is the same shape with the 1..128 bound
    // (src/iprange6_main.c:112-125).
    for (value, expected) in [
        ("+64", None),
        ("129", Some("129")),
        ("2147483648", Some("2147483648")),
    ] {
        let argv = [
            OsStr::new("-6"),
            OsStr::new("--min-prefix"),
            OsStr::new(value),
        ];
        match expected {
            None => {
                let mut a = argv.to_vec();
                a.push(in6.as_os_str());
                assert_parity(
                    &format!("-6 --min-prefix {value} accepted"),
                    &a,
                    b"",
                    (0, b"2001:db8::/124\n".as_slice(), b"".as_slice()),
                );
            }
            Some(_) => {
                let message = format!(
                    "iprange: Invalid value '{value}' for --min-prefix. It must be between 1 and 128.\n"
                )
                .into_bytes();
                check(
                    &format!("-6 --min-prefix {value} rejected"),
                    &in6,
                    &argv,
                    (1, b"".as_slice(), message.as_slice()),
                );
            }
        }
    }
}

#[test]
fn dns_threads_shares_the_long_parser_and_is_not_truncated() {
    let dir = Scratch::new("dns-threads-strtol");
    let (in4, _) = inputs(&dir);

    check(
        "--dns-threads +2 accepted",
        &in4,
        &[OsStr::new("--dns-threads"), OsStr::new("+2")],
        (0, b"10.0.0.0/30\n".as_slice(), b"".as_slice()),
    );
    // INT_MAX + 1 is inside `long` but above the option bound, so it is
    // reported unwrapped rather than as a negative value.
    let message = b"iprange: Invalid value '2147483648' for --dns-threads. \
         It must be an integer greater than or equal to 1.\n"
        .to_vec();
    check(
        "--dns-threads 2147483648 rejected unwrapped",
        &in4,
        &[OsStr::new("--dns-threads"), OsStr::new("2147483648")],
        (1, b"".as_slice(), message.as_slice()),
    );
    // The reduce options are the unsigned parser (strtoull with a digit
    // first character), which does not accept a sign.
    let message =
        b"iprange: Invalid value '+10' for --reduce-entries. It must be a non-negative integer.\n"
            .to_vec();
    check(
        "--reduce-entries +10 rejected",
        &in4,
        &[OsStr::new("--reduce-entries"), OsStr::new("+10")],
        (1, b"".as_slice(), message.as_slice()),
    );
}
