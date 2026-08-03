package vpp

import (
	"context"
	"fmt"
	"strings"
)

func (channel vppctlChannel) doManagementLCPLifecycle(ctx context.Context, operation Operation, management ManagementLCP) (Reply, error) {
	results, err := channel.runServiceChainCommands(ctx, operation, "show lcp")
	if err != nil {
		return Reply{}, err
	}
	for _, pair := range managementLCPPairs(resultStdout(results, "show lcp"), management.HostInterface) {
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("lcp delete %s", pair))
		if removeErr != nil {
			return Reply{}, removeErr
		}
		results = append(results, removed...)
	}
	if management.Enabled {
		applied, applyErr := channel.runServiceChainCommands(ctx, operation,
			fmt.Sprintf("lcp create %s host-if %s", management.VPPInterface, management.HostInterface),
			"lcp lcp-sync on", "show lcp")
		if applyErr != nil {
			return Reply{}, applyErr
		}
		results = append(results, applied...)
		if !managementLCPPresent(resultStdoutLast(results, "show lcp"), management.VPPInterface, management.HostInterface) {
			return Reply{}, snapshotDecodeError("shared management LCP pair %s/%s is absent from readback", management.VPPInterface, management.HostInterface)
		}
		return routePolicyLifecycleReply(operation, results), nil
	}
	verified, verifyErr := channel.runServiceChainCommands(ctx, operation, "show lcp")
	if verifyErr != nil {
		return Reply{}, verifyErr
	}
	results = append(results, verified...)
	if len(managementLCPPairs(resultStdoutLast(results, "show lcp"), management.HostInterface)) != 0 {
		return Reply{}, snapshotDecodeError("exclusive management left LCP host interface %s attached", management.HostInterface)
	}
	return routePolicyLifecycleReply(operation, results), nil
}

func managementLCPPairs(output, hostInterface string) []string {
	pairs := []string{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "itf-pair:" {
			continue
		}
		for _, field := range fields[2:] {
			if field == hostInterface {
				pairs = append(pairs, fields[2])
				break
			}
		}
	}
	return pairs
}

func managementLCPPresent(output, vppInterface, hostInterface string) bool {
	for _, pair := range managementLCPPairs(output, hostInterface) {
		if pair == vppInterface {
			return true
		}
	}
	return false
}
