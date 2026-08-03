// Package dnsguard provides the fail-closed DNS listener placed in front of
// SmartDNS. It deliberately implements only the DNS message framing needed to
// preserve a client's question and return a standards-compatible NODATA reply.
package dnsguard

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

const maxMessageSize = 65535

// Config defines the local listener and explicitly permitted DNS suffixes.
// An empty AllowedDomains list is deny-all, never allow-all.
type Config struct {
	ListenAddress        string   `json:"listen_address"`
	UpstreamAddress      string   `json:"upstream_address"`
	AllowedDomains       []string `json:"allowed_domains"`
	RequestTimeoutMillis int      `json:"request_timeout_millis,omitempty"`
}

func (config Config) validate() error {
	if strings.TrimSpace(config.ListenAddress) == "" {
		return errors.New("DNS guard listen address is required")
	}
	if strings.TrimSpace(config.UpstreamAddress) == "" {
		return errors.New("DNS guard upstream address is required")
	}
	if config.RequestTimeoutMillis < 0 || config.RequestTimeoutMillis > 30000 {
		return errors.New("DNS guard request timeout must be between 0 and 30000 milliseconds")
	}
	for _, domain := range config.AllowedDomains {
		if _, err := normalizeDomain(domain); err != nil {
			return fmt.Errorf("invalid allowed DNS domain: %w", err)
		}
	}
	return nil
}

func (config Config) requestTimeout() time.Duration {
	if config.RequestTimeoutMillis == 0 {
		return 2 * time.Second
	}
	return time.Duration(config.RequestTimeoutMillis) * time.Millisecond
}

// Guard handles DNS packets. It is safe to use concurrently.
type Guard struct{ config Config }

func New(config Config) (*Guard, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Guard{config: config}, nil
}

// Serve listens for both DNS transports until context cancellation.
func (guard *Guard) Serve(ctx context.Context) error {
	udp, err := net.ListenPacket("udp", guard.config.ListenAddress)
	if err != nil {
		return err
	}
	tcp, err := net.Listen("tcp", guard.config.ListenAddress)
	if err != nil {
		_ = udp.Close()
		return err
	}
	defer udp.Close()
	defer tcp.Close()

	var group sync.WaitGroup
	group.Add(2)
	go func() { defer group.Done(); guard.serveUDP(ctx, udp) }()
	go func() { defer group.Done(); guard.serveTCP(ctx, tcp) }()
	<-ctx.Done()
	_ = udp.Close()
	_ = tcp.Close()
	group.Wait()
	return nil
}

func (guard *Guard) Handle(query []byte, network string) []byte {
	domain, err := queryDomain(query)
	if err != nil || !guard.allowed(domain) {
		return nodata(query)
	}
	response, err := guard.forward(query, network)
	if err != nil || !validResponse(response, query) {
		return nodata(query)
	}
	return response
}

func (guard *Guard) allowed(domain string) bool {
	for _, candidate := range guard.config.AllowedDomains {
		candidate, err := normalizeDomain(candidate)
		if err == nil && (domain == candidate || strings.HasSuffix(domain, "."+candidate)) {
			return true
		}
	}
	return false
}

func (guard *Guard) forward(query []byte, network string) ([]byte, error) {
	timeout := guard.config.requestTimeout()
	switch network {
	case "udp":
		connection, err := net.DialTimeout("udp", guard.config.UpstreamAddress, timeout)
		if err != nil {
			return nil, err
		}
		defer connection.Close()
		if err := connection.SetDeadline(time.Now().Add(timeout)); err != nil {
			return nil, err
		}
		if _, err := connection.Write(query); err != nil {
			return nil, err
		}
		response := make([]byte, maxMessageSize)
		count, err := connection.Read(response)
		return response[:count], err
	case "tcp":
		connection, err := net.DialTimeout("tcp", guard.config.UpstreamAddress, timeout)
		if err != nil {
			return nil, err
		}
		defer connection.Close()
		if err := connection.SetDeadline(time.Now().Add(timeout)); err != nil {
			return nil, err
		}
		if err := writeTCPMessage(connection, query); err != nil {
			return nil, err
		}
		return readTCPMessage(connection)
	default:
		return nil, fmt.Errorf("unsupported DNS transport %q", network)
	}
}

func (guard *Guard) serveUDP(ctx context.Context, listener net.PacketConn) {
	buffer := make([]byte, maxMessageSize)
	for {
		count, peer, err := listener.ReadFrom(buffer)
		if err != nil {
			return
		}
		query := append([]byte(nil), buffer[:count]...)
		go func() { _, _ = listener.WriteTo(guard.Handle(query, "udp"), peer) }()
	}
}

func (guard *Guard) serveTCP(ctx context.Context, listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer connection.Close()
			query, err := readTCPMessage(connection)
			if err != nil {
				return
			}
			_ = writeTCPMessage(connection, guard.Handle(query, "tcp"))
		}()
	}
}

func queryDomain(message []byte) (string, error) {
	if len(message) < 17 || binary.BigEndian.Uint16(message[4:6]) != 1 {
		return "", errors.New("DNS query must contain exactly one question")
	}
	offset := 12
	labels := make([]string, 0, 4)
	for {
		if offset >= len(message) {
			return "", errors.New("truncated DNS name")
		}
		length := int(message[offset])
		offset++
		if length == 0 {
			break
		}
		if length > 63 || offset+length > len(message) {
			return "", errors.New("invalid DNS label")
		}
		labels = append(labels, string(message[offset:offset+length]))
		offset += length
	}
	if offset+4 > len(message) || len(labels) == 0 {
		return "", errors.New("truncated DNS question")
	}
	return normalizeDomain(strings.Join(labels, "."))
}

func normalizeDomain(domain string) (string, error) {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if domain == "" || len(domain) > 253 || strings.ContainsAny(domain, " /\t\n\r") {
		return "", errors.New("invalid domain")
	}
	return domain, nil
}

func nodata(query []byte) []byte {
	if len(query) < 12 {
		return nil
	}
	questionEnd := len(query)
	if _, err := queryDomain(query); err != nil {
		questionEnd = 12
	} else {
		offset := 12
		for offset < len(query) && query[offset] != 0 {
			offset += int(query[offset]) + 1
		}
		questionEnd = offset + 5
	}
	if questionEnd > len(query) {
		questionEnd = 12
	}
	response := make([]byte, questionEnd)
	copy(response[:2], query[:2])
	flags := binary.BigEndian.Uint16(query[2:4])
	flags = 0x8000 | (flags & 0x7800) | (flags & 0x0100) | 0x0080
	binary.BigEndian.PutUint16(response[2:4], flags)
	binary.BigEndian.PutUint16(response[4:6], 1)
	copy(response[12:], query[12:questionEnd])
	return response
}

func validResponse(response, query []byte) bool {
	return len(response) >= 12 && len(query) >= 2 && response[0] == query[0] && response[1] == query[1] && response[2]&0x80 != 0
}

func readTCPMessage(reader io.Reader) ([]byte, error) {
	var length [2]byte
	if _, err := io.ReadFull(reader, length[:]); err != nil {
		return nil, err
	}
	size := int(binary.BigEndian.Uint16(length[:]))
	if size < 12 || size > maxMessageSize {
		return nil, errors.New("invalid DNS TCP message size")
	}
	message := make([]byte, size)
	_, err := io.ReadFull(reader, message)
	return message, err
}

func writeTCPMessage(writer io.Writer, message []byte) error {
	if len(message) < 12 || len(message) > maxMessageSize {
		return errors.New("invalid DNS TCP response size")
	}
	var length [2]byte
	binary.BigEndian.PutUint16(length[:], uint16(len(message)))
	if _, err := writer.Write(length[:]); err != nil {
		return err
	}
	_, err := writer.Write(message)
	return err
}
