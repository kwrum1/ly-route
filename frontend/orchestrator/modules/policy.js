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

    function policyForm(selectedGroup = "") {
      const groups = state.topology?.orchestration_groups || [];
      const options = groups.map((group) => `<option value="${safeText(group.name)}">${safeText(group.name)}</option>`).join("");
      const groupSelector = groups.length
        ? `<label data-required><span>策略组</span><select data-policy-group ${selectedGroup ? "disabled" : ""}>${groups.map((group) => `<option value="${safeText(group.name)}" ${group.name === selectedGroup ? "selected" : ""}>${safeText(group.name)}</option>`).join("")}</select></label>`
        : `<label data-required><span>策略组</span><input data-policy-group required maxlength="63"></label><label data-required><span>组优先级</span><input data-policy-position type="number" min="1" step="1" value="10" required></label>`;
      return `<form class="orchestrator-form pa-config-form" data-policy-form>
        <section class="pa-form-section"><h3>基本设置</h3><div class="pa-form-rows">${groupSelector}
          <label data-required><span>策略序号</span><input data-policy-sequence type="number" min="1" step="1" value="10" required></label>
          <label data-required><span>明细名称</span><input data-policy-rule required maxlength="63"></label>
        </div></section>
        <section class="pa-form-section"><h3>匹配条件</h3><div class="pa-form-rows"><label><span>源 / 目的地址</span><span class="pa-address-summary-pair">${conditionSummary("source", "源地址")}<em>/</em>${conditionSummary("destination", "目的地址")}</span></label>
          <label><span>源 / 目的端口</span><span class="pa-port-pair"><input data-policy-source-port placeholder="0"><em>/</em><input data-policy-destination-port placeholder="0"></span></label>
          <label><span>协议</span><select data-policy-protocol><option value="any">Any</option><option value="tcp">TCP</option><option value="udp">UDP</option><option value="icmp">ICMP</option><option value="icmpv6">ICMPv6</option></select></label>
        </div></section>
        <section class="pa-form-section"><h3>执行动作</h3><div class="pa-form-rows">
          <label data-required><span>流量路径</span><select data-policy-action><option value="via">经过编排组</option><option value="direct">直接转发</option><option value="drop">丢弃</option></select></label>
          <label data-policy-group-target><span>目标编排组</span><select data-policy-target>${options}</select></label>
        </div></section>
      </form>`;
    }

    function groupForm() {
      const nextPosition = ((currentPolicy().policy_groups || []).length + 1) * 10;
      return `<form class="orchestrator-form pa-config-form" data-policy-group-form>
        <section class="pa-form-section"><h3>策略组设置</h3><div class="pa-form-rows">
          <label data-required><span>策略组名称</span><input data-policy-group-name required maxlength="63"></label>
          <label data-required><span>优先级</span><input data-policy-group-position type="number" min="1" step="1" value="${nextPosition}" required><small>数值越小越优先</small></label>
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
      const action = form.querySelector("[data-policy-action]");
      const targetRow = form.querySelector("[data-policy-group-target]");
      const target = targetRow.querySelector("select");
      const syncAction = () => {
        const visible = action.value === "via";
        targetRow.hidden = !visible;
        target.disabled = !visible;
      };
      action.addEventListener("change", syncAction);
      syncAction();
    }

    function openCreate(trigger, selectedGroup = "") {
      modal.open({
        title: selectedGroup ? `新增策略明细 · ${selectedGroup}` : "新增编排策略明细",
        html: policyForm(selectedGroup),
        trigger,
        async onSubmit(body) {
          state.busy = true;
          try {
            const form = body.querySelector("[data-policy-form]");
            const actionKind = form.querySelector("[data-policy-action]").value;
            const group = form.querySelector("[data-policy-group]").value.trim();
            const rule = form.querySelector("[data-policy-rule]").value.trim();
            if (!/^[-A-Za-z0-9._:]{1,63}$/.test(group) || !/^[-A-Za-z0-9._:]{1,63}$/.test(rule)) throw new Error("策略组和明细名称仅允许字母、数字、点、下划线、冒号和连字符");
            const next = JSON.parse(JSON.stringify(currentPolicy()));
            next.schema_version = 1;
            delete next.schema;
            next.ip_objects ||= [];
            let policyGroup = next.policy_groups.find((item) => item.id === group);
            if (!policyGroup) {
              policyGroup = { id: group, position: Number(form.querySelector("[data-policy-position]")?.value || 10), rules: [] };
              next.policy_groups.push(policyGroup);
            }
            const action = { kind: actionKind };
            if (actionKind === "via") {
              action.group = form.querySelector("[data-policy-target]").value;
              if (!action.group) throw new Error("经过编排组时必须选择目标编排组");
            }
            policyGroup.rules.push({
              id: rule,
              sequence: Number(form.querySelector("[data-policy-sequence]").value),
              match: { sources: addressSelector(form, "source", next, rule), destinations: addressSelector(form, "destination", next, rule), source_ports: splitValues(form.querySelector("[data-policy-source-port]").value), dest_ports: splitValues(form.querySelector("[data-policy-destination-port]").value), protocol: form.querySelector("[data-policy-protocol]").value },
              action,
            });
            state.policy = await client.savePolicy(next);
            state.notice = { tone: "success", text: "编排策略已保存并完成 API 回读" };
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
          const position = Number(form.querySelector("[data-policy-group-position]").value);
          if (!/^[-A-Za-z0-9._:]{1,63}$/.test(id)) {
            state.notice = { tone: "error", text: "策略组名称仅允许字母、数字、点、下划线、冒号和连字符" };
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
      const actionLabel = ({ via: "经过编排组", direct: "直接转发", drop: "丢弃" })[action.kind] || action.kind;
      return `<tr><td><strong>${safeText(rule.sequence)}</strong></td><td><strong>${safeText(rule.id)}</strong></td><td>${safeText((rule.match?.sources || ["any"]).join(", "))}</td><td>${safeText((rule.match?.destinations || ["any"]).join(", "))}</td><td>${safeText(rule.match?.protocol || "any")}</td><td>${safeText(actionLabel)}${action.group ? ` · ${safeText(action.group)}` : ""}</td></tr>`;
    }

    function renderPage() {
      const policy = currentPolicy();
      const groups = [...(policy.policy_groups || [])].sort((left, right) => left.position - right.position);
      const groupCards = groups.map((group, groupIndex) => {
        const rules = (group.rules || []).slice().sort((left, right) => left.sequence - right.sequence);
        const controls = `${groupIndex > 0 ? `<button class="icon-btn" type="button" data-policy-up="${safeText(group.id)}" aria-label="上移策略组" title="上移策略组">↑</button>` : ""}${groupIndex < groups.length - 1 ? `<button class="icon-btn" type="button" data-policy-down="${safeText(group.id)}" aria-label="下移策略组" title="下移策略组">↓</button>` : ""}`;
        return `<article class="policy-group-card" draggable="true" data-policy-group-card="${safeText(group.id)}"><header><div class="policy-group-order"><span>优先级</span><strong>${safeText(group.position)}</strong></div><div><h2>${safeText(group.id)}</h2><p>${rules.length} 条策略明细</p></div><div class="policy-group-actions">${controls}<button class="ghost-btn" type="button" data-create-policy-in-group="${safeText(group.id)}">新增明细</button></div></header><div class="orchestrator-table-wrap"><table class="data-table orchestrator-table policy-rule-table"><thead><tr><th>序号</th><th>明细</th><th>源 IP</th><th>目的 IP</th><th>协议</th><th>流量路径</th></tr></thead><tbody>${rules.length ? rules.map((rule) => ruleRow(group, rule)).join("") : '<tr><td colspan="6" class="orchestrator-empty">本组暂无策略明细</td></tr>'}</tbody></table></div></article>`;
      }).join("");
      return `<section class="page-body list-page"><section class="list-content policy-workbench">${state.renderNotice()}<div class="orchestrator-toolbar pa-list-toolbar"><div><strong>策略组</strong><span>共 ${groups.length} 组</span></div><div><button class="ghost-btn" type="button" data-create-policy ${state.busy || !modelConfigured() ? "disabled" : ""}>新增明细</button><button class="primary" type="button" data-create-policy-group ${state.busy || !modelConfigured() ? "disabled" : ""}>新增策略组</button></div></div>${modelConfigured() ? `<section class="policy-default-path"><strong>缺省规则</strong><span>未命中任何策略</span><b>${safeText(policy.default?.kind || "direct") === "direct" ? "转发到 LAN" : safeText(policy.default?.kind || "direct")}</b></section><div class="policy-group-list" data-policy-group-list>${groupCards || '<p class="orchestrator-empty">暂无策略组</p>'}</div>` : '<div class="orchestrator-gate" role="status"><strong>请先完成网卡设置</strong><span>配置 LAN/WAN 后可创建编排策略。</span></div>'}</section></section>`;
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
      const inGroup = target.closest("[data-create-policy-in-group]");
      if (inGroup) return openCreate(inGroup, inGroup.dataset.createPolicyInGroup);
      if (target.closest("[data-create-policy]")) return openCreate(target);
      const up = target.closest("[data-policy-up]");
      if (up) return moveGroup(up.dataset.policyUp, -1);
      const down = target.closest("[data-policy-down]");
      if (down) return moveGroup(down.dataset.policyDown, 1);
    }

    return Object.freeze({ handle, renderPage, moveGroupBefore });
  }

  window.LyRouteOrchestratorPolicy = Object.freeze({ createPolicyController });
}());
