#include "iprange.h"
#include "iprange6.h"
#include "ipset6.h"

static int compar_netaddr6(const void *p1, const void *p2) {
    const network_addr6_t *na1 = (const network_addr6_t *)p1;
    const network_addr6_t *na2 = (const network_addr6_t *)p2;

    if(na1->addr < na2->addr) return -1;
    if(na1->addr > na2->addr) return 1;
    if(na1->broadcast > na2->broadcast) return -1;
    if(na1->broadcast < na2->broadcast) return 1;
    return 0;
}

inline void ipset6_optimize(ipset6 *ips) {
    network_addr6_t *naddrs;
    size_t i, n = ips->entries, lines = ips->lines;
    network_addr6_t *oaddrs = ips->netaddrs;
    ipv6_addr_t lo, hi;

    if(unlikely(ips->flags & IPSET_FLAG_OPTIMIZED)) return;

    if(unlikely(debug)) fprintf(stderr, "%s: Optimizing %s (IPv6)\n", PROG, ips->filename);

    if(unlikely(n == 0)) {
        ips->flags |= IPSET_FLAG_OPTIMIZED;
        ips->unique_ips = 0;
        return;
    }

    qsort((void *)ips->netaddrs, ips->entries, sizeof(network_addr6_t), compar_netaddr6);

    naddrs = malloc(ips->entries * sizeof(network_addr6_t));
    if(unlikely(!naddrs)) {
        fprintf(stderr, "%s: Cannot allocate memory (%zu bytes)\n", PROG, n * sizeof(network_addr6_t));
        exit(1);
    }

    ips->netaddrs = naddrs;
    ips->entries = 0;
    ips->unique_ips = 0;
    ips->lines = 0;

    lo = oaddrs[0].addr;
    hi = oaddrs[0].broadcast;
    for(i = 1; i < n; i++) {
        if(oaddrs[i].broadcast <= hi)
            continue;

        /* overflow-safe adjacency check: hi + 1 would overflow if hi == max */
        if(oaddrs[i].addr <= hi || (hi != IPV6_ADDR_MAX && oaddrs[i].addr == hi + 1)) {
            hi = oaddrs[i].broadcast;
            continue;
        }

        ipset6_add_ip_range(ips, lo, hi);

        lo = oaddrs[i].addr;
        hi = oaddrs[i].broadcast;
    }
    ipset6_add_ip_range(ips, lo, hi);
    ips->lines = lines;

    ips->flags |= IPSET_FLAG_OPTIMIZED;

    free(oaddrs);
}

inline void ipset6_optimize_all(ipset6 *root) {
    ipset6 *ips;
    for(ips = root; ips; ips = ips->next)
        ipset6_optimize(ips);
}
