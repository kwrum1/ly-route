#ifndef included_ly_route_smart_qos_h
#define included_ly_route_smart_qos_h

#include <vnet/vnet.h>
#include <vnet/ip/ip.h>
#include <vnet/ethernet/ethernet.h>

#define LY_SQ_FLOW_COUNT 1024
#define LY_SQ_HOST_COUNT 256
#define LY_SQ_DEFAULT_SLOTS 4096
#define LY_SQ_QUANTUM 1514
#define LY_SQ_TARGET_SECONDS 0.005
#define LY_SQ_INTERVAL_SECONDS 0.100
#define LY_SQ_MAX_BURST 64
#define LY_SQ_INVALID_INDEX ((u32) ~0)
#define LY_SQ_HASH_SEED 0x6c797371
#define LY_SQ_FEATURE_CONFIG_MAGIC 0x6c797366

/* The output feature runs after a tunnel has selected its logical interface.
 * Carry the physical interface selected by the control plane in the feature
 * configuration so delayed packets can always resume on the real carrier. */
typedef struct
{
  u32 sw_if_index;
  u32 magic;
} ly_sq_feature_config_t;

typedef enum
{
  LY_SQ_HOST_SOURCE,
  LY_SQ_HOST_DESTINATION,
} ly_sq_host_isolation_t;

typedef struct
{
  u32 buffer_index;
  u32 next;
  u32 length;
  u32 output_next_index;
  f64 enqueue_time;
} ly_sq_entry_t;

typedef struct
{
  u32 head;
  u32 tail;
  u32 packets;
  u32 bytes;
  i32 deficit;
  u32 next_active;
  u32 host_index;
  u8 active;
  u8 dropping;
  u32 drop_count;
  f64 first_above_time;
  f64 drop_next;
} ly_sq_flow_t;

typedef struct
{
  u32 flow_head;
  u32 flow_tail;
  i32 deficit;
  u32 next_active;
  u8 active;
} ly_sq_host_t;

typedef struct
{
  u32 sw_if_index;
  u32 output_next_index;
  u32 free_head;
  u32 slots;
  u32 queued_packets;
  u32 queued_bytes;
  u32 active_head;
  u32 active_tail;
  u32 fattest_flow;
  u64 rate_bps;
  f64 tokens;
  f64 last_token_time;
  u64 enqueued;
  u64 transmitted;
  u64 aqm_drops;
  u64 overflow_drops;
  u8 enabled;
  ly_sq_host_isolation_t host_isolation;
  ly_sq_entry_t *entries;
  ly_sq_flow_t *flows;
  ly_sq_host_t *hosts;
} ly_sq_scheduler_t;

typedef struct
{
  vlib_main_t *vlib_main;
  vnet_main_t *vnet_main;
  u16 arc_index;
  u32 frame_queue_index;
  u16 scheduler_thread_index;
  ly_sq_scheduler_t **schedulers_by_thread;
  u8 *enabled_by_sw_if_index;
  u64 *rate_by_sw_if_index;
} ly_sq_main_t;

extern ly_sq_main_t ly_sq_main;
extern vlib_node_registration_t ly_sq_feature_node;
extern vlib_node_registration_t ly_sq_enqueue_node;
extern vlib_node_registration_t ly_sq_scheduler_node;

int ly_sq_enable_disable (u32 sw_if_index, u64 rate_kbps,
                          ly_sq_host_isolation_t host_isolation, int enable);

#endif
