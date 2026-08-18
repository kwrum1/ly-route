cmake_minimum_required(VERSION 3.16)
project(ly-route-vpp-dns-intercept C)

set(CMAKE_LIBRARY_OUTPUT_DIRECTORY ${CMAKE_BINARY_DIR}/lib)
include_directories(${CMAKE_SOURCE_DIR})
find_package(VPP REQUIRED)
add_subdirectory(ly_route_dns_intercept)
