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
}

type PortMapping struct {
	ID              string `json:"id"`
	Protocol        string `json:"protocol"`
	ExternalAddress string `json:"external_address,omitempty"`
	ExternalPort    int    `json:"external_port"`
	InternalHost    string `json:"internal_host"`
	InternalPort    int    `json:"internal_port"`
	WANInterface    string `json:"wan_interface,omitempty"`
	Hairpin         bool   `json:"hairpin,omitempty"`
}

type CompiledConfig struct {
	StaticMappings []StaticMapping `json:"static_mappings,omitempty"`
	PortMappings   []PortMapping   `json:"port_mappings,omitempty"`
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
		staticMappings = append(staticMappings, mapping)
	}

	portMappings := make([]PortMapping, 0, len(portMapItems))
	for _, item := range portMapItems {
		if !enabled(item) {
			continue
		}
		mapping, err := compilePortMapping(item, wanAddresses)
		if err != nil {
			return CompiledConfig{}, err
		}
		portMappings = append(portMappings, mapping)
	}

	return CompiledConfig{StaticMappings: staticMappings, PortMappings: portMappings}, nil
}

func compileStaticMapping(item map[string]any) (StaticMapping, error) {
	id := stringValue(item, "id", "name")
	mapping := StaticMapping{
		ID:              id,
		ExternalAddress: stringValue(item, "external_address", "public_ip", "wan_ip", "external_ip"),
		InternalAddress: stringValue(item, "internal_address", "internal_host", "private_ip", "inside_address"),
		WANInterface:    stringValue(item, "wan_interface", "wan_link", "interface_id", "egress"),
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
	}
	if mapping.Protocol == "" {
		mapping.Protocol = "tcp"
	}
	if mapping.ExternalAddress == "" && mapping.WANInterface != "" {
		mapping.ExternalAddress = wanAddresses[mapping.WANInterface]
	}
	if err := requireID(id, "port_map"); err != nil {
		return PortMapping{}, err
	}
	if mapping.ExternalAddress == "" {
		return PortMapping{}, fmt.Errorf("port_map %q requires external_address until WAN acquired address resolution is implemented", id)
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
