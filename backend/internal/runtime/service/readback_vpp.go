package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ly-route/backend/internal/runtime/vpp"
)

const vppApplyReceiptPath = "/var/lib/ly-route/vpp-apply-receipt.json"

type vppApplyReadback struct {
	Status     string                 `json:"status"`
	Operations []vppOperationReadback `json:"operations"`
}

type vppOperationReadback struct {
	Name     string               `json:"name"`
	Resource string               `json:"resource"`
	Results  []vppCommandReadback `json:"results"`
}

type vppCommandReadback struct {
	Command string `json:"command"`
	Status  string `json:"status"`
}

func validateVPPReadback(ctx context.Context, runner CommandRunner, artifacts []RenderedArtifact) error {
	if _, err := requiredOutput(ctx, runner, "vppctl", "show", "version"); err != nil {
		return err
	}
	content, err := artifactContent(artifacts, "/var/lib/ly-route/vpp/operations.json")
	if err != nil {
		return err
	}
	var expected struct {
		Operations []vpp.Operation `json:"operations"`
	}
	if err := json.Unmarshal([]byte(content), &expected); err != nil {
		return fmt.Errorf("decode expected VPP operations: %w", err)
	}
	if len(expected.Operations) == 0 {
		return fmt.Errorf("VPP readback requires expected applied objects")
	}
	receiptOutput, err := requiredOutput(ctx, runner, "cat", vppApplyReceiptPath)
	if err != nil {
		return err
	}
	var receipt vppApplyReadback
	if err := json.Unmarshal([]byte(receiptOutput), &receipt); err != nil {
		return fmt.Errorf("decode VPP live apply receipt: %w", err)
	}
	if receipt.Status != "applied" {
		return fmt.Errorf("VPP live apply status = %q, want applied", receipt.Status)
	}
	for _, operation := range expected.Operations {
		if err := requireVPPOperation(operation, receipt.Operations); err != nil {
			return err
		}
	}
	return nil
}

func requireVPPOperation(expected vpp.Operation, actual []vppOperationReadback) error {
	for _, operation := range actual {
		if operation.Name != expected.Name || operation.Resource != expected.Resource {
			continue
		}
		if len(operation.Results) == 0 {
			return fmt.Errorf("VPP object %s/%s has no applied command results", expected.Name, expected.Resource)
		}
		for _, command := range expected.VPPCtlCommands {
			optional := strings.HasPrefix(strings.TrimSpace(command), "?")
			command = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(command), "?"))
			if !hasAppliedVPPCommand(operation.Results, command, optional) {
				return fmt.Errorf("VPP object %s/%s command %q is not applied", expected.Name, expected.Resource, command)
			}
		}
		return nil
	}
	return fmt.Errorf("VPP expected applied object %s/%s is missing", expected.Name, expected.Resource)
}

func hasAppliedVPPCommand(results []vppCommandReadback, command string, optional bool) bool {
	for _, result := range results {
		if result.Command != command {
			continue
		}
		return result.Status == "applied" || optional && result.Status == "ignored-failure"
	}
	return false
}
