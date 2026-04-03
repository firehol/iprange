#include "iprange.h"
#include "iprange6.h"
#include "ipset6.h"

inline ipset6 *ipset6_copy(ipset6 *ips1) {
    ipset6 *ips;

    if(unlikely(debug)) fprintf(stderr, "%s: Copying %s (IPv6)\n", PROG, ips1->filename);

    if(unlikely(ips1->entries > ips1->entries_max)) {
        fprintf(stderr, "%s: Cannot copy ipset %s because it has an invalid internal entry count\n", PROG, ips1->filename);
        return NULL;
    }

    ips = ipset6_create(ips1->filename, ips1->entries);
    if(unlikely(!ips)) return NULL;

    memcpy(&ips->netaddrs[0], &ips1->netaddrs[0], ips1->entries * sizeof(network_addr6_t));

    ips->entries = ips1->entries;
    ips->unique_ips = ips1->unique_ips;
    ips->lines = ips1->lines;
    ips->flags = ips1->flags;

    return ips;
}
