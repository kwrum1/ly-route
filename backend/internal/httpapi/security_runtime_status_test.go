package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestSecurityRuntimeStatusFailsClosedAndIsReadOnly(t *testing.T) {
	server := New(WithClock(fixedClock()))
	response := request(t, server, http.MethodGet, "/api/v1/security/runtime")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"runtime_state":"locked"`) || !strings.Contains(response.Body.String(), `"rules":[]`) {
		t.Fatalf("security runtime = %d: %s", response.Code, response.Body.String())
	}
	mutation := request(t, server, http.MethodPost, "/api/v1/security/runtime")
	if mutation.Code != http.StatusMethodNotAllowed || !strings.Contains(mutation.Body.String(), "read-only") {
		t.Fatalf("security runtime mutation = %d: %s", mutation.Code, mutation.Body.String())
	}
}

func TestSecurityRuntimeStatusReportsOnlyObservedVPPState(t *testing.T) {
	now := fixedClock()().UTC()
	server := New(WithClock(fixedClock()), WithSecurityGuardRuntime(fakeSecurityGuardRuntime{rules: []SecurityGuardRuntimeRule{
		{ID: "rule-z", Interface: "wan0", Family: 6, Enabled: true, ThresholdPPS: 100, BurstPackets: 20, Matched: 30, Conform: 20, Exceeded: 10, Alerts: 10, ObservedAt: now},
		{ID: "rule-a", Interface: "lan0", Family: 4, Enabled: true, ThresholdPPS: 50, BurstPackets: 10, Matched: 15, Conform: 10, Exceeded: 5, Dropped: 5, ObservedAt: now},
	}}))
	response := request(t, server, http.MethodGet, "/api/v1/security/runtime")
	if response.Code != http.StatusOK {
		t.Fatalf("security runtime = %d: %s", response.Code, response.Body.String())
	}
	for _, required := range []string{`"runtime_state":"running"`, `"implementation":"ly_route_security_guard"`, `"id":"rule-a"`, `"dropped":5`, `"alerts":10`} {
		if !strings.Contains(response.Body.String(), required) {
			t.Fatalf("security runtime missing %q: %s", required, response.Body.String())
		}
	}
	if strings.Index(response.Body.String(), `"id":"rule-a"`) > strings.Index(response.Body.String(), `"id":"rule-z"`) {
		t.Fatalf("security runtime rules are not deterministic: %s", response.Body.String())
	}
	degraded := New(WithClock(fixedClock()), WithSecurityGuardRuntime(fakeSecurityGuardRuntime{err: errors.New("VPP socket unavailable")}))
	response = request(t, degraded, http.MethodGet, "/api/v1/security/runtime")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"runtime_state":"degraded"`) || !strings.Contains(response.Body.String(), "VPP socket unavailable") {
		t.Fatalf("degraded security runtime = %d: %s", response.Code, response.Body.String())
	}
}

type fakeSecurityGuardRuntime struct {
	rules []SecurityGuardRuntimeRule
	err   error
}

func (observer fakeSecurityGuardRuntime) SecurityGuardRules(context.Context) ([]SecurityGuardRuntimeRule, error) {
	if observer.err != nil {
		return nil, observer.err
	}
	return observer.rules, nil
}
