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
        <label data-bond-field><span>聚合名称</span><input aria-label="聚合名称" data-bond-name value="${safeText(current?.bond?.name || `bond-${role}`)}" pattern="[A-Za-z0-9._:\\-]{1,63}" required></label>
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
            nextRole.bond = { name: form.querySelector("[data-bond-name]").value.trim(), members };
          }
          state.draft = model.replaceRole(topologyForDraft(), nextRole);
          state.notice = { tone: "pending", text: `${role.toUpperCase()} 草稿已更新，尚未写入 API` };
          state.stale = false;
          render();
          return true;
        },
      });
      syncRoleForm(document.getElementById("modalBody"));
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
      const rows = state.inventory.map((item) => {
        const managementPort = item.name === management;
        const health = item.link_state === "down" ? "链路断开" : "链路正常";
        const sharedLAN = managementPort && topology.management_shared === true && model.rolePorts(lan).includes(item.name);
        const owner = sharedLAN ? "LAN + 管理共享" : managementPort ? "管理专用" : model.rolePorts(lan).includes(item.name) ? "LAN" : model.rolePorts(wan).includes(item.name) ? "WAN" : owners.has(item.name) ? "编排组" : "可用";
        return `<tr><td><strong>${safeText(item.name)}</strong></td><td><span class="orchestrator-health ${item.link_state === "down" ? "is-down" : "is-up"}">${health}</span></td><td>${safeText(item.speed_mbps || "未知")} Mbps</td><td>${safeText(item.driver || "未报告")}</td><td>${owner}</td></tr>`;
      }).join("");
      return `<section class="page-body list-page orchestrator-settings">
        ${state.renderNotice()}
        <section class="orchestrator-summary" aria-label="LAN 和 WAN 配置">
          <div><span>管理口</span><strong>${safeText(management || "未报告")}</strong><small>${topology.management_shared ? "与 LAN 共享，仅管理流量交给 Linux" : "Linux 管理面独占，不参与数据口选择"}</small></div>
          <div><span>LAN</span><strong>${safeText(model.roleLabel(lan))}</strong><button type="button" data-configure-role="lan">配置 LAN</button></div>
          <div><span>WAN</span><strong>${safeText(model.roleLabel(wan))}</strong><button type="button" data-configure-role="wan">配置 WAN</button></div>
        </section>
        <div class="orchestrator-toolbar"><div><strong>物理网卡库存</strong><span>${state.inventory.length} 个端口 · ${topology.management_shared ? "管理口仅可复用为 LAN" : "管理口不可分配"}</span></div><button class="primary" type="button" data-save-nics ${state.busy || !model.configured(topology) ? "disabled" : ""}>保存网卡设置</button></div>
        <div class="orchestrator-table-wrap"><table class="data-table orchestrator-table"><thead><tr><th>接口</th><th>链路健康</th><th>速率</th><th>驱动</th><th>所有权</th></tr></thead><tbody>${rows}</tbody></table></div>
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
