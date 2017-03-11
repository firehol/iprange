#include "iprange.h"

#define IPSET_ENTRIES_INCREASE_STEP 1024

/* ----------------------------------------------------------------------------
 * ipset_create()
 *
 * create an empty ipset with the given name and free entries in its array
 *
 */

ipset *ipset_create(const char *filename, size_t entries) {
    ipset *ips = malloc(sizeof(ipset));
    if(!ips) return NULL;

    if(entries < IPSET_ENTRIES_INCREASE_STEP) entries = IPSET_ENTRIES_INCREASE_STEP;

    ips->netaddrs = malloc(entries * sizeof(network_addr_t));
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

    /* strcpy(ips->name, ips->filename); */

    return ips;
}


/* ----------------------------------------------------------------------------
 * ipset_free()
 *
 * release the memory of an ipset and re-link its siblings so that lingage will
 * be consistent
 *
 */

void ipset_free(ipset *ips) {
    if(ips->next) ips->next->prev = ips->prev;
    if(ips->prev) ips->prev->next = ips->next;

    free(ips->netaddrs);
    free(ips);
}


/* ----------------------------------------------------------------------------
 * ipset_free_all()
 *
 * release all the memory occupied by all ipsets linked together (prev, next)
 *
 */

void ipset_free_all(ipset *ips) {
    if(ips->prev) {
        ips->prev->next = NULL;
        ipset_free_all(ips->prev);
    }

    if(ips->next) {
        ips->next->prev = NULL;
        ipset_free_all(ips->next);
    }

    ipset_free(ips);
}


void ipset_grow_internal(ipset *ips, size_t free_entries_needed) {

    // make sure we allocate at least IPSET_ENTRIES_INCREASE_STEP entries
    ips->entries_max += (free_entries_needed < IPSET_ENTRIES_INCREASE_STEP)?IPSET_ENTRIES_INCREASE_STEP:free_entries_needed;

    ips->netaddrs = realloc(ips->netaddrs, ips->entries_max * sizeof(network_addr_t));
    if(unlikely(!ips->netaddrs)) {
        fprintf(stderr, "%s: Cannot re-allocate memory (%zu bytes)\n", PROG, ips->entries_max * sizeof(network_addr_t));
        exit(1);
    }
}


inline size_t ipset_unique_ips(ipset *ips) {
    if(unlikely(!(ips->flags & IPSET_FLAG_OPTIMIZED)))
        ipset_optimize(ips);

    return(ips->unique_ips);
}

/* ----------------------------------------------------------------------------
 * ipset_histogram()
 *
 * generate histogram for ipset
 *
 */

/*
int ipset_histogram(ipset *ips, const char *path) {
    make sure the path exists
    if this is the first time:
     - create a directory for this ipset, in path
     - create the 'new' directory inside this ipset path
     - assume the 'latest' is empty
     - keep the starting date
     - print an empty histogram
    save in 'new' the IPs of current excluding the 'latest'
    save 'current' as 'latest'
    assume the histogram is complete
    for each file in 'new'
     - if the file is <= to histogram start date, the histogram is incomplete
     - calculate the hours passed to the 'current'
     - find the IPs in this file common to 'current' = 'stillthere'
     - find the IPs in this file not in 'stillthere' = 'removed'
     - if there are IPs in 'removed', add an entry to the retention histogram
     - if there are no IPs in 'stillthere', delete the file
     - else replace the file with the contents of 'stillthere'
    return 0;
}
*/


