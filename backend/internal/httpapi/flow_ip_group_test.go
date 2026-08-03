package httpapi

import (
	"context"
	"reflect"
	"testing"

	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/flow"
)

func TestFlowIntentExpandsIPGroupsOnlyForRuntimeCompilation(t *testing.T) {
	store, err := persistence.Open(context.Background(), "file:flow-ip-group?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := fixedClock()()
	if err := store.SaveConfig(context.Background(), configDocument(t, "object_group", "group-lan", map[string]any{
		"id": "group-lan", "kind": "ip", "entries": []any{"192.168.88.10", "192.168.88.20-192.168.88.21"},
	}, now)); err != nil {
		t.Fatal(err)
	}
	server := New(WithStore(store))
	desired := flow.Intent{ID: "default", Rules: []flow.Rule{{ID: "limit", Granularity: flow.RuleGranularity, Match: flow.Match{Sources: []string{"group-lan"}, Destinations: []string{"any"}}, Actions: []flow.Action{flow.Police(20_000_000, 2_000_000)}}}}
	expanded, err := server.expandFlowIntentAddressGroups(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	wantSources := []string{"192.168.88.10/32", "192.168.88.20/31"}
	if !reflect.DeepEqual(expanded.Rules[0].Match.Sources, wantSources) {
		t.Fatalf("expanded sources = %#v, want %#v", expanded.Rules[0].Match.Sources, wantSources)
	}
	if !reflect.DeepEqual(expanded.Rules[0].Match.Destinations, []string{"0.0.0.0/0"}) {
		t.Fatalf("expanded destinations = %#v", expanded.Rules[0].Match.Destinations)
	}
	if !reflect.DeepEqual(desired.Rules[0].Match.Sources, []string{"group-lan"}) {
		t.Fatalf("desired intent was mutated: %#v", desired)
	}
}
