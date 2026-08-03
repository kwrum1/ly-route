package main

import (
	"encoding/binary"
	"log"
	"net"
)

const (
	optClientID = 1
	optServerID = 2
	optIAPD     = 25
	optIAPrefix = 26
)

var serverID = []byte{0, 3, 0, 1, 0x02, 0, 0, 0, 0, 1}

func main() {
	conn, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6unspecified, Port: 547})
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	buffer := make([]byte, 4096)
	for {
		n, peer, err := conn.ReadFromUDP(buffer)
		if err != nil {
			log.Print(err)
			continue
		}
                if n < 4 || (buffer[0] != 1 && buffer[0] != 3) {
                        log.Printf("ignored DHCPv6 packet length=%d type=%d peer=%s", n, buffer[0], peer)
                        continue
                }
		options := parseOptions(buffer[4:n])
		clientID, okClient := options[optClientID]
		iaPD, okPD := options[optIAPD]
                if !okClient || !okPD || len(iaPD) < 12 {
                        log.Printf("ignored DHCPv6 options client=%t ia_pd=%t ia_pd_len=%d", okClient, okPD, len(iaPD))
                        continue
                }
                log.Printf("DHCPv6 request type=%d length=%d peer=%s", buffer[0], n, peer)
		responseType := byte(2)
		if buffer[0] == 3 {
			responseType = 7
		}
		response := append([]byte{responseType}, buffer[1:4]...)
		response = appendOption(response, optClientID, clientID)
		response = appendOption(response, optServerID, serverID)
		response = appendOption(response, optIAPD, delegatedIAPD(iaPD[:4]))
                if _, err := conn.WriteToUDP(response, peer); err != nil {
                        log.Print(err)
                } else {
                        log.Printf("DHCPv6 delegated prefix 2001:db8:100::/56 to %s", peer)
                }
	}
}

func parseOptions(data []byte) map[uint16][]byte {
	result := map[uint16][]byte{}
	for len(data) >= 4 {
		code, length := binary.BigEndian.Uint16(data[:2]), int(binary.BigEndian.Uint16(data[2:4]))
		if length > len(data)-4 {
			break
		}
		result[code] = append([]byte(nil), data[4:4+length]...)
		data = data[4+length:]
	}
	return result
}

func appendOption(dst []byte, code uint16, value []byte) []byte {
	header := make([]byte, 4)
	binary.BigEndian.PutUint16(header[:2], code)
	binary.BigEndian.PutUint16(header[2:], uint16(len(value)))
	return append(append(dst, header...), value...)
}

func delegatedIAPD(iaid []byte) []byte {
	result := append([]byte(nil), iaid...)
	timers := make([]byte, 8)
	binary.BigEndian.PutUint32(timers[:4], 600)
	binary.BigEndian.PutUint32(timers[4:], 1200)
	result = append(result, timers...)
	prefix := make([]byte, 25)
	binary.BigEndian.PutUint32(prefix[:4], 1800)
	binary.BigEndian.PutUint32(prefix[4:8], 3600)
	prefix[8] = 56
	copy(prefix[9:], net.ParseIP("2001:db8:100::").To16())
	return appendOption(result, optIAPrefix, prefix)
}
