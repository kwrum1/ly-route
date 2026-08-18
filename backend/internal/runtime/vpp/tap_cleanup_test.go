package vpp

import (
	"reflect"
	"testing"

	"ly-route/backend/internal/runtime/trafficpolicy"
)

func TestManagedTAPNamesPreservesDirectRouteServicePath(t *testing.T) {
	plan := Plan{Policy: trafficpolicy.Config{RoutePolicies: []trafficpolicy.RoutePolicy{{
		ID:   "route-100",
		Path: &trafficpolicy.WANPath{VPPInterface: "lypxinffdc88", NextHop: "198.18.34.90"},
	}}}}
	targets := managedTAPNames(plan)
	if _, ok := targets["lypxinffdc88"]; !ok {
		t.Fatalf("managed TAP targets = %#v, want direct route service TAP", targets)
	}
}

func TestManagedTAPDeleteIndicesRetainsSoleServiceTAPs(t *testing.T) {
	records := []managedTAPRecord{
		{index: 6, matches: []string{"lypxhinffdc88", "lypxinffdc88"}},
		{index: 4, matches: []string{"lydnshfcdfb0", "lydnsfcdfb0"}},
	}
	targets := map[string]struct{}{"lypxhinffdc88": {}, "lypxinffdc88": {}, "lydnshfcdfb0": {}, "lydnsfcdfb0": {}}
	if got := managedTAPDeleteIndices(records, targets); len(got) != 0 {
		t.Fatalf("delete indices = %v, want no deletion of sole service TAPs", got)
	}
}

func TestManagedTAPDeleteIndicesRemovesOnlyExactDuplicateIdentity(t *testing.T) {
	records := []managedTAPRecord{
		{index: 9, matches: []string{"lypxhinffdc88", "lypxinffdc88"}},
		{index: 6, matches: []string{"lypxhinffdc88", "lypxinffdc88"}},
		{index: 7, matches: []string{"lypxhinother", "lypxinother"}},
	}
	targets := map[string]struct{}{"lypxhinffdc88": {}, "lypxinffdc88": {}, "lypxhinother": {}, "lypxinother": {}}
	if got, want := managedTAPDeleteIndices(records, targets), []int{9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("delete indices = %v, want %v", got, want)
	}
}

func TestManagedTAPDeleteIndicesRemovesOrphansAbsentFromBothPlans(t *testing.T) {
	records := []managedTAPRecord{
		{index: 4, matches: []string{"lydnshfcdfb0", "lydnsfcdfb0"}},
		{index: 6, matches: []string{"lydnshold001", "lydnsold001"}},
		{index: 9, matches: []string{"lydnshfcdfb0", "lydnsfcdfb0"}},
	}
	targets := map[string]struct{}{"lydnshfcdfb0": {}, "lydnsfcdfb0": {}}
	if got, want := managedTAPDeleteIndices(records, targets), []int{6, 9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("delete indices = %v, want %v", got, want)
	}
}
