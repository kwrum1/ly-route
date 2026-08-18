package proxy

import (
	"reflect"
	"testing"
)

func TestXrayNodeBootstrapDNSIsFixedToBuiltinForeignDoHBootstrap(t *testing.T) {
	want := [5]string{"1.1.1.1", "1.0.0.1", "8.8.8.8", "8.8.4.4", "9.9.9.9"}
	if !reflect.DeepEqual(nodeBootstrapServers, want) {
		t.Fatalf("Xray node Bootstrap DNS = %#v, want fixed built-in DoH bootstrap %#v", nodeBootstrapServers, want)
	}
}
