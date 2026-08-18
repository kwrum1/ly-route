package nat

import (
	"fmt"
	"strconv"
	"strings"
)

type StaticMapping struct {
	ID              string `json:"id"`
	ExternalAddress string `json:"external_address"`
	InternalAddress string `json:"internal_address"`
	WANInterface    string `json:"wan_interface,omitempty"`
	WANNextHop      string `json:"wan_next_hop,omitempty"`
	ReturnPathGuard bool   `json:"return_path_guard,omitempty"`
}

type PortMapping struct {
	ID              string `json:"id"`
	Protocol        string `json:"protocol"`
	ExternalAddress string `json:"external_address,omitempty"`
	ExternalPort    int    `json:"external_port"`
	InternalHost    string `json:"internal_host"`
	InternalPort    int    `json:"internal_port"`
	WANInterface    string `json:"wan_interface,omitempty"`
	WANNextHop      string `json:"wan_next_hop,omitempty"`
	Hairpin         bool   `json:"hairpin,omitempty"`
	ReturnPathGuard bool   `json:"return_path_guard,omitempty"`
}

// Behavior selects the NAT44 connection-tracking semantics for the gateway.
// Endpoint-dependent is the safe default used by NAT44-ED. Full-cone uses
// VPP's endpoint-independent NAT44-EI plugin and is a global gateway mode;
// the two VPP plugins are never mixed in one running plan.
type Behavior string

const (
	BehaviorEndpointDependent Behavior = "endpoint_dependent"
	BehaviorFullCone          Behavior = "full_cone"
)

type CompiledConfig struct {
	StaticMappings []StaticMapping `json:"static_mappings,omitempty"`
	PortMappings   []PortMapping   `json:"port_mappings,omitempty"`
	Behavior       Behavior        `json:"behavior,omitempty"`
}

// ResolveBehavior reads accepted API aliases from all NAT-related resources.
// Missing values use the endpoint-dependent default. Explicit conflicting
// values are rejected because VPP cannot safely run NAT44-ED and NAT44-EI on
// the same gateway at the same time.
func ResolveBehavior(itemSets ...[]map[string]any) (Behavior, error) {
	behavior := BehaviorEndpointDependent
	configured := false
	for _, items := range itemSets {
		for _, item := range items {
			if !enabled(item) {
				continue
			}
			candidate, explicit, err := requestedBehavior(item)
			if err != nil {
				return "", err
			}
			if !explicit {
				continue
			}
			if configured && candidate != behavior {
				return "", fmt.Errorf("conflicting NAT behaviors: %q and %q cannot be active together", behavior, candidate)
			}
			behavior, configured = candidate, true
		}
	}
	return behavior, nil
}

func requestedBehavior(item map[string]any) (Behavior, bool, error) {
	for _, key := range []string{"full_cone", "full_cone_nat", "endpoint_independent"} {
		if value, ok := item[key]; ok && truthy(value) {
			return BehaviorFullCone, true, nil
		}
	}
	for _, key := range []string{"nat_behavior", "nat_mode"} {
		value := strings.TrimSpace(strings.ToLower(fmt.Sprint(item[key])))
		if value == "" || value == "<nil>" {
			continue
		}
		normalized := strings.ReplaceAll(strings.ReplaceAll(value, "-", "_"), " ", "_")
		switch normalized {
		case "full_cone", "fullcone", "endpoint_independent", "endpoint_independent_nat", "cone":
			return BehaviorFullCone, true, nil
		case "endpoint_dependent", "endpoint_dependent_nat", "nat44_ed", "ed", "default":
			return BehaviorEndpointDependent, true, nil
		default:
			return "", false, fmt.Errorf("unsupported NAT behavior %q; use endpoint_dependent or full_cone", value)
		}
	}
	return BehaviorEndpointDependent, false, nil
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		value := strings.ToLower(strings.TrimSpace(typed))
		return value == "true" || value == "1" || value == "enabled" || value == "yes"
	default:
		return false
	}
}

func BindWANInterfaces(config *CompiledConfig, bindings map[string]string) error {
	if config == nil {
		return fmt.Errorf("NAT config is required")
	}
	for index := range config.StaticMappings {
		if err := bindWANInterface(&config.StaticMappings[index].WANInterface, config.StaticMappings[index].ID, "nat_static", bindings); err != nil {
			return err
		}
	}
	for index := range config.PortMappings {
		if err := bindWANInterface(&config.PortMappings[index].WANInterface, config.PortMappings[index].ID, "port_map", bindings); err != nil {
			return err
		}
	}
	return nil
}

func bindWANInterface(target *string, id, kind string, bindings map[string]string) error {
	wanID := strings.TrimSpace(*target)
	if wanID == "" {
		return nil
	}
	interfaceName, exists := bindings[wanID]
	if !exists || strings.TrimSpace(interfaceName) == "" {
		return fmt.Errorf("%s %q WAN %q has no runtime VPP interface binding", kind, id, wanID)
	}
	if err := optionalToken(interfaceName, kind, id, "runtime WAN interface"); err != nil {
		return err
	}
	*target = strings.TrimSpace(interfaceName)
	return nil
}

func CompileConfig(staticItems, portMapItems []map[string]any) (CompiledConfig, error) {
	return CompileConfigWithWANs(staticItems, portMapItems, nil)
}

func CompileConfigWithWANs(staticItems, portMapItems, wanItems []map[string]any) (CompiledConfig, error) {
	wanAddresses := compileWANAddresses(wanItems)
	staticMappings := make([]StaticMapping, 0, len(staticItems))
	for _, item := range staticItems {
		if !enabled(item) {
			continue
		}
		mapping, err := compileStaticMapping(item)
		if err != nil {
			return CompiledConfig{}, err
		}
		if mapping.WANInterface != "" {
			// Static mappings created through the router UI are bound to a WAN,
			// not to a transient DHCP or PPPoE lease. Keep their public address
			// in sync with the live WAN for the same reconnect behavior as port
			// mappings. A stale address creates a VPP mapping that cannot receive
			// replies on the current WAN session.
			if liveAddress := strings.TrimSpace(wanAddresses[mapping.WANInterface]); liveAddress != "" {
				mapping.ExternalAddress = liveAddress
			}
		}
		staticMappings = append(staticMappings, mapping)
	}

	portMappings := make([]PortMapping, 0, len(portMapItems))
	validatedPortMappings := make([]PortMapping, 0, len(portMapItems))
	for _, item := range portMapItems {
		if !enabled(item) {
			continue
		}
		mapping, err := compilePortMapping(item, wanAddresses)
		if err != nil {
			return CompiledConfig{}, err
		}
		validatedPortMappings = append(validatedPortMappings, mapping)
		// A port map bound to a dynamic WAN is valid before the WAN has a
		// lease. Do not let it block PPPoE startup; it is installed on the
		// next reconcile after the WAN address is available.
		if mapping.ExternalAddress == "" {
			continue
		}
		portMappings = append(portMappings, mapping)
	}
	if err := validatePortMappingUniqueness(validatedPortMappings); err != nil {
		return CompiledConfig{}, err
	}

	behavior, err := ResolveBehavior(staticItems, portMapItems, wanItems)
	if err != nil {
		return CompiledConfig{}, err
	}
	return CompiledConfig{StaticMappings: staticMappings, PortMappings: portMappings, Behavior: behavior}, nil
}

// validatePortMappingUniqueness enforces the VPP NAT44 static mapping
// contract. NAT44-EI and the gateway's NAT44-ED adapter both identify a
// mapping by protocol plus internal endpoint, so changing only the public
// port cannot create a second mapping for the same LAN service. Rejecting it
// during compilation keeps a bad desired state from reaching runtime apply.
func validatePortMappingUniqueness(mappings []PortMapping) error {
	seen := make(map[string]PortMapping, len(mappings))
	for _, mapping := range mappings {
		key := strings.Join([]string{mapping.Protocol, mapping.InternalHost, strconv.Itoa(mapping.InternalPort)}, "/")
		if previous, exists := seen[key]; exists {
			return fmt.Errorf("port_map %q conflicts with %q: %s %s:%d is already mapped; VPP supports one mapping per internal endpoint", mapping.ID, previous.ID, mapping.Protocol, mapping.InternalHost, mapping.InternalPort)
		}
		seen[key] = mapping
	}
	return nil
}

func compileStaticMapping(item map[string]any) (StaticMapping, error) {
	id := stringValue(item, "id", "name")
	mapping := StaticMapping{
		ID:              id,
		ExternalAddress: stringValue(item, "external_address", "public_ip", "wan_ip", "external_ip"),
		InternalAddress: stringValue(item, "internal_address", "internal_host", "private_ip", "inside_address"),
		WANInterface:    stringValue(item, "wan_interface", "wan_link", "interface_id", "egress"),
		ReturnPathGuard: true,
	}
	if err := requireID(id, "nat_static"); err != nil {
		return StaticMapping{}, err
	}
	if err := requireToken(mapping.ExternalAddress, "nat_static", id, "external_address"); err != nil {
		return StaticMapping{}, err
	}
	if err := requireToken(mapping.InternalAddress, "nat_static", id, "internal_address"); err != nil {
		return StaticMapping{}, err
	}
	if err := optionalToken(mapping.WANInterface, "nat_static", id, "wan_interface"); err != nil {
		return StaticMapping{}, err
	}
	if strings.Contains(mapping.ExternalAddress, ":") || strings.Contains(mapping.InternalAddress, ":") {
		return StaticMapping{}, fmt.Errorf("nat_static %q only supports IPv4 NAT44", id)
	}
	return mapping, nil
}

func compilePortMapping(item map[string]any, wanAddresses map[string]string) (PortMapping, error) {
	id := stringValue(item, "id", "name")
	internalHost := stringValue(item, "internal_host", "internal_address", "private_ip", "dst_ip")
	internalPort, hasInternalPort := intValue(item, "internal_port", "private_port", "dst_port")
	if internalHost == "" || !hasInternalPort {
		host, port, ok := splitHostPort(stringValue(item, "internal_target"))
		if internalHost == "" {
			internalHost = host
		}
		if !hasInternalPort && ok {
			internalPort = port
			hasInternalPort = true
		}
	}
	externalPort, hasExternalPort := intValue(item, "external_port", "public_port", "src_port")
	mapping := PortMapping{
		ID:              id,
		Protocol:        strings.ToLower(stringValue(item, "protocol")),
		ExternalAddress: stringValue(item, "external_address", "public_ip", "wan_ip", "external_ip"),
		ExternalPort:    externalPort,
		InternalHost:    internalHost,
		InternalPort:    internalPort,
		WANInterface:    stringValue(item, "wan_interface", "wan_link", "interface_id", "egress"),
		Hairpin:         boolValue(item, "hairpin", "internal_loopback", "loopback"),
		ReturnPathGuard: true,
	}
	if mapping.Protocol == "" {
		mapping.Protocol = "tcp"
	}
	if mapping.WANInterface != "" {
		// Port mappings created from the router UI are bound to a WAN, not to a
		// transient PPPoE lease. Prefer the live WAN address on every compile so
		// reconnects cannot leave a stale public address in VPP.
		if liveAddress := strings.TrimSpace(wanAddresses[mapping.WANInterface]); liveAddress != "" {
			mapping.ExternalAddress = liveAddress
		}
	}
	if err := requireID(id, "port_map"); err != nil {
		return PortMapping{}, err
	}
	if mapping.ExternalAddress == "" && mapping.WANInterface == "" {
		return PortMapping{}, fmt.Errorf("port_map %q requires an acquired WAN address or external_address", id)
	}
	if err := optionalToken(mapping.ExternalAddress, "port_map", id, "external_address"); err != nil {
		return PortMapping{}, err
	}
	if err := optionalToken(mapping.WANInterface, "port_map", id, "wan_interface"); err != nil {
		return PortMapping{}, err
	}
	if err := requireToken(mapping.InternalHost, "port_map", id, "internal_host"); err != nil {
		return PortMapping{}, err
	}
	if strings.Contains(mapping.ExternalAddress, ":") || strings.Contains(mapping.InternalHost, ":") {
		return PortMapping{}, fmt.Errorf("port_map %q only supports IPv4 NAT44", id)
	}
	if err := requireToken(mapping.Protocol, "port_map", id, "protocol"); err != nil {
		return PortMapping{}, err
	}
	if mapping.Protocol != "tcp" && mapping.Protocol != "udp" {
		return PortMapping{}, fmt.Errorf("port_map %q protocol must be tcp or udp", id)
	}
	if !hasExternalPort || externalPort <= 0 || externalPort > 65535 {
		return PortMapping{}, fmt.Errorf("port_map %q requires external_port in 1..65535", id)
	}
	if !hasInternalPort || internalPort <= 0 || internalPort > 65535 {
		return PortMapping{}, fmt.Errorf("port_map %q requires internal_port in 1..65535", id)
	}
	if mapping.Hairpin && externalPort != internalPort {
		return PortMapping{}, fmt.Errorf("port_map %q hairpin requires identical external_port and internal_port on the qualified VPP NAT44 ED path", id)
	}
	return mapping, nil
}

func compileWANAddresses(items []map[string]any) map[string]string {
	addresses := map[string]string{}
	for _, item := range items {
		id := stringValue(item, "id", "interface_id", "name")
		address := normalizeIPv4Address(stringValue(item, "external_address", "current_address", "address", "ip", "ip_cidr", "cidr"))
		if id != "" && address != "" {
			addresses[id] = address
		}
	}
	return addresses
}

func normalizeIPv4Address(value string) string {
	address := strings.TrimSpace(value)
	if address == "" || strings.Contains(address, ":") {
		return ""
	}
	if index := strings.Index(address, "/"); index > 0 {
		address = address[:index]
	}
	return address
}

func enabled(item map[string]any) bool {
	value, ok := item["enabled"]
	if !ok {
		return true
	}
	boolValue, ok := value.(bool)
	return !ok || boolValue
}

func boolValue(item map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, ok := item[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return typed
		case string:
			return strings.EqualFold(strings.TrimSpace(typed), "true") || strings.TrimSpace(typed) == "1"
		}
	}
	return false
}

func stringValue(item map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := item[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if trimmed := strings.TrimSpace(typed); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func intValue(item map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		value, ok := item[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case int:
			return typed, true
		case int64:
			return int(typed), true
		case float64:
			if typed == float64(int(typed)) {
				return int(typed), true
			}
		case jsonNumber:
			parsed, err := strconv.Atoi(string(typed))
			if err == nil {
				return parsed, true
			}
		case string:
			parsed, err := strconv.Atoi(strings.TrimSpace(typed))
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

type jsonNumber string

func splitHostPort(value string) (string, int, bool) {
	index := strings.LastIndex(value, ":")
	if index <= 0 || index == len(value)-1 {
		return "", 0, false
	}
	port, err := strconv.Atoi(value[index+1:])
	if err != nil {
		return "", 0, false
	}
	return strings.TrimSpace(value[:index]), port, true
}

func requireID(id, resourceType string) error {
	if id == "" {
		return fmt.Errorf("%s requires id", resourceType)
	}
	return requireCleanToken(id, resourceType, id, "id")
}

func requireToken(value, resourceType, id, field string) error {
	if value == "" {
		return fmt.Errorf("%s %q requires %s", resourceType, id, field)
	}
	return requireCleanToken(value, resourceType, id, field)
}

func optionalToken(value, resourceType, id, field string) error {
	if value == "" {
		return nil
	}
	return requireCleanToken(value, resourceType, id, field)
}

func requireCleanToken(value, resourceType, id, field string) error {
	if strings.ContainsAny(value, " \t\n\r|&;$`()<>\\\"") {
		return fmt.Errorf("%s %q %s contains unsupported command characters", resourceType, id, field)
	}
	return nil
}
