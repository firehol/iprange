#include <errno.h>
#include <limits.h>

#include "abi_test_support.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* Reads v4/conformance/cases.json and checks each fixture through the C
 * ABI against the FULL manifest: every bounded range address, per-range
 * feed membership identities, every structured field, cardinalities
 * (range record count, active feed count), the value tag, and the
 * metadata state. A caller that does not open the manifest cannot name
 * the ranges, the feeds, or the holes. */

#define MAX_RANGE_FEEDS 24
#define MAX_FEED_NAME 32
#define MAX_UNIVERSE_FEEDS 80
#define MAX_RANGE_ADDRESSES 65536ULL /* per-range full-iteration bound */

typedef struct {
    iprange_v4_abi1_ip from;
    iprange_v4_abi1_ip to;
    uint32_t value; /* direct value, or structured asn */
    int has_value;
    uint32_t country_id;
    uint32_t state_id;
    uint32_t city_id;
    int32_t latitude;
    int32_t longitude;
    int has_geo;     /* manifest carries a location object */
    int has_location; /* manifest location is non-null */
    char feeds[MAX_RANGE_FEEDS][MAX_FEED_NAME];
    size_t feed_count;
} case_range;

#define FEED_INDEX_ABSENT 0xFFFFFFFFu

typedef struct {
    char names[MAX_UNIVERSE_FEEDS][MAX_FEED_NAME];
    uint32_t indices[MAX_UNIVERSE_FEEDS]; /* manifest-declared index */
    size_t count;
} feed_universe;

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

static int read_u32_key(const char *key, const char *start, const char *end, uint32_t *output)
{
    char text[32];
    const char *found = find_key(start, end, key);
    const char *cursor;
    size_t written = 0;
    if (found == NULL) {
        return 1;
    }
    cursor = found + strlen(key);
    while (cursor < end && *cursor >= '0' && *cursor <= '9') {
        if (written + 1 >= sizeof(text)) {
            return 1;
        }
        text[written++] = *cursor++;
    }
    if (written == 0) {
        return 1;
    }
    text[written] = '\0';
    return parse_u32(text, output);
}

static int read_i32_key(const char *key, const char *start, const char *end, int32_t *output)
{
    uint32_t raw;
    if (read_u32_key(key, start, end, &raw) != 0) {
        return 1;
    }
    /* Both manifest latitudes/longitudes fit in int32 as written; the
     * unsigned parse is a transport detail. */
    *output = (int32_t)raw;
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

/* The matching closing brace of the object at `start` (which must point
 * at '{'). A nested object (for example a structured range's location)
 * would otherwise end the slice at its own '}', silently dropping every
 * key that follows it. */
static const char *matching_brace(const char *start, const char *limit)
{
    int depth = 0;
    const char *cursor = start;
    while (cursor < limit) {
        if (*cursor == '{') {
            depth++;
        } else if (*cursor == '}') {
            depth--;
            if (depth == 0) {
                return cursor;
            }
        }
        cursor++;
    }
    return NULL;
}

/* Reads one string-list array like "feeds": ["a", "b"] into names. */
static int read_name_list(const char *object, const char *object_end, const char *key,
                           char names[][MAX_FEED_NAME], size_t *count, size_t limit)
{
    const char *found = find_key(object, object_end, key);
    const char *array;
    const char *end;
    const char *cursor;
    size_t used = 0;
    if (found == NULL) {
        *count = 0;
        return 0;
    }
    array = strchr(found, '[');
    if (array == NULL || array >= object_end) {
        return 1;
    }
    end = array_end(array, object_end);
    if (end == NULL) {
        return 1;
    }
    cursor = array + 1;
    while (cursor < end && used < limit) {
        const char *quote = strchr(cursor, '"');
        if (quote == NULL || quote >= end) {
            break;
        }
        cursor = read_string("\"", quote - 1, end, names[used], MAX_FEED_NAME) == 0
                     ? quote + 1
                     : cursor;
        if (names[used][0] == '\0') {
            return 1;
        }
        used++;
        cursor = strchr(cursor, ',');
        if (cursor == NULL || cursor >= end) {
            break;
        }
        cursor++;
    }
    *count = used;
    return 0;
}

/* Reads the fixture-level universe: "feeds": [ {"name": ..., "index": ...} ]. */
static int read_feed_universe(const char *slice, const char *slice_end, feed_universe *universe)
{
    const char *found = find_key(slice, slice_end, "\"feeds\": [");
    const char *array;
    const char *end;
    const char *cursor;
    size_t used = 0;
    memset(universe, 0, sizeof(*universe));
    if (found == NULL) {
        return 0; /* no feeds key: empty universe */
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
    while (cursor < end && used < MAX_UNIVERSE_FEEDS) {
        const char *object = strchr(cursor, '{');
        const char *object_end;
        if (object == NULL || object >= end) {
            break;
        }
        object_end = strchr(object, '}');
        if (object_end == NULL || object_end > end) {
            return 1;
        }
        if (read_string("\"name\": \"", object, object_end, universe->names[used],
                        MAX_FEED_NAME) != 0) {
            return 1;
        }
        universe->indices[used] = FEED_INDEX_ABSENT;
        {
            /* The manifest declares each feed's index; the checker must
             * hold the implementation to it, not take the index from the
             * implementation's own answer (astra turn-2). */
            const char *index_at = find_key(object, object_end, "\"index\": ");
            if (index_at != NULL) {
                char digits[24];
                const char *cursor = index_at + strlen("\"index\": ");
                size_t written = 0;
                unsigned long parsed;
                while (cursor < object_end && *cursor >= '0' && *cursor <= '9' &&
                       written + 1 < sizeof(digits)) {
                    digits[written++] = *cursor++;
                }
                digits[written] = '\0';
                parsed = strtoul(digits, NULL, 10);
                if (written == 0 || parsed > 0xFFFFFFFFul) {
                    return 1;
                }
                universe->indices[used] = (uint32_t)parsed;
            }
        }
        used++;
        cursor = object_end + 1;
    }
    universe->count = used;
    return 0;
}

static int collect_ranges(const char *slice, const char *slice_end, const char *key,
                          int ipv6, int kind, case_range **output, size_t *count)
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
        case_range range;
        if (object == NULL || object >= end) {
            break;
        }
        object_end = matching_brace(object, end);
        if (object_end == NULL) {
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
        if (kind == 0) { /* direct: "value" */
            if (read_u32_key("\"value\": ", object, object_end, &range.value) != 0) {
                free(ranges);
                return 1;
            }
            range.has_value = 1;
        } else if (kind == 2) { /* structured: every enrichment field */
            const char *location = find_key(object, object_end, "\"location\": {");
            if (read_u32_key("\"asn\": ", object, object_end, &range.value) != 0 ||
                read_u32_key("\"country_id\": ", object, object_end, &range.country_id) != 0 ||
                read_u32_key("\"state_id\": ", object, object_end, &range.state_id) != 0 ||
                read_u32_key("\"city_id\": ", object, object_end, &range.city_id) != 0) {
                free(ranges);
                return 1;
            }
            range.has_value = 1;
            range.has_geo = location != NULL;
            if (location != NULL) {
                const char *location_end = strchr(location, '}');
                if (location_end == NULL || location_end > object_end) {
                    free(ranges);
                    return 1;
                }
                if (read_i32_key("\"latitude_microdegrees\": ", location, location_end,
                                 &range.latitude) != 0 ||
                    read_i32_key("\"longitude_microdegrees\": ", location, location_end,
                                 &range.longitude) != 0) {
                    free(ranges);
                    return 1;
                }
                range.has_location = 1;
            }
        }
        if (kind != 0) { /* membership and structured carry feed lists */
            if (read_name_list(object, object_end, "\"feeds\": [", range.feeds,
                               &range.feed_count, MAX_RANGE_FEEDS) != 0) {
                free(ranges);
                return 1;
            }
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

/* Bounded iteration: IPv4 ranges under the cap are verified at every
 * address; larger or IPv6 ranges verify first and last only. */
static int for_each_address(const case_range *range,
                            int (*visit)(const iprange_v4_abi1_ip *, void *, size_t),
                            void *context, const char *what)
{
    iprange_v4_abi1_ip address = range->from;
    size_t visited = 0;
    while (visited <= MAX_RANGE_ADDRESSES) {
        if (visit(&address, context, visited) != 0) {
            fprintf(stderr, "%s check failed at a covered address\n", what);
            return 1;
        }
        visited++;
        if (same_address(address, range->to)) {
            return 0; /* fully covered */
        }
        address = next_address(address);
    }
    /* The cap fired before the last address: the range is larger than
     * the full-iteration bound. The first address was already visited;
     * visit the last address too. */
    address = range->to;
    return visit(&address, context, visited);
}

typedef struct {
    const iprange_v4_abi1_reader *reader;
    uint32_t value;
} direct_context;

static int visit_direct(const iprange_v4_abi1_ip *address, void *opaque, size_t ordinal)
{
    direct_context *context = (direct_context *)opaque;
    iprange_v4_abi1_error *error = NULL;
    uint8_t present = 0;
    uint32_t value = 0;
    (void)ordinal;
    CHECK(iprange_v4_abi1_reader_lookup_direct(context->reader, *address, &present, &value,
                                               &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL && present == 1 && value == context->value);
    return 0;
}

static int check_direct(const iprange_v4_abi1_reader *reader, const case_range *ranges,
                        size_t count, int *gaps)
{
    size_t index;
    for (index = 0; index < count; index++) {
        direct_context context;
        context.reader = reader;
        context.value = ranges[index].value;
        CHECK(for_each_address(&ranges[index], visit_direct, &context, "direct") == 0);
        if (index + 1 == count) {
            continue;
        }
        {
            iprange_v4_abi1_error *error = NULL;
            uint8_t present = 1;
            uint32_t value = 0;
            iprange_v4_abi1_ip hole = next_address(ranges[index].to);
            if (same_address(hole, ranges[index + 1].from)) {
                continue;
            }
            CHECK(iprange_v4_abi1_reader_lookup_direct(reader, hole, &present, &value, &error) ==
                  IPRANGE_V4_ABI1_STATUS_OK);
            CHECK(error == NULL && present == 0);
            (*gaps)++;
        }
    }
    return 0;
}

typedef struct {
    const iprange_v4_abi1_reader *reader;
    const feed_universe *universe;
    const case_range *range;
    uint32_t *indices; /* universe name -> feed index */
} membership_context;

static int range_has_feed(const case_range *range, const char *name)
{
    size_t index;
    for (index = 0; index < range->feed_count; index++) {
        if (strcmp(range->feeds[index], name) == 0) {
            return 1;
        }
    }
    return 0;
}

static int visit_membership(const iprange_v4_abi1_ip *address, void *opaque, size_t ordinal)
{
    membership_context *context = (membership_context *)opaque;
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_membership_view *view = NULL;
    size_t index;
    CHECK(iprange_v4_abi1_reader_lookup_membership(context->reader, *address, &view, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL && view != NULL);
    for (index = 0; index < context->universe->count; index++) {
        uint8_t contains = 0;
        CHECK(iprange_v4_abi1_membership_view_contains_index(
                  view, context->indices[index], &contains, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(error == NULL);
        CHECK(contains == (range_has_feed(context->range, context->universe->names[index]) ? 1 : 0));
    }
    CHECK(iprange_v4_abi1_membership_view_close(view, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    CHECK(iprange_v4_abi1_membership_view_destroy(view, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    (void)ordinal;
    return 0;
}

static int check_membership(const iprange_v4_abi1_reader *reader, const case_range *ranges,
                            size_t count, const feed_universe *universe, int *gaps)
{
    uint32_t *indices = NULL;
    size_t index;
    if (universe->count > 0) {
        indices = calloc(universe->count, sizeof(*indices));
        CHECK(indices != NULL);
        for (index = 0; index < universe->count; index++) {
            iprange_v4_abi1_error *error = NULL;
            uint8_t present = 0;
            iprange_v4_abi1_feed_info info;
            const char *name = universe->names[index];
            memset(&info, 0, sizeof(info));
            CHECK(iprange_v4_abi1_reader_lookup_feed(reader, (const uint8_t *)name,
                                                      strlen(name), &present, &info,
                                                      &error) == IPRANGE_V4_ABI1_STATUS_OK);
            CHECK(error == NULL && present == 1);
            CHECK(strcmp((const char *)info.name, name) == 0 && info.name_length == strlen(name));
            /* The returned index is the implementation's answer; the
             * manifest's declared index is the expectation. A reader
             * renumbering feeds silently would otherwise go unseen. */
            if (universe->indices[index] != FEED_INDEX_ABSENT) {
                CHECK(info.index == universe->indices[index]);
            }
            indices[index] = info.index;
        }
    }
    for (index = 0; index < count; index++) {
        membership_context context;
        context.reader = reader;
        context.universe = universe;
        context.range = &ranges[index];
        context.indices = indices;
        CHECK(for_each_address(&ranges[index], visit_membership, &context, "membership") == 0);
        if (index + 1 == count) {
            continue;
        }
        {
            iprange_v4_abi1_ip hole = next_address(ranges[index].to);
            if (same_address(hole, ranges[index + 1].from)) {
                continue;
            }
            /* The hole must carry no membership: verified, not just
             * counted. An address with no coverage answers OK with a
             * NULL view (the Option::None shape). */
            {
                iprange_v4_abi1_error *error = NULL;
                iprange_v4_abi1_membership_view *view = (iprange_v4_abi1_membership_view *)1;
                CHECK(iprange_v4_abi1_reader_lookup_membership(reader, hole, &view,
                                                                &error) ==
                      IPRANGE_V4_ABI1_STATUS_OK);
                CHECK(error == NULL && view == NULL);
            }
            (*gaps)++;
        }
    }
    free(indices);
    return 0;
}

typedef struct {
    const iprange_v4_abi1_reader *reader;
    const case_range *range;
    const feed_universe *universe;
    const uint32_t *indices;
} structured_context;

static int visit_structured(const iprange_v4_abi1_ip *address, void *opaque, size_t ordinal)
{
    structured_context *context = (structured_context *)opaque;
    iprange_v4_abi1_error *error = NULL;
    uint8_t present = 0;
    iprange_v4_abi1_network_enrichment_v1 found;
    iprange_v4_abi1_membership_view *view = NULL;
    size_t index;
    const case_range *range = context->range;
    memset(&found, 0, sizeof(found));
    /* The with-membership lookup returns the enrichment value together
     * with the threat-feed membership it carries: the structured record
     * must not only hold the scalar fields, its feed set must match the
     * manifest per range (astra turn-2). */
    CHECK(iprange_v4_abi1_reader_lookup_network_enrichment_v1_with_membership(
              context->reader, *address, &present, &found, &view, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL && present == 1);
    CHECK(found.asn == range->value);
    CHECK(found.country_id == range->country_id);
    CHECK(found.state_id == range->state_id);
    CHECK(found.city_id == range->city_id);
    CHECK(found.has_location == (range->has_location ? 1u : 0u));
    if (range->has_location) {
        CHECK(found.latitude_microdegrees == range->latitude);
        CHECK(found.longitude_microdegrees == range->longitude);
    }
    if (context->universe != NULL && context->universe->count > 0) {
        CHECK(view != NULL);
        for (index = 0; index < context->universe->count; index++) {
            uint8_t contains = 0;
            CHECK(iprange_v4_abi1_membership_view_contains_index(
                      view, context->indices[index], &contains, &error) ==
                  IPRANGE_V4_ABI1_STATUS_OK);
            CHECK(error == NULL);
            CHECK(contains ==
                  (range_has_feed(range, context->universe->names[index]) ? 1u : 0u));
        }
        CHECK(iprange_v4_abi1_membership_view_close(view, &error) == IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(error == NULL);
        CHECK(iprange_v4_abi1_membership_view_destroy(view, &error) == IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(error == NULL);
    } else {
        CHECK(view == NULL);
    }
    (void)ordinal;
    return 0;
}

static int check_structured(const iprange_v4_abi1_reader *reader, const case_range *ranges,
                            size_t count, const feed_universe *universe, int *gaps)
{
    size_t index;
    uint32_t *indices = NULL;
    if (universe != NULL && universe->count > 0) {
        indices = calloc(universe->count, sizeof(*indices));
        CHECK(indices != NULL);
        for (index = 0; index < universe->count; index++) {
            iprange_v4_abi1_error *error = NULL;
            uint8_t present = 0;
            iprange_v4_abi1_feed_info info;
            const char *name = universe->names[index];
            memset(&info, 0, sizeof(info));
            CHECK(iprange_v4_abi1_reader_lookup_feed(reader, (const uint8_t *)name,
                                                      strlen(name), &present, &info,
                                                      &error) == IPRANGE_V4_ABI1_STATUS_OK);
            CHECK(error == NULL && present == 1);
            CHECK(strcmp((const char *)info.name, name) == 0 &&
                  info.name_length == strlen(name));
            if (universe->indices[index] != FEED_INDEX_ABSENT) {
                CHECK(info.index == universe->indices[index]);
            }
            indices[index] = info.index;
        }
    }
    for (index = 0; index < count; index++) {
        structured_context context;
        context.reader = reader;
        context.range = &ranges[index];
        context.universe = universe;
        context.indices = indices;
        CHECK(for_each_address(&ranges[index], visit_structured, &context, "structured") == 0);
        if (index + 1 == count) {
            continue;
        }
        {
            iprange_v4_abi1_error *error = NULL;
            uint8_t present = 1;
            iprange_v4_abi1_network_enrichment_v1 found;
            iprange_v4_abi1_ip hole = next_address(ranges[index].to);
            memset(&found, 0, sizeof(found));
            if (same_address(hole, ranges[index + 1].from)) {
                continue;
            }
            CHECK(iprange_v4_abi1_reader_lookup_network_enrichment_v1(
                      reader, hole, &present, &found, &error) == IPRANGE_V4_ABI1_STATUS_OK);
            CHECK(error == NULL && present == 0);
            (*gaps)++;
        }
    }
    free(indices);
    return 0;
}

static int check_value_tag(const iprange_v4_abi1_database_info *info, const char *tag)
{
    uint8_t want[16];
    size_t length = strlen(tag);
    if (length > 16) {
        return 1;
    }
    memset(want, 0, sizeof(want));
    memcpy(want, tag, length);
    return memcmp(info->value_tag, want, sizeof(want)) == 0 ? 0 : 1;
}

static int check_metadata(const iprange_v4_abi1_reader *reader, const char *slice,
                          const char *slice_end)
{
    char state[16];
    iprange_v4_abi1_error *error = NULL;
    uint8_t present = 0;
    uint64_t required = 0;
    CHECK(read_string("\"state\": \"", slice, slice_end, state, sizeof(state)) == 0);
    CHECK(iprange_v4_abi1_reader_metadata_query(reader, &present, &required, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    if (strcmp(state, "absent") == 0) {
        CHECK(present == 0);
        return 0;
    }
    CHECK(present == 1);
    if (strcmp(state, "empty") == 0) {
        CHECK(required == 0);
        return 0;
    }
    if (strcmp(state, "repeat") == 0) {
        uint64_t length = 0;
        unsigned long repeat_byte = 256; /* absent sentinel */
        CHECK(read_string("\"length\": ", slice, slice_end, state, sizeof(state)) == 1);
        {
            const char *found = find_key(slice, slice_end, "\"length\": ");
            char digits[24];
            const char *cursor;
            size_t written = 0;
            CHECK(found != NULL);
            cursor = found + strlen("\"length\": ");
            while (cursor < slice_end && *cursor >= '0' && *cursor <= '9' &&
                   written + 1 < sizeof(digits)) {
                digits[written++] = *cursor++;
            }
            digits[written] = '\0';
            length = strtoull(digits, NULL, 10);
        }
        {
            /* The manifest names the repeated byte; the length check
             * alone cannot see a wrong payload (astra turn-2). */
            const char *found = find_key(slice, slice_end, "\"byte\": ");
            if (found != NULL) {
                char digits[24];
                const char *cursor = found + strlen("\"byte\": ");
                size_t written = 0;
                while (cursor < slice_end && *cursor >= '0' && *cursor <= '9' &&
                       written + 1 < sizeof(digits)) {
                    digits[written++] = *cursor++;
                }
                digits[written] = '\0';
                CHECK(written > 0);
                repeat_byte = strtoul(digits, NULL, 10);
                CHECK(repeat_byte <= 255);
            }
        }
        CHECK(required == length);
        if (repeat_byte <= 255) {
            uint8_t *buffer = malloc(length > 0 ? (size_t)length : 1);
            uint64_t copied = 0;
            size_t index;
            CHECK(buffer != NULL);
            CHECK(iprange_v4_abi1_reader_metadata_read(
                      reader, ((iprange_v4_abi1_mutable_byte_slice){buffer, length}), &copied,
                      &error) == IPRANGE_V4_ABI1_STATUS_OK);
            CHECK(error == NULL);
            CHECK(copied == length);
            for (index = 0; index < length; index++) {
                CHECK(buffer[index] == (uint8_t)repeat_byte);
            }
            free(buffer);
        }
        return 0;
    }
    CHECK(strcmp(state, "text") == 0);
    {
        /* The manifest value is a JSON string with escaped quotes; read
         * it with backslash unescaping so the byte comparison is exact. */
        const char *found = find_key(slice, slice_end, "\"value\": \"");
        const char *cursor;
        char value[256];
        size_t length = 0;
        uint8_t buffer[256];
        uint64_t copied = 0;
        CHECK(found != NULL);
        cursor = found + strlen("\"value\": \"");
        while (cursor < slice_end && *cursor != '"' && length + 1 < sizeof(value)) {
            if (*cursor == '\\' && cursor + 1 < slice_end) {
                cursor++;
            }
            value[length++] = *cursor++;
        }
        value[length] = '\0';
        CHECK(cursor < slice_end && length > 0);
        CHECK(required == (uint64_t)length);
        CHECK(length <= sizeof(buffer));
        CHECK(iprange_v4_abi1_reader_metadata_read(
                  reader, ((iprange_v4_abi1_mutable_byte_slice){buffer, length}), &required,
                  &error) == IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(error == NULL);
        CHECK(memcmp(buffer, value, length) == 0);
        (void)copied;
    }
    return 0;
}

/* Borrow-safe span between two packed big-endian addresses: IPv4 fits
 * one 64-bit word (width < 8 packs wholly into the low word); IPv6
 * splits into hi/lo words with a borrow. Returns 0 and sets
 * *unrepresentable when the difference does not fit 64 bits. */
static int packed_span(const iprange_v4_abi1_ip *from, const iprange_v4_abi1_ip *to,
                       int width, unsigned long long *span, int *unrepresentable)
{
    unsigned long long to_high = 0, to_low = 0;
    unsigned long long from_high = 0, from_low = 0;
    unsigned long long borrow, low, high;
    int position;
    *unrepresentable = 0;
    *span = 0;
    for (position = 0; position < width; position++) {
        if (position < width - 8) {
            to_high = (to_high << 8) | to->bytes[position];
            from_high = (from_high << 8) | from->bytes[position];
        } else {
            to_low = (to_low << 8) | to->bytes[position];
            from_low = (from_low << 8) | from->bytes[position];
        }
    }
    borrow = from_low > to_low ? 1u : 0u;
    low = to_low - from_low;
    high = to_high - from_high - borrow;
    if (high != 0) {
        *unrepresentable = 1;
        return 0;
    }
    if (low + 1 == 0) {
        /* The full low word plus one wraps: unrepresentable. */
        *unrepresentable = 1;
        return 0;
    }
    *span = low + 1;
    return 0;
}

/* The five-shape pin for packed_span (r93): borrow, simple, v4
 * universe, v6 universe, v6 documentation range. Runs before the
 * corpus walk; a regression in the arithmetic fails the whole run. */
static int selftest_packed_span(void)
{
    iprange_v4_abi1_ip from, to;
    unsigned long long span;
    int unrepresentable;

    memset(&from, 0, sizeof(from));
    memset(&to, 0, sizeof(to));

    /* borrow: 10.0.0.5 -> 10.0.1.3 = 255 */
    from.family = to.family = IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4;
    from.bytes[0] = 10; to.bytes[0] = 10;
    to.bytes[2] = 1; to.bytes[3] = 3; from.bytes[3] = 5;
    CHECK(packed_span(&from, &to, 4, &span, &unrepresentable) == 0);
    CHECK(!unrepresentable && span == 255);

    /* simple: 10.0.0.1 -> 10.0.0.3 = 3 */
    from.bytes[3] = 1; to.bytes[2] = 0; to.bytes[3] = 3;
    CHECK(packed_span(&from, &to, 4, &span, &unrepresentable) == 0);
    CHECK(!unrepresentable && span == 3);

    /* v4 universe: 0.0.0.0 -> 255.255.255.255 = 2^32 */
    memset(from.bytes, 0, 16);
    memset(to.bytes, 0xff, 4);
    CHECK(packed_span(&from, &to, 4, &span, &unrepresentable) == 0);
    CHECK(!unrepresentable && span == 4294967296ull);

    /* v6 universe: :: -> ffff:...:ffff is unrepresentable */
    memset(from.bytes, 0, 16);
    memset(to.bytes, 0xff, 16);
    packed_span(&from, &to, 16, &span, &unrepresentable);
    CHECK(unrepresentable);

    /* v6 documentation range: 2001:db8::1 -> 2001:db8::ffff = 65535 */
    memset(from.bytes, 0, 16);
    memset(to.bytes, 0, 16);
    from.bytes[0] = 0x20; from.bytes[1] = 0x01; from.bytes[2] = 0x0d; from.bytes[3] = 0xb8;
    to.bytes[0] = 0x20; to.bytes[1] = 0x01; to.bytes[2] = 0x0d; to.bytes[3] = 0xb8;
    to.bytes[15] = 0xff; to.bytes[14] = 0xff; from.bytes[15] = 1;
    CHECK(packed_span(&from, &to, 16, &span, &unrepresentable) == 0);
    CHECK(!unrepresentable && span == 65535);

    /* cross-word borrow: the low word of `to` is numerically BELOW the
     * low word of `from` and the high word advances by one — the exact
     * shape the digit-wise form wrapped on (r91/r95). from high=1,
     * low=0x0000000000000100; to high=2, low=0. True span =
     * 2^64 - 0x100 + 1. */
    memset(from.bytes, 0, 16);
    memset(to.bytes, 0, 16);
    from.bytes[7] = 1; from.bytes[15] = 0x00; from.bytes[14] = 0x01;
    to.bytes[0] = 0; to.bytes[7] = 2;
    /* from = 0x00000001_00000000_00000000_00000100 → high=1, low=0x100
     * to   = 0x00000002_00000000_00000000_00000000 → high=2, low=0 */
    packed_span(&from, &to, 16, &span, &unrepresentable);
    CHECK(!unrepresentable && span == 0xffffffffffffff01ull);

    /* wrap arm: an exact 2^64 span (from = H:0, to = H:UINT64_MAX)
     * does not fit low+1 — the unrepresentable skip must fire, not
     * wrap to 0 (r97: the wrap arm was unpinned). */
    memset(from.bytes, 0, 16);
    memset(to.bytes, 0, 16);
    from.bytes[7] = 1;
    to.bytes[7] = 1;
    {
        int byte;
        for (byte = 8; byte < 16; byte++) {
            to.bytes[byte] = 0xff;
        }
    }
    packed_span(&from, &to, 16, &span, &unrepresentable);
    CHECK(unrepresentable);

    return 0;
}

static int check_fixture(const char *corpus, const char *slice, const char *slice_end, int *gaps)
{
    char file[128];
    char family[8];
    char kind[16];
    char tag[24];
    char path[512];
    int ipv6;
    uint32_t want_family;
    uint32_t want_kind;
    const char *range_key;
    int kind_code;
    case_range *ranges = NULL;
    size_t count = 0;
    feed_universe universe;
    uint32_t range_records = 0;
    iprange_v4_abi1_reader *reader = NULL;
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_database_info info;
    memset(&info, 0, sizeof(info));
    CHECK(read_string("\"file\": \"", slice, slice_end, file, sizeof(file)) == 0);
    CHECK(read_string("\"family\": \"", slice, slice_end, family, sizeof(family)) == 0);
    CHECK(read_string("\"kind\": \"", slice, slice_end, kind, sizeof(kind)) == 0);
    CHECK(read_string("\"tag\": \"", slice, slice_end, tag, sizeof(tag)) == 0);
    ipv6 = strcmp(family, "ipv6") == 0;
    want_family = ipv6 ? IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV6 : IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4;
    if (strcmp(kind, "direct") == 0) {
        want_kind = IPRANGE_V4_ABI1_VALUE_KIND_DIRECT;
        range_key = "\"direct_ranges\"";
        kind_code = 0;
    } else if (strcmp(kind, "membership") == 0) {
        want_kind = IPRANGE_V4_ABI1_VALUE_KIND_MEMBERSHIP;
        range_key = "\"membership_ranges\"";
        kind_code = 1;
    } else {
        CHECK(strcmp(kind, "structured") == 0);
        want_kind = IPRANGE_V4_ABI1_VALUE_KIND_STRUCTURED;
        range_key = "\"structured_ranges\"";
        kind_code = 2;
    }
    CHECK(snprintf(path, sizeof(path), "%s/%s", corpus, file) < (int)sizeof(path));
    CHECK(iprange_v4_abi1_open_immutable_reader(path_from(path), &reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    CHECK(iprange_v4_abi1_reader_database_info(reader, &info, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    CHECK(info.address_family == want_family);
    CHECK(info.value_kind == want_kind);
    CHECK(check_value_tag(&info, tag) == 0);
    CHECK(read_feed_universe(slice, slice_end, &universe) == 0);
    CHECK(info.active_feed_count == (uint64_t)universe.count);
    CHECK(collect_ranges(slice, slice_end, range_key, ipv6, kind_code, &ranges, &count) == 0);
    CHECK(count > 0);
    CHECK(info.range_record_count == (uint64_t)count);
    /* The manifest's address_count is the sum of the range spans. It
     * is checked wherever the total fits a uint64: a reader whose
     * covered address set differed from the manifest's would otherwise
     * pass with only the per-range walks agreeing with itself. Totals
     * beyond uint64 (the ipv6 whole-universe fixtures) stay out of
     * scope of this comparison; range_record_count and
     * active_feed_count remain the cardinality contract there. */
    {
        const char *found = find_key(slice, slice_end, "\"address_count\": \"");
        if (found != NULL) {
            char digits[48];
            const char *cursor = found + strlen("\"address_count\": \"");
            size_t written = 0;
            unsigned long long claimed;
            char *parse_end = NULL;
            while (cursor < slice_end && *cursor != '"' && written + 1 < sizeof(digits)) {
                digits[written++] = *cursor++;
            }
            digits[written] = '\0';
            CHECK(written > 0);
            errno = 0;
            claimed = strtoull(digits, &parse_end, 10);
            if (errno == 0 && parse_end != NULL && *parse_end == '\0') {
                /* Sum of the range spans, borrow-safe: subtract the
                 * packed big-endian addresses (IPv4 fits one 64-bit
                 * word; IPv6 splits into hi/lo with a borrow). A span
                 * — or the running total — that does not fit 64 bits
                 * (the whole-universe fixtures) makes the fixture's
                 * claimed total unverifiable at this width and skips
                 * the comparison; range_record_count remains its
                 * cardinality contract. */
                unsigned long long total = 0;
                int unrepresentable = 0;
                size_t index2;
                for (index2 = 0; index2 < count && !unrepresentable; index2++) {
                    unsigned long long span;
                    int span_unrepresentable;
                    int width = ipv6 ? 16 : 4;
                    packed_span(&ranges[index2].from, &ranges[index2].to, width,
                                &span, &span_unrepresentable);
                    if (span_unrepresentable) {
                        unrepresentable = 1;
                        break;
                    }
                    if (total > UINT64_MAX - span) {
                        /* the total would overflow: unverifiable at
                         * 64 bits. */
                        unrepresentable = 1;
                        break;
                    }
                    total += span;
                }
                if (!unrepresentable) {
                    CHECK(total == claimed);
                }
            }
        }
    }
    (void)range_records;
    if (kind_code == 0) {
        CHECK(check_direct(reader, ranges, count, gaps) == 0);
    } else if (kind_code == 1) {
        CHECK(check_membership(reader, ranges, count, &universe, gaps) == 0);
    } else {
        CHECK(check_structured(reader, ranges, count, &universe, gaps) == 0);
    }
    CHECK(check_metadata(reader, slice, slice_end) == 0);
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
    CHECK(selftest_packed_span() == 0);
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
    CHECK(gaps >= 4); /* the manifest holes exist and are verified absent */
    printf("cases=%d gaps=%d\n", opened, gaps);
    return 0;
}
