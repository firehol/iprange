#include "iprange.h"

static uint32_t endianness = 0x1A2B3C4D;

/* ----------------------------------------------------------------------------
 * binary files v1.0
 *
 */

static void binary_write_failed(void) {
    fprintf(stderr, "%s: cannot write binary output: %s\n", PROG, strerror(errno));
    exit(1);
}

static int compare_network_addr_binary(const void *p1, const void *p2)
{
    const network_addr_t *na1 = (const network_addr_t *)p1;
    const network_addr_t *na2 = (const network_addr_t *)p2;

    if(na1->addr < na2->addr) return -1;
    if(na1->addr > na2->addr) return 1;
    if(na1->broadcast > na2->broadcast) return -1;
    if(na1->broadcast < na2->broadcast) return 1;
    return 0;
}

static int binary_range_size(network_addr_t *netaddr, uint64_t *size)
{
    if(unlikely(netaddr->addr > netaddr->broadcast)) return 1;
    *size = (uint64_t)netaddr->broadcast - (uint64_t)netaddr->addr + UINT64_C(1);
    return 0;
}

static int binary_add_unique_ips(uint64_t *total, uint64_t size)
{
    if(unlikely(*total > (UINT64_MAX - size))) return 1;
    *total += size;
    return 0;
}

static int binary_validate_payload(ipset *ips, int header_optimized, size_t entries, uint64_t expected_unique_ips, int *payload_is_optimized)
{
    size_t i;
    uint64_t actual_unique_ips = 0;
    uint64_t range_size = 0;

    *payload_is_optimized = 1;

    if(!entries) {
        if(unlikely(expected_unique_ips != 0)) {
            fprintf(stderr, "%s: %s: unique IPs (%" PRIu64 ") do not match the binary payload (%u)\n", PROG, ips->filename, expected_unique_ips, 0U);
            return 1;
        }

        return 0;
    }

    for(i = 0; i < entries; i++) {
        if(unlikely(binary_range_size(&ips->netaddrs[ips->entries + i], &range_size))) {
            fprintf(stderr, "%s: %s: invalid binary record %zu has addr > broadcast\n", PROG, ips->filename, i + 1);
            return 1;
        }
    }

    for(i = 1; i < entries; i++) {
        network_addr_t *prev = &ips->netaddrs[ips->entries + i - 1];
        network_addr_t *curr = &ips->netaddrs[ips->entries + i];

        if(curr->addr < prev->addr
           || curr->addr <= prev->broadcast
           || (prev->broadcast != UINT32_MAX && curr->addr == (prev->broadcast + 1))) {
            *payload_is_optimized = 0;
            break;
        }
    }

    if(*payload_is_optimized) {
        for(i = 0; i < entries; i++) {
            if(binary_range_size(&ips->netaddrs[ips->entries + i], &range_size)
               || binary_add_unique_ips(&actual_unique_ips, range_size)) {
                fprintf(stderr, "%s: %s: invalid unique IP accounting in binary payload\n", PROG, ips->filename);
                return 1;
            }
        }
    }
    else {
        network_addr_t *tmp;
        in_addr_t lo, hi;

        tmp = malloc(entries * sizeof(network_addr_t));
        if(unlikely(!tmp)) {
            fprintf(stderr, "%s: Cannot allocate memory (%zu bytes)\n", PROG, entries * sizeof(network_addr_t));
            return 1;
        }

        memcpy(tmp, &ips->netaddrs[ips->entries], entries * sizeof(network_addr_t));
        qsort(tmp, entries, sizeof(network_addr_t), compare_network_addr_binary);

        lo = tmp[0].addr;
        hi = tmp[0].broadcast;

        for(i = 1; i < entries; i++) {
            if(tmp[i].broadcast <= hi) continue;

            if(tmp[i].addr <= hi || (hi != UINT32_MAX && tmp[i].addr == (hi + 1))) {
                hi = tmp[i].broadcast;
                continue;
            }

            range_size = (uint64_t)hi - (uint64_t)lo + UINT64_C(1);
            if(unlikely(binary_add_unique_ips(&actual_unique_ips, range_size))) {
                free(tmp);
                fprintf(stderr, "%s: %s: invalid unique IP accounting in binary payload\n", PROG, ips->filename);
                return 1;
            }

            lo = tmp[i].addr;
            hi = tmp[i].broadcast;
        }

        range_size = (uint64_t)hi - (uint64_t)lo + UINT64_C(1);
        if(unlikely(binary_add_unique_ips(&actual_unique_ips, range_size))) {
            free(tmp);
            fprintf(stderr, "%s: %s: invalid unique IP accounting in binary payload\n", PROG, ips->filename);
            return 1;
        }

        free(tmp);
    }

    if(unlikely(expected_unique_ips != actual_unique_ips)) {
        fprintf(stderr, "%s: %s: unique IPs (%" PRIu64 ") do not match the binary payload (%" PRIu64 ")\n", PROG, ips->filename, expected_unique_ips, actual_unique_ips);
        return 1;
    }

    if(unlikely(header_optimized && !*payload_is_optimized)) {
        fprintf(stderr, "%s: %s: binary payload claims to be optimized but contains overlapping, adjacent, or unsorted records\n", PROG, ips->filename);
        return 1;
    }

    return 0;
}

static int parse_binary_size_field(ipset *ips, const char *field, const char *value, size_t *parsed_value)
{
    char *end = NULL;
    unsigned long long parsed;

    if(!value || *value < '0' || *value > '9') {
        fprintf(stderr, "%s: %s: invalid %s value '%s'\n", PROG, ips->filename, field, value?value:"");
        return 1;
    }

    errno = 0;
    parsed = strtoull(value, &end, 10);
    if(errno || !end || end == value || (*end != '\n' && *end != '\0') || parsed > SIZE_MAX) {
        fprintf(stderr, "%s: %s: invalid %s value '%s'\n", PROG, ips->filename, field, value);
        return 1;
    }

    *parsed_value = (size_t)parsed;
    return 0;
}

static int parse_binary_u64_field(ipset *ips, const char *field, const char *value, uint64_t *parsed_value)
{
    char *end = NULL;
    unsigned long long parsed;

    if(!value || *value < '0' || *value > '9') {
        fprintf(stderr, "%s: %s: invalid %s value '%s'\n", PROG, ips->filename, field, value?value:"");
        return 1;
    }

    errno = 0;
    parsed = strtoull(value, &end, 10);
    if(errno || !end || end == value || (*end != '\n' && *end != '\0')) {
        fprintf(stderr, "%s: %s: invalid %s value '%s'\n", PROG, ips->filename, field, value);
        return 1;
    }

    *parsed_value = (uint64_t)parsed;
    return 0;
}

int ipset_load_binary_v10(FILE *fp, ipset *ips, int first_line_missing) {
    char buffer[MAX_LINE + 1], *s;
    size_t entries, bytes, lines, expected_bytes, record_size;
    uint64_t unique_ips;
    uint32_t endian;
    size_t loaded;
    int header_optimized = 0;
    int payload_is_optimized = 0;

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
    if(!strcmp(s, "optimized\n")) header_optimized = 1;

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || strncmp(s, "record size ", 12)) {
        fprintf(stderr, "%s: %s 3rd line should be the record size, but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    if(parse_binary_size_field(ips, "record size", &s[12], &record_size))
        return 1;
    if(record_size != sizeof(network_addr_t)) {
        fprintf(stderr, "%s: %s: invalid record size %zu (expected %lu)\n", PROG, ips->filename, record_size, (unsigned long)sizeof(network_addr_t));
        return 1;
    }

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || strncmp(s, "records ", 8)) {
        fprintf(stderr, "%s: %s 4th line should be the number of records, but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    if(parse_binary_size_field(ips, "records", &s[8], &entries))
        return 1;

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || strncmp(s, "bytes ", 6)) {
        fprintf(stderr, "%s: %s 5th line should be the number of bytes, but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    if(parse_binary_size_field(ips, "bytes", &s[6], &bytes))
        return 1;

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || strncmp(s, "lines ", 6)) {
        fprintf(stderr, "%s: %s 6th line should be the number of lines read, but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    if(parse_binary_size_field(ips, "lines", &s[6], &lines))
        return 1;

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || strncmp(s, "unique ips ", 11)) {
        fprintf(stderr, "%s: %s 7th line should be the number of unique IPs, but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    if(parse_binary_u64_field(ips, "unique ips", &s[11], &unique_ips))
        return 1;

    if(entries > ((SIZE_MAX - sizeof(uint32_t)) / sizeof(network_addr_t))) {
        fprintf(stderr, "%s: %s: invalid number of records (%zu)\n", PROG, ips->filename, entries);
        return 1;
    }

    if(entries > (SIZE_MAX - ips->entries_max)) {
        fprintf(stderr, "%s: %s: too many records to load safely (%zu)\n", PROG, ips->filename, entries);
        return 1;
    }

    expected_bytes = (sizeof(network_addr_t) * entries) + sizeof(uint32_t);
    if(bytes != expected_bytes) {
        fprintf(stderr, "%s: %s invalid number of bytes, found %zu, expected %zu.\n", PROG, ips->filename, bytes, expected_bytes);
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
        fprintf(stderr, "%s: %s: unique IPs (%" PRIu64 ") cannot be less than entries (%zu)\n", PROG, ips->filename, unique_ips, entries);
        return 1;
    }

    if(lines < entries) {
        fprintf(stderr, "%s: %s: lines (%zu) cannot be less than entries (%zu)\n", PROG, ips->filename, lines, entries);
        return 1;
    }

    ipset_grow(ips, entries);

    loaded = fread(&ips->netaddrs[ips->entries], sizeof(network_addr_t), entries, fp);

    if(loaded != entries) {
        fprintf(stderr, "%s: %s: expected to load %lu entries, loaded %zu\n", PROG, ips->filename, entries, loaded);
        return 1;
    }

    if(fread(buffer, 1, 1, fp) != 0) {
        fprintf(stderr, "%s: %s: trailing data found after binary payload\n", PROG, ips->filename);
        return 1;
    }
    if(ferror(fp)) {
        fprintf(stderr, "%s: %s: error while checking for trailing binary data\n", PROG, ips->filename);
        return 1;
    }

    if(binary_validate_payload(ips, header_optimized, entries, unique_ips, &payload_is_optimized))
        return 1;

    ips->entries += loaded;
    ips->lines += lines;
    ips->unique_ips += unique_ips;
    ips->flags &= ~IPSET_FLAG_OPTIMIZED;
    if(header_optimized && payload_is_optimized) ips->flags |= IPSET_FLAG_OPTIMIZED;

    return 0;
}

void ipset_save_binary_v10(ipset *ips) {
    // it is crucial not to generate any output
    // if the ipset is empty:
    // the caller may do 'test -s file' to check it
    if(!ips->entries) return;

    if(fprintf(stdout, BINARY_HEADER_V10) < 0) binary_write_failed();
    if(ips->flags & IPSET_FLAG_OPTIMIZED) {
        if(fprintf(stdout, "optimized\n") < 0) binary_write_failed();
    }
    else if(fprintf(stdout, "non-optimized\n") < 0) {
        binary_write_failed();
    }
    if(fprintf(stdout, "record size %zu\n", sizeof(network_addr_t)) < 0) binary_write_failed();
    if(fprintf(stdout, "records %zu\n", ips->entries) < 0) binary_write_failed();
    if(fprintf(stdout, "bytes %zu\n", (sizeof(network_addr_t) * ips->entries) + sizeof(uint32_t)) < 0) binary_write_failed();
    if(fprintf(stdout, "lines %zu\n", ips->lines) < 0) binary_write_failed();
    if(fprintf(stdout, "unique ips %" PRIu64 "\n", ips->unique_ips) < 0) binary_write_failed();
    if(fwrite(&endianness, sizeof(uint32_t), 1, stdout) != 1) binary_write_failed();
    if(fwrite(ips->netaddrs, sizeof(network_addr_t), ips->entries, stdout) != ips->entries) binary_write_failed();
    if(fflush(stdout) != 0) binary_write_failed();
}
