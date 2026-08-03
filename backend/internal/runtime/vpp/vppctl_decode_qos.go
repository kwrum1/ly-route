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
		if len(candidate.Objects) == 0 {
			return QoSReadback{}, snapshotDecodeError("QoS candidate %q has no objects", candidate.Kind)
		}
		if isSharedQoSState(candidate.Kind) && len(candidate.Objects) != 1 {
			return QoSReadback{}, snapshotDecodeError("QoS candidate %q has ambiguous shared state", candidate.Kind)
		}
		for _, object := range candidate.Objects {
			if err := verifyQoSObject(results, candidate.Kind, object); err != nil {
				return QoSReadback{}, err
			}
		}
		groups = append(groups, candidate)
	}
	for _, kind := range request.AbsentQoS {
		candidate, found := candidates[strings.TrimSpace(kind)]
		if !found {
			return QoSReadback{}, snapshotDecodeError("absent QoS group %q has no prior candidate", kind)
		}
		if flowGroupUsesACL(candidate) {
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

func verifyQoSObject(results []VPPCTLCommandResult, kind string, object flow.VPPObject) error {
	target := flow.Target{Kind: kind, RuleID: object.RuleID, Granularity: object.Granularity, Action: object.Action, Class: object.Class, DSCP: object.DSCP, RemarkBehavior: object.RemarkBehavior, Policer: object.Policer, Match: object.Match, Attachments: object.Attachments}
	switch kind {
	case "vpp.acl.drop":
		return verifyDynamicFlowACL(results, object, "deny")
	case "vpp.behavior.rate":
		if err := verifyDynamicFlowACL(results, object, "permit"); err != nil {
			return err
		}
		return verifyPolicerResult(results, target)
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

func policerValues(target flow.Target) (uint64, uint64) {
	rate := uint64(1_000_000)
	burst := uint64(100_000)
	if target.Policer != nil {
		if target.Policer.RateBPS > 0 {
			rate = target.Policer.RateBPS / 1000
			if rate == 0 {
				rate = 1
			}
		}
		if target.Policer.BurstBPS > 0 {
			burst = target.Policer.BurstBPS / 1000
			if burst == 0 {
				burst = 1
			}
		}
	}
	return rate, burst
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
