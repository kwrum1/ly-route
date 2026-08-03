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
          <label data-required><span>组名称</span><input aria-label="组名称" data-group-name value="${safeText(group?.name || "")}" pattern="[A-Za-z0-9._:\\-]{1,63}" ${group ? "readonly" : ""} required></label>
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
