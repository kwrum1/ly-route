#include <arpa/inet.h>
#include <string.h>

#include <vlib/vlib.h>
#include <vnet/feature/feature.h>
#include <vnet/fib/fib_table.h>
#include <vnet/ip/ip4.h>
#include <vnet/ip/format.h>
#include <vnet/plugin/plugin.h>
#include <vnet/tcp/tcp_packet.h>
#include <vnet/udp/udp_packet.h>
#include <vpp/app/version.h>

typedef enum
{
  LY_PRE_NAT_ROUTE_ERROR_MATCHED,
  LY_PRE_NAT_ROUTE_ERROR_BYPASSED_NAT,
  LY_PRE_NAT_ROUTE_ERROR_BYPASSED_ABF,
  LY_PRE_NAT_ROUTE_ERROR_PASSED,
  LY_PRE_NAT_ROUTE_N_ERROR,
} ly_pre_nat_route_error_t;

static char *ly_pre_nat_route_error_strings[] = {
  "pre-NAT route policies matched",
  "pre-NAT route policies sent directly to the selected FIB",
  "pre-NAT route policies bypassed post-NAT ABF",
  "packets passed",
};

typedef struct
{
  u32 id;
  u32 priority;
  u32 table_id;
  u32 fib_index;
  ip4_address_t source;
  ip4_address_t destination;
  u8 source_len;
  u8 destination_len;
  u8 protocol;
  u16 source_port_first;
  u16 source_port_last;
  u16 destination_port_first;
  u16 destination_port_last;
  u8 skip_nat;
  u8 bypass;
} ly_pre_nat_route_rule_t;

typedef struct
{
  u32 children[2];
  u32 *rule_indices;
} ly_pre_nat_route_trie_node_t;

typedef struct
{
  vlib_main_t *vlib_main;
  vnet_main_t *vnet_main;
  u32 sw_if_index;
  ip4_address_t lan_prefix;
  u8 lan_prefix_len;
  u32 pre_ip4_lookup_next;
  u32 post_ip4_lookup_next;
  u8 enabled;
  ly_pre_nat_route_rule_t *rules;
  ly_pre_nat_route_trie_node_t *trie;
} ly_pre_nat_route_main_t;

static ly_pre_nat_route_main_t ly_pre_nat_route_main;

#define LY_PRE_NAT_ROUTE_TAG 0x4c595250u

static_always_inline void
ly_pre_nat_route_set_tag (vlib_buffer_t *buffer, u32 fib_index)
{
  vnet_buffer2 (buffer)->unused[0] = LY_PRE_NAT_ROUTE_TAG;
  vnet_buffer2 (buffer)->unused[1] = fib_index;
}

static int
ly_pre_nat_route_prefix_matches (ip4_address_t value, ip4_address_t prefix,
                                 u8 prefix_len)
{
  if (prefix_len == 0)
    return 1;
  u32 host_mask = prefix_len == 32 ? ~0u : (~0u << (32 - prefix_len));
  u32 mask = clib_host_to_net_u32 (host_mask);
  return (value.as_u32 & mask) == (prefix.as_u32 & mask);
}

static void
ly_pre_nat_route_trie_reset (void)
{
  ly_pre_nat_route_trie_node_t *node;
  vec_foreach (node, ly_pre_nat_route_main.trie)
    vec_free (node->rule_indices);
  vec_free (ly_pre_nat_route_main.trie);

  vec_add2 (ly_pre_nat_route_main.trie, node, 1);
  clib_memset (node, 0, sizeof (*node));
  node->children[0] = node->children[1] = ~0;
}

static u32
ly_pre_nat_route_trie_new_node (void)
{
  ly_pre_nat_route_trie_node_t *node;
  vec_add2 (ly_pre_nat_route_main.trie, node, 1);
  clib_memset (node, 0, sizeof (*node));
  node->children[0] = node->children[1] = ~0;
  return (u32) (node - ly_pre_nat_route_main.trie);
}

static void
ly_pre_nat_route_trie_insert (u32 rule_index)
{
  ly_pre_nat_route_rule_t *rule =
    vec_elt_at_index (ly_pre_nat_route_main.rules, rule_index);
  u32 address = clib_net_to_host_u32 (rule->destination.as_u32);
  u32 node_index = 0;

  for (u32 depth = 0; depth < rule->destination_len; depth++)
    {
      u32 bit = (address >> (31 - depth)) & 1;
      u32 child = ly_pre_nat_route_main.trie[node_index].children[bit];
      if (child == ~0)
        {
          child = ly_pre_nat_route_trie_new_node ();
          ly_pre_nat_route_main.trie[node_index].children[bit] = child;
        }
      node_index = child;
    }
  vec_add1 (ly_pre_nat_route_main.trie[node_index].rule_indices, rule_index);
}

static void
ly_pre_nat_route_trie_rebuild (void)
{
  ly_pre_nat_route_trie_reset ();
  for (u32 index = 0; index < vec_len (ly_pre_nat_route_main.rules); index++)
    ly_pre_nat_route_trie_insert (index);
}

static int
ly_pre_nat_route_rule_matches (ly_pre_nat_route_rule_t *rule,
                               ip4_header_t *ip4, u32 packet_length)
{
  if (!ly_pre_nat_route_prefix_matches (ip4->src_address, rule->source,
                                        rule->source_len) ||
      !ly_pre_nat_route_prefix_matches (ip4->dst_address, rule->destination,
                                        rule->destination_len))
    return 0;
  if (rule->protocol != 0 && rule->protocol != ip4->protocol)
    return 0;
  if (rule->source_port_first == 0 && rule->source_port_last == 65535 &&
      rule->destination_port_first == 0 &&
      rule->destination_port_last == 65535)
    return 1;
  if (ip4->protocol != IP_PROTOCOL_TCP && ip4->protocol != IP_PROTOCOL_UDP)
    return 0;
  u32 header_bytes = ip4_header_bytes (ip4);
  if (packet_length < header_bytes + sizeof (udp_header_t))
    return 0;
  udp_header_t *transport = (udp_header_t *) ((u8 *) ip4 + header_bytes);
  u16 source_port = clib_net_to_host_u16 (transport->src_port);
  u16 destination_port = clib_net_to_host_u16 (transport->dst_port);
  return source_port >= rule->source_port_first &&
         source_port <= rule->source_port_last &&
         destination_port >= rule->destination_port_first &&
         destination_port <= rule->destination_port_last;
}

static ly_pre_nat_route_rule_t *
ly_pre_nat_route_match (vlib_buffer_t *buffer)
{
  if (!ly_pre_nat_route_main.enabled ||
      vnet_buffer (buffer)->sw_if_index[VLIB_RX] !=
        ly_pre_nat_route_main.sw_if_index)
    return 0;
  ip4_header_t *ip4 = vlib_buffer_get_current (buffer);
  if (ly_pre_nat_route_prefix_matches (ip4->dst_address,
                                       ly_pre_nat_route_main.lan_prefix,
                                       ly_pre_nat_route_main.lan_prefix_len))
    return 0;
  u32 packet_length = vlib_buffer_length_in_chain (
    ly_pre_nat_route_main.vlib_main, buffer);
  u32 address = clib_net_to_host_u32 (ip4->dst_address.as_u32);
  u32 node_index = 0;
  u32 best_rule_index = ~0;

  /* A policy priority is authoritative across overlapping prefixes. Walk the
   * destination radix path once and evaluate only rules stored on matching
   * prefix nodes; this keeps GeoIP lookup bounded to 33 nodes instead of a
   * linear scan over every provider prefix. */
  for (u32 depth = 0; depth <= 32; depth++)
    {
      ly_pre_nat_route_trie_node_t *node =
        vec_elt_at_index (ly_pre_nat_route_main.trie, node_index);
      u32 *rule_index;
      vec_foreach (rule_index, node->rule_indices)
        {
          ly_pre_nat_route_rule_t *rule =
            vec_elt_at_index (ly_pre_nat_route_main.rules, rule_index[0]);
          if (!ly_pre_nat_route_rule_matches (rule, ip4, packet_length))
            continue;
          if (best_rule_index == ~0)
            best_rule_index = rule_index[0];
          else
            {
              ly_pre_nat_route_rule_t *best = vec_elt_at_index (
                ly_pre_nat_route_main.rules, best_rule_index);
              if (rule->priority < best->priority ||
                  (rule->priority == best->priority && rule->id < best->id))
                best_rule_index = rule_index[0];
            }
        }
      if (depth == 32)
        break;
      u32 bit = (address >> (31 - depth)) & 1;
      node_index = node->children[bit];
      if (node_index == ~0)
        break;
    }
  return best_rule_index == ~0 ? 0 :
    vec_elt_at_index (ly_pre_nat_route_main.rules, best_rule_index);
}

VLIB_NODE_FN (ly_pre_nat_route_ip4_node) (vlib_main_t *vm,
                                           vlib_node_runtime_t *node,
                                           vlib_frame_t *frame)
{
  u32 *from = vlib_frame_vector_args (frame);
  u16 nexts[VLIB_FRAME_SIZE];
  for (u32 index = 0; index < frame->n_vectors; index++)
    {
      vlib_buffer_t *buffer = vlib_get_buffer (vm, from[index]);
      ly_pre_nat_route_rule_t *rule = ly_pre_nat_route_match (buffer);
      if (rule)
        {
          if (rule->bypass)
            {
              /* Port-mapping replies belong to an existing NAT session.  Let
               * NAT restore that session before ordinary policy routing can
               * select a proxy FIB for the same LAN host. */
              vnet_feature_next_u16 (&nexts[index], buffer);
              vlib_node_increment_counter (
                vm, node->node_index, LY_PRE_NAT_ROUTE_ERROR_PASSED, 1);
              continue;
            }
          if (rule->skip_nat)
            {
              // Transparent-proxy paths must retain the original LAN source
              // address for TPROXY. Enter the selected FIB immediately: the
              // NAT worker handoff does not resume the remaining feature arc.
              vnet_buffer (buffer)->sw_if_index[VLIB_TX] = rule->fib_index;
              nexts[index] = ly_pre_nat_route_main.pre_ip4_lookup_next;
              vlib_node_increment_counter (
                vm, node->node_index,
                LY_PRE_NAT_ROUTE_ERROR_BYPASSED_NAT, 1);
              continue;
            }
          // Keep the selected FIB through NAT. The post-NAT feature consumes
          // this override before legacy ABF rules can observe the rewritten
          // source address and replace the intended path.
          vnet_buffer (buffer)->sw_if_index[VLIB_TX] = rule->fib_index;
          ly_pre_nat_route_set_tag (buffer, rule->fib_index);
          vlib_node_increment_counter (vm, node->node_index,
                                       LY_PRE_NAT_ROUTE_ERROR_MATCHED, 1);
        }
      else
        vlib_node_increment_counter (vm, node->node_index,
                                     LY_PRE_NAT_ROUTE_ERROR_PASSED, 1);
      vnet_feature_next_u16 (&nexts[index], buffer);
    }
  vlib_buffer_enqueue_to_next (vm, node, from, nexts, frame->n_vectors);
  return frame->n_vectors;
}

VLIB_NODE_FN (ly_pre_nat_route_post_ip4_node) (vlib_main_t *vm,
                                                vlib_node_runtime_t *node,
                                                vlib_frame_t *frame)
{
  u32 *from = vlib_frame_vector_args (frame);
  u16 nexts[VLIB_FRAME_SIZE];
  for (u32 index = 0; index < frame->n_vectors; index++)
    {
      vlib_buffer_t *buffer = vlib_get_buffer (vm, from[index]);
      u32 fib_index = vnet_buffer2 (buffer)->unused[1];
      int selected = vnet_buffer2 (buffer)->unused[0] == LY_PRE_NAT_ROUTE_TAG;
      if (selected)
        {
          vnet_buffer2 (buffer)->unused[0] = 0;
          vnet_buffer2 (buffer)->unused[1] = 0;
          vnet_buffer (buffer)->sw_if_index[VLIB_TX] = fib_index;
          nexts[index] = ly_pre_nat_route_main.post_ip4_lookup_next;
          vlib_node_increment_counter (vm, node->node_index,
                                       LY_PRE_NAT_ROUTE_ERROR_BYPASSED_ABF, 1);
        }
      else
        {
          vnet_feature_next_u16 (&nexts[index], buffer);
          vlib_node_increment_counter (vm, node->node_index,
                                       LY_PRE_NAT_ROUTE_ERROR_PASSED, 1);
        }
    }
  vlib_buffer_enqueue_to_next (vm, node, from, nexts, frame->n_vectors);
  return frame->n_vectors;
}

VLIB_REGISTER_NODE (ly_pre_nat_route_ip4_node) = {
  .name = "ly-route-pre-nat-route-ip4",
  .vector_size = sizeof (u32),
  .n_errors = LY_PRE_NAT_ROUTE_N_ERROR,
  .error_strings = ly_pre_nat_route_error_strings,
};

VLIB_REGISTER_NODE (ly_pre_nat_route_post_ip4_node) = {
  .name = "ly-route-pre-nat-route-post-ip4",
  .vector_size = sizeof (u32),
  .n_errors = LY_PRE_NAT_ROUTE_N_ERROR,
  .error_strings = ly_pre_nat_route_error_strings,
};

VNET_FEATURE_INIT (ly_pre_nat_route_ip4_feature, static) = {
  .arc_name = "ip4-unicast",
  .node_name = "ly-route-pre-nat-route-ip4",
  /* Security and rate limiting must see every LAN packet, including traffic
   * selected for a transparent proxy.  Classification still runs before NAT,
   * so the original client address remains available to policy matching. */
  .runs_after = VNET_FEATURES ("policer-input", "acl-plugin-in-ip4-fa"),
  .runs_before = VNET_FEATURES ("nat44-ed-in2out", "nat44-ei-in2out"),
};

VNET_FEATURE_INIT (ly_pre_nat_route_post_ip4_feature, static) = {
  .arc_name = "ip4-unicast",
  .node_name = "ly-route-pre-nat-route-post-ip4",
  .runs_after = VNET_FEATURES ("nat44-ed-in2out", "nat44-ei-in2out"),
  .runs_before = VNET_FEATURES ("abf-input-ip4"),
};

static void
ly_pre_nat_route_delete_id (u32 id)
{
  if (!ly_pre_nat_route_main.rules)
    return;

  u32 write = 0;
  for (u32 read = 0; read < vec_len (ly_pre_nat_route_main.rules); read++)
    if (ly_pre_nat_route_main.rules[read].id != id)
      ly_pre_nat_route_main.rules[write++] = ly_pre_nat_route_main.rules[read];
  if (write == 0)
    vec_free (ly_pre_nat_route_main.rules);
  else
    _vec_set_len (ly_pre_nat_route_main.rules, write,
                  sizeof (ly_pre_nat_route_main.rules[0]));
  ly_pre_nat_route_trie_rebuild ();
}

static u8
ly_pre_nat_route_protocol (u8 *value, u8 *protocol)
{
  if (!value || !value[0] || !strcmp ((char *) value, "any"))
    *protocol = 0;
  else if (!strcmp ((char *) value, "tcp"))
    *protocol = IP_PROTOCOL_TCP;
  else if (!strcmp ((char *) value, "udp"))
    *protocol = IP_PROTOCOL_UDP;
  else if (!strcmp ((char *) value, "icmp"))
    *protocol = IP_PROTOCOL_ICMP;
  else
    return 0;
  return 1;
}

static clib_error_t *
ly_pre_nat_route_set_command_fn (vlib_main_t *vm, unformat_input_t *input,
                                 vlib_cli_command_t *cmd)
{
  u32 sw_if_index = ~0, id = 0, priority = 0, table_id = 0;
  u32 lan_prefix_len = 0, source_len = 0, destination_len = 0,
      source_port_first = 0,
      source_port_last = 65535, destination_port_first = 0,
      destination_port_last = 65535;
  ip4_address_t source = { 0 }, destination = { 0 }, lan_prefix = { 0 };
  u8 *verb = 0, *protocol_name = 0;
  u8 has_interface = 0, has_source = 0, has_destination = 0,
     skip_nat = 0, bypass = 0,
     has_lan_prefix = 0;

  while (unformat_check_input (input) != UNFORMAT_END_OF_INPUT)
    {
      if (unformat (input, "interface %U", unformat_vnet_sw_interface,
                    ly_pre_nat_route_main.vnet_main, &sw_if_index))
        has_interface = 1;
      else if (unformat (input, "lan-prefix %U/%u", unformat_ip4_address,
                         &lan_prefix, &lan_prefix_len))
        {
          has_lan_prefix = 1;
        }
      else if (unformat (input, "add"))
        verb = format (0, "add%c", 0);
      else if (unformat (input, "del"))
        verb = format (0, "del%c", 0);
      else if (unformat (input, "clear"))
        verb = format (0, "clear%c", 0);
      else if (unformat (input, "id %u", &id))
        ;
      else if (unformat (input, "priority %u", &priority))
        ;
      else if (unformat (input, "source %U/%u", unformat_ip4_address,
                         &source, &source_len))
        has_source = 1;
      else if (unformat (input, "destination %U/%u", unformat_ip4_address,
                         &destination, &destination_len))
        has_destination = 1;
      else if (unformat (input, "protocol %s", &protocol_name))
        ;
      else if (unformat (input, "sport %u-%u", &source_port_first,
                         &source_port_last))
        ;
      else if (unformat (input, "dport %u-%u", &destination_port_first,
                         &destination_port_last))
        ;
      else if (unformat (input, "table %u", &table_id))
        ;
      else if (unformat (input, "skip-nat"))
        skip_nat = 1;
      else if (unformat (input, "bypass"))
        bypass = 1;
      else
        {
          vec_free (verb);
          vec_free (protocol_name);
          return clib_error_return (0, "unknown input '%U'",
                                    format_unformat_error, input);
        }
    }

  if (has_interface)
    {
      if (!has_lan_prefix || lan_prefix_len > 32)
        return clib_error_return (0, "interface requires an IPv4 lan-prefix");
      if (ly_pre_nat_route_main.enabled)
        {
          vnet_feature_enable_disable ("ip4-unicast",
                                       "ly-route-pre-nat-route-ip4",
                                       ly_pre_nat_route_main.sw_if_index, 0,
                                       0, 0);
          vnet_feature_enable_disable ("ip4-unicast",
                                       "ly-route-pre-nat-route-post-ip4",
                                       ly_pre_nat_route_main.sw_if_index, 0,
                                       0, 0);
        }
      ly_pre_nat_route_main.sw_if_index = sw_if_index;
      ly_pre_nat_route_main.lan_prefix = lan_prefix;
      ly_pre_nat_route_main.lan_prefix_len = lan_prefix_len;
      ly_pre_nat_route_main.enabled = 1;
      int rv = vnet_feature_enable_disable ("ip4-unicast",
                                             "ly-route-pre-nat-route-ip4",
                                             sw_if_index, 1, 0, 0);
      rv |= vnet_feature_enable_disable ("ip4-unicast",
                                         "ly-route-pre-nat-route-post-ip4",
                                         sw_if_index, 1, 0, 0);
      if (rv)
        return clib_error_return (0, "pre-NAT route feature attachment failed: %d", rv);
      vec_free (verb);
      vec_free (protocol_name);
      return 0;
    }

  if (!verb || !verb[0])
    return clib_error_return (0, "missing route action");
  if (!strcmp ((char *) verb, "clear"))
    {
      vlib_worker_thread_barrier_sync (vm);
      vec_free (ly_pre_nat_route_main.rules);
      ly_pre_nat_route_trie_reset ();
      vlib_worker_thread_barrier_release (vm);
      vec_free (verb);
      vec_free (protocol_name);
      return 0;
    }
  if (id == 0)
    return clib_error_return (0, "route id is required");
  if (!strcmp ((char *) verb, "del"))
    {
      vlib_worker_thread_barrier_sync (vm);
      ly_pre_nat_route_delete_id (id);
      vlib_worker_thread_barrier_release (vm);
      vec_free (verb);
      vec_free (protocol_name);
      return 0;
    }
  if (strcmp ((char *) verb, "add") || !has_source || !has_destination ||
      source_len > 32 || destination_len > 32 || (!bypass && table_id == 0) ||
      source_port_first > source_port_last ||
      destination_port_first > destination_port_last ||
      !ly_pre_nat_route_main.enabled)
    return clib_error_return (0, "incomplete pre-NAT route rule");

  u8 protocol = 0;
  if (!ly_pre_nat_route_protocol (protocol_name, &protocol))
    return clib_error_return (0, "unsupported route protocol");
  u32 fib_index = 0;
  if (!bypass)
    {
      fib_index = fib_table_find (FIB_PROTOCOL_IP4, table_id);
      if (fib_index == FIB_NODE_INDEX_INVALID)
        return clib_error_return (0, "IPv4 table %u does not exist", table_id);
    }

  vlib_worker_thread_barrier_sync (vm);
  ly_pre_nat_route_rule_t *rule;
  vec_add2 (ly_pre_nat_route_main.rules, rule, 1);
  u32 rule_index = (u32) (rule - ly_pre_nat_route_main.rules);
  clib_memset (rule, 0, sizeof (*rule));
  rule->id = id;
  rule->priority = priority;
  rule->table_id = table_id;
  rule->fib_index = fib_index;
  rule->source = source;
  rule->destination = destination;
  rule->source_len = source_len;
  rule->destination_len = destination_len;
  rule->protocol = protocol;
  rule->source_port_first = source_port_first;
  rule->source_port_last = source_port_last;
  rule->destination_port_first = destination_port_first;
  rule->destination_port_last = destination_port_last;
  rule->skip_nat = skip_nat;
  rule->bypass = bypass;
  ly_pre_nat_route_trie_insert (rule_index);
  vlib_worker_thread_barrier_release (vm);
  vec_free (verb);
  vec_free (protocol_name);
  (void) vm;
  (void) cmd;
  return 0;
}

VLIB_CLI_COMMAND (ly_pre_nat_route_set_command, static) = {
  .path = "set ly-route pre-nat-route",
  .short_help = "set ly-route pre-nat-route interface <if> lan-prefix <prefix> | add id <id> priority <n> source <prefix> destination <prefix> protocol <any|tcp|udp|icmp> sport <first>-<last> dport <first>-<last> (table <id> [skip-nat] | bypass) | del id <id> | clear",
  .function = ly_pre_nat_route_set_command_fn,
};

static clib_error_t *
ly_pre_nat_route_show_command_fn (vlib_main_t *vm, unformat_input_t *input,
                                  vlib_cli_command_t *cmd)
{
  u8 detail = 0;
  if (unformat_check_input (input) != UNFORMAT_END_OF_INPUT)
    {
      if (!unformat (input, "detail"))
        return clib_error_return (0, "unknown input '%U'",
                                  format_unformat_error, input);
      detail = 1;
    }
  vlib_cli_output (vm,
                   "enabled %u interface %U lan-prefix %U/%u rules %u radix-nodes %u",
                   ly_pre_nat_route_main.enabled,
                   format_vnet_sw_if_index_name, ly_pre_nat_route_main.vnet_main,
                   ly_pre_nat_route_main.sw_if_index,
                   format_ip4_address, &ly_pre_nat_route_main.lan_prefix,
                   ly_pre_nat_route_main.lan_prefix_len,
                   vec_len (ly_pre_nat_route_main.rules),
                   vec_len (ly_pre_nat_route_main.trie));
  u32 *shown_ids = 0;
  ly_pre_nat_route_rule_t *rule;
  vec_foreach (rule, ly_pre_nat_route_main.rules)
    {
      if (detail)
        {
          vlib_cli_output (vm,
                           "rule id %u priority %u source %U/%u destination %U/%u protocol %u sport %u-%u dport %u-%u table %u fib-index %u skip-nat %u bypass %u",
                           rule->id, rule->priority, format_ip4_address,
                           &rule->source, rule->source_len, format_ip4_address,
                           &rule->destination, rule->destination_len,
                           rule->protocol, rule->source_port_first,
                           rule->source_port_last, rule->destination_port_first,
                           rule->destination_port_last, rule->table_id,
                           rule->fib_index, rule->skip_nat, rule->bypass);
          continue;
        }
      u8 seen = 0;
      u32 *shown;
      vec_foreach (shown, shown_ids)
        if (shown[0] == rule->id)
          {
            seen = 1;
            break;
          }
      if (seen)
        continue;
      vec_add1 (shown_ids, rule->id);
      u32 count = 0;
      ly_pre_nat_route_rule_t *candidate;
      vec_foreach (candidate, ly_pre_nat_route_main.rules)
        if (candidate->id == rule->id)
          count++;
      vlib_cli_output (vm,
                       "rule id %u priority %u prefixes %u table %u fib-index %u skip-nat %u bypass %u",
                       rule->id, rule->priority, count, rule->table_id,
                       rule->fib_index, rule->skip_nat, rule->bypass);
    }
  vec_free (shown_ids);
  (void) cmd;
  return 0;
}

VLIB_CLI_COMMAND (ly_pre_nat_route_show_command, static) = {
  .path = "show ly-route pre-nat-route",
  .short_help = "show ly-route pre-nat-route [detail]",
  .function = ly_pre_nat_route_show_command_fn,
};

static clib_error_t *
ly_pre_nat_route_init (vlib_main_t *vm)
{
  ly_pre_nat_route_main.vlib_main = vm;
  ly_pre_nat_route_main.vnet_main = vnet_get_main ();
  ly_pre_nat_route_main.sw_if_index = ~0;
  ly_pre_nat_route_trie_reset ();
  vlib_node_t *lookup = vlib_get_node_by_name (vm, (u8 *) "ip4-lookup");
  if (!lookup)
    return clib_error_return (0, "ip4-lookup node is unavailable");
  ly_pre_nat_route_main.pre_ip4_lookup_next = vlib_node_add_next (
    vm, ly_pre_nat_route_ip4_node.index, lookup->index);
  ly_pre_nat_route_main.post_ip4_lookup_next = vlib_node_add_next (
    vm, ly_pre_nat_route_post_ip4_node.index, lookup->index);
  return 0;
}

VLIB_INIT_FUNCTION (ly_pre_nat_route_init);

VLIB_PLUGIN_REGISTER () = {
  .version = VPP_BUILD_VER,
  .description = "Ly Route native pre-NAT policy routing",
};
