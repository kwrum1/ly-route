package pppoeclient

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

const (
	controlConfigureRequest uint8 = 1
	controlConfigureAck     uint8 = 2
	controlConfigureNak     uint8 = 3
	controlConfigureReject  uint8 = 4
	controlTerminateRequest uint8 = 5
	controlTerminateAck     uint8 = 6
	controlEchoRequest      uint8 = 9
	controlEchoReply        uint8 = 10
)

type Config struct {
	Interface string        `json:"control_interface"`
	Username  string        `json:"username"`
	Password  string        `json:"password"`
	MRU       uint16        `json:"mru,omitempty"`
	Timeout   time.Duration `json:"-"`
	Retries   int           `json:"retries,omitempty"`
}

type Session struct {
	ID            uint16 `json:"session_id"`
	LocalAddress  string `json:"local_address"`
	RemoteAddress string `json:"remote_address"`
	ACMAC         MAC    `json:"ac_mac"`
	ClientMAC     MAC    `json:"client_mac"`
	AuthProtocol  uint16 `json:"auth_protocol,omitempty"`
	LocalIPv6     string `json:"local_ipv6,omitempty"`
	RemoteIPv6    string `json:"remote_ipv6,omitempty"`
	IPv6Ready     bool   `json:"ipv6_ready"`
}

type Client struct {
	config        Config
	link          Link
	acMAC         MAC
	sid           uint16
	auth          uint16
	chapAlgorithm uint8
	magic         [4]byte
}

func New(config Config, link Link) (*Client, error) {
	if config.Interface == "" || config.Username == "" || link == nil {
		return nil, errors.New("PPPoE client requires interface, username, and link")
	}
	if config.MRU == 0 {
		config.MRU = 1492
	}
	if config.MRU < 1280 || config.MRU > 1492 {
		return nil, errors.New("PPPoE MRU must be between 1280 and 1492")
	}
	if config.Timeout <= 0 {
		config.Timeout = 3 * time.Second
	}
	if config.Retries <= 0 {
		config.Retries = 4
	}
	client := &Client{config: config, link: link}
	if _, err := rand.Read(client.magic[:]); err != nil {
		return nil, err
	}
	return client, nil
}

func (client *Client) Connect(ctx context.Context) (Session, error) {
	if err := client.discover(ctx); err != nil {
		return Session{}, err
	}
	if err := client.negotiateLCP(ctx); err != nil {
		return Session{}, err
	}
	if err := client.authenticate(ctx); err != nil {
		return Session{}, err
	}
	local, remote, err := client.negotiateIPCP(ctx)
	if err != nil {
		return Session{}, err
	}
	localIPv6, remoteIPv6, ipv6Err := client.negotiateIPv6CP(ctx)
	session := Session{ID: client.sid, LocalAddress: local.String(), RemoteAddress: remote.String(), ACMAC: client.acMAC, ClientMAC: client.link.MAC(), AuthProtocol: client.auth}
	if ipv6Err == nil {
		session.LocalIPv6 = localIPv6.String()
		session.RemoteIPv6 = remoteIPv6.String()
		session.IPv6Ready = true
	}
	return session, nil
}

func (client *Client) negotiateIPv6CP(ctx context.Context) (netip.Addr, netip.Addr, error) {
	identifier := uint8(1)
	localIID := interfaceIdentifier(client.link.MAC())
	var remoteIID [8]byte
	localAck, peerAck := false, false
	request := func() []byte { return append([]byte{1, 10}, localIID[:]...) }
	for attempt := 0; attempt < client.config.Retries && !(localAck && peerAck); attempt++ {
		if !localAck {
			if err := client.sendControl(ctx, ProtocolIPv6CP, controlConfigureRequest, identifier, request()); err != nil {
				return netip.Addr{}, netip.Addr{}, err
			}
		}
		deadline := time.Now().Add(client.config.Timeout)
		for time.Now().Before(deadline) && !(localAck && peerAck) {
			packet, err := client.receiveSession(ctx, time.Until(deadline))
			if err != nil {
				break
			}
			if packet.Protocol == ProtocolLCP {
				client.handleLCPKeepalive(ctx, packet)
				continue
			}
			if packet.Protocol != ProtocolIPv6CP {
				continue
			}
			code, id, body, err := DecodeControl(packet.Payload)
			if err != nil {
				continue
			}
			switch code {
			case controlConfigureRequest:
				iid, ok := ipv6CPInterfaceIdentifier(body)
				if !ok || iid == [8]byte{} {
					_ = client.sendControl(ctx, ProtocolIPv6CP, controlConfigureReject, id, body)
					continue
				}
				remoteIID = iid
				_ = client.sendControl(ctx, ProtocolIPv6CP, controlConfigureAck, id, body)
				peerAck = true
			case controlConfigureNak:
				if iid, ok := ipv6CPInterfaceIdentifier(body); ok && iid != [8]byte{} {
					localIID = iid
					identifier++
					localAck = false
				}
			case controlConfigureAck:
				if id == identifier && string(body) == string(request()) {
					localAck = true
				}
			case controlConfigureReject:
				return netip.Addr{}, netip.Addr{}, errors.New("IPv6CP interface identifier rejected")
			}
		}
	}
	if !localAck || !peerAck || remoteIID == [8]byte{} {
		return netip.Addr{}, netip.Addr{}, errors.New("IPv6CP negotiation timed out")
	}
	return linkLocalFromIID(localIID), linkLocalFromIID(remoteIID), nil
}

func interfaceIdentifier(mac MAC) [8]byte {
	return [8]byte{mac[0] ^ 0x02, mac[1], mac[2], 0xff, 0xfe, mac[3], mac[4], mac[5]}
}

func ipv6CPInterfaceIdentifier(options []byte) ([8]byte, bool) {
	for len(options) >= 2 {
		length := int(options[1])
		if length < 2 || length > len(options) {
			break
		}
		if options[0] == 1 && length == 10 {
			var iid [8]byte
			copy(iid[:], options[2:10])
			return iid, true
		}
		options = options[length:]
	}
	return [8]byte{}, false
}

func linkLocalFromIID(iid [8]byte) netip.Addr {
	var raw [16]byte
	raw[0], raw[1] = 0xfe, 0x80
	copy(raw[8:], iid[:])
	return netip.AddrFrom16(raw)
}

// Serve keeps the negotiated control channel alive. IPv4 and IPv6 payloads do
// not traverse this method; VPP's native pppoe_session interface owns them.
func (client *Client) Serve(ctx context.Context) error {
	for {
		packet, err := client.receiveSession(ctx, time.Second)
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

func (client *Client) Disconnect(ctx context.Context) error {
	if client.sid == 0 {
		return nil
	}
	err := client.sendDiscovery(ctx, client.acMAC, CodePADT, client.sid, nil)
	client.sid = 0
	return err
}

func (client *Client) discover(ctx context.Context) error {
	hostUnique := make([]byte, 4)
	if _, err := rand.Read(hostUnique); err != nil {
		return err
	}
	service := []byte{}
	var cookie []byte
	for attempt := 0; attempt < client.config.Retries; attempt++ {
		if err := client.sendDiscovery(ctx, Broadcast, CodePADI, 0, []Tag{{Type: TagServiceName, Value: service}, {Type: TagHostUnique, Value: hostUnique}}); err != nil {
			return err
		}
		frame, packet, err := client.waitPacket(ctx, client.config.Timeout, func(frame Frame, packet Packet) bool {
			value, ok := FindTag(packet.Tags, TagHostUnique)
			return frame.EtherType == EtherTypeDiscovery && packet.Code == CodePADO && ok && string(value) == string(hostUnique)
		})
		if err != nil {
			continue
		}
		client.acMAC = frame.Source
		if value, ok := FindTag(packet.Tags, TagServiceName); ok {
			service = value
		}
		cookie, _ = FindTag(packet.Tags, TagACCookie)
		tags := []Tag{{Type: TagServiceName, Value: service}, {Type: TagHostUnique, Value: hostUnique}}
		if len(cookie) > 0 {
			tags = append(tags, Tag{Type: TagACCookie, Value: cookie})
		}
		if err := client.sendDiscovery(ctx, client.acMAC, CodePADR, 0, tags); err != nil {
			return err
		}
		_, pads, err := client.waitPacket(ctx, client.config.Timeout, func(frame Frame, packet Packet) bool {
			value, ok := FindTag(packet.Tags, TagHostUnique)
			return frame.EtherType == EtherTypeDiscovery && frame.Source == client.acMAC && packet.Code == CodePADS && ok && string(value) == string(hostUnique)
		})
		if err == nil && pads.SessionID != 0 {
			client.sid = pads.SessionID
			return nil
		}
	}
	return errors.New("PPPoE discovery timed out")
}

func (client *Client) negotiateLCP(ctx context.Context) error {
	identifier := uint8(1)
	request := []byte{1, 4, byte(client.config.MRU >> 8), byte(client.config.MRU), 5, 6, client.magic[0], client.magic[1], client.magic[2], client.magic[3]}
	localAck, peerAck := false, false
	for attempt := 0; attempt < client.config.Retries && !(localAck && peerAck); attempt++ {
		if !localAck {
			if err := client.sendControl(ctx, ProtocolLCP, controlConfigureRequest, identifier, request); err != nil {
				return err
			}
		}
		deadline := time.Now().Add(client.config.Timeout)
		for time.Now().Before(deadline) && !(localAck && peerAck) {
			packet, err := client.receiveSession(ctx, time.Until(deadline))
			if err != nil {
				break
			}
			if packet.Protocol != ProtocolLCP {
				continue
			}
			code, id, body, err := DecodeControl(packet.Payload)
			if err != nil {
				continue
			}
			switch code {
			case controlConfigureRequest:
				rejected := client.inspectLCPOptions(body)
				if len(rejected) > 0 {
					_ = client.sendControl(ctx, ProtocolLCP, controlConfigureReject, id, rejected)
				} else {
					_ = client.sendControl(ctx, ProtocolLCP, controlConfigureAck, id, body)
					peerAck = true
				}
			case controlConfigureAck:
				if id == identifier && string(body) == string(request) {
					localAck = true
				}
			case controlConfigureNak, controlConfigureReject:
				identifier++
				localAck = false
			case controlEchoRequest:
				_ = client.sendControl(ctx, ProtocolLCP, controlEchoReply, id, append(client.magic[:], body[min(4, len(body)):]...))
			case controlTerminateRequest:
				_ = client.sendControl(ctx, ProtocolLCP, controlTerminateAck, id, body)
				return errors.New("PPPoE peer terminated LCP")
			}
		}
	}
	if !localAck || !peerAck {
		return errors.New("LCP negotiation timed out")
	}
	return nil
}

func (client *Client) inspectLCPOptions(options []byte) []byte {
	var rejected []byte
	for len(options) >= 2 {
		length := int(options[1])
		if length < 2 || length > len(options) {
			return append(rejected, options...)
		}
		option := options[:length]
		switch option[0] {
		case 1, 2, 5:
		case 3:
			if length >= 4 {
				protocol := binary.BigEndian.Uint16(option[2:4])
				if protocol == ProtocolPAP || protocol == ProtocolCHAP && length >= 5 && option[4] == 5 {
					client.auth = protocol
					if protocol == ProtocolCHAP {
						client.chapAlgorithm = option[4]
					}
					break
				}
			}
			rejected = append(rejected, option...)
		default:
			rejected = append(rejected, option...)
		}
		options = options[length:]
	}
	return rejected
}

func (client *Client) authenticate(ctx context.Context) error {
	switch client.auth {
	case 0:
		return nil
	case ProtocolPAP:
		body := append([]byte{byte(len(client.config.Username))}, []byte(client.config.Username)...)
		body = append(body, byte(len(client.config.Password)))
		body = append(body, []byte(client.config.Password)...)
		for attempt := 0; attempt < client.config.Retries; attempt++ {
			if err := client.sendControl(ctx, ProtocolPAP, 1, 1, body); err != nil {
				return err
			}
			packet, err := client.waitProtocol(ctx, ProtocolPAP, client.config.Timeout)
			if err != nil {
				continue
			}
			code, _, _, err := DecodeControl(packet.Payload)
			if err == nil && code == 2 {
				return nil
			}
			if err == nil && code == 3 {
				return errors.New("PAP authentication rejected")
			}
		}
		return errors.New("PAP authentication timed out")
	case ProtocolCHAP:
		packet, err := client.waitProtocol(ctx, ProtocolCHAP, client.config.Timeout*time.Duration(client.config.Retries))
		if err != nil {
			return errors.New("CHAP challenge timed out")
		}
		code, id, body, err := DecodeControl(packet.Payload)
		if err != nil || code != 1 || len(body) < 1 || int(body[0]) > len(body)-1 {
			return errors.New("invalid CHAP challenge")
		}
		challenge := body[1 : 1+int(body[0])]
		hash := md5.New()
		hash.Write([]byte{id})
		hash.Write([]byte(client.config.Password))
		hash.Write(challenge)
		digest := hash.Sum(nil)
		response := append([]byte{byte(len(digest))}, digest...)
		response = append(response, []byte(client.config.Username)...)
		if err := client.sendControl(ctx, ProtocolCHAP, 2, id, response); err != nil {
			return err
		}
		result, err := client.waitProtocol(ctx, ProtocolCHAP, client.config.Timeout)
		if err != nil {
			return errors.New("CHAP result timed out")
		}
		resultCode, _, _, _ := DecodeControl(result.Payload)
		if resultCode != 3 {
			return errors.New("CHAP authentication rejected")
		}
		return nil
	default:
		return fmt.Errorf("unsupported authentication protocol %#x", client.auth)
	}
}

func (client *Client) negotiateIPCP(ctx context.Context) (netip.Addr, netip.Addr, error) {
	identifier := uint8(1)
	local := [4]byte{}
	remote := [4]byte{}
	localAck, peerAck := false, false
	request := func() []byte { return []byte{3, 6, local[0], local[1], local[2], local[3]} }
	for attempt := 0; attempt < client.config.Retries && !(localAck && peerAck); attempt++ {
		if !localAck {
			if err := client.sendControl(ctx, ProtocolIPCP, controlConfigureRequest, identifier, request()); err != nil {
				return netip.Addr{}, netip.Addr{}, err
			}
		}
		deadline := time.Now().Add(client.config.Timeout)
		for time.Now().Before(deadline) && !(localAck && peerAck) {
			packet, err := client.receiveSession(ctx, time.Until(deadline))
			if err != nil {
				break
			}
			if packet.Protocol == ProtocolLCP {
				client.handleLCPKeepalive(ctx, packet)
				continue
			}
			if packet.Protocol != ProtocolIPCP {
				continue
			}
			code, id, body, err := DecodeControl(packet.Payload)
			if err != nil {
				continue
			}
			switch code {
			case controlConfigureRequest:
				rejected := unsupportedIPCPOptions(body)
				if len(rejected) > 0 {
					_ = client.sendControl(ctx, ProtocolIPCP, controlConfigureReject, id, rejected)
				} else {
					if address, ok := addressOption(body); ok {
						remote = address
					}
					_ = client.sendControl(ctx, ProtocolIPCP, controlConfigureAck, id, body)
					peerAck = true
				}
			case controlConfigureNak:
				if address, ok := addressOption(body); ok {
					local = address
					identifier++
					localAck = false
				}
			case controlConfigureAck:
				if id == identifier && string(body) == string(request()) {
					localAck = true
				}
			case controlConfigureReject:
				return netip.Addr{}, netip.Addr{}, errors.New("IPCP address option rejected")
			}
		}
	}
	if !localAck || !peerAck || local == [4]byte{} || remote == [4]byte{} {
		return netip.Addr{}, netip.Addr{}, errors.New("IPCP negotiation timed out")
	}
	return netip.AddrFrom4(local), netip.AddrFrom4(remote), nil
}

func unsupportedIPCPOptions(options []byte) []byte {
	var rejected []byte
	for len(options) >= 2 {
		length := int(options[1])
		if length < 2 || length > len(options) {
			return append(rejected, options...)
		}
		if options[0] != 3 {
			rejected = append(rejected, options[:length]...)
		}
		options = options[length:]
	}
	return rejected
}

func addressOption(options []byte) ([4]byte, bool) {
	for len(options) >= 2 {
		length := int(options[1])
		if length < 2 || length > len(options) {
			break
		}
		if options[0] == 3 && length == 6 {
			var address [4]byte
			copy(address[:], options[2:6])
			return address, true
		}
		options = options[length:]
	}
	return [4]byte{}, false
}

func (client *Client) handleLCPKeepalive(ctx context.Context, packet Packet) {
	code, id, body, err := DecodeControl(packet.Payload)
	if err == nil && code == controlEchoRequest {
		_ = client.sendControl(ctx, ProtocolLCP, controlEchoReply, id, append(client.magic[:], body[min(4, len(body)):]...))
	}
}

func (client *Client) sendDiscovery(ctx context.Context, destination MAC, code uint8, sid uint16, tags []Tag) error {
	return client.link.Send(ctx, Frame{Destination: destination, Source: client.link.MAC(), EtherType: EtherTypeDiscovery, Payload: EncodeDiscovery(code, sid, tags)})
}

func (client *Client) sendControl(ctx context.Context, protocol uint16, code, id uint8, body []byte) error {
	return client.link.Send(ctx, Frame{Destination: client.acMAC, Source: client.link.MAC(), EtherType: EtherTypeSession, Payload: EncodeSession(client.sid, protocol, EncodeControl(code, id, body))})
}

func (client *Client) receiveSession(ctx context.Context, timeout time.Duration) (Packet, error) {
	_, packet, err := client.waitPacket(ctx, timeout, func(frame Frame, packet Packet) bool {
		return frame.EtherType == EtherTypeSession && frame.Source == client.acMAC && packet.SessionID == client.sid
	})
	return packet, err
}

func (client *Client) waitProtocol(ctx context.Context, protocol uint16, timeout time.Duration) (Packet, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		packet, err := client.receiveSession(ctx, time.Until(deadline))
		if err != nil {
			return Packet{}, err
		}
		if packet.Protocol == ProtocolLCP {
			client.handleLCPKeepalive(ctx, packet)
		}
		if packet.Protocol == protocol {
			return packet, nil
		}
	}
	return Packet{}, context.DeadlineExceeded
}

func (client *Client) waitPacket(ctx context.Context, timeout time.Duration, accept func(Frame, Packet) bool) (Frame, Packet, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		frame, err := client.link.Receive(waitCtx)
		if err != nil {
			return Frame{}, Packet{}, err
		}
		if frame.EtherType != EtherTypeDiscovery && frame.EtherType != EtherTypeSession {
			continue
		}
		packet, err := Decode(frame.EtherType, frame.Payload)
		if err == nil && accept(frame, packet) {
			return frame, packet, nil
		}
	}
}
