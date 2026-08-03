package service

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

var ErrIPv6PrefixTooSmall = errors.New("ipv6 delegated prefix is too small for LAN RA")

type IPv6RAPlan struct {
	Interface       string `json:"interface"`
	DelegatedPrefix string `json:"delegated_prefix"`
}

func RenderIPv6RA(plan IPv6RAPlan) ([]RenderedArtifact, error) {
	interfaceName := strings.TrimSpace(plan.Interface)
	if interfaceName == "" || interfaceName != plan.Interface || strings.ContainsAny(interfaceName, "\x00\n\r\t ;|&$()<>\\\"") {
		return nil, fmt.Errorf("ipv6 RA interface is invalid")
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(plan.DelegatedPrefix))
	if err != nil || !prefix.Addr().Is6() {
		return nil, fmt.Errorf("ipv6 delegated prefix %q is invalid", plan.DelegatedPrefix)
	}
	prefix = prefix.Masked()
	if prefix.Bits() > 64 {
		return nil, fmt.Errorf("%w: %s cannot allocate a /64", ErrIPv6PrefixTooSmall, prefix)
	}
	lanPrefix := netip.PrefixFrom(prefix.Addr(), 64).Masked()
	content := fmt.Sprintf("interface %s\n{\n  AdvSendAdvert on;\n  MinRtrAdvInterval 30;\n  MaxRtrAdvInterval 100;\n  prefix %s\n  {\n    AdvOnLink on;\n    AdvAutonomous on;\n  };\n};\n", interfaceName, lanPrefix)
	return []RenderedArtifact{NewArtifact(IPv6RA, "/etc/radvd.conf", content, "reload")}, nil
}
