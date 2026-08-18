package vpp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func TestDecodeServiceChainReadback_accepts_stock_attachment_format(t *testing.T) {
	// Given
	chain := testServiceChain()
	operations, err := BuildServiceChainOperations("txn-stock-readback", chain, testServiceChainAttachments())
	if err != nil {
		t.Fatal(err)
	}

	// When
	readback, err := DecodeServiceChainReadback(chain, operations, stockServiceChainResults(operations))

	// Then
	if err != nil {
		t.Fatal(err)
	}
	policy := operations[0].Payload.(ServiceChainPolicy)
	if len(readback.Policies) != len(operations) {
		t.Fatalf("policy count = %d, want %d", len(readback.Policies), len(operations))
	}
	observed := readback.Policies[0]
	if observed.PolicyID != policy.PolicyID || observed.ACLID != policy.ACLID || observed.AddressFamily != policy.AddressFamily || observed.IngressInterface != policy.IngressInterface || observed.ServiceInterface != policy.ServiceInterface || observed.NextHop != policy.NextHop || observed.Match != policy.Match || observed.Priority != policy.Priority || !observed.Attached {
		t.Fatalf("observed policy = %#v, want %#v", observed, policy)
	}
}

func TestTaggedServiceChainACLIDsAcceptsStockVPPHeader(t *testing.T) {
	output := `acl-index 2 count 1 tag {ly-route-flow_30}
          0: ipv4 permit src 192.168.50.102/32 dst 0.0.0.0/0 proto 6 sport 0-65535 dport 0-65535
`
	ids := taggedServiceChainACLIDs(output, "ly-route-flow_30")
	if len(ids) != 1 || ids[0] != 2 {
		t.Fatalf("tagged ACL IDs = %#v, want [2]", ids)
	}
}

func TestDecodeServiceChainReadback_rejects_wrong_observed_acl(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(ServiceChainPolicy) string
		wantDetail string
	}{
		{name: "tuple", wantDetail: "ACL tuple", mutate: func(policy ServiceChainPolicy) string {
			policy.Match.DestinationIP = "198.18.9.9"
			return stockServiceChainACL(policy)
		}},
		{name: "identity", wantDetail: "ACL identity", mutate: func(policy ServiceChainPolicy) string {
			output := stockServiceChainACL(policy)
			return strings.Replace(output, "acl-index "+strconv.Itoa(policy.ACLID), "acl-index "+strconv.Itoa(policy.ACLID+1), 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			chain := testServiceChain()
			operations, err := BuildServiceChainOperations("txn-wrong-acl", chain, testServiceChainAttachments())
			if err != nil {
				t.Fatal(err)
			}
			policy := operations[0].Payload.(ServiceChainPolicy)
			results := replaceServiceChainOutput(stockServiceChainResults(operations), "show acl-plugin acl index "+strconv.Itoa(policy.ACLID), test.mutate(policy))

			// When
			_, err = DecodeServiceChainReadback(chain, operations, results)

			// Then
			if !errors.Is(err, ErrServiceChainReadback) || !strings.Contains(err.Error(), test.wantDetail) {
				t.Fatalf("error = %v, want %q", err, test.wantDetail)
			}
		})
	}
}

func TestDecodeServiceChainReadback_rejects_wrong_observed_abf_path(t *testing.T) {
	// Given
	chain := testServiceChain()
	operations, err := BuildServiceChainOperations("txn-wrong-abf", chain, testServiceChainAttachments())
	if err != nil {
		t.Fatal(err)
	}
	policy := operations[0].Payload.(ServiceChainPolicy)
	wrong := policy
	wrong.NextHop = "198.18.99.2"
	results := replaceServiceChainOutput(stockServiceChainResults(operations), policyShowCommand(policy), stockServiceChainABFPolicy(wrong))

	// When
	_, err = DecodeServiceChainReadback(chain, operations, results)

	// Then
	if !errors.Is(err, ErrServiceChainReadback) || !strings.Contains(err.Error(), "ABF path") {
		t.Fatalf("error = %v, want ABF path mismatch", err)
	}
}

func TestDecodeServiceChainReadback_rejects_wrong_observed_attachment(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(ServiceChainPolicy) ServiceChainPolicy
		wantDetail string
	}{
		{name: "family", wantDetail: "attachment family", mutate: func(policy ServiceChainPolicy) ServiceChainPolicy {
			policy.AddressFamily = "ip6"
			return policy
		}},
		{name: "priority", wantDetail: "attachment priority", mutate: func(policy ServiceChainPolicy) ServiceChainPolicy {
			policy.Priority++
			return policy
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			chain := testServiceChain()
			operations, err := BuildServiceChainOperations("txn-wrong-attachment", chain, testServiceChainAttachments())
			if err != nil {
				t.Fatal(err)
			}
			policy := operations[0].Payload.(ServiceChainPolicy)
			results := replaceServiceChainOutput(stockServiceChainResults(operations), attachmentShowCommand(policy), stockServiceChainAttachment(test.mutate(policy)))

			// When
			_, err = DecodeServiceChainReadback(chain, operations, results)

			// Then
			if !errors.Is(err, ErrServiceChainReadback) || !strings.Contains(err.Error(), test.wantDetail) {
				t.Fatalf("error = %v, want %q", err, test.wantDetail)
			}
		})
	}
}

func stockServiceChainResults(operations []Operation) []VPPCTLCommandResult {
	results := make([]VPPCTLCommandResult, 0, len(operations)*4)
	for _, operation := range operations {
		policy := operation.Payload.(ServiceChainPolicy)
		results = append(results,
			VPPCTLCommandResult{Command: "show acl-plugin acl index " + strconv.Itoa(policy.ACLID), Stdout: stockServiceChainACL(policy)},
			VPPCTLCommandResult{Command: policyShowCommand(policy), Stdout: stockServiceChainABFPolicy(policy)},
			VPPCTLCommandResult{Command: attachmentShowCommand(policy), Stdout: stockServiceChainAttachment(policy)},
			VPPCTLCommandResult{Command: "show interface " + policy.IngressInterface, Stdout: stockServiceChainInterface(policy)},
		)
	}
	return results
}

func stockServiceChainACL(policy ServiceChainPolicy) string {
	family := stockServiceChainFamily(policy)
	return fmt.Sprintf("acl-index %d count 1 tag {%s}\n  0: %s permit src %s dst %s proto %d sport %s dport %s\n", policy.ACLID, serviceChainACLTag(policy), family, hostPrefix(policy.Match.SourceIP), hostPrefix(policy.Match.DestinationIP), serviceChainProtocol(policy.Match.Protocol), serviceChainPort(policy.Match.SourcePort), serviceChainPort(policy.Match.DestinationPort))
}

func stockServiceChainABFPolicy(policy ServiceChainPolicy) string {
	family := stockServiceChainFamily(policy)
	return fmt.Sprintf("abf:[0]: policy:%d acl:%d\n path-list:[17] locks:1 flags:shared len:1\n  path:[21] pl-index:17 %s weight=1 pref=0\n    [@0]: %s via %s %s\n", policy.PolicyID, policy.ACLID, policy.AddressFamily, family, policy.NextHop, policy.ServiceInterface)
}

func stockServiceChainAttachment(policy ServiceChainPolicy) string {
	family := stockServiceChainFamily(policy)
	return fmt.Sprintf("%s:\n abf-interface-attach: policy:%d priority:%d\n  [@0]: dpo-load-balance: [proto:%s index:7 buckets:1]\n", family, policy.PolicyID, policy.Priority, policy.AddressFamily)
}

func stockServiceChainInterface(policy ServiceChainPolicy) string {
	return policy.IngressInterface + " (up):\n  rx packets 10\n  tx packets 9\n  rx bytes 1000\n  tx bytes 900\n"
}

func stockServiceChainFamily(policy ServiceChainPolicy) string {
	if policy.AddressFamily == "ip6" {
		return "ipv6"
	}
	return "ipv4"
}

func replaceServiceChainOutput(results []VPPCTLCommandResult, command, output string) []VPPCTLCommandResult {
	updated := append([]VPPCTLCommandResult(nil), results...)
	for index := range updated {
		if updated[index].Command == command {
			updated[index].Stdout = output
			return updated
		}
	}
	return updated
}
