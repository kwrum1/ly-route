package gateway

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ly-route/backend/internal/httpapi"
)

type productionSecurityGuardObserver struct {
	binary string
	now    func() time.Time
	run    func(context.Context, string, ...string) (string, error)
}

func (observer productionSecurityGuardObserver) SecurityGuardRules(ctx context.Context) ([]httpapi.SecurityGuardRuntimeRule, error) {
	binary := strings.TrimSpace(observer.binary)
	if binary == "" {
		return nil, fmt.Errorf("VPP CLI binary is not configured")
	}
	run := observer.run
	if run == nil {
		run = runVPPCTLTelemetryCommand
	}
	output, err := run(ctx, binary, "show", "ly-route", "security-guard")
	if err != nil {
		return nil, err
	}
	now := time.Now
	if observer.now != nil {
		now = observer.now
	}
	return parseSecurityGuardRuntime(output, now().UTC())
}

func parseSecurityGuardRuntime(output string, observedAt time.Time) ([]httpapi.SecurityGuardRuntimeRule, error) {
	rules := []httpapi.SecurityGuardRuntimeRule{}
	for _, raw := range strings.Split(output, "\n") {
		fields := strings.Fields(raw)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 22 || fields[0] != "rule" || fields[2] != "enabled" || fields[4] != "family" || fields[6] != "interface" || fields[8] != "threshold-pps" || fields[10] != "burst-packets" || fields[12] != "matched" || fields[14] != "conform" || fields[16] != "exceed" || fields[18] != "alerts" || fields[20] != "drops" {
			return nil, fmt.Errorf("invalid VPP security-guard readback line %q", strings.TrimSpace(raw))
		}
		family, err := strconv.Atoi(fields[5])
		if err != nil || (family != 4 && family != 6) {
			return nil, fmt.Errorf("invalid VPP security-guard family for %q", fields[1])
		}
		enabled, err := strconv.ParseUint(fields[3], 10, 8)
		if err != nil || enabled > 1 {
			return nil, fmt.Errorf("invalid VPP security-guard enabled state for %q", fields[1])
		}
		values := make([]uint64, 0, 7)
		for _, index := range []int{9, 11, 13, 15, 17, 19, 21} {
			value, parseErr := strconv.ParseUint(fields[index], 10, 64)
			if parseErr != nil {
				return nil, fmt.Errorf("invalid VPP security-guard counter for %q", fields[1])
			}
			values = append(values, value)
		}
		if values[0] > uint64(^uint(0)>>1) || values[1] > uint64(^uint(0)>>1) {
			return nil, fmt.Errorf("VPP security-guard rate exceeds API integer range for %q", fields[1])
		}
		rules = append(rules, httpapi.SecurityGuardRuntimeRule{ID: fields[1], Interface: fields[7], Family: family, Enabled: enabled == 1, ThresholdPPS: int(values[0]), BurstPackets: int(values[1]), Matched: values[2], Conform: values[3], Exceeded: values[4], Alerts: values[5], Dropped: values[6], ObservedAt: observedAt})
	}
	return rules, nil
}
