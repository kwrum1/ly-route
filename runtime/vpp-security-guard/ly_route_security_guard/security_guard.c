#include <arpa/inet.h>
#include <string.h>

#include <vnet/feature/feature.h>
#include <vnet/ip/ip4.h>
#include <vnet/ip/ip6.h>
#include <vnet/ip/format.h>
#include <vnet/tcp/tcp_packet.h>
#include <vnet/udp/udp_packet.h>
#include <vpp/app/version.h>
#include <vnet/plugin/plugin.h>
#include <ly_route_security_guard/security_guard.h>

typedef enum
{
  LY_SG_ERROR_PASSED,
  LY_SG_ERROR_ALERT,
  LY_SG_ERROR_DROPPED,
  LY_SG_N_ERROR,
} ly_sg_error_t;

static char *ly_sg_error_strings[] = {
  "packets passed",
  "packets alerted",
  "packets dropped",
};

ly_sg_main_t ly_sg_main;

static int
ly_sg_prefix_match (const u8 *address, const u8 *prefix, u8 prefix_len,
                    u8 family)
{
  u8 full = prefix_len / 8;
  u8 remainder = prefix_len % 8;
  u8 bytes = family == 4 ? 4 : 16;
  if (prefix_len == 0)
    return 1;
  if (full > bytes || clib_memcmp (address, prefix, full) != 0)
    return 0;
  return remainder == 0 ||
         (address[full] & (u8) (0xff << (8 - remainder))) ==
           (prefix[full] & (u8) (0xff << (8 - remainder)));
}

static int
ly_sg_match_rule (ly_sg_rule_t *rule, u32 sw_if_index, u8 family,
                  u8 protocol, u8 tcp_flags, const u8 *source,
                  const u8 *destination)
{
  if (!rule->enabled || rule->family != family ||
      rule->sw_if_index != sw_if_index ||
      !ly_sg_prefix_match (source, rule->source, rule->source_prefix_len,
                           family) ||
      !ly_sg_prefix_match (destination, rule->destination,
                           rule->destination_prefix_len, family))
    return 0;
  switch (rule->attack_type)
    {
    case LY_SG_ATTACK_SYN_FLOOD:
    case LY_SG_ATTACK_NEW_CONNECTION_RATE:
      return protocol == IP_PROTOCOL_TCP && (tcp_flags & 0x02) != 0 &&
             (tcp_flags & 0x10) == 0;
    case LY_SG_ATTACK_UDP_FLOOD:
      return protocol == IP_PROTOCOL_UDP;
    case LY_SG_ATTACK_ICMP_FLOOD:
      return family == 4 ? protocol == IP_PROTOCOL_ICMP : protocol == 58;
    default:
      return 0;
    }
}

static int
ly_sg_take_token (ly_sg_rule_t *rule, f64 now)
{
  int allowed;
  clib_spinlock_lock (&rule->lock);
  if (rule->last_update == 0)
    rule->last_update = now;
  rule->tokens += (now - rule->last_update) * rule->threshold_pps;
  if (rule->tokens > rule->burst_packets)
    rule->tokens = rule->burst_packets;
  rule->last_update = now;
  allowed = rule->tokens >= 1;
  if (allowed)
    {
      rule->tokens -= 1;
      rule->conform++;
    }
  else
    rule->exceed++;
  clib_spinlock_unlock (&rule->lock);
  return allowed;
}

static int
ly_sg_feature_needed (u8 family, u32 sw_if_index)
{
  for (u32 i = 0; i < ly_sg_main.rule_count; i++)
    if (ly_sg_main.rules[i].enabled && ly_sg_main.rules[i].family == family &&
        ly_sg_main.rules[i].sw_if_index == sw_if_index)
      return 1;
  return 0;
}

static_always_inline void
ly_sg_process (vlib_main_t *vm, vlib_node_runtime_t *node,
               vlib_frame_t *frame, u8 family)
{
  u32 *from = vlib_frame_vector_args (frame);
  u16 nexts[VLIB_FRAME_SIZE];
  u32 index;
  f64 now = vlib_time_now (vm);
  for (index = 0; index < frame->n_vectors; index++)
    {
      vlib_buffer_t *buffer = vlib_get_buffer (vm, from[index]);
      u32 sw_if_index = vnet_buffer (buffer)->sw_if_index[VLIB_RX];
      u8 *data = vlib_buffer_get_current (buffer);
      u8 protocol = 0, tcp_flags = 0;
      u8 source[16] = { 0 }, destination[16] = { 0 };
      if (family == 4)
        {
          ip4_header_t *ip4 = (ip4_header_t *) data;
          u32 header_bytes = ip4_header_bytes (ip4);
          protocol = ip4->protocol;
          clib_memcpy_fast (source, &ip4->src_address, 4);
          clib_memcpy_fast (destination, &ip4->dst_address, 4);
          if (protocol == IP_PROTOCOL_TCP &&
              vlib_buffer_length_in_chain (vm, buffer) >= header_bytes + 14)
            tcp_flags = ((tcp_header_t *) (data + header_bytes))->flags;
        }
      else
        {
          ip6_header_t *ip6 = (ip6_header_t *) data;
          protocol = ip6->protocol;
          clib_memcpy_fast (source, &ip6->src_address, 16);
          clib_memcpy_fast (destination, &ip6->dst_address, 16);
          if (protocol == IP_PROTOCOL_TCP &&
              vlib_buffer_length_in_chain (vm, buffer) >= sizeof (*ip6) + 14)
            tcp_flags = ((tcp_header_t *) (data + sizeof (*ip6)))->flags;
        }
      nexts[index] = 0;
      for (u32 rule_index = 0; rule_index < ly_sg_main.rule_count;
           rule_index++)
        {
          ly_sg_rule_t *rule = &ly_sg_main.rules[rule_index];
          if (!ly_sg_match_rule (rule, sw_if_index, family, protocol,
                                 tcp_flags, source, destination))
            continue;
          clib_spinlock_lock (&rule->lock);
          rule->matched++;
          clib_spinlock_unlock (&rule->lock);
          if (ly_sg_take_token (rule, now))
            {
              vlib_node_increment_counter (vm, node->node_index,
                                           LY_SG_ERROR_PASSED, 1);
              continue;
            }
          if (rule->enforcement == LY_SG_ALERT)
            {
              clib_spinlock_lock (&rule->lock);
              rule->alerts++;
              clib_spinlock_unlock (&rule->lock);
              vlib_node_increment_counter (vm, node->node_index,
                                           LY_SG_ERROR_ALERT, 1);
              continue;
            }
          clib_spinlock_lock (&rule->lock);
          rule->drops++;
          clib_spinlock_unlock (&rule->lock);
          nexts[index] = family == 4 ? ly_sg_main.error_drop_next_index_ip4 :
                                      ly_sg_main.error_drop_next_index_ip6;
          vlib_node_increment_counter (vm, node->node_index,
                                       LY_SG_ERROR_DROPPED, 1);
          break;
        }
      if (nexts[index] == 0)
        vnet_feature_next_u16 (&nexts[index], buffer);
    }
  vlib_buffer_enqueue_to_next (vm, node, from, nexts, frame->n_vectors);
}

VLIB_NODE_FN (ly_sg_ip4_node) (vlib_main_t *vm, vlib_node_runtime_t *node,
                               vlib_frame_t *frame)
{
  ly_sg_process (vm, node, frame, 4);
  return frame->n_vectors;
}

VLIB_NODE_FN (ly_sg_ip6_node) (vlib_main_t *vm, vlib_node_runtime_t *node,
                               vlib_frame_t *frame)
{
  ly_sg_process (vm, node, frame, 6);
  return frame->n_vectors;
}

VLIB_REGISTER_NODE (ly_sg_ip4_node) = {
  .name = "ly-route-security-guard-ip4",
  .vector_size = sizeof (u32),
  .n_errors = LY_SG_N_ERROR,
  .error_strings = ly_sg_error_strings,
};

VLIB_REGISTER_NODE (ly_sg_ip6_node) = {
  .name = "ly-route-security-guard-ip6",
  .vector_size = sizeof (u32),
  .n_errors = LY_SG_N_ERROR,
  .error_strings = ly_sg_error_strings,
};

VNET_FEATURE_INIT (ly_sg_ip4_feature, static) = {
  .arc_name = "ip4-unicast",
  .node_name = "ly-route-security-guard-ip4",
  .runs_before = VNET_FEATURES ("ip4-lookup"),
};

VNET_FEATURE_INIT (ly_sg_ip6_feature, static) = {
  .arc_name = "ip6-unicast",
  .node_name = "ly-route-security-guard-ip6",
  .runs_before = VNET_FEATURES ("ip6-lookup"),
};

static int
ly_sg_parse_attack (char *value, ly_sg_attack_type_t *type)
{
  if (!strcmp (value, "syn_flood"))
    *type = LY_SG_ATTACK_SYN_FLOOD;
  else if (!strcmp (value, "new_connection_rate"))
    *type = LY_SG_ATTACK_NEW_CONNECTION_RATE;
  else if (!strcmp (value, "udp_flood"))
    *type = LY_SG_ATTACK_UDP_FLOOD;
  else if (!strcmp (value, "icmp_flood"))
    *type = LY_SG_ATTACK_ICMP_FLOOD;
  else
    return 0;
  return 1;
}

static clib_error_t *
ly_sg_set_command_fn (vlib_main_t *vm, unformat_input_t *input,
                      vlib_cli_command_t *cmd)
{
  u8 *id = 0, *attack = 0;
  u32 sw_if_index = LY_SG_INVALID_INDEX, threshold = 0, burst = 0;
  u32 family = 4, source_len = 0, destination_len = 0;
  ip4_address_t source4 = { 0 }, destination4 = { 0 };
  ip6_address_t source6 = { 0 }, destination6 = { 0 };
  u8 has_source = 0, has_destination = 0, enable = 1, enforce = 1;
  u8 old_enabled = 0, old_family = 0;
  u32 old_sw_if_index = LY_SG_INVALID_INDEX;
  while (unformat_check_input (input) != UNFORMAT_END_OF_INPUT)
    {
      if (unformat (input, "rule %s", &id))
        ;
      else if (unformat (input, "interface %U", unformat_vnet_sw_interface,
                         ly_sg_main.vnet_main, &sw_if_index))
        ;
      else if (unformat (input, "family ip4"))
        family = 4;
      else if (unformat (input, "family ip6"))
        family = 6;
      else if (unformat (input, "attack-type %s", &attack))
        ;
      else if (unformat (input, "threshold-pps %u", &threshold))
        ;
      else if (unformat (input, "burst-packets %u", &burst))
        ;
      else if (unformat (input, "mode alert"))
        enforce = 0;
      else if (unformat (input, "mode enforce"))
        enforce = 1;
      else if (family == 4 &&
               unformat (input, "source %U/%u", unformat_ip4_address,
                         &source4, &source_len))
        has_source = 1;
      else if (family == 4 &&
               unformat (input, "destination %U/%u", unformat_ip4_address,
                         &destination4, &destination_len))
        has_destination = 1;
      else if (family == 6 &&
               unformat (input, "source %U/%u", unformat_ip6_address,
                         &source6, &source_len))
        has_source = 1;
      else if (family == 6 &&
               unformat (input, "destination %U/%u", unformat_ip6_address,
                         &destination6, &destination_len))
        has_destination = 1;
      else if (unformat (input, "disable"))
        enable = 0;
      else
        return clib_error_return (0, "unknown input '%U'",
                                  format_unformat_error, input);
    }
  (void) source6;
  (void) destination6;
  (void) vm;
  (void) cmd;
  ly_sg_attack_type_t attack_type;
  if (!id || !id[0] || sw_if_index == LY_SG_INVALID_INDEX || !attack ||
      !attack[0] ||
      threshold == 0 || burst == 0 || (has_source && source_len > (family == 4 ? 32 : 128)) ||
      (has_destination && destination_len > (family == 4 ? 32 : 128)) ||
      !ly_sg_parse_attack ((char *) attack, &attack_type))
    {
      vec_free (id);
      vec_free (attack);
      return clib_error_return (0, "invalid security guard rule");
    }
  ly_sg_rule_t *rule = 0;
  for (u32 i = 0; i < ly_sg_main.rule_count; i++)
    if (!strcmp (ly_sg_main.rules[i].id, id))
      rule = &ly_sg_main.rules[i];
  if (!rule)
    {
      if (ly_sg_main.rule_count == LY_SG_MAX_RULES)
        return clib_error_return (0, "security guard rule capacity reached");
      rule = &ly_sg_main.rules[ly_sg_main.rule_count++];
      clib_spinlock_init (&rule->lock);
    }
  else
    {
      old_enabled = rule->enabled;
      old_family = rule->family;
      old_sw_if_index = rule->sw_if_index;
    }
  clib_memset (rule, 0, sizeof (*rule));
  clib_spinlock_init (&rule->lock);
  rule->enabled = enable;
  rule->family = family == 6 ? 6 : 4;
  rule->sw_if_index = sw_if_index;
  rule->threshold_pps = threshold;
  rule->burst_packets = burst;
  rule->tokens = burst;
  rule->attack_type = attack_type;
  rule->enforcement = enforce ? LY_SG_ENFORCE : LY_SG_ALERT;
  clib_strncpy (rule->id, (char *) id, sizeof (rule->id));
  if (has_source && family == 4)
    {
      rule->source_prefix_len = source_len;
      clib_memcpy_fast (rule->source, &source4, 4);
    }
  if (has_destination && family == 4)
    {
      rule->destination_prefix_len = destination_len;
      clib_memcpy_fast (rule->destination, &destination4, 4);
    }
  if (has_source && family == 6)
    {
      rule->source_prefix_len = source_len;
      clib_memcpy_fast (rule->source, &source6, 16);
    }
  if (has_destination && family == 6)
    {
      rule->destination_prefix_len = destination_len;
      clib_memcpy_fast (rule->destination, &destination6, 16);
    }
  if (old_enabled &&
      !ly_sg_feature_needed (old_family, old_sw_if_index))
    vnet_feature_enable_disable (old_family == 4 ? "ip4-unicast" :
                                    "ip6-unicast",
                                 old_family == 4 ?
                                   "ly-route-security-guard-ip4" :
                                   "ly-route-security-guard-ip6",
                                 old_sw_if_index, 0, 0, 0);
  if (enable)
    {
      int rv = vnet_feature_enable_disable (
        family == 4 ? "ip4-unicast" : "ip6-unicast",
        family == 4 ? "ly-route-security-guard-ip4" :
                      "ly-route-security-guard-ip6",
        sw_if_index, 1, 0, 0);
      if (rv)
        {
          vec_free (id);
          vec_free (attack);
          return clib_error_return (0,
                                    "security guard feature attachment failed: %d",
                                    rv);
        }
    }
  vec_free (id);
  vec_free (attack);
  return 0;
}

VLIB_CLI_COMMAND (ly_sg_set_command, static) = {
  .path = "set ly-route security-guard",
  .short_help = "set ly-route security-guard rule <id> interface <interface> "
                "family ip4|ip6 attack-type <type> threshold-pps <n> "
                "burst-packets <n> mode alert|enforce [source A.B.C.D/N] "
                "[destination A.B.C.D/N] [disable]",
  .function = ly_sg_set_command_fn,
};

static clib_error_t *
ly_sg_delete_command_fn (vlib_main_t *vm, unformat_input_t *input,
                         vlib_cli_command_t *cmd)
{
  u8 *id = 0;
  u32 rule_index = LY_SG_INVALID_INDEX;
  while (unformat_check_input (input) != UNFORMAT_END_OF_INPUT)
    {
      if (unformat (input, "rule %s", &id))
        ;
      else
        return clib_error_return (0, "unknown input '%U'",
                                  format_unformat_error, input);
    }
  if (!id || !id[0])
    {
      vec_free (id);
      return clib_error_return (0, "security guard rule ID is required");
    }
  for (u32 i = 0; i < ly_sg_main.rule_count; i++)
    if (!strcmp (ly_sg_main.rules[i].id, (char *) id))
      {
        rule_index = i;
        break;
      }
  if (rule_index == LY_SG_INVALID_INDEX)
    {
      vec_free (id);
      return clib_error_return (0, "security guard rule %s does not exist", id);
    }
  ly_sg_rule_t removed = ly_sg_main.rules[rule_index];
  if (rule_index + 1 < ly_sg_main.rule_count)
    clib_memcpy_fast (&ly_sg_main.rules[rule_index],
                      &ly_sg_main.rules[rule_index + 1],
                      (ly_sg_main.rule_count - rule_index - 1) *
                        sizeof (ly_sg_rule_t));
  ly_sg_main.rule_count--;
  if (removed.enabled &&
      !ly_sg_feature_needed (removed.family, removed.sw_if_index))
    vnet_feature_enable_disable (
      removed.family == 4 ? "ip4-unicast" : "ip6-unicast",
      removed.family == 4 ? "ly-route-security-guard-ip4" :
                            "ly-route-security-guard-ip6",
      removed.sw_if_index, 0, 0, 0);
  vec_free (id);
  (void) vm;
  (void) cmd;
  return 0;
}

VLIB_CLI_COMMAND (ly_sg_delete_command, static) = {
  .path = "delete ly-route security-guard",
  .short_help = "delete ly-route security-guard rule <id>",
  .function = ly_sg_delete_command_fn,
};

static clib_error_t *
ly_sg_show_command_fn (vlib_main_t *vm, unformat_input_t *input,
                       vlib_cli_command_t *cmd)
{
  for (u32 i = 0; i < ly_sg_main.rule_count; i++)
    {
      ly_sg_rule_t *rule = &ly_sg_main.rules[i];
      vlib_cli_output (vm, "rule %s enabled %u family %u interface %U "
                       "threshold-pps %u burst-packets %u matched %llu "
                       "conform %llu exceed %llu alerts %llu drops %llu",
                       rule->id, rule->enabled, rule->family,
                       format_vnet_sw_if_index_name, ly_sg_main.vnet_main,
                       rule->sw_if_index, rule->threshold_pps,
                       rule->burst_packets, rule->matched, rule->conform,
                       rule->exceed, rule->alerts, rule->drops);
    }
  (void) input;
  (void) cmd;
  return 0;
}

VLIB_CLI_COMMAND (ly_sg_show_command, static) = {
  .path = "show ly-route security-guard",
  .short_help = "show ly-route security-guard",
  .function = ly_sg_show_command_fn,
};

static clib_error_t *
ly_sg_init (vlib_main_t *vm)
{
  ly_sg_main.vlib_main = vm;
  ly_sg_main.vnet_main = vnet_get_main ();
  ly_sg_main.error_drop_next_index_ip4 =
    vlib_node_add_next (vm, ly_sg_ip4_node.index,
                        vlib_get_node_by_name (vm, (u8 *) "error-drop")
                          ->index);
  ly_sg_main.error_drop_next_index_ip6 =
    vlib_node_add_next (vm, ly_sg_ip6_node.index,
                        vlib_get_node_by_name (vm, (u8 *) "error-drop")
                          ->index);
  return 0;
}

VLIB_INIT_FUNCTION (ly_sg_init);

VLIB_PLUGIN_REGISTER () = {
  .version = VPP_BUILD_VER,
  .description = "Ly Route protocol-aware security rate guard",
};
