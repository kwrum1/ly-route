package proxy

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const MaxSubscriptionBytes = 4 << 20

func ParseSubscription(content []byte) ([]Node, error) {
	if len(content) == 0 || len(content) > MaxSubscriptionBytes {
		return nil, fmt.Errorf("%w: subscription content must be between 1 byte and %d bytes", ErrInvalidSubscription, MaxSubscriptionBytes)
	}
	text := strings.TrimSpace(string(content))
	if !strings.Contains(text, "://") {
		if decoded, ok := decodeBase64(text); ok {
			text = strings.TrimSpace(string(decoded))
		}
	}
	lines := strings.Fields(text)
	if len(lines) == 0 {
		return nil, fmt.Errorf("%w: subscription contains no nodes", ErrInvalidSubscription)
	}
	nodes := make([]Node, 0, len(lines))
	seen := map[string]bool{}
	for index, line := range lines {
		node, err := ParseNodeURI(strings.TrimSpace(line))
		if err != nil {
			return nil, fmt.Errorf("%w: node %d: %v", ErrInvalidSubscription, index+1, err)
		}
		if seen[node.ID] {
			continue
		}
		seen[node.ID] = true
		nodes = append(nodes, node)
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("%w: subscription contains no supported nodes", ErrInvalidSubscription)
	}
	return nodes, nil
}

func ParseNodeURI(raw string) (Node, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(trimmed), "vmess://") {
		return parseVMessURI(trimmed)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" {
		return Node{}, fmt.Errorf("invalid node URI")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "vless":
		return parseVLESSURI(trimmed, parsed)
	case "trojan":
		return parseTrojanURI(trimmed, parsed)
	case "ss":
		return parseShadowsocksURI(trimmed)
	default:
		return Node{}, fmt.Errorf("unsupported node scheme %q", parsed.Scheme)
	}
}

func parseVLESSURI(raw string, parsed *url.URL) (Node, error) {
	secret := ""
	if parsed.User != nil {
		secret = parsed.User.Username()
	}
	host, port, err := nodeHostPort(parsed)
	if err != nil || secret == "" {
		return Node{}, fmt.Errorf("vless URI requires UUID, host, and port")
	}
	query := parsed.Query()
	settings := map[string]any{"encryption": firstQuery(query, "encryption", "none")}
	copyQuerySetting(settings, query, "type", "network")
	copyQuerySetting(settings, query, "security", "security")
	copyQuerySetting(settings, query, "flow", "flow")
	if query.Get("security") == "reality" {
		reality := map[string]any{}
		copyQuerySetting(reality, query, "pbk", "publicKey")
		copyQuerySetting(reality, query, "fp", "fingerprint")
		copyQuerySetting(reality, query, "sni", "serverName")
		copyQuerySetting(reality, query, "sid", "shortId")
		copyQuerySetting(reality, query, "spx", "spiderX")
		if len(reality) == 0 {
			return Node{}, fmt.Errorf("vless Reality URI requires Reality parameters")
		}
		settings["realitySettings"] = reality
	}
	applyTransportSettings(settings, query)
	return Node{ID: nodeURIID(raw), Name: nodeName(parsed, host), Protocol: "vless", Address: host, Port: port, Secret: secret, Settings: settings}, nil
}

func parseTrojanURI(raw string, parsed *url.URL) (Node, error) {
	secret := ""
	if parsed.User != nil {
		secret = parsed.User.Username()
	}
	host, port, err := nodeHostPort(parsed)
	if err != nil || secret == "" {
		return Node{}, fmt.Errorf("trojan URI requires password, host, and port")
	}
	query := parsed.Query()
	settings := map[string]any{}
	copyQuerySetting(settings, query, "type", "network")
	security := firstQuery(query, "security", "tls")
	settings["security"] = security
	if security == "tls" {
		tls := map[string]any{}
		copyQuerySetting(tls, query, "sni", "serverName")
		copyQuerySetting(tls, query, "fp", "fingerprint")
		if len(tls) > 0 {
			settings["tlsSettings"] = tls
		}
	}
	applyTransportSettings(settings, query)
	return Node{ID: nodeURIID(raw), Name: nodeName(parsed, host), Protocol: "trojan", Address: host, Port: port, Secret: secret, Settings: settings}, nil
}

func parseVMessURI(raw string) (Node, error) {
	encoded := strings.TrimPrefix(raw, "vmess://")
	decoded, ok := decodeBase64(encoded)
	if !ok {
		return Node{}, fmt.Errorf("vmess payload is not valid base64")
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return Node{}, fmt.Errorf("vmess payload is not valid JSON")
	}
	host := strings.TrimSpace(fmt.Sprint(payload["add"]))
	secret := strings.TrimSpace(fmt.Sprint(payload["id"]))
	port, err := strconv.Atoi(strings.TrimSpace(fmt.Sprint(payload["port"])))
	if err != nil || host == "" || secret == "" || port <= 0 || port > 65535 {
		return Node{}, fmt.Errorf("vmess payload requires id, address, and port")
	}
	settings := map[string]any{}
	if value := strings.TrimSpace(fmt.Sprint(payload["net"])); value != "" && value != "<nil>" {
		settings["network"] = value
	}
	if value := strings.TrimSpace(fmt.Sprint(payload["tls"])); value != "" && value != "none" && value != "<nil>" {
		settings["security"] = value
		tls := map[string]any{}
		if sni := strings.TrimSpace(fmt.Sprint(payload["sni"])); sni != "" && sni != "<nil>" {
			tls["serverName"] = sni
		}
		if len(tls) > 0 {
			settings["tlsSettings"] = tls
		}
	}
	if cipher := strings.TrimSpace(fmt.Sprint(payload["scy"])); cipher != "" && cipher != "<nil>" {
		settings["cipher"] = cipher
	}
	name := strings.TrimSpace(fmt.Sprint(payload["ps"]))
	if name == "<nil>" || name == "" {
		name = host
	}
	return Node{ID: nodeURIID(raw), Name: name, Protocol: "vmess", Address: host, Port: port, Secret: secret, Settings: settings}, nil
}

func parseShadowsocksURI(raw string) (Node, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return Node{}, fmt.Errorf("invalid shadowsocks URI")
	}
	var method, password, host string
	var port int
	if parsed.User != nil && parsed.Host != "" {
		userinfo := parsed.User.Username()
		if decoded, ok := decodeBase64(userinfo); ok {
			userinfo = string(decoded)
		}
		method, password, _ = strings.Cut(userinfo, ":")
		host, port, err = nodeHostPort(parsed)
	} else {
		encoded := strings.TrimPrefix(strings.SplitN(raw, "#", 2)[0], "ss://")
		decoded, ok := decodeBase64(encoded)
		if !ok {
			return Node{}, fmt.Errorf("shadowsocks payload is not valid base64")
		}
		credentials, endpoint, found := strings.Cut(string(decoded), "@")
		if !found {
			return Node{}, fmt.Errorf("shadowsocks payload requires endpoint")
		}
		method, password, _ = strings.Cut(credentials, ":")
		var portString string
		host, portString, err = net.SplitHostPort(endpoint)
		if err != nil {
			return Node{}, fmt.Errorf("shadowsocks endpoint is invalid")
		}
		port, err = strconv.Atoi(portString)
	}
	if err != nil || strings.TrimSpace(method) == "" || password == "" || host == "" || port <= 0 || port > 65535 {
		return Node{}, fmt.Errorf("shadowsocks URI requires method, password, host, and port")
	}
	return Node{ID: nodeURIID(raw), Name: nodeName(parsed, host), Protocol: "shadowsocks", Address: host, Port: port, Secret: password, Settings: map[string]any{"method": method}}, nil
}

func applyTransportSettings(settings map[string]any, query url.Values) {
	switch query.Get("type") {
	case "ws":
		ws := map[string]any{}
		copyQuerySetting(ws, query, "path", "path")
		if host := query.Get("host"); host != "" {
			ws["headers"] = map[string]any{"Host": host}
		}
		if len(ws) > 0 {
			settings["wsSettings"] = ws
		}
	case "grpc":
		grpc := map[string]any{}
		copyQuerySetting(grpc, query, "serviceName", "serviceName")
		if len(grpc) > 0 {
			settings["grpcSettings"] = grpc
		}
	}
}

func nodeHostPort(parsed *url.URL) (string, int, error) {
	host := strings.TrimSpace(parsed.Hostname())
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || host == "" || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("invalid host or port")
	}
	return host, port, nil
}

func nodeName(parsed *url.URL, fallback string) string {
	if name, err := url.PathUnescape(parsed.Fragment); err == nil && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return fallback
}

func nodeURIID(raw string) string {
	withoutFragment := strings.SplitN(strings.TrimSpace(raw), "#", 2)[0]
	digest := sha256.Sum256([]byte(withoutFragment))
	return "imported-" + hex.EncodeToString(digest[:6])
}

func decodeBase64(value string) ([]byte, bool) {
	value = strings.TrimSpace(value)
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := encoding.DecodeString(value); err == nil {
			return decoded, true
		}
	}
	return nil, false
}

func copyQuerySetting(target map[string]any, query url.Values, source, destination string) {
	if value := strings.TrimSpace(query.Get(source)); value != "" {
		target[destination] = value
	}
}

func firstQuery(query url.Values, key, fallback string) string {
	if value := strings.TrimSpace(query.Get(key)); value != "" {
		return value
	}
	return fallback
}
