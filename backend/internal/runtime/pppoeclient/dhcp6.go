package pppoeclient

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

const (
	dhcp6Solicit   = 1
	dhcp6Advertise = 2
	dhcp6Request   = 3
	dhcp6Renew     = 5
	dhcp6Rebind    = 6
	dhcp6Reply     = 7

	dhcp6OptionClientID = 1
	dhcp6OptionServerID = 2
	dhcp6OptionElapsed  = 8
	dhcp6OptionIAPD     = 25
	dhcp6OptionIAPrefix = 26

	dhcp6IAID = 1
)

var allDHCP6Servers = netip.MustParseAddr("ff02::1:2")

const delegatedPrefixRetryInterval = 30 * time.Second

type DelegatedPrefixLease struct {
	Prefix            netip.Prefix
	PreferredLifetime uint32
	ValidLifetime     uint32
	T1                uint32
	T2                uint32
	ServerID          []byte
	ClientID          []byte
	AcquiredAt        time.Time
}

func (lease DelegatedPrefixLease) valid() bool {
	return lease.Prefix.IsValid() && lease.Prefix.Addr().Is6() && lease.Prefix.Bits() <= 64 && lease.ValidLifetime > 0
}

func (lease DelegatedPrefixLease) renewAt() time.Time {
	seconds := lease.T1
	if seconds == 0 {
		seconds = lease.ValidLifetime / 2
	}
	return lease.AcquiredAt.Add(time.Duration(seconds) * time.Second)
}

func (lease DelegatedPrefixLease) rebindAt() time.Time {
	seconds := lease.T2
	if seconds == 0 {
		seconds = lease.ValidLifetime * 4 / 5
	}
	return lease.AcquiredAt.Add(time.Duration(seconds) * time.Second)
}

func (lease DelegatedPrefixLease) expiresAt() time.Time {
	return lease.AcquiredAt.Add(time.Duration(lease.ValidLifetime) * time.Second)
}

func (client *Client) AcquireDelegatedPrefix(ctx context.Context, session Session) (DelegatedPrefixLease, error) {
	if !session.IPv6Ready {
		return DelegatedPrefixLease{}, errors.New("DHCPv6-PD requires an active IPv6CP session")
	}
	clientID := dhcp6DUIDLL(client.link.MAC())
	offer, messageType, err := client.exchangeDHCPv6(ctx, session, dhcp6Solicit, DelegatedPrefixLease{ClientID: clientID}, dhcp6Advertise, dhcp6Reply)
	if err != nil {
		return DelegatedPrefixLease{}, err
	}
	if messageType == dhcp6Reply {
		return offer, nil
	}
	lease, _, err := client.exchangeDHCPv6(ctx, session, dhcp6Request, offer, dhcp6Reply)
	if err != nil {
		return DelegatedPrefixLease{}, err
	}
	return lease, nil
}

func (client *Client) renewDelegatedPrefix(ctx context.Context, session Session, lease DelegatedPrefixLease, rebind bool) (DelegatedPrefixLease, error) {
	messageType := byte(dhcp6Renew)
	if rebind {
		messageType = dhcp6Rebind
		lease.ServerID = nil
	}
	updated, _, err := client.exchangeDHCPv6(ctx, session, messageType, lease, dhcp6Reply)
	return updated, err
}

func (client *Client) exchangeDHCPv6(ctx context.Context, session Session, messageType byte, lease DelegatedPrefixLease, accepted ...byte) (DelegatedPrefixLease, byte, error) {
	var transaction [3]byte
	if _, err := rand.Read(transaction[:]); err != nil {
		return DelegatedPrefixLease{}, 0, err
	}
	payload := encodeDHCPv6Message(messageType, transaction, lease)
	for attempt := 0; attempt < client.config.Retries; attempt++ {
		if err := client.sendDHCPv6(ctx, session, payload); err != nil {
			return DelegatedPrefixLease{}, 0, err
		}
		deadline := time.Now().Add(client.config.Timeout)
		for time.Now().Before(deadline) {
			packet, err := client.receiveSession(ctx, time.Until(deadline))
			if err != nil {
				break
			}
			if packet.Protocol == ProtocolLCP {
				client.handleLCPKeepalive(ctx, packet)
				continue
			}
			if packet.Protocol != ProtocolIPv6 {
				continue
			}
			response, err := decodeDHCPv6UDP(packet.Payload)
			if err != nil || len(response) < 4 || string(response[1:4]) != string(transaction[:]) || !containsByte(accepted, response[0]) {
				continue
			}
			parsed, err := parseDelegatedPrefix(response, lease.ClientID)
			if err != nil {
				continue
			}
			return parsed, response[0], nil
		}
	}
	return DelegatedPrefixLease{}, 0, fmt.Errorf("DHCPv6-PD message %d timed out", messageType)
}

func (client *Client) sendDHCPv6(ctx context.Context, session Session, payload []byte) error {
	source, err := netip.ParseAddr(session.LocalIPv6)
	if err != nil || !source.Is6() {
		return fmt.Errorf("invalid IPv6CP local address %q", session.LocalIPv6)
	}
	ipv6, err := encodeIPv6UDP(source, allDHCP6Servers, 546, 547, payload)
	if err != nil {
		return err
	}
	return client.link.Send(ctx, Frame{
		Destination: client.acMAC,
		Source:      client.link.MAC(),
		EtherType:   EtherTypeSession,
		Payload:     EncodeSession(client.sid, ProtocolIPv6, ipv6),
	})
}

func (client *Client) ServeWithDelegatedPrefix(ctx context.Context, session Session, lease DelegatedPrefixLease, update func(context.Context, DelegatedPrefixLease) error) error {
	if !lease.valid() {
		return errors.New("delegated prefix lease is invalid")
	}
	return client.serveWithDelegatedPrefix(ctx, session, lease, update)
}

func (client *Client) ServeWithDelegatedPrefixRecovery(ctx context.Context, session Session, lease DelegatedPrefixLease, update func(context.Context, DelegatedPrefixLease) error) error {
	if !session.IPv6Ready {
		return errors.New("DHCPv6-PD recovery requires an active IPv6CP session")
	}
	return client.serveWithDelegatedPrefix(ctx, session, lease, update)
}

func (client *Client) serveWithDelegatedPrefix(ctx context.Context, session Session, lease DelegatedPrefixLease, update func(context.Context, DelegatedPrefixLease) error) error {
	nextRenew := time.Time{}
	if lease.valid() {
		nextRenew = lease.renewAt()
	} else if session.IPv6Ready {
		nextRenew = time.Now()
	}
	for {
		if !nextRenew.IsZero() && !time.Now().Before(nextRenew) {
			var updated DelegatedPrefixLease
			var err error
			if lease.valid() {
				rebind := !time.Now().Before(lease.rebindAt())
				updated, err = client.renewDelegatedPrefix(ctx, session, lease, rebind)
			} else {
				updated, err = client.AcquireDelegatedPrefix(ctx, session)
			}
			if err == nil {
				if update != nil {
					if err := update(ctx, updated); err != nil {
						return err
					}
				}
				lease = updated
				nextRenew = lease.renewAt()
			} else {
				if lease.valid() && !time.Now().Before(lease.expiresAt()) {
					return fmt.Errorf("DHCPv6-PD lease expired: %w", err)
				}
				nextRenew = time.Now().Add(delegatedPrefixRetryInterval)
			}
			continue
		}

		wait := time.Second
		if !nextRenew.IsZero() && time.Until(nextRenew) < wait {
			wait = time.Until(nextRenew)
		}
		if wait <= 0 {
			continue
		}
		packet, err := client.receiveSession(ctx, wait)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if packet.Protocol != ProtocolLCP {
			continue
		}
		code, id, body, err := DecodeControl(packet.Payload)
		if err != nil {
			continue
		}
		switch code {
		case controlEchoRequest:
			if err := client.sendControl(ctx, ProtocolLCP, controlEchoReply, id, append(client.magic[:], body[min(4, len(body)):]...)); err != nil {
				return err
			}
		case controlTerminateRequest:
			_ = client.sendControl(ctx, ProtocolLCP, controlTerminateAck, id, body)
			return errors.New("PPPoE peer terminated the session")
		}
	}
}

func encodeDHCPv6Message(messageType byte, transaction [3]byte, lease DelegatedPrefixLease) []byte {
	message := append([]byte{messageType}, transaction[:]...)
	clientID := lease.ClientID
	if len(clientID) > 0 {
		message = appendDHCPv6Option(message, dhcp6OptionClientID, clientID)
	}
	if len(lease.ServerID) > 0 && messageType != dhcp6Solicit && messageType != dhcp6Rebind {
		message = appendDHCPv6Option(message, dhcp6OptionServerID, lease.ServerID)
	}
	message = appendDHCPv6Option(message, dhcp6OptionElapsed, []byte{0, 0})
	iaPD := make([]byte, 12)
	binary.BigEndian.PutUint32(iaPD[:4], dhcp6IAID)
	if messageType == dhcp6Renew || messageType == dhcp6Rebind {
		binary.BigEndian.PutUint32(iaPD[4:8], lease.T1)
		binary.BigEndian.PutUint32(iaPD[8:12], lease.T2)
	}
	if lease.Prefix.IsValid() {
		prefix := make([]byte, 25)
		binary.BigEndian.PutUint32(prefix[:4], lease.PreferredLifetime)
		binary.BigEndian.PutUint32(prefix[4:8], lease.ValidLifetime)
		prefix[8] = byte(lease.Prefix.Bits())
		raw := lease.Prefix.Masked().Addr().As16()
		copy(prefix[9:], raw[:])
		iaPD = appendDHCPv6Option(iaPD, dhcp6OptionIAPrefix, prefix)
	}
	return appendDHCPv6Option(message, dhcp6OptionIAPD, iaPD)
}

func parseDelegatedPrefix(message, expectedClientID []byte) (DelegatedPrefixLease, error) {
	if len(message) < 4 || (message[0] != dhcp6Advertise && message[0] != dhcp6Reply) {
		return DelegatedPrefixLease{}, errors.New("unexpected DHCPv6 response")
	}
	options, err := parseDHCPv6Options(message[4:])
	if err != nil {
		return DelegatedPrefixLease{}, err
	}
	clientID := options[dhcp6OptionClientID]
	if len(expectedClientID) == 0 || string(clientID) != string(expectedClientID) {
		return DelegatedPrefixLease{}, errors.New("DHCPv6 response client ID mismatch")
	}
	serverID := options[dhcp6OptionServerID]
	if len(serverID) == 0 {
		return DelegatedPrefixLease{}, errors.New("DHCPv6 response has no server ID")
	}
	iaPD := options[dhcp6OptionIAPD]
	if len(iaPD) < 12 || binary.BigEndian.Uint32(iaPD[:4]) != dhcp6IAID {
		return DelegatedPrefixLease{}, errors.New("DHCPv6 response has no matching IA_PD")
	}
	nested, err := parseDHCPv6Options(iaPD[12:])
	if err != nil {
		return DelegatedPrefixLease{}, err
	}
	prefixOption := nested[dhcp6OptionIAPrefix]
	if len(prefixOption) < 25 || prefixOption[8] > 64 {
		return DelegatedPrefixLease{}, errors.New("DHCPv6 response has no usable delegated prefix")
	}
	var raw [16]byte
	copy(raw[:], prefixOption[9:25])
	prefix := netip.PrefixFrom(netip.AddrFrom16(raw), int(prefixOption[8])).Masked()
	lease := DelegatedPrefixLease{
		Prefix:            prefix,
		PreferredLifetime: binary.BigEndian.Uint32(prefixOption[:4]),
		ValidLifetime:     binary.BigEndian.Uint32(prefixOption[4:8]),
		T1:                binary.BigEndian.Uint32(iaPD[4:8]),
		T2:                binary.BigEndian.Uint32(iaPD[8:12]),
		ServerID:          append([]byte(nil), serverID...),
		ClientID:          append([]byte(nil), clientID...),
		AcquiredAt:        time.Now(),
	}
	if !lease.valid() || lease.PreferredLifetime > lease.ValidLifetime {
		return DelegatedPrefixLease{}, errors.New("DHCPv6 response contains invalid prefix lifetimes")
	}
	return lease, nil
}

func appendDHCPv6Option(destination []byte, code uint16, value []byte) []byte {
	header := make([]byte, 4)
	binary.BigEndian.PutUint16(header[:2], code)
	binary.BigEndian.PutUint16(header[2:4], uint16(len(value)))
	destination = append(destination, header...)
	return append(destination, value...)
}

func parseDHCPv6Options(data []byte) (map[uint16][]byte, error) {
	options := map[uint16][]byte{}
	for len(data) > 0 {
		if len(data) < 4 {
			return nil, errors.New("truncated DHCPv6 option")
		}
		code := binary.BigEndian.Uint16(data[:2])
		length := int(binary.BigEndian.Uint16(data[2:4]))
		if length > len(data)-4 {
			return nil, errors.New("truncated DHCPv6 option value")
		}
		if _, exists := options[code]; !exists {
			options[code] = append([]byte(nil), data[4:4+length]...)
		}
		data = data[4+length:]
	}
	return options, nil
}

func dhcp6DUIDLL(mac MAC) []byte {
	duid := make([]byte, 10)
	binary.BigEndian.PutUint16(duid[:2], 3)
	binary.BigEndian.PutUint16(duid[2:4], 1)
	copy(duid[4:], mac[:])
	return duid
}

func encodeIPv6UDP(source, destination netip.Addr, sourcePort, destinationPort uint16, payload []byte) ([]byte, error) {
	if !source.Is6() || !destination.Is6() || len(payload) > 65527 {
		return nil, errors.New("invalid IPv6 UDP packet")
	}
	udpLength := 8 + len(payload)
	packet := make([]byte, 40+udpLength)
	packet[0] = 0x60
	binary.BigEndian.PutUint16(packet[4:6], uint16(udpLength))
	packet[6], packet[7] = 17, 1
	sourceRaw, destinationRaw := source.As16(), destination.As16()
	copy(packet[8:24], sourceRaw[:])
	copy(packet[24:40], destinationRaw[:])
	udp := packet[40:]
	binary.BigEndian.PutUint16(udp[:2], sourcePort)
	binary.BigEndian.PutUint16(udp[2:4], destinationPort)
	binary.BigEndian.PutUint16(udp[4:6], uint16(udpLength))
	copy(udp[8:], payload)
	checksum := udp6Checksum(sourceRaw, destinationRaw, udp)
	if checksum == 0 {
		checksum = 0xffff
	}
	binary.BigEndian.PutUint16(udp[6:8], checksum)
	return packet, nil
}

func decodeDHCPv6UDP(packet []byte) ([]byte, error) {
	if len(packet) < 48 || packet[0]>>4 != 6 || packet[6] != 17 {
		return nil, errors.New("not an IPv6 UDP packet")
	}
	payloadLength := int(binary.BigEndian.Uint16(packet[4:6]))
	if payloadLength < 8 || payloadLength > len(packet)-40 {
		return nil, errors.New("invalid IPv6 payload length")
	}
	udp := packet[40 : 40+payloadLength]
	udpLength := int(binary.BigEndian.Uint16(udp[4:6]))
	if udpLength < 8 || udpLength > len(udp) || binary.BigEndian.Uint16(udp[:2]) != 547 || binary.BigEndian.Uint16(udp[2:4]) != 546 {
		return nil, errors.New("not a DHCPv6 server response")
	}
	var source, destination [16]byte
	copy(source[:], packet[8:24])
	copy(destination[:], packet[24:40])
	if foldChecksum(udp6ChecksumSum(source, destination, udp[:udpLength])) != 0xffff {
		return nil, errors.New("invalid DHCPv6 UDP checksum")
	}
	return append([]byte(nil), udp[8:udpLength]...), nil
}

func udp6Checksum(source, destination [16]byte, udp []byte) uint16 {
	return ^foldChecksum(udp6ChecksumSum(source, destination, udp))
}

func udp6ChecksumSum(source, destination [16]byte, udp []byte) uint32 {
	var sum uint32
	add := func(data []byte) {
		for len(data) >= 2 {
			sum += uint32(binary.BigEndian.Uint16(data[:2]))
			data = data[2:]
		}
		if len(data) == 1 {
			sum += uint32(data[0]) << 8
		}
	}
	add(source[:])
	add(destination[:])
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, uint32(len(udp)))
	add(length)
	add([]byte{0, 0, 0, 17})
	add(udp)
	return sum
}

func foldChecksum(sum uint32) uint16 {
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return uint16(sum)
}

func containsByte(values []byte, candidate byte) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
