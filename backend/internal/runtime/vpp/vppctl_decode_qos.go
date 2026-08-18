package vpp

import (
	"fmt"
	"strconv"
	"strings"

	"ly-route/backend/internal/runtime/flow"
)

type qosInterfaceProof struct {
	command string
	verb    string
	value   int
}

func decodeVPPCTLQoS(request SnapshotRequest, results []VPPCTLCommandResult) (QoSReadback, error) {
	candidates := make(map[string]flow.VPPObjectGroup, len(request.Candidates.QoS))
	for _, candidate := range request.Candidates.QoS {
		kind := strings.TrimSpace(candidate.Kind)
		if kind == "" {
			return QoSReadback{}, snapshotDecodeError("QoS candidate kind is empty")
		}
		if _, duplicate := candidates[kind]; duplicate {
			return QoSReadback{}, snapshotDecodeError("QoS candidate %q is ambiguous", kind)
		}
		candidates[kind] = candidate
	}
	if err := requireQoSCandidateNames(request.QoS, candidates); err != nil {
		return QoSReadback{}, err
	}
	groups := make([]flow.VPPObjectGroup, 0, len(request.QoS))
	for _, kind := range request.QoS {
		candidate := candidates[strings.TrimSpace(kind)]
		proofCandidate := rewriteFlowObjectGroupLANInterface(candidate, request.LANVPPInterface)
		if len(candidate.Objects) == 0 {
			return QoSReadback{}, snapshotDecodeError("QoS candidate %q has no objects", candidate.Kind)
		}
		if isSharedQoSState(candidate.Kind) && len(candidate.Objects) != 1 {
			return QoSReadback{}, snapshotDecodeError("QoS candidate %q has ambiguous shared state", candidate.Kind)
		}
		missing := false
		for _, object := range proofCandidate.Objects {
			// Legacy rate objects were stored without a concrete feature
			// attachment. During repair they are intentionally treated as drift.
			if proofCandidate.Kind == "vpp.behavior.rate" && len(object.Attachments) == 0 &&
				(request.AllowMissing || len(resultStdoutLast(results, "show ly-route flow-rate")) == 0 &&
					strings.Contains(resultStdoutLast(results, "show acl-plugin acl"), "tag {ly-route-"+safeTag(object.RuleID)+"}") &&
					!strings.Contains(resultStdoutLast(results, "show acl-plugin acl"), "ipv6 permit")) {
				missing = true
				break
			}
			if request.AllowMissing {
				objectMissing, err := qosObjectMissing(results, proofCandidate.Kind, object)
				if err != nil {
					return QoSReadback{}, err
				}
				if objectMissing {
					missing = true
					break
				}
			}
			if err := verifyQoSObject(results, proofCandidate.Kind, object); err != nil {
				// A verified repair snapshot can observe an object created by an
				// older command compiler. Treat an exact-object parameter mismatch
				// as drift so reconciliation removes and recreates it. Normal and
				// post-apply readbacks remain strict.
				if request.AllowMissing && (qosObjectParametersMismatch(err) ||
					(proofCandidate.Kind == "vpp.behavior.rate" &&
						strings.Contains(err.Error(), "flow rate rule") &&
						strings.Contains(err.Error(), "missing"))) {
					missing = true
					break
				}
				return QoSReadback{}, err
			}
		}
		if missing {
			continue
		}
		groups = append(groups, candidate)
	}
	for _, kind := range request.AbsentQoS {
		candidate, found := candidates[strings.TrimSpace(kind)]
		if !found {
			return QoSReadback{}, snapshotDecodeError("absent QoS group %q has no prior candidate", kind)
		}
		if candidate.Kind == "vpp.behavior.rate" {
			for _, object := range candidate.Objects {
				if flowRateRulePresent(resultStdoutLast(results, "show ly-route flow-rate"), object.RuleID) {
					return QoSReadback{}, snapshotDecodeError("deleted flow rate rule %q remains", object.RuleID)
				}
				command := "show policer name ly_route_" + safeTag(object.RuleID)
				if strings.TrimSpace(resultStdoutLast(results, command)) != "" {
					return QoSReadback{}, snapshotDecodeError("deleted QoS policer %q remains", object.RuleID)
				}
			}
		} else if flowGroupUsesACL(candidate) {
			for _, object := range candidate.Objects {
				if _, _, present, err := taggedACLOutput(results, "ly-route-"+safeTag(object.RuleID)); err != nil {
					return QoSReadback{}, err
				} else if present {
					return QoSReadback{}, snapshotDecodeError("deleted QoS ACL %q remains", object.RuleID)
				}
				if candidate.Kind == "vpp.behavior.rate" {
					command := "show policer name ly_route_" + safeTag(object.RuleID)
					if strings.TrimSpace(resultStdoutLast(results, command)) != "" {
						return QoSReadback{}, snapshotDecodeError("deleted QoS policer %q remains", object.RuleID)
					}
				}
			}
		} else {
			commands := qosSnapshotCommands(SnapshotRequest{Candidates: SnapshotCandidates{QoS: []flow.VPPObjectGroup{candidate}}})
			if err := verifyVPPCTLAbsence(results, commands, "QoS group", kind); err != nil {
				return QoSReadback{}, err
			}
		}
	}
	return QoSReadback{Groups: groups}, nil
}

func qosObjectParametersMismatch(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "policer") && strings.Contains(message, "values do not match candidate")
}

func qosObjectMissing(results []VPPCTLCommandResult, kind string, object flow.VPPObject) (bool, error) {
	if flowGroupUsesACL(flow.VPPObjectGroup{Kind: kind}) {
		_, _, present, err := taggedACLOutput(results, "ly-route-"+safeTag(object.RuleID))
		if err != nil || !present {
			return !present, err
		}
	}
	if kind == "vpp.behavior.rate" {
		return !flowRateRulePresent(resultStdoutLast(results, "show ly-route flow-rate"), object.RuleID), nil
	}
	if kind == "vpp.policer" {
		command := "show policer name ly_route_" + safeTag(object.RuleID)
		return strings.TrimSpace(resultStdoutLast(results, command)) == "", nil
	}
	return false, nil
}

func verifyQoSObject(results []VPPCTLCommandResult, kind string, object flow.VPPObject) error {
	target := flow.Target{Kind: kind, RuleID: object.RuleID, Granularity: object.Granularity, Action: object.Action, Class: object.Class, DSCP: object.DSCP, RemarkBehavior: object.RemarkBehavior, Policer: object.Policer, Match: object.Match, Attachments: object.Attachments}
	switch kind {
	case "vpp.acl.drop":
		return verifyDynamicFlowACL(results, object, "deny")
	case "vpp.behavior.rate":
		if err := verifyFlowRateResult(results, target); err != nil {
			return err
		}
		return nil
	case "vpp.qos.classify":
		if err := verifyQOSInterfaceResult(results, qosInterfaceProof{command: "show qos record lyroute-$LY_ROUTE_LAN_INTERFACE", verb: "recorded", value: -1}); err != nil {
			return err
		}
		return verifyQOSInterfaceResult(results, qosInterfaceProof{command: "show qos store lyroute-$LY_ROUTE_LAN_INTERFACE", verb: "stored", value: qosClassValue(target)})
	case "vpp.qos.record":
		return verifyQOSInterfaceResult(results, qosInterfaceProof{command: "show qos record lyroute-$LY_ROUTE_LAN_INTERFACE", verb: "recorded", value: -1})
	case "vpp.qos.store":
		return verifyQOSInterfaceResult(results, qosInterfaceProof{command: "show qos store lyroute-$LY_ROUTE_LAN_INTERFACE", verb: "stored", value: qosClassValue(target)})
	case "vpp.qos.egress-map":
		return verifyQOSMapResult(results, target)
	case "vpp.qos.mark":
		if err := verifyQOSMapResult(results, target); err != nil {
			return err
		}
		mapID := stableID("qos-map:"+object.RuleID, 1, 999)
		return verifyQOSInterfaceResult(results, qosInterfaceProof{command: "show qos mark lyroute-$LY_ROUTE_LAN_INTERFACE", verb: "marked", value: mapID})
	case "vpp.policer":
		return verifyPolicerResult(results, target)
	default:
		return snapshotDecodeError("unsupported QoS kind %q", kind)
	}
}

func verifyQOSMapResult(results []VPPCTLCommandResult, target flow.Target) error {
	mapID := stableID("qos-map:"+target.RuleID, 1, 999)
	command := fmt.Sprintf("show qos egress map id %d", mapID)
	output, err := commandOutput(results, command)
	if err != nil {
		return err
	}
	lines := nonBlankLines(output)
	if len(lines) != 5 || lines[0] != fmt.Sprintf("Map-ID:%d", mapID) {
		return snapshotDecodeError("QoS egress map %d does not match candidate", mapID)
	}
	rows := make(map[string][]int, 4)
	for _, line := range lines[1:] {
		separator := strings.Index(line, ":[")
		if separator < 1 || !strings.HasSuffix(line, "]") {
			return snapshotDecodeError("unknown QoS egress map grammar %q", line)
		}
		source := line[:separator]
		if _, duplicate := rows[source]; duplicate {
			return snapshotDecodeError("QoS egress map source %q is ambiguous", source)
		}
		values := strings.Split(line[separator+2:len(line)-1], ",")
		if len(values) != 256 {
			return snapshotDecodeError("QoS egress map source %q is truncated", source)
		}
		parsed := make([]int, len(values))
		for index, value := range values {
			parsed[index], err = strconv.Atoi(value)
			if err != nil || parsed[index] < 0 || parsed[index] > 255 {
				return snapshotDecodeError("malformed QoS egress value %q", value)
			}
		}
		rows[source] = parsed
	}
	for _, source := range []string{"ext", "VLAN", "MPLS", "IP"} {
		if len(rows[source]) != 256 {
			return snapshotDecodeError("QoS egress map source %q is missing", source)
		}
	}
	if rows["IP"][qosClassValue(target)] != dscpValue(target.DSCP) {
		return snapshotDecodeError("QoS egress map %d does not match candidate", mapID)
	}
	return nil
}

func verifyQOSInterfaceResult(results []VPPCTLCommandResult, proof qosInterfaceProof) error {
	output, err := commandOutput(results, proof.command)
	if err != nil {
		return err
	}
	fields := strings.Fields(strings.TrimSpace(output))
	switch proof.verb {
	case "recorded":
		lines := nonBlankLines(output)
		if len(lines) != 2 || !strings.HasSuffix(lines[0], ":") || lines[1] != "IP" {
			return snapshotDecodeError("unknown QoS record grammar %q", strings.TrimSpace(output))
		}
	case "stored":
		lines := nonBlankLines(output)
		if len(lines) != 2 || !strings.HasSuffix(lines[0], ":") {
			return snapshotDecodeError("unknown QoS store grammar %q", strings.TrimSpace(output))
		}
		fields = strings.Fields(lines[1])
		if len(fields) != 3 || fields[0] != "IP" || fields[1] != "->" {
			return snapshotDecodeError("unknown QoS store grammar %q", strings.TrimSpace(output))
		}
		value, parseErr := strconv.Atoi(fields[2])
		if parseErr != nil || value != proof.value {
			return snapshotDecodeError("QoS store value does not match candidate")
		}
	case "marked":
		lines := nonBlankLines(output)
		if len(lines) != 2 || !strings.HasSuffix(lines[0], ":") {
			return snapshotDecodeError("unknown QoS mark grammar %q", strings.TrimSpace(output))
		}
		fields = strings.Fields(lines[1])
		if len(fields) != 2 || fields[0] != "IP:" || !strings.HasPrefix(fields[1], "map:") {
			return snapshotDecodeError("unknown QoS mark grammar %q", strings.TrimSpace(output))
		}
		if _, parseErr := strconv.Atoi(strings.TrimPrefix(fields[1], "map:")); parseErr != nil {
			return snapshotDecodeError("malformed QoS mark map %q", fields[1])
		}
	default:
		return snapshotDecodeError("unsupported QoS interface grammar %q", proof.verb)
	}
	return nil
}

func verifyPolicerResult(results []VPPCTLCommandResult, target flow.Target) error {
	name := "ly_route_" + safeTag(target.RuleID)
	output, err := commandOutput(results, "show policer name "+name)
	if err != nil {
		return err
	}
	lines := nonBlankLines(output)
	if len(lines) < 2 {
		return snapshotDecodeError("policer %q output is truncated", name)
	}
	fields := strings.Fields(lines[0])
	if len(fields) != 12 || fields[0] != "Name" || strings.Trim(fields[1], "\"") != name || fields[2] != "type" || fields[4] != "cir" || fields[6] != "eir" || fields[8] != "cb" || fields[10] != "eb" {
		return snapshotDecodeError("unknown policer grammar %q", strings.TrimSpace(output))
	}
	rate, rateErr := strconv.ParseUint(fields[5], 10, 64)
	burst, burstErr := strconv.ParseUint(fields[9], 10, 64)
	wantRate, wantBurst := policerValues(target)
	if rateErr != nil || burstErr != nil || rate != wantRate || burst != wantBurst {
		return snapshotDecodeError("policer %q values do not match candidate", name)
	}
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "rate type ") || strings.HasPrefix(line, "conform action ") || strings.HasPrefix(line, "Policer at index ") || strings.HasPrefix(line, "cir ") || strings.HasPrefix(line, "cur lim ") || strings.HasPrefix(line, "last update ") || strings.HasPrefix(line, "conform ") || strings.HasPrefix(line, "exceed ") || strings.HasPrefix(line, "violate ") || line == "-----------" {
			continue
		}
		return snapshotDecodeError("unknown policer instance grammar %q", line)
	}
	return nil
}

func verifyFlowRateResult(results []VPPCTLCommandResult, target flow.Target) error {
	// A replace lifecycle reads the inventory once after deleting the old rule
	// and again after creating the new one. The final inventory is authoritative.
	output, err := commandOutputLastAllowEmpty(results, "show ly-route flow-rate")
	if err != nil {
		return err
	}
	if strings.TrimSpace(output) == "" {
		return snapshotDecodeError("flow rate rule %q is missing", target.RuleID)
	}
	want := flowRateRuleCommands(target)
	if len(want) == 0 {
		return snapshotDecodeError("flow rate rule %q has no match clauses", target.RuleID)
	}
	lines := nonBlankLines(output)
	matched := make(map[string]struct{}, len(lines))
	var hits uint64
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 28 || fields[0] != "rule" || fields[2] != "interface" || fields[4] != "direction" || fields[6] != "source" || fields[8] != "destination" || fields[10] != "protocol" || fields[12] != "source-port" || fields[14] != "destination-port" || fields[16] != "rate-kbps" || fields[18] != "burst-bytes" || fields[20] != "matched-packets" || fields[22] != "matched-bytes" || fields[24] != "conform-packets" || fields[26] != "dropped-packets" {
			return snapshotDecodeError("unknown flow-rate grammar %q", line)
		}
		if !strings.HasPrefix(fields[1], safeTag(target.RuleID)+"_") {
			continue
		}
		for _, counterIndex := range []int{21, 23, 25, 27} {
			if _, parseErr := strconv.ParseUint(fields[counterIndex], 10, 64); parseErr != nil {
				return snapshotDecodeError("flow rate rule %q has malformed counters", target.RuleID)
			}
		}
		value, _ := strconv.ParseUint(fields[21], 10, 64)
		hits += value
		key := strings.Join([]string{fields[3], fields[5], fields[7], fields[9], fields[11], fields[13], fields[15], fields[17], fields[19]}, "|")
		if _, duplicate := matched[key]; duplicate {
			return snapshotDecodeError("flow rate rule %q has duplicate runtime clauses", target.RuleID)
		}
		matched[key] = struct{}{}
	}
	for _, command := range want {
		fields := strings.Fields(command)
		if len(fields) != 23 || fields[0] != "set" || fields[1] != "ly-route" || fields[2] != "flow-rate" || fields[3] != "rule" || fields[5] != "interface" || fields[7] != "direction" || fields[9] != "source" || fields[11] != "destination" || fields[13] != "protocol" || fields[15] != "source-port" || fields[17] != "destination-port" || fields[19] != "rate-kbps" || fields[21] != "burst-bytes" {
			return snapshotDecodeError("flow rate rule %q has malformed desired clause", target.RuleID)
		}
		key := strings.Join([]string{fields[6], fields[8], fields[10], fields[12], flowRateProtocolNumber(fields[14]), fields[16], fields[18], fields[20], fields[22]}, "|")
		if _, ok := matched[key]; !ok {
			return snapshotDecodeError("flow rate rule %q runtime clause is missing", target.RuleID)
		}
	}
	if len(matched) != len(want) {
		return snapshotDecodeError("flow rate rule %q runtime clause count does not match candidate", target.RuleID)
	}
	_ = hits
	return nil
}

func flowRateRulePresent(output, ruleID string) bool {
	prefix := "rule " + safeTag(ruleID) + "_"
	for _, line := range nonBlankLines(output) {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func flowRateProtocolNumber(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "tcp", "6":
		return "6"
	case "udp", "17":
		return "17"
	case "icmp", "1":
		return "1"
	default:
		return "0"
	}
}

func policerValues(target flow.Target) (uint64, uint64) {
	rate := uint64(1_000_000)
	burst := uint64(100_000)
	if target.Policer != nil {
		if target.Policer.RateBPS > 0 {
			rate = divideRoundUp(target.Policer.RateBPS, 1000)
		}
		if target.Policer.BurstBPS > 0 {
			// The API carries the committed burst in bits, while VPP's cb is bytes.
			burst = divideRoundUp(target.Policer.BurstBPS, 8)
		}
		if target.Policer.RateBPS > 0 {
			// Keep at least 100 ms of tokens. Smaller buckets cause normal TCP
			// bursts to collapse far below the configured bandwidth.
			minimumBurst := divideRoundUp(target.Policer.RateBPS, 80)
			if burst < minimumBurst {
				burst = minimumBurst
			}
		}
	}
	return rate, burst
}

func divideRoundUp(value, divisor uint64) uint64 {
	if value == 0 {
		return 0
	}
	return 1 + (value-1)/divisor
}

func requireQoSCandidateNames(names []string, candidates map[string]flow.VPPObjectGroup) error {
	for _, name := range names {
		if _, ok := candidates[strings.TrimSpace(name)]; !ok {
			return snapshotDecodeError("QoS group %q has no candidate", name)
		}
	}
	return nil
}

func isSharedQoSState(kind string) bool {
	return kind == "vpp.qos.classify" || kind == "vpp.qos.record" || kind == "vpp.qos.store"
}
