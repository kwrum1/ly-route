package pppoeclient

import (
	"reflect"
	"testing"
)

func TestPPPoEBindingCommandTargetsOneWAN(t *testing.T) {
	want := []string{"set", "ly-route", "pppoe-client", "control-interface", "tap701", "wan-interface", "lyroute-ens35", "disable"}
	if got := pppoeBindingCommand("tap701", "lyroute-ens35", true); !reflect.DeepEqual(got, want) {
		t.Fatalf("binding command = %#v, want %#v", got, want)
	}
}
