package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ly-route/backend/internal/httpapi"
	"ly-route/backend/internal/persistence"
	"ly-route/backend/internal/runtime/proxy"
	serviceRuntime "ly-route/backend/internal/runtime/service"
)

const gatewayActiveSessionSeconds = 5

type gatewayTelemetryConfigReader interface {
	Configs(context.Context, string) ([]persistence.ConfigDocument, error)
}

type gatewayVPPCTLRunner func(context.Context, string, ...string) (string, error)

type vppctlGatewayTelemetry struct {
	binary string
	store  gatewayTelemetryConfigReader
	now    func() time.Time
	run    gatewayVPPCTLRunner

	mu       sync.Mutex
	previous map[string]vppTelemetryCounter
	totals   map[string]vppTelemetryCounter
}

type vppTelemetryCounter struct {
	download int64
	upload   int64
	linkUp   bool
}

type gatewayWANLink struct {
	id               string
	name             string
	interfaceID      string
	pppoe            bool
	sessionInterface string
}

type gatewayWANGroup struct {
	id      string
	name    string
	members []string
}

type gatewayProxyEgress struct {
	id         string
	name       string
	underlayID string
	activeNode string
}

func newVPPCTLGatewayTelemetry(store gatewayTelemetryConfigReader, binary string, now func() time.Time) *vppctlGatewayTelemetry {
	if now == nil {
		now = time.Now
	}
	return &vppctlGatewayTelemetry{
		binary:   strings.TrimSpace(binary),
		store:    store,
		now:      now,
		run:      runVPPCTLTelemetryCommand,
		previous: map[string]vppTelemetryCounter{},
		totals:   map[string]vppTelemetryCounter{},
	}
}

func (collector *vppctlGatewayTelemetry) Collect(ctx context.Context) (httpapi.GatewayTelemetrySnapshot, error) {
	collector.mu.Lock()
	defer collector.mu.Unlock()

	if collector.store == nil {
		return httpapi.GatewayTelemetrySnapshot{}, fmt.Errorf("gateway telemetry configuration store is not configured")
	}
	if collector.run == nil {
		return httpapi.GatewayTelemetrySnapshot{}, fmt.Errorf("gateway VPP telemetry runner is not configured")
	}
	binary := collector.binary
	if binary == "" {
		binary = "vppctl"
	}
	interfaceOutput, err := collector.run(ctx, binary, "show", "interface")
	if err != nil {
		return httpapi.GatewayTelemetrySnapshot{}, fmt.Errorf("read VPP interface telemetry: %w", err)
	}
	neighborOutput, err := collector.run(ctx, binary, "show", "ip", "neighbors")
	if err != nil {
		return httpapi.GatewayTelemetrySnapshot{}, fmt.Errorf("read VPP neighbor telemetry: %w", err)
	}
	pppoeOutput, _ := collector.run(ctx, binary, "show", "pppoe", "session")
	natEDOutput, natEDErr := collector.run(ctx, binary, "show", "nat44", "sessions")
	natEIOutput, natEIErr := collector.run(ctx, binary, "show", "nat44", "ei", "sessions", "detail")
	clockOutput, clockErr := collector.run(ctx, binary, "show", "clock")
	if natEDErr != nil && natEIErr != nil {
		return httpapi.GatewayTelemetrySnapshot{}, fmt.Errorf("read VPP connection telemetry: endpoint-dependent: %v; endpoint-independent: %w", natEDErr, natEIErr)
	}

	now := collector.now().UTC()
	interfaces := indexVPPInterfaceTelemetry(parseAllVPPInterfaceTelemetry(interfaceOutput))
	deltas := collector.interfaceDeltas(interfaces)
	links, groups, proxies, lanInterfaces, lanPrefixes, err := collector.logicalEgressConfiguration(ctx)
	if err != nil {
		return httpapi.GatewayTelemetrySnapshot{}, err
	}
	links = bindPPPoESessionInterfaces(links, interfaceOutput, pppoeOutput)
	logical := collector.logicalEgressCounters(interfaces, deltas, links, groups, proxies)
	connections := parseGatewayNATConnections(natEDOutput, now, lanPrefixes)
	if natEIErr == nil && clockErr == nil {
		connections = append(connections, parseGatewayNATEIActiveSessions(natEIOutput, parseVPPClock(clockOutput), now, lanPrefixes)...)
	}
	return httpapi.GatewayTelemetrySnapshot{
		ObservedAt:      now,
		LogicalEgresses: logical,
		Connections:     connections,
		Neighbors:       parseGatewayNeighbors(neighborOutput, now, lanInterfaces),
	}, nil
}

func bindPPPoESessionInterfaces(links []gatewayWANLink, interfaceOutput, sessionOutput string) []gatewayWANLink {
	indexToName := map[int]string{}
	for _, raw := range strings.Split(interfaceOutput, "\n") {
		fields := strings.Fields(raw)
		if len(fields) < 3 {
			continue
		}
		index, err := strconv.Atoi(fields[1])
		if err == nil {
			indexToName[index] = fields[0]
		}
	}
	physicalToSession := map[string]string{}
	for _, raw := range strings.Split(sessionOutput, "\n") {
		fields := strings.Fields(strings.TrimSpace(raw))
		var sessionIndex, physicalIndex int
		for index, field := range fields {
			if field == "sw-if-index" && index+1 < len(fields) {
				sessionIndex, _ = strconv.Atoi(fields[index+1])
			}
			if field == "encap-if-index" && index+1 < len(fields) {
				physicalIndex, _ = strconv.Atoi(fields[index+1])
			}
		}
		if indexToName[sessionIndex] != "" && indexToName[physicalIndex] != "" {
			physicalToSession[indexToName[physicalIndex]] = indexToName[sessionIndex]
		}
	}
	for index := range links {
		if session := physicalToSession["lyroute-"+links[index].interfaceID]; session != "" {
			links[index].sessionInterface = session
		}
	}
	return links
}

func indexVPPInterfaceTelemetry(items []map[string]any) map[string]vppTelemetryCounter {
	result := make(map[string]vppTelemetryCounter, len(items))
	for _, item := range items {
		name, _ := item["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		result[name] = vppTelemetryCounter{
			download: telemetryInt64(item["rx_bytes"]),
			upload:   telemetryInt64(item["tx_bytes"]),
			linkUp:   strings.EqualFold(telemetryString(item, "link_state"), "up"),
		}
	}
	return result
}

func (collector *vppctlGatewayTelemetry) interfaceDeltas(current map[string]vppTelemetryCounter) map[string]vppTelemetryCounter {
	deltas := make(map[string]vppTelemetryCounter, len(current))
	for name, counter := range current {
		previous, found := collector.previous[name]
		if found {
			deltas[name] = vppTelemetryCounter{
				download: monotonicCounterDelta(counter.download, previous.download),
				upload:   monotonicCounterDelta(counter.upload, previous.upload),
				linkUp:   counter.linkUp,
			}
		} else {
			deltas[name] = vppTelemetryCounter{linkUp: counter.linkUp}
		}
	}
	collector.previous = current
	return deltas
}

func monotonicCounterDelta(current, previous int64) int64 {
	if current < 0 {
		return 0
	}
	if current < previous {
		return current
	}
	return current - previous
}

func (collector *vppctlGatewayTelemetry) logicalEgressCounters(
	interfaces map[string]vppTelemetryCounter,
	deltas map[string]vppTelemetryCounter,
	links []gatewayWANLink,
	groups []gatewayWANGroup,
	proxies []gatewayProxyEgress,
) []httpapi.LogicalEgressCounter {
	linksByID := make(map[string]gatewayWANLink, len(links))
	for _, link := range links {
		linksByID[link.id] = link
	}
	groupMembers := map[string]struct{}{}
	logicalDeltas := map[string]vppTelemetryCounter{}
	health := map[string]bool{}
	for _, group := range groups {
		var aggregate vppTelemetryCounter
		for _, memberID := range group.members {
			groupMembers[memberID] = struct{}{}
			member, found := linksByID[memberID]
			if !found {
				continue
			}
			name := gatewayWANDataInterface(member)
			delta := deltas[name]
			aggregate.download += delta.download
			aggregate.upload += delta.upload
			aggregate.linkUp = aggregate.linkUp || gatewayWANLinkUp(member, interfaces)
		}
		logicalDeltas[group.id] = aggregate
		health[group.id] = aggregate.linkUp
	}
	for _, link := range links {
		if _, grouped := groupMembers[link.id]; grouped {
			continue
		}
		logicalDeltas[link.id] = deltas[gatewayWANDataInterface(link)]
		health[link.id] = gatewayWANLinkUp(link, interfaces)
	}

	for _, egress := range proxies {
		network := proxy.ServiceNetworkForEgressID(egress.id)
		userDelta := deltas[network.IngressVPPInterface]
		logicalDeltas[egress.id] = userDelta
		ingress := interfaces[network.IngressVPPInterface]
		egressSide := interfaces[network.EgressVPPInterface]
		health[egress.id] = ingress.linkUp && egressSide.linkUp

		// The physical underlay includes encrypted proxy transport. Remove that
		// transport from the direct/WAN-group series so aggregate UI totals do
		// not count the same bytes twice.
		underlay := logicalDeltas[egress.underlayID]
		transport := deltas[network.EgressVPPInterface]
		underlay.download = maxInt64(0, underlay.download-transport.upload)
		underlay.upload = maxInt64(0, underlay.upload-transport.download)
		logicalDeltas[egress.underlayID] = underlay
	}

	result := make([]httpapi.LogicalEgressCounter, 0, len(logicalDeltas))
	for _, link := range links {
		if _, grouped := groupMembers[link.id]; grouped {
			continue
		}
		result = append(result, collector.logicalCounter(link.id, link.name, httpapi.LogicalEgressDirectWAN, "", "", logicalDeltas[link.id], health[link.id]))
	}
	for _, group := range groups {
		result = append(result, collector.logicalCounter(group.id, group.name, httpapi.LogicalEgressWANGroup, "", firstHealthyWANMember(group, linksByID, interfaces), logicalDeltas[group.id], health[group.id]))
	}
	for _, egress := range proxies {
		result = append(result, collector.logicalCounter(egress.id, egress.name, httpapi.LogicalEgressProxy, egress.underlayID, egress.activeNode, logicalDeltas[egress.id], health[egress.id]))
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

func (collector *vppctlGatewayTelemetry) logicalCounter(id, name string, kind httpapi.LogicalEgressKind, underlay, activeMember string, delta vppTelemetryCounter, up bool) httpapi.LogicalEgressCounter {
	total := collector.totals[id]
	total.download += maxInt64(0, delta.download)
	total.upload += maxInt64(0, delta.upload)
	total.linkUp = up
	collector.totals[id] = total
	health, state := "healthy", "available"
	if !up {
		health, state = "failed", "failed"
	}
	return httpapi.LogicalEgressCounter{
		ID: id, Name: name, Kind: kind, Health: health, State: state,
		UnderlayWANID: underlay, ActiveMember: activeMember,
		DownloadBytes: total.download, UploadBytes: total.upload,
	}
}

func gatewayWANLinkUp(link gatewayWANLink, interfaces map[string]vppTelemetryCounter) bool {
	if !interfaces["lyroute-"+link.interfaceID].linkUp {
		return false
	}
	if link.pppoe {
		return interfaces[link.sessionInterface].linkUp
	}
	return true
}

func gatewayWANDataInterface(link gatewayWANLink) string {
	if link.pppoe && strings.TrimSpace(link.sessionInterface) != "" {
		return link.sessionInterface
	}
	return "lyroute-" + link.interfaceID
}

func firstHealthyWANMember(group gatewayWANGroup, links map[string]gatewayWANLink, interfaces map[string]vppTelemetryCounter) string {
	for _, id := range group.members {
		if link, found := links[id]; found && gatewayWANLinkUp(link, interfaces) {
			return id
		}
	}
	return ""
}

func (collector *vppctlGatewayTelemetry) logicalEgressConfiguration(ctx context.Context) ([]gatewayWANLink, []gatewayWANGroup, []gatewayProxyEgress, map[string]struct{}, []netip.Prefix, error) {
	interfacePayloads, err := collector.configPayloads(ctx, "interface")
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	lanInterfaces := map[string]struct{}{}
	lanPrefixes := []netip.Prefix{}
	for _, payload := range interfacePayloads {
		if telemetryString(payload, "gateway_role", "role") != "lan" && telemetryNestedString(payload, "mode_role", "gateway") != "lan" {
			continue
		}
		id := telemetryString(payload, "system_name", "interface_id", "id", "name")
		if id != "" {
			lanInterfaces["lyroute-"+id] = struct{}{}
		}
		if prefix, parseErr := netip.ParsePrefix(telemetryString(payload, "cidr", "ip_cidr", "address", "ip")); parseErr == nil {
			lanPrefixes = append(lanPrefixes, prefix.Masked())
		}
	}

	wanPayloads, err := collector.configPayloads(ctx, "wan_link")
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	links := []gatewayWANLink{}
	for _, payload := range wanPayloads {
		if !telemetryEnabled(payload) {
			continue
		}
		id := telemetryString(payload, "id")
		interfaceID := telemetryString(payload, "system_name", "interface_id")
		if id == "" || interfaceID == "" {
			continue
		}
		mode := strings.ToLower(telemetryString(payload, "type", "wan_type"))
		if ipv4, ok := payload["ipv4"].(map[string]any); ok {
			mode = strings.ToLower(telemetryString(ipv4, "mode"))
		}
		isPPPoE := mode == "pppoe"
		sessionInterface := ""
		if isPPPoE {
			sessionInterface = serviceRuntime.PPPoEInterfaceName(id)
		}
		links = append(links, gatewayWANLink{id: id, name: telemetryDefaultName(payload, id), interfaceID: interfaceID, pppoe: isPPPoE, sessionInterface: sessionInterface})
	}

	groupPayloads, err := collector.configPayloads(ctx, "wan_group")
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	groups := []gatewayWANGroup{}
	for _, payload := range groupPayloads {
		if !telemetryEnabled(payload) {
			continue
		}
		id := telemetryString(payload, "id")
		members := telemetryStringSlice(payload, "wan_members", "members")
		if id != "" && len(members) > 0 {
			groups = append(groups, gatewayWANGroup{id: id, name: telemetryDefaultName(payload, id), members: members})
		}

	}
	proxyPayloads, err := collector.configPayloads(ctx, "proxy_egress")
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	proxies := []gatewayProxyEgress{}
	for _, payload := range proxyPayloads {
		if !telemetryEnabled(payload) {
			continue
		}
		id := telemetryString(payload, "id")
		underlayID := telemetryString(payload, "underlay_wan_id")
		if id == "" || underlayID == "" {
			continue
		}
		proxies = append(proxies, gatewayProxyEgress{id: id, name: telemetryDefaultName(payload, id), underlayID: underlayID, activeNode: telemetryString(payload, "node_id", "subscription_id")})
	}
	return links, groups, proxies, lanInterfaces, lanPrefixes, nil
}

func (collector *vppctlGatewayTelemetry) configPayloads(ctx context.Context, resourceType string) ([]map[string]any, error) {
	documents, err := collector.store.Configs(ctx, resourceType)
	if err != nil {
		return nil, fmt.Errorf("read %s telemetry configuration: %w", resourceType, err)
	}
	result := make([]map[string]any, 0, len(documents))
	for _, document := range documents {
		var payload map[string]any
		if err := json.Unmarshal(document.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode %s %s telemetry configuration: %w", resourceType, document.ResourceID, err)
		}
		result = append(result, payload)
	}
	return result, nil
}

func parseGatewayNeighbors(output string, observedAt time.Time, lanInterfaces map[string]struct{}) []httpapi.GatewayNeighbor {
	result := []httpapi.GatewayNeighbor{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		age, err := strconv.ParseFloat(fields[0], 64)
		if err != nil || age < 0 {
			continue
		}
		ip, err := netip.ParseAddr(fields[1])
		if err != nil || !ip.Is4() {
			continue
		}
		mac := strings.ToLower(fields[len(fields)-2])
		interfaceName := fields[len(fields)-1]
		if len(lanInterfaces) > 0 {
			if _, allowed := lanInterfaces[interfaceName]; !allowed {
				continue
			}
		} else if !strings.HasPrefix(interfaceName, "lyroute-") {
			continue
		}
		result = append(result, httpapi.GatewayNeighbor{IP: ip.String(), MAC: mac, LastSeen: observedAt.Add(-time.Duration(age * float64(time.Second)))})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].IP < result[right].IP })
	return result
}

func parseGatewayNATConnections(output string, observedAt time.Time, lanPrefixes []netip.Prefix) []httpapi.GatewayConnection {
	result := []httpapi.GatewayConnection{}
	var current httpapi.GatewayConnection
	valid := false
	flush := func() {
		if valid && current.SourceIP != "" && current.DestinationIP != "" && gatewayLANAddress(current.SourceIP, lanPrefixes) {
			current.ObservedAt = observedAt
			result = append(result, current)
		}
		current = httpapi.GatewayConnection{}
		valid = false
	}
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		fields := strings.Fields(line)
		if len(fields) >= 8 && fields[0] == "i2o" && fields[2] == "proto" && fields[4] == "port" {
			flush()
			current.SourceIP = fields[1]
			current.Protocol = strings.ToLower(fields[3])
			port, err := strconv.Atoi(fields[5])
			if err == nil {
				current.SourcePort = port
			}
			valid = true
			continue
		}
		if valid && strings.HasPrefix(line, "external host ") {
			hostPort := strings.TrimSpace(strings.TrimPrefix(line, "external host "))
			separator := strings.LastIndex(hostPort, ":")
			if separator > 0 {
				current.DestinationIP = hostPort[:separator]
				port, err := strconv.Atoi(hostPort[separator+1:])
				if err == nil {
					current.DestinationPort = port
				}
			}
			continue
		}
		if valid && len(fields) == 6 && fields[0] == "total" && fields[1] == "pkts" && fields[3] == "total" && fields[4] == "bytes" {
			bytes, err := strconv.ParseInt(fields[5], 10, 64)
			if err == nil {
				current.Bytes = bytes
			}
		}
	}
	flush()
	sort.Slice(result, func(left, right int) bool { return result[left].Bytes > result[right].Bytes })
	return result
}

func parseGatewayNATEISummaries(output string, observedAt time.Time, lanPrefixes []netip.Prefix) []httpapi.GatewayConnection {
	result := []httpapi.GatewayConnection{}
	for _, raw := range strings.Split(output, "\n") {
		fields := strings.Fields(strings.TrimSpace(raw))
		if len(fields) != 7 || fields[2] != "dynamic" || fields[3] != "translations," || fields[5] != "static" || fields[6] != "translations" {
			continue
		}
		source := strings.TrimSuffix(fields[0], ":")
		if !gatewayLANAddress(source, lanPrefixes) {
			continue
		}
		dynamicCount, dynamicErr := strconv.Atoi(fields[1])
		staticCount, staticErr := strconv.Atoi(fields[4])
		if dynamicErr != nil || staticErr != nil || dynamicCount+staticCount == 0 {
			continue
		}
		result = append(result, httpapi.GatewayConnection{
			SourceIP: source, ConnectionCount: dynamicCount + staticCount, ObservedAt: observedAt,
		})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ConnectionCount > result[right].ConnectionCount })
	return result
}

func parseVPPClock(output string) float64 {
	fields := strings.Fields(output)
	for index := range fields {
		if index > 0 && fields[index-1] == "now" {
			value, err := strconv.ParseFloat(strings.TrimSuffix(fields[index], ","), 64)
			if err == nil {
				return value
			}
		}
	}
	return 0
}

func parseGatewayNATEIActiveSessions(output string, clock float64, observedAt time.Time, lanPrefixes []netip.Prefix) []httpapi.GatewayConnection {
	items := []httpapi.GatewayConnection{}
	current := httpapi.GatewayConnection{}
	lastHeard := float64(-1)
	flush := func() {
		age := clock - lastHeard
		if current.SourceIP != "" && lastHeard >= 0 && age >= 0 && age <= gatewayActiveSessionSeconds && gatewayLANAddress(current.SourceIP, lanPrefixes) {
			current.AgeSeconds = age
			current.ObservedAt = observedAt
			current.ConnectionCount = 1
			current.SessionKind = "full_cone_mapping"
			items = append(items, current)
		}
		current = httpapi.GatewayConnection{}
		lastHeard = -1
	}
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		fields := strings.Fields(line)
		if len(fields) >= 8 && fields[0] == "i2o" && fields[2] == "proto" && fields[4] == "port" {
			flush()
			current.SourceIP = fields[1]
			current.Protocol = strings.ToLower(fields[3])
			current.SourcePort, _ = strconv.Atoi(fields[5])
			continue
		}
		if current.SourceIP == "" {
			continue
		}
		if len(fields) >= 8 && fields[0] == "o2i" && fields[2] == "proto" && fields[4] == "port" {
			current.TranslatedIP = fields[1]
			current.TranslatedPort, _ = strconv.Atoi(fields[5])
			continue
		}
		if strings.HasPrefix(line, "last heard ") {
			lastHeard, _ = strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "last heard ")), 64)
			continue
		}
		if len(fields) == 6 && fields[0] == "total" && fields[1] == "pkts" && fields[3] == "total" && fields[4] == "bytes" {
			current.Bytes, _ = strconv.ParseInt(fields[5], 10, 64)
		}
	}
	flush()
	sort.Slice(items, func(left, right int) bool {
		if items[left].SourceIP != items[right].SourceIP {
			return items[left].SourceIP < items[right].SourceIP
		}
		return items[left].SourcePort < items[right].SourcePort
	})
	return items
}

func gatewayLANAddress(value string, prefixes []netip.Prefix) bool {
	address, err := netip.ParseAddr(value)
	if err != nil || !address.Is4() {
		return false
	}
	if len(prefixes) == 0 {
		return !netip.MustParsePrefix("198.18.0.0/15").Contains(address)
	}
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func telemetryString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func telemetryNestedString(item map[string]any, key, child string) string {
	nested, _ := item[key].(map[string]any)
	return telemetryString(nested, child)
}

func telemetryDefaultName(item map[string]any, fallback string) string {
	if name := telemetryString(item, "name", "display_name"); name != "" {
		return name
	}
	return fallback
}

func telemetryEnabled(item map[string]any) bool {
	enabled, exists := item["enabled"]
	if !exists {
		return true
	}
	value, ok := enabled.(bool)
	return !ok || value
}

func telemetryStringSlice(item map[string]any, keys ...string) []string {
	for _, key := range keys {
		switch values := item[key].(type) {
		case []string:
			return append([]string(nil), values...)
		case []any:
			result := make([]string, 0, len(values))
			for _, value := range values {
				if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
					result = append(result, strings.TrimSpace(text))
				}
			}
			if len(result) > 0 {
				return result
			}
		}
	}
	return nil
}

func telemetryInt64(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case uint64:
		if typed <= uint64(^uint64(0)>>1) {
			return int64(typed)
		}
	case float64:
		return int64(typed)
	}
	return 0
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
