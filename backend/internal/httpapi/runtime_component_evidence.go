package httpapi

import (
	"context"
	"fmt"
	"strings"

	"ly-route/backend/internal/runtime/apply"
	serviceRuntime "ly-route/backend/internal/runtime/service"
)

func (server *Server) attachServiceEvidence(ctx context.Context, components []RuntimeComponentState, artifacts []serviceRuntime.RenderedArtifact) []RuntimeComponentState {
	evidence := server.runtimeEvidence()
	services := map[string]serviceRuntime.ServiceName{
		"smartdns":        serviceRuntime.SmartDNS,
		"kea":             serviceRuntime.Kea,
		"xray":            serviceRuntime.Xray,
		"pppoe":           serviceRuntime.PPPoE,
		"vpp":             serviceRuntime.VPP,
		"nftables_tproxy": serviceRuntime.Nftables,
		"linux_routing":   serviceRuntime.LinuxRouting,
		"ipv6-ra":         serviceRuntime.IPv6RA,
	}
	for index := range components {
		service, exists := services[components[index].Name]
		if !exists {
			continue
		}
		if components[index].Name == "vpp" && evidence.Status == RuntimeStatusCommitted && components[index].State == "running" && len(evidence.GatewayEvidence) > 0 {
			components[index].TransactionID = evidence.TransactionID
			components[index].ApplyReceipt = evidence.Receipt
			components[index].ApplyReceipt.Capability = "vpp"
			components[index].ReadbackAt = evidence.Readback.Timestamp
			components[index].Fresh = evidence.Readback.Fresh
			components[index].Capability = "vpp"
			continue
		}
		serviceArtifacts := artifactsForService(artifacts, service)
		if len(serviceArtifacts) == 0 {
			if evidence.Status == RuntimeStatusCommitted && components[index].Name == "vpp" && components[index].State == "running" {
				continue
			}
			if evidence.Status == RuntimeStatusCommitted {
				components[index].State = "not_configured"
				components[index].Available = false
				components[index].Reason = ""
			}
			components[index].ApplyReceipt.Status = apply.ReceiptMissing
			components[index].Fresh = false
			continue
		}
		request := RuntimeEvidenceRequest{TransactionID: evidence.TransactionID, Capability: components[index].Name, Artifacts: serviceArtifacts}
		receipt, receiptErr := server.runtimeReceipt(ctx, request)
		readback, readbackErr := server.runtimeReadback(ctx, request)
		if receiptErr != nil || readbackErr != nil {
			components[index].State = "degraded"
			components[index].Available = false
			components[index].Fresh = false
			components[index].Reason = strings.TrimSpace(fmt.Sprintf("%v %v", receiptErr, readbackErr))
			continue
		}
		components[index].TransactionID = evidence.TransactionID
		components[index].ApplyReceipt = receipt
		components[index].ReadbackAt = readback.Timestamp
		components[index].Fresh = readback.Fresh
	}
	return components
}
