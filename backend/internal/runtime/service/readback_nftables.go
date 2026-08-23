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
		next := nftablesRulePosition(normalized, rule, position)
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

func nftablesRulePosition(live, rendered string, previous int) int {
	position := strings.Index(live, rendered)
	if position > previous {
		return position
	}
	for _, protocol := range []string{"tcp", "udp"} {
		for _, suffix := range []string{" meta mark", " counter meta mark"} {
			variant := strings.Replace(rendered, " meta l4proto "+protocol+suffix, suffix, 1)
			if variant == rendered {
				continue
			}
			position = strings.Index(live, variant)
			if position > previous {
				return position
			}
		}
	}
	return -1
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
		isDNS := strings.Contains(normalized, "dport 53") && nftablesRuleHasToken(normalized, "redirect")
		isTProxy := nftablesRuleHasToken(normalized, "tproxy")
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

func nftablesRuleHasToken(rule, token string) bool {
	for _, field := range strings.Fields(rule) {
		if field == token {
			return true
		}
	}
	return false
}

func normalizedNftables(value string) string {
	fields := strings.Fields(value)
	transport := ""
	if strings.Contains(value, "jhash") {
		for _, protocol := range []string{"tcp", "udp"} {
			if strings.Contains(value, protocol+" sport") {
				transport = protocol
				break
			}
		}
	}
	normalized := make([]string, 0, len(fields))
	for index := 0; index < len(fields); index++ {
		field := fields[index]
		if field == "counter" && index+4 < len(fields) && fields[index+1] == "packets" && fields[index+3] == "bytes" {
			normalized = append(normalized, field)
			index += 4
			continue
		}
		if field == "==" {
			continue
		}
		if transport != "" && index+2 < len(fields) && field == "meta" && fields[index+1] == "l4proto" && fields[index+2] == transport {
			index += 2
			continue
		}
		if !strings.HasPrefix(field, "0x") {
			normalized = append(normalized, field)
			continue
		}
		parsed, err := strconv.ParseUint(field, 0, 64)
		if err == nil {
			field = fmt.Sprintf("0x%x", parsed)
		}
		normalized = append(normalized, field)
	}
	return strings.Join(normalized, " ")
}
