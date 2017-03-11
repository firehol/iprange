cmake_minimum_required(VERSION 3.3)
project(iprange C)

find_package (Threads)

set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} -Wall -Wextra -Wformat-signedness -Werror=format-security")

set(SOURCE_FILES
        config.h
        iprange.c ipset.c ipset.h iprange.h ipset_binary.c ipset_binary.h ipset_load.c ipset_load.h ipset_reduce.c ipset_print.c ipset_print.h ipset_optimize.c ipset_optimize.h ipset_reduce.h ipset_diff.c ipset_diff.h ipset_common.c ipset_common.h ipset_exclude.c ipset_exclude.h ipset_merge.c ipset_merge.h ipset_copy.c ipset_copy.h ipset_combine.c ipset_combine.h)

include_directories(AFTER .)
add_definitions("-DHAVE_CONFIG_H")

add_executable(iprange_git ${SOURCE_FILES})