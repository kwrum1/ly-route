(function initializeOrchestratorAPI() {
  class OrchestratorAPIError extends Error {
    constructor(status, code, message) {
      super(message);
      this.name = "OrchestratorAPIError";
      this.status = status;
      this.code = code;
    }
  }

  async function parseResponse(response) {
    if (response.status === 204) return null;
    const text = await response.text();
    if (!text) return null;
    try {
      return JSON.parse(text);
    } catch {
      throw new OrchestratorAPIError(response.status, "invalid_response", "API 返回了无效 JSON");
    }
  }

  function createClient() {
    async function request(path, options = {}) {
      const response = await fetch(path, { ...options, credentials: "same-origin" });
      const payload = await parseResponse(response);
      if (!response.ok) {
        const code = payload?.error?.code || "request_failed";
        const message = payload?.error?.message || `API ${response.status}`;
        throw new OrchestratorAPIError(response.status, code, message);
      }
      return payload;
    }

    return Object.freeze({
      session: () => fetch("/api/v1/auth/session", { credentials: "same-origin" }),
      login: (username, password) => fetch("/api/v1/auth/login", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ username, password }),
      }),
      logout: () => fetch("/api/v1/auth/logout", { method: "POST", credentials: "same-origin" }),
      product: () => request("/api/v1/product"),
      health: () => request("/api/v1/health"),
      capabilities: () => request("/api/v1/capabilities"),
      inventory: () => request("/api/v1/interfaces"),
      managementNetwork: () => request("/api/v1/management/network"),
      ipGroups: () => request("/api/v1/objects/ip-groups"),
      saveIPGroup: (payload, exists) => request(exists ? `/api/v1/objects/ip-groups/${encodeURIComponent(payload.id)}` : "/api/v1/objects/ip-groups", {
        method: exists ? "PATCH" : "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      }),
      deleteIPGroup: (id) => request(`/api/v1/objects/ip-groups/${encodeURIComponent(id)}`, { method: "DELETE" }),
      securityACLs: () => request("/api/v1/security/acls"),
      saveSecurityACL: (payload, exists) => request(exists ? `/api/v1/security/acls/${encodeURIComponent(payload.id)}` : "/api/v1/security/acls", {
        method: exists ? "PATCH" : "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      }),
      deleteSecurityACL: (id) => request(`/api/v1/security/acls/${encodeURIComponent(id)}`, { method: "DELETE" }),
      users: () => request("/api/v1/auth/users"),
      saveUser: (payload, exists) => request(exists ? `/api/v1/auth/users/${encodeURIComponent(payload.username)}` : "/api/v1/auth/users", {
        method: exists ? "PATCH" : "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      }),
      deleteUser: (username) => request(`/api/v1/auth/users/${encodeURIComponent(username)}`, { method: "DELETE" }),
      trafficControl: () => request("/api/v1/flow-control/policies"),
      saveTrafficControl: (payload, exists) => request(exists ? `/api/v1/flow-control/policies/${encodeURIComponent(payload.id)}` : "/api/v1/flow-control/policies", {
        method: exists ? "PATCH" : "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      }),
      applyRuntime: () => request("/api/v1/runtime/apply", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: "{}",
      }),
      saveManagementNetwork: (payload) => request("/api/v1/management/network", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      }),
      topology: () => request("/api/v1/orchestrator/topology"),
      saveTopology: (topology) => request("/api/v1/orchestrator/topology", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(topology),
      }),
      createGroup: (group) => request("/api/v1/orchestrator/orchestration-groups", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(group),
      }),
      updateGroup: (name, group) => request(`/api/v1/orchestrator/orchestration-groups/${encodeURIComponent(name)}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(group),
      }),
      deleteGroup: (name) => request(`/api/v1/orchestrator/orchestration-groups/${encodeURIComponent(name)}`, { method: "DELETE" }),
      policy: () => request("/api/v1/orchestrator/policy"),
      savePolicy: (policy) => request("/api/v1/orchestrator/policy", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(policy) }),
      flowSummary: () => request("/api/v1/telemetry/dashboard"),
      policyHits: () => request("/api/v1/telemetry/policy-hits"),
      onlineUsers: () => request("/api/v1/telemetry/online-users"),
      topConnections: () => request("/api/v1/telemetry/top-sessions"),
      runtimeStatus: () => request("/api/v1/runtime/status"),
      configExport: () => request("/api/v1/config/export"),
      snapshots: () => request("/api/v1/config/snapshots"),
      createSnapshot: (name) => request("/api/v1/config/snapshots", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }) }),
      restoreSnapshot: (id) => request(`/api/v1/config/snapshots/${encodeURIComponent(id)}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" }),
      preflightConfigImport: (packageData) => request("/api/v1/config/import", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ...packageData, confirm: false, dry_run: true }) }),
      confirmConfigImport: (packageData, preflight) => request("/api/v1/config/import", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ...packageData, confirm: true, dry_run: false, confirmation_token: preflight.confirmation_token, confirmation_actor: preflight.confirmation_actor, confirmation_expires_at: preflight.confirmation_expires_at, package_hash: preflight.package_hash, diff_hash: preflight.diff_hash }),
      }),
    });
  }

  window.LyRouteOrchestratorAPI = Object.freeze({ createClient, OrchestratorAPIError });
}());
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
    if (item.port) return item.port;
    return (item.bond?.members || []).join(" ") || "链路聚合";
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
    const physical = (item) => {
      const name = String(item.name || item.id || "");
      return item.type !== "bond" && item.kind !== "bond" && !item.bond && !Array.isArray(item.members)
        && !name.startsWith("bond-") && !name.startsWith("聚合");
    };
    return inventory.filter((item) => {
      if (!physical(item)) return false;
      const sharedManagementLAN = role === "lan" && topology.management_shared === true && item.name === topology.management_interface;
      return (item.name !== topology.management_interface || sharedManagementLAN) && (sharedManagementLAN || !owned.has(item.name) || current.has(item.name));
    });
  }

  function groupCandidates(inventory, topology, groupName = "") {
    const owned = ownedPorts(topology, "", groupName);
    const current = new Set((topology.orchestration_groups.find((group) => group.name === groupName)?.ports || []).map((port) => port.interface));
    return inventory.filter((item) => {
      const name = String(item.name || item.id || "");
      const physical = item.type !== "bond" && item.kind !== "bond" && !item.bond && !Array.isArray(item.members)
        && !name.startsWith("bond-") && !name.startsWith("聚合");
      return physical && item.name !== topology.management_interface && (!owned.has(item.name) || current.has(item.name));
    });
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
(function initializeOrchestratorModal() {
  const modal = document.getElementById("modal");
  const backdrop = document.getElementById("modalBackdrop");
  const title = document.getElementById("modalTitle");
  const body = document.getElementById("modalBody");
  const closeButton = document.getElementById("modalClose");
  const cancelButton = document.getElementById("modalCancel");
  const submitButton = document.getElementById("modalOk");
  let submit = null;
  let trigger = null;
  let busy = false;

  function focusable() {
    return [...modal.querySelectorAll("button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex='-1'])")]
      .filter((element) => !element.hidden && element.getClientRects().length > 0);
  }

  function close() {
    if (busy) return;
    modal.classList.add("is-hidden");
    backdrop.classList.add("is-hidden");
    body.innerHTML = "";
    submit = null;
    const restore = trigger;
    trigger = null;
    restore?.focus();
  }

  function onKeydown(event) {
    if (event.key === "Escape") {
      event.preventDefault();
      close();
      return;
    }
    if (event.key !== "Tab") return;
    const controls = focusable();
    const first = controls[0];
    const last = controls[controls.length - 1];
    if (!first || !last) return;
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  function open(options) {
    trigger = options.trigger || document.activeElement;
    title.textContent = options.title;
    body.innerHTML = options.html;
    submitButton.textContent = options.submitLabel || "确定";
    submit = options.onSubmit;
    backdrop.classList.remove("is-hidden");
    modal.classList.remove("is-hidden");
    queueMicrotask(() => (body.querySelector("input, select, textarea") || closeButton).focus());
  }

  closeButton.addEventListener("click", close);
  cancelButton.addEventListener("click", close);
  backdrop.addEventListener("click", close);
  modal.addEventListener("keydown", onKeydown);
  submitButton.addEventListener("click", async () => {
    if (busy || !submit) return;
    const form = body.querySelector("form");
    if (form && !form.reportValidity()) return;
    busy = true;
    submitButton.disabled = true;
    cancelButton.disabled = true;
    try {
      const shouldClose = await submit(body);
      if (shouldClose !== false) {
        busy = false;
        close();
      }
    } finally {
      busy = false;
      submitButton.disabled = false;
      cancelButton.disabled = false;
    }
  });

  window.LyRouteOrchestratorModal = Object.freeze({ close, open });
}());
(function initializeNICSettings() {
  const model = window.LyRouteOrchestratorModel;

  function option(item, selected = false) {
    const health = item.link_state === "down" ? "链路断开" : "链路正常";
    return `<option value="${item.name}" ${selected ? "selected" : ""}>${item.name} · ${item.speed_mbps || "未知"} Mbps · ${health}</option>`;
  }

  function createNICController({ client, modal, safeText, state, render }) {
    function draftTopology() {
      if (state.draft) return state.draft;
      const management = model.managementName(state.inventory, state.topology);
      return state.topology ? model.clone(state.topology) : model.emptyTopology(management);
    }

    function roleForm(role) {
      const topology = draftTopology();
      const current = model.roleInterface(topology, role);
      const candidates = model.roleCandidates(state.inventory, topology, role);
      const currentPorts = new Set(model.rolePorts(current));
      const kind = current?.bond ? "bond" : "port";
      const portOptions = candidates.map((item) => option(item, current?.port === item.name)).join("");
      const members = candidates.map((item) => `<label class="orchestrator-port-choice"><input type="checkbox" data-bond-member value="${item.name}" ${currentPorts.has(item.name) ? "checked" : ""}><span><strong>${item.name}</strong><small>${item.link_state === "down" ? "链路断开" : "链路正常"} · ${item.speed_mbps || "未知"} Mbps</small></span></label>`).join("");
      return `<form class="orchestrator-form" data-role-form data-role="${role}">
        <label><span>连接方式</span><select aria-label="连接方式" data-role-kind><option value="port" ${kind === "port" ? "selected" : ""}>单物理端口</option><option value="bond" ${kind === "bond" ? "selected" : ""}>LAN/WAN 链路聚合</option></select></label>
        <label data-port-field><span>物理网卡</span><select aria-label="物理网卡" data-role-port>${portOptions}</select></label>
        <fieldset data-bond-field><legend>聚合成员（至少两个）</legend><div class="orchestrator-port-grid">${members}</div></fieldset>
      </form>`;
    }

    function syncRoleForm(body) {
      const kind = body.querySelector("[data-role-kind]");
      const sync = () => {
        const usesBond = kind.value === "bond";
        const portField = body.querySelector("[data-port-field]");
        portField.hidden = usesBond;
        portField.querySelectorAll("input, select, textarea").forEach((control) => { control.disabled = usesBond; });
        body.querySelectorAll("[data-bond-field]").forEach((field) => {
          field.hidden = !usesBond;
          field.querySelectorAll("input, select, textarea").forEach((control) => { control.disabled = !usesBond; });
        });
        if (!usesBond && !portField.querySelector("[data-role-port] option:checked")) {
          portField.querySelector("[data-role-port]")?.options[0] && (portField.querySelector("[data-role-port]").selectedIndex = 0);
        }
      };
      kind.addEventListener("change", sync);
      sync();
    }

    function openRole(role, trigger) {
      modal.open({
        title: `配置 ${role.toUpperCase()}`,
        html: roleForm(role),
        trigger,
        onSubmit(body) {
          const form = body.querySelector("[data-role-form]");
          const kind = form.querySelector("[data-role-kind]").value;
          const nextRole = { name: role, role };
          if (kind === "port") {
            const port = form.querySelector("[data-role-port]").value;
            if (!port) return false;
            nextRole.port = port;
          } else {
            const members = [...form.querySelectorAll("[data-bond-member]:checked")].map((item) => item.value);
            if (members.length < 2) {
              state.notice = { tone: "error", text: "链路聚合至少需要两个成员端口" };
              return false;
            }
            nextRole.bond = { name: `bond-${role}`, members };
          }
          state.draft = model.replaceRole(topologyForDraft(), nextRole);
          return save();
        },
      });
      const roleBody = document.getElementById("modalBody");
      const roleKind = roleBody.querySelector("[data-role-kind]");
      if (roleKind) {
        const portOption = roleKind.querySelector('option[value="port"]');
        const bondOption = roleKind.querySelector('option[value="bond"]');
        if (portOption) portOption.textContent = "单物理口";
        if (bondOption) bondOption.textContent = "链路聚合";
      }
      syncRoleForm(roleBody);
    }

    function topologyForDraft() {
      return state.draft || draftTopology();
    }

    async function save() {
      const expected = model.normalizeTopology(topologyForDraft());
      if (!model.configured(expected)) {
        state.notice = { tone: "error", text: "必须先配置一个 LAN 和一个 WAN" };
        render();
        return;
      }
      state.busy = true;
      state.notice = { tone: "pending", text: "正在保存并等待 API 回读" };
      render();
      try {
        await client.saveTopology(expected);
        let readback;
        try {
          readback = await client.topology();
        } catch (error) {
          const management = model.managementName(state.inventory, state.topology);
          state.draft = state.topology ? model.clone(state.topology) : model.emptyTopology(management);
          state.stale = true;
          state.notice = { tone: "stale", text: "当前显示的是陈旧状态；保存已响应，但 API 回读失败" };
          return;
        }
        state.topology = readback.item;
        state.checksum = readback.checksum;
        state.draft = model.clone(readback.item);
        state.stale = false;
        state.notice = model.topologyMatches(expected, readback.item)
          ? { tone: "success", text: `已从 API 回读确认 · ${readback.checksum}` }
          : { tone: "error", text: "保存响应与 API 回读不一致" };
      } catch (error) {
        state.notice = { tone: "error", text: model.errorMessage(error) };
      } finally {
        state.busy = false;
        render();
      }
    }

    function renderPage() {
      const topology = topologyForDraft();
      const management = model.managementName(state.inventory, topology);
      const lan = model.roleInterface(topology, "lan");
      const wan = model.roleInterface(topology, "wan");
      const owners = model.ownedPorts(topology);
      const bondMembers = new Set([...(lan?.bond?.members || []), ...(wan?.bond?.members || [])]);
      const rows = state.inventory.filter((item) => !bondMembers.has(item.name)).map((item) => {
        const managementPort = item.name === management;
        const health = item.link_state === "down" ? "链路断开" : "链路正常";
        const sharedLAN = managementPort && topology.management_shared === true && model.rolePorts(lan).includes(item.name);
        const owner = sharedLAN ? "LAN + 管理共享" : managementPort ? "管理专用" : model.rolePorts(lan).includes(item.name) ? "LAN" : model.rolePorts(wan).includes(item.name) ? "WAN" : owners.has(item.name) ? "编排组" : "可用";
        return `<tr><td><strong>${safeText(item.name)}</strong></td><td><span class="orchestrator-health ${item.link_state === "down" ? "is-down" : "is-up"}">${health}</span></td><td>${safeText(item.speed_mbps || "未知")} Mbps</td><td>${safeText(item.driver || "未报告")}</td><td>${owner}</td></tr>`;
      }).join("");
      const bondRows = [lan, wan].filter((item) => item?.bond).map((item) => {
        const members = item.bond.members || [];
        const memberItems = members.map((name) => state.inventory.find((entry) => entry.name === name)).filter(Boolean);
        const down = memberItems.some((entry) => entry.link_state === "down");
        const speed = memberItems.reduce((sum, entry) => sum + Number(entry.speed_mbps || 0), 0);
        const roleLabel = item.role === "lan" ? "聚合 LAN" : "聚合 WAN";
        return `<tr><td><strong>${roleLabel}</strong><small>${safeText(members.join(" "))}</small></td><td><span class="orchestrator-health ${down ? "is-down" : "is-up"}">${down ? "链路断开" : "链路正常"}</span></td><td>${speed ? `${speed} Mbps` : "未知"}</td><td>bond</td><td>${item.role.toUpperCase()}</td></tr>`;
      }).join("");
      return `<section class="page-body list-page orchestrator-settings">
        ${state.renderNotice()}
        <section class="orchestrator-summary" aria-label="LAN 和 WAN 配置">
          <div><span>管理口</span><strong>${safeText(management || "未报告")}</strong><small>${topology.management_shared ? "与 LAN 共享，仅管理流量交给 Linux" : "Linux 管理面独占，不参与数据口选择"}</small></div>
          <div><span>LAN</span><strong>${safeText(model.roleLabel(lan))}</strong><button type="button" data-configure-role="lan">配置 LAN</button></div>
          <div><span>WAN</span><strong>${safeText(model.roleLabel(wan))}</strong><button type="button" data-configure-role="wan">配置 WAN</button></div>
        </section>
        <div class="orchestrator-table-wrap"><table class="data-table orchestrator-table"><thead><tr><th>接口</th><th>链路健康</th><th>速率</th><th>驱动</th><th>所有权</th></tr></thead><tbody>${rows}${bondRows}</tbody></table></div>
      </section>`;
    }

    async function handle(target) {
      const roleButton = target.closest("[data-configure-role]");
      if (roleButton) return openRole(roleButton.dataset.configureRole, roleButton);
      if (target.closest("[data-save-nics]")) await save();
    }

    return Object.freeze({ handle, renderPage });
  }

  window.LyRouteOrchestratorNIC = Object.freeze({ createNICController });
}());
(function initializeGroupSettings() {
  const model = window.LyRouteOrchestratorModel;

  function createGroupController({ client, modal, safeText, state, render }) {
    function groupForm(group = null) {
      const topology = state.topology;
      const candidates = model.groupCandidates(state.inventory, topology, group?.name || "");
      const configuredLAN = group?.ports.find((port) => port.direction === "lan_facing")?.interface || "";
      const configuredWAN = group?.ports.find((port) => port.direction === "wan_facing")?.interface || "";
      const wanPort = configuredWAN || candidates[0]?.name || "";
      const lanPort = configuredLAN || candidates.find((item) => item.name !== wanPort)?.name || "";
      const options = (selected) => candidates.map((item) => `<option value="${item.name}" ${item.name === selected ? "selected" : ""}>${item.name} · ${item.link_state === "down" ? "链路断开" : "链路正常"}</option>`).join("");
      return `<form class="orchestrator-form pa-config-form" data-group-form data-original-name="${safeText(group?.name || "")}">
        <section class="pa-form-section"><h3>基本设置</h3><div class="pa-form-rows">
          <label data-required><span>组名称</span><input aria-label="组名称" data-group-name value="${safeText(group?.name || "")}" maxlength="63" ${group ? "readonly" : ""} required></label>
        </div></section>
        <section class="pa-form-section"><h3>接口方向</h3><div class="pa-form-rows">
          <label data-required><span>WAN 侧端口</span><select aria-label="WAN 侧端口" data-group-wan required>${options(wanPort)}</select></label>
          <div class="pa-direction-line"><span>流量方向</span><strong>WAN <b>→</b> 编排设备 <b>→</b> LAN</strong></div>
          <label data-required><span>LAN 侧端口</span><select aria-label="LAN 侧端口" data-group-lan required>${options(lanPort)}</select></label>
        </div></section>
      </form>`;
    }

    function openEditor(group, trigger) {
      modal.open({
        title: group ? `编辑 ${group.name}` : "新建编排组",
        html: groupForm(group),
        trigger,
        async onSubmit(body) {
          const form = body.querySelector("[data-group-form]");
          const name = form.querySelector("[data-group-name]").value.trim();
          const lanPort = form.querySelector("[data-group-lan]").value;
          const wanPort = form.querySelector("[data-group-wan]").value;
          if (!name || [...name].length > 63) {
            state.notice = { tone: "error", text: "组名称不能为空，且不能超过 63 个字符" };
            return false;
          }
          if (lanPort === wanPort) {
            state.notice = { tone: "error", text: "LAN 侧和 WAN 侧必须使用不同端口" };
            return false;
          }
          const expected = { name, ports: [{ interface: lanPort, direction: "lan_facing" }, { interface: wanPort, direction: "wan_facing" }] };
          const confirmed = model.clone(state.topology);
          state.busy = true;
          try {
            if (group) await client.updateGroup(group.name, expected);
            else await client.createGroup(expected);
            try {
              const readback = await client.topology();
              state.topology = readback.item;
              state.draft = model.clone(readback.item);
              state.checksum = readback.checksum;
              state.stale = false;
              state.notice = model.groupMatches(expected, readback.item)
                ? { tone: "success", text: `已从 API 回读确认 · ${readback.checksum}` }
                : { tone: "error", text: "保存响应与 API 回读不一致" };
            } catch {
              state.topology = confirmed;
              state.draft = model.clone(confirmed);
              state.stale = true;
              state.notice = { tone: "stale", text: "当前显示的是陈旧状态；保存已响应，但 API 回读失败" };
            }
          } catch (error) {
            state.notice = { tone: "error", text: model.errorMessage(error) };
          } finally {
            state.busy = false;
            render();
          }
          return true;
        },
      });
    }

    function confirmDelete(group, trigger) {
      modal.open({
        title: `删除 ${group.name}`,
        html: `<p class="orchestrator-confirm">确认删除编排组 <strong>${safeText(group.name)}</strong>？端口会在 API 回读确认后释放。</p>`,
        submitLabel: "确认删除",
        trigger,
        async onSubmit() {
          const confirmed = model.clone(state.topology);
          state.busy = true;
          try {
            await client.deleteGroup(group.name);
            try {
              const readback = await client.topology();
              state.topology = readback.item;
              state.draft = model.clone(readback.item);
              state.checksum = readback.checksum;
              state.stale = false;
              state.notice = readback.item.orchestration_groups.some((item) => item.name === group.name)
                ? { tone: "error", text: "删除响应与 API 回读不一致" }
                : { tone: "success", text: `删除已从 API 回读确认 · ${readback.checksum}` };
            } catch {
              state.topology = confirmed;
              state.draft = model.clone(confirmed);
              state.stale = true;
              state.notice = { tone: "stale", text: "当前显示的是陈旧状态；删除已响应，但 API 回读失败" };
            }
          } catch (error) {
            state.notice = { tone: "error", text: model.errorMessage(error) };
          } finally {
            state.busy = false;
            render();
          }
          return true;
        },
      });
    }

    function renderPage() {
      const ready = model.configured(state.topology);
      const groups = state.topology?.orchestration_groups || [];
      const rows = groups.map((group) => {
        const lan = group.ports.find((port) => port.direction === "lan_facing");
        const wan = group.ports.find((port) => port.direction === "wan_facing");
        const health = (name) => state.inventory.find((item) => item.name === name)?.link_state === "down" ? "链路断开" : "链路正常";
        return `<tr><td data-label="组名称"><strong>${safeText(group.name)}</strong></td><td data-label="WAN 侧端口">${safeText(wan?.interface || "未设置")}<small>${health(wan?.interface)}</small></td><td data-label="默认正向"><span class="orchestrator-direction" aria-hidden="true">→</span><span class="sr-only">流向 LAN</span></td><td data-label="LAN 侧端口">${safeText(lan?.interface || "未设置")}<small>${health(lan?.interface)}</small></td><td data-label="操作"><button class="link-btn" type="button" data-edit-group="${safeText(group.name)}" aria-label="编辑 ${safeText(group.name)}">编辑</button><button class="link-btn is-danger" type="button" data-delete-group="${safeText(group.name)}" aria-label="删除 ${safeText(group.name)}">删除</button></td></tr>`;
      }).join("");
      return `<section class="page-body list-page orchestrator-settings">
        ${state.renderNotice()}
        <div class="orchestrator-toolbar pa-list-toolbar"><div><strong>编排组</strong><span>共 ${groups.length} 组</span></div><button class="primary" type="button" data-create-group ${!ready || state.busy ? "disabled" : ""}>新增</button></div>
        ${ready ? "" : '<div class="orchestrator-gate" role="status"><strong>请先完成网卡设置</strong><span>配置并回读确认 LAN/WAN 后才能创建编排组。</span></div>'}
        <div class="orchestrator-table-wrap"><table class="data-table orchestrator-table orchestrator-group-table"><thead><tr><th>组名称</th><th>WAN 侧端口</th><th>默认正向</th><th>LAN 侧端口</th><th>操作</th></tr></thead><tbody>${rows || '<tr><td colspan="5" class="orchestrator-empty">暂无编排组</td></tr>'}</tbody></table></div>
      </section>`;
    }

    async function handle(target) {
      const create = target.closest("[data-create-group]");
      if (create) return openEditor(null, create);
      const edit = target.closest("[data-edit-group]");
      if (edit) return openEditor(state.topology.orchestration_groups.find((group) => group.name === edit.dataset.editGroup), edit);
      const remove = target.closest("[data-delete-group]");
      if (remove) confirmDelete(state.topology.orchestration_groups.find((group) => group.name === remove.dataset.deleteGroup), remove);
    }

    return Object.freeze({ handle, renderPage });
  }

  window.LyRouteOrchestratorGroups = Object.freeze({ createGroupController });
}());
(function initializeOrchestratorPolicy() {
  function createPolicyController({ client, modal, safeText, state, render }) {
    function currentPolicy() {
      return state.policy?.item || { schema_version: 1, ip_objects: [], policy_groups: [], default: { kind: "direct" } };
    }

    function splitValues(value) {
      return String(value || "").split(/[\n,\s]+/).map((item) => item.trim()).filter(Boolean);
    }

    function conditionRow(prefix, type = "literal", value = "") {
      const groups = (state.ipGroups?.items || []).map((group) => `<option value="${safeText(group.id)}" ${type === "ip_group" && group.id === value ? "selected" : ""}>${safeText(group.name || group.id)}</option>`).join("");
      const placeholder = type === "range" ? "192.168.1.10-192.168.1.20" : type === "ipv6" ? "2001:db8::/64" : "192.168.1.0/24";
      return `<div class="pa-condition-row" data-policy-condition-row><select data-policy-condition-type aria-label="地址类型"><option value="literal" ${type === "literal" ? "selected" : ""}>IPv4 地址 / 网段</option><option value="ipv6" ${type === "ipv6" ? "selected" : ""}>IPv6 地址 / 网段</option><option value="range" ${type === "range" ? "selected" : ""}>IPv4 起止范围</option><option value="ip_group" ${type === "ip_group" ? "selected" : ""}>IP 组</option></select><input data-policy-condition-value value="${safeText(type === "ip_group" ? "" : value)}" placeholder="${placeholder}"><select data-policy-condition-group><option value="">请选择 IP 组</option>${groups}</select><button type="button" data-policy-condition-delete aria-label="删除条件">×</button></div>`;
    }

    function conditionSummary(prefix, label) {
      return `<textarea readonly rows="3" data-policy-condition-summary="${prefix}" data-condition-label="${label}" data-condition-values="[]" aria-label="编辑${label}" placeholder="任意"></textarea>`;
    }

    function policyConditionValues(rule, key) {
      const references = rule?.match?.[key] || [];
      const policy = currentPolicy();
      return references.flatMap((reference) => {
        if (reference === "any") return [];
        if ((state.ipGroups?.items || []).some((item) => item.id === reference)) return [reference];
        const object = (policy.ip_objects || []).find((item) => item.id === reference);
        return object?.prefixes?.length ? object.prefixes : [reference];
      });
    }

    function policyForm(selectedGroup = "", rule = null) {
      const groups = state.topology?.orchestration_groups || [];
      const selectedTarget = rule?.action?.group || "";
      const options = groups.map((group) => `<option value="${safeText(group.name)}" ${group.name === selectedTarget ? "selected" : ""}>${safeText(group.name)}</option>`).join("");
      const sourceValues = policyConditionValues(rule, "sources");
      const destinationValues = policyConditionValues(rule, "destinations");
      return `<form class="orchestrator-form pa-config-form" data-policy-form>
        <input type="hidden" data-policy-group value="${safeText(selectedGroup)}">
        <input type="hidden" data-policy-rule value="${safeText(rule?.id || "")}">
        <section class="pa-form-section"><h3>基本设置</h3><div class="pa-form-rows">
          <label data-required><span>策略序号</span><input data-policy-sequence type="number" min="1" step="1" value="${safeText(rule?.sequence || 10)}" required></label>
        </div></section>
        <section class="pa-form-section"><h3>匹配条件</h3><div class="pa-form-rows"><label><span>源 / 目的地址</span><span class="pa-address-summary-pair"><textarea readonly rows="3" data-policy-condition-summary="source" data-condition-label="源地址" data-condition-values='${safeText(JSON.stringify(sourceValues))}' aria-label="编辑源地址" placeholder="任意">${safeText(sourceValues.join("\n"))}</textarea><em>/</em><textarea readonly rows="3" data-policy-condition-summary="destination" data-condition-label="目的地址" data-condition-values='${safeText(JSON.stringify(destinationValues))}' aria-label="编辑目的地址" placeholder="任意">${safeText(destinationValues.join("\n"))}</textarea></span></label>
          <label><span>源 / 目的端口</span><span class="pa-port-pair"><input data-policy-source-port value="${safeText((rule?.match?.source_ports || []).join(","))}" placeholder="0"><em>/</em><input data-policy-destination-port value="${safeText((rule?.match?.dest_ports || []).join(","))}" placeholder="0"></span></label>
          <label><span>协议</span><select data-policy-protocol>${["any", "tcp", "udp", "icmp", "icmpv6"].map((protocol) => `<option value="${protocol}" ${protocol === (rule?.match?.protocol || "any") ? "selected" : ""}>${protocol === "any" ? "Any" : protocol.toUpperCase()}</option>`).join("")}</select></label>
        </div></section>
        <section class="pa-form-section"><h3>执行动作</h3><div class="pa-form-rows">
          <label data-required><span>流量路径</span><select data-policy-target required><option value="">请选择编排组</option>${options}</select></label>
        </div></section>
      </form>`;
    }

    function groupForm() {
      const nextPosition = ((currentPolicy().policy_groups || []).length + 1) * 10;
      return `<form class="orchestrator-form pa-config-form" data-policy-group-form>
        <section class="pa-form-section"><h3>策略组设置</h3><div class="pa-form-rows">
          <label data-required><span>策略组名称</span><input data-policy-group-name required maxlength="63"></label>
        </div></section>
      </form>`;
    }

    function addressSelector(form, prefix, next, ruleID) {
      const summary = form.querySelector(`[data-policy-condition-summary="${prefix}"]`);
      const values = JSON.parse(summary.dataset.conditionValues || "[]");
      if (!values.length) return ["any"];
      const refs = [];
      const literals = [];
      values.forEach((value) => {
        const groupMatch = (state.ipGroups?.items || []).some((item) => item.id === value);
        if (groupMatch) {
          const group = (state.ipGroups?.items || []).find((item) => item.id === value);
          const entries = group?.entries || group?.members || [];
          if (!group || !entries.length) throw new Error(`${prefix === "source" ? "源" : "目的"} IP 组无有效成员`);
          next.ip_objects = (next.ip_objects || []).filter((item) => item.id !== value);
          next.ip_objects.push({ id: value, prefixes: entries });
          refs.push(value);
        } else literals.push(value);
      });
      if (literals.length) {
        const id = `${prefix}-${ruleID}`;
        next.ip_objects = (next.ip_objects || []).filter((item) => item.id !== id);
        next.ip_objects.push({ id, prefixes: literals });
        refs.unshift(id);
      }
      return refs;
    }

    function openConditionDialog(summary) {
      document.querySelector("[data-pa-condition-layer]")?.remove();
      const values = JSON.parse(summary.dataset.conditionValues || "[]");
      const groups = new Set((state.ipGroups?.items || []).map((group) => group.id));
      const rowsHTML = values.map((value) => conditionRow(summary.dataset.policyConditionSummary, groups.has(value) ? "ip_group" : value.includes("-") ? "range" : value.includes(":") ? "ipv6" : "literal", value)).join("");
      const layer = document.createElement("div");
      layer.className = "pa-condition-layer";
      layer.dataset.paConditionLayer = "";
      layer.innerHTML = `<section class="pa-condition-dialog" role="dialog" aria-modal="true"><header><h2>编辑${safeText(summary.dataset.conditionLabel)}</h2><button type="button" data-condition-close aria-label="关闭">×</button></header><div class="pa-condition-toolbar"><strong>类型</strong><strong>IP / 群组</strong><span><button type="button" data-condition-add>＋ 添加</button><button type="button" data-condition-clear>清空</button></span></div><div class="pa-condition-body" data-condition-rows>${rowsHTML}</div><footer><button type="button" class="primary" data-condition-confirm>确定</button><button type="button" data-condition-cancel>取消</button></footer></section>`;
      document.body.appendChild(layer);
      const rows = layer.querySelector("[data-condition-rows]");
      const wireRow = (row) => {
        const type = row.querySelector("[data-policy-condition-type]");
        const input = row.querySelector("[data-policy-condition-value]");
        const group = row.querySelector("[data-policy-condition-group]");
        const sync = () => { const isGroup = type.value === "ip_group"; input.hidden = isGroup; input.disabled = isGroup; group.hidden = !isGroup; group.disabled = !isGroup; input.placeholder = type.value === "range" ? "192.168.1.10-192.168.1.20" : type.value === "ipv6" ? "2001:db8::/64" : "192.168.1.0/24"; };
        type.addEventListener("change", sync);
        row.querySelector("[data-policy-condition-delete]").addEventListener("click", () => row.remove());
        sync();
      };
      Array.from(rows.children).forEach(wireRow);
      const close = () => { layer.remove(); summary.focus(); };
      layer.querySelector("[data-condition-add]").addEventListener("click", () => { rows.insertAdjacentHTML("beforeend", conditionRow(summary.dataset.policyConditionSummary)); wireRow(rows.lastElementChild); rows.lastElementChild.querySelector("input").focus(); });
      layer.querySelector("[data-condition-clear]").addEventListener("click", () => { rows.innerHTML = ""; });
      layer.querySelector("[data-condition-close]").addEventListener("click", close);
      layer.querySelector("[data-condition-cancel]").addEventListener("click", close);
      layer.addEventListener("click", (event) => { if (event.target === layer) close(); });
      layer.querySelector("[data-condition-confirm]").addEventListener("click", () => { const items = Array.from(rows.children).map((row) => row.querySelector("[data-policy-condition-type]").value === "ip_group" ? row.querySelector("[data-policy-condition-group]").value.trim() : row.querySelector("[data-policy-condition-value]").value.trim()); if (items.some((value) => !value)) return; summary.dataset.conditionValues = JSON.stringify(items); summary.value = items.join("\n"); summary.placeholder = items.length ? "" : "任意"; close(); });
    }

    function syncPolicyForm(body) {
      const form = body.querySelector("[data-policy-form]");
      if (!form) return;
      form.querySelectorAll("[data-policy-condition-summary]").forEach((summary) => summary.addEventListener("click", () => openConditionDialog(summary)));
    }

    function openCreate(trigger, selectedGroup = "", existingRule = null) {
      modal.open({
        title: existingRule ? `编辑策略明细 · ${selectedGroup}` : `新增策略明细 · ${selectedGroup}`,
        html: policyForm(selectedGroup, existingRule),
        trigger,
        async onSubmit(body) {
          state.busy = true;
          try {
            const form = body.querySelector("[data-policy-form]");
            const group = form.querySelector("[data-policy-group]").value.trim();
            const rule = form.querySelector("[data-policy-rule]").value || `rule-${Date.now()}`;
            if (!group) throw new Error("当前策略组不能为空");
            const next = JSON.parse(JSON.stringify(currentPolicy()));
            next.schema_version = 1;
            delete next.schema;
            next.ip_objects ||= [];
            let policyGroup = next.policy_groups.find((item) => item.id === group);
            if (!policyGroup) {
              policyGroup = { id: group, position: ((next.policy_groups || []).length + 1) * 10, rules: [] };
              next.policy_groups.push(policyGroup);
            }
            const action = { kind: "via", group: form.querySelector("[data-policy-target]").value };
            if (!action.group) throw new Error("请选择目标编排组");
            const nextRule = {
              id: rule,
              sequence: Number(form.querySelector("[data-policy-sequence]").value),
              match: { sources: addressSelector(form, "source", next, rule), destinations: addressSelector(form, "destination", next, rule), source_ports: splitValues(form.querySelector("[data-policy-source-port]").value), dest_ports: splitValues(form.querySelector("[data-policy-destination-port]").value), protocol: form.querySelector("[data-policy-protocol]").value },
              action,
            };
            const existingIndex = policyGroup.rules.findIndex((item) => item.id === rule);
            if (existingIndex >= 0) policyGroup.rules[existingIndex] = nextRule;
            else policyGroup.rules.push(nextRule);
            state.policy = await client.savePolicy(next);
            state.notice = { tone: "success", text: existingRule ? "策略明细已修改并完成 API 回读" : "策略明细已新增并完成 API 回读" };
            return true;
          } catch (error) {
            state.notice = { tone: "error", text: error.message || "编排策略保存失败" };
            return false;
          } finally {
            state.busy = false;
            render();
          }
        },
      });
      syncPolicyForm(document.getElementById("modalBody"));
    }

    function openCreateGroup(trigger) {
      modal.open({
        title: "新增编排策略组",
        html: groupForm(),
        submitLabel: "创建策略组",
        trigger,
        async onSubmit(body) {
          const form = body.querySelector("[data-policy-group-form]");
          const id = form.querySelector("[data-policy-group-name]").value.trim();
          const position = ((currentPolicy().policy_groups || []).length + 1) * 10;
          if (!id || [...id].length > 63) {
            state.notice = { tone: "error", text: "策略组名称不能为空，且不能超过 63 个字符" };
            render();
            return false;
          }
          const next = JSON.parse(JSON.stringify(currentPolicy()));
          if ((next.policy_groups || []).some((group) => group.id === id)) {
            state.notice = { tone: "error", text: "策略组名称已存在" };
            render();
            return false;
          }
          next.policy_groups ||= [];
          next.policy_groups.push({ id, position, rules: [] });
          state.busy = true;
          try {
            state.policy = await client.savePolicy(next);
            state.notice = { tone: "success", text: "策略组已保存并完成 API 回读" };
            return true;
          } catch (error) {
            state.notice = { tone: "error", text: error.message || "策略组保存失败" };
            return false;
          } finally {
            state.busy = false;
            render();
          }
        },
      });
    }

    function ruleRow(group, rule) {
      const action = rule.action || {};
      const actionLabel = action.group || group.id;
      return `<tr><td><strong>${safeText(rule.sequence)}</strong></td><td>${safeText((rule.match?.sources || ["any"]).join(", "))}</td><td>${safeText((rule.match?.destinations || ["any"]).join(", "))}</td><td>${safeText(rule.match?.protocol || "any")}</td><td>${safeText(actionLabel)}</td><td><button class="link-btn" type="button" data-edit-policy-rule="${safeText(rule.id)}" data-policy-rule-group="${safeText(group.id)}">编辑</button><button class="link-btn is-danger" type="button" data-delete-policy-rule="${safeText(rule.id)}" data-policy-rule-group="${safeText(group.id)}">删除</button></td></tr>`;
    }

    async function deleteRule(groupID, ruleID) {
      const next = JSON.parse(JSON.stringify(currentPolicy()));
      const group = (next.policy_groups || []).find((item) => item.id === groupID);
      if (!group) return;
      group.rules = (group.rules || []).filter((item) => item.id !== ruleID);
      next.ip_objects = (next.ip_objects || []).filter((item) => item.id !== `source-${ruleID}` && item.id !== `destination-${ruleID}`);
      state.busy = true;
      try {
        state.policy = await client.savePolicy(next);
        state.notice = { tone: "success", text: "策略明细已删除并完成 API 回读" };
      } catch (error) {
        state.notice = { tone: "error", text: error.message || "策略明细删除失败" };
      } finally {
        state.busy = false;
        render();
      }
    }

    function renderPage() {
      const policy = currentPolicy();
      const groups = [...(policy.policy_groups || [])].sort((left, right) => left.position - right.position);
      const groupCards = groups.map((group, groupIndex) => {
        const rules = (group.rules || []).slice().sort((left, right) => left.sequence - right.sequence);
        const collapsed = state.collapsedPolicyGroups.has(group.id);
        const body = collapsed ? "" : `<div class="policy-group-body"><div class="orchestrator-table-wrap"><table class="data-table orchestrator-table policy-rule-table"><thead><tr><th>序号</th><th>源 IP</th><th>目的 IP</th><th>协议</th><th>流量路径</th><th class="policy-operation-head">操作 <button class="primary policy-table-add-detail" type="button" data-create-policy-in-group="${safeText(group.id)}">新增明细</button></th></tr></thead><tbody>${rules.length ? rules.map((rule) => ruleRow(group, rule)).join("") : '<tr><td colspan="6" class="orchestrator-empty">暂无策略明细</td></tr>'}</tbody></table></div></div>`;
        return `<article class="policy-group-card ${collapsed ? "is-collapsed" : ""}" data-policy-group-card="${safeText(group.id)}"><header class="policy-group-bar" draggable="true" data-policy-group-drag="${safeText(group.id)}"><strong class="policy-group-name">${safeText(group.id)}</strong><button class="policy-group-toggle" type="button" data-toggle-policy-group="${safeText(group.id)}" aria-expanded="${collapsed ? "false" : "true"}" aria-label="${collapsed ? "展开" : "折叠"} ${safeText(group.id)}">${collapsed ? "v" : "^"}</button></header>${body}</article>`;
      }).join("");
      return `<section class="page-body list-page"><section class="list-content policy-workbench">${modelConfigured() ? `<div class="policy-group-list" data-policy-group-list>${groupCards || '<p class="orchestrator-empty">暂无策略组</p>'}</div>` : '<div class="orchestrator-gate" role="status"><strong>请先完成网卡设置</strong><span>配置 LAN/WAN 后可创建编排策略。</span></div>'}</section></section>`;
    }

    function modelConfigured() {
      return Boolean(state.topology?.interfaces?.some((item) => item.role === "lan") && state.topology?.interfaces?.some((item) => item.role === "wan"));
    }

    async function saveGroupOrder(groups) {
      const next = JSON.parse(JSON.stringify(currentPolicy()));
      next.policy_groups = groups;
      state.busy = true;
      try { state.policy = await client.savePolicy(next); state.notice = { tone: "success", text: "策略组顺序已保存并完成回读" }; }
      catch (error) { state.notice = { tone: "error", text: error.message || "策略组排序失败" }; }
      finally { state.busy = false; render(); }
    }

    async function moveGroup(name, direction) {
      const next = JSON.parse(JSON.stringify(currentPolicy()));
      const groups = [...next.policy_groups].sort((left, right) => left.position - right.position);
      const index = groups.findIndex((group) => group.id === name);
      const other = index + direction;
      if (index < 0 || other < 0 || other >= groups.length) return;
      [groups[index], groups[other]] = [groups[other], groups[index]];
      groups.forEach((group, order) => { group.position = (order + 1) * 10; });
      return saveGroupOrder(groups);
    }

    async function moveGroupBefore(sourceID, targetID) {
      if (!sourceID || !targetID || sourceID === targetID) return;
      const groups = [...(currentPolicy().policy_groups || [])].sort((left, right) => left.position - right.position);
      const sourceIndex = groups.findIndex((group) => group.id === sourceID);
      const targetIndex = groups.findIndex((group) => group.id === targetID);
      if (sourceIndex < 0 || targetIndex < 0) return;
      const [source] = groups.splice(sourceIndex, 1);
      groups.splice(groups.findIndex((group) => group.id === targetID), 0, source);
      groups.forEach((group, order) => { group.position = (order + 1) * 10; });
      return saveGroupOrder(groups);
    }

    async function handle(target) {
      if (target.closest("[data-create-policy-group]")) return openCreateGroup(target.closest("[data-create-policy-group]"));
      const toggle = target.closest("[data-toggle-policy-group]");
      if (toggle) {
        const id = toggle.dataset.togglePolicyGroup;
        if (state.collapsedPolicyGroups.has(id)) state.collapsedPolicyGroups.delete(id);
        else state.collapsedPolicyGroups.add(id);
        return render();
      }
      const inGroup = target.closest("[data-create-policy-in-group]");
      if (inGroup) return openCreate(inGroup, inGroup.dataset.createPolicyInGroup);
      const editRule = target.closest("[data-edit-policy-rule]");
      if (editRule) {
        const groupID = editRule.dataset.policyRuleGroup;
        const group = (currentPolicy().policy_groups || []).find((item) => item.id === groupID);
        const rule = (group?.rules || []).find((item) => item.id === editRule.dataset.editPolicyRule);
        if (group && rule) return openCreate(editRule, groupID, rule);
      }
      const deletePolicyRule = target.closest("[data-delete-policy-rule]");
      if (deletePolicyRule) return deleteRule(deletePolicyRule.dataset.policyRuleGroup, deletePolicyRule.dataset.deletePolicyRule);
      const up = target.closest("[data-policy-up]");
      if (up) return moveGroup(up.dataset.policyUp, -1);
      const down = target.closest("[data-policy-down]");
      if (down) return moveGroup(down.dataset.policyDown, 1);
    }

    return Object.freeze({ handle, renderPage, moveGroupBefore });
  }

  window.LyRouteOrchestratorPolicy = Object.freeze({ createPolicyController });
}());
window.LY_ROUTE_PRODUCT_ENTRYPOINT = "orchestrator";

(function initializeOrchestratorEntrypoint() {
  const { createProductShell, safeText } = window.LyRouteShell;
  const { createClient, OrchestratorAPIError } = window.LyRouteOrchestratorAPI;
  const model = window.LyRouteOrchestratorModel;
  const modal = window.LyRouteOrchestratorModal;
  const client = createClient();
  const sections = [
    { id: "overview", no: "01", title: "系统概况", pages: [["dashboard/overview", "系统概况", "dashboard"], ["telemetry/flow-summary", "流量概况", "table"], ["telemetry/online-users", "在线用户", "table"], ["telemetry/top-connections", "Top连接", "table"]] },
    { id: "operations", no: "02", title: "网络与编排", pages: [["orchestrator/nic-settings", "网卡设置", "settings"], ["orchestrator/group-settings", "编排设置", "settings"], ["orchestrator/policy", "流量编排", "tabs"], ["flow-control/traffic", "流量控制", "table"], ["security/policies", "安全控制", "table"], ["object/ip", "IP管理", "table"]] },
    { id: "system", no: "03", title: "系统维护", pages: [["system/users", "系统用户管理", "table"], ["system/config", "配置管理", "settings"]] },
  ];
  const elements = {
    loginScreen: document.getElementById("loginScreen"),
    loginForm: document.getElementById("loginForm"),
    appShell: document.getElementById("appShell"),
    logoutButton: document.getElementById("logoutButton"),
    loginHint: document.getElementById("loginHint"),
    workspace: document.getElementById("workspace"),
  };
  const state = {
    product: null,
    health: null,
    capabilities: null,
    inventory: [],
    managementNetwork: null,
    ipGroups: { items: [] },
    securityACLs: { items: [] },
    users: { items: [] },
    runtimeStatus: null,
    configExport: null,
    snapshots: { items: [] },
    trafficControl: null,
    topology: null,
    telemetry: {},
    policy: null,
    draft: null,
    checksum: "",
    notice: null,
    error: "",
    stale: false,
    busy: false,
    collapsedPolicyGroups: new Set(),
    renderNotice() {
      return "";
    },
  };
  let shell;
  let activePage = "dashboard/overview";
  function updateTablePager(pager, page) {
    const wrap = pager.previousElementSibling;
    const table = wrap?.querySelector("table");
    if (!table) return;
    const rows = [...(table.tBodies[0]?.rows || [])];
    const pageSize = 10;
    const totalPages = Math.max(1, Math.ceil(rows.length / pageSize));
    const current = Math.min(Math.max(1, Number(page) || 1), totalPages);
    rows.forEach((row, index) => { row.hidden = index < (current - 1) * pageSize || index >= current * pageSize; });
    pager.dataset.page = String(current);
    pager.querySelector("[data-pager-page]").textContent = String(current);
    pager.querySelector("[data-pager-prev]").disabled = current <= 1;
    pager.querySelector("[data-pager-next]").disabled = current >= totalPages;
  }

  function enhanceTablePagers() {
    document.querySelectorAll(".orchestrator-table-wrap").forEach((wrap) => {
      if (wrap.nextElementSibling?.matches(".table-pager")) return;
      const table = wrap.querySelector("table");
      if (!table) return;
      const body = table.tBodies[0];
      const columnCount = table.tHead?.rows[0]?.cells.length || 1;
      if (body) {
        while (body.rows.length < 10) {
          const row = document.createElement("tr");
          row.className = "telemetry-placeholder";
          const cell = document.createElement("td");
          cell.colSpan = columnCount;
          row.appendChild(cell);
          body.appendChild(row);
        }
      }
      const pager = document.createElement("div");
      pager.className = "table-pager";
      pager.innerHTML = '<button type="button" data-pager-prev>&#19978;&#19968;&#39029;</button><span data-pager-page>1</span><button type="button" data-pager-next>&#19979;&#19968;&#39029;</button>';
      wrap.after(pager);
      updateTablePager(pager, 1);
    });
  }

  document.addEventListener("lyroute:rendered", enhanceTablePagers);

  const render = () => { shell.render(); enhanceTablePagers(); };
  const nic = window.LyRouteOrchestratorNIC.createNICController({ client, modal, safeText, state, render });
  const groups = window.LyRouteOrchestratorGroups.createGroupController({ client, modal, safeText, state, render });
  const policy = window.LyRouteOrchestratorPolicy.createPolicyController({ client, modal, safeText, state, render });

  function capabilitiesText() {
    const names = (state.capabilities?.items || []).filter((item) => item.available !== false).map((item) => item.name);
    return names.join(" · ") || "未报告能力";
  }

  function formatMegabytes(value) {
    const bytes = Number(value) || 0;
    if (bytes === 0) return "0 M";
    const megabytes = bytes / 1024 / 1024;
    return `${megabytes >= 100 ? megabytes.toFixed(0) : megabytes.toFixed(2)} M`;
  }

  function formatTrafficRate(value) {
    if (value === undefined || value === null || value === "") return "-";
    const mbps = Number(value) * 8 / 1000000;
    return `${mbps >= 100 ? mbps.toFixed(0) : mbps.toFixed(2)} Mbps`;
  }

  function groupStateText(group) {
    if (group.bypass) return "旁路";
    if (group.state === "running" || group.state === "healthy") return "正常";
    if (group.state === "degraded") return "受限";
    return "等待回读";
  }

  const telemetryLabels = {
    ip: "IP地址",
    ip_address: "IP地址",
    address: "IP地址",
    mac: "MAC地址",
    mac_address: "MAC地址",
    interface: "接口",
    interface_id: "接口",
    protocol: "协议",
    source: "源地址",
    source_ip: "源IP",
    destination: "目的地址",
    destination_ip: "目的IP",
    source_port: "源端口",
    destination_port: "目的端口",
    bytes: "流量",
    packets: "数据包",
    state: "状态",
    duration: "持续时间",
    last_seen: "最近活动",
    policy: "编排策略",
    group: "编排组",
  };

  function telemetryLabel(key) {
    return telemetryLabels[key] || key;
  }

  function telemetryValue(key, value) {
    if (value === undefined || value === null || value === "") return "-";
    if (["bytes", "rx_bytes", "tx_bytes", "wan_to_lan_bytes", "lan_to_wan_bytes"].includes(key)) return formatMegabytes(value);
    if (typeof value === "boolean") return value ? "是" : "否";
    if (typeof value === "object") return JSON.stringify(value);
    return String(value);
  }

  function overview() {
    const status = state.health?.status || "未读取";
    const topology = state.topology || {};
    const interfaces = topology.interfaces || [];
    const groups = topology.orchestration_groups || [];
    const flowGroups = state.telemetry.flowSummary?.orchestration_groups || [];
    const bypassed = flowGroups.filter((group) => group.bypass || group.state === "bypass").length;
    const lan = interfaces.find((item) => item.role === "lan");
    const wan = interfaces.find((item) => item.role === "wan");
    const boundary = (item) => item?.bond?.name || item?.port || "-";
    const stat = (label, value, note, toneClass = "") => `<article class="orch-stat ${toneClass}"><span>${label}</span><strong>${safeText(value)}</strong><small>${safeText(note)}</small></article>`;
    const groupRows = groups.map((group) => {
      const live = flowGroups.find((item) => item.name === group.name) || {};
      const statusText = groupStateText(live);
      const statusClass = live.bypass ? "status warn" : live.state === "running" || live.state === "healthy" ? "status ok" : "status";
      return `<tr><td><strong>${safeText(group.name)}</strong></td><td>${safeText(group.ports?.map((port) => port.interface).join(" ↔ ") || "-")}</td><td><span class="${statusClass}">${safeText(statusText)}</span></td><td>${safeText(formatMegabytes(live.wan_to_lan?.bytes))}</td><td>${safeText(formatMegabytes(live.lan_to_wan?.bytes))}</td></tr>`;
    }).join("");
    const groupPlaceholders = Array.from({ length: Math.max(0, 10 - Math.max(1, groups.length)) }, () => '<tr class="telemetry-placeholder"><td colspan="5"></td></tr>').join("");
    return `<section class="page-body orch-overview-page"><section class="orch-stat-grid">${stat("逻辑 LAN", boundary(lan), "接口")}${stat("逻辑 WAN", boundary(wan), "接口")}${stat("编排组", groups.length, "已创建")}${stat("旁路线路", bypassed, "线路")}</section><section class="orch-overview-grid"><section class="orch-panel orch-status-panel"><header><div><h2>编排组运行状态</h2><p>按线路查看流量和连接状态。</p></div><button class="ghost-btn" type="button" data-open-page="telemetry/flow-summary">查看流量</button></header><div class="orchestrator-table-wrap"><table class="data-table orchestrator-table"><thead><tr><th>编排组</th><th>方向接口</th><th>状态</th><th>WAN→LAN 流量</th><th>LAN→WAN 流量</th></tr></thead><tbody>${groupRows || '<tr><td colspan="5" class="orchestrator-empty">暂无编排组</td></tr>'}${groupPlaceholders}</tbody></table></div></section><aside class="orch-panel orch-boundary-panel"><header><div><h2>流量路径</h2><p>当前路由器的默认转发关系。</p></div></header><div class="orch-path-flow"><span>WAN</span><b>→</b><span>编排组</span><b>→</b><span>LAN</span></div><dl><div><dt>入口接口</dt><dd>WAN</dd></div><div><dt>策略命中</dt><dd>按组和序号</dd></div><div><dt>未命中策略</dt><dd>转发到 LAN</dd></div><div><dt>线路故障</dt><dd>自动旁路</dd></div></dl></aside></section></section>`;
  }

  function placeholder(page) {
    return `<section class="page-body list-page"><section class="list-content"><div class="canvas-box">暂无数据</div></section></section>`;
  }

  function configPage() {
    const item = state.managementNetwork?.item || state.managementNetwork || {};
    const management = item.interface_id || model.managementName(state.inventory, state.topology) || "eth0";
    const mode = item.mode === "shared_lan" ? "shared_lan" : "exclusive";
    const snapshotRows = (state.snapshots?.items || []).map((snapshot) => `<tr><td data-label="快照">${safeText(snapshot.id)}</td><td data-label="来源">${safeText(snapshot.source_transaction_id || "manual")}</td><td data-label="创建时间">${safeText(snapshot.created_at || "-")}</td><td data-label="操作"><button class="icon-btn" type="button" data-restore-snapshot="${safeText(snapshot.id)}" aria-label="恢复 ${safeText(snapshot.id)}">恢复</button></td></tr>`).join("");
    return `<section class="page-body list-page"><section class="list-content">${state.renderNotice()}<section class="config-op management-network-op"><div class="config-op-copy"><strong>管理口设置</strong><span>共享 LAN 时管理地址与 LAN 共用网口，普通转发流量保持正常。</span><small>${safeText(item.new_url ? `应用后访问 ${item.new_url}` : "切换模式前请确认管理访问路径。")}</small></div><div class="management-network-form"><fieldset class="management-mode"><legend>管理模式</legend><label><input type="radio" name="orchestrator-management-mode" value="exclusive" ${mode === "exclusive" ? "checked" : ""}>独占管理口</label><label><input type="radio" name="orchestrator-management-mode" value="shared_lan" ${mode === "shared_lan" ? "checked" : ""}>与 LAN 共享</label></fieldset><label>管理接口<select data-management-interface aria-label="管理接口"><option value="${safeText(management)}">${safeText(management)}</option></select></label><label>IP/掩码<input data-management-cidr aria-label="IP/掩码" value="${safeText(item.cidr || item.ip_cidr || "192.168.88.1/24")}"></label><label>网关<input data-management-gateway aria-label="网关" value="${safeText(item.gateway || "")}"></label><label class="management-confirm"><input type="checkbox" data-management-confirm>确认修改管理访问地址</label><button class="primary" type="button" data-save-management ${state.busy ? "disabled" : ""}>保存管理口</button></div></section><section class="config-op"><div class="config-op-copy"><strong>配置备份与快照</strong><span>导入配置前自动检查内容，确认后替换当前设置。</span></div><div class="orchestrator-toolbar"><button class="ghost-btn" type="button" data-export-config>导出配置</button><button class="ghost-btn" type="button" data-import-config>导入配置</button><button class="primary" type="button" data-create-snapshot>创建快照</button></div></section><div class="orchestrator-table-wrap"><table class="data-table orchestrator-table"><thead><tr><th>快照</th><th>来源</th><th>创建时间</th><th>操作</th></tr></thead><tbody>${snapshotRows || '<tr><td colspan="4" class="orchestrator-empty">暂无配置快照</td></tr>'}</tbody></table></div></section></section>`;
  }

  function interfaceStatusPage() {
    const roleByPort = new Map((state.topology?.interfaces || []).flatMap((item) => item.port ? [[item.port, item.role]] : (item.bond?.members || []).map((port) => [port, item.role])));
    const rows = state.inventory.map((item) => `<tr><td data-label="接口"><strong>${safeText(item.name || item.id)}</strong></td><td data-label="角色">${safeText(roleByPort.get(item.name || item.id) || (item.management ? "management" : "未分配"))}</td><td data-label="链路">${safeText(item.link_state || "unknown")}</td><td data-label="速率">${safeText(item.speed_mbps ? `${item.speed_mbps} Mbps` : item.speed || "unknown")}</td><td data-label="驱动">${safeText(item.driver || item.active_path || "unknown")}</td></tr>`).join("");
    return `<section class="page-body list-page"><section class="list-content"><div class="orchestrator-toolbar"><div><strong>接口运行状态</strong><span>数据来自接口清单与当前拓扑回读</span></div></div><div class="orchestrator-table-wrap"><table class="data-table orchestrator-table"><thead><tr><th>接口</th><th>角色</th><th>链路</th><th>速率</th><th>驱动/路径</th></tr></thead><tbody>${rows || '<tr><td colspan="5" class="orchestrator-empty">暂无接口状态</td></tr>'}</tbody></table></div></section></section>`;
  }

  function runtimePage() {
    const runtime = state.runtimeStatus || {};
    const services = runtime.services || runtime.items || [];
    const rows = services.map((item) => `<tr><td data-label="组件"><strong>${safeText(item.label || item.name)}</strong></td><td data-label="状态">${safeText(item.state || item.status || "unknown")}</td><td data-label="原因">${safeText(item.reason || "-")}</td></tr>`).join("");
    return `<section class="page-body list-page"><section class="list-content">${state.renderNotice()}<div class="orchestrator-toolbar"><div><strong>运行态</strong><span>${safeText(runtime.runtime_state || runtime.status || "等待运行时回读")}</span></div></div><div class="orchestrator-table-wrap"><table class="data-table orchestrator-table"><thead><tr><th>组件</th><th>状态</th><th>原因</th></tr></thead><tbody>${rows || '<tr><td colspan="3" class="orchestrator-empty">暂无运行态组件</td></tr>'}</tbody></table></div></section></section>`;
  }

  function trafficControlIntent() {
    const items = state.trafficControl?.items || [];
    return items.find((item) => item.id === "orchestrator-rate-policies") || { id: "orchestrator-rate-policies", rules: [] };
  }

  function trafficControlPage() {
    const intent = trafficControlIntent();
    const rows = (intent.rules || []).map((rule, index) => {
      const match = rule.match || {};
      const policer = rule.actions?.find((action) => action.kind === "policer")?.policer || {};
      const rateMbps = Number(policer.rate_bps || 0) / 1000000;
      return `<tr><td data-label="序号">${index + 1}</td><td data-label="名称"><strong>${safeText(rule.name || rule.id)}</strong></td><td data-label="源IP">${safeText((match.sources || ["any"]).join(", "))}</td><td data-label="目的IP">${safeText((match.destinations || ["any"]).join(", "))}</td><td data-label="协议">${safeText((match.protocols || ["any"]).join(", "))}</td><td data-label="方向">${safeText(({ uplink: "上行", downlink: "下行", both: "双向" })[match.direction] || "双向")}</td><td data-label="限速">${safeText(rateMbps)} Mbps</td><td data-label="操作"><button class="icon-btn" type="button" data-edit-rate="${index}" aria-label="编辑 ${safeText(rule.name || rule.id)}">编辑</button><button class="icon-btn danger" type="button" data-delete-rate="${index}" aria-label="删除 ${safeText(rule.name || rule.id)}">删除</button></td></tr>`;
    }).join("");
    return `<section class="page-body list-page"><section class="list-content">${state.renderNotice()}<div class="orchestrator-toolbar"><div><strong>限速策略</strong><span>安全拒绝优先，限速在流量编排路径选择前生效</span></div><div><button class="primary" type="button" data-add-rate>新增限速规则</button></div></div><div class="orchestrator-table-wrap"><table class="data-table orchestrator-table"><thead><tr><th>序号</th><th>名称</th><th>源IP</th><th>目的IP</th><th>协议</th><th>方向</th><th>限速</th><th>操作</th></tr></thead><tbody>${rows || '<tr><td colspan="8" class="orchestrator-empty">暂无限速策略</td></tr>'}</tbody></table></div></section></section>`;
  }

  function ipGroupsPage() {
    const rows = (state.ipGroups?.items || []).map((item) => `<tr><td data-label="名称"><strong>${safeText(item.name || item.id)}</strong></td><td data-label="地址成员">${safeText((item.entries || item.members || []).join(", ") || "-")}</td><td data-label="成员数">${safeText((item.entries || item.members || []).length)}</td><td data-label="操作"><button class="icon-btn" type="button" data-edit-ip-group="${safeText(item.id)}" aria-label="编辑 ${safeText(item.name || item.id)}">编辑</button><button class="icon-btn danger" type="button" data-delete-ip-group="${safeText(item.id)}" aria-label="删除 ${safeText(item.name || item.id)}">删除</button></td></tr>`).join("");
    return `<section class="page-body list-page"><section class="list-content">${state.renderNotice()}<div class="orchestrator-toolbar"><div><strong>IP 地址组</strong><span>可用于流量编排、限速和安全策略</span></div><div><button class="ghost-btn" type="button" data-export-ip-groups>导出</button><button class="primary" type="button" data-import-ip-groups>新增IP</button></div></div><div class="orchestrator-table-wrap"><table class="data-table orchestrator-table"><thead><tr><th>名称</th><th>地址成员</th><th>成员数</th><th>操作</th></tr></thead><tbody>${rows || '<tr><td colspan="4" class="orchestrator-empty">暂无 IP 地址组</td></tr>'}</tbody></table></div></section></section>`;
  }

  function securityPage() {
    const rows = (state.securityACLs?.items || []).map((item) => { const match = item.match || {}; return `<tr><td data-label="名称"><strong>${safeText(item.name || item.id)}</strong></td><td data-label="方向">${safeText(match.direction || "any")}</td><td data-label="源IP">${safeText(match.src_ip || "any")}</td><td data-label="目的IP">${safeText(match.dst_ip || "any")}</td><td data-label="协议">${safeText(match.protocol || "any")}</td><td data-label="动作">${safeText(item.action || "deny")}</td><td data-label="状态">${item.enabled !== false ? "启用" : "停用"}</td><td data-label="操作"><button class="icon-btn" type="button" data-edit-acl="${safeText(item.id)}" aria-label="编辑 ${safeText(item.name || item.id)}">编辑</button><button class="icon-btn danger" type="button" data-delete-acl="${safeText(item.id)}" aria-label="删除 ${safeText(item.name || item.id)}">删除</button></td></tr>`; }).join("");
    return `<section class="page-body list-page"><section class="list-content">${state.renderNotice()}<div class="orchestrator-toolbar"><div><strong>访问控制策略</strong><span>拒绝策略优先于限速和流量编排</span></div><div><button class="primary" type="button" data-add-acl>新增安全策略</button></div></div><div class="orchestrator-table-wrap"><table class="data-table orchestrator-table"><thead><tr><th>名称</th><th>方向</th><th>源IP</th><th>目的IP</th><th>协议</th><th>动作</th><th>状态</th><th>操作</th></tr></thead><tbody>${rows || '<tr><td colspan="8" class="orchestrator-empty">暂无安全策略</td></tr>'}</tbody></table></div></section></section>`;
  }

  function usersPage() {
    const rows = (state.users?.items || []).map((item) => `<tr><td data-label="用户名"><strong>${safeText(item.username)}</strong></td><td data-label="角色">${item.role === "admin" ? "管理员" : "只读用户"}</td><td data-label="状态">${item.enabled === false ? "停用" : "启用"}</td><td data-label="操作"><button class="icon-btn" type="button" data-edit-user="${safeText(item.username)}" aria-label="编辑 ${safeText(item.username)}">编辑</button><button class="icon-btn danger" type="button" data-delete-user="${safeText(item.username)}" aria-label="删除 ${safeText(item.username)}">删除</button></td></tr>`).join("");
    return `<section class="page-body list-page"><section class="list-content">${state.renderNotice()}<div class="orchestrator-toolbar"><div><strong>系统用户</strong><span>管理员可修改配置，只读用户仅可查看</span></div><div><button class="primary" type="button" data-add-user>新增用户</button></div></div><div class="orchestrator-table-wrap"><table class="data-table orchestrator-table"><thead><tr><th>用户名</th><th>角色</th><th>状态</th><th>操作</th></tr></thead><tbody>${rows || '<tr><td colspan="4" class="orchestrator-empty">暂无系统用户</td></tr>'}</tbody></table></div></section></section>`;
  }

  function pageBody(page) {
    activePage = page.id;
    if (page.id === "dashboard/overview") return overview();
    if (page.id === "orchestrator/nic-settings") return nic.renderPage();
    if (page.id === "orchestrator/group-settings") return groups.renderPage();
    if (page.id === "telemetry/flow-summary") return flowSummaryPage();
    if (page.id === "telemetry/online-users") return telemetryPage("在线用户", state.telemetry.onlineUsers, "items");
    if (page.id === "telemetry/top-connections") return telemetryPage("Top连接", state.telemetry.topConnections, "items");
    if (page.id === "orchestrator/policy") return policy.renderPage();
    if (page.id === "security/policies") return securityPage();
    if (page.id === "flow-control/traffic") return trafficControlPage();
    if (page.id === "object/ip") return ipGroupsPage();
    if (page.id === "system/users") return usersPage();
    if (page.id === "monitor/interfaces") return interfaceStatusPage();
    if (page.id === "system/runtime") return runtimePage();
    if (page.id === "system/config") return configPage();
    return placeholder(page);
  }

  function policyPage() {
    const policy = state.policy?.item || { policy_groups: [], default: {} };
    const groups = [...(policy.policy_groups || [])].sort((left, right) => left.position - right.position);
    const rows = groups.flatMap((group) => (group.rules || []).slice().sort((left, right) => left.sequence - right.sequence).map((rule) => {
      const match = rule.match || {};
      const action = rule.action || {};
      const path = action.group || group.id;
      return `<tr><td data-label="策略组"><strong>${safeText(group.id)}</strong><small>组优先级 ${safeText(group.position)}</small></td><td data-label="序号">${safeText(rule.sequence)}</td><td data-label="源IP">${safeText((match.sources || []).join(", "))}</td><td data-label="目的IP">${safeText((match.destinations || []).join(", "))}</td><td data-label="协议">${safeText(match.protocol || "any")}</td><td data-label="流量路径">${safeText(path)}</td></tr>`;
    })).join("");
    return `<section class="page-body list-page"><section class="list-content"><div class="orchestrator-toolbar"><div><strong>流量编排策略</strong><span>组按位置优先，组内按序号优先；未命中默认转发到 LAN</span></div></div><div class="orchestrator-table-wrap"><table class="data-table orchestrator-table"><thead><tr><th>策略组</th><th>序号</th><th>源IP</th><th>目的IP</th><th>协议</th><th>流量路径</th></tr></thead><tbody>${rows || '<tr><td colspan="6" class="orchestrator-empty">暂无策略明细</td></tr>'}</tbody></table></div><div class="orchestrator-form-note">默认动作：${safeText(policy.default?.kind || "未配置")}</div></section></section>`;
  }

  function telemetryPage(title, payload, itemKey) {
    const items = Array.isArray(payload?.[itemKey]) ? payload[itemKey] : Array.isArray(payload?.items) ? payload.items : Array.isArray(payload) ? payload : [];
    const defaultColumns = title === "在线用户" ? ["ip_address", "mac_address", "interface", "last_seen"] : ["source_ip", "destination_ip", "protocol", "destination_port", "bytes"];
    const columns = items.length ? Object.keys(items[0]).filter((key) => key !== "runtime_state" && key !== "state") : defaultColumns;
    const rows = items.map((item) => `<tr>${columns.map((key) => `<td data-label="${safeText(telemetryLabel(key))}">${safeText(telemetryValue(key, item[key]))}</td>`).join("")}</tr>`).join("");
    const colspan = Math.max(columns.length, 1);
    const firstRow = rows || `<tr class="orchestrator-empty"><td colspan="${colspan}">暂无可用数据</td></tr>`;
    const placeholders = Array.from({ length: Math.max(0, 10 - Math.max(1, items.length)) }, () => `<tr class="telemetry-placeholder"><td colspan="${colspan}"></td></tr>`).join("");
    return `<section class="page-body list-page"><section class="list-content telemetry-list"><div class="orchestrator-toolbar"><div><strong>${safeText(title)}</strong><span>当前网络活动</span></div></div><div class="orchestrator-table-wrap telemetry-table-wrap"><table class="data-table orchestrator-table telemetry-table"><thead><tr>${columns.map((key) => `<th>${safeText(telemetryLabel(key))}</th>`).join("")}</tr></thead><tbody>${firstRow}${placeholders}</tbody></table></div></section></section>`;
  }

  function flowSummaryPage() {
    const payload = state.telemetry.flowSummary || {};
    const rows = (payload.orchestration_groups || []).map((group) => {
      const wanToLan = group.wan_to_lan || {};
      const lanToWan = group.lan_to_wan || {};
      const status = group.bypass ? "旁路" : groupStateText(group);
      return `<tr><td data-label="编排组"><strong>${safeText(group.name)}</strong></td><td data-label="状态">${safeText(status)}</td><td data-label="WAN→LAN 流量">${safeText(formatMegabytes(wanToLan.bytes))}</td><td data-label="WAN→LAN 速率">${safeText(formatTrafficRate(wanToLan.bytes_per_second))}</td><td data-label="LAN→WAN 流量">${safeText(formatMegabytes(lanToWan.bytes))}</td><td data-label="LAN→WAN 速率">${safeText(formatTrafficRate(lanToWan.bytes_per_second))}</td></tr>`;
    }).join("");
    const totals = payload.orchestration_groups?.reduce((sum, group) => sum + Number(group.wan_to_lan?.bytes || 0) + Number(group.lan_to_wan?.bytes || 0), 0) || 0;
    const placeholders = Array.from({ length: Math.max(0, 10 - Math.max(1, (payload.orchestration_groups || []).length)) }, () => '<tr class="telemetry-placeholder"><td colspan="6"></td></tr>').join("");
    return `<section class="page-body list-page"><section class="list-content telemetry-list"><div class="orchestrator-toolbar"><div><strong>流量概况</strong><span>按编排组查看上下行流量。</span></div></div><section class="orch-flow-summary"><div><span>编排组总数</span><strong>${safeText(payload.orchestration_groups?.length || 0)}</strong></div><div><span>累计流量</span><strong>${safeText(formatMegabytes(totals))}</strong></div><div><span>统计周期</span><strong>当前</strong></div></section><div class="orchestrator-table-wrap telemetry-table-wrap"><table class="data-table orchestrator-table telemetry-table"><thead><tr><th>编排组</th><th>状态</th><th>WAN→LAN 流量</th><th>WAN→LAN 速率</th><th>LAN→WAN 流量</th><th>LAN→WAN 速率</th></tr></thead><tbody>${rows || '<tr><td colspan="6" class="orchestrator-empty">暂无编排组流量</td></tr>'}${placeholders}</tbody></table></div></section></section>`;
  }

  shell = createProductShell({
    sections,
    initialPage: "dashboard/overview",
    renderPage(page) {
      const action = page.id === "orchestrator/policy" ? `<button class="primary page-title-action" type="button" data-create-policy-group ${state.busy || !state.topology?.interfaces?.some((item) => item.role === "lan") || !state.topology?.interfaces?.some((item) => item.role === "wan") ? "disabled" : ""}>新增策略组</button>` : "";
      return `<article class="page-card"><header class="page-title"><h1>${safeText(page.title)}</h1>${action}</header>${pageBody(page)}</article>`;
    },
  });
  const shellRender = shell.render.bind(shell);
  shell.render = () => { shellRender(); enhanceTablePagers(); };

  async function readTopology() {
    try {
      return await client.topology();
    } catch (error) {
      if (error instanceof OrchestratorAPIError && error.status === 404) return null;
      throw error;
    }
  }

  async function refresh(options = {}) {
    if (state.busy) return;
    state.busy = true;
    state.error = "";
    if (!options.silent) render();
    try {
      const optional = (promise, fallback) => promise.catch((error) => ({ ...fallback, degraded_reason: model.errorMessage(error) }));
      const [product, health, capabilities, inventoryPayload, managementNetwork, ipGroups, securityACLs, users, runtimeStatus, configExport, snapshots, trafficControl, topologyPayload, flowSummary, onlineUsers, topConnections, policy] = await Promise.all([
        client.product(), client.health(), client.capabilities(), client.inventory(), client.managementNetwork(), optional(client.ipGroups(), { items: [] }), optional(client.securityACLs(), { items: [] }), optional(client.users(), { items: [] }), optional(client.runtimeStatus(), {}), optional(client.configExport(), null), optional(client.snapshots(), { items: [] }), optional(client.trafficControl(), { items: [] }), readTopology(),
        optional(client.flowSummary(), { orchestration_groups: [] }),
        optional(client.onlineUsers(), { items: [] }),
        optional(client.topConnections(), { items: [] }),
        optional(client.policy(), { item: { schema_version: 1, ip_objects: [], policy_groups: [], default: { kind: "direct" } } }),
      ]);
      state.product = product;
      state.health = health;
      state.capabilities = capabilities;
      state.inventory = model.inventoryItems(inventoryPayload);
      state.managementNetwork = managementNetwork;
      state.ipGroups = ipGroups;
      state.securityACLs = securityACLs;
      state.users = users;
      state.runtimeStatus = runtimeStatus;
      state.configExport = configExport;
      state.snapshots = snapshots;
      state.trafficControl = trafficControl;
      state.topology = topologyPayload?.item || null;
      state.telemetry = { flowSummary, onlineUsers, topConnections };
      state.policy = policy;
      state.draft = state.topology ? model.clone(state.topology) : model.emptyTopology(model.managementName(state.inventory, null));
      state.draft.management_shared = (managementNetwork?.item || managementNetwork)?.mode === "shared_lan";
      state.checksum = topologyPayload?.checksum || "";
      state.stale = false;
      state.notice = state.topology ? null : { tone: "pending", text: "尚未初始化拓扑，请先配置 LAN 和 WAN" };
    } catch (error) {
      state.error = model.errorMessage(error);
      state.stale = Boolean(state.topology || state.inventory.length);
      state.notice = { tone: state.stale ? "stale" : "error", text: state.stale ? `当前显示的是陈旧状态 · ${state.error}` : state.error };
    } finally {
      state.busy = false;
      render();
    }
  }

  function showLogin(message = "使用授权账号登录管理控制台。") {
    stopAutoRefresh();
    elements.appShell.classList.add("is-hidden");
    elements.loginScreen.classList.remove("is-hidden");
    elements.loginHint.textContent = message;
  }

  function showShell() {
    elements.loginScreen.classList.add("is-hidden");
    elements.appShell.classList.remove("is-hidden");
    refresh();
    startAutoRefresh();
  }

  const autoRefreshIntervalMs = 5000;
  let autoRefreshTimer = null;

  function isWorkspaceEditing() {
    const active = document.activeElement;
    return active instanceof HTMLElement
      && elements.workspace.contains(active)
      && (active.matches("input, select, textarea") || active.isContentEditable);
  }

  function canAutoRefresh() {
    return !document.hidden
      && !elements.appShell.classList.contains("is-hidden")
      && document.getElementById("modal")?.classList.contains("is-hidden")
      && !isWorkspaceEditing()
      && !state.busy;
  }

  function autoRefresh() {
    if (canAutoRefresh()) refresh({ silent: true });
  }

  function startAutoRefresh() {
    stopAutoRefresh();
    autoRefreshTimer = window.setInterval(autoRefresh, autoRefreshIntervalMs);
  }

  function stopAutoRefresh() {
    if (autoRefreshTimer === null) return;
    window.clearInterval(autoRefreshTimer);
    autoRefreshTimer = null;
  }

  function rateConditionRow(type = "literal", value = "") {
    const groups = (state.ipGroups?.items || []).map((group) => `<option value="${safeText(group.id)}" ${type === "ip_group" && group.id === value ? "selected" : ""}>${safeText(group.name || group.id)}</option>`).join("");
    const placeholder = type === "range" ? "192.168.1.10-192.168.1.20" : type === "ipv6" ? "2001:db8::/64" : "192.168.1.0/24";
    return `<div class="pa-condition-row" data-rate-condition-row><select data-rate-condition-type><option value="literal" ${type === "literal" ? "selected" : ""}>IPv4 地址 / 网段</option><option value="ipv6" ${type === "ipv6" ? "selected" : ""}>IPv6 地址 / 网段</option><option value="range" ${type === "range" ? "selected" : ""}>IPv4 起止范围</option><option value="ip_group" ${type === "ip_group" ? "selected" : ""}>IP 组</option></select><input data-rate-condition-value value="${safeText(type === "ip_group" ? "" : value)}" placeholder="${placeholder}"><select data-rate-condition-group><option value="">请选择 IP 组</option>${groups}</select><button type="button" data-rate-condition-delete aria-label="删除条件">×</button></div>`;
  }

  function rateConditionEditor(name, label, values) {
    const entries = Array.isArray(values) && values.length ? values : ["any"];
    const normalized = entries.filter((value) => value !== "any");
    return `<textarea readonly rows="3" data-rate-condition-summary="${name}" data-condition-label="${label}" data-condition-values="${encodeURIComponent(JSON.stringify(normalized))}" aria-label="编辑${label}" placeholder="任意">${safeText(normalized.join("\n"))}</textarea>`;
  }

  function wireRateConditionEditors(body) {
    body.querySelectorAll("[data-rate-condition-summary]").forEach((summary) => summary.addEventListener("click", () => {
      document.querySelector("[data-pa-condition-layer]")?.remove();
      const values = JSON.parse(decodeURIComponent(summary.dataset.conditionValues || "%5B%5D"));
      const groups = new Set((state.ipGroups?.items || []).map((group) => group.id));
      const layer = document.createElement("div");
      layer.className = "pa-condition-layer";
      layer.dataset.paConditionLayer = "";
      layer.innerHTML = `<section class="pa-condition-dialog" role="dialog" aria-modal="true"><header><h2>编辑${safeText(summary.dataset.conditionLabel)}</h2><button type="button" data-condition-close aria-label="关闭">×</button></header><div class="pa-condition-toolbar"><strong>类型</strong><strong>IP / 群组</strong><span><button type="button" data-condition-add>＋ 添加</button><button type="button" data-condition-clear>清空</button></span></div><div class="pa-condition-body" data-condition-rows>${values.map((value) => rateConditionRow(groups.has(value) ? "ip_group" : value.includes("-") ? "range" : value.includes(":") ? "ipv6" : "literal", value)).join("")}</div><footer><button type="button" class="primary" data-condition-confirm>确定</button><button type="button" data-condition-cancel>取消</button></footer></section>`;
      document.body.appendChild(layer);
      const rows = layer.querySelector("[data-condition-rows]");
      const wireRow = (row) => {
        const type = row.querySelector("[data-rate-condition-type]");
        const input = row.querySelector("[data-rate-condition-value]");
        const group = row.querySelector("[data-rate-condition-group]");
        const sync = () => { const isGroup = type.value === "ip_group"; input.hidden = isGroup; input.disabled = isGroup; group.hidden = !isGroup; group.disabled = !isGroup; input.placeholder = type.value === "range" ? "192.168.1.10-192.168.1.20" : type.value === "ipv6" ? "2001:db8::/64" : "192.168.1.0/24"; };
        type.addEventListener("change", sync);
        row.querySelector("[data-rate-condition-delete]").addEventListener("click", () => row.remove());
        sync();
      };
      Array.from(rows.children).forEach(wireRow);
      const close = () => { layer.remove(); summary.focus(); };
      layer.querySelector("[data-condition-add]").addEventListener("click", () => { rows.insertAdjacentHTML("beforeend", rateConditionRow()); wireRow(rows.lastElementChild); rows.lastElementChild.querySelector("input").focus(); });
      layer.querySelector("[data-condition-clear]").addEventListener("click", () => { rows.innerHTML = ""; });
      layer.querySelector("[data-condition-close]").addEventListener("click", close);
      layer.querySelector("[data-condition-cancel]").addEventListener("click", close);
      layer.addEventListener("click", (event) => { if (event.target === layer) close(); });
      layer.querySelector("[data-condition-confirm]").addEventListener("click", () => { const items = Array.from(rows.children).map((row) => row.querySelector("[data-rate-condition-type]").value === "ip_group" ? row.querySelector("[data-rate-condition-group]").value.trim() : row.querySelector("[data-rate-condition-value]").value.trim()); if (items.some((value) => !value)) return; summary.dataset.conditionValues = encodeURIComponent(JSON.stringify(items)); summary.value = items.join("\n"); summary.placeholder = items.length ? "" : "任意"; close(); });
    }));
  }

  function rateConditionValues(body, name) {
    const values = JSON.parse(decodeURIComponent(body.querySelector(`[data-rate-condition-summary="${name}"]`).dataset.conditionValues || "%5B%5D"));
    return values.length ? values : ["any"];
  }

  function rateRuleForm(rule = {}) {
    const match = rule.match || {};
    const policer = rule.actions?.find((action) => action.kind === "policer")?.policer || {};
    const rate = Number(policer.rate_bps || 0) / 1000000 || "";
    return `<form class="orchestrator-rate-form pa-config-form" data-rate-form><section class="pa-form-section"><h3>基本设置</h3><div class="pa-form-rows"><label><span>名称</span><input required data-rate-name value="${safeText(rule.name || rule.id || "")}" placeholder="办公终端上行"></label></div></section><section class="pa-form-section"><h3>匹配条件</h3><div class="pa-form-rows"><label><span>源 / 目的地址</span><span class="pa-address-summary-pair">${rateConditionEditor("source", "源地址", match.sources)}<em>/</em>${rateConditionEditor("destination", "目的地址", match.destinations)}</span></label><label><span>源 / 目的端口</span><span class="pa-port-pair"><input data-rate-source-port value="${safeText((match.source_ports || [])[0] || '')}" placeholder="0"><em>/</em><input data-rate-destination-port value="${safeText((match.dest_ports || [])[0] || '')}" placeholder="0"></span></label><label><span>协议</span><select data-rate-protocol><option value="any" ${(match.protocols || []).includes("any") ? "selected" : ""}>Any</option><option value="tcp" ${(match.protocols || []).includes("tcp") ? "selected" : ""}>TCP</option><option value="udp" ${(match.protocols || []).includes("udp") ? "selected" : ""}>UDP</option></select></label></div></section><section class="pa-form-section"><h3>执行动作</h3><div class="pa-form-rows"><label><span>方向</span><select data-rate-direction><option value="both" ${match.direction === "both" || !match.direction ? "selected" : ""}>双向</option><option value="uplink" ${match.direction === "uplink" ? "selected" : ""}>上行</option><option value="downlink" ${match.direction === "downlink" ? "selected" : ""}>下行</option></select></label><label><span>限速</span><span class="pa-unit-input"><input required data-rate-mbps type="number" min="0.064" max="400000" step="0.001" value="${safeText(rate)}"><em>Mbps</em></span></label></div></section></form>`;
  }

  function splitAddressValues(value) {
    return String(value || "").split(/[\n,]+/).map((item) => item.trim()).filter(Boolean);
  }

  async function saveRateRule(index, body) {
    const intent = trafficControlIntent();
    const rules = [...(intent.rules || [])];
    const name = body.querySelector("[data-rate-name]").value.trim();
    const rateBPS = Math.round(Number(body.querySelector("[data-rate-mbps]").value) * 1000000);
    const previous = Number.isInteger(index) ? rules[index] : null;
    const id = previous?.id || `rate-${name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || Date.now()}`;
    const next = { id, name, granularity: "rule", match: { sources: rateConditionValues(body, "source"), destinations: rateConditionValues(body, "destination"), source_ports: splitAddressValues(body.querySelector("[data-rate-source-port]").value), dest_ports: splitAddressValues(body.querySelector("[data-rate-destination-port]").value), protocols: [body.querySelector("[data-rate-protocol]").value], direction: body.querySelector("[data-rate-direction]").value }, actions: [{ kind: "policer", policer: { rate_bps: rateBPS, burst_bps: Math.max(8000, Math.round(rateBPS / 10)) } }] };
    if (previous) rules[index] = next; else rules.push(next);
    const exists = (state.trafficControl?.items || []).some((item) => item.id === intent.id);
    try {
      state.busy = true;
      await client.saveTrafficControl({ id: intent.id, rules }, exists);
      await client.applyRuntime();
      state.trafficControl = await client.trafficControl();
      const confirmed = trafficControlIntent().rules || [];
      if (!confirmed.some((rule) => rule.id === id)) throw new Error("保存响应与 API 回读不一致");
      state.notice = { tone: "success", text: "限速策略已应用并从 API 回读确认" };
      return true;
    } catch (error) {
      state.notice = { tone: "error", text: model.errorMessage(error) };
      return false;
    } finally {
      state.busy = false;
      render();
    }
  }

  async function deleteRateRule(index) {
    const intent = trafficControlIntent();
    const rules = (intent.rules || []).filter((_, itemIndex) => itemIndex !== index);
    try {
      await client.saveTrafficControl({ id: intent.id, rules }, true);
      await client.applyRuntime();
      state.trafficControl = await client.trafficControl();
      state.notice = { tone: "success", text: "限速策略已删除并回读确认" };
    } catch (error) {
      state.notice = { tone: "error", text: model.errorMessage(error) };
    }
    render();
  }

  function ipGroupForm(item = {}) {
    return `<form class="orchestrator-rate-form" data-ip-group-form><label>名称<input required data-ip-group-name value="${safeText(item.name || "")}" placeholder="办公终端"></label><label>地址成员<textarea required data-ip-group-entries rows="7" placeholder="192.168.1.10&#10;192.168.2.0/24&#10;192.168.3.10-192.168.3.20">${safeText((item.entries || item.members || []).join("\n"))}</textarea></label></form>`;
  }

  function ipEntryForm() {
    const groups = (state.ipGroups?.items || []).filter((item) => item.kind === "ip");
    return `<form class="orchestrator-form pa-config-form" data-ip-entry-form><section class="pa-form-section"><h3>添加方式</h3><div class="pa-form-rows"><label><span>目标 IP 组</span><select data-ip-entry-mode><option value="append">追加到已有 IP 组</option><option value="create">创建新的 IP 组</option></select></label><label data-ip-entry-existing><span>选择 IP 组</span><select data-ip-entry-group>${groups.map((item) => `<option value="${safeText(item.id)}">${safeText(item.name || item.id)}</option>`).join("") || '<option value="">暂无 IP 组，请先创建</option>'}</select></label><label data-ip-entry-new hidden><span>IP 组名称</span><input data-ip-entry-name placeholder="例如：办公终端"></label></div></section><section class="pa-form-section"><h3>IP 内容</h3><div class="pa-form-rows"><label><span>每行一个 IP</span><textarea required data-ip-entry-values rows="7" placeholder="192.168.1.10&#10;192.168.1.0/24&#10;192.168.1.10-192.168.1.20"></textarea></label></div></section></form>`;
  }

  function wireIPEntryForm(body) {
    const form = body.querySelector("[data-ip-entry-form]");
    if (!form) return;
    const mode = form.querySelector("[data-ip-entry-mode]");
    const existing = form.querySelector("[data-ip-entry-existing]");
    const creating = form.querySelector("[data-ip-entry-new]");
    const sync = () => { const create = mode.value === "create"; existing.hidden = create; creating.hidden = !create; existing.style.display = create ? "none" : "grid"; creating.style.display = create ? "grid" : "none"; form.querySelector("[data-ip-entry-group]").disabled = create; form.querySelector("[data-ip-entry-name]").disabled = !create; form.querySelector("[data-ip-entry-name]").required = create; };
    mode.addEventListener("change", sync);
    sync();
  }

  function aclForm(item = {}) {
    const match = item.match || {};
    return `<form class="orchestrator-rate-form pa-config-form" data-acl-form><section class="pa-form-section"><h3>基本设置</h3><div class="pa-form-rows"><label><span>名称</span><input required data-acl-name value="${safeText(item.name || "")}"></label><label><span>方向</span><select data-acl-direction><option value="any" ${match.direction === "any" ? "selected" : ""}>任意</option><option value="wan_to_lan" ${match.direction === "wan_to_lan" ? "selected" : ""}>WAN 到 LAN</option><option value="lan_to_wan" ${match.direction === "lan_to_wan" ? "selected" : ""}>LAN 到 WAN</option></select></label></div></section><section class="pa-form-section"><h3>匹配条件</h3><div class="pa-form-rows"><label><span>源 / 目的地址</span><span class="pa-address-summary-pair">${rateConditionEditor("acl-source", "源地址", match.src_ip ? [match.src_ip] : [])}<em>/</em>${rateConditionEditor("acl-destination", "目的地址", match.dst_ip ? [match.dst_ip] : [])}</span></label><label><span>源 / 目的端口</span><span class="pa-port-pair"><input data-acl-source-port value="${safeText(match.src_port || '')}" placeholder="0"><em>/</em><input data-acl-destination-port value="${safeText(match.dst_port || '')}" placeholder="0"></span></label><label><span>协议</span><select data-acl-protocol><option value="any" ${match.protocol === "any" || !match.protocol ? "selected" : ""}>Any</option><option value="tcp" ${match.protocol === "tcp" ? "selected" : ""}>TCP</option><option value="udp" ${match.protocol === "udp" ? "selected" : ""}>UDP</option><option value="icmp" ${match.protocol === "icmp" ? "selected" : ""}>ICMP</option></select></label></div></section><section class="pa-form-section"><h3>执行动作</h3><div class="pa-form-rows"><label><span>动作</span><select data-acl-action><option value="deny" ${item.action === "deny" || !item.action ? "selected" : ""}>拒绝</option><option value="allow" ${item.action === "allow" ? "selected" : ""}>允许</option></select></label><label><span>状态</span><label class="pa-inline-check"><input type="checkbox" data-acl-enabled ${item.enabled === false ? "" : "checked"}>启用</label></label></div></section></form>`;
  }

  function userForm(item = {}) {
    const exists = Boolean(item.username);
    return `<form class="orchestrator-rate-form" data-user-form><label>用户名<input required data-user-name value="${safeText(item.username || "")}" ${exists ? "readonly" : ""}></label><label>角色<select data-user-role><option value="readonly" ${item.role === "readonly" || !item.role ? "selected" : ""}>只读用户</option><option value="admin" ${item.role === "admin" ? "selected" : ""}>管理员</option></select></label><label>${exists ? "新密码（留空则不修改）" : "密码"}<input ${exists ? "" : "required"} type="password" data-user-password autocomplete="new-password"></label></form>`;
  }

  function stableID(prefix, name) {
    return `${prefix}-${String(name || "").toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || Date.now()}`;
  }

  async function saveIPGroup(existing, body) {
    const name = body.querySelector("[data-ip-group-name]").value.trim();
    const payload = { id: existing?.id || stableID("ip", name), name, kind: "ip", entries: splitAddressValues(body.querySelector("[data-ip-group-entries]").value) };
    try {
      await client.saveIPGroup(payload, Boolean(existing));
      state.ipGroups = await client.ipGroups();
      if (!(state.ipGroups.items || []).some((item) => item.id === payload.id && item.kind === "ip")) throw new Error("保存响应与 API 回读不一致");
      state.notice = { tone: "success", text: "IP 组已保存并回读确认" };
      return true;
    } catch (error) { state.notice = { tone: "error", text: model.errorMessage(error) }; return false; }
    finally { render(); }
  }

  async function importIPGroups(body) {
    const entryForm = body.querySelector("[data-ip-entry-form]");
    if (entryForm) {
      const mode = entryForm.querySelector("[data-ip-entry-mode]").value;
      const entries = splitAddressValues(entryForm.querySelector("[data-ip-entry-values]").value);
      if (!entries.length) throw new Error("请至少填写一条 IP、网段或 IP 范围");
      const existing = mode === "append" ? (state.ipGroups.items || []).find((item) => item.id === entryForm.querySelector("[data-ip-entry-group]").value) : null;
      const name = mode === "create" ? entryForm.querySelector("[data-ip-entry-name]").value.trim() : existing?.name;
      if (!name) throw new Error(mode === "create" ? "请输入 IP 组名称" : "请选择 IP 组");
      const payload = { id: existing?.id || stableID("ip", name), name, kind: "ip", entries: [...new Set([...(existing?.entries || existing?.members || []), ...entries])] };
      try { state.busy = true; await client.saveIPGroup(payload, Boolean(existing)); state.ipGroups = await client.ipGroups(); state.notice = { tone: "success", text: mode === "append" ? "IP 内容已追加并完成回读" : "IP 组已创建并完成回读" }; return true; }
      catch (error) { state.notice = { tone: "error", text: model.errorMessage(error) }; return false; }
      finally { state.busy = false; render(); }
    }
    const file = body.querySelector('[data-ip-import-file]')?.files?.[0];
    const content = file ? await file.text() : body.querySelector('[data-ip-import-lines]').value;
    let lines;
    if (content.trim().startsWith('[')) {
      const parsed = JSON.parse(content);
      lines = parsed.map((item) => `${item.name || item.id}|${(item.entries || item.members || []).join(',')}`);
    } else lines = content.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
    if (!lines.length) throw new Error('请填写至少一条 IP 组');
    const payloads = lines.map((line) => {
      const separator = line.includes('|') ? '|' : '=';
      const [name, members] = line.split(separator, 2).map((item) => item.trim());
      const entries = splitAddressValues(members);
      if (!name || !entries.length) throw new Error('每行格式应为：组名|IP、网段或范围');
      return { id: stableID('ip', name), name, kind: 'ip', entries };
    });
    try {
      state.busy = true;
      for (const payload of payloads) await client.saveIPGroup(payload, false);
      state.ipGroups = await client.ipGroups();
      state.notice = { tone: 'success', text: `已导入 ${payloads.length} 个 IP 组` };
      return true;
    } catch (error) { state.notice = { tone: 'error', text: model.errorMessage(error) }; return false; }
    finally { state.busy = false; render(); }
  }

  function exportIPGroups() {
    const data = JSON.stringify(state.ipGroups?.items || [], null, 2);
    const blob = new Blob([data], { type: 'application/json' });
    const link = document.createElement('a');
    link.href = URL.createObjectURL(blob);
    link.download = 'ly-route-ip-groups.json';
    link.click();
    URL.revokeObjectURL(link.href);
  }

  async function saveACL(existing, body) {
    const name = body.querySelector("[data-acl-name]").value.trim();
    const payload = { id: existing?.id || stableID("acl", name), name, kind: "acl", enabled: body.querySelector("[data-acl-enabled]").checked, match: { enabled: true, schedule: "always", direction: body.querySelector("[data-acl-direction]").value, src_ip: rateConditionValues(body, "acl-source").join(","), dst_ip: rateConditionValues(body, "acl-destination").join(","), src_port: body.querySelector("[data-acl-source-port]").value.trim(), dst_port: body.querySelector("[data-acl-destination-port]").value.trim(), protocol: body.querySelector("[data-acl-protocol]").value }, action: body.querySelector("[data-acl-action]").value };
    try {
      await client.saveSecurityACL(payload, Boolean(existing));
      state.securityACLs = await client.securityACLs();
      if (!(state.securityACLs.items || []).some((item) => item.id === payload.id)) throw new Error("保存响应与 API 回读不一致");
      state.notice = { tone: "success", text: "安全策略已保存并回读确认" };
      return true;
    } catch (error) { state.notice = { tone: "error", text: model.errorMessage(error) }; return false; }
    finally { render(); }
  }

  async function saveUser(existing, body) {
    const payload = { username: body.querySelector("[data-user-name]").value.trim(), role: body.querySelector("[data-user-role]").value };
    const password = body.querySelector("[data-user-password]").value;
    if (password) payload.password = password;
    try {
      await client.saveUser(payload, Boolean(existing));
      state.users = await client.users();
      if (!(state.users.items || []).some((item) => item.username === payload.username && item.role === payload.role)) throw new Error("保存响应与 API 回读不一致");
      state.notice = { tone: "success", text: "系统用户已保存并回读确认" };
      return true;
    } catch (error) { state.notice = { tone: "error", text: model.errorMessage(error) }; return false; }
    finally { render(); }
  }

  async function deleteAndReadback(remove, reload, collection, id, success) {
    try {
      await remove(id);
      state[collection] = await reload();
      state.notice = { tone: "success", text: success };
    } catch (error) { state.notice = { tone: "error", text: model.errorMessage(error) }; }
    render();
  }

  function downloadConfigExport() {
    if (!state.configExport) {
      state.notice = { tone: "error", text: "当前没有可导出的配置" };
      return render();
    }
    const blob = new Blob([`${JSON.stringify(state.configExport, null, 2)}\n`], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `ly-route-orchestrator-config-${new Date().toISOString().slice(0, 10)}.json`;
    anchor.click();
    URL.revokeObjectURL(url);
    state.notice = { tone: "success", text: "编排器配置已导出" };
    render();
  }

  async function createSnapshot(body) {
    const name = body.querySelector("[data-snapshot-name]").value.trim();
    try {
      await client.createSnapshot(name);
      state.snapshots = await client.snapshots();
      state.notice = { tone: "success", text: "配置快照已创建并回读确认" };
      return true;
    } catch (error) { state.notice = { tone: "error", text: model.errorMessage(error) }; return false; }
    finally { render(); }
  }

  async function restoreSnapshot(id) {
    try {
      await client.restoreSnapshot(id);
      state.snapshots = await client.snapshots();
      state.notice = { tone: "success", text: "期望配置已从快照恢复，需执行运行时应用" };
      return true;
    } catch (error) { state.notice = { tone: "error", text: model.errorMessage(error) }; return false; }
    finally { render(); }
  }

  function openImportConfirmation(packageData, preflight) {
    modal.open({
      title: "确认导入配置",
      html: `<form class="orchestrator-form"><p class="orchestrator-confirm">产品：${safeText(packageData.payload?.product || "unknown")}<br>包哈希：${safeText(preflight.package_hash || "-")}<br>确认有效期：${safeText(preflight.confirmation_expires_at || "-")}</p><label><span>最终确认</span><span><input type="checkbox" required data-import-confirm> 替换当前期望配置</span></label></form>`,
      submitLabel: "确认导入",
      async onSubmit() {
        try {
          await client.confirmConfigImport(packageData, preflight);
          state.configExport = await client.configExport();
          state.snapshots = await client.snapshots();
          state.notice = { tone: "success", text: "配置已导入，需执行运行时应用" };
          render();
          return true;
        } catch (error) { state.notice = { tone: "error", text: model.errorMessage(error) }; render(); return false; }
      },
    });
  }

  async function preflightConfigImport(body) {
    try {
      const file = body.querySelector("[data-import-file]").files[0];
      if (!file || file.size > 10 * 1024 * 1024) throw new Error("请选择不超过 10 MiB 的 JSON 配置包");
      const packageData = JSON.parse(await file.text());
      if (!packageData.package_manifest || !packageData.payload) throw new Error("配置包缺少 package_manifest 或 payload");
      const preflight = await client.preflightConfigImport(packageData);
      setTimeout(() => openImportConfirmation(packageData, preflight), 0);
      return true;
    } catch (error) { state.notice = { tone: "error", text: model.errorMessage(error) }; render(); return false; }
  }

  elements.workspace.addEventListener("click", async (event) => {
    const target = event.target;
    if (!(target instanceof Element)) return;
    const pagerAction = target.closest("[data-pager-prev], [data-pager-next]");
    if (pagerAction) {
      const pager = pagerAction.closest(".table-pager");
      const current = Number(pager?.dataset.page || 1);
      updateTablePager(pager, current + (pagerAction.hasAttribute("data-pager-next") ? 1 : -1));
      return;
    }
    const openPage = target.closest("[data-open-page]");
    if (openPage) {
      activePage = openPage.dataset.openPage;
      shell.state.active = activePage;
      shell.render();
      enhanceTablePagers();
      return;
    }
    if (activePage === "object/ip" && target.closest("[data-add-ip-group]")) return modal.open({ title: "新增 IP 组", html: ipGroupForm(), onSubmit: (body) => saveIPGroup(null, body), trigger: target.closest("[data-add-ip-group]") });
    if (activePage === "object/ip" && target.closest("[data-edit-ip-group]")) {
      const item = (state.ipGroups.items || []).find((entry) => entry.id === target.closest("[data-edit-ip-group]").dataset.editIpGroup);
      return modal.open({ title: "编辑 IP 组", html: ipGroupForm(item), onSubmit: (body) => saveIPGroup(item, body), trigger: target.closest("[data-edit-ip-group]") });
    }
    if (activePage === "object/ip" && target.closest("[data-delete-ip-group]")) return deleteAndReadback(client.deleteIPGroup, client.ipGroups, "ipGroups", target.closest("[data-delete-ip-group]").dataset.deleteIpGroup, "IP 组已删除并回读确认");
    if (activePage === "security/policies" && target.closest("[data-add-acl]")) { modal.open({ title: "新增安全策略", html: aclForm(), onSubmit: (body) => saveACL(null, body), trigger: target.closest("[data-add-acl]") }); wireRateConditionEditors(document.getElementById("modalBody")); return; }
    if (activePage === "security/policies" && target.closest("[data-edit-acl]")) {
      const item = (state.securityACLs.items || []).find((entry) => entry.id === target.closest("[data-edit-acl]").dataset.editAcl);
      modal.open({ title: "编辑安全策略", html: aclForm(item), onSubmit: (body) => saveACL(item, body), trigger: target.closest("[data-edit-acl]") });
      wireRateConditionEditors(document.getElementById("modalBody"));
      return;
    }
    if (activePage === "security/policies" && target.closest("[data-delete-acl]")) return deleteAndReadback(client.deleteSecurityACL, client.securityACLs, "securityACLs", target.closest("[data-delete-acl]").dataset.deleteAcl, "安全策略已删除并回读确认");
    if (activePage === "object/ip" && target.closest("[data-export-ip-groups]")) return exportIPGroups();
    if (activePage === "object/ip" && target.closest("[data-import-ip-groups]")) { modal.open({ title: "新增IP", html: ipEntryForm(), submitLabel: "确定", onSubmit: importIPGroups, trigger: target.closest("[data-import-ip-groups]") }); wireIPEntryForm(document.getElementById("modalBody")); return; }
    if (activePage === "system/users" && target.closest("[data-add-user]")) return modal.open({ title: "新增系统用户", html: userForm(), onSubmit: (body) => saveUser(null, body), trigger: target.closest("[data-add-user]") });
    if (activePage === "system/users" && target.closest("[data-edit-user]")) {
      const item = (state.users.items || []).find((entry) => entry.username === target.closest("[data-edit-user]").dataset.editUser);
      return modal.open({ title: "编辑系统用户", html: userForm(item), onSubmit: (body) => saveUser(item, body), trigger: target.closest("[data-edit-user]") });
    }
    if (activePage === "system/users" && target.closest("[data-delete-user]")) return deleteAndReadback(client.deleteUser, client.users, "users", target.closest("[data-delete-user]").dataset.deleteUser, "系统用户已删除并回读确认");
    if (activePage === "system/config" && target.closest("[data-export-config]")) return downloadConfigExport();
    if (activePage === "system/config" && target.closest("[data-import-config]")) return modal.open({ title: "预检配置导入", html: '<form class="orchestrator-form"><label><span>配置包</span><input required type="file" accept="application/json,.json" data-import-file></label><p class="orchestrator-form-note">仅接受当前 Orchestrator 产品导出的 JSON；密钥材料和 Gateway 资源会被拒绝。</p></form>', submitLabel: "开始预检", onSubmit: preflightConfigImport, trigger: target.closest("[data-import-config]") });
    if (activePage === "system/config" && target.closest("[data-create-snapshot]")) return modal.open({ title: "创建配置快照", html: '<form class="orchestrator-form"><label><span>快照名称</span><input required maxlength="40" data-snapshot-name placeholder="变更前备份"></label></form>', onSubmit: createSnapshot, trigger: target.closest("[data-create-snapshot]") });
    if (activePage === "system/config" && target.closest("[data-restore-snapshot]")) {
      const id = target.closest("[data-restore-snapshot]").dataset.restoreSnapshot;
      return modal.open({ title: "恢复配置快照", html: `<p class="orchestrator-confirm">恢复 ${safeText(id)} 将替换当前期望配置，运行态不会自动应用。</p>`, submitLabel: "确认恢复", onSubmit: () => restoreSnapshot(id), trigger: target.closest("[data-restore-snapshot]") });
    }
    if (activePage === "flow-control/traffic" && target.closest("[data-add-rate]")) {
      modal.open({ title: "新增限速规则", html: rateRuleForm(), onSubmit: (body) => saveRateRule(null, body), trigger: target.closest("[data-add-rate]") });
      wireRateConditionEditors(document.getElementById("modalBody"));
      return;
    }
    if (activePage === "flow-control/traffic" && target.closest("[data-edit-rate]")) {
      const index = Number(target.closest("[data-edit-rate]").dataset.editRate);
      modal.open({ title: "编辑限速规则", html: rateRuleForm(trafficControlIntent().rules[index]), onSubmit: (body) => saveRateRule(index, body), trigger: target.closest("[data-edit-rate]") });
      wireRateConditionEditors(document.getElementById("modalBody"));
      return;
    }
    if (activePage === "flow-control/traffic" && target.closest("[data-delete-rate]")) {
      await deleteRateRule(Number(target.closest("[data-delete-rate]").dataset.deleteRate));
      return;
    }
    if (target.closest("[data-save-management]")) {
      const root = target.closest(".management-network-op");
      if (!root.querySelector("[data-management-confirm]").checked) {
        state.notice = { tone: "error", text: "请先确认修改管理访问地址" };
        return render();
      }
      const payload = {
        confirm_change: true,
        mode: root.querySelector('input[name="orchestrator-management-mode"]:checked').value,
        interface_id: root.querySelector("[data-management-interface]").value,
        cidr: root.querySelector("[data-management-cidr]").value.trim(),
        gateway: root.querySelector("[data-management-gateway]").value.trim(),
      };
      try {
        state.busy = true;
        await client.saveManagementNetwork(payload);
        state.managementNetwork = await client.managementNetwork();
        const confirmed = state.managementNetwork?.item || state.managementNetwork || {};
        if (confirmed.mode !== payload.mode || confirmed.cidr !== payload.cidr) throw new Error("保存响应与 API 回读不一致");
        if (state.draft) state.draft.management_shared = confirmed.mode === "shared_lan";
        state.notice = { tone: "success", text: "管理口配置已从 API 回读确认" };
      } catch (error) {
        state.notice = { tone: "error", text: model.errorMessage(error) };
      } finally {
        state.busy = false;
        render();
      }
      return;
    }
    if (activePage === "orchestrator/nic-settings") await nic.handle(target);
    if (activePage === "orchestrator/group-settings") await groups.handle(target);
    if (activePage === "orchestrator/policy") await policy.handle(target);
  });
  let draggedPolicyGroup = "";
  elements.workspace.addEventListener("dragstart", (event) => {
    const bar = event.target instanceof Element ? event.target.closest("[data-policy-group-drag]") : null;
    if (!bar || activePage !== "orchestrator/policy") return;
    draggedPolicyGroup = bar.dataset.policyGroupDrag || "";
    event.dataTransfer?.setData("text/plain", draggedPolicyGroup);
    event.dataTransfer?.setDragImage(bar, 24, 18);
  });
  elements.workspace.addEventListener("dragover", (event) => {
    if (activePage !== "orchestrator/policy" || !draggedPolicyGroup) return;
    const bar = event.target instanceof Element ? event.target.closest("[data-policy-group-drag]") : null;
    if (!bar || bar.dataset.policyGroupDrag === draggedPolicyGroup) return;
    event.preventDefault();
  });
  elements.workspace.addEventListener("drop", async (event) => {
    if (activePage !== "orchestrator/policy") return;
    const bar = event.target instanceof Element ? event.target.closest("[data-policy-group-drag]") : null;
    const targetGroup = bar?.dataset.policyGroupDrag || "";
    if (!draggedPolicyGroup || !targetGroup || targetGroup === draggedPolicyGroup) return;
    event.preventDefault();
    const sourceGroup = draggedPolicyGroup;
    draggedPolicyGroup = "";
    await policy.moveGroupBefore(sourceGroup, targetGroup);
  });
  elements.workspace.addEventListener("dragend", () => { draggedPolicyGroup = ""; });
  elements.loginForm.addEventListener("submit", async (event) => {
    event.preventDefault();
    const username = document.getElementById("username")?.value || "";
    const password = document.getElementById("password")?.value || "";
    try {
      const response = await client.login(username, password);
      if (response.ok) showShell();
      else showLogin("用户名或密码不正确。");
    } catch {
      showLogin("无法连接认证服务，请稍后重试。");
    }
  });
  elements.logoutButton.addEventListener("click", async () => {
    await client.logout().catch(() => null);
    showLogin();
  });

  client.session().then((response) => response.ok ? showShell() : showLogin()).catch(() => showLogin("无法连接认证服务，请稍后重试。"));
}());
