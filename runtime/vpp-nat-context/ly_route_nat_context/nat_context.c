#include <vlib/vlib.h>
#include <vnet/feature/feature.h>
#include <vnet/interface.h>
#include <vnet/plugin/plugin.h>
#include <vpp/app/version.h>

typedef enum
{
  LY_NAT_CONTEXT_ERROR_REWRITTEN,
  LY_NAT_CONTEXT_ERROR_PASSED,
  LY_NAT_CONTEXT_N_ERROR,
} ly_nat_context_error_t;

static char *ly_nat_context_error_strings[] = {
  "packets assigned to an egress NAT context",
  "packets passed",
};

typedef struct
{
  u32 ingress_sw_if_index;
  u32 egress_sw_if_index;
  u32 context_sw_if_index;
} ly_nat_context_mapping_t;

typedef struct
{
  vnet_main_t *vnet_main;
  ly_nat_context_mapping_t *mappings;
} ly_nat_context_main_t;

static ly_nat_context_main_t ly_nat_context_main;

static ly_nat_context_mapping_t *
ly_nat_context_lookup (u32 ingress_sw_if_index, u32 egress_sw_if_index)
{
  ly_nat_context_mapping_t *mapping;
  vec_foreach (mapping, ly_nat_context_main.mappings)
    if (mapping->ingress_sw_if_index == ingress_sw_if_index &&
        mapping->egress_sw_if_index == egress_sw_if_index)
      return mapping;
  return 0;
}

VLIB_NODE_FN (ly_nat_context_ip4_node) (vlib_main_t *vm,
                                         vlib_node_runtime_t *node,
                                         vlib_frame_t *frame)
{
  u32 *from = vlib_frame_vector_args (frame);
  u16 nexts[VLIB_FRAME_SIZE];
  for (u32 index = 0; index < frame->n_vectors; index++)
    {
      vlib_buffer_t *buffer = vlib_get_buffer (vm, from[index]);
      u32 ingress = vnet_buffer (buffer)->sw_if_index[VLIB_RX];
      u32 egress = vnet_buffer (buffer)->sw_if_index[VLIB_TX];
      ly_nat_context_mapping_t *mapping =
        ly_nat_context_lookup (ingress, egress);
      if (mapping)
        {
          /* NAT44 EI keys a new session by the RX FIB.  Use a per-WAN
           * context interface only after FIB lookup selected this egress. */
          vnet_buffer (buffer)->sw_if_index[VLIB_RX] =
            mapping->context_sw_if_index;
          vlib_node_increment_counter (
            vm, node->node_index, LY_NAT_CONTEXT_ERROR_REWRITTEN, 1);
        }
      else
        vlib_node_increment_counter (
          vm, node->node_index, LY_NAT_CONTEXT_ERROR_PASSED, 1);
      vnet_feature_next_u16 (&nexts[index], buffer);
    }
  vlib_buffer_enqueue_to_next (vm, node, from, nexts, frame->n_vectors);
  return frame->n_vectors;
}

VLIB_REGISTER_NODE (ly_nat_context_ip4_node) = {
  .name = "ly-route-nat-context-ip4",
  .vector_size = sizeof (u32),
  .n_errors = LY_NAT_CONTEXT_N_ERROR,
  .error_strings = ly_nat_context_error_strings,
};

VNET_FEATURE_INIT (ly_nat_context_ip4_feature, static) = {
  .arc_name = "ip4-output",
  .node_name = "ly-route-nat-context-ip4",
  .runs_before = VNET_FEATURES ("nat44-ei-in2out-output"),
};

static int
ly_nat_context_has_egress (u32 egress_sw_if_index)
{
  ly_nat_context_mapping_t *mapping;
  vec_foreach (mapping, ly_nat_context_main.mappings)
    if (mapping->egress_sw_if_index == egress_sw_if_index)
      return 1;
  return 0;
}

static clib_error_t *
ly_nat_context_set_command_fn (vlib_main_t *vm, unformat_input_t *input,
                               vlib_cli_command_t *cmd)
{
  u32 ingress = ~0, egress = ~0, context = ~0;
  u8 is_add = 0, is_del = 0, is_clear = 0;

  while (unformat_check_input (input) != UNFORMAT_END_OF_INPUT)
    {
      if (unformat (input, "add"))
        is_add = 1;
      else if (unformat (input, "del"))
        is_del = 1;
      else if (unformat (input, "clear"))
        is_clear = 1;
      else if (unformat (input, "ingress %U", unformat_vnet_sw_interface,
                         ly_nat_context_main.vnet_main, &ingress))
        ;
      else if (unformat (input, "egress %U", unformat_vnet_sw_interface,
                         ly_nat_context_main.vnet_main, &egress))
        ;
      else if (unformat (input, "context %U", unformat_vnet_sw_interface,
                         ly_nat_context_main.vnet_main, &context))
        ;
      else
        return clib_error_return (0, "unknown input '%U'",
                                  format_unformat_error, input);
    }

  if ((is_add + is_del + is_clear) != 1)
    return clib_error_return (0, "choose exactly one of add, del, or clear");

  if (is_clear)
    {
      vlib_worker_thread_barrier_sync (vm);
      ly_nat_context_mapping_t *mapping;
      vec_foreach (mapping, ly_nat_context_main.mappings)
        vnet_feature_enable_disable ("ip4-output", "ly-route-nat-context-ip4",
                                     mapping->egress_sw_if_index, 0, 0, 0);
      vec_free (ly_nat_context_main.mappings);
      vlib_worker_thread_barrier_release (vm);
      return 0;
    }

  if (ingress == ~0 || egress == ~0 || (is_add && context == ~0))
    return clib_error_return (
      0, "usage: set ly-route nat-context add ingress <if> egress <if> context <if> | del ingress <if> egress <if> | clear");

  vlib_worker_thread_barrier_sync (vm);
  if (is_add)
    {
      if (ly_nat_context_lookup (ingress, egress))
        {
          vlib_worker_thread_barrier_release (vm);
          return clib_error_return (0, "NAT context mapping already exists");
        }
      ly_nat_context_mapping_t *mapping;
      vec_add2 (ly_nat_context_main.mappings, mapping, 1);
      mapping->ingress_sw_if_index = ingress;
      mapping->egress_sw_if_index = egress;
      mapping->context_sw_if_index = context;
      int rv = vnet_feature_enable_disable (
        "ip4-output", "ly-route-nat-context-ip4", egress, 1, 0, 0);
      vlib_worker_thread_barrier_release (vm);
      return rv ? clib_error_return (0, "NAT context feature attachment failed: %d", rv) : 0;
    }

  ly_nat_context_mapping_t *mapping = ly_nat_context_main.mappings;
  u32 count = vec_len (mapping);
  u32 write = 0;
  u8 found = 0;
  for (u32 read = 0; read < count; read++)
    {
      if (mapping[read].ingress_sw_if_index == ingress &&
          mapping[read].egress_sw_if_index == egress)
        {
          found = 1;
          continue;
        }
      mapping[write++] = mapping[read];
    }
  _vec_set_len (ly_nat_context_main.mappings, write, sizeof (mapping[0]));
  if (!ly_nat_context_has_egress (egress))
    vnet_feature_enable_disable ("ip4-output", "ly-route-nat-context-ip4",
                                 egress, 0, 0, 0);
  vlib_worker_thread_barrier_release (vm);
  return found ? 0 : clib_error_return (0, "NAT context mapping was not found");
}

VLIB_CLI_COMMAND (ly_nat_context_set_command, static) = {
  .path = "set ly-route nat-context",
  .short_help = "set ly-route nat-context add ingress <if> egress <if> context <if> | del ingress <if> egress <if> | clear",
  .function = ly_nat_context_set_command_fn,
};

static clib_error_t *
ly_nat_context_show_command_fn (vlib_main_t *vm, unformat_input_t *input,
                                vlib_cli_command_t *cmd)
{
  if (unformat_check_input (input) != UNFORMAT_END_OF_INPUT)
    return clib_error_return (0, "unknown input '%U'",
                              format_unformat_error, input);
  ly_nat_context_mapping_t *mapping;
  vec_foreach (mapping, ly_nat_context_main.mappings)
    vlib_cli_output (vm, "ingress %U egress %U context %U",
                     format_vnet_sw_if_index_name,
                     ly_nat_context_main.vnet_main,
                     mapping->ingress_sw_if_index,
                     format_vnet_sw_if_index_name,
                     ly_nat_context_main.vnet_main,
                     mapping->egress_sw_if_index,
                     format_vnet_sw_if_index_name,
                     ly_nat_context_main.vnet_main,
                     mapping->context_sw_if_index);
  (void) cmd;
  return 0;
}

VLIB_CLI_COMMAND (ly_nat_context_show_command, static) = {
  .path = "show ly-route nat-context",
  .short_help = "show ly-route nat-context",
  .function = ly_nat_context_show_command_fn,
};

static clib_error_t *
ly_nat_context_init (vlib_main_t *vm)
{
  ly_nat_context_main.vnet_main = vnet_get_main ();
  (void) vm;
  return 0;
}

VLIB_INIT_FUNCTION (ly_nat_context_init);

VLIB_PLUGIN_REGISTER () = {
  .version = VPP_BUILD_VER,
  .description = "Ly Route per-WAN NAT context selection",
};
