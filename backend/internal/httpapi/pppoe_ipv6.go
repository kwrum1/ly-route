package httpapi

import (
	"context"
	"strings"
)

func (server *Server) pppoeIPv6LANConfig(ctx context.Context, wanID string) (string, []string, error) {
	wanID = strings.TrimSpace(wanID)
	if wanID == "" {
		return "", nil, nil
	}
	interfaces, err := server.desiredItems(ctx, "interface")
	if err != nil {
		return "", nil, err
	}
	lanInterfaces := []string{}
	for _, item := range interfaces {
		ipv6, _ := item["ipv6"].(map[string]any)
		if !strings.EqualFold(stringField(ipv6, "mode"), "delegated_prefix") || stringField(ipv6, "source_wan_id") != wanID {
			continue
		}
		interfaceID := server.resolveInterfaceID(ctx, firstNonEmpty(stringField(item, "interface_id"), stringField(item, "id")))
		if interfaceID == "" {
			continue
		}
		if !strings.HasPrefix(interfaceID, "lyroute-") {
			interfaceID = "lyroute-" + interfaceID
		}
		lanInterfaces = appendUniqueString(lanInterfaces, interfaceID)
	}
	if len(lanInterfaces) == 0 {
		return "", nil, nil
	}
	return "ly-route-" + wanID, lanInterfaces, nil
}
