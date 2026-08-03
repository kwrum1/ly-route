package httpapi

import (
	"context"
	"testing"
	"time"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/dns"
)

func TestDNSPolicyExpandsSourceIPGroupAndPreservesReference(t *testing.T) {
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:httpapi-dns-ip-group-test?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	payload, hash, err := persistence.MarshalPayload(map[string]any{
		"id": "clients", "kind": "ip", "enabled": true,
		"entries": []string{"192.0.2.10", "192.0.2.16/28"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConfig(ctx, persistence.ConfigDocument{ResourceType: "object_group", ResourceID: "clients", Payload: payload, PayloadHash: hash, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	server := New(WithStore(store))
	resource, _, _, err := server.compileDNSPolicyResource(ctx, DNSPolicyResource{
		ID: "dns-group", Enabled: true,
		Policy: dns.NewPolicy(dns.Reject(), []dns.Rule{{
			ID: "group-rule", SourcePrefixes: []string{"clients"}, Domains: []string{"example.test"}, Outcome: dns.Direct(),
		}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resource.Policy.Rules[0].SourcePrefixes[0] != "clients" {
		t.Fatalf("stored selector = %v, want group reference", resource.Policy.Rules[0].SourcePrefixes)
	}
	got := resource.Render.Rules[0].SourcePrefixes
	if len(got) != 2 || got[0] != "192.0.2.10/32" || got[1] != "192.0.2.16/28" {
		t.Fatalf("rendered selectors = %v", got)
	}
}
