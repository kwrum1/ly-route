package vpp

import (
	"errors"
	"testing"
)

func TestSelectNAT44AllowsVerifiedMissingMappingForDriftRepair(t *testing.T) {
	request := SnapshotRequest{
		AllowMissing:    true,
		NATPortMappings: []string{"accept-portmap"},
	}

	statics, ports, err := selectNAT44(NAT44Readback{}, request)
	if err != nil {
		t.Fatalf("select NAT44 drift readback: %v", err)
	}
	if len(statics) != 0 || len(ports) != 0 {
		t.Fatalf("missing drift state = statics:%v ports:%v, want empty", statics, ports)
	}
}

func TestSelectNAT44FailsClosedWhenMappingIsRequired(t *testing.T) {
	request := SnapshotRequest{NATPortMappings: []string{"accept-portmap"}}

	_, _, err := selectNAT44(NAT44Readback{}, request)
	if !errors.Is(err, ErrSnapshotIncomplete) {
		t.Fatalf("select NAT44 required readback error = %v, want ErrSnapshotIncomplete", err)
	}
}
