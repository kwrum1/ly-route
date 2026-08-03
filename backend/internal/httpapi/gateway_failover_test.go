package httpapi

import (
	"testing"
	"time"
)

func TestGatewayTrafficTrendKeepsFourSeriesStableThroughWANGroupFailover(t *testing.T) {
	start := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	now := start
	collector := &scriptedGatewayTelemetry{snapshots: []GatewayTelemetrySnapshot{
		failoverSnapshot(start, "wan-a", 1_000),
		failoverSnapshot(start.Add(5*time.Minute), "wan-b", 31_000),
	}}
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(func() time.Time { return now }), WithGatewayTelemetry(collector))
	api := authenticatedHTTPClient(t, server)

	first := api.trend(t)
	now = start.Add(5 * time.Minute)
	second := api.trend(t)

	wantKinds := map[string]LogicalEgressKind{
		"group-primary": LogicalEgressWANGroup,
		"proxy-media":   LogicalEgressProxy,
		"proxy-work":    LogicalEgressProxy,
		"wan-direct":    LogicalEgressDirectWAN,
	}
	if len(first.Series.LogicalEgresses) != len(wantKinds) || len(second.Series.LogicalEgresses) != len(wantKinds) {
		t.Fatalf("logical series counts = %d then %d, want four stable series", len(first.Series.LogicalEgresses), len(second.Series.LogicalEgresses))
	}
	for _, series := range second.Series.LogicalEgresses {
		if series.Kind != wantKinds[series.ID] {
			t.Fatalf("logical series %q kind = %q, want stable %q", series.ID, series.Kind, wantKinds[series.ID])
		}
	}
	group := logicalSeriesByID(t, second.Series.LogicalEgresses, "group-primary")
	if group.ActiveMember != "wan-b" || len(group.Samples) != 2 {
		t.Fatalf("WAN group continuity = %#v, want stable history after failover", group)
	}
	wantDownload := float64((30_000 + 60_000 + 48_000 + 24_000) * 8 / 300)
	wantUpload := float64((15_000 + 30_000 + 24_000 + 12_000) * 8 / 300)
	assertWithinFivePercent(t, second.Totals.DownloadBPS, wantDownload)
	assertWithinFivePercent(t, second.Totals.UploadBPS, wantUpload)
}

func failoverSnapshot(observedAt time.Time, activeMember string, directDownload int64) GatewayTelemetrySnapshot {
	return GatewayTelemetrySnapshot{ObservedAt: observedAt, LogicalEgresses: []LogicalEgressCounter{
		{ID: "wan-direct", Name: "Direct WAN", Kind: LogicalEgressDirectWAN, Health: "healthy", DownloadBytes: directDownload, UploadBytes: directDownload / 2},
		{ID: "group-primary", Name: "Primary Group", Kind: LogicalEgressWANGroup, Health: "healthy", ActiveMember: activeMember, DownloadBytes: directDownload * 2, UploadBytes: directDownload},
		{ID: "proxy-media", Name: "Media Proxy", Kind: LogicalEgressProxy, Health: "healthy", UnderlayWANID: "wan-a", ActiveMember: "node-media", DownloadBytes: directDownload * 8 / 5, UploadBytes: directDownload * 4 / 5},
		{ID: "proxy-work", Name: "Work Proxy", Kind: LogicalEgressProxy, Health: "healthy", UnderlayWANID: "wan-b", ActiveMember: "node-work", DownloadBytes: directDownload * 4 / 5, UploadBytes: directDownload * 2 / 5},
	}}
}
