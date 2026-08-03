import { readFile } from "node:fs/promises";
import { createServer } from "node:http";
import { extname, join, normalize } from "node:path";

const jsonHeaders = { "content-type": "application/json; charset=utf-8" };

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function canonicalTopology(value) {
  const topology = clone(value);
  topology.interfaces.sort((left, right) => left.role.localeCompare(right.role));
  topology.orchestration_groups.sort((left, right) => left.name.localeCompare(right.name));
  for (const item of topology.interfaces) item.bond?.members.sort();
  for (const group of topology.orchestration_groups) group.ports.sort((left, right) => left.direction.localeCompare(right.direction));
  return topology;
}

function checksumFor(topology) {
  return `fixture-${Buffer.from(JSON.stringify(topology)).toString("base64url").slice(0, 24)}`;
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

function apiError(response, status, code, message) {
  sendJSON(response, status, { error: { code, message } });
}

function contentType(path) {
  return ({ ".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".css": "text/css; charset=utf-8", ".json": "application/json; charset=utf-8" })[extname(path)] || "application/octet-stream";
}

export async function startOrchestratorFixture({ bundleDir, fixturePath }) {
  const immutableFixture = JSON.parse(await readFile(fixturePath, "utf8"));
  const inventory = [
    { name: "eth0", management: true, link_state: "up", speed_mbps: 1000, driver: "igc" },
    { name: "eth1", link_state: "up", speed_mbps: 2500, driver: "igc" },
    { name: "eth2", link_state: "up", speed_mbps: 2500, driver: "igc" },
    { name: "eth3", link_state: "up", speed_mbps: 1000, driver: "ixgbe" },
    { name: "eth4", link_state: "up", speed_mbps: 1000, driver: "ixgbe" },
    { name: "eth5", link_state: "down", speed_mbps: 1000, driver: "ixgbe" },
    { name: "eth6", link_state: "up", speed_mbps: 1000, driver: "ixgbe" },
    { name: "eth7", link_state: "up", speed_mbps: 1000, driver: "ixgbe" },
  ];
  const state = {
    topology: null,
    managementNetwork: { mode: "exclusive", interface_id: "eth0", cidr: "192.168.88.1/24", gateway: "" },
    ipGroups: [{ id: "ip-ops", name: "运维终端", kind: "ip", entries: ["10.10.10.0/24"] }],
    securityACLs: [],
    users: [{ username: "admin", role: "admin", enabled: true }],
    snapshots: [],
    policy: {
      schema_version: 1,
      ip_objects: [],
      policy_groups: [],
      default: { kind: "direct" },
    },
    trafficControl: { id: "orchestrator-rate-policies", rules: [] },
    mode: "normal",
    failReadback: 0,
    requests: [],
  };

  function topologyRead(response) {
    if (state.failReadback > 0) {
      state.failReadback -= 1;
      apiError(response, 500, "internal_error", "fixture readback unavailable");
      return;
    }
    if (!state.topology) {
      apiError(response, 404, "topology_not_found", "orchestrator topology not found");
      return;
    }
    sendJSON(response, 200, { item: clone(state.topology), checksum: checksumFor(state.topology) });
  }

  function applyMutation(response, next, successStatus, item) {
    const mode = state.mode;
    state.mode = "normal";
    if (mode === "api-error") {
      apiError(response, 500, "internal_error", "fixture write failed");
      return;
    }
    if (mode === "conflict") {
      apiError(response, 409, "interface_already_owned", "interface already owned by concurrent state");
      return;
    }
    const canonical = canonicalTopology(next);
    if (mode !== "false-success") state.topology = canonical;
    if (mode === "stale-readback") state.failReadback = 1;
    sendJSON(response, successStatus, { item: clone(item || canonical), checksum: checksumFor(canonical) });
  }

  async function apiRequest(request, response, url) {
    if (url.pathname === "/api/v1/auth/session") return sendJSON(response, 200, { session: { username: "qa", role: "admin" } });
    if (url.pathname === "/api/v1/auth/logout") return sendJSON(response, 200, { status: "ok" });
    if (url.pathname === "/api/v1/auth/login") return sendJSON(response, 200, { session: { username: "qa", role: "admin" } });
    if (url.pathname === "/api/v1/product") return sendJSON(response, 200, { product: "orchestrator" });
    if (url.pathname === "/api/v1/health") return sendJSON(response, 200, { status: "ok" });
    if (url.pathname === "/api/v1/capabilities") return sendJSON(response, 200, { items: [{ name: "orchestrator_topology", available: true }] });
    if (url.pathname === "/api/v1/runtime/status") return sendJSON(response, 200, { runtime_state: "running", services: [{ name: "vpp", label: "VPP", state: "running" }, { name: "control-api", label: "Control API", state: "running" }] });
    if (url.pathname === "/api/v1/config/export") return sendJSON(response, 200, { package_manifest: { schema_version: 1, product: "orchestrator", secret_policy: "excluded" }, payload: { schema_version: 1, product: "orchestrator", resources: { object_group: clone(state.ipGroups), security_acl: clone(state.securityACLs) } } });
    if (url.pathname === "/api/v1/config/import" && request.method === "POST") {
      const body = await requestJSON(request);
      if (body.payload?.product !== "orchestrator" || body.package_manifest?.product !== "orchestrator") return apiError(response, 422, "product_mismatch", "config package product mismatch");
      if ((body.payload?.resources?.object_group || []).some((item) => item.kind !== "ip")) return apiError(response, 422, "invalid_import_package", "Orchestrator supports IP groups only");
      if (!body.confirm) return sendJSON(response, 200, { status: "dry_run", safe_to_apply: true, rollback_snapshot_required: true, package_hash: "sha256:fixture-package", diff_hash: "sha256:fixture-diff", confirmation_token: "fixture-confirm", confirmation_actor: "qa", confirmation_expires_at: "2099-08-01T08:15:00Z" });
      if (body.confirmation_token !== "fixture-confirm" || body.package_hash !== "sha256:fixture-package" || body.diff_hash !== "sha256:fixture-diff") return apiError(response, 422, "confirmation_required", "confirmation does not match preflight");
      state.ipGroups = clone(body.payload.resources?.object_group || []);
      state.securityACLs = clone(body.payload.resources?.security_acl || []);
      return sendJSON(response, 200, { status: "persisted", runtime_state: "desired_not_applied", rollback_snapshot_id: "pre-import-fixture", imported_resources: state.ipGroups.length + state.securityACLs.length });
    }
    const snapshotMatch = url.pathname.match(/^\/api\/v1\/config\/snapshots(?:\/([^/]+))?$/);
    if (snapshotMatch && request.method === "GET") return sendJSON(response, 200, { items: clone(state.snapshots) });
    if (snapshotMatch && request.method === "POST" && !snapshotMatch[1]) {
      const body = await requestJSON(request);
      const snapshot = { id: `manual-${String(body.name || "snapshot").replace(/\s+/g, "-")}-fixture`, product: "orchestrator", source_transaction_id: "manual", payload_hash: "sha256:fixture", created_at: "2026-08-01T08:00:00Z" };
      state.snapshots = [...state.snapshots.filter((item) => item.id !== snapshot.id), snapshot];
      return sendJSON(response, 200, { status: "created", snapshot: clone(snapshot), runtime_state: "desired_not_applied" });
    }
    if (snapshotMatch?.[1] && request.method === "POST") {
      const id = decodeURIComponent(snapshotMatch[1]);
      const snapshot = state.snapshots.find((item) => item.id === id);
      return snapshot ? sendJSON(response, 200, { status: "restored", snapshot: clone(snapshot), runtime_state: "desired_not_applied" }) : apiError(response, 404, "snapshot_not_found", "snapshot does not exist");
    }
    if (url.pathname === "/api/v1/interfaces") return sendJSON(response, 200, { items: clone(inventory), total: inventory.length });
    if (url.pathname === "/api/v1/management/network" && request.method === "GET") return sendJSON(response, 200, { item: clone(state.managementNetwork) });
    if (url.pathname === "/api/v1/management/network" && request.method === "POST") {
      const next = await requestJSON(request);
      if (next.confirm_change !== true) return apiError(response, 422, "invalid_management_network", "confirmation required");
      state.managementNetwork = { ...state.managementNetwork, ...next, new_url: `https://${String(next.cidr).split("/")[0]}/` };
      return sendJSON(response, 200, { item: clone(state.managementNetwork), runtime_state: "desired_not_applied" });
    }
    const ipGroupMatch = url.pathname.match(/^\/api\/v1\/objects\/ip-groups(?:\/([^/]+))?$/);
    if (ipGroupMatch && request.method === "GET") return sendJSON(response, 200, { items: clone(state.ipGroups) });
    if (ipGroupMatch && ["POST", "PATCH"].includes(request.method)) {
      const item = await requestJSON(request);
      if (item.kind !== "ip") return apiError(response, 422, "invalid_desired_resource", "Orchestrator supports IP groups only");
      state.ipGroups = [...state.ipGroups.filter((entry) => entry.id !== item.id), item];
      return sendJSON(response, 200, { item: clone(item), runtime_state: "desired_not_applied" });
    }
    if (ipGroupMatch?.[1] && request.method === "DELETE") {
      state.ipGroups = state.ipGroups.filter((entry) => entry.id !== decodeURIComponent(ipGroupMatch[1]));
      return sendJSON(response, 200, { status: "deleted" });
    }
    const aclMatch = url.pathname.match(/^\/api\/v1\/security\/acls(?:\/([^/]+))?$/);
    if (aclMatch && request.method === "GET") return sendJSON(response, 200, { items: clone(state.securityACLs) });
    if (aclMatch && ["POST", "PATCH"].includes(request.method)) {
      const item = await requestJSON(request);
      state.securityACLs = [...state.securityACLs.filter((entry) => entry.id !== item.id), item];
      return sendJSON(response, 200, { item: clone(item), runtime_state: "desired_not_applied" });
    }
    if (aclMatch?.[1] && request.method === "DELETE") {
      state.securityACLs = state.securityACLs.filter((entry) => entry.id !== decodeURIComponent(aclMatch[1]));
      return sendJSON(response, 200, { status: "deleted" });
    }
    const userMatch = url.pathname.match(/^\/api\/v1\/auth\/users(?:\/([^/]+))?$/);
    if (userMatch && request.method === "GET") return sendJSON(response, 200, { items: clone(state.users) });
    if (userMatch && ["POST", "PATCH"].includes(request.method)) {
      const item = await requestJSON(request);
      const publicItem = { username: item.username, role: item.role, enabled: true };
      state.users = [...state.users.filter((entry) => entry.username !== item.username), publicItem];
      return sendJSON(response, 200, { item: clone(publicItem) });
    }
    if (userMatch?.[1] && request.method === "DELETE") {
      const username = decodeURIComponent(userMatch[1]);
      if (username === "admin") return apiError(response, 422, "protected_user", "cannot delete the built-in admin");
      state.users = state.users.filter((entry) => entry.username !== username);
      return sendJSON(response, 200, { status: "deleted" });
    }
    if (url.pathname === "/api/v1/flow-control/policies" && request.method === "GET") return sendJSON(response, 200, { items: [clone(state.trafficControl)] });
    if (url.pathname === "/api/v1/flow-control/policies" && request.method === "POST") {
      state.trafficControl = await requestJSON(request);
      return sendJSON(response, 200, { item: clone(state.trafficControl), runtime_state: "desired_not_applied" });
    }
    if (url.pathname === "/api/v1/flow-control/policies/orchestrator-rate-policies" && request.method === "PATCH") {
      state.trafficControl = await requestJSON(request);
      return sendJSON(response, 200, { item: clone(state.trafficControl), runtime_state: "desired_not_applied" });
    }
    if (url.pathname === "/api/v1/runtime/apply" && request.method === "POST") return sendJSON(response, 200, { status: "committed", transaction_id: "fixture-flow-apply" });
    if (url.pathname === "/api/v1/telemetry/dashboard") return sendJSON(response, 200, { device_mode: "orchestrator", degraded: false, orchestration_groups: [{ name: "fixture-chain", state: "available", additive: false, wan_to_lan: { bytes: 4096, bytes_per_second: 1024, rate_state: "available" }, lan_to_wan: { bytes: 2048, bytes_per_second: 512, rate_state: "available" } }] });
    if (url.pathname === "/api/v1/telemetry/policy-hits") return sendJSON(response, 200, { policy_hits: [{ group: "fixture-chain", bytes: 0, packets: 0 }] });
    if (url.pathname === "/api/v1/telemetry/online-users") return sendJSON(response, 200, { items: [] });
    if (url.pathname === "/api/v1/telemetry/top-sessions") return sendJSON(response, 200, { items: [] });
    if (url.pathname === "/api/v1/orchestrator/policy" && request.method === "GET") return sendJSON(response, 200, { item: clone(state.policy) });
    if (url.pathname === "/api/v1/orchestrator/policy" && request.method === "PUT") {
      state.policy = await requestJSON(request);
      return sendJSON(response, 200, { item: clone(state.policy) });
    }
    if (url.pathname === "/api/v1/orchestrator/topology" && request.method === "GET") return topologyRead(response);
    if (url.pathname === "/api/v1/orchestrator/topology" && request.method === "PUT") {
      const next = await requestJSON(request);
      return applyMutation(response, next, 200);
    }
    const groupMatch = url.pathname.match(/^\/api\/v1\/orchestrator\/orchestration-groups(?:\/([^/]+))?$/);
    if (groupMatch && request.method === "GET") {
      if (!state.topology) return apiError(response, 404, "topology_not_found", "orchestrator topology not found");
      const name = groupMatch[1] ? decodeURIComponent(groupMatch[1]) : "";
      if (!name) return sendJSON(response, 200, { items: clone(state.topology.orchestration_groups), checksum: checksumFor(state.topology) });
      const group = state.topology.orchestration_groups.find((item) => item.name === name);
      return group ? sendJSON(response, 200, { item: clone(group), checksum: checksumFor(state.topology) }) : apiError(response, 404, "group_not_found", "group not found");
    }
    if (groupMatch && ["POST", "PUT"].includes(request.method)) {
      if (!state.topology) return apiError(response, 409, "topology_conflict", "topology must be initialized");
      const group = await requestJSON(request);
      const pathName = groupMatch[1] ? decodeURIComponent(groupMatch[1]) : "";
      const groups = state.topology.orchestration_groups.filter((item) => item.name !== (pathName || group.name));
      const next = { ...clone(state.topology), orchestration_groups: [...groups, group] };
      return applyMutation(response, next, request.method === "POST" ? 201 : 200, group);
    }
    if (groupMatch?.[1] && request.method === "DELETE") {
      if (!state.topology) return apiError(response, 404, "topology_not_found", "orchestrator topology not found");
      const name = decodeURIComponent(groupMatch[1]);
      const next = { ...clone(state.topology), orchestration_groups: state.topology.orchestration_groups.filter((item) => item.name !== name) };
      const mode = state.mode;
      state.mode = "normal";
      if (mode === "api-error") return apiError(response, 500, "internal_error", "fixture delete failed");
      if (mode === "conflict") return apiError(response, 409, "topology_conflict", "stale topology mutation");
      state.topology = canonicalTopology(next);
      if (mode === "stale-readback") state.failReadback = 1;
      response.writeHead(204);
      return response.end();
    }
    apiError(response, 404, "not_found", `fixture route missing: ${request.method} ${url.pathname}`);
  }

  const server = createServer(async (request, response) => {
    const url = new URL(request.url || "/", "http://fixture.local");
    state.requests.push(`${request.method} ${url.pathname}`);
    if (url.pathname === "/__fixture__/state" && request.method === "POST") {
      const body = await requestJSON(request);
      state.topology = body.initial === "fixture" ? canonicalTopology(immutableFixture) : null;
      state.managementNetwork = { mode: "exclusive", interface_id: "eth0", cidr: "192.168.88.1/24", gateway: "" };
      state.ipGroups = [{ id: "ip-ops", name: "运维终端", kind: "ip", entries: ["10.10.10.0/24"] }];
      state.securityACLs = [];
      state.users = [{ username: "admin", role: "admin", enabled: true }];
      state.snapshots = [];
      state.policy = { schema_version: 1, ip_objects: [], policy_groups: [], default: { kind: "direct" } };
      state.trafficControl = { id: "orchestrator-rate-policies", rules: [] };
      state.mode = body.mode || "normal";
      state.failReadback = 0;
      state.requests = [];
      return sendJSON(response, 200, { status: "reset" });
    }
    if (url.pathname === "/__fixture__/mode" && request.method === "POST") {
      const body = await requestJSON(request);
      state.mode = body.mode || "normal";
      return sendJSON(response, 200, { mode: state.mode });
    }
    if (url.pathname === "/__fixture__/requests") return sendJSON(response, 200, { items: state.requests });
    if (url.pathname.startsWith("/api/v1/")) return apiRequest(request, response, url);
    const requested = url.pathname === "/" ? "index.html" : url.pathname.slice(1);
    const path = normalize(join(bundleDir, requested));
    if (!path.startsWith(normalize(bundleDir))) return apiError(response, 403, "forbidden", "invalid path");
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
