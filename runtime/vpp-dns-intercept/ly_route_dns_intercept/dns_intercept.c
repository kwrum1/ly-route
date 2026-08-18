#include <arpa/inet.h>
#include <string.h>

#include <vlib/vlib.h>
#include <vnet/feature/feature.h>
#include <vnet/fib/fib_table.h>
#include <vnet/ip/ip4.h>
#include <vnet/plugin/plugin.h>
#include <vnet/udp/udp_packet.h>
#include <vpp/app/version.h>

/*
 * The production VPP 25.10 packages register plugin CLI commands by linking
 * them into the global command list. Their runtime does not export the newer
 * vlib_cli_command_registration_helper used by the development headers.
 */
#define LY_ROUTE_VLIB_CLI_COMMAND(x, ...)                                    \
  __VA_ARGS__ vlib_cli_command_t x;                                           \
  static void __ly_route_cli_registration_##x (void)                          \
    __attribute__ ((__constructor__));                                        \
  static void __ly_route_cli_registration_##x (void)                          \
  {                                                                            \
    vlib_global_main_t *vgm = vlib_get_global_main ();                        \
    vlib_cli_main_t *cm = &vgm->cli_main;                                     \
    x.next_cli_command = cm->cli_command_registrations;                       \
    cm->cli_command_registrations = &x;                                       \
  }                                                                            \
  __VA_ARGS__ vlib_cli_command_t x

typedef enum
{
  LY_DNS_INTERCEPT_ERROR_INTERCEPTED,
  LY_DNS_INTERCEPT_ERROR_PASSED,
  LY_DNS_INTERCEPT_N_ERROR,
} ly_dns_intercept_error_t;

static char *ly_dns_intercept_error_strings[] = {
  "DNS packets intercepted",
  "packets passed",
};

typedef struct
{
  vlib_main_t *vlib_main;
  vnet_main_t *vnet_main;
  u32 sw_if_index;
  u32 fib_index;
  u32 ip4_lookup_next;
  u8 enabled;
} ly_dns_intercept_main_t;

static ly_dns_intercept_main_t ly_dns_intercept_main;

static_always_inline int
ly_dns_intercept_packet (vlib_buffer_t *buffer)
{
  u8 *data = vlib_buffer_get_current (buffer);
  ip4_header_t *ip4 = (ip4_header_t *) data;
  u32 header_bytes = ip4_header_bytes (ip4);
  if (vnet_buffer (buffer)->sw_if_index[VLIB_RX] !=
      ly_dns_intercept_main.sw_if_index ||
      ip4->protocol != IP_PROTOCOL_UDP && ip4->protocol != IP_PROTOCOL_TCP)
    return 0;

  if (vlib_buffer_length_in_chain (ly_dns_intercept_main.vlib_main, buffer) <
      header_bytes + sizeof (udp_header_t))
    return 0;

  udp_header_t *transport = (udp_header_t *) (data + header_bytes);
  return transport->dst_port == clib_host_to_net_u16 (53);
}

VLIB_NODE_FN (ly_dns_intercept_ip4_node) (vlib_main_t *vm,
                                           vlib_node_runtime_t *node,
                                           vlib_frame_t *frame)
{
  u32 *from = vlib_frame_vector_args (frame);
  u16 nexts[VLIB_FRAME_SIZE];
  u32 index;

  for (index = 0; index < frame->n_vectors; index++)
    {
      vlib_buffer_t *buffer = vlib_get_buffer (vm, from[index]);
      nexts[index] = 0;
      if (ly_dns_intercept_main.enabled && ly_dns_intercept_packet (buffer))
        {
            // VPP 25.x ip4-lookup honors the TX FIB selector here. Keep this
            // as the FIB index selected from the dedicated DNS route table.
            vnet_buffer (buffer)->sw_if_index[VLIB_TX] =
              ly_dns_intercept_main.fib_index;
          nexts[index] = ly_dns_intercept_main.ip4_lookup_next;
          vlib_node_increment_counter (vm, node->node_index,
                                       LY_DNS_INTERCEPT_ERROR_INTERCEPTED, 1);
        }
      else
        {
          vnet_feature_next_u16 (&nexts[index], buffer);
          vlib_node_increment_counter (vm, node->node_index,
                                       LY_DNS_INTERCEPT_ERROR_PASSED, 1);
        }
    }
  vlib_buffer_enqueue_to_next (vm, node, from, nexts, frame->n_vectors);
  return frame->n_vectors;
}

VLIB_REGISTER_NODE (ly_dns_intercept_ip4_node) = {
  .name = "ly-route-dns-intercept-ip4",
  .vector_size = sizeof (u32),
  .n_errors = LY_DNS_INTERCEPT_N_ERROR,
  .error_strings = ly_dns_intercept_error_strings,
};

VNET_FEATURE_INIT (ly_dns_intercept_ip4_feature, static) = {
  .arc_name = "ip4-unicast",
  .node_name = "ly-route-dns-intercept-ip4",
  .runs_before = VNET_FEATURES ("policer-input",
                                "acl-plugin-in-ip4-fa",
                                "ly-route-pre-nat-route-ip4",
                                "nat44-ed-in2out",
                                "nat44-ed-in2out-output",
                                "nat44-ei-in2out",
                                "nat44-ei-in2out-output",
                                "ip4-lookup"),
};

static clib_error_t *
ly_dns_intercept_set_command_fn (vlib_main_t *vm, unformat_input_t *input,
                                 vlib_cli_command_t *cmd)
{
  u32 sw_if_index = ~0, table_id = 0;
  u8 disable = 0;
  while (unformat_check_input (input) != UNFORMAT_END_OF_INPUT)
    {
      if (unformat (input, "interface %U", unformat_vnet_sw_interface,
                    ly_dns_intercept_main.vnet_main, &sw_if_index))
        ;
      else if (unformat (input, "table %u", &table_id))
        ;
      else if (unformat (input, "disable"))
        disable = 1;
      else
        return clib_error_return (0, "unknown input '%U'",
                                  format_unformat_error, input);
    }

  if (ly_dns_intercept_main.enabled)
    vnet_feature_enable_disable ("ip4-unicast", "ly-route-dns-intercept-ip4",
                                 ly_dns_intercept_main.sw_if_index, 0, 0, 0);
  ly_dns_intercept_main.enabled = 0;
  if (disable)
    return 0;
  if (sw_if_index == ~0 || table_id == 0)
    return clib_error_return (0,
                              "DNS interception requires interface and table");

  u32 fib_index = fib_table_find (FIB_PROTOCOL_IP4, table_id);
  if (fib_index == FIB_NODE_INDEX_INVALID)
    return clib_error_return (0, "IPv4 table %u does not exist", table_id);
  ly_dns_intercept_main.sw_if_index = sw_if_index;
  ly_dns_intercept_main.fib_index = fib_index;
  ly_dns_intercept_main.enabled = 1;
  int rv = vnet_feature_enable_disable ("ip4-unicast",
                                        "ly-route-dns-intercept-ip4",
                                        sw_if_index, 1, 0, 0);
  if (rv)
    {
      ly_dns_intercept_main.enabled = 0;
      return clib_error_return (0, "DNS interception attachment failed: %d",
                                rv);
    }
  (void) vm;
  (void) cmd;
  return 0;
}

LY_ROUTE_VLIB_CLI_COMMAND (ly_dns_intercept_set_command, static) = {
  .path = "set ly-route dns-intercept",
  .short_help = "set ly-route dns-intercept interface <interface> table <id> [disable]",
  .function = ly_dns_intercept_set_command_fn,
};

static clib_error_t *
ly_dns_intercept_show_command_fn (vlib_main_t *vm, unformat_input_t *input,
                                  vlib_cli_command_t *cmd)
{
  vlib_cli_output (vm, "enabled %u interface %U fib-index %u",
                   ly_dns_intercept_main.enabled,
                   format_vnet_sw_if_index_name, ly_dns_intercept_main.vnet_main,
                   ly_dns_intercept_main.sw_if_index,
                   ly_dns_intercept_main.fib_index);
  (void) input;
  (void) cmd;
  return 0;
}

LY_ROUTE_VLIB_CLI_COMMAND (ly_dns_intercept_show_command, static) = {
  .path = "show ly-route dns-intercept",
  .short_help = "show ly-route dns-intercept",
  .function = ly_dns_intercept_show_command_fn,
};

static clib_error_t *
ly_dns_intercept_init (vlib_main_t *vm)
{
  ly_dns_intercept_main.vlib_main = vm;
  ly_dns_intercept_main.vnet_main = vnet_get_main ();
  vlib_node_t *lookup = vlib_get_node_by_name (vm, (u8 *) "ip4-lookup");
  if (!lookup)
    return clib_error_return (0, "ip4-lookup node is unavailable");
  ly_dns_intercept_main.ip4_lookup_next =
    vlib_node_add_next (vm, ly_dns_intercept_ip4_node.index, lookup->index);
  ly_dns_intercept_main.sw_if_index = ~0;
  return 0;
}

VLIB_INIT_FUNCTION (ly_dns_intercept_init);

VLIB_PLUGIN_REGISTER () = {
  .version = VPP_BUILD_VER,
  .description = "Ly Route native pre-NAT transparent DNS interception",
};
