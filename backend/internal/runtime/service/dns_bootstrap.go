package service

import "strings"

// DNSBootstrapProfile selects the resolver IPs SmartDNS uses to resolve a
// hostname-based DoH endpoint before the endpoint itself can be reached.
// These are ordinary IP DNS servers and are only used for bootstrap lookup;
// policy traffic still uses the selected DoH upstream and WAN.
type DNSBootstrapProfile string

const (
	DNSBootstrapDomestic DNSBootstrapProfile = "domestic"
	DNSBootstrapForeign  DNSBootstrapProfile = "foreign"
)

type DNSBootstrapDefaults struct {
	Profile          DNSBootstrapProfile
	BootstrapServers []string
}

var dnsBootstrapDefaults = map[DNSBootstrapProfile]DNSBootstrapDefaults{
	DNSBootstrapDomestic: {
		Profile:          DNSBootstrapDomestic,
		BootstrapServers: []string{"223.5.5.5", "223.6.6.6", "119.29.29.29", "182.254.116.116", "180.76.76.76"},
	},
	DNSBootstrapForeign: {
		Profile:          DNSBootstrapForeign,
		BootstrapServers: []string{"1.1.1.1", "1.0.0.1", "8.8.8.8", "8.8.4.4", "9.9.9.9"},
	},
}

func BuiltinDNSBootstrap(profile string) DNSBootstrapDefaults {
	selected := DNSBootstrapForeign
	if strings.EqualFold(strings.TrimSpace(profile), string(DNSBootstrapDomestic)) {
		selected = DNSBootstrapDomestic
	}
	defaults := dnsBootstrapDefaults[selected]
	defaults.BootstrapServers = append([]string(nil), defaults.BootstrapServers...)
	return defaults
}

func IsDoHServer(server string) bool {
	lower := strings.ToLower(strings.TrimSpace(server))
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "h3://")
}
