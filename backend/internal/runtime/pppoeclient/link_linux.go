//go:build linux

package pppoeclient

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"golang.org/x/sys/unix"
)

type Link interface {
	MAC() MAC
	Send(context.Context, Frame) error
	Receive(context.Context) (Frame, error)
	Close() error
}

type RawLink struct {
	fd      int
	ifIndex int
	mac     MAC
}

func OpenRawLink(interfaceName string) (*RawLink, error) {
	iface, err := net.InterfaceByName(interfaceName)
	if err != nil {
		return nil, err
	}
	if len(iface.HardwareAddr) != 6 {
		return nil, fmt.Errorf("interface %s has no Ethernet MAC", interfaceName)
	}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		return nil, err
	}
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: htons(unix.ETH_P_ALL), Ifindex: iface.Index}); err != nil {
		unix.Close(fd)
		return nil, err
	}
	link := &RawLink{fd: fd, ifIndex: iface.Index}
	copy(link.mac[:], iface.HardwareAddr)
	return link, nil
}

func (link *RawLink) MAC() MAC { return link.mac }

func (link *RawLink) Send(ctx context.Context, frame Frame) error {
	packet := make([]byte, 14+len(frame.Payload))
	copy(packet[:6], frame.Destination[:])
	copy(packet[6:12], frame.Source[:])
	binary.BigEndian.PutUint16(packet[12:14], frame.EtherType)
	copy(packet[14:], frame.Payload)
	if err := ctx.Err(); err != nil {
		return err
	}
	return unix.Sendto(link.fd, packet, 0, &unix.SockaddrLinklayer{Ifindex: link.ifIndex, Protocol: htons(frame.EtherType), Halen: 6, Addr: [8]byte{frame.Destination[0], frame.Destination[1], frame.Destination[2], frame.Destination[3], frame.Destination[4], frame.Destination[5]}})
}

func (link *RawLink) Receive(ctx context.Context) (Frame, error) {
	buffer := make([]byte, 2048)
	for {
		if err := ctx.Err(); err != nil {
			return Frame{}, err
		}
		poll := []unix.PollFd{{Fd: int32(link.fd), Events: unix.POLLIN}}
		ready, err := unix.Poll(poll, 250)
		if err != nil && !errors.Is(err, unix.EINTR) {
			return Frame{}, err
		}
		if ready == 0 || poll[0].Revents&unix.POLLIN == 0 {
			continue
		}
		length, _, err := unix.Recvfrom(link.fd, buffer, 0)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			return Frame{}, err
		}
		if length < 14 {
			continue
		}
		var frame Frame
		copy(frame.Destination[:], buffer[:6])
		copy(frame.Source[:], buffer[6:12])
		frame.EtherType = binary.BigEndian.Uint16(buffer[12:14])
		frame.Payload = append([]byte(nil), buffer[14:length]...)
		return frame, nil
	}
}

func (link *RawLink) Close() error { return unix.Close(link.fd) }

func htons(value uint16) uint16 { return value<<8 | value>>8 }

var _ = time.Second
