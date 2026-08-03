#include <vnet/feature/feature.h>
#include <vnet/bonding/node.h>
#include <vnet/tcp/tcp_packet.h>
#include <vnet/udp/udp_packet.h>
#include <ly_route_orchestrator/orchestrator.h>

typedef enum
{
  LY_ORCH_ERROR_FORWARDED,
  LY_ORCH_ERROR_DROPPED,
  LY_ORCH_ERROR_NON_IP,
  LY_ORCH_N_ERROR,
} ly_orch_error_t;

static char *ly_orch_error_strings[] = {
  "packets transparently forwarded",
  "packets dropped by orchestration policy",
  "non-IP packets directly forwarded",
};

typedef struct
{
  ip46_address_t source;
  ip46_address_t destination;
  u16 source_port;
  u16 destination_port;
  u8 protocol;
  u8 is_ip6;
  u8 ports_valid;
  u8 valid;
} ly_orch_tuple_t;

typedef enum
{
  LY_ORCH_DIRECTION_FORWARD,
  LY_ORCH_DIRECTION_REVERSE,
  LY_ORCH_DIRECTION_INVALID,
} ly_orch_direction_t;

static_always_inline int
ly_orch_prefix_matches (const u8 *address, const u8 *prefix, u8 bits)
{
  u8 bytes = bits / 8;
  u8 remaining = bits % 8;
  if (bytes && memcmp (address, prefix, bytes) != 0)
    return 0;
  if (!remaining)
    return 1;
  u8 mask = (u8) (0xff << (8 - remaining));
  return (address[bytes] & mask) == (prefix[bytes] & mask);
}

static_always_inline ly_orch_tuple_t
ly_orch_parse_tuple (vlib_buffer_t *buffer)
{
  ly_orch_tuple_t tuple = { 0 };
  u8 *data = vlib_buffer_get_current (buffer);
  u32 length = buffer->current_length;
  u32 offset = sizeof (ethernet_header_t);
  u16 type;
  if (length < offset)
    return tuple;
  type = clib_net_to_host_u16 (*(u16 *) (data + 12));
  while ((type == ETHERNET_TYPE_VLAN || type == ETHERNET_TYPE_DOT1AD) &&
         length >= offset + 4)
    {
      type = clib_net_to_host_u16 (*(u16 *) (data + offset + 2));
      offset += 4;
    }
  if (type == ETHERNET_TYPE_IP4 && length >= offset + sizeof (ip4_header_t))
    {
      ip4_header_t *ip = (ip4_header_t *) (data + offset);
      u32 header_length = ip4_header_bytes (ip);
      if (header_length < sizeof (*ip) || length < offset + header_length)
        return tuple;
      tuple.source.ip4 = ip->src_address;
      tuple.destination.ip4 = ip->dst_address;
      tuple.protocol = ip->protocol;
      if ((ip->protocol == IP_PROTOCOL_TCP || ip->protocol == IP_PROTOCOL_UDP) &&
          length >= offset + header_length + 4)
        {
          udp_header_t *transport =
            (udp_header_t *) (data + offset + header_length);
          tuple.source_port = clib_net_to_host_u16 (transport->src_port);
          tuple.destination_port = clib_net_to_host_u16 (transport->dst_port);
          tuple.ports_valid = 1;
        }
      tuple.valid = 1;
      return tuple;
    }
  if (type == ETHERNET_TYPE_IP6 && length >= offset + sizeof (ip6_header_t))
    {
      ip6_header_t *ip = (ip6_header_t *) (data + offset);
      u32 transport_offset = offset + sizeof (*ip);
      u8 protocol = ip->protocol;
      u8 depth;
      tuple.is_ip6 = 1;
      tuple.source.ip6 = ip->src_address;
      tuple.destination.ip6 = ip->dst_address;
      for (depth = 0; depth < IP6_EXT_HDR_MAX; depth++)
        {
          u32 extension_length;
          ip6_ext_header_t *extension;
          if (ip6_ext_hdr (protocol))
            {
              if (length < transport_offset + sizeof (*extension))
                return tuple;
              extension = (ip6_ext_header_t *) (data + transport_offset);
              extension_length = ip6_ext_header_len (extension);
            }
          else if (protocol == IP_PROTOCOL_IPV6_FRAGMENTATION)
            {
              ip6_frag_hdr_t *fragment;
              if (length < transport_offset + sizeof (*fragment))
                return tuple;
              fragment = (ip6_frag_hdr_t *) (data + transport_offset);
              protocol = fragment->next_hdr;
              transport_offset += sizeof (*fragment);
              if (ip6_frag_hdr_offset (fragment) > 0)
                break;
              continue;
            }
          else if (protocol == IP_PROTOCOL_IPSEC_AH)
            {
              if (length < transport_offset + sizeof (*extension))
                return tuple;
              extension = (ip6_ext_header_t *) (data + transport_offset);
              extension_length = ip6_ext_authhdr_len (extension);
            }
          else
            break;
          if (extension_length < sizeof (*extension) ||
              length < transport_offset + extension_length)
            return tuple;
          protocol = extension->next_hdr;
          transport_offset += extension_length;
        }
      tuple.protocol = protocol;
      if ((protocol == IP_PROTOCOL_TCP || protocol == IP_PROTOCOL_UDP) &&
          length >= transport_offset + 4)
        {
          udp_header_t *transport = (udp_header_t *) (data + transport_offset);
          tuple.source_port = clib_net_to_host_u16 (transport->src_port);
          tuple.destination_port = clib_net_to_host_u16 (transport->dst_port);
          tuple.ports_valid = 1;
        }
      tuple.valid = 1;
    }
  return tuple;
}

static_always_inline void
ly_orch_reverse_tuple (ly_orch_tuple_t *tuple)
{
  ip46_address_t address = tuple->source;
  u16 port = tuple->source_port;
  tuple->source = tuple->destination;
  tuple->destination = address;
  tuple->source_port = tuple->destination_port;
  tuple->destination_port = port;
}

static_always_inline int
ly_orch_rule_matches (const ly_orch_rule_t *rule,
                      const ly_orch_tuple_t *tuple)
{
  const u8 *source;
  const u8 *destination;
  const u8 *rule_source;
  const u8 *rule_destination;
  if (!tuple->valid || rule->is_ip6 != tuple->is_ip6 ||
      (rule->protocol && rule->protocol != tuple->protocol))
    return 0;
  source = tuple->is_ip6 ? tuple->source.ip6.as_u8 : tuple->source.ip4.as_u8;
  destination = tuple->is_ip6 ? tuple->destination.ip6.as_u8 :
                                tuple->destination.ip4.as_u8;
  rule_source = rule->is_ip6 ? rule->source.ip6.as_u8 : rule->source.ip4.as_u8;
  rule_destination = rule->is_ip6 ? rule->destination.ip6.as_u8 :
                                    rule->destination.ip4.as_u8;
  if (!ly_orch_prefix_matches (source, rule_source,
                               rule->source_prefix_length) ||
      !ly_orch_prefix_matches (destination, rule_destination,
                               rule->destination_prefix_length))
    return 0;
  if (rule->protocol == IP_PROTOCOL_TCP || rule->protocol == IP_PROTOCOL_UDP)
    return (!tuple->ports_valid && rule->source_port_start == 0 &&
            rule->source_port_end == 65535 &&
            rule->destination_port_start == 0 &&
            rule->destination_port_end == 65535) ||
           (tuple->ports_valid &&
            tuple->source_port >= rule->source_port_start &&
           tuple->source_port <= rule->source_port_end &&
           tuple->destination_port >= rule->destination_port_start &&
            tuple->destination_port <= rule->destination_port_end);
  return 1;
}

static_always_inline int
ly_orch_group_available (ly_orch_config_t *config, u32 group_index)
{
  ly_orch_group_t *group = &config->groups[group_index];
  vnet_main_t *vnm = ly_orch_main.vnet_main;
  return vnet_sw_interface_is_up (vnm, group->wan_sw_if_index) &&
         vnet_sw_interface_is_up (vnm, group->lan_sw_if_index);
}

static_always_inline u8
ly_orch_build_path (ly_orch_config_t *config, const ly_orch_tuple_t *tuple,
                    u32 path[LY_ORCH_MAX_HOPS], ly_orch_worker_t *worker,
                    u32 packet_bytes, u8 *drop)
{
  u32 index = 0;
  u8 path_count = 0;
  *drop = 0;
  while (index < vec_len (config->rules))
    {
      u32 group_position = config->rules[index].group_position;
      u32 scan = index;
      int selected = -1;
      while (scan < vec_len (config->rules) &&
             config->rules[scan].group_position == group_position)
        {
          if (selected < 0 && ly_orch_rule_matches (&config->rules[scan], tuple))
            selected = scan;
          scan++;
        }
      if (selected >= 0)
        {
          ly_orch_rule_t *rule = &config->rules[selected];
          if (worker)
            {
              worker->rule_counters[selected].packets++;
              worker->rule_counters[selected].bytes += packet_bytes;
            }
          if (rule->action == LY_ORCH_ACTION_DROP)
            {
              *drop = 1;
              return path_count;
            }
          if (rule->action == LY_ORCH_ACTION_VIA)
            {
              if (!ly_orch_group_available (config, rule->target_group))
                {
                  if (worker)
                    worker->group_bypass_packets[rule->target_group]++;
                  index = scan;
                  continue;
                }
              if (path_count >= LY_ORCH_MAX_HOPS)
                {
                  *drop = 1;
                  return path_count;
                }
              path[path_count++] = rule->target_group;
            }
        }
      index = scan;
    }
  if (config->default_action == LY_ORCH_ACTION_DROP)
    *drop = 1;
  return path_count;
}

static_always_inline ly_orch_direction_t
ly_orch_direction (ly_orch_config_t *config, u32 rx, u32 *return_group,
                   u8 *boundary)
{
  u32 index;
  *return_group = LY_ORCH_INVALID_INDEX;
  *boundary = 0;
  if (rx == config->wan_sw_if_index)
    {
      *boundary = 1;
      return LY_ORCH_DIRECTION_FORWARD;
    }
  if (rx == config->lan_sw_if_index)
    {
      *boundary = 1;
      return LY_ORCH_DIRECTION_REVERSE;
    }
  vec_foreach_index (index, config->groups)
    {
      if (rx == config->groups[index].lan_sw_if_index)
        {
          *return_group = index;
          return LY_ORCH_DIRECTION_FORWARD;
        }
      if (rx == config->groups[index].wan_sw_if_index)
        {
          *return_group = index;
          return LY_ORCH_DIRECTION_REVERSE;
        }
    }
  return LY_ORCH_DIRECTION_INVALID;
}

static_always_inline u32
ly_orch_logical_ingress (u32 rx)
{
  member_if_t *member = bond_get_member_by_sw_if_index (rx);
  bond_if_t *bond;

  if (!member)
    return rx;
  bond = bond_get_bond_if_by_dev_instance (member->bif_dev_instance);
  return bond ? bond->sw_if_index : rx;
}

static_always_inline u64
ly_orch_flow_hash (const ly_orch_tuple_t *tuple)
{
  return hash_memory ((void *) tuple, sizeof (*tuple), 0x6c796f72);
}

static_always_inline int
ly_orch_flow_equal (const ly_orch_flow_t *flow, const ly_orch_tuple_t *tuple)
{
  if (!flow->occupied || flow->is_ip6 != tuple->is_ip6 ||
      flow->protocol != tuple->protocol ||
      flow->source_port != tuple->source_port ||
      flow->destination_port != tuple->destination_port)
    return 0;
  if (tuple->is_ip6)
    return ip6_address_is_equal (&flow->source.ip6, &tuple->source.ip6) &&
           ip6_address_is_equal (&flow->destination.ip6,
                                 &tuple->destination.ip6);
  return ip4_address_is_equal (&flow->source.ip4, &tuple->source.ip4) &&
         ip4_address_is_equal (&flow->destination.ip4,
                               &tuple->destination.ip4);
}

static_always_inline void
ly_orch_record_flow (vlib_main_t *vm, ly_orch_worker_t *worker,
                     const ly_orch_tuple_t *tuple, const u32 *path,
                     u8 path_count, u32 packet_bytes)
{
  u32 start = ly_orch_flow_hash (tuple) & (LY_ORCH_FLOW_SLOTS - 1);
  u32 probe, selected = start;
  f64 oldest = 1e30;
  f64 now = vlib_time_now (vm);
  ly_orch_flow_t *flow;
  for (probe = 0; probe < LY_ORCH_FLOW_PROBES; probe++)
    {
      u32 slot = (start + probe) & (LY_ORCH_FLOW_SLOTS - 1);
      flow = &worker->flows[slot];
      if (!flow->occupied || now - flow->last_seen > LY_ORCH_FLOW_TTL_SECONDS ||
          ly_orch_flow_equal (flow, tuple))
        {
          selected = slot;
          break;
        }
      if (flow->last_seen < oldest)
        {
          oldest = flow->last_seen;
          selected = slot;
        }
    }
  flow = &worker->flows[selected];
  if (!ly_orch_flow_equal (flow, tuple))
    {
      clib_memset (flow, 0, sizeof (*flow));
      flow->source = tuple->source;
      flow->destination = tuple->destination;
      flow->source_port = tuple->source_port;
      flow->destination_port = tuple->destination_port;
      flow->protocol = tuple->protocol;
      flow->is_ip6 = tuple->is_ip6;
      flow->group_count = path_count;
      clib_memcpy_fast (flow->groups, path, sizeof (u32) * path_count);
      flow->occupied = 1;
    }
  flow->packets++;
  flow->bytes += packet_bytes;
  flow->last_seen = now;
}

static_always_inline u16
ly_orch_target_next (ly_orch_config_t *config, ly_orch_direction_t direction,
                     u32 return_group, const u32 *path, u8 path_count,
                     u32 *target_sw_if_index)
{
  i32 position;
  if (direction == LY_ORCH_DIRECTION_FORWARD)
    {
      position = 0;
      if (return_group != LY_ORCH_INVALID_INDEX)
        {
          for (position = 0; position < path_count; position++)
            if (path[position] == return_group)
              break;
          if (position == path_count)
            return 0;
          position++;
        }
      if (position < path_count)
        {
          ly_orch_group_t *group = &config->groups[path[position]];
          *target_sw_if_index = group->wan_sw_if_index;
          return group->wan_next_index;
        }
      *target_sw_if_index = config->lan_sw_if_index;
      return config->lan_next_index;
    }
  position = path_count - 1;
  if (return_group != LY_ORCH_INVALID_INDEX)
    {
      for (position = path_count - 1; position >= 0; position--)
        if (path[position] == return_group)
          break;
      if (position < 0)
        return 0;
      position--;
    }
  if (position >= 0)
    {
      ly_orch_group_t *group = &config->groups[path[position]];
      *target_sw_if_index = group->lan_sw_if_index;
      return group->lan_next_index;
    }
  *target_sw_if_index = config->wan_sw_if_index;
  return config->wan_next_index;
}

VLIB_NODE_FN (ly_orch_node) (vlib_main_t *vm, vlib_node_runtime_t *node,
                             vlib_frame_t *frame)
{
  ly_orch_main_t *om = &ly_orch_main;
  ly_orch_config_t *config = &om->active;
  ly_orch_worker_t *worker = &om->workers[vm->thread_index];
  u32 *from = vlib_frame_vector_args (frame);
  u32 buffers[VLIB_FRAME_SIZE];
  u16 nexts[VLIB_FRAME_SIZE];
  u32 forwarded = 0, dropped = 0, non_ip = 0, index;

  for (index = 0; index < frame->n_vectors; index++)
    {
      u32 buffer_index = from[index];
      vlib_buffer_t *buffer = vlib_get_buffer (vm, buffer_index);
      u32 rx = ly_orch_logical_ingress (
        vnet_buffer (buffer)->sw_if_index[VLIB_RX]);
      u32 return_group, target = LY_ORCH_INVALID_INDEX;
      u32 path[LY_ORCH_MAX_HOPS] = { 0 };
      u8 drop = 0, boundary = 0;
      ly_orch_tuple_t tuple = ly_orch_parse_tuple (buffer);
      ly_orch_direction_t direction =
        ly_orch_direction (config, rx, &return_group, &boundary);
      u16 next = 0;
      u8 path_count;
      u32 packet_bytes = vlib_buffer_length_in_chain (vm, buffer);

      if (direction == LY_ORCH_DIRECTION_INVALID || !config->valid)
        drop = 1;
      if (direction == LY_ORCH_DIRECTION_REVERSE && tuple.valid)
        ly_orch_reverse_tuple (&tuple);
      if (!drop && tuple.valid)
        path_count = ly_orch_build_path (config, &tuple, path,
                                         boundary ? worker : 0, packet_bytes,
                                         &drop);
      else
        path_count = 0;
      if (!drop && !tuple.valid && !boundary)
        drop = 1;
      if (!drop)
        next = ly_orch_target_next (config, direction, return_group, path,
                                    path_count, &target);
      if (!next)
        drop = 1;

      if (boundary && tuple.valid)
        {
          ly_orch_record_flow (vm, worker, &tuple, path, path_count,
                               packet_bytes);
        }

      if (drop)
        {
          buffer->error = node->errors[LY_ORCH_ERROR_DROPPED];
          nexts[index] = 0;
          dropped++;
        }
      else
        {
          vnet_buffer (buffer)->sw_if_index[VLIB_TX] = target;
          nexts[index] = next;
          forwarded++;
          if (!tuple.valid)
            non_ip++;
        }
      buffers[index] = buffer_index;
    }
  vlib_buffer_enqueue_to_next (vm, node, buffers, nexts, frame->n_vectors);
  vlib_node_increment_counter (vm, node->node_index, LY_ORCH_ERROR_FORWARDED,
                               forwarded);
  vlib_node_increment_counter (vm, node->node_index, LY_ORCH_ERROR_DROPPED,
                               dropped);
  vlib_node_increment_counter (vm, node->node_index, LY_ORCH_ERROR_NON_IP,
                               non_ip);
  return frame->n_vectors;
}

VLIB_REGISTER_NODE (ly_orch_node) = {
  .name = "ly-route-orchestrator",
  .vector_size = sizeof (u32),
  .type = VLIB_NODE_TYPE_INTERNAL,
  .n_errors = LY_ORCH_N_ERROR,
  .error_strings = ly_orch_error_strings,
  .n_next_nodes = 1,
  .next_nodes = { [0] = "error-drop" },
};
