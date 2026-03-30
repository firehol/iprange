#include "iprange.h"
#include <string.h>

char *PROG = "merge_overflow";
int debug = 0;
int cidr_use_network = 1;
int default_prefix = 32;

int main(void)
{
    ipset *to = ipset_create("to", 1);
    ipset *add = ipset_create("add", 1);
    size_t original_entries;
    size_t original_lines;
    uint32_t original_flags;

    if(!to || !add) return 2;

    memset(to->netaddrs, 0, to->entries_max * sizeof(network_addr_t));
    memset(add->netaddrs, 0, add->entries_max * sizeof(network_addr_t));

    to->entries = SIZE_MAX - 2048;
    to->lines = 7;
    to->flags = IPSET_FLAG_OPTIMIZED;
    add->entries = 4096;
    add->lines = 3;

    original_entries = to->entries;
    original_lines = to->lines;
    original_flags = to->flags;

    ipset_merge(to, add);

    if(to->entries != original_entries) return 1;
    if(to->lines != original_lines) return 1;
    if(to->flags != original_flags) return 1;

    ipset_free(to);
    ipset_free(add);
    return 0;
}
