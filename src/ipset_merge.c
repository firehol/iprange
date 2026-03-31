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

inline int ipset_merge(ipset *to, ipset *add) {
    size_t total_entries, total_lines;

    if(unlikely(debug)) fprintf(stderr, "%s: Merging %s to %s\n", PROG, add->filename, to->filename);

    if(unlikely(to->entries > to->entries_max || add->entries > add->entries_max)) {
        fprintf(stderr, "%s: Cannot merge ipset %s to %s because one of them has an invalid internal entry count\n", PROG, add->filename, to->filename);
        return -1;
    }

    if(unlikely(ipset_size_add_overflows(to->entries, add->entries, &total_entries) || ipset_entries_allocation_overflows(total_entries))) {
        fprintf(stderr, "%s: Cannot merge ipset %s to %s safely: too many entries\n", PROG, add->filename, to->filename);
        return -1;
    }

    if(unlikely(ipset_size_add_overflows(to->lines, add->lines, &total_lines))) {
        fprintf(stderr, "%s: Cannot merge ipset %s to %s safely: too many input lines\n", PROG, add->filename, to->filename);
        return -1;
    }

    ipset_grow(to, add->entries);

    memcpy(&to->netaddrs[to->entries], &add->netaddrs[0], add->entries * sizeof(network_addr_t));

    to->entries = total_entries;
    to->lines = total_lines;
    to->flags &= ~IPSET_FLAG_OPTIMIZED;
    return 0;
}
