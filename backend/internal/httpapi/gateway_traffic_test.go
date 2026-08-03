package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type scriptedGatewayTelemetry struct {
	snapshots []GatewayTelemetrySnapshot
	errors    []error
	calls     int
}

func (collector *scriptedGatewayTelemetry) Collect(context.Context) (GatewayTelemetrySnapshot, error) {
	index := collector.calls
	collector.calls++
	if index < len(collector.errors) && collector.errors[index] != nil {
		return GatewayTelemetrySnapshot{}, collector.errors[index]
	}
	if index >= len(collector.snapshots) {
		return GatewayTelemetrySnapshot{}, errors.New("collector stopped")
	}
	return collector.snapshots[index], nil
}

func TestGatewayTrafficTrendReconcilesLogicalCountersWithoutUnderlayDoubleCount(t *testing.T) {
	start := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	now := start
	collector := &scriptedGatewayTelemetry{snapshots: []GatewayTelemetrySnapshot{
		logicalSnapshot(start, 1_000, 2_000),
		logicalSnapshot(start.Add(5*time.Minute), 31_000, 62_000),
	}}
	server := New(
		WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}),
		WithClock(func() time.Time { return now }),
		WithGatewayTelemetry(collector),
	)
	api := authenticatedHTTPClient(t, server)

	first := api.trend(t)
	if len(first.Series.LogicalEgresses) != 4 {
		t.Fatalf("first logical series = %d, want 4: %#v", len(first.Series.LogicalEgresses), first.Series.LogicalEgresses)
	}
	wantKinds := map[string]LogicalEgressKind{
		"proxy-media": LogicalEgressProxy,
		"proxy-work":  LogicalEgressProxy,
		"wan-a":       LogicalEgressDirectWAN,
		"wan-b":       LogicalEgressDirectWAN,
	}
	directWANs, proxies := 0, 0
	for _, series := range first.Series.LogicalEgresses {
		if wantKinds[series.ID] != series.Kind {
			t.Fatalf("first logical series %q kind = %q, want %q", series.ID, series.Kind, wantKinds[series.ID])
		}
		if series.Kind == LogicalEgressDirectWAN {
			directWANs++
		}
		if series.Kind == LogicalEgressProxy {
			proxies++
		}
	}
	if directWANs != 2 || proxies != 2 {
		t.Fatalf("logical series kinds = %#v, want two direct WANs and two proxies", first.Series.LogicalEgresses)
	}
	now = start.Add(5 * time.Minute)
	second := api.trend(t)
	for _, series := range second.Series.LogicalEgresses {
		if wantKinds[series.ID] != series.Kind {
			t.Fatalf("second logical series %q kind = %q, want stable %q", series.ID, series.Kind, wantKinds[series.ID])
		}
	}

	wantDownload := float64((30_000 + 60_000 + 60_000 + 48_000) * 8 / 300)
	wantUpload := float64((15_000 + 30_000 + 30_000 + 24_000) * 8 / 300)
	assertWithinFivePercent(t, second.Totals.DownloadBPS, wantDownload)
	assertWithinFivePercent(t, second.Totals.UploadBPS, wantUpload)
	if second.Totals.DownloadBPS >= wantDownload+float64(30_000*8/300) {
		t.Fatalf("download total %.2f includes proxy underlay traffic", second.Totals.DownloadBPS)
	}
	proxy := logicalSeriesByID(t, second.Series.LogicalEgresses, "proxy-media")
	if proxy.UnderlayWANID != "wan-a" {
		t.Fatalf("proxy underlay = %q, want metadata-only wan-a", proxy.UnderlayWANID)
	}
}

func TestGatewayTrafficTrendReplacesDuplicateTimestampWithLatestReadback(t *testing.T) {
	observedAt := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	collector := &scriptedGatewayTelemetry{snapshots: []GatewayTelemetrySnapshot{
		{ObservedAt: observedAt, LogicalEgresses: []LogicalEgressCounter{{ID: "wan-a", Name: "WAN A", Kind: LogicalEgressDirectWAN, Health: "healthy", DownloadBytes: 1_000, UploadBytes: 500}}},
		{ObservedAt: observedAt, LogicalEgresses: []LogicalEgressCounter{{ID: "wan-a", Name: "WAN A", Kind: LogicalEgressDirectWAN, Health: "healthy", DownloadBytes: 2_000, UploadBytes: 1_000}}},
	}}
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(func() time.Time { return observedAt }), WithGatewayTelemetry(collector))
	api := authenticatedHTTPClient(t, server)

	api.trend(t)
	result := api.trend(t)

	samples := logicalSeriesByID(t, result.Series.LogicalEgresses, "wan-a").Samples
	if len(samples) != 1 || samples[0].DownloadBytes != 2_000 || samples[0].UploadBytes != 1_000 {
		t.Fatalf("duplicate timestamp samples = %#v, want latest readback replacement", samples)
	}
}

func TestGatewayTrafficTrendExcludesUnhealthySeriesFromFreshTotals(t *testing.T) {
	start := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	now := start
	collector := &scriptedGatewayTelemetry{snapshots: []GatewayTelemetrySnapshot{
		{ObservedAt: start, LogicalEgresses: []LogicalEgressCounter{{ID: "wan-a", Name: "WAN A", Kind: LogicalEgressDirectWAN, Health: "healthy", DownloadBytes: 1_000, UploadBytes: 500}}},
		{ObservedAt: start.Add(5 * time.Minute), LogicalEgresses: []LogicalEgressCounter{{ID: "wan-a", Name: "WAN A", Kind: LogicalEgressDirectWAN, Health: "failed", DownloadBytes: 31_000, UploadBytes: 15_500}}},
	}}
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(func() time.Time { return now }), WithGatewayTelemetry(collector))
	api := authenticatedHTTPClient(t, server)

	api.trend(t)
	now = start.Add(5 * time.Minute)
	result := api.trend(t)

	series := logicalSeriesByID(t, result.Series.LogicalEgresses, "wan-a")
	if series.State != "failed" || series.Fresh {
		t.Fatalf("unhealthy series state = %q fresh=%t, want failed and not fresh", series.State, series.Fresh)
	}
	if result.Totals.DownloadBPS != 0 || result.Totals.UploadBPS != 0 {
		t.Fatalf("unhealthy totals = %#v, want zero", result.Totals)
	}
}

func TestGatewayTrafficTrendPreservesGapsAndPrunesBeyond24Hours(t *testing.T) {
	start := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	now := start
	collector := &scriptedGatewayTelemetry{
		snapshots: []GatewayTelemetrySnapshot{
			logicalSnapshot(start, 1_000, 2_000),
			{},
			logicalSnapshot(start.Add(10*time.Minute), 61_000, 122_000),
			logicalSnapshot(start.Add(25*time.Hour), 121_000, 242_000),
		},
		errors: []error{nil, errors.New("sample unavailable")},
	}
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}), WithClock(func() time.Time { return now }), WithGatewayTelemetry(collector))
	api := authenticatedHTTPClient(t, server)

	api.trend(t)
	now = start.Add(5 * time.Minute)
	stale := api.trend(t)
	if !stale.Degraded || stale.Series.LogicalEgresses[0].State != "stale" {
		t.Fatalf("failed collection response = %#v, want stale retained data", stale)
	}
	now = start.Add(10 * time.Minute)
	gap := api.trend(t)
	series := logicalSeriesByID(t, gap.Series.LogicalEgresses, "wan-a")
	if len(series.Samples) != 2 || series.Samples[0].Timestamp.Add(5*time.Minute).Equal(series.Samples[1].Timestamp) {
		t.Fatalf("samples = %#v, want missing five-minute timestamp to remain a gap", series.Samples)
	}
	now = start.Add(25 * time.Hour)
	pruned := api.trend(t)
	if samples := logicalSeriesByID(t, pruned.Series.LogicalEgresses, "wan-a").Samples; len(samples) != 1 || !samples[0].Timestamp.Equal(now) {
		t.Fatalf("retained samples = %#v, want rolling 24-hour window", samples)
	}
}

func TestGatewayTrafficTrendReportsUnavailableBeforeFirstSuccessfulSample(t *testing.T) {
	server := New(WithAuthConfig(AuthConfig{AdminUsername: "admin", AdminPassword: "secret"}))
	api := authenticatedHTTPClient(t, server)

	result := api.trend(t)

	if !result.Degraded || result.State != "unavailable" || len(result.Series.LogicalEgresses) != 0 {
		t.Fatalf("empty collector result = %#v, want explicit unavailable state", result)
	}
}

func logicalSnapshot(observedAt time.Time, wanDownload, proxyDownload int64) GatewayTelemetrySnapshot {
	return GatewayTelemetrySnapshot{ObservedAt: observedAt, LogicalEgresses: []LogicalEgressCounter{
		{ID: "wan-a", Name: "WAN A", Kind: LogicalEgressDirectWAN, Health: "healthy", DownloadBytes: wanDownload, UploadBytes: wanDownload / 2},
		{ID: "wan-b", Name: "WAN B", Kind: LogicalEgressDirectWAN, Health: "healthy", DownloadBytes: wanDownload * 2, UploadBytes: wanDownload},
		{ID: "proxy-media", Name: "Media Proxy", Kind: LogicalEgressProxy, Health: "healthy", UnderlayWANID: "wan-a", ActiveMember: "node-media", DownloadBytes: proxyDownload, UploadBytes: proxyDownload / 2},
		{ID: "proxy-work", Name: "Work Proxy", Kind: LogicalEgressProxy, Health: "healthy", UnderlayWANID: "wan-b", ActiveMember: "node-work", DownloadBytes: proxyDownload * 4 / 5, UploadBytes: proxyDownload * 2 / 5},
	}}
}

type trafficTrendHTTPResult struct {
	State    string `json:"state"`
	Degraded bool   `json:"degraded"`
	Totals   struct {
		DownloadBPS float64 `json:"download_bps"`
		UploadBPS   float64 `json:"upload_bps"`
	} `json:"totals"`
	Series struct {
		LogicalEgresses []LogicalEgressSeries `json:"logical_egresses"`
	} `json:"series"`
}

type authenticatedTelemetryAPI struct {
	client  *http.Client
	baseURL string
	cookie  *http.Cookie
}

func (api authenticatedTelemetryAPI) trend(t *testing.T) trafficTrendHTTPResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, api.baseURL+"/api/v1/telemetry/traffic-trend?window=24h&points=288", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(api.cookie)
	res, err := api.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var result trafficTrendHTTPResult
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func authenticatedHTTPClient(t *testing.T, server *Server) authenticatedTelemetryAPI {
	t.Helper()
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	client := httpServer.Client()
	res, err := client.Post(httpServer.URL+"/api/v1/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || len(res.Cookies()) == 0 {
		t.Fatalf("login status = %d, cookies = %d", res.StatusCode, len(res.Cookies()))
	}
	return authenticatedTelemetryAPI{client: client, baseURL: httpServer.URL, cookie: res.Cookies()[0]}
}

func logicalSeriesByID(t *testing.T, series []LogicalEgressSeries, id string) LogicalEgressSeries {
	t.Helper()
	for _, item := range series {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("logical series %q not found in %#v", id, series)
	return LogicalEgressSeries{}
}

func assertWithinFivePercent(t *testing.T, got, want float64) {
	t.Helper()
	if got < want*0.95 || got > want*1.05 {
		t.Fatalf("counter rate = %.2f, want %.2f within 5%%", got, want)
	}
}
