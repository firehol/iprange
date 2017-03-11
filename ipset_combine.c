#include "iprange.h"

/* ----------------------------------------------------------------------------
 * ipset_combine()
 *
 * it returns a new ipset that has all the entries of both ipsets given
 * the result is never optimized, even when the source ipsets are
 *
 */

inline ipset *ipset_combine(ipset *ips1, ipset *ips2) {
    ipset *ips;

    if(unlikely(debug)) fprintf(stderr, "%s: Combining %s and %s\n", PROG, ips1->filename, ips2->filename);

    ips = ipset_create("combined", ips1->entries + ips2->entries);
    if(unlikely(!ips)) return NULL;

    memcpy(&ips->netaddrs[0], &ips1->netaddrs[0], ips1->entries * sizeof(network_addr_t));
    memcpy(&ips->netaddrs[ips1->entries], &ips2->netaddrs[0], ips2->entries * sizeof(network_addr_t));

    ips->entries = ips1->entries + ips2->entries;
    ips->lines = ips1->lines + ips2->lines;

    return ips;
}

