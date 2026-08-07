package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

type policyRoutingExpectation struct {
	Rules  []policyRuleExpectation
	Routes []policyRouteExpectation
}

type policyRuleExpectation struct {
	Mark     string
	Mask     string
	Source   string
	Table    int
	Priority int
}

type policyRouteExpectation struct {
	Type        string
	Destination string
	Via         string
	Device      string
	Scope       string
	Table       int
}

func validatePolicyRoutingReadback(ctx context.Context, runner CommandRunner, artifacts []RenderedArtifact) error {
	content, err := artifactContent(artifacts, "/var/lib/ly-route/policy-routing/apply.sh")
	if err != nil {
		return err
	}
	expected, err := parsePolicyRoutingExpectation(content)
	if err != nil {
		return err
	}
	rulesOutput, err := requiredOutput(ctx, runner, "ip", "-j", "rule", "show")
	if err != nil {
		return err
	}
	for _, rule := range expected.Rules {
		if err := requirePolicyRule(rulesOutput, rule); err != nil {
			return err
		}
	}
	for _, route := range expected.Routes {
		routesOutput, err := requiredOutput(ctx, runner, "ip", "-j", "route", "show", "table", strconv.Itoa(route.Table))
		if err != nil {
			return err
		}
		if err := requirePolicyRoute(routesOutput, route); err != nil {
			return err
		}
	}
	return nil
}

func parsePolicyRoutingExpectation(content string) (policyRoutingExpectation, error) {
	var expected policyRoutingExpectation
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 9 && strings.Join(fields[:4], " ") == "ip rule add fwmark" {
			var rule policyRuleExpectation
			mark := strings.SplitN(fields[4], "/", 2)
			if len(mark) == 2 {
				rule.Mark, rule.Mask = mark[0], mark[1]
				rule.Table, _ = strconv.Atoi(fields[6])
				rule.Priority, _ = strconv.Atoi(fields[8])
			}
			if rule.Mark == "" || rule.Mask == "" || rule.Table <= 0 || rule.Priority <= 0 {
				return policyRoutingExpectation{}, fmt.Errorf("rendered policy routing rule is incomplete")
			}
			expected.Rules = append(expected.Rules, rule)
		}
		if len(fields) == 9 && strings.Join(fields[:4], " ") == "ip rule add from" {
			rule := policyRuleExpectation{Source: normalizePolicyRuleSource(fields[4])}
			if fields[5] == "lookup" && fields[7] == "priority" {
				rule.Table, _ = strconv.Atoi(fields[6])
				rule.Priority, _ = strconv.Atoi(fields[8])
			}
			if rule.Source == "" || rule.Table <= 0 || rule.Priority <= 0 {
				return policyRoutingExpectation{}, fmt.Errorf("rendered source policy routing rule is incomplete")
			}
			expected.Rules = append(expected.Rules, rule)
		}
		if len(fields) >= 6 && strings.Join(fields[:3], " ") == "ip route replace" {
			route := policyRouteExpectation{Table: 254}
			optionIndex := 4
			if fields[3] == "local" {
				route.Type = "local"
				route.Destination = normalizeRouteDestination(fields[4])
				optionIndex = 5
			} else {
				route.Destination = normalizeRouteDestination(fields[3])
			}
			for index := optionIndex; index+1 < len(fields); index += 2 {
				switch fields[index] {
				case "via":
					route.Via = fields[index+1]
				case "dev":
					route.Device = fields[index+1]
				case "scope":
					route.Scope = fields[index+1]
				case "table":
					route.Table, _ = strconv.Atoi(fields[index+1])
				}
			}
			if route.Destination == "" || route.Device == "" || route.Table <= 0 {
				return policyRoutingExpectation{}, fmt.Errorf("rendered policy route is incomplete")
			}
			expected.Routes = append(expected.Routes, route)
		}
	}
	if len(expected.Rules) == 0 && len(expected.Routes) == 0 {
		if strings.Contains(content, linuxRoutingResetMarker) {
			return expected, nil
		}
		return policyRoutingExpectation{}, fmt.Errorf("rendered policy routing plan is incomplete")
	}
	return expected, nil
}

func normalizeRouteDestination(destination string) string {
	if destination == "0.0.0.0/0" {
		return "default"
	}
	if prefix, err := netip.ParsePrefix(destination); err == nil && prefix.Addr().Is4() && prefix.Bits() == 32 {
		return prefix.Addr().String()
	}
	return destination
}

func normalizePolicyRuleSource(source string) string {
	source = strings.TrimSpace(source)
	if prefix, err := netip.ParsePrefix(source); err == nil && prefix.Bits() == prefix.Addr().BitLen() {
		return prefix.Addr().String()
	}
	if address, err := netip.ParseAddr(source); err == nil {
		return address.String()
	}
	return source
}

func requirePolicyRule(output string, expected policyRuleExpectation) error {
	objects, err := decodeJSONObjects(output)
	if err != nil {
		return fmt.Errorf("decode policy rules: %w", err)
	}
	for _, object := range objects {
		identitiesMatch := numericJSONValue(object["priority"]) == strconv.Itoa(expected.Priority) && numericJSONValue(object["table"]) == strconv.Itoa(expected.Table)
		if expected.Source != "" && identitiesMatch && normalizePolicyRuleSource(fmt.Sprint(object["src"])) == expected.Source {
			return nil
		}
		if expected.Source == "" && identitiesMatch && equalNumeric(object["fwmark"], expected.Mark) && equalPolicyMask(object["fwmask"], expected.Mask) {
			return nil
		}
	}
	if expected.Source != "" {
		return fmt.Errorf("policy readback missing source %s priority %d table %d", expected.Source, expected.Priority, expected.Table)
	}
	return fmt.Errorf("policy readback missing fwmark %s/%s priority %d table %d", expected.Mark, expected.Mask, expected.Priority, expected.Table)
}

func equalPolicyMask(actual any, expected string) bool {
	if actual == nil {
		parsed, err := strconv.ParseUint(expected, 0, 64)
		return err == nil && parsed == 0xffffffff
	}
	return equalNumeric(actual, expected)
}

func requirePolicyRoute(output string, expected policyRouteExpectation) error {
	objects, err := decodeJSONObjects(output)
	if err != nil {
		return fmt.Errorf("decode policy routes: %w", err)
	}
	for _, object := range objects {
		if fmt.Sprint(object["dst"]) == expected.Destination && fmt.Sprint(object["dev"]) == expected.Device && (expected.Type == "" || fmt.Sprint(object["type"]) == expected.Type) && (expected.Via == "" || fmt.Sprint(object["gateway"]) == expected.Via) && (expected.Scope == "" || fmt.Sprint(object["scope"]) == expected.Scope) {
			return nil
		}
	}
	return fmt.Errorf("policy table %d missing route %s via %s dev %s scope %s", expected.Table, expected.Destination, expected.Via, expected.Device, expected.Scope)
}

func decodeJSONObjects(output string) ([]map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.UseNumber()
	var objects []map[string]any
	err := decoder.Decode(&objects)
	return objects, err
}

func numericJSONValue(value any) string {
	switch typed := value.(type) {
	case json.Number:
		return typed.String()
	case string:
		parsed, err := strconv.ParseUint(typed, 0, 64)
		if err == nil {
			return strconv.FormatUint(parsed, 10)
		}
	}
	return fmt.Sprint(value)
}

func equalNumeric(actual any, expected string) bool {
	parsed, err := strconv.ParseUint(expected, 0, 64)
	return err == nil && numericJSONValue(actual) == strconv.FormatUint(parsed, 10)
}
