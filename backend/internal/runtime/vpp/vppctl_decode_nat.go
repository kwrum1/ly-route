package vpp

import (
	"net/netip"
	"strconv"
	"strings"

	"ly-route/backend/internal/runtime/nat"
)

type liveNATStatic struct {
	internal string
	external string
}

type liveNATPort struct {
	protocol     string
	internalHost string
	internalPort int
	external     string
	externalPort int
}

func decodeVPPCTLNAT44(request SnapshotRequest, results []VPPCTLCommandResult) (NAT44Readback, error) {
	if err := requireNATCandidates(request); err != nil {
		return NAT44Readback{}, err
	}
	output, err := commandOutput(results, "show nat44 static mappings")
	if err != nil {
		return NAT44Readback{}, err
	}
	statics, ports, err := parseNAT44Rows(output)
	if err != nil {
		return NAT44Readback{}, err
	}
	readback := NAT44Readback{}
	matchedStatic := make(map[string]struct{})
	for _, row := range statics {
		matches := make([]nat.StaticMapping, 0, 1)
		for _, candidate := range request.Candidates.NATStaticMappings {
			if candidate.InternalAddress == row.internal && candidate.ExternalAddress == row.external {
				matches = append(matches, candidate)
			}
		}
		if len(matches) != 1 {
			return NAT44Readback{}, snapshotDecodeError("NAT44 static row %s -> %s maps to %d candidates", row.internal, row.external, len(matches))
		}
		if _, duplicate := matchedStatic[matches[0].ID]; duplicate {
			return NAT44Readback{}, snapshotDecodeError("NAT44 static mapping %q is ambiguous", matches[0].ID)
		}
		matchedStatic[matches[0].ID] = struct{}{}
		readback.StaticMappings = append(readback.StaticMappings, matches[0])
	}
	matchedPorts := make(map[string]struct{})
	for _, row := range ports {
		matches := make([]nat.PortMapping, 0, 1)
		for _, candidate := range request.Candidates.NATPortMappings {
			if candidate.Protocol == row.protocol && candidate.InternalHost == row.internalHost && candidate.InternalPort == row.internalPort && candidate.ExternalAddress == row.external && candidate.ExternalPort == row.externalPort {
				matches = append(matches, candidate)
			}
		}
		if len(matches) != 1 {
			return NAT44Readback{}, snapshotDecodeError("NAT44 port row maps to %d candidates", len(matches))
		}
		if _, duplicate := matchedPorts[matches[0].ID]; duplicate {
			return NAT44Readback{}, snapshotDecodeError("NAT44 port mapping %q is ambiguous", matches[0].ID)
		}
		matchedPorts[matches[0].ID] = struct{}{}
		readback.PortMappings = append(readback.PortMappings, matches[0])
	}
	if err := requireMatchedNAT(request.NATStaticMappings, matchedStatic, "static mapping"); err != nil {
		return NAT44Readback{}, err
	}
	if err := requireMatchedNAT(request.NATPortMappings, matchedPorts, "port mapping"); err != nil {
		return NAT44Readback{}, err
	}
	if err := requireAbsentNAT(request.AbsentNATStatic, matchedStatic, "static mapping"); err != nil {
		return NAT44Readback{}, err
	}
	if err := requireAbsentNAT(request.AbsentNATPort, matchedPorts, "port mapping"); err != nil {
		return NAT44Readback{}, err
	}
	return readback, nil
}

func parseNAT44Rows(output string) ([]liveNATStatic, []liveNATPort, error) {
	lines := nonBlankLines(output)
	if len(lines) == 0 || lines[0] != "NAT44 static mappings:" {
		return nil, nil, snapshotDecodeError("NAT44 mapping header is missing")
	}
	var statics []liveNATStatic
	var ports []liveNATPort
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 6 && fields[0] == "local" && fields[2] == "external" && fields[4] == "vrf" && fields[5] == "0":
			if err := validNATAddress(fields[1]); err != nil {
				return nil, nil, err
			}
			if err := validNATAddress(fields[3]); err != nil {
				return nil, nil, err
			}
			statics = append(statics, liveNATStatic{internal: fields[1], external: fields[3]})
		case len(fields) == 7 && (fields[0] == "tcp" || fields[0] == "udp") && fields[1] == "local" && fields[3] == "external" && fields[5] == "vrf" && fields[6] == "0":
			internalHost, internalPort, err := parseNATEndpoint(fields[2])
			if err != nil {
				return nil, nil, err
			}
			external, externalPort, err := parseNATEndpoint(fields[4])
			if err != nil {
				return nil, nil, err
			}
			ports = append(ports, liveNATPort{protocol: fields[0], internalHost: internalHost, internalPort: internalPort, external: external, externalPort: externalPort})
		default:
			return nil, nil, snapshotDecodeError("unknown NAT44 mapping grammar %q", line)
		}
	}
	return statics, ports, nil
}

func parseNATEndpoint(value string) (string, int, error) {
	separator := strings.LastIndexByte(value, ':')
	if separator < 1 || separator == len(value)-1 {
		return "", 0, snapshotDecodeError("malformed NAT44 endpoint %q", value)
	}
	host := value[:separator]
	if err := validNATAddress(host); err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(value[separator+1:])
	if err != nil || port < 1 || port > 65535 {
		return "", 0, snapshotDecodeError("malformed NAT44 port %q", value[separator+1:])
	}
	return host, port, nil
}

func validNATAddress(value string) error {
	address, err := netip.ParseAddr(value)
	if err != nil || !address.Is4() {
		return snapshotDecodeError("malformed NAT44 address %q", value)
	}
	return nil
}

func requireNATCandidates(request SnapshotRequest) error {
	statics := make(map[string]struct{}, len(request.Candidates.NATStaticMappings))
	for _, candidate := range request.Candidates.NATStaticMappings {
		if candidate.ID == "" {
			return snapshotDecodeError("NAT44 static candidate ID is empty")
		}
		if _, duplicate := statics[candidate.ID]; duplicate {
			return snapshotDecodeError("NAT44 static candidate %q is ambiguous", candidate.ID)
		}
		statics[candidate.ID] = struct{}{}
	}
	if err := requireCandidateNames(request.NATStaticMappings, statics, "NAT44 static mapping"); err != nil {
		return err
	}
	ports := make(map[string]struct{}, len(request.Candidates.NATPortMappings))
	for _, candidate := range request.Candidates.NATPortMappings {
		if candidate.ID == "" {
			return snapshotDecodeError("NAT44 port candidate ID is empty")
		}
		if _, duplicate := ports[candidate.ID]; duplicate {
			return snapshotDecodeError("NAT44 port candidate %q is ambiguous", candidate.ID)
		}
		ports[candidate.ID] = struct{}{}
	}
	return requireCandidateNames(request.NATPortMappings, ports, "NAT44 port mapping")
}

func requireMatchedNAT(requested []string, matched map[string]struct{}, kind string) error {
	for _, id := range requested {
		if _, ok := matched[strings.TrimSpace(id)]; !ok {
			return snapshotDecodeError("NAT44 %s %q was not returned", kind, id)
		}
	}
	return nil
}

func requireAbsentNAT(requested []string, matched map[string]struct{}, kind string) error {
	for _, id := range requested {
		if _, present := matched[strings.TrimSpace(id)]; present {
			return snapshotDecodeError("deleted NAT44 %s %q is still present", kind, id)
		}
	}
	return nil
}
