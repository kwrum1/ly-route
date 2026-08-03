(function initializeOrchestratorModel() {
  function clone(value) {
    return JSON.parse(JSON.stringify(value));
  }

  function inventoryItems(payload) {
    if (!Array.isArray(payload?.items)) return [];
    return payload.items.map((item) => {
      const speed = String(item.speed || "").match(/\d+/);
      return {
        ...item,
        management: item.management === true
          || item.gateway_role === "management"
          || item.mode_role?.gateway === "management",
        speed_mbps: item.speed_mbps || (speed ? Number(speed[0]) : 0),
        driver: item.driver || item.active_path || item.work_mode || "",
      };
    });
  }

  function managementName(inventory, topology) {
    return topology?.management_interface || inventory.find((item) => item.management === true)?.name || "";
  }

  function emptyTopology(managementInterface) {
    return { schema_version: 1, management_interface: managementInterface, interfaces: [], orchestration_groups: [] };
  }

  function normalizeTopology(topology) {
    const normalized = clone(topology);
    normalized.interfaces.sort((left, right) => left.role.localeCompare(right.role));
    normalized.orchestration_groups.sort((left, right) => left.name.localeCompare(right.name));
    for (const item of normalized.interfaces) item.bond?.members.sort();
    for (const group of normalized.orchestration_groups) group.ports.sort((left, right) => left.direction.localeCompare(right.direction));
    return normalized;
  }

  function topologyMatches(expected, actual) {
    const comparable = (topology) => {
      const normalized = normalizeTopology(topology);
      delete normalized.management_shared;
      return normalized;
    };
    return JSON.stringify(comparable(expected)) === JSON.stringify(comparable(actual));
  }

  function roleInterface(topology, role) {
    return topology?.interfaces?.find((item) => item.role === role) || null;
  }

  function rolePorts(item) {
    if (!item) return [];
    if (item.port) return [item.port];
    return Array.isArray(item.bond?.members) ? item.bond.members : [];
  }

  function roleLabel(item) {
    if (!item) return "未配置";
    if (item.port) return `单口 ${item.port}`;
    return `${item.bond?.name || "链路聚合"} · ${(item.bond?.members || []).join(" + ")}`;
  }

  function configured(topology) {
    return Boolean(roleInterface(topology, "lan") && roleInterface(topology, "wan"));
  }

  function ownedPorts(topology, excludedRole = "", excludedGroup = "") {
    const owned = new Set();
    for (const item of topology?.interfaces || []) {
      if (item.role !== excludedRole) rolePorts(item).forEach((port) => owned.add(port));
    }
    for (const group of topology?.orchestration_groups || []) {
      if (group.name !== excludedGroup) group.ports.forEach((port) => owned.add(port.interface));
    }
    if (topology?.management_interface) owned.add(topology.management_interface);
    return owned;
  }

  function roleCandidates(inventory, topology, role) {
    const owned = ownedPorts(topology, role, "");
    const current = new Set(rolePorts(roleInterface(topology, role)));
    return inventory.filter((item) => {
      const sharedManagementLAN = role === "lan" && topology.management_shared === true && item.name === topology.management_interface;
      return (item.name !== topology.management_interface || sharedManagementLAN) && (sharedManagementLAN || !owned.has(item.name) || current.has(item.name));
    });
  }

  function groupCandidates(inventory, topology, groupName = "") {
    const owned = ownedPorts(topology, "", groupName);
    const current = new Set((topology.orchestration_groups.find((group) => group.name === groupName)?.ports || []).map((port) => port.interface));
    return inventory.filter((item) => item.name !== topology.management_interface && (!owned.has(item.name) || current.has(item.name)));
  }

  function replaceRole(topology, nextRole) {
    return normalizeTopology({ ...clone(topology), interfaces: [...topology.interfaces.filter((item) => item.role !== nextRole.role), nextRole] });
  }

  function groupMatches(expected, topology) {
    const actual = topology.orchestration_groups.find((group) => group.name === expected.name);
    if (!actual) return false;
    const sort = (ports) => clone(ports).sort((left, right) => left.direction.localeCompare(right.direction));
    return JSON.stringify(sort(expected.ports)) === JSON.stringify(sort(actual.ports));
  }

  function errorMessage(error) {
    const messages = {
      interface_already_owned: "接口已被其他配置占用",
      management_interface_forbidden: "管理口只能用于管理访问",
      group_bond_forbidden: "编排组只能选择物理端口",
      topology_conflict: "配置已被其他管理员更新，请刷新后重试",
      duplicate_group: "编排组名称已存在",
      unauthorized: "登录会话已失效",
      forbidden: "当前账号没有修改权限",
    };
    return messages[error?.code] || error?.message || "API 请求失败";
  }

  window.LyRouteOrchestratorModel = Object.freeze({
    clone, configured, emptyTopology, errorMessage, groupCandidates, groupMatches, inventoryItems,
    managementName, normalizeTopology, ownedPorts, replaceRole, roleCandidates, roleInterface,
    roleLabel, rolePorts, topologyMatches,
  });
}());
