package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestFilesystemControllerReadsXrayObservatoryHealthAndDelay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/debug/vars" {
			t.Fatalf("metrics path = %q", request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"observatory":{"subscription-sub-node-a":{"alive":true,"delay":10,"outbound_tag":"subscription-sub-node-a","last_try_time":100},"subscription-sub-node-b":{"alive":false,"delay":20,"outbound_tag":"subscription-sub-node-b","last_try_time":101}}}`))
	}))
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	states, err := (FilesystemController{XrayMetricsAddress: endpoint.Host}).XrayObservatoryStates(context.Background())

	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states[0].OutboundTag != "subscription-sub-node-a" || !states[0].Alive || states[0].DelayMilliseconds != 10 {
		t.Fatalf("observatory states = %#v", states)
	}
	if states[1].OutboundTag != "subscription-sub-node-b" || states[1].Alive {
		t.Fatalf("dead observatory state = %#v", states[1])
	}
}
