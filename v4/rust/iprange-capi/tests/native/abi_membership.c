#include "abi_test_support.h"

typedef struct {
    uint64_t batches;
    uint64_t records;
    uint32_t alpha;
    uint32_t gamma;
    int failed;
} sink_state;

static uint32_t feed_sink(void *context,
                          const iprange_v4_abi1_feed_info *records,
                          uint64_t count,
                          iprange_v4_abi1_callback_failure *failure)
{
    sink_state *state = context;
    uint64_t index;
    (void)failure;
    state->batches++;
    state->records += count;
    for (index = 0; index < count; index++) {
        if (records[index].name_length == 5 &&
            memcmp(records[index].name, "alpha", 5) == 0) {
            state->alpha = records[index].index;
        } else if (records[index].name_length == 5 &&
                   memcmp(records[index].name, "gamma", 5) == 0) {
            state->gamma = records[index].index;
        } else {
            state->failed = 1;
        }
    }
    return IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE;
}

static uint32_t coverage_sink(void *context,
                              const iprange_v4_abi1_range *records,
                              uint64_t count,
                              iprange_v4_abi1_callback_failure *failure)
{
    sink_state *state = context;
    (void)records;
    (void)failure;
    state->batches++;
    state->records += count;
    return IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE;
}

static uint32_t membership_sink(
    void *context,
    const iprange_v4_abi1_membership_range *records,
    uint64_t count,
    iprange_v4_abi1_callback_failure *failure)
{
    sink_state *state = context;
    iprange_v4_abi1_error *error = NULL;
    uint64_t index;
    (void)failure;
    state->batches++;
    state->records += count;
    for (index = 0; index < count; index++) {
        uint32_t words = 0;
        uint8_t present = 0;
        uint8_t contains = 0;
        uint64_t word = 0;
        uint64_t copied = 0;
        uint64_t output[2] = {0};
        const iprange_v4_abi1_borrowed_membership_view *view =
            records[index].membership;
        if (iprange_v4_abi1_borrowed_membership_view_word_count(
                view, &words, &error) != IPRANGE_V4_ABI1_STATUS_OK ||
            words == 0 ||
            iprange_v4_abi1_borrowed_membership_view_word(
                view, 0, &present, &word, &error) != IPRANGE_V4_ABI1_STATUS_OK ||
            present != 1 ||
            iprange_v4_abi1_borrowed_membership_view_read_words(
                view, 0, output, 2, &copied, &error) !=
                IPRANGE_V4_ABI1_STATUS_OK ||
            copied == 0 ||
            iprange_v4_abi1_borrowed_membership_view_contains_index(
                view, state->alpha, &contains, &error) !=
                IPRANGE_V4_ABI1_STATUS_OK ||
            contains != 1) {
            state->failed = 1;
            if (error != NULL) {
                (void)iprange_v4_abi1_error_destroy(error);
            }
            return IPRANGE_V4_ABI1_SINK_OUTCOME_ERROR;
        }
    }
    return IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE;
}

static int commit(iprange_v4_abi1_writer *writer)
{
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_commit_report facts = {0};
    CHECK(iprange_v4_abi1_writer_commit(writer, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    CHECK(iprange_v4_abi1_report_get_commit(report, &facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(facts.durability == IPRANGE_V4_ABI1_COMMIT_DURABILITY_COMMITTED);
    CHECK(destroy_report(report) == 0);
    return 0;
}

static int expect_membership(iprange_v4_abi1_reader *reader,
                             uint32_t address,
                             uint32_t alpha,
                             uint32_t gamma,
                             uint8_t expected)
{
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_membership_view *view = NULL;
    uint8_t contains = 0;
    uint8_t word_present = 0;
    uint32_t word_count = 0;
    uint64_t word = 0;
    uint64_t words[2] = {0};
    uint64_t copied = 0;
    CHECK(iprange_v4_abi1_reader_lookup_membership(
              reader, ipv4(address), &view, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    if (!expected) {
        CHECK(view == NULL);
        return 0;
    }
    CHECK(view != NULL);
    CHECK(iprange_v4_abi1_membership_view_word_count(
              view, &word_count, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(word_count > 0);
    CHECK(iprange_v4_abi1_membership_view_word(
              view, 0, &word_present, &word, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(word_present == 1);
    CHECK(iprange_v4_abi1_membership_view_read_words(
              view, 0, words, 2, &copied, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(copied > 0 && words[0] == word);
    CHECK(iprange_v4_abi1_membership_view_contains_index(
              view, alpha, &contains, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(contains == 1);
    CHECK(iprange_v4_abi1_membership_view_contains_index(
              view, gamma, &contains, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(contains == 1);
    CHECK(iprange_v4_abi1_membership_view_close(view, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_view_destroy(view, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    return 0;
}

int main(int argc, char **argv)
{
    static const uint8_t tag[] = "bitmap";
    static const uint8_t metadata[] = "{\"source\":\"native-c\"}";
    iprange_v4_abi1_byte_slice value_tag = {tag, sizeof(tag) - 1};
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_writer *writer = NULL;
    iprange_v4_abi1_reader *reader = NULL;
    iprange_v4_abi1_writer_feed_ref *alpha = NULL;
    iprange_v4_abi1_writer_feed_ref *alpha_lookup = NULL;
    iprange_v4_abi1_writer_feed_ref *beta = NULL;
    iprange_v4_abi1_writer_feed_ref *gamma = NULL;
    iprange_v4_abi1_writer_feed_ref *discard = NULL;
    iprange_v4_abi1_membership_builder *builder = NULL;
    iprange_v4_abi1_membership_ref *membership = NULL;
    iprange_v4_abi1_transaction_budget budget = transaction_budget();
    iprange_v4_abi1_feed_info alpha_info = {0};
    iprange_v4_abi1_feed_info gamma_info = {0};
    iprange_v4_abi1_range ranges[5] = {
        ipv4_range(10, 20),
        ipv4_range(30, 40),
        ipv4_range(12, 13),
        ipv4_range(18, 25),
        ipv4_range(35, 45),
    };
    coverage_source sources[5];
    sink_state sink = {0};
    uint64_t feed_count = 0;
    uint8_t changed = 0;
    uint8_t present = 0;
    uint64_t required = 0;
    uint8_t metadata_copy[64] = {0};
    uint32_t operation;
    uint32_t index;

    CHECK(argc == 3);
    CHECK(iprange_v4_abi1_create_live(
              path_from(argv[1]),
              IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4,
              IPRANGE_V4_ABI1_VALUE_KIND_MEMBERSHIP,
              value_tag,
              4,
              no_cancellation(),
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    CHECK(destroy_report(report) == 0);
    report = NULL;

    CHECK(iprange_v4_abi1_open_live_writer(
              path_from(argv[1]), &budget, no_cancellation(), &writer, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_begin_membership(
              writer, no_cancellation(), &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_feed_ensure(
              writer, (const uint8_t *)"alpha", 5, &alpha, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_feed_ensure(
              writer, (const uint8_t *)"beta", 4, &beta, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_feed_ensure(
              writer, (const uint8_t *)"discard", 7, &discard, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_feed_lookup(
              writer, (const uint8_t *)"alpha", 5, &alpha_lookup, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(alpha_lookup != NULL);
    CHECK(iprange_v4_abi1_writer_feed_lookup(
              writer, (const uint8_t *)"missing", 7, &gamma, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(gamma == NULL);
    CHECK(iprange_v4_abi1_writer_feed_ref_info(alpha, &alpha_info, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_feed_rename(
              writer, beta, (const uint8_t *)"gamma", 5, &gamma, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_feed_ref_info(gamma, &gamma_info, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_feed_delete(writer, discard, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_feed_enumerate(
              writer, feed_sink, &sink, &feed_count, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(feed_count == 2 && sink.records == 2 && sink.failed == 0);

    CHECK(iprange_v4_abi1_writer_membership_builder_create(
              writer, &builder, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_builder_add_feed(builder, alpha, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_builder_add_feed(builder, gamma, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_builder_finish(
              builder, &membership, &error) == IPRANGE_V4_ABI1_STATUS_OK);

    for (index = 0; index < 5; index++) {
        sources[index].records = &ranges[index];
        sources[index].length = 1;
        sources[index].offset = 0;
    }
    operation = IPRANGE_V4_ABI1_MEMBERSHIP_OPERATION_UNION;
    CHECK(iprange_v4_abi1_writer_membership_apply_ranges(
              writer, membership, operation, coverage_callback, &sources[0], &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    operation = IPRANGE_V4_ABI1_MEMBERSHIP_OPERATION_REPLACE;
    CHECK(iprange_v4_abi1_writer_membership_apply_ranges(
              writer, membership, operation, coverage_callback, &sources[1], &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    operation = IPRANGE_V4_ABI1_MEMBERSHIP_OPERATION_DIFFERENCE;
    CHECK(iprange_v4_abi1_writer_membership_apply_ranges(
              writer, membership, operation, coverage_callback, &sources[2], &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    operation = IPRANGE_V4_ABI1_MEMBERSHIP_OPERATION_INTERSECTION;
    CHECK(iprange_v4_abi1_writer_membership_apply_ranges(
              writer, membership, operation, coverage_callback, &sources[3], &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    operation = IPRANGE_V4_ABI1_MEMBERSHIP_OPERATION_XOR;
    CHECK(iprange_v4_abi1_writer_membership_apply_ranges(
              writer, membership, operation, coverage_callback, &sources[4], &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);

    CHECK(iprange_v4_abi1_writer_set_metadata_json(
              writer,
              metadata,
              sizeof(metadata) - 1,
              no_cancellation(),
              &changed,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(changed == 1);
    CHECK(iprange_v4_abi1_writer_metadata_query(
              writer, &present, &required, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(present == 1 && required == sizeof(metadata) - 1);
    CHECK(iprange_v4_abi1_writer_metadata_read(
              writer,
              (iprange_v4_abi1_mutable_byte_slice){metadata_copy, sizeof(metadata_copy)},
              &required,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(memcmp(metadata_copy, metadata, sizeof(metadata) - 1) == 0);

    CHECK(iprange_v4_abi1_membership_ref_destroy(membership, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_builder_destroy(builder, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_feed_ref_destroy(alpha_lookup, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_feed_ref_destroy(discard, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_feed_ref_destroy(beta, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_feed_ref_destroy(gamma, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_feed_ref_destroy(alpha, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(commit(writer) == 0);

    CHECK(iprange_v4_abi1_writer_close(writer, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(report) == 0);
    report = NULL;
    CHECK(iprange_v4_abi1_writer_destroy(writer, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    writer = NULL;

    CHECK(iprange_v4_abi1_open_live_reader(
              path_from(argv[1]), no_cancellation(), &reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(expect_membership(
              reader, 10, alpha_info.index, gamma_info.index, 1) == 0);
    CHECK(expect_membership(
              reader, 12, alpha_info.index, gamma_info.index, 0) == 0);
    CHECK(expect_membership(
              reader, 19, alpha_info.index, gamma_info.index, 1) == 0);
    CHECK(expect_membership(
              reader, 37, alpha_info.index, gamma_info.index, 0) == 0);
    CHECK(expect_membership(
              reader, 43, alpha_info.index, gamma_info.index, 1) == 0);

    CHECK(iprange_v4_abi1_reader_lookup_feed(
              reader,
              (const uint8_t *)"alpha",
              5,
              &present,
              &alpha_info,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(present == 1);
    CHECK(iprange_v4_abi1_reader_metadata_query(
              reader, &present, &required, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(present == 1 && required == sizeof(metadata) - 1);
    memset(metadata_copy, 0, sizeof(metadata_copy));
    CHECK(iprange_v4_abi1_reader_metadata_read(
              reader,
              (iprange_v4_abi1_mutable_byte_slice){metadata_copy, sizeof(metadata_copy)},
              &required,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(memcmp(metadata_copy, metadata, sizeof(metadata) - 1) == 0);

    sink = (sink_state){0};
    CHECK(iprange_v4_abi1_reader_enumerate_feeds(
              reader, no_cancellation(), feed_sink, &sink, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(sink.records == 2 && sink.failed == 0);
    CHECK(destroy_report(report) == 0);
    report = NULL;
    sink.alpha = alpha_info.index;
    sink.gamma = gamma_info.index;
    CHECK(iprange_v4_abi1_reader_scan_membership(
              reader,
              IPRANGE_V4_ABI1_CURSOR_DIRECTION_FORWARD,
              NULL,
              no_cancellation(),
              membership_sink,
              &sink,
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(sink.records > 0 && sink.failed == 0);
    CHECK(destroy_report(report) == 0);
    report = NULL;
    sink = (sink_state){0};
    CHECK(iprange_v4_abi1_reader_scan_feed(
              reader,
              (const uint8_t *)"alpha",
              5,
              IPRANGE_V4_ABI1_CURSOR_DIRECTION_BACKWARD,
              NULL,
              no_cancellation(),
              coverage_sink,
              &sink,
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(sink.records > 0);
    CHECK(destroy_report(report) == 0);
    report = NULL;

    {
        iprange_v4_abi1_cursor *cursor = NULL;
        iprange_v4_abi1_membership_range item = {0};
        uint8_t item_present = 0;
        uint8_t contains = 0;
        CHECK(iprange_v4_abi1_reader_open_membership_cursor(
                  reader,
                  IPRANGE_V4_ABI1_CURSOR_DIRECTION_FORWARD,
                  NULL,
                  &cursor,
                  &error) == IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(iprange_v4_abi1_cursor_next_membership(
                  cursor, &item_present, &item, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(item_present == 1 && item.membership != NULL);
        CHECK(iprange_v4_abi1_borrowed_membership_view_contains_index(
                  item.membership, alpha_info.index, &contains, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(contains == 1);
        CHECK(iprange_v4_abi1_cursor_close(cursor, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(iprange_v4_abi1_cursor_destroy(cursor, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);

        CHECK(iprange_v4_abi1_reader_open_feed_cursor(
                  reader,
                  (const uint8_t *)"gamma",
                  5,
                  IPRANGE_V4_ABI1_CURSOR_DIRECTION_BACKWARD,
                  NULL,
                  &cursor,
                  &error) == IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(iprange_v4_abi1_cursor_next_coverage(
                  cursor, &item_present, &ranges[0], &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(item_present == 1);
        CHECK(iprange_v4_abi1_cursor_close(cursor, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(iprange_v4_abi1_cursor_destroy(cursor, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
    }

    CHECK(iprange_v4_abi1_reader_close(reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_reader_destroy(reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    reader = NULL;

    {
        iprange_v4_abi1_snapshot_budget snapshot = {0};
        snapshot.abi_version = IPRANGE_V4_ABI1_ABI_VERSION;
        snapshot.struct_size = sizeof(snapshot);
        snapshot.max_heap_bytes = 8 * 1024 * 1024;
        snapshot.max_output_pages = 65536;
        snapshot.max_open_files = 4;
        CHECK(iprange_v4_abi1_snapshot_to(
                  path_from(argv[1]),
                  IPRANGE_V4_ABI1_OPEN_MODE_LIVE,
                  path_from(argv[2]),
                  IPRANGE_V4_ABI1_DESTINATION_POLICY_FAIL_IF_EXISTS,
                  &snapshot,
                  no_cancellation(),
                  &report,
                  &error) == IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(destroy_report(report) == 0);
        report = NULL;
    }
    CHECK(iprange_v4_abi1_open_immutable_reader(
              path_from(argv[2]), &reader, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(expect_membership(
              reader, 43, alpha_info.index, gamma_info.index, 1) == 0);
    CHECK(iprange_v4_abi1_reader_close(reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_reader_destroy(reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    return 0;
}
