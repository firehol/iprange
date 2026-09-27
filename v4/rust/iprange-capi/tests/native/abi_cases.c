#include "abi_test_support.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* Reads v4/conformance/cases.json and checks each fixture through the C ABI.
 * A caller that does not open that file cannot name the ranges or the
 * structured holes. */

typedef struct {
    iprange_v4_abi1_ip from;
    iprange_v4_abi1_ip to;
    uint32_t value;
    int has_value;
} case_range;

static char *read_file(const char *path, size_t *length)
{
    FILE *stream = fopen(path, "rb");
    char *text;
    long size;
    if (stream == NULL) {
        return NULL;
    }
    if (fseek(stream, 0, SEEK_END) != 0) {
        fclose(stream);
        return NULL;
    }
    size = ftell(stream);
    if (size < 0) {
        fclose(stream);
        return NULL;
    }
    if (fseek(stream, 0, SEEK_SET) != 0) {
        fclose(stream);
        return NULL;
    }
    text = malloc((size_t)size + 1);
    if (text == NULL) {
        fclose(stream);
        return NULL;
    }
    if (fread(text, 1, (size_t)size, stream) != (size_t)size) {
        free(text);
        fclose(stream);
        return NULL;
    }
    fclose(stream);
    text[size] = '\0';
    *length = (size_t)size;
    return text;
}

static const char *find_key(const char *start, const char *end, const char *key)
{
    size_t length = strlen(key);
    const char *cursor = start;
    while (cursor + length < end) {
        if (memcmp(cursor, key, length) == 0) {
            return cursor;
        }
        cursor++;
    }
    return NULL;
}

static int read_string(const char *key, const char *start, const char *end, char *output, size_t output_size)
{
    const char *found = find_key(start, end, key);
    const char *cursor;
    size_t written = 0;
    if (found == NULL) {
        return 1;
    }
    cursor = found + strlen(key);
    while (cursor < end && *cursor != '"') {
        if (written + 1 >= output_size) {
            return 1;
        }
        output[written++] = *cursor++;
    }
    output[written] = '\0';
    return cursor < end ? 0 : 1;
}

static int parse_u32(const char *text, uint32_t *output)
{
    char *end = NULL;
    unsigned long value = strtoul(text, &end, 10);
    if (end == text || *end != '\0' || value > 0xffffffffUL) {
        return 1;
    }
    *output = (uint32_t)value;
    return 0;
}

static int hex_value(char text)
{
    if (text >= '0' && text <= '9') {
        return text - '0';
    }
    if (text >= 'a' && text <= 'f') {
        return text - 'a' + 10;
    }
    if (text >= 'A' && text <= 'F') {
        return text - 'A' + 10;
    }
    return -1;
}

static int parse_ipv4(const char *text, iprange_v4_abi1_ip *output)
{
    unsigned parts[4];
    char tail;
    if (sscanf(text, "%u.%u.%u.%u%c", &parts[0], &parts[1], &parts[2], &parts[3], &tail) != 4) {
        return 1;
    }
    if (parts[0] > 255 || parts[1] > 255 || parts[2] > 255 || parts[3] > 255) {
        return 1;
    }
    *output = ipv4((parts[0] << 24) | (parts[1] << 16) | (parts[2] << 8) | parts[3]);
    return 0;
}

static int parse_group(const char *text, size_t length, unsigned *output)
{
    size_t index;
    unsigned value = 0;
    if (length == 0 || length > 4) {
        return 1;
    }
    for (index = 0; index < length; index++) {
        int digit = hex_value(text[index]);
        if (digit < 0) {
            return 1;
        }
        value = (value << 4) | (unsigned)digit;
    }
    *output = value;
    return 0;
}

static int parse_ipv6(const char *text, iprange_v4_abi1_ip *output)
{
    unsigned groups[8] = {0};
    int count = 0;
    int gap_at = -1;
    const char *cursor = text;
    memset(output, 0, sizeof(*output));
    output->family = IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV6;
    if (strcmp(text, "::") == 0) {
        return 0;
    }
    while (*cursor != '\0') {
        const char *colon;
        size_t length;
        if (cursor[0] == ':' && cursor[1] == ':') {
            if (gap_at >= 0 || count >= 8) {
                return 1;
            }
            gap_at = count;
            cursor += 2;
            if (*cursor == '\0') {
                break;
            }
            continue;
        }
        colon = strchr(cursor, ':');
        length = colon == NULL ? strlen(cursor) : (size_t)(colon - cursor);
        if (count >= 8 || parse_group(cursor, length, &groups[count]) != 0) {
            return 1;
        }
        count++;
        if (colon == NULL) {
            break;
        }
        cursor = colon + 1;
        if (*cursor == ':') {
            if (gap_at >= 0 || count >= 8) {
                return 1;
            }
            gap_at = count;
            cursor++;
            if (*cursor == '\0') {
                break;
            }
            continue;
        }
        if (*cursor == '\0') {
            return 1;
        }
    }
    if (gap_at < 0) {
        if (count != 8) {
            return 1;
        }
    } else {
        int tail = count - gap_at;
        int index;
        unsigned placed[8] = {0};
        if (count > 7) {
            return 1;
        }
        for (index = 0; index < gap_at; index++) {
            placed[index] = groups[index];
        }
        for (index = 0; index < tail; index++) {
            placed[8 - tail + index] = groups[gap_at + index];
        }
        memcpy(groups, placed, sizeof(groups));
    }
    {
        int index;
        for (index = 0; index < 8; index++) {
            output->bytes[index * 2] = (uint8_t)(groups[index] >> 8);
            output->bytes[index * 2 + 1] = (uint8_t)groups[index];
        }
    }
    return 0;
}

static int parse_address(const char *text, int ipv6, iprange_v4_abi1_ip *output)
{
    return ipv6 ? parse_ipv6(text, output) : parse_ipv4(text, output);
}

static int same_address(iprange_v4_abi1_ip left, iprange_v4_abi1_ip right)
{
    return left.family == right.family && memcmp(left.bytes, right.bytes, 16) == 0;
}

static iprange_v4_abi1_ip next_address(iprange_v4_abi1_ip value)
{
    int index = value.family == IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4 ? 3 : 15;
    for (; index >= 0; index--) {
        value.bytes[index]++;
        if (value.bytes[index] != 0) {
            break;
        }
    }
    return value;
}

static const char *array_end(const char *start, const char *limit)
{
    int depth = 0;
    const char *cursor = start;
    while (cursor < limit) {
        if (*cursor == '[') {
            depth++;
        } else if (*cursor == ']') {
            depth--;
            if (depth == 0) {
                return cursor;
            }
        }
        cursor++;
    }
    return NULL;
}

static int collect_ranges(const char *slice, const char *slice_end, const char *key, int ipv6, int with_value, case_range **output, size_t *count)
{
    const char *found = find_key(slice, slice_end, key);
    const char *array;
    const char *end;
    const char *cursor;
    case_range *ranges = NULL;
    size_t used = 0;
    size_t capacity = 0;
    if (found == NULL) {
        *output = NULL;
        *count = 0;
        return 0;
    }
    array = strchr(found, '[');
    if (array == NULL || array >= slice_end) {
        return 1;
    }
    end = array_end(array, slice_end);
    if (end == NULL) {
        return 1;
    }
    cursor = array + 1;
    while (cursor < end) {
        const char *object = strchr(cursor, '{');
        const char *object_end;
        char from_text[64];
        char to_text[64];
        char value_text[16];
        case_range range;
        if (object == NULL || object >= end) {
            break;
        }
        object_end = strchr(object, '}');
        if (object_end == NULL || object_end > end) {
            free(ranges);
            return 1;
        }
        memset(&range, 0, sizeof(range));
        if (read_string("\"from\": \"", object, object_end, from_text, sizeof(from_text)) != 0 ||
            read_string("\"to\": \"", object, object_end, to_text, sizeof(to_text)) != 0 ||
            parse_address(from_text, ipv6, &range.from) != 0 ||
            parse_address(to_text, ipv6, &range.to) != 0) {
            fprintf(stderr, "range parse failed from=%s to=%s ipv6=%d\n", from_text, to_text, ipv6);
            free(ranges);
            return 1;
        }
        if (with_value) {
            const char *value = find_key(object, object_end, with_value == 1 ? "\"value\": " : "\"asn\": ");
            const char *stop;
            if (value == NULL) {
                free(ranges);
                return 1;
            }
            value += strlen(with_value == 1 ? "\"value\": " : "\"asn\": ");
            stop = value;
            while (stop < object_end && *stop >= '0' && *stop <= '9') {
                stop++;
            }
            if (stop == value || (size_t)(stop - value) >= sizeof(value_text)) {
                free(ranges);
                return 1;
            }
            memcpy(value_text, value, (size_t)(stop - value));
            value_text[stop - value] = '\0';
            if (parse_u32(value_text, &range.value) != 0) {
                free(ranges);
                return 1;
            }
            range.has_value = 1;
        }
        if (used == capacity) {
            size_t next_capacity = capacity == 0 ? 8 : capacity * 2;
            case_range *grown = realloc(ranges, next_capacity * sizeof(*ranges));
            if (grown == NULL) {
                free(ranges);
                return 1;
            }
            ranges = grown;
            capacity = next_capacity;
        }
        ranges[used++] = range;
        cursor = object_end + 1;
    }
    *output = ranges;
    *count = used;
    return 0;
}

static int close_reader(iprange_v4_abi1_reader *reader)
{
    iprange_v4_abi1_error *error = NULL;
    CHECK(iprange_v4_abi1_reader_close(reader, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    CHECK(iprange_v4_abi1_reader_destroy(reader, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    return 0;
}

static int check_direct(const iprange_v4_abi1_reader *reader, const case_range *ranges, size_t count)
{
    size_t index;
    for (index = 0; index < count; index++) {
        iprange_v4_abi1_error *error = NULL;
        uint8_t present = 0;
        uint32_t value = 0;
        CHECK(iprange_v4_abi1_reader_lookup_direct(reader, ranges[index].from, &present, &value, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(error == NULL && present == 1 && value == ranges[index].value);
    }
    return 0;
}

static int check_membership(const iprange_v4_abi1_reader *reader, const case_range *ranges, size_t count)
{
    size_t index;
    for (index = 0; index < count; index++) {
        iprange_v4_abi1_error *error = NULL;
        iprange_v4_abi1_membership_view *view = NULL;
        CHECK(iprange_v4_abi1_reader_lookup_membership(reader, ranges[index].from, &view, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(error == NULL && view != NULL);
        CHECK(iprange_v4_abi1_membership_view_close(view, &error) == IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(iprange_v4_abi1_membership_view_destroy(view, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    }
    return 0;
}

static int check_structured(const iprange_v4_abi1_reader *reader, const case_range *ranges, size_t count, int *gaps)
{
    size_t index;
    for (index = 0; index < count; index++) {
        iprange_v4_abi1_error *error = NULL;
        uint8_t present = 0;
        iprange_v4_abi1_network_enrichment_v1 found;
        iprange_v4_abi1_ip hole;
        memset(&found, 0, sizeof(found));
        CHECK(iprange_v4_abi1_reader_lookup_network_enrichment_v1(
                  reader, ranges[index].from, &present, &found, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(error == NULL && present == 1 && found.asn == ranges[index].value);
        if (index + 1 == count) {
            continue;
        }
        hole = next_address(ranges[index].to);
        if (same_address(hole, ranges[index + 1].from)) {
            continue;
        }
        present = 1;
        CHECK(iprange_v4_abi1_reader_lookup_network_enrichment_v1(
                  reader, hole, &present, &found, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(error == NULL && present == 0);
        (*gaps)++;
    }
    return 0;
}

static int check_fixture(const char *corpus, const char *slice, const char *slice_end, int *gaps)
{
    char file[128];
    char family[8];
    char kind[16];
    char path[512];
    int ipv6;
    uint32_t want_family;
    uint32_t want_kind;
    const char *range_key;
    int with_value;
    case_range *ranges = NULL;
    size_t count = 0;
    iprange_v4_abi1_reader *reader = NULL;
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_database_info info;
    memset(&info, 0, sizeof(info));
    CHECK(read_string("\"file\": \"", slice, slice_end, file, sizeof(file)) == 0);
    CHECK(read_string("\"family\": \"", slice, slice_end, family, sizeof(family)) == 0);
    CHECK(read_string("\"kind\": \"", slice, slice_end, kind, sizeof(kind)) == 0);
    ipv6 = strcmp(family, "ipv6") == 0;
    want_family = ipv6 ? IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV6 : IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4;
    if (strcmp(kind, "direct") == 0) {
        want_kind = IPRANGE_V4_ABI1_VALUE_KIND_DIRECT;
        range_key = "\"direct_ranges\"";
        with_value = 1;
    } else if (strcmp(kind, "membership") == 0) {
        want_kind = IPRANGE_V4_ABI1_VALUE_KIND_MEMBERSHIP;
        range_key = "\"membership_ranges\"";
        with_value = 0;
    } else {
        CHECK(strcmp(kind, "structured") == 0);
        want_kind = IPRANGE_V4_ABI1_VALUE_KIND_STRUCTURED;
        range_key = "\"structured_ranges\"";
        with_value = 2;
    }
    CHECK(snprintf(path, sizeof(path), "%s/%s", corpus, file) < (int)sizeof(path));
    CHECK(iprange_v4_abi1_open_immutable_reader(path_from(path), &reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    CHECK(iprange_v4_abi1_reader_database_info(reader, &info, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    CHECK(info.address_family == want_family);
    CHECK(info.value_kind == want_kind);
    CHECK(collect_ranges(slice, slice_end, range_key, ipv6, with_value, &ranges, &count) == 0);
    CHECK(count > 0);
    if (want_kind == IPRANGE_V4_ABI1_VALUE_KIND_DIRECT) {
        CHECK(check_direct(reader, ranges, count) == 0);
    } else if (want_kind == IPRANGE_V4_ABI1_VALUE_KIND_MEMBERSHIP) {
        CHECK(check_membership(reader, ranges, count) == 0);
    } else {
        CHECK(check_structured(reader, ranges, count, gaps) == 0);
    }
    free(ranges);
    return close_reader(reader);
}

int main(int argc, char **argv)
{
    char cases_path[512];
    char *text;
    size_t length = 0;
    const char *cursor;
    int opened = 0;
    int gaps = 0;
    int saw_rust_v6 = 0;
    int saw_go_v6 = 0;
    if (argc != 2) {
        fprintf(stderr, "usage: abi_cases <conformance-dir>\n");
        return 1;
    }
    CHECK(snprintf(cases_path, sizeof(cases_path), "%s/cases.json", argv[1]) < (int)sizeof(cases_path));
    text = read_file(cases_path, &length);
    CHECK(text != NULL);
    cursor = text;
    while ((cursor = strstr(cursor, "\"file\": \"")) != NULL) {
        const char *next = strstr(cursor + 1, "\"file\": \"");
        const char *end = next == NULL ? text + length : next;
        char file[128];
        if (read_string("\"file\": \"", cursor, end, file, sizeof(file)) != 0) {
            fprintf(stderr, "cases.json file name was not readable\n");
            free(text);
            return 1;
        }
        if (check_fixture(argv[1], cursor, end, &gaps) != 0) {
            fprintf(stderr, "fixture failed: %s\n", file);
            free(text);
            return 1;
        }
        if (strcmp(file, "rust/structured-ipv6.iprdb") == 0) {
            saw_rust_v6 = 1;
        }
        if (strcmp(file, "go/structured-ipv6.iprdb") == 0) {
            saw_go_v6 = 1;
        }
        opened++;
        if (next == NULL) {
            break;
        }
        cursor = next;
    }
    free(text);
    CHECK(opened == 16);
    CHECK(saw_rust_v6 == 1 && saw_go_v6 == 1);
    CHECK(gaps == 4);
    printf("cases=%d gaps=%d\n", opened, gaps);
    return 0;
}
