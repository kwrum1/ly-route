package vpp

import (
	"reflect"
	"testing"
)

func TestManagementLCPPairs(t *testing.T) {
	output := `lcp default netns '<unset>'
itf-pair: [0] lyroute-eth0 tap4096 lymgmt0 2 type tap
itf-pair: [1] lyroute-eth1 tap4097 other0 3 type tap
`
	if got := managementLCPPairs(output, "lymgmt0"); !reflect.DeepEqual(got, []string{"lyroute-eth0"}) {
		t.Fatalf("management pairs = %#v", got)
	}
	if !managementLCPPresent(output, "lyroute-eth0", "lymgmt0") {
		t.Fatal("management pair was not found")
	}
}
