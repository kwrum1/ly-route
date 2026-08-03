#include <vnet/feature/feature.h>
#include <vnet/bonding/node.h>
#include <vnet/plugin/plugin.h>
#include <vpp/app/version.h>
#include <ly_route_orchestrator/orchestrator.h>

ly_orch_main_t ly_orch_main;

VLIB_PLUGIN_REGISTER () = {
  .version = VPP_BUILD_VER,
  .description = "LY-Route transparent traffic orchestrator",
};

static void
ly_orch_copy_name (u8 destination[LY_ORCH_NAME_LEN], const u8 *source)
{
  clib_memset (destination, 0, LY_ORCH_NAME_LEN);
  if (source)
    clib_strncpy ((char *) destination, (const char *) source,
                  LY_ORCH_NAME_LEN - 1);
}

void
ly_orch_config_free (ly_orch_config_t *config)
{
  vec_free (config->groups);
  vec_free (config->rules);
  clib_memset (config, 0, sizeof (*config));
  config->wan_sw_if_index = LY_ORCH_INVALID_INDEX;
  config->lan_sw_if_index = LY_ORCH_INVALID_INDEX;
  config->wan_next_index = LY_ORCH_INVALID_INDEX;
  config->lan_next_index = LY_ORCH_INVALID_INDEX;
}

u32
ly_orch_output_next (u32 sw_if_index)
{
  ly_orch_main_t *om = &ly_orch_main;
  vnet_hw_interface_t *hw =
    vnet_get_sup_hw_interface (om->vnet_main, sw_if_index);
  return vlib_node_add_next (om->vlib_main, ly_orch_node.index,
                             hw->output_node_index);
}

static int
ly_orch_rule_compare (void *left, void *right)
{
  ly_orch_rule_t *a = left;
  ly_orch_rule_t *b = right;
  if (a->group_position != b->group_position)
    return a->group_position < b->group_position ? -1 : 1;
  if (a->sequence != b->sequence)
    return a->sequence < b->sequence ? -1 : 1;
  return strcmp ((char *) a->id, (char *) b->id);
}

static int
ly_orch_find_group (ly_orch_config_t *config, const u8 *name)
{
  u32 index;
  vec_foreach_index (index, config->groups)
    if (strcmp ((char *) config->groups[index].name,
                (const char *) name) == 0)
      return index;
  return -1;
}

static int
ly_orch_interface_is_hardware (u32 sw_if_index)
{
  ly_orch_main_t *om = &ly_orch_main;
  if (pool_is_free_index (om->vnet_main->interface_main.sw_interfaces,
                          sw_if_index))
    return 0;
  return vnet_get_sw_interface (om->vnet_main, sw_if_index)->type ==
         VNET_SW_INTERFACE_TYPE_HARDWARE;
}

static clib_error_t *
ly_orch_candidate_clear (void)
{
  ly_orch_config_free (&ly_orch_main.candidate);
  ly_orch_main.candidate.default_action = LY_ORCH_ACTION_DIRECT;
  return 0;
}

static clib_error_t *
ly_orch_candidate_boundary (unformat_input_t *input)
{
  ly_orch_config_t *candidate = &ly_orch_main.candidate;
  u32 wan = LY_ORCH_INVALID_INDEX, lan = LY_ORCH_INVALID_INDEX;
  if (!unformat (input, "wan %U lan %U", unformat_vnet_sw_interface,
                 ly_orch_main.vnet_main, &wan, unformat_vnet_sw_interface,
                 ly_orch_main.vnet_main, &lan))
    return clib_error_return (0, "expected wan <interface> lan <interface>");
  if (wan == lan || !ly_orch_interface_is_hardware (wan) ||
      !ly_orch_interface_is_hardware (lan))
    return clib_error_return (0, "WAN and LAN must be different hardware interfaces");
  candidate->wan_sw_if_index = wan;
  candidate->lan_sw_if_index = lan;
  candidate->valid = 1;
  return 0;
}

static clib_error_t *
ly_orch_candidate_group (unformat_input_t *input)
{
  ly_orch_config_t *candidate = &ly_orch_main.candidate;
  ly_orch_group_t group = { 0 };
  u8 *name = 0;
  if (!unformat (input, "%s wan-facing %U lan-facing %U", &name,
                 unformat_vnet_sw_interface, ly_orch_main.vnet_main,
                 &group.wan_sw_if_index, unformat_vnet_sw_interface,
                 ly_orch_main.vnet_main, &group.lan_sw_if_index))
    {
      vec_free (name);
      return clib_error_return (
        0, "expected <name> wan-facing <interface> lan-facing <interface>");
    }
  if (!candidate->valid)
    {
      vec_free (name);
      return clib_error_return (0, "candidate boundary is required first");
    }
  if (vec_len (candidate->groups) >= 64 || !name || vec_len (name) >= LY_ORCH_NAME_LEN ||
      ly_orch_find_group (candidate, name) >= 0 ||
      group.wan_sw_if_index == group.lan_sw_if_index ||
      !ly_orch_interface_is_hardware (group.wan_sw_if_index) ||
      !ly_orch_interface_is_hardware (group.lan_sw_if_index))
    {
      vec_free (name);
      return clib_error_return (0, "invalid or duplicate orchestration group");
    }
  ly_orch_copy_name (group.name, name);
  vec_free (name);
  vec_add1 (candidate->groups, group);
  return 0;
}

static clib_error_t *
ly_orch_candidate_rule (unformat_input_t *input)
{
  ly_orch_config_t *candidate = &ly_orch_main.candidate;
  ly_orch_rule_t rule = { 0 };
  u8 *id = 0, *action = 0, *target = 0, *family = 0;
  ip4_address_t source4 = { 0 }, destination4 = { 0 };
  ip6_address_t source6 = { 0 }, destination6 = { 0 };
  u32 protocol = 0, source_start = 0, source_end = 65535;
  u32 destination_start = 0, destination_end = 65535;
  u32 source_prefix = 0, destination_prefix = 0;
  clib_error_t *error = 0;

  if (!unformat (input,
                 "id %s group-position %u sequence %u action %s target %s "
                 "family %s",
                 &id, &rule.group_position, &rule.sequence, &action, &target,
                 &family))
    {
      error = clib_error_return (0, "invalid rule header");
      goto done;
    }
  if (!candidate->valid || !id || vec_len (id) >= LY_ORCH_NAME_LEN ||
      rule.group_position == 0 || rule.sequence == 0)
    {
      error = clib_error_return (0, "invalid rule identity or ordering");
      goto done;
    }
  if (strcmp ((char *) action, "via") == 0)
    {
      int group = ly_orch_find_group (candidate, target);
      if (group < 0)
        {
          error = clib_error_return (0, "rule target group does not exist");
          goto done;
        }
      rule.action = LY_ORCH_ACTION_VIA;
      rule.target_group = group;
    }
  else if (strcmp ((char *) action, "direct") == 0)
    rule.action = LY_ORCH_ACTION_DIRECT;
  else if (strcmp ((char *) action, "drop") == 0)
    rule.action = LY_ORCH_ACTION_DROP;
  else
    {
      error = clib_error_return (0, "rule action must be via, direct, or drop");
      goto done;
    }

  if (strcmp ((char *) family, "ip4") == 0)
    {
      if (!unformat (input,
                     "src %U/%u dst %U/%u proto %u sport %u-%u dport %u-%u",
                     unformat_ip4_address, &source4, &source_prefix,
                     unformat_ip4_address, &destination4,
                     &destination_prefix, &protocol, &source_start,
                     &source_end, &destination_start, &destination_end) ||
          source_prefix > 32 || destination_prefix > 32)
        {
          error = clib_error_return (0, "invalid IPv4 rule match");
          goto done;
        }
      rule.source.ip4 = source4;
      rule.destination.ip4 = destination4;
    }
  else if (strcmp ((char *) family, "ip6") == 0)
    {
      rule.is_ip6 = 1;
      if (!unformat (input,
                     "src %U/%u dst %U/%u proto %u sport %u-%u dport %u-%u",
                     unformat_ip6_address, &source6, &source_prefix,
                     unformat_ip6_address, &destination6,
                     &destination_prefix, &protocol, &source_start,
                     &source_end, &destination_start, &destination_end) ||
          source_prefix > 128 || destination_prefix > 128)
        {
          error = clib_error_return (0, "invalid IPv6 rule match");
          goto done;
        }
      rule.source.ip6 = source6;
      rule.destination.ip6 = destination6;
    }
  else
    {
      error = clib_error_return (0, "rule family must be ip4 or ip6");
      goto done;
    }
  if (protocol > 255 || source_start > source_end || source_end > 65535 ||
      destination_start > destination_end || destination_end > 65535)
    {
      error = clib_error_return (0, "invalid protocol or port range");
      goto done;
    }
  ly_orch_copy_name (rule.id, id);
  rule.protocol = protocol;
  rule.source_prefix_length = source_prefix;
  rule.destination_prefix_length = destination_prefix;
  rule.source_port_start = source_start;
  rule.source_port_end = source_end;
  rule.destination_port_start = destination_start;
  rule.destination_port_end = destination_end;
  if (vec_len (candidate->rules) >= 4096)
    error = clib_error_return (0, "candidate rule limit exceeded");
  else
    vec_add1 (candidate->rules, rule);

done:
  vec_free (id);
  vec_free (action);
  vec_free (target);
  vec_free (family);
  return error;
}

static int
ly_orch_capture_interface_matches (u32 configured_sw_if_index,
                                   u32 capture_sw_if_index)
{
  bond_if_t *bond =
    bond_get_bond_if_by_sw_if_index (configured_sw_if_index);
  u32 index;

  if (!bond)
    return configured_sw_if_index == capture_sw_if_index;
  vec_foreach_index (index, bond->members)
    if (bond->members[index] == capture_sw_if_index)
      return 1;
  return 0;
}

static int
ly_orch_config_interface_set (ly_orch_config_t *config, u32 sw_if_index)
{
  u32 index;
  if (ly_orch_capture_interface_matches (config->wan_sw_if_index,
                                         sw_if_index) ||
      ly_orch_capture_interface_matches (config->lan_sw_if_index,
                                         sw_if_index))
    return 1;
  vec_foreach_index (index, config->groups)
    if (ly_orch_capture_interface_matches (
          config->groups[index].wan_sw_if_index, sw_if_index) ||
        ly_orch_capture_interface_matches (
          config->groups[index].lan_sw_if_index, sw_if_index))
      return 1;
  return 0;
}

static clib_error_t *
ly_orch_validate_candidate (ly_orch_config_t *candidate)
{
  u32 first, second;
  if (!candidate->valid || candidate->wan_sw_if_index == LY_ORCH_INVALID_INDEX ||
      candidate->lan_sw_if_index == LY_ORCH_INVALID_INDEX)
    return clib_error_return (0, "candidate boundary is incomplete");
  for (first = 0; first < vec_len (candidate->groups); first++)
    {
      ly_orch_group_t *group = &candidate->groups[first];
      if (group->wan_sw_if_index == candidate->wan_sw_if_index ||
          group->wan_sw_if_index == candidate->lan_sw_if_index ||
          group->lan_sw_if_index == candidate->wan_sw_if_index ||
          group->lan_sw_if_index == candidate->lan_sw_if_index)
        return clib_error_return (0, "group interface overlaps WAN or LAN");
      for (second = first + 1; second < vec_len (candidate->groups); second++)
        if (group->wan_sw_if_index == candidate->groups[second].wan_sw_if_index ||
            group->wan_sw_if_index == candidate->groups[second].lan_sw_if_index ||
            group->lan_sw_if_index == candidate->groups[second].wan_sw_if_index ||
            group->lan_sw_if_index == candidate->groups[second].lan_sw_if_index)
          return clib_error_return (0, "group interfaces overlap");
    }
  return 0;
}

static void
ly_orch_workers_free (ly_orch_worker_t *workers)
{
  u32 index;
  vec_foreach_index (index, workers)
    {
      vec_free (workers[index].rule_counters);
      vec_free (workers[index].group_bypass_packets);
      clib_mem_free (workers[index].flows);
    }
  vec_free (workers);
}

static ly_orch_worker_t *
ly_orch_workers_create (u32 rules, u32 groups)
{
  ly_orch_worker_t *workers = 0;
  u32 index, count = vlib_get_n_threads ();
  vec_validate (workers, count - 1);
  for (index = 0; index < count; index++)
    {
      if (rules)
        vec_validate (workers[index].rule_counters, rules - 1);
      if (groups)
        vec_validate (workers[index].group_bypass_packets, groups - 1);
      workers[index].flows = clib_mem_alloc_aligned (
        sizeof (ly_orch_flow_t) * LY_ORCH_FLOW_SLOTS,
        CLIB_CACHE_LINE_BYTES);
      clib_memset (workers[index].flows, 0,
                   sizeof (ly_orch_flow_t) * LY_ORCH_FLOW_SLOTS);
    }
  return workers;
}

static ly_orch_worker_t *
ly_orch_workers_snapshot (u32 rules, u32 groups)
{
  ly_orch_main_t *om = &ly_orch_main;
  ly_orch_worker_t *snapshot = ly_orch_workers_create (rules, groups);
  u32 index;

  vlib_worker_thread_barrier_sync (om->vlib_main);
  vec_foreach_index (index, snapshot)
    {
      if (rules)
        clib_memcpy_fast (snapshot[index].rule_counters,
                          om->workers[index].rule_counters,
                          sizeof (ly_orch_rule_counter_t) * rules);
      if (groups)
        clib_memcpy_fast (snapshot[index].group_bypass_packets,
                          om->workers[index].group_bypass_packets,
                          sizeof (u64) * groups);
      clib_memcpy_fast (snapshot[index].flows, om->workers[index].flows,
                        sizeof (ly_orch_flow_t) * LY_ORCH_FLOW_SLOTS);
    }
  vlib_worker_thread_barrier_release (om->vlib_main);
  return snapshot;
}

static clib_error_t *
ly_orch_commit (unformat_input_t *input)
{
  ly_orch_main_t *om = &ly_orch_main;
  ly_orch_config_t *candidate = &om->candidate;
  ly_orch_config_t old_config;
  ly_orch_worker_t *old_workers;
  ly_orch_worker_t *new_workers;
  u8 *generation = 0;
  u32 index, max_sw_if_index = 0;
  clib_error_t *error;

  if (!unformat (input, "generation %s", &generation) || !generation ||
      vec_len (generation) >= LY_ORCH_GENERATION_LEN)
    {
      vec_free (generation);
      return clib_error_return (0, "valid generation is required");
    }
  if ((error = ly_orch_validate_candidate (candidate)))
    {
      vec_free (generation);
      return error;
    }
  vec_sort_with_function (candidate->rules, ly_orch_rule_compare);
  candidate->wan_next_index = ly_orch_output_next (candidate->wan_sw_if_index);
  candidate->lan_next_index = ly_orch_output_next (candidate->lan_sw_if_index);
  vec_foreach_index (index, candidate->groups)
    {
      candidate->groups[index].wan_next_index =
        ly_orch_output_next (candidate->groups[index].wan_sw_if_index);
      candidate->groups[index].lan_next_index =
        ly_orch_output_next (candidate->groups[index].lan_sw_if_index);
    }
  clib_memset (candidate->generation, 0, LY_ORCH_GENERATION_LEN);
  clib_strncpy ((char *) candidate->generation, (const char *) generation,
                LY_ORCH_GENERATION_LEN - 1);
  vec_free (generation);
  new_workers = ly_orch_workers_create (vec_len (candidate->rules),
                                        vec_len (candidate->groups));

  if (vec_len (om->vnet_main->interface_main.sw_interfaces))
    max_sw_if_index =
      vec_len (om->vnet_main->interface_main.sw_interfaces) - 1;
  vec_validate_init_empty (om->enabled_by_sw_if_index, max_sw_if_index, 0);

  vlib_worker_thread_barrier_sync (om->vlib_main);
  for (index = 0; index < vec_len (om->enabled_by_sw_if_index); index++)
    if (om->enabled_by_sw_if_index[index] &&
        !ly_orch_config_interface_set (candidate, index))
      {
        vnet_feature_enable_disable ("device-input", "ly-route-orchestrator",
                                     index, 0, 0, 0);
        om->enabled_by_sw_if_index[index] = 0;
      }
  for (index = 0; index < vec_len (om->enabled_by_sw_if_index); index++)
    if (ly_orch_config_interface_set (candidate, index) &&
        !om->enabled_by_sw_if_index[index])
      {
        vnet_feature_enable_disable ("device-input", "ly-route-orchestrator",
                                     index, 1, 0, 0);
        om->enabled_by_sw_if_index[index] = 1;
      }
  old_config = om->active;
  old_workers = om->workers;
  om->active = *candidate;
  om->workers = new_workers;
  clib_memset (candidate, 0, sizeof (*candidate));
  candidate->wan_sw_if_index = LY_ORCH_INVALID_INDEX;
  candidate->lan_sw_if_index = LY_ORCH_INVALID_INDEX;
  candidate->default_action = LY_ORCH_ACTION_DIRECT;
  vlib_worker_thread_barrier_release (om->vlib_main);

  ly_orch_config_free (&old_config);
  ly_orch_workers_free (old_workers);
  return 0;
}

static clib_error_t *
ly_orch_disable (void)
{
  ly_orch_main_t *om = &ly_orch_main;
  ly_orch_config_t old_config;
  ly_orch_worker_t *old_workers;
  u32 index;

  vlib_worker_thread_barrier_sync (om->vlib_main);
  for (index = 0; index < vec_len (om->enabled_by_sw_if_index); index++)
    if (om->enabled_by_sw_if_index[index])
      {
        vnet_feature_enable_disable ("device-input", "ly-route-orchestrator",
                                     index, 0, 0, 0);
        om->enabled_by_sw_if_index[index] = 0;
      }
  old_config = om->active;
  old_workers = om->workers;
  clib_memset (&om->active, 0, sizeof (om->active));
  om->active.wan_sw_if_index = LY_ORCH_INVALID_INDEX;
  om->active.lan_sw_if_index = LY_ORCH_INVALID_INDEX;
  om->workers = 0;
  vlib_worker_thread_barrier_release (om->vlib_main);

  ly_orch_config_free (&old_config);
  ly_orch_workers_free (old_workers);
  ly_orch_candidate_clear ();
  return 0;
}

static clib_error_t *
ly_orch_set_command_fn (vlib_main_t *vm, unformat_input_t *input,
                        vlib_cli_command_t *cmd)
{
  if (unformat (input, "candidate clear"))
    return ly_orch_candidate_clear ();
  if (unformat (input, "candidate boundary"))
    return ly_orch_candidate_boundary (input);
  if (unformat (input, "candidate group"))
    return ly_orch_candidate_group (input);
  if (unformat (input, "candidate rule"))
    return ly_orch_candidate_rule (input);
  if (unformat (input, "candidate default direct"))
    {
      ly_orch_main.candidate.default_action = LY_ORCH_ACTION_DIRECT;
      return 0;
    }
  if (unformat (input, "candidate default drop"))
    {
      ly_orch_main.candidate.default_action = LY_ORCH_ACTION_DROP;
      return 0;
    }
  if (unformat (input, "disable"))
    return ly_orch_disable ();
  if (unformat (input, "commit"))
    return ly_orch_commit (input);
  return clib_error_return (0, "unknown orchestrator configuration command");
}

VLIB_CLI_COMMAND (ly_orch_set_command, static) = {
  .path = "set ly-route orchestrator",
  .short_help =
    "set ly-route orchestrator candidate clear|boundary|group|rule|default; "
    "set ly-route orchestrator commit generation <id>|disable",
  .function = ly_orch_set_command_fn,
};

static const char *
ly_orch_action_name (ly_orch_action_t action)
{
  if (action == LY_ORCH_ACTION_VIA)
    return "via";
  if (action == LY_ORCH_ACTION_DROP)
    return "drop";
  return "direct";
}

static clib_error_t *
ly_orch_show_command_fn (vlib_main_t *vm, unformat_input_t *input,
                         vlib_cli_command_t *cmd)
{
  ly_orch_main_t *om = &ly_orch_main;
  ly_orch_config_t *config = &om->active;
  ly_orch_worker_t *workers;
  u32 index, thread;
  f64 now = vlib_time_now (vm);
  if (!config->valid)
    {
      vlib_cli_output (vm, "state locked");
      return 0;
    }
  workers = ly_orch_workers_snapshot (vec_len (config->rules),
                                      vec_len (config->groups));
  vlib_cli_output (vm, "state running");
  vlib_cli_output (vm, "generation %s", config->generation);
  vlib_cli_output (vm, "boundary wan %U lan %U", format_vnet_sw_if_index_name,
                   om->vnet_main, config->wan_sw_if_index,
                   format_vnet_sw_if_index_name, om->vnet_main,
                   config->lan_sw_if_index);
  vlib_cli_output (vm, "default %s",
                   ly_orch_action_name (config->default_action));
  vec_foreach_index (index, config->groups)
    {
      u64 bypass_packets = 0;
      vec_foreach_index (thread, workers)
        bypass_packets += workers[thread].group_bypass_packets[index];
      vlib_cli_output (vm, "group %s wan-facing %U lan-facing %U",
                       config->groups[index].name,
                       format_vnet_sw_if_index_name, om->vnet_main,
                       config->groups[index].wan_sw_if_index,
                       format_vnet_sw_if_index_name, om->vnet_main,
                       config->groups[index].lan_sw_if_index);
      vlib_cli_output (vm, "group-health %s state %s bypass-packets %llu",
                       config->groups[index].name,
                       vnet_sw_interface_is_up (
                         om->vnet_main,
                         config->groups[index].wan_sw_if_index) &&
                           vnet_sw_interface_is_up (
                             om->vnet_main,
                             config->groups[index].lan_sw_if_index) ?
                         "up" : "bypass",
                       bypass_packets);
    }
  vec_foreach_index (index, config->rules)
    {
      u64 packets = 0, bytes = 0;
      vec_foreach_index (thread, workers)
        if (index < vec_len (workers[thread].rule_counters))
          {
            packets += workers[thread].rule_counters[index].packets;
            bytes += workers[thread].rule_counters[index].bytes;
          }
      vlib_cli_output (vm,
                       "policy %s group-position %u sequence %u action %s "
                       "packets %llu bytes %llu",
                       config->rules[index].id,
                       config->rules[index].group_position,
                       config->rules[index].sequence,
                       ly_orch_action_name (config->rules[index].action),
                       packets, bytes);
    }
  vec_foreach_index (thread, workers)
    for (index = 0; index < LY_ORCH_FLOW_SLOTS; index++)
      {
        ly_orch_flow_t *flow = &workers[thread].flows[index];
        u32 group;
        u8 *groups = 0;
        if (!flow->occupied ||
            now - flow->last_seen > LY_ORCH_FLOW_TTL_SECONDS)
          continue;
        for (group = 0; group < flow->group_count; group++)
          groups = format (groups, "%s%s", group ? "," : "",
                           config->groups[flow->groups[group]].name);
        vec_add1 (groups, 0);
        vlib_cli_output (
          vm,
          "flow family %s src %U dst %U proto %u sport %u dport %u "
          "packets %llu bytes %llu age %.6f groups %s",
          flow->is_ip6 ? "ip6" : "ip4",
          flow->is_ip6 ? format_ip6_address : format_ip4_address,
          flow->is_ip6 ? (void *) &flow->source.ip6 :
                         (void *) &flow->source.ip4,
          flow->is_ip6 ? format_ip6_address : format_ip4_address,
          flow->is_ip6 ? (void *) &flow->destination.ip6 :
                         (void *) &flow->destination.ip4,
          flow->protocol, flow->source_port, flow->destination_port,
          flow->packets, flow->bytes, now - flow->last_seen,
          groups ? groups : (u8 *) "-");
        vec_free (groups);
      }
  ly_orch_workers_free (workers);
  return 0;
}

VLIB_CLI_COMMAND (ly_orch_show_command, static) = {
  .path = "show ly-route orchestrator",
  .short_help = "show ly-route orchestrator",
  .function = ly_orch_show_command_fn,
};

static clib_error_t *
ly_orch_init (vlib_main_t *vm)
{
  ly_orch_main_t *om = &ly_orch_main;
  om->vlib_main = vm;
  om->vnet_main = vnet_get_main ();
  ly_orch_config_free (&om->active);
  ly_orch_config_free (&om->candidate);
  om->candidate.default_action = LY_ORCH_ACTION_DIRECT;
  return 0;
}

VLIB_INIT_FUNCTION (ly_orch_init);

VNET_FEATURE_INIT (ly_orch_input_feature, static) = {
  .arc_name = "device-input",
  .node_name = "ly-route-orchestrator",
  .runs_before = VNET_FEATURES ("ethernet-input"),
};
