#include "iprange.h"

static uint32_t endianness = 0x1A2B3C4D;

/* ----------------------------------------------------------------------------
 * binary files v1.0
 *
 */

int ipset_load_binary_v10(FILE *fp, ipset *ips, int first_line_missing) {
    char buffer[MAX_LINE + 1], *s;
    unsigned long entries, bytes, lines, unique_ips;
    uint32_t endian;
    size_t loaded;

    if(!first_line_missing) {
        s = fgets(buffer, MAX_LINE, fp);
        buffer[MAX_LINE] = '\0';
        if(!s || strcmp(s, BINARY_HEADER_V10)) {
            fprintf(stderr, "%s: %s expecting binary header but found '%s'.\n", PROG, ips->filename, s?s:"");
            return 1;
        }
    }

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || ( strcmp(s, "optimized\n") && strcmp(s, "non-optimized\n") )) {
        fprintf(stderr, "%s: %s 2nd line should be the optimized flag, but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    if(!strcmp(s, "optimized\n")) ips->flags |= IPSET_FLAG_OPTIMIZED;
    else ips->flags &= ~IPSET_FLAG_OPTIMIZED;

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || strncmp(s, "record size ", 12)) {
        fprintf(stderr, "%s: %s 3rd line should be the record size, but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    if(atol(&s[12]) != sizeof(network_addr_t)) {
        fprintf(stderr, "%s: %s: invalid record size %ld (expected %lu)\n", PROG, ips->filename, atol(&s[12]), (unsigned long)sizeof(network_addr_t));
        return 1;
    }

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || strncmp(s, "records ", 8)) {
        fprintf(stderr, "%s: %s 4th line should be the number of records, but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    entries = strtoul(&s[8], NULL, 10);

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || strncmp(s, "bytes ", 6)) {
        fprintf(stderr, "%s: %s 5th line should be the number of bytes, but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    bytes = strtoul(&s[6], NULL, 10);

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || strncmp(s, "lines ", 6)) {
        fprintf(stderr, "%s: %s 6th line should be the number of lines read, but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    lines = strtoul(&s[6], NULL, 10);

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || strncmp(s, "unique ips ", 11)) {
        fprintf(stderr, "%s: %s 7th line should be the number of unique IPs, but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    unique_ips = strtoul(&s[11], NULL, 10);

    if(bytes != ((sizeof(network_addr_t) * entries) + sizeof(uint32_t))) {
        fprintf(stderr, "%s: %s invalid number of bytes, found %lu, expected %lu.\n", PROG, ips->filename, bytes, ((sizeof(network_addr_t) * entries) + sizeof(uint32_t)));
        return 1;
    }

    loaded = fread(&endian, sizeof(uint32_t), 1, fp);
    if(loaded != 1) {
        fprintf(stderr, "%s: %s: cannot load ipset header\n", PROG, ips->filename);
        return 1;
    }

    if(endian != endianness) {
        fprintf(stderr, "%s: %s: incompatible endianness\n", PROG, ips->filename);
        return 1;
    }

    if(unique_ips < entries) {
        fprintf(stderr, "%s: %s: unique IPs (%lu) cannot be less than entries (%lu)\n", PROG, ips->filename, unique_ips, entries);
        return 1;
    }

    if(lines < entries) {
        fprintf(stderr, "%s: %s: lines (%lu) cannot be less than entries (%lu)\n", PROG, ips->filename, lines, entries);
        return 1;
    }

    ipset_grow(ips, entries);

    loaded = fread(&ips->netaddrs[ips->entries], sizeof(network_addr_t), entries, fp);

    if(loaded != entries) {
        fprintf(stderr, "%s: %s: expected to load %lu entries, loaded %zu\n", PROG, ips->filename, entries, loaded);
        return 1;
    }

    ips->entries += loaded;
    ips->lines += lines;
    ips->unique_ips += unique_ips;

    return 0;
}

void ipset_save_binary_v10(ipset *ips) {
    // it is crucial not to generate any output
    // if the ipset is empty:
    // the caller may do 'test -s file' to check it
    if(!ips->entries) return;

    fprintf(stdout, BINARY_HEADER_V10);
    if(ips->flags & IPSET_FLAG_OPTIMIZED) fprintf(stdout, "optimized\n");
    else fprintf(stdout, "non-optimized\n");
    fprintf(stdout, "record size %lu\n", (unsigned long)sizeof(network_addr_t));
    fprintf(stdout, "records %lu\n", ips->entries);
    fprintf(stdout, "bytes %lu\n", (sizeof(network_addr_t) * ips->entries) + sizeof(uint32_t));
    fprintf(stdout, "lines %lu\n", ips->entries);
    fprintf(stdout, "unique ips %lu\n", ips->unique_ips);
    fwrite(&endianness, sizeof(uint32_t), 1, stdout);
    fwrite(ips->netaddrs, sizeof(network_addr_t), ips->entries, stdout);
}


