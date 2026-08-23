(function initializeGatewayDependencies(root) {
  const resourceMeta = Object.freeze({
    interfaces: { label: '物理接口' },
    interfaceBonds: { label: '聚合接口' },
    wanLinks: { label: 'WAN 线路' },
    wanGroups: { label: 'WAN 群组' },
    proxyEgresses: { label: '代理出口' },
    routePolicies: { label: '策略路由' },
    portMaps: { label: '端口映射' },
    dnsUpstreams: { label: 'DNS 上游' },
    dnsPolicies: { label: 'DNS 策略' },
    dhcpServers: { label: 'DHCP 服务' },
    dhcpBindings: { label: '静态分配' },
    trafficControl: { label: '流量控制' },
    securityAcls: { label: '安全策略' },
    objectGroups: { label: '对象组' }
  });

  function items(payload) {
    if (Array.isArray(payload)) return payload;
    if (Array.isArray(payload?.items)) return payload.items;
    if (Array.isArray(payload?.data)) return payload.data;
    if (Array.isArray(payload?.data?.items)) return payload.data.items;
    return [];
  }

  function resourceID(item) {
    return String(item?.id || item?.name || item?.username || '').trim();
  }

  function resourceName(item) {
    return String(item?.name || item?.display_name || item?.id || item?.username || '').trim();
  }

  function matches(value, target) {
    if (Array.isArray(value)) return value.some((entry) => matches(entry, target));
    if (value && typeof value === 'object') return ['id', 'name', 'interface_id', 'wan_id', 'egress'].some((key) => matches(value[key], target));
    return String(value || '').trim() === String(target || '').trim();
  }

  function groupReferences(item, target) {
    return [item.members, item.wan_members, item.member_weights, item.weights, item.primary_member, item.backup_member].some((value) => {
      if (value && !Array.isArray(value) && typeof value === 'object') return Object.keys(value).some((key) => matches(key, target)) || matches(value, target);
      return matches(value, target);
    });
  }

  function routeReferences(item, target) {
    return [item.egress, item.wan_group, item.wan_link, item.target_line, item.outbound, item.action?.egress, item.action?.wan_group].some((value) => matches(value, target));
  }

  function portMapReferences(item, target) {
    return [item.wan_link, item.egress, item.wan_id, item.interface_id].some((value) => matches(value, target));
  }

  function dnsOutcomes(item) {
    const policy = item.policy || {};
    const rules = Array.isArray(policy.rules) ? policy.rules : Array.isArray(item.rules) ? item.rules : [];
    return [policy.miss, item.outcome, ...rules.map((rule) => rule?.outcome)].filter(Boolean);
  }

  function dnsPolicyReferencesWAN(item, target) {
    return dnsOutcomes(item).some((outcome) => matches(outcome.wan_egress_id, target));
  }

  function dnsPolicyReferencesUpstream(item, target) {
    return dnsOutcomes(item).some((outcome) => matches(outcome.upstream_id, target));
  }

  function explicitPolicyReference(item, target) {
    return [item.egress, item.wan_id, item.wan_group, item.target_line, item.outbound, item.match?.egress, item.action?.egress].some((value) => matches(value, target));
  }

  function objectReference(value, target, key = '') {
    if (Array.isArray(value)) return value.some((entry) => objectReference(entry, target, key));
    if (value && typeof value === 'object') return Object.entries(value).some(([childKey, child]) => objectReference(child, target, childKey));
    const referenceKey = /(group|object|set|source|destination|src|dst)/i.test(key);
    return referenceKey && matches(value, target);
  }

  function directDependencies(resources, key, id) {
    const dependencies = [];
    const collect = (resourceKey, relation, predicate, actionForItem = () => 'delete') => {
      items(resources[resourceKey]).forEach((item) => {
        if (!predicate(item)) return;
        const itemID = resourceID(item);
        if (!itemID || (resourceKey === key && itemID === id)) return;
        dependencies.push({ resourceKey, resourceId: itemID, name: resourceName(item) || itemID, relation, action: actionForItem(item) });
      });
    };

    if (key === 'interfaces' || key === 'interfaceBonds') {
      collect('wanLinks', '使用该接口', (item) => [item.interface_id, item.system_name].some((value) => matches(value, id)));
      collect('dhcpServers', '绑定该接口', (item) => matches(item.interface_id, id));
      collect('dhcpBindings', '绑定该接口', (item) => matches(item.interface_id, id));
    }
    if (key === 'wanLinks') {
      collect('wanGroups', '包含该 WAN', (item) => groupReferences(item, id), (item) => {
        const members = Array.isArray(item.members) ? item.members : Array.isArray(item.wan_members) ? item.wan_members : [];
        return members.length > 1 ? 'detach_member' : 'delete';
      });
      collect('proxyEgresses', '使用该 WAN 作为承载线路', (item) => matches(item.underlay_wan_id, id));
      collect('routePolicies', '选择该 WAN 作为出口', (item) => routeReferences(item, id));
      collect('portMaps', '绑定该 WAN', (item) => portMapReferences(item, id));
      collect('dnsUpstreams', '通过该 WAN 解析', (item) => matches(item.wan_egress_id, id));
      collect('dnsPolicies', '选择该 WAN 作为解析线路', (item) => dnsPolicyReferencesWAN(item, id));
      collect('trafficControl', '绑定该 WAN', (item) => explicitPolicyReference(item, id));
      collect('securityAcls', '动作引用该 WAN', (item) => explicitPolicyReference(item, id));
    }
    if (key === 'wanGroups' || key === 'proxyEgresses') {
      collect('routePolicies', '选择该出口', (item) => routeReferences(item, id));
      collect('dnsUpstreams', '通过该出口解析', (item) => matches(item.wan_egress_id, id));
      collect('dnsPolicies', '选择该出口作为解析线路', (item) => dnsPolicyReferencesWAN(item, id));
      collect('trafficControl', '绑定该出口', (item) => explicitPolicyReference(item, id));
      collect('securityAcls', '动作引用该出口', (item) => explicitPolicyReference(item, id));
    }
    if (key === 'dnsUpstreams') collect('dnsPolicies', '使用该 DNS 上游', (item) => dnsPolicyReferencesUpstream(item, id));
    if (key === 'objectGroups') {
      ['routePolicies', 'portMaps', 'dnsPolicies', 'trafficControl', 'securityAcls'].forEach((resourceKey) => collect(resourceKey, '引用该对象组', (item) => objectReference(item, id)));
    }
    return dependencies;
  }

  function buildPlan(resourceKey, resourceId, resources) {
    const operations = [];
    const visiting = new Set();
    const visited = new Set();
    const visit = (key, id, relation = '目标资源', name = '', action = 'delete') => {
      const identity = `${key}:${id}`;
      if (visited.has(identity) || visiting.has(identity)) return;
      visiting.add(identity);
      if (action !== 'detach_member') directDependencies(resources, key, id).forEach((dependency) => visit(dependency.resourceKey, dependency.resourceId, dependency.relation, dependency.name, dependency.action));
      visiting.delete(identity);
      visited.add(identity);
      operations.push({ resourceKey: key, resourceId: id, name: name || id, relation, label: resourceMeta[key]?.label || key, action });
    };
    visit(resourceKey, resourceId);
    return {
      target: operations.at(-1),
      dependencies: operations.slice(0, -1),
      operations
    };
  }

  root.LyRouteGatewayDependencies = Object.freeze({ buildPlan, directDependencies, resourceMeta });
}(typeof window === 'undefined' ? globalThis : window));
