#include "iprange.h"
#include "iprange6.h"
#include "ipset6.h"

inline ipset6 *ipset6_common(ipset6 *ips1, ipset6 *ips2) {
    ipset6 *ips;
    unsigned long int n1, n2, i1 = 0, i2 = 0;
    ipv6_addr_t lo1, lo2, hi1, hi2, lo, hi;

    if(unlikely(!(ips1->flags & IPSET_FLAG_OPTIMIZED)))
        ipset6_optimize(ips1);

    if(unlikely(!(ips2->flags & IPSET_FLAG_OPTIMIZED)))
        ipset6_optimize(ips2);

    if(unlikely(debug)) fprintf(stderr, "%s: Finding common IPs in %s and %s (IPv6)\n", PROG, ips1->filename, ips2->filename);

    ips = ipset6_create("common", 0);
    if(unlikely(!ips)) return NULL;

    n1 = ips1->entries;
    n2 = ips2->entries;

    if(unlikely(n1 == 0 || n2 == 0)) {
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
            i2++;
            if(i2 < n2) {
                lo2 = ips2->netaddrs[i2].addr;
                hi2 = ips2->netaddrs[i2].broadcast;
            }
            continue;
        }

        if(lo2 > hi1) {
            i1++;
            if(i1 < n1) {
                lo1 = ips1->netaddrs[i1].addr;
                hi1 = ips1->netaddrs[i1].broadcast;
            }
            continue;
        }

        lo = (lo1 > lo2) ? lo1 : lo2;

        if(hi1 < hi2) {
            hi = hi1;
            i1++;
            if(i1 < n1) {
                lo1 = ips1->netaddrs[i1].addr;
                hi1 = ips1->netaddrs[i1].broadcast;
            }
        }
        else {
            hi = hi2;
            i2++;
            if(i2 < n2) {
                lo2 = ips2->netaddrs[i2].addr;
                hi2 = ips2->netaddrs[i2].broadcast;
            }
        }

        ipset6_add_ip_range(ips, lo, hi);
    }

    ips->lines = ips1->lines + ips2->lines;
    ips->flags |= IPSET_FLAG_OPTIMIZED;

    return ips;
}
