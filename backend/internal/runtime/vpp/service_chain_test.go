package vpp

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"ly-route/backend/internal/orchestrator"
)

func TestBuildServiceChainOperations_emits_forward_then_reverse_ABF_policies(t *testing.T) {
	// Given
	chain := testServiceChain()
	attachments := testServiceChainAttachments()

	// When
	operations, err := BuildServiceChainOperations("txn-chain", chain, attachments)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 4 {
		t.Fatalf("operation count = %d, want 4", len(operations))
	}
	wantDirections := []orchestrator.ServiceChainDirection{orchestrator.ServiceChainForward, orchestrator.ServiceChainForward, orchestrator.ServiceChainReverse, orchestrator.ServiceChainReverse}
	gotDirections := make([]orchestrator.ServiceChainDirection, 0, len(operations))
	for _, operation := range operations {
		policy, ok := operation.Payload.(ServiceChainPolicy)
		if !ok {
			t.Fatalf("payload = %T, want ServiceChainPolicy", operation.Payload)
		}
		gotDirections = append(gotDirections, policy.Direction)
		if !strings.Contains(strings.Join(operation.VPPCtlCommands, "\n"), "abf policy add") || !strings.Contains(strings.Join(operation.VPPCtlCommands, "\n"), "show interface") {
			t.Fatalf("commands = %#v, want apply and typed readback", operation.VPPCtlCommands)
		}
	}
	if !reflect.DeepEqual(gotDirections, wantDirections) {
		t.Fatalf("directions = %#v, want %#v", gotDirections, wantDirections)
	}
	first := operations[0].Payload.(ServiceChainPolicy)
	last := operations[3].Payload.(ServiceChainPolicy)
	if first.IngressInterface != "vpp-wan0" || first.ServiceInterface != "vpp-a-wan" || first.NextHop != "198.18.1.2" {
		t.Fatalf("first policy = %#v", first)
	}
	if last.IngressInterface != "vpp-b-wan" || last.ServiceInterface != "vpp-a-lan" || last.NextHop != "198.18.2.2" {
		t.Fatalf("last policy = %#v", last)
	}
}

func TestBuildServiceChainOperations_rejects_unproven_or_unsupported_attachment(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(NativeAttachment) NativeAttachment
	}{
		{name: "forged attachment", mutate: func(attachment NativeAttachment) NativeAttachment {
			return NativeAttachment{LinuxInterface: attachment.LinuxInterface, VPPInterface: attachment.VPPInterface, Hook: attachment.Hook, Mode: attachment.Mode}
		}},
		{name: "copy mode", mutate: func(attachment NativeAttachment) NativeAttachment {
			attachment.Mode = NativeModeCopy
			return attachment
		}},
		{name: "unsupported hook", mutate: func(attachment NativeAttachment) NativeAttachment {
			attachment.Hook = NativeHook("forged")
			return attachment
		}},
		{name: "altered mapping", mutate: func(attachment NativeAttachment) NativeAttachment {
			attachment.VPPInterface += "-forged"
			return attachment
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Given
			attachments := testServiceChainAttachments()
			attachments[0] = test.mutate(attachments[0])

			// When
			_, err := BuildServiceChainOperations("txn-chain", testServiceChain(), attachments)

			// Then
			if !errors.Is(err, ErrServiceChainCapability) {
				t.Fatalf("error = %v, want ErrServiceChainCapability", err)
			}
		})
	}
}

func TestBuildServiceChainOperations_rejects_unproven_interface(t *testing.T) {
	// Given
	attachments := testServiceChainAttachments()
	attachments = attachments[:len(attachments)-1]

	// When
	_, err := BuildServiceChainOperations("txn-chain", testServiceChain(), attachments)

	// Then
	if !errors.Is(err, ErrServiceChainCapability) {
		t.Fatalf("error = %v, want ErrServiceChainCapability", err)
	}
}

func TestBuildServiceChainOperations_direct_flow_has_no_ABF_state(t *testing.T) {
	// Given
	chain := testServiceChain()
	chain.Direct = true
	chain.Forward.Hops = nil
	chain.Reverse.Hops = nil

	// When
	operations, err := BuildServiceChainOperations("txn-direct", chain, testServiceChainAttachments())

	// Then
	if err != nil || len(operations) != 0 {
		t.Fatalf("direct operations = %#v, error = %v", operations, err)
	}
}

func TestDecodeServiceChainReadback_returns_typed_policy_and_interface_counters(t *testing.T) {
	// Given
	operations, err := BuildServiceChainOperations("txn-chain", testServiceChain(), testServiceChainAttachments())
	if err != nil {
		t.Fatal(err)
	}
	results := stockServiceChainResults(operations)

	// When
	readback, err := DecodeServiceChainReadback(testServiceChain(), operations, results)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(readback.Policies) != 4 || len(readback.Interfaces) != 4 {
		t.Fatalf("readback = %#v", readback)
	}
	if readback.Interfaces[0].RXPackets != 10 || readback.Interfaces[0].TXBytes != 900 {
		t.Fatalf("counter readback = %#v", readback.Interfaces[0])
	}
}

func TestDecodeServiceChainReadback_rejects_missing_reverse_policy(t *testing.T) {
	// Given
	operations, err := BuildServiceChainOperations("txn-chain", testServiceChain(), testServiceChainAttachments())
	if err != nil {
		t.Fatal(err)
	}
	operations = operations[:2]

	// When
	_, err = DecodeServiceChainReadback(testServiceChain(), operations, nil)

	// Then
	if !errors.Is(err, ErrServiceChainReadback) {
		t.Fatalf("error = %v, want ErrServiceChainReadback", err)
	}
}

func TestApplyServiceChain_executes_commands_and_returns_typed_readback(t *testing.T) {
	// Given
	chain := testServiceChain()
	attachments := testServiceChainAttachments()
	operations, err := BuildServiceChainOperations("txn-apply-chain", chain, attachments)
	if err != nil {
		t.Fatal(err)
	}
	responses := make(map[string]fakeVPPResponse)
	for _, operation := range operations {
		policy := operation.Payload.(ServiceChainPolicy)
		for _, command := range operation.VPPCtlCommands {
			command = strings.TrimPrefix(command, "?")
			responses[command] = fakeVPPResponse{}
		}
		responses[policyShowCommand(policy)] = fakeVPPResponse{stdout: stockServiceChainABFPolicy(policy)}
		responses[attachmentShowCommand(policy)] = fakeVPPResponse{stdout: stockServiceChainAttachment(policy)}
		responses["show interface "+policy.IngressInterface] = fakeVPPResponse{stdout: stockServiceChainInterface(policy)}
		responses["show acl-plugin acl index "+strconv.Itoa(policy.ACLID)] = fakeVPPResponse{stdout: stockServiceChainACL(policy)}
	}
	adapter := Adapter{Client: NewVPPCTLClient(writeFakeVPPCTL(t, responses))}

	// When
	result, err := adapter.ApplyServiceChain(context.Background(), "txn-apply-chain", chain, attachments)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.RequestID != "txn-apply-chain" || len(result.Receipt.Operations) != 4 || len(result.Readback.Policies) != 4 {
		t.Fatalf("apply result = %#v", result)
	}
}

func testServiceChain() orchestrator.ServiceChain {
	forward := orchestrator.FlowTuple{SourceIP: "198.18.0.2", DestinationIP: "198.18.5.2", Protocol: orchestrator.ProtocolTCP, SourcePort: 41000, DestinationPort: 443}
	reverse := orchestrator.FlowTuple{SourceIP: forward.DestinationIP, DestinationIP: forward.SourceIP, Protocol: forward.Protocol, SourcePort: forward.DestinationPort, DestinationPort: forward.SourcePort}
	return orchestrator.ServiceChain{ID: "chain-1", Forward: orchestrator.ServiceChainPath{Direction: orchestrator.ServiceChainForward, Match: forward, IngressInterface: "wan0", ExitInterface: "lan0", Hops: []orchestrator.ServiceChainHop{{Position: 1, Group: "group-a", IngressInterface: "wan0", ServiceInterface: "a-wan", ReturnInterface: "a-lan", NextHop: "198.18.1.2"}, {Position: 2, Group: "group-b", IngressInterface: "a-lan", ServiceInterface: "b-wan", ReturnInterface: "b-lan", NextHop: "198.18.3.2"}}}, Reverse: orchestrator.ServiceChainPath{Direction: orchestrator.ServiceChainReverse, Match: reverse, IngressInterface: "lan0", ExitInterface: "wan0", Hops: []orchestrator.ServiceChainHop{{Position: 1, Group: "group-b", IngressInterface: "lan0", ServiceInterface: "b-lan", ReturnInterface: "b-wan", NextHop: "198.18.4.2"}, {Position: 2, Group: "group-a", IngressInterface: "b-wan", ServiceInterface: "a-lan", ReturnInterface: "a-wan", NextHop: "198.18.2.2"}}}}
}

func testServiceChainAttachments() []NativeAttachment {
	names := []string{"wan0", "lan0", "a-wan", "a-lan", "b-wan", "b-lan"}
	attachments := make([]NativeAttachment, 0, len(names))
	for _, name := range names {
		attachments = append(attachments, proveNativeAttachment(NativeAttachment{LinuxInterface: name, VPPInterface: "vpp-" + name, Hook: NativeHookAFXDP, Mode: NativeModeZeroCopy}))
	}
	return attachments
}
