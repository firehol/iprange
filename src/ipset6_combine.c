#include "iprange.h"
#include "iprange6.h"
#include "ipset6.h"

inline ipset6 *ipset6_combine(ipset6 *ips1, ipset6 *ips2) {
    ipset6 *ips;
    size_t total_entries, total_lines;

    if(unlikely(debug)) fprintf(stderr, "%s: Combining %s and %s (IPv6)\n", PROG, ips1->filename, ips2->filename);

    if(unlikely(ips1->entries > ips1->entries_max || ips2->entries > ips2->entries_max)) {
        fprintf(stderr, "%s: Cannot combine ipsets %s and %s because one of them has an invalid internal entry count\n", PROG, ips1->filename, ips2->filename);
        return NULL;
    }

    if(unlikely(ipset6_size_add_overflows(ips1->entries, ips2->entries, &total_entries) || ipset6_entries_allocation_overflows(total_entries))) {
        fprintf(stderr, "%s: Cannot combine ipsets %s and %s safely: too many entries\n", PROG, ips1->filename, ips2->filename);
        return NULL;
    }

    if(unlikely(ipset6_size_add_overflows(ips1->lines, ips2->lines, &total_lines))) {
        fprintf(stderr, "%s: Cannot combine ipsets %s and %s safely: too many input lines\n", PROG, ips1->filename, ips2->filename);
        return NULL;
    }

    ips = ipset6_create("combined", total_entries);
    if(unlikely(!ips)) return NULL;

    memcpy(&ips->netaddrs[0], &ips1->netaddrs[0], ips1->entries * sizeof(network_addr6_t));
    memcpy(&ips->netaddrs[ips1->entries], &ips2->netaddrs[0], ips2->entries * sizeof(network_addr6_t));

    ips->entries = total_entries;
    ips->lines = total_lines;

    return ips;
}
