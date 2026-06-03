cmake_minimum_required(VERSION 3.10)
project(iprange C)

find_package(Threads REQUIRED)

include(CheckTypeSize)
check_type_size("__uint128_t" UINT128_SIZE LANGUAGE C)
if(HAVE_UINT128_SIZE)
    message(STATUS "Native __uint128_t detected — using hardware 128-bit integers for IPv6")
else()
    message(STATUS "__uint128_t not available — using portable 128-bit arithmetic for IPv6")
endif()

# Generate config.h with the defines the source expects
include(CheckIncludeFile)
check_include_file(inttypes.h HAVE_INTTYPES_H)
check_include_file(stdint.h HAVE_STDINT_H)

include(CheckCSourceCompiles)
set(IPRANGE_CMAKE_REQUIRED_DEFINITIONS "${CMAKE_REQUIRED_DEFINITIONS}")
set(CMAKE_REQUIRED_DEFINITIONS "-D_FILE_OFFSET_BITS=64" "-D_TIME_BITS=64")
check_c_source_compiles("
#include <sys/stat.h>
#include <time.h>
typedef char time_t_must_be_64_bits[(sizeof(time_t) >= 8) ? 1 : -1];
int main(void) {
    struct stat st;
    return sizeof(st.st_mtime) >= 8 ? 0 : 1;
}
" HAVE_64BIT_TIME_WITH_FEATURE_MACROS)
set(CMAKE_REQUIRED_DEFINITIONS "${IPRANGE_CMAKE_REQUIRED_DEFINITIONS}")
unset(IPRANGE_CMAKE_REQUIRED_DEFINITIONS)

configure_file(
    ${CMAKE_CURRENT_SOURCE_DIR}/cmake/config.h.in
    ${CMAKE_CURRENT_BINARY_DIR}/config.h
)

set(SOURCES
    src/iprange.c
    src/iprange6_main.c
    src/ipset.c
    src/ipset6.c
    src/ipset6_binary.c
    src/ipset6_combine.c
    src/ipset6_common.c
    src/ipset6_copy.c
    src/ipset6_diff.c
    src/ipset6_dns.c
    src/ipset6_exclude.c
    src/ipset6_load.c
    src/ipset6_merge.c
    src/ipset6_optimize.c
    src/ipset6_print.c
    src/ipset_binary.c
    src/ipset_combine.c
    src/ipset_common.c
    src/ipset_copy.c
    src/ipset_diff.c
    src/ipset_dns.c
    src/ipset_exclude.c
    src/ipset_load.c
    src/ipset_merge.c
    src/ipset_optimize.c
    src/ipset_print.c
    src/ipset_reduce.c
)

add_executable(iprange ${SOURCES})
target_include_directories(iprange PRIVATE
    ${CMAKE_CURRENT_BINARY_DIR}
    ${CMAKE_CURRENT_SOURCE_DIR}/src
)
target_compile_definitions(iprange PRIVATE
    HAVE_CONFIG_H
    COMPARE_WITH_COMMON=1
    VERSION="${PROJECT_VERSION}"
)
if(HAVE_64BIT_TIME_WITH_FEATURE_MACROS)
    target_compile_definitions(iprange PRIVATE
        _FILE_OFFSET_BITS=64
        _TIME_BITS=64
    )
endif()
target_compile_options(iprange PRIVATE
    -Wall -Wextra -Wformat-signedness -Werror=format-security
)
target_link_libraries(iprange Threads::Threads)
