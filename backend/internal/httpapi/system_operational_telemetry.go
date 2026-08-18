package httpapi

import (
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	controlapi "ly-route/backend/internal/api"
)

var systemOperationalReadFile = os.ReadFile

func (server *Server) handleDashboardSummaryTelemetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if _, ok := server.sessionFromRequest(r); !ok {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	dependencies := server.capabilities(r.Context())
	system, systemCapability := readSystemSummary()
	facts, factsCapability := readSystemOperationalFacts(server.now(), systemOperationalReadFile)
	for key, value := range facts {
		system[key] = value
	}

	degraded := !systemCapability.Available || !factsCapability.Available
	for _, dependency := range dependencies {
		if requiredCapability(dependency.Name) && !dependency.Available {
			degraded = true
			break
		}
	}
	status := "ok"
	if degraded {
		status = "degraded"
	}
	capabilities := append([]controlapi.CapabilityState{}, dependencies...)
	capabilities = append(capabilities, systemCapability, factsCapability)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": status, "degraded": degraded, "dependencies": dependencies,
		"system": system, "capabilities": capabilities, "request_id": requestID(r),
	})
}

func readSystemOperationalFacts(now time.Time, readFile func(string) ([]byte, error)) (map[string]any, controlapi.CapabilityState) {
	facts := map[string]any{
		"system_time":    now.Format(time.RFC3339),
		"platform":       systemPlatform(readFile),
		"uptime_seconds": int64(0),
	}
	uptime, err := systemUptime(readFile)
	if err != nil {
		return facts, controlapi.CapabilityState{
			Name: "system_operational_facts", Available: false,
			State: controlapi.CapabilityDegraded, Reason: err.Error(),
		}
	}
	facts["uptime_seconds"] = uptime
	return facts, controlapi.CapabilityState{Name: "system_operational_facts", Available: true, State: controlapi.CapabilityAvailable}
}

func systemUptime(readFile func(string) ([]byte, error)) (int64, error) {
	content, err := readFile("/proc/uptime")
	if err != nil {
		return 0, fmt.Errorf("/proc/uptime unavailable: %w", err)
	}
	fields := strings.Fields(string(content))
	if len(fields) == 0 {
		return 0, fmt.Errorf("/proc/uptime is empty")
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || seconds < 0 {
		return 0, fmt.Errorf("/proc/uptime has invalid uptime %q", fields[0])
	}
	return int64(seconds), nil
}

func systemPlatform(readFile func(string) ([]byte, error)) string {
	for _, path := range []string{
		"/sys/devices/virtual/dmi/id/product_name",
		"/sys/firmware/devicetree/base/model",
		"/proc/device-tree/model",
	} {
		content, err := readFile(path)
		if err != nil {
			continue
		}
		if value := strings.Trim(strings.TrimSpace(string(content)), "\x00"); value != "" {
			return value
		}
	}
	return runtime.GOOS + "/" + runtime.GOARCH
}
