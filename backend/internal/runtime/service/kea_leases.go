package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultKeaDHCP4LeaseFile = "/var/lib/kea/kea-leases4.csv"
	maxKeaLeaseFileSize      = 64 << 20
)

// KeaMemfileLeaseCollector exposes only active DHCPv4 leases from Kea's
// memfile backend. Kea appends updates, so the last row for an address is the
// authoritative row, including release/reclaim tombstones.
type KeaMemfileLeaseCollector struct {
	Path string
	Now  func() time.Time
}

type keaLeaseCandidate struct {
	address       netip.Addr
	hardware      string
	hostname      string
	validLifetime uint64
	expires       time.Time
	subnetID      uint64
	active        bool
}

func (collector KeaMemfileLeaseCollector) Leases(ctx context.Context) ([]map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := strings.TrimSpace(collector.Path)
	if path == "" {
		path = DefaultKeaDHCP4LeaseFile
	}
	content, err := readStableKeaLeaseFile(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("read Kea DHCPv4 lease database: %w", err)
	}
	candidates, err := parseKeaDHCP4Leases(ctx, content)
	if err != nil {
		return nil, fmt.Errorf("parse Kea DHCPv4 lease database: %w", err)
	}
	now := time.Now().UTC()
	if collector.Now != nil {
		now = collector.Now().UTC()
	}
	active := make([]keaLeaseCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.active && candidate.expires.After(now) {
			active = append(active, candidate)
		}
	}
	sort.Slice(active, func(left, right int) bool { return active[left].address.Less(active[right].address) })
	leases := make([]map[string]any, 0, len(active))
	for _, candidate := range active {
		lease := map[string]any{
			"id":                     candidate.address.String(),
			"ip_address":             candidate.address.String(),
			"lease_end":              candidate.expires.Format(time.RFC3339),
			"valid_lifetime_seconds": candidate.validLifetime,
			"subnet_id":              candidate.subnetID,
			"state":                  "active",
		}
		if candidate.validLifetime <= uint64(candidate.expires.Unix()) {
			lease["lease_start"] = candidate.expires.Add(-time.Duration(candidate.validLifetime) * time.Second).Format(time.RFC3339)
		}
		if candidate.hardware != "" {
			lease["mac"] = candidate.hardware
		}
		if candidate.hostname != "" {
			lease["hostname"] = candidate.hostname
		}
		leases = append(leases, lease)
	}
	return leases, nil
}

func readStableKeaLeaseFile(ctx context.Context, path string) ([]byte, error) {
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		before, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !before.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a regular file", path)
		}
		if before.Size() > maxKeaLeaseFileSize {
			return nil, fmt.Errorf("lease database exceeds %d bytes", maxKeaLeaseFileSize)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		after, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if before.Size() == after.Size() && before.ModTime() == after.ModTime() {
			return content, nil
		}
	}
	return nil, fmt.Errorf("lease database changed during three consecutive reads")
}

func parseKeaDHCP4Leases(ctx context.Context, content []byte) (map[netip.Addr]keaLeaseCandidate, error) {
	reader := csv.NewReader(bytes.NewReader(content))
	reader.TrimLeadingSpace = true
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("lease database is empty and has no header")
	}
	header := map[string]int{}
	for index, name := range records[0] {
		name = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(name)), "\ufeff")
		if name == "" {
			return nil, fmt.Errorf("header column %d is empty", index+1)
		}
		if _, exists := header[name]; exists {
			return nil, fmt.Errorf("header contains duplicate column %q", name)
		}
		header[name] = index
	}
	for _, required := range []string{"address", "hwaddr", "valid_lifetime", "expire", "subnet_id", "hostname", "state"} {
		if _, ok := header[required]; !ok {
			return nil, fmt.Errorf("required column %q is missing", required)
		}
	}
	candidates := map[netip.Addr]keaLeaseCandidate{}
	for rowIndex, row := range records[1:] {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(row) == 1 && strings.TrimSpace(row[0]) == "" {
			continue
		}
		if len(row) != len(records[0]) {
			return nil, fmt.Errorf("row %d has %d columns; expected %d", rowIndex+2, len(row), len(records[0]))
		}
		address, err := netip.ParseAddr(strings.TrimSpace(row[header["address"]]))
		if err != nil || !address.Is4() {
			return nil, fmt.Errorf("row %d has an invalid DHCPv4 address", rowIndex+2)
		}
		state, err := strconv.ParseUint(strings.TrimSpace(row[header["state"]]), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("row %d has an invalid lease state", rowIndex+2)
		}
		candidate := keaLeaseCandidate{address: address, active: state == 0}
		if candidate.active {
			candidate.validLifetime, err = parseKeaUint(row[header["valid_lifetime"]], "valid lifetime", rowIndex+2)
			if err != nil {
				return nil, err
			}
			expires, parseErr := parseKeaUint(row[header["expire"]], "expiration", rowIndex+2)
			if parseErr != nil || expires > uint64(^uint64(0)>>1) {
				if parseErr != nil {
					return nil, parseErr
				}
				return nil, fmt.Errorf("row %d has an invalid expiration", rowIndex+2)
			}
			candidate.expires = time.Unix(int64(expires), 0).UTC()
			candidate.subnetID, err = parseKeaUint(row[header["subnet_id"]], "subnet id", rowIndex+2)
			if err != nil {
				return nil, err
			}
			candidate.hostname = strings.TrimSpace(row[header["hostname"]])
			candidate.hardware = strings.ToLower(strings.TrimSpace(row[header["hwaddr"]]))
			if candidate.hardware != "" {
				parsed, macErr := net.ParseMAC(candidate.hardware)
				if macErr != nil {
					return nil, fmt.Errorf("row %d has an invalid hardware address", rowIndex+2)
				}
				candidate.hardware = strings.ToLower(parsed.String())
			}
		}
		candidates[address] = candidate
	}
	return candidates, nil
}

func parseKeaUint(value, field string, row int) (uint64, error) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("row %d has an invalid %s", row, field)
	}
	return parsed, nil
}
