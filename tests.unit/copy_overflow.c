#include "iprange.h"

int cidr_use_network = 1;
int default_prefix = 32;
char *PROG = "copy_overflow";
int debug = 0;

int main(void)
{
    ipset *src = ipset_create("src", 1);
    ipset *copy;

    if(!src) return 2;

    memset(src->netaddrs, 0, src->entries_max * sizeof(network_addr_t));
    src->entries = src->entries_max + 4096;
    src->lines = src->entries;

    copy = ipset_copy(src);
    if(copy) {
        ipset_free(copy);
        ipset_free(src);
        return 1;
    }

    ipset_free(src);
    return 0;
}
