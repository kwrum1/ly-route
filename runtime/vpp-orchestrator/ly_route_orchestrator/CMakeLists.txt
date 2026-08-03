include_directories(${CMAKE_SOURCE_DIR})

add_vpp_plugin(ly_route_orchestrator
  SOURCES
  orchestrator.c
  node.c

  COMPONENT ly-route-vpp-orchestrator
)
