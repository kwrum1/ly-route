#include <vnet/plugin/plugin.h>
#include <vpp/app/version.h>
#include <ly_route_smart_qos/smart_qos.h>

ly_sq_main_t ly_sq_main;

VLIB_PLUGIN_REGISTER () = {
  .version = VPP_BUILD_VER,
  .description = "Ly Route FQ-CoDel smart QoS",
};

static void
ly_sq_scheduler_free (vlib_main_t *vm, ly_sq_scheduler_t *scheduler)
{
  u32 index;
  if (scheduler->entries)
    {
      for (index = 0; index < scheduler->slots; index++)
        if (scheduler->entries[index].buffer_index != LY_SQ_INVALID_INDEX)
          vlib_buffer_free_one (vm, scheduler->entries[index].buffer_index);
      clib_mem_free (scheduler->entries);
    }
  if (scheduler->flows)
    clib_mem_free (scheduler->flows);
  if (scheduler->hosts)
    clib_mem_free (scheduler->hosts);
  clib_memset (scheduler, 0, sizeof (*scheduler));
}

static void
ly_sq_scheduler_init (ly_sq_scheduler_t *scheduler, u32 sw_if_index,
                      u32 output_next_index, u64 rate_bps,
                      ly_sq_host_isolation_t host_isolation)
{
  u32 index;
  clib_memset (scheduler, 0, sizeof (*scheduler));
  scheduler->sw_if_index = sw_if_index;
  scheduler->output_next_index = output_next_index;
  scheduler->slots = LY_SQ_DEFAULT_SLOTS;
  scheduler->rate_bps = rate_bps;
  scheduler->host_isolation = host_isolation;
  scheduler->free_head = 0;
  scheduler->active_head = LY_SQ_INVALID_INDEX;
  scheduler->active_tail = LY_SQ_INVALID_INDEX;
  scheduler->fattest_flow = LY_SQ_INVALID_INDEX;
  scheduler->entries = clib_mem_alloc_aligned (
    sizeof (*scheduler->entries) * scheduler->slots, CLIB_CACHE_LINE_BYTES);
  scheduler->flows = clib_mem_alloc_aligned (
    sizeof (*scheduler->flows) * LY_SQ_FLOW_COUNT, CLIB_CACHE_LINE_BYTES);
  scheduler->hosts = clib_mem_alloc_aligned (
    sizeof (*scheduler->hosts) * LY_SQ_HOST_COUNT, CLIB_CACHE_LINE_BYTES);
  clib_memset (scheduler->entries, 0,
               sizeof (*scheduler->entries) * scheduler->slots);
  clib_memset (scheduler->flows, 0,
               sizeof (*scheduler->flows) * LY_SQ_FLOW_COUNT);
  clib_memset (scheduler->hosts, 0,
               sizeof (*scheduler->hosts) * LY_SQ_HOST_COUNT);
  for (index = 0; index < scheduler->slots; index++)
    {
      scheduler->entries[index].buffer_index = LY_SQ_INVALID_INDEX;
      scheduler->entries[index].next =
        index + 1 < scheduler->slots ? index + 1 : LY_SQ_INVALID_INDEX;
    }
  for (index = 0; index < LY_SQ_FLOW_COUNT; index++)
    {
      scheduler->flows[index].head = LY_SQ_INVALID_INDEX;
      scheduler->flows[index].tail = LY_SQ_INVALID_INDEX;
      scheduler->flows[index].next_active = LY_SQ_INVALID_INDEX;
      scheduler->flows[index].host_index = LY_SQ_INVALID_INDEX;
    }
  for (index = 0; index < LY_SQ_HOST_COUNT; index++)
    {
      scheduler->hosts[index].flow_head = LY_SQ_INVALID_INDEX;
      scheduler->hosts[index].flow_tail = LY_SQ_INVALID_INDEX;
      scheduler->hosts[index].next_active = LY_SQ_INVALID_INDEX;
    }
  scheduler->enabled = 1;
}

int
ly_sq_enable_disable (u32 sw_if_index, u64 rate_kbps,
                      ly_sq_host_isolation_t host_isolation, int enable)
{
  ly_sq_main_t *sqm = &ly_sq_main;
  vnet_hw_interface_t *hw;
  vnet_sw_interface_t *sw;
  u32 thread_index;
  u32 output_next_index;
  u32 thread_count = vlib_get_n_threads ();
  u8 any_enabled = 0;
  u8 previously_enabled = 0;

  if (pool_is_free_index (sqm->vnet_main->interface_main.sw_interfaces,
                          sw_if_index))
    return VNET_API_ERROR_INVALID_SW_IF_INDEX;
  sw = vnet_get_sw_interface (sqm->vnet_main, sw_if_index);
  if (sw->type != VNET_SW_INTERFACE_TYPE_HARDWARE)
    return VNET_API_ERROR_INVALID_SW_IF_INDEX;
  if (enable && (rate_kbps < 64 || rate_kbps > 400000000))
    return VNET_API_ERROR_INVALID_VALUE;

  hw = vnet_get_hw_interface (sqm->vnet_main, sw_if_index);
  output_next_index = vlib_node_add_next (
    sqm->vlib_main, ly_sq_scheduler_node.index, hw->output_node_index);

  for (thread_index = 0;
       thread_index < vec_len (sqm->enabled_by_sw_if_index); thread_index++)
    previously_enabled |= sqm->enabled_by_sw_if_index[thread_index];
  if (!previously_enabled)
    sqm->scheduler_thread_index = thread_count > 1 ? 1 : 0;

  vlib_worker_thread_barrier_sync (sqm->vlib_main);
  vec_validate_init_empty (sqm->enabled_by_sw_if_index, sw_if_index, 0);
  vec_validate_init_empty (sqm->rate_by_sw_if_index, sw_if_index, 0);
  vec_validate (sqm->schedulers_by_thread, thread_count - 1);
  for (thread_index = 0; thread_index < thread_count; thread_index++)
    {
      vlib_main_t *thread_vm = vlib_get_main_by_index (thread_index);
      vec_validate (sqm->schedulers_by_thread[thread_index], sw_if_index);
      ly_sq_scheduler_t *scheduler =
        &sqm->schedulers_by_thread[thread_index][sw_if_index];
      if (scheduler->enabled || scheduler->entries)
        ly_sq_scheduler_free (thread_vm, scheduler);
      if (enable && thread_index == sqm->scheduler_thread_index)
        ly_sq_scheduler_init (scheduler, sw_if_index, output_next_index,
                              rate_kbps * 1000, host_isolation);
    }
  sqm->enabled_by_sw_if_index[sw_if_index] = enable != 0;
  sqm->rate_by_sw_if_index[sw_if_index] = enable ? rate_kbps * 1000 : 0;
  for (thread_index = 0;
       thread_index < vec_len (sqm->enabled_by_sw_if_index); thread_index++)
    any_enabled |= sqm->enabled_by_sw_if_index[thread_index];
  for (thread_index = 0; thread_index < thread_count; thread_index++)
    vlib_node_set_state (
      vlib_get_main_by_index (thread_index), ly_sq_scheduler_node.index,
      any_enabled && thread_index == sqm->scheduler_thread_index ?
        VLIB_NODE_STATE_POLLING :
        VLIB_NODE_STATE_DISABLED);
  vlib_worker_thread_barrier_release (sqm->vlib_main);

  return vnet_feature_enable_disable (
    "interface-output", "ly-route-smart-qos-output", sw_if_index, enable, 0,
    0);
}

static clib_error_t *
ly_sq_set_command_fn (vlib_main_t *vm, unformat_input_t *input,
                      vlib_cli_command_t *cmd)
{
  u32 sw_if_index = LY_SQ_INVALID_INDEX;
  u64 rate_kbps = 0;
  int enable = 1;
  ly_sq_host_isolation_t host_isolation = LY_SQ_HOST_SOURCE;
  int rv;

  while (unformat_check_input (input) != UNFORMAT_END_OF_INPUT)
    {
      if (unformat (input, "interface %U", unformat_vnet_sw_interface,
                    ly_sq_main.vnet_main, &sw_if_index))
        ;
      else if (unformat (input, "rate %llu", &rate_kbps))
        ;
      else if (unformat (input, "host-isolation source"))
        host_isolation = LY_SQ_HOST_SOURCE;
      else if (unformat (input, "host-isolation destination"))
        host_isolation = LY_SQ_HOST_DESTINATION;
      else if (unformat (input, "disable"))
        enable = 0;
      else
        return clib_error_return (0, "unknown input '%U'",
                                  format_unformat_error, input);
    }
  if (sw_if_index == LY_SQ_INVALID_INDEX)
    return clib_error_return (0, "interface is required");
  if (enable && rate_kbps == 0)
    return clib_error_return (0, "rate is required");
  rv = ly_sq_enable_disable (sw_if_index, rate_kbps, host_isolation, enable);
  if (rv)
    return clib_error_return (0, "smart QoS configuration failed: %d", rv);
  return 0;
}

VLIB_CLI_COMMAND (ly_sq_set_command, static) = {
  .path = "set ly-route smart-qos",
  .short_help =
    "set ly-route smart-qos interface <interface> rate <kbps> "
    "[host-isolation source|destination] [disable]",
  .function = ly_sq_set_command_fn,
};

static clib_error_t *
ly_sq_show_command_fn (vlib_main_t *vm, unformat_input_t *input,
                       vlib_cli_command_t *cmd)
{
  ly_sq_main_t *sqm = &ly_sq_main;
  u32 sw_if_index;
  u8 running = 0;

  for (sw_if_index = 0; sw_if_index < vec_len (sqm->enabled_by_sw_if_index);
       sw_if_index++)
    running |= sqm->enabled_by_sw_if_index[sw_if_index];
  vlib_cli_output (vm, "state %s", running ? "running" : "locked");
  vlib_cli_output (vm, "algorithm fq-codel");
  vlib_cli_output (vm, "qualification production");
  vlib_cli_output (vm, "scheduler-thread %u", sqm->scheduler_thread_index);
  for (sw_if_index = 0; sw_if_index < vec_len (sqm->enabled_by_sw_if_index);
       sw_if_index++)
    if (sqm->enabled_by_sw_if_index[sw_if_index])
      {
        u64 enqueued = 0, transmitted = 0, aqm_drops = 0,
            overflow_drops = 0;
        u32 queued_packets = 0, thread_index;
        for (thread_index = 0;
             thread_index < vec_len (sqm->schedulers_by_thread);
             thread_index++)
          {
            ly_sq_scheduler_t *scheduler =
              &sqm->schedulers_by_thread[thread_index][sw_if_index];
            enqueued += scheduler->enqueued;
            transmitted += scheduler->transmitted;
            aqm_drops += scheduler->aqm_drops;
            overflow_drops += scheduler->overflow_drops;
            queued_packets += scheduler->queued_packets;
          }
        vlib_cli_output (
          vm,
          "interface %U enabled rate-kbps %llu host-isolation %s queued %u enqueued %llu "
          "transmitted %llu aqm-drops %llu overflow-drops %llu",
          format_vnet_sw_if_index_name, sqm->vnet_main, sw_if_index,
          sqm->rate_by_sw_if_index[sw_if_index] / 1000,
          sqm->schedulers_by_thread[sqm->scheduler_thread_index][sw_if_index]
              .host_isolation == LY_SQ_HOST_SOURCE ?
            "source" :
            "destination",
          queued_packets,
          enqueued, transmitted, aqm_drops, overflow_drops);
      }
  return 0;
}

VLIB_CLI_COMMAND (ly_sq_show_command, static) = {
  .path = "show ly-route smart-qos",
  .short_help = "show ly-route smart-qos",
  .function = ly_sq_show_command_fn,
};

static clib_error_t *
ly_sq_init (vlib_main_t *vm)
{
  ly_sq_main.vlib_main = vm;
  ly_sq_main.vnet_main = vnet_get_main ();
  ly_sq_main.arc_index =
    ly_sq_main.vnet_main->interface_main.output_feature_arc_index;
  ly_sq_main.scheduler_thread_index = 0;
  ly_sq_main.frame_queue_index =
    vlib_frame_queue_main_init (ly_sq_enqueue_node.index, 0);
  return 0;
}

VLIB_INIT_FUNCTION (ly_sq_init);

VNET_FEATURE_INIT (ly_sq_output_feature, static) = {
  .arc_name = "interface-output",
  .node_name = "ly-route-smart-qos-output",
  .runs_before = VNET_FEATURES ("interface-output-arc-end"),
};
