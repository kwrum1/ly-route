package vpp

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// CleanupManagedTAPs removes duplicate TAPs and orphaned Ly Route service
// handoffs that are absent from both the persisted and desired plans. Keeping
// both plans protects an active ABF path until its normal route lifecycle has
// completed, while preventing deleted DNS or proxy services from accumulating
// in VPP across configuration transactions.
func (a Adapter) CleanupManagedTAPs(ctx context.Context, plans ...Plan) error {
	if a.Client == nil {
		return nil
	}
	channel, err := a.Client.OpenChannel(ctx)
	if err != nil {
		return fmt.Errorf("open TAP cleanup channel: %w", err)
	}
	defer channel.Close()
	requestID := ""
	targets := make(map[string]struct{})
	for _, plan := range plans {
		if requestID == "" {
			requestID = strings.TrimSpace(plan.RequestID)
		}
		for name := range managedTAPNames(plan) {
			targets[name] = struct{}{}
		}
	}
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
	records := make([]managedTAPRecord, 0)
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
		records = append(records, managedTAPRecord{index: index, matches: matches})
	}
	for _, index := range managedTAPDeleteIndices(records, targets) {
		_, deleteErr := channel.Do(ctx, Operation{
			Name:           "vpp.tap.cleanup.delete",
			RequestID:      requestID,
			Resource:       strconv.Itoa(index),
			VPPCtlCommands: []string{fmt.Sprintf("delete tap sw_if_index %d", index)},
		})
		if deleteErr != nil {
			return fmt.Errorf("remove stale managed TAP sw_if_index %d: %w", index, deleteErr)
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
	// Route policies can reference a proxy service chain directly even when
	// the compiled proxy steering list is not persisted in the gateway plan.
	// Preserve those TAPs during reconciliation; deleting the ingress TAP here
	// makes the subsequent ABF next-hop command fail with VPP's misleading
	// "unknown input"/Invalid policy response.
	for _, route := range plan.Policy.RoutePolicies {
		if route.Path != nil {
			add(route.Path.VPPInterface)
		}
	}
	for _, group := range plan.Policy.WANGroups {
		for _, path := range group.Paths {
			add(path.VPPInterface)
		}
	}
	return targets
}

type managedTAPRecord struct {
	index   int
	matches []string
}

func managedTAPDeleteIndices(records []managedTAPRecord, targets map[string]struct{}) []int {
	sort.Slice(records, func(i, j int) bool { return records[i].index < records[j].index })
	seen := make(map[string]struct{})
	deletes := make([]int, 0)
	for _, record := range records {
		keep := false
		for _, name := range record.matches {
			if _, wanted := targets[name]; !wanted {
				continue
			}
			if _, exists := seen[name]; !exists {
				seen[name] = struct{}{}
				keep = true
			}
		}
		if !keep {
			deletes = append(deletes, record.index)
		}
	}
	return deletes
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
