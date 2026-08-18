package httpapi

import "testing"

func TestEqualGatewaySliceUnorderedAcceptsVPPPortMapReadbackOrder(t *testing.T) {
	type portMap struct {
		ID   string `json:"id"`
		Port int    `json:"port"`
	}
	desired := []portMap{{ID: "first", Port: 18080}, {ID: "second", Port: 18081}}
	actual := []portMap{{ID: "second", Port: 18081}, {ID: "first", Port: 18080}}
	if !equalGatewaySliceUnordered(actual, desired) {
		t.Fatalf("unordered VPP readback was treated as different: actual=%#v desired=%#v", actual, desired)
	}
}
