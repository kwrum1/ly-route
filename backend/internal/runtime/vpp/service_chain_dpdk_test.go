package vpp

import "testing"

func TestBuildServiceChainOperationsAcceptsSelectedDPDKTier(t *testing.T) {
	attachments := testServiceChainAttachments()
	for index := range attachments {
		attachments[index].Tier = DataplaneTierDPDK
		attachments[index].Hook = NativeHookDPDK
		attachments[index].Mode = NativeModeDPDKVFIO
		attachments[index].PCIAddress = "0000:03:0" + string(rune('0'+index)) + ".0"
		attachments[index].IOMMUGroup = "17"
		attachments[index] = proveNativeAttachment(attachments[index])
	}
	operations, err := BuildServiceChainOperations("txn-dpdk-chain", testServiceChain(), attachments)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) == 0 {
		t.Fatal("DPDK-selected service chain produced no operations")
	}
}

func TestBuildServiceChainOperationsAcceptsSelectedDPDKUIOTier(t *testing.T) {
	attachments := testServiceChainAttachments()
	for index := range attachments {
		attachments[index].Tier = DataplaneTierDPDK
		attachments[index].Hook = NativeHookDPDK
		attachments[index].Mode = NativeModeDPDKUIO
		attachments[index].PCIAddress = "0000:03:0" + string(rune('0'+index)) + ".0"
		attachments[index].IOMMUGroup = "none"
		attachments[index] = proveNativeAttachment(attachments[index])
	}
	operations, err := BuildServiceChainOperations("txn-dpdk-uio-chain", testServiceChain(), attachments)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) == 0 {
		t.Fatal("DPDK UIO-selected service chain produced no operations")
	}
}
