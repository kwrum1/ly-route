package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestKeaMemfileLeaseCollectorReturnsOnlyLatestActiveUnexpiredRows(t *testing.T) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "kea-leases4.csv")
	content := strings.Join([]string{
		"address,hwaddr,client_id,valid_lifetime,expire,subnet_id,fqdn_fwd,fqdn_rev,hostname,state,user_context,pool_id",
		"192.0.2.12,02:00:00:00:00:12,secret-client,120,1785571199,1,0,0,expired,0,{secret},0",
		"192.0.2.11,02:00:00:00:00:11,secret-client,120,1785571320,1,0,0,released-old,0,{secret},0",
		"192.0.2.10,02:00:00:00:00:0A,secret-client,120,1785571320,1,0,0,active-client,0,{secret},0",
		"192.0.2.11,02:00:00:00:00:11,secret-client,120,1785571320,1,0,0,,2,{secret},0",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	collector := KeaMemfileLeaseCollector{Path: path, Now: func() time.Time { return now }}
	leases, err := collector.Leases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 1 {
		t.Fatalf("expected one active lease, got %#v", leases)
	}
	lease := leases[0]
	if lease["ip_address"] != "192.0.2.10" || lease["mac"] != "02:00:00:00:00:0a" || lease["hostname"] != "active-client" || lease["state"] != "active" {
		t.Fatalf("unexpected active lease: %#v", lease)
	}
	if lease["lease_start"] != "2026-08-01T08:00:00Z" || lease["lease_end"] != "2026-08-01T08:02:00Z" {
		t.Fatalf("unexpected lease interval: %#v", lease)
	}
	if _, leaked := lease["client_id"]; leaked {
		t.Fatalf("client identifier leaked: %#v", lease)
	}
	if _, leaked := lease["user_context"]; leaked {
		t.Fatalf("Kea user context leaked: %#v", lease)
	}
}

func TestKeaMemfileLeaseCollectorSortsAddresses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kea-leases4.csv")
	content := "address,hwaddr,valid_lifetime,expire,subnet_id,hostname,state\n192.0.2.20,,120,2000000000,1,,0\n192.0.2.3,,120,2000000000,1,,0\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	leases, err := (KeaMemfileLeaseCollector{Path: path, Now: func() time.Time { return time.Unix(1900000000, 0) }}).Leases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 2 || leases[0]["ip_address"] != "192.0.2.3" || leases[1]["ip_address"] != "192.0.2.20" {
		t.Fatalf("leases are not sorted by address: %#v", leases)
	}
}

func TestKeaMemfileLeaseCollectorFailsClosedOnMalformedInput(t *testing.T) {
	tests := map[string]string{
		"missing header": "address,hwaddr,valid_lifetime,expire,subnet_id,hostname\n",
		"invalid row":    "address,hwaddr,valid_lifetime,expire,subnet_id,hostname,state\nnot-an-ip,broken,120,2000000000,1,,0\n",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "kea-leases4.csv")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := (KeaMemfileLeaseCollector{Path: path}).Leases(context.Background()); err == nil {
				t.Fatal("expected malformed lease database to fail closed")
			}
		})
	}
}

func TestKeaMemfileLeaseCollectorReportsMissingFile(t *testing.T) {
	_, err := (KeaMemfileLeaseCollector{Path: filepath.Join(t.TempDir(), "missing.csv")}).Leases(context.Background())
	if err == nil || !strings.Contains(err.Error(), "read Kea DHCPv4 lease database") {
		t.Fatalf("expected explicit missing database error, got %v", err)
	}
}

func TestKeaMemfileLeaseCollectorIntegration(t *testing.T) {
	path := strings.TrimSpace(os.Getenv("LY_ROUTE_KEA_LEASE_INTEGRATION_FILE"))
	if path == "" {
		t.Skip("LY_ROUTE_KEA_LEASE_INTEGRATION_FILE is not set")
	}
	leases, err := (KeaMemfileLeaseCollector{Path: path}).Leases(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 1 || leases[0]["ip_address"] != "192.0.2.50" || leases[0]["mac"] != "02:00:00:00:00:02" {
		t.Fatalf("real Kea lease not read back: %#v", leases)
	}
}
