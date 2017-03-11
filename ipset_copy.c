#include "iprange.h"

/* ----------------------------------------------------------------------------
 * ipset_copy()
 *
 * it returns a new ipset that is an exact copy of the ipset given
 *
 */

inline ipset *ipset_copy(ipset *ips1) {
    ipset *ips;

    if(unlikely(debug)) fprintf(stderr, "%s: Copying %s\n", PROG, ips1->filename);

    ips = ipset_create(ips1->filename, ips1->entries);
    if(unlikely(!ips)) return NULL;

    /*strcpy(ips->name, ips1->name); */
    memcpy(&ips->netaddrs[0], &ips1->netaddrs[0], ips1->entries * sizeof(network_addr_t));

    ips->entries = ips1->entries;
    ips->unique_ips = ips1->unique_ips;
    ips->lines = ips1->lines;
    ips->flags = ips1->flags;

    return ips;
}


