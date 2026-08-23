(function initializeGatewayOverview() {
  const palette = ["#1677c8", "#19a078", "#e19a2b", "#795fd2", "#d75b65", "#168ca5", "#607d9b", "#bb5f91"];
  const windows = [["realtime", "实时"], ["5m", "5分钟"], ["1h", "1小时"], ["24h", "24小时"]];

  function formatRate(value) {
    const rate = Number(value) || 0;
    if (rate >= 1_000_000_000) return `${(rate / 1_000_000_000).toFixed(2)} Gbps`;
    if (rate >= 1_000_000) return `${(rate / 1_000_000).toFixed(2)} Mbps`;
    if (rate >= 1_000) return `${(rate / 1_000).toFixed(2)} Kbps`;
    if (rate > 0 && rate < 10) return `${rate.toFixed(rate < 1 ? 2 : 1)} bps`;
    return `${Math.round(rate)} bps`;
  }

  function formatBytes(value) {
    const bytes = Number(value) || 0;
    if (bytes >= 1_099_511_627_776) return `${(bytes / 1_099_511_627_776).toFixed(2)} TiB`;
    if (bytes >= 1_073_741_824) return `${(bytes / 1_073_741_824).toFixed(2)} GiB`;
    if (bytes >= 1_048_576) return `${(bytes / 1_048_576).toFixed(2)} MiB`;
    if (bytes >= 1_024) return `${(bytes / 1_024).toFixed(2)} KiB`;
    return `${Math.round(bytes)} B`;
  }

  function formatTime(value) {
    if (!value) return "--";
    const date = new Date(value);
    return Number.isNaN(date.valueOf()) ? String(value) : date.toLocaleString("zh-CN", { hour12: false });
  }

  function formatAxisTime(value) {
    const date = new Date(value);
    return Number.isNaN(date.valueOf()) ? String(value || "") : date.toLocaleTimeString("zh-CN", { hour12: false, hour: "2-digit", minute: "2-digit", second: "2-digit" });
  }

  function latest(series) {
    return series.samples?.[series.samples.length - 1] || null;
  }

  function rateAt(series, timestamp, direction) {
    const sample = series.samples?.find((item) => item.timestamp === timestamp);
    if (!sample) return null;
    if (direction === "total") return Number(sample.download_bps || 0) + Number(sample.upload_bps || 0);
    return Number(sample[`${direction}_bps`] || 0);
  }

  function kindLabel(kind) {
    return ({ direct_wan: "WAN", wan_group: "WAN 群组", proxy: "代理 WAN" })[kind] || "WAN";
  }

  function egressName(item, index, names) {
    const source = String(item?.name || item?.id || "").trim();
    const resourceName = names?.get(String(item?.id || ""));
    if (resourceName) return resourceName;
    return source || `${kindLabel(item?.kind)} ${index + 1}`;
  }

  function egressNames(resources) {
    const payloadItems = (payload) => Array.isArray(payload) ? payload : Array.isArray(payload?.items) ? payload.items : Array.isArray(payload?.data) ? payload.data : [];
    const entries = [resources?.wanLinks, resources?.wanGroups, resources?.proxyEgresses].flatMap(payloadItems).map((item) => [String(item?.id || ""), String(item?.name || item?.display_name || item?.id || "")]).filter(([id, name]) => id && name);
    return new Map(entries);
  }

  function configuredSeries(series, resources) {
    const items = (payload) => Array.isArray(payload) ? payload : Array.isArray(payload?.items) ? payload.items : Array.isArray(payload?.data) ? payload.data : [];
    const groups = items(resources?.wanGroups).filter((item) => item.deleted !== true);
    const groupedWANs = new Set(groups.flatMap((item) => item.members || item.wan_members || []).map((member) => String(typeof member === "object" ? member.id || member.name || "" : member)));
    const valid = new Set(groups.map((item) => String(item.id || item.name || "")).filter(Boolean));
    items(resources?.wanLinks).filter((item) => item.deleted !== true && !groupedWANs.has(String(item.id || item.name || ""))).forEach((item) => valid.add(String(item.id || item.name || "")));
    items(resources?.proxyEgresses).filter((item) => item.deleted !== true && String(item.id || "") !== "proxy-egress-default").forEach((item) => valid.add(String(item.id || item.name || "")));
    return series.filter((item) => valid.has(String(item.id || "")));
  }

  function visibleSeries(options) {
    return (options.trend?.series?.logical_egresses || []).filter((series) => !options.hidden.has(series.id));
  }

  function measuredSeries(series) {
    return series.filter((item) => Array.isArray(item.samples) && item.samples.length > 0);
  }

  function currentTotals(series) {
    return series.reduce((totals, item) => {
      const sample = latest(item);
      totals.download += Number(sample?.download_bps || 0);
      totals.upload += Number(sample?.upload_bps || 0);
      return totals;
    }, { download: 0, upload: 0 });
  }

  function niceMaximum(value) {
    const maximum = Number(value) || 0;
    if (maximum <= 0) return 1;
    const magnitude = 10 ** Math.floor(Math.log10(maximum));
    const normalized = maximum / magnitude;
    const step = normalized <= 1 ? 1 : normalized <= 2 ? 2 : normalized <= 5 ? 5 : 10;
    return step * magnitude;
  }

  function lineModel(series, direction) {
    const timestamps = [...new Set(series.flatMap((item) => (item.samples || []).map((sample) => sample.timestamp)))].sort();
    const values = series.map((item) => timestamps.map((timestamp) => rateAt(item, timestamp, direction)));
    const maximum = niceMaximum(Math.max(0, ...values.flatMap((row) => row.filter((value) => value !== null))));
    return { timestamps, values, maximum };
  }

  function pointSegments(points) {
    const segments = [];
    let current = [];
    points.forEach((point) => {
      if (point) current.push(point);
      else if (current.length) {
        segments.push(current);
        current = [];
      }
    });
    if (current.length) segments.push(current);
    return segments;
  }

  function renderRateChart(series, direction, colors, escape, options = {}) {
    const model = lineModel(series, direction);
    const plot = { left: 70, right: 950, top: 16, bottom: 184 };
    const width = plot.right - plot.left;
    const height = plot.bottom - plot.top;
    const xAt = (index) => model.timestamps.length <= 1 ? plot.right : plot.left + index * width / (model.timestamps.length - 1);
    const yAt = (value) => plot.bottom - (Number(value) || 0) * height / model.maximum;
    const grid = [0, .25, .5, .75, 1].map((ratio) => {
      const y = plot.bottom - ratio * height;
      return `<line x1="${plot.left}" y1="${y}" x2="${plot.right}" y2="${y}" class="router-chart-grid"></line><text x="${plot.left - 12}" y="${y + 4}" class="router-chart-y-label">${formatRate(model.maximum * ratio)}</text>`;
    }).join("");
    const paths = series.map((item, seriesIndex) => {
      const color = colors.get(item.id) || palette[seriesIndex % palette.length];
      const points = model.values[seriesIndex].map((value, index) => value === null ? null : ({ x: xAt(index), y: yAt(value) }));
      return pointSegments(points).map((segment) => {
        const line = segment.map((point, index) => `${index ? "L" : "M"}${point.x.toFixed(2)},${point.y.toFixed(2)}`).join(" ");
        const area = segment.length > 1 ? `${line} L${segment.at(-1).x.toFixed(2)},${plot.bottom} L${segment[0].x.toFixed(2)},${plot.bottom} Z` : "";
        const dot = segment.length === 1 ? `<circle cx="${segment[0].x}" cy="${segment[0].y}" r="3.5" fill="${color}"></circle>` : "";
        return `${area ? `<path d="${area}" fill="${color}" class="router-chart-area"></path>` : ""}<path d="${line}" fill="none" stroke="${color}" class="router-chart-line"></path>${dot}`;
      }).join("");
    }).join("");
    const xLabels = model.timestamps.length ? [0, Math.floor((model.timestamps.length - 1) / 2), model.timestamps.length - 1]
      .filter((index, position, values) => values.indexOf(index) === position)
      .map((index) => `<text x="${xAt(index)}" y="216" class="router-chart-x-label" text-anchor="${model.timestamps.length === 1 ? "end" : index === 0 ? "start" : index === model.timestamps.length - 1 ? "end" : "middle"}">${escape(formatAxisTime(model.timestamps[index]))}</text>`).join("") : "";
    const hitTargets = model.timestamps.map((timestamp, index) => {
      const span = model.timestamps.length <= 1 ? width : width / (model.timestamps.length - 1);
      const x = Math.max(plot.left, xAt(index) - span / 2);
      return `<rect x="${x}" y="${plot.top}" width="${Math.min(span, plot.right - x)}" height="${height}" class="router-chart-hit" tabindex="0" data-chart-point="${index}" data-chart-direction="${direction}" aria-label="${escape(formatTime(timestamp))}"></rect>`;
    }).join("");
    const hasSamples = model.values.some((row) => row.some((value) => value !== null));
    options.models[direction] = model;
    return `<div class="router-rate-chart ${options.className || ""}" data-rate-chart="${direction}"><svg viewBox="0 0 1000 226" preserveAspectRatio="none" role="img" aria-label="${escape(options.label || "流量趋势")}">${grid}${paths}${xLabels}${hitTargets}</svg>${hasSamples ? "" : '<div class="router-chart-empty">暂无流量记录</div>'}<div class="traffic-chart-tooltip" role="tooltip" hidden></div></div>`;
  }

  function tooltipHtml(options, direction, timestamp) {
    const series = visibleSeries(options);
    const total = series.reduce((sum, item) => sum + (rateAt(item, timestamp, direction) || 0), 0);
    const title = direction === "upload" ? "上行" : direction === "download" ? "下行" : "上下行总和";
    return `<strong>${options.escape(formatTime(timestamp))}</strong><span class="traffic-tooltip-total">${title} ${formatRate(total)}</span>${series.map((item, index) => `<span><i style="background:${options.colors.get(item.id)}"></i><b>${options.escape(egressName(item, index, options.egressNames))}</b><em>${formatRate(rateAt(item, timestamp, direction))}</em></span>`).join("")}`;
  }

  function renderLegend(series, colors, escape, hidden, interactive = false, names = new Map()) {
    if (!series.length) return '<span class="router-legend-empty">暂无 WAN 出口</span>';
    return series.map((item, index) => {
      const content = `<i style="background:${colors.get(item.id)}"></i><span>${escape(egressName(item, index, names))}</span><em>${escape(kindLabel(item.kind))}</em>`;
      if (!interactive) return `<span class="router-chart-legend-item">${content}</span>`;
      return `<button type="button" class="router-chart-legend-item" data-egress-legend="${escape(item.id)}" aria-pressed="${!hidden.has(item.id)}">${content}</button>`;
    }).join("");
  }

  function usage(label, value) {
    const percent = Number.isFinite(Number(value)) ? Math.max(0, Math.min(100, Math.round(Number(value)))) : null;
    const level = percent === null ? "unknown" : percent >= 85 ? "high" : percent >= 65 ? "medium" : "normal";
    const state = percent === null ? "正在读取" : level === "high" ? "负载较高" : level === "medium" ? "负载适中" : "运行平稳";
    return `<article class="system-metric-card is-${level}"><header><strong>${label}</strong><b>${percent === null ? "--" : `${percent}%`}</b></header><div class="system-meter" role="progressbar" aria-label="${label}" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${percent || 0}"><i style="width:${percent || 0}%"></i></div><footer><span>${state}</span></footer></article>`;
  }

  function uptimeLabel(value) {
    const seconds = Number(value);
    if (!Number.isFinite(seconds) || seconds < 0) return "--";
    const days = Math.floor(seconds / 86400);
    const hours = Math.floor((seconds % 86400) / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    if (days) return `${days}天 ${hours}小时`;
    if (hours) return `${hours}小时 ${minutes}分钟`;
    return `${minutes}分钟`;
  }

  function payloadItems(payload) {
    if (Array.isArray(payload)) return payload;
    if (Array.isArray(payload?.items)) return payload.items;
    if (Array.isArray(payload?.data)) return payload.data;
    if (Array.isArray(payload?.data?.items)) return payload.data.items;
    return [];
  }

  function onlineIPv4Count(payload) {
    return payloadItems(payload).filter((item) => /^\d{1,3}(?:\.\d{1,3}){3}$/.test(String(item.ip || item.ip_address || item.address || item.user || "").trim())).length;
  }

  function summarySystem(payload) {
    const root = payload?.data && !Array.isArray(payload.data) ? payload.data : payload || {};
    return root.system || root.data?.system || root.data || root;
  }

  function renderWanTrend(options) {
    const allSeries = configuredSeries(options.trafficTrend?.series?.logical_egresses || [], options.resources);
    const series = allSeries;
    const colors = new Map(allSeries.map((item, index) => [item.id, palette[index % palette.length]]));
    const names = egressNames(options.resources);
    const models = {};
    const totals = currentTotals(series);
    const current = totals.upload + totals.download;
    const samples = series.flatMap((item) => item.samples || []);
    const peak = samples.length ? Math.max(...samples.map((sample) => Number(sample.download_bps || 0) + Number(sample.upload_bps || 0))) : 0;
    const chartOptions = { models, className: "system-wan-chart", label: "WAN 上下行总流量趋势" };
    return `<section class="system-panel system-traffic-panel"><header class="system-panel-head"><div><h3>WAN 流量趋势</h3><p>出口上下行总流量</p></div><div class="system-traffic-summary"><span><small>当前</small><b>${formatRate(current)}</b></span><span><small>峰值</small><b>${formatRate(peak)}</b></span></div></header><div class="router-chart-legend">${renderLegend(series, colors, options.escape, new Set(), false, names)}</div>${renderRateChart(series, "total", colors, options.escape, chartOptions)}</section>`;
  }

  function renderSystem(options) {
    const system = { ...summarySystem(options.summary), ...(options.runtime?.system || options.runtime?.data?.system || {}) };
    const load = system.load_average || {};
    const loadText = [load["1m"], load["5m"], load["15m"]].every((item) => Number.isFinite(Number(item))) ? `${Number(load["1m"]).toFixed(2)} / ${Number(load["5m"]).toFixed(2)} / ${Number(load["15m"]).toFixed(2)}` : "--";
    const facts = [
      ["运行时长", uptimeLabel(system.uptime_seconds ?? system.uptime ?? system.runtime_seconds)],
      ["当前 IP 数", String(onlineIPv4Count(options.onlineUsers))],
      ["系统时间", formatTime(system.system_time || new Date().toISOString())],
      ["硬件平台", system.platform || system.hardware_platform || system.device_model || "--"]
    ];
    return `<div class="system-overview"><div class="system-overview-layout"><section class="system-panel system-resource-panel"><header class="system-panel-head"><div><h3>资源使用</h3><p>负载均值 ${options.escape(loadText)}</p></div></header><div class="system-resource-grid">${usage("CPU 使用率", system.cpu_busy_percent)}${usage("内存使用率", system.memory_used_percent)}</div></section><section class="system-panel system-info-panel"><header class="system-panel-head"><div><h3>设备信息</h3><p>本机运行信息</p></div></header><div class="system-fact-grid">${facts.map(([label, value]) => `<div class="system-fact"><span>${label}</span><strong>${options.escape(value)}</strong></div>`).join("")}</div></section></div>${renderWanTrend(options)}</div>`;
  }

  function renderTraffic(options) {
    const allSeries = configuredSeries(options.trend?.series?.logical_egresses || [], options.resources);
    const series = allSeries.filter((item) => !options.hidden.has(item.id));
    const totals = currentTotals(series);
    const colors = new Map(allSeries.map((item, index) => [item.id, palette[index % palette.length]]));
    const names = egressNames(options.resources);
    const models = {};
    const dashboard = options.dashboard?.data || options.dashboard || {};
    options.colors = colors;
    options.models = models;
    options.egressNames = names;
    return `<div class="traffic-pa-page"><section class="traffic-pa-kpis"><article><span>当前上行</span><strong>${formatRate(totals.upload)}</strong><em>发送速率</em></article><article><span>当前下行</span><strong>${formatRate(totals.download)}</strong><em>接收速率</em></article><article><span>活动连接</span><strong>${Number(dashboard.sessions || dashboard.connections || 0)}</strong><em>最近 5 秒</em></article><article><span>在线用户</span><strong>${onlineIPv4Count(options.onlineUsers)}</strong><em>IPv4 用户</em></article><article><span>WAN 出口</span><strong>${series.length}</strong><em>逻辑出口</em></article></section><section class="traffic-pa-toolbar"><div class="traffic-pa-legend">${renderLegend(series, colors, options.escape, options.hidden, true, names)}</div><div class="traffic-windows" aria-label="时间范围">${windows.map(([value, label]) => `<button type="button" data-traffic-window="${value}" aria-pressed="${options.window === value}">${label}</button>`).join("")}</div></section><section class="traffic-chart-grid"><section class="traffic-pa-chart-card"><header><div><h3>上行趋势</h3><span>出口发送流量</span></div><strong>${formatRate(totals.upload)}</strong></header>${renderRateChart(series, "upload", colors, options.escape, { models, label: "上行流量趋势" })}</section><section class="traffic-pa-chart-card"><header><div><h3>下行趋势</h3><span>出口接收流量</span></div><strong>${formatRate(totals.download)}</strong></header>${renderRateChart(series, "download", colors, options.escape, { models, label: "下行流量趋势" })}</section></section></div>`;
  }

  function wireTraffic(root, options, actions) {
    root.querySelectorAll("[data-traffic-window]").forEach((button) => button.addEventListener("click", () => actions.window(button.dataset.trafficWindow)));
    root.querySelectorAll("[data-egress-legend]").forEach((button) => button.addEventListener("click", () => actions.toggle(button.dataset.egressLegend)));
    root.querySelectorAll("[data-chart-point]").forEach((target) => {
      const show = (event) => {
        const chart = target.closest("[data-rate-chart]");
        const tooltip = chart?.querySelector(".traffic-chart-tooltip");
        const direction = target.dataset.chartDirection;
        const timestamp = options.models?.[direction]?.timestamps?.[Number(target.dataset.chartPoint)];
        if (!tooltip || !timestamp) return;
        tooltip.innerHTML = tooltipHtml(options, direction, timestamp);
        tooltip.hidden = false;
        if (event instanceof MouseEvent) {
          const bounds = chart.getBoundingClientRect();
          tooltip.style.left = `${Math.max(12, Math.min(bounds.width - 236, event.clientX - bounds.left + 12))}px`;
          tooltip.style.top = `${Math.max(10, event.clientY - bounds.top - 88)}px`;
        }
      };
      target.addEventListener("mouseenter", show);
      target.addEventListener("mousemove", show);
      target.addEventListener("focus", show);
      target.addEventListener("mouseleave", () => { const tooltip = target.closest("[data-rate-chart]")?.querySelector(".traffic-chart-tooltip"); if (tooltip) tooltip.hidden = true; });
      target.addEventListener("blur", () => { const tooltip = target.closest("[data-rate-chart]")?.querySelector(".traffic-chart-tooltip"); if (tooltip) tooltip.hidden = true; });
    });
  }

  window.LyRouteGatewayOverview = Object.freeze({ renderSystem, renderTraffic, wireTraffic, formatRate, formatBytes });
}());
