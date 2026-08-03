package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func validatePPPoEReadback(ctx context.Context, runner CommandRunner, artifacts []RenderedArtifact) error {
	peerCount := 0
	for _, artifact := range artifacts {
		if !strings.HasPrefix(artifact.Path, "/etc/ly-route/pppoe/ly-route-") {
			continue
		}
		peerCount++
		var plan struct {
			ID         string `json:"id"`
			StatusFile string `json:"status_file"`
		}
		if err := json.Unmarshal([]byte(artifact.Content), &plan); err != nil || plan.ID == "" || plan.StatusFile == "" {
			return fmt.Errorf("PPPoE plan %s has no native status contract", artifact.Path)
		}
		if err := validateNativePPPoEReadback(ctx, runner, plan.ID, plan.StatusFile); err != nil {
			return err
		}
	}
	if peerCount == 0 {
		return fmt.Errorf("PPPoE artifacts contain no peer plans")
	}
	return nil
}

func validateNativePPPoEReadback(ctx context.Context, runner CommandRunner, peerID, statusFile string) error {
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := validateNativePPPoEOnce(ctx, runner, statusFile); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("PPPoE peer %s did not converge: %w", peerID, errors.Join(lastErr, ctx.Err()))
		case <-deadline.C:
			return fmt.Errorf("PPPoE peer %s did not converge within 15s: %w", peerID, lastErr)
		case <-ticker.C:
		}
	}
}

func validateNativePPPoEOnce(ctx context.Context, runner CommandRunner, statusFile string) error {
	output, err := requiredOutput(ctx, runner, "cat", statusFile)
	if err != nil {
		return err
	}
	var status struct {
		State     string `json:"state"`
		Interface string `json:"interface"`
		Session   struct {
			LocalAddress string `json:"local_address"`
		} `json:"session"`
	}
	if err := json.Unmarshal([]byte(output), &status); err != nil {
		return fmt.Errorf("decode native PPPoE status: %w", err)
	}
	if status.State != "connected" || status.Interface == "" || status.Session.LocalAddress == "" {
		return fmt.Errorf("native PPPoE status is not connected")
	}
	vpp, err := requiredOutput(ctx, runner, "vppctl", "show", "pppoe", "session")
	if err != nil {
		return err
	}
	if !strings.Contains(vpp, status.Session.LocalAddress) {
		return fmt.Errorf("VPP PPPoE readback does not match native client status")
	}
	addresses, err := requiredOutput(ctx, runner, "vppctl", "show", "interface", "address", status.Interface)
	if err != nil {
		return err
	}
	if !strings.Contains(addresses, status.Session.LocalAddress+"/32") {
		return fmt.Errorf("VPP PPPoE interface %s does not own negotiated address %s", status.Interface, status.Session.LocalAddress)
	}
	return nil
}

func validateIPv6RAReadback(ctx context.Context, runner CommandRunner, artifacts []RenderedArtifact) error {
	content, err := artifactContent(artifacts, "/etc/radvd.conf")
	if err != nil {
		return err
	}
	interfaceName, prefix, err := expectedIPv6RA(content)
	if err != nil {
		return err
	}
	output, err := requiredOutput(ctx, runner, "radvdump")
	if err != nil {
		return err
	}
	normalized := normalizedLine(output)
	if !strings.Contains(normalized, "interface "+interfaceName) || !strings.Contains(normalized, "prefix "+prefix) {
		return fmt.Errorf("IPv6 RA live state missing interface %s delegated prefix %s", interfaceName, prefix)
	}
	return nil
}

func expectedIPv6RA(content string) (string, string, error) {
	var interfaceName, prefix string
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "interface" {
			interfaceName = fields[1]
		}
		if len(fields) == 2 && fields[0] == "prefix" {
			prefix = fields[1]
		}
	}
	if interfaceName == "" || prefix == "" || !strings.HasSuffix(prefix, "/64") {
		return "", "", fmt.Errorf("rendered IPv6 RA plan lacks delegated /64")
	}
	return interfaceName, prefix, nil
}
