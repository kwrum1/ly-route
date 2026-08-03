package product

import (
	"encoding/json"
	"errors"
	"fmt"
)

var ErrInvalidProductID = errors.New("invalid product ID")

type ID struct {
	value string
}

func (id ID) String() string {
	return id.value
}

func (id ID) MarshalJSON() ([]byte, error) {
	if _, err := ParseProfile(id.value); err != nil {
		return nil, fmt.Errorf("marshal product ID: %w", err)
	}
	return json.Marshal(id.value)
}

func (id *ID) UnmarshalJSON(data []byte) error {
	var raw string
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("unmarshal product ID: %w", err)
	}
	profile, err := ParseProfile(raw)
	if err != nil {
		return fmt.Errorf("unmarshal product ID: %w", err)
	}
	*id = profile.ID()
	return nil
}

type Capability string

const (
	CapabilityProduct        Capability = "product"
	CapabilityHealth         Capability = "health"
	CapabilityAuth           Capability = "auth"
	CapabilityManagement     Capability = "management"
	CapabilityInterfaces     Capability = "interfaces"
	CapabilityGatewayMode    Capability = "gateway_mode"
	CapabilityObjectGroups   Capability = "object_groups"
	CapabilityGatewayWAN     Capability = "gateway_wan"
	CapabilityGatewayPPPoE   Capability = "gateway_pppoe"
	CapabilityGatewayRouting Capability = "gateway_routing"
	CapabilityGatewayNAT     Capability = "gateway_nat"
	CapabilityProxy          Capability = "proxy"
	CapabilityDNS            Capability = "dns"
	CapabilityDHCP           Capability = "dhcp"
	CapabilitySecurity       Capability = "security"
	CapabilityTrafficControl Capability = "traffic_control"
	CapabilityRuntime        Capability = "runtime"
	CapabilityConfig         Capability = "config"
	CapabilityFirmware       Capability = "firmware"
	CapabilityDashboard      Capability = "dashboard"
	CapabilityTelemetry      Capability = "telemetry"
	CapabilityTopDomains     Capability = "top_domains"
	capabilityCount                     = 22
)

var allCapabilities = [capabilityCount]Capability{
	CapabilityProduct,
	CapabilityHealth,
	CapabilityAuth,
	CapabilityManagement,
	CapabilityInterfaces,
	CapabilityGatewayMode,
	CapabilityObjectGroups,
	CapabilityGatewayWAN,
	CapabilityGatewayPPPoE,
	CapabilityGatewayRouting,
	CapabilityGatewayNAT,
	CapabilityProxy,
	CapabilityDNS,
	CapabilityDHCP,
	CapabilitySecurity,
	CapabilityTrafficControl,
	CapabilityRuntime,
	CapabilityConfig,
	CapabilityFirmware,
	CapabilityDashboard,
	CapabilityTelemetry,
	CapabilityTopDomains,
}

type Profile struct {
	id           ID
	capabilities [capabilityCount]bool
	services     [serviceCount]bool
}

func ParseProfile(raw string) (Profile, error) {
	switch raw {
	case "gateway":
		return Gateway(), nil
	case "orchestrator":
		return Orchestrator(), nil
	default:
		return Profile{}, fmt.Errorf("%w: %q", ErrInvalidProductID, raw)
	}
}

func Gateway() Profile {
	profile := Profile{id: ID{value: "gateway"}}
	for index := range profile.capabilities {
		profile.capabilities[index] = true
	}
	for _, service := range []Service{
		ServiceControlAPI,
		ServiceVPP,
		ServiceSmartDNS,
		ServiceDNSVPPProxy,
		ServiceKea,
		ServiceXray,
		ServicePPPoE,
	} {
		if index, valid := lookupServiceIndex(service); valid {
			profile.services[index] = true
		}
	}
	return profile
}

func Orchestrator() Profile {
	profile := Profile{id: ID{value: "orchestrator"}}
	for _, capability := range []Capability{
		CapabilityProduct,
		CapabilityHealth,
		CapabilityAuth,
		CapabilityManagement,
		CapabilityInterfaces,
		CapabilityObjectGroups,
		CapabilitySecurity,
		CapabilityTrafficControl,
		CapabilityRuntime,
		CapabilityConfig,
		CapabilityDashboard,
		CapabilityTelemetry,
	} {
		if index, valid := lookupCapabilityIndex(capability); valid {
			profile.capabilities[index] = true
		}
	}
	for _, service := range []Service{ServiceControlAPI, ServiceVPP} {
		if index, valid := lookupServiceIndex(service); valid {
			profile.services[index] = true
		}
	}
	return profile
}

func (profile Profile) ID() ID {
	return profile.id
}

func (profile Profile) Capabilities() []Capability {
	capabilities := make([]Capability, 0, len(allCapabilities))
	for index, capability := range allCapabilities {
		if profile.capabilities[index] {
			capabilities = append(capabilities, capability)
		}
	}
	return capabilities
}

func (profile Profile) Services() []Service {
	services := make([]Service, 0, len(allServices))
	for index, service := range allServices {
		if profile.services[index] {
			services = append(services, service)
		}
	}
	return services
}

func (profile Profile) Allows(capability Capability) bool {
	index, valid := lookupCapabilityIndex(capability)
	return valid && profile.capabilities[index]
}

func (profile Profile) AllowsService(service Service) bool {
	index, valid := lookupServiceIndex(service)
	return valid && profile.services[index]
}

func (profile Profile) Validate() error {
	_, err := ParseProfile(profile.id.value)
	return err
}

func lookupCapabilityIndex(capability Capability) (int, bool) {
	switch capability {
	case CapabilityProduct:
		return 0, true
	case CapabilityHealth:
		return 1, true
	case CapabilityAuth:
		return 2, true
	case CapabilityManagement:
		return 3, true
	case CapabilityInterfaces:
		return 4, true
	case CapabilityGatewayMode:
		return 5, true
	case CapabilityObjectGroups:
		return 6, true
	case CapabilityGatewayWAN:
		return 7, true
	case CapabilityGatewayPPPoE:
		return 8, true
	case CapabilityGatewayRouting:
		return 9, true
	case CapabilityGatewayNAT:
		return 10, true
	case CapabilityProxy:
		return 11, true
	case CapabilityDNS:
		return 12, true
	case CapabilityDHCP:
		return 13, true
	case CapabilitySecurity:
		return 14, true
	case CapabilityTrafficControl:
		return 15, true
	case CapabilityRuntime:
		return 16, true
	case CapabilityConfig:
		return 17, true
	case CapabilityFirmware:
		return 18, true
	case CapabilityDashboard:
		return 19, true
	case CapabilityTelemetry:
		return 20, true
	case CapabilityTopDomains:
		return 21, true
	default:
		return 0, false
	}
}
