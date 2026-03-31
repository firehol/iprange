#include "iprange.h"
#include <string.h>

char *PROG = "combine_overflow";
int debug = 0;
int cidr_use_network = 1;
int default_prefix = 32;

int main(void)
{
    ipset *a = ipset_create("a", 1);
    ipset *b = ipset_create("b", 1);
    ipset *combined;

    if(!a || !b) return 2;

    memset(a->netaddrs, 0, a->entries_max * sizeof(network_addr_t));
    memset(b->netaddrs, 0, b->entries_max * sizeof(network_addr_t));

    a->entries = SIZE_MAX - 2048;
    b->entries = 4096;

    combined = ipset_combine(a, b);
    if(combined) {
        ipset_free(combined);
        return 1;
    }

    ipset_free(a);
    ipset_free(b);
    return 0;
}
