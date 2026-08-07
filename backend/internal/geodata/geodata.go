// Package geodata reads the compact protobuf files published by
// Loyalsoldier/v2ray-rules-dat without pulling the whole V2Ray dependency tree
// into the control plane.  The files are treated as data, never executed.
package geodata

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	FormatGeoIP   = "geoip"
	FormatGeoSite = "geosite"
)

// Data is the normalized form consumed by object groups.
// Domain selectors use a leading dot for a suffix and no dot for an exact
// domain, matching the object's existing editable text format.
type Data struct {
	Format       string
	Category     string
	Entries      []string
	SourceFile   string
	SourceSHA256 string
}

type Source struct {
	Format     string `json:"format"`
	Category   string `json:"category"`
	File       string `json:"file"`
	SHA256     string `json:"sha256,omitempty"`
	EntryCount int    `json:"entry_count,omitempty"`
}

// LoadSource resolves a source file from the configured geodata directory and
// parses one category.  It deliberately does not fall back to a network URL.
func LoadSource(source Source) (Data, error) {
	format := strings.ToLower(strings.TrimSpace(source.Format))
	if format != FormatGeoIP && format != FormatGeoSite {
		return Data{}, fmt.Errorf("unsupported geodata format %q", source.Format)
	}
	category := strings.ToUpper(strings.TrimSpace(source.Category))
	if category == "" {
		return Data{}, errors.New("geodata category is required")
	}
	file := strings.TrimSpace(source.File)
	if file == "" {
		file = format + ".dat"
	}
	if filepath.Base(file) != file {
		return Data{}, errors.New("geodata source file must be a basename")
	}
	path, err := findFile(file)
	if err != nil {
		return Data{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Data{}, fmt.Errorf("read geodata source %s: %w", path, err)
	}
	hash := sha256.Sum256(data)
	actualHash := hex.EncodeToString(hash[:])
	if expected := strings.ToLower(strings.TrimSpace(source.SHA256)); expected != "" && expected != actualHash {
		return Data{}, fmt.Errorf("geodata source %s sha256 mismatch: got %s, want %s", file, actualHash, expected)
	}
	var parsed Data
	switch format {
	case FormatGeoIP:
		parsed, err = ParseGeoIP(data, category)
	case FormatGeoSite:
		parsed, err = ParseGeoSite(data, category)
	}
	if err != nil {
		return Data{}, err
	}
	parsed.SourceFile = file
	parsed.SourceSHA256 = actualHash
	return parsed, nil
}

func findFile(file string) (string, error) {
	paths := []string{}
	if dir := strings.TrimSpace(os.Getenv("LY_ROUTE_GEODATA_DIR")); dir != "" {
		paths = append(paths, filepath.Join(dir, file))
	}
	paths = append(paths,
		filepath.Join("/usr/share/ly-route/geodata", file),
		filepath.Join("/etc/ly-route/geodata", file),
		filepath.Join("geodata", file),
	)
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("geodata source %s is not installed; place it under /usr/share/ly-route/geodata or set LY_ROUTE_GEODATA_DIR", file)
}

func ParseGeoIP(raw []byte, category string) (Data, error) {
	messages, err := topLevelMessages(raw)
	if err != nil {
		return Data{}, fmt.Errorf("parse geoip: %w", err)
	}
	want := strings.ToUpper(strings.TrimSpace(category))
	for _, message := range messages {
		code, fields, err := parseFields(message)
		_ = code
		if err != nil {
			return Data{}, fmt.Errorf("parse geoip category: %w", err)
		}
		name := firstString(fields, 1)
		if !strings.EqualFold(name, want) {
			continue
		}
		entries := make([]string, 0)
		for _, cidrMessage := range fields.bytes[2] {
			ip, prefix, err := parseCIDR(cidrMessage)
			if err != nil {
				return Data{}, fmt.Errorf("geoip %s: %w", want, err)
			}
			entries = append(entries, netip.PrefixFrom(ip, prefix).Masked().String())
		}
		return Data{Format: FormatGeoIP, Category: want, Entries: uniqueSorted(entries)}, nil
	}
	return Data{}, fmt.Errorf("geoip category %q was not found", want)
}

func ParseGeoSite(raw []byte, category string) (Data, error) {
	messages, err := topLevelMessages(raw)
	if err != nil {
		return Data{}, fmt.Errorf("parse geosite: %w", err)
	}
	want := strings.ToUpper(strings.TrimSpace(category))
	for _, message := range messages {
		_, fields, err := parseFields(message)
		if err != nil {
			return Data{}, fmt.Errorf("parse geosite category: %w", err)
		}
		name := firstString(fields, 1)
		if !strings.EqualFold(name, want) {
			continue
		}
		entries := make([]string, 0)
		for _, domainMessage := range fields.bytes[2] {
			typ, value, err := parseDomain(domainMessage)
			if err != nil {
				return Data{}, fmt.Errorf("geosite %s: %w", want, err)
			}
			value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
			if value == "" || strings.ContainsAny(value, " \t\r\n/:") {
				continue
			}
			switch typ {
			case 2: // Domain: the domain and all subdomains.
				entries = append(entries, "."+strings.TrimPrefix(value, "."))
			case 3: // Full: exact match.
				entries = append(entries, strings.TrimPrefix(value, "."))
			case 1:
				// Regex entries cannot be represented safely by the editable
				// object-group grammar.  Keep the rest of the category usable.
				continue
			}
		}
		return Data{Format: FormatGeoSite, Category: want, Entries: uniqueSorted(entries)}, nil
	}
	return Data{}, fmt.Errorf("geosite category %q was not found", want)
}

func ParseText(raw []byte, kind string) (Data, error) {
	entries := make([]string, 0)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		if line != "" {
			entries = append(entries, line)
		}
	}
	return Data{Format: "text", Category: strings.TrimSpace(kind), Entries: uniqueSorted(entries)}, nil
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

type fieldSet struct {
	strings map[int][]string
	bytes   map[int][][]byte
}

func parseFields(raw []byte) (string, fieldSet, error) {
	fields := fieldSet{strings: map[int][]string{}, bytes: map[int][][]byte{}}
	for len(raw) > 0 {
		key, n, err := readVarint(raw)
		if err != nil {
			return "", fields, err
		}
		raw = raw[n:]
		field := int(key >> 3)
		wire := int(key & 7)
		switch wire {
		case 0:
			_, n, err = readVarint(raw)
			if err != nil {
				return "", fields, err
			}
			raw = raw[n:]
		case 1:
			if len(raw) < 8 {
				return "", fields, io.ErrUnexpectedEOF
			}
			raw = raw[8:]
		case 2:
			length, n, err := readVarint(raw)
			if err != nil {
				return "", fields, err
			}
			raw = raw[n:]
			if length > uint64(len(raw)) {
				return "", fields, io.ErrUnexpectedEOF
			}
			value := append([]byte(nil), raw[:length]...)
			fields.bytes[field] = append(fields.bytes[field], value)
			if isText(value) {
				fields.strings[field] = append(fields.strings[field], string(value))
			}
			raw = raw[length:]
		case 5:
			if len(raw) < 4 {
				return "", fields, io.ErrUnexpectedEOF
			}
			raw = raw[4:]
		default:
			return "", fields, fmt.Errorf("unsupported protobuf wire type %d", wire)
		}
	}
	return firstString(fields, 1), fields, nil
}

func topLevelMessages(raw []byte) ([][]byte, error) {
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		reader, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		decompressed, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil {
			return nil, err
		}
		raw = decompressed
	}
	var result [][]byte
	for len(raw) > 0 {
		key, n, err := readVarint(raw)
		if err != nil {
			return nil, err
		}
		raw = raw[n:]
		if key != 0x0a {
			return nil, fmt.Errorf("unexpected top-level protobuf field %d", key>>3)
		}
		length, n, err := readVarint(raw)
		if err != nil {
			return nil, err
		}
		raw = raw[n:]
		if length > uint64(len(raw)) {
			return nil, io.ErrUnexpectedEOF
		}
		result = append(result, append([]byte(nil), raw[:length]...))
		raw = raw[length:]
	}
	return result, nil
}

func parseCIDR(raw []byte) (netip.Addr, int, error) {
	_, fields, err := parseFields(raw)
	if err != nil {
		return netip.Addr{}, 0, err
	}
	if len(fields.bytes[1]) == 0 {
		return netip.Addr{}, 0, errors.New("CIDR has no address")
	}
	ipBytes := fields.bytes[1][0]
	var addr netip.Addr
	switch len(ipBytes) {
	case 4:
		var value [4]byte
		copy(value[:], ipBytes)
		addr = netip.AddrFrom4(value)
	case 16:
		var value [16]byte
		copy(value[:], ipBytes)
		addr = netip.AddrFrom16(value)
	default:
		return netip.Addr{}, 0, fmt.Errorf("CIDR address has length %d", len(ipBytes))
	}
	prefixValues := fieldsVarints(raw, 2)
	if len(prefixValues) == 0 {
		return netip.Addr{}, 0, errors.New("CIDR has no prefix length")
	}
	prefix := int(prefixValues[0])
	if prefix < 0 || prefix > addr.BitLen() {
		return netip.Addr{}, 0, fmt.Errorf("invalid CIDR prefix %d", prefix)
	}
	return addr, prefix, nil
}

func parseDomain(raw []byte) (int, string, error) {
	_, fields, err := parseFields(raw)
	if err != nil {
		return 0, "", err
	}
	types := fieldsVarints(raw, 1)
	if len(types) == 0 || len(fields.strings[2]) == 0 {
		return 0, "", errors.New("domain is missing type or value")
	}
	return int(types[0]), fields.strings[2][0], nil
}

func fieldsVarints(raw []byte, wanted int) []uint64 {
	var result []uint64
	for len(raw) > 0 {
		key, n, err := readVarint(raw)
		if err != nil {
			return result
		}
		raw = raw[n:]
		field, wire := int(key>>3), int(key&7)
		if wire == 0 {
			value, n, err := readVarint(raw)
			if err != nil {
				return result
			}
			raw = raw[n:]
			if field == wanted {
				result = append(result, value)
			}
			continue
		}
		switch wire {
		case 1:
			if len(raw) < 8 {
				return result
			}
			raw = raw[8:]
		case 2:
			length, n, err := readVarint(raw)
			if err != nil || length > uint64(len(raw)-n) {
				return result
			}
			raw = raw[n+int(length):]
		case 5:
			if len(raw) < 4 {
				return result
			}
			raw = raw[4:]
		default:
			return result
		}
	}
	return result
}

func firstString(fields fieldSet, field int) string {
	if values := fields.strings[field]; len(values) > 0 {
		return values[0]
	}
	return ""
}

func isText(value []byte) bool {
	if len(value) == 0 {
		return true
	}
	for _, b := range value {
		if b == 0 || b < 0x09 || (b > 0x0d && b < 0x20) {
			return false
		}
	}
	return true
}

func readVarint(raw []byte) (uint64, int, error) {
	var value uint64
	for index, b := range raw {
		if index >= 10 {
			return 0, 0, errors.New("protobuf varint is too long")
		}
		value |= uint64(b&0x7f) << (7 * index)
		if b < 0x80 {
			return value, index + 1, nil
		}
	}
	return 0, 0, io.ErrUnexpectedEOF
}
