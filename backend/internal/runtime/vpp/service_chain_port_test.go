package vpp

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestDecodeServiceChainReadback_accepts_stock_singleton_and_equal_range_ports(t *testing.T) {
	tests := []struct {
		name      string
		singleton bool
	}{
		{name: "singleton", singleton: true},
		{name: "equal range", singleton: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			chain := testServiceChain()
			operations, err := BuildServiceChainOperations("txn-port-form", chain, testServiceChainAttachments())
			if err != nil {
				t.Fatal(err)
			}
			results := stockServiceChainResultsWithPortForm(operations, test.singleton)

			// When
			readback, err := DecodeServiceChainReadback(chain, operations, results)

			// Then
			if err != nil {
				t.Fatal(err)
			}
			for index, operation := range operations {
				want := operation.Payload.(ServiceChainPolicy).Match
				if readback.Policies[index].Match != want {
					t.Fatalf("match = %#v, want %#v", readback.Policies[index].Match, want)
				}
			}
		})
	}
}

func TestDecodeServiceChainReadback_rejects_malformed_stock_ports(t *testing.T) {
	tests := []struct {
		name            string
		sourcePort      string
		destinationPort string
	}{
		{name: "unequal range", sourcePort: "41000-41001", destinationPort: "443"},
		{name: "missing range bound", sourcePort: "41000-", destinationPort: "443"},
		{name: "out of range singleton", sourcePort: "65536", destinationPort: "443"},
		{name: "zero singleton", sourcePort: "0", destinationPort: "443"},
		{name: "nonnumeric destination", sourcePort: "41000", destinationPort: "web"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			chain := testServiceChain()
			operations, err := BuildServiceChainOperations("txn-malformed-port", chain, testServiceChainAttachments())
			if err != nil {
				t.Fatal(err)
			}
			policy := operations[0].Payload.(ServiceChainPolicy)
			results := stockServiceChainResults(operations)
			output := stockServiceChainACL(policy)
			output = strings.Replace(output, "sport "+serviceChainPort(policy.Match.SourcePort), "sport "+test.sourcePort, 1)
			output = strings.Replace(output, "dport "+serviceChainPort(policy.Match.DestinationPort), "dport "+test.destinationPort, 1)
			results = replaceServiceChainOutput(results, "show acl-plugin acl index "+strconv.Itoa(policy.ACLID), output)

			// When
			_, err = DecodeServiceChainReadback(chain, operations, results)

			// Then
			if !errors.Is(err, ErrServiceChainReadback) || !strings.Contains(err.Error(), "ACL tuple port") {
				t.Fatalf("error = %v, want malformed ACL tuple port", err)
			}
		})
	}
}

func stockServiceChainResultsWithPortForm(operations []Operation, singleton bool) []VPPCTLCommandResult {
	results := stockServiceChainResults(operations)
	if !singleton {
		return results
	}
	for _, operation := range operations {
		policy := operation.Payload.(ServiceChainPolicy)
		output := stockServiceChainACL(policy)
		if policy.Match.SourcePort != 0 {
			output = strings.Replace(output, "sport "+serviceChainPort(policy.Match.SourcePort), "sport "+strconv.Itoa(int(policy.Match.SourcePort)), 1)
		}
		if policy.Match.DestinationPort != 0 {
			output = strings.Replace(output, "dport "+serviceChainPort(policy.Match.DestinationPort), "dport "+strconv.Itoa(int(policy.Match.DestinationPort)), 1)
		}
		results = replaceServiceChainOutput(results, "show acl-plugin acl index "+strconv.Itoa(policy.ACLID), output)
	}
	return results
}
