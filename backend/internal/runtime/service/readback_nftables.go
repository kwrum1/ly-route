package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

func validateNftablesReadback(ctx context.Context, runner CommandRunner, artifacts []RenderedArtifact) error {
	content, err := artifactContent(artifacts, "/etc/nftables.conf")
	if err != nil {
		return err
	}
	family, table, rules, err := expectedNftablesRules(content)
	if err != nil {
		return err
	}
	output, err := requiredOutput(ctx, runner, "nft", "list", "table", family, table)
	if err != nil {
		return err
	}
	normalized := normalizedNftables(output)
	position := -1
	for _, rule := range rules {
		next := strings.Index(normalized, rule)
		if next < 0 {
			return fmt.Errorf("nftables live table missing rule %q", rule)
		}
		if next <= position {
			return fmt.Errorf("nftables live rule %q violates rendered rule order", rule)
		}
		position = next
	}
	return nil
}

func expectedNftablesRules(content string) (string, string, []string, error) {
	var family, table string
	rules := make([]string, 0, 4)
	dnsRules := 0
	tproxyRules := 0
	firstTProxy := -1
	lastDNS := -1
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "table" {
			family, table = fields[1], fields[2]
		}
		normalized := normalizedNftables(line)
		isDNS := strings.Contains(normalized, "dport 53")
		isTProxy := strings.Contains(normalized, "tproxy to")
		if isDNS || isTProxy {
			rules = append(rules, normalized)
			position := len(rules) - 1
			if isDNS {
				dnsRules++
				lastDNS = position
			}
			if isTProxy {
				tproxyRules++
				if firstTProxy < 0 {
					firstTProxy = position
				}
			}
		}
	}
	if family == "" || table == "" {
		return "", "", nil, fmt.Errorf("rendered nftables plan lacks a table declaration")
	}
	if dnsRules == 0 && tproxyRules == 0 {
		return "", "", nil, fmt.Errorf("rendered nftables plan lacks capture rules")
	}
	if dnsRules > 0 && dnsRules < 2 {
		return "", "", nil, fmt.Errorf("rendered nftables plan requires TCP and UDP DNS rules")
	}
	if tproxyRules > 0 && tproxyRules < 2 {
		return "", "", nil, fmt.Errorf("rendered nftables plan requires TCP and UDP TProxy rules")
	}
	if dnsRules > 0 && tproxyRules > 0 && lastDNS >= firstTProxy {
		return "", "", nil, fmt.Errorf("rendered nftables plan violates DNS-before-TProxy semantics")
	}
	return family, table, rules, nil
}

func normalizedNftables(value string) string {
	fields := strings.Fields(value)
	for index, field := range fields {
		if !strings.HasPrefix(field, "0x") {
			continue
		}
		parsed, err := strconv.ParseUint(field, 0, 64)
		if err == nil {
			fields[index] = fmt.Sprintf("0x%x", parsed)
		}
	}
	return strings.Join(fields, " ")
}
