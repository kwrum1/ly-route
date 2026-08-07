package vpp

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// CleanupManagedTAPs removes only extra copies of Ly Route's DNS/proxy TAPs.
// It runs before a gateway snapshot so an interrupted replay cannot make a
// valid configuration unreadable through duplicate interface names. One
// existing copy is deliberately retained; the normal apply replay can then
// refresh it without causing a needless dataplane outage.
func (a Adapter) CleanupManagedTAPs(ctx context.Context, plan Plan) error {
	targets := managedTAPNames(plan)
	if a.Client == nil {
		return nil
	}
	channel, err := a.Client.OpenChannel(ctx)
	if err != nil {
		return fmt.Errorf("open TAP cleanup channel: %w", err)
	}
	defer channel.Close()
	requestID := strings.TrimSpace(plan.RequestID)
	reply, err := channel.Do(ctx, Operation{
		Name:           "vpp.tap.cleanup",
		RequestID:      requestID,
		Resource:       "managed",
		VPPCtlCommands: []string{"show tap"},
	})
	if err != nil {
		return fmt.Errorf("read VPP TAP inventory: %w", err)
	}
	payload, ok := reply.Payload.(VPPCTLReplyPayload)
	if !ok {
		return fmt.Errorf("read VPP TAP inventory returned payload %T", reply.Payload)
	}
	output := ""
	for _, result := range payload.CommandResults {
		if result.Command == "show tap" {
			if result.Retval != 0 {
				return fmt.Errorf("show tap returned VPP retval %d", result.Retval)
			}
			output = result.Stdout
			break
		}
	}
	if strings.TrimSpace(output) == "" {
		return nil
	}
	type tapRecord struct {
		index   int
		matches []string
	}
	records := make([]tapRecord, 0)
	for _, block := range tapInventoryBlocks(output) {
		index, names, ok := parseTAPInventoryBlock(block)
		if !ok {
			continue
		}
		matches := make([]string, 0)
		for name := range names {
			if isManagedServiceTAPName(name) {
				matches = append(matches, name)
			}
		}
		if len(matches) == 0 {
			continue
		}
		sort.Strings(matches)
		records = append(records, tapRecord{index: index, matches: matches})
	}
	// Keep one object only for names still present in the desired plan. Any
	// Ly Route DNS/proxy TAP that is no longer desired is stale and is removed,
	// even when it is the sole remaining copy.
	keep := make(map[int]bool)
	for target := range targets {
		indices := make([]int, 0)
		for _, record := range records {
			if containsTAPName(record.matches, target) {
				indices = append(indices, record.index)
			}
		}
		if len(indices) > 0 {
			sort.Ints(indices)
			keep[indices[0]] = true
		}
	}
	for _, record := range records {
		if keep[record.index] {
			continue
		}
		_, deleteErr := channel.Do(ctx, Operation{
			Name:           "vpp.tap.cleanup.delete",
			RequestID:      requestID,
			Resource:       strconv.Itoa(record.index),
			VPPCtlCommands: []string{fmt.Sprintf("delete tap sw_if_index %d", record.index)},
		})
		if deleteErr != nil {
			return fmt.Errorf("remove stale TAP sw_if_index %d: %w", record.index, deleteErr)
		}
	}
	return nil
}

func isManagedServiceTAPName(name string) bool {
	name = strings.TrimSpace(name)
	for _, prefix := range []string{"lydns", "lydnsh", "lypxin", "lypxhin", "lypxout", "lypxhout"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func managedTAPNames(plan Plan) map[string]struct{} {
	targets := make(map[string]struct{})
	add := func(values ...string) {
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				targets[value] = struct{}{}
			}
		}
	}
	for _, network := range plan.DNSServiceNetworks {
		add(network.VPPInterface, network.HostInterface)
	}
	for _, steering := range plan.Proxy.VPPSteering {
		network := steering.ServiceNetwork
		add(network.IngressVPPInterface, network.IngressHostInterface,
			network.EgressVPPInterface, network.EgressHostInterface)
	}
	return targets
}

var tapInventoryBlockPattern = regexp.MustCompile(`(?m)^Interface:\s`)
var tapInventoryHeaderPattern = regexp.MustCompile(`(?m)^Interface:\s+(\S+)\s+\(ifindex\s+(\d+)\)`)
var tapInventoryNamePattern = regexp.MustCompile(`(?m)^\s*name\s+"([^"]+)"`)

func tapInventoryBlocks(output string) []string {
	locations := tapInventoryBlockPattern.FindAllStringIndex(output, -1)
	blocks := make([]string, 0, len(locations))
	for index, location := range locations {
		end := len(output)
		if index+1 < len(locations) {
			end = locations[index+1][0]
		}
		blocks = append(blocks, output[location[0]:end])
	}
	return blocks
}

func parseTAPInventoryBlock(block string) (int, map[string]struct{}, bool) {
	header := tapInventoryHeaderPattern.FindStringSubmatch(block)
	if len(header) != 3 {
		return 0, nil, false
	}
	index, err := strconv.Atoi(header[2])
	if err != nil {
		return 0, nil, false
	}
	names := map[string]struct{}{header[1]: {}}
	for _, match := range tapInventoryNamePattern.FindAllStringSubmatch(block, -1) {
		if len(match) == 2 {
			names[match[1]] = struct{}{}
		}
	}
	return index, names, true
}

func containsTAPName(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
