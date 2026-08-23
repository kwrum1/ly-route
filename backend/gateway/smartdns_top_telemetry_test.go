package gateway

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSmartDNSAuditTelemetryAggregatesDomains(t *testing.T) {
	path := filepath.Join(t.TempDir(), "smartdns-audit.log")
	content := "[2026-08-23 10:00:00,001] 192.168.50.10 query www.example.com, type 1, time 2ms, result\n" +
		"[2026-08-23 10:00:01,002] 192.168.50.10 query www.example.com, type 28, time 3ms, result\n" +
		"[2026-08-23 10:00:02,003] 192.168.50.10 query cloudflare.com., type 1, time 4ms, result\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	collector := newSmartDNSAuditTelemetry(path)
	collector.now = func() time.Time { return time.Date(2026, 8, 23, 10, 1, 0, 0, time.Local) }
	items, err := collector.TopDomains(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0]["domain"] != "www.example.com" || items[0]["count"] != 2 || items[1]["domain"] != "cloudflare.com" {
		t.Fatalf("items = %#v", items)
	}
}
