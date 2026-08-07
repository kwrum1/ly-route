package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func liveReadback(ctx context.Context, runner CommandRunner, service ServiceName, artifacts []RenderedArtifact) error {
	var err error
	// PPPoE uses one instantiated unit per WAN, while Linux routing is applied by
	// a verified oneshot helper. Their live network state is authoritative.
	if service != PPPoE && service != LinuxRouting {
		health, healthErr := runner.Status(ctx, service)
		if healthErr != nil {
			return healthErr
		}
		if !health.Available {
			return errors.New(health.Reason)
		}
	}

	switch service {
	case SmartDNS:
		_, err = requiredOutput(ctx, runner, "systemctl", "show", applyUnit(service), "--property=ActiveEnterTimestampMonotonic", "--value")
	case Kea:
		_, err = requiredOutput(ctx, runner, "kea-dhcp4", "-t", "/etc/kea/kea-dhcp4.conf")
	case Xray:
		_, err = requiredOutput(ctx, runner, "xray", "run", "-test", "-config", "/etc/xray/config.json")
	case PPPoE:
		err = validatePPPoEReadback(ctx, runner, artifacts)
	case Nftables:
		err = validateNftablesReadback(ctx, runner, artifacts)
	case LinuxRouting:
		err = validatePolicyRoutingReadback(ctx, runner, artifacts)
	case VPP:
		err = validateVPPReadback(ctx, runner, artifacts)
	case IPv6RA:
		err = validateIPv6RAReadback(ctx, runner, artifacts)
	default:
		err = fmt.Errorf("%s has no live readback", service)
	}
	return err
}

func requiredOutput(ctx context.Context, runner CommandRunner, name string, args ...string) (string, error) {
	output, err := runner.Output(ctx, name, args...)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(output) == "" {
		return "", fmt.Errorf("%s %s returned empty live readback", name, strings.Join(args, " "))
	}
	return output, nil
}

func serviceReadbackCommands(service ServiceName) ([][]string, error) {
	switch service {
	case SmartDNS:
		return [][]string{{"systemctl", "show", applyUnit(service), "--property=ActiveEnterTimestampMonotonic", "--value"}}, nil
	case Kea:
		return [][]string{{"kea-dhcp4", "-t", "/etc/kea/kea-dhcp4.conf"}}, nil
	case Xray:
		return [][]string{{"xray", "run", "-test", "-config", "/etc/xray/config.json"}}, nil
	case PPPoE:
		return [][]string{{"ip", "-j", "address", "show", "dev", "ppp0"}}, nil
	case IPv6RA:
		return [][]string{{"radvdump"}}, nil
	default:
		return nil, fmt.Errorf("%s readback commands depend on rendered artifacts", service)
	}
}
