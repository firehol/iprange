#ifndef IPRANGE_IPSET_H
#define IPRANGE_IPSET_H

#define MAX_LINE 1024

#define IPSET_FLAG_OPTIMIZED 	0x00000001

typedef struct ipset {
    char filename[FILENAME_MAX+1];
    /* char name[FILENAME_MAX+1]; */

    size_t lines;
    size_t entries;
    size_t entries_max;
    size_t unique_ips;		/* this is updated only after calling ipset_optimize() */

    uint32_t flags;

    struct ipset *next;
    struct ipset *prev;

    network_addr_t *netaddrs;
} ipset;

extern ipset *ipset_create(const char *filename, size_t entries);
extern void ipset_free(ipset *ips);
extern void ipset_free_all(ipset *ips);

extern size_t prefix_counters[33];

extern size_t ipset_unique_ips(ipset *ips);


/* ----------------------------------------------------------------------------
 * ipset_grow()
 *
 * exprand the ipset so that it will have at least the given number of free
 * entries in its internal array
 *
 */

extern void ipset_grow_internal(ipset *ips, size_t free_entries_needed);

static inline void ipset_grow(ipset *ips, size_t free_entries_needed) {
    if(unlikely(!ips)) return;

    if(unlikely(!free_entries_needed))
        free_entries_needed = 1;

    if(unlikely((ips->entries_max - ips->entries) < free_entries_needed))
        ipset_grow_internal(ips, free_entries_needed);
}

/* ----------------------------------------------------------------------------
 * ipset_added_entry()
 *
 * validate and check the ipset, after appending one more entry
 *
 */

static inline void ipset_added_entry(ipset *ips) {
    size_t entries = ips->entries;

    ips->lines++;
    ips->unique_ips += ips->netaddrs[entries].broadcast - ips->netaddrs[entries].addr + 1;

    if(likely(ips->flags & IPSET_FLAG_OPTIMIZED && entries > 0)) {
        // the new is just next to the last
        if(unlikely(ips->netaddrs[entries].addr == (ips->netaddrs[entries - 1].broadcast + 1))) {
            ips->netaddrs[entries - 1].broadcast = ips->netaddrs[entries].broadcast;
            return;
        }

        // the new is after the end of the last
        if(likely(ips->netaddrs[entries].addr > ips->netaddrs[entries - 1].broadcast)) {
            ips->entries++;
            return;
        }

        // the new is before the beginning of the last
        ips->flags &= ~IPSET_FLAG_OPTIMIZED;

        if(unlikely(debug)) {
            in_addr_t new_from = ips->netaddrs[ips->entries].addr;
            in_addr_t new_to   = ips->netaddrs[ips->entries].broadcast;

            in_addr_t last_from = ips->netaddrs[ips->entries - 1].addr;
            in_addr_t last_to   = ips->netaddrs[ips->entries - 1].broadcast;

            char buf[IP2STR_MAX_LEN + 1];
            fprintf(stderr, "%s: NON-OPTIMIZED %s at line %lu, entry %lu, last was %s (%u) - ", PROG, ips->filename, ips->lines, ips->entries, ip2str_r(buf, last_from), last_from);
            fprintf(stderr, "%s (%u), new is ", ip2str_r(buf, last_to), last_to);
            fprintf(stderr, "%s (%u) - ", ip2str_r(buf, new_from), new_from);
            fprintf(stderr, "%s (%u)\n", ip2str_r(buf, new_to), new_to);
        }
    }

    ips->entries++;
}


/* ----------------------------------------------------------------------------
 * ipset_add_ip_range()
 *
 * add an IP entry (from - to) to the ipset given
 *
 */

static inline void ipset_add_ip_range(ipset *ips, in_addr_t from, in_addr_t to) {
    ipset_grow(ips, 1);

    ips->netaddrs[ips->entries].addr = from;
    ips->netaddrs[ips->entries].broadcast = to;
    ipset_added_entry(ips);
}


/* ----------------------------------------------------------------------------
 * ipset_add_ipstr()
 *
 * add a single IP entry to an ipset, by parsing the given IP string
 *
 */

static inline int ipset_add_ipstr(ipset *ips, char *ipstr) {
    int err = 0;

    ipset_grow(ips, 1);

    ips->netaddrs[ips->entries] = str2netaddr(ipstr, &err);
    if(!err) ipset_added_entry(ips);
    return !err;

}

#endif //IPRANGE_IPSET_H
