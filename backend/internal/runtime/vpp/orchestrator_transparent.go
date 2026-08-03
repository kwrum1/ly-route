package vpp

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"ly-route/backend/internal/orchestrator"
)

const maxTransparentOrchestratorRuleVariants = 4096

type TransparentOrchestratorConfig struct {
	Generation string
	Topology   orchestrator.TopologyView
	Policy     *orchestrator.PolicyView
}

type TransparentOrchestratorReadback struct {
	Generation string `json:"generation"`
	State      string `json:"state"`
	Output     string `json:"output"`
}

func BuildTransparentOrchestratorDisableCommands() []string {
	return []string{"set ly-route orchestrator disable", "show ly-route orchestrator"}
}

func BuildTransparentOrchestratorCommands(config TransparentOrchestratorConfig) ([]string, error) {
	generation := strings.TrimSpace(config.Generation)
	if generation == "" || strings.ContainsAny(generation, " \t\r\n") {
		return nil, errors.New("transparent orchestrator generation is invalid")
	}
	wan, lan, err := transparentBoundaryInterfaces(config.Topology)
	if err != nil {
		return nil, err
	}
	groups := append([]orchestrator.GroupView(nil), config.Topology.Groups...)
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	commands := []string{
		"set ly-route orchestrator candidate clear",
		fmt.Sprintf("set ly-route orchestrator candidate boundary wan %s lan %s", wan, lan),
	}
	knownGroups := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		wanFacing, lanFacing, groupErr := transparentGroupInterfaces(group)
		if groupErr != nil {
			return nil, groupErr
		}
		knownGroups[group.Name] = struct{}{}
		commands = append(commands, fmt.Sprintf("set ly-route orchestrator candidate group %s wan-facing %s lan-facing %s", group.Name, wanFacing, lanFacing))
	}
	defaultAction := orchestrator.ActionDirect
	if config.Policy != nil {
		policyCommands, action, buildErr := transparentPolicyCommands(*config.Policy, knownGroups)
		if buildErr != nil {
			return nil, buildErr
		}
		commands = append(commands, policyCommands...)
		defaultAction = action
	}
	commands = append(commands,
		fmt.Sprintf("set ly-route orchestrator candidate default %s", defaultAction),
		fmt.Sprintf("set ly-route orchestrator commit generation %s", generation),
		"show ly-route orchestrator",
	)
	return commands, nil
}

func transparentBoundaryInterfaces(topology orchestrator.TopologyView) (string, string, error) {
	var wan, lan string
	for _, item := range topology.Interfaces {
		name := item.Port
		if item.Bond != nil {
			name = item.Bond.Name
		}
		name = transparentVPPInterface(name)
		switch item.Role {
		case orchestrator.RoleWAN:
			if wan != "" {
				return "", "", orchestrator.ErrDuplicateWAN
			}
			wan = name
		case orchestrator.RoleLAN:
			if lan != "" {
				return "", "", orchestrator.ErrDuplicateLAN
			}
			lan = name
		}
	}
	if wan == "" {
		return "", "", orchestrator.ErrMissingWAN
	}
	if lan == "" {
		return "", "", orchestrator.ErrMissingLAN
	}
	return wan, lan, nil
}

func transparentGroupInterfaces(group orchestrator.GroupView) (string, string, error) {
	var wan, lan string
	for _, port := range group.Ports {
		if port.Bond != nil {
			return "", "", orchestrator.ErrGroupBond
		}
		switch port.Direction {
		case orchestrator.DirectionWANFacing:
			wan = transparentVPPInterface(port.Interface)
		case orchestrator.DirectionLANFacing:
			lan = transparentVPPInterface(port.Interface)
		}
	}
	if wan == "" || lan == "" {
		return "", "", fmt.Errorf("%w: %q", orchestrator.ErrGroupDirection, group.Name)
	}
	return wan, lan, nil
}

func transparentVPPInterface(name string) string {
	name = strings.TrimSpace(name)
	if strings.HasPrefix(name, "lyroute-") {
		return name
	}
	return "lyroute-" + name
}

type transparentPrefix struct {
	prefix netip.Prefix
}

func transparentPolicyCommands(policy orchestrator.PolicyView, knownGroups map[string]struct{}) ([]string, orchestrator.ActionKind, error) {
	objects := make(map[string][]netip.Prefix, len(policy.IPObjects))
	for _, object := range policy.IPObjects {
		for _, raw := range object.Prefixes {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				return nil, "", fmt.Errorf("policy object %q contains invalid normalized prefix %q", object.ID, raw)
			}
			objects[object.ID] = append(objects[object.ID], prefix.Masked())
		}
	}
	groups := append([]orchestrator.PolicyGroupView(nil), policy.Groups...)
	sort.Slice(groups, func(i, j int) bool { return groups[i].Position < groups[j].Position })
	commands := []string{}
	variants := 0
	for _, group := range groups {
		rules := append([]orchestrator.PolicyRuleView(nil), group.Rules...)
		sort.Slice(rules, func(i, j int) bool { return rules[i].Sequence < rules[j].Sequence })
		for _, rule := range rules {
			sources, err := transparentSelectorPrefixes(rule.Match.Sources, objects)
			if err != nil {
				return nil, "", fmt.Errorf("rule %q source selector: %w", rule.ID, err)
			}
			destinations, err := transparentSelectorPrefixes(rule.Match.Destinations, objects)
			if err != nil {
				return nil, "", fmt.Errorf("rule %q destination selector: %w", rule.ID, err)
			}
			sourcePorts := transparentPortRanges(rule.Match.SourcePorts)
			destinationPorts := transparentPortRanges(rule.Match.DestinationPorts)
			protocol, family, err := transparentProtocol(rule.Match.Protocol)
			if err != nil {
				return nil, "", err
			}
			target := "none"
			if rule.Action.Kind == orchestrator.ActionVia {
				if _, ok := knownGroups[rule.Action.Group]; !ok {
					return nil, "", fmt.Errorf("rule %q references unknown orchestration group %q", rule.ID, rule.Action.Group)
				}
				target = rule.Action.Group
			}
			for _, source := range sources {
				for _, destination := range destinations {
					if source.prefix.Addr().BitLen() != destination.prefix.Addr().BitLen() || (family != 0 && source.prefix.Addr().BitLen() != family) {
						continue
					}
					for _, sourcePort := range sourcePorts {
						for _, destinationPort := range destinationPorts {
							variants++
							if variants > maxTransparentOrchestratorRuleVariants {
								return nil, "", fmt.Errorf("transparent orchestrator policy expands beyond %d VPP rule variants", maxTransparentOrchestratorRuleVariants)
							}
							addressFamily := "ip4"
							if source.prefix.Addr().Is6() {
								addressFamily = "ip6"
							}
							commands = append(commands, fmt.Sprintf(
								"set ly-route orchestrator candidate rule id %s group-position %d sequence %d action %s target %s family %s src %s dst %s proto %d sport %d-%d dport %d-%d",
								rule.ID, group.Position, rule.Sequence, rule.Action.Kind, target, addressFamily, source.prefix, destination.prefix, protocol,
								sourcePort.Start, sourcePort.End, destinationPort.Start, destinationPort.End,
							))
						}
					}
				}
			}
		}
	}
	if policy.Default.Kind != orchestrator.ActionDirect && policy.Default.Kind != orchestrator.ActionDrop {
		return nil, "", fmt.Errorf("transparent orchestrator default action %q is not terminal", policy.Default.Kind)
	}
	return commands, policy.Default.Kind, nil
}

func transparentSelectorPrefixes(selectors []string, objects map[string][]netip.Prefix) ([]transparentPrefix, error) {
	result := []transparentPrefix{}
	for _, selector := range selectors {
		if selector == "any" {
			result = append(result,
				transparentPrefix{prefix: netip.MustParsePrefix("0.0.0.0/0")},
				transparentPrefix{prefix: netip.MustParsePrefix("::/0")},
			)
			continue
		}
		prefixes, ok := objects[selector]
		if !ok {
			return nil, fmt.Errorf("unknown IP object %q", selector)
		}
		for _, prefix := range prefixes {
			result = append(result, transparentPrefix{prefix: prefix})
		}
	}
	if len(result) == 0 {
		return nil, errors.New("selector is empty")
	}
	return result, nil
}

func transparentPortRanges(ranges []orchestrator.PortRangeInput) []orchestrator.PortRangeInput {
	if len(ranges) == 0 {
		return []orchestrator.PortRangeInput{{Start: 0, End: 65535}}
	}
	return ranges
}

func transparentProtocol(protocol orchestrator.Protocol) (number uint8, family int, err error) {
	switch protocol {
	case orchestrator.ProtocolAny:
		return 0, 0, nil
	case orchestrator.ProtocolTCP:
		return 6, 0, nil
	case orchestrator.ProtocolUDP:
		return 17, 0, nil
	case orchestrator.ProtocolICMP:
		return 1, 32, nil
	case orchestrator.ProtocolICMPv6:
		return 58, 128, nil
	default:
		return 0, 0, fmt.Errorf("unsupported transparent orchestrator protocol %q", protocol)
	}
}

func (adapter Adapter) ApplyTransparentOrchestrator(ctx context.Context, requestID string, config TransparentOrchestratorConfig) (TransparentOrchestratorReadback, error) {
	commands, err := BuildTransparentOrchestratorCommands(config)
	if err != nil {
		return TransparentOrchestratorReadback{}, err
	}
	if adapter.Client == nil {
		return TransparentOrchestratorReadback{}, VPPError{Operation: "vpp.transparent-orchestrator", RequestID: requestID, Err: errors.New("vpp client is not configured")}
	}
	channel, err := adapter.Client.OpenChannel(ctx)
	if err != nil {
		return TransparentOrchestratorReadback{}, VPPError{Operation: "open_channel", RequestID: requestID, Err: err}
	}
	defer channel.Close()
	operation := Operation{Name: "vpp.transparent-orchestrator", RequestID: requestID, Resource: config.Generation, VPPCtlCommands: commands}
	reply, err := channel.Do(ctx, operation)
	if err != nil {
		return TransparentOrchestratorReadback{}, VPPError{Operation: operation.Name, RequestID: requestID, Err: err}
	}
	payload, ok := reply.Payload.(VPPCTLReplyPayload)
	if !ok || len(payload.CommandResults) == 0 {
		return TransparentOrchestratorReadback{}, fmt.Errorf("%w: transparent orchestrator command results are missing", ErrSnapshotIncomplete)
	}
	show := payload.CommandResults[len(payload.CommandResults)-1]
	if show.Command != "show ly-route orchestrator" || show.Retval != 0 {
		return TransparentOrchestratorReadback{}, fmt.Errorf("%w: transparent orchestrator readback command is missing", ErrSnapshotIncomplete)
	}
	if err := verifyTransparentOrchestratorReadback(show.Stdout, config, commands); err != nil {
		return TransparentOrchestratorReadback{}, err
	}
	return TransparentOrchestratorReadback{Generation: config.Generation, State: "running", Output: show.Stdout}, nil
}

func (adapter Adapter) ObserveTransparentOrchestrator(ctx context.Context, requestID string) (TransparentOrchestratorReadback, error) {
	if adapter.Client == nil {
		return TransparentOrchestratorReadback{}, VPPError{Operation: "vpp.transparent-orchestrator.observe", RequestID: requestID, Err: errors.New("vpp client is not configured")}
	}
	channel, err := adapter.Client.OpenChannel(ctx)
	if err != nil {
		return TransparentOrchestratorReadback{}, VPPError{Operation: "open_channel", RequestID: requestID, Err: err}
	}
	defer channel.Close()
	operation := Operation{Name: "vpp.transparent-orchestrator.observe", RequestID: requestID, Resource: "active", VPPCtlCommands: []string{"show ly-route orchestrator"}}
	reply, err := channel.Do(ctx, operation)
	if err != nil {
		return TransparentOrchestratorReadback{}, VPPError{Operation: operation.Name, RequestID: requestID, Err: err}
	}
	payload, ok := reply.Payload.(VPPCTLReplyPayload)
	if !ok || len(payload.CommandResults) != 1 || payload.CommandResults[0].Command != "show ly-route orchestrator" || payload.CommandResults[0].Retval != 0 {
		return TransparentOrchestratorReadback{}, fmt.Errorf("%w: transparent orchestrator observation is incomplete", ErrSnapshotIncomplete)
	}
	output := payload.CommandResults[0].Stdout
	readback := TransparentOrchestratorReadback{Output: output}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		switch fields[0] {
		case "state":
			readback.State = fields[1]
		case "generation":
			readback.Generation = fields[1]
		}
	}
	if readback.State == "" || (readback.State == "running" && readback.Generation == "") {
		return TransparentOrchestratorReadback{}, fmt.Errorf("%w: transparent orchestrator state/generation is missing", ErrSnapshotIncomplete)
	}
	return readback, nil
}

func (adapter Adapter) DisableTransparentOrchestrator(ctx context.Context, requestID string) error {
	if adapter.Client == nil {
		return VPPError{Operation: "vpp.transparent-orchestrator.disable", RequestID: requestID, Err: errors.New("vpp client is not configured")}
	}
	channel, err := adapter.Client.OpenChannel(ctx)
	if err != nil {
		return VPPError{Operation: "open_channel", RequestID: requestID, Err: err}
	}
	defer channel.Close()
	operation := Operation{Name: "vpp.transparent-orchestrator.disable", RequestID: requestID, Resource: "active", VPPCtlCommands: BuildTransparentOrchestratorDisableCommands()}
	reply, err := channel.Do(ctx, operation)
	if err != nil {
		return VPPError{Operation: operation.Name, RequestID: requestID, Err: err}
	}
	payload, ok := reply.Payload.(VPPCTLReplyPayload)
	if !ok || len(payload.CommandResults) != len(operation.VPPCtlCommands) {
		return fmt.Errorf("%w: transparent orchestrator disable results are incomplete", ErrSnapshotIncomplete)
	}
	show := payload.CommandResults[len(payload.CommandResults)-1]
	if show.Command != "show ly-route orchestrator" || show.Retval != 0 || !strings.Contains(show.Stdout, "state locked") {
		return fmt.Errorf("%w: transparent orchestrator did not enter locked state", ErrSnapshotIncomplete)
	}
	return nil
}

func verifyTransparentOrchestratorReadback(output string, config TransparentOrchestratorConfig, commands []string) error {
	required := []string{"state running", "generation " + config.Generation}
	for _, command := range commands {
		switch {
		case strings.Contains(command, " candidate boundary "):
			required = append(required, strings.TrimPrefix(command, "set ly-route orchestrator candidate "))
		case strings.Contains(command, " candidate group "):
			required = append(required, strings.TrimPrefix(command, "set ly-route orchestrator candidate "))
		case strings.Contains(command, " candidate default "):
			required = append(required, strings.TrimPrefix(command, "set ly-route orchestrator candidate "))
		}
	}
	for _, fragment := range required {
		if !strings.Contains(output, fragment) {
			return fmt.Errorf("%w: transparent orchestrator readback is missing %q", ErrSnapshotIncomplete, fragment)
		}
	}
	expectedPolicies := 0
	for _, command := range commands {
		if strings.Contains(command, " candidate rule ") {
			expectedPolicies++
		}
	}
	observedPolicies := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "policy ") {
			observedPolicies++
		}
	}
	if observedPolicies != expectedPolicies {
		return fmt.Errorf("%w: transparent orchestrator readback has %s policy rows, want %s", ErrSnapshotIncomplete, strconv.Itoa(observedPolicies), strconv.Itoa(expectedPolicies))
	}
	return nil
}
