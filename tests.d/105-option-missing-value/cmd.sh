#!/bin/bash

# A value-taking option as the last command-line argument must terminate
# the run, not loop.
#
# Every option branch of the C scan is guarded by `i + 1 < argc`
# (src/iprange.c:515-706, src/iprange6_main.c:112-170), so an option
# without a following value is not taken as an option at all: the token
# falls through to the input branch. In IPv4 mode that means it is opened
# as a file and the run exits 1 with the two load lines; in IPv6 mode the
# token is discarded as a flag (src/iprange6_main.c:176), so the run reads
# stdin and exits 0.
#
# The Rust scan used to return from its value helper without advancing
# the cursor and every caller arm continued the loop, pushing another
# input source per pass: an endless loop that also allocated without
# bound (measured 12.7 GB in 5 s). Both bounds are applied here, because
# a timeout alone would wait the whole timeout and could be preceded by
# memory exhaustion: `ulimit -v` caps the address space (an allocation
# bomb then dies at once with SIGABRT) and `timeout` caps the wall clock.
#
# The option list is derived from the code, not from memory: every token
# whose C branch reads argv[++i], with the aliases the parser accepts.
# `--at` and `--file-list` are absent because neither tool has them.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

IPRANGE=../../iprange
REFERENCE=${IPRANGE_REFERENCE:-/usr/bin/iprange}

# 2 GiB of address space and 10 s: a correct run of this tool needs
# neither, a runaway loop trips one of them immediately.
MEM_KIB=2000000
BOUND=10

printf '1.2.3.4\n1.2.3.5\n' >"$tmpdir/in4.txt"
printf '::1\n' >"$tmpdir/in6.txt"

reference_used=0
[ -x "$REFERENCE" ] && reference_used=1

fail() { echo "# ERROR: $*"; return 1; }

# run_bounded <file-prefix> <args...>: run the engine under both bounds,
# recording rc, stdout and stderr in <file-prefix>.{rc,out,err}.
run_bounded() {
    local prefix=$1
    shift
    (
        ulimit -v "$MEM_KIB"
        timeout "$BOUND" "$IPRANGE" "$@" <"$tmpdir/in" >"$prefix.out" 2>"$prefix.err"
        echo $? >"$prefix.rc"
    )
}

# check_option <option> <valid value>: the trailing (value-less) form in
# both families, and the present-value form as a control.
check_option() {
    local opt=$1 val=$2
    : >"$tmpdir/in"

    # Value-less, IPv4: the token is an input path that cannot be opened.
    run_bounded "$tmpdir/t" "$opt" || return 1
    local rc
    rc=$(cat "$tmpdir/t.rc")
    if [ "$rc" -ne 1 ]; then
        fail "$opt (trailing): exit $rc, expected 1 (124 = hung, 134/126+ = died on the memory cap)"
        cat "$tmpdir/t.err"
        return 1
    fi
    if [ -s "$tmpdir/t.out" ]; then
        fail "$opt (trailing): stdout must be empty, got: $(cat "$tmpdir/t.out")"
        return 1
    fi
    if ! cmp -s "$tmpdir/t.err" <(printf 'iprange: %s - No such file or directory\niprange: Cannot load ipset: %s\n' "$opt" "$opt"); then
        fail "$opt (trailing): stderr is not the C load failure"
        cat -v "$tmpdir/t.err"
        return 1
    fi
    echo "# OK: $opt without a value exits 1 (IPv4, bounded by ulimit -v and timeout)"

    # Value-less, IPv6: the token is a discarded flag, stdin is the input.
    printf '::/0\n' >"$tmpdir/in"
    run_bounded "$tmpdir/t" -6 "$opt" || return 1
    rc=$(cat "$tmpdir/t.rc")
    if [ "$rc" -ne 0 ] || ! cmp -s "$tmpdir/t.out" <(printf '::/0\n'); then
        fail "-6 $opt (trailing): exit $rc stdout $(cat -v "$tmpdir/t.out"), expected 0 and ::/0 from stdin"
        cat -v "$tmpdir/t.err"
        return 1
    fi
    echo "# OK: -6 $opt without a value is skipped and stdin loads (exit 0)"

    # Control: the same option with its value behaves normally.
    : >"$tmpdir/in"
    run_bounded "$tmpdir/v" "$opt" "$val" "$tmpdir/in4.txt" || return 1
    rc=$(cat "$tmpdir/v.rc")
    if [ "$rc" -ne 0 ]; then
        fail "$opt $val (control): exit $rc, expected 0 ($(cat -v "$tmpdir/v.err"))"
        return 1
    fi
    echo "# OK: $opt with its value still works (exit 0)"

    if [ "$reference_used" -eq 1 ]; then
        for argv in "$opt" "-6 $opt" "$opt $val $tmpdir/in4.txt"; do
            : >"$tmpdir/in"
            # shellcheck disable=SC2086
            ( ulimit -v "$MEM_KIB"; timeout "$BOUND" "$REFERENCE" $argv <"$tmpdir/in" >"$tmpdir/r.out" 2>"$tmpdir/r.err"; echo $? >"$tmpdir/r.rc" )
            local rrc
            rrc=$(cat "$tmpdir/r.rc")
            # The timing line of -v is not compared (none of these uses -v).
            # shellcheck disable=SC2086
            run_bounded "$tmpdir/e" $argv || return 1
            local erc
            erc=$(cat "$tmpdir/e.rc")
            if [ "$rrc" -ne "$erc" ] || ! cmp -s "$tmpdir/r.out" "$tmpdir/e.out" || ! cmp -s "$tmpdir/r.err" "$tmpdir/e.err"; then
                fail "$opt: engine and $REFERENCE disagree on [$argv] (engine exit $erc vs reference $rrc)"
                echo "#   reference stderr: $(cat -v "$tmpdir/r.err")"
                echo "#   engine stderr:    $(cat -v "$tmpdir/e.err")"
                return 1
            fi
        done
        echo "# OK: $opt matches $REFERENCE on the trailing and valued forms"
    fi
    return 0
}

# Every value-taking option and its alias, with a value valid in both
# families (src/iprange.c:515-706, src/iprange6_main.c:112-170).
for opt in --min-prefix --prefixes --default-prefix -p \
           --ipset-reduce --reduce-factor --ipset-reduce-entries --reduce-entries \
           --print-prefix --print-prefix-ips --print-prefix-nets \
           --print-suffix --print-suffix-ips --print-suffix-nets \
           --dns-threads; do
    case $opt in
        --min-prefix) val=8 ;;
        --prefixes) val=32 ;;
        --default-prefix|-p) val=24 ;;
        --ipset-reduce|--reduce-factor) val=10 ;;
        --ipset-reduce-entries|--reduce-entries) val=1 ;;
        --print-prefix|--print-prefix-ips|--print-prefix-nets) val=P ;;
        --print-suffix|--print-suffix-ips|--print-suffix-nets) val=S ;;
        --dns-threads) val=2 ;;
    esac
    check_option "$opt" "$val" || exit 1
done

# `as` is not dash-prefixed, so the trailing keyword is an input path in
# both families (its C branch also requires a following argument).
for family in "" "-6"; do
    : >"$tmpdir/in"
    # shellcheck disable=SC2086
    run_bounded "$tmpdir/a" $family as || exit 1
    rc=$(cat "$tmpdir/a.rc")
    if [ "$rc" -ne 1 ] || ! cmp -s "$tmpdir/a.err" <(printf 'iprange: as - No such file or directory\niprange: Cannot load ipset: as\n'); then
        echo "# ERROR: [$family as] exited $rc with stderr $(cat -v "$tmpdir/a.err")"
        exit 1
    fi
    echo "# OK: trailing 'as'${family:+ after $family} is an input path (exit 1)"
done

echo "# OK: 15 value-taking options and 'as' terminate cleanly without a value"
exit 0
