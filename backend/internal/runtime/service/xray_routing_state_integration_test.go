package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestXrayBalancerStateIntegration(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv("LY_ROUTE_XRAY_BALANCER_API_BINARY"))
	if binary == "" {
		t.Skip("LY_ROUTE_XRAY_BALANCER_API_BINARY is not set")
	}
	collector := FilesystemController{Runner: xrayBinaryRunner{binary: binary}}
	states, err := collector.XrayBalancerStates(context.Background(), []string{"subscription-main-fastest"})
	if os.Getenv("LY_ROUTE_XRAY_EXPECT_UNAVAILABLE") == "1" {
		if err == nil || len(states) != 0 || !strings.Contains(err.Error(), "no healthy selected outbound") {
			t.Fatalf("failed Xray balancer did not fail closed: states=%#v err=%v", states, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.TrimSpace(os.Getenv("LY_ROUTE_XRAY_EXPECT_SELECTED"))
	if len(states) != 1 || len(states[0].SelectedOutboundTags) != 1 || states[0].SelectedOutboundTags[0] != expected {
		t.Fatalf("live Xray selection = %#v, want %q", states, expected)
	}
}

type xrayBinaryRunner struct{ binary string }

func (runner xrayBinaryRunner) Run(context.Context, string, ...string) error {
	return errors.New("run is not supported by Xray state integration runner")
}

func (runner xrayBinaryRunner) Output(ctx context.Context, name string, args ...string) (string, error) {
	if name != "xray" {
		return "", errors.New("unexpected integration readback command")
	}
	output, err := exec.CommandContext(ctx, runner.binary, args...).CombinedOutput()
	return string(output), err
}

func (runner xrayBinaryRunner) Status(context.Context, ServiceName) (Health, error) {
	return Health{}, errors.New("status is not supported by Xray state integration runner")
}
