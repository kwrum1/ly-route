#include <vlib/vlib.h>
#include <vnet/ethernet/ethernet.h>
#include <vnet/feature/feature.h>
#include <vnet/interface.h>
#include <vnet/plugin/plugin.h>
#include <vpp/app/version.h>

typedef struct
{
  vlib_main_t *vlib_main;
  vnet_main_t *vnet_main;
  u32 control_sw_if_index;
  u32 wan_sw_if_index;
  u64 discovery_packets;
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
  LY_PPPOE_CLIENT_N_ERROR,
} ly_pppoe_client_error_t;

static char *ly_pppoe_client_error_strings[] = {
  "packets passed",
  "PPPoE discovery broadcasts forwarded",
};

static ly_pppoe_client_main_t ly_pppoe_client_main;

static_always_inline int
ly_pppoe_is_broadcast (const u8 *address)
{
  return address[0] == 0xff && address[1] == 0xff &&
         address[2] == 0xff && address[3] == 0xff &&
         address[4] == 0xff && address[5] == 0xff;
}

static_always_inline int
ly_pppoe_is_client_discovery (vlib_buffer_t *buffer)
{
  ethernet_header_t *ethernet = vlib_buffer_get_current (buffer);
  u16 type = clib_net_to_host_u16 (ethernet->type);
  u8 *pppoe = (u8 *) (ethernet + 1);

  if (type == ETHERNET_TYPE_PPPOE_DISCOVERY)
    return ly_pppoe_is_broadcast (ethernet->dst_address) || pppoe[1] == 0xa7;
  if (type == ETHERNET_TYPE_VLAN)
    {
      ethernet_vlan_header_t *vlan = (ethernet_vlan_header_t *) (ethernet + 1);
      pppoe = (u8 *) (vlan + 1);
      return clib_net_to_host_u16 (vlan->type) ==
               ETHERNET_TYPE_PPPOE_DISCOVERY &&
             (ly_pppoe_is_broadcast (ethernet->dst_address) ||
              pppoe[1] == 0xa7);
    }
  return 0;
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

      vnet_feature_next_u16 (&nexts[index], buffer);
      if (rx_sw_if_index != main->control_sw_if_index ||
          main->wan_sw_if_index == ~0 ||
          !ly_pppoe_is_client_discovery (buffer))
        {
          vlib_node_increment_counter (vm, node->node_index,
                                       LY_PPPOE_CLIENT_ERROR_PASSED, 1);
          continue;
        }

      vnet_sw_interface_t *wan =
        vnet_get_sw_interface (main->vnet_main, main->wan_sw_if_index);
      if (wan->type == VNET_SW_INTERFACE_TYPE_SUB)
        wan = vnet_get_sw_interface (main->vnet_main, wan->sup_sw_if_index);
      vnet_hw_interface_t *hardware =
        vnet_get_hw_interface (main->vnet_main, wan->hw_if_index);
      ethernet_header_t *ethernet = vlib_buffer_get_current (buffer);
      clib_memcpy_fast (ethernet->src_address, hardware->hw_address, 6);

      vnet_buffer (buffer)->sw_if_index[VLIB_TX] = main->wan_sw_if_index;
      nexts[index] = LY_PPPOE_CLIENT_NEXT_INTERFACE_OUTPUT;
      main->discovery_packets++;
      vlib_node_increment_counter (
        vm, node->node_index, LY_PPPOE_CLIENT_ERROR_DISCOVERY_FORWARDED, 1);
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
      if (main->control_sw_if_index != ~0)
        vnet_feature_enable_disable ("device-input", "ly-route-pppoe-client",
                                     main->control_sw_if_index, 0, 0, 0);
      main->control_sw_if_index = ~0;
      main->wan_sw_if_index = ~0;
      return 0;
    }
  if (control_sw_if_index == ~0 || wan_sw_if_index == ~0)
    return clib_error_return (
      0, "control-interface and wan-interface are required");
  if (control_sw_if_index == wan_sw_if_index)
    return clib_error_return (0, "control and WAN interfaces must differ");

  if (main->control_sw_if_index == control_sw_if_index &&
      main->wan_sw_if_index == wan_sw_if_index)
    return 0;

  if (main->control_sw_if_index != ~0)
    vnet_feature_enable_disable ("device-input", "ly-route-pppoe-client",
                                 main->control_sw_if_index, 0, 0, 0);
  main->control_sw_if_index = control_sw_if_index;
  main->wan_sw_if_index = wan_sw_if_index;
  if (vnet_feature_enable_disable ("device-input", "ly-route-pppoe-client",
                                   control_sw_if_index, 1, 0, 0) != 0)
    return clib_error_return (0, "failed to enable PPPoE discovery forwarding");
  return 0;
}

VLIB_CLI_COMMAND (ly_pppoe_client_set_command, static) = {
  .path = "set ly-route pppoe-client",
  .short_help = "set ly-route pppoe-client control-interface <interface> "
                "wan-interface <interface> | disable",
  .function = ly_pppoe_client_set_command_fn,
};

static clib_error_t *
ly_pppoe_client_show_command_fn (vlib_main_t *vm, unformat_input_t *input,
                                 vlib_cli_command_t *cmd)
{
  ly_pppoe_client_main_t *main = &ly_pppoe_client_main;
  if (main->control_sw_if_index == ~0 || main->wan_sw_if_index == ~0)
    vlib_cli_output (vm, "state disabled");
  else
    vlib_cli_output (vm,
                     "state enabled\ncontrol-interface %U\nwan-interface %U\n"
                     "discovery-forwarded %llu",
                     format_vnet_sw_if_index_name, main->vnet_main,
                     main->control_sw_if_index, format_vnet_sw_if_index_name,
                     main->vnet_main, main->wan_sw_if_index,
                     main->discovery_packets);
  return 0;
}

VLIB_CLI_COMMAND (ly_pppoe_client_show_command, static) = {
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
  main->control_sw_if_index = ~0;
  main->wan_sw_if_index = ~0;
  return 0;
}

VLIB_INIT_FUNCTION (ly_pppoe_client_init);

VLIB_PLUGIN_REGISTER () = {
  .version = VPP_BUILD_VER,
  .description = "Ly Route native VPP PPPoE client discovery binding",
};
