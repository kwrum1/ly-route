package product

type Service string

const (
	ServiceControlAPI   Service = "control-api"
	ServiceVPP          Service = "vpp"
	ServiceSmartDNS     Service = "smartdns"
	ServiceDNSVPPProxy  Service = "dns-vpp-proxy"
	ServiceKea          Service = "kea"
	ServiceXray         Service = "xray"
	ServicePPPoE        Service = "pppoe"
	ServiceNftables     Service = "nftables"
	ServiceLinuxRouting Service = "linux-routing"
	serviceCount                = 9
)

var allServices = [serviceCount]Service{
	ServiceControlAPI,
	ServiceVPP,
	ServiceSmartDNS,
	ServiceDNSVPPProxy,
	ServiceKea,
	ServiceXray,
	ServicePPPoE,
	ServiceNftables,
	ServiceLinuxRouting,
}

func lookupServiceIndex(service Service) (int, bool) {
	switch service {
	case ServiceControlAPI:
		return 0, true
	case ServiceVPP:
		return 1, true
	case ServiceSmartDNS:
		return 2, true
	case ServiceDNSVPPProxy:
		return 3, true
	case ServiceKea:
		return 4, true
	case ServiceXray:
		return 5, true
	case ServicePPPoE:
		return 6, true
	case ServiceNftables:
		return 7, true
	case ServiceLinuxRouting:
		return 8, true
	default:
		return 0, false
	}
}
