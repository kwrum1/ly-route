package vpp

import (
	"fmt"
	"net/netip"
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
	// VPP appliances use two covering /1 routes for a WAN-group default. Keep
	// each prefix separate while parsing: a table can temporarily contain the
	// old exact default alongside the replacement /1 routes during reconcile.
	// Prefer the replacement representation and fall back to 0/0 for older
	// snapshots.
	sections := map[string][]fibPath{}
	pendingWeight, pendingPreference := 1, 0
	activePrefix := ""
	for _, line := range lines[1:] {
		if strings.Count(line, "/") == 1 && strings.Contains(line, ".") {
			switch line {
			case "0.0.0.0/0", "0.0.0.0/1", "128.0.0.0/1":
				activePrefix = line
			default:
				activePrefix = ""
			}
			pendingWeight, pendingPreference = 1, 0
			continue
		}
		if activePrefix == "" {
			continue
		}
		switch {
		case line == "unicast-ip4-chain", strings.HasPrefix(line, "path-list:["), strings.HasPrefix(line, "[@") && strings.Contains(line, "dpo-load-balance"):
			continue
		case strings.Contains(line, "dpo-drop ip4"):
			// A known but unusable route created by an unresolved next hop.
			// Report no forwarding path so a verified-drift snapshot can
			// replace the route; this is not an unknown CLI grammar.
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
			sections[activePrefix] = append(sections[activePrefix], fibPath{via: via, weight: pendingWeight, preference: pendingPreference})
			pendingWeight, pendingPreference = 1, 0
		case strings.Contains(line, "lookup in ipv4-VRF:"):
			tableID := strings.TrimSpace(strings.SplitN(line, "lookup in ipv4-VRF:", 2)[1])
			if _, parseErr := strconv.Atoi(tableID); parseErr != nil {
				return nil, snapshotDecodeError("malformed FIB lookup table %q", tableID)
			}
			sections[activePrefix] = append(sections[activePrefix], fibPath{via: "table " + tableID, weight: pendingWeight, preference: pendingPreference})
			pendingWeight, pendingPreference = 1, 0
		default:
			return nil, snapshotDecodeError("unknown FIB path grammar %q in %q", line, output)
		}
	}
	paths := append([]fibPath(nil), sections["0.0.0.0/1"]...)
	paths = append(paths, sections["128.0.0.0/1"]...)
	if len(paths) > 0 {
		return uniqueFIBPaths(paths), nil
	}
	paths = append(paths, sections["0.0.0.0/0"]...)
	// A VPP 25.10 load-balance DPO may include an implicit lookup back to the
	// main table after its configured forwarding buckets.  It is the miss
	// fallback, not another configured WAN member.  Preserve a sole table-0
	// path because a policy may intentionally target the main table.
	if len(paths) > 1 {
		forwarding := paths[:0]
		for _, path := range paths {
			if path.via != "table 0" {
				forwarding = append(forwarding, path)
			}
		}
		if len(forwarding) > 0 {
			paths = forwarding
		}
	}
	return uniqueFIBPaths(paths), nil
}

func uniqueFIBPaths(paths []fibPath) []fibPath {
	unique := make([]fibPath, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		key := fmt.Sprintf("%s|%d|%d", path.via, path.weight, path.preference)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, path)
	}
	return unique
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
		case pending != nil:
			if via, ok := configuredFIBAttachedNextHop(line); ok {
				// Prefer the configured next hop. The following forwarding DPO
				// normalizes a PPPoE peer to 0.0.0.0 and loses this identity.
				pending.via = via
				continue
			}
			if pending.via == "" && strings.Contains(line, "ipv4 via ") {
				pending.via = strings.TrimSpace(strings.SplitN(line, "ipv4 via ", 2)[1])
			}
		}
	}
	flushList()
	return lists, nil
}

func configuredFIBAttachedNextHop(line string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 3 && fields[2] == "(p2p)" {
		fields = fields[:2]
	}
	if len(fields) != 2 {
		return "", false
	}
	if _, err := netip.ParseAddr(fields[0]); err != nil {
		return "", false
	}
	return strings.Join(fields, " "), true
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
