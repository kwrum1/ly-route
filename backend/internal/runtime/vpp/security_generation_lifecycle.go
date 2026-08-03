package vpp

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var securityGenerationACLHeader = regexp.MustCompile(`^acl-index\s+(\d+)\s+count\s+\d+\s+tag\s+\{([^}]*)\}`)
var securityGenerationMACIPIndex = regexp.MustCompile(`(?i)(?:acl[_-]index|index)\s*:?\s*(\d+)`)
var securityGenerationACLRule = regexp.MustCompile(`^\d+:\s+ipv[46]\s+(.+)$`)
var securityGenerationMACIPRule = regexp.MustCompile(`^rule\s+\d+:\s+ipv4\s+action\s+([01])\s+(.+)$`)

func (channel vppctlChannel) doSecurityGenerationLifecycle(ctx context.Context, operation Operation, generation SecurityGeneration, deleting bool) (Reply, error) {
	if !deleting {
		preflight, err := channel.runServiceChainCommands(ctx, operation, "show interface")
		if err != nil {
			return Reply{}, err
		}
		if err := validateSecurityGenerationInterfaces(generation, resultStdoutLast(preflight, "show interface")); err != nil {
			return Reply{}, err
		}
	}
	removed, err := channel.removeSecurityGenerationState(ctx, operation, generation)
	if err != nil || deleting {
		return routePolicyLifecycleReply(operation, removed), err
	}
	results := append([]VPPCTLCommandResult(nil), removed...)
	for _, group := range generation.ACLs {
		rules, err := securityInterfaceACLRules(group.Rules)
		if err != nil {
			return Reply{}, err
		}
		tag := "ly-route-security-gen-" + safeTag(group.Interface+"-"+group.Direction)
		created, err := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("set acl-plugin acl %s tag %s", strings.Join(rules, ", "), tag))
		if err != nil {
			return Reply{}, err
		}
		aclID, err := allocatedServiceChainACLID(created)
		if err != nil {
			return Reply{}, err
		}
		attached, err := channel.runServiceChainCommands(ctx, operation,
			fmt.Sprintf("set acl-plugin interface %s %s acl %d", group.Interface, group.Direction, aclID),
			fmt.Sprintf("show acl-plugin acl index %d", aclID), "show acl-plugin interface", "show interface")
		if err != nil {
			return Reply{}, err
		}
		results = append(results, created...)
		results = append(results, attached...)
		if err := verifySecurityInterfaceACL(group, aclID, results); err != nil {
			return Reply{}, err
		}
	}
	for _, group := range generation.MACIP {
		rules, err := securityMACIPRules(group)
		if err != nil {
			return Reply{}, err
		}
		tag := "ly-route-security-macip-" + safeTag(group.Interface)
		created, err := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("set acl-plugin macip acl %s tag %s", strings.Join(rules, ", "), tag))
		if err != nil {
			return Reply{}, err
		}
		aclID, err := allocatedSecurityMACIPACLID(created)
		if err != nil {
			return Reply{}, err
		}
		attached, err := channel.runServiceChainCommands(ctx, operation,
			fmt.Sprintf("set acl-plugin macip interface %s acl %d", group.Interface, aclID),
			"show acl-plugin macip acl", "show acl-plugin macip interface", "show interface")
		if err != nil {
			return Reply{}, err
		}
		results = append(results, created...)
		results = append(results, attached...)
		if err := verifySecurityMACIP(group, aclID, results); err != nil {
			return Reply{}, err
		}
	}
	attackCommands, err := securityAttackCommands(generation.AttackRules)
	if err != nil {
		return Reply{}, err
	}
	if len(attackCommands) > 0 {
		created, applyErr := channel.runServiceChainCommands(ctx, operation, attackCommands...)
		if applyErr != nil {
			return Reply{}, applyErr
		}
		results = append(results, created...)
	}
	final, err := channel.runServiceChainCommands(ctx, operation, "show acl-plugin acl", "show acl-plugin interface", "show acl-plugin macip acl", "show acl-plugin macip interface", "show policer", "show ly-route security-guard")
	if err != nil {
		return Reply{}, err
	}
	results = append(results, final...)
	if err := verifySecurityGenerationReadback(generation, results); err != nil {
		return Reply{}, err
	}
	if err := verifySecurityGuardReadback(generation.AttackRules, results); err != nil {
		return Reply{}, err
	}
	return routePolicyLifecycleReply(operation, results), nil
}

func validateSecurityGenerationInterfaces(generation SecurityGeneration, output string) error {
	available := map[string]struct{}{}
	for _, name := range securityGenerationInterfaceNames(output) {
		available[name] = struct{}{}
	}
	interfaces := []string{}
	for _, group := range generation.ACLs {
		interfaces = append(interfaces, group.Interface)
	}
	for _, group := range generation.MACIP {
		interfaces = append(interfaces, group.Interface)
	}
	for _, attack := range generation.AttackRules {
		interfaces = append(interfaces, attack.Interface)
	}
	for _, name := range interfaces {
		if _, found := available[name]; !found {
			return snapshotDecodeError("security generation interface %q is not present in VPP", name)
		}
	}
	return nil
}

func (channel vppctlChannel) removeSecurityGenerationState(ctx context.Context, operation Operation, generation SecurityGeneration) ([]VPPCTLCommandResult, error) {
	results, err := channel.runServiceChainCommands(ctx, operation, "show interface", "show acl-plugin acl", "show acl-plugin interface", "show acl-plugin macip acl", "show acl-plugin macip interface", "show policer", "show ly-route security-guard")
	if err != nil {
		return nil, err
	}
	interfaces := securityGenerationInterfaceNames(resultStdout(results, "show interface"))
	ownedACLs := securityGenerationACLIDs(resultStdout(results, "show acl-plugin acl"))
	ownedMACIP := securityGenerationMACIPIDs(resultStdout(results, "show acl-plugin macip acl"))
	if (len(ownedACLs) > 0 || len(ownedMACIP) > 0) && len(interfaces) == 0 {
		return nil, snapshotDecodeError("security generation cleanup cannot map VPP interfaces")
	}
	for _, item := range ownedACLs {
		for _, interfaceName := range interfaces {
			for _, direction := range []string{"input", "output"} {
				removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("?set acl-plugin interface %s %s acl %d del", interfaceName, direction, item.ID))
				if removeErr != nil {
					return nil, removeErr
				}
				results = append(results, removed...)
			}
		}
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("?delete acl-plugin acl index %d", item.ID))
		if removeErr != nil {
			return nil, removeErr
		}
		results = append(results, removed...)
	}
	for _, item := range ownedMACIP {
		for _, interfaceName := range interfaces {
			removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("?set acl-plugin macip interface %s acl %d del", interfaceName, item))
			if removeErr != nil {
				return nil, removeErr
			}
			results = append(results, removed...)
		}
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("?delete acl-plugin macip acl index %d", item))
		if removeErr != nil {
			return nil, removeErr
		}
		results = append(results, removed...)
	}
	for _, name := range securityGenerationPolicerNames(resultStdout(results, "show policer")) {
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("?policer del name %s", name))
		if removeErr != nil {
			return nil, removeErr
		}
		results = append(results, removed...)
	}
	for _, id := range securityGenerationGuardIDs(resultStdout(results, "show ly-route security-guard")) {
		removed, removeErr := channel.runServiceChainCommands(ctx, operation, fmt.Sprintf("delete ly-route security-guard rule %s", id))
		if removeErr != nil {
			return nil, removeErr
		}
		results = append(results, removed...)
	}
	verified, err := channel.runServiceChainCommands(ctx, operation, "show acl-plugin acl", "show acl-plugin macip acl", "show policer", "show ly-route security-guard")
	if err != nil {
		return nil, err
	}
	results = append(results, verified...)
	if len(securityGenerationACLIDs(resultStdoutLast(results, "show acl-plugin acl"))) > 0 || len(securityGenerationMACIPIDs(resultStdoutLast(results, "show acl-plugin macip acl"))) > 0 || len(securityGenerationPolicerNames(resultStdoutLast(results, "show policer"))) > 0 || len(securityGenerationGuardIDs(resultStdoutLast(results, "show ly-route security-guard"))) > 0 {
		return nil, snapshotDecodeError("security generation remains after cleanup")
	}
	return results, nil
}

func securityGenerationInterfaceNames(output string) []string {
	result := []string{}
	seen := map[string]struct{}{}
	for _, line := range nonBlankLines(output) {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if _, err := strconv.Atoi(fields[1]); err != nil {
			continue
		}
		if _, duplicate := seen[fields[0]]; duplicate {
			continue
		}
		seen[fields[0]] = struct{}{}
		result = append(result, fields[0])
	}
	sort.Strings(result)
	return result
}

type securityGenerationACLIdentity struct{ ID, InterfaceIndex int }

func securityGenerationACLIDs(output string) []securityGenerationACLIdentity {
	identities := []securityGenerationACLIdentity{}
	lines := nonBlankLines(output)
	for index, line := range lines {
		match := securityGenerationACLHeader.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) != 3 || !strings.HasPrefix(match[2], "ly-route-security-gen-") {
			continue
		}
		aclID, _ := strconv.Atoi(match[1])
		interfaceIndex := 0
		for _, next := range lines[index+1:] {
			if strings.HasPrefix(strings.TrimSpace(next), "acl-index ") {
				break
			}
			if fields := strings.Fields(next); len(fields) > 0 && strings.Contains(next, "applied") {
				for _, field := range fields {
					if parsed, err := strconv.Atoi(strings.TrimSuffix(field, ":")); err == nil {
						interfaceIndex = parsed
						break
					}
				}
			}
		}
		identities = append(identities, securityGenerationACLIdentity{ID: aclID, InterfaceIndex: interfaceIndex})
	}
	return identities
}

func securityGenerationMACIPIDs(output string) []int {
	ids := []int{}
	for _, line := range nonBlankLines(output) {
		if !strings.Contains(strings.ToLower(line), "ly-route-security-macip-") {
			continue
		}
		if match := securityGenerationMACIPIndex.FindStringSubmatch(line); len(match) == 2 {
			id, _ := strconv.Atoi(match[1])
			ids = append(ids, id)
		}
	}
	return ids
}

func securityGenerationPolicerNames(output string) []string {
	result := []string{}
	for _, line := range nonBlankLines(output) {
		for _, field := range strings.Fields(line) {
			if strings.HasPrefix(field, "ly_route_security_") {
				result = append(result, strings.Trim(field, ":"))
			}
		}
	}
	return result
}

func securityGenerationGuardIDs(output string) []string {
	ids := []string{}
	for _, line := range nonBlankLines(output) {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "rule" && strings.HasPrefix(fields[1], "ly-route-security-attack-") {
			ids = append(ids, fields[1])
		}
	}
	sort.Strings(ids)
	return ids
}

func allocatedSecurityMACIPACLID(results []VPPCTLCommandResult) (int, error) {
	for _, result := range results {
		if match := securityGenerationMACIPIndex.FindStringSubmatch(result.Stdout); len(match) == 2 {
			id, err := strconv.Atoi(match[1])
			if err == nil {
				return id, nil
			}
		}
	}
	return 0, fmt.Errorf("MACIP ACL creation did not return an ACL index")
}

func verifySecurityInterfaceACL(group SecurityInterfaceACL, id int, results []VPPCTLCommandResult) error {
	output, err := commandOutput(results, fmt.Sprintf("show acl-plugin acl index %d", id))
	if err != nil {
		return err
	}
	tag := "ly-route-security-gen-" + safeTag(group.Interface+"-"+group.Direction)
	lines := nonBlankLines(output)
	if len(lines) == 0 {
		return snapshotDecodeError("security ACL generation %q readback is empty", group.Interface)
	}
	header := securityGenerationACLHeader.FindStringSubmatch(strings.TrimSpace(lines[0]))
	if len(header) != 3 || header[1] != strconv.Itoa(id) || header[2] != tag {
		return snapshotDecodeError("security ACL generation %q was not read back", group.Interface)
	}
	expected, err := securityInterfaceACLRules(group.Rules)
	if err != nil {
		return err
	}
	actual := []string{}
	for _, line := range nonBlankLines(output) {
		if match := securityGenerationACLRule.FindStringSubmatch(strings.TrimSpace(line)); len(match) == 2 {
			actual = append(actual, match[1])
		}
	}
	if strings.Join(actual, "\n") != strings.Join(expected, "\n") {
		return snapshotDecodeError("security ACL generation %q rules do not match desired order", group.Interface)
	}
	if !securityACLInterfaceAttached(resultStdoutLast(results, "show acl-plugin interface"), resultStdoutLast(results, "show interface"), group.Interface, group.Direction, id) {
		return snapshotDecodeError("security ACL generation %q is not attached %s", group.Interface, group.Direction)
	}
	return nil
}

func verifySecurityMACIP(group SecurityMACIPACL, id int, results []VPPCTLCommandResult) error {
	tag := "ly-route-security-macip-" + safeTag(group.Interface)
	block, blockID, found, err := taggedSecurityMACIPOutput(resultStdoutLast(results, "show acl-plugin macip acl"), tag)
	if err != nil || !found || blockID != id {
		return snapshotDecodeError("security MACIP generation %q ACL %d was not read back", group.Interface, id)
	}
	expected, err := securityMACIPRules(group)
	if err != nil {
		return err
	}
	actual := []string{}
	for _, line := range nonBlankLines(block) {
		if match := securityGenerationMACIPRule.FindStringSubmatch(strings.TrimSpace(line)); len(match) == 3 {
			action := "deny"
			if match[1] == "1" {
				action = "permit"
			}
			actual = append(actual, action+" "+match[2])
		}
	}
	if strings.Join(actual, "\n") != strings.Join(expected, "\n") {
		return snapshotDecodeError("security MACIP generation %q rules do not match desired order", group.Interface)
	}
	interfaceIndex, ok := securityGenerationInterfaceIndex(resultStdoutLast(results, "show interface"), group.Interface)
	if !ok || !securityMACIPInterfaceAttached(resultStdoutLast(results, "show acl-plugin macip interface"), interfaceIndex, id) {
		return snapshotDecodeError("security MACIP generation %q is not attached", group.Interface)
	}
	return nil
}

func verifySecurityGenerationReadback(generation SecurityGeneration, results []VPPCTLCommandResult) error {
	aclOutput := resultStdoutLast(results, "show acl-plugin acl")
	if len(securityGenerationACLIDs(aclOutput)) != len(generation.ACLs) {
		return snapshotDecodeError("security ACL generation readback count does not match desired state")
	}
	for _, group := range generation.ACLs {
		tag := "ly-route-security-gen-" + safeTag(group.Interface+"-"+group.Direction)
		if strings.Count(aclOutput, "tag {"+tag+"}") != 1 {
			return snapshotDecodeError("security ACL generation tag %q is missing or ambiguous", tag)
		}
	}
	macipOutput := resultStdoutLast(results, "show acl-plugin macip acl")
	if len(securityGenerationMACIPIDs(macipOutput)) != len(generation.MACIP) {
		return snapshotDecodeError("security MACIP generation readback count does not match desired state")
	}
	for _, group := range generation.MACIP {
		tag := "ly-route-security-macip-" + safeTag(group.Interface)
		if strings.Count(macipOutput, "tag {"+tag+"}") != 1 {
			return snapshotDecodeError("security MACIP generation tag %q is missing or ambiguous", tag)
		}
	}
	return nil
}

func verifySecurityGuardReadback(attacks []SecurityAttackRule, results []VPPCTLCommandResult) error {
	expected, err := securityAttackFamilies(attacks)
	if err != nil {
		return err
	}
	output := resultStdoutLast(results, "show ly-route security-guard")
	actual := securityGenerationGuardIDs(output)
	if len(actual) != len(expected) {
		return snapshotDecodeError("security guard readback count does not match desired state")
	}
	for _, item := range expected {
		id := securityAttackRuleID(item)
		lineCount := 0
		for _, line := range nonBlankLines(output) {
			fields := strings.Fields(line)
			if len(fields) < 12 || fields[0] != "rule" || fields[1] != id {
				continue
			}
			lineCount++
			if fields[2] != "enabled" || fields[3] != "1" || fields[4] != "family" || fields[5] != strconv.Itoa(item.Family) || fields[6] != "interface" || fields[7] != item.Rule.Interface || fields[8] != "threshold-pps" || fields[9] != strconv.Itoa(item.Rule.ThresholdPPS) || fields[10] != "burst-packets" || fields[11] != strconv.Itoa(item.Rule.BurstPackets) {
				return snapshotDecodeError("security guard rule %q readback does not match desired state", id)
			}
		}
		if lineCount != 1 {
			return snapshotDecodeError("security guard rule %q is missing or ambiguous", id)
		}
	}
	return nil
}

func taggedSecurityMACIPOutput(output, tag string) (string, int, bool, error) {
	lines := nonBlankLines(output)
	matched, matchedID := "", 0
	for index := 0; index < len(lines); {
		if !strings.HasPrefix(strings.TrimSpace(lines[index]), "MACIP acl_index:") {
			index++
			continue
		}
		end := index + 1
		for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "MACIP acl_index:") {
			end++
		}
		block := strings.Join(lines[index:end], "\n")
		if strings.Contains(lines[index], "tag {"+tag+"}") {
			if matched != "" {
				return "", 0, false, snapshotDecodeError("security MACIP tag %q is ambiguous", tag)
			}
			idMatch := securityGenerationMACIPIndex.FindStringSubmatch(lines[index])
			if len(idMatch) != 2 {
				return "", 0, false, snapshotDecodeError("security MACIP tag %q has no ACL index", tag)
			}
			matchedID, _ = strconv.Atoi(idMatch[1])
			matched = block
		}
		index = end
	}
	return matched, matchedID, matched != "", nil
}

func securityGenerationInterfaceIndex(output, interfaceName string) (int, bool) {
	for _, line := range nonBlankLines(output) {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != interfaceName {
			continue
		}
		index, err := strconv.Atoi(fields[1])
		return index, err == nil
	}
	return 0, false
}

func securityMACIPInterfaceAttached(output string, interfaceIndex, aclID int) bool {
	wanted := fmt.Sprintf("sw_if_index %d: %d", interfaceIndex, aclID)
	for _, line := range nonBlankLines(output) {
		if strings.TrimSpace(line) == wanted {
			return true
		}
	}
	return false
}
