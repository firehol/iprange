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
    size_t total_entries, total_lines;

    if(unlikely(debug)) fprintf(stderr, "%s: Combining %s and %s\n", PROG, ips1->filename, ips2->filename);

    if(unlikely(ips1->entries > ips1->entries_max || ips2->entries > ips2->entries_max)) {
        fprintf(stderr, "%s: Cannot combine ipsets %s and %s because one of them has an invalid internal entry count\n", PROG, ips1->filename, ips2->filename);
        return NULL;
    }

    if(unlikely(ipset_size_add_overflows(ips1->entries, ips2->entries, &total_entries) || ipset_entries_allocation_overflows(total_entries))) {
        fprintf(stderr, "%s: Cannot combine ipsets %s and %s safely: too many entries\n", PROG, ips1->filename, ips2->filename);
        return NULL;
    }

    if(unlikely(ipset_size_add_overflows(ips1->lines, ips2->lines, &total_lines))) {
        fprintf(stderr, "%s: Cannot combine ipsets %s and %s safely: too many input lines\n", PROG, ips1->filename, ips2->filename);
        return NULL;
    }

    ips = ipset_create("combined", total_entries);
    if(unlikely(!ips)) return NULL;

    memcpy(&ips->netaddrs[0], &ips1->netaddrs[0], ips1->entries * sizeof(network_addr_t));
    memcpy(&ips->netaddrs[ips1->entries], &ips2->netaddrs[0], ips2->entries * sizeof(network_addr_t));

    ips->entries = total_entries;
    ips->lines = total_lines;

    return ips;
}
