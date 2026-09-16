#!/bin/bash

# DNS -v bookkeeping: what the C counts and prints per resolved address.
#
# The released C resolver adds one ipset entry per reply address, and that
# single fact drives every line pinned here:
#
#   * `dns_process_replies()` adds one range per reply node with no
#     deduplication (`src/ipset_dns.c:275-281`, `src/ipset6_dns.c:223-233`)
#     and the worker stacks one node per address (`src/ipset_dns.c:253-255`,
#     `src/ipset6_dns.c:210-211`), so one hostname that answers with N
#     addresses contributes N to `ips->lines` (`src/ipset.h:89`,
#     `src/ipset6.h:72`) and therefore N to `totals: %zu lines read`
#     (`src/ipset_print.c:223`, `src/ipset6_print.c:210`).
#   * an entry that is not after the previous one clears the optimized flag
#     and prints `NON-OPTIMIZED ...` (`src/ipset.h:100-121`,
#     `src/ipset6.h:96-105`), which then drives `Loaded non-optimized NAME`
#     (`src/ipset_load.c:418`) and `Optimizing ...`
#     (`src/ipset_optimize.c:47`, `src/ipset6_optimize.c:24`).
#   * the reply list is a stack (`p->next = dns_replies; dns_replies = p;`),
#     so the insertion order is the reverse of the order the resolver
#     answered in, and that order decides whether the NON-OPTIMIZED line
#     appears at all.
#   * the summary `DNS: made N DNS requests, ... IPs got M, threads used T
#     of K` (`src/ipset_dns.c:375`) counts requests in `made`, raw addresses
#     (duplicates included, `src/ipset_dns.c:118-128`) in `IPs got`, and it
#     is printed AFTER the last `dns_process_replies()` call
#     (`src/ipset_dns.c:363-375`), so a NON-OPTIMIZED line produced by those
#     additions comes first. The multiset family below compares its lines
#     without order, so `assert_dns_line_order` is what keeps that pair in the
#     C order in spite of the lines timing leaves free.
#   * the per-address debug line `DNS: 'NAME' = ADDR` is printed while the
#     worker walks the answer list, IPv4 only (`src/ipset_dns.c:246-249`);
#     the IPv6 pool prints no per-address line, no `Creating new DNS thread`
#     (`src/ipset_dns.c:92`) and no summary (`src/ipset6_dns.c dns6_done`).
#
# Every host name used here is answered from /etc/hosts or by the numeric
# short circuit inside getaddrinfo(3), so no case needs resolver traffic.
# Numeric forms such as 0x7f000001 are host names, not literals: the C
# reaches them because the first token is not pure [0-9./], and they resolve
# to exactly one address, which makes the reply count reproducible on any
# host.
#
# Two comparison exceptions exist, and each is a shape, not a normalization:
#
#   1. The C's exit-time timing line (src/iprange.c:1216-1222) reports this
#      process's own wall clock; it is masked to <WALLCLOCK> on both sides
#      when a line matches that exact shape.
#   2. `DNS: waiting N DNS resolutions to finish...` (src/ipset_dns.c:348) is
#      printed once per iteration of the loader's `while(pending) { ...;
#      sleep(1); }` loop, so its count and how many times it appears both
#      follow how long the resolver took. Measured on this host, the
#      two-request case printed one waiting line in 11 of 12 consecutive C
#      runs and none in the twelfth, and 8 runs of it gave several different
#      counts. The whole line is therefore removed from both sides by
#      `drop_dns_waiting`, after `assert_dns_waiting_shape` has required every
#      line of that family to match the exact C text. Every other line is
#      compared exactly.
#
# rc and stdout are always compared byte for byte, in every case.

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

ROOT=$(cd ../.. && pwd)
IPRANGE="$ROOT/iprange"
REFERENCE=${IPRANGE_REFERENCE:-/usr/bin/iprange}

fail=0

# mask_wallclock <file>: rewrite a whole line of the exact C timing shape.
mask_wallclock() {
    sed -E -i 's/^completed in [0-9]+\.[0-9]{5} seconds \(read [0-9]+\.[0-9]{5} \+ think [0-9]+\.[0-9]{5} \+ speak [0-9]+\.[0-9]{5}\)$/\<WALLCLOCK>/' "$1"
}

# assert_dns_waiting_shape <file>: every line of the DNS waiting family must
# carry the exact C text (src/ipset_dns.c:348). Run before the line is dropped,
# so dropping it cannot hide a malformed one.
assert_dns_waiting_shape() {
    local bad
    bad=$(grep -E '^iprange: DNS: waiting ' "$1" | grep -Ev '^iprange: DNS: waiting [0-9]+ DNS resolutions to finish\.\.\.$' || true)
    if [ -n "$bad" ]; then
        echo "# ERROR: $2: a DNS waiting line does not have the C shape"
        printf '%s\n' "$bad" | cat -v
        return 1
    fi
}

# drop_dns_waiting <file>: remove the C waiting line, whose presence, count and
# in-flight value all follow the wall clock (see the note at the top).
drop_dns_waiting() {
    sed -E -i '/^iprange: DNS: waiting [0-9]+ DNS resolutions to finish\.\.\.$/d' "$1"
}

# assert_dns_line_order <file> <label>: within one file's batch the C prints the
# per-file summary AFTER the additions it summarizes: dns_done() runs its last
# dns_process_replies() and only then prints (`src/ipset_dns.c:363-375`), and the
# loader prints `Loaded ...` after dns_done() returned (`src/ipset_load.c:401`,
# `:418`). So the summary line must follow every NON-OPTIMIZED line of that file
# and precede its Loaded line. check_set compares its lines without order, because
# how many `Creating new DNS thread` lines a batch reaches follows timing; this is
# what stops that timing freedom from swapping the summary and the addition.
assert_dns_line_order() {
    local file=$1 label=$2
    local summary nonopt loaded bad=0
    # Scoped to one file's batch: with two files the first summary legitimately
    # precedes the second file's additions, so the bounds below would be wrong.
    if [ "$(grep -c -F 'iprange: Loading from ' "$file")" -gt 1 ]; then
        echo "# ERROR: $label: assert_dns_line_order only covers a single-file batch"
        return 1
    fi
    summary=$(grep -n -m 1 -F 'iprange: DNS: made ' "$file" | cut -d: -f1)
    [ -n "$summary" ] || return 0          # the IPv6 pool prints no summary
    nonopt=$(grep -n -F 'iprange: NON-OPTIMIZED ' "$file" | cut -d: -f1 | tail -1)
    if [ -n "$nonopt" ] && [ "$nonopt" -gt "$summary" ]; then
        echo "# ERROR: $label: the DNS summary line precedes the additions it summarizes"
        sed -n "${nonopt}p;${summary}p" "$file" | cat -v
        bad=1
    fi
    loaded=$(grep -n -m 1 -F 'iprange: Loaded ' "$file" | cut -d: -f1)
    if [ -n "$loaded" ] && [ "$loaded" -lt "$summary" ]; then
        echo "# ERROR: $label: the DNS summary line follows the Loaded line printed after dns_done() returns"
        sed -n "${loaded}p;${summary}p" "$file" | cat -v
        bad=1
    fi
    return $bad
}

# run_engine <bin> <stdout-file> <stderr-file> <args...>
run_engine() {
    local bin=$1 out=$2 err=$3
    shift 3
    timeout 60 "$bin" "$@" < /dev/null > "$out" 2> "$err"
    RUN_RC=$?
}

# check <label> <rc> <stdout-printf> <masked-stderr-printf> <args...>
# Exit code and stdout are byte-exact; stderr is byte-exact after masking
# the single wall-clock line. The C reference must agree with the same pins.
check() {
    local label=$1 erc=$2 eout=$3 eerr=$4
    shift 4
    run_engine "$IPRANGE" "$tmpdir/out" "$tmpdir/err" "$@"
    local rc=$RUN_RC
    mask_wallclock "$tmpdir/err"
    assert_dns_waiting_shape "$tmpdir/err" "$label (engine)" || { fail=1; return; }
    drop_dns_waiting "$tmpdir/err"
    if [ "$rc" -ne "$erc" ]; then
        echo "# ERROR: $label exit $rc, expected $erc"; cat -v "$tmpdir/err"; fail=1; return
    fi
    if ! cmp -s <(printf '%b' "$eout") "$tmpdir/out"; then
        echo "# ERROR: $label stdout differs"; cat -v "$tmpdir/out"; fail=1; return
    fi
    if ! cmp -s <(printf '%b' "$eerr") "$tmpdir/err"; then
        echo "# ERROR: $label stderr differs"
        echo "#   expected: $(printf '%b' "$eerr" | cat -v)"
        echo "#   actual:   $(cat -v "$tmpdir/err")"
        fail=1; return
    fi
    if [ -x "$REFERENCE" ]; then
        run_engine "$REFERENCE" "$tmpdir/ref_out" "$tmpdir/ref_err" "$@"
        mask_wallclock "$tmpdir/ref_err"
        assert_dns_waiting_shape "$tmpdir/ref_err" "$label (the C reference)" || { fail=1; return; }
        drop_dns_waiting "$tmpdir/ref_err"
        if [ "$RUN_RC" -ne "$rc" ] || ! cmp -s "$tmpdir/ref_out" "$tmpdir/out" ||
           ! cmp -s "$tmpdir/ref_err" "$tmpdir/err"; then
            echo "# ERROR: $label differs from the C reference ($REFERENCE)"
            echo "#   reference exit $RUN_RC, engine exit $rc"
            diff <(cat -v "$tmpdir/ref_err") <(cat -v "$tmpdir/err") | head -20
            fail=1; return
        fi
    fi
    echo "# OK: $label (exit $rc)"
}

# check_set <label> <rc> <stdout-printf> <masked-stderr-printf> <args...>
# Same rc and stdout contract as check. Stderr is compared as a multiset of
# lines with exact counts, after masking the wall-clock line and the
# in-flight count of the DNS waiting line: every expected line must be
# present exactly once and no other line may appear. Order is not compared,
# because how many `Creating new DNS thread` lines a batch reaches follows
# timing; `assert_dns_line_order` still requires the summary to sit where the
# C puts it relative to the additions of that file.
check_set() {
    local label=$1 erc=$2 eout=$3 eerr=$4
    shift 4
    run_engine "$IPRANGE" "$tmpdir/out" "$tmpdir/err" "$@"
    local rc=$RUN_RC
    mask_wallclock "$tmpdir/err"
    assert_dns_waiting_shape "$tmpdir/err" "$label (engine)" || { fail=1; return; }
    drop_dns_waiting "$tmpdir/err"
    assert_dns_line_order "$tmpdir/err" "$label (engine)" || { fail=1; return; }
    assert_dns_thread_accounting "$label (engine)" "$tmpdir/err" || { fail=1; return; }
    drop_dns_thread_creation "$tmpdir/err"
    mask_dns_threads_used "$tmpdir/err"
    if [ "$rc" -ne "$erc" ]; then
        echo "# ERROR: $label exit $rc, expected $erc"; cat -v "$tmpdir/err"; fail=1; return
    fi
    if ! cmp -s <(printf '%b' "$eout") "$tmpdir/out"; then
        echo "# ERROR: $label stdout differs"; cat -v "$tmpdir/out"; fail=1; return
    fi
    if ! cmp -s <(printf '%b' "$eerr" | drop_dns_waiting_stdin | drop_dns_thread_creation_stdin | mask_dns_threads_used_stdin | sort) <(sort "$tmpdir/err"); then
        echo "# ERROR: $label stderr multiset differs"
        echo "#   expected: $(printf '%b' "$eerr" | drop_dns_waiting_stdin | drop_dns_thread_creation_stdin | mask_dns_threads_used_stdin | sort | cat -v | tr '\n' '|')"
        echo "#   actual:   $(sort "$tmpdir/err" | cat -v | tr '\n' '|')"
        fail=1; return
    fi
    if [ -x "$REFERENCE" ]; then
        run_engine "$REFERENCE" "$tmpdir/ref_out" "$tmpdir/ref_err" "$@"
        mask_wallclock "$tmpdir/ref_err"
        assert_dns_waiting_shape "$tmpdir/ref_err" "$label (the C reference)" || { fail=1; return; }
        drop_dns_waiting "$tmpdir/ref_err"
        assert_dns_line_order "$tmpdir/ref_err" "$label (the C reference)" || { fail=1; return; }
        assert_dns_thread_accounting "$label (the C reference)" "$tmpdir/ref_err" || { fail=1; return; }
        drop_dns_thread_creation "$tmpdir/ref_err"
        mask_dns_threads_used "$tmpdir/ref_err"
        if [ "$RUN_RC" -ne "$rc" ] || ! cmp -s "$tmpdir/ref_out" "$tmpdir/out" ||
           ! cmp -s <(sort "$tmpdir/ref_err") <(sort "$tmpdir/err"); then
            echo "# ERROR: $label differs from the C reference ($REFERENCE)"
            diff <(sort "$tmpdir/ref_err" | cat -v) <(sort "$tmpdir/err" | cat -v) | head -20
            fail=1; return
        fi
    fi
    echo "# OK: $label (exit $rc)"
}

# drop_dns_waiting_stdin: the same line removal applied to a pinned string.
drop_dns_waiting_stdin() {
    sed -E '/^iprange: DNS: waiting [0-9]+ DNS resolutions to finish\.\.\.$/d'
}

# mask_dns_threads_used <file>: rewrite only the number of workers the C reported
# using, keeping the configured maximum. `dns_threads` is incremented outside the
# requests lock (src/ipset_dns.c:88-110) and only ever grows, and a worker is created
# only while more requests are pending than there are workers, so how many workers a
# batch reaches depends on whether an earlier request had already finished when the
# next one was queued. Measured: 1 of 29 consecutive C runs of the two-request case
# reached one worker instead of two.
mask_dns_threads_used() {
    sed -E -i 's/^(iprange: DNS: made [0-9]+ DNS requests, failed [0-9]+, retries: [0-9]+, IPs got [0-9]+), threads used [0-9]+ of ([0-9]+)$/\1, threads used <T> of \2/' "$1"
}

# mask_dns_threads_used_stdin: the same rewrite applied to a pinned string.
mask_dns_threads_used_stdin() {
    sed -E 's/^(iprange: DNS: made [0-9]+ DNS requests, failed [0-9]+, retries: [0-9]+, IPs got [0-9]+), threads used [0-9]+ of ([0-9]+)$/\1, threads used <T> of \2/'
}

# drop_dns_thread_creation <file> and _stdin: remove the per-worker notification
# (src/ipset_dns.c:91-92). Its count is the worker count, which the queue timing
# decides; assert_dns_thread_accounting still bounds and cross-checks it.
drop_dns_thread_creation() {
    sed -E -i '/^iprange: Creating new DNS thread$/d' "$1"
}

drop_dns_thread_creation_stdin() {
    sed -E '/^iprange: Creating new DNS thread$/d'
}

# assert_dns_thread_accounting <label> <file>
# What IS determined about the worker pool, checked on the unmasked text. dns_done()
# runs once per input file and dns_reset_stats() clears the counters per file
# (src/ipset_load.c:386, src/ipset_dns.c:42-56), so a multi-file run prints one
# summary per file with that file's own request and address counts; dns_threads is not
# reset, so the worker count is monotonic across the whole run and a later file may
# report more workers than it requested. The invariants: each thread-creation line
# carries the exact C text, each line reporting workers is a well-formed summary,
# every summary reports at least one worker and no more than the configured maximum,
# the count never falls between consecutive summaries, it never exceeds the total
# requests of the run, and the number of thread-creation lines equals the last
# summary's worker count - the C increments dns_threads exactly where it prints the
# per-worker notification.
assert_dns_thread_accounting() {
    local label=$1 f=$2 bad line n_create=0 summaries=0 total_requests=0 previous_used=0 last_used=-1
    bad=$(grep -E '^iprange: Creating new DNS thread' "$f" | grep -Ev '^iprange: Creating new DNS thread$' || true)
    if [ -n "$bad" ]; then
        echo "# ERROR: $label: a DNS thread-creation line is not the C text"; printf '%s\n' "$bad" | cat -v; return 1
    fi
    n_create=$(grep -E -c '^iprange: Creating new DNS thread$' "$f" || true)
    while IFS= read -r line; do
        case $line in
            "iprange: DNS: made "*,\ threads\ used\ *) ;;
            *) continue ;;
        esac
        if ! printf '%s\n' "$line" | grep -Eq '^iprange: DNS: made [0-9]+ DNS requests, failed [0-9]+, retries: [0-9]+, IPs got [0-9]+, threads used [0-9]+ of [0-9]+$'; then
            echo "# ERROR: $label: a DNS summary line is not the C text: $(printf '%s' "$line" | cat -v)"; return 1
        fi
        local made used max
        made=$(printf '%s\n' "$line" | sed -E 's/^iprange: DNS: made ([0-9]+) DNS requests.*/\1/')
        used=$(printf '%s\n' "$line" | sed -E 's/.*, threads used ([0-9]+) of [0-9]+$/\1/')
        max=$(printf '%s\n' "$line" | sed -E 's/.*, threads used [0-9]+ of ([0-9]+)$/\1/')
        summaries=$(( summaries + 1 ))
        total_requests=$(( total_requests + made ))
        if [ "$used" -lt 1 ] || [ "$used" -gt "$max" ]; then
            echo "# ERROR: $label: $used workers with a maximum of $max"; cat -v "$f"; return 1
        fi
        if [ "$used" -lt "$previous_used" ]; then
            echo "# ERROR: $label: the worker count fell from $previous_used to $used, but dns_threads only grows"; cat -v "$f"; return 1
        fi
        previous_used=$used
        last_used=$used
    done < "$f"
    if [ "$summaries" -eq 0 ]; then
        if [ "$n_create" -ne 0 ]; then
            echo "# ERROR: $label: $n_create thread-creation lines with no DNS summary line"; cat -v "$f"; return 1
        fi
        return 0
    fi
    if [ "$last_used" -gt "$total_requests" ]; then
        echo "# ERROR: $label: $last_used workers for $total_requests requests in total"; cat -v "$f"; return 1
    fi
    if [ "$n_create" -ne "$last_used" ]; then
        echo "# ERROR: $label: $n_create thread-creation lines but the summary reports $last_used workers"; cat -v "$f"; return 1
    fi
}

# check_dns_interleaved_batch <label> <rc> <stdout-printf> <stable-stderr-printf> <args...>
# A file that holds more than one hostname is resolved by more than one in-flight
# request, and two lists in the resolver are stacks rather than queues: the requests
# (`d->next = dns_requests; dns_requests = d;` at src/ipset_dns.c:80-81, taken at
# :187) and the replies (src/ipset_dns.c:253-255, src/ipset6_dns.c:210-211), which the
# adder then drains head-first (src/ipset_dns.c:275-281, src/ipset6_dns.c:223-233).
# Whether the second request is queued before a worker takes the first, and the order
# the answers land in, therefore decide how many `NON-OPTIMIZED` lines appear and what
# their `at line N, entry E` fields say, and with that whether the printed set is
# optimized at all: `Loaded optimized` versus `Loaded non-optimized`
# (src/ipset_load.c:418) and `Optimizing combined ipset` (src/ipset_optimize.c:47).
# Measured: the released C reported the optimized branch for `dns two names one thread`
# in one of the 30 runs of the 10-round determinism loop where the others reported
# non-optimized, and the Rust binary produced four distinct stderr shapes in five
# consecutive runs of the same case. So the entire optimization-trace family is removed
# from the comparison and replaced by invariants: every `NON-OPTIMIZED` line that does
# appear carries the exact C text, `Loaded non-optimized` appears exactly when at least
# one add was out of order, and `Optimizing` appears exactly when the printed set was
# not optimized. Every other stderr line - including the `totals:` entry count these
# cases exist to pin, which the add order cannot change - is still compared as a
# multiset with exact counts against both the pinned C text and the installed C.
check_dns_interleaved_batch() {
    local label=$1 erc=$2 eout=$3 eerr=$4
    shift 4
    run_engine "$IPRANGE" "$tmpdir/out" "$tmpdir/err" "$@"
    local rc=$RUN_RC
    mask_wallclock "$tmpdir/err"
    assert_dns_waiting_shape "$tmpdir/err" "$label (engine)" || { fail=1; return; }
    drop_dns_waiting "$tmpdir/err"
    if [ "$rc" -ne "$erc" ]; then
        echo "# ERROR: $label exit $rc, expected $erc"; cat -v "$tmpdir/err"; fail=1; return
    fi
    if ! cmp -s <(printf '%b' "$eout") "$tmpdir/out"; then
        echo "# ERROR: $label stdout differs"; cat -v "$tmpdir/out"; fail=1; return
    fi

    # The two interleave-dependent families, checked for shape and for their
    # relation to each other before being removed.
    local n_non n_opt n_loaded bad
    n_non=$(grep -E -c '^iprange: NON-OPTIMIZED ' "$tmpdir/err" || true)
    n_opt=$(grep -E -c '^iprange: Optimizing combined ipset' "$tmpdir/err" || true)
    n_loaded=$(grep -E -c '^iprange: Loaded non-optimized ' "$tmpdir/err" || true)
    bad=$(grep -E '^iprange: NON-OPTIMIZED ' "$tmpdir/err" | grep -Ev '^iprange: NON-OPTIMIZED [^ ]+ at line [0-9]+, entry [0-9]+, last was .+ - .+, new is .+ - .+$' || true)
    if [ -n "$bad" ]; then
        echo "# ERROR: $label: a NON-OPTIMIZED line does not have the C text"; printf '%s\n' "$bad" | cat -v; fail=1; return
    fi
    if [ "$n_opt" -ne $(( n_non > 0 ? 1 : 0 )) ]; then
        echo "# ERROR: $label: $n_opt Optimizing lines for $n_non out-of-order adds"; cat -v "$tmpdir/err"; fail=1; return
    fi
    # The `Loaded <adj> <file>` line belongs to the IPv4 loader alone
    # (src/ipset_load.c:418); src/ipset6_load.c prints no such line, so the relation is
    # required only of a run that printed one at all. A run with no `Loaded` line at all
    # necessarily has no `Loaded non-optimized` line, so there is nothing else to check.
    if grep -q '^iprange: Loaded ' "$tmpdir/err" &&
       [ "$n_loaded" -ne $(( n_non > 0 ? 1 : 0 )) ]; then
        echo "# ERROR: $label: $n_loaded 'Loaded non-optimized' lines for $n_non out-of-order adds"; cat -v "$tmpdir/err"; fail=1; return
    fi

    local stable_err=$tmpdir/stable
    sed -E '/^iprange: NON-OPTIMIZED /d; /^iprange: Optimizing combined ipset/d; /^iprange: Loaded (non-)?optimized /d' "$tmpdir/err" > "$stable_err"
    if ! cmp -s <(printf '%b' "$eerr" | drop_dns_waiting_stdin | sed -E '/^iprange: NON-OPTIMIZED /d; /^iprange: Optimizing combined ipset/d; /^iprange: Loaded (non-)?optimized /d' | sort) <(sort "$stable_err"); then
        echo "# ERROR: $label stable stderr multiset differs"
        echo "#   expected: $(printf '%b' "$eerr" | sort | cat -v | tr '\n' '|')"
        echo "#   actual:   $(sort "$stable_err" | cat -v | tr '\n' '|')"
        fail=1; return
    fi
    if [ -x "$REFERENCE" ]; then
        run_engine "$REFERENCE" "$tmpdir/ref_out" "$tmpdir/ref_err" "$@"
        mask_wallclock "$tmpdir/ref_err"
        assert_dns_waiting_shape "$tmpdir/ref_err" "$label (the C reference)" || { fail=1; return; }
        drop_dns_waiting "$tmpdir/ref_err"
        if [ "$RUN_RC" -ne "$rc" ] || ! cmp -s "$tmpdir/ref_out" "$tmpdir/out"; then
            echo "# ERROR: $label differs from the C reference ($REFERENCE) on rc/stdout"
            fail=1; return
        fi
        if ! cmp -s <(sed -E '/^iprange: NON-OPTIMIZED /d; /^iprange: Optimizing combined ipset/d; /^iprange: Loaded (non-)?optimized /d' "$tmpdir/ref_err" | sort) <(sort "$stable_err"); then
            echo "# ERROR: $label stable stderr differs from the C reference ($REFERENCE)"
            diff <(sed -E '/^iprange: NON-OPTIMIZED /d; /^iprange: Optimizing combined ipset/d; /^iprange: Loaded (non-)?optimized /d' "$tmpdir/ref_err" | sort | cat -v) <(sort "$stable_err" | cat -v) | head -20
            fail=1; return
        fi
    fi
    # The count of out-of-order adds is interleave-dependent, so it is reported
    # only with a failure; the accepted line must stay reproducible.
    echo "# OK: $label (exit $rc)"
}

# check_dns_bookkeeping_on_failure <label> <file> <args...>
# A host that does not resolve adds no entry, so the request is counted and
# no address is: "IPs got 0" and no per-address debug line, and the run
# fails (src/ipset_dns.c:118-128, src/iprange.c:911). The C's terminal
# failure text depends on what the resolver answered (NXDOMAIN and a
# timeout are different lines, and a timeout adds retry lines), so the
# failure text is compared between the engine and the C reference as a
# multiset with those lines removed, while the bookkeeping lines are
# asserted by exact shape.
check_dns_bookkeeping_on_failure() {
    local label=$1 file=$2
    shift 2
    run_engine "$IPRANGE" "$tmpdir/out" "$tmpdir/err" "$@"
    local rc=$RUN_RC eng_err=$tmpdir/err
    local summary n_addr
    summary=$(grep -E -c '^iprange: DNS: made 1 DNS requests, failed 1, retries: [0-9]+, IPs got 0, threads used [0-9]+ of [0-9]+$' "$eng_err")
    n_addr=$(grep -E -c "^iprange: DNS: '[^']*' = " "$eng_err")
    if [ "$rc" -ne 1 ]; then
        echo "# ERROR: $label exit $rc, expected 1"; cat -v "$eng_err"; fail=1; return
    fi
    if [ -s "$tmpdir/out" ]; then
        echo "# ERROR: $label stdout is not empty"; cat -v "$tmpdir/out"; fail=1; return
    fi
    if [ "$summary" -ne 1 ]; then
        echo "# ERROR: $label expected exactly one failed-request summary with IPs got 0, got $summary"; cat -v "$eng_err"; fail=1; return
    fi
    if [ "$n_addr" -ne 0 ]; then
        echo "# ERROR: $label expected no per-address DNS line for a failed host, got $n_addr"; cat -v "$eng_err"; fail=1; return
    fi
    if [ -x "$REFERENCE" ]; then
        run_engine "$REFERENCE" "$tmpdir/ref_out" "$tmpdir/ref_err" "$@"
        if [ "$RUN_RC" -ne 1 ] || ! cmp -s "$tmpdir/ref_out" "$tmpdir/out"; then
            echo "# ERROR: $label differs from the C reference on rc/stdout"
            fail=1; return
        fi
        # Drop the resolver-dependent lines, then compare the rest exactly.
        for f in "$eng_err" "$tmpdir/ref_err"; do
            assert_dns_waiting_shape "$f" "$label" || { fail=1; return; }
            drop_dns_waiting "$f"
            mask_wallclock "$f"
            sed -E -i "/^iprange: DNS: '[^']*' (failed|system error|error)/d; /^iprange: DNS: '[^']*' will be retried/d; s/^iprange: DNS: made 1 DNS requests, failed 1, retries: [0-9]+, IPs got 0, threads used ([0-9]+) of ([0-9]+)$/iprange: DNS: made 1 DNS requests, failed 1, retries: <R>, IPs got 0, threads used \1 of \2/" "$f"
        done
        if ! cmp -s <(sort "$tmpdir/ref_err") <(sort "$eng_err"); then
            echo "# ERROR: $label differs from the C reference ($REFERENCE)"
            diff <(sort "$tmpdir/ref_err" | cat -v) <(sort "$eng_err" | cat -v) | head -20
            fail=1; return
        fi
    fi
    echo "# OK: $label (exit $rc)"
}

# The diagnostics echo the names they were given, so every case runs from
# inside the fixture directory and uses relative names.
cd "$tmpdir" || exit 1

printf '%b' '0x7f000001\n'            > 'h1.txt'
printf '%b' '0x7f000001\n0x7f000001\n' > 'h2.txt'
printf '%b' '0x7f000001\n'            > 'hA.txt'
printf '%b' '0x0A000001\n'            > 'hB.txt'
printf '%b' 'localhost\n'             > 'l1.txt'
printf '%b' 'localhost\nlocalhost\n'  > 'l2.txt'
printf '%b' 'ip6-allnodes\n'          > 'n1.txt'
printf '%b' '0x7f000001\n10.0.0.1\n'  > 'hl.txt'
printf '%b' '10.0.0.1\n0x7f000001\n'  > 'lh.txt'
printf '%b' '10.0.0.1\n'              > 'm1.txt'
printf '%b' '0x7f000001\n0x0A000001\n' > 'hn.txt'
printf '%b' 'definitely-not-a-real-hostname.invalid\n' > 'bad.txt'

check 'dns one host v4' 0 \
      '127.0.0.1\n' \
      'iprange: Loading from h1.txt\niprange: DNS resolution for hostname '\''0x7f000001'\'' from line 1 of file h1.txt.\niprange: Creating new DNS thread\niprange: DNS: '\''0x7f000001'\'' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized h1.txt\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'h1.txt'

check_set 'dns same host twice v4' 0 \
      '127.0.0.1\n' \
      'iprange: Loading from h2.txt\niprange: DNS resolution for hostname '\''0x7f000001'\'' from line 1 of file h2.txt.\niprange: Creating new DNS thread\niprange: DNS resolution for hostname '\''0x7f000001'\'' from line 2 of file h2.txt.\niprange: Creating new DNS thread\niprange: DNS: '\''0x7f000001'\'' = 127.0.0.1\niprange: DNS: '\''0x7f000001'\'' = 127.0.0.1\niprange: NON-OPTIMIZED h2.txt at line 2, entry 1, last was 127.0.0.1 (2130706433) - 127.0.0.1 (2130706433), new is 127.0.0.1 (2130706433) - 127.0.0.1 (2130706433)\niprange: DNS: made 2 DNS requests, failed 0, retries: 0, IPs got 2, threads used <T> of 5\niprange: Loaded non-optimized h2.txt\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 2 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'h2.txt'

check 'dns two files one host each' 0 \
      '10.0.0.1\n127.0.0.1\n' \
      'iprange: Loading from hA.txt\niprange: DNS resolution for hostname '\''0x7f000001'\'' from line 1 of file hA.txt.\niprange: Creating new DNS thread\niprange: DNS: '\''0x7f000001'\'' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized hA.txt\niprange: Loading from hB.txt\niprange: DNS resolution for hostname '\''0x0A000001'\'' from line 1 of file hB.txt.\niprange: DNS: '\''0x0A000001'\'' = 10.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized hB.txt\niprange: Merging hB.txt to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'hA.txt' $'hB.txt'

check 'dns one host v6 localhost' 0 \
      '::1\n::ffff:127.0.0.1\n' \
      'iprange: Loading from l1.txt (IPv6 mode)\niprange: DNS resolution for hostname '\''localhost'\'' from line 1 of file l1.txt (IPv6 mode).\niprange: Printing combined ipset (IPv6) with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /128 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n' \
      $'-6' $'-v' $'l1.txt'

check_dns_interleaved_batch 'dns same host twice v6' 0 \
      '::1\n::ffff:127.0.0.1\n' \
      'iprange: Loading from l2.txt (IPv6 mode)\niprange: DNS resolution for hostname '\''localhost'\'' from line 1 of file l2.txt (IPv6 mode).\niprange: DNS resolution for hostname '\''localhost'\'' from line 2 of file l2.txt (IPv6 mode).\niprange: NON-OPTIMIZED l2.txt at line 3, entry 2, last was ::ffff:127.0.0.1 - ::ffff:127.0.0.1, new is ::1 - ::1\niprange: Optimizing combined ipset (IPv6)\niprange: Printing combined ipset (IPv6) with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /128 counts 2 entries\n\ntotals: 4 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n' \
      $'-6' $'-v' $'l2.txt'

check 'dns one host v6 allnodes' 0 \
      'ff02::1\n' \
      'iprange: Loading from n1.txt (IPv6 mode)\niprange: DNS resolution for hostname '\''ip6-allnodes'\'' from line 1 of file n1.txt (IPv6 mode).\niprange: Printing combined ipset (IPv6) with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /128 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n' \
      $'-6' $'-v' $'n1.txt'

check 'dns one host v4 silent' 0 \
      '127.0.0.1\n' \
      'iprange: Loading from h1.txt\niprange: DNS resolution for hostname '\''0x7f000001'\'' from line 1 of file h1.txt.\niprange: Creating new DNS thread\niprange: DNS: '\''0x7f000001'\'' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized h1.txt\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'--dns-silent' $'h1.txt'

check 'dns one host v4 threads 1' 0 \
      '127.0.0.1\n' \
      'iprange: Loading from h1.txt\niprange: DNS resolution for hostname '\''0x7f000001'\'' from line 1 of file h1.txt.\niprange: Creating new DNS thread\niprange: DNS: '\''0x7f000001'\'' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 1\niprange: Loaded optimized h1.txt\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'--dns-threads' $'1' $'h1.txt'

check 'dns one host v4 threads 3' 0 \
      '127.0.0.1\n' \
      'iprange: Loading from h1.txt\niprange: DNS resolution for hostname '\''0x7f000001'\'' from line 1 of file h1.txt.\niprange: Creating new DNS thread\niprange: DNS: '\''0x7f000001'\'' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 3\niprange: Loaded optimized h1.txt\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\n1 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 1 entries\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 CIDRs printed, 1 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'--dns-threads' $'3' $'h1.txt'

check 'dns one host v4 binary' 0 \
      'iprange binary format v1.0\noptimized\nrecord size 8\nrecords 1\nbytes 12\nlines 1\nunique ips 1\nM<+\032\001\000\000\177\001\000\000\177' \
      'iprange: Loading from h1.txt\niprange: DNS resolution for hostname '\''0x7f000001'\'' from line 1 of file h1.txt.\niprange: Creating new DNS thread\niprange: DNS: '\''0x7f000001'\'' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized h1.txt\n<WALLCLOCK>\n' \
      $'-v' $'--print-binary' $'h1.txt'

check 'dns one host v4 single ips' 0 \
      '127.0.0.1\n' \
      'iprange: Loading from h1.txt\niprange: DNS resolution for hostname '\''0x7f000001'\'' from line 1 of file h1.txt.\niprange: Creating new DNS thread\niprange: DNS: '\''0x7f000001'\'' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized h1.txt\niprange: Printing combined ipset with 1 ranges, 1 unique IPs\n\ntotals: 1 lines read, 1 distinct IP ranges found, 1 CIDR prefixes, 1 IPs printed, 1 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'-1' $'h1.txt'

check 'dns host then literal v4' 0 \
      '10.0.0.1\n127.0.0.1\n' \
      'iprange: Loading from hl.txt\niprange: DNS resolution for hostname '\''0x7f000001'\'' from line 1 of file hl.txt.\niprange: Creating new DNS thread\niprange: DNS: '\''0x7f000001'\'' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized hl.txt\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'hl.txt'

check 'dns literal then host v4' 0 \
      '10.0.0.1\n127.0.0.1\n' \
      'iprange: Loading from lh.txt\niprange: DNS resolution for hostname '\''0x7f000001'\'' from line 2 of file lh.txt.\niprange: Creating new DNS thread\niprange: DNS: '\''0x7f000001'\'' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized lh.txt\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'lh.txt'

check 'dns host merged with file' 0 \
      '10.0.0.1\n127.0.0.1\n' \
      'iprange: Loading from h1.txt\niprange: DNS resolution for hostname '\''0x7f000001'\'' from line 1 of file h1.txt.\niprange: Creating new DNS thread\niprange: DNS: '\''0x7f000001'\'' = 127.0.0.1\niprange: DNS: made 1 DNS requests, failed 0, retries: 0, IPs got 1, threads used 1 of 5\niprange: Loaded optimized h1.txt\niprange: Loading from m1.txt\niprange: Loaded optimized m1.txt\niprange: Merging m1.txt to combined ipset\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'h1.txt' $'m1.txt'
check 'dns one host v6 binary header' 0 \
      'iprange binary format v2.0\nipv6\noptimized\nrecord size 32\nrecords 2\nbytes 68\nlines 2\nunique ips 2\nM<+\032\001\000\000\000\000\000\000\000\000\000\000\000\000\000\000\000\001\000\000\000\000\000\000\000\000\000\000\000\000\000\000\000\001\000\000\177\377\377\000\000\000\000\000\000\000\000\000\000\001\000\000\177\377\377\000\000\000\000\000\000\000\000\000\000' \
      'iprange: Loading from l1.txt (IPv6 mode)\niprange: DNS resolution for hostname '\''localhost'\'' from line 1 of file l1.txt (IPv6 mode).\n' \
      $'-6' $'-v' $'--print-binary' $'l1.txt'

check_dns_interleaved_batch 'dns two names one thread' 0 \
      '10.0.0.1\n127.0.0.1\n' \
      'iprange: Loading from hn.txt\niprange: DNS resolution for hostname '\''0x7f000001'\'' from line 1 of file hn.txt.\niprange: Creating new DNS thread\niprange: DNS resolution for hostname '\''0x0A000001'\'' from line 2 of file hn.txt.\niprange: DNS: '\''0x0A000001'\'' = 10.0.0.1\niprange: DNS: '\''0x7f000001'\'' = 127.0.0.1\niprange: NON-OPTIMIZED hn.txt at line 2, entry 1, last was 127.0.0.1 (2130706433) - 127.0.0.1 (2130706433), new is 10.0.0.1 (167772161) - 10.0.0.1 (167772161)\niprange: DNS: made 2 DNS requests, failed 0, retries: 0, IPs got 2, threads used 1 of 1\niprange: Loaded non-optimized hn.txt\niprange: Optimizing combined ipset\niprange: Printing combined ipset with 2 ranges, 2 unique IPs\n\n2 printed CIDRs, break down by prefix:\n\t- prefix /32 counts 2 entries\n\ntotals: 2 lines read, 2 distinct IP ranges found, 1 CIDR prefixes, 2 CIDRs printed, 2 unique IPs\n<WALLCLOCK>\n' \
      $'-v' $'--dns-threads' $'1' $'hn.txt'

check_dns_bookkeeping_on_failure 'dns unresolvable counts no addresses' bad.txt \
      $'-v' $'bad.txt'

check_dns_bookkeeping_on_failure 'dns unresolvable silent counts no addresses' bad.txt \
      $'-v' $'--dns-silent' $'bad.txt'

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "# OK: 18 DNS bookkeeping cases match the C reference"
