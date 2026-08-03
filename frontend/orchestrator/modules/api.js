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
