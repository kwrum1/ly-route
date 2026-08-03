package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGatewayHTTPTelemetryCollectsTypedSnapshotOverRealHTTP(t *testing.T) {
	observedAt := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	requestSeen := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.Header.Get("Accept") != "application/json" {
			t.Errorf("collector request = %s Accept=%q", request.Method, request.Header.Get("Accept"))
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Connection", "close")
		_, _ = fmt.Fprintf(writer, `{"observed_at":%q,"logical_egresses":[{"id":"wan-a","name":"WAN A","kind":"direct_wan","health":"healthy","download_bytes":1000,"upload_bytes":500}],"connections":[{"src_ip":"192.168.88.10","dst_ip":"8.8.8.8","protocol":"udp","src_port":53000,"dst_port":53,"bytes":4096}],"neighbors":[{"ip":"192.168.88.10","mac":"00:11:22:33:44:55","last_seen":%q,"download_bytes":100000,"upload_bytes":20000}]}`, observedAt.Format(time.RFC3339), observedAt.Format(time.RFC3339))
		requestSeen <- struct{}{}
	}))
	t.Cleanup(upstream.Close)
	collector, err := newGatewayHTTPTelemetry(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(collector.client.CloseIdleConnections)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	snapshot, err := collector.Collect(ctx)

	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.LogicalEgresses) != 1 || snapshot.LogicalEgresses[0].ID != "wan-a" || snapshot.LogicalEgresses[0].DownloadBytes != 1000 {
		t.Fatalf("logical egress snapshot = %#v", snapshot.LogicalEgresses)
	}
	if len(snapshot.Connections) != 1 || snapshot.Connections[0].Bytes != 4096 {
		t.Fatalf("connection snapshot = %#v", snapshot.Connections)
	}
	if len(snapshot.Neighbors) != 1 || snapshot.Neighbors[0].MAC != "00:11:22:33:44:55" {
		t.Fatalf("neighbor snapshot = %#v", snapshot.Neighbors)
	}
	select {
	case <-requestSeen:
	case <-ctx.Done():
		t.Fatal("upstream request did not complete")
	}
}

func TestGatewayHTTPTelemetryRejectsUnavailableAndMalformedSources(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "unavailable", status: http.StatusServiceUnavailable, body: `{}`},
		{name: "unknown field", status: http.StatusOK, body: `{"observed_at":"2026-07-29T08:00:00Z","unknown":true}`},
		{name: "multiple values", status: http.StatusOK, body: `{}` + "\n" + `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.body))
			}))
			t.Cleanup(upstream.Close)
			collector, err := newGatewayHTTPTelemetry(upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(collector.client.CloseIdleConnections)

			_, err = collector.Collect(context.Background())

			if err == nil {
				t.Fatal("Collect succeeded, want source rejection")
			}
		})
	}
}
