package service

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
)

const DefaultXrayRoutingAPIAddress = "127.0.0.1:10085"

type XrayBalancerState struct {
	Tag                  string   `json:"tag"`
	SelectedOutboundTags []string `json:"selected_outbound_tags"`
}

type XrayRoutingStateController interface {
	XrayBalancerStates(context.Context, []string) ([]XrayBalancerState, error)
}

func (controller FilesystemController) XrayBalancerStates(ctx context.Context, tags []string) ([]XrayBalancerState, error) {
	if controller.Runner == nil {
		return nil, fmt.Errorf("Xray balancer readback requires a command runner")
	}
	address, err := loopbackXrayAPIAddress(controller.XrayAPIAddress)
	if err != nil {
		return nil, err
	}
	states := make([]XrayBalancerState, 0, len(tags))
	seen := map[string]bool{}
	for _, rawTag := range tags {
		tag := strings.TrimSpace(rawTag)
		if tag == "" || !serviceTokenSafe(tag) {
			return nil, fmt.Errorf("Xray balancer tag %q is unsafe", tag)
		}
		if seen[tag] {
			continue
		}
		seen[tag] = true
		output, err := controller.Runner.Output(ctx, "xray", "api", "bi", "--server="+address, tag)
		if err != nil {
			return nil, fmt.Errorf("read Xray balancer %s: %w", tag, err)
		}
		selected := parseXrayBalancerSelections(output)
		if len(selected) == 0 {
			return nil, fmt.Errorf("Xray balancer %s has no healthy selected outbound", tag)
		}
		states = append(states, XrayBalancerState{Tag: tag, SelectedOutboundTags: selected})
	}
	return states, nil
}

func loopbackXrayAPIAddress(raw string) (string, error) {
	address := strings.TrimSpace(raw)
	if address == "" {
		address = DefaultXrayRoutingAPIAddress
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("Xray routing API address is invalid: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("Xray routing API must use a numeric loopback address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("Xray routing API port is invalid")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(port)), nil
}

func parseXrayBalancerSelections(output string) []string {
	selects := false
	result := make([]string, 0)
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "- Selects:" {
			selects = true
			continue
		}
		if strings.HasPrefix(line, "-") {
			selects = false
			continue
		}
		if !selects || line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if _, err := strconv.Atoi(fields[0]); err != nil || !serviceTokenSafe(fields[1]) || seen[fields[1]] {
			continue
		}
		seen[fields[1]] = true
		result = append(result, fields[1])
	}
	return result
}
