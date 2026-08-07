(function initializeGatewayOverview() {
  const palette = ["#1683ff", "#7468f2", "#17b26a", "#d58b18", "#e5484d", "#2ba7a0", "#a65bd6", "#65758b"];
  const windows = [["realtime", "实时"], ["5m", "5分钟"], ["1h", "1小时"], ["24h", "24小时"]];

  function formatRate(value) {
    const rate = Number(value) || 0;
    if (rate >= 1_000_000_000) return `${(rate / 1_000_000_000).toFixed(2)} Gbps`;
    if (rate >= 1_000_000) return `${(rate / 1_000_000).toFixed(2)} Mbps`;
    if (rate >= 1_000) return `${(rate / 1_000).toFixed(2)} Kbps`;
    return `${Math.round(rate)} bps`;
  }

  function formatBytes(value) {
    const bytes = Number(value) || 0;
    if (bytes >= 1_073_741_824) return `${(bytes / 1_073_741_824).toFixed(2)} GiB`;
    if (bytes >= 1_048_576) return `${(bytes / 1_048_576).toFixed(2)} MiB`;
    if (bytes >= 1_024) return `${(bytes / 1_024).toFixed(2)} KiB`;
    return `${Math.round(bytes)} B`;
  }

  function formatTime(value) {
    if (!value) return "无成功采样";
    const date = new Date(value);
    return Number.isNaN(date.valueOf()) ? String(value) : date.toLocaleString("zh-CN", { hour12: false });
  }

  function latest(series) {
    return series.samples?.[series.samples.length - 1] || null;
  }

  function visibleSeries(options) {
    return (options.trend?.series?.logical_egresses || []).filter((series) => !options.hidden.has(series.id));
  }

  function currentTotals(series) {
    return series.reduce((totals, item) => {
      const sample = latest(item);
      totals.download += Number(sample?.download_bps || 0);
      totals.upload += Number(sample?.upload_bps || 0);
      return totals;
    }, { download: 0, upload: 0 });
  }

  function rateAt(series, timestamp, field) {
    const sample = series.samples?.find((item) => item.timestamp === timestamp);
    return sample ? Number(sample[field] || 0) : null;
  }

  function segments(points) {
    const output = [];
    let active = [];
    for (const point of points) {
      if (point) active.push(point);
      else if (active.length) {
        output.push(active);
        active = [];
      }
    }
    if (active.length) output.push(active);
    return output;
  }

  function areaPath(points) {
    return `${points.map((point, index) => `${index ? "L" : "M"}${point.x.toFixed(2)},${point.outer.toFixed(2)}`).join(" ")} ${[...points].reverse().map((point) => `L${point.x.toFixed(2)},${point.inner.toFixed(2)}`).join(" ")} Z`;
  }

  function chartModel(series) {
    const timestamps = [...new Set(series.flatMap((item) => (item.samples || []).map((sample) => sample.timestamp)))].sort();
    const totals = timestamps.map((timestamp) => ({
      download: series.reduce((sum, item) => sum + (rateAt(item, timestamp, "download_bps") || 0), 0),
      upload: series.reduce((sum, item) => sum + (rateAt(item, timestamp, "upload_bps") || 0), 0),
      complete: series.every((item) => rateAt(item, timestamp, "download_bps") !== null && rateAt(item, timestamp, "upload_bps") !== null),
    }));
    const maximum = Math.max(1, ...totals.flatMap((item) => [item.download, item.upload]));
    return { timestamps, totals, maximum };
  }

  function renderAreas(series, model, colors) {
    const width = 1000;
    const zero = 120;
    const scale = 100 / model.maximum;
    return series.flatMap((item, seriesIndex) => ["download", "upload"].flatMap((direction) => {
      const field = `${direction}_bps`;
      const sign = direction === "download" ? -1 : 1;
      const points = model.timestamps.map((timestamp, timeIndex) => {
        const value = rateAt(item, timestamp, field);
        if (value === null) return null;
        const lower = series.slice(0, seriesIndex).reduce((sum, prior) => sum + (rateAt(prior, timestamp, field) || 0), 0);
        const x = model.timestamps.length === 1 ? width / 2 : timeIndex * width / (model.timestamps.length - 1);
        return { x, inner: zero + sign * lower * scale, outer: zero + sign * (lower + value) * scale };
      });
      return segments(points).filter((segment) => segment.length > 1).map((segment) => `<path data-egress-series="${item.id}-${direction}" d="${areaPath(segment)}" fill="${colors.get(item.id)}" fill-opacity="${direction === "download" ? ".72" : ".42"}"></path>`);
    })).join("");
  }

  function contourPath(model, direction) {
    const sign = direction === "download" ? -1 : 1;
    const scale = 100 / model.maximum;
    const points = model.timestamps.map((timestamp, index) => model.totals[index].complete ? {
      x: model.timestamps.length === 1 ? 500 : index * 1000 / (model.timestamps.length - 1),
      y: 120 + sign * model.totals[index][direction] * scale,
    } : null);
    return segments(points).filter((segment) => segment.length > 1).map((segment) => `<path class="traffic-total-contour" d="${segment.map((point, index) => `${index ? "L" : "M"}${point.x.toFixed(2)},${point.y.toFixed(2)}`).join(" ")}"></path>`).join("");
  }

  function kindLabel(kind) {
    return ({ direct_wan: "直连 WAN", wan_group: "WAN 群组", proxy: "代理出口" })[kind] || kind;
  }

  function tooltipHtml(options, timestamp) {
    const series = visibleSeries(options);
    const total = series.reduce((sum, item) => sum + (rateAt(item, timestamp, "download_bps") || 0) + (rateAt(item, timestamp, "upload_bps") || 0), 0);
    return `<strong>${options.escape(formatTime(timestamp))}</strong>${series.map((item) => {
      const sample = item.samples?.find((candidate) => candidate.timestamp === timestamp);
      const throughput = Number(sample?.download_bps || 0) + Number(sample?.upload_bps || 0);
      const bytes = Number(sample?.download_bytes || 0) + Number(sample?.upload_bytes || 0);
      return `<section><b><i style="background:${options.colors.get(item.id)}"></i>${options.escape(item.name)}</b><span>逻辑出口：${options.escape(kindLabel(item.kind))}</span><span>上传速率：${formatRate(sample?.upload_bps)}</span><span>下载速率：${formatRate(sample?.download_bps)}</span><span>区间字节：${formatBytes(bytes)}</span><span>占总流量：${total ? Math.round(throughput / total * 100) : 0}%</span><span>健康状态：${options.escape(sample?.health || item.health || "unknown")}</span>${item.underlay_wan_id ? `<span>底层 WAN：${options.escape(item.underlay_wan_id)}</span>` : ""}${item.active_member ? `<span>活动成员：${options.escape(item.active_member)}</span><span>最近切换：${options.escape(formatTime(item.last_switch_at))}</span><span>切换原因：${options.escape(item.switch_reason || "未返回")}</span>` : ""}</section>`;
    }).join("")}`;
  }

  function stateNotice(options, series) {
    if (options.loading) return '<p class="traffic-state is-loading">正在加载流量采样…</p>';
    if (options.error) return '<p class="traffic-state is-offline">流量采集离线，显示上次确认数据</p>';
    if (options.trend?.state === "unavailable") return '<p class="traffic-state is-empty"><strong>尚无流量采样</strong><span>流量采集服务暂未启用，完成部署后将自动显示数据。</span></p>';
    if (!series.length) return '<p class="traffic-state is-empty"><strong>当前窗口暂无流量</strong><span>没有可显示的逻辑出口采样。</span></p>';
    if (options.trend?.state === "stale" || options.trend?.degraded) {
      const lastSample = series.map((item) => item.last_sample_at).filter(Boolean).sort().at(-1);
      return `<p class="traffic-state is-stale"><strong>数据已陈旧</strong><span>最后成功采样 ${options.escape(formatTime(lastSample))}</span></p>`;
    }
    return '<p class="traffic-state is-fresh">实时采样正常</p>';
  }

  function directionalModel(series, direction) {
    const timestamps = [...new Set(series.flatMap((item) => (item.samples || []).map((sample) => sample.timestamp)))].sort();
    const values = timestamps.map((timestamp) => series.map((item) => rateAt(item, timestamp, `${direction}_bps`) || 0));
    const maximum = Math.max(0, ...values.map((row) => row.reduce((sum, value) => sum + value, 0)));
    return { timestamps, values, maximum };
  }

  function directionalChart(series, direction, colors, escape) {
    const model = directionalModel(series, direction);
    const width = 1000;
    const height = 220;
    const scaleMaximum = model.maximum || 1;
    const areas = model.timestamps.length > 1 ? series.map((item, seriesIndex) => {
      const points = model.timestamps.map((_, timeIndex) => {
        const value = model.values[timeIndex][seriesIndex] || 0;
        const lower = model.values[timeIndex].slice(0, seriesIndex).reduce((sum, itemValue) => sum + itemValue, 0);
        const x = timeIndex * width / (model.timestamps.length - 1);
        return { x, y: height - (lower + value) * (height - 10) / scaleMaximum, lowerY: height - lower * (height - 10) / scaleMaximum };
      });
      const path = `${points.map((point, index) => `${index ? "L" : "M"}${point.x.toFixed(2)},${point.y.toFixed(2)}`).join(" ")} ${[...points].reverse().map((point) => `L${point.x.toFixed(2)},${point.lowerY.toFixed(2)}`).join(" ")} Z`;
      return `<path class="traffic-pa-area" d="${path}" fill="${colors.get(item.id)}" fill-opacity=".82"></path>`;
    }).join("") : "";
    const grid = [0, 25, 50, 75, 100].map((ratio) => {
      const y = height - ratio * (height - 10) / 100;
      return `<line x1="0" y1="${y}" x2="${width}" y2="${y}" class="traffic-pa-grid-line"></line><text x="0" y="${Math.max(12, y - 5)}" class="traffic-pa-axis-label">${formatRate(model.maximum * ratio / 100)}</text>`;
    }).join("");
    const labels = model.timestamps.length ? [model.timestamps[0], model.timestamps[Math.floor(model.timestamps.length / 2)], model.timestamps.at(-1)].filter((value, index, list) => value && list.indexOf(value) === index).map((timestamp) => `<span>${escape(formatTime(timestamp))}</span>`).join("") : "";
    return `<div class="traffic-pa-chart"><svg viewBox="0 0 1000 220" preserveAspectRatio="none" role="img" aria-label="${direction === "upload" ? "上行" : "下行"}流量趋势">${grid}${areas}</svg><div class="traffic-pa-xlabels">${labels}</div>${!areas ? '<div class="traffic-pa-empty">暂无流量采样</div>' : ""}</div>`;
  }

  function renderTraffic(options) {
    const allSeries = options.trend?.series?.logical_egresses || [];
    const series = visibleSeries(options);
    const totals = currentTotals(series);
    const colors = new Map([...allSeries].sort((left, right) => left.id.localeCompare(right.id)).map((item, index) => [item.id, palette[index % palette.length]]));
    const activeConnections = Number(options.dashboard.sessions || options.dashboard.connections || 0);
    const onlineUsers = Number(options.dashboard.online_users || 0);
    return `<div class="traffic-pa-page"><section class="traffic-pa-kpis"><article><span>上行</span><strong>${formatRate(totals.upload)}</strong></article><article><span>下行</span><strong>${formatRate(totals.download)}</strong></article><article><span>活动连接</span><strong>${activeConnections}</strong></article><article><span>在线用户</span><strong>${onlineUsers}</strong></article><article><span>WAN 出口</span><strong>${allSeries.length}</strong></article></section><section class="traffic-pa-toolbar"><div><h2>流量趋势</h2><p>上行与下行流量趋势</p></div><div class="traffic-windows">${windows.map(([value, label]) => `<button type="button" data-traffic-window="${value}" aria-pressed="${options.window === value}">${label}</button>`).join("")}</div></section><section class="traffic-pa-chart-card"><header><h3>上行趋势</h3><div class="traffic-pa-chart-meta">当前 ${formatRate(totals.upload)}</div></header>${directionalChart(series, "upload", colors, options.escape)}</section><section class="traffic-pa-chart-card"><header><h3>下行趋势</h3><div class="traffic-pa-chart-meta">当前 ${formatRate(totals.download)}</div></header>${directionalChart(series, "download", colors, options.escape)}</section></div>`;
  }

  function wireTraffic(root, options, actions) {
    root.querySelector("[data-traffic-refresh]")?.addEventListener("click", actions.refresh);
    root.querySelectorAll("[data-traffic-window]").forEach((button) => button.addEventListener("click", () => actions.window(button.dataset.trafficWindow)));
    root.querySelectorAll("[data-egress-legend]").forEach((button) => button.addEventListener("click", () => actions.toggle(button.dataset.egressLegend)));
    const tooltip = root.querySelector("[role='tooltip']");
    root.querySelectorAll("[data-chart-point]").forEach((point) => point.addEventListener("mouseenter", () => {
      tooltip.innerHTML = tooltipHtml(options, options.model.timestamps[Number(point.dataset.chartPoint)]);
      tooltip.hidden = false;
    }));
    root.querySelector(".traffic-chart")?.addEventListener("mouseleave", () => { if (tooltip) tooltip.hidden = true; });
  }

  function usage(label, value) {
    const percent = Number.isFinite(Number(value)) ? Math.max(0, Math.min(100, Math.round(Number(value)))) : null;
    const level = percent === null ? "unknown" : percent >= 85 ? "high" : percent >= 65 ? "medium" : "normal";
    const levelLabel = { normal: "运行平稳", medium: "负载适中", high: "负载较高", unknown: "采样中" }[level];
    return `<article class="system-metric-card is-${level}">
      <header><div><span class="system-metric-icon">${label === "CPU使用率" ? "CPU" : "MEM"}</span><strong>${label}</strong></div><b>${percent === null ? "--" : `${percent}%`}</b></header>
      <div class="system-meter" role="progressbar" aria-label="${label}" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${percent === null ? 0 : percent}"><i style="width:${percent || 0}%"></i><span></span></div>
      <footer><span>${levelLabel}</span><small>${percent === null ? "正在获取本机数据" : `已使用 ${percent}%`}</small></footer>
    </article>`;
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

  function onlineIPv4Count(payload) {
    const items = Array.isArray(payload) ? payload : Array.isArray(payload?.items) ? payload.items : Array.isArray(payload?.data) ? payload.data : Array.isArray(payload?.data?.items) ? payload.data.items : [];
    return items.filter((item) => /^\d{1,3}(?:\.\d{1,3}){3}$/.test(String(item.ip || item.address || item.user || "").trim())).length;
  }

  function wanTrendModel(trend) {
    const series = trend?.series?.logical_egresses || [];
    const timestamps = [...new Set(series.flatMap((item) => (item.samples || []).map((sample) => sample.timestamp)))].sort();
    const totals = timestamps.map((timestamp) => series.map((item) => {
      const sample = item.samples?.find((candidate) => candidate.timestamp === timestamp);
      return Number(sample?.download_bps || 0) + Number(sample?.upload_bps || 0);
    }));
    const maximum = Math.max(1, ...totals.map((row) => row.reduce((sum, value) => sum + value, 0)));
    return { series, timestamps, totals, maximum };
  }

  function renderWanTrend(options) {
    const model = wanTrendModel(options.trafficTrend);
    const colors = new Map(model.series.map((item, index) => [item.id, palette[(index * 3 + 1) % palette.length]]));
    const width = 1000;
    const height = 248;
    const chart = model.timestamps.length > 1 ? model.series.map((item, seriesIndex) => {
      const points = model.timestamps.map((timestamp, timeIndex) => {
        const value = model.totals[timeIndex][seriesIndex] || 0;
        const lower = model.series.slice(0, seriesIndex).reduce((sum, _, priorIndex) => sum + (model.totals[timeIndex][priorIndex] || 0), 0);
        const x = timeIndex * width / (model.timestamps.length - 1);
        return { x, y: height - (lower + value) * (height - 12) / model.maximum, lowerY: height - lower * (height - 12) / model.maximum };
      });
      const path = `${points.map((point, index) => `${index ? "L" : "M"}${point.x.toFixed(2)},${point.y.toFixed(2)}`).join(" ")} ${[...points].reverse().map((point) => `L${point.x.toFixed(2)},${point.lowerY.toFixed(2)}`).join(" ")} Z`;
      return `<path class="system-wan-area" d="${path}" fill="${colors.get(item.id)}" fill-opacity=".78"></path>`;
    }).join("") : "";
    const peak = model.timestamps.length ? model.maximum : 0;
    const current = model.totals.at(-1)?.reduce((sum, value) => sum + value, 0) || 0;
    const average = model.totals.length ? model.totals.reduce((sum, row) => sum + row.reduce((rowSum, value) => rowSum + value, 0), 0) / model.totals.length : 0;
    const grid = [0, 25, 50, 75, 100].map((ratio) => {
      const y = height - ratio * (height - 12) / 100;
      return `<line x1="0" y1="${y}" x2="${width}" y2="${y}" class="system-wan-grid-line"></line><text x="0" y="${Math.max(12, y - 5)}" class="system-wan-axis-label">${formatRate(peak * ratio / 100)}</text>`;
    }).join("");
    const legend = model.series.map((item) => `<span class="system-wan-legend-item"><i style="background:${colors.get(item.id)}"></i>${options.escape(item.name || item.id)}</span>`).join("");
    return `<section class="system-panel system-traffic-panel"><header class="system-panel-head"><div><h3>WAN 流量趋势</h3></div><div class="system-traffic-summary"><span>当前 ${formatRate(current)}</span><span>峰值 ${formatRate(peak)}</span><span>平均 ${formatRate(average)}</span></div></header><div class="system-wan-legend">${legend || '<span class="system-wan-empty">暂无 WAN 流量采样</span>'}</div><div class="system-wan-chart"><svg viewBox="0 0 1000 248" preserveAspectRatio="none" role="img" aria-label="WAN 流量趋势">${grid}${chart}</svg>${!chart ? '<div class="system-wan-chart-empty">暂无流量采样</div>' : ''}</div></section>`;
  }

  function renderSystem(options) {
    const system = options.summary?.system || {};
    const platform = system.platform || "--";
    const systemTime = system.system_time ? formatTime(system.system_time) : "--";
    const facts = [["运行时长", uptimeLabel(system.uptime_seconds), "设备已连续运行"], ["在线用户", `${onlineIPv4Count(options.onlineUsers)} 个`, "当前 IP 数"], ["系统时间", systemTime, "设备本地时间"], ["硬件平台", platform, "设备运行平台"]];
    return `<div class="system-overview">
      <section class="system-overview-hero"><div class="system-hero-copy"><span class="system-eyebrow">ROUTER SYSTEM</span><h2>设备运行概况</h2><p>查看设备资源、运行时长、在线用户和系统时间。</p></div></section>
      <section class="system-panel system-resource-panel"><header class="system-panel-head"><div><h3>资源使用</h3><p>当前设备运行负载</p></div></header><div class="system-resource-grid">${usage("CPU使用率", system.cpu_busy_percent)}${usage("内存使用率", system.memory_used_percent)}</div></section>
      <section class="system-panel system-info-panel"><header class="system-panel-head"><div><h3>设备信息</h3><p>当前系统基础信息</p></div><span class="system-panel-mark">系统</span></header><div class="system-fact-grid">${facts.map(([label, value, hint]) => `<div class="system-fact"><span>${label}</span><strong>${options.escape(String(value))}</strong><small>${hint}</small></div>`).join("")}</div></section>
      ${renderWanTrend(options)}
    </div>`;
  }

  window.LyRouteGatewayOverview = Object.freeze({ renderSystem, renderTraffic, wireTraffic });
}());
