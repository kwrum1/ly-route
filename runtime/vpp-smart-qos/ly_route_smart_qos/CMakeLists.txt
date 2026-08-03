include_directories(${CMAKE_SOURCE_DIR})

add_vpp_plugin(ly_route_smart_qos
  SOURCES
  smart_qos.c
  node.c

  LINK_LIBRARIES
  m

  COMPONENT ly-route-vpp-smart-qos
)
