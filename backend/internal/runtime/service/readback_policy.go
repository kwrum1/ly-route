package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type policyRoutingExpectation struct {
	Mark        string
	Mask        string
	Table       int
	Priority    int
	Destination string
	Via         string
	Device      string
	Scope       string
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
	if err := requirePolicyRule(rulesOutput, expected); err != nil {
		return err
	}
	routesOutput, err := requiredOutput(ctx, runner, "ip", "-j", "route", "show", "table", strconv.Itoa(expected.Table))
	if err != nil {
		return err
	}
	return requirePolicyRoute(routesOutput, expected)
}

func parsePolicyRoutingExpectation(content string) (policyRoutingExpectation, error) {
	var expected policyRoutingExpectation
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 9 && strings.Join(fields[:4], " ") == "ip rule add fwmark" {
			mark := strings.SplitN(fields[4], "/", 2)
			if len(mark) == 2 {
				expected.Mark, expected.Mask = mark[0], mark[1]
				expected.Table, _ = strconv.Atoi(fields[6])
				expected.Priority, _ = strconv.Atoi(fields[8])
			}
		}
		if len(fields) >= 8 && strings.Join(fields[:3], " ") == "ip route replace" {
			expected.Destination = fields[3]
			for index := 4; index+1 < len(fields); index += 2 {
				switch fields[index] {
				case "via":
					expected.Via = fields[index+1]
				case "dev":
					expected.Device = fields[index+1]
				case "scope":
					expected.Scope = fields[index+1]
				case "table":
					expected.Table, _ = strconv.Atoi(fields[index+1])
				}
			}
		}
	}
	if expected.Mark == "" || expected.Mask == "" || expected.Table <= 0 || expected.Priority <= 0 || expected.Destination == "" || expected.Device == "" {
		return policyRoutingExpectation{}, fmt.Errorf("rendered policy routing plan is incomplete")
	}
	return expected, nil
}

func requirePolicyRule(output string, expected policyRoutingExpectation) error {
	objects, err := decodeJSONObjects(output)
	if err != nil {
		return fmt.Errorf("decode policy rules: %w", err)
	}
	for _, object := range objects {
		if numericJSONValue(object["priority"]) == strconv.Itoa(expected.Priority) && numericJSONValue(object["table"]) == strconv.Itoa(expected.Table) && equalNumeric(object["fwmark"], expected.Mark) && equalNumeric(object["fwmask"], expected.Mask) {
			return nil
		}
	}
	return fmt.Errorf("policy readback missing fwmark %s/%s priority %d table %d", expected.Mark, expected.Mask, expected.Priority, expected.Table)
}

func requirePolicyRoute(output string, expected policyRoutingExpectation) error {
	objects, err := decodeJSONObjects(output)
	if err != nil {
		return fmt.Errorf("decode policy routes: %w", err)
	}
	for _, object := range objects {
		if fmt.Sprint(object["dst"]) == expected.Destination && fmt.Sprint(object["dev"]) == expected.Device && (expected.Via == "" || fmt.Sprint(object["gateway"]) == expected.Via) && (expected.Scope == "" || fmt.Sprint(object["scope"]) == expected.Scope) {
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
