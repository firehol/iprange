#ifndef IPRANGE_IPSET6_H
#define IPRANGE_IPSET6_H

#include "iprange6.h"

typedef struct ipset6 {
    char filename[FILENAME_MAX+1];

    size_t lines;
    size_t entries;
    size_t entries_max;
    __uint128_t unique_ips;

    uint32_t flags;

    struct ipset6 *next;
    struct ipset6 *prev;

    network_addr6_t *netaddrs;
} ipset6;

extern ipset6 *ipset6_create(const char *filename, size_t entries);
extern void ipset6_free(ipset6 *ips);
extern void ipset6_free_all(ipset6 *ips);

extern size_t prefix6_counters[129];

extern __uint128_t ipset6_unique_ips(ipset6 *ips);

static inline int ipset6_entries_allocation_overflows(size_t entries) {
    return (entries > (SIZE_MAX / sizeof(network_addr6_t)));
}

static inline int ipset6_size_add_overflows(size_t left, size_t right, size_t *sum) {
    if(unlikely(left > (SIZE_MAX - right))) return 1;
    *sum = left + right;
    return 0;
}

extern void ipset6_grow_internal(ipset6 *ips, size_t free_entries_needed);

static inline void ipset6_grow(ipset6 *ips, size_t free_entries_needed) {
    if(unlikely(!ips)) return;

    if(unlikely(!free_entries_needed))
        free_entries_needed = 1;

    if(unlikely((ips->entries_max - ips->entries) < free_entries_needed))
        ipset6_grow_internal(ips, free_entries_needed);
}

static inline void ipset6_added_entry(ipset6 *ips) {
    size_t entries = ips->entries;

    ips->lines++;
    ips->unique_ips += (__uint128_t)ips->netaddrs[entries].broadcast - (__uint128_t)ips->netaddrs[entries].addr + 1;

    if(likely(ips->flags & IPSET_FLAG_OPTIMIZED && entries > 0)) {
        if(unlikely(ips->netaddrs[entries].addr == (ips->netaddrs[entries - 1].broadcast + 1))) {
            ips->netaddrs[entries - 1].broadcast = ips->netaddrs[entries].broadcast;
            return;
        }

        if(likely(ips->netaddrs[entries].addr > ips->netaddrs[entries - 1].broadcast)) {
            ips->entries++;
            return;
        }

        ips->flags &= ~IPSET_FLAG_OPTIMIZED;
    }

    ips->entries++;
}

static inline void ipset6_add_ip_range(ipset6 *ips, ipv6_addr_t from, ipv6_addr_t to) {
    ipset6_grow(ips, 1);

    ips->netaddrs[ips->entries].addr = from;
    ips->netaddrs[ips->entries].broadcast = to;
    ipset6_added_entry(ips);
}

static inline int ipset6_add_ipstr(ipset6 *ips, char *ipstr) {
    int err = 0;

    ipset6_grow(ips, 1);

    ips->netaddrs[ips->entries] = str2netaddr6(ipstr, &err);
    if(!err) ipset6_added_entry(ips);
    return !err;
}

/* Forward declarations for IPv6 operations */
extern void ipset6_optimize(ipset6 *ips);
extern void ipset6_optimize_all(ipset6 *root);
extern int ipset6_merge(ipset6 *to, ipset6 *add);
extern ipset6 *ipset6_common(ipset6 *ips1, ipset6 *ips2);
extern ipset6 *ipset6_exclude(ipset6 *ips1, ipset6 *ips2);
extern ipset6 *ipset6_diff(ipset6 *ips1, ipset6 *ips2);
extern ipset6 *ipset6_combine(ipset6 *ips1, ipset6 *ips2);
extern ipset6 *ipset6_copy(ipset6 *ips1);

#endif /* IPRANGE_IPSET6_H */
