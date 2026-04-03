#include "iprange.h"
#include "iprange6.h"
#include "ipset6.h"

#define IPSET6_ENTRIES_INCREASE_STEP 1024

ipset6 *ipset6_create(const char *filename, size_t entries) {
    ipset6 *ips = malloc(sizeof(ipset6));
    if(!ips) return NULL;

    if(entries < IPSET6_ENTRIES_INCREASE_STEP) entries = IPSET6_ENTRIES_INCREASE_STEP;

    if(unlikely(ipset6_entries_allocation_overflows(entries))) {
        free(ips);
        return NULL;
    }

    ips->netaddrs = malloc(entries * sizeof(network_addr6_t));
    if(!ips->netaddrs) {
        free(ips);
        return NULL;
    }

    ips->lines = 0;
    ips->entries = 0;
    ips->entries_max = entries;
    ips->unique_ips = 0;
    ips->next = NULL;
    ips->prev = NULL;
    ips->flags = 0;

    strncpy(ips->filename, (filename && *filename)?filename:"stdin", FILENAME_MAX);
    ips->filename[FILENAME_MAX] = '\0';

    return ips;
}

void ipset6_free(ipset6 *ips) {
    if(ips->next) ips->next->prev = ips->prev;
    if(ips->prev) ips->prev->next = ips->next;

    free(ips->netaddrs);
    free(ips);
}

void ipset6_free_all(ipset6 *ips) {
    ipset6 *prev, *next;

    if(!ips) return;

    prev = ips->prev;
    next = ips->next;

    if(prev) {
        prev->next = NULL;
        ips->prev = NULL;
        ipset6_free_all(prev);
    }

    if(next) {
        next->prev = NULL;
        ips->next = NULL;
        ipset6_free_all(next);
    }

    free(ips->netaddrs);
    free(ips);
}

void ipset6_grow_internal(ipset6 *ips, size_t free_entries_needed) {
    size_t increase;
    size_t new_entries_max;

    increase = (free_entries_needed < IPSET6_ENTRIES_INCREASE_STEP)?IPSET6_ENTRIES_INCREASE_STEP:free_entries_needed;
    if(unlikely(ipset6_size_add_overflows(ips->entries_max, increase, &new_entries_max) || ipset6_entries_allocation_overflows(new_entries_max))) {
        fprintf(stderr, "%s: Cannot grow ipset %s safely beyond %zu entries\n", PROG, ips->filename, ips->entries_max);
        exit(1);
    }

    ips->entries_max = new_entries_max;

    ips->netaddrs = realloc(ips->netaddrs, ips->entries_max * sizeof(network_addr6_t));
    if(unlikely(!ips->netaddrs)) {
        fprintf(stderr, "%s: Cannot re-allocate memory (%zu bytes)\n", PROG, ips->entries_max * sizeof(network_addr6_t));
        exit(1);
    }
}

inline __uint128_t ipset6_unique_ips(ipset6 *ips) {
    if(unlikely(!(ips->flags & IPSET_FLAG_OPTIMIZED)))
        ipset6_optimize(ips);

    return ips->unique_ips;
}
