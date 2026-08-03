package service

import (
	"context"
	"fmt"
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
	normalized := normalizedLine(output)
	position := -1
	for _, rule := range rules {
		next := strings.Index(normalized, rule)
		if next < 0 {
			return fmt.Errorf("nftables live table missing rule %q", rule)
		}
		if next <= position {
			return fmt.Errorf("nftables live rule %q violates DNS-before-TProxy order", rule)
		}
		position = next
	}
	return nil
}

func expectedNftablesRules(content string) (string, string, []string, error) {
	var family, table string
	rules := make([]string, 0, 4)
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "table" {
			family, table = fields[1], fields[2]
		}
		normalized := normalizedLine(line)
		if strings.Contains(normalized, "dport 53") || strings.Contains(normalized, "tproxy to") {
			rules = append(rules, normalized)
		}
	}
	if family == "" || table == "" || len(rules) < 4 || !strings.Contains(rules[0], "dport 53") || !strings.Contains(rules[1], "dport 53") || !strings.Contains(rules[2], "tproxy to") {
		return "", "", nil, fmt.Errorf("rendered nftables plan lacks DNS-before-TProxy semantics")
	}
	return family, table, rules, nil
}
