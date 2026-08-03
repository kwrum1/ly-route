#include <math.h>
#include <vnet/feature/feature.h>
#include <vnet/tcp/tcp_packet.h>
#include <vnet/udp/udp_packet.h>
#include <ly_route_smart_qos/smart_qos.h>

typedef enum
{
  LY_SQ_ERROR_ENQUEUED,
  LY_SQ_ERROR_TRANSMITTED,
  LY_SQ_ERROR_AQM_DROP,
  LY_SQ_ERROR_OVERFLOW_DROP,
  LY_SQ_N_ERROR,
} ly_sq_error_t;

static char *ly_sq_error_strings[] = {
  "packets enqueued",
  "packets transmitted",
  "packets dropped by CoDel",
  "packets dropped on queue overflow",
};

static_always_inline void
ly_sq_packet_hashes (vlib_main_t *vm, vlib_buffer_t *buffer,
                     ly_sq_host_isolation_t isolation, u32 *flow_index,
                     u32 *host_index)
{
  typedef struct
    {
      u8 family;
      u8 protocol;
      u16 src_port;
      u16 dst_port;
      u8 addresses[32];
    } ly_sq_flow_key_t;
  ly_sq_flow_key_t key = { 0 };
  u8 *data = vlib_buffer_get_current (buffer);
  u32 length = buffer->current_length;
  u8 *network = data;
  u16 ethernet_type = 0;

  if (length >= sizeof (ethernet_header_t))
    {
      ethernet_header_t *ethernet = (ethernet_header_t *) data;
      ethernet_type = clib_net_to_host_u16 (ethernet->type);
      if (ethernet_type == ETHERNET_TYPE_IP4 ||
          ethernet_type == ETHERNET_TYPE_IP6)
        {
          network += sizeof (*ethernet);
          length -= sizeof (*ethernet);
        }
    }
  if (length >= sizeof (ip4_header_t) && (network[0] >> 4) == 4)
    {
      ip4_header_t *ip4 = (ip4_header_t *) network;
      u32 header_length = ip4_header_bytes (ip4);
      key.family = 4;
      key.protocol = ip4->protocol;
      clib_memcpy_fast (key.addresses, &ip4->src_address, 4);
      clib_memcpy_fast (key.addresses + 4, &ip4->dst_address, 4);
      if (length >= header_length + 4 &&
          (ip4->protocol == IP_PROTOCOL_TCP ||
           ip4->protocol == IP_PROTOCOL_UDP))
        {
          udp_header_t *transport = (udp_header_t *) (network + header_length);
          key.src_port = transport->src_port;
          key.dst_port = transport->dst_port;
        }
      *flow_index = hash_memory (&key, sizeof (key), LY_SQ_HASH_SEED) &
                    (LY_SQ_FLOW_COUNT - 1);
      void *host_address = isolation == LY_SQ_HOST_SOURCE ?
                             (void *) &ip4->src_address :
                             (void *) &ip4->dst_address;
      *host_index = hash_memory (host_address, 4, LY_SQ_HASH_SEED) &
                    (LY_SQ_HOST_COUNT - 1);
      return;
    }
  if (length >= sizeof (ip6_header_t) && (network[0] >> 4) == 6)
    {
      ip6_header_t *ip6 = (ip6_header_t *) network;
      key.family = 6;
      key.protocol = ip6->protocol;
      clib_memcpy_fast (key.addresses, &ip6->src_address, 16);
      clib_memcpy_fast (key.addresses + 16, &ip6->dst_address, 16);
      if (length >= sizeof (*ip6) + 4 &&
          (ip6->protocol == IP_PROTOCOL_TCP ||
           ip6->protocol == IP_PROTOCOL_UDP))
        {
          udp_header_t *transport = (udp_header_t *) (ip6 + 1);
          key.src_port = transport->src_port;
          key.dst_port = transport->dst_port;
        }
      *flow_index = hash_memory (&key, sizeof (key), LY_SQ_HASH_SEED) &
                    (LY_SQ_FLOW_COUNT - 1);
      void *host_address = isolation == LY_SQ_HOST_SOURCE ?
                             (void *) &ip6->src_address :
                             (void *) &ip6->dst_address;
      *host_index = hash_memory (host_address, 16, LY_SQ_HASH_SEED) &
                    (LY_SQ_HOST_COUNT - 1);
      return;
    }
  u32 sample_length = clib_min (vlib_buffer_length_in_chain (vm, buffer), 64);
  u32 hash = hash_memory (data, sample_length, LY_SQ_HASH_SEED);
  *flow_index = hash & (LY_SQ_FLOW_COUNT - 1);
  *host_index = hash & (LY_SQ_HOST_COUNT - 1);
}

static_always_inline void
ly_sq_activate_flow (ly_sq_scheduler_t *scheduler, u32 flow_index,
                     u32 host_index)
{
  ly_sq_flow_t *flow = &scheduler->flows[flow_index];
  ly_sq_host_t *host = &scheduler->hosts[host_index];
  if (flow->active)
    return;
  flow->host_index = host_index;
  flow->active = 1;
  flow->next_active = LY_SQ_INVALID_INDEX;
  flow->deficit = LY_SQ_QUANTUM;
  if (host->flow_tail == LY_SQ_INVALID_INDEX)
    host->flow_head = flow_index;
  else
    scheduler->flows[host->flow_tail].next_active = flow_index;
  host->flow_tail = flow_index;
  if (host->active)
    return;
  host->active = 1;
  host->deficit = LY_SQ_QUANTUM;
  host->next_active = LY_SQ_INVALID_INDEX;
  if (scheduler->active_tail == LY_SQ_INVALID_INDEX)
    scheduler->active_head = host_index;
  else
    scheduler->hosts[scheduler->active_tail].next_active = host_index;
  scheduler->active_tail = host_index;
}

static_always_inline void
ly_sq_rotate_flow (ly_sq_scheduler_t *scheduler, ly_sq_host_t *host)
{
  u32 flow_index = host->flow_head;
  ly_sq_flow_t *flow;
  if (flow_index == LY_SQ_INVALID_INDEX ||
      flow_index == host->flow_tail)
    return;
  flow = &scheduler->flows[flow_index];
  host->flow_head = flow->next_active;
  flow->next_active = LY_SQ_INVALID_INDEX;
  scheduler->flows[host->flow_tail].next_active = flow_index;
  host->flow_tail = flow_index;
}

static_always_inline void
ly_sq_rotate_host (ly_sq_scheduler_t *scheduler)
{
  u32 host_index = scheduler->active_head;
  ly_sq_host_t *host;
  if (host_index == LY_SQ_INVALID_INDEX ||
      host_index == scheduler->active_tail)
    return;
  host = &scheduler->hosts[host_index];
  scheduler->active_head = host->next_active;
  host->next_active = LY_SQ_INVALID_INDEX;
  scheduler->hosts[scheduler->active_tail].next_active = host_index;
  scheduler->active_tail = host_index;
}

static_always_inline void
ly_sq_deactivate_flow_head (ly_sq_scheduler_t *scheduler,
                            ly_sq_host_t *host)
{
  u32 flow_index = host->flow_head;
  ly_sq_flow_t *flow = &scheduler->flows[flow_index];
  host->flow_head = flow->next_active;
  if (host->flow_head == LY_SQ_INVALID_INDEX)
    host->flow_tail = LY_SQ_INVALID_INDEX;
  flow->active = 0;
  flow->host_index = LY_SQ_INVALID_INDEX;
  flow->next_active = LY_SQ_INVALID_INDEX;
  flow->deficit = 0;
  if (host->flow_head != LY_SQ_INVALID_INDEX)
    return;
  ASSERT (host == &scheduler->hosts[scheduler->active_head]);
  scheduler->active_head = host->next_active;
  if (scheduler->active_head == LY_SQ_INVALID_INDEX)
    scheduler->active_tail = LY_SQ_INVALID_INDEX;
  host->active = 0;
  host->next_active = LY_SQ_INVALID_INDEX;
  host->deficit = 0;
}

static_always_inline u32
ly_sq_release_head (ly_sq_scheduler_t *scheduler, ly_sq_flow_t *flow)
{
  u32 entry_index = flow->head;
  ly_sq_entry_t *entry = &scheduler->entries[entry_index];
  flow->head = entry->next;
  if (flow->head == LY_SQ_INVALID_INDEX)
    flow->tail = LY_SQ_INVALID_INDEX;
  flow->packets--;
  flow->bytes -= entry->length;
  scheduler->queued_packets--;
  scheduler->queued_bytes -= entry->length;
  return entry_index;
}

static_always_inline void
ly_sq_free_entry (ly_sq_scheduler_t *scheduler, u32 entry_index)
{
  ly_sq_entry_t *entry = &scheduler->entries[entry_index];
  entry->buffer_index = LY_SQ_INVALID_INDEX;
  entry->next = scheduler->free_head;
  scheduler->free_head = entry_index;
}

static_always_inline u32
ly_sq_find_fattest_flow (ly_sq_scheduler_t *scheduler)
{
  u32 index, fattest = LY_SQ_INVALID_INDEX, max_bytes = 0;
  for (index = 0; index < LY_SQ_FLOW_COUNT; index++)
    if (scheduler->flows[index].bytes > max_bytes)
      {
        max_bytes = scheduler->flows[index].bytes;
        fattest = index;
      }
  scheduler->fattest_flow = fattest;
  return fattest;
}

static_always_inline int
ly_sq_evict_from_fattest (ly_sq_scheduler_t *scheduler,
                          u32 *buffer_index)
{
  u32 flow_index = scheduler->fattest_flow;
  if (flow_index == LY_SQ_INVALID_INDEX ||
      scheduler->flows[flow_index].head == LY_SQ_INVALID_INDEX)
    flow_index = ly_sq_find_fattest_flow (scheduler);
  if (flow_index == LY_SQ_INVALID_INDEX)
    return 0;
  ly_sq_flow_t *flow = &scheduler->flows[flow_index];
  ly_sq_entry_t *entry = &scheduler->entries[flow->head];
  u32 entry_index = ly_sq_release_head (scheduler, flow);
  *buffer_index = entry->buffer_index;
  ly_sq_free_entry (scheduler, entry_index);
  if (flow->head == LY_SQ_INVALID_INDEX)
    ly_sq_find_fattest_flow (scheduler);
  return 1;
}

static_always_inline int
ly_sq_codel_ok_to_drop (ly_sq_flow_t *flow, ly_sq_entry_t *entry, f64 now)
{
  f64 sojourn = now - entry->enqueue_time;
  if (sojourn < LY_SQ_TARGET_SECONDS || flow->bytes <= LY_SQ_QUANTUM)
    {
      flow->first_above_time = 0;
      return 0;
    }
  if (flow->first_above_time == 0)
    {
      flow->first_above_time = now + LY_SQ_INTERVAL_SECONDS;
      return 0;
    }
  return now >= flow->first_above_time;
}

static_always_inline f64
ly_sq_codel_control_law (f64 time, u32 count)
{
  return time + LY_SQ_INTERVAL_SECONDS / sqrt ((f64) count);
}

static_always_inline int
ly_sq_codel_should_drop (ly_sq_flow_t *flow, ly_sq_entry_t *entry, f64 now)
{
  int ok_to_drop = ly_sq_codel_ok_to_drop (flow, entry, now);
  if (flow->dropping)
    {
      if (!ok_to_drop)
        flow->dropping = 0;
      else if (now >= flow->drop_next)
        {
          flow->drop_count++;
          flow->drop_next =
            ly_sq_codel_control_law (flow->drop_next, flow->drop_count);
          return 1;
        }
    }
  else if (ok_to_drop)
    {
      flow->dropping = 1;
      flow->drop_count = 1;
      flow->drop_next = ly_sq_codel_control_law (now, flow->drop_count);
      return 1;
    }
  return 0;
}

static_always_inline u32
ly_sq_enqueue_buffer (vlib_main_t *vm, ly_sq_scheduler_t *scheduler,
                      u32 buffer_index, f64 now)
{
  vlib_buffer_t *buffer = vlib_get_buffer (vm, buffer_index);
  u32 dropped_buffer = LY_SQ_INVALID_INDEX;
  u32 flow_index, host_index;
  ly_sq_packet_hashes (vm, buffer, scheduler->host_isolation, &flow_index,
                       &host_index);

  if (scheduler->free_head == LY_SQ_INVALID_INDEX)
    {
      if (!ly_sq_evict_from_fattest (scheduler, &dropped_buffer))
        {
          scheduler->overflow_drops++;
          return buffer_index;
        }
      scheduler->overflow_drops++;
    }
  u32 entry_index = scheduler->free_head;
  ly_sq_entry_t *entry = &scheduler->entries[entry_index];
  ly_sq_flow_t *flow = &scheduler->flows[flow_index];
  scheduler->free_head = entry->next;
  entry->buffer_index = buffer_index;
  entry->length = vlib_buffer_length_in_chain (vm, buffer);
  entry->output_next_index = scheduler->output_next_index;
  entry->enqueue_time = now;
  entry->next = LY_SQ_INVALID_INDEX;
  if (flow->tail == LY_SQ_INVALID_INDEX)
    flow->head = entry_index;
  else
    scheduler->entries[flow->tail].next = entry_index;
  flow->tail = entry_index;
  flow->packets++;
  flow->bytes += entry->length;
  if (scheduler->fattest_flow == LY_SQ_INVALID_INDEX ||
      flow->bytes > scheduler->flows[scheduler->fattest_flow].bytes)
    scheduler->fattest_flow = flow_index;
  scheduler->queued_packets++;
  scheduler->queued_bytes += entry->length;
  scheduler->enqueued++;
  ly_sq_activate_flow (scheduler, flow_index, host_index);
  return dropped_buffer;
}

VLIB_NODE_FN (ly_sq_feature_node) (vlib_main_t *vm,
                                   vlib_node_runtime_t *node,
                                   vlib_frame_t *frame)
{
  ly_sq_main_t *sqm = &ly_sq_main;
  u32 *from = vlib_frame_vector_args (frame);
  u32 n_left = frame->n_vectors;
  u32 drops[VLIB_FRAME_SIZE], forwards[VLIB_FRAME_SIZE];
  u32 handoffs[VLIB_FRAME_SIZE];
  u16 forward_nexts[VLIB_FRAME_SIZE];
  u16 handoff_threads[VLIB_FRAME_SIZE];
  u32 *drop = drops, *forward = forwards;
  u32 *handoff = handoffs;
  u16 *forward_next = forward_nexts;
  vnet_feature_config_main_t *fcm =
    vnet_feature_get_config_main (sqm->arc_index);
  f64 now = vlib_time_now (vm);

  while (n_left--)
    {
      u32 buffer_index = from[0];
      vlib_buffer_t *buffer = vlib_get_buffer (vm, buffer_index);
      u32 sw_if_index = vnet_buffer (buffer)->sw_if_index[VLIB_TX];
      ly_sq_scheduler_t *scheduler = 0;
      u32 next_index;
      from++;
      vnet_get_config_data (&fcm->config_main,
                            &buffer->current_config_index, &next_index, 0);
      if (PREDICT_FALSE (buffer->flags & LY_SQ_REINJECT_FLAG))
        {
          buffer->flags &= ~LY_SQ_REINJECT_FLAG;
          forward[0] = buffer_index;
          forward_next[0] = next_index;
          forward++;
          forward_next++;
          continue;
        }
      if (sw_if_index >= vec_len (sqm->enabled_by_sw_if_index) ||
          !sqm->enabled_by_sw_if_index[sw_if_index])
        {
          forward[0] = buffer_index;
          forward_next[0] = next_index;
          forward++;
          forward_next++;
          continue;
        }
      if (vm->thread_index != sqm->scheduler_thread_index)
        {
          handoff_threads[handoff - handoffs] = sqm->scheduler_thread_index;
          handoff[0] = buffer_index;
          handoff++;
          continue;
        }
      scheduler =
        &sqm->schedulers_by_thread[sqm->scheduler_thread_index][sw_if_index];
      u32 victim_buffer =
        ly_sq_enqueue_buffer (vm, scheduler, buffer_index, now);
      if (victim_buffer != LY_SQ_INVALID_INDEX)
        {
          drop[0] = victim_buffer;
          drop++;
        }
    }
  if (handoff > handoffs)
    {
      u32 n_handoff = handoff - handoffs;
      u32 n_enqueued = vlib_buffer_enqueue_to_thread (
        vm, node, sqm->frame_queue_index, handoffs, handoff_threads,
        n_handoff, 1);
      if (n_enqueued < n_handoff)
        vlib_node_increment_counter (vm, node->node_index,
                                     LY_SQ_ERROR_OVERFLOW_DROP,
                                     n_handoff - n_enqueued);
    }
  if (drop > drops)
    {
      vlib_buffer_free (vm, drops, drop - drops);
      vlib_node_increment_counter (vm, node->node_index,
                                   LY_SQ_ERROR_OVERFLOW_DROP, drop - drops);
    }
  if (forward > forwards)
    vlib_buffer_enqueue_to_next (vm, node, forwards, forward_nexts,
                                 forward - forwards);
  vlib_node_increment_counter (vm, node->node_index, LY_SQ_ERROR_ENQUEUED,
                               frame->n_vectors - (drop - drops) -
                                 (forward - forwards));
  return frame->n_vectors;
}

VLIB_NODE_FN (ly_sq_enqueue_node) (vlib_main_t *vm,
                                   vlib_node_runtime_t *node,
                                   vlib_frame_t *frame)
{
  ly_sq_main_t *sqm = &ly_sq_main;
  u32 *from = vlib_frame_vector_args (frame);
  u32 drops[VLIB_FRAME_SIZE], n_drop = 0, index;
  f64 now = vlib_time_now (vm);

  if (PREDICT_FALSE (vm->thread_index != sqm->scheduler_thread_index))
    {
      vlib_buffer_free (vm, from, frame->n_vectors);
      return frame->n_vectors;
    }
  for (index = 0; index < frame->n_vectors; index++)
    {
      u32 buffer_index = from[index];
      vlib_buffer_t *buffer = vlib_get_buffer (vm, buffer_index);
      u32 sw_if_index = vnet_buffer (buffer)->sw_if_index[VLIB_TX];
      if (sw_if_index >=
            vec_len (sqm->schedulers_by_thread[vm->thread_index]) ||
          !sqm->schedulers_by_thread[vm->thread_index][sw_if_index].enabled)
        {
          drops[n_drop++] = buffer_index;
          continue;
        }
      u32 victim_buffer = ly_sq_enqueue_buffer (
        vm, &sqm->schedulers_by_thread[vm->thread_index][sw_if_index],
        buffer_index, now);
      if (victim_buffer != LY_SQ_INVALID_INDEX)
        drops[n_drop++] = victim_buffer;
    }
  if (n_drop)
    {
      vlib_buffer_free (vm, drops, n_drop);
      vlib_node_increment_counter (vm, node->node_index,
                                   LY_SQ_ERROR_OVERFLOW_DROP, n_drop);
    }
  vlib_node_increment_counter (vm, node->node_index, LY_SQ_ERROR_ENQUEUED,
                               frame->n_vectors - n_drop);
  return frame->n_vectors;
}

VLIB_NODE_FN (ly_sq_scheduler_node) (vlib_main_t *vm,
                                     vlib_node_runtime_t *node,
                                     vlib_frame_t *frame)
{
  ly_sq_main_t *sqm = &ly_sq_main;
  ly_sq_scheduler_t *schedulers =
    sqm->schedulers_by_thread[vm->thread_index];
  u32 buffers[LY_SQ_MAX_BURST], drops[LY_SQ_MAX_BURST];
  u16 nexts[LY_SQ_MAX_BURST];
  u32 n_tx = 0, n_drop = 0, sw_if_index;
  f64 now = vlib_time_now (vm);

  for (sw_if_index = 0;
       sw_if_index < vec_len (schedulers) &&
       n_tx + n_drop < LY_SQ_MAX_BURST;
       sw_if_index++)
    {
      ly_sq_scheduler_t *scheduler = &schedulers[sw_if_index];
      if (!scheduler->enabled || scheduler->queued_packets == 0)
        continue;
      if (scheduler->last_token_time == 0)
        scheduler->last_token_time = now;
      scheduler->tokens +=
        (now - scheduler->last_token_time) * scheduler->rate_bps / 8.0;
      scheduler->tokens = clib_min (scheduler->tokens, 65536.0);
      scheduler->last_token_time = now;

      while (scheduler->active_head != LY_SQ_INVALID_INDEX &&
             n_tx + n_drop < LY_SQ_MAX_BURST)
        {
          ly_sq_host_t *host =
            &scheduler->hosts[scheduler->active_head];
          if (host->flow_head == LY_SQ_INVALID_INDEX)
            {
              host->active = 0;
              scheduler->active_head = host->next_active;
              if (scheduler->active_head == LY_SQ_INVALID_INDEX)
                scheduler->active_tail = LY_SQ_INVALID_INDEX;
              continue;
            }
          ly_sq_flow_t *flow = &scheduler->flows[host->flow_head];
          if (flow->head == LY_SQ_INVALID_INDEX)
            {
              ly_sq_deactivate_flow_head (scheduler, host);
              continue;
            }
          ly_sq_entry_t *entry = &scheduler->entries[flow->head];
          if (ly_sq_codel_should_drop (flow, entry, now))
            {
              u32 entry_index = ly_sq_release_head (scheduler, flow);
              drops[n_drop++] = entry->buffer_index;
              ly_sq_free_entry (scheduler, entry_index);
              scheduler->aqm_drops++;
              if (flow->head == LY_SQ_INVALID_INDEX)
                ly_sq_deactivate_flow_head (scheduler, host);
              continue;
            }
          if (flow->deficit < (i32) entry->length)
            {
              flow->deficit += LY_SQ_QUANTUM;
              ly_sq_rotate_flow (scheduler, host);
              continue;
            }
          if (host->deficit < (i32) entry->length)
            {
              host->deficit += LY_SQ_QUANTUM;
              ly_sq_rotate_host (scheduler);
              continue;
            }
          if (scheduler->tokens < entry->length)
            break;
          u32 entry_index = ly_sq_release_head (scheduler, flow);
          buffers[n_tx] = entry->buffer_index;
          nexts[n_tx] = entry->output_next_index;
          vlib_get_buffer (vm, entry->buffer_index)->flags |=
            LY_SQ_REINJECT_FLAG;
          n_tx++;
          flow->deficit -= entry->length;
          host->deficit -= entry->length;
          scheduler->tokens -= entry->length;
          scheduler->transmitted++;
          ly_sq_free_entry (scheduler, entry_index);
          if (flow->head == LY_SQ_INVALID_INDEX)
            ly_sq_deactivate_flow_head (scheduler, host);
          else
            {
              ly_sq_rotate_flow (scheduler, host);
              ly_sq_rotate_host (scheduler);
            }
        }
    }
  if (n_drop)
    {
      vlib_buffer_free (vm, drops, n_drop);
      vlib_node_increment_counter (vm, node->node_index,
                                   LY_SQ_ERROR_AQM_DROP, n_drop);
    }
  if (n_tx)
    {
      vlib_buffer_enqueue_to_next (vm, node, buffers, nexts, n_tx);
      vlib_node_increment_counter (vm, node->node_index,
                                   LY_SQ_ERROR_TRANSMITTED, n_tx);
    }
  return n_tx;
}

VLIB_REGISTER_NODE (ly_sq_feature_node) = {
  .name = "ly-route-smart-qos-output",
  .vector_size = sizeof (u32),
  .type = VLIB_NODE_TYPE_INTERNAL,
  .n_errors = LY_SQ_N_ERROR,
  .error_strings = ly_sq_error_strings,
};

VLIB_REGISTER_NODE (ly_sq_scheduler_node) = {
  .name = "ly-route-smart-qos-scheduler",
  .type = VLIB_NODE_TYPE_INPUT,
  .state = VLIB_NODE_STATE_DISABLED,
  .n_errors = LY_SQ_N_ERROR,
  .error_strings = ly_sq_error_strings,
};

VLIB_REGISTER_NODE (ly_sq_enqueue_node) = {
  .name = "ly-route-smart-qos-enqueue",
  .vector_size = sizeof (u32),
  .type = VLIB_NODE_TYPE_INTERNAL,
  .n_errors = LY_SQ_N_ERROR,
  .error_strings = ly_sq_error_strings,
};
