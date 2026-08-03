package pppoeclient

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	EtherTypeDiscovery uint16 = 0x8863
	EtherTypeSession   uint16 = 0x8864

	CodePADI uint8 = 0x09
	CodePADO uint8 = 0x07
	CodePADR uint8 = 0x19
	CodePADS uint8 = 0x65
	CodePADT uint8 = 0xa7

	TagEndOfList   uint16 = 0x0000
	TagServiceName uint16 = 0x0101
	TagACName      uint16 = 0x0102
	TagHostUnique  uint16 = 0x0103
	TagACCookie    uint16 = 0x0104

	ProtocolIPv4   uint16 = 0x0021
	ProtocolIPv6   uint16 = 0x0057
	ProtocolLCP    uint16 = 0xc021
	ProtocolPAP    uint16 = 0xc023
	ProtocolCHAP   uint16 = 0xc223
	ProtocolIPCP   uint16 = 0x8021
	ProtocolIPv6CP uint16 = 0x8057
)

type MAC [6]byte

var Broadcast = MAC{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

type Frame struct {
	Destination MAC
	Source      MAC
	EtherType   uint16
	Payload     []byte
}

type Tag struct {
	Type  uint16
	Value []byte
}

type Packet struct {
	Code      uint8
	SessionID uint16
	Protocol  uint16
	Tags      []Tag
	Payload   []byte
}

func EncodeDiscovery(code uint8, sessionID uint16, tags []Tag) []byte {
	body := make([]byte, 0, 64)
	for _, tag := range tags {
		entry := make([]byte, 4+len(tag.Value))
		binary.BigEndian.PutUint16(entry[0:2], tag.Type)
		binary.BigEndian.PutUint16(entry[2:4], uint16(len(tag.Value)))
		copy(entry[4:], tag.Value)
		body = append(body, entry...)
	}
	return encodeHeader(code, sessionID, body)
}

func EncodeSession(sessionID, protocol uint16, payload []byte) []byte {
	body := make([]byte, 2+len(payload))
	binary.BigEndian.PutUint16(body[:2], protocol)
	copy(body[2:], payload)
	return encodeHeader(0, sessionID, body)
}

func encodeHeader(code uint8, sessionID uint16, body []byte) []byte {
	result := make([]byte, 6+len(body))
	result[0] = 0x11
	result[1] = code
	binary.BigEndian.PutUint16(result[2:4], sessionID)
	binary.BigEndian.PutUint16(result[4:6], uint16(len(body)))
	copy(result[6:], body)
	return result
}

func Decode(etherType uint16, data []byte) (Packet, error) {
	if len(data) < 6 || data[0] != 0x11 {
		return Packet{}, errors.New("invalid PPPoE header")
	}
	length := int(binary.BigEndian.Uint16(data[4:6]))
	if length > len(data)-6 {
		return Packet{}, errors.New("truncated PPPoE payload")
	}
	packet := Packet{Code: data[1], SessionID: binary.BigEndian.Uint16(data[2:4])}
	body := data[6 : 6+length]
	switch etherType {
	case EtherTypeDiscovery:
		for len(body) > 0 {
			if len(body) < 4 {
				return Packet{}, errors.New("truncated PPPoE tag")
			}
			tagLength := int(binary.BigEndian.Uint16(body[2:4]))
			if tagLength > len(body)-4 {
				return Packet{}, errors.New("truncated PPPoE tag value")
			}
			packet.Tags = append(packet.Tags, Tag{Type: binary.BigEndian.Uint16(body[:2]), Value: append([]byte(nil), body[4:4+tagLength]...)})
			body = body[4+tagLength:]
		}
	case EtherTypeSession:
		if len(body) < 2 {
			return Packet{}, errors.New("missing PPP protocol")
		}
		packet.Protocol = binary.BigEndian.Uint16(body[:2])
		packet.Payload = append([]byte(nil), body[2:]...)
	default:
		return Packet{}, fmt.Errorf("unsupported EtherType %#x", etherType)
	}
	return packet, nil
}

func FindTag(tags []Tag, tagType uint16) ([]byte, bool) {
	for _, tag := range tags {
		if tag.Type == tagType {
			return append([]byte(nil), tag.Value...), true
		}
	}
	return nil, false
}

func EncodeControl(code, id uint8, data []byte) []byte {
	result := make([]byte, 4+len(data))
	result[0], result[1] = code, id
	binary.BigEndian.PutUint16(result[2:4], uint16(len(result)))
	copy(result[4:], data)
	return result
}

func DecodeControl(data []byte) (code, id uint8, body []byte, err error) {
	if len(data) < 4 {
		return 0, 0, nil, errors.New("truncated PPP control packet")
	}
	length := int(binary.BigEndian.Uint16(data[2:4]))
	if length < 4 || length > len(data) {
		return 0, 0, nil, errors.New("invalid PPP control packet length")
	}
	return data[0], data[1], append([]byte(nil), data[4:length]...), nil
}
