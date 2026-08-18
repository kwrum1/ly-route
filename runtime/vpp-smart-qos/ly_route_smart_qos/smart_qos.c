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
  vnet_sw_interface_t *sw;
  vlib_node_t *arc_end;
  u32 thread_index;
  u32 output_next_index;
  u32 thread_count = vlib_get_n_threads ();
  u8 any_enabled = 0;
  u8 previously_enabled = 0;
  u8 was_enabled;
  int feature_rv = 0;

  if (pool_is_free_index (sqm->vnet_main->interface_main.sw_interfaces,
                          sw_if_index))
    return VNET_API_ERROR_INVALID_SW_IF_INDEX;
  sw = vnet_get_sw_interface (sqm->vnet_main, sw_if_index);
  if (sw->type != VNET_SW_INTERFACE_TYPE_HARDWARE)
    return VNET_API_ERROR_INVALID_SW_IF_INDEX;
  if (enable && (rate_kbps < 64 || rate_kbps > 400000000))
    return VNET_API_ERROR_INVALID_VALUE;

  arc_end = vlib_get_node_by_name (sqm->vlib_main,
                                   (u8 *) "interface-output-arc-end");
  if (!arc_end)
    return VNET_API_ERROR_UNSUPPORTED;
  output_next_index = vlib_node_add_next (
    sqm->vlib_main, ly_sq_scheduler_node.index, arc_end->index);

  for (thread_index = 0;
       thread_index < vec_len (sqm->enabled_by_sw_if_index); thread_index++)
    previously_enabled |= sqm->enabled_by_sw_if_index[thread_index];
  if (!previously_enabled)
    sqm->scheduler_thread_index = thread_count > 1 ? 1 : 0;

  vlib_worker_thread_barrier_sync (sqm->vlib_main);
  vec_validate_init_empty (sqm->enabled_by_sw_if_index, sw_if_index, 0);
  vec_validate_init_empty (sqm->rate_by_sw_if_index, sw_if_index, 0);
  was_enabled = sqm->enabled_by_sw_if_index[sw_if_index];
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

  if (was_enabled && !enable)
    {
      ly_sq_feature_config_t feature_config = {
        .sw_if_index = sw_if_index,
        .magic = LY_SQ_FEATURE_CONFIG_MAGIC,
      };
    feature_rv = vnet_feature_enable_disable (
      "interface-output", "ly-route-smart-qos-output", sw_if_index, 0,
      &feature_config, sizeof (feature_config));
    }
  else if (!was_enabled && enable)
    {
      ly_sq_feature_config_t feature_config = {
        .sw_if_index = sw_if_index,
        .magic = LY_SQ_FEATURE_CONFIG_MAGIC,
      };
    feature_rv = vnet_feature_enable_disable (
      "interface-output", "ly-route-smart-qos-output", sw_if_index, 1,
      &feature_config, sizeof (feature_config));
    }
  return feature_rv;
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

int
ly_sq_rate_feature_needed (u32 sw_if_index, u8 direction)
{
  for (u32 index = 0; index < ly_sq_main.rate_rule_count; index++)
    if (ly_sq_main.rate_rules[index].enabled &&
        ly_sq_main.rate_rules[index].sw_if_index == sw_if_index &&
        ly_sq_main.rate_rules[index].direction == direction)
      return 1;
  return 0;
}

static u8
ly_sq_rate_protocol (u8 *value)
{
  if (!value || !strcmp ((char *) value, "any"))
    return 0;
  if (!strcmp ((char *) value, "tcp"))
    return IP_PROTOCOL_TCP;
  if (!strcmp ((char *) value, "udp"))
    return IP_PROTOCOL_UDP;
  if (!strcmp ((char *) value, "icmp"))
    return IP_PROTOCOL_ICMP;
  return 255;
}

static void
ly_sq_rate_bucket_id (char *destination, size_t destination_size,
                       const char *rule_id)
{
  char *separator;
  size_t length = strlen (rule_id);
  if (length >= destination_size)
    length = destination_size - 1;
  memcpy (destination, rule_id, length);
  destination[length] = 0;
  separator = strrchr (destination, '_');
  if (!separator || !separator[1])
    return;
  for (char *cursor = separator + 1; *cursor; cursor++)
    if (*cursor < '0' || *cursor > '9')
      return;
  *separator = 0;
}

static u32
ly_sq_rate_bucket_find (const char *id)
{
  for (u32 index = 0; index < LY_SQ_RATE_BUCKET_COUNT; index++)
    if (ly_sq_main.rate_buckets[index].enabled &&
        !strcmp (ly_sq_main.rate_buckets[index].id, id))
      return index;
  return LY_SQ_INVALID_INDEX;
}

static u32
ly_sq_rate_bucket_allocate (const char *id, u64 rate_kbps, u64 burst_bytes)
{
  u32 index = ly_sq_rate_bucket_find (id);
  if (index == LY_SQ_INVALID_INDEX)
    for (index = 0; index < LY_SQ_RATE_BUCKET_COUNT; index++)
      if (!ly_sq_main.rate_buckets[index].enabled)
        {
          ly_sq_rate_bucket_t *bucket = &ly_sq_main.rate_buckets[index];
          clib_memset (bucket, 0, sizeof (*bucket));
          clib_spinlock_init (&bucket->lock);
          memcpy (bucket->id, id, strlen (id) + 1);
          bucket->enabled = 1;
          break;
        }
  if (index == LY_SQ_RATE_BUCKET_COUNT)
    return LY_SQ_INVALID_INDEX;
  ly_sq_rate_bucket_t *bucket = &ly_sq_main.rate_buckets[index];
  bucket->rate_bytes_per_second = rate_kbps * 1000 / 8;
  bucket->burst_bytes = burst_bytes;
  bucket->tokens = burst_bytes;
  bucket->last_refill = 0;
  return index;
}

static void
ly_sq_rate_bucket_release_unused (void)
{
  for (u32 bucket_index = 0; bucket_index < LY_SQ_RATE_BUCKET_COUNT;
       bucket_index++)
    {
      ly_sq_rate_bucket_t *bucket = &ly_sq_main.rate_buckets[bucket_index];
      if (!bucket->enabled)
        continue;
      int used = 0;
      for (u32 rule_index = 0; rule_index < ly_sq_main.rate_rule_count;
           rule_index++)
        if (ly_sq_main.rate_rules[rule_index].enabled &&
            ly_sq_main.rate_rules[rule_index].bucket_index == bucket_index)
          {
            used = 1;
            break;
          }
      if (!used)
        bucket->enabled = 0;
    }
}

static clib_error_t *
ly_sq_rate_delete_rules (vlib_main_t *vm, const u8 *id)
{
  u32 removed_sw_if_indices[LY_SQ_RATE_RULE_COUNT];
  u8 removed_directions[LY_SQ_RATE_RULE_COUNT];
  u32 removed_count = 0;
  size_t id_length = strlen ((const char *) id);

  vlib_worker_thread_barrier_sync (vm);
  u32 write_index = 0;
  for (u32 index = 0; index < ly_sq_main.rate_rule_count; index++)
    {
      ly_sq_rate_rule_t *rule = &ly_sq_main.rate_rules[index];
      int matches = !strcmp (rule->id, (const char *) id) ||
                    (!strncmp (rule->id, (const char *) id, id_length) &&
                     rule->id[id_length] == '_');
      if (!matches)
        {
          if (write_index != index)
            ly_sq_main.rate_rules[write_index] = *rule;
          write_index++;
          continue;
        }
      removed_sw_if_indices[removed_count] = rule->sw_if_index;
      removed_directions[removed_count++] = rule->direction;
    }
  ly_sq_main.rate_rule_count = write_index;
  ly_sq_rate_bucket_release_unused ();
  vlib_worker_thread_barrier_release (vm);

  for (u32 index = 0; index < removed_count; index++)
      if (!ly_sq_rate_feature_needed (removed_sw_if_indices[index],
                                      removed_directions[index]))
        vnet_feature_enable_disable (
          removed_directions[index] == LY_SQ_RATE_DIRECTION_INPUT ?
            "ip4-unicast" : "interface-output",
        removed_directions[index] == LY_SQ_RATE_DIRECTION_INPUT ?
          "ly-route-flow-rate-input" : "ly-route-flow-rate-output",
        removed_sw_if_indices[index], 0, 0, 0);
  return 0;
}

static clib_error_t *
ly_sq_rate_set_command_fn (vlib_main_t *vm, unformat_input_t *input,
                           vlib_cli_command_t *cmd)
{
  u8 *id = 0, *protocol_name = 0;
  u32 sw_if_index = LY_SQ_INVALID_INDEX, source_len = 0,
      destination_len = 0;
  u32 source_first = 0, source_last = 65535;
  u32 destination_first = 0, destination_last = 65535;
  u8 direction = 0, protocol;
  u32 existing_index = LY_SQ_INVALID_INDEX;
  u32 old_sw_if_index = LY_SQ_INVALID_INDEX;
  u32 bucket_index = LY_SQ_INVALID_INDEX;
  u8 old_direction = 0;
  char bucket_id[LY_SQ_RATE_RULE_ID_SIZE];
  ip4_address_t source = { 0 }, destination = { 0 };
  u64 rate_kbps = 0, burst_bytes = 0;

  if (unformat (input, "delete rule %s", &id))
    {
      clib_error_t *error;
      if (!id || !id[0] ||
          unformat_check_input (input) != UNFORMAT_END_OF_INPUT)
        {
          vec_free (id);
          return clib_error_return (0, "flow-rate rule ID is required");
        }
      error = ly_sq_rate_delete_rules (vm, id);
      vec_free (id);
      return error;
    }
  while (unformat_check_input (input) != UNFORMAT_END_OF_INPUT)
    {
      if (unformat (input, "rule %s", &id))
        ;
      else if (unformat (input, "interface %U", unformat_vnet_sw_interface,
                         ly_sq_main.vnet_main, &sw_if_index))
        ;
      else if (unformat (input, "direction input"))
        direction = LY_SQ_RATE_DIRECTION_INPUT;
      else if (unformat (input, "direction output"))
        direction = LY_SQ_RATE_DIRECTION_OUTPUT;
      else if (unformat (input, "source %U/%u", unformat_ip4_address,
                         &source, &source_len))
        ;
      else if (unformat (input, "destination %U/%u", unformat_ip4_address,
                         &destination, &destination_len))
        ;
      else if (unformat (input, "protocol %s", &protocol_name))
        ;
      else if (unformat (input, "source-port %u-%u", &source_first,
                         &source_last))
        ;
      else if (unformat (input, "destination-port %u-%u", &destination_first,
                         &destination_last))
        ;
      else if (unformat (input, "rate-kbps %llu", &rate_kbps))
        ;
      else if (unformat (input, "burst-bytes %llu", &burst_bytes))
        ;
      else
        return clib_error_return (0, "unknown input '%U'",
                                  format_unformat_error, input);
    }
  protocol = ly_sq_rate_protocol (protocol_name);
  if (!id || !id[0] || sw_if_index == LY_SQ_INVALID_INDEX || !direction ||
      source_len > 32 || destination_len > 32 || protocol == 255 ||
      source_first > source_last || source_last > 65535 ||
      destination_first > destination_last || destination_last > 65535 ||
      strlen ((char *) id) >= LY_SQ_RATE_RULE_ID_SIZE ||
      rate_kbps == 0 || rate_kbps > 400000000 || burst_bytes == 0 ||
      burst_bytes > (1ULL << 32))
    {
      vec_free (id);
      vec_free (protocol_name);
      return clib_error_return (0, "invalid flow-rate rule");
    }
  for (u32 index = 0; index < ly_sq_main.rate_rule_count; index++)
    if (!strcmp (ly_sq_main.rate_rules[index].id, (char *) id))
      {
        existing_index = index;
        old_sw_if_index = ly_sq_main.rate_rules[index].sw_if_index;
        old_direction = ly_sq_main.rate_rules[index].direction;
        break;
      }
  if (existing_index == LY_SQ_INVALID_INDEX &&
      ly_sq_main.rate_rule_count == LY_SQ_RATE_RULE_COUNT)
    {
      vec_free (id);
      vec_free (protocol_name);
      return clib_error_return (0, "flow-rate rule capacity reached");
    }
  int feature_was_needed = ly_sq_rate_feature_needed (sw_if_index, direction);
  int rv = 0;
  if (!feature_was_needed)
    rv = vnet_feature_enable_disable (
      direction == LY_SQ_RATE_DIRECTION_INPUT ? "ip4-unicast" :
                                                "interface-output",
      direction == LY_SQ_RATE_DIRECTION_INPUT ? "ly-route-flow-rate-input" :
                                                "ly-route-flow-rate-output",
      sw_if_index, 1, 0, 0);
  if (rv)
    {
      vec_free (id);
      vec_free (protocol_name);
      return clib_error_return (0, "flow-rate feature attachment failed: %d", rv);
    }
  vlib_worker_thread_barrier_sync (vm);
  ly_sq_rate_bucket_id (bucket_id, sizeof (bucket_id), (char *) id);
  bucket_index = ly_sq_rate_bucket_allocate (bucket_id, rate_kbps,
                                              burst_bytes);
  if (bucket_index == LY_SQ_INVALID_INDEX)
    {
      vlib_worker_thread_barrier_release (vm);
      vec_free (id);
      vec_free (protocol_name);
      return clib_error_return (0, "flow-rate bucket capacity reached");
    }
  if (existing_index == LY_SQ_INVALID_INDEX)
    existing_index = ly_sq_main.rate_rule_count++;
  ly_sq_rate_rule_t *rule = &ly_sq_main.rate_rules[existing_index];
  clib_memset (rule, 0, sizeof (*rule));
  memcpy (rule->id, id, strlen ((char *) id) + 1);
  rule->sw_if_index = sw_if_index;
  rule->bucket_index = bucket_index;
  rule->source = source;
  rule->destination = destination;
  rule->source_prefix_len = source_len;
  rule->destination_prefix_len = destination_len;
  rule->protocol = protocol;
  rule->direction = direction;
  rule->source_port_first = source_first;
  rule->source_port_last = source_last;
  rule->destination_port_first = destination_first;
  rule->destination_port_last = destination_last;
  rule->enabled = 1;
  vlib_worker_thread_barrier_release (vm);
  if (old_sw_if_index != LY_SQ_INVALID_INDEX &&
      (old_sw_if_index != sw_if_index || old_direction != direction) &&
      !ly_sq_rate_feature_needed (old_sw_if_index, old_direction))
    vnet_feature_enable_disable (
      old_direction == LY_SQ_RATE_DIRECTION_INPUT ? "ip4-unicast" :
                                                    "interface-output",
      old_direction == LY_SQ_RATE_DIRECTION_INPUT ?
        "ly-route-flow-rate-input" : "ly-route-flow-rate-output",
      old_sw_if_index, 0, 0, 0);
  vec_free (id);
  vec_free (protocol_name);
  (void) cmd;
  return 0;
}

VLIB_CLI_COMMAND (ly_sq_rate_set_command, static) = {
  .path = "set ly-route flow-rate",
  .short_help = "set ly-route flow-rate delete rule <id> | rule <id> interface <interface> "
                "direction input|output source A.B.C.D/N destination A.B.C.D/N "
                "protocol any|tcp|udp|icmp source-port <first>-<last> "
                "destination-port <first>-<last> rate-kbps <rate> burst-bytes <burst>",
  .function = ly_sq_rate_set_command_fn,
};

static clib_error_t *
ly_sq_rate_show_command_fn (vlib_main_t *vm, unformat_input_t *input,
                            vlib_cli_command_t *cmd)
{
  for (u32 index = 0; index < ly_sq_main.rate_rule_count; index++)
    {
      ly_sq_rate_rule_t *rule = &ly_sq_main.rate_rules[index];
      if (rule->bucket_index >= LY_SQ_RATE_BUCKET_COUNT ||
          !ly_sq_main.rate_buckets[rule->bucket_index].enabled)
        continue;
      ly_sq_rate_bucket_t *bucket =
        &ly_sq_main.rate_buckets[rule->bucket_index];
      clib_spinlock_lock (&bucket->lock);
      vlib_cli_output (
        vm, "rule %s interface %U direction %s source %U/%u destination %U/%u "
            "protocol %u source-port %u-%u destination-port %u-%u rate-kbps %llu burst-bytes %llu "
            "matched-packets %llu matched-bytes %llu conform-packets %llu "
            "dropped-packets %llu",
        rule->id, format_vnet_sw_if_index_name, ly_sq_main.vnet_main,
        rule->sw_if_index,
        rule->direction == LY_SQ_RATE_DIRECTION_INPUT ? "input" : "output",
        format_ip4_address, &rule->source, rule->source_prefix_len,
        format_ip4_address, &rule->destination, rule->destination_prefix_len,
        rule->protocol, rule->source_port_first, rule->source_port_last,
        rule->destination_port_first, rule->destination_port_last,
        (bucket->rate_bytes_per_second * 8) / 1000, bucket->burst_bytes,
        rule->matched_packets, rule->matched_bytes,
        rule->conform_packets, rule->dropped_packets);
      clib_spinlock_unlock (&bucket->lock);
    }
  (void) input;
  (void) cmd;
  return 0;
}

VLIB_CLI_COMMAND (ly_sq_rate_show_command, static) = {
  .path = "show ly-route flow-rate",
  .short_help = "show ly-route flow-rate",
  .function = ly_sq_rate_show_command_fn,
};

static clib_error_t *
ly_sq_init (vlib_main_t *vm)
{
  vlib_handoff_alloc_queues_args_t queue_args = {
    .node_index = ly_sq_enqueue_node.index,
  };

  ly_sq_main.vlib_main = vm;
  ly_sq_main.vnet_main = vnet_get_main ();
  ly_sq_main.arc_index =
    ly_sq_main.vnet_main->interface_main.output_feature_arc_index;
  ly_sq_main.scheduler_thread_index = 0;
  ly_sq_main.frame_queue_index = vlib_handoff_alloc_queues (&queue_args);
  return 0;
}

VLIB_INIT_FUNCTION (ly_sq_init);

VNET_FEATURE_INIT (ly_sq_output_feature, static) = {
  .arc_name = "interface-output",
  .node_name = "ly-route-smart-qos-output",
  .runs_before = VNET_FEATURES ("interface-output-arc-end"),
};
