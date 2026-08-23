package pppoeclient

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type silentLink struct {
	sends atomic.Int32
}

func (link *silentLink) MAC() MAC {
	return MAC{0, 1, 2, 3, 4, 5}
}

func (link *silentLink) Send(context.Context, Frame) error {
	link.sends.Add(1)
	return nil
}

func (link *silentLink) Receive(ctx context.Context) (Frame, error) {
	<-ctx.Done()
	return Frame{}, ctx.Err()
}

func (link *silentLink) Close() error {
	return nil
}

func TestDelegatedPrefixRecoveryKeepsSessionAfterInitialTimeout(t *testing.T) {
	link := &silentLink{}
	client, err := New(Config{Interface: "test0", Username: "user", Timeout: time.Millisecond, Retries: 1}, link)
	if err != nil {
		t.Fatal(err)
	}
	client.sid = 7
	client.acMAC = MAC{0, 10, 11, 12, 13, 14}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err = client.ServeWithDelegatedPrefixRecovery(ctx, Session{IPv6Ready: true, LocalIPv6: "fe80::1"}, DelegatedPrefixLease{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("recovery returned %v, want caller deadline", err)
	}
	if link.sends.Load() == 0 {
		t.Fatal("recovery did not attempt DHCPv6-PD acquisition")
	}
}
