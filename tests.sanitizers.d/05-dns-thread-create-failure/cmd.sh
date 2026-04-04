#!/bin/bash

tmpdir=$(mktemp -d)
stderr="$tmpdir/stderr"
trap 'rm -rf "$tmpdir"' EXIT

cat >"$tmpdir/dns_create_fail.c" <<'EOF'
#include "iprange.h"
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>

char *PROG = "dns-create-fail";
int debug;
int cidr_use_network = 1;
int default_prefix = 32;
int active_family = 0;
unsigned long ipv6_dropped_in_ipv4_mode = 0;

int pthread_create(pthread_t *thread, const pthread_attr_t *attr, void *(*start_routine)(void *), void *arg) {
    (void)thread;
    (void)attr;
    (void)start_routine;
    (void)arg;
    return 1;
}

static void on_alarm(int signo) {
    (void)signo;
    _exit(124);
}

int main(void) {
    FILE *fp;
    ipset *ips;

    signal(SIGALRM, on_alarm);
    alarm(2);

    fp = fopen("input.txt", "w");
    if(fp == NULL)
        return 2;

    fputs("localhost\n", fp);
    fclose(fp);

    ips = ipset_load("input.txt");
    if(ips != NULL) {
        ipset_free(ips);
        return 3;
    }

    return 0;
}
EOF

if ! "${CC:-clang}" \
    -DHAVE_CONFIG_H \
    -I"$BUILD_DIR" \
    -I../.. \
    -I../../src \
    -pthread \
    -fsanitize=address,undefined \
    -g -O1 -fno-omit-frame-pointer \
    "$tmpdir/dns_create_fail.c" \
    ../../src/ipset.c \
    ../../src/ipset_binary.c \
    ../../src/ipset_combine.c \
    ../../src/ipset_common.c \
    ../../src/ipset_copy.c \
    ../../src/ipset_diff.c \
    ../../src/ipset_dns.c \
    ../../src/ipset_exclude.c \
    ../../src/ipset_load.c \
    ../../src/ipset_merge.c \
    ../../src/ipset_optimize.c \
    ../../src/ipset_print.c \
    ../../src/ipset_reduce.c \
    -o "$tmpdir/dns_create_fail"; then
    echo "# ERROR: failed to build DNS thread-create harness"
    exit 1
fi

(
    cd "$tmpdir" &&
    ./dns_create_fail >/dev/null 2>"$stderr"
)
rc=$?

if [ $rc -eq 124 ]; then
    echo "# ERROR: DNS thread-create failure caused a hang"
    cat "$stderr"
    exit 1
fi

if [ $rc -ne 0 ]; then
    echo "# ERROR: DNS thread-create harness exited unexpectedly with $rc"
    cat "$stderr"
    exit 1
fi

if grep -Eq "AddressSanitizer|UndefinedBehaviorSanitizer|runtime error|heap-use-after-free" "$stderr"; then
    echo "# ERROR: DNS thread-create failure triggered sanitizer findings"
    cat "$stderr"
    exit 1
fi

if ! grep -q "Cannot create DNS thread" "$stderr"; then
    echo "# ERROR: expected DNS thread-create failure message"
    cat "$stderr"
    exit 1
fi

echo "# OK: DNS thread-create failure fails cleanly without hanging"
