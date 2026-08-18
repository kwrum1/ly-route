package vpp

import (
	"fmt"

	"ly-route/backend/internal/runtime/nat"
)

func parseNAT44Readback(payload any) (NAT44Readback, error) {
	payload = unwrapVPPCTLReadback(payload)
	switch value := payload.(type) {
	case NAT44Readback:
		return value, nil
	case nat.CompiledConfig:
		return NAT44Readback{StaticMappings: value.StaticMappings, PortMappings: value.PortMappings, Behavior: value.Behavior}, nil
	default:
		return NAT44Readback{}, fmt.Errorf("%w: NAT44 readback payload has type %T", ErrSnapshotIncomplete, payload)
	}
}
