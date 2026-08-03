#ifndef included_ly_route_security_guard_h
#define included_ly_route_security_guard_h

#include <vlib/vlib.h>
#include <vnet/vnet.h>

#define LY_SG_MAX_RULES 128
#define LY_SG_RULE_ID_LEN 64
#define LY_SG_INVALID_INDEX ((u32) ~0)

typedef enum
{
  LY_SG_ATTACK_SYN_FLOOD,
  LY_SG_ATTACK_NEW_CONNECTION_RATE,
  LY_SG_ATTACK_UDP_FLOOD,
  LY_SG_ATTACK_ICMP_FLOOD,
} ly_sg_attack_type_t;

typedef enum
{
  LY_SG_ALERT,
  LY_SG_ENFORCE,
} ly_sg_enforcement_t;

typedef struct
{
  u8 enabled;
  u8 family;
  u8 source_prefix_len;
  u8 destination_prefix_len;
  u32 sw_if_index;
  u8 source[16];
  u8 destination[16];
  u32 threshold_pps;
  u32 burst_packets;
  ly_sg_attack_type_t attack_type;
  ly_sg_enforcement_t enforcement;
  char id[LY_SG_RULE_ID_LEN];
  f64 tokens;
  f64 last_update;
  u64 matched;
  u64 conform;
  u64 exceed;
  u64 alerts;
  u64 drops;
  clib_spinlock_t lock;
} ly_sg_rule_t;

typedef struct
{
  vlib_main_t *vlib_main;
  vnet_main_t *vnet_main;
  ly_sg_rule_t rules[LY_SG_MAX_RULES];
  u32 rule_count;
  u32 error_drop_next_index_ip4;
  u32 error_drop_next_index_ip6;
} ly_sg_main_t;

extern ly_sg_main_t ly_sg_main;
extern vlib_node_registration_t ly_sg_ip4_node;
extern vlib_node_registration_t ly_sg_ip6_node;

#endif
