package proxy

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// PrepareNodeTLS keeps public-CA nodes on normal verification and converts
// private-PKI nodes to an Xray 26 certificate pin without user-managed CA data.
func PrepareNodeTLS(ctx context.Context, node Node) (Node, error) {
	if settingString(node.Settings, "security", "") != "tls" {
		return node, nil
	}
	tlsSettings, err := clonedTLSSettings(node.Settings)
	if err != nil {
		return Node{}, err
	}
	if strings.TrimSpace(settingString(tlsSettings, "pinnedPeerCertSha256", "")) != "" {
		return node, nil
	}
	serverName := settingString(tlsSettings, "serverName", node.Address)
	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second},
		Config: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			ServerName:         serverName,
			InsecureSkipVerify: true, // Verification is performed below or replaced by an Xray pin.
		},
	}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(node.Address, strconv.Itoa(node.Port)))
	if err != nil {
		return Node{}, fmt.Errorf("discover TLS certificate for node %q: %w", node.ID, err)
	}
	defer connection.Close()
	state := connection.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return Node{}, fmt.Errorf("discover TLS certificate for node %q: peer returned no certificate", node.ID)
	}
	if verifiesWithSystemRoots(state.PeerCertificates, serverName) {
		return node, nil
	}
	digest := sha256.Sum256(state.PeerCertificates[0].Raw)
	tlsSettings["pinnedPeerCertSha256"] = hex.EncodeToString(digest[:])
	settings := cloneSettings(node.Settings)
	settings["tlsSettings"] = tlsSettings
	node.Settings = settings
	return node, nil
}

func clonedTLSSettings(settings map[string]any) (map[string]any, error) {
	result := map[string]any{}
	configured, exists := settings["tlsSettings"]
	if !exists {
		return result, nil
	}
	values, ok := configured.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tlsSettings must be an object")
	}
	for key, value := range values {
		result[key] = value
	}
	return result, nil
}

func verifiesWithSystemRoots(certificates []*x509.Certificate, serverName string) bool {
	intermediates := x509.NewCertPool()
	for _, certificate := range certificates[1:] {
		intermediates.AddCert(certificate)
	}
	_, err := certificates[0].Verify(x509.VerifyOptions{DNSName: serverName, Intermediates: intermediates})
	return err == nil
}
