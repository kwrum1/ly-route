import { readFile } from "node:fs/promises";
import { createServer } from "node:http";
import { extname, join, normalize } from "node:path";

const jsonHeaders = { "content-type": "application/json; charset=utf-8" };
const observedAt = "2026-07-30T08:00:00Z";
const sampleTimes = [
  "2026-07-30T07:59:56Z",
  "2026-07-30T07:59:57Z",
  "2026-07-30T07:59:58Z",
  "2026-07-30T07:59:59Z",
  observedAt,
];

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function sendJSON(response, status, payload) {
  response.writeHead(status, jsonHeaders);
  response.end(JSON.stringify(payload));
}

async function requestJSON(request) {
  const chunks = [];
  for await (const chunk of request) chunks.push(chunk);
  return JSON.parse(Buffer.concat(chunks).toString("utf8") || "{}");
}

function apiError(response, status, message) {
  sendJSON(response, status, { error: { code: "fixture_error", message } });
}

function contentType(path) {
  return ({
    ".html": "text/html; charset=utf-8",
    ".js": "text/javascript; charset=utf-8",
    ".css": "text/css; charset=utf-8",
    ".json": "application/json; charset=utf-8",
  })[extname(path)] || "application/octet-stream";
}

function samples(download, upload, missingIndex = -1) {
  return sampleTimes.flatMap((timestamp, index) => index === missingIndex ? [] : [{
    timestamp,
    health: "healthy",
    download_bytes: download * (index + 1),
    upload_bytes: upload * (index + 1),
    download_bps: download + index * 100_000,
    upload_bps: upload + index * 50_000,
  }]);
}

function logicalSeries() {
  return [
    { id: "wan-primary", name: "主线路", kind: "direct_wan", health: "healthy", state: "available", fresh: true, last_sample_at: observedAt, samples: samples(18_000_000, 7_000_000) },
    { id: "wan-backup", name: "备用线路", kind: "direct_wan", health: "healthy", state: "available", fresh: true, last_sample_at: observedAt, samples: samples(7_000_000, 3_000_000, 2) },
    { id: "office-group", name: "办公出口组", kind: "wan_group", health: "degraded", state: "available", fresh: true, active_member: "wan-primary", last_switch_at: "2026-07-30T07:58:20Z", switch_reason: "health probe recovered", last_sample_at: observedAt, samples: samples(11_000_000, 5_000_000) },
    { id: "proxy-hk", name: "香港代理", kind: "proxy", health: "healthy", state: "available", fresh: true, underlay_wan_id: "wan-primary", last_sample_at: observedAt, samples: samples(9_000_000, 4_000_000) },
  ];
}

function trafficTrend(mode, window) {
  if (mode === "unavailable") return {
    window,
    points: 0,
    sampling_interval_seconds: 1,
    state: "unavailable",
    degraded: true,
    degraded_reason: "collector has not produced a sample",
    totals: { download_bps: 0, upload_bps: 0 },
    series: { logical_egresses: [] },
  };
  const series = logicalSeries();
  if (mode === "stale") {
    for (const item of series) {
      item.fresh = false;
      item.state = "stale";
      item.last_sample_at = "2026-07-30T07:40:00Z";
    }
  }
  return {
    window,
    points: sampleTimes.length,
    sampling_interval_seconds: 1,
    state: mode === "stale" ? "stale" : "available",
    degraded: mode === "stale",
    degraded_reason: mode === "stale" ? "collector timeout" : "",
    totals: { download_bps: 46_600_000, upload_bps: 20_800_000 },
    series: { logical_egresses: series },
  };
}

export async function startGatewayFixture({ bundleDir }) {
  const resources = {
    "/api/v1/interfaces": [{ id: "wan0", name: "wan0", gateway_role: "wan", link_state: "up", active_path: "vpp", work_mode: "vpp" }],
    "/api/v1/gateway/wan-links": [{ id: "wan-primary", name: "主线路", interface_id: "wan0", kind: "static", state: "available" }],
    "/api/v1/gateway/wan-groups": [],
    "/api/v1/proxy/egresses": [{ id: "proxy-hk", name: "香港代理", semantic_type: "proxy_egress", display_list: "wan", underlay_wan_id: "wan-primary", health: "healthy" }],
    "/api/v1/objects/groups": [{ id: "grp-office", kind: "ip", name: "办公网段", entries: ["192.168.88.0/24"] }],
    "/api/v1/gateway/policies/routes": [],
  };
  const state = { mode: "normal", requests: [], managementNetwork: { mode: "exclusive", interface_id: "eth0", cidr: "192.168.88.1/24", gateway: "192.168.88.1" } };
  const components = [
    { name: "vpp", label: "VPP", state: "running", available: true, checked_at: observedAt },
    { name: "smartdns", label: "SmartDNS", state: "degraded", available: true, reason: "上游探测延迟", checked_at: observedAt },
    { name: "kea", label: "Kea DHCP", state: "disabled", available: false, reason: "未配置 DHCP 服务", checked_at: observedAt },
    { name: "xray", label: "Xray", state: "failed", available: false, reason: "进程退出", checked_at: observedAt },
  ];

  async function apiRequest(request, response, url) {
    if (url.pathname === "/api/v1/auth/session") return sendJSON(response, 200, { session: { username: "qa", role: "admin" } });
    if (url.pathname === "/api/v1/auth/logout") return sendJSON(response, 200, { status: "ok" });
    if (url.pathname === "/api/v1/health") return sendJSON(response, 200, { status: "degraded", degraded: true, version: "10.4.0", dependencies: components });
    if (url.pathname === "/api/v1/mode") return sendJSON(response, 200, { mode: "gateway" });
    if (url.pathname === "/api/v1/capabilities") return sendJSON(response, 200, { items: components });
    if (url.pathname === "/api/v1/telemetry/dashboard") return sendJSON(response, 200, { data: { online_users: 23, sessions: 1486 } });
    if (url.pathname === "/api/v1/dashboard/summary") return sendJSON(response, 200, { system: { cpu_busy_percent: 31, memory_used_percent: 54, uptime_seconds: 98765, platform: "x86_64 appliance", version: "10.4.0", system_time: observedAt } });
    if (url.pathname === "/api/v1/telemetry/traffic-trend") {
      if (state.mode === "offline") return apiError(response, 503, "traffic collector offline");
      return sendJSON(response, 200, trafficTrend(state.mode, url.searchParams.get("window") || "5m"));
    }
    if (url.pathname === "/api/v1/runtime/status") return sendJSON(response, 200, { status: "degraded", components, generated_at: observedAt });
    if (url.pathname === "/api/v1/runtime/preview") return sendJSON(response, 200, { status: "preview", plan: { components } });
    if (url.pathname === "/api/v1/runtime/apply" && request.method === "POST") return sendJSON(response, 200, { status: "committed", transaction_id: "fixture-apply" });
    if (url.pathname === "/api/v1/config/export") return sendJSON(response, 200, { payload: { device_mode: "gateway" } });
    if (url.pathname === "/api/v1/management/network" && request.method === "GET") return sendJSON(response, 200, { item: clone(state.managementNetwork) });
    if (url.pathname === "/api/v1/management/network" && request.method === "POST") {
      state.managementNetwork = await requestJSON(request);
      state.managementNetwork.new_url = `https://${state.managementNetwork.cidr.split("/")[0]}/`;
      return sendJSON(response, 200, { item: clone(state.managementNetwork) });
    }
    if (url.pathname === "/api/v1/flow-control/smart-qos") return sendJSON(response, 200, { item: { id: "builtin-smart-qos", enabled: true, mutable: false, configuration_mode: "built_in", implementation: "ly_route_vpp_smart_qos", runtime_state: "running", reason: "VPP Smart QoS 生产插件已激活并通过运行态回读", selected_dataplane_tier: "vpp_native", low_level_controls: [] } });
    if (url.pathname === "/api/v1/firmware/update/status") return sendJSON(response, 200, { staged: false });
    if (url.pathname === "/api/v1/proxy/xray/status") return sendJSON(response, 200, components[3]);
    if (url.pathname === "/api/v1/proxy/xray/logs") return sendJSON(response, 200, { logs: "" });
    if (url.pathname === "/api/v1/gateway/pppoe/status") return sendJSON(response, 200, { status: "disabled", peers: [] });

    const collectionPath = Object.keys(resources).find((path) => url.pathname === path || url.pathname.startsWith(`${path}/`));
    if (collectionPath) {
      if (request.method === "GET") return sendJSON(response, 200, { items: clone(resources[collectionPath]), total: resources[collectionPath].length });
      if (["POST", "PATCH"].includes(request.method)) {
        const payload = await requestJSON(request);
        if (state.mode !== "false-success") {
          const items = resources[collectionPath].filter((item) => item.id !== payload.id);
          resources[collectionPath] = [...items, payload];
        }
        return sendJSON(response, request.method === "POST" ? 201 : 200, { item: clone(payload) });
      }
    }
    if (request.method === "GET") return sendJSON(response, 200, { items: [], total: 0 });
    return sendJSON(response, 200, { status: "ok" });
  }

  const server = createServer(async (request, response) => {
    const url = new URL(request.url || "/", "http://fixture.local");
    state.requests.push(`${request.method} ${url.pathname}${url.search}`);
    if (url.pathname === "/__fixture__/mode" && request.method === "POST") {
      const body = await requestJSON(request);
      state.mode = body.mode || "normal";
      return sendJSON(response, 200, { mode: state.mode });
    }
    if (url.pathname === "/__fixture__/requests") return sendJSON(response, 200, { items: state.requests });
    if (url.pathname.startsWith("/api/v1/")) return apiRequest(request, response, url);
    const requested = url.pathname === "/" ? "index.html" : url.pathname.slice(1);
    const path = normalize(join(bundleDir, requested));
    if (!path.startsWith(normalize(bundleDir))) return apiError(response, 403, "invalid path");
    try {
      const body = await readFile(path);
      response.writeHead(200, { "content-type": contentType(path) });
      response.end(body);
    } catch {
      response.writeHead(404);
      response.end("not found");
    }
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  return {
    url: `http://127.0.0.1:${address.port}`,
    close: () => new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve())),
  };
}
