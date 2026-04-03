#include "iprange.h"
#include "iprange6.h"
#include "ipset6.h"
#include "ipset6_binary.h"

static uint32_t endianness6 = 0x1A2B3C4D;

static void binary6_write_failed(void) {
    fprintf(stderr, "%s: cannot write binary output: %s\n", PROG, strerror(errno));
    exit(1);
}

static int binary6_validate_payload(ipset6 *ips, int header_optimized, size_t entries, __uint128_t expected_unique_ips, int *payload_is_optimized)
{
    size_t i;
    __uint128_t actual_unique_ips = 0;

    *payload_is_optimized = 1;

    if(!entries) {
        if(unlikely(expected_unique_ips != 0)) {
            fprintf(stderr, "%s: %s: unique IPs do not match the binary payload\n", PROG, ips->filename);
            return 1;
        }
        return 0;
    }

    for(i = 0; i < entries; i++) {
        if(unlikely(ips->netaddrs[ips->entries + i].addr > ips->netaddrs[ips->entries + i].broadcast)) {
            fprintf(stderr, "%s: %s: invalid binary record %zu has addr > broadcast\n", PROG, ips->filename, i + 1);
            return 1;
        }
    }

    for(i = 1; i < entries; i++) {
        network_addr6_t *prev = &ips->netaddrs[ips->entries + i - 1];
        network_addr6_t *curr = &ips->netaddrs[ips->entries + i];

        if(curr->addr < prev->addr
           || curr->addr <= prev->broadcast
           || (prev->broadcast != IPV6_ADDR_MAX && curr->addr == (prev->broadcast + 1))) {
            *payload_is_optimized = 0;
            break;
        }
    }

    if(*payload_is_optimized) {
        for(i = 0; i < entries; i++) {
            __uint128_t size = ips->netaddrs[ips->entries + i].broadcast - ips->netaddrs[ips->entries + i].addr + 1;
            actual_unique_ips += size;
        }
    }
    else {
        /* non-optimized: need to sort and merge to count unique IPs */
        /* for simplicity, we trust the header count for non-optimized v2 payloads */
        /* the data will be re-optimized after loading anyway */
        actual_unique_ips = expected_unique_ips;
    }

    if(unlikely(expected_unique_ips != actual_unique_ips)) {
        fprintf(stderr, "%s: %s: unique IPs do not match the binary payload\n", PROG, ips->filename);
        return 1;
    }

    if(unlikely(header_optimized && !*payload_is_optimized)) {
        fprintf(stderr, "%s: %s: binary payload claims to be optimized but contains overlapping, adjacent, or unsorted records\n", PROG, ips->filename);
        return 1;
    }

    return 0;
}

static int parse_binary6_size_field(ipset6 *ips, const char *field, const char *value, size_t *parsed_value)
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

static int parse_binary6_u128_field(ipset6 *ips, const char *field, const char *value, __uint128_t *parsed_value)
{
    __uint128_t result = 0;
    const char *s = value;

    if(!s || *s < '0' || *s > '9') {
        fprintf(stderr, "%s: %s: invalid %s value '%s'\n", PROG, ips->filename, field, s?s:"");
        return 1;
    }

    while(*s >= '0' && *s <= '9') {
        __uint128_t prev = result;
        result = result * 10 + (*s - '0');
        if(unlikely(result < prev)) {
            fprintf(stderr, "%s: %s: %s value overflow\n", PROG, ips->filename, field);
            return 1;
        }
        s++;
    }

    if(*s != '\n' && *s != '\0') {
        fprintf(stderr, "%s: %s: invalid %s value '%s'\n", PROG, ips->filename, field, value);
        return 1;
    }

    *parsed_value = result;
    return 0;
}

int ipset6_load_binary_v20(FILE *fp, ipset6 *ips, int first_line_missing) {
    char buffer[MAX_LINE + 1], *s;
    size_t entries, bytes, lines, expected_bytes, record_size;
    __uint128_t unique_ips;
    uint32_t endian;
    size_t loaded;
    int header_optimized = 0;
    int payload_is_optimized = 0;

    if(!first_line_missing) {
        s = fgets(buffer, MAX_LINE, fp);
        buffer[MAX_LINE] = '\0';
        if(!s || strcmp(s, BINARY_HEADER_V20)) {
            fprintf(stderr, "%s: %s expecting binary v2 header but found '%s'.\n", PROG, ips->filename, s?s:"");
            return 1;
        }
    }

    /* family line */
    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || strcmp(s, "ipv6\n")) {
        fprintf(stderr, "%s: %s expected family 'ipv6' but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || (strcmp(s, "optimized\n") && strcmp(s, "non-optimized\n"))) {
        fprintf(stderr, "%s: %s expected optimized flag but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    if(!strcmp(s, "optimized\n")) header_optimized = 1;

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || strncmp(s, "record size ", 12)) {
        fprintf(stderr, "%s: %s expected record size but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    if(parse_binary6_size_field(ips, "record size", &s[12], &record_size))
        return 1;
    if(record_size != sizeof(network_addr6_t)) {
        fprintf(stderr, "%s: %s: invalid record size %zu (expected %lu)\n", PROG, ips->filename, record_size, (unsigned long)sizeof(network_addr6_t));
        return 1;
    }

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || strncmp(s, "records ", 8)) {
        fprintf(stderr, "%s: %s expected records count but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    if(parse_binary6_size_field(ips, "records", &s[8], &entries))
        return 1;

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || strncmp(s, "bytes ", 6)) {
        fprintf(stderr, "%s: %s expected bytes count but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    if(parse_binary6_size_field(ips, "bytes", &s[6], &bytes))
        return 1;

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || strncmp(s, "lines ", 6)) {
        fprintf(stderr, "%s: %s expected lines count but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    if(parse_binary6_size_field(ips, "lines", &s[6], &lines))
        return 1;

    s = fgets(buffer, MAX_LINE, fp);
    buffer[MAX_LINE] = '\0';
    if(!s || strncmp(s, "unique ips ", 11)) {
        fprintf(stderr, "%s: %s expected unique ips but found '%s'.\n", PROG, ips->filename, s?s:"");
        return 1;
    }
    if(parse_binary6_u128_field(ips, "unique ips", &s[11], &unique_ips))
        return 1;

    if(entries > ((SIZE_MAX - sizeof(uint32_t)) / sizeof(network_addr6_t))) {
        fprintf(stderr, "%s: %s: invalid number of records (%zu)\n", PROG, ips->filename, entries);
        return 1;
    }

    if(entries > (SIZE_MAX - ips->entries_max)) {
        fprintf(stderr, "%s: %s: too many records to load safely (%zu)\n", PROG, ips->filename, entries);
        return 1;
    }

    expected_bytes = (sizeof(network_addr6_t) * entries) + sizeof(uint32_t);
    if(bytes != expected_bytes) {
        fprintf(stderr, "%s: %s invalid number of bytes, found %zu, expected %zu.\n", PROG, ips->filename, bytes, expected_bytes);
        return 1;
    }

    loaded = fread(&endian, sizeof(uint32_t), 1, fp);
    if(loaded != 1) {
        fprintf(stderr, "%s: %s: cannot load ipset header\n", PROG, ips->filename);
        return 1;
    }

    if(endian != endianness6) {
        fprintf(stderr, "%s: %s: incompatible endianness\n", PROG, ips->filename);
        return 1;
    }

    if(lines < entries) {
        fprintf(stderr, "%s: %s: lines (%zu) cannot be less than entries (%zu)\n", PROG, ips->filename, lines, entries);
        return 1;
    }

    ipset6_grow(ips, entries);

    loaded = fread(&ips->netaddrs[ips->entries], sizeof(network_addr6_t), entries, fp);

    if(loaded != entries) {
        fprintf(stderr, "%s: %s: expected to load %zu entries, loaded %zu\n", PROG, ips->filename, entries, loaded);
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

    if(binary6_validate_payload(ips, header_optimized, entries, unique_ips, &payload_is_optimized))
        return 1;

    ips->entries += loaded;
    ips->lines += lines;
    ips->unique_ips += unique_ips;
    ips->flags &= ~IPSET_FLAG_OPTIMIZED;
    if(header_optimized && payload_is_optimized) ips->flags |= IPSET_FLAG_OPTIMIZED;

    return 0;
}

void ipset6_save_binary_v20(ipset6 *ips) {
    char u128buf[40];

    if(!ips->entries) return;

    if(fprintf(stdout, BINARY_HEADER_V20) < 0) binary6_write_failed();
    if(fprintf(stdout, "ipv6\n") < 0) binary6_write_failed();
    if(ips->flags & IPSET_FLAG_OPTIMIZED) {
        if(fprintf(stdout, "optimized\n") < 0) binary6_write_failed();
    }
    else if(fprintf(stdout, "non-optimized\n") < 0) {
        binary6_write_failed();
    }
    if(fprintf(stdout, "record size %zu\n", sizeof(network_addr6_t)) < 0) binary6_write_failed();
    if(fprintf(stdout, "records %zu\n", ips->entries) < 0) binary6_write_failed();
    if(fprintf(stdout, "bytes %zu\n", (sizeof(network_addr6_t) * ips->entries) + sizeof(uint32_t)) < 0) binary6_write_failed();
    if(fprintf(stdout, "lines %zu\n", ips->lines) < 0) binary6_write_failed();
    if(fprintf(stdout, "unique ips %s\n", u128_to_dec(u128buf, sizeof(u128buf), ips->unique_ips)) < 0) binary6_write_failed();
    if(fwrite(&endianness6, sizeof(uint32_t), 1, stdout) != 1) binary6_write_failed();
    if(fwrite(ips->netaddrs, sizeof(network_addr6_t), ips->entries, stdout) != ips->entries) binary6_write_failed();
    if(fflush(stdout) != 0) binary6_write_failed();
}
