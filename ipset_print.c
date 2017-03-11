#include "iprange.h"

uint8_t prefix_enabled[] = { 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1 };

char *print_prefix_ips = "";
char *print_prefix_nets = "";
char *print_suffix_ips = "";
char *print_suffix_nets = "";

inline void prefix_update_counters(in_addr_t addr, int prefix) {
    (void)addr;

    if(likely(prefix >= 0 && prefix <= 32))
        prefix_counters[prefix]++;
}

inline void print_addr(in_addr_t addr, int prefix) {
    prefix_update_counters(addr, prefix);

    char buf[IP2STR_MAX_LEN + 1];

    if (prefix < 32)
        printf("%s%s/%d%s\n", print_prefix_nets, ip2str_r(buf, addr), prefix, print_suffix_nets);
    else
        printf("%s%s%s\n", print_prefix_ips, ip2str_r(buf, addr), print_suffix_ips);

}				/* print_addr() */

/*------------------------------------------------------*/
/* Print out an address range in a.b.c.d-A.B.C.D format */
/*------------------------------------------------------*/
inline void print_addr_range(in_addr_t lo, in_addr_t hi) {
    char buf[IP2STR_MAX_LEN + 1];

    if(unlikely(lo > hi)) {
        /*
         * it should never happen
         * give a log for the user to see
         */
        in_addr_t t = hi;
        fprintf(stderr, "%s: WARNING: invalid range reversed start=%s", PROG, ip2str_r(buf, lo));
        fprintf(stderr, " end=%s\n", ip2str_r(buf, hi));
        hi = lo;
        lo = t;
    }

    if(lo == hi) {
        printf("%s%s-", print_prefix_ips, ip2str_r(buf, lo));
        printf("%s%s\n", ip2str_r(buf, hi), print_suffix_ips);
    }
    else {
        printf("%s%s-", print_prefix_nets, ip2str_r(buf, lo));
        printf("%s%s\n", ip2str_r(buf, hi), print_suffix_nets);
    }

}

inline void print_addr_single(in_addr_t x) {
    char buf[IP2STR_MAX_LEN + 1];
    printf("%s%s%s\n", print_prefix_ips, ip2str_r(buf, x), print_suffix_ips);

}

/*------------------------------------------------------------*/
/* Recursively compute network addresses to cover range lo-hi */
/*------------------------------------------------------------*/
/* Note: Worst case scenario is when lo=0.0.0.1 and hi=255.255.255.254
 *       We then have 62 CIDR blocks to cover this interval, and 125
 *       calls to split_range();
 *       The maximum possible recursion depth is 32.
 */

inline int split_range(in_addr_t addr, int prefix, in_addr_t lo, in_addr_t hi, void (*print)(in_addr_t, int)) {
    char buf[IP2STR_MAX_LEN + 1];
    in_addr_t bc, lower_half, upper_half;

    if(unlikely(lo > hi)) {
        /*
         * it should never happen
         * give a log for the user to see
         */
        in_addr_t t = hi;
        fprintf(stderr, "%s: WARNING: invalid range reversed start=%s", PROG, ip2str_r(buf, lo));
        fprintf(stderr, " end=%s\n", ip2str_r(buf, hi));
        hi = lo;
        lo = t;
    }

    if (unlikely((prefix < 0) || (prefix > 32))) {
        fprintf(stderr, "%s: Invalid netmask %d!\n", PROG, prefix);
        return 0;
    }

    bc = broadcast(addr, prefix);

    if (unlikely((lo < addr) || (hi > bc))) {
        fprintf(stderr, "%s: Out of range limits: %x, %x for "
                "network %x/%d, broadcast: %x!\n", PROG, lo, hi, addr, prefix, bc);
        return 0;
    }

    if ((lo == addr) && (hi == bc) && prefix_enabled[prefix]) {
        print(addr, prefix);
        return 1;
    }

    prefix++;
    lower_half = addr;
    upper_half = set_bit(addr, prefix, 1);

    if (hi < upper_half)
        return split_range(lower_half, prefix, lo, hi, print);
    else if (lo >= upper_half)
        return split_range(upper_half, prefix, lo, hi, print);
    else
        return (
                split_range(lower_half, prefix, lo, broadcast(lower_half, prefix), print) +
                split_range(upper_half, prefix, upper_half, hi, print)
        );
}


/* ----------------------------------------------------------------------------
 * ipset_print()
 *
 * print the ipset given to stdout
 *
 */

void ipset_print(ipset *ips, IPSET_PRINT_CMD print) {
    size_t i, n, total = 0;

    if(unlikely(!(ips->flags & IPSET_FLAG_OPTIMIZED)))
        ipset_optimize(ips);

    if(print == PRINT_BINARY) {
        ipset_save_binary_v10(ips);
        return;
    }

    if(unlikely(debug)) fprintf(stderr, "%s: Printing %s with %lu ranges, %lu unique IPs\n", PROG, ips->filename, ips->entries, ips->unique_ips);

    switch(print) {
        case PRINT_CIDR:
            /* reset the prefix counters */
            for(i = 0; i <= 32; i++)
                prefix_counters[i] = 0;

            n = ips->entries;
            for(i = 0; i < n ;i++)
                total += split_range(0, 0, ips->netaddrs[i].addr, ips->netaddrs[i].broadcast, print_addr);

            break;

        case PRINT_SINGLE_IPS:
            n = ips->entries;
            for(i = 0; i < n ;i++) {
                in_addr_t x, start = ips->netaddrs[i].addr, end = ips->netaddrs[i].broadcast;
                if(unlikely(start > end)) {
                    char buf[IP2STR_MAX_LEN + 1];
                    fprintf(stderr, "%s: WARNING: invalid range reversed start=%s", PROG, ip2str_r(buf, start));
                    fprintf(stderr, " end=%s\n", ip2str_r(buf, end));
                    x = end;
                    end = start;
                    start = x;
                }
                if(unlikely(end - start > (256 * 256 * 256))) {
                    char buf[IP2STR_MAX_LEN + 1];
                    fprintf(stderr, "%s: too big range eliminated start=%s", PROG, ip2str_r(buf, start));
                    fprintf(stderr, " end=%s gives %lu IPs\n", ip2str_r(buf, end), (unsigned long)(end - start));
                    continue;
                }
                for( x = start ; x >= start && x <= end ; x++ ) {
                    print_addr_single(x);
                    total++;
                }
            }
            break;

        default:
            n = ips->entries;
            for(i = 0; i < n ;i++) {
                print_addr_range(ips->netaddrs[i].addr, ips->netaddrs[i].broadcast);
                total++;
            }
            break;
    }

    /* print prefix break down */
    if(unlikely(debug)) {
        int prefixes = 0;

        if (print == PRINT_CIDR) {

            fprintf(stderr, "\n%lu printed CIDRs, break down by prefix:\n", total);

            total = 0;
            for(i = 0; i <= 32 ;i++) {
                if(prefix_counters[i]) {
                    fprintf(stderr, "	- prefix /%zu counts %zu entries\n", i, prefix_counters[i]);
                    total += prefix_counters[i];
                    prefixes++;
                }
            }
        }
        else if (print == PRINT_SINGLE_IPS) prefixes = 1;

        {
            char *units;
            if (print == PRINT_CIDR) units = "CIDRs";
            else if (print == PRINT_SINGLE_IPS) units = "IPs";
            else units = "ranges";

            fprintf(stderr, "\ntotals: %lu lines read, %lu distinct IP ranges found, %d CIDR prefixes, %lu %s printed, %lu unique IPs\n", ips->lines, ips->entries, prefixes, total, units, ips->unique_ips);
        }
    }
}
