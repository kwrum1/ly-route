package vpp

import "testing"

func TestVerifyVPPCTLAbsenceAcceptsEmptyShowOutput(t *testing.T) {
	const command = "show ip fib table 86417"
	results := []VPPCTLCommandResult{{Command: command, Stdout: "\n"}}
	if err := verifyVPPCTLAbsence(results, []string{command}, "WAN group", "acceptance-dual-wan"); err != nil {
		t.Fatalf("empty VPP show output should prove absence: %v", err)
	}
}

func TestVerifyVPPCTLAbsenceRejectsLiveOutput(t *testing.T) {
	const command = "show ip fib table 86417"
	results := []VPPCTLCommandResult{{Command: command, Stdout: "ipv4-VRF:86417"}}
	if err := verifyVPPCTLAbsence(results, []string{command}, "WAN group", "acceptance-dual-wan"); err == nil {
		t.Fatal("live VPP show output must not prove absence")
	}
}
