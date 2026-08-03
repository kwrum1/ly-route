cmake_minimum_required(VERSION 3.16)
project(ly-route-orchestrator C)

set(CMAKE_LIBRARY_OUTPUT_DIRECTORY ${CMAKE_BINARY_DIR}/lib)

find_package(VPP REQUIRED)
add_subdirectory(ly_route_orchestrator)
