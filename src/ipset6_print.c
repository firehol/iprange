#include "iprange.h"
#include "iprange6.h"
#include "ipset6.h"
#include "ipset6_binary.h"
#include "ipset6_print.h"

uint8_t prefix6_enabled[129] = {
    1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,  /* 0-15 */
    1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,  /* 16-31 */
    1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,  /* 32-47 */
    1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,  /* 48-63 */
    1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,  /* 64-79 */
    1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,  /* 80-95 */
    1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,  /* 96-111 */
    1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,1,  /* 112-127 */
    1                                    /* 128 */
};

size_t prefix6_counters[129];

/* hard cap on -1 output for IPv6 (same concept as IPv4's 256*256*256 cap) */
#define IPV6_SINGLE_IP_CAP (256ULL * 256 * 256)

inline void prefix6_update_counters(ipv6_addr_t addr, int prefix) {
    (void)addr;
    if(likely(prefix >= 0 && prefix <= 128))
        prefix6_counters[prefix]++;
}

inline void print_addr6(ipv6_addr_t addr, int prefix) {
    prefix6_update_counters(addr, prefix);

    char buf[IP6STR_MAX_LEN + 1];

    if(prefix < 128)
        printf("%s%s/%d%s\n", print_prefix_nets, ip6str_r(buf, addr), prefix, print_suffix_nets);
    else
        printf("%s%s%s\n", print_prefix_ips, ip6str_r(buf, addr), print_suffix_ips);
}

inline void print_addr6_range(ipv6_addr_t lo, ipv6_addr_t hi) {
    char buf[IP6STR_MAX_LEN + 1];

    if(unlikely(u128_gt(lo, hi))) {
        ipv6_addr_t t = hi;
        fprintf(stderr, "%s: WARNING: invalid range reversed start=%s", PROG, ip6str_r(buf, lo));
        fprintf(stderr, " end=%s\n", ip6str_r(buf, hi));
        hi = lo;
        lo = t;
    }

    if(u128_eq(lo, hi)) {
        printf("%s%s-", print_prefix_ips, ip6str_r(buf, lo));
        printf("%s%s\n", ip6str_r(buf, hi), print_suffix_ips);
    }
    else {
        printf("%s%s-", print_prefix_nets, ip6str_r(buf, lo));
        printf("%s%s\n", ip6str_r(buf, hi), print_suffix_nets);
    }
}

inline void print_addr6_single(ipv6_addr_t x) {
    char buf[IP6STR_MAX_LEN + 1];
    printf("%s%s%s\n", print_prefix_ips, ip6str_r(buf, x), print_suffix_ips);
}

/*------------------------------------------------------------*/
/* Recursively compute network addresses to cover range lo-hi */
/* for IPv6 (0..128 prefix space)                             */
/* Maximum recursion depth is 128.                            */
/*------------------------------------------------------------*/
inline int split_range6(ipv6_addr_t addr, int prefix, ipv6_addr_t lo, ipv6_addr_t hi, void (*print)(ipv6_addr_t, int)) {
    ipv6_addr_t bc, lower_half, upper_half;

    if(unlikely(u128_gt(lo, hi))) {
        ipv6_addr_t t = hi;
        char buf[IP6STR_MAX_LEN + 1];
        fprintf(stderr, "%s: WARNING: invalid range reversed start=%s", PROG, ip6str_r(buf, lo));
        fprintf(stderr, " end=%s\n", ip6str_r(buf, hi));
        hi = lo;
        lo = t;
    }

    if(unlikely(prefix < 0 || prefix > 128)) {
        fprintf(stderr, "%s: Invalid IPv6 prefix %d!\n", PROG, prefix);
        return 0;
    }

    bc = broadcast6(addr, prefix);

    if(unlikely(u128_lt(lo, addr) || u128_gt(hi, bc))) {
        char buf[IP6STR_MAX_LEN + 1];
        fprintf(stderr, "%s: Out of range limits for IPv6 network %s/%d\n", PROG, ip6str_r(buf, addr), prefix);
        return 0;
    }

    if(u128_eq(lo, addr) && u128_eq(hi, bc) && prefix6_enabled[prefix]) {
        print(addr, prefix);
        return 1;
    }

    prefix++;
    lower_half = addr;
    upper_half = set_bit6(addr, prefix, 1);

    if(u128_lt(hi, upper_half)) {
        /* cppcheck-suppress misra-c2012-17.2 -- CIDR splitting is bounded to 128 levels. */
        return split_range6(lower_half, prefix, lo, hi, print);
    }
    else if(u128_ge(lo, upper_half)) {
        /* cppcheck-suppress misra-c2012-17.2 -- CIDR splitting is bounded to 128 levels. */
        return split_range6(upper_half, prefix, lo, hi, print);
    }
    else {
        /* cppcheck-suppress misra-c2012-17.2 -- CIDR splitting is bounded to 128 levels. */
        int lower = split_range6(lower_half, prefix, lo, broadcast6(lower_half, prefix), print);
        /* cppcheck-suppress misra-c2012-17.2 -- CIDR splitting is bounded to 128 levels. */
        int upper = split_range6(upper_half, prefix, upper_half, hi, print);
        return (
            lower +
            upper
        );
    }
}

void ipset6_print(ipset6 *ips, IPSET_PRINT_CMD print) {
    size_t i, n, total = 0;
    char u128buf[40];

    if(unlikely(!(ips->flags & IPSET_FLAG_OPTIMIZED)))
        ipset6_optimize(ips);

    if(print == PRINT_BINARY) {
        ipset6_save_binary_v20(ips);
        return;
    }

    if(unlikely(debug)) fprintf(stderr, "%s: Printing %s (IPv6) with %zu ranges, %s unique IPs\n",
        PROG, ips->filename, ips->entries, u128_to_dec(u128buf, sizeof(u128buf), ips->unique_ips));

    switch(print) {
        case PRINT_CIDR:
            for(i = 0; i <= 128; i++)
                prefix6_counters[i] = 0;

            n = ips->entries;
            for(i = 0; i < n; i++)
                total += split_range6(U128_ZERO, 0, ips->netaddrs[i].addr, ips->netaddrs[i].broadcast, print_addr6);
            break;

        case PRINT_SINGLE_IPS:
            n = ips->entries;
            for(i = 0; i < n; i++) {
                ipv6_addr_t start = ips->netaddrs[i].addr;
                ipv6_addr_t end = ips->netaddrs[i].broadcast;
                ipv6_addr_t x;

                if(unlikely(u128_gt(start, end))) {
                    char buf[IP6STR_MAX_LEN + 1];
                    fprintf(stderr, "%s: WARNING: invalid range reversed start=%s", PROG, ip6str_r(buf, start));
                    fprintf(stderr, " end=%s\n", ip6str_r(buf, end));
                    x = end;
                    end = start;
                    start = x;
                }
                if(unlikely(u128_gt(u128_sub(end, start), u128_from_u64(IPV6_SINGLE_IP_CAP)))) {
                    char buf[IP6STR_MAX_LEN + 1];
                    fprintf(stderr, "%s: too big range eliminated start=%s", PROG, ip6str_r(buf, start));
                    fprintf(stderr, " end=%s\n", ip6str_r(buf, end));
                    continue;
                }
                for(x = start; u128_ge(x, start) && u128_le(x, end); x = u128_inc(x)) {
                    print_addr6_single(x);
                    total++;
                }
            }
            break;

        default:
            n = ips->entries;
            for(i = 0; i < n; i++) {
                print_addr6_range(ips->netaddrs[i].addr, ips->netaddrs[i].broadcast);
                total++;
            }
            break;
    }

    if(unlikely(debug)) {
        int prefixes = 0;

        if(print == PRINT_CIDR) {
            fprintf(stderr, "\n%zu printed CIDRs, break down by prefix:\n", total);
            total = 0;
            for(i = 0; i <= 128; i++) {
                if(prefix6_counters[i]) {
                    fprintf(stderr, "	- prefix /%zu counts %zu entries\n", i, prefix6_counters[i]);
                    total += prefix6_counters[i];
                    prefixes++;
                }
            }
        }
        else if(print == PRINT_SINGLE_IPS) prefixes = 1;

        {
            char *units;
            if(print == PRINT_CIDR) units = "CIDRs";
            else if(print == PRINT_SINGLE_IPS) units = "IPs";
            else units = "ranges";

            fprintf(stderr, "\ntotals: %zu lines read, %zu distinct IP ranges found, %d CIDR prefixes, %zu %s printed, %s unique IPs\n",
                ips->lines, ips->entries, prefixes, total, units,
                u128_to_dec(u128buf, sizeof(u128buf), ips->unique_ips));
        }
    }
}
