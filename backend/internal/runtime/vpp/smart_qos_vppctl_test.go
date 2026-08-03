package vpp

import (
	"strings"
	"testing"
)

func TestValidateSmartQoSReadbackRequiresExactProductionContract(t *testing.T) {
	expected := SmartQoSInterface{VPPInterface: "lyroute-eth1", Role: "lan", RateKbps: 100000, HostIsolation: "destination"}
	valid := strings.Join([]string{
		"state running",
		"algorithm fq-codel",
		"qualification production",
		"scheduler-thread 1",
		"interface lyroute-eth1 enabled rate-kbps 100000 host-isolation destination queued 0 enqueued 1 transmitted 1 aqm-drops 0 overflow-drops 0",
	}, "\n")
	if err := validateSmartQoSReadback(expected, valid); err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string]string{
		"development qualification": strings.Replace(valid, "qualification production", "qualification development-multi-worker", 1),
		"wrong rate":                strings.Replace(valid, "rate-kbps 100000", "rate-kbps 99999", 1),
		"wrong isolation":           strings.Replace(valid, "host-isolation destination", "host-isolation source", 1),
		"wrong interface":           strings.Replace(valid, "lyroute-eth1", "lyroute-eth2", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateSmartQoSReadback(expected, mutation); err == nil {
				t.Fatalf("mutated readback unexpectedly passed: %s", mutation)
			}
		})
	}
}

func TestValidateSmartQoSDisabledReadbackRejectsStillEnabledInterface(t *testing.T) {
	expected := SmartQoSInterface{VPPInterface: "lyroute-eth1"}
	if err := validateSmartQoSDisabledReadback(expected, "state running\ninterface lyroute-eth2 enabled rate-kbps 1000 host-isolation source"); err != nil {
		t.Fatal(err)
	}
	if err := validateSmartQoSDisabledReadback(expected, "state running\ninterface lyroute-eth1 enabled rate-kbps 1000 host-isolation source"); err == nil {
		t.Fatal("still-enabled interface unexpectedly passed disable readback")
	}
}
