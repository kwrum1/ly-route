package dns

import (
	"bufio"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// IPSetMember is a validated DNS answer observed in a SmartDNS-managed
// timeout set. ExpiresAt is derived from the kernel timeout, never from text
// supplied by the control plane.
type IPSetMember struct {
	SetName   string    `json:"set_name"`
	IP        string    `json:"ip"`
	ExpiresAt time.Time `json:"expires_at"`
}

// ParseIPSetList parses the output of `ipset list <set> -t`. The expected set
// name is required so a stale or substituted command cannot inject answers
// from another policy set.
func ParseIPSetList(output, expectedSet string, now time.Time) ([]IPSetMember, error) {
	expectedSet = strings.TrimSpace(expectedSet)
	if expectedSet == "" {
		return nil, fmt.Errorf("expected IP set name is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	setName := ""
	inMembers := false
	seen := map[string]struct{}{}
	members := make([]IPSetMember, 0)
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "Name:"):
			setName = strings.TrimSpace(strings.TrimPrefix(line, "Name:"))
		case line == "Members:":
			inMembers = true
		case inMembers && line != "":
			fields := strings.Fields(line)
			if len(fields) != 3 || fields[1] != "timeout" {
				return nil, fmt.Errorf("invalid IP set member line %q", line)
			}
			address, err := netip.ParseAddr(fields[0])
			if err != nil {
				return nil, fmt.Errorf("invalid IP set address %q: %w", fields[0], err)
			}
			seconds, err := strconv.ParseInt(fields[2], 10, 64)
			if err != nil || seconds <= 0 {
				return nil, fmt.Errorf("invalid IP set timeout %q", fields[2])
			}
			canonical := address.String()
			if _, exists := seen[canonical]; exists {
				continue
			}
			seen[canonical] = struct{}{}
			members = append(members, IPSetMember{SetName: expectedSet, IP: canonical, ExpiresAt: now.Add(time.Duration(seconds) * time.Second)})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read IP set output: %w", err)
	}
	if setName != expectedSet {
		return nil, fmt.Errorf("IP set name %q does not match expected %q", setName, expectedSet)
	}
	return members, nil
}
