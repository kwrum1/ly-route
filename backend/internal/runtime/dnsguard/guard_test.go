package dnsguard

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestGuardReturnsNoErrorNoDataForUnmatchedAndUnavailableQueries(t *testing.T) {
	guard, err := New(Config{ListenAddress: "127.0.0.1:0", UpstreamAddress: "127.0.0.1:1", AllowedDomains: []string{"updates.example"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range []string{"unmatched.example", "updates.example"} {
		response := guard.Handle(dnsQuery(0x1234, domain), "udp")
		assertNODATA(t, response, 0x1234, domain)
	}
}

func TestGuardForwardsAllowedUDPDNSResponse(t *testing.T) {
	upstream, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		buffer := make([]byte, 1024)
		count, peer, readErr := upstream.ReadFrom(buffer)
		if readErr != nil {
			return
		}
		response := append([]byte(nil), buffer[:count]...)
		response[2] |= 0x80
		_, _ = upstream.WriteTo(response, peer)
	}()
	guard, err := New(Config{ListenAddress: "127.0.0.1:0", UpstreamAddress: upstream.LocalAddr().String(), AllowedDomains: []string{"updates.example"}})
	if err != nil {
		t.Fatal(err)
	}
	query := dnsQuery(0x5678, "cdn.updates.example")
	response := guard.Handle(query, "udp")
	if string(response) != string(append([]byte{0x56, 0x78, query[2] | 0x80}, query[3:]...)) {
		t.Fatalf("forwarded response does not preserve upstream reply: %x", response)
	}
}

func TestGuardRejectsUnsafeConfiguration(t *testing.T) {
	for _, config := range []Config{
		{UpstreamAddress: "127.0.0.1:53"},
		{ListenAddress: "127.0.0.1:0"},
		{ListenAddress: "127.0.0.1:0", UpstreamAddress: "127.0.0.1:53", AllowedDomains: []string{"bad domain"}},
	} {
		if _, err := New(config); err == nil {
			t.Fatalf("New(%#v) succeeded", config)
		}
	}
}

func TestGuardTimesOutAndReturnsNODATA(t *testing.T) {
	upstream, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	guard, err := New(Config{ListenAddress: "127.0.0.1:0", UpstreamAddress: upstream.LocalAddr().String(), AllowedDomains: []string{"updates.example"}, RequestTimeoutMillis: 25})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	response := guard.Handle(dnsQuery(0x9911, "updates.example"), "udp")
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("guard timeout took %s", elapsed)
	}
	assertNODATA(t, response, 0x9911, "updates.example")
}

func assertNODATA(t *testing.T, response []byte, id uint16, domain string) {
	t.Helper()
	if len(response) < 12 || binary.BigEndian.Uint16(response[:2]) != id || binary.BigEndian.Uint16(response[2:4])&0x000f != 0 || binary.BigEndian.Uint16(response[6:8]) != 0 {
		t.Fatalf("response for %s is not NOERROR/NODATA: %x", domain, response)
	}
	if got, err := queryDomain(response); err != nil || got != domain {
		t.Fatalf("NODATA question = %q, %v; want %q", got, err, domain)
	}
}

func dnsQuery(id uint16, domain string) []byte {
	message := make([]byte, 12)
	binary.BigEndian.PutUint16(message[:2], id)
	binary.BigEndian.PutUint16(message[2:4], 0x0100)
	binary.BigEndian.PutUint16(message[4:6], 1)
	for _, label := range splitDomain(domain) {
		message = append(message, byte(len(label)))
		message = append(message, label...)
	}
	message = append(message, 0, 0, 1, 0, 1)
	return message
}

func splitDomain(domain string) []string {
	var labels []string
	start := 0
	for index := range domain {
		if domain[index] == '.' {
			labels = append(labels, domain[start:index])
			start = index + 1
		}
	}
	return append(labels, domain[start:])
}
