package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/proxy"
)

func (server *Server) handleProxySubscriptionRefresh(w http.ResponseWriter, r *http.Request, id string) {
	session, ok := server.sessionFromRequest(r)
	if !ok {
		server.recordAudit("anonymous", "system", r.URL.Path, "refresh", "denied", "authentication required", r)
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if session.Role != "admin" {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "refresh", "denied", "readonly mutation denied", r)
		writeError(w, r, http.StatusForbidden, "forbidden", "admin role required")
		return
	}
	if server.passwordChangeRequired(w, r, session, r.URL.Path, "refresh") {
		return
	}
	if server.store == nil {
		writeError(w, r, http.StatusServiceUnavailable, "store_unavailable", "local store is not configured")
		return
	}
	subscription, exists, err := server.desiredItem(r.Context(), "proxy_subscription", id)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "desired_state_read_failed", err.Error())
		return
	}
	if !exists {
		writeError(w, r, http.StatusNotFound, "not_found", "subscription was not found")
		return
	}
	endpoint, err := server.store.Secret(r.Context(), "proxy_subscription", id, "url")
	if err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "refresh", "failure", "subscription credential is unavailable", r)
		writeError(w, r, http.StatusUnprocessableEntity, "subscription_url_unavailable", "subscription URL is unavailable; update the subscription URL before refreshing")
		return
	}
	content, err := server.subscriptionFetch(r.Context(), endpoint, true)
	if err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "refresh", "failure", err.Error(), r)
		writeError(w, r, http.StatusBadGateway, "subscription_fetch_failed", err.Error())
		return
	}
	nodes, err := proxy.ParseSubscription(content)
	if err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "refresh", "failure", err.Error(), r)
		writeError(w, r, http.StatusUnprocessableEntity, "subscription_parse_failed", err.Error())
		return
	}
	now := server.now().UTC()
	writes := make([]persistence.ConfigWithSecrets, 0, len(nodes)+1)
	references := make([]string, 0, len(nodes))
	current := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		node.ID = id + "-" + strings.TrimPrefix(node.ID, "imported-")
		if prepared, prepareErr := proxy.PrepareNodeTLS(r.Context(), node); prepareErr == nil {
			node = prepared
		}
		if _, compileErr := proxy.CompileNodeOutbound(node); compileErr != nil {
			server.recordAudit(session.Username, session.Role, r.URL.Path, "refresh", "failure", compileErr.Error(), r)
			writeError(w, r, http.StatusUnprocessableEntity, "subscription_node_invalid", compileErr.Error())
			return
		}
		public := map[string]any{
			"id": node.ID, "kind": "node", "name": node.Name, "enabled": true,
			"protocol": node.Protocol, "address": node.Address, "port": node.Port,
			"settings": node.Settings, "source": "subscription", "subscription_id": id,
			"secret_redacted": "redacted", "credential_ref": "local-secret:proxy_node:" + node.ID + ":secret",
			"runtime_state": "desired_not_applied",
		}
		raw, hash, marshalErr := persistence.MarshalPayload(public)
		if marshalErr != nil {
			writeError(w, r, http.StatusInternalServerError, "subscription_persist_failed", "subscription node could not be encoded")
			return
		}
		writes = append(writes, persistence.ConfigWithSecrets{Document: persistence.ConfigDocument{ResourceType: "proxy_node", ResourceID: node.ID, Payload: raw, PayloadHash: hash, UpdatedAt: now}, Secrets: map[string]string{"secret": node.Secret}})
		references = append(references, node.ID)
		current[node.ID] = true
	}
	subscription = cloneObject(subscription)
	subscription["node_refs"] = references
	subscription["node_count"] = len(references)
	subscription["last_refresh_at"] = now.Format("2006-01-02T15:04:05.000000000Z")
	subscription["refresh_state"] = "succeeded"
	subscription["runtime_state"] = "desired_not_applied"
	raw, hash, err := persistence.MarshalPayload(subscription)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "subscription_persist_failed", "subscription could not be encoded")
		return
	}
	writes = append(writes, persistence.ConfigWithSecrets{Document: persistence.ConfigDocument{ResourceType: "proxy_subscription", ResourceID: id, Payload: raw, PayloadHash: hash, UpdatedAt: now}})

	allNodes, err := server.desiredItems(r.Context(), "proxy_node")
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "desired_state_read_failed", err.Error())
		return
	}
	deletes := []persistence.ConfigKey{}
	for _, existing := range allNodes {
		existingID := stringField(existing, "id")
		if stringField(existing, "subscription_id") == id && !current[existingID] {
			deletes = append(deletes, persistence.ConfigKey{ResourceType: "proxy_node", ResourceID: existingID})
		}
	}
	if err := server.store.SaveConfigsWithSecretsAndDelete(r.Context(), writes, deletes); err != nil {
		server.recordAudit(session.Username, session.Role, r.URL.Path, "refresh", "failure", err.Error(), r)
		writeError(w, r, http.StatusInternalServerError, "subscription_persist_failed", "subscription refresh could not be persisted")
		return
	}
	server.recordAudit(session.Username, session.Role, r.URL.Path, "refresh", "success", fmt.Sprintf("nodes=%d removed=%d self_signed_tls=true", len(nodes), len(deletes)), r)
	response := cloneObject(subscription)
	delete(response, "url")
	delete(response, "subscription_url")
	encoded, _ := json.Marshal(response)
	var safeResponse map[string]any
	_ = json.Unmarshal(encoded, &safeResponse)
	writeJSON(w, http.StatusOK, map[string]any{"item": safeResponse, "imported_nodes": len(nodes), "removed_nodes": len(deletes), "request_id": requestID(r)})
}
