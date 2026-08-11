#include "abi_test_support.h"

typedef struct {
    uint32_t threat_index;
    uint64_t records;
    int failed;
} enrichment_sink_state;

static int commit(iprange_v4_abi1_writer *writer)
{
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_commit_report facts = {0};
    CHECK(iprange_v4_abi1_writer_commit(writer, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL && report != NULL);
    CHECK(iprange_v4_abi1_report_get_commit(report, &facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(facts.durability == IPRANGE_V4_ABI1_COMMIT_DURABILITY_COMMITTED);
    CHECK(destroy_report(report) == 0);
    return 0;
}

static int check_borrowed_membership(
    const iprange_v4_abi1_borrowed_membership_view *view,
    uint32_t threat_index)
{
    iprange_v4_abi1_error *error = NULL;
    uint8_t contains = 0;
    CHECK(view != NULL);
    CHECK(iprange_v4_abi1_borrowed_membership_view_contains_index(
              view, threat_index, &contains, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL && contains == 1);
    return 0;
}

static int reject_value(
    iprange_v4_abi1_writer *writer,
    iprange_v4_abi1_network_enrichment_v1 value,
    uint32_t expected_code)
{
    iprange_v4_abi1_structure_ref *structure = NULL;
    iprange_v4_abi1_error *error = NULL;
    CHECK(iprange_v4_abi1_writer_network_enrichment_v1_intern(
              writer, value, NULL, &structure, &error) ==
          IPRANGE_V4_ABI1_STATUS_ERROR);
    CHECK(structure == NULL && error != NULL);
    if (error_code(error) == IPRANGE_V4_ABI1_ERROR_CODE_TRANSACTION_ABORTED) {
        const iprange_v4_abi1_error *cause = NULL;
        uint32_t cause_code = 0;
        uint8_t caller_present = 0;
        uint64_t caller_code = 0;
        CHECK(iprange_v4_abi1_error_cause(error, &cause) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(cause != NULL);
        CHECK(iprange_v4_abi1_error_code(
                  cause, &cause_code, &caller_present, &caller_code) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(cause_code == expected_code);
    } else {
        CHECK(error_code(error) == expected_code);
    }
    CHECK(destroy_error(error) == 0);
    return 0;
}

static uint32_t enrichment_sink(
    void *context,
    const iprange_v4_abi1_network_enrichment_v1_range *records,
    uint64_t count,
    iprange_v4_abi1_callback_failure *failure)
{
    enrichment_sink_state *state = context;
    uint64_t index;
    (void)failure;
    for (index = 0; index < count; index++) {
        const iprange_v4_abi1_network_enrichment_v1_range *record = &records[index];
        state->records++;
        if (record->value.reserved != 0 || record->value.has_location > 1) {
            state->failed = 1;
            return IPRANGE_V4_ABI1_SINK_OUTCOME_ERROR;
        }
        if (record->value.asn == 64512) {
            if (record->membership == NULL ||
                check_borrowed_membership(record->membership, state->threat_index) != 0) {
                state->failed = 1;
                return IPRANGE_V4_ABI1_SINK_OUTCOME_ERROR;
            }
        } else if (record->value.asn == 64513) {
            if (record->membership != NULL) {
                state->failed = 1;
                return IPRANGE_V4_ABI1_SINK_OUTCOME_ERROR;
            }
        } else {
            state->failed = 1;
            return IPRANGE_V4_ABI1_SINK_OUTCOME_ERROR;
        }
    }
    return IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE;
}

int main(int argc, char **argv)
{
    iprange_v4_abi1_byte_slice value_tag = {
        (const uint8_t *)"enrichment", 10};
    iprange_v4_abi1_transaction_budget budget = transaction_budget();
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_writer *writer = NULL;
    iprange_v4_abi1_reader *reader = NULL;
    iprange_v4_abi1_writer_feed_ref *feed = NULL;
    iprange_v4_abi1_membership_builder *builder = NULL;
    iprange_v4_abi1_membership_ref *membership = NULL;
    iprange_v4_abi1_structure_ref *with_threat = NULL;
    iprange_v4_abi1_structure_ref *scalar_only = NULL;
    iprange_v4_abi1_membership_view *view = NULL;
    iprange_v4_abi1_cursor *cursor = NULL;
    iprange_v4_abi1_feed_info feed_info = {0};
    iprange_v4_abi1_database_info info = {0};
    iprange_v4_abi1_network_enrichment_v1 value_a = {
        64512, 1, 2, 3, 37500000, 23700000, 1, 0};
    iprange_v4_abi1_network_enrichment_v1 value_b = {
        64513, 4, 5, 6, 0, 0, 0, 0};
    iprange_v4_abi1_network_enrichment_v1 found = {0};
    iprange_v4_abi1_network_enrichment_v1_range cursor_record = {0};
    iprange_v4_abi1_range ranges[2] = {
        ipv4_range(10, 20),
        ipv4_range(30, 40),
    };
    coverage_source source_a = {&ranges[0], 1, 0};
    coverage_source source_b = {&ranges[1], 1, 0};
    enrichment_sink_state sink = {0};
    uint8_t present = 0;
    uint8_t contains = 0;

    CHECK(argc == 2);
    CHECK(iprange_v4_abi1_create_live(
              path_from(argv[1]),
              IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4,
              IPRANGE_V4_ABI1_VALUE_KIND_DIRECT,
              IPRANGE_V4_ABI1_STRUCTURE_KIND_NETWORK_ENRICHMENT_V1,
              value_tag,
              4,
              no_cancellation(),
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_ERROR);
    CHECK(report == NULL && error != NULL);
    CHECK(error_code(error) == IPRANGE_V4_ABI1_ERROR_CODE_WRONG_STRUCTURE_KIND);
    CHECK(destroy_error(error) == 0);
    error = NULL;
    CHECK(iprange_v4_abi1_create_live(
              path_from(argv[1]),
              IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4,
              IPRANGE_V4_ABI1_VALUE_KIND_STRUCTURED,
              IPRANGE_V4_ABI1_STRUCTURE_KIND_NONE,
              value_tag,
              4,
              no_cancellation(),
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_ERROR);
    CHECK(report == NULL && error != NULL);
    CHECK(error_code(error) == IPRANGE_V4_ABI1_ERROR_CODE_WRONG_STRUCTURE_KIND);
    CHECK(destroy_error(error) == 0);
    error = NULL;
    CHECK(iprange_v4_abi1_create_live(
              path_from(argv[1]),
              IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4,
              IPRANGE_V4_ABI1_VALUE_KIND_STRUCTURED,
              UINT32_C(255),
              value_tag,
              4,
              no_cancellation(),
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_ERROR);
    CHECK(report == NULL && error != NULL);
    CHECK(error_code(error) == IPRANGE_V4_ABI1_ERROR_CODE_INVALID_ENUM);
    CHECK(destroy_error(error) == 0);
    error = NULL;
    CHECK(iprange_v4_abi1_create_live(
              path_from(argv[1]),
              IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4,
              IPRANGE_V4_ABI1_VALUE_KIND_STRUCTURED,
              IPRANGE_V4_ABI1_STRUCTURE_KIND_NETWORK_ENRICHMENT_V1,
              value_tag,
              4,
              no_cancellation(),
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL && report != NULL);
    CHECK(destroy_report(report) == 0);
    report = NULL;

    CHECK(iprange_v4_abi1_open_live_writer(
              path_from(argv[1]), &budget, no_cancellation(), &writer, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_begin_structured(
              writer, no_cancellation(), &error) == IPRANGE_V4_ABI1_STATUS_OK);
    {
        iprange_v4_abi1_network_enrichment_v1 invalid = value_a;
        invalid.reserved = 1;
        CHECK(reject_value(writer, invalid,
                           IPRANGE_V4_ABI1_ERROR_CODE_RESERVED_NONZERO) == 0);
        invalid = value_a;
        invalid.has_location = 2;
        CHECK(reject_value(writer, invalid,
                           IPRANGE_V4_ABI1_ERROR_CODE_INVALID_ENUM) == 0);
        invalid = value_b;
        invalid.latitude_microdegrees = 1;
        CHECK(reject_value(writer, invalid,
                           IPRANGE_V4_ABI1_ERROR_CODE_INVALID_ARGUMENT) == 0);
    }
    CHECK(iprange_v4_abi1_writer_feed_ensure(
              writer, (const uint8_t *)"threat", 6, &feed, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_feed_ref_info(feed, &feed_info, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_membership_builder_create(
              writer, &builder, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_builder_add_feed(builder, feed, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_builder_finish(builder, &membership, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_network_enrichment_v1_intern(
              writer, value_a, membership, &with_threat, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_network_enrichment_v1_intern(
              writer, value_b, NULL, &scalar_only, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_structured_assign_ranges(
              writer, with_threat, coverage_callback, &source_a, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_structured_assign_ranges(
              writer, scalar_only, coverage_callback, &source_b, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);

    source_b.offset = 0;
    CHECK(iprange_v4_abi1_writer_structured_clear_ranges(
              writer, coverage_callback, &source_b, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    source_b.offset = 0;
    CHECK(iprange_v4_abi1_writer_structured_assign_ranges(
              writer, scalar_only, coverage_callback, &source_b, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);

    CHECK(iprange_v4_abi1_structure_ref_destroy(with_threat, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_structure_ref_destroy(scalar_only, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_ref_destroy(membership, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_builder_destroy(builder, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_feed_ref_destroy(feed, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(commit(writer) == 0);
    CHECK(iprange_v4_abi1_writer_close(writer, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(report) == 0);
    report = NULL;
    CHECK(iprange_v4_abi1_writer_destroy(writer, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);

    CHECK(iprange_v4_abi1_open_live_reader(
              path_from(argv[1]), no_cancellation(), &reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_reader_database_info(reader, &info, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(info.value_kind == IPRANGE_V4_ABI1_VALUE_KIND_STRUCTURED);
    CHECK(info.structure_kind ==
          IPRANGE_V4_ABI1_STRUCTURE_KIND_NETWORK_ENRICHMENT_V1);

    CHECK(iprange_v4_abi1_reader_lookup_network_enrichment_v1(
              reader, ipv4(15), &present, &found, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(present == 1 && found.asn == value_a.asn && found.has_location == 1);
    CHECK(iprange_v4_abi1_reader_lookup_network_enrichment_v1_with_membership(
              reader, ipv4(15), &present, &found, &view, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(present == 1 && view != NULL);
    CHECK(iprange_v4_abi1_membership_view_contains_index(
              view, feed_info.index, &contains, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(contains == 1);
    CHECK(iprange_v4_abi1_membership_view_close(view, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_view_destroy(view, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    view = NULL;

    CHECK(iprange_v4_abi1_reader_lookup_network_enrichment_v1_with_membership(
              reader, ipv4(35), &present, &found, &view, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(present == 1 && found.asn == value_b.asn && view == NULL);

    CHECK(iprange_v4_abi1_reader_open_network_enrichment_v1_cursor(
              reader, IPRANGE_V4_ABI1_CURSOR_DIRECTION_FORWARD, NULL, &cursor,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_cursor_next_network_enrichment_v1(
              cursor, &present, &cursor_record, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(present == 1 && cursor_record.value.asn == value_a.asn);
    CHECK(check_borrowed_membership(cursor_record.membership, feed_info.index) == 0);
    CHECK(iprange_v4_abi1_cursor_next_network_enrichment_v1(
              cursor, &present, &cursor_record, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(present == 1 && cursor_record.value.asn == value_b.asn &&
          cursor_record.membership == NULL);
    CHECK(iprange_v4_abi1_cursor_close(cursor, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_cursor_destroy(cursor, &error) == IPRANGE_V4_ABI1_STATUS_OK);

    sink.threat_index = feed_info.index;
    CHECK(iprange_v4_abi1_reader_scan_network_enrichment_v1(
              reader,
              IPRANGE_V4_ABI1_CURSOR_DIRECTION_FORWARD,
              NULL,
              no_cancellation(),
              enrichment_sink,
              &sink,
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(sink.records == 2 && sink.failed == 0);
    CHECK(destroy_report(report) == 0);
    report = NULL;

    CHECK(iprange_v4_abi1_reader_close(reader, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_reader_destroy(reader, &error) == IPRANGE_V4_ABI1_STATUS_OK);

    writer = NULL;
    CHECK(iprange_v4_abi1_open_live_writer(
              path_from(argv[1]), &budget, no_cancellation(), &writer, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_begin_structured(
              writer, no_cancellation(), &error) == IPRANGE_V4_ABI1_STATUS_OK);
    {
        iprange_v4_abi1_network_enrichment_v1 invalid = value_a;
        invalid.latitude_microdegrees = 90000001;
        CHECK(reject_value(writer, invalid,
                           IPRANGE_V4_ABI1_ERROR_CODE_INVALID_ARGUMENT) == 0);
    }
    CHECK(iprange_v4_abi1_writer_close(writer, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(report) == 0);
    CHECK(iprange_v4_abi1_writer_destroy(writer, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    return 0;
}
