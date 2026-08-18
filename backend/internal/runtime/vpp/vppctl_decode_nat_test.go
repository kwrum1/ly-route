package vpp

import (
	"reflect"
	"testing"

	"ly-route/backend/internal/runtime/nat"
)

func TestDecodeVPPCTLNAT44IgnoresUnrelatedLiveMappings(t *testing.T) {
	static := nat.StaticMapping{ID: "wanted-static", InternalAddress: "192.0.2.10", ExternalAddress: "198.51.100.10"}
	port := nat.PortMapping{ID: "wanted-port", Protocol: "tcp", InternalHost: "192.0.2.20", InternalPort: 8080, ExternalAddress: "198.51.100.20", ExternalPort: 18080}
	request := SnapshotRequest{
		NATStaticMappings: []string{static.ID},
		NATPortMappings:   []string{port.ID},
		Candidates: SnapshotCandidates{
			NATStaticMappings: []nat.StaticMapping{static},
			NATPortMappings:   []nat.PortMapping{port},
		},
	}
	results := []VPPCTLCommandResult{{Command: "show nat44 static mappings", Stdout: `NAT44 static mappings:
  local 192.0.2.9 external 198.51.100.9 vrf 0
  local 192.0.2.10 external 198.51.100.10 vrf 0
  TCP local 192.0.2.19:8080 external 198.51.100.19:18080 vrf 0
  TCP local 192.0.2.20:8080 external 198.51.100.20:18080 vrf 0
`}}
	readback, err := decodeVPPCTLNAT44(request, results)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(readback.StaticMappings, []nat.StaticMapping{static}) || !reflect.DeepEqual(readback.PortMappings, []nat.PortMapping{port}) {
		t.Fatalf("readback = %#v", readback)
	}
}
