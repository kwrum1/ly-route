package vpp

import (
	"reflect"
	"testing"
)

func TestDNSPolicyAttachmentsParsesFamilyAndInterface(t *testing.T) {
	output := `tap0
ipv4:
 abf-interface-attach: policy:9100 priority:0
ipv6:
 abf-interface-attach: policy:9200 priority:0
lyroute-lan1
ipv4:
 abf-interface-attach: policy:9100 priority:0
ipv6:
 abf-interface-attach: policy:9300 priority:0
`
	if got := dnsPolicyAttachments(output, 9100, "ip4"); !reflect.DeepEqual(got, []string{"tap0", "lyroute-lan1"}) {
		t.Fatalf("IPv4 attachments = %#v", got)
	}
	if got := dnsPolicyAttachments(output, 9200, "ip6"); !reflect.DeepEqual(got, []string{"tap0"}) {
		t.Fatalf("IPv6 attachments = %#v", got)
	}
}
