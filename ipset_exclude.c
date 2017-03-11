#include "iprange.h"

/* ----------------------------------------------------------------------------
 * ipset_exclude()
 *
 * it takes 2 ipsets (ips1, ips2)
 * it returns 1 new ipset having all the IPs of ips1, excluding the IPs of ips2
 *
 * the result is optimized
 */

inline ipset *ipset_exclude(ipset *ips1, ipset *ips2) {
    ipset *ips;
    unsigned long int n1, n2, i1 = 0, i2 = 0;
    in_addr_t lo1, lo2, hi1, hi2;

    if(unlikely(!(ips1->flags & IPSET_FLAG_OPTIMIZED)))
        ipset_optimize(ips1);

    if(unlikely(!(ips2->flags & IPSET_FLAG_OPTIMIZED)))
        ipset_optimize(ips2);

    if(unlikely(debug)) fprintf(stderr, "%s: Removing IPs in %s from %s\n", PROG, ips2->filename, ips1->filename);

    ips = ipset_create(ips1->filename, 0);
    if(unlikely(!ips)) return NULL;

    n1 = ips1->entries;
    n2 = ips2->entries;

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
            ipset_add_ip_range(ips, lo1, hi1);

            i1++;
            if(i1 < n1) {
                lo1 = ips1->netaddrs[i1].addr;
                hi1 = ips1->netaddrs[i1].broadcast;
            }
            continue;
        }

        /* they overlap */

        if(lo1 < lo2) {
            ipset_add_ip_range(ips, lo1, lo2 - 1);
            lo1 = lo2;
        }

        if(hi1 == hi2) {
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
        else if(hi1 < hi2) {
            i1++;
            if(i1 < n1) {
                lo1 = ips1->netaddrs[i1].addr;
                hi1 = ips1->netaddrs[i1].broadcast;
            }
        }
        else if(hi1 > hi2) {
            lo1 = hi2 + 1;
            i2++;
            if(i2 < n2) {
                lo2 = ips2->netaddrs[i2].addr;
                hi2 = ips2->netaddrs[i2].broadcast;
            }
        }
    }

    if(i1 < n1) {
        ipset_add_ip_range(ips, lo1, hi1);
        i1++;

        /* if there are entries left in ips1, copy them */
        while(i1 < n1) {
            ipset_add_ip_range(ips, ips1->netaddrs[i1].addr, ips1->netaddrs[i1].broadcast);
            i1++;
        }
    }

    ips->lines = ips1->lines + ips2->lines;
    ips->flags |= IPSET_FLAG_OPTIMIZED;
    return ips;
}


