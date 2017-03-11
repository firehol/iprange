#include "iprange.h"

/*----------------------------------------------------------*/
/* compare two network_addr_t structures; used with qsort() */
/* sort in increasing order by address, then by prefix.     */
/*----------------------------------------------------------*/
int compar_netaddr(const void *p1, const void *p2) {

    network_addr_t *na1 = (network_addr_t *) p1, *na2 = (network_addr_t *) p2;

    if (na1->addr < na2->addr)
        return (-1);
    if (na1->addr > na2->addr)
        return (1);
    if (na1->broadcast > na2->broadcast)
        return (-1);
    if (na1->broadcast < na2->broadcast)
        return (1);

    return (0);

}				/* compar_netaddr() */

/* ----------------------------------------------------------------------------
 * ipset_optimize()
 *
 * takes an ipset with any number of entries (lo-hi pairs) in any order and
 * it optimizes it in place
 * after this optimization, all entries in the ipset are sorted (ascending)
 * and non-overlapping (it returns less or equal number of entries)
 *
 */

inline void ipset_optimize(ipset *ips) {
    network_addr_t *naddrs;
    size_t i, n = ips->entries, lines = ips->lines;
    network_addr_t *oaddrs = ips->netaddrs;
    in_addr_t lo, hi;

    if(unlikely(ips->flags & IPSET_FLAG_OPTIMIZED)) {
        fprintf(stderr, "%s: Is already optimized %s\n", PROG, ips->filename);
        return;
    }

    if(unlikely(debug)) fprintf(stderr, "%s: Optimizing %s\n", PROG, ips->filename);

    /* sort it */
    qsort((void *)ips->netaddrs, ips->entries, sizeof(network_addr_t), compar_netaddr);

    /* optimize it in a new space */
    naddrs = malloc(ips->entries * sizeof(network_addr_t));
    if(unlikely(!naddrs)) {
        ipset_free(ips);
        fprintf(stderr, "%s: Cannot allocate memory (%zu bytes)\n", PROG, ips->entries * sizeof(network_addr_t));
        exit(1);
    }

    ips->netaddrs = naddrs;
    ips->entries = 0;
    ips->unique_ips = 0;
    ips->lines = 0;

    if(!n) return;

    lo = oaddrs[0].addr;
    hi = oaddrs[0].broadcast;
    for (i = 1; i < n; i++) {
        /*
         * if the broadcast of this
         * is before the broadcast of the last
         * then skip it = it fits entirely inside the current
         */
        if (oaddrs[i].broadcast <= hi)
            continue;

        /*
         * if the network addr of this
         * overlaps or is adjustent to the last
         * then merge it = extent the broadcast of the last
         */
        if (oaddrs[i].addr <= hi + 1) {
            hi = oaddrs[i].broadcast;
            continue;
        }

        /*
         * at this point we are sure the old lo, hi
         * do not overlap and are not adjustent to the current
         * so, add the last to the new set
         */
        ipset_add_ip_range(ips, lo, hi);

        /* prepare for the next loop */
        lo = oaddrs[i].addr;
        hi = oaddrs[i].broadcast;
    }
    ipset_add_ip_range(ips, lo, hi);
    ips->lines = lines;

    ips->flags |= IPSET_FLAG_OPTIMIZED;

    free(oaddrs);
}

/* ----------------------------------------------------------------------------
 * ipset_optimize_all()
 *
 * it calls ipset_optimize() for all ipsets linked to 'next' to the given
 *
 */

inline void ipset_optimize_all(ipset *root) {
    ipset *ips;

    for(ips = root; ips ;ips = ips->next)
        ipset_optimize(ips);
}
