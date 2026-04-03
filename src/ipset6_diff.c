#include "iprange.h"
#include "iprange6.h"
#include "ipset6.h"

inline ipset6 *ipset6_diff(ipset6 *ips1, ipset6 *ips2) {
    ipset6 *ips;
    unsigned long int n1, n2, i1 = 0, i2 = 0;
    ipv6_addr_t lo1, lo2, hi1, hi2;

    if(unlikely(!(ips1->flags & IPSET_FLAG_OPTIMIZED)))
        ipset6_optimize(ips1);

    if(unlikely(!(ips2->flags & IPSET_FLAG_OPTIMIZED)))
        ipset6_optimize(ips2);

    if(unlikely(debug)) fprintf(stderr, "%s: Finding diff IPs in %s and %s (IPv6)\n", PROG, ips1->filename, ips2->filename);

    ips = ipset6_create("diff", 0);
    if(unlikely(!ips)) return NULL;

    n1 = ips1->entries;
    n2 = ips2->entries;

    if(unlikely(n1 == 0 && n2 == 0)) {
        ips->lines = ips1->lines + ips2->lines;
        ips->flags |= IPSET_FLAG_OPTIMIZED;
        return ips;
    }

    if(unlikely(n1 == 0)) {
        while(i2 < n2) {
            ipset6_add_ip_range(ips, ips2->netaddrs[i2].addr, ips2->netaddrs[i2].broadcast);
            i2++;
        }
        ips->lines = ips1->lines + ips2->lines;
        ips->flags |= IPSET_FLAG_OPTIMIZED;
        return ips;
    }

    if(unlikely(n2 == 0)) {
        while(i1 < n1) {
            ipset6_add_ip_range(ips, ips1->netaddrs[i1].addr, ips1->netaddrs[i1].broadcast);
            i1++;
        }
        ips->lines = ips1->lines + ips2->lines;
        ips->flags |= IPSET_FLAG_OPTIMIZED;
        return ips;
    }

    lo1 = ips1->netaddrs[0].addr;
    lo2 = ips2->netaddrs[0].addr;
    hi1 = ips1->netaddrs[0].broadcast;
    hi2 = ips2->netaddrs[0].broadcast;

    while(i1 < n1 && i2 < n2) {
        if(lo1 > hi2) {
            ipset6_add_ip_range(ips, lo2, hi2);
            i2++;
            if(i2 < n2) {
                lo2 = ips2->netaddrs[i2].addr;
                hi2 = ips2->netaddrs[i2].broadcast;
            }
            continue;
        }
        if(lo2 > hi1) {
            ipset6_add_ip_range(ips, lo1, hi1);
            i1++;
            if(i1 < n1) {
                lo1 = ips1->netaddrs[i1].addr;
                hi1 = ips1->netaddrs[i1].broadcast;
            }
            continue;
        }

        if(lo1 > lo2)
            ipset6_add_ip_range(ips, lo2, lo1 - 1);
        else if(lo2 > lo1)
            ipset6_add_ip_range(ips, lo1, lo2 - 1);

        if(hi1 > hi2) {
            lo1 = hi2 + 1;
            i2++;
            if(i2 < n2) {
                lo2 = ips2->netaddrs[i2].addr;
                hi2 = ips2->netaddrs[i2].broadcast;
            }
            continue;
        }
        else if(hi2 > hi1) {
            lo2 = hi1 + 1;
            i1++;
            if(i1 < n1) {
                lo1 = ips1->netaddrs[i1].addr;
                hi1 = ips1->netaddrs[i1].broadcast;
            }
            continue;
        }
        else {
            i1++;
            if(i1 < n1) {
                lo1 = ips1->netaddrs[i1].addr;
                hi1 = ips1->netaddrs[i1].broadcast;
            }
            i2++;
            if(i2 < n2) {
                lo2 = ips2->netaddrs[i2].addr;
                hi2 = ips2->netaddrs[i2].broadcast;
            }
        }
    }
    while(i1 < n1) {
        ipset6_add_ip_range(ips, lo1, hi1);
        i1++;
        if(i1 < n1) {
            lo1 = ips1->netaddrs[i1].addr;
            hi1 = ips1->netaddrs[i1].broadcast;
        }
    }
    while(i2 < n2) {
        ipset6_add_ip_range(ips, lo2, hi2);
        i2++;
        if(i2 < n2) {
            lo2 = ips2->netaddrs[i2].addr;
            hi2 = ips2->netaddrs[i2].broadcast;
        }
    }

    ips->lines = ips1->lines + ips2->lines;
    ips->flags |= IPSET_FLAG_OPTIMIZED;

    return ips;
}
