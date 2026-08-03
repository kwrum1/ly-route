const { createPageMap, safeText } = window.LyRouteShell;

const sections = [
  { id: 'overview', no: '01', title: '系统概况', pages: [
    ['system/system_overview', '系统概况', 'system-overview'],
    ['dashboard/dashboard', '流量概况', 'dashboard'],
    ['monitor/ipobj_main', '在线用户', 'table'],
    ['monitor/flow_topn', 'Top连接', 'table'],
    ['monitor/domain_topn', 'Top域名', 'table']
  ]},
  { id: 'network', no: '02', title: '网络设置', pages: [
    ['monitor/interface_list', '网卡设置', 'table'],
    ['network/proxy_main', 'LAN/WAN', 'settings'],
    ['network/wangroup_manager', 'WAN群组', 'table'],
    ['route/route_policy_main', '策略路由', 'tabs'],
    ['route/dnspolicy_main', 'DNS策略', 'table'],
    ['network/dhcpsvr_main', 'DHCP服务', 'settings']
  ]},
  { id: 'behavior', no: '03', title: '行为管理', pages: [
    ['flowcontrol/flowct_main', '流量控制', 'table']
  ]},
  { id: 'object', no: '05', title: '对象管理', pages: [
    ['object/urlgrp_list', '域名管理', 'table'],
    ['object/iptab_list', 'IP管理', 'table']
  ]},
  { id: 'system', no: '06', title: '系统维护', pages: [
    ['system/webuser_main', '用户管理', 'table'],
    ['system/sys_config', '配置管理', 'settings'],
  ]}
];

const pageMap = createPageMap(sections);
const gatewayRouting = window.LyRouteGatewayRouting;
const gatewayOverview = window.LyRouteGatewayOverview;

const state = { active: gatewayRouting.routeFromHash(pageMap, 'system/system_overview'), query: '', checkedRows: new Set(), batchOpen: false, collapsedSections: new Set(), activeTabs: { 'network/proxy_main': 0, 'route/route_policy_main': 0, 'object/urlgrp_list': 0, 'object/iptab_list': 0 }, selectedDomainGroup: '', selectedIpGroup: '', trafficWindow: '5m', hiddenEgresses: new Set(), trafficRenderOptions: null, controlPlane: { loading: false, error: '', health: null, mode: null, capabilities: null, telemetry: {}, resources: {}, audit: null, configExport: null, configApply: null, managementNetwork: null, smartQoS: null, runtimeStatus: null, runtimePreview: null, runtimeApply: null, firmwareStatus: null, proxyStatus: null, proxyLogs: null, pppoeStatus: null, endpointErrors: {}, runtimeBusy: '', firmwareBusy: '', trafficTrendLoading: false } };
const el = {
  loginScreen: document.getElementById('loginScreen'), loginForm: document.getElementById('loginForm'), appShell: document.getElementById('appShell'),
  sideMenu: document.getElementById('sideMenu'), menuSearch: document.getElementById('menuSearch'), workspace: document.getElementById('workspace'), mobileMenuToggle: document.getElementById('mobileMenuToggle'), sidebar: document.querySelector('.paui-sidebar'),
  logoutButton: document.getElementById('logoutButton'),
  modalBackdrop: document.getElementById('modalBackdrop'), modal: document.getElementById('modal'), modalTitle: document.getElementById('modalTitle'), modalBody: document.getElementById('modalBody'), modalClose: document.getElementById('modalClose'), modalCancel: document.getElementById('modalCancel'), modalOk: document.getElementById('modalOk'), toast: document.getElementById('toast')
};

function escapeAttr(value) { return safeText(value); }
function currentPage() { return pageMap.get(state.active) || pageMap.values().next().value; }
let pendingModalSubmit = null;
let modalSubmitting = false;
const modalController = window.LyRouteGatewayModal.create({
  modal: el.modal,
  backdrop: el.modalBackdrop,
  title: el.modalTitle,
  body: el.modalBody,
  closeButton: el.modalClose,
  cancelButton: el.modalCancel
}, {
  isBusy: () => modalSubmitting,
  onClose: () => { pendingModalSubmit = null; }
});
const networkPages = {
  'monitor/interface_list': {
    tabs: ['网络接口配置'],
    actions: ['创建聚合'],
    columns: ['接口', '状态', '工作模式', '方向', '链路聚合', '实时流量', '数据包'],
    rows: [],
    form: [['方向', 'WAN'], ['聚合组', '']]
  },
  'network/proxy_main': {
    tabs: ['LAN接口', 'WAN接口', '线路日志'],
    summary: [['总流入', ''], ['总流出', ''], ['连接数', '']],
    settings: [['接口名称', ''], ['IP地址/掩码', ''], ['网关', ''], ['DNS', ''], ['NAT', '启用'], ['MTU', ''], ['带宽', ''], ['备注', '']],
    actions: ['新增', '编辑', '导入', '批量操作', '删除'],
    columns: ['接口名称', '类型', 'IP地址/掩码', '网关', 'DNS', 'NAT', 'MTU', '带宽', '总流入', '总流出', '连接数', '备注'],
    rows: [],
    logs: [],
    form: [['接口名称', ''], ['IP地址/掩码', ''], ['网关', ''], ['DNS', ''], ['NAT', '启用'], ['MTU', ''], ['备注', '']]
  },
  'network/wangroup_manager': {
    tabs: ['WAN群组'],
    actions: ['新增群组', '转移', '移出', '批量操作', '删除'],
    columns: ['聚合组', '方向', '成员网卡'],
    rows: [],
    form: [['名称', ''], ['最大上行总带宽', ''], ['最大下行带宽', '']]
  },
  'route/route_policy_main': {
    tabs: ['IPv4'],
    actions: ['新增策略', '批量操作', '删除'],
    columns: ['序号', '类型', '源地址', '源端口', '目的地址', '目的端口', '目标线路', '下一跳', '命中次数', '备注'],
    rows: [],
    form: [['序号', ''], ['类型', 'NAT'], ['源地址', ''], ['目的地址', ''], ['目标线路', '空线路']]
  },
  'route/portmap_list': {
    actions: ['新增映射', '批量操作', '删除', '单个新增', '批量新增', '导入', '导出'],
    filters: [['映射线路', ['所有线路', '请选择线路']], ['关键字搜索', '']],
    columns: ['策略名称', '映射线路', '外部端口', '内网主机', '内网端口', '协议', '会话', '备注'],
    rows: [],
    form: [['策略名称', ''], ['映射线路', '请选择线路'], ['外部端口', ''], ['内网主机', ''], ['内网端口', ''], ['协议', 'TCP']]
  },
  'route/dnspolicy_main': {
    actions: ['新增管控', '编辑', '批量操作', '删除'],
    columns: ['策略序号', '源IP', '访问域名', '解析线路', '解析地址', '动作'],
    rows: [],
    form: [['策略序号', ''], ['源IP', ''], ['访问域名', ''], ['解析线路', '请选择线路'], ['解析地址', ''], ['动作', '解析']]
  },
  'network/dhcpsvr_main': {
    tabs: ['服务列表', '静态分配'],
    actions: ['新增服务', '批量操作', '删除'],
    columns: ['类型', '接口/主机', '子网/IP', '网关', '策略/MAC', '租约/时间', '状态', '备注'],
    rows: [],
    form: []
  },
  'flowcontrol/flowct_main': {
    actions: ['新增'],
    columns: ['策略序号', '内 / 外地址', '内 / 外端口', '执行动作', '流控方向', '限速大小', '状态'],
    rows: [],
    form: [['策略序号', ''], ['内 / 外地址', ''], ['内 / 外端口', ''], ['执行动作', '限速'], ['流控方向', '全线路'], ['限速大小', '']]
  },
  'object/urlgrp_list': {
    tabs: ['域名群组', '域名展示'],
    actions: ['新增', '导出'],
    filters: [],
    columns: ['群组名称', '备注', '域名数量', '关联策略', '更新时间', '状态'],
    rows: []
  },
  'object/iptab_list': {
    tabs: ['IP群组', 'IP展示'],
    actions: ['新增', '导出'],
    filters: [],
    columns: ['群组名称', '备注', '成员数', '关联策略', '更新时间', '状态'],
    rows: []
  }
};

function isNetworkContentPage(page) { return Boolean(networkPages[page.id]); }

function renderMenu() {
  const q = state.query.trim().toLowerCase();
  el.sideMenu.innerHTML = sections.map((section) => {
    const pages = section.pages.map((raw) => pageMap.get(raw[0])).filter((page) => !q || `${section.title} ${page.title} ${page.id}`.toLowerCase().includes(q));
    if (!pages.length) return '';
    const hasActivePage = pages.some((page) => page.id === state.active);
    const collapsed = state.collapsedSections.has(section.id) && !q;
    return `<div class="menu-group ${collapsed ? 'is-collapsed' : ''}">
      <button class="menu-head ${hasActivePage ? 'is-active' : ''}" type="button" data-section="${section.id}" aria-expanded="${!collapsed}">
        <strong>${section.title}</strong>
      </button>
      <div class="menu-pages">${pages.map((page) => `<button class="menu-page ${page.id === state.active ? 'is-active' : ''}" type="button" data-page="${page.id}"><span>${safeText(page.title)}</span></button>`).join('')}</div>
    </div>`;
  }).join('');
  el.sideMenu.querySelectorAll('[data-section]').forEach((button) => button.addEventListener('click', () => toggleSection(button.dataset.section)));
  el.sideMenu.querySelectorAll('[data-page]').forEach((button) => button.addEventListener('click', () => {
    openPage(button.dataset.page);
    setMobileMenuOpen(false);
  }));
}

function setMobileMenuOpen(open) {
  const visible = Boolean(open);
  el.appShell.classList.toggle('mobile-menu-open', visible);
  if (el.sidebar && window.matchMedia('(max-width: 900px)').matches) el.sidebar.style.display = visible ? 'block' : 'none';
  el.mobileMenuToggle.setAttribute('aria-expanded', String(visible));
  el.mobileMenuToggle.setAttribute('aria-label', visible ? '关闭菜单' : '打开菜单');
  el.mobileMenuToggle.title = visible ? '关闭菜单' : '打开菜单';
}

function toggleSection(id) {
  if (state.collapsedSections.has(id)) state.collapsedSections.delete(id);
  else state.collapsedSections.add(id);
  renderMenu();
}
function openPage(id, updateLocation = true) {
  if (!pageMap.has(id)) return;
  state.active = id;
  state.checkedRows.clear();
  state.batchOpen = false;
  if (updateLocation) gatewayRouting.navigate(id);
  render();
}
function renderWorkspace() {
  const page = currentPage();
  const objectDisplay = isObjectDisplayPage(page);
  const cardClass = `page-card${objectDisplay ? ' object-display-page' : ''}`;
  const bodyClass = page.type === 'dashboard' ? 'page-body' : `page-body list-page${objectDisplay ? ' object-display-page' : ''}`;
  el.workspace.innerHTML = `<article class="${cardClass}">
    <header class="page-title"><h1>${safeText(page.title)}</h1></header>
    ${renderControlPlaneStatus()}
    <div class="${bodyClass}">${renderPageBody(page)}</div>
  </article>`;
  wireWorkspaceEvents(page);
}

function renderControlPlaneStatus() {
  return '';
}
function renderEndpointErrors() {
  const entries = Object.entries(state.controlPlane.endpointErrors || {});
  if (!entries.length) return '';
  return `<div class="api-endpoint-errors">${entries.slice(0, 5).map(([key]) => `<span>${safeText(endpointLabel(key))}数据暂不可用</span>`).join('')}</div>`;
}
function endpointLabel(key) {
  return ({ dashboard: '概况', dashboardSummary: '概况汇总', trafficTrend: '趋势', interfaces: '接口遥测', topSessions: 'Top连接', topDomains: 'Top域名', onlineUsers: '在线用户', policyHits: '策略命中', audit: '审计', configExport: '配置导出', runtimeStatus: '运行态', firmwareStatus: '固件更新', proxyStatus: 'xray', proxyLogs: 'xray日志', pppoeStatus: 'PPPoE' })[key] || key;
}
function capabilityItems() {
  const seen = new Set();
  return [...(state.controlPlane.health?.dependencies || []), ...(state.controlPlane.capabilities?.items || [])].filter((item) => {
    if (!item || item.name === 'transparent_proxy_handoff' || item.state !== 'degraded') return false;
    if (seen.has(item.name)) return false;
    seen.add(item.name);
    return true;
  });
}
function renderPageBody(page) {
  if (page.type === 'dashboard') return renderDashboard();
  if (page.type === 'system-overview') return gatewayOverview.renderSystem({ summary: state.controlPlane.telemetry.dashboardSummary, onlineUsers: state.controlPlane.telemetry.onlineUsers, trafficTrend: state.controlPlane.telemetry.trafficTrend, runtime: state.controlPlane.runtimeStatus, health: state.controlPlane.health, escape: safeText });
  if (page.type === 'runtime') return renderRuntimeOperationsHtml();
  const content = page.type === 'settings' ? renderSettings(page) : page.type === 'tabs' ? renderTabsPage(page) : renderTable(page);
  const actions = page.id === 'system/webuser_main' ? renderSystemUserActions() : isNetworkContentPage(page) && !['network', 'behavior'].includes(page.sectionId) ? renderNetworkActions(page) : '';
  const contentClass = `list-content${isObjectDisplayPage(page) ? ' object-display-content' : ''}`;
  const pager = ['system-overview', 'dashboard', 'runtime'].includes(page.type) || ['system/sys_config', 'system/firmware_update'].includes(page.id) ? '' : renderPager();
  return `<section class="${contentClass}">${actions}${content}${pager}</section>`;
}
function isObjectDisplayPage(page) {
  return (page.id === 'object/urlgrp_list' || page.id === 'object/iptab_list') && (state.activeTabs[page.id] || 0) === 1;
}
function isOverviewEmptyListPage(page) {
  return page.id === 'monitor/ipobj_main' || page.id === 'monitor/flow_topn' || page.id === 'monitor/domain_topn';
}
function pageHasRows(page) {
  if (isOverviewEmptyListPage(page)) return false;
  if (isNetworkContentPage(page)) return networkVisibleRowCount(page) > 0;
  return false;
}
function networkVisibleRowCount(page) {
  if (page.id === 'network/proxy_main') {
    if ((state.activeTabs[page.id] || 0) === 2) return networkPages[page.id].logs.length;
    return proxyInterfaceRows(page).length;
  }
  if (page.id === 'network/dhcpsvr_main') return dhcpRows(page).length;
  return networkTableRowEntries(page).length;
}
function renderDashboard() {
  const dashboard = state.controlPlane.telemetry.dashboard?.data || {};
  state.trafficRenderOptions = { dashboard, trend: state.controlPlane.telemetry.trafficTrend || {}, error: state.controlPlane.endpointErrors.trafficTrend || '', loading: state.controlPlane.trafficTrendLoading, window: state.trafficWindow, hidden: state.hiddenEgresses, escape: safeText };
  return gatewayOverview.renderTraffic(state.trafficRenderOptions);
}
function renderTable(page) {
	if (page.id === 'system/webuser_main') return renderSystemUsersTable();
  if (page.id === 'monitor/ipobj_main') return renderOnlineUsersTable();
  if (page.id === 'monitor/flow_topn') return renderTopConnectionsTable();
  if (page.id === 'monitor/domain_topn') return renderTopDomainsTable();
  if (isNetworkContentPage(page)) return renderNetworkTablePage(page);
  const cols = tableColumns(page);
  return `<table class="data-table"><thead><tr><th><input data-select-all type="checkbox"></th>${cols.map((col) => `<th>${col}</th>`).join('')}<th>状态</th><th>操作</th></tr></thead><tbody></tbody></table>`;
}

function renderSystemUsersTable() {
	const users = envelopeItems(state.controlPlane.resources.authUsers);
	const body = users.length ? users.map((item) => {
		const username = item.username || item.name || '';
		const role = item.role || '';
		const stateText = item.enabled === false ? '禁用' : '启用';
		return `<tr><td>${safeText(username)}</td><td>${safeText(role)}</td><td>${renderNetworkCell(stateText, null, 0)}</td><td><button type="button" data-auth-action="password" data-user="${escapeAttr(username)}">改密</button>${username === 'admin' ? '' : `<button type="button" data-auth-action="delete" data-user="${escapeAttr(username)}">删除</button>`}</td></tr>`;
	}).join('') : renderEmptyTableRow(4, '暂无系统用户');
        return `<table class="data-table"><thead><tr><th>用户名</th><th>角色</th><th>状态</th><th>操作</th></tr></thead><tbody>${renderFixedTableRows(body, users.length, 4, '暂无系统用户')}</tbody></table>`;
}
function renderOnlineUsersTable() {
  const cols = ['序号', 'IP', 'MAC', '连接数', '流入bps', '流出bps', '在线时长', '流入总流量', '流出总流量'];
  return renderTelemetryTable(cols, telemetryRows('onlineUsers', mapOnlineUserRow), 'online-users-table', (col) => `<th><span>${col}</span></th>`);
}
function renderTopConnectionsTable() {
  const cols = ['IP', '连接数'];
  return renderTelemetryTable(cols, telemetryRows('topSessions', mapTopSessionRow));
}
function renderTopDomainsTable() {
  const cols = ['域名', '最后一次访问时间', '命中次数'];
  const colgroup = '<colgroup><col class="domain-name-col"><col class="domain-time-col"><col class="domain-hit-col"></colgroup>';
  return renderTelemetryTable(cols, telemetryRows('topDomains', mapTopDomainRow), 'top-domains-table', null, colgroup);
}
function renderTelemetryTable(cols, rows, className = '', renderHeadCell = null, colgroup = '') {
  const tableClass = `data-table${className ? ` ${className}` : ''}`;
  const headCells = cols.map((col) => renderHeadCell ? renderHeadCell(col) : `<th>${col}</th>`).join('');
  const body = rows.length ? rows.map((row) => `<tr>${row.map((value) => `<td>${renderTelemetryCell(value)}</td>`).join('')}</tr>`).join('') : '';
  return `<table class="${tableClass}">${colgroup}<thead><tr>${headCells}</tr></thead><tbody>${renderFixedTableRows(body, rows.length, cols.length, '暂无遥测数据')}</tbody></table>`;
}
function telemetryRows(key, mapper) {
  return envelopeItems(state.controlPlane.telemetry[key]).map(mapper);
}
function mapOnlineUserRow(item, index) {
  return [
    item.index || index + 1,
    item.ip || item.address || item.user || '',
    item.mac || item.mac_address || '',
    displayValue(item.sessions, item.connections, item.connection_count),
    displayValue(item.rx_bps, item.in_bps, item.download_bps),
    displayValue(item.tx_bps, item.out_bps, item.upload_bps),
    displayValue(item.online_duration, item.duration, item.uptime),
    displayValue(item.rx_bytes, item.in_bytes, item.download_bytes),
    displayValue(item.tx_bytes, item.out_bytes, item.upload_bytes)
  ];
}
function mapTopSessionRow(item) {
  const endpoint = [item.src || item.source_ip || item.ip, item.dst || item.destination_ip].filter(Boolean).join(' → ');
  return [endpoint || item.user || '', displayValue(item.sessions, item.connections, item.connection_count, item.bytes)];
}
function mapTopDomainRow(item) {
  return [item.domain || item.name || '', displayValue(item.last_seen, item.last_access, item.last_access_time), displayValue(item.hits, item.queries, item.count)];
}
function envelopeItems(payload) {
  if (Array.isArray(payload)) return payload;
  if (!payload || typeof payload !== 'object') return [];
  if (Array.isArray(payload.items)) return payload.items;
  if (Array.isArray(payload.data)) return payload.data;
  if (Array.isArray(payload.data?.items)) return payload.data.items;
  return [];
}
function displayValue(...values) {
  const value = values.find((item) => item !== undefined && item !== null && item !== '');
  return value === undefined ? '' : value;
}
function renderSettings(page) {
  if (isNetworkContentPage(page)) return renderNetworkSettings(page);
  if (page.id === 'system/sys_config') return systemConfigOperationsHtml();
  if (page.id === 'system/firmware_update') return `<div class="config-ops">${renderFirmwareOperationsHtml()}</div>`;
  return renderTable(page);
}
function renderSystemUserActions() {
	return '<div class="toolbar toolbar-action-right"><button class="primary" type="button" data-action="add">新增账号</button></div>';
}
function systemConfigOperationsHtml() {
	const manifest = state.controlPlane.configExport?.package_manifest;
	const exportState = manifest ? `<p class="config-export-state">最近导出包：${safeText(manifest.content_type)} · ${safeText(manifest.package_hash || '')}</p>` : '<p class="config-export-state">尚未读取配置导出包。</p>';
	const operations = [
		['配置导入', '导入主配置文件，导入过程中，网络会暂时中断', 'import'],
		['配置导出', '导出主配置文件，不包含本地账号，以及APP的配置', 'export'],
		['配置初始化', '', 'init']
	];
	const operationRows = operations.map(([title, description, action]) => `<section class="config-op config-quick-item"><strong>${safeText(title)}</strong>${description ? `<span>${safeText(description)}</span>` : ''}<button class="primary" type="button" data-action="${safeText(action)}">${safeText(title)}</button></section>`).join('');
	return `<div class="config-ops">${exportState}${renderManagementNetworkEditor()}<div class="config-quick-actions">${operationRows}</div><section class="config-op config-time-op"><div class="config-op-copy"><strong>系统时间</strong><span>使用当前浏览器时间保存</span></div><div class="config-time-controls"><input data-config-time-input value="${formatBrowserDateTime()}" aria-label="系统时间"><button class="primary" type="button" data-config-time-save>同步当前浏览器时间并保存</button></div></section>${renderFirmwareOperationsHtml()}</div>`;
}

function renderManagementNetworkEditor() {
	const item = state.controlPlane.managementNetwork?.item || state.controlPlane.managementNetwork || {};
	const iface = item.interface_id || 'eth0';
	const cidr = item.cidr || item.ip_cidr || '192.168.88.1/24';
	const gateway = item.gateway || '';
	const mode = item.mode === 'shared_lan' ? 'shared_lan' : 'exclusive';
	const interfaceRows = networkRowsForPage('monitor/interface_list');
	const options = interfaceRows.map((row) => row[0]).filter(Boolean);
	if (iface && !options.includes(iface)) options.unshift(iface);
	const interfaceOptions = options.map((value) => `<option value="${escapeAttr(value)}" ${value === iface ? 'selected' : ''}>${safeText(value)}</option>`).join('');
	return `<section class="config-op management-network-op"><div class="config-op-copy"><strong>管理口设置</strong><span>管理地址可以独占物理口，也可以与 LAN 共用。</span></div><div class="management-network-form"><fieldset class="management-mode"><legend>管理模式</legend><label><input type="radio" name="management-mode" data-management-mode value="exclusive" ${mode === 'exclusive' ? 'checked' : ''}>独占管理口</label><label><input type="radio" name="management-mode" data-management-mode value="shared_lan" ${mode === 'shared_lan' ? 'checked' : ''}>与 LAN 共享</label></fieldset><label>管理接口<select data-management-interface>${interfaceOptions}</select></label><label>IP/掩码<input data-management-cidr value="${safeText(cidr)}"></label><label>网关<input data-management-gateway value="${safeText(gateway)}"></label><button class="primary" type="button" data-action="management-save">保存管理口</button></div></section>`;
}

function renderFirmwareOperationsHtml() {
	const status = state.controlPlane.firmwareStatus?.status || state.controlPlane.firmwareStatus || null;
	const summary = status?.staged ? `已选择升级包：${safeText(status.image_path || '')}` : '请选择升级包。';
	const busy = state.controlPlane.firmwareBusy;
	return `<section class="config-op firmware-op"><div class="config-op-copy"><strong>应用在线升级</strong><span>选择升级包后自动校验，升级时自动重启控制服务。</span><small>${summary}</small></div><div class="firmware-controls"><input type="file" data-firmware-image accept=".tar.zst,.zst" ${busy ? 'disabled' : ''}><button class="primary" type="button" data-action="firmware-install" ${busy || !status?.staged ? 'disabled' : ''}>开始升级</button></div></section>`;
}
function renderConfigApplyResult() {
  const result = state.controlPlane.configApply;
  if (!result) return '<section class="config-apply-result is-empty"><strong>最近应用结果</strong><span>尚未执行应用配置。</span></section>';
  const status = result.status || 'unknown';
  const runtimeState = result.runtime_state || (status === 'committed' ? 'committed' : 'unknown');
  const failed = status === 'apply_failed' || runtimeState === 'degraded';
  const stateClass = status === 'committed' ? 'is-ok' : failed ? 'is-failed' : 'is-degraded';
  const reason = result.reason || (failed ? '后端未返回降级原因' : '');
  return `<section class="config-apply-result ${stateClass}">
    <div class="config-apply-head"><div><strong>最近应用结果</strong><span>状态 ${safeText(status)} · 运行态 ${safeText(runtimeState)}</span></div><code>${safeText(result.transaction_id || '无事务号')}</code></div>
    <dl class="config-apply-facts"><div><dt>transaction_id</dt><dd>${safeText(result.transaction_id || '无')}</dd></div>${reason ? `<div><dt>降级原因</dt><dd>${safeText(reason)}</dd></div>` : ''}</dl>
    ${renderConfigApplyRollback(result.rollback)}
    ${renderConfigApplyEvents(result.events)}
  </section>`;
}
function renderConfigApplyRollback(rollback) {
  if (!rollback) return '<div class="config-apply-evidence"><h3>回滚证据</h3><p>未返回回滚记录。</p></div>';
  const fields = [
    ['rollback_id', rollback.id],
    ['target_snapshot_id', rollback.target_snapshot_id],
    ['status', rollback.status],
    ['reason', rollback.reason],
    ['error', rollback.error],
    ['requested_at', rollback.requested_at],
    ['completed_at', rollback.completed_at]
  ].filter(([, value]) => value !== undefined && value !== null && value !== '');
  return `<div class="config-apply-evidence"><h3>回滚证据</h3><dl class="config-apply-facts">${fields.map(([label, value]) => `<div><dt>${safeText(label)}</dt><dd>${safeText(value)}</dd></div>`).join('')}</dl></div>`;
}
function renderConfigApplyEvents(events) {
  const items = Array.isArray(events) ? events : [];
  if (!items.length) return '<div class="config-apply-evidence"><h3>流水线事件</h3><p>未返回事件。</p></div>';
  return `<div class="config-apply-evidence"><h3>流水线事件</h3><ol class="config-apply-events">${items.map((event) => `<li><span>${safeText(event.timestamp || '')}</span><strong>${safeText(event.action || 'event')}</strong><em>${safeText(event.status || 'unknown')}</em>${event.error ? `<small>${safeText(event.error)}</small>` : ''}</li>`).join('')}</ol></div>`;
}
function formatBrowserDateTime(date = new Date()) {
  const pad = (value) => String(value).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}
function runtimeStatusPayload() {
  return state.controlPlane.runtimeStatus || { status: 'unavailable', components: [], error: '尚未读取运行态状态' };
}
function runtimeStateClass(value) {
  const stateValue = String(value || '').toLowerCase();
  if (['running', 'committed'].includes(stateValue)) return 'is-ok';
  if (['unavailable', 'apply_failed', 'compile_failed'].includes(stateValue)) return 'is-failed';
  return 'is-degraded';
}
function renderRuntimeChip(label, value, reason = '') {
  const stateValue = value || 'unknown';
  return `<span class="runtime-chip ${runtimeStateClass(stateValue)}"><strong>${safeText(label)}</strong><em>${safeText(stateValue)}</em>${reason ? `<small>${safeText(reason)}</small>` : ''}</span>`;
}
function renderRuntimePanel(placement = 'full') {
  const payload = runtimeStatusPayload();
  const busy = state.controlPlane.runtimeBusy;
  const last = state.controlPlane.runtimeApply || payload.last_apply;
  const summary = payload.error || (payload.status === 'running' ? 'Gateway 运行态组件正在运行。' : '存在降级或不可用组件，请查看原因后再应用。');
  const actions = `<div class="runtime-actions"><button type="button" data-action="runtime-preview" ${busy ? 'disabled' : ''}>预览运行态</button><button type="button" data-action="runtime-apply" ${busy ? 'disabled' : ''}>应用运行态</button>${busy ? `<span>正在执行：${safeText(busy)}</span>` : ''}</div>`;
  return `<section class="runtime-panel runtime-panel-${safeText(placement)}">
    <header class="runtime-panel-head"><div><strong>运行态操作</strong><span>${safeText(summary)} 组件健康以系统概况为准。</span></div>${renderRuntimeChip('status', payload.status || 'unavailable', payload.error || '')}</header>
    ${last ? renderRuntimeLastApply(last) : '<p class="runtime-empty">尚未执行运行态应用。</p>'}
    ${actions}
  </section>`;
}
function renderRuntimeOperationsHtml() {
  return `<div class="runtime-ops">
    ${renderRuntimePanel('full')}
    ${renderProxyRuntimePanel()}
    ${renderPPPoEStatusPanel()}
    ${renderRuntimePreview()}
  </div>`;
}
function renderProxyRuntimePanel() {
  const status = state.controlPlane.proxyStatus || { service: 'xray', state: 'unavailable', available: false, reason: '尚未读取 xray 状态' };
  const logs = state.controlPlane.proxyLogs?.logs || '';
  return `<section class="runtime-preview-block"><h3>xray 代理运行态</h3>${renderRuntimeChip(status.service || 'xray', status.state || (status.available ? 'available' : 'unavailable'), status.reason || '')}<pre class="runtime-log-preview">${safeText(logs || '暂无日志输出。')}</pre></section>`;
}
function renderPPPoEStatusPanel() {
  const status = state.controlPlane.pppoeStatus || { status: 'unavailable', peers: [], reason: '尚未读取 PPPoE 状态' };
  const peers = Array.isArray(status.peers) ? status.peers : [];
  const rows = peers.length ? peers.map((peer) => `<tr><td>${safeText(peer.id || peer.name || '')}</td><td>${safeText(peer.interface || peer.iface || '')}</td><td>${safeText(peer.state || peer.status || '')}</td><td>${safeText(peer.route_handoff?.state || peer.vpp_route_handoff?.state || '')}</td></tr>`).join('') : `<tr><td colspan="4">${safeText(status.reason || status.error || '暂无 PPPoE peer。')}</td></tr>`;
  return `<section class="runtime-preview-block"><h3>PPPoE 生产路径</h3>${renderRuntimeChip('pppoe', status.status || status.state || 'unavailable', status.reason || status.error || '')}<table class="data-table"><thead><tr><th>Peer</th><th>接口</th><th>状态</th><th>VPP 路由交接</th></tr></thead><tbody>${rows}</tbody></table></section>`;
}
function renderRuntimeLastApply(result) {
  const applied = result.applied_services || result.applied || [];
  const appliedText = applied.length ? applied.join(', ') : '无服务提交记录';
  const fields = [
    ['transaction_id', result.transaction_id],
    ['runtime_state', result.runtime_state],
    ['applied_services', appliedText],
    ['snapshot_hash', result.snapshot_hash],
    ['applied_at', result.applied_at]
  ].filter(([, value]) => value !== undefined && value !== null && value !== '');
  return `<section class="runtime-last ${runtimeStateClass(result.status || result.runtime_state)}"><header><strong>最近运行态应用</strong><span>${safeText(result.status || 'unknown')}${result.reason ? ` · ${safeText(result.reason)}` : ''}</span></header><dl>${fields.map(([label, value]) => `<div><dt>${safeText(label)}</dt><dd>${safeText(value)}</dd></div>`).join('')}</dl></section>`;
}
function renderRuntimePreview() {
  const preview = state.controlPlane.runtimePreview;
  if (!preview) return '<section class="runtime-preview is-empty"><header><strong>运行态预览</strong><span>点击“预览运行态”后显示编译计划，不会修改服务。</span></header></section>';
  const plan = preview.plan || {};
  return `<section class="runtime-preview">
    <header><strong>运行态预览</strong><span>状态 ${safeText(preview.status || 'preview')} · request ${safeText(preview.request_id || '无')}</span></header>
    ${renderRuntimePlanFacts(plan)}
    ${renderRuntimeArtifacts(plan.service_artifacts)}
    ${renderRuntimeVppOperations(plan.vpp_operations)}
    ${renderRuntimeDataplanePlans(plan)}
    ${renderRuntimeWarnings(plan.warnings)}
  </section>`;
}
function renderRuntimePlanFacts(plan) {
  const facts = [
    ['DNS策略', (plan.dns_policies || []).length],
    ['服务制品', (plan.service_artifacts || []).length],
    ['VPP操作', (plan.vpp_operations || []).length],
    ['DHCP服务', (plan.dhcp_servers || []).length],
    ['PPPoE对端', (plan.pppoe_peers || []).length],
    ['nftables/TProxy', plan.nftables_tproxy_plan ? '已编译' : '未返回'],
    ['Linux routing', plan.linux_policy_routing_plan ? '已编译' : '未返回']
  ];
  return `<dl class="runtime-plan-facts">${facts.map(([label, value]) => `<div><dt>${safeText(label)}</dt><dd>${safeText(value)}</dd></div>`).join('')}</dl>`;
}
function renderRuntimeArtifacts(artifacts) {
  const items = Array.isArray(artifacts) ? artifacts : [];
  if (!items.length) return '<div class="runtime-preview-block"><h3>服务制品</h3><p>未返回服务制品。</p></div>';
  return `<div class="runtime-preview-block"><h3>服务制品</h3><table class="runtime-mini-table"><thead><tr><th>服务</th><th>路径</th><th>重载</th><th>hash</th></tr></thead><tbody>${items.map((item) => `<tr><td>${safeText(item.service || '')}</td><td>${safeText(item.path || '')}</td><td>${safeText(item.reload_mode || '')}</td><td>${safeText(shortHash(item.content_hash))}</td></tr>`).join('')}</tbody></table></div>`;
}
function renderRuntimeVppOperations(operations) {
  const items = Array.isArray(operations) ? operations : [];
  if (!items.length) return '<div class="runtime-preview-block"><h3>VPP操作</h3><p>未返回 VPP 操作。</p></div>';
  return `<div class="runtime-preview-block"><h3>VPP操作</h3><table class="runtime-mini-table"><thead><tr><th>名称</th><th>资源</th><th>request_id</th></tr></thead><tbody>${items.map((item) => `<tr><td>${safeText(item.Name || item.name || '')}</td><td>${safeText(item.Resource || item.resource || '')}</td><td>${safeText(item.RequestID || item.request_id || '')}</td></tr>`).join('')}</tbody></table></div>`;
}
function renderRuntimeDataplanePlans(plan) {
  const nft = plan.nftables_tproxy_plan || {};
  const linux = plan.linux_policy_routing_plan || {};
  const nftFacts = [
    ['family', nft.family],
    ['table', nft.table],
    ['target_port', nft.target_port],
    ['mark', nft.mark],
    ['chains', Array.isArray(nft.chains) ? nft.chains.length : ''],
    ['rules', Array.isArray(nft.rules) ? nft.rules.length : '']
  ];
  const route = linux.default_route || {};
  const linuxFacts = [
    ['table', linux.table_name || linux.table_id],
    ['rule_priority', linux.rule_priority],
    ['mark', linux.mark],
    ['default_route', [route.destination, route.via ? `via ${route.via}` : '', route.device ? `dev ${route.device}` : ''].filter(Boolean).join(' ')],
    ['underlay', linux.underlay ? `${linux.underlay.kind || ''}:${linux.underlay.id || ''}` : '']
  ];
  return `<div class="runtime-preview-block"><h3>nftables/TProxy 与 Linux 策略路由</h3><div class="runtime-plan-pair"><section><strong>nftables/TProxy</strong>${renderRuntimeFactList(nftFacts)}</section><section><strong>Linux routing</strong>${renderRuntimeFactList(linuxFacts)}</section></div></div>`;
}
function renderRuntimeFactList(facts) {
  const items = facts.filter(([, value]) => value !== undefined && value !== null && value !== '');
  if (!items.length) return '<p>未返回计划明细。</p>';
  return `<dl class="runtime-plan-facts">${items.map(([label, value]) => `<div><dt>${safeText(label)}</dt><dd>${safeText(value)}</dd></div>`).join('')}</dl>`;
}
function renderRuntimeWarnings(warnings) {
  const items = Array.isArray(warnings) ? warnings : [];
  if (!items.length) return '';
  return `<div class="runtime-preview-block is-warning"><h3>预览警告</h3><ul>${items.map((warning) => `<li>${safeText(warning)}</li>`).join('')}</ul></div>`;
}
function shortHash(value) {
  const text = String(value || '');
  return text.length > 12 ? `${text.slice(0, 12)}...` : text;
}
function renderTabsPage(page) {
  if (isNetworkContentPage(page)) return renderNetworkTabsPage(page);
  return `<div class="tabbar"><button class="tab is-active" type="button">IPv4</button><button class="tab" type="button">NAT</button><button class="tab" type="button">策略路由</button><button class="tab" type="button">日志</button></div><div class="canvas-box">${safeText(page.title)} 的多标签内容区：表格 / 规则 / 日志集中管理</div>${renderTable(page)}`;
}
function renderNetworkTabs(page) {
  const config = networkPages[page.id];
  const inlineAddLabel = networkInlineAddLabel(page);
  const primaryAction = renderNetworkPrimaryAction(page);
  if (!config.tabs && inlineAddLabel) return `<div class="network-tab-row"><div class="tabbar network-tabbar"><button class="tab is-active" type="button">${safeText(page.title)}</button></div>${primaryAction}</div>`;
  if (!config.tabs) return '';
  const largeTabSections = ['network', 'behavior', 'object'];
  const tabClass = largeTabSections.includes(page.sectionId) ? 'tabbar network-tabbar' : 'tabbar';
  const activeIndex = state.activeTabs[page.id] || 0;
  return `<div class="network-tab-row"><div class="${tabClass}">${config.tabs.map((tab, index) => `<button class="tab ${index === activeIndex ? 'is-active' : ''}" data-tab-index="${index}" type="button">${safeText(tab)}</button>`).join('')}</div>${primaryAction}</div>`;
}
function renderNetworkPrimaryAction(page) {
  if (!canMutateConfig()) return '';
  const activeIndex = state.activeTabs[page.id] || 0;
  const actions = {
    'monitor/interface_list': ['add', '创建聚合'],
    'network/wangroup_manager': ['add-line', '新增群组'],
    'route/route_policy_main': ['add', '新增策略'],
    'route/portmap_list': ['add', '新增映射'],
    'route/dnspolicy_main': ['add', '新增管控'],
    'flowcontrol/flowct_main': ['add', '新增'],
    'object/urlgrp_list': ['add', '新增域名组'],
    'object/iptab_list': ['add', '新增IP组'],
  };
  if (page.id === 'network/proxy_main') {
    if (activeIndex === 2) return '';
    return `<button class="network-primary-action primary" data-action="add" type="button">${activeIndex === 1 ? '新增WAN' : '新增LAN'}</button>`;
  }
  if (page.id === 'network/dhcpsvr_main') actions[page.id] = ['add', dhcpAddLabel(page)];
  if (page.id === 'object/urlgrp_list' && activeIndex === 1) actions[page.id] = null;
  if (page.id === 'object/iptab_list' && activeIndex === 1) actions[page.id] = null;
  const action = actions[page.id];
  if (action?.[0] === 'view') return `<button class="network-primary-action primary" type="button">${safeText(action[1])}</button>`;
  return action ? `<button class="network-primary-action primary" data-action="${action[0]}" type="button">${safeText(action[1])}</button>` : '';
}
function networkInlineAddLabel(page) {
  return ({
    'route/portmap_list': '新增映射',
    'route/dnspolicy_main': '新增管控',
    'flowcontrol/flowct_main': '新增'
  })[page.id] || '';
}
function dhcpActiveTabIndex(page) {
  return state.activeTabs[page.id] || 0;
}
function dhcpAddLabel(page) {
  return dhcpActiveTabIndex(page) === 1 ? '新增静态分配' : '新增服务';
}
function dhcpAddTitle(page) {
  return dhcpActiveTabIndex(page) === 1 ? '新增静态分配' : '新增DHCP服务';
}
function renderNetworkActions(page) {
  if (!canMutateConfig()) return '';
  const config = networkPages[page.id];
  const inlineAddLabel = networkInlineAddLabel(page);
  const filters = (config.filters || []).map(([label, value]) => {
    if (Array.isArray(value)) return `<label>${safeText(label)}<select>${value.map((item) => `<option>${safeText(item)}</option>`).join('')}</select></label>`;
    return `<label>${safeText(label)}<input value="${safeText(value)}"></label>`;
  }).join('');
  const buttons = (config.actions || ['新增', '批量操作', '删除']).filter((label) => label !== inlineAddLabel).map((label) => {
    const action = label.includes('删除') ? 'delete' : label.includes('批量操作') ? 'batch' : label.includes('导入') ? 'import' : label.includes('编辑') ? 'edit' : label.includes('导出') ? 'export' : label.includes('移出') ? 'remove' : label.includes('转移') ? 'transfer' : 'add';
    return ['batch', 'delete'].includes(action) ? `<button type="button" data-action="${action}">${safeText(label)}</button>` : '';
  }).join('');
  const batch = state.batchOpen ? `<span class="pager-total">已选择 ${state.checkedRows.size} 条，可执行转移、移出或删除</span>` : '';
  return `<div class="toolbar">${filters}${buttons}${batch}</div>`;
}

function canMutateConfig() {
  const capabilities = capabilityItems();
  const persistence = capabilities.find((item) => item.name === 'persistence');
  return !persistence || persistence.available !== false;
}
function renderCapabilityNotice(name, label) {
  const item = capabilityItems().find((capability) => capability.name === name);
  if (!item || item.available !== false) return '';
  return `<div class="capability-notice"><strong>${safeText(label)}暂未启用</strong><span>完成服务部署后将自动接入；详细状态请在运行态操作中查看。</span></div>`;
}
function renderNetworkSettings(page) {
  const config = networkPages[page.id];
  if (page.id === 'network/proxy_main') return `${renderNetworkTabs(page)}${renderProxyMainContent(page)}`;
  const formFields = config.settings || config.form || [];
  return `${renderNetworkTabs(page)}${renderNetworkSummary(config)}${formFields.length ? renderNetworkForm(formFields) : ''}${config.note ? `<div class="canvas-box">${safeText(config.note)}</div>` : ''}${renderNetworkTable(page)}`;
}
function renderProxyMainContent(page) {
  return (state.activeTabs[page.id] || 0) === 2 ? renderProxyLogTable(page) : renderNetworkTable(page);
}
function renderNetworkTabsPage(page) {
  const config = networkPages[page.id];
  return `${renderNetworkTabs(page)}${config.description ? `<div class="canvas-box">${safeText(config.description)}</div>` : ''}${renderNetworkTable(page)}`;
}
function renderNetworkTablePage(page) {
  const config = networkPages[page.id];
  return `${renderNetworkTabs(page)}${config.note ? `<div class="canvas-box">${safeText(config.note)}</div>` : ''}${renderNetworkTable(page)}`;
}
function renderSmartQoSStatus(payload) {
  const item = payload?.item || payload || {};
  const labels = { running: '运行中', adapter_pending: '适配器待实现', locked: '能力锁定' };
  const runtimeState = item.runtime_state || 'locked';
  const tone = runtimeState === 'running' ? 'is-running' : runtimeState === 'adapter_pending' ? 'is-pending' : 'is-locked';
  const tier = item.selected_dataplane_tier || '未选定';
  return `<section class="smart-qos-status ${tone}" aria-label="内置智能 QoS 状态"><div><strong>内置智能 QoS</strong><span>系统内置 · 不可修改</span></div><dl><div><dt>运行状态</dt><dd>${safeText(labels[runtimeState] || runtimeState)}</dd></div><div><dt>数据路径</dt><dd>${safeText(tier)}</dd></div></dl><p>${safeText(item.reason || '尚未取得能力探测结果')}</p></section>`;
}
function renderNetworkSummary(config) {
  if (!config.summary) return '';
  return `<div class="dashboard-grid">${config.summary.map(([label, value]) => `<div class="snap"><span>${safeText(label)}</span><strong>${safeText(value)}</strong></div>`).join('')}</div>`;
}
function renderNetworkStateBand(page) {
  const resources = state.controlPlane.resources || {};
  const rows = networkRowsForPage(page.id);
  const labels = ({
    'monitor/interface_list': ['接口总数', rows.length, '物理口与聚合组'],
    'network/proxy_main': ['已配置接口', rows.length, 'LAN/WAN 逻辑接口'],
    'network/wangroup_manager': ['线路组', rows.length, '主备、加权、五元组'],
    'route/route_policy_main': ['策略规则', rows.length, '按优先级匹配'],
    'route/portmap_list': ['端口映射', rows.length, 'NAT 入站规则'],
    'route/dnspolicy_main': ['DNS 策略', rows.length, '固定线路优先'],
    'network/dhcpsvr_main': ['配置项', rows.length, 'Kea 服务与静态分配'],
    'flowcontrol/flowct_main': ['限速规则', rows.length, '用户限速与智能 QoS'],
  })[page.id];
  if (!labels) return '';
  const enabled = rows.filter((item) => item?.enabled !== false && item?.status !== 'disabled').length;
  return `<section class="gateway-state-band"><div><span>${safeText(labels[0])}</span><strong>${safeText(labels[1])}</strong><small>${safeText(labels[2])}</small></div><div><span>当前启用</span><strong>${safeText(enabled)}</strong><small>已启用配置</small></div><div><span>更新方式</span><strong>自动</strong><small>每 5 秒同步</small></div></section>`;
}
function renderNetworkForm(fields = []) {
  return `<div class="form-grid">${fields.map(([label, value]) => `<label>${safeText(label)}<input value="${safeText(value)}"></label>`).join('')}</div>`;
}
function renderNetworkTable(page) {
  const config = networkPages[page.id];
  if (page.id === 'monitor/interface_list') return renderInterfaceTable(page);
  if (page.id === 'network/proxy_main') return renderProxyInterfaceTable(page);
  if (page.id === 'network/dhcpsvr_main') return renderDhcpTable(page);
  if (page.id === 'object/urlgrp_list') return renderDomainGroupContent(page);
  if (page.id === 'object/iptab_list') return renderIpGroupContent(page);
  const rowEntries = networkTableRowEntries(page);
  const tableClass = page.id === 'monitor/interface_list' ? 'data-table nic-table' : 'data-table';
  const canMutate = canMutateConfig();
  const actionButtons = page.id === 'monitor/interface_list'
    ? (index) => `<button class="link-btn" data-row-action="edit" data-row="${index}" type="button">编辑</button>`
    : page.id === 'network/proxy_main' || page.id === 'network/wangroup_manager'
      ? (index) => `<button class="link-btn" data-row-action="edit" data-row="${index}" type="button">编辑</button><button class="link-btn" data-row-action="delete" data-row="${index}" type="button">删除</button>`
      : (index) => `<button class="link-btn" data-row-action="edit" data-row="${index}" type="button">编辑</button><button class="link-btn" data-row-action="delete" data-row="${index}" type="button">删除</button>`;
  const colspan = config.columns.length + 1 + (canMutate ? 1 : 0);
  const rowsHTML = rowEntries.map(({ row, index }) => `<tr><td><input data-row-check="${index}" type="checkbox" ${canMutate ? '' : 'disabled'} ${state.checkedRows.has(index) ? 'checked' : ''}></td>${row.map((value, colIndex) => `<td>${renderNetworkCell(value, page, colIndex)}</td>`).join('')}${canMutate ? `<td>${actionButtons(index)}</td>` : ''}</tr>`).join('');
  return `<table class="${tableClass}"><thead><tr><th><input data-select-all type="checkbox" ${canMutate ? '' : 'disabled'}></th>${config.columns.map((col) => `<th>${safeText(col)}</th>`).join('')}${canMutate ? '<th>操作</th>' : ''}</tr></thead><tbody>${renderFixedTableRows(rowsHTML, rowEntries.length, colspan)}</tbody></table>`;
}
function renderDomainGroupContent(page) {
  return (state.activeTabs[page.id] || 0) === 1 ? renderDomainGroupDisplay(page) : renderObjectGroupTable(page);
}
function renderIpGroupContent(page) {
  return (state.activeTabs[page.id] || 0) === 1 ? renderIpGroupDisplay(page) : renderObjectGroupTable(page);
}
function renderObjectGroupTable(page) {
  const config = networkPages[page.id];
  const rowEntries = networkTableRowEntries(page);
  const actionButtons = (index) => `<button class="link-btn" data-row-action="edit" data-row="${index}" type="button">编辑</button><button class="link-btn" data-row-action="delete" data-row="${index}" type="button">删除</button>`;
  const rowsHTML = rowEntries.map(({ row, index }) => `<tr><td><input data-row-check="${index}" type="checkbox" ${state.checkedRows.has(index) ? 'checked' : ''}></td>${row.slice(0, config.columns.length).map((value, colIndex) => `<td>${renderNetworkCell(value, page, colIndex)}</td>`).join('')}<td>${actionButtons(index)}</td></tr>`).join('');
  return `<table class="data-table"><thead><tr><th><input data-select-all type="checkbox"></th>${config.columns.map((col) => `<th>${safeText(col)}</th>`).join('')}<th>操作</th></tr></thead><tbody>${renderFixedTableRows(rowsHTML, rowEntries.length, config.columns.length + 2)}</tbody></table>`;
}
function renderDomainGroupDisplay(page) {
  const rows = networkRowsForPage(page.id);
  const selectedGroup = selectedDomainGroupName(page);
  const members = domainGroupMembers(selectedGroup).split('\\n').filter((line) => line.trim());
  const tableRows = members.map((member) => `<tr><td>${safeText(member)}</td><td>${safeText(selectedGroup)}</td></tr>`).join('');
  return `<div class="ip-display-workspace object-display-workspace"><div class="object-display-toolbar"><label>查看域名组<select data-domain-group-select>${rows.map((row) => row[0]).filter(Boolean).map((group) => `<option value="${escapeAttr(group)}" ${group === selectedGroup ? 'selected' : ''}>${safeText(group)}</option>`).join('')}</select></label></div><table class="data-table ip-display-table"><thead><tr><th>域名内容</th><th>所属组</th></tr></thead><tbody>${renderFixedTableRows(tableRows, members.length, 2, '请选择域名组')}</tbody></table></div>`;
}
function renderIpGroupDisplay(page) {
  const rows = networkRowsForPage(page.id);
  const selectedGroup = selectedIpGroupName(page);
  const groupButtons = rows.map((row, index) => `<button class="object-group-name ${row[0] === selectedGroup ? 'is-selected' : ''}" data-ip-group="${safeText(row[0])}" data-row="${index}" type="button"><span>${safeText(row[0])}</span><em>${ipGroupMembers(row[0]).split('\n').filter(Boolean).length} 条</em></button>`).join('');
  const members = ipGroupMembers(selectedGroup).split('\n').filter((line) => line.trim());
  const tableRows = members.map((member) => `<tr><td>${safeText(member)}</td><td>${safeText(selectedGroup)}</td></tr>`).join('');
	return `<div class="ip-display-workspace object-display-workspace"><div class="object-display-toolbar"><label>查看IP组<select data-ip-group-select>${rows.map((row) => row[0]).filter(Boolean).map((group) => `<option value="${escapeAttr(group)}" ${group === selectedGroup ? 'selected' : ''}>${safeText(group)}</option>`).join('')}</select></label></div><table class="data-table ip-display-table"><thead><tr><th>IP内容</th><th>所属组</th></tr></thead><tbody>${renderFixedTableRows(tableRows, members.length, 2, '请选择 IP 组')}</tbody></table></div>`;
}
function renderObjectGroupDisplay(page, options) {
  const rows = networkRowsForPage(page.id);
  const selectedGroup = options.selectedGroup;
  const groupButtons = rows.map((row, index) => {
    const groupName = row[0];
    const memberCount = options.memberGetter(groupName).split('\n').filter((line) => line.trim()).length;
    const selected = groupName === selectedGroup;
    return `<button class="domain-group-name object-group-name ${selected ? 'is-selected' : ''}" ${options.selectedAttr}="${safeText(groupName)}" data-row="${index}" type="button" aria-pressed="${selected ? 'true' : 'false'}"><span>${safeText(groupName)}</span><em>${memberCount} 条</em></button>`;
  }).join('');
  return `<div class="domain-display-workspace object-display-workspace"><section class="domain-display-groups object-display-groups" aria-label="群组名称"><header>群组名称</header><div class="object-group-list">${groupButtons}</div></section>${renderObjectGroupContentPanel(selectedGroup, options)}</div>`;
}
function selectedDomainGroupName(page) {
  const names = networkRowsForPage(page.id).map((row) => row[0]);
  return names.includes(state.selectedDomainGroup) ? state.selectedDomainGroup : names[0] || '';
}
function selectedIpGroupName(page) {
  const names = networkRowsForPage(page.id).map((row) => row[0]);
  return names.includes(state.selectedIpGroup) ? state.selectedIpGroup : names[0] || '';
}
function renderObjectGroupContentPanel(groupName, options) {
  const members = options.memberGetter(groupName).split('\n').filter((line) => line.trim());
  const rows = members.map((member, index) => `<div class="domain-content-row object-content-row"><span>${String(index + 1).padStart(2, '0')}</span><strong>${safeText(member)}</strong></div>`).join('');
  return `<section class="domain-display-panel object-display-panel" aria-label="${safeText(options.contentLabel)}"><header><span>${safeText(options.contentLabel)}</span><strong>${safeText(groupName)}</strong><em>${members.length} 条</em><small>${safeText(options.memberLabel)}</small></header><div class="domain-content-lines object-content-list">${rows}</div></section>`;
}
function networkRowsForPage(pageId) {
  const rows = resourceRowsForPage(pageId);
  return rows.length ? rows : (isMockMode() ? networkPages[pageId].rows : []);
}

function isMockMode() {
  const search = new URLSearchParams(window.location.search);
  return document.body?.dataset.lyRouteMock === '1' || search.get('mock') === '1' || window.LY_ROUTE_MOCK === true;
}
function resourceRowsForPage(pageId) {
  const resources = state.controlPlane.resources || {};
  const mappers = {
    'monitor/interface_list': () => interfaceListRows(resources),
    'network/proxy_main': () => [
      ...envelopeItems(resources.interfaces).map((item) => withRowResource(mapProxyInterfaceRow(item), 'interfaces', item)),
      ...envelopeItems(resources.wanLinks).map((item) => withRowResource(mapWanLinkRow(item), 'wanLinks', item)),
      ...envelopeItems(resources.proxyEgresses).filter((item) => userProxyEgressVisible(item)).map((item) => withRowResource(mapProxyEgressRow(item), 'proxyEgresses', item))
    ],
    'network/wangroup_manager': () => envelopeItems(resources.wanGroups).map((item) => withRowResource(mapWanGroupRow(item), 'wanGroups', item)),
    'route/route_policy_main': () => envelopeItems(resources.routePolicies).map((item, index) => withRowResource(mapRoutePolicyRow(item, index), 'routePolicies', item)),
    'route/portmap_list': () => envelopeItems(resources.portMaps).map((item) => withRowResource(mapPortMapRow(item), 'portMaps', item)),
    'route/dnspolicy_main': () => envelopeItems(resources.dnsPolicies).map((item, index) => withRowResource(mapDnsPolicyRow(item, index), 'dnsPolicies', item)),
    'network/dhcpsvr_main': () => [...envelopeItems(resources.dhcpServers).map((item) => withRowResource(mapDhcpServerRow(item), 'dhcpServers', item)), ...envelopeItems(resources.dhcpBindings).map((item) => withRowResource(mapDhcpBindingRow(item), 'dhcpBindings', item))],
    'object/urlgrp_list': () => envelopeItems(resources.objectGroups).filter((item) => objectGroupType(item) === 'domain').map((item) => withRowResource(mapObjectGroupRow(item), 'objectGroups', item)),
    'object/iptab_list': () => envelopeItems(resources.objectGroups).filter((item) => objectGroupType(item) === 'ip').map((item) => withRowResource(mapObjectGroupRow(item), 'objectGroups', item)),
    'flowcontrol/flowct_main': () => envelopeItems(resources.trafficControl).map((item, index) => withRowResource(mapTrafficControlRow(item, index), 'trafficControl', item))
  };
  return mappers[pageId]?.() || [];
}
function withRowResource(row, resourceKey, item) {
  row._resourceKey = resourceKey;
  row._resourceId = item?.id || item?.name || '';
  row._systemId = item?.system_name || item?.interface_id || item?.id || item?.name || '';
  row._resourceKind = item?.kind || item?.type || '';
  row._wanType = resourceKey === 'proxyEgresses' ? 'proxy' : resourceKey === 'wanLinks' ? (item?.type || item?.wan_type || '') : '';
  row._resource = item || null;
  return row;
}
function objectGroupType(item) {
  return item.type || item.group_type || item.kind || '';
}
function interfaceListRows(resources) {
  const bondedMembers = new Set(envelopeItems(resources.interfaceBonds).flatMap((item) => stringList(item.members)));
  const visibleInterfaces = envelopeItems(resources.interfaces).filter((item) => {
    const ids = [item.id, item.name, item.interface_id, item.system_name].filter(Boolean).map(String);
    return !ids.some((id) => bondedMembers.has(id));
  });
  return [
    ...visibleInterfaces.map((item) => withRowResource(mapInterfaceRow(item), 'interfaces', item)),
    ...envelopeItems(resources.interfaceBonds).map((item) => withRowResource(mapInterfaceBondRow(item), 'interfaceBonds', item))
  ];
}
function mapInterfaceRow(item) {
  const role = item.mode_role?.gateway || item.gateway_role || '';
  const direction = role === 'management' ? '管理口' : role === 'wan' ? 'WAN接口' : role === 'lan' ? 'LAN接口' : '未配置';
  const bondName = interfaceBondNameForItem(item);
  const workMode = interfaceWorkModeForItem(item);
  const displayName = item.display_name || item.alias || item.name || item.id || '';
  return [
    displayName,
    item.link_state === 'down' || item.admin_state === 'down' ? 'LINKDOWN' : 'LINKUP',
    displayWorkMode(workMode),
    direction,
    bondName || '',
    displayValue(item.rx_bps, item.stats?.rx_bps),
    displayValue(item.tx_bps, item.stats?.tx_bps),
    displayValue(item.rx_pps, item.stats?.rx_pps),
    displayValue(item.tx_pps, item.stats?.tx_pps)
  ];
}
function interfaceWorkModeForItem(item) {
  const ids = new Set([item.id, item.name, item.interface_id, item.system_name].filter(Boolean).map(String));
  const bond = envelopeItems(state.controlPlane.resources?.interfaceBonds).find((entry) => stringList(entry.members).some((member) => ids.has(member)));
  if (bond?.work_mode) return bond.work_mode;
  if (item.vpp_interface && (item.work_mode === 'kernel_stack' || item.active_path === 'kernel_stack')) return 'af_xdp';
  return item.work_mode || item.active_path || '';
}
function interfaceBondNameForItem(item) {
  const direct = item.bond_id || item.member_of || item.lag || item.bond || '';
  if (direct) return direct;
  const ids = new Set([item.id, item.name, item.interface_id, item.system_name].filter(Boolean).map(String));
  const bond = envelopeItems(state.controlPlane.resources?.interfaceBonds).find((entry) => stringList(entry.members).some((member) => ids.has(member)));
  return bond ? displayBondName(bond) : '';
}
function stringList(value) {
  if (Array.isArray(value)) return value.map(String).filter(Boolean);
  if (typeof value === 'string') return value.split(/[\n,]/).map((item) => item.trim()).filter(Boolean);
  return [];
}
function displayBondName(item) {
  return String(item.name || item.id || '未命名聚合组');
}
function displayWorkMode(value) {
  const text = String(value || '').trim().toLowerCase();
  if (!text) return '未识别';
  if (text === 'kernel_stack') return '内核管理通道';
  if (text === 'af_xdp') return 'XDP快速路径';
  if (text === 'xdp') return 'XDP快速路径';
  if (text === 'vpp') return 'VPP高速转发';
  if (text === 'linux') return 'Linux普通转发';
  if (text === 'dpdk') return 'DPDK高速转发';
  if (text === 'bridge') return '桥接转发';
  return value;
}
function mapInterfaceBondRow(item) {
  const members = stringList(item.members);
  const stateText = item.link_state === 'down' ? 'LINKDOWN' : 'LINKUP';
  const role = item.mode_role?.gateway || item.gateway_role || '';
  const direction = role === 'wan' ? 'WAN接口' : role === 'lan' ? 'LAN接口' : '未配置';
  return [displayBondName(item), stateText, displayWorkMode(item.work_mode || 'vpp'), direction, `成员 ${members.join(', ') || '未选择'}`, displayValue(item.rx_bps), displayValue(item.tx_bps), displayValue(item.rx_pps), displayValue(item.tx_pps)];
}
function mapProxyInterfaceRow(item) {
  const row = mapInterfaceRow(item);
  const members = interfaceMembersText(item);
  const hasInterfaceParams = Boolean(displayValue(item.cidr, item.ip_cidr, item.ip, item.address, item.gateway, item.dns, item.dns_servers, item.mtu, item.bandwidth));
  const type = item.kind === 'lan_bridge' || item.type === 'lan_bridge' ? 'LAN桥' : row[3] === 'WAN接口' ? 'WAN线路' : row[3] === 'LAN接口' ? 'LAN接口' : item.role_configured && !hasInterfaceParams ? '' : '';
  return [row[0], type, displayValue(item.cidr, item.ip_cidr, item.ip, item.address), displayValue(item.gateway), displayValue(item.dns, item.dns_servers), item.nat === false ? '禁用' : '启用', displayValue(item.mtu), displayValue(item.bandwidth), row[5], row[6], displayValue(item.sessions, item.connection_count), members, displayValue(item.description, item.remark, item.notes)];
}
function interfaceMembersText(item) {
  const members = item.bridge_members || item.members || [];
  return Array.isArray(members) ? members.join('\n') : String(members || '');
}
function mapWanLinkRow(item) {
  const wanType = item.type || item.wan_type || '';
  return [item.name || item.interface_id || item.id || '', 'WAN线路', displayValue(item.cidr, item.ip_cidr, item.address), displayValue(item.gateway), displayValue(item.dns, item.dns_servers), item.enabled === false ? '禁用' : '启用', displayValue(item.mtu), displayValue(item.bandwidth), displayValue(item.rx_bps), displayValue(item.tx_bps), displayValue(item.sessions), displayValue(item.description, item.remark, item.notes), wanLineTypeLabel(wanType)];
}
function mapProxyEgressRow(item) {
	const underlay = displayValue(item.underlay_wan_id, item.underlay, '未选择承载出口');
	return [item.name || item.id || '', 'WAN线路', `承载 ${underlay}`, underlay, displayValue(item.proxy_profile_id, item.runtime_profile), item.enabled === false ? '禁用' : '启用', '', '', displayValue(item.rx_bps), displayValue(item.tx_bps), displayValue(item.sessions), displayValue(item.description, item.remark, item.notes), wanLineTypeLabel('proxy')];
}

function userProxyEgressVisible(item) {
  const id = String(item?.id || '').trim().toLowerCase();
  if (item?.deleted === true) return false;
  if (id === 'proxy-egress-default') return false;
  return Boolean(String(item?.underlay_wan_id || item?.underlay || '').trim());
}

function wanLineTypeLabel(value) {
  const normalized = String(value || '').trim().toLowerCase();
  return ({ static: '固定 IPv4', static4: '固定 IPv4', static6: '固定 IPv6', dhcp: 'DHCP IPv4', dhcp4: 'DHCP IPv4', dhcp6: 'DHCP IPv6', pppoe: 'PPPoE', proxy: '代理线路' })[normalized] || displayValue(value, '未设置');
}

function wanFormType(value) {
  const normalized = String(value || '').trim().toLowerCase();
  return ({ static: 'static4', dhcp: 'dhcp4' })[normalized] || normalized || 'static4';
}

function wanConfigFamily(value) {
  const normalized = String(value || '').trim().toLowerCase();
  if (normalized.endsWith('6')) return 'ipv6';
  if (normalized === 'proxy') return 'proxy';
  return 'ipv4';
}
function mapWanGroupRow(item) {
  const members = wanGroupMembers(item);
  const mode = item.load_balance_mode || item.mode || item.strategy || 'flow_hash';
  const hash = item.hash_fields || item.load_balance || item.hash || 'src-dst-ip-port';
  return [item.name || item.id || '', members.length ? members.join(', ') : '未选择线路', wanGroupModeLabel(mode), wanGroupHashLabel(hash), displayValue(item.health, item.status), item.description || item.remark || ''];
}

function wanGroupMembers(item) {
  const members = item.wan_members || item.members || [];
  return Array.isArray(members) ? members.filter(Boolean) : String(members || '').split(',').map((value) => value.trim()).filter(Boolean);
}

function wanGroupModeLabel(value) {
  return ({ flow_hash: '五元组负载', five_tuple: '五元组负载', weighted: '加权负载', primary_backup: '主备模式', failover: '主备模式' })[String(value || '').trim()] || displayValue(value, '主备模式');
}

function wanGroupHashLabel(value) {
  return ({ five_tuple: '五元组', 'src-dst-ip-port': '源/目的IP+端口', 'src-ip': '源IP', 'dst-ip': '目的IP', 'src-dst-ip': '源/目的IP' })[String(value || '').trim()] || displayValue(value, '五元组');
}
function mapRoutePolicyRow(item, index) {
  const match = item.match || {};
  return [displayValue(item.priority, index + 1), item.action || item.kind || '', stringList(match.sources || match.src_ip || item.source).join('\n'), match.src_port || stringList(match.source_ports).join('\n'), stringList(match.destinations || match.dst_ip || item.destination).join('\n'), match.dst_port || stringList(match.dest_ports).join('\n') || match.protocol || '', item.egress || item.target_line || item.wan_group || '', item.next_hop || '', displayValue(item.hits, item.hit_count), item.name || item.id || ''];
}
function mapPortMapRow(item) {
  return [item.name || item.id || '', displayValue(item.wan_link, item.interface_id, item.egress), displayValue(item.external_port, item.public_port, item.src_port), displayValue(item.internal_host, item.private_ip, item.dst_ip), displayValue(item.internal_port, item.private_port, item.dst_port), String(item.protocol || 'TCP').toUpperCase(), displayValue(item.sessions, item.connection_count), item.description || item.remark || ''];
}
function mapDnsPolicyRow(item, index) {
  const rule = item.policy?.rules?.[0] || item.rules?.[0] || {};
  const outcome = rule.outcome || item.outcome || {};
  const action = outcome.kind === 'reject' ? '拉黑' : outcome.kind === 'fixed_answer' ? '解析' : item.action || '放行';
  return [displayValue(item.priority, index + 1), displayValue(item.source_ip, item.src_ip, rule.source_prefixes?.join('\n')), (item.domains || rule.domains || [item.name]).filter(Boolean).join('\n'), (item.upstreams || []).join('\n'), displayValue(item.resolve_address, item.redirect_to, outcome.fixed_answers?.join('\n')), action];
}
function mapDhcpServerRow(item) {
  return ['服务列表', item.interface_id || item.name || item.id || '', displayValue(item.subnet, item.pool_start && item.pool_end ? `${item.pool_start}-${item.pool_end}` : ''), item.gateway || '', '默认 DNS 劫持', displayValue(item.lease_time_seconds), item.enabled === false ? '禁用' : '启用', item.name || item.id || ''];
}
function mapDhcpBindingRow(item) {
  return ['静态分配', item.name || item.id || '', item.ip || '', '', item.mac || '', displayValue(item.lease_time_seconds), item.enabled === false ? '禁用' : '启用', item.description || ''];
}
function mapObjectGroupRow(item) {
  return [item.name || item.id || '', item.description || '', (item.members || []).length, (item.nested_groups || []).join(', '), displayValue(item.updated_at, item.modified_at), item.enabled === false ? '禁用' : '启用', (item.members || []).join('\n')];
}
function mapTrafficControlRow(item, index) {
  const rule = item.rules?.[0] || item;
  const match = rule.match || {};
  const action = rule.actions?.[0] || {};
  return [displayValue(item.priority, index + 1), [match.sources, match.destinations].flat().filter(Boolean).join('\n'), [match.source_ports, match.dest_ports].flat().filter(Boolean).join('\n'), action.kind || '', match.direction || '', displayValue(action.policer?.rate_bps, item.rate_bps), item.enabled === false ? '禁用' : '启用'];
}
function networkTableRowEntries(page) {
  const rowEntries = networkRowsForPage(page.id).map((row, index) => ({ row, index }));
  if (page.id !== 'route/route_policy_main') return rowEntries;
  const activeIndex = state.activeTabs[page.id] || 0;
  return rowEntries.filter(({ row }) => isRouteIpv6Row(row) === (activeIndex === 1));
}
function isRouteIpv6Row(row) {
  return row.some((value) => String(value).includes(':')) || String(row[1]).toLowerCase().includes('v6');
}
function renderDhcpTable(page) {
  const config = networkPages[page.id];
  const rows = dhcpRows(page);
  const rowsHTML = rows.map(({ row, index }) => `<tr><td><input data-row-check="${index}" type="checkbox" ${state.checkedRows.has(index) ? 'checked' : ''}></td>${row.map((value, colIndex) => `<td>${renderNetworkCell(value, page, colIndex)}</td>`).join('')}<td><button class="link-btn" data-row-action="edit" data-row="${index}" type="button">编辑</button><button class="link-btn" data-row-action="delete" data-row="${index}" type="button">删除</button></td></tr>`).join('');
  return `<table class="data-table"><thead><tr><th><input data-select-all type="checkbox"></th>${config.columns.map((col) => `<th>${safeText(col)}</th>`).join('')}<th>操作</th></tr></thead><tbody>${renderFixedTableRows(rowsHTML, rows.length, config.columns.length + 2)}</tbody></table>`;
}
function dhcpRows(page) {
  const config = networkPages[page.id];
  const activeIndex = state.activeTabs[page.id] || 0;
  const type = config.tabs[activeIndex];
  return networkRowsForPage(page.id).map((row, index) => ({ row, index })).filter(({ row }) => row[0] === type);
}
function renderProxyInterfaceTable(page) {
  const rows = proxyInterfaceRows(page);
  const isWanTab = (state.activeTabs[page.id] || 0) === 1;
  const cols = isWanTab ? ['接口名称', 'IP地址/掩码', 'MTU', '总流入', '总流出', '连接数', '线路类型', '备注'] : ['接口名称', 'IP地址/掩码', 'MTU', '总流入', '总流出', '连接数', '接口成员', '备注'];
  const visibleIndexes = isWanTab ? [0, 2, 6, 8, 9, 10, 12, 11] : [0, 2, 6, 8, 9, 10, 11, 12];
  const rowsHTML = rows.map(({ row, index }) => `<tr><td><input data-row-check="${index}" type="checkbox" ${state.checkedRows.has(index) ? 'checked' : ''}></td>${visibleIndexes.map((colIndex) => `<td>${renderNetworkCell(row[colIndex], page, colIndex)}</td>`).join('')}<td><button class="link-btn" data-row-action="edit" data-row="${index}" type="button">编辑</button><button class="link-btn" data-row-action="delete" data-row="${index}" type="button">删除</button></td></tr>`).join('');
  return `<table class="data-table"><thead><tr><th><input data-select-all type="checkbox"></th>${cols.map((col) => `<th>${safeText(col)}</th>`).join('')}<th>操作</th></tr></thead><tbody>${renderFixedTableRows(rowsHTML, rows.length, cols.length + 2)}</tbody></table>`;
}
function proxyInterfaceRows(page) {
  const activeIndex = state.activeTabs[page.id] || 0;
  const type = activeIndex === 1 ? 'WAN线路' : 'LAN接口';
  return networkRowsForPage(page.id).map((row, index) => ({ row, index })).filter(({ row }) => {
    const hasAddress = Boolean(String(row[2] || '').trim());
    const hasWanGateway = Boolean(String(row[3] || '').trim()) && row[1] === 'WAN线路';
    const hasLanMembers = Boolean(String(row[11] || '').trim()) && row[1] === 'LAN桥';
    const isWanLink = row._resourceKey === 'wanLinks' && wanLinkConfigured(row);
    const isProxyEgress = row._resourceKey === 'proxyEgresses' || row._wanType === 'proxy';
    const configured = hasAddress || hasWanGateway || hasLanMembers || isWanLink || isProxyEgress;
    if (activeIndex === 0) return configured && (row[1] === 'LAN接口' || row[1] === 'LAN桥');
    return configured && row[1] === type;
  });
}

function wanLinkConfigured(row) {
  if (!row || row._resourceKey !== 'wanLinks') return false;
  const type = String(row._wanType || row[12] || '').trim().toLowerCase();
  if (type === 'dhcp4' || type === 'dhcp6' || type === 'pppoe' || row[12] === 'DHCP IPv4' || row[12] === 'DHCP IPv6' || row[12] === 'PPPoE') return true;
  return Boolean(String(row[2] || '').trim() || String(row[3] || '').trim());
}
function renderProxyLogTable(page) {
  const cols = ['时间', '线路', '事件', '说明'];
  const logs = interfaceActionLogs();
  return `<table class="data-table proxy-log-table"><colgroup><col class="log-col-time"><col class="log-col-line"><col class="log-col-event"><col class="log-col-desc"></colgroup><thead><tr>${cols.map((col) => `<th>${safeText(col)}</th>`).join('')}</tr></thead><tbody>${logs.length ? logs.map((row) => `<tr>${row.map((value) => `<td>${renderNetworkCell(value, page, 0)}</td>`).join('')}</tr>`).join('') : renderEmptyTableRow(cols.length, '暂无线路操作日志')}</tbody></table>`;
}
function interfaceActionLogs() {
  const items = envelopeItems(state.controlPlane.audit);
  const lineResources = ['/api/v1/interfaces', '/api/v1/gateway/wan-links', '/api/v1/gateway/wan-groups', '/api/v1/proxy/egresses', '/api/v1/proxy/nodes', '/api/v1/proxy/subscriptions'];
  return items.filter((item) => {
    const resource = String(item.resource || item.detail || '');
    const action = String(item.action || '').toLowerCase();
    return lineResources.some((prefix) => resource.includes(prefix)) || ['interface', 'wan', 'proxy'].some((token) => action.includes(token));
  }).map((item) => [formatFullDateTime(item.timestamp || item.time || item.created_at), interfaceLogLine(item.resource || item.line || ''), interfaceLogAction(item.action || ''), interfaceLogDescription(item)]);
}
function interfaceLogLine(resource) {
  const parts = String(resource || '').split('/').filter(Boolean);
  const last = parts[parts.length - 1] || '';
  if (last && !['interfaces', 'wan-links', 'wan-groups', 'egresses', 'nodes', 'subscriptions'].includes(last)) return last;
  if (String(resource).includes('/proxy/')) return '代理线路';
  if (String(resource).includes('/wan-')) return 'WAN线路';
  return '接口配置';
}
function interfaceLogAction(action) {
  return ({ create: '创建线路', update: '更改线路', delete: '删除线路' })[String(action || '').toLowerCase()] || action || '线路动作';
}
function interfaceLogDescription(item) {
  const action = interfaceLogAction(item.action || '');
  const line = interfaceLogLine(item.resource || item.line || '');
  const status = String(item.status || item.result || '').toLowerCase();
  if (status === 'success') return `${action}成功：${line}`;
  if (status === 'failure' || status === 'denied') return `${action}失败：${item.error || item.detail || line}`;
  return item.detail || `${action}：${line}`;
}
function recordLocalLineEvent(resource, action, status = 'success', detail = '') {
  const audit = state.controlPlane.audit || { items: [] };
  const items = envelopeItems(audit).slice();
  items.unshift({ id: `local-${Date.now()}-${Math.random().toString(16).slice(2)}`, timestamp: new Date().toISOString(), actor: state.auth?.username || 'local', role: 'admin', resource, action, status, detail });
  state.controlPlane.audit = { ...audit, items, total: items.length };
}
function formatFullDateTime(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value || '');
  const pad = (n) => String(n).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}
function renderEmptyTableRow(colspan, message) {
  return `<tr class="empty-row"><td colspan="${colspan}">${safeText(message)}</td></tr>`;
}
function renderFixedTableRows(content, count, colspan, message = '暂无配置') {
  const realRows = count ? content : renderEmptyTableRow(colspan, message);
  const placeholders = Math.max(0, 10 - Math.max(1, count));
  return `${realRows}${Array.from({ length: placeholders }, () => `<tr class="placeholder-row"><td colspan="${colspan}"></td></tr>`).join('')}`;
}
function renderInterfaceTable(page) {
  const rows = networkRowsForPage(page.id);
  const cols = ['接口', '状态', '工作模式', '方向', '链路聚合', '实时流量', '操作'];
  const rowsHTML = rows.map((row, index) => `<tr>${renderInterfaceNameCell(row)}<td>${renderNetworkCell(row[1], page, 1)}</td><td>${safeText(row[2])}</td><td>${renderInterfaceRoleCell(row)}</td><td>${renderInterfaceBondCell(row, index)}</td><td><div class="nic-metric"><span>${safeText(row[5])}</span><span>${safeText(row[6])}</span></div></td><td>${renderInterfaceRowAction(row, index)}</td></tr>`).join('');
  return `<table class="data-table nic-table"><colgroup><col class="nic-col-name"><col class="nic-col-status"><col class="nic-col-mode"><col class="nic-col-dir"><col class="nic-col-lag"><col class="nic-col-traffic"><col class="nic-col-action"></colgroup><thead><tr>${cols.map((col) => `<th>${safeText(col)}</th>`).join('')}</tr></thead><tbody>${renderFixedTableRows(rowsHTML, rows.length, cols.length, '暂无接口')}</tbody></table>`;
}
function renderInterfaceNameCell(row) {
  return `<td><div class="nic-name-stack"><strong class="nic-name">${safeText(row[0])}</strong></div></td>`;
}
function renderInterfaceRoleCell(row) {
  const role = row[3] || '未配置';
  const className = role === '管理口' ? 'management' : role === 'WAN接口' ? 'wan' : role === 'LAN接口' ? 'lan' : role === '聚合接口' ? 'bond' : 'empty';
  return `<span class="nic-role nic-role-${className}">${safeText(role)}</span>`;
}
function renderInterfaceBondCell(row, index) {
  if (row._resourceKey === 'interfaceBonds') return `<span class="nic-bond-chip">${safeText(row[4] || '成员组')}</span>`;
  return row[4] ? `<span class="nic-bond-chip">${safeText(row[4])}</span>` : '<span class="nic-muted">未加入</span>';
}
function renderInterfaceRowAction(row, index) {
  if (row[3] === '管理口') return '<span class="nic-muted">不可配置</span>';
  if (row._resourceKey === 'interfaceBonds') return `<button class="link-btn" data-row-action="edit" data-row="${index}" type="button">角色配置</button><button class="link-btn danger" data-row-action="delete" data-row="${index}" type="button">删除聚合</button>`;
  return `<button class="link-btn" data-row-action="edit" data-row="${index}" type="button">角色配置</button>`;
}
function renderNetworkCell(value) {
    const text = safeText(value);
  if (['固定 IPv4', '固定 IPv6', 'DHCP IPv4', 'DHCP IPv6', 'PPPoE', '代理线路'].includes(value)) return `<span class="line-type-chip">${text}</span>`;
  if (['启用', 'UP', 'UP/RUNNING', 'RUNNING', '在线', 'ACK', 'LINKUP'].includes(value)) return `<span class="status ok">${text}</span>`;
  if (['禁用', '停用', '异常', 'LINKDOWN'].includes(value)) return `<span class="status off">${text}</span>`;
  if (String(value).includes('失败')) return `<span class="status warn">${text}</span>`;
  return text;
}
function renderTelemetryCell(value) {
  const text = safeText(value);
  if (String(value).includes('.')) return `<button class="link-btn" type="button">${text}</button>`;
  return text;
}
function renderHardwareLine(line) {
  const match = String(line).match(/^(驱动模式|网卡型号|网络地址)\s+(.+)$/);
  if (!match) return `<span>${safeText(line)}</span>`;
  return `<span><em>${safeText(match[1])}</em> ${safeText(match[2])}</span>`;
}
function renderPager() { return `<nav class="pager" aria-label="分页"><button type="button">上一页</button><button class="primary" type="button" aria-current="page">1</button><button type="button">下一页</button></nav>`; }
function tableColumns(page) {
  if (page.id.includes('interface')) return ['接口', 'IP/掩码', '速率', '备注'];
  if (page.id.includes('user')) return ['用户', 'IP', '认证方式', '上线时间'];
  if (page.id.includes('domain')) return ['域名', '分类', '命中次数', '动作'];
  if (page.id.includes('route') || page.id.includes('nat')) return ['规则名', '源地址', '目的地址', '出口'];
  return ['名称', '对象/参数', '命中/流量', '备注'];
}
function wireWorkspaceEvents(page) {
  el.workspace.querySelectorAll('[data-tab-index]').forEach((button) => button.addEventListener('click', () => { state.activeTabs[page.id] = Number(button.dataset.tabIndex); state.checkedRows.clear(); renderWorkspace(); }));
  el.workspace.querySelectorAll('[data-action]').forEach((button) => button.addEventListener('click', () => handleAction(button.dataset.action, page)));
  if (page.type === 'dashboard' && state.trafficRenderOptions) gatewayOverview.wireTraffic(el.workspace, state.trafficRenderOptions, {
    window: (value) => { state.trafficWindow = value; refreshTrafficTrend(); },
    toggle: (id) => { if (state.hiddenEgresses.has(id)) state.hiddenEgresses.delete(id); else state.hiddenEgresses.add(id); renderWorkspace(); }
  });
  el.workspace.querySelectorAll('[data-system-link]').forEach((button) => button.addEventListener('click', () => openPage(button.dataset.systemLink)));
  el.workspace.querySelector('[data-firmware-image]')?.addEventListener('change', () => handleFirmwareAction('firmware-stage'));
  wireConfigTimeSync(page);
  el.workspace.querySelectorAll('[data-domain-group]').forEach((button) => button.addEventListener('click', () => { state.selectedDomainGroup = button.dataset.domainGroup; renderWorkspace(); }));
  el.workspace.querySelectorAll('[data-ip-group]').forEach((button) => button.addEventListener('click', () => { state.selectedIpGroup = button.dataset.ipGroup; renderWorkspace(); }));
  el.workspace.querySelector('[data-domain-group-select]')?.addEventListener('change', (event) => { state.selectedDomainGroup = event.target.value; renderWorkspace(); });
  el.workspace.querySelector('[data-ip-group-select]')?.addEventListener('change', (event) => { state.selectedIpGroup = event.target.value; renderWorkspace(); });
  el.workspace.querySelectorAll('[data-row-check]').forEach((checkbox) => checkbox.addEventListener('change', () => { const id = Number(checkbox.dataset.rowCheck); checkbox.checked ? state.checkedRows.add(id) : state.checkedRows.delete(id); }));
  el.workspace.querySelector('[data-select-all]')?.addEventListener('change', (event) => { const rowIndexes = isNetworkContentPage(page) ? networkTableRowEntries(page).map(({ index }) => index) : []; state.checkedRows = event.target.checked ? new Set(rowIndexes) : new Set(); renderWorkspace(); });
  el.workspace.querySelectorAll('[data-row-action]').forEach((button) => button.addEventListener('click', () => {
    const rowIndex = Number(button.dataset.row);
    if (button.dataset.rowAction === 'delete' && page.id === 'monitor/interface_list') { confirmDeleteResourceRow(page, rowIndex, '删除聚合后会释放成员网卡，成员会重新显示在网卡设置中。'); return; }
    if (button.dataset.rowAction === 'delete') { confirmDeleteResourceRow(page, rowIndex); return; }
    if (button.dataset.rowAction === 'edit' && isRoutePolicyRow(page, rowIndex)) { openModal('编辑策略', routePolicyFormHtml(rowIndex), 'route-policy'); pendingModalSubmit = () => submitResourceModal(page, 'edit', rowIndex); return; }
    if (button.dataset.rowAction === 'edit' && page.id === 'route/portmap_list') { openModal('编辑映射', portMapFormHtml(rowIndex), 'route-policy'); pendingModalSubmit = () => submitResourceModal(page, 'edit', rowIndex); return; }
    if (page.id === 'network/wangroup_manager' && button.dataset.rowAction === 'edit') { openModal('编辑WAN群组', wanGroupLineFormHtml(rowIndex)); pendingModalSubmit = () => submitResourceModal(page, 'edit', rowIndex); return; }
    if (button.dataset.rowAction === 'edit' && isObjectGroupPage(page)) { openModal(`编辑${page.title}`, objectGroupEditFormHtml(page, rowIndex), 'route-policy'); pendingModalSubmit = () => submitResourceModal(page, 'edit', rowIndex); return; }
    openModal(`${button.textContent}${page.title}`, formHtml(page, rowIndex));
    pendingModalSubmit = () => submitResourceModal(page, 'edit', rowIndex);
  }));
  el.workspace.querySelectorAll('[data-auth-action]').forEach((button) => button.addEventListener('click', () => handleAuthUserAction(button.dataset.authAction, button.dataset.user || '')));
}
function confirmDeleteResourceRow(page, rowIndex, message = '') {
  const row = networkRowsForPage(page.id)[rowIndex];
  const endpoint = deleteEndpointForRow(row);
  openModal('删除确认', `<p>${safeText(message || `确认删除这条 ${page.title} 记录？`)}</p>`);
  pendingModalSubmit = endpoint ? () => deleteResourceRow(endpoint, page.title) : () => { closeModal(true); toast(`${page.title} 暂不支持删除`); };
}
function confirmDeleteSelectedResources(page) {
  const selected = Array.from(state.checkedRows).map((index) => networkRowsForPage(page.id)[index]).filter(Boolean);
  if (selected.length === 0) { toast('请先选择要删除的记录'); return; }
  const endpoints = selected.map(deleteEndpointForRow).filter(Boolean);
  openModal('删除确认', `<p>确认删除 ${endpoints.length} 条 ${safeText(page.title)} 记录？</p>`);
  pendingModalSubmit = endpoints.length ? () => deleteResourceRows(endpoints, page.title) : () => { closeModal(true); toast(`${page.title} 暂不支持删除`); };
}
function deleteEndpointForRow(row) {
  if (!row?._resourceKey || !row._resourceId || !resourceEndpoints[row._resourceKey]) return '';
  return `${resourceEndpoints[row._resourceKey]}/${encodeURIComponent(row._resourceId)}`;
}
async function deleteResourceRows(endpoints, title) {
  try {
    for (const endpoint of endpoints) {
      await apiJSON(endpoint, { method: 'DELETE' });
      recordLocalLineEvent(endpoint, 'delete');
    }
    state.checkedRows.clear();
    closeModal(true);
    toast(`${title} 已删除`);
    await refreshControlPlane({ resourcesOnly: true });
  } catch (error) {
    toast(error.message || `${title} 删除失败`);
  }
}
async function deleteResourceRow(endpoint, title) {
  try {
    await apiJSON(endpoint, { method: 'DELETE' });
    recordLocalLineEvent(endpoint, 'delete');
    closeModal(true);
    if (endpoint.includes('/interface-bonds/')) {
      const applyResult = await apiJSON(controlApi.runtimeApply, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
      state.controlPlane.runtimeApply = applyResult;
      toast(`${title} 已删除，成员网卡已释放`);
    } else {
      toast(`${title} 已删除`);
    }
    await refreshControlPlane({ resourcesOnly: true });
  } catch (error) {
    toast(error.message || `${title} 删除失败`);
  }
}

async function submitResourceModal(page, action = 'add', rowIndex = null) {
  const currentRow = Number.isInteger(rowIndex) ? networkRowsForPage(page.id)[rowIndex] : null;
  const payload = resourcePayloadForPage(page, rowIndex);
  const sideResources = Array.isArray(payload?._sideResources) ? payload._sideResources : [];
  if (payload && Object.prototype.hasOwnProperty.call(payload, '_sideResources')) delete payload._sideResources;
  const resourceKey = resourceKeyForPayload(page, payload, currentRow);
  if (!resourceKey || !resourceEndpoints[resourceKey]) {
    closeModal(true);
    toast(`${page.title} 暂不支持保存`);
    return;
  }
  try {
    const currentID = currentRow?._resourceId || '';
    const endpointID = logicalEndpointID(resourceKey, currentRow) || currentID;
    const endpoint = action === 'edit' && endpointID ? `${resourceEndpoints[resourceKey]}/${encodeURIComponent(endpointID)}` : resourceEndpoints[resourceKey];
    const method = action === 'edit' && currentID ? 'PATCH' : 'POST';
    const cleanup = interfaceRoleChangeCleanup(page, currentRow, payload);
    if (cleanup && !window.confirm(cleanup.message)) return;
    for (const cleanupEndpoint of cleanup?.endpoints || []) {
      await apiJSON(cleanupEndpoint, { method: 'DELETE' });
      recordLocalLineEvent(cleanupEndpoint, 'delete');
    }
    const mutation = await apiJSON(endpoint, { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
    for (const side of sideResources) {
      if (!side?.resourceKey || !resourceEndpoints[side.resourceKey] || !side.payload) continue;
      await apiJSON(resourceEndpoints[side.resourceKey], { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(side.payload) });
      recordLocalLineEvent(`${resourceEndpoints[side.resourceKey]}/${side.payload.id || ''}`, 'create');
    }
    recordLocalLineEvent(`${resourceEndpoints[resourceKey]}/${payload.id || endpointID || ''}`, method === 'PATCH' ? 'update' : 'create');
    await confirmResourceReadback(resourceKey, mutation.item || mutation, payload, endpointID);
    const applyResult = await applyRuntimeAfterNetworkSave(resourceKey, payload);
    closeModal(true);
    if (applyResult) toast(`${page.title} 已保存并下发，已从 API 回读确认`);
    else toast(page.id === 'monitor/interface_list' ? '角色已从 API 回读确认，请到 LAN/WAN 页面补全参数' : `${page.title} 已从 API 回读确认`);
    await refreshControlPlane(applyResult || resourceKey === 'interfaces' ? { settleInterfaces: resourceKey === 'interfaces' } : { resourcesOnly: true });
  } catch (error) {
    toast(error.message || `${page.title} 保存失败`);
  }
}

async function confirmResourceReadback(resourceKey, mutationItem, payload, endpointID) {
  let readback;
  try {
    readback = await apiJSON(resourceEndpoints[resourceKey]);
  } catch (error) {
    throw new Error(`保存后 API 回读失败：${error.message || '接口不可用'}`);
  }
  // The mutation response may include desired-path metadata while list readback
  // overlays the currently active dataplane path. Verify the user's submitted
  // configuration here; runtime convergence is checked by runtime status.
  const expected = payload;
  const identity = expected.id || payload.id || endpointID || expected.name || payload.name || '';
  const item = envelopeItems(readback).find((candidate) => [candidate.id, candidate.name, candidate.username].filter(Boolean).map(String).includes(String(identity)));
  if (resourceKey === 'interfaces' && payload.role_configured === true) {
    const expectedRole = normalizeInterfaceRole(payload.gateway_role || payload.mode_role?.gateway || '');
    const actualRole = normalizeInterfaceRole(item?.gateway_role || item?.mode_role?.gateway || '');
    if (!item || !expectedRole || expectedRole !== actualRole) throw new Error('保存响应与 API 回读不一致');
    state.controlPlane.resources = { ...state.controlPlane.resources, [resourceKey]: readback };
    return;
  }
  if (!item || !sharedReadbackFieldsMatch(expected, item)) throw new Error('保存响应与 API 回读不一致');
  state.controlPlane.resources = { ...state.controlPlane.resources, [resourceKey]: readback };
}

function sharedReadbackFieldsMatch(expected, actual) {
  const derivedFields = ['created_at', 'updated_at', 'observed_at', 'runtime_state', 'active_path', 'stats', 'capability'];
  const keys = Object.keys(expected).filter((key) => Object.prototype.hasOwnProperty.call(actual, key) && !derivedFields.includes(key));
  return keys.length > 0 && keys.every((key) => {
    if (key === 'interface_id') return interfaceIdentifiersMatch(expected[key], actual[key]);
    return stableJSON(expected[key]) === stableJSON(actual[key]);
  });
}

function interfaceIdentifiersMatch(expected, actual) {
  if (String(expected || '') === String(actual || '')) return true;
  const expectedID = String(expected || '');
  const actualID = String(actual || '');
  return envelopeItems(state.controlPlane.resources?.interfaces).some((item) => {
    const aliases = [item.id, item.name, item.system_name, item.vpp_interface, item.host_interface]
      .filter(Boolean)
      .map(String);
    return aliases.includes(expectedID) && aliases.includes(actualID);
  });
}

function stableJSON(value) {
  if (Array.isArray(value)) return `[${value.map(stableJSON).join(',')}]`;
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableJSON(value[key])}`).join(',')}}`;
  }
  return JSON.stringify(value);
}

function interfaceRoleChangeCleanup(page, row, payload) {
  if (page.id !== 'monitor/interface_list' || !row || !payload?.gateway_role) return null;
  const currentRole = normalizeInterfaceRole(row[3] || '');
  const nextRole = normalizeInterfaceRole(payload.gateway_role || '');
  if (!currentRole || !nextRole || currentRole === nextRole) return null;
  const ids = new Set([row[0], row._resourceId, row._systemId].filter(Boolean).map(String));
  const endpoints = [];
  const labels = [];
  for (const item of envelopeItems(state.controlPlane.resources?.wanLinks)) {
    const itemIDs = [item.id, item.name, item.interface_id, item.system_name].filter(Boolean).map(String);
    if (!itemIDs.some((id) => ids.has(id))) continue;
    const id = item.id || item.name || '';
    if (!id) continue;
    endpoints.push(`${resourceEndpoints.wanLinks}/${encodeURIComponent(id)}`);
    labels.push(item.name || item.id || id);
  }
  if (currentRole === 'lan' && String(row[2] || '').trim()) labels.push(`${row[0]} LAN IP ${row[2]}`);
  if (!labels.length) return null;
  const message = `接口 ${row[0]} 当前已有 ${labels.join('、')} 配置。切换为 ${nextRole.toUpperCase()} 前需要清空原有接口配置，是否继续？`;
  return { endpoints: Array.from(new Set(endpoints)), message };
}

function logicalEndpointID(resourceKey, row) {
  if (!row) return '';
  if (['wanLinks', 'proxyEgresses', 'wanGroups', 'routePolicies', 'portMaps', 'dnsPolicies', 'dhcpServers', 'dhcpBindings'].includes(resourceKey)) return row._resourceId || '';
  return row._systemId || row._resourceId || '';
}

async function applyRuntimeAfterNetworkSave(resourceKey, payload = {}) {
  const runtimeResources = [
    'interfaces', 'interfaceBonds', 'wanLinks', 'wanGroups', 'proxyEgresses', 'proxyNodes', 'proxySubscriptions',
    'routePolicies', 'portMaps', 'dnsPolicies', 'dhcpServers', 'dhcpBindings', 'trafficControl',
    'securityAcls', 'securityIpMac', 'securityThreatIntel', 'securityAttackRules'
  ];
  if (!runtimeResources.includes(resourceKey)) return null;
  if (resourceKey === 'interfaces' && payload.runtime_state === 'role_configured') return null;
  const result = await apiJSON(controlApi.runtimeApply, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
  state.controlPlane.runtimeApply = result;
  if (result?.status === 'apply_failed') throw new Error(result.reason || '配置下发失败');
  return result;
}

function resourceKeyForPayload(page, payload, currentRow) {
  if (page.id === 'monitor/interface_list') return currentRow?._resourceKey || 'interfaces';
  if (payload?.semantic_type === 'proxy_egress') return 'proxyEgresses';
  if (page.id === 'network/proxy_main' && (state.activeTabs[page.id] || 0) === 1) return currentRow?._resourceKey || 'wanLinks';
  if (payload?.gateway_role === 'lan') return 'interfaces';
  if (payload?.gateway_role === 'wan') return 'wanLinks';
  return currentRow?._resourceKey || resourceKeyForPage(page);
}

function handleAuthUserAction(action, username) {
  if (action === 'password') {
    openModal(`修改 ${username} 密码`, systemUserFormHtml(username));
    pendingModalSubmit = () => submitAuthUser('edit', username);
    return;
  }
  if (action === 'delete') {
    openModal('删除账号', `<p>确认删除账号 ${safeText(username)}？</p>`);
    pendingModalSubmit = () => deleteAuthUser(username);
  }
}

async function submitAuthUser(action, username = '') {
  const values = modalControls();
  const targetUser = username || values[0] || '';
  const role = values[1] || 'readonly';
  const password = values[2] || '';
  const confirm = values[3] || '';
  if (!targetUser || !password || password !== confirm) {
    toast('请填写账号并确认两次密码一致');
    return;
  }
  const endpoint = action === 'edit' ? `${resourceEndpoints.authUsers}/${encodeURIComponent(targetUser)}` : resourceEndpoints.authUsers;
  const method = action === 'edit' ? 'PATCH' : 'POST';
  try {
    await apiJSON(endpoint, { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ username: targetUser, role, password }) });
    closeModal(true);
    toast('系统用户已保存');
    await refreshControlPlane();
  } catch (error) {
    toast(error.message || '系统用户保存失败');
  }
}

async function deleteAuthUser(username) {
  try {
    await apiJSON(`${resourceEndpoints.authUsers}/${encodeURIComponent(username)}`, { method: 'DELETE' });
    closeModal(true);
    toast('系统用户已删除');
    await refreshControlPlane();
  } catch (error) {
    toast(error.message || '系统用户删除失败');
  }
}

function resourceKeyForPage(page) {
  return ({
    'monitor/interface_list': 'interfaces',
    'network/proxy_main': 'interfaces',
    'network/wangroup_manager': 'wanGroups',
    'network/dhcpsvr_main': (state.activeTabs['network/dhcpsvr_main'] || 0) === 1 ? 'dhcpBindings' : 'dhcpServers',
    'route/route_policy_main': 'routePolicies',
    'route/portmap_list': 'portMaps',
    'route/dnspolicy_main': 'dnsPolicies',
    'object/urlgrp_list': 'objectGroups',
    'object/iptab_list': 'objectGroups',
    'flowcontrol/flowct_main': 'trafficControl'
  })[page.id] || '';
}

function resourcePayloadForPage(page, rowIndex = null) {
  if (page.id === 'route/route_policy_main') return routePolicyPayload(rowIndex);
  if (page.id === 'network/dhcpsvr_main') return dhcpPayloadForRow(rowIndex);
  if (page.id === 'monitor/interface_list') return interfacePayload(rowIndex);
  if (page.id === 'network/proxy_main') return proxyInterfacePayload(rowIndex);
  if (page.id === 'network/wangroup_manager') return wanGroupPayload(rowIndex);
  if (page.id === 'route/portmap_list') return portMapPayload(rowIndex);
  if (page.id === 'route/dnspolicy_main') return dnsPolicyPayload(rowIndex);
  if (isObjectGroupPage(page)) return objectGroupPayload(page, rowIndex);
  if (page.id === 'flowcontrol/flowct_main') return trafficControlPayload(rowIndex);
  return { id: `item-${Date.now()}` };
}

function modalControls() {
  return Array.from(el.modal.querySelectorAll('input, select, textarea')).map((node) => node.value.trim());
}

function routePolicyPayload(rowIndex = null) {
  const row = Number.isInteger(rowIndex) ? networkRowsForPage('route/route_policy_main')[rowIndex] : null;
  const form = el.modal.querySelector('[data-route-policy-form]');
  const priority = Number(form?.querySelector('[data-route-priority]')?.value || row?._resource?.priority || row?.[0] || Date.now());
  const actionText = form?.querySelector('[data-route-action]')?.value || row?._resource?.action || row?.[1] || 'NAT';
  const action = actionText === '路由' || actionText === 'route' ? 'route' : 'nat';
  const target = form?.querySelector('[data-route-line]')?.value || row?._resource?.egress || row?.[6] || '';
  const payload = { id: row?._resourceId || `route-${priority}`, name: form?.querySelector('[data-route-name]')?.value || row?._resource?.name || row?.[9] || `策略${priority}`, priority, enabled: true, action, match: { sources: addressConditionValues(form, 'src'), destinations: addressConditionValues(form, 'dst'), src_port: form?.querySelector('[data-route-src-port]')?.value || '', dst_port: form?.querySelector('[data-route-dst-port]')?.value || '' }, next_hop: form?.querySelector('[data-route-next-hop]')?.value || row?._resource?.next_hop || '' };
  if (target) payload.egress = target;
  if (form?.querySelector('[data-full-cone] input')?.checked) payload.full_cone = true;
  return payload;
}

function proxyInterfacePayload(rowIndex = null) {
  const values = modalControls();
  const row = Number.isInteger(rowIndex) ? networkRowsForPage('network/proxy_main')[rowIndex] : null;
  const activeTab = state.activeTabs['network/proxy_main'] || 0;
  const id = row?._resourceId || values[0] || values[1] || `if-${Date.now()}`;
  if (activeTab === 1 || row?._resourceKey === 'wanLinks') {
    const form = el.modal.querySelector('[data-wan-form]');
    const name = form?.querySelector('[data-wan-name]')?.value.trim() || row?.[0] || id;
    const wanType = form?.querySelector('[data-wan-type]')?.value || 'static4';
    const interfaceID = form?.querySelector('[data-wan-interface]')?.value.trim() || row?._systemId || row?.[0] || id;
    const mtu = form?.querySelector('[data-wan-mtu]')?.value.trim() || row?.[6] || 1500;
    const uploadMbps = Number(form?.querySelector('[data-wan-upload-mbps]')?.value || (Number(row?._resource?.smart_qos_upload_kbps || 0) / 1000));
    const downloadMbps = Number(form?.querySelector('[data-wan-download-mbps]')?.value || (Number(row?._resource?.smart_qos_download_kbps || 0) / 1000));
    const ipv4 = form?.querySelector('[data-wan-ipv4]')?.value.trim() || '';
    const ipv4Prefix = form?.querySelector('[data-wan-ipv4-prefix]')?.value.trim() || '24';
    const ipv4Gateway = form?.querySelector('[data-wan-ipv4-gateway]')?.value.trim() || '';
    const ipv6 = form?.querySelector('[data-wan-ipv6]')?.value.trim() || '';
    const ipv6Prefix = form?.querySelector('[data-wan-ipv6-prefix]')?.value.trim() || '64';
    const ipv6Gateway = form?.querySelector('[data-wan-ipv6-gateway]')?.value.trim() || '';
    const description = form?.querySelector('[data-wan-remark]')?.value.trim() || '';
    const typeForAPI = ({ static4: 'static', static6: 'static6', dhcp4: 'dhcp4', dhcp6: 'dhcp6' })[wanType] || wanType;
    const configID = row?._resourceId || `wan-${slugID(interfaceID)}-${wanConfigFamily(wanType)}`;
    if (wanType !== 'proxy' && (!(uploadMbps > 0) || !(downloadMbps > 0))) throw new Error('请填写有效的上行和下行带宽');
    const payload = { id: configID, name, interface_id: interfaceID, enabled: true, gateway_role: 'wan', mode_role: { gateway: 'wan', bridge: null }, type: typeForAPI, wan_type: typeForAPI, mtu: Number(mtu || 1500), bandwidth: `${uploadMbps}/${downloadMbps} M`, smart_qos_upload_kbps: Math.round(uploadMbps * 1000), smart_qos_download_kbps: Math.round(downloadMbps * 1000), description, runtime_state: 'desired_not_applied' };
    if (wanType === 'static4') {
      const cidr = ipv4 ? `${ipv4}/${ipPrefix(ipv4Prefix, 24, 32)}` : row?.[2] || '';
      const gateway = ipv4Gateway || row?.[3] || '';
      if (!cidr || !gateway) throw new Error('固定 IPv4 WAN 需要填写地址和网关');
      payload.cidr = cidr;
      payload.gateway = gateway;
      payload.dns_servers = [];
      payload.nat = true;
      payload.ipv4 = { mode: 'static', address: cidr, gateway: payload.gateway };
      payload.ipv6 = { mode: 'disabled' };
    }
    if (wanType === 'static6') {
      const cidr = ipv6 ? `${ipv6}/${ipPrefix(ipv6Prefix, 64, 128)}` : row?.[2] || '';
      const gateway = ipv6Gateway || row?.[3] || '';
      if (!cidr || !gateway) throw new Error('固定 IPv6 WAN 需要填写地址和网关');
      payload.cidr = cidr;
      payload.gateway = gateway;
      payload.dns_servers = [];
      payload.nat = true;
      payload.ipv4 = { mode: 'disabled' };
      payload.ipv6 = { mode: 'static', address: cidr, gateway: payload.gateway };
    }
    if (wanType === 'dhcp4' || wanType === 'dhcp6') {
      payload.cidr = '';
      payload.gateway = '';
      payload.dns_servers = [];
      payload.nat = true;
      payload.ipv4 = { mode: wanType === 'dhcp4' ? 'dhcp4' : 'disabled' };
      payload.ipv6 = { mode: wanType === 'dhcp6' ? 'dhcpv6_pd' : 'disabled' };
    }
    if (wanType === 'pppoe') {
      payload.username = form?.querySelector('[data-wan-username]')?.value.trim() || '';
      payload.password = form?.querySelector('[data-wan-password]')?.value || '';
      if (!payload.username || !payload.password) throw new Error('PPPoE WAN 需要填写账号和密码');
      payload.ipv4 = { mode: 'pppoe' };
    }
    if (wanType === 'proxy') {
      const underlay = form?.querySelector('[data-proxy-underlay]')?.value.trim() || '';
      const businessAddress = form?.querySelector('[data-proxy-address]')?.value.trim() || '';
      const businessName = form?.querySelector('[data-proxy-business-name]')?.value.trim() || name;
      const payload = { id: `proxy-egress-${slugID(name)}`, kind: 'egress', name, enabled: true, semantic_type: 'proxy_egress', display_list: 'wan', proxy_profile_id: 'xray-tproxy-outbound', underlay_wan_id: underlay, low_copy: false, description };
      const sideResource = proxyBusinessSideResource(businessAddress, businessName);
      if (sideResource) payload._sideResources = [sideResource];
      return payload;
    }
    return payload;
  }
  const isBridge = el.modal.querySelector('[data-lan-kind]')?.value === 'lan_bridge' || row?.[1] === 'LAN桥';
  const role = 'lan';
  const bridgeMembers = Array.from(el.modal.querySelectorAll('[data-bridge-member]:checked')).map((item) => item.value).filter(Boolean);
  const name = el.modal.querySelector('[data-lan-name]')?.value.trim() || row?.[0] || (isBridge ? nextLanBridgeName() : id);
  const interfaceID = isBridge ? lanBridgeID(name) : el.modal.querySelector('[data-lan-interface]')?.value.trim() || row?._systemId || row?.[0] || id;
  const ip = el.modal.querySelector('[data-lan-address]')?.value.trim() || '';
  const cidr = ip ? `${ip}/${maskToPrefix(el.modal.querySelector('[data-lan-prefix]')?.value || 24) || 24}` : row?.[2] || '';
  const description = el.modal.querySelector('[data-lan-remark]')?.value.trim() || '';
  const ipv6Enabled = el.modal.querySelector('[data-lan-ipv6-enabled]')?.checked === true;
  const prefixWan = el.modal.querySelector('[data-lan-prefix-wan]')?.value || '';
  return { id: interfaceID, name: isBridge ? name : interfaceID, display_name: name, interface_id: interfaceID, cidr, gateway: '', dns_servers: [], nat: false, mtu: Number(el.modal.querySelector('[data-lan-mtu]')?.value.trim() || row?.[6] || 1500), bandwidth: row?.[7] || '', description, gateway_role: role, mode_role: { gateway: role, bridge: isBridge ? name : null }, bridge_members: isBridge ? bridgeMembers : [], kind: isBridge ? 'lan_bridge' : 'interface', type: isBridge ? 'lan_bridge' : 'interface', ipv6: ipv6Enabled ? { mode: 'delegated_prefix', source_wan_id: prefixWan, prefix_delegation: true, ra: true, default_route: true } : { mode: 'disabled' }, runtime_state: 'desired_not_applied' };
}

function proxyBusinessSideResource(address, name) {
  const value = String(address || '').trim();
  if (!value) return null;
  const protocol = value.split(':', 1)[0].toLowerCase();
  const id = slugID(name || value);
  if (value.startsWith('http://') || value.startsWith('https://')) {
    return { resourceKey: 'proxySubscriptions', payload: { id: `proxy-sub-${id}`, kind: 'subscription', name: name || '代理订阅', enabled: true, url: value } };
  }
  return { resourceKey: 'proxyNodes', payload: proxyNodePayloadFromURL(value, name || '代理节点', `proxy-node-${id}`, protocol) };
}

function proxyNodePayloadFromURL(value, name, id, fallbackProtocol) {
  const payload = { id, kind: 'node', name, enabled: true, protocol: fallbackProtocol || 'url', address: value };
  try {
    const parsed = new URL(value);
    payload.protocol = parsed.protocol.replace(':', '') || payload.protocol;
    payload.address = parsed.hostname || value;
    if (parsed.port) payload.port = Number(parsed.port);
    if (parsed.username) payload.secret = decodeURIComponent(parsed.username);
    const transport = parsed.searchParams.get('type') || parsed.searchParams.get('network');
    if (transport) payload.transport = transport;
  } catch (error) {
    payload.address = value;
  }
  return payload;
}

function wanGroupPayload(rowIndex = null) {
  const row = Number.isInteger(rowIndex) ? networkRowsForPage('network/wangroup_manager')[rowIndex] : null;
  const form = el.modal.querySelector('[data-wan-group-form]');
  const name = form?.querySelector('[data-wan-group-name]')?.value.trim() || row?._resource?.name || row?.[0] || '';
  const mode = form?.querySelector('[data-wan-group-mode]')?.value || row?._resource?.load_balance_mode || 'primary_backup';
  const hash = form?.querySelector('[data-wan-group-hash]')?.value || row?._resource?.hash_fields || 'src-dst-ip-port';
  const members = Array.from(el.modal.querySelectorAll('[data-wan-member]:checked')).map((item) => item.value).filter(Boolean);
  const weights = {};
  el.modal.querySelectorAll('[data-wan-member-weight]').forEach((input) => {
    const id = input.dataset.wanMemberWeight || '';
    const weight = Number(input.value || 1);
    if (id && weight > 0) weights[id] = weight;
  });
  if (!name) throw new Error('请填写群组名称');
  if (members.length < 2) throw new Error('请选择至少两条 WAN 线路');
  if (mode === 'weighted') {
    const total = members.reduce((sum, id) => sum + (weights[id] || 0), 0);
    if (total !== 100) throw new Error('加权负载的线路占用比例合计必须为 100%');
  }
  const memberWeights = members.map((id) => ({ id, weight: weights[id] || 1 }));
  const primaryMember = form?.querySelector('[data-wan-primary]')?.value || members[0] || '';
  const backupMember = form?.querySelector('[data-wan-backup]')?.value || members.find((id) => id !== primaryMember) || '';
  const id = row?._resourceId || `wangroup-${slugID(name)}`;
  return { id, name, kind: 'wan_group', direction: 'wan', load_balance_mode: mode, mode, hash_fields: hash, load_balance: hash, wan_members: members, members, weights, member_weights: memberWeights, primary_member: primaryMember, backup_member: backupMember };
}

function portMapPayload(rowIndex = null) {
  const row = Number.isInteger(rowIndex) ? networkRowsForPage('route/portmap_list')[rowIndex] : null;
  const form = el.modal.querySelector('[data-portmap-form]');
  const name = form?.querySelector('[data-portmap-name]')?.value.trim() || row?._resource?.name || row?.[0] || 'port-map';
  const wanLink = form?.querySelector('[data-portmap-wan]')?.value || row?._resource?.wan_link || row?.[1] || '';
  const protocol = form?.querySelector('[data-portmap-protocol]')?.value || row?._resource?.protocol || row?.[5] || 'TCP';
  return {
    id: row?._resourceId || slugID(name),
    name,
    wan_link: wanLink,
    external_port: Number(form?.querySelector('[data-portmap-external-port]')?.value || row?._resource?.external_port || row?.[2] || 0),
    internal_host: form?.querySelector('[data-portmap-internal-host]')?.value.trim() || row?._resource?.internal_host || row?.[3] || '',
    internal_port: Number(form?.querySelector('[data-portmap-internal-port]')?.value || row?._resource?.internal_port || row?.[4] || 0),
    protocol: protocol.toUpperCase(),
    description: form?.querySelector('[data-portmap-description]')?.value.trim() || row?._resource?.description || row?.[7] || '',
    enabled: true
  };
}

function dnsPolicyPayload(rowIndex = null) {
  const row = Number.isInteger(rowIndex) ? networkRowsForPage('route/dnspolicy_main')[rowIndex] : null;
  const form = el.modal.querySelector('[data-dns-policy-form]');
  const domainGroup = form?.querySelector('[data-dns-domain-group]')?.value || '';
  const sources = addressConditionValues(form, 'dns-src');
  if (!domainGroup) throw new Error('请选择目的域名组');
  const domainSetIDs = domainGroup ? [domainGroup] : [];
  const actionText = form?.querySelector('[data-dns-action]')?.value || row?.[5] || '解析';
  const upstreamAddress = form?.querySelector('[data-dns-upstream]')?.value.trim() || '';
  if (upstreamAddress && !/^\d{1,3}(?:\.\d{1,3}){3}$/.test(upstreamAddress)) throw new Error('解析上游必须填写单个 IPv4 地址');
  const outcome = actionText.includes('拒') || actionText.includes('拉黑') ? { kind: 'reject' } : upstreamAddress ? { kind: 'fixed_answer', fixed_answers: [upstreamAddress] } : { kind: 'direct' };
  const id = row?._resourceId || slugID(form?.querySelector('[data-dns-name]')?.value || row?.[0] || `dns-${Date.now()}`);
  return { id, kind: 'policy', name: form?.querySelector('[data-dns-name]')?.value.trim() || row?.[1] || id, enabled: true, policy: { engine: 'smartdns', miss: { kind: 'reject' }, rules: [{ id: `${id}-rule`, source_prefixes: sources, domains: [], domain_set_ids: domainSetIDs, outcome }] } };
}

function objectGroupPayload(page, rowIndex = null) {
  const values = modalControls();
  const row = Number.isInteger(rowIndex) ? networkRowsForPage(page.id)[rowIndex] : null;
  const name = values[0] || row?.[0] || 'object-group';
  return { id: row?._resourceId || slugID(name), name, kind: page.id === 'object/urlgrp_list' ? 'domain' : 'ip', description: values[1] || row?.[1] || '', members: (values[2] || row?.[6] || '').split(/[\s,]+/).filter(Boolean), enabled: true };
}

function trafficControlPayload(rowIndex = null) {
  const row = Number.isInteger(rowIndex) ? networkRowsForPage('flowcontrol/flowct_main')[rowIndex] : null;
  const form = el.modal.querySelector('[data-flow-control-form]');
  const priority = Number(form?.querySelector('[data-flow-priority]')?.value || row?.[0] || Date.now());
  const rate = Math.round(Number(form?.querySelector('[data-flow-rate]')?.value || 0) * 1000000);
  const direction = form?.querySelector('[data-flow-direction]')?.value || 'both';
  const action = form?.querySelector('[data-flow-action]')?.value || '限速';
  const ruleID = `flow-${priority}`;
  return { id: row?._resourceId || ruleID, rules: [{ id: ruleID, granularity: 'rule', match: { sources: addressConditionValues(form, 'flow-src'), destinations: addressConditionValues(form, 'flow-dst'), source_ports: stringList(form?.querySelector('[data-flow-src-port]')?.value), dest_ports: stringList(form?.querySelector('[data-flow-dst-port]')?.value), protocols: [form?.querySelector('[data-flow-protocol]')?.value || 'any'], direction }, actions: action === '阻断' ? [{ kind: 'drop' }] : [{ kind: 'policer', policer: { rate_bps: rate, burst_bps: Math.max(8000, Math.round(rate / 10)) } }] }] };
}

function slugID(value) {
  return String(value || '').trim().toLowerCase().replace(/[^a-z0-9_-]+/g, '-').replace(/^-+|-+$/g, '') || `item-${Date.now()}`;
}

function dhcpServerPayload(rowIndex = null) {
  const row = Number.isInteger(rowIndex) ? networkRowsForPage('network/dhcpsvr_main')[rowIndex] : null;
  const form = el.modal.querySelector('[data-dhcp-form]');
  const interfaceID = (form?.querySelector('[data-dhcp-interface]')?.value || row?.[1] || '').split(' ')[0];
  if (!interfaceID || interfaceID === '请选择接口') throw new Error('没有可用的 LAN 接口，请先创建 LAN 接口');
  const pool = form?.querySelector('[data-dhcp-pool]')?.value.trim() || '';
  const prefix = form?.querySelector('[data-dhcp-prefix]')?.value.trim() || '24';
  const subnet = pool.includes('-') ? subnetFromPool(pool, prefix) : pool || row?.[2] || '192.168.88.0/24';
  const pools = row?.[2] && row[2].includes('-') ? [row[2]] : pool.includes('-') ? [pool] : deriveDhcpPools(subnet);
  const gateway = form?.querySelector('[data-dhcp-gateway]')?.value.trim() || row?.[3] || '';
  return { id: row?._resourceId || `dhcp-${interfaceID}`, name: `DHCP ${interfaceID}`, engine: 'kea', enabled: true, interface_id: interfaceID, subnet, pools, routers: gateway ? [gateway] : [], name_servers: [], lease_time_seconds: leaseSeconds(form?.querySelector('[data-dhcp-lease]')?.value || row?.[5]) };
}

function dhcpPayloadForRow(rowIndex = null) {
  const row = Number.isInteger(rowIndex) ? networkRowsForPage('network/dhcpsvr_main')[rowIndex] : null;
  if (row?._resourceKey === 'dhcpBindings' || row?.[0] === '静态分配') return dhcpBindingPayload(rowIndex);
  if (row?._resourceKey === 'dhcpServers' || row?.[0] === '服务列表') return dhcpServerPayload(rowIndex);
  return (state.activeTabs['network/dhcpsvr_main'] || 0) === 1 ? dhcpBindingPayload(rowIndex) : dhcpServerPayload(rowIndex);
}

function deriveDhcpPools(subnet) {
  const match = String(subnet || '').trim().match(/^(\d+\.\d+\.\d+)\.0\/24$/);
  if (!match) return [];
  return [`${match[1]}.100-${match[1]}.199`];
}

function subnetFromPool(pool, mask) {
  const first = String(pool || '').split('-')[0]?.trim();
  const prefix = maskToPrefix(mask) || 24;
  const octets = first.split('.').map((item) => Number(item));
  if (octets.length !== 4 || octets.some((item) => Number.isNaN(item))) return `192.168.88.0/${prefix}`;
  if (prefix === 24) return `${octets[0]}.${octets[1]}.${octets[2]}.0/24`;
  return `${octets.join('.')}/${prefix}`;
}

function maskToPrefix(mask) {
  const value = String(mask || '').trim();
  if (/^\d{1,2}$/.test(value)) return Math.min(32, Math.max(0, Number(value)));
  const octets = value.split('.').map((item) => Number(item));
  if (octets.length !== 4 || octets.some((item) => Number.isNaN(item))) return 0;
  return octets.map((item) => item.toString(2).padStart(8, '0')).join('').replace(/0+$/, '').length;
}

function ipPrefix(value, fallback, max = 32) {
  const numeric = Number(String(value || '').trim());
  if (!Number.isFinite(numeric)) return fallback;
  return Math.min(max, Math.max(0, numeric));
}

function leaseSeconds(value) {
  const text = String(value || '12小时');
  const hours = Number(text.match(/\d+/)?.[0] || 12);
  return hours * 3600;
}

function dhcpBindingPayload(rowIndex = null) {
  const values = modalControls();
  const row = Number.isInteger(rowIndex) ? networkRowsForPage('network/dhcpsvr_main')[rowIndex] : null;
  return { id: row?._resourceId || (values[0] || '').replaceAll(':', '') || `binding-${Date.now()}`, mac: values[0] || '', ip: values[1] || '', ip_address: values[1] || '', hostname: values[2] || '', enabled: true };
}

function interfacePayload(rowIndex = null) {
  const values = modalControls();
  const row = Number.isInteger(rowIndex) ? networkRowsForPage('monitor/interface_list')[rowIndex] : null;
  const id = row?._resourceId || row?.[0] || `if-${Date.now()}`;
  const role = normalizeInterfaceRole(values[0] || row?.[3] || '');
  if (!role) throw new Error('管理口不能配置为 WAN 或 LAN');
  const payload = { id, name: row?.[0] || id, gateway_role: role, mode_role: { gateway: role, bridge: null }, role_configured: true, runtime_state: 'role_configured' };
  if (row?._resourceKey === 'interfaceBonds') {
    payload.members = stringList(String(row[4] || '').replace(/^成员\s*/, ''));
    payload.work_mode = workModeValueFromDisplay(row[2]);
  }
  return payload;
}
function interfaceBondPayload() {
  const name = el.modal.querySelector('[data-bond-name]')?.value.trim() || '';
  const members = Array.from(el.modal.querySelectorAll('[data-bond-member]:checked')).map((item) => item.value).filter(Boolean);
  const randomID = globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  return { id: `bond-${randomID}`, name, members };
}
function workModeValueFromDisplay(value) {
  const text = String(value || '').trim();
  if (text === 'XDP快速路径') return 'af_xdp';
  if (text === 'VPP高速转发') return 'vpp';
  if (text === 'Linux普通转发') return 'linux';
  if (text === '内核管理通道') return 'kernel_stack';
  return text || 'vpp';
}
function normalizeInterfaceRole(value) {
  const role = String(value || '').trim().toLowerCase();
  if (role === 'wan' || role === 'wan接口') return 'wan';
  if (role === 'lan' || role === 'lan接口') return 'lan';
  return '';
}
async function submitInterfaceBondModal() {
  try {
    const payload = interfaceBondPayload();
    if (!payload.name) {
      toast('请输入聚合组名称');
      return;
    }
    const duplicateName = envelopeItems(state.controlPlane.resources?.interfaceBonds).some((item) => String(item.name || '').trim().toLowerCase() === payload.name.toLowerCase());
    if (duplicateName) {
      toast('聚合组名称已存在');
      return;
    }
    if (payload.members.length < 2) {
      toast('请选择至少两张成员网卡');
      return;
    }
    await apiJSON(resourceEndpoints.interfaceBonds, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
    const applyResult = await apiJSON(controlApi.runtimeApply, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
    state.controlPlane.runtimeApply = applyResult;
    closeModal(true);
    toast(`${payload.name} 已创建并下发运行态`);
    await refreshControlPlane({ resourcesOnly: true });
  } catch (error) {
    toast(error.message || '静态链路聚合创建失败');
  }
}
function wireConfigTimeSync(page) {
  if (page.id !== 'system/sys_config') return;
  const timeInput = el.workspace.querySelector('[data-config-time-input]');
  const syncButton = el.workspace.querySelector('[data-config-time-sync]');
  const saveButton = el.workspace.querySelector('[data-config-time-save]');
  saveButton?.addEventListener('click', () => {
    if (timeInput) timeInput.value = formatBrowserDateTime();
    toast(`时间同步已保存：${timeInput?.value.trim() || '未填写'}`);
  });
}
function handleAction(action, page) {
  if (action.startsWith('runtime-')) { handleRuntimeAction(action); return; }
  if (action.startsWith('firmware-')) { handleFirmwareAction(action); return; }
  if (action === 'batch') { state.batchOpen = !state.batchOpen; renderWorkspace(); return; }
  if (page.id === 'system/sys_config') { handleConfigAction(action); return; }
  if (page.id === 'system/webuser_main' && action === 'add') { openModal('新增系统用户', systemUserFormHtml()); pendingModalSubmit = () => submitAuthUser('add'); return; }
  if (action === 'add' && isObjectGroupPage(page)) { openModal(`新增${page.title}`, objectGroupAddFormHtml(page)); pendingModalSubmit = () => submitResourceModal(page, 'add'); return; }
  if (action === 'add' && page.id === 'network/proxy_main') { openModal((state.activeTabs[page.id] || 0) === 1 ? '新增WAN' : '新增LAN', formHtml(page)); pendingModalSubmit = () => submitResourceModal(page, 'add'); return; }
  if (action === 'add' && page.id === 'network/wangroup_manager') { openModal('新增群组', wanGroupLineFormHtml()); pendingModalSubmit = () => submitResourceModal(page, 'add'); return; }
  if (action === 'add' && page.id === 'monitor/interface_list') { openModal('创建静态链路聚合', interfaceBondFormHtml()); pendingModalSubmit = () => submitInterfaceBondModal(); return; }
  if (action === 'add' && page.id === 'network/dhcpsvr_main') { openModal(dhcpAddTitle(page), formHtml(page)); pendingModalSubmit = () => submitResourceModal(page, 'add'); return; }
  if (action === 'add-line' && page.id === 'network/wangroup_manager') { openModal('新增群组', wanGroupLineFormHtml()); pendingModalSubmit = () => submitResourceModal(page, 'add'); return; }
  if (action === 'add' && isRoutePolicyContext(page)) { openModal('添加策略', routePolicyFormHtml(), 'route-policy'); pendingModalSubmit = () => submitResourceModal(page, 'add'); return; }
  if (action === 'add' && page.id === 'route/portmap_list') { openModal('新增映射', portMapFormHtml(), 'route-policy'); pendingModalSubmit = () => submitResourceModal(page, 'add'); return; }
  if (action === 'add' && page.id === 'flowcontrol/flowct_main') { openModal('新增流量控制', flowControlFormHtml(), 'route-policy'); pendingModalSubmit = () => submitResourceModal(page, 'add'); return; }
  if (['add', 'edit', 'import'].includes(action)) { openModal(action === 'add' ? `新增${page.title}` : action === 'edit' ? `编辑${page.title}` : `导入${page.title}`, formHtml(page)); pendingModalSubmit = () => submitResourceModal(page, action); return; }
  if (action === 'delete') { confirmDeleteSelectedResources(page); return; }
  toast(`${page.title}：已提交`);
}
function isRoutePolicyContext(page) {
  return page.id === 'route/route_policy_main';
}
function isObjectGroupPage(page) {
  return page.id === 'object/urlgrp_list' || page.id === 'object/iptab_list';
}
function isRoutePolicyRow(page, rowIndex) {
  const row = networkRowsForPage('route/route_policy_main')[rowIndex];
  return isRoutePolicyContext(page) && row && isRouteIpv6Row(row) === ((state.activeTabs[page.id] || 0) === 1);
}
function routePolicyFormHtml(rowIndex) {
  const row = Number.isInteger(rowIndex) ? networkRowsForPage('route/route_policy_main')[rowIndex] : null;
  const resource = row?._resource || {};
  const sequence = row?.[0] || '';
  const remark = resource.name || row?.[9] || '';
  const sourceAddress = resource.match?.sources || resource.match?.src_ip || row?.[2] || '';
  const sourcePort = row?.[3] || '';
  const destinationAddress = resource.match?.destinations || resource.match?.dst_ip || row?.[4] || '';
  const destinationPort = routePolicyPortValue(row?.[5]);
  const actionValue = String(resource.action || row?.[1] || '').toLowerCase();
  const action = actionValue === 'route' || String(row?.[1] || '').includes('路由') ? '路由' : 'NAT';
  const line = resource.egress || row?.[6] || '';
  const nextHop = row?.[7] || '';
  const lineLabel = action === '路由' ? '路由线路' : 'NAT线路';
  return `<div class="route-policy-form" data-route-policy-form><div class="route-policy-fields route-policy-top"><label data-required><span>策略序号</span><span class="route-policy-control"><input data-route-priority type="number" min="1" max="65535" value="${safeText(sequence)}"><small>序号从小往大匹配，范围1-65535</small></span></label><label><span>策略备注</span><input data-route-name value="${safeText(remark)}"></label></div><section class="route-policy-section"><h3>匹配条件</h3><div class="route-policy-fields"><label class="route-policy-split"><span>源 / 目的地址</span><span class="route-policy-pair route-address-summary-pair">${addressConditionSummaryHtml('src', '源地址', sourceAddress)}<em>/</em>${addressConditionSummaryHtml('dst', '目的地址', destinationAddress)}</span></label><label class="route-policy-split"><span>源 / 目的端口</span><span class="route-policy-pair"><input data-route-src-port value="${safeText(sourcePort)}" placeholder="0"><em>/</em><input data-route-dst-port value="${safeText(destinationPort)}" placeholder="0"></span></label></div></section><section class="route-policy-section"><h3>执行动作</h3><div class="route-policy-fields"><label><span>执行动作</span><span class="route-policy-action"><select data-route-action>${routePolicyOptions(['NAT', '路由'], action)}</select><span class="route-policy-check" data-full-cone><input type="checkbox">全锥型NAT</span></span></label><label><span data-route-line-label>${lineLabel}</span><select data-route-line>${lineSelectOptionsHtml(line)}</select></label><label><span>下一跳</span><input data-route-next-hop value="${safeText(nextHop)}"></label></div></section></div>`;
}

function portMapFormHtml(rowIndex) {
  const row = Number.isInteger(rowIndex) ? networkRowsForPage('route/portmap_list')[rowIndex] : null;
  const resource = row?._resource || {};
  const name = resource.name || row?.[0] || '';
  const wanLink = resource.wan_link || row?.[1] || '';
  const externalPort = displayValue(resource.external_port, row?.[2]);
  const internalHost = resource.internal_host || row?.[3] || '';
  const internalPort = displayValue(resource.internal_port, row?.[4]);
  const protocol = String(resource.protocol || row?.[5] || 'TCP').toUpperCase();
  const description = resource.description || row?.[7] || '';
  return `<div class="route-policy-form" data-portmap-form><div class="route-policy-fields route-policy-top"><label data-required><span>规则名</span><input data-portmap-name value="${safeText(name)}"></label><label data-required><span>映射线路</span><select data-portmap-wan>${lineSelectOptionsHtml(wanLink)}</select></label><label><span>协议</span><select data-portmap-protocol>${optionValueLabel('TCP', 'TCP', protocol)}${optionValueLabel('UDP', 'UDP', protocol)}${optionValueLabel('TCP_UDP', 'TCP+UDP', protocol)}</select></label></div><section class="route-policy-section"><h3>端口映射</h3><div class="route-policy-fields"><label data-required><span>外部端口</span><input data-portmap-external-port type="number" min="1" max="65535" value="${safeText(externalPort)}"></label><label data-required><span>内网主机</span><input data-portmap-internal-host value="${safeText(internalHost)}" placeholder="192.168.88.10"></label><label data-required><span>内网端口</span><input data-portmap-internal-port type="number" min="1" max="65535" value="${safeText(internalPort)}"></label><label><span>备注</span><input data-portmap-description value="${safeText(description)}"></label></div></section></div>`;
}
function addressConditionSummaryHtml(name, label, values = []) {
  const entries = stringList(values).filter((value) => value && value.toLowerCase() !== 'any');
  return `<textarea readonly rows="3" data-address-summary="${name}" data-address-label="${label}" data-address-values="${escapeAttr(JSON.stringify(entries))}" aria-label="编辑${label}" placeholder="任意">${safeText(entries.join('\n'))}</textarea>`;
}

function addressConditionRowHtml(name, type = 'literal', value = '') {
  const groupOptions = objectGroupOptionsHtml('ip', type === 'ip_group' ? value : '').replace('不选择', '请选择 IP 组');
  const literal = type !== 'ip_group' ? value : '';
  const placeholder = type === 'range' ? '192.168.1.10-192.168.1.20' : type === 'ipv6' ? '2001:db8::/64' : '192.168.1.0/24';
  return `<div class="condition-row" data-address-row><select data-address-type aria-label="条件类型">${routePolicyOptionsWithValues([['literal', 'IPv4 地址 / 网段'], ['ipv6', 'IPv6 地址 / 网段'], ['range', 'IPv4 起止范围'], ['ip_group', 'IP 组']], type)}</select><input data-address-value value="${safeText(literal)}" placeholder="${placeholder}"><select data-address-group>${groupOptions}</select><button type="button" class="condition-delete" data-address-delete aria-label="删除条件">×</button></div>`;
}

function routePolicyOptionsWithValues(options, selected) {
  return options.map(([value, label]) => optionValueLabel(value, label, value === selected)).join('');
}

function addressConditionValues(form, name) {
  const summary = form?.querySelector(`[data-address-summary="${name}"]`);
  const values = JSON.parse(summary?.dataset.addressValues || '[]');
  return values.length ? values : ['any'];
}

function objectGroupOptionsHtml(kind, selected = '') {
  const groups = envelopeItems(state.controlPlane.resources.objectGroups).filter((item) => objectGroupType(item) === kind);
  return `${optionValueLabel('', '不选择', selected)}${groups.map((item) => optionValueLabel(item.id || item.name, item.name || item.id, selected)).join('')}`;
}

function objectGroupMembers(id) {
  const group = envelopeItems(state.controlPlane.resources.objectGroups).find((item) => (item.id || item.name) === id);
  return stringList(group?.members || group?.entries);
}
function routePolicyPortValue(value) {
  const text = String(value || '').trim();
  if (!text || text.toLowerCase() === 'any') return '';
  return text.replace(/^(tcp|udp|icmp)\s+/i, '');
}
function routePolicyOptions(options, selected) {
  return options.map((option) => `<option ${option === selected ? 'selected' : ''}>${safeText(option)}</option>`).join('');
}
function optionValueLabel(value, label, selected = '') {
  if (value === 'sticky') return '';
  const legacyWANMode = { flow_hash: 'five_tuple', failover: 'primary_backup' };
  if (Object.prototype.hasOwnProperty.call(legacyWANMode, value)) {
    value = legacyWANMode[value];
  }
  selected = legacyWANMode[selected] || selected;
  return `<option value="${escapeAttr(value)}" ${value === selected ? 'selected' : ''}>${safeText(label)}</option>`;
}
function lineSelectOptionsHtml(selected = '') {
  const rows = lineOptionItems({ includeProxy: true, includeGroups: true });
  const hasSelected = rows.some((item) => item.value === selected);
  if (!rows.length) return optionValueLabel('', '请先创建 WAN 线路', selected);
  return `${optionValueLabel('', '不指定', selected)}${rows.map((item) => optionValueLabel(item.value, item.label, selected)).join('')}${selected && !hasSelected ? optionValueLabel(selected, selected, selected) : ''}`;
}

function lineOptionItems({ includeProxy = true, includeGroups = true } = {}) {
  return [
    ...wanLineRows({ includeProxy }).map((row) => ({ value: wanLineID(row), label: wanLineLabel(row) })),
    ...(includeGroups ? networkRowsForPage('network/wangroup_manager').map((row) => ({ value: row._resourceId || row[0], label: `WAN组 · ${row[0]}` })) : [])
  ].filter((item) => item.value);
}
function interfaceSelectOptions(direction) {
  const role = direction === 'WAN' ? 'WAN接口' : direction === 'LAN' ? 'LAN接口' : '';
  const options = networkRowsForPage('monitor/interface_list').filter((row) => row[3] !== '管理口' && row[3] !== '聚合接口' && (!role || row[3] === role)).map((row) => row[0]).filter(Boolean);
  return options.length ? options : ['请先在网卡设置中标记接口'];
}
function proxyEgressOptions(selected = '') {
  const rows = lineOptionItems({ includeProxy: false, includeGroups: false });
  if (!rows.length) return optionValueLabel('', '请先创建 WAN 线路', selected);
  const hasSelected = rows.some((item) => item.value === selected);
  return `${rows.map((item) => optionValueLabel(item.value, item.label, selected)).join('')}${selected && !hasSelected ? optionValueLabel(selected, selected, selected) : ''}`;
}

function wanLineRows({ includeProxy = true } = {}) {
  return networkRowsForPage('network/proxy_main').filter((row) => row[1] === 'WAN线路' && row._resourceKey === 'wanLinks' && (includeProxy || row._wanType !== 'proxy'));
}

function wanLineID(row) {
  return row?._resourceId || row?._systemId || row?.[0] || '';
}

function wanLineLabel(row) {
  return [row?.[0], row?.[12]].filter(Boolean).join(' · ') || wanLineID(row);
}

function bondMemberRows() {
  return networkRowsForPage('monitor/interface_list').filter((row) => row[3] !== '管理口' && row[3] !== '聚合接口' && !String(row[4] || '').startsWith('成员') && !String(row[4] || '').startsWith('聚合组'));
}
function lanBridgeMemberRows() {
  return networkRowsForPage('monitor/interface_list').filter((row) => row[3] === 'LAN接口' && row._resourceKind !== 'lan_bridge' && !String(row[4] || '').startsWith('成员') && !String(row[4] || '').startsWith('聚合组'));
}
function flowControlFormHtml() {
  return `<div class="route-policy-form" data-flow-control-form><div class="route-policy-fields route-policy-top"><label data-required><span>策略序号</span><span class="route-policy-control"><input data-flow-priority type="number" min="1" max="65535" value=""><small>序号从小往大匹配，范围1-65535</small></span></label></div><section class="route-policy-section"><h3>匹配条件</h3><div class="route-policy-fields"><label class="route-policy-split"><span>源 / 目的地址</span><span class="route-policy-pair route-address-summary-pair">${addressConditionSummaryHtml('flow-src', '源地址')}<em>/</em>${addressConditionSummaryHtml('flow-dst', '目的地址')}</span></label><label class="route-policy-split"><span>源 / 目的端口</span><span class="route-policy-pair"><input data-flow-src-port placeholder="0"><em>/</em><input data-flow-dst-port placeholder="0"></span></label><label><span>协议</span><select data-flow-protocol>${routePolicyOptionsWithValues([['any', 'Any'], ['tcp', 'TCP'], ['udp', 'UDP'], ['icmp', 'ICMP']], 'any')}</select></label></div></section><section class="route-policy-section"><h3>执行动作</h3><div class="route-policy-fields"><label><span>执行动作</span><span class="route-policy-action"><select data-flow-action>${routePolicyOptions(['限速', '阻断'], '限速')}</select></span></label><label data-flow-speed-field><span>流控方向</span><select data-flow-direction>${routePolicyOptionsWithValues([['both', '双向'], ['uplink', '上行'], ['downlink', '下行']], 'both')}</select></label><label data-flow-speed-field><span>限速大小</span><span class="route-policy-control route-policy-unit-control"><span class="route-policy-unit-input"><input data-flow-rate type="number" min="0.064" step="0.001" value="" aria-label="限速大小"><em>Mbps</em></span></span></label></div></section></div>`;
}
function wanGroupLineFormHtml(rowIndex) {
  const row = Number.isInteger(rowIndex) ? networkRowsForPage('network/wangroup_manager')[rowIndex] : null;
  const resource = row?._resource || {};
  const groupName = resource.name || row?.[0] || '';
  const selectedMembers = wanGroupMembers(resource).length ? wanGroupMembers(resource) : String(row?.[1] || '').split(',').map((item) => item.trim()).filter((item) => item && item !== '未选择线路');
  const weights = resource.weights || {};
  const rows = wanLineRows({ includeProxy: false });
  const memberControls = rows.length ? rows.map((member) => {
    const id = wanLineID(member);
    return `<label class="wan-member-card"><input data-wan-member type="checkbox" value="${escapeAttr(id)}" ${selectedMembers.includes(id) ? 'checked' : ''}><span><strong>${safeText(member[0])}</strong><small>${safeText(wanLineLabel(member))}</small></span><span class="wan-weight-field"><input data-wan-member-weight="${escapeAttr(id)}" type="number" min="1" max="100" value="${safeText(weights[id] || 1)}" aria-label="${safeText(member[0])} 占用比例"><em>%</em></span></label>`;
  }).join('') : '<p class="empty-hint">请先在 LAN/WAN 页面创建 WAN 线路。</p>';
  const mode = resource.load_balance_mode || resource.mode || 'primary_backup';
  const hash = resource.hash_fields || resource.load_balance || 'five_tuple';
  const memberOptions = rows.map((member) => optionValueLabel(wanLineID(member), member[0], resource.primary_member || ''));
  const backupOptions = rows.map((member) => optionValueLabel(wanLineID(member), member[0], resource.backup_member || ''));
  return `<div class="lan-edit-form wangroup-edit-form wan-load-form" data-wan-group-form><label><span>群组名称</span><input data-wan-group-name value="${safeText(groupName)}"></label><label data-required><span>负载模式</span><select data-wan-group-mode>${optionValueLabel('primary_backup', '主备模式', mode)}${optionValueLabel('weighted', '加权负载', mode)}${optionValueLabel('flow_hash', '五元组负载', mode)}</select></label><label data-wan-group-hash-field><span>负载效果</span><select data-wan-group-hash>${optionValueLabel('five_tuple', '五元组', hash)}${optionValueLabel('src-dst-ip-port', '源/目的IP+端口', hash)}${optionValueLabel('src-ip', '源IP', hash)}${optionValueLabel('dst-ip', '目的IP', hash)}</select></label><section class="wan-group-mode-panel is-hidden" data-wan-failover-fields><label><span>主线路</span><select data-wan-primary>${memberOptions.join('')}</select></label><label><span>备选线路</span><select data-wan-backup>${backupOptions.join('')}</select></label></section><fieldset class="form-fieldset wan-member-selector"><legend>选择线路</legend><div class="wan-member-grid">${memberControls}</div><small class="form-note" data-wan-weight-note>加权负载按每条线路百分比发送，合计建议为 100%。</small></fieldset></div>`;
}
function formHtml(page, row = 0) {
  if (isNetworkContentPage(page)) return networkFormHtml(page, row);
  return `<div class="form-grid"><label>名称<input value=""></label><label>对象<input value=""></label><label>状态<select><option>启用</option><option>停用</option></select></label><label>接口<select><option>请选择接口</option></select></label><label>产品范围<input value="固定网关模式" readonly></label><label>备注<input value=""></label></div>`;
}
function systemUserFormHtml(user = '') {
  return `<div class="form-grid system-user-add-form"><label data-required>用户名<input autocomplete="username" value="${safeText(user)}" ${user ? 'readonly' : ''}></label><label>角色<select><option value="admin">admin</option><option value="readonly">readonly</option></select></label><label data-required><span>密码</span><input autocomplete="new-password" type="password" value=""></label><label data-required><span>确认密码</span><input autocomplete="new-password" type="password" value=""></label></div>`;
}
function networkFormHtml(page, row = 0) {
  if (page.id === 'monitor/interface_list') return interfaceFormHtml(row);
  if (page.id === 'network/proxy_main') return lanInterfaceFormHtml(row);
  if (page.id === 'network/dhcpsvr_main') return dhcpServiceFormHtml(row);
  if (page.id === 'route/dnspolicy_main') return dnsPolicyFormHtml(row);
  if (page.id === 'flowcontrol/flowct_main') return flowControlFormHtml();
  if (isObjectGroupPage(page)) return objectGroupAddFormHtml(page);
  const config = networkPages[page.id];
  const rows = networkRowsForPage(page.id);
  const rowData = rows[row % rows.length] || [];
  const fields = config.form || config.columns.slice(0, 6).map((label, index) => [label, rowData[index] || '']);
  const hasStatus = fields.some(([label]) => label.includes('状态'));
  const skipStatus = page.id === 'route/portmap_list';
  return `<div class="form-grid">${fields.map(([label, fallback]) => `<label>${safeText(label)}<input value="${safeText(fallback)}"></label>`).join('')}${hasStatus || skipStatus ? '' : '<label>状态<select><option>启用</option><option>禁用</option></select></label>'}</div>`;
}
function objectGroupAddFormHtml() {
  return `<div class="lan-edit-form"><label data-required><span>群组名</span><input value=""></label><label><span>备注</span><input value=""></label></div>`;
}
function objectGroupEditFormHtml(page, rowIndex) {
  const row = Number.isInteger(rowIndex) ? networkRowsForPage(page.id)[rowIndex] : null;
  const groupName = row?.[0] || '';
  const remark = row?.[1] || '';
  const isDomain = page.id === 'object/urlgrp_list';
  const memberLabel = isDomain ? '域名' : '多个IP/掩码';
  const memberValue = isDomain ? domainGroupMembers(groupName) : ipGroupMembers(groupName);
  return `<div class="route-policy-form"><section class="route-policy-section"><h3>群组信息</h3><div class="route-policy-fields"><label data-required><span>群组名</span><input value="${safeText(groupName)}"></label><label><span>备注</span><input value="${safeText(remark)}"></label></div></section><section class="route-policy-section"><h3>成员维护</h3><div class="route-policy-fields"><label><span>${memberLabel}</span><textarea>${safeText(memberValue)}</textarea></label><label><span>文件导入</span><input type="file"></label><label><span>导入方式</span><select>${routePolicyOptions(['追加', '覆盖'], '追加')}</select></label></div></section></div>`;
}
function domainGroupMembers(groupName) { return objectGroupMembers('object/urlgrp_list', groupName); }
function ipGroupMembers(groupName) { return objectGroupMembers('object/iptab_list', groupName); }
function objectGroupMembers(pageId, groupName) {
  const row = networkRowsForPage(pageId).find((item) => item[0] === groupName);
  return row?.[6] || '';
}
function dhcpServiceFormHtml(row = 0) {
  if ((state.activeTabs['network/dhcpsvr_main'] || 0) === 1) return dhcpStaticAllocationFormHtml(row);
  const services = networkRowsForPage('network/dhcpsvr_main').filter((item) => item[0] === '服务列表');
  const rowData = services[row % services.length] || services[0] || [];
  const lanRows = networkRowsForPage('network/proxy_main').filter((item) => item[1] === 'LAN接口');
  const lanOptions = lanRows.length ? lanRows.map((item) => `${item[0]} (${item[2]})`) : ['请选择接口'];
  const selectedLanOption = rowData[1] ? `${rowData[1]} (${rowData[2] || ''})` : lanOptions[0];
  return `<div class="lan-edit-form dhcp-service-form" data-dhcp-form><label data-required><span>LAN接口</span><select data-dhcp-interface>${routePolicyOptions(lanOptions, selectedLanOption)}</select></label><label data-required><span>IP分配范围</span><input data-dhcp-pool value="${safeText(rowData[2] || '')}" placeholder="192.168.88.100-192.168.88.199"></label><label data-required><span>线路掩码</span><input data-dhcp-prefix value="24" placeholder="24"></label><label><span>网关</span><input data-dhcp-gateway value="${safeText(rowData[3] || '')}"></label><label><span>租约时间</span><select data-dhcp-lease>${routePolicyOptions(['4小时', '8小时', '12小时', '24小时'], rowData[5] || '12小时')}</select></label></div>`;
}
function dhcpStaticAllocationFormHtml(row = 0) {
  const allocations = networkRowsForPage('network/dhcpsvr_main').filter((item) => item[0] === '静态分配');
  const rowData = allocations[row % allocations.length] || allocations[0] || [];
  return `<div class="lan-edit-form dhcp-service-form"><label data-required><span>终端MAC</span><input value="${safeText(rowData[4] || '')}"></label><label data-required><span>终端IP</span><input value="${safeText(rowData[2] || '')}"></label><label><span>备注</span><input value="${safeText(rowData[7] || '')}"></label></div>`;
}
function dnsPolicyFormHtml(row = 0) {
  const rows = networkRowsForPage('route/dnspolicy_main');
  const rowData = rows[row % rows.length] || rows[0] || [];
  const resource = rowData._resource || {};
  const rule = resource.policy?.rules?.[0] || {};
  const outcome = rule.outcome || {};
  const action = outcome.kind === 'reject' ? '拉黑' : outcome.kind === 'fixed_answer' ? '解析' : rowData[5] || '放行';
  return `<div class="form-grid dns-policy-form" data-dns-policy-form><label><span>策略名称</span><input data-dns-name value="${safeText(resource.name || rowData[0] || '')}"></label><label><span>源IP / 网段</span><span class="route-address-summary-pair">${addressConditionSummaryHtml('dns-src', '源IP / 网段', rule.source_prefixes || rowData[1] || '')}</span></label><label data-required><span>目的域名组</span><select data-dns-domain-group>${objectGroupOptionsHtml('domain', rule.domain_set_ids?.[0] || '')}</select></label><label><span>解析上游</span><input data-dns-upstream value="${safeText((outcome.fixed_answers || []).join('') || rowData[4] || '')}" placeholder="例如 223.5.5.5" pattern="^\\d{1,3}(?:\\.\\d{1,3}){3}$"></label><label><span>动作</span><select data-dns-action>${routePolicyOptions(['放行', '解析', '拉黑'], action)}</select></label></div>`;
}
function lanInterfaceFormHtml(row = 0) {
  const rows = networkRowsForPage('network/proxy_main');
  const rowData = rows[row % rows.length] || rows[0] || [];
  const resource = rowData._resource || {};
  const isWan = (state.activeTabs['network/proxy_main'] || 0) === 1;
  if (isWan) {
    const selectedType = rowData._wanType === 'proxy' ? 'proxy' : wanFormType(rowData._wanType || rowData[12] || 'static4');
    const address = String(rowData[2] || '');
    const [addressValue, prefixValue] = address.split('/');
    const wanTypes = [
      ['pppoe', 'PPPoE'],
      ['static4', '固定IP-v4'],
      ['static6', '固定IP-v6'],
      ['dhcp4', 'DHCP-v4'],
      ['dhcp6', 'DHCP-v6'],
      ['proxy', '代理线路']
    ].map(([value, label]) => optionValueLabel(value, label, selectedType)).join('');
    return `<div class="lan-edit-form wan-edit-form" data-wan-form>
      <label data-required><span>名称</span><input data-wan-name value="${row ? safeText(rowData[0] || '') : ''}"></label>
      <label data-required data-wan-field="pppoe static4 static6 dhcp4 dhcp6"><span>网卡</span><select data-wan-interface>${routePolicyOptions(interfaceSelectOptions('WAN'), row ? rowData[0] : interfaceSelectOptions('WAN')[0])}</select></label>
      <label data-required><span>线路类型</span><select data-wan-type>${wanTypes}</select></label>
      <label data-wan-field="pppoe" data-required><span>账号</span><input data-wan-username></label>
      <label data-wan-field="pppoe" data-required><span>密码</span><input data-wan-password type="password"></label>
      <label data-wan-field="pppoe static4 static6 dhcp4 dhcp6" data-required><span>MTU</span><input data-wan-mtu value="${row ? safeText(rowData[6] || '1500') : '1500'}"></label>
      <label data-wan-field="pppoe static4 static6 dhcp4 dhcp6" data-required><span>上行带宽</span><span class="route-policy-unit-input"><input data-wan-upload-mbps type="number" min="1" step="1" value="${safeText(resource.smart_qos_upload_kbps ? Math.round(resource.smart_qos_upload_kbps / 1000) : '')}" placeholder="例如 100"><em>M</em></span></label>
      <label data-wan-field="pppoe static4 static6 dhcp4 dhcp6" data-required><span>下行带宽</span><span class="route-policy-unit-input"><input data-wan-download-mbps type="number" min="1" step="1" value="${safeText(resource.smart_qos_download_kbps ? Math.round(resource.smart_qos_download_kbps / 1000) : '')}" placeholder="例如 500"><em>M</em></span></label>
      <label data-wan-field="static4" data-required><span>IPv4地址</span><input data-wan-ipv4 value="${selectedType === 'static4' ? safeText(addressValue || '') : ''}" placeholder="203.0.113.18"></label>
      <label data-wan-field="static4" data-required><span>IPv4前缀</span><input data-wan-ipv4-prefix value="${selectedType === 'static4' ? safeText(prefixValue || '24') : '24'}" placeholder="24"></label>
      <label data-wan-field="static4" data-required><span>IPv4网关</span><input data-wan-ipv4-gateway value="${selectedType === 'static4' ? safeText(rowData[3] || '') : ''}"></label>
      <label data-wan-field="static6" data-required><span>IPv6地址</span><input data-wan-ipv6 value="${selectedType === 'static6' ? safeText(addressValue || '') : ''}" placeholder="2001:db8::2"></label>
      <label data-wan-field="static6" data-required><span>IPv6前缀</span><input data-wan-ipv6-prefix value="${selectedType === 'static6' ? safeText(prefixValue || '64') : '64'}" placeholder="64"></label>
      <label data-wan-field="static6" data-required><span>IPv6网关</span><input data-wan-ipv6-gateway value="${selectedType === 'static6' ? safeText(rowData[3] || '') : ''}" placeholder="fe80::1"></label>
      <label data-wan-field="proxy" data-required><span>承载出口</span><select data-proxy-underlay>${proxyEgressOptions(rowData._resource?.underlay_wan_id || rowData._resource?.underlay || '')}</select></label>
      <label data-wan-field="proxy"><span>业务名称</span><input data-proxy-business-name value="${row ? safeText(rowData[0] || '') : ''}" placeholder="订阅或节点名称"></label>
      <label data-wan-field="proxy"><span>订阅/节点地址</span><textarea data-proxy-address placeholder="https://example.com/sub 或 vless://..."></textarea></label>
      <label><span>备注</span><input data-wan-remark value="${row ? safeText(rowData[11] || '') : ''}"></label>
    </div>`;
  }
  const isBridge = rowData[1] === 'LAN桥';
  const bridgeMembers = bridgeMemberOptions(rowData[11]);
  return `<div class="lan-edit-form lan-bridge-form" data-lan-form>
    <label data-required><span>名称</span><input data-lan-name value="${row ? safeText(rowData[0] || '') : ''}"></label>
    <label data-required><span>接口类型</span><select data-lan-kind><option value="lan_interface" ${isBridge ? '' : 'selected'}>LAN接口</option><option value="lan_bridge" ${isBridge ? 'selected' : ''}>LAN桥</option></select></label>
    <label data-required data-lan-interface-field><span>网卡</span><select data-lan-interface>${routePolicyOptions(interfaceSelectOptions('LAN'), row && !isBridge ? rowData[0] : interfaceSelectOptions('LAN')[0])}</select></label>
    <label data-required><span>IP</span><input data-lan-address value="${row ? safeText(String(rowData[2] || '').split('/')[0]) : ''}"></label>
    <label data-required><span>线路掩码</span><input data-lan-prefix value="${row && String(rowData[2] || '').includes('/') ? String(rowData[2]).split('/')[1] : '24'}"></label>
    <label><span>MTU</span><input data-lan-mtu value="${row ? safeText(rowData[6] || '') : '1500'}"></label>
    <label><span>IPv6</span><label class="inline-check"><input data-lan-ipv6-enabled type="checkbox" ${resource.ipv6?.mode === 'delegated_prefix' ? 'checked' : ''}>启用 IPv6 前缀派发</label></label>
    <label class="is-hidden" data-lan-ipv6-fields><span>前缀来源 WAN</span><select data-lan-prefix-wan>${proxyEgressOptions(resource.ipv6?.source_wan_id || '')}</select></label>
    <label><span>备注</span><input data-lan-remark value="${row ? safeText(rowData[12] || '') : ''}"></label>
    <section class="bridge-member-section ${isBridge ? '' : 'is-hidden'}" data-bridge-members><h3>LAN桥成员</h3><div class="bridge-member-grid">${bridgeMembers}</div><p class="empty-hint">只显示已标记为 LAN接口、且未加入聚合组的接口。</p></section>
  </div>`;
}

function bridgeMemberOptions(selectedText = '') {
  const selected = new Set(String(selectedText || '').split('\n').map((item) => item.trim()).filter(Boolean));
  const rows = lanBridgeMemberRows();
  return rows.length ? rows.map((row) => `<label class="bridge-member-card"><input data-bridge-member type="checkbox" value="${escapeAttr(row[0])}" ${selected.has(row[0]) ? 'checked' : ''}><span><strong>${safeText(row[0])}</strong><small>${safeText(row[2] || '')} · ${safeText(row[1] || '')}</small></span></label>`).join('') : '<p class="empty-hint">没有可加入 LAN桥的非管理网卡。</p>';
}

function nextLanBridgeName() {
  const used = new Set(networkRowsForPage('network/proxy_main').map((row) => row[0]));
  for (let index = 1; index < 100; index += 1) {
    const name = `lan桥${index}`;
    if (!used.has(name)) return name;
  }
  return `lan桥${Date.now()}`;
}

function lanBridgeID(name) {
  const number = String(name || '').match(/\d+/)?.[0] || String(nextLanBridgeName()).match(/\d+/)?.[0] || Date.now();
  return `lan-bridge-${number}`;
}
function interfaceFormHtml(row = 0) {
  const rows = networkRowsForPage('monitor/interface_list');
  const rowData = rows[row % rows.length] || rows[0] || [];
  const isManagement = rowData[3] === '管理口';
  const summary = `<div class="interface-form-summary"><span>当前接口</span><strong>${safeText(rowData[0] || '')}</strong><em>${safeText(rowData[2] || '未识别工作模式')}</em></div>`;
  if (isManagement) return `<div class="interface-edit-form interface-role-form compact-role-form"><section><h3>角色配置</h3>${summary}<p class="empty-hint">管理口保留在管理面，不能配置为 WAN 或 LAN。</p></section></div>`;
  return `<div class="interface-edit-form interface-role-form compact-role-form"><section><h3>角色配置</h3>${summary}<label class="role-direction-field"><span>方向</span><select><option value="wan" ${rowData[3] === 'WAN接口' ? 'selected' : ''}>WAN接口</option><option value="lan" ${rowData[3] === 'LAN接口' ? 'selected' : ''}>LAN接口</option></select></label><p class="empty-hint">这里只标记接口用途。随后到 LAN/WAN 页面新增接口时选择它，再填写 IP、网关、DNS、MTU 等参数。</p></section></div>`;
}
function interfaceBondFormHtml() {
  const rows = bondMemberRows();
  const members = rows.length ? rows.map((row) => `<label class="lag-member-card"><input data-bond-member type="checkbox" value="${escapeAttr(row[0])}"><span><strong>${safeText(row[0])}</strong><small>${safeText(row[2] || '')} · ${safeText(row[1] || '')}</small></span></label>`).join('') : '<p class="empty-hint">没有可加入聚合组的非管理网卡。</p>';
  return `<div class="interface-edit-form lag-create-form"><section><h3>基本设置</h3><label data-required><span>聚合组名称</span><input data-bond-name required maxlength="63" placeholder="例如：核心上联"></label></section><section><h3>成员网卡</h3><fieldset class="form-fieldset lag-member-grid">${members}</fieldset><p>请选择至少两张同速率、同工作模式的网卡。</p></section></div>`;
}
function openModal(title, html, variant = '') {
  modalController.open({
    title,
    html,
    variant,
    onReady: () => { wireWanTypeForm(); wireLanKindForm(); wireWanGroupForm(); wireAddressConditionEditors(); wireRoutePolicyForm(); wireFlowControlForm(); }
  });
}
function wireAddressConditionEditors() {
  el.modalBody.querySelectorAll('[data-address-summary]').forEach((summary) => {
    summary.addEventListener('click', () => openAddressConditionDialog(summary));
    summary.addEventListener('keydown', (event) => { if (event.key === 'Enter' || event.key === ' ') { event.preventDefault(); openAddressConditionDialog(summary); } });
  });
}

function openAddressConditionDialog(summary) {
  document.querySelector('[data-condition-dialog-backdrop]')?.remove();
  const values = JSON.parse(summary.dataset.addressValues || '[]');
  const groupIDs = new Set(envelopeItems(state.controlPlane.resources.objectGroups).filter((item) => objectGroupType(item) === 'ip').map((item) => item.id || item.name));
  const rowsHTML = values.map((value) => addressConditionRowHtml(summary.dataset.addressSummary, groupIDs.has(value) ? 'ip_group' : String(value).includes('-') ? 'range' : String(value).includes(':') ? 'ipv6' : 'literal', value)).join('');
  const layer = document.createElement('div');
  layer.className = 'condition-dialog-backdrop';
  layer.dataset.conditionDialogBackdrop = '';
  layer.innerHTML = `<section class="condition-dialog" role="dialog" aria-modal="true" aria-label="编辑${safeText(summary.dataset.addressLabel)}"><header><h2>编辑${safeText(summary.dataset.addressLabel)}</h2><button type="button" data-condition-close aria-label="关闭">×</button></header><div class="condition-dialog-tools"><strong>类型</strong><strong>IP / 群组</strong><span><button type="button" data-condition-add>＋ 添加</button><button type="button" data-condition-clear>清空</button></span></div><div class="condition-dialog-body" data-address-rows>${rowsHTML}</div><footer><button type="button" class="primary" data-condition-confirm>确定</button><button type="button" data-condition-cancel>取消</button></footer></section>`;
  document.body.appendChild(layer);
  const rows = layer.querySelector('[data-address-rows]');
  const wireRow = (row) => {
    const type = row.querySelector('[data-address-type]');
    const input = row.querySelector('[data-address-value]');
    const group = row.querySelector('[data-address-group]');
    const sync = () => { const isGroup = type.value === 'ip_group'; input.hidden = isGroup; input.disabled = isGroup; group.hidden = !isGroup; group.disabled = !isGroup; input.placeholder = type.value === 'range' ? '192.168.1.10-192.168.1.20' : type.value === 'ipv6' ? '2001:db8::/64' : '192.168.1.0/24'; };
    type.addEventListener('change', sync);
    row.querySelector('[data-address-delete]').addEventListener('click', () => row.remove());
    sync();
  };
  Array.from(rows.children).forEach(wireRow);
  const close = () => { layer.remove(); summary.focus(); };
  layer.querySelector('[data-condition-add]').addEventListener('click', () => { rows.insertAdjacentHTML('beforeend', addressConditionRowHtml(summary.dataset.addressSummary)); wireRow(rows.lastElementChild); rows.lastElementChild.querySelector('[data-address-value]').focus(); });
  layer.querySelector('[data-condition-clear]').addEventListener('click', () => { rows.innerHTML = ''; });
  layer.querySelector('[data-condition-close]').addEventListener('click', close);
  layer.querySelector('[data-condition-cancel]').addEventListener('click', close);
  layer.addEventListener('click', (event) => { if (event.target === layer) close(); });
  layer.querySelector('[data-condition-confirm]').addEventListener('click', () => {
    const items = Array.from(rows.querySelectorAll('[data-address-row]')).map((row) => row.querySelector('[data-address-type]').value === 'ip_group' ? row.querySelector('[data-address-group]').value.trim() : row.querySelector('[data-address-value]').value.trim());
    if (items.some((value) => !value)) { toast('请填写完整的地址条件'); return; }
    summary.dataset.addressValues = JSON.stringify(items);
    summary.value = items.join('\n');
    summary.placeholder = items.length ? '' : '任意';
    close();
  });
  layer.querySelector('[data-condition-add]')?.focus();
}
function wireRoutePolicyForm() {
  const form = el.modal.querySelector('[data-route-policy-form]');
  const action = form?.querySelector('[data-route-action]');
  const lineLabel = form?.querySelector('[data-route-line-label]');
  const fullCone = form?.querySelector('[data-full-cone]');
  if (!form || !action || !lineLabel) return;
  const sync = () => {
    const isRoute = action.value === '路由';
    lineLabel.textContent = isRoute ? '路由线路' : 'NAT线路';
    fullCone?.classList.toggle('is-hidden', isRoute);
    const fullConeInput = fullCone?.querySelector('input');
    if (isRoute && fullConeInput) fullConeInput.checked = false;
  };
  action.addEventListener('change', sync);
  sync();
}
function wireWanTypeForm() {
  const form = el.modal.querySelector('[data-wan-form]');
  const type = form?.querySelector('[data-wan-type]');
  if (!form || !type) return;
  const sync = () => {
    form.querySelectorAll('[data-wan-field]').forEach((field) => { field.classList.toggle('is-hidden', !field.dataset.wanField.split(' ').includes(type.value)); field.classList.remove('is-invalid'); });
    const mtu = form.querySelector('[data-wan-mtu]');
    if (mtu) mtu.value = type.value === 'pppoe' ? '1460' : '1500';
  };
  type.addEventListener('change', sync);
  sync();
}
function wireLanKindForm() {
  const form = el.modal.querySelector('[data-lan-form]');
  const kind = form?.querySelector('[data-lan-kind]');
  const ipv6 = form?.querySelector('[data-lan-ipv6-enabled]');
  if (!form || !kind) return;
  const sync = () => {
    const bridge = kind.value === 'lan_bridge';
    form.querySelector('[data-lan-interface-field]')?.classList.toggle('is-hidden', bridge);
    form.querySelector('[data-bridge-members]')?.classList.toggle('is-hidden', !bridge);
    form.querySelector('[data-lan-ipv6-fields]')?.classList.toggle('is-hidden', !ipv6?.checked);
  };
  kind.addEventListener('change', sync);
  ipv6?.addEventListener('change', sync);
  sync();
}
function wireWanGroupForm() {
  const form = el.modal.querySelector('[data-wan-group-form]');
  const mode = form?.querySelector('[data-wan-group-mode]');
  if (!form || !mode) return;
  const sync = () => {
    const failover = mode.value === 'primary_backup';
    const weighted = mode.value === 'weighted';
    form.querySelector('[data-wan-failover-fields]')?.classList.toggle('is-hidden', !failover);
    form.querySelector('[data-wan-group-hash-field]')?.classList.toggle('is-hidden', mode.value !== 'flow_hash');
    form.querySelector('[data-wan-weight-note]')?.classList.toggle('is-hidden', !weighted);
    form.querySelectorAll('[data-wan-member-weight]').forEach((input) => input.disabled = !weighted);
  };
  mode.addEventListener('change', sync);
  sync();
}
function wireFlowControlForm() {
  const form = el.modal.querySelector('[data-flow-control-form]');
  const action = form?.querySelector('[data-flow-action]');
  if (!form || !action) return;
  const sync = () => {
    const isBlocked = action.value === '阻断';
    form.querySelectorAll('[data-flow-speed-field]').forEach((field) => { field.classList.toggle('is-hidden', isBlocked); field.setAttribute('aria-hidden', String(isBlocked)); });
  };
  action.addEventListener('change', sync);
  sync();
}
function validateModalRequired() {
  const fields = Array.from(el.modal.querySelectorAll('[data-required]')).filter((field) => !field.classList.contains('is-hidden'));
  fields.forEach((field) => field.classList.remove('is-invalid'));
  const empty = fields.find((field) => {
    const control = field.querySelector('input, select');
    return control && !control.value.trim();
  });
  if (!empty) return true;
  empty.classList.add('is-invalid');
  toast(`请填写${empty.querySelector('span')?.textContent || '必填项'}`);
  empty.querySelector('input, select')?.focus();
  return false;
}
function closeModal(force = false) { modalController.close(force); }
function toast(message) { el.toast.textContent = message; el.toast.classList.remove('is-hidden'); clearTimeout(toast.timer); toast.timer = setTimeout(() => el.toast.classList.add('is-hidden'), 1600); }
function render() { renderMenu(); renderWorkspace(); }

const authApi = { session: '/api/v1/auth/session', login: '/api/v1/auth/login', logout: '/api/v1/auth/logout', changePassword: '/api/v1/auth/change-password' };
const controlApi = {
  health: '/api/v1/health', mode: '/api/v1/mode', capabilities: '/api/v1/capabilities',
  dashboard: '/api/v1/telemetry/dashboard', dashboardSummary: '/api/v1/dashboard/summary', trafficTrend: trafficTrendEndpoint('5m'), interfaces: '/api/v1/telemetry/interfaces', topSessions: '/api/v1/telemetry/top-sessions',
  topDomains: '/api/v1/telemetry/top-domains', onlineUsers: '/api/v1/telemetry/online-users', policyHits: '/api/v1/telemetry/policy-hits',
  audit: '/api/v1/telemetry/audit-events', configExport: '/api/v1/config/export', configImport: '/api/v1/config/import', configApply: '/api/v1/config/apply', factoryReset: '/api/v1/config/factory-reset', managementNetwork: '/api/v1/management/network',
    smartQoS: '/api/v1/flow-control/smart-qos', runtimeStatus: '/api/v1/runtime/status', runtimePreview: '/api/v1/runtime/preview', runtimeApply: '/api/v1/runtime/apply', firmwareStatus: '/api/v1/firmware/update/status', firmwareStage: '/api/v1/firmware/update/stage', firmwareInstall: '/api/v1/firmware/update/install', proxyStatus: '/api/v1/proxy/xray/status', proxyLogs: '/api/v1/proxy/xray/logs', pppoeStatus: '/api/v1/gateway/pppoe/status'
};
function trafficTrendEndpoint(windowName) {
  const points = ({ realtime: 120, '5m': 300, '1h': 240, '24h': 288 })[windowName] || 300;
  return `/api/v1/telemetry/traffic-trend?window=${encodeURIComponent(windowName)}&points=${points}`;
}
const resourceEndpoints = {
  interfaces: '/api/v1/interfaces', interfaceBonds: '/api/v1/interface-bonds', wanLinks: '/api/v1/gateway/wan-links', wanGroups: '/api/v1/gateway/wan-groups', proxyEgresses: '/api/v1/proxy/egresses', proxyNodes: '/api/v1/proxy/nodes', proxySubscriptions: '/api/v1/proxy/subscriptions', routePolicies: '/api/v1/gateway/policies/routes', portMaps: '/api/v1/gateway/nat/port-maps', dnsPolicies: '/api/v1/dns/policies', dhcpServers: '/api/v1/dhcp/servers', dhcpBindings: '/api/v1/dhcp/static-bindings', objectGroups: '/api/v1/objects/groups', trafficControl: '/api/v1/gateway/traffic-control', securityAcls: '/api/v1/security/acls', securityIpMac: '/api/v1/security/ip-mac-bindings', securityThreatIntel: '/api/v1/security/threat-intel', securityAttackRules: '/api/v1/security/attack-rules', authUsers: '/api/v1/auth/users'
};
const defaultLoginHint = '使用授权账号登录管理控制台。';

function setLoginHint(message = defaultLoginHint) {
  const hint = document.getElementById('loginHint');
  if (hint) hint.textContent = message;
}
function showLogin(message = defaultLoginHint) {
  stopAutoRefresh();
  el.appShell.classList.add('is-hidden');
  el.loginScreen.classList.remove('is-hidden');
  setLoginHint(message);
}
function showShell() {
  el.loginScreen.classList.add('is-hidden');
  el.appShell.classList.remove('is-hidden');
  setLoginHint();
  if (!window.location.hash) gatewayRouting.navigate(state.active, true);
  render();
  refreshControlPlane();
  startAutoRefresh();
}

function showPasswordChange(currentPassword = '') {
  stopAutoRefresh();
  el.appShell.classList.add('is-hidden');
  el.loginScreen.classList.remove('is-hidden');
  setLoginHint('首次登录必须修改管理员密码。');
  el.modalTitle.textContent = '修改管理员密码';
  el.modalBody.innerHTML = `
    <div class="form-grid password-change-form">
      <label><span>当前密码</span><input type="password" name="current_password" autocomplete="current-password" value="${escapeAttr(currentPassword)}" required></label>
      <label><span>新密码</span><input type="password" name="new_password" autocomplete="new-password" required minlength="8"></label>
      <label><span>确认新密码</span><input type="password" name="confirm_password" autocomplete="new-password" required minlength="8"></label>
    </div>
    <p class="modal-note password-change-note">新密码至少 8 位，不能包含用户名；修改后会自动重新登录。</p>`;
  el.modalBackdrop.classList.remove('is-hidden');
  el.modal.classList.remove('is-hidden');
  pendingModalSubmit = submitPasswordChange;
}

async function submitPasswordChange() {
  const currentPassword = el.modalBody.querySelector('[name="current_password"]')?.value || '';
  const newPassword = el.modalBody.querySelector('[name="new_password"]')?.value || '';
  const confirmPassword = el.modalBody.querySelector('[name="confirm_password"]')?.value || '';
  if (newPassword !== confirmPassword) { toast('两次输入的新密码不一致'); return; }
  if (newPassword.length < 8) { toast('新密码至少 8 位'); return; }
  try {
    const response = await authFetch(authApi.changePassword, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ current_password: currentPassword, new_password: newPassword })
    });
    if (!response.ok) { toast(response.status === 422 ? '新密码不符合要求' : '修改密码失败'); return; }
    closeModal(true);
    toast('密码已修改');
    showShell();
  } catch (error) {
    toast('无法连接认证服务');
  }
}

function authFetch(path, options = {}) {
  return fetch(path, { ...options, credentials: 'same-origin' });
}
async function apiJSON(path, options = {}) {
  const response = await authFetch(path, options);
  const text = await response.text();
  const payload = text ? JSON.parse(text) : {};
  if (!response.ok) throw new Error(payload.error?.message || `API ${path} returned ${response.status}`);
  return payload;
}
async function refreshControlPlane(options = {}) {
  const configApply = state.controlPlane.configApply;
  const runtimePreview = state.controlPlane.runtimePreview;
  const runtimeApply = state.controlPlane.runtimeApply;
  const previousResources = state.controlPlane.resources || {};
  const previousTelemetry = state.controlPlane.telemetry || {};
  const previousErrors = state.controlPlane.endpointErrors || {};
  if (options.resourcesOnly) {
    try {
      const resourceResult = await fetchResourceEndpoints(previousResources, previousErrors);
      state.controlPlane.resources = resourceResult.payloads;
      state.controlPlane.endpointErrors = { ...previousErrors, ...resourceResult.errors };
      state.controlPlane.loading = false;
      state.controlPlane.error = '';
    } catch (error) {
      state.controlPlane.error = error.message || '资源刷新失败';
    }
    renderWorkspace();
    return;
  }
  state.controlPlane.loading = true;
  state.controlPlane.error = '';
  if (!options.silent) render();
  try {
    const [health, mode, capabilities, telemetryResult, audit, configExport, managementNetwork, smartQoS, runtimeStatus, firmwareStatus, proxyStatus, proxyLogs, pppoeStatus, resourceResult] = await Promise.all([
      apiJSON(controlApi.health), apiJSON(controlApi.mode), apiJSON(controlApi.capabilities), fetchTelemetryEndpoints(previousTelemetry, previousErrors), safeApiJSON(controlApi.audit, state.controlPlane.audit), safeApiJSON(controlApi.configExport, state.controlPlane.configExport), safeApiJSON(controlApi.managementNetwork, state.controlPlane.managementNetwork), safeApiJSON(controlApi.smartQoS, state.controlPlane.smartQoS), fetchRuntimeStatus(), safeApiJSON(controlApi.firmwareStatus, state.controlPlane.firmwareStatus), safeApiJSON(controlApi.proxyStatus, state.controlPlane.proxyStatus), safeApiJSON(controlApi.proxyLogs, state.controlPlane.proxyLogs), safeApiJSON(controlApi.pppoeStatus, state.controlPlane.pppoeStatus), fetchResourceEndpoints(previousResources, previousErrors)
    ]);
    state.controlPlane = { loading: false, error: '', health, mode, capabilities, telemetry: telemetryResult.payloads, resources: resourceResult.payloads, audit, configExport, configApply, managementNetwork, smartQoS, runtimeStatus, runtimePreview, runtimeApply, firmwareStatus, proxyStatus, proxyLogs, pppoeStatus, endpointErrors: { ...telemetryResult.errors, ...resourceResult.errors }, runtimeBusy: '', firmwareBusy: '', trafficTrendLoading: false };
    if (options.settleInterfaces) await settleInterfaceReadback();
  } catch (error) {
    state.controlPlane.loading = false;
    state.controlPlane.error = error.message || '控制面 API 请求失败';
  }
  render();
}

const autoRefreshIntervalMs = 5000;
let autoRefreshInFlight = false;
let autoRefreshTimer = null;

function isWorkspaceEditing() {
  const active = document.activeElement;
  return active instanceof HTMLElement
    && el.workspace.contains(active)
    && (active.matches('input, select, textarea') || active.isContentEditable);
}

function canAutoRefresh() {
  return !document.hidden
    && !el.appShell.classList.contains('is-hidden')
    && el.modal.classList.contains('is-hidden')
    && !isWorkspaceEditing()
    && !state.controlPlane.loading
    && !state.controlPlane.runtimeBusy
    && !state.controlPlane.firmwareBusy
    && !autoRefreshInFlight;
}

async function autoRefresh() {
  if (!canAutoRefresh()) return;
  autoRefreshInFlight = true;
  try {
    await refreshControlPlane({ silent: true });
  } finally {
    autoRefreshInFlight = false;
  }
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

async function refreshTrafficTrend() {
  state.controlPlane.trafficTrendLoading = true;
  renderWorkspace();
  try {
    const trafficTrend = await apiJSON(trafficTrendEndpoint(state.trafficWindow));
    state.controlPlane.telemetry = { ...state.controlPlane.telemetry, trafficTrend };
    const { trafficTrend: ignored, ...remainingErrors } = state.controlPlane.endpointErrors;
    state.controlPlane.endpointErrors = remainingErrors;
  } catch (error) {
    state.controlPlane.endpointErrors = { ...state.controlPlane.endpointErrors, trafficTrend: error.message || '流量采集接口不可用' };
  } finally {
    state.controlPlane.trafficTrendLoading = false;
    renderWorkspace();
  }
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function settleInterfaceReadback() {
  await delay(1200);
  const [interfacesTelemetry, interfacesResource] = await Promise.all([
    safeApiJSON(controlApi.interfaces, state.controlPlane.telemetry.interfaces),
    safeApiJSON(resourceEndpoints.interfaces, state.controlPlane.resources.interfaces)
  ]);
  state.controlPlane.telemetry = { ...state.controlPlane.telemetry, interfaces: interfacesTelemetry };
  state.controlPlane.resources = { ...state.controlPlane.resources, interfaces: interfacesResource };
}
async function safeApiJSON(path, fallback = null) {
  try { return await apiJSON(path); }
  catch (error) { return fallback; }
}
async function fetchTelemetryEndpoints(previousTelemetry = {}, previousErrors = {}) {
  return fetchEndpointMap({ dashboard: controlApi.dashboard, dashboardSummary: controlApi.dashboardSummary, trafficTrend: controlApi.trafficTrend, interfaces: controlApi.interfaces, topSessions: controlApi.topSessions, topDomains: controlApi.topDomains, onlineUsers: controlApi.onlineUsers, policyHits: controlApi.policyHits }, previousTelemetry, previousErrors);
}
async function fetchResourceEndpoints(previousResources = {}, previousErrors = {}) {
  return fetchEndpointMap(resourceEndpoints, previousResources, previousErrors);
}
async function fetchEndpointMap(endpoints, previousPayloads = {}, previousErrors = {}) {
  const entries = await Promise.all(Object.entries(endpoints).map(async ([key, endpoint]) => {
    try {
      return [key, await apiJSON(endpoint), ''];
    } catch (error) {
      return [key, previousPayloads[key] || null, error.message || previousErrors[key] || '接口不可用'];
    }
  }));
  const payloads = Object.fromEntries(entries.filter(([, payload]) => payload).map(([key, payload]) => [key, payload]));
  const errors = Object.fromEntries(entries.filter(([, , message]) => message).map(([key, , message]) => [key, message]));
  return { payloads, errors };
}
async function fetchRuntimeStatus() {
  try {
    return await apiJSON(controlApi.runtimeStatus);
  } catch (error) {
    return { status: 'unavailable', components: [], error: error.message || '运行态状态接口不可用' };
  }
}
async function handleRuntimeAction(action) {
  const busy = action.replace('runtime-', '');
  state.controlPlane.runtimeBusy = busy;
  renderWorkspace();
  try {
    if (action === 'runtime-preview') {
      state.controlPlane.runtimePreview = await apiJSON(controlApi.runtimePreview);
      state.controlPlane.runtimeStatus = await fetchRuntimeStatus();
      const plan = state.controlPlane.runtimePreview.plan || {};
      toast(`运行态预览完成：服务产物 ${(plan.service_artifacts || []).length} 个，VPP 操作 ${(plan.vpp_operations || []).length} 个`);
      return;
    }
    if (action === 'runtime-apply') {
      const result = await apiJSON(controlApi.runtimeApply, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
      state.controlPlane.runtimeApply = result;
      state.controlPlane.runtimeStatus = await fetchRuntimeStatus();
      if (result.status === 'committed') toast(`运行态已提交：${result.transaction_id || '无事务号'}`);
      else toast(`运行态未完全可用：${result.reason || result.status || '后端返回降级状态'}`);
      return;
    }
  } catch (error) {
    toast(error.message || '运行态操作失败');
  } finally {
    state.controlPlane.runtimeBusy = '';
    renderWorkspace();
  }
}

async function handleFirmwareAction(action) {
	const busy = action.replace('firmware-', '');
	const image = el.workspace.querySelector('[data-firmware-image]')?.files?.[0];
	state.controlPlane.firmwareBusy = busy;
	renderWorkspace();
	try {
		if (action === 'firmware-stage') {
			if (!image) {
				toast('请选择升级包');
				return;
			}
			const form = new FormData();
			form.append('firmware', image);
			const response = await authFetch(controlApi.firmwareStage, { method: 'POST', body: form });
			const payload = await response.json().catch(() => ({}));
			if (!response.ok) throw new Error(payload?.error?.message || payload?.message || '固件上传失败');
			state.controlPlane.firmwareStatus = payload;
			toast('升级包已上传并校验通过');
			return;
		}
		if (action === 'firmware-install') {
			const response = await authFetch(controlApi.firmwareInstall, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ confirm_install: true, target_dir: '/usr/lib/ly-route', reboot: false }) });
			const payload = await response.json().catch(() => ({}));
			if (!response.ok) throw new Error(payload?.error?.message || payload?.message || '固件升级启动失败');
			state.controlPlane.firmwareStatus = payload;
			toast('应用升级已启动，控制服务将重启');
			return;
		}
		const response = await authFetch(controlApi.firmwareStatus);
		const payload = await response.json().catch(() => ({}));
		if (!response.ok) throw new Error(payload?.error?.message || payload?.message || '固件状态刷新失败');
		state.controlPlane.firmwareStatus = payload;
		toast('固件状态已刷新');
	} catch (error) {
		toast(error.message || '固件操作失败');
	} finally {
		state.controlPlane.firmwareBusy = '';
		renderWorkspace();
	}
}

async function handleConfigAction(action) {
  try {
    if (action.startsWith('runtime-')) {
      await handleRuntimeAction(action);
      return;
    }
    if (action.startsWith('firmware-')) {
      await handleFirmwareAction(action);
      return;
    }
    if (action === 'apply') {
      const result = await apiJSON(controlApi.configApply, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
      state.controlPlane.configApply = result;
      renderWorkspace();
      if (result.status === 'committed') toast(`应用配置已提交：${result.transaction_id || '无事务号'}`);
      else if (result.status === 'apply_failed') toast(`应用配置失败：${result.reason || '运行态降级'}`);
      else toast(`应用配置返回：${result.status || '未知状态'}`);
      return;
    }
    if (action === 'management-save') {
      const payload = {
		mode: el.workspace.querySelector('[data-management-mode]:checked')?.value || 'exclusive',
        interface_id: el.workspace.querySelector('[data-management-interface]')?.value.trim() || '',
        cidr: el.workspace.querySelector('[data-management-cidr]')?.value.trim() || '',
        gateway: el.workspace.querySelector('[data-management-gateway]')?.value.trim() || '',
        confirm_change: true
      };
      const result = await apiJSON(controlApi.managementNetwork, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
      state.controlPlane.managementNetwork = result.item || result;
      toast('管理口配置已保存');
      await refreshControlPlane();
      return;
    }
    if (action === 'export') {
      state.controlPlane.configExport = await apiJSON(controlApi.configExport);
      toast('配置导出包已从后端读取');
      renderWorkspace();
      return;
    }
    if (action === 'import') {
      const payload = state.controlPlane.configExport?.payload || { device_mode: 'gateway' };
      const result = await apiJSON(controlApi.configImport, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ dry_run: true, payload }) });
      toast(`导入预检完成：${result.status || 'dry_run'}`);
      return;
    }
    if (action === 'init') {
      const result = await apiJSON(controlApi.factoryReset, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
      toast(`初始化预检：${result.status || 'validated_not_applied'}`);
      await refreshControlPlane();
      return;
    }
    toast('配置操作已提交');
  } catch (error) {
    toast(error.message || '配置操作失败');
  }
}
async function loadSession() {
  try {
    const response = await authFetch(authApi.session, { method: 'GET' });
    if (response.ok) {
      const payload = await response.json();
      if (payload.password_change_required || payload.session?.password_change_required) { showPasswordChange(); return; }
      showShell();
      return;
    }
    showLogin(response.status === 401 ? defaultLoginHint : '会话检查失败，请稍后重试。');
  } catch (error) {
    showLogin('无法连接认证服务，请稍后重试。');
  }
}
async function submitLogin(event) {
  event.preventDefault();
  const username = document.getElementById('username')?.value || '';
  const password = document.getElementById('password')?.value || '';
  setLoginHint('正在验证账号...');
  try {
    const response = await authFetch(authApi.login, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password })
    });
    if (response.ok) {
      const payload = await response.json();
      if (payload.password_change_required || payload.session?.password_change_required) { showPasswordChange(password); return; }
      showShell();
      return;
    }
    showLogin(response.status === 401 ? '用户名或密码不正确。' : '登录失败，请稍后重试。');
  } catch (error) {
    showLogin('无法连接认证服务，请稍后重试。');
  }
}
async function submitLogout() {
  try {
    const response = await authFetch(authApi.logout, { method: 'POST' });
    if (response.ok || response.status === 401) { showLogin(); return; }
    toast('退出失败，请稍后重试');
  } catch (error) {
    toast('退出失败，请稍后重试');
  }
}

el.loginForm.addEventListener('submit', submitLogin);
el.menuSearch.addEventListener('input', (event) => { state.query = event.target.value; renderMenu(); });
el.mobileMenuToggle.addEventListener('click', (event) => {
  event.stopPropagation();
  setMobileMenuOpen(!el.appShell.classList.contains('mobile-menu-open'));
});
document.addEventListener('click', (event) => {
  if (!el.appShell.classList.contains('mobile-menu-open')) return;
  if (!el.sidebar.contains(event.target) && !el.mobileMenuToggle.contains(event.target)) setMobileMenuOpen(false);
});
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && el.appShell.classList.contains('mobile-menu-open')) setMobileMenuOpen(false);
});
el.logoutButton.addEventListener('click', submitLogout);
el.modalOk.addEventListener('click', async () => {
  if (modalSubmitting) return;
  if (!validateModalRequired()) return;
  if (pendingModalSubmit) {
    modalSubmitting = true;
    el.modalOk.disabled = true;
    el.modalCancel.disabled = true;
    try {
      await pendingModalSubmit();
    } catch (error) {
      toast(error?.message || '保存失败，请检查填写内容');
    } finally {
      modalSubmitting = false;
      el.modalOk.disabled = false;
      el.modalCancel.disabled = false;
    }
    return;
  }
  closeModal();
  toast('已保存');
});
gatewayRouting.listen(pageMap, 'system/system_overview', (route) => {
  if (route !== state.active) openPage(route, false);
});
loadSession();
