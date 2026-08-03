package httpapi

import (
	"context"
	"fmt"
	"strings"

	serviceRuntime "ly-route/backend/internal/runtime/service"
)

func (server *Server) currentIPv6RAArtifacts(ctx context.Context) ([]serviceRuntime.RenderedArtifact, string) {
	wanLinks, err := server.desiredItems(ctx, "wan_link")
	if err != nil {
		return nil, fmt.Sprintf("IPv6 PD read failed: %v", err)
	}
	delegatedPrefix := ""
	for _, wan := range wanLinks {
		ipv6, _ := wan["ipv6"].(map[string]any)
		delegatedPrefix = firstNonEmpty(
			stringField(ipv6, "delegated_prefix"),
			stringField(ipv6, "prefix"),
			stringField(wan, "delegated_prefix"),
		)
		if delegatedPrefix != "" {
			break
		}
	}
	if delegatedPrefix == "" {
		return nil, ""
	}
	interfaces, err := server.desiredItems(ctx, "interface")
	if err != nil {
		return nil, fmt.Sprintf("IPv6 LAN read failed: %v", err)
	}
	lanInterface := ""
	for _, item := range interfaces {
		role := strings.ToLower(firstNonEmpty(stringField(item, "gateway_role"), stringField(item, "role")))
		if role == "lan" || role == "internal" {
			lanInterface = firstNonEmpty(stringField(item, "interface_id"), stringField(item, "id"))
			break
		}
	}
	if lanInterface == "" {
		return nil, "IPv6 delegated prefix has no LAN interface for RA"
	}
	artifacts, err := serviceRuntime.RenderIPv6RA(serviceRuntime.IPv6RAPlan{Interface: lanInterface, DelegatedPrefix: delegatedPrefix})
	if err != nil {
		return nil, err.Error()
	}
	return artifacts, ""
}
