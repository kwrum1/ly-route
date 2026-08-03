#ifndef included_ly_route_orchestrator_h
#define included_ly_route_orchestrator_h

#include <vnet/vnet.h>
#include <vnet/ip/ip.h>
#include <vnet/ethernet/ethernet.h>

#define LY_ORCH_NAME_LEN 64
#define LY_ORCH_GENERATION_LEN 80
#define LY_ORCH_MAX_HOPS 8
#define LY_ORCH_FLOW_SLOTS 8192
#define LY_ORCH_FLOW_PROBES 16
#define LY_ORCH_FLOW_TTL_SECONDS 300.0
#define LY_ORCH_INVALID_INDEX ((u32) ~0)

typedef enum
{
  LY_ORCH_ACTION_VIA,
  LY_ORCH_ACTION_DIRECT,
  LY_ORCH_ACTION_DROP,
} ly_orch_action_t;

typedef struct
{
  u8 name[LY_ORCH_NAME_LEN];
  u32 wan_sw_if_index;
  u32 lan_sw_if_index;
  u32 wan_next_index;
  u32 lan_next_index;
} ly_orch_group_t;

typedef struct
{
  u8 id[LY_ORCH_NAME_LEN];
  u32 group_position;
  u32 sequence;
  u32 target_group;
  u8 is_ip6;
  u8 protocol;
  u8 source_prefix_length;
  u8 destination_prefix_length;
  ip46_address_t source;
  ip46_address_t destination;
  u16 source_port_start;
  u16 source_port_end;
  u16 destination_port_start;
  u16 destination_port_end;
  ly_orch_action_t action;
} ly_orch_rule_t;

typedef struct
{
  u32 wan_sw_if_index;
  u32 lan_sw_if_index;
  u32 wan_next_index;
  u32 lan_next_index;
  ly_orch_group_t *groups;
  ly_orch_rule_t *rules;
  ly_orch_action_t default_action;
  u8 generation[LY_ORCH_GENERATION_LEN];
  u8 valid;
} ly_orch_config_t;

typedef struct
{
  u64 packets;
  u64 bytes;
} ly_orch_rule_counter_t;

typedef struct
{
  ip46_address_t source;
  ip46_address_t destination;
  u16 source_port;
  u16 destination_port;
  u8 protocol;
  u8 is_ip6;
  u8 group_count;
  u8 occupied;
  u32 groups[LY_ORCH_MAX_HOPS];
  u64 packets;
  u64 bytes;
  f64 last_seen;
} ly_orch_flow_t;

typedef struct
{
  ly_orch_rule_counter_t *rule_counters;
  u64 *group_bypass_packets;
  ly_orch_flow_t *flows;
} ly_orch_worker_t;

typedef struct
{
  vlib_main_t *vlib_main;
  vnet_main_t *vnet_main;
  ly_orch_config_t active;
  ly_orch_config_t candidate;
  ly_orch_worker_t *workers;
  u8 *enabled_by_sw_if_index;
} ly_orch_main_t;

extern ly_orch_main_t ly_orch_main;
extern vlib_node_registration_t ly_orch_node;

void ly_orch_config_free (ly_orch_config_t *config);
u32 ly_orch_output_next (u32 sw_if_index);

#endif
