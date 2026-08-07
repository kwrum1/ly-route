package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

var (
	aclInventoryHeader = regexp.MustCompile(`^acl-index\s+(\d+)\s+count\s+\d+\s+tag\s+\{([^}]*)\}`)
	aclLookupHit       = regexp.MustCompile(`\bacl\s+(\d+)\s+rule\s+(\d+).*\bhitcount\s+(\d+)\b`)
)

type vppctlPolicyCounters struct {
	store  gatewayTelemetryConfigReader
	binary string
	run    gatewayVPPCTLRunner
}

type observedACLHit struct {
	index    int
	tag      string
	hits     uint64
	ruleHits map[int]uint64
}

type desiredPolicyCounter struct {
	id        string
	operation string
}

func newVPPCTLPolicyCounters(store gatewayTelemetryConfigReader, binary string) *vppctlPolicyCounters {
	return &vppctlPolicyCounters{store: store, binary: strings.TrimSpace(binary), run: runVPPCTLTelemetryCommand}
}

func (collector *vppctlPolicyCounters) PolicyHits(ctx context.Context) ([]map[string]any, error) {
	if collector.store == nil {
		return nil, fmt.Errorf("policy counter configuration store is not configured")
	}
	if collector.run == nil {
		return nil, fmt.Errorf("policy counter VPP runner is not configured")
	}
	binary := collector.binary
	if binary == "" {
		binary = "vppctl"
	}
	inventory, err := collector.run(ctx, binary, "show", "acl-plugin", "acl")
	if err != nil {
		return nil, fmt.Errorf("read VPP ACL inventory: %w", err)
	}
	tables, err := collector.run(ctx, binary, "show", "acl-plugin", "tables")
	if err != nil {
		return nil, fmt.Errorf("read VPP ACL hit counters: %w", err)
	}
	observed := parseVPPACLHits(inventory, tables)
	desired, err := collector.desiredPolicies(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(desired))
	for _, policy := range desired {
		tag := "ly-route-" + policyTelemetrySafeTag(policy.id)
		item := map[string]any{
			"id": policy.id, "operation": policy.operation, "hits": uint64(0),
			"hit_source": "vpp_acl_lookup", "readback_state": "unavailable",
			"readback_reason": "configured policy ACL is not present in VPP",
		}
		if hit, found := observed[tag]; found {
			rules := make([]map[string]any, 0, len(hit.ruleHits))
			indexes := make([]int, 0, len(hit.ruleHits))
			for index := range hit.ruleHits {
				indexes = append(indexes, index)
			}
			sort.Ints(indexes)
			for _, index := range indexes {
				rules = append(rules, map[string]any{"rule": index, "hits": hit.ruleHits[index]})
			}
			item["hits"] = hit.hits
			item["acl_index"] = hit.index
			item["rule_hits"] = rules
			item["readback_state"] = "available"
			delete(item, "readback_reason")
		}
		items = append(items, item)
	}
	return items, nil
}

func (collector *vppctlPolicyCounters) desiredPolicies(ctx context.Context) ([]desiredPolicyCounter, error) {
	types := []struct {
		resourceType string
		operation    string
	}{
		{resourceType: "route_policy", operation: "vpp.route-policy"},
		{resourceType: "security_acl", operation: "vpp.security-acl"},
		{resourceType: "traffic_control", operation: "vpp.traffic-control"},
	}
	seen := map[string]struct{}{}
	items := []desiredPolicyCounter{}
	for _, policyType := range types {
		documents, err := collector.store.Configs(ctx, policyType.resourceType)
		if err != nil {
			return nil, fmt.Errorf("read %s policy configuration: %w", policyType.resourceType, err)
		}
		for _, document := range documents {
			var payload map[string]any
			if err := json.Unmarshal(document.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode %s policy %s: %w", policyType.resourceType, document.ResourceID, err)
			}
			if enabled, present := payload["enabled"].(bool); present && !enabled {
				continue
			}
			ids := []string{strings.TrimSpace(document.ResourceID)}
			if policyType.resourceType == "traffic_control" {
				ids = append(ids, nestedPolicyRuleIDs(payload)...)
			}
			for _, id := range ids {
				if id == "" {
					continue
				}
				key := policyType.operation + "\x00" + id
				if _, found := seen[key]; found {
					continue
				}
				seen[key] = struct{}{}
				items = append(items, desiredPolicyCounter{id: id, operation: policyType.operation})
			}
		}
	}
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].operation == items[right].operation {
			return items[left].id < items[right].id
		}
		return items[left].operation < items[right].operation
	})
	return items, nil
}

func nestedPolicyRuleIDs(value any) []string {
	ids := []string{}
	switch typed := value.(type) {
	case map[string]any:
		if id, _ := typed["id"].(string); strings.TrimSpace(id) != "" {
			ids = append(ids, strings.TrimSpace(id))
		}
		for key, child := range typed {
			if key == "id" {
				continue
			}
			ids = append(ids, nestedPolicyRuleIDs(child)...)
		}
	case []any:
		for _, child := range typed {
			ids = append(ids, nestedPolicyRuleIDs(child)...)
		}
	}
	return ids
}

func parseVPPACLHits(inventory, tables string) map[string]observedACLHit {
	byIndex := map[int]observedACLHit{}
	for _, raw := range strings.Split(inventory, "\n") {
		matches := aclInventoryHeader.FindStringSubmatch(strings.TrimSpace(raw))
		if len(matches) != 3 {
			continue
		}
		index, err := strconv.Atoi(matches[1])
		if err != nil {
			continue
		}
		byIndex[index] = observedACLHit{index: index, tag: strings.TrimSpace(matches[2]), ruleHits: map[int]uint64{}}
	}
	for _, raw := range strings.Split(tables, "\n") {
		matches := aclLookupHit.FindStringSubmatch(raw)
		if len(matches) != 4 {
			continue
		}
		index, indexErr := strconv.Atoi(matches[1])
		rule, ruleErr := strconv.Atoi(matches[2])
		hits, hitsErr := strconv.ParseUint(matches[3], 10, 64)
		observed, found := byIndex[index]
		if indexErr != nil || ruleErr != nil || hitsErr != nil || !found {
			continue
		}
		observed.hits += hits
		observed.ruleHits[rule] += hits
		byIndex[index] = observed
	}
	result := make(map[string]observedACLHit, len(byIndex))
	for _, observed := range byIndex {
		if observed.tag != "" {
			result[observed.tag] = observed
		}
	}
	return result
}

func policyTelemetrySafeTag(value string) string {
	var builder strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(character)
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte('_')
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		return "default"
	}
	return result
}
