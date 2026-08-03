package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"ly-route/backend/internal/runtime/dns"
	"ly-route/backend/internal/runtime/flow"
	"ly-route/backend/internal/runtime/proxy"
)

func TestStorePersistsAndReloadsApplyRecordFromFileSQLite(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "lyroute.db")
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	record := buildApplyRecord(t, now)

	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveApply(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })

	configDocument, err := reopened.Config(ctx, "proxy_egress", "proxy-media")
	if err != nil {
		t.Fatal(err)
	}
	var proxyEgress proxy.Egress
	if err := json.Unmarshal(configDocument.Payload, &proxyEgress); err != nil {
		t.Fatal(err)
	}
	if proxyEgress.SemanticType != proxy.ProxyEgress || proxyEgress.ID != "proxy-media" {
		t.Fatalf("proxy config = %#v, want persisted proxy egress", proxyEgress)
	}

	policyDocument, err := reopened.Policy(ctx, "dns-policy", "default")
	if err != nil {
		t.Fatal(err)
	}
	var dnsPolicy dns.Policy
	if err := json.Unmarshal(policyDocument.Payload, &dnsPolicy); err != nil {
		t.Fatal(err)
	}
	if !policyDocument.Enabled || dnsPolicy.Miss.Kind != dns.OutcomeReject {
		t.Fatalf("DNS policy = %#v enabled=%v, want reject miss enabled policy", dnsPolicy, policyDocument.Enabled)
	}

	snapshot, err := reopened.RuntimeSnapshot(ctx, "snapshot-before-apply")
	if err != nil {
		t.Fatal(err)
	}
	var runtimePayload persistedRuntime
	if err := json.Unmarshal(snapshot.Payload, &runtimePayload); err != nil {
		t.Fatal(err)
	}
	if runtimePayload.Flow.ID != "default" || snapshot.SourceTransactionID != "txn-apply-1" {
		t.Fatalf("runtime snapshot = %#v source=%q, want persisted flow/default source", runtimePayload, snapshot.SourceTransactionID)
	}

	rollback, err := reopened.Rollback(ctx, "rollback-for-apply")
	if err != nil {
		t.Fatal(err)
	}
	if rollback.TargetSnapshotID != "snapshot-before-apply" || rollback.Reason != "health check failure" || rollback.Status != "pending" {
		t.Fatalf("rollback metadata = %#v, want target snapshot/reason/status", rollback)
	}
}

func TestStorePersistsInMemorySQLiteAndInitializesIdempotently(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, "file:lyroute-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	record := buildApplyRecord(t, time.Date(2026, 6, 5, 12, 30, 0, 0, time.UTC))
	if err := store.SaveApply(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RuntimeSnapshot(ctx, record.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRehydratesRenderedSmartDNSPolicyDocument(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, "file:lyroute-smartdns-render-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	policy := dns.NewPolicy(dns.Reject(), []dns.Rule{
		{ID: "direct-local", Domains: []string{"lan.example"}, Outcome: dns.Direct()},
		{ID: "proxy-media", Domains: []string{"video.example"}, Outcome: dns.Proxy("proxy-media")},
		{ID: "reject-ads", Domains: []string{"ads.example"}, Outcome: dns.Reject()},
	})
	compiled, err := dns.CompilePolicy(policy, []proxy.Egress{proxy.NewProxyEgress("proxy-media", "xray-tproxy-outbound")})
	if err != nil {
		t.Fatalf("compile DNS policy: %v", err)
	}
	rendered := compiled.RenderSmartDNS()
	payload, payloadHash, err := MarshalPayload(rendered)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 6, 5, 14, 0, 0, 0, time.UTC)
	if err := store.SaveApply(ctx, ApplyRecord{Policies: []PolicyDocument{{
		Namespace:   "dns-render",
		PolicyID:    "smartdns-default",
		Priority:    0,
		Enabled:     true,
		Payload:     payload,
		PayloadHash: payloadHash,
		UpdatedAt:   now,
	}}}); err != nil {
		t.Fatal(err)
	}

	document, err := store.Policy(ctx, "dns-render", "smartdns-default")
	if err != nil {
		t.Fatal(err)
	}
	var rehydrated dns.SmartDNSRender
	if err := json.Unmarshal(document.Payload, &rehydrated); err != nil {
		t.Fatal(err)
	}
	rehydratedPayload, err := json.Marshal(rehydrated)
	if err != nil {
		t.Fatal(err)
	}
	if string(rehydratedPayload) != string(payload) || document.PayloadHash != payloadHash || !document.Enabled {
		t.Fatalf("rehydrated SmartDNS render = %s hash=%q enabled=%v, want %s hash=%q enabled=true", rehydratedPayload, document.PayloadHash, document.Enabled, payload, payloadHash)
	}
	if rehydrated.Rules[1].Action != "proxy_dns_request" || rehydrated.Rules[1].ResolverPath != "proxy_egress_resolver" || rehydrated.Rules[1].DNSRequestPath != "proxy_egress_dns" || rehydrated.Rules[1].ProxyEgressID != "proxy-media" {
		t.Fatalf("proxy DNS render = %#v, want DNS-request proxying through proxy egress", rehydrated.Rules[1])
	}
}

func TestStoreDeletesConfigDocuments(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, "file:lyroute-delete-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	payload, hash, err := MarshalPayload(map[string]any{"id": "wan-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(ctx, ConfigDocument{ResourceType: "wan_link", ResourceID: "wan-test", Payload: payload, PayloadHash: hash, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteConfig(ctx, "wan_link", "wan-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Config(ctx, "wan_link", "wan-test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Config after delete error = %v, want ErrNotFound", err)
	}
	if err := store.DeleteConfig(ctx, "wan_link", "wan-test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second DeleteConfig error = %v, want ErrNotFound", err)
	}
}

func TestReplaceConfigsForTypesRemovesStaleDocuments(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, "file:lyroute-replace-configs-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	stalePayload, staleHash, err := MarshalPayload(map[string]any{"id": "stale"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfigs(ctx, []ConfigDocument{{ResourceType: "wan_link", ResourceID: "stale", Payload: stalePayload, PayloadHash: staleHash, UpdatedAt: now}, {ResourceType: "port_map", ResourceID: "stale-map", Payload: stalePayload, PayloadHash: staleHash, UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	freshPayload, freshHash, err := MarshalPayload(map[string]any{"id": "fresh"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceConfigsForTypes(ctx, []string{"wan_link", "port_map"}, []ConfigDocument{{ResourceType: "wan_link", ResourceID: "fresh", Payload: freshPayload, PayloadHash: freshHash, UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Config(ctx, "wan_link", "stale"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale wan lookup error = %v, want ErrNotFound", err)
	}
	if _, err := store.Config(ctx, "port_map", "stale-map"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale port map lookup error = %v, want ErrNotFound", err)
	}
	if _, err := store.Config(ctx, "wan_link", "fresh"); err != nil {
		t.Fatalf("fresh wan lookup error = %v", err)
	}
}

func TestSaveApplyIsTransactional(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, "file:lyroute-transaction-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	record := buildApplyRecord(t, time.Date(2026, 6, 5, 13, 0, 0, 0, time.UTC))
	record.Rollback.TargetSnapshotID = "missing-snapshot"
	if err := store.SaveApply(ctx, record); err == nil {
		t.Fatal("SaveApply succeeded with rollback metadata targeting a missing snapshot")
	}

	if _, err := store.Config(ctx, "proxy_egress", "proxy-media"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("config lookup error = %v, want ErrNotFound after rollback", err)
	}
	if _, err := store.RuntimeSnapshot(ctx, "snapshot-before-apply"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("snapshot lookup error = %v, want ErrNotFound after rollback", err)
	}
}

type persistedRuntime struct {
	Proxy proxy.Egress `json:"proxy"`
	DNS   dns.Policy   `json:"dns"`
	Flow  flow.Intent  `json:"flow"`
}

func buildApplyRecord(t *testing.T, now time.Time) ApplyRecord {
	t.Helper()
	proxyEgress := proxy.NewProxyEgress("proxy-media", "xray-tproxy-outbound")
	dnsPolicy := dns.NewPolicy(dns.Reject(), []dns.Rule{
		{ID: "proxy-media", Domains: []string{"media.example"}, Outcome: dns.Proxy("proxy-media")},
	})
	flowIntent := flow.NewIntent("default", []flow.Rule{
		flow.NewRule("classify-video", flow.RuleGranularity, flow.Classify("video"), flow.Remark("AF41"), flow.Police(10_000_000, 1_000_000)),
	})
	runtimePayload := persistedRuntime{Proxy: proxyEgress, DNS: dnsPolicy, Flow: flowIntent}

	proxyPayload, proxyHash, err := MarshalPayload(proxyEgress)
	if err != nil {
		t.Fatal(err)
	}
	policyPayload, policyHash, err := MarshalPayload(dnsPolicy)
	if err != nil {
		t.Fatal(err)
	}
	snapshotPayload, snapshotHash, err := MarshalPayload(runtimePayload)
	if err != nil {
		t.Fatal(err)
	}

	return ApplyRecord{
		ConfigDocuments: []ConfigDocument{{
			ResourceType: "proxy_egress",
			ResourceID:   "proxy-media",
			Payload:      proxyPayload,
			PayloadHash:  proxyHash,
			UpdatedAt:    now,
		}},
		Policies: []PolicyDocument{{
			Namespace:   "dns-policy",
			PolicyID:    "default",
			Priority:    10,
			Enabled:     true,
			Payload:     policyPayload,
			PayloadHash: policyHash,
			UpdatedAt:   now,
		}},
		Snapshot: RuntimeSnapshot{
			ID:                  "snapshot-before-apply",
			SourceTransactionID: "txn-apply-1",
			Payload:             snapshotPayload,
			PayloadHash:         snapshotHash,
			CreatedAt:           now,
		},
		Rollback: RollbackMetadata{
			ID:               "rollback-for-apply",
			TargetSnapshotID: "snapshot-before-apply",
			Reason:           "health check failure",
			Status:           "pending",
			RequestedAt:      now.Add(time.Minute),
		},
	}
}
