package vpp

import (
	"fmt"
	"strconv"
	"strings"
)

func parseFIBResult(results []VPPCTLCommandResult, tableID int) ([]fibPath, error) {
	output, err := commandOutput(results, fmt.Sprintf("show ip fib table %d", tableID))
	if err != nil {
		return nil, err
	}
	lines := nonBlankLines(output)
	if len(lines) == 0 || !strings.HasPrefix(lines[0], fmt.Sprintf("ipv4-VRF:%d,", tableID)) {
		return nil, snapshotDecodeError("FIB table %d header is missing", tableID)
	}
	var paths []fibPath
	pendingWeight, pendingPreference := 1, 0
	defaultRoute := false
	for _, line := range lines[1:] {
		if strings.Count(line, "/") == 1 && strings.Contains(line, ".") {
			defaultRoute = line == "0.0.0.0/0"
			continue
		}
		if !defaultRoute {
			continue
		}
		switch {
		case line == "unicast-ip4-chain", strings.HasPrefix(line, "path-list:["), strings.HasPrefix(line, "[@") && strings.Contains(line, "dpo-load-balance"):
			continue
		case line == "stacked-on:", strings.HasPrefix(line, "[@") && strings.Contains(line, "-tx-dpo:"):
			// VPP 25.10 prints the underlying interface DPO on a nested
			// `stacked-on:` line after the resolved `via ...` path.  It is
			// descriptive output, not a second forwarding path.
			continue
		case strings.HasPrefix(line, "path:["):
			pendingWeight, pendingPreference = 1, 0
			if pendingWeight, err = fibPathAttribute(line, "weight=", 1); err != nil {
				return nil, err
			}
			if pendingPreference, err = fibPathAttribute(line, "pref=", 0); err != nil {
				return nil, err
			}
		case strings.Contains(line, " via "):
			via := strings.TrimSpace(strings.SplitN(line, " via ", 2)[1])
			paths = append(paths, fibPath{via: via, weight: pendingWeight, preference: pendingPreference})
			pendingWeight, pendingPreference = 1, 0
		case strings.Contains(line, "lookup in ipv4-VRF:"):
			tableID := strings.TrimSpace(strings.SplitN(line, "lookup in ipv4-VRF:", 2)[1])
			if _, parseErr := strconv.Atoi(tableID); parseErr != nil {
				return nil, snapshotDecodeError("malformed FIB lookup table %q", tableID)
			}
			paths = append(paths, fibPath{via: "table " + tableID, weight: pendingWeight, preference: pendingPreference})
			pendingWeight, pendingPreference = 1, 0
		default:
			return nil, snapshotDecodeError("unknown FIB path grammar %q in %q", line, output)
		}
	}
	return paths, nil
}

// parseConfiguredFIBPathLists reads the configured path objects rather than the
// compact forwarding buckets printed by VPP 25.10's `show ip fib`. The latter
// omits configured weights and hides inactive primary/backup paths.
func parseConfiguredFIBPathLists(results []VPPCTLCommandResult) ([][]fibPath, error) {
	output, err := commandOutput(results, "show fib path-lists")
	if err != nil {
		return nil, err
	}
	var lists [][]fibPath
	var current []fibPath
	var pending *fibPath
	flushPath := func() {
		if pending != nil && pending.via != "" {
			current = append(current, *pending)
		}
		pending = nil
	}
	flushList := func() {
		flushPath()
		if len(current) > 0 {
			lists = append(lists, current)
		}
		current = nil
	}
	for _, line := range nonBlankLines(output) {
		switch {
		case strings.HasPrefix(line, "path-list:["):
			flushList()
		case strings.HasPrefix(line, "path:["):
			flushPath()
			weight, parseErr := fibPathAttribute(line, "weight=", 1)
			if parseErr != nil {
				return nil, parseErr
			}
			preference, parseErr := fibPathAttribute(line, "pref=", 0)
			if parseErr != nil {
				return nil, parseErr
			}
			pending = &fibPath{weight: weight, preference: preference}
		case pending != nil && strings.Contains(line, "ipv4 via "):
			pending.via = strings.TrimSpace(strings.SplitN(line, "ipv4 via ", 2)[1])
		}
	}
	flushList()
	return lists, nil
}

func fibPathAttribute(line, marker string, fallback int) (int, error) {
	index := strings.Index(line, marker)
	if index < 0 {
		return fallback, nil
	}
	value := strings.Fields(line[index+len(marker):])[0]
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 || marker == "weight=" && parsed < 1 {
		return 0, snapshotDecodeError("malformed FIB %s %q", strings.TrimSuffix(marker, "="), value)
	}
	return parsed, nil
}
