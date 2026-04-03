#include "iprange.h"
#include "iprange6.h"
#include "ipset6.h"

inline int ipset6_merge(ipset6 *to, ipset6 *add) {
    size_t total_entries, total_lines;

    if(unlikely(debug)) fprintf(stderr, "%s: Merging %s to %s (IPv6)\n", PROG, add->filename, to->filename);

    if(unlikely(to->entries > to->entries_max || add->entries > add->entries_max)) {
        fprintf(stderr, "%s: Cannot merge ipset %s to %s because one of them has an invalid internal entry count\n", PROG, add->filename, to->filename);
        return -1;
    }

    if(unlikely(ipset6_size_add_overflows(to->entries, add->entries, &total_entries) || ipset6_entries_allocation_overflows(total_entries))) {
        fprintf(stderr, "%s: Cannot merge ipset %s to %s safely: too many entries\n", PROG, add->filename, to->filename);
        return -1;
    }

    if(unlikely(ipset6_size_add_overflows(to->lines, add->lines, &total_lines))) {
        fprintf(stderr, "%s: Cannot merge ipset %s to %s safely: too many input lines\n", PROG, add->filename, to->filename);
        return -1;
    }

    ipset6_grow(to, add->entries);

    memcpy(&to->netaddrs[to->entries], &add->netaddrs[0], add->entries * sizeof(network_addr6_t));

    to->entries = total_entries;
    to->lines = total_lines;
    to->flags &= ~IPSET_FLAG_OPTIMIZED;
    return 0;
}
