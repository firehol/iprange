#include "iprange.h"

/* ----------------------------------------------------------------------------
 * ipset_merge()
 *
 * merges the second ipset (add) to the first ipset (to)
 * they may not be optimized
 * the result is never optimized (even if the sources are)
 * to optimize it call ipset_optimize()
 *
 */

inline void ipset_merge(ipset *to, ipset *add) {
    if(unlikely(debug)) fprintf(stderr, "%s: Merging %s to %s\n", PROG, add->filename, to->filename);

    ipset_grow(to, add->entries);

    memcpy(&to->netaddrs[to->entries], &add->netaddrs[0], add->entries * sizeof(network_addr_t));

    to->entries = to->entries + add->entries;
    to->lines += add->lines;
    to->flags &= ~IPSET_FLAG_OPTIMIZED;
}
