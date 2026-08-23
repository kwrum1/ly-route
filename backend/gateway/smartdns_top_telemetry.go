package gateway

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

var smartDNSAuditLine = regexp.MustCompile(`^\[([^]]+)]\s+\S+\s+query\s+([^,\s]+),`)

type smartDNSAuditTelemetry struct {
	path string
	now  func() time.Time
}

func newSmartDNSAuditTelemetry(path string) smartDNSAuditTelemetry {
	return smartDNSAuditTelemetry{path: strings.TrimSpace(path), now: time.Now}
}

func (collector smartDNSAuditTelemetry) TopSessions(context.Context) ([]map[string]any, error) {
	return nil, fmt.Errorf("top sessions are provided by VPP")
}

func (collector smartDNSAuditTelemetry) TopDomains(context.Context) ([]map[string]any, error) {
	file, err := os.Open(collector.path)
	if err != nil {
		return nil, fmt.Errorf("read SmartDNS audit log: %w", err)
	}
	defer file.Close()

	type domainStat struct {
		count int
		last  time.Time
	}
	stats := map[string]domainStat{}
	cutoff := collector.now().Add(-24 * time.Hour)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		matches := smartDNSAuditLine.FindStringSubmatch(scanner.Text())
		if len(matches) != 3 {
			continue
		}
		domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(matches[2])), ".")
		if domain == "" {
			continue
		}
		observedAt, parseErr := time.ParseInLocation("2006-01-02 15:04:05,000", matches[1], time.Local)
		if parseErr != nil {
			continue
		}
		if observedAt.Before(cutoff) {
			continue
		}
		stat := stats[domain]
		stat.count++
		if observedAt.After(stat.last) {
			stat.last = observedAt
		}
		stats[domain] = stat
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan SmartDNS audit log: %w", err)
	}

	items := make([]map[string]any, 0, len(stats))
	for domain, stat := range stats {
		items = append(items, map[string]any{"domain": domain, "count": stat.count, "queries": stat.count, "hits": stat.count, "last_seen": stat.last.Format(time.RFC3339)})
	}
	sort.Slice(items, func(left, right int) bool {
		leftCount, _ := items[left]["count"].(int)
		rightCount, _ := items[right]["count"].(int)
		if leftCount != rightCount {
			return leftCount > rightCount
		}
		return items[left]["domain"].(string) < items[right]["domain"].(string)
	})
	return items, nil
}
