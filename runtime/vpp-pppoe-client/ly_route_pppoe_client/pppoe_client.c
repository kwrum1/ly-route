#include <vlib/vlib.h>
#include <vnet/ethernet/ethernet.h>
#include <vnet/feature/feature.h>
#include <vnet/interface.h>
#include <vnet/plugin/plugin.h>
#include <vpp/app/version.h>

#define LY_ROUTE_VLIB_CLI_COMMAND(x, ...)                                    \
  __VA_ARGS__ vlib_cli_command_t x;                                          \
  static void __ly_route_cli_registration_##x (void)                         \
    __attribute__ ((__constructor__));                                       \
  static void __ly_route_cli_registration_##x (void)                         \
  {                                                                          \
    vlib_global_main_t *vgm = vlib_get_global_main ();                       \
    vlib_cli_main_t *cm = &vgm->cli_main;                                    \
    x.next_cli_command = cm->cli_command_registrations;                      \
    cm->cli_command_registrations = &x;                                      \
  }                                                                          \
  __VA_ARGS__ vlib_cli_command_t x

typedef struct
{
  u32 control_sw_if_index;
  u32 wan_sw_if_index;
  u64 discovery_packets;
  u64 control_packets;
  u64 dhcp6_packets;
} ly_pppoe_client_binding_t;

typedef struct
{
  vlib_main_t *vlib_main;
  vnet_main_t *vnet_main;
  ly_pppoe_client_binding_t *bindings;
} ly_pppoe_client_main_t;

typedef enum
{
  LY_PPPOE_CLIENT_NEXT_INTERFACE_OUTPUT,
  LY_PPPOE_CLIENT_N_NEXT,
} ly_pppoe_client_next_t;

typedef enum
{
  LY_PPPOE_CLIENT_ERROR_PASSED,
  LY_PPPOE_CLIENT_ERROR_DISCOVERY_FORWARDED,
  LY_PPPOE_CLIENT_ERROR_CONTROL_FORWARDED,
  LY_PPPOE_CLIENT_ERROR_DHCP6_FORWARDED,
  LY_PPPOE_CLIENT_N_ERROR,
} ly_pppoe_client_error_t;

static char *ly_pppoe_client_error_strings[] = {
  "packets passed",
  "PPPoE discovery packets forwarded",
  "PPPoE control packets forwarded",
  "PPPoE DHCPv6 control packets forwarded",
};

static ly_pppoe_client_main_t ly_pppoe_client_main;
typedef struct
{
  u8 discovery;
  u32 pppoe_offset;
  u16 ppp_protocol;
} ly_pppoe_packet_t;

static_always_inline int
ly_pppoe_parse (vlib_buffer_t *buffer, ly_pppoe_packet_t *packet)
{
  u32 length = buffer->current_length;
  u32 offset = sizeof (ethernet_header_t);
  if (length < offset + 6)
    return 0;
  ethernet_header_t *ethernet = vlib_buffer_get_current (buffer);
  u16 type = clib_net_to_host_u16 (ethernet->type);

  if (type == ETHERNET_TYPE_VLAN)
    {
      if (length < offset + sizeof (ethernet_vlan_header_t) + 6)
        return 0;
      ethernet_vlan_header_t *vlan = (ethernet_vlan_header_t *) (ethernet + 1);
      type = clib_net_to_host_u16 (vlan->type);
      offset += sizeof (*vlan);
    }
  if (type != ETHERNET_TYPE_PPPOE_DISCOVERY &&
      type != ETHERNET_TYPE_PPPOE_SESSION)
    return 0;

  u8 *pppoe = (u8 *) ethernet + offset;
  if (pppoe[0] != 0x11)
    return 0;
  packet->discovery = type == ETHERNET_TYPE_PPPOE_DISCOVERY;
  packet->pppoe_offset = offset;
  packet->ppp_protocol = 0;
  if (!packet->discovery)
    {
      if (length < offset + 8 || pppoe[1] != 0)
        return 0;
      packet->ppp_protocol = ((u16) pppoe[6] << 8) | pppoe[7];
    }
  return 1;
}

static_always_inline int
ly_pppoe_is_control_protocol (u16 protocol)
{
  switch (protocol)
    {
    case 0xc021: /* LCP */
    case 0xc023: /* PAP */
    case 0xc223: /* CHAP */
    case 0xc227: /* EAP */
    case 0x8021: /* IPCP */
    case 0x8057: /* IPv6CP */
    case 0x80fd: /* CCP */
    case 0x8053: /* ECP */
      return 1;
    default:
      return 0;
    }
}

static_always_inline int
ly_pppoe_is_dhcp6 (vlib_buffer_t *buffer, u32 pppoe_offset,
                   int client_to_server)
{
  u8 *frame = vlib_buffer_get_current (buffer);
  u32 length = buffer->current_length;
  if (length < pppoe_offset + 8 + 40 + 8)
    return 0;
  u8 *pppoe = frame + pppoe_offset;
  u16 ppp_protocol = ((u16) pppoe[6] << 8) | pppoe[7];
  if (pppoe[0] != 0x11 || pppoe[1] != 0 || ppp_protocol != 0x0057)
    return 0;

  u8 *ip6 = pppoe + 8;
  if ((ip6[0] >> 4) != 6 || ip6[6] != 17)
    return 0;
  u8 *udp = ip6 + 40;
  u16 source = ((u16) udp[0] << 8) | udp[1];
  u16 destination = ((u16) udp[2] << 8) | udp[3];
  if (client_to_server)
    return source == 546 && destination == 547;
  return source == 547 && destination == 546;
}

static_always_inline ly_pppoe_client_binding_t *
ly_pppoe_binding_for_interface (ly_pppoe_client_main_t *main,
                                u32 sw_if_index, int *from_control)
{
  ly_pppoe_client_binding_t *binding;
  vec_foreach (binding, main->bindings)
    {
      if (binding->control_sw_if_index == sw_if_index)
        {
          *from_control = 1;
          return binding;
        }
      if (binding->wan_sw_if_index == sw_if_index)
        {
          *from_control = 0;
          return binding;
        }
    }
  return 0;
}

static_always_inline int
ly_pppoe_interface_is_valid (ly_pppoe_client_main_t *main, u32 sw_if_index)
{
  return sw_if_index != ~0 &&
    !pool_is_free_index (main->vnet_main->interface_main.sw_interfaces,
                         sw_if_index);
}

static_always_inline void
ly_pppoe_forward_to_interface (vlib_buffer_t *buffer, u32 sw_if_index)
{
  vnet_buffer (buffer)->sw_if_index[VLIB_TX] = sw_if_index;
}

VLIB_NODE_FN (ly_pppoe_client_node) (vlib_main_t *vm,
                                     vlib_node_runtime_t *node,
                                     vlib_frame_t *frame)
{
  ly_pppoe_client_main_t *main = &ly_pppoe_client_main;
  u32 *from = vlib_frame_vector_args (frame);
  u16 nexts[VLIB_FRAME_SIZE];

  for (u32 index = 0; index < frame->n_vectors; index++)
    {
      vlib_buffer_t *buffer = vlib_get_buffer (vm, from[index]);
      u32 rx_sw_if_index = vnet_buffer (buffer)->sw_if_index[VLIB_RX];
      int from_control = 0;
      ly_pppoe_client_binding_t *binding =
        ly_pppoe_binding_for_interface (main, rx_sw_if_index, &from_control);

      vnet_feature_next_u16 (&nexts[index], buffer);
      if (!binding)
        {
          vlib_node_increment_counter (vm, node->node_index,
                                       LY_PPPOE_CLIENT_ERROR_PASSED, 1);
          continue;
        }

      ly_pppoe_packet_t packet = { 0 };
      if (!ly_pppoe_parse (buffer, &packet))
        {
          vlib_node_increment_counter (vm, node->node_index,
                                       LY_PPPOE_CLIENT_ERROR_PASSED, 1);
          continue;
        }

      int dhcp6 = !packet.discovery && packet.ppp_protocol == 0x0057 &&
                  ly_pppoe_is_dhcp6 (buffer, packet.pppoe_offset,
                                     from_control);
      int control =
        !packet.discovery && ly_pppoe_is_control_protocol (packet.ppp_protocol);
      if (!from_control && !packet.discovery && !control && !dhcp6)
        {
          vlib_node_increment_counter (vm, node->node_index,
                                       LY_PPPOE_CLIENT_ERROR_PASSED, 1);
          continue;
        }

      if (from_control)
        {
          if (!ly_pppoe_interface_is_valid (main, binding->wan_sw_if_index))
            {
              vlib_node_increment_counter (vm, node->node_index,
                                           LY_PPPOE_CLIENT_ERROR_PASSED, 1);
              continue;
            }
          vnet_sw_interface_t *wan = vnet_get_sw_interface (
            main->vnet_main, binding->wan_sw_if_index);
          if (wan->type == VNET_SW_INTERFACE_TYPE_SUB)
            {
              if (!ly_pppoe_interface_is_valid (main, wan->sup_sw_if_index))
                {
                  vlib_node_increment_counter (
                    vm, node->node_index, LY_PPPOE_CLIENT_ERROR_PASSED, 1);
                  continue;
                }
              wan = vnet_get_sw_interface (main->vnet_main,
                                           wan->sup_sw_if_index);
            }
          if (wan->hw_if_index == ~0 ||
              pool_is_free_index (main->vnet_main->interface_main.hw_interfaces,
                                  wan->hw_if_index))
            {
              vlib_node_increment_counter (vm, node->node_index,
                                           LY_PPPOE_CLIENT_ERROR_PASSED, 1);
              continue;
            }
          vnet_hw_interface_t *hardware =
            vnet_get_hw_interface (main->vnet_main, wan->hw_if_index);
          ethernet_header_t *ethernet = vlib_buffer_get_current (buffer);
          clib_memcpy_fast (ethernet->src_address, hardware->hw_address, 6);
          ly_pppoe_forward_to_interface (buffer, binding->wan_sw_if_index);
        }
      else
        {
          ly_pppoe_forward_to_interface (buffer,
                                         binding->control_sw_if_index);
        }

      nexts[index] = LY_PPPOE_CLIENT_NEXT_INTERFACE_OUTPUT;
      if (packet.discovery)
        {
          binding->discovery_packets++;
          vlib_node_increment_counter (
            vm, node->node_index,
            LY_PPPOE_CLIENT_ERROR_DISCOVERY_FORWARDED, 1);
        }
      else if (dhcp6)
        {
          binding->dhcp6_packets++;
          vlib_node_increment_counter (
            vm, node->node_index, LY_PPPOE_CLIENT_ERROR_DHCP6_FORWARDED, 1);
        }
      else
        {
          binding->control_packets++;
          vlib_node_increment_counter (
            vm, node->node_index, LY_PPPOE_CLIENT_ERROR_CONTROL_FORWARDED, 1);
        }
    }

  vlib_buffer_enqueue_to_next (vm, node, from, nexts, frame->n_vectors);
  return frame->n_vectors;
}

VLIB_REGISTER_NODE (ly_pppoe_client_node) = {
  .name = "ly-route-pppoe-client",
  .vector_size = sizeof (u32),
  .n_errors = LY_PPPOE_CLIENT_N_ERROR,
  .error_strings = ly_pppoe_client_error_strings,
  .n_next_nodes = LY_PPPOE_CLIENT_N_NEXT,
  .next_nodes = {
    [LY_PPPOE_CLIENT_NEXT_INTERFACE_OUTPUT] = "interface-output",
  },
};

VNET_FEATURE_INIT (ly_pppoe_client_feature, static) = {
  .arc_name = "device-input",
  .node_name = "ly-route-pppoe-client",
  .runs_before = VNET_FEATURES ("pppoe-input"),
};

static clib_error_t *
ly_pppoe_client_set_command_fn (vlib_main_t *vm, unformat_input_t *input,
                                vlib_cli_command_t *cmd)
{
  ly_pppoe_client_main_t *main = &ly_pppoe_client_main;
  u32 control_sw_if_index = ~0, wan_sw_if_index = ~0;
  u8 disable = 0;

  while (unformat_check_input (input) != UNFORMAT_END_OF_INPUT)
    {
      if (unformat (input, "control-interface %U", unformat_vnet_sw_interface,
                    main->vnet_main, &control_sw_if_index))
        ;
      else if (unformat (input, "wan-interface %U", unformat_vnet_sw_interface,
                         main->vnet_main, &wan_sw_if_index))
        ;
      else if (unformat (input, "disable"))
        disable = 1;
      else
        return clib_error_return (0, "unknown input `%U'",
                                  format_unformat_error, input);
    }

  if (disable)
    {
      if ((control_sw_if_index == ~0) != (wan_sw_if_index == ~0))
        return clib_error_return (
          0, "control-interface and wan-interface must be specified together");
      vlib_worker_thread_barrier_sync (vm);
      if (control_sw_if_index == ~0)
        {
          ly_pppoe_client_binding_t *binding;
          vec_foreach (binding, main->bindings)
            {
              vnet_feature_enable_disable (
                "device-input", "ly-route-pppoe-client",
                binding->control_sw_if_index, 0, 0, 0);
              vnet_feature_enable_disable (
                "device-input", "ly-route-pppoe-client",
                binding->wan_sw_if_index, 0, 0, 0);
            }
          vec_free (main->bindings);
        }
      else
        {
          u32 index;
          vec_foreach_index (index, main->bindings)
            if (main->bindings[index].control_sw_if_index ==
                  control_sw_if_index &&
                main->bindings[index].wan_sw_if_index == wan_sw_if_index)
              {
                vnet_feature_enable_disable (
                  "device-input", "ly-route-pppoe-client",
                  control_sw_if_index, 0, 0, 0);
                vnet_feature_enable_disable (
                  "device-input", "ly-route-pppoe-client", wan_sw_if_index, 0,
                  0, 0);
                vec_del1 (main->bindings, index);
                break;
              }
        }
      vlib_worker_thread_barrier_release (vm);
      return 0;
    }
  if (control_sw_if_index == ~0 || wan_sw_if_index == ~0)
    return clib_error_return (
      0, "control-interface and wan-interface are required");
  if (control_sw_if_index == wan_sw_if_index)
    return clib_error_return (0, "control and WAN interfaces must differ");

  u32 index;
  vec_foreach_index (index, main->bindings)
    if (main->bindings[index].control_sw_if_index == control_sw_if_index &&
        main->bindings[index].wan_sw_if_index == wan_sw_if_index)
      return 0;

  vlib_worker_thread_barrier_sync (vm);
  for (index = vec_len (main->bindings); index > 0; index--)
    {
      u32 candidate = index - 1;
      if (main->bindings[candidate].control_sw_if_index !=
            control_sw_if_index &&
          main->bindings[candidate].wan_sw_if_index != wan_sw_if_index)
        continue;
      vnet_feature_enable_disable (
        "device-input", "ly-route-pppoe-client",
        main->bindings[candidate].control_sw_if_index, 0, 0, 0);
      vnet_feature_enable_disable (
        "device-input", "ly-route-pppoe-client",
        main->bindings[candidate].wan_sw_if_index, 0, 0, 0);
      vec_del1 (main->bindings, candidate);
    }
  if (vnet_feature_enable_disable ("device-input", "ly-route-pppoe-client",
                                   control_sw_if_index, 1, 0, 0) != 0)
    {
      vlib_worker_thread_barrier_release (vm);
      return clib_error_return (0,
                                "failed to enable PPPoE control forwarding");
    }
  if (vnet_feature_enable_disable ("device-input", "ly-route-pppoe-client",
                                   wan_sw_if_index, 1, 0, 0) != 0)
    {
      vnet_feature_enable_disable ("device-input", "ly-route-pppoe-client",
                                   control_sw_if_index, 0, 0, 0);
      vlib_worker_thread_barrier_release (vm);
      return clib_error_return (0,
                                "failed to enable PPPoE WAN forwarding");
    }
  ly_pppoe_client_binding_t *binding;
  vec_add2 (main->bindings, binding, 1);
  clib_memset (binding, 0, sizeof (*binding));
  binding->control_sw_if_index = control_sw_if_index;
  binding->wan_sw_if_index = wan_sw_if_index;
  vlib_worker_thread_barrier_release (vm);
  return 0;
}

LY_ROUTE_VLIB_CLI_COMMAND (ly_pppoe_client_set_command, static) = {
  .path = "set ly-route pppoe-client",
  .short_help = "set ly-route pppoe-client control-interface <interface> "
                "wan-interface <interface> [disable] | disable",
  .function = ly_pppoe_client_set_command_fn,
};

static clib_error_t *
ly_pppoe_client_show_command_fn (vlib_main_t *vm, unformat_input_t *input,
                                 vlib_cli_command_t *cmd)
{
  ly_pppoe_client_main_t *main = &ly_pppoe_client_main;
  if (vec_len (main->bindings) == 0)
    vlib_cli_output (vm, "state disabled");
  else
    {
      vlib_cli_output (vm, "state enabled\nbindings %u",
                       vec_len (main->bindings));
      vlib_cli_output (vm, "stock-pppoe-link-mode explicit-encap-interface");
      u32 index;
      vec_foreach_index (index, main->bindings)
        {
          ly_pppoe_client_binding_t *binding = &main->bindings[index];
          vlib_cli_output (
            vm,
            "[%u] control-interface %U wan-interface %U "
            "discovery-forwarded %llu control-forwarded %llu "
            "dhcp6-forwarded %llu",
            index, format_vnet_sw_if_index_name, main->vnet_main,
            binding->control_sw_if_index, format_vnet_sw_if_index_name,
            main->vnet_main, binding->wan_sw_if_index,
            binding->discovery_packets, binding->control_packets,
            binding->dhcp6_packets);
        }
    }
  return 0;
}

LY_ROUTE_VLIB_CLI_COMMAND (ly_pppoe_client_show_command, static) = {
  .path = "show ly-route pppoe-client",
  .short_help = "show ly-route pppoe-client",
  .function = ly_pppoe_client_show_command_fn,
};

static clib_error_t *
ly_pppoe_client_init (vlib_main_t *vm)
{
  ly_pppoe_client_main_t *main = &ly_pppoe_client_main;
  main->vlib_main = vm;
  main->vnet_main = vnet_get_main ();
  main->bindings = 0;
  return 0;
}

VLIB_INIT_FUNCTION (ly_pppoe_client_init);

VLIB_PLUGIN_REGISTER () = {
  .version = VPP_BUILD_VER,
  .description = "Ly Route native VPP multi-WAN PPPoE control binding",
};
