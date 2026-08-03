package pppoeclient

import "testing"

func TestIPv6CPInterfaceIdentifierAndLinkLocal(t *testing.T) {
	iid := interfaceIdentifier(MAC{0x02, 0x11, 0x22, 0x33, 0x44, 0x55})
	want := [8]byte{0x00, 0x11, 0x22, 0xff, 0xfe, 0x33, 0x44, 0x55}
	if iid != want {
		t.Fatalf("IID = %x, want %x", iid, want)
	}
	if got := linkLocalFromIID(iid).String(); got != "fe80::11:22ff:fe33:4455" {
		t.Fatalf("link-local = %s", got)
	}
}

func TestIPv6CPInterfaceIdentifierOption(t *testing.T) {
	want := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	got, ok := ipv6CPInterfaceIdentifier(append([]byte{1, 10}, want[:]...))
	if !ok || got != want {
		t.Fatalf("IID = %x, %v", got, ok)
	}
	if _, ok := ipv6CPInterfaceIdentifier([]byte{2, 4, 0, 0}); ok {
		t.Fatal("accepted unsupported option")
	}
}
