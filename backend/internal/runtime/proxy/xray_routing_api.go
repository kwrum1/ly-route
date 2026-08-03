package proxy

import "fmt"

const (
	XrayRoutingAPIAddress = "127.0.0.1:10085"
	XrayRoutingAPITag     = "ly-route-routing-api"
	XrayRoutingAPIInbound = "ly-route-routing-api-in"
)

// EnableXrayRoutingAPI adds a loopback-only RoutingService endpoint used to
// read the balancer's actual selection and health. It is never exposed on LAN.
func EnableXrayRoutingAPI(payload *XrayConfigPayload) error {
	if payload == nil || payload.Routing == nil {
		return fmt.Errorf("xray routing API requires a routing configuration")
	}
	for _, inbound := range payload.Inbounds {
		if inbound.Tag == XrayRoutingAPIInbound || inbound.Listen == "127.0.0.1" && inbound.Port == 10085 {
			return fmt.Errorf("xray routing API endpoint conflicts with inbound %q", inbound.Tag)
		}
	}
	payload.API = &XrayAPI{Tag: XrayRoutingAPITag, Services: []string{"RoutingService"}}
	payload.Inbounds = append(payload.Inbounds, XrayInbound{
		Tag:      XrayRoutingAPIInbound,
		Listen:   "127.0.0.1",
		Port:     10085,
		Protocol: "dokodemo-door",
		Settings: XrayDokodemoSettings{Address: "127.0.0.1", Network: "tcp"},
	})
	payload.Routing.Rules = append([]XrayRoutingRule{{Type: "field", InboundTags: []string{XrayRoutingAPIInbound}, OutboundTag: XrayRoutingAPITag}}, payload.Routing.Rules...)
	return nil
}
