package flow

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestIntentRepresentsClassifyRemarkPolicerAndGranularity(t *testing.T) {
	remarkBehavior := RemarkBehavior{ProtectedClass: "bulk", DownstreamPolicy: "wan-protect-bulk"}
	intent := NewIntent("default", []Rule{
		NewRule("classify-video", RuleGranularity, Classify("video")),
		NewClassRule("remark-bulk", "bulk", RemarkWithBehavior("AF11", remarkBehavior)),
		NewClassRule("police-bulk", "bulk", Police(10_000_000, 1_000_000)),
	})

	if intent.Rules[0].Granularity != RuleGranularity {
		t.Fatalf("first rule granularity = %q, want rule", intent.Rules[0].Granularity)
	}
	if intent.Rules[1].Granularity != ClassGranularity || intent.Rules[2].Granularity != ClassGranularity {
		t.Fatalf("class rule granularities = %q/%q, want class/class", intent.Rules[1].Granularity, intent.Rules[2].Granularity)
	}
	if intent.Rules[1].Class != "bulk" || intent.Rules[2].Class != "bulk" {
		t.Fatalf("class rule classes = %q/%q, want bulk/bulk", intent.Rules[1].Class, intent.Rules[2].Class)
	}
	if intent.Rules[0].Actions[0].Kind != ActionClassify || intent.Rules[1].Actions[0].Kind != ActionRemark || intent.Rules[2].Actions[0].Kind != ActionPolicer {
		t.Fatalf("action kinds = %q/%q/%q, want classify/remark/policer", intent.Rules[0].Actions[0].Kind, intent.Rules[1].Actions[0].Kind, intent.Rules[2].Actions[0].Kind)
	}
	if intent.Rules[1].Actions[0].RemarkBehavior == nil || *intent.Rules[1].Actions[0].RemarkBehavior != remarkBehavior {
		t.Fatalf("remark behavior = %#v, want explicit protected class and downstream policy", intent.Rules[1].Actions[0].RemarkBehavior)
	}
	if intent.Rules[2].Actions[0].Policer == nil || intent.Rules[2].Actions[0].Policer.RateBPS != 10_000_000 {
		t.Fatalf("policer = %#v, want configured token bucket", intent.Rules[2].Actions[0].Policer)
	}
}

func TestFlowIntentDoesNotExposeHiddenOrOutOfScopeFeatures(t *testing.T) {
	payload, err := json.Marshal(NewIntent("default", []Rule{
		NewRule("classify-video", RuleGranularity, Classify("video"), Remark("AF41"), Police(10_000_000, 1_000_000)),
	}))
	if err != nil {
		t.Fatal(err)
	}

	encoded := string(payload)
	for _, required := range []string{"classify", "remark", "policer", "rule"} {
		if !strings.Contains(encoded, required) {
			t.Fatalf("flow intent payload missing %q: %s", required, encoded)
		}
	}
	for _, forbidden := range forbiddenFlowControlTerms() {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("flow intent payload leaked forbidden field %q: %s", forbidden, encoded)
		}
	}
}

func TestCompileIntentProducesDeterministicTargets(t *testing.T) {
	remarkBehavior := RemarkBehavior{ProtectedClass: "video", DownstreamPolicy: "wan-priority"}
	intent := NewIntent("default", []Rule{
		NewRule("classify-video", RuleGranularity, Classify("video")),
		NewClassRule("classify-bulk", "bulk", Classify("bulk")),
		NewClassRule("remark-video", "video", RemarkWithBehavior("AF41", remarkBehavior)),
		NewClassRule("police-bulk", "bulk", Police(10_000_000, 1_000_000)),
	})
	compiled, err := CompileIntent(intent)
	if err != nil {
		t.Fatalf("compile intent: %v", err)
	}

	if compiled.ID != "default" {
		t.Fatalf("compiled id = %q, want default", compiled.ID)
	}
	if len(compiled.Targets) != 4 {
		t.Fatalf("target count = %d, want 4", len(compiled.Targets))
	}
	if compiled.Targets[0].Kind != "vpp.qos.classify" || compiled.Targets[0].RuleID != "classify-video" || compiled.Targets[0].Granularity != RuleGranularity || compiled.Targets[0].Class != "video" {
		t.Fatalf("classify target = %#v, want rule granular vpp.qos.classify for video", compiled.Targets[0])
	}
	if compiled.Targets[1].Kind != "vpp.qos.classify" || compiled.Targets[1].RuleID != "classify-bulk" || compiled.Targets[1].Granularity != ClassGranularity || compiled.Targets[1].Class != "bulk" {
		t.Fatalf("second classify target = %#v, want class granular vpp.qos.classify for bulk", compiled.Targets[1])
	}
	if compiled.Targets[2].Kind != "vpp.qos.mark" || compiled.Targets[2].RuleID != "remark-video" || compiled.Targets[2].Granularity != ClassGranularity || compiled.Targets[2].Class != "video" || compiled.Targets[2].DSCP != "AF41" || compiled.Targets[2].RemarkBehavior == nil || *compiled.Targets[2].RemarkBehavior != remarkBehavior {
		t.Fatalf("remark target = %#v, want class granular vpp.qos.mark for video AF41 with explicit behavior", compiled.Targets[2])
	}
	if compiled.Targets[2].RemarkBehavior == intent.Rules[2].Actions[0].RemarkBehavior {
		t.Fatal("compiled remark behavior must not alias input action behavior")
	}
	if compiled.Targets[3].Kind != "vpp.policer" || compiled.Targets[3].RuleID != "police-bulk" || compiled.Targets[3].Granularity != ClassGranularity || compiled.Targets[3].Class != "bulk" || compiled.Targets[3].Policer == nil {
		t.Fatalf("policer target = %#v, want class granular vpp.policer with token bucket", compiled.Targets[3])
	}
	if compiled.Targets[3].Policer.RateBPS != 10_000_000 || compiled.Targets[3].Policer.BurstBPS != 1_000_000 {
		t.Fatalf("policer token bucket = %#v, want 10000000/1000000", compiled.Targets[3].Policer)
	}
	if compiled.Targets[3].Policer == intent.Rules[3].Actions[0].Policer {
		t.Fatal("compiled policer must not alias input action policer")
	}

	wantGroups := []VPPObjectGroup{
		{Kind: "vpp.qos.classify", Objects: []VPPObject{
			{Name: "default/rule/classify-video/qos.classify", RuleID: "classify-video", Granularity: RuleGranularity, Action: ActionClassify, Class: "video"},
			{Name: "default/class/bulk/qos.classify", RuleID: "classify-bulk", Granularity: ClassGranularity, Action: ActionClassify, Class: "bulk"},
		}},
		{Kind: "vpp.qos.record", Objects: []VPPObject{
			{Name: "default/rule/classify-video/qos.record", RuleID: "classify-video", Granularity: RuleGranularity, Action: ActionClassify, Class: "video"},
			{Name: "default/class/bulk/qos.record", RuleID: "classify-bulk", Granularity: ClassGranularity, Action: ActionClassify, Class: "bulk"},
		}},
		{Kind: "vpp.qos.store", Objects: []VPPObject{
			{Name: "default/rule/classify-video/qos.store", RuleID: "classify-video", Granularity: RuleGranularity, Action: ActionClassify, Class: "video"},
			{Name: "default/class/bulk/qos.store", RuleID: "classify-bulk", Granularity: ClassGranularity, Action: ActionClassify, Class: "bulk"},
		}},
		{Kind: "vpp.qos.egress-map", Objects: []VPPObject{
			{Name: "default/class/video/qos.egress-map", RuleID: "remark-video", Granularity: ClassGranularity, Action: ActionRemark, Class: "video", DSCP: "AF41", RemarkBehavior: &RemarkBehavior{ProtectedClass: "video", DownstreamPolicy: "wan-priority"}},
		}},
		{Kind: "vpp.qos.mark", Objects: []VPPObject{
			{Name: "default/class/video/qos.mark", RuleID: "remark-video", Granularity: ClassGranularity, Action: ActionRemark, Class: "video", DSCP: "AF41", RemarkBehavior: &RemarkBehavior{ProtectedClass: "video", DownstreamPolicy: "wan-priority"}},
		}},
		{Kind: "vpp.policer", Objects: []VPPObject{
			{Name: "default/class/bulk/policer", RuleID: "police-bulk", Granularity: ClassGranularity, Action: ActionPolicer, Class: "bulk", Policer: &Policer{RateBPS: 10_000_000, BurstBPS: 1_000_000}},
		}},
	}
	if !reflect.DeepEqual(compiled.VPPGroups, wantGroups) {
		t.Fatalf("vpp groups = %#v, want %#v", compiled.VPPGroups, wantGroups)
	}
	if compiled.VPPGroups[5].Objects[0].Policer == intent.Rules[3].Actions[0].Policer {
		t.Fatal("compiled VPP policer must not alias input action policer")
	}
	if compiled.VPPGroups[3].Objects[0].RemarkBehavior == intent.Rules[2].Actions[0].RemarkBehavior || compiled.VPPGroups[4].Objects[0].RemarkBehavior == intent.Rules[2].Actions[0].RemarkBehavior {
		t.Fatal("compiled VPP remark behavior must not alias input action behavior")
	}
}

func TestCompileIntentProducesBehaviorPolicyTargets(t *testing.T) {
	intent := NewIntent("behavior", []Rule{
		{ID: "drop-guest", Granularity: RuleGranularity, Match: Match{Sources: []string{"192.168.20.0/24"}, Destinations: []string{"10.0.0.0/8"}, Protocols: []string{"tcp"}, DestPorts: []string{"443"}, Direction: "uplink"}, Actions: []Action{Drop()}},
		{ID: "limit-video", Granularity: RuleGranularity, Match: Match{Destinations: []string{"203.0.113.10"}, Protocols: []string{"udp"}, Direction: "downlink"}, Actions: []Action{Police(20_000_000, 2_000_000)}},
	})

	compiled, err := CompileIntent(intent)
	if err != nil {
		t.Fatalf("compile behavior intent: %v", err)
	}
	if len(compiled.Targets) != 2 {
		t.Fatalf("targets = %#v, want two behavior targets", compiled.Targets)
	}
	drop := compiled.Targets[0]
	if drop.Kind != "vpp.acl.drop" || drop.Action != ActionDrop || drop.Match.Sources[0] != "192.168.20.0/24" || drop.Attachments[0] != "input:host-$LY_ROUTE_LAN_INTERFACE" || drop.HitCountState != "unavailable" {
		t.Fatalf("drop target = %#v", drop)
	}
	rate := compiled.Targets[1]
	if rate.Kind != "vpp.behavior.rate" || rate.Action != ActionPolicer || rate.Policer == nil || rate.Attachments[0] != "output:host-$LY_ROUTE_LAN_INTERFACE" {
		t.Fatalf("rate target = %#v", rate)
	}
	if !hasVPPGroup(compiled.VPPGroups, "vpp.acl.drop") || !hasVPPGroup(compiled.VPPGroups, "vpp.behavior.rate") {
		t.Fatalf("behavior groups = %#v", compiled.VPPGroups)
	}
	if hasVPPGroup(compiled.VPPGroups, "vpp.policer") {
		t.Fatalf("matched rate rule must not also create an interface policer: %#v", compiled.VPPGroups)
	}
	for _, group := range compiled.VPPGroups {
		if len(group.Objects) == 0 {
			t.Fatalf("compiled intent contains empty VPP group %q", group.Kind)
		}
	}
}

func hasVPPGroup(groups []VPPObjectGroup, kind string) bool {
	for _, group := range groups {
		if group.Kind == kind {
			return true
		}
	}
	return false
}

func TestCompileIntentCarriesRemarkBehaviorMetadata(t *testing.T) {
	intent := NewIntent("remark-policy", []Rule{
		NewClassRule("remark-protected", "business_critical", RemarkForProtectedClass("AF31", "control")),
		NewClassRule("remark-downstream", "default", RemarkForDownstreamPolicy("AF21", "wan-low-latency")),
	})
	compiled, err := CompileIntent(intent)
	if err != nil {
		t.Fatalf("compile intent: %v", err)
	}

	wantTargets := []Target{
		{Kind: "vpp.qos.mark", RuleID: "remark-protected", Granularity: ClassGranularity, Action: ActionRemark, Class: "business_critical", DSCP: "AF31", RemarkBehavior: &RemarkBehavior{ProtectedClass: "control", DownstreamPolicy: "remark-protected"}},
		{Kind: "vpp.qos.mark", RuleID: "remark-downstream", Granularity: ClassGranularity, Action: ActionRemark, Class: "default", DSCP: "AF21", RemarkBehavior: &RemarkBehavior{ProtectedClass: "default", DownstreamPolicy: "wan-low-latency"}},
	}
	if !reflect.DeepEqual(compiled.Targets, wantTargets) {
		t.Fatalf("targets = %#v, want %#v", compiled.Targets, wantTargets)
	}

	wantGroups := []VPPObjectGroup{
		{Kind: "vpp.qos.egress-map", Objects: []VPPObject{
			{Name: "remark-policy/class/business_critical/qos.egress-map", RuleID: "remark-protected", Granularity: ClassGranularity, Action: ActionRemark, Class: "business_critical", DSCP: "AF31", RemarkBehavior: &RemarkBehavior{ProtectedClass: "control", DownstreamPolicy: "remark-protected"}},
			{Name: "remark-policy/class/default/qos.egress-map", RuleID: "remark-downstream", Granularity: ClassGranularity, Action: ActionRemark, Class: "default", DSCP: "AF21", RemarkBehavior: &RemarkBehavior{ProtectedClass: "default", DownstreamPolicy: "wan-low-latency"}},
		}},
		{Kind: "vpp.qos.mark", Objects: []VPPObject{
			{Name: "remark-policy/class/business_critical/qos.mark", RuleID: "remark-protected", Granularity: ClassGranularity, Action: ActionRemark, Class: "business_critical", DSCP: "AF31", RemarkBehavior: &RemarkBehavior{ProtectedClass: "control", DownstreamPolicy: "remark-protected"}},
			{Name: "remark-policy/class/default/qos.mark", RuleID: "remark-downstream", Granularity: ClassGranularity, Action: ActionRemark, Class: "default", DSCP: "AF21", RemarkBehavior: &RemarkBehavior{ProtectedClass: "default", DownstreamPolicy: "wan-low-latency"}},
		}},
	}
	if !reflect.DeepEqual(compiled.VPPGroups, wantGroups) {
		t.Fatalf("vpp groups = %#v, want %#v", compiled.VPPGroups, wantGroups)
	}
}

func TestCompileIntentSupportsRemarkProtectedClassAndDownstreamPolicyMetadata(t *testing.T) {
	intent := NewIntent("default", []Rule{
		NewClassRule("remark-control", "control", RemarkForProtectedClass("CS6", "control")),
		NewClassRule("remark-bulk", "bulk", RemarkForDownstreamPolicy("AF11", "downstream-bulk")),
		NewClassRule("remark-business", "business_critical", RemarkWithBehavior("AF31", RemarkBehavior{ProtectedClass: "business_critical", DownstreamPolicy: "downstream-business"})),
	})
	compiled, err := CompileIntent(intent)
	if err != nil {
		t.Fatalf("compile intent: %v", err)
	}

	wantTargets := []Target{
		{Kind: "vpp.qos.mark", RuleID: "remark-control", Granularity: ClassGranularity, Action: ActionRemark, Class: "control", DSCP: "CS6", RemarkBehavior: &RemarkBehavior{ProtectedClass: "control", DownstreamPolicy: "remark-control"}},
		{Kind: "vpp.qos.mark", RuleID: "remark-bulk", Granularity: ClassGranularity, Action: ActionRemark, Class: "bulk", DSCP: "AF11", RemarkBehavior: &RemarkBehavior{ProtectedClass: "bulk", DownstreamPolicy: "downstream-bulk"}},
		{Kind: "vpp.qos.mark", RuleID: "remark-business", Granularity: ClassGranularity, Action: ActionRemark, Class: "business_critical", DSCP: "AF31", RemarkBehavior: &RemarkBehavior{ProtectedClass: "business_critical", DownstreamPolicy: "downstream-business"}},
	}
	if !reflect.DeepEqual(compiled.Targets, wantTargets) {
		t.Fatalf("remark targets = %#v, want %#v", compiled.Targets, wantTargets)
	}
	if compiled.Targets[2].RemarkBehavior == intent.Rules[2].Actions[0].RemarkBehavior {
		t.Fatal("compiled remark behavior must not alias input action behavior")
	}

	for _, group := range compiled.VPPGroups {
		objects := group.Objects
		if len(objects) != 3 {
			t.Fatalf("%s object count = %d, want 3", group.Kind, len(objects))
		}
		for index, object := range objects {
			if !reflect.DeepEqual(object.RemarkBehavior, wantTargets[index].RemarkBehavior) {
				t.Fatalf("%s object %d remark behavior = %#v, want %#v", group.Kind, index, object.RemarkBehavior, wantTargets[index].RemarkBehavior)
			}
			if object.RemarkBehavior == intent.Rules[index].Actions[0].RemarkBehavior {
				t.Fatalf("%s object %d remark behavior aliases input action behavior", group.Kind, index)
			}
		}
	}
}

func TestCompileIntentPreservesRuleAndClassGranularityBoundaries(t *testing.T) {
	intent := NewIntent("mixed", []Rule{
		NewRule("rule-video-primary", RuleGranularity, Classify("video")),
		NewRule("rule-video-secondary", RuleGranularity, Classify("video")),
		NewClassRule("class-video-classify", "video", Classify("video")),
		NewClassRule("class-bulk-classify", "bulk", Classify("bulk")),
		NewClassRule("class-video-remark", "video", Remark("AF41")),
		NewClassRule("class-bulk-police", "bulk", Police(2_000_000, 200_000)),
	})
	compiled, err := CompileIntent(intent)
	if err != nil {
		t.Fatalf("compile intent: %v", err)
	}

	wantTargets := []Target{
		{Kind: "vpp.qos.classify", RuleID: "rule-video-primary", Granularity: RuleGranularity, Action: ActionClassify, Class: "video"},
		{Kind: "vpp.qos.classify", RuleID: "rule-video-secondary", Granularity: RuleGranularity, Action: ActionClassify, Class: "video"},
		{Kind: "vpp.qos.classify", RuleID: "class-video-classify", Granularity: ClassGranularity, Action: ActionClassify, Class: "video"},
		{Kind: "vpp.qos.classify", RuleID: "class-bulk-classify", Granularity: ClassGranularity, Action: ActionClassify, Class: "bulk"},
		{Kind: "vpp.qos.mark", RuleID: "class-video-remark", Granularity: ClassGranularity, Action: ActionRemark, Class: "video", DSCP: "AF41", RemarkBehavior: &RemarkBehavior{ProtectedClass: "video", DownstreamPolicy: "class-video-remark"}},
		{Kind: "vpp.policer", RuleID: "class-bulk-police", Granularity: ClassGranularity, Action: ActionPolicer, Class: "bulk", Policer: &Policer{RateBPS: 2_000_000, BurstBPS: 200_000}},
	}
	if !reflect.DeepEqual(compiled.Targets, wantTargets) {
		t.Fatalf("targets = %#v, want %#v", compiled.Targets, wantTargets)
	}

	wantNamesByKind := map[string][]string{
		"vpp.qos.classify":   {"mixed/rule/rule-video-primary/qos.classify", "mixed/rule/rule-video-secondary/qos.classify", "mixed/class/video/qos.classify", "mixed/class/bulk/qos.classify"},
		"vpp.qos.record":     {"mixed/rule/rule-video-primary/qos.record", "mixed/rule/rule-video-secondary/qos.record", "mixed/class/video/qos.record", "mixed/class/bulk/qos.record"},
		"vpp.qos.store":      {"mixed/rule/rule-video-primary/qos.store", "mixed/rule/rule-video-secondary/qos.store", "mixed/class/video/qos.store", "mixed/class/bulk/qos.store"},
		"vpp.qos.egress-map": {"mixed/class/video/qos.egress-map"},
		"vpp.qos.mark":       {"mixed/class/video/qos.mark"},
		"vpp.policer":        {"mixed/class/bulk/policer"},
	}
	for _, group := range compiled.VPPGroups {
		wantNames := wantNamesByKind[group.Kind]
		if len(group.Objects) != len(wantNames) {
			t.Fatalf("%s object count = %d, want %d", group.Kind, len(group.Objects), len(wantNames))
		}
		for index, object := range group.Objects {
			if object.Name != wantNames[index] {
				t.Fatalf("%s object %d name = %q, want %q", group.Kind, index, object.Name, wantNames[index])
			}
			if object.Granularity != RuleGranularity && object.Granularity != ClassGranularity {
				t.Fatalf("%s object %d granularity = %q, want rule or class", group.Kind, index, object.Granularity)
			}
		}
	}
}

func TestCompileIntentKeepsPolicerSelectiveByRuleAndClassScope(t *testing.T) {
	intent := NewIntent("selective", []Rule{
		NewRule("rule-video-classify", RuleGranularity, Classify("video")),
		NewRule("rule-voice-police", RuleGranularity, Classify("voice"), Police(8_000_000, 800_000)),
		NewClassRule("class-bulk-police", "bulk", Police(2_000_000, 200_000)),
		NewClassRule("class-video-remark", "video", RemarkForDownstreamPolicy("AF41", "wan-priority")),
	})
	compiled, err := CompileIntent(intent)
	if err != nil {
		t.Fatalf("compile intent: %v", err)
	}

	wantTargets := []Target{
		{Kind: "vpp.qos.classify", RuleID: "rule-video-classify", Granularity: RuleGranularity, Action: ActionClassify, Class: "video"},
		{Kind: "vpp.qos.classify", RuleID: "rule-voice-police", Granularity: RuleGranularity, Action: ActionClassify, Class: "voice"},
		{Kind: "vpp.policer", RuleID: "rule-voice-police", Granularity: RuleGranularity, Action: ActionPolicer, Policer: &Policer{RateBPS: 8_000_000, BurstBPS: 800_000}},
		{Kind: "vpp.policer", RuleID: "class-bulk-police", Granularity: ClassGranularity, Action: ActionPolicer, Class: "bulk", Policer: &Policer{RateBPS: 2_000_000, BurstBPS: 200_000}},
		{Kind: "vpp.qos.mark", RuleID: "class-video-remark", Granularity: ClassGranularity, Action: ActionRemark, Class: "video", DSCP: "AF41", RemarkBehavior: &RemarkBehavior{ProtectedClass: "video", DownstreamPolicy: "wan-priority"}},
	}
	if !reflect.DeepEqual(compiled.Targets, wantTargets) {
		t.Fatalf("targets = %#v, want %#v", compiled.Targets, wantTargets)
	}

	policerObjects := compiled.VPPGroups[5].Objects
	wantPolicerObjects := []VPPObject{
		{Name: "selective/rule/rule-voice-police/policer", RuleID: "rule-voice-police", Granularity: RuleGranularity, Action: ActionPolicer, Policer: &Policer{RateBPS: 8_000_000, BurstBPS: 800_000}},
		{Name: "selective/class/bulk/policer", RuleID: "class-bulk-police", Granularity: ClassGranularity, Action: ActionPolicer, Class: "bulk", Policer: &Policer{RateBPS: 2_000_000, BurstBPS: 200_000}},
	}
	if !reflect.DeepEqual(policerObjects, wantPolicerObjects) {
		t.Fatalf("policer objects = %#v, want %#v", policerObjects, wantPolicerObjects)
	}
	for _, target := range compiled.Targets {
		if target.RuleID == "rule-video-classify" && target.Kind == "vpp.policer" {
			t.Fatalf("unrequested rule %q received policer target: %#v", target.RuleID, target)
		}
		if target.RuleID == "class-video-remark" && target.Kind == "vpp.policer" {
			t.Fatalf("unrequested class %q received policer target: %#v", target.RuleID, target)
		}
	}
}

func TestCompiledIntentVPPObjectsStayInScope(t *testing.T) {
	compiled, err := CompileIntent(NewIntent("default", []Rule{
		NewRule("classify-video", RuleGranularity, Classify("video")),
		NewClassRule("remark-video", "video", Remark("AF41")),
		NewClassRule("police-bulk", "bulk", Police(10_000_000, 1_000_000)),
	}))
	if err != nil {
		t.Fatalf("compile intent: %v", err)
	}
	payload, err := json.Marshal(compiled)
	if err != nil {
		t.Fatal(err)
	}

	encoded := string(payload)
	for _, required := range []string{"vpp.qos.classify", "vpp.qos.record", "vpp.qos.store", "vpp.qos.egress-map", "vpp.qos.mark", "vpp.policer", "rule", "class", "protected_class", "downstream_policy"} {
		if !strings.Contains(encoded, required) {
			t.Fatalf("compiled flow payload missing %q: %s", required, encoded)
		}
	}
	for _, forbidden := range forbiddenFlowControlTerms() {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("compiled flow payload leaked forbidden field %q: %s", forbidden, encoded)
		}
	}
}

func TestCompiledIntentDoesNotExposeOtherProductionLimiters(t *testing.T) {
	compiled, err := CompileIntent(NewIntent("default", []Rule{
		NewRule("rule-police-voice", RuleGranularity, Classify("voice"), Police(8_000_000, 800_000)),
		NewClassRule("class-police-bulk", "bulk", Police(2_000_000, 200_000)),
	}))
	if err != nil {
		t.Fatalf("compile intent: %v", err)
	}
	payload, err := json.Marshal(compiled)
	if err != nil {
		t.Fatal(err)
	}

	encoded := string(payload)
	if strings.Count(encoded, "vpp.policer") == 0 {
		t.Fatalf("compiled flow payload missing policer limiter: %s", encoded)
	}
	for _, forbidden := range append(forbiddenFlowControlTerms(), "limiter", "queueing") {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("compiled flow payload leaked unsupported limiter %q: %s", forbidden, encoded)
		}
	}
}

func TestCompileIntentRejectsInvalidAndHiddenActions(t *testing.T) {
	for name, intent := range map[string]Intent{
		"missing intent id": NewIntent("", []Rule{NewRule("classify-video", RuleGranularity, Classify("video"))}),
		"no rules":          NewIntent("default", nil),
		"missing rule id":   NewIntent("default", []Rule{NewRule("", RuleGranularity, Classify("video"))}),
		"rule with class": NewIntent("default", []Rule{
			{ID: "classify-video", Granularity: RuleGranularity, Class: "video", Actions: []Action{Classify("video")}},
		}),
		"class without class": NewIntent("default", []Rule{
			NewRule("remark-video", ClassGranularity, Remark("AF41")),
		}),
		"bridge granularity": NewIntent("default", []Rule{
			NewRule("classify-video", "bridge_mode", Classify("video")),
		}),
		"no actions": NewIntent("default", []Rule{NewRule("classify-video", RuleGranularity)}),
		"empty classify": NewIntent("default", []Rule{
			NewRule("classify-video", RuleGranularity, Classify("")),
		}),
		"mixed classify fields": NewIntent("default", []Rule{
			NewRule("classify-video", RuleGranularity, Action{Kind: ActionClassify, TrafficClass: "video", DSCP: "AF41"}),
		}),
		"classify with remark behavior": NewIntent("default", []Rule{
			NewRule("classify-video", RuleGranularity, Action{Kind: ActionClassify, TrafficClass: "video", RemarkBehavior: &RemarkBehavior{ProtectedClass: "video"}}),
		}),
		"empty remark": NewIntent("default", []Rule{
			NewClassRule("remark-video", "video", Remark("")),
		}),
		"empty remark behavior": NewIntent("default", []Rule{
			NewClassRule("remark-video", "video", RemarkWithBehavior("AF41", RemarkBehavior{})),
		}),
		"spaced protected class": NewIntent("default", []Rule{
			NewClassRule("remark-video", "video", RemarkForProtectedClass("AF41", " video")),
		}),
		"spaced downstream policy": NewIntent("default", []Rule{
			NewClassRule("remark-video", "video", RemarkForDownstreamPolicy("AF41", "wan-priority ")),
		}),
		"zero policer": NewIntent("default", []Rule{
			NewClassRule("police-bulk", "bulk", Police(0, 1_000_000)),
		}),
		"policer with remark behavior": NewIntent("default", []Rule{
			NewClassRule("police-bulk", "bulk", Action{Kind: ActionPolicer, RemarkBehavior: &RemarkBehavior{ProtectedClass: "bulk"}, Policer: &Policer{RateBPS: 10_000_000, BurstBPS: 1_000_000}}),
		}),
		"queue action": NewIntent("default", []Rule{
			NewClassRule("queue-bulk", "bulk", Action{Kind: "queue"}),
		}),
		"sqm action": NewIntent("default", []Rule{
			NewClassRule("sqm-bulk", "bulk", Action{Kind: "SQM"}),
		}),
		"cake action": NewIntent("default", []Rule{
			NewClassRule("cake-bulk", "bulk", Action{Kind: "CAKE"}),
		}),
		"fq codel action": NewIntent("default", []Rule{
			NewClassRule("fq-codel-bulk", "bulk", Action{Kind: "FQ-CoDel"}),
		}),
		"connection limit action": NewIntent("default", []Rule{
			NewClassRule("limit-abusive", "abusive", Action{Kind: "connection_limit"}),
		}),
		"connection-limit action": NewIntent("default", []Rule{
			NewClassRule("limit-abusive", "abusive", Action{Kind: "connection-limit"}),
		}),
		"max connections action": NewIntent("default", []Rule{
			NewClassRule("limit-abusive", "abusive", Action{Kind: "max_connections"}),
		}),
		"max-connections action": NewIntent("default", []Rule{
			NewClassRule("limit-abusive", "abusive", Action{Kind: "max-connections"}),
		}),
		"record action": NewIntent("default", []Rule{
			NewClassRule("record-video", "video", Action{Kind: "record"}),
		}),
		"store action": NewIntent("default", []Rule{
			NewClassRule("store-video", "video", Action{Kind: "store"}),
		}),
		"egress map action": NewIntent("default", []Rule{
			NewClassRule("egress-map-video", "video", Action{Kind: "egress-map"}),
		}),
		"mark action": NewIntent("default", []Rule{
			NewClassRule("mark-video", "video", Action{Kind: "mark"}),
		}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CompileIntent(intent); !errors.Is(err, ErrInvalidIntent) {
				t.Fatalf("CompileIntent error = %v, want ErrInvalidIntent", err)
			}
		})
	}
}

func forbiddenFlowControlTerms() []string {
	return []string{"connection_limit", "connection-limit", "max_connections", "max-connections", "bridge", "bridge_mode", "queue", "sqm", "cake", "fq_codel", "fq-codel"}
}
