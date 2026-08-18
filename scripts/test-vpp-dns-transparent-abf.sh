#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
image=${LY_ROUTE_VPP_TEST_IMAGE:-ly-route/vpp-test:25.10}
smartdns_deb=${LY_ROUTE_SMARTDNS_DEB:-/root/ly-route/runtime-debs/smartdns_0~48.1_amd64.deb}
dns_intercept_plugin=${LY_ROUTE_VPP_DNS_INTERCEPT_PLUGIN:-}
name="lyroute-smartdns-packet-$$"
tmpdir=$(mktemp -d)
keep_container=${LY_ROUTE_KEEP_TEST_CONTAINER:-0}

[ -r "$smartdns_deb" ] || { echo "SmartDNS package is missing: $smartdns_deb" >&2; exit 1; }
if [ -z "$dns_intercept_plugin" ]; then
  dns_intercept_plugin=$(find "$repo_root/build" -type f -name 'ly_route_dns_intercept_plugin.so' -print -quit 2>/dev/null || true)
fi
if [ -z "$dns_intercept_plugin" ] || [ ! -r "$dns_intercept_plugin" ]; then
  echo "DNS intercept VPP plugin is required; set LY_ROUTE_VPP_DNS_INTERCEPT_PLUGIN or build it first" >&2
  exit 1
fi
cleanup() {
  status=$?
  if [ "$status" -ne 0 ] && docker inspect "$name" >/dev/null 2>&1; then
    echo "DNS transparent test failed; preserving container diagnostics:" >&2
    docker exec "$name" sh -c 'for f in /tmp/dns-vpp-proxy.log /tmp/dns-vpp-proxy-v6.log /tmp/smartdns.log /tmp/vpp.log; do if [ -f "$f" ]; then echo "--- $f"; tail -80 "$f"; fi; done' >&2 || true
    docker logs "$name" 2>&1 | tail -80 >&2 || true
  fi
  if [ "$keep_container" -ne 1 ]; then
    docker rm -f "$name" >/dev/null 2>&1 || true
  else
    echo "preserved DNS test container: $name" >&2
  fi
  rm -rf "$tmpdir"
  trap - EXIT INT TERM
  exit "$status"
}
trap cleanup EXIT INT TERM

cat >"$tmpdir/client.go" <<'EOF'
package main

import (
  "bytes"
  "encoding/binary"
  "fmt"
  "net"
  "os"
  "syscall"
  "time"
)

func checksum(data []byte) uint16 {
  var sum uint32
  for len(data) >= 2 { sum += uint32(binary.BigEndian.Uint16(data)); data = data[2:] }
  if len(data) != 0 { sum += uint32(data[0]) << 8 }
  for sum > 0xffff { sum = (sum & 0xffff) + (sum >> 16) }
  return ^uint16(sum)
}

func main() {
  if len(os.Args) != 2 { panic("interface is required") }
  iface, err := net.InterfaceByName(os.Args[1]); if err != nil { panic(err) }
  fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(0x0003))); if err != nil { panic(err) }
  defer syscall.Close(fd)
  addr := &syscall.SockaddrLinklayer{Ifindex: iface.Index, Protocol: htons(0x0800)}
  if err := syscall.Bind(fd, addr); err != nil { panic(err) }
  qname := []byte{7,'u','p','d','a','t','e','s',7,'e','x','a','m','p','l','e',0}
  dns := make([]byte, 12+len(qname)+4); binary.BigEndian.PutUint16(dns[0:2], 0xcafe); binary.BigEndian.PutUint16(dns[2:4], 0x0100); binary.BigEndian.PutUint16(dns[4:6], 1); copy(dns[12:], qname); binary.BigEndian.PutUint16(dns[12+len(qname):], 1); binary.BigEndian.PutUint16(dns[14+len(qname):], 1)
  frame := make([]byte, 14+20+8+len(dns)); copy(frame[0:6], []byte{2,0,0,0,0,1}); copy(frame[6:12], iface.HardwareAddr); binary.BigEndian.PutUint16(frame[12:14], 0x0800)
  ip := frame[14:]; ip[0]=0x45; binary.BigEndian.PutUint16(ip[2:4], uint16(20+8+len(dns))); ip[8]=64; ip[9]=17; copy(ip[12:16], []byte{192,0,2,10}); copy(ip[16:20], []byte{8,8,8,8}); binary.BigEndian.PutUint16(ip[10:12], checksum(ip[:20]))
  udp := ip[20:]; binary.BigEndian.PutUint16(udp[0:2], 40000); binary.BigEndian.PutUint16(udp[2:4], 53); binary.BigEndian.PutUint16(udp[4:6], uint16(8+len(dns))); copy(udp[8:], dns)
  copy(udp[6:8], []byte{0, 0})
  if err := syscall.Sendto(fd, frame, 0, addr); err != nil { panic(err) }
  _ = syscall.SetNonblock(fd, true); deadline := time.Now().Add(5*time.Second); buf := make([]byte, 4096)
  for time.Now().Before(deadline) { n, _, err := syscall.Recvfrom(fd, buf, 0); if err != nil { time.Sleep(10*time.Millisecond); continue }; if n < 14+28 || binary.BigEndian.Uint16(buf[12:14]) != 0x0800 || buf[23] != 17 { continue }; if binary.BigEndian.Uint16(buf[14+20:14+22]) != 53 || binary.BigEndian.Uint16(buf[14+22:14+24]) != 40000 { continue }; fmt.Printf("candidate UDP response source=%v destination=%v\n", net.IP(buf[14+12:14+16]), net.IP(buf[14+16:14+20])); if !bytes.Equal(buf[14+12:14+16], []byte{8,8,8,8}) { continue }; payload := buf[14+20+8:n]; if len(payload) >= 12 && binary.BigEndian.Uint16(payload[:2]) == 0xcafe && binary.BigEndian.Uint16(payload[6:8]) > 0 && bytes.Contains(payload, []byte{203,0,113,53}) { fmt.Println("VPP DNS packet response received with original source 8.8.8.8"); return } }
  panic("timed out waiting for VPP DNS response")
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }
EOF

go build -trimpath -o "$tmpdir/dns-packet-client" "$tmpdir/client.go"
cc -O2 -Wall -Wextra -Werror -std=c11 -o "$tmpdir/dns-vpp-proxy" "$repo_root/packaging/runtime/dns-vpp-proxy.c" -ldl
cat >"$tmpdir/dns-tcp-client.c" <<'EOF'
#include <arpa/inet.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <sys/socket.h>
#include <unistd.h>

static int read_full(int fd, void *buffer, size_t length) {
  size_t offset = 0;
  while (offset < length) { ssize_t n = recv(fd, (uint8_t *)buffer + offset, length - offset, 0); if (n <= 0) return -1; offset += (size_t)n; }
  return 0;
}

static int write_full(int fd, const void *buffer, size_t length) {
  size_t offset = 0;
  while (offset < length) { ssize_t n = send(fd, (const uint8_t *)buffer + offset, length - offset, 0); if (n <= 0) return -1; offset += (size_t)n; }
  return 0;
}

int main(void) {
  uint8_t dns[33] = {0};
  uint8_t frame[35] = {0};
  uint8_t response[65537] = {0};
  struct sockaddr_in address = {.sin_family = AF_INET, .sin_port = htons(53), .sin_addr = {.s_addr = htonl(0xc0000201)}};
  dns[0] = 0xca; dns[1] = 0xfe; dns[2] = 1; dns[5] = 1;
  uint8_t qname[] = {7,'u','p','d','a','t','e','s',7,'e','x','a','m','p','l','e',0};
  memcpy(dns + 12, qname, sizeof(qname)); dns[29] = 0; dns[30] = 1; dns[31] = 0; dns[32] = 1;
  frame[0] = sizeof(dns) >> 8; frame[1] = sizeof(dns) & 0xff; memcpy(frame + 2, dns, sizeof(dns));
  int fd = socket(AF_INET, SOCK_STREAM, 0); if (fd < 0 || connect(fd, (struct sockaddr *)&address, sizeof(address)) < 0) return 1;
  if (write_full(fd, frame, sizeof(frame)) < 0 || read_full(fd, response, 2) < 0) return 1;
  size_t length = ((size_t)response[0] << 8) | response[1]; if (length < 12 || length > 65535 || read_full(fd, response + 2, length) < 0) return 1;
  close(fd);
  for (size_t i = 0; i + 3 < length + 2; i++) if (response[i] == 203 && response[i+1] == 0 && response[i+2] == 113 && response[i+3] == 53) { puts("VPP DNS TCP response received"); return 0; }
  return 1;
}
EOF
cc -O2 -Wall -Wextra -Werror -std=c11 -o "$tmpdir/dns-tcp-client" "$tmpdir/dns-tcp-client.c"
cat >"$tmpdir/tcp-packet-client.go" <<'EOF'
package main

import (
  "bytes"
  "encoding/binary"
  "fmt"
  "net"
  "os"
  "syscall"
  "time"
)

func checksum(data []byte) uint16 { var sum uint32; for len(data) >= 2 { sum += uint32(binary.BigEndian.Uint16(data)); data = data[2:] }; if len(data) != 0 { sum += uint32(data[0]) << 8 }; for sum > 0xffff { sum = (sum & 0xffff) + (sum >> 16) }; return ^uint16(sum) }

func tcpChecksum(src, dst []byte, tcp []byte) uint16 { pseudo := make([]byte, 12+len(tcp)); copy(pseudo[0:4], src); copy(pseudo[4:8], dst); pseudo[9] = 6; binary.BigEndian.PutUint16(pseudo[10:12], uint16(len(tcp))); copy(pseudo[12:], tcp); return checksum(pseudo) }

func makeSegment(iface *net.Interface, seq, ack uint32, flags byte, payload []byte) []byte {
  frame := make([]byte, 14+20+20+len(payload)); copy(frame[0:6], []byte{2,0,0,0,0,1}); copy(frame[6:12], iface.HardwareAddr); binary.BigEndian.PutUint16(frame[12:14], 0x0800)
  ip := frame[14:]; ip[0] = 0x45; binary.BigEndian.PutUint16(ip[2:4], uint16(40+len(payload))); ip[8] = 64; ip[9] = 6; copy(ip[12:16], []byte{192,0,2,10}); copy(ip[16:20], []byte{8,8,8,8}); binary.BigEndian.PutUint16(ip[10:12], checksum(ip[:20]))
  tcp := ip[20:]; binary.BigEndian.PutUint16(tcp[0:2], 40001); binary.BigEndian.PutUint16(tcp[2:4], 53); binary.BigEndian.PutUint32(tcp[4:8], seq); binary.BigEndian.PutUint32(tcp[8:12], ack); tcp[12] = 5 << 4; tcp[13] = flags; binary.BigEndian.PutUint16(tcp[14:16], 65535); copy(tcp[20:], payload); binary.BigEndian.PutUint16(tcp[16:18], tcpChecksum(ip[12:16], ip[16:20], tcp)); return frame
}

func main() {
  if len(os.Args) != 2 { panic("interface is required") }; iface, err := net.InterfaceByName(os.Args[1]); if err != nil { panic(err) }
  fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(0x0003))); if err != nil { panic(err) }; defer syscall.Close(fd); addr := &syscall.SockaddrLinklayer{Ifindex: iface.Index, Protocol: htons(0x0800)}; if err := syscall.Bind(fd, addr); err != nil { panic(err) }; _ = syscall.SetNonblock(fd, true)
  dns := make([]byte, 33); binary.BigEndian.PutUint16(dns[0:2], 0xcafe); binary.BigEndian.PutUint16(dns[2:4], 0x0100); binary.BigEndian.PutUint16(dns[4:6], 1); copy(dns[12:], []byte{7,'u','p','d','a','t','e','s',7,'e','x','a','m','p','l','e',0}); binary.BigEndian.PutUint16(dns[29:31], 1); binary.BigEndian.PutUint16(dns[31:33], 1); payload := append([]byte{0, byte(len(dns))}, dns...)
  clientSeq := uint32(1000); if err := syscall.Sendto(fd, makeSegment(iface, clientSeq, 0, 0x02, nil), 0, addr); err != nil { panic(err) }
  deadline := time.Now().Add(5*time.Second); buf := make([]byte, 4096); var serverSeq uint32
  for time.Now().Before(deadline) { n, _, e := syscall.Recvfrom(fd, buf, 0); if e != nil { time.Sleep(10*time.Millisecond); continue }; if n < 54 || binary.BigEndian.Uint16(buf[12:14]) != 0x0800 || buf[23] != 6 || !bytes.Equal(buf[14+12:14+16], []byte{8,8,8,8}) { continue }; ihl := int(buf[14]&15)*4; tcp := buf[14+ihl:n]; if len(tcp) < 20 || binary.BigEndian.Uint16(tcp[0:2]) != 53 || binary.BigEndian.Uint16(tcp[2:4]) != 40001 { continue }; flags := tcp[13]; if flags&0x12 == 0x12 { serverSeq = binary.BigEndian.Uint32(tcp[4:8]); if err := syscall.Sendto(fd, makeSegment(iface, clientSeq+1, serverSeq+1, 0x10, nil), 0, addr); err != nil { panic(err) }; if err := syscall.Sendto(fd, makeSegment(iface, clientSeq+1, serverSeq+1, 0x18, payload), 0, addr); err != nil { panic(err) }; break } }
  if serverSeq == 0 { panic("timed out waiting for TCP SYN/ACK") }
  for time.Now().Before(deadline.Add(5*time.Second)) { n, _, e := syscall.Recvfrom(fd, buf, 0); if e != nil { time.Sleep(10*time.Millisecond); continue }; if n < 54 || binary.BigEndian.Uint16(buf[12:14]) != 0x0800 || buf[23] != 6 || !bytes.Equal(buf[14+12:14+16], []byte{8,8,8,8}) { continue }; ihl := int(buf[14]&15)*4; tcp := buf[14+ihl:n]; if len(tcp) < 20 || binary.BigEndian.Uint16(tcp[0:2]) != 53 || binary.BigEndian.Uint16(tcp[2:4]) != 40001 { continue }; offset := int(tcp[12]>>4)*4; if len(tcp) < offset { continue }; data := tcp[offset:]; if bytes.Contains(data, []byte{203,0,113,53}) { fmt.Println("VPP DNS TCP packet response received with original source 8.8.8.8"); return }; }
  panic("timed out waiting for VPP TCP DNS response")
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }
EOF
go build -trimpath -o "$tmpdir/dns-tcp-packet-client" "$tmpdir/tcp-packet-client.go"
cat >"$tmpdir/dns-v6-packet-client.go" <<'EOF'
package main

import (
  "bytes"
  "encoding/binary"
  "fmt"
  "net"
  "os"
  "syscall"
  "time"
)

func checksum(data []byte) uint16 { var sum uint32; for len(data) >= 2 { sum += uint32(binary.BigEndian.Uint16(data)); data = data[2:] }; if len(data) != 0 { sum += uint32(data[0]) << 8 }; for sum > 0xffff { sum = (sum & 0xffff) + (sum >> 16) }; return ^uint16(sum) }
func udp6Checksum(src, dst net.IP, udp []byte) uint16 { pseudo := make([]byte, 40+len(udp)); copy(pseudo[0:16], src.To16()); copy(pseudo[16:32], dst.To16()); binary.BigEndian.PutUint32(pseudo[32:36], uint32(len(udp))); pseudo[39] = 17; copy(pseudo[40:], udp); return checksum(pseudo) }

func main() {
  if len(os.Args) != 2 { panic("interface is required") }; iface, err := net.InterfaceByName(os.Args[1]); if err != nil { panic(err) }; fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(0x0003))); if err != nil { panic(err) }; defer syscall.Close(fd); addr := &syscall.SockaddrLinklayer{Ifindex: iface.Index, Protocol: htons(0x86dd)}; if err := syscall.Bind(fd, addr); err != nil { panic(err) }; _ = syscall.SetNonblock(fd, true)
  src := net.ParseIP("2001:db8:1::10").To16(); dst := net.ParseIP("2001:4860:4860::8888").To16(); qname := []byte{7,'u','p','d','a','t','e','s',7,'e','x','a','m','p','l','e',0}; dns := make([]byte, 12+len(qname)+4); binary.BigEndian.PutUint16(dns[0:2], 0xcafe); binary.BigEndian.PutUint16(dns[2:4], 0x0100); binary.BigEndian.PutUint16(dns[4:6], 1); copy(dns[12:], qname); binary.BigEndian.PutUint16(dns[12+len(qname):], 1); binary.BigEndian.PutUint16(dns[14+len(qname):], 1)
  udp := make([]byte, 8+len(dns)); binary.BigEndian.PutUint16(udp[0:2], 40002); binary.BigEndian.PutUint16(udp[2:4], 53); binary.BigEndian.PutUint16(udp[4:6], uint16(len(udp))); copy(udp[8:], dns); binary.BigEndian.PutUint16(udp[6:8], udp6Checksum(src, dst, udp)); frame := make([]byte, 14+40+len(udp)); copy(frame[0:6], []byte{2,0,0,0,0,1}); copy(frame[6:12], iface.HardwareAddr); binary.BigEndian.PutUint16(frame[12:14], 0x86dd); ip := frame[14:]; ip[0] = 0x60; binary.BigEndian.PutUint16(ip[4:6], uint16(len(udp))); ip[6] = 17; ip[7] = 64; copy(ip[8:24], src); copy(ip[24:40], dst); copy(ip[40:], udp); if err := syscall.Sendto(fd, frame, 0, addr); err != nil { panic(err) }
  deadline := time.Now().Add(5*time.Second); buf := make([]byte, 4096); for time.Now().Before(deadline) { n, _, e := syscall.Recvfrom(fd, buf, 0); if e != nil { time.Sleep(10*time.Millisecond); continue }; if n < 14+40+8 || binary.BigEndian.Uint16(buf[12:14]) != 0x86dd || buf[20] != 17 || !bytes.Equal(buf[14+8:14+24], dst) { continue }; udp := buf[14+40:n]; if binary.BigEndian.Uint16(udp[0:2]) != 53 || binary.BigEndian.Uint16(udp[2:4]) != 40002 { continue }; if bytes.Contains(udp[8:], []byte{203,0,113,53}) { fmt.Println("VPP DNS IPv6 packet response received with original source 2001:4860:4860::8888"); return } }; panic("timed out waiting for VPP IPv6 DNS response")
}
func htons(v uint16) uint16 { return v<<8 | v>>8 }
EOF
go build -trimpath -o "$tmpdir/dns-v6-packet-client" "$tmpdir/dns-v6-packet-client.go"
cat >"$tmpdir/dns-v6-tcp-packet-client.go" <<'EOF'
package main

import (
  "bytes"
  "encoding/binary"
  "fmt"
  "net"
  "os"
  "syscall"
  "time"
)

var source = net.ParseIP("2001:db8:1::10").To16()
var destination = net.ParseIP("2001:4860:4860::8888").To16()

func checksum(data []byte) uint16 { var sum uint32; for len(data) >= 2 { sum += uint32(binary.BigEndian.Uint16(data)); data = data[2:] }; if len(data) != 0 { sum += uint32(data[0]) << 8 }; for sum > 0xffff { sum = (sum & 0xffff) + (sum >> 16) }; return ^uint16(sum) }
func tcp6Checksum(tcp []byte) uint16 { pseudo := make([]byte, 40+len(tcp)); copy(pseudo[0:16], source); copy(pseudo[16:32], destination); binary.BigEndian.PutUint32(pseudo[32:36], uint32(len(tcp))); pseudo[39] = 6; copy(pseudo[40:], tcp); return checksum(pseudo) }

func makeSegment(iface *net.Interface, seq, ack uint32, flags byte, payload []byte) []byte {
  frame := make([]byte, 14+40+20+len(payload)); copy(frame[0:6], []byte{2,0,0,0,0,1}); copy(frame[6:12], iface.HardwareAddr); binary.BigEndian.PutUint16(frame[12:14], 0x86dd)
  ip := frame[14:]; ip[0] = 0x60; binary.BigEndian.PutUint16(ip[4:6], uint16(20+len(payload))); ip[6] = 6; ip[7] = 64; copy(ip[8:24], source); copy(ip[24:40], destination)
  tcp := ip[40:]; binary.BigEndian.PutUint16(tcp[0:2], 40003); binary.BigEndian.PutUint16(tcp[2:4], 53); binary.BigEndian.PutUint32(tcp[4:8], seq); binary.BigEndian.PutUint32(tcp[8:12], ack); tcp[12] = 5 << 4; tcp[13] = flags; binary.BigEndian.PutUint16(tcp[14:16], 65535); copy(tcp[20:], payload); binary.BigEndian.PutUint16(tcp[16:18], tcp6Checksum(tcp)); return frame
}

func main() {
  if len(os.Args) != 2 { panic("interface is required") }; iface, err := net.InterfaceByName(os.Args[1]); if err != nil { panic(err) }
  fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(0x0003))); if err != nil { panic(err) }; defer syscall.Close(fd); addr := &syscall.SockaddrLinklayer{Ifindex: iface.Index, Protocol: htons(0x86dd)}; if err := syscall.Bind(fd, addr); err != nil { panic(err) }; _ = syscall.SetNonblock(fd, true)
  dns := make([]byte, 33); binary.BigEndian.PutUint16(dns[0:2], 0xcafe); binary.BigEndian.PutUint16(dns[2:4], 0x0100); binary.BigEndian.PutUint16(dns[4:6], 1); copy(dns[12:], []byte{7,'u','p','d','a','t','e','s',7,'e','x','a','m','p','l','e',0}); binary.BigEndian.PutUint16(dns[29:31], 1); binary.BigEndian.PutUint16(dns[31:33], 1); payload := append([]byte{0, byte(len(dns))}, dns...)
  clientSeq := uint32(2000); if err := syscall.Sendto(fd, makeSegment(iface, clientSeq, 0, 0x02, nil), 0, addr); err != nil { panic(err) }
  deadline := time.Now().Add(5*time.Second); buf := make([]byte, 4096); var serverSeq uint32
  for time.Now().Before(deadline) { n, _, e := syscall.Recvfrom(fd, buf, 0); if e != nil { time.Sleep(10*time.Millisecond); continue }; if n < 74 || binary.BigEndian.Uint16(buf[12:14]) != 0x86dd || buf[20] != 6 || !bytes.Equal(buf[14+8:14+24], destination) { continue }; tcp := buf[14+40:n]; if len(tcp) < 20 || binary.BigEndian.Uint16(tcp[0:2]) != 53 || binary.BigEndian.Uint16(tcp[2:4]) != 40003 { continue }; if tcp[13]&0x12 == 0x12 { serverSeq = binary.BigEndian.Uint32(tcp[4:8]); if err := syscall.Sendto(fd, makeSegment(iface, clientSeq+1, serverSeq+1, 0x10, nil), 0, addr); err != nil { panic(err) }; if err := syscall.Sendto(fd, makeSegment(iface, clientSeq+1, serverSeq+1, 0x18, payload), 0, addr); err != nil { panic(err) }; break } }
  if serverSeq == 0 { panic("timed out waiting for IPv6 TCP SYN/ACK") }
  for time.Now().Before(deadline.Add(5*time.Second)) { n, _, e := syscall.Recvfrom(fd, buf, 0); if e != nil { time.Sleep(10*time.Millisecond); continue }; if n < 74 || binary.BigEndian.Uint16(buf[12:14]) != 0x86dd || buf[20] != 6 || !bytes.Equal(buf[14+8:14+24], destination) { continue }; tcp := buf[14+40:n]; if len(tcp) < 20 || binary.BigEndian.Uint16(tcp[0:2]) != 53 || binary.BigEndian.Uint16(tcp[2:4]) != 40003 { continue }; offset := int(tcp[12]>>4)*4; if len(tcp) < offset { continue }; if bytes.Contains(tcp[offset:], []byte{203,0,113,53}) { fmt.Println("VPP DNS IPv6 TCP packet response received with original source 2001:4860:4860::8888"); return } }
  panic("timed out waiting for VPP IPv6 TCP DNS response")
}
func htons(v uint16) uint16 { return v<<8 | v>>8 }
EOF
go build -trimpath -o "$tmpdir/dns-v6-tcp-packet-client" "$tmpdir/dns-v6-tcp-packet-client.go"
(cd "$repo_root/backend" && go test -c -o "$tmpdir/vpp-runtime.test" ./internal/runtime/vpp)
container_id=$(docker create --name "$name" --network none --device /dev/net/tun --device /dev/vhost-net \
  --cap-add NET_ADMIN --cap-add NET_RAW \
  -v "$smartdns_deb:/tmp/smartdns.deb:ro" \
  -v "$dns_intercept_plugin:/usr/lib/x86_64-linux-gnu/vpp_plugins/ly_route_dns_intercept_plugin.so:ro" \
  -v "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/dns-vpp-v6-namespace-apply:/tmp/dns-vpp-namespace-apply:ro" \
  -v "$repo_root/packaging/rootfs-overlay/usr/lib/ly-route/dns-vpp-session-apply:/tmp/dns-vpp-session-apply:ro" \
  --entrypoint sh "$image" -c 'printf "unix {\n  nodaemon\n  cli-listen /run/vpp/cli.sock\n  runtime-dir /run/vpp\n}\nsession {\n  enable rt-backend rule-table\n  use-app-socket-api\n}\n" >/tmp/vpp.conf; vpp -c /tmp/vpp.conf >/tmp/vpp.log 2>&1')
docker start "$name" >/dev/null
docker cp "$tmpdir/dns-packet-client" "$name:/tmp/dns-packet-client"
docker cp "$tmpdir/dns-tcp-client" "$name:/tmp/dns-tcp-client"
docker cp "$tmpdir/dns-tcp-packet-client" "$name:/tmp/dns-tcp-packet-client"
docker cp "$tmpdir/dns-v6-packet-client" "$name:/tmp/dns-v6-packet-client"
docker cp "$tmpdir/dns-v6-tcp-packet-client" "$name:/tmp/dns-v6-tcp-packet-client"
docker cp "$tmpdir/dns-vpp-proxy" "$name:/tmp/dns-vpp-proxy"
docker cp "$tmpdir/vpp-runtime.test" "$name:/tmp/vpp-runtime.test"
docker exec "$name" sh -c '
  set -eu
  for i in $(seq 1 30); do test -S /run/vpp/cli.sock && break; sleep 1; done
  vppctl "create tap id 0 hw-addr 02:00:00:00:00:01 host-if-name lyroute-dns-tes host-mac-addr 02:00:00:00:00:02"
  vppctl "set interface ip address tap0 192.0.2.1/24"
  vppctl "set interface ip address tap0 2001:db8:1::1/64"
  vppctl "set interface state tap0 up"
  vppctl "create tap id 1 hw-addr 02:00:00:00:01:01 host-if-name lyroute-wan-tes host-mac-addr 02:00:00:00:01:02"
  vppctl "set interface ip address tap1 203.0.113.2/24"
  vppctl "set interface state tap1 up"
  vppctl "nat44 plugin enable"
  vppctl "set interface nat44 in tap0"
  vppctl "set interface nat44 out tap1 output-feature"
  vppctl "nat44 add address 203.0.113.2"
  LY_ROUTE_VPPCTL_INTEGRATION_BINARY=vppctl /tmp/vpp-runtime.test -test.run '^TestDNSTransparentVPPCTLIntegration$' -test.v
  LY_ROUTE_VPPCTL=vppctl sh /tmp/dns-vpp-namespace-apply
  vppctl "show acl-plugin acl"
  vppctl "show abf policy"
  vppctl "show abf attach tap0"
  vppctl "show interface features tap0"
  vppctl "show nat44 interfaces"
  vppctl "trace add virtio-input 20"
  vppctl "set ip neighbor tap0 192.0.2.10 02:00:00:00:00:02"
  vppctl "set ip neighbor tap0 2001:db8:1::10 02:00:00:00:00:02"
  dpkg-deb -x /tmp/smartdns.deb /opt/smartdns
  printf "vcl {\n  app-socket-api /run/vpp/app_ns_sockets/dns-v4\n  app-scope-local\n  app-scope-global\n  app_original_dst\n  namespace-id dns-v4\n  namespace-secret 4242\n}\n" >/tmp/vcl.conf
  printf "vcl {\n  app-socket-api /run/vpp/app_ns_sockets/dns-v6\n  app-scope-local\n  app-scope-global\n  app_original_dst\n  namespace-id dns-v6\n  namespace-secret 4242\n}\n" >/tmp/vcl-v6.conf
  printf "bind 127.0.0.1:1053\nbind-tcp 127.0.0.1:1053\naddress /updates.example/203.0.113.54\nbind 127.0.0.1:12000 -group source-client\nbind-tcp 127.0.0.1:12000 -group source-client\ngroup-begin source-client -inherit none\naddress /updates.example/203.0.113.53\ngroup-end\n" >/tmp/smartdns.conf
  printf "# source-prefix match-kind domain smartdns-port\n192.0.2.0/24 exact updates.example 12000\n2001:db8:1::/64 exact updates.example 12000\n" >/tmp/dns-source-routes.conf
  /opt/smartdns/usr/sbin/smartdns -f -x -p - -c /tmp/smartdns.conf >/tmp/smartdns.log 2>&1 &
  echo $! >/tmp/smartdns.pid
  sleep 3
  if ! kill -0 "$(cat /tmp/smartdns.pid)"; then
    cat /tmp/smartdns.log /tmp/vpp.log >&2 || true
    ps aux >&2 || true
    exit 1
  fi
  ! grep -q "bind service .* failed" /tmp/smartdns.log
  LY_ROUTE_DNS_ROUTE_DEBUG=1 LY_ROUTE_DNS_SOURCE_ROUTES=/tmp/dns-source-routes.conf LY_ROUTE_DNS_FAMILY=ipv4 VCL_APP_NAMESPACE_ID=dns-v4 VCL_APP_NAMESPACE_SECRET=4242 VCL_CONFIG=/tmp/vcl.conf VCL_VPP_API_SOCKET=/run/vpp/api.sock LD_PRELOAD=/usr/lib/x86_64-linux-gnu/libvcl_ldpreload.so.25.10 /tmp/dns-vpp-proxy >/tmp/dns-vpp-proxy.log 2>&1 &
  echo $! >/tmp/dns-vpp-proxy.pid
  sleep 2
  if ! kill -0 "$(cat /tmp/dns-vpp-proxy.pid)"; then
    cat /tmp/dns-vpp-proxy.log /tmp/smartdns.log /tmp/vpp.log >&2 || true
    ps aux >&2 || true
    exit 1
  fi
  cp /tmp/dns-vpp-proxy /tmp/dns-vpp-proxy-v6
  LY_ROUTE_DNS_ROUTE_DEBUG=1 LY_ROUTE_DNS_SOURCE_ROUTES=/tmp/dns-source-routes.conf LY_ROUTE_DNS_FAMILY=ipv6 VCL_APP_NAMESPACE_ID=dns-v6 VCL_APP_NAMESPACE_SECRET=4242 VCL_CONFIG=/tmp/vcl-v6.conf VCL_VPP_API_SOCKET=/run/vpp/api.sock LD_PRELOAD=/usr/lib/x86_64-linux-gnu/libvcl_ldpreload.so.25.10 /tmp/dns-vpp-proxy-v6 >/tmp/dns-vpp-proxy-v6.log 2>&1 &
  echo $! >/tmp/dns-vpp-proxy-v6.pid
  sleep 2
  if ! kill -0 "$(cat /tmp/dns-vpp-proxy-v6.pid)"; then
    cat /tmp/dns-vpp-proxy-v6.log /tmp/vpp.log >&2 || true
    vppctl show app ns >&2 || true
    exit 1
  fi
  if ! LY_ROUTE_SMARTDNS_V4_APP_PATTERN="^dns-vpp-proxy-ldp" LY_ROUTE_SMARTDNS_V6_APP_PATTERN="^dns-vpp-proxy-v6-ldp" sh /tmp/dns-vpp-session-apply; then
    cat /tmp/dns-vpp-proxy.log /tmp/smartdns.log /tmp/vpp.log >&2 || true
    vppctl show app >&2 || true
    exit 1
  fi
  if ! /tmp/dns-packet-client lyroute-dns-tes; then
    cat /tmp/dns-vpp-proxy.log /tmp/smartdns.log >&2 || true
    vppctl show app verbose >&2 || true
    vppctl show acl-plugin acl index 0 >&2 || true
    vppctl show abf policy >&2 || true
    vppctl show abf attach tap0 >&2 || true
    vppctl show trace >&2 || true
    vppctl show session verbose >&2 || true
    vppctl show interface >&2 || true
    vppctl show session stats >&2 || true
    exit 1
  fi
  if ! /tmp/dns-tcp-packet-client lyroute-dns-tes; then
    cat /tmp/dns-vpp-proxy.log /tmp/smartdns.log >&2 || true
    vppctl show app verbose >&2 || true
    vppctl show session verbose >&2 || true
    exit 1
  fi
  if ! /tmp/dns-v6-packet-client lyroute-dns-tes; then
    cat /tmp/dns-vpp-proxy.log /tmp/smartdns.log >&2 || true
    vppctl show app verbose >&2 || true
    vppctl "show session rules appns dns-v6" >&2 || true
    vppctl show session verbose >&2 || true
    vppctl "show acl-plugin acl index 1" >&2 || true
    vppctl "show abf policy" >&2 || true
    vppctl "show abf attach tap0" >&2 || true
    vppctl "show ip fib table 101" >&2 || true
    vppctl show trace >&2 || true
    vppctl show errors >&2 || true
    exit 1
  fi
  if ! /tmp/dns-v6-tcp-packet-client lyroute-dns-tes; then
    cat /tmp/dns-vpp-proxy-v6.log /tmp/smartdns.log >&2 || true
    vppctl show app verbose >&2 || true
    vppctl show session verbose >&2 || true
    vppctl show trace >&2 || true
    exit 1
  fi
  grep -q "source=192.0.2.10 domain=updates.example port=12000" /tmp/dns-vpp-proxy.log
  grep -q "source=2001:db8:1::10 domain=updates.example port=12000" /tmp/dns-vpp-proxy-v6.log
  vppctl show version >/dev/null
  kill "$(cat /tmp/dns-vpp-proxy.pid)" "$(cat /tmp/dns-vpp-proxy-v6.pid)" "$(cat /tmp/smartdns.pid)" 2>/dev/null || true
'

printf '%s\n' 'VPP ABF arbitrary-target SmartDNS IPv4/IPv6 UDP/TCP packet-flow verification passed'
