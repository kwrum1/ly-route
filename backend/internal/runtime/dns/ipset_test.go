package dns

import (
	"strings"
	"testing"
	"time"
)

func TestParseIPSetListValidatesNameAddressesAndKernelTTL(t *testing.T) {
	now := time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)
	members, err := ParseIPSetList(strings.Join([]string{
		"Name: lyroute_dns_fixed_wan",
		"Type: hash:ip",
		"Header: family inet timeout 600",
		"Members:",
		"203.0.113.53 timeout 42",
		"203.0.113.53 timeout 41",
	}, "\n"), "lyroute_dns_fixed_wan", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].IP != "203.0.113.53" || !members[0].ExpiresAt.Equal(now.Add(42*time.Second)) {
		t.Fatalf("members = %#v", members)
	}
}

func TestParseIPSetListRejectsSubstitutionAndMalformedTimeout(t *testing.T) {
	base := "Name: other\nMembers:\n203.0.113.53 timeout 42\n"
	if _, err := ParseIPSetList(base, "lyroute_dns_fixed_wan", time.Now()); err == nil {
		t.Fatal("accepted an unexpected IP set name")
	}
	malformed := "Name: lyroute_dns_fixed_wan\nMembers:\n203.0.113.53 timeout forever\n"
	if _, err := ParseIPSetList(malformed, "lyroute_dns_fixed_wan", time.Now()); err == nil {
		t.Fatal("accepted a malformed timeout")
	}
}
